package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type wcJobMemberFixture struct {
	members []db.Member
	err     error
	calls   []pgtype.UUID
}

func (f *wcJobMemberFixture) ListMembers(_ context.Context, workspaceID pgtype.UUID) ([]db.Member, error) {
	f.calls = append(f.calls, workspaceID)
	if f.err != nil {
		return nil, f.err
	}
	return append([]db.Member(nil), f.members...), nil
}

func wcJobMember(workspaceID pgtype.UUID, userID pgtype.UUID, role string, createdAt time.Time) db.Member {
	return db.Member{
		ID:          pgtype.UUID{Bytes: [16]byte(uuid.New()), Valid: true},
		WorkspaceID: workspaceID, UserID: userID, Role: role,
		CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
	}
}

type wcJobDrainRunner struct {
	calls  []service.WorkConservingDrainRequest
	result service.WorkConservingDrainResult
	err    error
}

func (r *wcJobDrainRunner) Drain(_ context.Context, req service.WorkConservingDrainRequest) (service.WorkConservingDrainResult, error) {
	r.calls = append(r.calls, req)
	if r.err != nil {
		return r.result, r.err
	}
	return r.result, nil
}

// countingDrainRunner proves the job itself performs at most one Drain call
// per tick; it never inspects or retries the inner result.
type countingDrainRunner struct {
	inner WorkConservingDrainRunner
	calls int
}

func (r *countingDrainRunner) Drain(ctx context.Context, req service.WorkConservingDrainRequest) (service.WorkConservingDrainResult, error) {
	r.calls++
	return r.inner.Drain(ctx, req)
}

type wcJobProjector struct {
	projection service.WorkConservingProjection
}

func (p *wcJobProjector) ProjectWorkConserving(_ context.Context, req service.WorkConservingProjectionRequest) (service.WorkConservingProjection, error) {
	projection := p.projection
	projection.Limit, projection.Offset = req.Limit, req.Offset
	return projection, nil
}

// wcJobDispatcher succeeds for the first okCalls dispatches and then returns
// the exact receipt-conflict sentinel, simulating a coordinator that already
// committed the identical Task between two ticks.
type wcJobDispatcher struct {
	calls    int
	okCalls  int
	dispatch []service.ContinuousDispatchTriggerResult
}

func (d *wcJobDispatcher) DispatchIssue(_ context.Context, _, _, _ pgtype.UUID, _ pgtype.UUID, _ string) (service.ContinuousDispatchTriggerResult, error) {
	index := d.calls
	d.calls++
	if index < d.okCalls && index < len(d.dispatch) {
		return d.dispatch[index], nil
	}
	return service.ContinuousDispatchTriggerResult{}, service.ErrContinuousDispatchReceiptConflict
}

func wcJobUUID(seed byte) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte{seed}, Valid: true} }

func wcJobBinding(workspaceID, projectID pgtype.UUID) service.WorkConservingGoalSourceBinding {
	return service.WorkConservingGoalSourceBinding{
		SchemaVersion: "hivecosm.goal-graph/v2", GoalID: "goal-drain",
		WorkspaceID: uuid.UUID(workspaceID.Bytes).String(),
		ProjectID:   uuid.UUID(projectID.Bytes).String(),
		SourceRef:   "/goal/CHECKLIST.yaml",
	}
}

func wcJobBindingReader(binding service.WorkConservingGoalSourceBinding, err error) (WorkConservingGoalBindingReader, *int) {
	calls := 0
	return func(string) (service.WorkConservingGoalSourceBinding, error) {
		calls++
		return binding, err
	}, &calls
}

func wcJobProjection(workspaceID, projectID pgtype.UUID, suggestion continuousdispatch.WorkConservingSuggestion, now time.Time) service.WorkConservingProjection {
	return service.WorkConservingProjection{
		SchemaVersion: service.WorkConservingProjectionSchemaV1,
		State:         service.WorkConservingProjectionReady, ReasonCode: "plan_ready", GoalID: "goal-drain",
		Authority: service.WorkConservingAuthoritySnapshot{
			WorkspaceID: uuid.UUID(workspaceID.Bytes).String(), ProjectID: uuid.UUID(projectID.Bytes).String(),
			SourceRef: "/goal/CHECKLIST.yaml", Revision: "sha256:" + strings.Repeat("a", 64),
			ObservedAt: now.Add(-time.Minute).Format(time.RFC3339), ExpiresAt: now.Add(14 * time.Minute).Format(time.RFC3339),
		},
		Suggestions: []continuousdispatch.WorkConservingSuggestion{suggestion},
		Mismatch:    continuousdispatch.WorkConservingMismatch{OpenIssues: 1, PlannedIssues: 1},
		Total:       1, NoWrite: true,
	}
}

