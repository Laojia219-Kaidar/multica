package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type drainProjectorFixture struct {
	projection WorkConservingProjection
	err        error
	calls      []WorkConservingProjectionRequest
}

func (f *drainProjectorFixture) ProjectWorkConserving(_ context.Context, req WorkConservingProjectionRequest) (WorkConservingProjection, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return WorkConservingProjection{}, f.err
	}
	projection := f.projection
	projection.Limit, projection.Offset = req.Limit, req.Offset
	return projection, nil
}

type drainDispatchCall struct {
	workspaceID, projectID, issueID, actorUserID pgtype.UUID
	handoffNote                                  string
}

type drainDispatcherFixture struct {
	calls []drainDispatchCall
	fail  map[string]error
}

func (f *drainDispatcherFixture) DispatchIssue(
	_ context.Context,
	workspaceID, projectID, issueID, actorUserID pgtype.UUID,
	handoffNote string,
) (ContinuousDispatchTriggerResult, error) {
	f.calls = append(f.calls, drainDispatchCall{workspaceID, projectID, issueID, actorUserID, handoffNote})
	if err := f.fail[shadowUUIDString(issueID)]; err != nil {
		return ContinuousDispatchTriggerResult{}, err
	}
	receipt := dispatchReceiptFixture(60)
	receipt.Identity.WorkspaceID = shadowUUIDString(workspaceID)
	receipt.Identity.IssueID = shadowUUIDString(issueID)
	return ContinuousDispatchTriggerResult{Receipt: receipt}, nil
}

type drainRecordingExactDispatcher struct {
	inner    ContinuousDispatchExactDispatcher
	requests []ContinuousDispatchRequest
}

func (d *drainRecordingExactDispatcher) Dispatch(ctx context.Context, req ContinuousDispatchRequest) (ContinuousDispatchReceipt, error) {
	d.requests = append(d.requests, req)
	return d.inner.Dispatch(ctx, req)
}

func drainSuggestion(goalID string, issueID pgtype.UUID, employeeID string, agentID, runtimeID pgtype.UUID) continuousdispatch.WorkConservingSuggestion {
	return continuousdispatch.WorkConservingSuggestion{
		IssueID: shadowUUIDString(issueID), GoalID: goalID, EmployeeID: employeeID,
		AgentID: shadowUUIDString(agentID), RuntimeID: shadowUUIDString(runtimeID),
		Receiver: "dispatch-coordinator", WakeCondition: "dispatch coordinator confirms candidate and current write lease",
	}
}

func drainBlockedIssue(goalID string, issueID pgtype.UUID, reasons ...continuousdispatch.Reason) continuousdispatch.WorkConservingBlockedIssue {
	return continuousdispatch.WorkConservingBlockedIssue{
		IssueID: shadowUUIDString(issueID), GoalID: goalID, Reasons: reasons,
		Receiver: "dispatch-coordinator", WakeCondition: "an eligible non-conflicting candidate is observed",
	}
}

func drainProjectionFixture(workspaceID, projectID pgtype.UUID, goalID string, suggestions []continuousdispatch.WorkConservingSuggestion, blocked []continuousdispatch.WorkConservingBlockedIssue, now time.Time) WorkConservingProjection {
	state, reason := WorkConservingProjectionReady, "plan_ready"
	if len(blocked) > 0 || len(suggestions) == 0 {
		state, reason = WorkConservingProjectionBlocked, "blocked_backlog"
	}
	return WorkConservingProjection{
		SchemaVersion: WorkConservingProjectionSchemaV1, State: state, ReasonCode: reason, GoalID: goalID,
		Authority: WorkConservingAuthoritySnapshot{
			WorkspaceID: shadowUUIDString(workspaceID), ProjectID: shadowUUIDString(projectID),
			SourceRef: "/goal/CHECKLIST.yaml", Revision: "sha256:" + strings.Repeat("a", 64),
			ObservedAt: now.Add(-time.Minute).Format(time.RFC3339), ExpiresAt: now.Add(14 * time.Minute).Format(time.RFC3339),
		},
		Suggestions: suggestions, BlockedBacklog: blocked,
		Mismatch: continuousdispatch.WorkConservingMismatch{
			OpenIssues: len(suggestions) + len(blocked), PlannedIssues: len(suggestions), BlockedBacklog: len(blocked),
		},
		Total: len(suggestions) + len(blocked), NoWrite: true,
	}
}

