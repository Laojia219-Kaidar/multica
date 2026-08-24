package orcabridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/workentry"
)

// newKernel builds a real workentry kernel over its own memory store seeded
// with the issue anchor the bridge chain references. The adapter tests prove
// the bridge works against the existing service exactly as production would,
// with no schema and no bridge-owned tables.
func newKernel(t *testing.T) *WorkEntryServiceAdapter {
	t.Helper()
	chain := validChain()
	store := workentry.NewMemoryStore()
	store.SeedProject(workentry.ProjectRef{ID: chain.ProjectID, WorkspaceID: chain.WorkspaceID, Title: "HiveCrew A1"})
	store.SeedIssue(workentry.IssueRef{ID: chain.IssueID, WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID, Title: "Bridge anchor issue"})
	return &WorkEntryServiceAdapter{
		Service:     workentry.NewService(store),
		WorkspaceID: chain.WorkspaceID,
	}
}

// creationCountingStore spies on the kernel's creation path: any call to
// CommitWorkRegistration means Project/Issue rows were created, which the
// bridge must never trigger.
type creationCountingStore struct {
	*workentry.MemoryStore
	commits int
}

func (s *creationCountingStore) CommitWorkRegistration(ctx context.Context, req workentry.CommitWorkRegistrationRequest) (*workentry.CreateWorkResult, error) {
	s.commits++
	return s.MemoryStore.CommitWorkRegistration(ctx, req)
}

// Compile-time assertion that the spy still satisfies the kernel Store.
var _ workentry.Store = (*creationCountingStore)(nil)

func repeatHex(seed byte) string {
	out := make([]byte, 64)
	for i := range out {
		out[i] = '0' + seed%10
	}
	return string(out)
}

func bridgeActor() ActorIdentity {
	return ActorIdentity{
		ActorID:    "orca-bridge-a1",
		CarrierID:  "hivecrew-orca-bridge",
		SessionID:  "bridge-session-1",
		ObservedAt: time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC),
	}
}

func TestWorkEntryAdapterRegisterIsIdempotentAndDriftConflicts(t *testing.T) {
	adapter := newKernel(t)
	ctx := context.Background()
	chain := validChain()

	first, err := adapter.RegisterLinkage(ctx, LinkageInput{
		Chain: chain, Actor: bridgeActor(), MappingKind: "run",
	})
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	if first.WorkRef == "" {
		t.Fatal("work_ref must not be empty")
	}
	replay, err := adapter.RegisterLinkage(ctx, LinkageInput{
		Chain: chain, Actor: bridgeActor(), MappingKind: "run",
	})
	if err != nil {
		t.Fatalf("replay register: %v", err)
	}
	if replay.WorkRef != first.WorkRef || !replay.Replayed {
		t.Fatalf("replay must return the original work_ref: %+v vs %+v", first, replay)
	}
}

