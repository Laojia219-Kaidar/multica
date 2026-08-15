package workentry

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func fixtureActor(t ActorType) WorkActorIdentityV1 {
	return WorkActorIdentityV1{
		ActorType:   t,
		ActorID:     "EXT-test-agent-01",
		CarrierID:   "claude",
		SessionID:   "session-01",
		WorkspaceID: "ws-1",
		ObservedAt:  "2026-08-15T22:00:00.000Z",
	}
}

func fixtureIntent() WorkIntentV1 {
	return WorkIntentV1{
		OwnerIntent:             "William: implement the work registration kernel",
		GoalRef:                 "HIVECREW-UNIVERSAL-DEVELOPMENT-ENTRY-PROJECT-OS-V1",
		Objective:               "implement the universal work registration kernel",
		ExpectedHumanResult:     "go build + go test pass and receipts replay",
		Repo:                    "/Volumes/HiveData/hivecosm/HQ-50-代码仓库/01-源码下载/multica",
		BaselineRevision:        "bd7b9a28b79f28b5305568dfecfca5ac092d76c6",
		BranchOrWorktree:        "/Users/jiawei/hivecosm-worktrees/hivecrew-universal-development-entry-project-os-v1",
		ReadScope:               []string{"/Volumes/HiveData/hivecosm/HQ-50-代码仓库/01-源码下载/multica"},
		WriteScope:              []string{"/Users/jiawei/hivecosm-worktrees/hivecrew-universal-development-entry-project-os-v1"},
		ExpectedOutcomes:        []string{"server/internal/workentry package"},
		CandidateFormalBoundary: BoundaryCandidate,
	}
}

func TestRegisterReplaySameDigest(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	actor := fixtureActor(ActorExternalAgent)
	intent := fixtureIntent()
	req := RegisterRequest{ResolveRequest: ResolveRequest{Actor: actor, Intent: intent}, ConfirmCreate: true}

	first, err := svc.Register(ctx, req)
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	if !first.Created {
		t.Fatalf("expected created receipt, got decision=%s", first.ResolutionDecision)
	}
	if first.Replay.Replayed {
		t.Fatalf("first register must not be a replay")
	}

	second, err := svc.Register(ctx, req)
	if err != nil {
		t.Fatalf("replay register: %v", err)
	}
	if !second.Replay.Replayed {
		t.Fatalf("expected replayed=true on same key + same digest")
	}
	if second.WorkRef != first.WorkRef {
		t.Fatalf("replay work_ref mismatch: %q != %q", second.WorkRef, first.WorkRef)
	}
	if second.ProjectID != first.ProjectID || second.IssueID != first.IssueID {
		t.Fatalf("replay lineage mismatch")
	}
	if second.Replay.OriginalReceiptRef != first.WorkRef {
		t.Fatalf("replay original_receipt_ref mismatch: %q != %q", second.Replay.OriginalReceiptRef, first.WorkRef)
	}
}

func TestRegisterConflictDifferentDigest(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	actor := fixtureActor(ActorExternalAgent)
	intent := fixtureIntent()
	if _, err := svc.Register(ctx, RegisterRequest{ResolveRequest: ResolveRequest{Actor: actor, Intent: intent}, ConfirmCreate: true}); err != nil {
		t.Fatalf("seed register: %v", err)
	}

	changed := intent
	changed.Objective = "a different objective with a different digest"
	_, err := svc.Register(ctx, RegisterRequest{ResolveRequest: ResolveRequest{Actor: actor, Intent: changed}, ConfirmCreate: true})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for same key + diff digest, got %v", err)
	}
}

func TestExternalAgentWithoutEmployeeIDRegisters(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	actor := fixtureActor(ActorExternalAgent)
	actor.EmployeeID = "" // external agents are not forced to carry an employee_id (VC-02)
	intent := fixtureIntent()

	receipt, err := svc.Register(ctx, RegisterRequest{ResolveRequest: ResolveRequest{Actor: actor, Intent: intent}, ConfirmCreate: true})
	if err != nil {
		t.Fatalf("external_agent without employee_id must register: %v", err)
	}
	if !receipt.Created {
		t.Fatalf("expected created receipt, got %s", receipt.ResolutionDecision)
	}
	if receipt.ActorIdentity.EmployeeID != "" {
		t.Fatalf("external_agent must not be forced into an employee_id")
	}
}