func drainTestClock() (time.Time, func() time.Time) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	return now, func() time.Time { return now }
}

// The request struct and the dispatcher seam are the complete caller-facing
// surface of the drain. Anything beyond workspace/project/actor/batch would be
// a caller-controlled route selector, so this test fails when one appears.
func TestWorkConservingDrainExposesNoCallerRouteSelector(t *testing.T) {
	requestType := reflect.TypeOf(WorkConservingDrainRequest{})
	allowed := map[string]struct{}{
		"WorkspaceID": {}, "ProjectID": {}, "ActorUserID": {}, "BatchSize": {},
	}
	if requestType.NumField() != len(allowed) {
		t.Fatalf("WorkConservingDrainRequest has %d fields, want exactly %d", requestType.NumField(), len(allowed))
	}
	for i := 0; i < requestType.NumField(); i++ {
		name := requestType.Field(i).Name
		if _, ok := allowed[name]; !ok {
			t.Fatalf("WorkConservingDrainRequest gained caller-controlled field %q", name)
		}
	}

	dispatcherType := reflect.TypeOf((*WorkConservingDrainDispatcher)(nil)).Elem()
	method, ok := dispatcherType.MethodByName("DispatchIssue")
	if !ok {
		t.Fatal("WorkConservingDrainDispatcher must dispatch through DispatchIssue")
	}
	signature := method.Type
	if signature.NumIn() != 6 {
		t.Fatalf("DispatchIssue takes %d parameters, want ctx + 4 uuid + handoff note only", signature.NumIn())
	}
	uuidType := reflect.TypeOf(pgtype.UUID{})
	for i := 1; i <= 4; i++ {
		if signature.In(i) != uuidType {
			t.Fatalf("DispatchIssue parameter %d is %s, want pgtype.UUID (no route/model/runtime selector)", i, signature.In(i))
		}
	}
	if signature.In(5).Kind() != reflect.String {
		t.Fatalf("DispatchIssue parameter 5 is %s, want the server-built handoff note string", signature.In(5))
	}
}