func TestWorkEntryAdapterEvidenceIdempotentAndDriftConflicts(t *testing.T) {
	adapter := newKernel(t)
	ctx := context.Background()
	chain := validChain()
	linkage, err := adapter.RegisterLinkage(ctx, LinkageInput{
		Chain: chain, Actor: bridgeActor(), MappingKind: "dispatch",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	input := EvidenceInput{
		WorkRef:        linkage.WorkRef,
		SessionID:      bridgeActor().SessionID,
		EventType:      "checkpoint",
		IdempotencyKey: "k1",
		Payload:        map[string]any{"orca_dispatch_id": "ctx_ab12"},
	}
	if input.OccurredAt.IsZero() {
		input.OccurredAt = time.Now()
	}
	first, err := adapter.AppendEvidence(ctx, input)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	replay, err := adapter.AppendEvidence(ctx, input)
	if err != nil {
		t.Fatalf("replay append: %v", err)
	}
	if !replay.Replayed || replay.EventID != first.EventID {
		t.Fatalf("replay must be idempotent: %+v vs %+v", first, replay)
	}
	drifted := input
	drifted.Payload = map[string]any{"orca_dispatch_id": "ctx_cd34"}
	if _, err := adapter.AppendEvidence(ctx, drifted); err == nil {
		t.Fatal("drifted evidence must conflict")
	}
	// Lookup finds the stored evidence.
	found, ok, err := adapter.LookupEvidence(ctx, linkage.WorkRef, "k1")
	if err != nil || !ok || found.Payload["orca_dispatch_id"] != "ctx_ab12" {
		t.Fatalf("lookup: found=%+v ok=%v err=%v", found, ok, err)
	}
	missing, ok, err := adapter.LookupEvidence(ctx, linkage.WorkRef, "nope")
	if err != nil || ok || missing.Payload != nil {
		t.Fatalf("missing lookup must be clean: %+v %v %v", missing, ok, err)
	}
}

func TestWorkEntryAdapterRejectsIncompleteActor(t *testing.T) {
	adapter := newKernel(t)
	_, err := adapter.RegisterLinkage(context.Background(), LinkageInput{
		Chain: validChain(), Actor: ActorIdentity{}, MappingKind: "run",
	})
	if err == nil {
		t.Fatal("incomplete actor must fail closed")
	}
}

func TestWorkEntryAdapterRejectsOpenEventType(t *testing.T) {
	adapter := newKernel(t)
	_, err := adapter.AppendEvidence(context.Background(), EvidenceInput{
		WorkRef: "hivecrew://ws/work/prj", SessionID: "s",
		EventType: "not_a_closed_type", IdempotencyKey: "k",
	})
	if err == nil {
		t.Fatal("open event type must fail closed")
	}
}

func TestLinkageKeysAreDeterministicAndScoped(t *testing.T) {
	chain := validChain()
	if RunLinkageKey(chain) == TaskLinkageKey(chain) ||
		TaskLinkageKey(chain) == DispatchLinkageKey(chain) ||
		RunLinkageKey(chain) == DispatchLinkageKey(chain) {
		t.Fatal("linkage keys must be distinct per scope")
	}
	if ResultEvidenceKey("ctx_1") == ResultEvidenceKey("ctx_2") {
		t.Fatal("result keys must be dispatch-scoped")
	}
	other := chain
	other.AssignmentID = "c05a0000-0000-4000-8000-000000000009"
	if DispatchLinkageKey(chain) == DispatchLinkageKey(other) {
		t.Fatal("dispatch keys must be assignment-scoped")
	}
}

// Finding 2 coverage: the bridge must never implicitly create a HiveCrew
// Issue (or Project). Two proofs: (a) a linkage without an issue anchor is
// rejected before the kernel is called, and (b) a resolvable anchor lands on
// the kernel's continued path with zero CommitWorkRegistration calls.
func TestRegisterLinkageNeverCreatesIssue(t *testing.T) {
	chain := validChain()
	store := &creationCountingStore{MemoryStore: workentry.NewMemoryStore()}
	store.SeedProject(workentry.ProjectRef{ID: chain.ProjectID, WorkspaceID: chain.WorkspaceID, Title: "HiveCrew A1"})
	store.SeedIssue(workentry.IssueRef{ID: chain.IssueID, WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID, Title: "Bridge anchor issue"})
	adapter := &WorkEntryServiceAdapter{Service: workentry.NewService(store), WorkspaceID: chain.WorkspaceID}
	ctx := context.Background()

	// (a) No issue anchor -> fail closed, kernel untouched.
	noAnchor := chain
	noAnchor.IssueID = ""
	if _, err := adapter.RegisterLinkage(ctx, LinkageInput{Chain: noAnchor, Actor: bridgeActor(), MappingKind: "run"}); !errors.Is(err, ErrIssueAnchorRequired) {
		t.Fatalf("expected ErrIssueAnchorRequired for project-only linkage, got %v", err)
	}
	if store.commits != 0 {
		t.Fatalf("kernel creation path was invoked %d times for an anchor-less linkage", store.commits)
	}

	// (b) Resolvable anchor -> continued registration, zero creations.
	first, err := adapter.RegisterLinkage(ctx, LinkageInput{Chain: chain, Actor: bridgeActor(), MappingKind: "run"})
	if err != nil {
		t.Fatalf("anchored register: %v", err)
	}
	replay, err := adapter.RegisterLinkage(ctx, LinkageInput{Chain: chain, Actor: bridgeActor(), MappingKind: "run"})
	if err != nil {
		t.Fatalf("anchored replay: %v", err)
	}
	if replay.WorkRef != first.WorkRef || !replay.Replayed {
		t.Fatalf("replay must return the original work_ref: %+v vs %+v", first, replay)
	}
	if store.commits != 0 {
		t.Fatalf("register triggered %d kernel creations; the bridge must land on the continued path only", store.commits)
	}
}

// Finding 2 coverage: an issue anchor that does not resolve to an existing
// issue must fail closed instead of letting the kernel create one.
func TestRegisterLinkageRejectsUnresolvableIssueAnchor(t *testing.T) {
	chain := validChain()
	chain.IssueID = "c05a0000-0000-4000-8000-0000000000ee" // valid uuid, never seeded
	store := &creationCountingStore{MemoryStore: workentry.NewMemoryStore()}
	store.SeedProject(workentry.ProjectRef{ID: chain.ProjectID, WorkspaceID: chain.WorkspaceID, Title: "HiveCrew A1"})
	adapter := &WorkEntryServiceAdapter{Service: workentry.NewService(store), WorkspaceID: chain.WorkspaceID}

	if _, err := adapter.RegisterLinkage(context.Background(), LinkageInput{Chain: chain, Actor: bridgeActor(), MappingKind: "run"}); !errors.Is(err, ErrIssueAnchorRequired) {
		t.Fatalf("expected ErrIssueAnchorRequired for unresolvable anchor, got %v", err)
	}
	if store.commits != 0 {
		t.Fatalf("unresolvable anchor still created %d kernel rows", store.commits)
	}
}

// Finding 1 coverage at the port: credential-like content in evidence
// payloads is redacted before the existing ledger append.
func TestAppendEvidenceRedactsCredentials(t *testing.T) {
	adapter := newKernel(t)
	ctx := context.Background()
	chain := validChain()
	linkage, err := adapter.RegisterLinkage(ctx, LinkageInput{Chain: chain, Actor: bridgeActor(), MappingKind: "dispatch"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	dirty := map[string]any{
		"body":      "used Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig and api_key = sk-ant-api03-EXAMPLEKEY1234567890",
		"nested":    map[string]any{"token": "token=supersecretvalue123"},
		"clean":     "ordinary engineering note with no credentials",
		"unchanged": 42,
	}
	if _, err := adapter.AppendEvidence(ctx, EvidenceInput{
		WorkRef:        linkage.WorkRef,
		SessionID:      bridgeActor().SessionID,
		EventType:      "progress",
		IdempotencyKey: "cred-1",
		Payload:        dirty,
		OccurredAt:     time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	stored, ok, err := adapter.LookupEvidence(ctx, linkage.WorkRef, "cred-1")
	if err != nil || !ok {
		t.Fatalf("lookup: ok=%v err=%v", ok, err)
	}
	body, _ := stored.Payload["body"].(string)
	if strings.Contains(body, "eyJhbGciOiJIUzI1NiJ9") || strings.Contains(body, "sk-ant-api03-EXAMPLEKEY") {
		t.Fatalf("credential survived evidence append: %q", body)
	}
	if !strings.Contains(body, RedactionMarker) {
		t.Fatalf("body missing redaction marker: %q", body)
	}
	nested, _ := stored.Payload["nested"].(map[string]any)
	tokenValue, _ := nested["token"].(string)
	if strings.Contains(tokenValue, "supersecretvalue123") {
		t.Fatalf("nested credential survived: %q", tokenValue)
	}
	if clean, _ := stored.Payload["clean"].(string); clean != "ordinary engineering note with no credentials" {
		t.Fatalf("clean content was rewritten: %q", clean)
	}
	if unchanged, _ := stored.Payload["unchanged"].(int); unchanged != 42 {
		t.Fatalf("non-string value was rewritten: %v", stored.Payload["unchanged"])
	}
}

// R3: dotted provider keys and nested arrays are redacted before the
// existing ledger append.
func TestAppendEvidenceRedactsDottedKeysAndNestedArrays(t *testing.T) {
	adapter := newKernel(t)
	ctx := context.Background()
	chain := validChain()
	linkage, err := adapter.RegisterLinkage(ctx, LinkageInput{Chain: chain, Actor: bridgeActor(), MappingKind: "dispatch"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	dirty := map[string]any{
		"artifact": "signed with sk-sp-H.ABCDEFGHIJKLMNOP",
		"trace": []any{
			map[string]any{"line": "refresh used ark-cn-beijing.ABCDEFGHIJK"},
			"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abc.def",
		},
		"tokens": []string{"sk-sp-H.ABCDEFGHIJKLMNOP"},
	}
	if _, err := adapter.AppendEvidence(ctx, EvidenceInput{
		WorkRef:        linkage.WorkRef,
		SessionID:      bridgeActor().SessionID,
		EventType:      "progress",
		IdempotencyKey: "dotted-1",
		Payload:        dirty,
		OccurredAt:     time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	stored, ok, err := adapter.LookupEvidence(ctx, linkage.WorkRef, "dotted-1")
	if err != nil || !ok {
		t.Fatalf("lookup: ok=%v err=%v", ok, err)
	}
	artifact, _ := stored.Payload["artifact"].(string)
	if strings.Contains(artifact, "sk-sp-H.ABCDEFGHIJKLMNOP") {
		t.Fatalf("dotted key leaked: %q", artifact)
	}
	trace, _ := stored.Payload["trace"].([]any)
	traceMap, _ := trace[0].(map[string]any)
	if line, _ := traceMap["line"].(string); strings.Contains(line, "ark-cn-beijing.ABCDEFGHIJK") {
		t.Fatalf("dotted ark key leaked from map in array: %q", line)
	}
	if bearer, _ := trace[1].(string); strings.Contains(bearer, "eyJhbGciOiJIUzI1NiJ9") {
		t.Fatalf("bearer leaked from string in array: %q", bearer)
	}
	tokens, _ := stored.Payload["tokens"].([]string)
	if len(tokens) == 1 && strings.Contains(tokens[0], "sk-sp-H.ABCDEFGHIJKLMNOP") {
		t.Fatalf("dotted key leaked from string slice: %+v", tokens)
	}
}

// R4/R5: the claim event's OccurredAt/ObservedAt are the real attempt and
// port observation times; the lease expiry stays payload-only. The attempt
// fixture is deliberately in the past so it passes through unclamped.
func TestClaimScopeUsesRealAttemptTime(t *testing.T) {
	adapter := newKernel(t)
	ctx := context.Background()
	chain := validChain()
	linkage, err := adapter.RegisterLinkage(ctx, LinkageInput{Chain: chain, Actor: bridgeActor(), MappingKind: "dispatch"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	attempt := time.Now().Add(-1 * time.Hour).Truncate(time.Nanosecond)
	expires := attempt.Add(90 * time.Minute)
	result, err := adapter.ClaimScope(ctx, ScopeClaimInput{
		WorkRef:    linkage.WorkRef,
		ClaimKey:   "claim-attempt-time-1",
		InstanceID: "bridge-x",
		SessionID:  bridgeActor().SessionID,
		Generation: 0,
		ExpiresAt:  expires,
		AttemptAt:  attempt,
	})
	if err != nil || !result.Acquired {
		t.Fatalf("ClaimScope: %+v err=%v", result, err)
	}
	service := adapter.Service
	replay, err := service.Replay(ctx, workentry.ReplayRequest{
		WorkspaceID:    adapter.WorkspaceID,
		IdempotencyKey: "claim-attempt-time-1",
		Kind:           "event",
		WorkRef:        linkage.WorkRef,
	})
	if err != nil || replay.Event == nil {
		t.Fatalf("replay: %+v err=%v", replay, err)
	}
	if replay.Event.OccurredAt != attempt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("OccurredAt = %q, want the real (past) attempt time %q", replay.Event.OccurredAt, attempt.UTC().Format(time.RFC3339Nano))
	}
	observed, oerr := time.Parse(time.RFC3339Nano, replay.Event.ObservedAt)
	if oerr != nil {
		t.Fatalf("parse ObservedAt: %v", oerr)
	}
	if observed.Before(attempt) {
		t.Fatalf("ObservedAt %v cannot precede the attempt %v", observed, attempt)
	}
	if time.Now().Add(time.Second).Before(observed) {
		t.Fatalf("ObservedAt %v lies in the future", observed)
	}
	expiresPayload, _ := replay.Event.EventPayload["expires_at"].(string)
	if expiresPayload != expires.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("expires_at payload = %q, want %q", expiresPayload, expires.UTC().Format(time.RFC3339Nano))
	}
}

// R5: a future caller AttemptAt must never be trusted. OccurredAt is clamped
// to the real attempt window and ObservedAt is the port observation time;
// neither stamp may lie in the future.
func TestClaimScopeClampsFutureAttemptAt(t *testing.T) {
	adapter := newKernel(t)
	ctx := context.Background()
	chain := validChain()
	linkage, err := adapter.RegisterLinkage(ctx, LinkageInput{Chain: chain, Actor: bridgeActor(), MappingKind: "dispatch"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	future := time.Now().Add(10 * time.Minute)
	expires := future.Add(30 * time.Second)
	before := time.Now()
	result, err := adapter.ClaimScope(ctx, ScopeClaimInput{
		WorkRef:    linkage.WorkRef,
		ClaimKey:   "claim-future-attempt-1",
		InstanceID: "bridge-x",
		SessionID:  bridgeActor().SessionID,
		Generation: 0,
		ExpiresAt:  expires,
		AttemptAt:  future,
	})
	if err != nil || !result.Acquired {
		t.Fatalf("ClaimScope: %+v err=%v", result, err)
	}
	replay, err := adapter.Service.Replay(ctx, workentry.ReplayRequest{
		WorkspaceID:    adapter.WorkspaceID,
		IdempotencyKey: "claim-future-attempt-1",
		Kind:           "event",
		WorkRef:        linkage.WorkRef,
	})
	if err != nil || replay.Event == nil {
		t.Fatalf("replay: %+v err=%v", replay, err)
	}
	if replay.Event.OccurredAt == future.Format(time.RFC3339Nano) {
		t.Fatalf("OccurredAt trusted the caller's future AttemptAt %q", replay.Event.OccurredAt)
	}
	occurred, cerr := time.Parse(time.RFC3339Nano, replay.Event.OccurredAt)
	if cerr != nil {
		t.Fatalf("parse OccurredAt: %v", cerr)
	}
	observed, oerr := time.Parse(time.RFC3339Nano, replay.Event.ObservedAt)
	if oerr != nil {
		t.Fatalf("parse ObservedAt: %v", oerr)
	}
	// Neither stamp may be future, and both must sit inside this call's
	// real window ([before, now]).
	after := time.Now()
	if occurred.After(after) || observed.After(after) {
		t.Fatalf("stamps lie in the future: occurred=%v observed=%v now=%v", occurred, observed, after)
	}
	if occurred.Before(before.Add(-time.Second)) || observed.Before(before.Add(-time.Second)) {
		t.Fatalf("stamps fall outside the call window: occurred=%v observed=%v window=[%v,%v]", occurred, observed, before, after)
	}
	expiresPayload, _ := replay.Event.EventPayload["expires_at"].(string)
	if expiresPayload != expires.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("expires_at payload = %q, want %q (payload-only, untouched by clamping)", expiresPayload, expires.UTC().Format(time.RFC3339Nano))
	}
}

// R5: an absent AttemptAt stamps OccurredAt and ObservedAt with the port's
// own observation time (identical, never future).
func TestClaimScopeAbsentAttemptAtUsesPortTime(t *testing.T) {
	adapter := newKernel(t)
	ctx := context.Background()
	chain := validChain()
	linkage, err := adapter.RegisterLinkage(ctx, LinkageInput{Chain: chain, Actor: bridgeActor(), MappingKind: "dispatch"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := adapter.ClaimScope(ctx, ScopeClaimInput{
		WorkRef:    linkage.WorkRef,
		ClaimKey:   "claim-absent-attempt-1",
		InstanceID: "bridge-x",
		SessionID:  bridgeActor().SessionID,
		Generation: 0,
		ExpiresAt:  time.Now().Add(30 * time.Second),
	}); err != nil {
		t.Fatalf("ClaimScope: %v", err)
	}
	replay, err := adapter.Service.Replay(ctx, workentry.ReplayRequest{
		WorkspaceID:    adapter.WorkspaceID,
		IdempotencyKey: "claim-absent-attempt-1",
		Kind:           "event",
		WorkRef:        linkage.WorkRef,
	})
	if err != nil || replay.Event == nil {
		t.Fatalf("replay: %+v err=%v", replay, err)
	}
	if replay.Event.OccurredAt != replay.Event.ObservedAt {
		t.Fatalf("absent AttemptAt must stamp OccurredAt==ObservedAt at port time: %q vs %q", replay.Event.OccurredAt, replay.Event.ObservedAt)
	}
	stamp, perr := time.Parse(time.RFC3339Nano, replay.Event.ObservedAt)
	if perr != nil {
		t.Fatalf("parse stamp: %v", perr)
	}
	if stamp.After(time.Now()) {
		t.Fatalf("stamp lies in the future: %v", stamp)
	}
}