func wcJobSuggestion(issueID pgtype.UUID) continuousdispatch.WorkConservingSuggestion {
	return continuousdispatch.WorkConservingSuggestion{
		IssueID: uuid.UUID(issueID.Bytes).String(), GoalID: "goal-drain", EmployeeID: "EMP-001",
		AgentID: "agent-001", RuntimeID: "runtime-001", Receiver: "dispatch-coordinator",
		WakeCondition: "dispatch coordinator confirms candidate and current write lease",
	}
}

func wcJobRunHandler(t *testing.T, spec JobSpec) (HandlerResult, error) {
	t.Helper()
	if spec.Handler == nil {
		t.Fatal("job spec has no handler")
	}
	return spec.Handler(context.Background(), HandlerInput{})
}

// The frozen job contract: one global scope, latest-only, five-minute timeout,
// MaxAttempts=1, no retry backoff, and no stale reentry because the drain
// writes a Task plus receipt and must never be stolen by a concurrent runner.
func TestWorkConservingDrainJobSpecContract(t *testing.T) {
	spec := WorkConservingDrainJob(&wcJobDrainRunner{}, &wcJobMemberFixture{}, "/goal/CHECKLIST.yaml")
	if spec.Name != "work_conserving_drain" {
		t.Fatalf("name = %q", spec.Name)
	}
	if spec.Cadence != time.Minute {
		t.Fatalf("cadence = %s, want 1m", spec.Cadence)
	}
	if spec.CatchUpMode != CatchUpLatestOnly {
		t.Fatalf("catch-up mode = %v, want latest-only", spec.CatchUpMode)
	}
	if spec.RunTimeout != 5*time.Minute {
		t.Fatalf("run timeout = %s, want 5m", spec.RunTimeout)
	}
	if spec.StaleTimeout <= spec.RunTimeout {
		t.Fatalf("stale timeout %s must exceed run timeout", spec.StaleTimeout)
	}
	if spec.MaxAttempts != 1 {
		t.Fatalf("max attempts = %d, want 1", spec.MaxAttempts)
	}
	if len(spec.RetryBackoff) != 0 {
		t.Fatalf("retry backoff = %v, want none", spec.RetryBackoff)
	}
	if spec.MaxPlansPerTick != 1 {
		t.Fatalf("max plans per tick = %d, want 1", spec.MaxPlansPerTick)
	}
	scopes, err := spec.Scopes(context.Background(), time.Now())
	if err != nil || len(scopes) != 1 || scopes[0] != ScopeGlobal {
		t.Fatalf("scopes = %v err = %v, want exactly the global scope", scopes, err)
	}
	if err := spec.validate(); err != nil {
		t.Fatalf("spec must validate: %v", err)
	}
}

func TestWorkConservingDrainJobSpecContract_AllowStaleReentryFalse(t *testing.T) {
	spec := WorkConservingDrainJob(&wcJobDrainRunner{}, &wcJobMemberFixture{}, "/goal/CHECKLIST.yaml")
	if spec.AllowStaleReentry {
		t.Fatal("AllowStaleReentry must be false: drain is non-idempotent at the Task-write layer")
	}
	if workConservingDrainBatchSize != 1 {
		t.Fatalf("batch size const = %d, want 1", workConservingDrainBatchSize)
	}
}