func TestWorkConservingDrainDispatchesBoundedBatchAndContinuesAfterSingleFailure(t *testing.T) {
	now, clock := drainTestClock()
	workspaceID, projectID, actor := dispatchReceiptUUID(1), dispatchReceiptUUID(2), dispatchReceiptUUID(3)
	first, second, third := dispatchReceiptUUID(4), dispatchReceiptUUID(5), dispatchReceiptUUID(6)
	suggestions := []continuousdispatch.WorkConservingSuggestion{
		drainSuggestion("goal-drain", first, "EMP-001", dispatchReceiptUUID(7), dispatchReceiptUUID(8)),
		drainSuggestion("goal-drain", second, "EMP-002", dispatchReceiptUUID(9), dispatchReceiptUUID(10)),
		drainSuggestion("goal-drain", third, "EMP-003", dispatchReceiptUUID(11), dispatchReceiptUUID(12)),
	}
	projector := &drainProjectorFixture{projection: drainProjectionFixture(workspaceID, projectID, "goal-drain", suggestions, nil, now)}
	dispatcher := &drainDispatcherFixture{fail: map[string]error{
		shadowUUIDString(first): ErrContinuousDispatchConflict,
	}}
	drain := NewWorkConservingDrainService(projector, dispatcher).WithClock(clock)

	result, err := drain.Drain(context.Background(), WorkConservingDrainRequest{
		WorkspaceID: workspaceID, ProjectID: projectID, ActorUserID: actor, BatchSize: 2,
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if result.State != WorkConservingDrainStateReady || result.BatchSize != 2 {
		t.Fatalf("result state/batch = %s/%d, want ready/2", result.State, result.BatchSize)
	}
	if len(dispatcher.calls) != 2 {
		t.Fatalf("dispatch attempts = %d, want the bounded batch of 2", len(dispatcher.calls))
	}
	if shadowUUIDString(dispatcher.calls[0].issueID) != suggestions[0].IssueID ||
		shadowUUIDString(dispatcher.calls[1].issueID) != suggestions[1].IssueID {
		t.Fatalf("dispatch order = %v,%v; want deterministic projection order", dispatcher.calls[0].issueID, dispatcher.calls[1].issueID)
	}
	if dispatcher.calls[0].workspaceID != workspaceID || dispatcher.calls[0].projectID != projectID || dispatcher.calls[0].actorUserID != actor {
		t.Fatalf("dispatch call leaked wrong scope: %+v", dispatcher.calls[0])
	}
	for _, call := range dispatcher.calls {
		if call.handoffNote != workConservingDrainHandoffNote("goal-drain", shadowUUIDString(call.issueID)) {
			t.Fatalf("handoff note must be server-built and deterministic, got %q", call.handoffNote)
		}
	}
	if result.DeferredSuggestions != 1 {
		t.Fatalf("deferred suggestions = %d, want 1 beyond the batch", result.DeferredSuggestions)
	}
	if len(result.Results) != 2 || result.Results[0].Outcome != WorkConservingDrainConflict || result.Results[1].Outcome != WorkConservingDrainDispatched {
		t.Fatalf("results = %+v, want first conflict then dispatched despite the failure", result.Results)
	}
	if result.Conflicts != 1 || result.Dispatched != 1 {
		t.Fatalf("counters conflict/dispatched = %d/%d, want 1/1", result.Conflicts, result.Dispatched)
	}
	if result.Results[1].Receipt == nil || result.Results[1].Receipt.TaskID != dispatchReceiptFixture(60).TaskID {
		t.Fatalf("dispatched row receipt = %+v, want the exact committed receipt", result.Results[1].Receipt)
	}
}

func TestWorkConservingDrainClassifiesPerIssueOutcomesIndependently(t *testing.T) {
	now, clock := drainTestClock()
	workspaceID, projectID, actor := dispatchReceiptUUID(20), dispatchReceiptUUID(21), dispatchReceiptUUID(22)
	ids := []pgtype.UUID{dispatchReceiptUUID(23), dispatchReceiptUUID(24), dispatchReceiptUUID(25), dispatchReceiptUUID(26), dispatchReceiptUUID(27), dispatchReceiptUUID(28)}
	suggestions := make([]continuousdispatch.WorkConservingSuggestion, 0, len(ids))
	for _, id := range ids {
		suggestions = append(suggestions, drainSuggestion("goal-drain", id, "EMP-00X", dispatchReceiptUUID(29), dispatchReceiptUUID(30)))
	}
	dispatcher := &drainDispatcherFixture{fail: map[string]error{
		shadowUUIDString(ids[0]): nil,
		shadowUUIDString(ids[1]): ErrContinuousDispatchIssueNotReady,
		shadowUUIDString(ids[2]): ErrContinuousDispatchNotReady,
		shadowUUIDString(ids[3]): fmtWrap(ErrContinuousDispatchRouteDrift),
		shadowUUIDString(ids[4]): fmtWrap(ErrContinuousDispatchIssueAbsent),
		shadowUUIDString(ids[5]): errors.New("database is unavailable"),
	}}
	projector := &drainProjectorFixture{projection: drainProjectionFixture(workspaceID, projectID, "goal-drain", suggestions, nil, now)}
	result, err := NewWorkConservingDrainService(projector, dispatcher).WithClock(clock).Drain(context.Background(), WorkConservingDrainRequest{
		WorkspaceID: workspaceID, ProjectID: projectID, ActorUserID: actor, BatchSize: workConservingDrainMaxBatch,
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	want := []string{
		WorkConservingDrainDispatched, WorkConservingDrainAlreadyTerminal, WorkConservingDrainBlocked,
		WorkConservingDrainConflict, WorkConservingDrainSourceGap, WorkConservingDrainSourceGap,
	}
	if len(result.Results) != len(want) {
		t.Fatalf("results = %d, want %d", len(result.Results), len(want))
	}
	for i, outcome := range want {
		if result.Results[i].Outcome != outcome {
			t.Fatalf("result[%d] outcome = %s, want %s", i, result.Results[i].Outcome, outcome)
		}
		if result.Results[i].IssueID != suggestions[i].IssueID {
			t.Fatalf("result[%d] issue drift", i)
		}
	}
	if result.Dispatched != 1 || result.AlreadyTerminal != 1 || result.Blocked != 1 || result.Conflicts != 1 || result.SourceGaps != 2 {
		t.Fatalf("counters = %+v", result)
	}
	if !strings.Contains(result.Results[5].Reason, "database is unavailable") {
		t.Fatalf("unknown error must be preserved verbatim, got %q", result.Results[5].Reason)
	}
}

func fmtWrap(err error) error { return errors.Join(err, errors.New("wrapped trigger failure")) }

func TestWorkConservingDrainNeverDispatchesBlockedBacklog(t *testing.T) {
	now, clock := drainTestClock()
	workspaceID, projectID, actor := dispatchReceiptUUID(40), dispatchReceiptUUID(41), dispatchReceiptUUID(42)
	ready := dispatchReceiptUUID(43)
	blockedOne, blockedTwo := dispatchReceiptUUID(44), dispatchReceiptUUID(45)
	suggestions := []continuousdispatch.WorkConservingSuggestion{drainSuggestion("goal-drain", ready, "EMP-001", dispatchReceiptUUID(46), dispatchReceiptUUID(47))}
	blocked := []continuousdispatch.WorkConservingBlockedIssue{
		drainBlockedIssue("goal-drain", blockedOne, continuousdispatch.ReasonNoHealthyIdleEmployee),
		drainBlockedIssue("goal-drain", blockedTwo, continuousdispatch.ReasonIssueWritePathConflict),
	}
	projector := &drainProjectorFixture{projection: drainProjectionFixture(workspaceID, projectID, "goal-drain", suggestions, blocked, now)}
	dispatcher := &drainDispatcherFixture{}
	result, err := NewWorkConservingDrainService(projector, dispatcher).WithClock(clock).Drain(context.Background(), WorkConservingDrainRequest{
		WorkspaceID: workspaceID, ProjectID: projectID, ActorUserID: actor, BatchSize: 5,
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(dispatcher.calls) != 1 || shadowUUIDString(dispatcher.calls[0].issueID) != shadowUUIDString(ready) {
		t.Fatalf("dispatch calls = %+v, want exactly the ready suggestion", dispatcher.calls)
	}
	if len(result.Results) != 3 {
		t.Fatalf("results = %d, want 1 dispatched + 2 reported blocked", len(result.Results))
	}
	for _, row := range result.Results[1:] {
		if row.Outcome != WorkConservingDrainBlocked || row.Receipt != nil || !row.NotAttempted {
			t.Fatalf("blocked row must be report-only: %+v", row)
		}
		if row.Receiver == "" || row.WakeCondition == "" || row.Reason == "" {
			t.Fatalf("blocked row must carry reasons, receiver and wake condition: %+v", row)
		}
	}
	if result.Blocked != 2 || result.Dispatched != 1 || result.ProjectionState != WorkConservingProjectionBlocked {
		t.Fatalf("counters = %+v, projection state = %s", result, result.ProjectionState)
	}
}

func TestWorkConservingDrainFailsClosedOnProjectionSourceGap(t *testing.T) {
	now, clock := drainTestClock()
	workspaceID, projectID, actor := dispatchReceiptUUID(50), dispatchReceiptUUID(51), dispatchReceiptUUID(52)
	dispatcher := &drainDispatcherFixture{}

	projectorErr := &drainProjectorFixture{err: fmtWrap(ErrWorkConservingProjectionSourceGap)}
	result, err := NewWorkConservingDrainService(projectorErr, dispatcher).WithClock(clock).Drain(context.Background(), WorkConservingDrainRequest{
		WorkspaceID: workspaceID, ProjectID: projectID, ActorUserID: actor, BatchSize: 5,
	})
	if err == nil || !errors.Is(err, ErrWorkConservingProjectionSourceGap) {
		t.Fatalf("provider failure error = %v, want source gap", err)
	}
	if result.State != WorkConservingDrainStateSourceGap || len(result.Results) != 0 || len(dispatcher.calls) != 0 {
		t.Fatalf("source-gap result = %+v with %d dispatches, want fail-closed empty result", result, len(dispatcher.calls))
	}

	stale := drainProjectionFixture(workspaceID, projectID, "goal-drain", []continuousdispatch.WorkConservingSuggestion{
		drainSuggestion("goal-drain", dispatchReceiptUUID(53), "EMP-001", dispatchReceiptUUID(54), dispatchReceiptUUID(55)),
	}, nil, now)
	stale.Authority.ExpiresAt = now.Add(-time.Second).Format(time.RFC3339)
	projectorStale := &drainProjectorFixture{projection: stale}
	result, err = NewWorkConservingDrainService(projectorStale, dispatcher).WithClock(clock).Drain(context.Background(), WorkConservingDrainRequest{
		WorkspaceID: workspaceID, ProjectID: projectID, ActorUserID: actor, BatchSize: 5,
	})
	if err == nil || !errors.Is(err, ErrWorkConservingProjectionSourceGap) {
		t.Fatalf("stale projection error = %v, want source gap", err)
	}
	if result.State != WorkConservingDrainStateSourceGap || result.ReasonCode != "projection_validation_failed" || len(dispatcher.calls) != 0 {
		t.Fatalf("stale projection result = %+v with %d dispatches", result, len(dispatcher.calls))
	}
}

func TestWorkConservingDrainRejectsInvalidRequestsBeforeAnyRead(t *testing.T) {
	workspaceID, projectID, actor := dispatchReceiptUUID(60), dispatchReceiptUUID(61), dispatchReceiptUUID(62)
	projector := &drainProjectorFixture{}
	dispatcher := &drainDispatcherFixture{}
	drain := NewWorkConservingDrainService(projector, dispatcher)
	invalid := []WorkConservingDrainRequest{
		{WorkspaceID: workspaceID, ProjectID: projectID, ActorUserID: actor, BatchSize: 0},
		{WorkspaceID: workspaceID, ProjectID: projectID, ActorUserID: actor, BatchSize: workConservingDrainMaxBatch + 1},
		{WorkspaceID: workspaceID, ProjectID: projectID, ActorUserID: pgtype.UUID{}, BatchSize: 1},
		{WorkspaceID: pgtype.UUID{}, ProjectID: projectID, ActorUserID: actor, BatchSize: 1},
	}
	for i, req := range invalid {
		if _, err := drain.Drain(context.Background(), req); err == nil {
			t.Fatalf("invalid request %d was accepted", i)
		}
	}
	if len(projector.calls) != 0 || len(dispatcher.calls) != 0 {
		t.Fatalf("invalid requests reached reads/writes: projector=%d dispatcher=%d", len(projector.calls), len(dispatcher.calls))
	}
	if _, err := NewWorkConservingDrainService(nil, dispatcher).Drain(context.Background(), invalid[0]); !errors.Is(err, ErrWorkConservingDrainNotConfigured) {
		t.Fatalf("unconfigured drain error = %v", err)
	}
}

func TestWorkConservingDrainStopsAttemptingWhenContextCanceled(t *testing.T) {
	now, clock := drainTestClock()
	workspaceID, projectID, actor := dispatchReceiptUUID(70), dispatchReceiptUUID(71), dispatchReceiptUUID(72)
	suggestions := []continuousdispatch.WorkConservingSuggestion{
		drainSuggestion("goal-drain", dispatchReceiptUUID(73), "EMP-001", dispatchReceiptUUID(74), dispatchReceiptUUID(75)),
		drainSuggestion("goal-drain", dispatchReceiptUUID(76), "EMP-002", dispatchReceiptUUID(77), dispatchReceiptUUID(78)),
	}
	projector := &drainProjectorFixture{projection: drainProjectionFixture(workspaceID, projectID, "goal-drain", suggestions, nil, now)}
	dispatcher := &drainDispatcherFixture{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := NewWorkConservingDrainService(projector, dispatcher).WithClock(clock).Drain(ctx, WorkConservingDrainRequest{
		WorkspaceID: workspaceID, ProjectID: projectID, ActorUserID: actor, BatchSize: 5,
	})
	if err != nil {
		t.Fatalf("canceled context must still return a truthful result, got %v", err)
	}
	if len(dispatcher.calls) != 0 {
		t.Fatalf("canceled context dispatched %d tasks", len(dispatcher.calls))
	}
	if len(result.Results) != 2 || result.SourceGaps != 2 {
		t.Fatalf("results = %+v, want both suggestions reported as source gaps", result.Results)
	}
	for _, row := range result.Results {
		if !row.NotAttempted || row.Outcome != WorkConservingDrainSourceGap {
			t.Fatalf("canceled row must be not-attempted source gap: %+v", row)
		}
	}
}

// Composes the drain with the real trigger and the real exact dispatcher. The
// second pass replays the identical request (same identity, route, and
// handoff note) and must return the stored receipt: one Task, one receipt, one
// notification across two drains.
func TestWorkConservingDrainExactReplayThroughRealTriggerAndDispatcher(t *testing.T) {
	now, clock := drainTestClock()
	workspaceID, projectID, actor := dispatchReceiptUUID(80), dispatchReceiptUUID(81), dispatchReceiptUUID(82)
	issueID, agentID, runtimeID := dispatchReceiptUUID(83), dispatchReceiptUUID(84), dispatchReceiptUUID(85)
	suggestion := drainSuggestion("goal-drain", issueID, "EMP-001", agentID, runtimeID)
	projector := &drainProjectorFixture{projection: drainProjectionFixture(workspaceID, projectID, "goal-drain", []continuousdispatch.WorkConservingSuggestion{suggestion}, nil, now)}
	item := triggerShadowItem(workspaceID, issueID, agentID, runtimeID)
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: {
		SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID),
		ProjectID: shadowUUIDString(projectID), Items: []ContinuousDispatchShadowItem{item}, Total: 1,
	}}}
	backend := &fakeContinuousDispatchBackend{
		issue: db.Issue{
			ID: issueID, WorkspaceID: workspaceID, Status: "todo",
			Metadata: []byte(`{"stage":"implementation","candidate_revision":"candidate-trigger","generation":"generation-trigger-1"}`),
		},
		nextTaskID: dispatchReceiptUUID(86),
	}
	recorder := &drainRecordingExactDispatcher{inner: NewContinuousDispatchService(backend)}
	trigger := NewContinuousDispatchTriggerService(inspector, recorder)
	drain := NewWorkConservingDrainService(projector, trigger).WithClock(clock)
	req := WorkConservingDrainRequest{WorkspaceID: workspaceID, ProjectID: projectID, ActorUserID: actor, BatchSize: 5}

	first, err := drain.Drain(context.Background(), req)
	if err != nil {
		t.Fatalf("first drain: %v", err)
	}
	second, err := drain.Drain(context.Background(), req)
	if err != nil {
		t.Fatalf("second drain: %v", err)
	}
	for i, result := range []WorkConservingDrainResult{first, second} {
		if result.State != WorkConservingDrainStateReady || result.Dispatched != 1 || len(result.Results) != 1 {
			t.Fatalf("drain %d result = %+v, want one dispatched row", i+1, result)
		}
		receipt := result.Results[0].Receipt
		if receipt == nil || receipt.TaskID != backend.nextTaskID {
			t.Fatalf("drain %d receipt = %+v, want the committed task", i+1, receipt)
		}
		if receipt.Identity.IssueID != suggestion.IssueID {
			t.Fatalf("drain %d receipt identity = %+v", i+1, receipt.Identity)
		}
	}
	if first.Results[0].Receipt.RequestDigest != second.Results[0].Receipt.RequestDigest {
		t.Fatal("exact replay must return the stored receipt digest")
	}
	if len(recorder.requests) != 2 || recorder.requests[0] != recorder.requests[1] {
		t.Fatalf("exact dispatcher requests = %+v, want two byte-identical server-built requests", recorder.requests)
	}
	if backend.prepareN != 1 || backend.appendN != 1 || backend.notifyN != 1 {
		t.Fatalf("prepare/append/notify = %d/%d/%d, want 1/1/1 across two drains", backend.prepareN, backend.appendN, backend.notifyN)
	}
}