func TestRegisteredEmployeeRequiresEmployeeID(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	actor := fixtureActor(ActorRegisteredEmployee)
	actor.ActorID = "DE-0001"
	actor.EmployeeID = "" // missing — must fail closed
	intent := fixtureIntent()

	_, err := svc.Register(ctx, RegisterRequest{ResolveRequest: ResolveRequest{Actor: actor, Intent: intent}, ConfirmCreate: true})
	if err == nil {
		t.Fatalf("registered_employee without employee_id must be rejected")
	}
	if errors.Is(err, ErrClassificationRequired) || errors.Is(err, ErrConflict) {
		t.Fatalf("wrong error class: %v", err)
	}
}

func TestResolveClassificationRequiredDoesNotCreate(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	actor := fixtureActor(ActorExternalAgent)
	intent := fixtureIntent()
	res, err := svc.ResolvePreview(ctx, ResolveRequest{Actor: actor, Intent: intent})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.ResolutionDecision != DecisionClassificationRequired {
		t.Fatalf("expected classification_required, got %s", res.ResolutionDecision)
	}

	// register without confirm_create must refuse to create.
	_, err = svc.Register(ctx, RegisterRequest{ResolveRequest: ResolveRequest{Actor: actor, Intent: intent}})
	if !errors.Is(err, ErrClassificationRequired) {
		t.Fatalf("expected ErrClassificationRequired, got %v", err)
	}

	if len(store.projects) != 0 || len(store.issues) != 0 {
		t.Fatalf("classification_required must not create any project/issue: projects=%d issues=%d", len(store.projects), len(store.issues))
	}
	if len(store.receipts) != 0 {
		t.Fatalf("classification_required must not persist a receipt")
	}
}

func TestResolveContinuedViaWorkOrderLink(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	const woRef = "hive://hivecosm/delivery/project/p1/work-order/WO-1"
	store.SeedWorkOrderLink(ExternalWorkOrderLink{
		WorkspaceID: "ws-1", WorkOrderRef: woRef, LinkedRevision: "r1",
		LinkedDigest: "sha256:" + strings.Repeat("a", 64), IssueID: "issue-1",
	})

	actor := fixtureActor(ActorExternalAgent)
	intent := fixtureIntent()
	intent.GoalRef = woRef

	res, err := svc.ResolvePreview(ctx, ResolveRequest{Actor: actor, Intent: intent})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.ResolutionDecision != DecisionContinued {
		t.Fatalf("expected continued, got %s", res.ResolutionDecision)
	}
	if len(res.Matches) != 1 || res.Matches[0].Kind != MatchWorkOrder || res.Matches[0].IssueID != "issue-1" {
		t.Fatalf("unexpected matches: %+v", res.Matches)
	}
}

func TestEventIdempotentAndConflict(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	event := WorkEventV1{
		WorkRef:        "hivecrew://ws-1/work/p1/i1",
		SessionID:      "session-01",
		EventType:      EventProgress,
		EventPayload:   map[string]any{"done": "types.go"},
		IdempotencyKey: "evt-1",
		OccurredAt:     "2026-08-15T22:01:00.000Z",
		ObservedAt:     "2026-08-15T22:01:00.000Z",
	}

	first, err := svc.Event(ctx, event)
	if err != nil {
		t.Fatalf("append event: %v", err)
	}
	if first.Replayed {
		t.Fatalf("first append must not replay")
	}

	second, err := svc.Event(ctx, event)
	if err != nil {
		t.Fatalf("replay event: %v", err)
	}
	if !second.Replayed || second.Sequence != first.Sequence {
		t.Fatalf("expected replayed with same sequence, got %+v vs %+v", second, first)
	}

	changed := event
	changed.EventPayload = map[string]any{"done": "different"}
	if _, err := svc.Event(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for same key + diff event payload, got %v", err)
	}
}

func TestBlockedEventRequiresBlockerReason(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	event := WorkEventV1{
		WorkRef:        "hivecrew://ws-1/work/p1/i1",
		SessionID:      "s",
		EventType:      EventBlocked,
		IdempotencyKey: "k",
		OccurredAt:     "2026-08-15T22:00:00.000Z",
		ObservedAt:     "2026-08-15T22:00:00.000Z",
	}
	if _, err := svc.Event(context.Background(), event); err == nil {
		t.Fatalf("blocked event without blocker_reason must be rejected")
	}
}