func TestWorkConservingDrainJobZeroOwnersFailsClosed(t *testing.T) {
	workspaceID, projectID := wcJobUUID(1), wcJobUUID(2)
	owner := wcJobUUID(3)
	members := &wcJobMemberFixture{members: []db.Member{
		wcJobMember(workspaceID, owner, "admin", time.Now()),
	}}
	runner := &wcJobDrainRunner{result: service.WorkConservingDrainResult{State: service.WorkConservingDrainStateReady, Dispatched: 1}}
	readBinding, bindingCalls := wcJobBindingReader(wcJobBinding(workspaceID, projectID), nil)
	spec := workConservingDrainJobSpec(runner, members, readBinding, "/goal/CHECKLIST.yaml")

	result, err := wcJobRunHandler(t, spec)
	if err != nil {
		t.Fatalf("zero owners must be a truthful no-write tick, got error %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("drain calls = %d, want zero", len(runner.calls))
	}
	if result.RowsAffected != 0 {
		t.Fatalf("rows affected = %d, want zero", result.RowsAffected)
	}
	if result.Result["state"] != workConservingDrainOwnerMissing {
		t.Fatalf("state = %v, want owner_missing", result.Result["state"])
	}
	if len(members.calls) != 1 || *bindingCalls != 1 {
		t.Fatalf("resolution reads members=%d binding=%d, want exactly one each", len(members.calls), *bindingCalls)
	}
}

func TestWorkConservingDrainJobMultipleOwnersFailsClosed(t *testing.T) {
	workspaceID, projectID := wcJobUUID(1), wcJobUUID(2)
	first, second := wcJobUUID(3), wcJobUUID(4)
	later, earlier := time.Now(), time.Now().Add(-time.Hour)
	members := &wcJobMemberFixture{members: []db.Member{
		// The later-created owner is listed first: ambiguity must not be
		// resolved by any ordering (created-at, user id, or listing order).
		wcJobMember(workspaceID, second, "owner", later),
		wcJobMember(workspaceID, first, "owner", earlier),
	}}
	runner := &wcJobDrainRunner{result: service.WorkConservingDrainResult{State: service.WorkConservingDrainStateReady, Dispatched: 1}}
	readBinding, _ := wcJobBindingReader(wcJobBinding(workspaceID, projectID), nil)
	spec := workConservingDrainJobSpec(runner, members, readBinding, "/goal/CHECKLIST.yaml")

	result, err := wcJobRunHandler(t, spec)
	if err != nil {
		t.Fatalf("ambiguous owners must be a truthful no-write tick, got error %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("drain calls = %d, want zero writes on ambiguity", len(runner.calls))
	}
	if result.RowsAffected != 0 {
		t.Fatalf("rows affected = %d, want zero", result.RowsAffected)
	}
	if result.Result["state"] != workConservingDrainOwnerAmbiguous {
		t.Fatalf("state = %v, want owner_ambiguous", result.Result["state"])
	}
	encoded, jsonErr := json.Marshal(result.Result)
	if jsonErr != nil {
		t.Fatal(jsonErr)
	}
	for _, id := range []pgtype.UUID{first, second} {
		if strings.Contains(string(encoded), uuid.UUID(id.Bytes).String()) {
			t.Fatalf("ambiguous-owner result must not encode a deterministic winner: %s", encoded)
		}
	}
}

func TestWorkConservingDrainJobForeignWorkspaceOwnerIgnored(t *testing.T) {
	workspaceID, projectID := wcJobUUID(1), wcJobUUID(2)
	foreign := wcJobUUID(9)
	members := &wcJobMemberFixture{members: []db.Member{
		wcJobMember(foreign, wcJobUUID(3), "owner", time.Now()),
	}}
	runner := &wcJobDrainRunner{result: service.WorkConservingDrainResult{State: service.WorkConservingDrainStateReady, Dispatched: 1}}
	readBinding, _ := wcJobBindingReader(wcJobBinding(workspaceID, projectID), nil)
	spec := workConservingDrainJobSpec(runner, members, readBinding, "/goal/CHECKLIST.yaml")

	result, err := wcJobRunHandler(t, spec)
	if err != nil {
		t.Fatalf("foreign-workspace owner must fail closed without error, got %v", err)
	}
	if len(runner.calls) != 0 || result.RowsAffected != 0 {
		t.Fatalf("runner calls = %d rows = %d, want zero writes", len(runner.calls), result.RowsAffected)
	}
	if result.Result["state"] != workConservingDrainOwnerMissing {
		t.Fatalf("state = %v, want owner_missing for a foreign-workspace owner row", result.Result["state"])
	}
}

func TestWorkConservingDrainJobInvalidOwnerIDFailsClosed(t *testing.T) {
	workspaceID, projectID := wcJobUUID(1), wcJobUUID(2)
	members := &wcJobMemberFixture{members: []db.Member{
		{ID: pgtype.UUID{Bytes: [16]byte{7}, Valid: true}, WorkspaceID: workspaceID, Role: "owner"},
		wcJobMember(workspaceID, wcJobUUID(3), "owner", time.Now()),
	}}
	runner := &wcJobDrainRunner{result: service.WorkConservingDrainResult{State: service.WorkConservingDrainStateReady, Dispatched: 1}}
	members.members = members.members[:1] // keep only the invalid-user-id owner row
	readBinding, _ := wcJobBindingReader(wcJobBinding(workspaceID, projectID), nil)
	spec := workConservingDrainJobSpec(runner, members, readBinding, "/goal/CHECKLIST.yaml")

	result, err := wcJobRunHandler(t, spec)
	if err != nil {
		t.Fatalf("invalid owner id must fail closed without error, got %v", err)
	}
	if len(runner.calls) != 0 || result.RowsAffected != 0 {
		t.Fatalf("runner calls = %d rows = %d, want zero writes", len(runner.calls), result.RowsAffected)
	}
	if result.Result["state"] != workConservingDrainOwnerMissing {
		t.Fatalf("state = %v, want owner_missing for an invalid owner id", result.Result["state"])
	}
}

func TestWorkConservingDrainJobExactlyOneOwnerSucceeds(t *testing.T) {
	workspaceID, projectID := wcJobUUID(1), wcJobUUID(2)
	owner, otherUser := wcJobUUID(3), wcJobUUID(4)
	members := &wcJobMemberFixture{members: []db.Member{
		wcJobMember(workspaceID, otherUser, "member", time.Now()),
		wcJobMember(workspaceID, owner, "owner", time.Now().Add(-time.Minute)),
		wcJobMember(workspaceID, otherUser, "admin", time.Now().Add(-2*time.Minute)),
	}}
	runner := &wcJobDrainRunner{result: service.WorkConservingDrainResult{
		State: service.WorkConservingDrainStateReady, GoalID: "goal-drain", BatchSize: workConservingDrainBatchSize, Dispatched: 1,
	}}
	readBinding, _ := wcJobBindingReader(wcJobBinding(workspaceID, projectID), nil)
	spec := workConservingDrainJobSpec(runner, members, readBinding, "/goal/CHECKLIST.yaml")

	result, err := wcJobRunHandler(t, spec)
	if err != nil {
		t.Fatalf("healthy tick: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("drain calls = %d, want exactly one per tick", len(runner.calls))
	}
	call := runner.calls[0]
	if call.WorkspaceID != workspaceID || call.ProjectID != projectID {
		t.Fatalf("drain scope = %+v, want the goal binding scope", call)
	}
	if call.ActorUserID != owner {
		t.Fatalf("actor = %v, want the single workspace owner", call.ActorUserID)
	}
	if call.BatchSize != 1 {
		t.Fatalf("batch size = %d, want 1", call.BatchSize)
	}
	if result.RowsAffected != 1 || result.Result["state"] != service.WorkConservingDrainStateReady {
		t.Fatalf("result = %+v, want one dispatched row in ready state", result)
	}
}

func TestWorkConservingDrainJobMissingGoalBindingPerformsZeroWrites(t *testing.T) {
	workspaceID := wcJobUUID(1)
	members := &wcJobMemberFixture{members: []db.Member{wcJobMember(workspaceID, wcJobUUID(3), "owner", time.Now())}}
	runner := &wcJobDrainRunner{result: service.WorkConservingDrainResult{State: service.WorkConservingDrainStateReady, Dispatched: 1}}
	readBinding, bindingCalls := wcJobBindingReader(service.WorkConservingGoalSourceBinding{}, fmt.Errorf("multi-document stream: %w", service.ErrWorkConservingProjectionSourceGap))
	spec := workConservingDrainJobSpec(runner, members, readBinding, "/goal/CHECKLIST.yaml")

	result, err := wcJobRunHandler(t, spec)
	if err != nil {
		t.Fatalf("unreadable goal source must stay a no-write tick, got %v", err)
	}
	if len(runner.calls) != 0 || len(members.calls) != 0 || result.RowsAffected != 0 {
		t.Fatalf("binding failure wrote: runner=%d members=%d rows=%d", len(runner.calls), len(members.calls), result.RowsAffected)
	}
	if *bindingCalls != 1 {
		t.Fatalf("binding reads = %d, want one", *bindingCalls)
	}
	if result.Result["state"] != service.WorkConservingDrainStateSourceGap || result.Result["reason"] != "goal_binding" {
		t.Fatalf("result = %v, want source_gap/goal_binding", result.Result)
	}
}

func TestWorkConservingDrainJobNonCanonicalGoalBindingFailsClosed(t *testing.T) {
	workspaceID, projectID := wcJobUUID(1), wcJobUUID(2)
	for name, binding := range map[string]service.WorkConservingGoalSourceBinding{
		"garbage workspace": {GoalID: "goal-drain", WorkspaceID: "not-a-uuid", ProjectID: uuid.UUID(projectID.Bytes).String()},
		"garbage project":   {GoalID: "goal-drain", WorkspaceID: uuid.UUID(workspaceID.Bytes).String(), ProjectID: "../escape"},
		"null workspace":    {GoalID: "goal-drain", WorkspaceID: "NULL", ProjectID: uuid.UUID(projectID.Bytes).String()},
	} {
		t.Run(name, func(t *testing.T) {
			members := &wcJobMemberFixture{members: []db.Member{wcJobMember(workspaceID, wcJobUUID(3), "owner", time.Now())}}
			runner := &wcJobDrainRunner{result: service.WorkConservingDrainResult{State: service.WorkConservingDrainStateReady, Dispatched: 1}}
			readBinding, _ := wcJobBindingReader(binding, nil)
			spec := workConservingDrainJobSpec(runner, members, readBinding, "/goal/CHECKLIST.yaml")
			result, err := wcJobRunHandler(t, spec)
			if err != nil {
				t.Fatalf("non-canonical binding must fail closed without error, got %v", err)
			}
			if len(runner.calls) != 0 || len(members.calls) != 0 || result.RowsAffected != 0 {
				t.Fatalf("non-canonical binding wrote: runner=%d members=%d rows=%d", len(runner.calls), len(members.calls), result.RowsAffected)
			}
			if result.Result["state"] != service.WorkConservingDrainStateSourceGap {
				t.Fatalf("state = %v, want source_gap", result.Result["state"])
			}
		})
	}
}

func TestWorkConservingDrainJobSurfacesMemberReadErrorFailsClosed(t *testing.T) {
	workspaceID, projectID := wcJobUUID(1), wcJobUUID(2)
	members := &wcJobMemberFixture{err: errors.New("directory unavailable")}
	runner := &wcJobDrainRunner{result: service.WorkConservingDrainResult{State: service.WorkConservingDrainStateReady, Dispatched: 1}}
	readBinding, _ := wcJobBindingReader(wcJobBinding(workspaceID, projectID), nil)
	spec := workConservingDrainJobSpec(runner, members, readBinding, "/goal/CHECKLIST.yaml")

	_, err := wcJobRunHandler(t, spec)
	if err == nil || !strings.Contains(err.Error(), "list members") {
		t.Fatalf("error = %v, want the member read failure surfaced", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %d, want zero writes", len(runner.calls))
	}
}

func TestWorkConservingDrainJobSurfacesDrainWholeFailureOnce(t *testing.T) {
	workspaceID, projectID := wcJobUUID(1), wcJobUUID(2)
	members := &wcJobMemberFixture{members: []db.Member{wcJobMember(workspaceID, wcJobUUID(3), "owner", time.Now())}}
	runner := &wcJobDrainRunner{
		result: service.WorkConservingDrainResult{State: service.WorkConservingDrainStateSourceGap, ReasonCode: "projection_source_gap"},
		err:    fmt.Errorf("work-conserving drain projection: %w", service.ErrWorkConservingProjectionSourceGap),
	}
	readBinding, _ := wcJobBindingReader(wcJobBinding(workspaceID, projectID), nil)
	spec := workConservingDrainJobSpec(runner, members, readBinding, "/goal/CHECKLIST.yaml")

	result, err := wcJobRunHandler(t, spec)
	if err == nil || !errors.Is(err, service.ErrWorkConservingProjectionSourceGap) {
		t.Fatalf("error = %v, want the whole-drain failure surfaced", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want exactly one attempt with no retry loop", len(runner.calls))
	}
	if result.RowsAffected != 0 || result.Result["state"] != service.WorkConservingDrainStateSourceGap {
		t.Fatalf("result = %+v, want zero rows in source_gap state", result)
	}
}

// Composes the real drain service over fake projector/dispatcher seams. The
// second tick replays the same request, hits the receipt conflict sentinel and
// reports it unchanged: the job adds no second idempotency layer and no retry.
func TestWorkConservingDrainJobRepeatedTickKeepsExistingIdempotency(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	workspaceID, projectID, owner := wcJobUUID(1), wcJobUUID(2), wcJobUUID(3)
	issueID := wcJobUUID(4)
	projector := &wcJobProjector{projection: wcJobProjection(workspaceID, projectID, wcJobSuggestion(issueID), now)}
	dispatcher := &wcJobDispatcher{okCalls: 1, dispatch: []service.ContinuousDispatchTriggerResult{
		{Receipt: service.ContinuousDispatchReceipt{TaskID: wcJobUUID(5)}},
	}}
	realDrain := service.NewWorkConservingDrainService(projector, dispatcher).WithClock(clock)
	members := &wcJobMemberFixture{members: []db.Member{wcJobMember(workspaceID, owner, "owner", time.Now())}}
	readBinding, _ := wcJobBindingReader(wcJobBinding(workspaceID, projectID), nil)
	spec := workConservingDrainJobSpec(realDrain, members, readBinding, "/goal/CHECKLIST.yaml")

	first, err := wcJobRunHandler(t, spec)
	if err != nil {
		t.Fatalf("first tick: %v", err)
	}
	second, err := wcJobRunHandler(t, spec)
	if err != nil {
		t.Fatalf("second tick must surface the receipt conflict without error, got %v", err)
	}
	if first.RowsAffected != 1 || first.Result["dispatched"] != 1 {
		t.Fatalf("first tick = %+v, want one dispatched Task", first)
	}
	if second.RowsAffected != 0 || second.Result["dispatched"] != 0 || second.Result["conflicts"] != 1 {
		t.Fatalf("second tick = %+v, want zero dispatches and one conflict", second)
	}
	if reason, _ := second.Result["conflict_reason"].(string); !strings.Contains(reason, service.ErrContinuousDispatchReceiptConflict.Error()) {
		t.Fatalf("conflict reason = %v, want the receipt-conflict sentinel unchanged", second.Result["conflict_reason"])
	}
	if dispatcher.calls != 2 {
		t.Fatalf("dispatcher calls = %d, want exactly one per tick with no internal retry", dispatcher.calls)
	}
}

func TestWorkConservingDrainJobDoesNotRetryOnConflict(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	workspaceID, projectID, owner := wcJobUUID(1), wcJobUUID(2), wcJobUUID(3)
	issueID := wcJobUUID(4)
	projector := &wcJobProjector{projection: wcJobProjection(workspaceID, projectID, wcJobSuggestion(issueID), now)}
	dispatcher := &wcJobDispatcher{okCalls: 0}
	realDrain := service.NewWorkConservingDrainService(projector, dispatcher).WithClock(clock)
	counting := &countingDrainRunner{inner: realDrain}
	members := &wcJobMemberFixture{members: []db.Member{wcJobMember(workspaceID, owner, "owner", time.Now())}}
	readBinding, _ := wcJobBindingReader(wcJobBinding(workspaceID, projectID), nil)
	spec := workConservingDrainJobSpec(counting, members, readBinding, "/goal/CHECKLIST.yaml")

	result, err := wcJobRunHandler(t, spec)
	if err != nil {
		t.Fatalf("first-tick conflict must be surfaced without error, got %v", err)
	}
	if counting.calls != 1 || dispatcher.calls != 1 {
		t.Fatalf("drain/dispatch calls = %d/%d, want one one-shot attempt and no loop", counting.calls, dispatcher.calls)
	}
	if result.RowsAffected != 0 || result.Result["conflicts"] != 1 || result.Result["dispatched"] != 0 {
		t.Fatalf("result = %+v, want zero writes with the conflict surfaced", result)
	}
	if reason, _ := result.Result["conflict_reason"].(string); !strings.Contains(reason, service.ErrContinuousDispatchReceiptConflict.Error()) {
		t.Fatalf("conflict reason = %v, want the sentinel unchanged", result.Result["conflict_reason"])
	}
}