func TestHandoffAndFinishNeverAutoPass(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	h, err := svc.Handoff(ctx, WorkHandoffV1{
		WorkRef:          "hivecrew://ws-1/work/p1/i1",
		Revision:         "r1",
		BranchOrWorktree: "/wt/1",
		DiffFiles:        []string{"a.go"},
		ArtifactRefs:     []string{"cand-1"},
		NextAction:       "review",
	})
	if err != nil {
		t.Fatalf("handoff: %v", err)
	}
	if !h.ReviewRouted || h.AutoPassed {
		t.Fatalf("handoff must route to review and never auto-pass: %+v", h)
	}

	f, err := svc.Finish(ctx, WorkCompletionV1{
		WorkRef: "hivecrew://ws-1/work/p1/i1",
		CompletionCandidate: CompletionCandidate{
			ArtifactRef: "cand-1", Digest: "sha256:" + strings.Repeat("b", 64), Revision: "r1",
		},
		Review:                      CompletionReview{Decision: ReviewPass},
		ProjectLifecycleConsequence: LifecycleContinue,
	})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if !f.ReviewRouted || f.AutoPassed {
		t.Fatalf("finish must route to review and never auto-pass: %+v", f)
	}
}

func TestHeartbeatAccepted(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	res, err := svc.Heartbeat(context.Background(), HeartbeatRecord{
		WorkspaceID: "ws-1", ActorID: "EXT-test-agent-01", SessionID: "s", Host: "host-1",
	})
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("heartbeat must be accepted")
	}
}

func TestDigestDeterministicAndPrefixed(t *testing.T) {
	actor := fixtureActor(ActorExternalAgent)
	intent := fixtureIntent()
	d1, err := ReceiptDigest(actor, intent)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	d2, err := ReceiptDigest(actor, intent)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if d1 != d2 {
		t.Fatalf("digest must be deterministic: %q != %q", d1, d2)
	}
	if !strings.HasPrefix(d1, "sha256:") || len(d1) != len("sha256:")+64 {
		t.Fatalf("digest must be sha256:<64hex>, got %q", d1)
	}
}

func TestFormatWorkRef(t *testing.T) {
	cases := []struct {
		ws, proj, issue, task string
		want                  string
	}{
		{"ws-1", "p1", "i1", "", "hivecrew://ws-1/work/p1/i1"},
		{"ws-1", "p1", "i1", "t1", "hivecrew://ws-1/work/p1/i1/t1"},
		{"ws-1", "", "", "", "hivecrew://ws-1/work/inbox"},
		{"ws-1", "", "i1", "", "hivecrew://ws-1/work/inbox/i1"},
	}
	for _, c := range cases {
		if got := FormatWorkRef(c.ws, c.proj, c.issue, c.task); got != c.want {
			t.Fatalf("FormatWorkRef(%q,%q,%q,%q) = %q, want %q", c.ws, c.proj, c.issue, c.task, got, c.want)
		}
	}
}

func TestReplayReturnsOriginalReceipt(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	actor := fixtureActor(ActorExternalAgent)
	intent := fixtureIntent()
	first, err := svc.Register(ctx, RegisterRequest{ResolveRequest: ResolveRequest{Actor: actor, Intent: intent}, ConfirmCreate: true})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	key := DedupeKey(actor.WorkspaceID, actor.ActorID, intent.GoalRef, intent.Repo, intent.BaselineRevision, intent.BranchOrWorktree)
	replay, err := svc.Replay(ctx, ReplayRequest{WorkspaceID: actor.WorkspaceID, IdempotencyKey: key})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.Receipt == nil {
		t.Fatalf("expected receipt in replay")
	}
	if replay.Receipt.WorkRef != first.WorkRef {
		t.Fatalf("replay receipt work_ref mismatch")
	}
	if !replay.Receipt.Replay.Replayed {
		t.Fatalf("replay receipt must be marked replayed")
	}
}

func TestParseWorkRef(t *testing.T) {
	ws, proj, issue, task := ParseWorkRef("hivecrew://ws-1/work/p1/i1")
	if ws != "ws-1" || proj != "p1" || issue != "i1" || task != "" {
		t.Fatalf("parse no-task: got %q %q %q %q", ws, proj, issue, task)
	}
	ws, proj, issue, task = ParseWorkRef("hivecrew://ws-1/work/p1/i1/t1")
	if ws != "ws-1" || proj != "p1" || issue != "i1" || task != "t1" {
		t.Fatalf("parse with-task: got %q %q %q %q", ws, proj, issue, task)
	}
	if a, b, c, d := ParseWorkRef("not-a-workref"); a != "" || b != "" || c != "" || d != "" {
		t.Fatalf("foreign format should be empty, got %q %q %q %q", a, b, c, d)
	}
}
