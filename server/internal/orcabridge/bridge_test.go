package orcabridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClient records every Orca call and answers from a scriptable state.
type fakeClient struct {
	mu sync.Mutex

	runs     []OrcaRun
	tasks    map[string][]OrcaTask // run id -> tasks
	dispatch *OrcaDispatch
	inbox    []OrcaMessage

	runCreateErr    error
	runListErr      error
	taskCreateErr   error
	taskListErr     error
	dispatchShowErr error

	workerStartFunc func(input WorkerStartInput) (*WorkerReceipt, error)

	runCreates, taskCreates, workerStarts int
	lastWorkerInput                       WorkerStartInput
}

func newFakeClient() *fakeClient {
	return &fakeClient{tasks: map[string][]OrcaTask{}}
}

func (f *fakeClient) RunCreate(ctx context.Context, objective string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runCreates++
	if f.runCreateErr != nil {
		return "", f.runCreateErr
	}
	id := fmt.Sprintf("run_create%d", f.runCreates)
	f.runs = append(f.runs, OrcaRun{ID: id, Objective: objective})
	return id, nil
}

func (f *fakeClient) RunList(ctx context.Context) ([]OrcaRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]OrcaRun(nil), f.runs...), f.runListErr
}

func (f *fakeClient) TaskCreate(ctx context.Context, input TaskCreateInput) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.taskCreates++
	if f.taskCreateErr != nil {
		return "", f.taskCreateErr
	}
	id := fmt.Sprintf("task_create%d", f.taskCreates)
	f.tasks[input.RunID] = append(f.tasks[input.RunID], OrcaTask{ID: id, RunID: input.RunID, Spec: input.Spec, Title: input.Title})
	return id, nil
}

func (f *fakeClient) TaskList(ctx context.Context, runID string) ([]OrcaTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]OrcaTask(nil), f.tasks[runID]...), f.taskListErr
}

func (f *fakeClient) WorkerStart(ctx context.Context, input WorkerStartInput) (*WorkerReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workerStarts++
	f.lastWorkerInput = input
	if f.workerStartFunc != nil {
		return f.workerStartFunc(input)
	}
	return &WorkerReceipt{
		State:               "ready",
		Stage:               "agent_ready",
		DispatchID:          fmt.Sprintf("ctx_ws%d", f.workerStarts),
		TaskID:              input.TaskID,
		RunID:               input.RunID,
		AgentTerminalHandle: "term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e",
		WorktreeID:          "wtr_908bfe359364",
		WorktreePath:        "/tmp/worktrees/hivecrew-run",
	}, nil
}

func (f *fakeClient) DispatchShow(ctx context.Context, taskID string) (*OrcaDispatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dispatchShowErr != nil {
		return nil, f.dispatchShowErr
	}
	return f.dispatch, nil
}

func (f *fakeClient) InboxMessages(ctx context.Context) ([]OrcaMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]OrcaMessage(nil), f.inbox...), nil
}

// fakeEntry is a WorkEntryPort fake with the same replay/conflict semantics
// as the real kernel: same key + same payload replays; drift conflicts.
type fakeEntry struct {
	mu          sync.Mutex
	linkages    map[string]LinkageReceipt
	linkPayload map[string]string
	events      map[string]map[string]map[string]any // workRef -> key -> payload

	// failAppendKey, when set, makes AppendEvidence for that exact
	// idempotency key return failAppendErr (recovery coverage).
	failMu        sync.Mutex
	failAppendKey string
	failAppendErr error

	appendAttempts int
}

func (f *fakeEntry) injectAppendFailure(key string, err error) {
	f.failMu.Lock()
	defer f.failMu.Unlock()
	f.failAppendKey = key
	f.failAppendErr = err
}

func (f *fakeEntry) clearAppendFailure() {
	f.failMu.Lock()
	defer f.failMu.Unlock()
	f.failAppendKey = ""
	f.failAppendErr = nil
}

func newFakeEntry() *fakeEntry {
	return &fakeEntry{
		linkages:    map[string]LinkageReceipt{},
		linkPayload: map[string]string{},
		events:      map[string]map[string]map[string]any{},
	}
}

func linkageKey(in LinkageInput) string {
	switch in.MappingKind {
	case "run":
		return RunLinkageKey(in.Chain)
	case "task":
		return TaskLinkageKey(in.Chain)
	case "dispatch":
		return DispatchLinkageKey(in.Chain)
	}
	return in.MappingKind + "/" + in.Chain.ProjectID
}

func payloadDigest(payload map[string]any) string {
	digest, _ := CanonicalDigest(payload)
	return digest
}

func (f *fakeEntry) RegisterLinkage(ctx context.Context, in LinkageInput) (LinkageReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := linkageKey(in)
	digest := payloadDigest(in.Payload)
	if existing, ok := f.linkages[key]; ok {
		if f.linkPayload[key] != digest {
			return existing, ErrMappingConflict
		}
		return existing, nil
	}
	receipt := LinkageReceipt{WorkRef: "hivecrew://ws/work/prj-" + in.MappingKind, Replayed: false}
	f.linkages[key] = receipt
	f.linkPayload[key] = digest
	return receipt, nil
}

func (f *fakeEntry) AppendEvidence(ctx context.Context, in EvidenceInput) (EvidenceReceipt, error) {
	f.failMu.Lock()
	failing := in.IdempotencyKey != "" && in.IdempotencyKey == f.failAppendKey && f.failAppendErr != nil
	f.failMu.Unlock()
	f.mu.Lock()
	f.appendAttempts++
	f.mu.Unlock()
	if failing {
		return EvidenceReceipt{}, ErrEvidenceConflict
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	byKey, ok := f.events[in.WorkRef]
	if !ok {
		byKey = map[string]map[string]any{}
		f.events[in.WorkRef] = byKey
	}
	if existing, ok := byKey[in.IdempotencyKey]; ok {
		if payloadDigest(existing) != payloadDigest(in.Payload) {
			return EvidenceReceipt{}, ErrEvidenceConflict
		}
		return EvidenceReceipt{EventID: in.IdempotencyKey, Replayed: true}, nil
	}
	byKey[in.IdempotencyKey] = in.Payload
	return EvidenceReceipt{EventID: in.IdempotencyKey, Replayed: false}, nil
}

func (f *fakeEntry) LookupEvidence(ctx context.Context, workRef, key string) (EvidenceRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	payload, ok := f.events[workRef][key]
	if !ok {
		return EvidenceRecord{}, false, nil
	}
	return EvidenceRecord{EventID: key, IdempotencyKey: key, Payload: payload}, true, nil
}

// ClaimScope implements the port's atomic first-writer-wins claim with the
// same semantics as the production adapter: absent key -> acquired; same
// payload -> acquired (replay); different payload -> held with the parsed
// holder. One mutex arbitrates all claimants, exactly like the kernel's
// unique-key append does per (work_ref, idempotency_key).
func (f *fakeEntry) ClaimScope(ctx context.Context, in ScopeClaimInput) (ScopeClaimResult, error) {
	if in.WorkRef == "" || in.ClaimKey == "" || in.InstanceID == "" || in.SessionID == "" {
		return ScopeClaimResult{}, ErrInvalidChain
	}
	payload := map[string]any{
		"claim":       true,
		"instance_id": in.InstanceID,
		"generation":  in.Generation,
		"expires_at":  in.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	byKey, ok := f.events[in.WorkRef]
	if !ok {
		byKey = map[string]map[string]any{}
		f.events[in.WorkRef] = byKey
	}
	existing, ok := byKey[in.ClaimKey]
	if !ok {
		byKey[in.ClaimKey] = payload
		return ScopeClaimResult{Acquired: true}, nil
	}
	if payloadDigest(existing) == payloadDigest(payload) {
		return ScopeClaimResult{Acquired: true}, nil
	}
	return ScopeClaimResult{Acquired: false, Holder: parseScopeClaimHolder(existing)}, nil
}

// evidenceStored reports whether one evidence key exists on the work chain.
func (f *fakeEntry) evidenceStored(workRef, key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.events[workRef][key]
	return ok
}

// fakeAssignmentPort replays the existing dispatch entry: idempotent by
// command id, and like the real entry every new command derives a fresh
// initial task (run row) while the issue stays bound to its project.
type fakeAssignmentPort struct {
	mu        sync.Mutex
	committed map[string]AssignmentOutcome
	nextSeed  byte
	fail      error
}

func newFakeAssignmentPort() *fakeAssignmentPort {
	return &fakeAssignmentPort{committed: map[string]AssignmentOutcome{}, nextSeed: 0x40}
}

func (f *fakeAssignmentPort) DispatchAssignment(ctx context.Context, cmd AssignmentCommand) (AssignmentOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return AssignmentOutcome{}, f.fail
	}
	if existing, ok := f.committed[cmd.CommandID]; ok {
		return existing, nil
	}
	taskUUID := fmt.Sprintf("c05a0000-0000-4000-8000-%012x", f.nextSeed)
	f.nextSeed++
	outcome := AssignmentOutcome{
		CommandID:     cmd.CommandID,
		WorkspaceID:   cmd.WorkspaceID,
		IssueID:       firstNonEmpty(cmd.IssueID, "c05a0000-0000-4000-8000-000000000003"),
		InitialTaskID: taskUUID,
	}
	f.committed[cmd.CommandID] = outcome
	return outcome, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// fakeDaemonPort records lifecycle verbs in order.
type fakeDaemonPort struct {
	mu        sync.Mutex
	claimed   []*DaemonTask
	started   []string
	completed []TaskCompletion
	failed    []TaskFailure
	cancelAck []string
	claimTask *DaemonTask
	claimErr  error
	startErr  error
}

func (f *fakeDaemonPort) ClaimTask(ctx context.Context, runtimeID string) (*DaemonTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	return f.claimTask, nil
}

func (f *fakeDaemonPort) StartTask(ctx context.Context, taskID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	f.started = append(f.started, taskID)
	return nil
}

func (f *fakeDaemonPort) CompleteTask(ctx context.Context, c TaskCompletion) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, c)
	return nil
}

func (f *fakeDaemonPort) FailTask(ctx context.Context, failure TaskFailure) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, failure)
	return nil
}

func (f *fakeDaemonPort) AckTaskCancelled(ctx context.Context, taskID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelAck = append(f.cancelAck, taskID)
	return nil
}

func instructions() string { return "do the thing" }

func testPlacement() PlacementInput {
	return PlacementInput{
		WorktreeMode: "new-child",
		WorktreeName: "hivecrew-a1",
		RepoSelector: "path:/repos/hivecrew",
		Agent:        "codex",
		Model:        "gpt-5.6-terra",
	}
}

func newTestBridge(client *fakeClient, entry *fakeEntry) *testBridge {
	return &testBridge{
		Bridge:     NewBridge(client, entry, newFakeAssignmentPort(), &fakeDaemonPort{}, bridgeActor()),
		client:     client,
		entry:      entry,
		assignment: nil,
		daemon:     nil,
	}
}

// testBridge keeps the port fakes reachable alongside the bridge.
type testBridge struct {
	*Bridge
	client     *fakeClient
	entry      *fakeEntry
	assignment *fakeAssignmentPort
	daemon     *fakeDaemonPort
}

func TestEnsureProjectRunIsIdempotent(t *testing.T) {
	client, entry := newFakeClient(), newFakeEntry()
	bridge := newTestBridge(client, entry)
	chain := validChain()

	first, err := bridge.EnsureProjectRun(context.Background(), ProjectRef{Chain: chain, DisplayObjective: "WO-P2 objective"})
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if client.runCreates != 1 {
		t.Fatalf("expected exactly one run create, got %d", client.runCreates)
	}
	second, err := bridge.EnsureProjectRun(context.Background(), ProjectRef{Chain: chain, DisplayObjective: "WO-P2 objective"})
	if err != nil {
		t.Fatalf("replay ensure: %v", err)
	}
	if second != first || client.runCreates != 1 {
		t.Fatalf("replay created a second run: %s vs %s creates=%d", first, second, client.runCreates)
	}
	// Objective drift fails closed.
	if _, err := bridge.EnsureProjectRun(context.Background(), ProjectRef{Chain: chain, DisplayObjective: "drifted objective"}); !errors.Is(err, ErrMappingConflict) {
		t.Fatalf("expected ErrMappingConflict, got %v", err)
	}
}

func TestEnsureProjectRunReconcilesOrphanRunAfterCrash(t *testing.T) {
	client, entry := newFakeClient(), newFakeEntry()
	bridge := newTestBridge(client, entry)
	chain := validChain()

	// A previous process created the Orca Run but crashed before appending
	// the linkage evidence: the marker in Orca is the recovery truth.
	client.runs = []OrcaRun{{ID: "run_orphan1", Objective: ProjectRunObjective("WO-P2 objective", chain)}}

	runID, err := bridge.EnsureProjectRun(context.Background(), ProjectRef{Chain: chain, DisplayObjective: "WO-P2 objective"})
	if err != nil {
		t.Fatalf("reconcile ensure: %v", err)
	}
	if runID != "run_orphan1" || client.runCreates != 0 {
		t.Fatalf("expected orphan reuse, got %q creates=%d", runID, client.runCreates)
	}
}

func TestEnsureIssueTaskIsIdempotentAndNested(t *testing.T) {
	client, entry := newFakeClient(), newFakeEntry()
	bridge := newTestBridge(client, entry)
	chain := validChain()

	runID, taskID, err := bridge.EnsureIssueTask(context.Background(), TaskRef{Chain: chain, Instructions: instructions()})
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if runID == "" || taskID == "" {
		t.Fatalf("mapping incomplete: run=%s task=%s", runID, taskID)
	}
	runID2, taskID2, err := bridge.EnsureIssueTask(context.Background(), TaskRef{Chain: chain, Instructions: instructions()})
	if err != nil {
		t.Fatalf("replay ensure: %v", err)
	}
	if taskID2 != taskID || runID2 != runID || client.taskCreates != 1 || client.runCreates != 1 {
		t.Fatalf("replay created duplicates: run=%s/%s task=%s/%s creates=%d/%d", runID, runID2, taskID, taskID2, client.runCreates, client.taskCreates)
	}
	// Instruction drift fails closed.
	if _, _, err := bridge.EnsureIssueTask(context.Background(), TaskRef{Chain: chain, Instructions: "changed instructions"}); !errors.Is(err, ErrMappingConflict) {
		t.Fatalf("expected ErrMappingConflict, got %v", err)
	}
}

func TestEnsureIssueTaskReconcilesOrphanTask(t *testing.T) {
	client, entry := newFakeClient(), newFakeEntry()
	bridge := newTestBridge(client, entry)
	chain := validChain()

	client.runs = []OrcaRun{{ID: "run_orphan1", Objective: ProjectRunObjective("WO-P2 objective", chain)}}
	client.tasks["run_orphan1"] = []OrcaTask{{ID: "task_orphan1", RunID: "run_orphan1", Spec: TaskSpec(instructions(), chain)}}

	_, taskID, err := bridge.EnsureIssueTask(context.Background(), TaskRef{Chain: chain, Instructions: instructions()})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if taskID != "task_orphan1" || client.taskCreates != 0 {
		t.Fatalf("expected orphan task reuse: %s creates=%d", taskID, client.taskCreates)
	}
}

func dispatchRef(chain Chain) AssignmentRef {
	return AssignmentRef{
		Command: AssignmentCommand{
			CommandID:    chain.AssignmentID,
			WorkspaceID:  chain.WorkspaceID,
			ProjectID:    chain.ProjectID,
			LocalAgentID: "c05a0000-0000-4000-8000-000000000007",
			ActorUserID:  "c05a0000-0000-4000-8000-000000000008",
			HandoffNote:  "handoff",
		},
		Placement:    testPlacement(),
		Instructions: instructions(),
	}
}

// newAssignmentBridge wires the dispatch port fake and returns the bridge.
func newAssignmentBridge(t *testing.T) *testBridge {
	t.Helper()
	client, entry := newFakeClient(), newFakeEntry()
	assignment, daemon := newFakeAssignmentPort(), &fakeDaemonPort{}
	bridge := &testBridge{
		Bridge:     NewBridge(client, entry, assignment, daemon, bridgeActor()),
		client:     client,
		entry:      entry,
		assignment: assignment,
		daemon:     daemon,
	}
	return bridge
}

func TestEnsureAssignmentIsIdempotent(t *testing.T) {
	tb := newAssignmentBridge(t)
	chain := validChain()
	ref := dispatchRef(chain)

	first, err := tb.EnsureAssignment(t.Context(), ref)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if first.OrcaTaskID == "" || first.Chain.TaskID == "" || first.Chain.IssueID == "" {
		t.Fatalf("mapping incomplete: %+v", first)
	}
	second, err := tb.EnsureAssignment(t.Context(), ref)
	if err != nil {
		t.Fatalf("replay ensure: %v", err)
	}
	if second != first || tb.client.taskCreates != 1 || tb.client.runCreates != 1 {
		t.Fatalf("replay created duplicates: %+v vs %+v creates=%d/%d", first, second, tb.client.runCreates, tb.client.taskCreates)
	}
	// Placement drift fails closed.
	drifted := ref
	drifted.Placement.Agent = "claude"
	if _, err := tb.EnsureAssignment(t.Context(), drifted); !errors.Is(err, ErrMappingConflict) {
		t.Fatalf("expected ErrMappingConflict, got %v", err)
	}
	// A new assignment command (a new run attempt) dispatches a new task.
	retryChain := chain
	retryChain.AssignmentID = "c05a0000-0000-4000-8000-000000000006"
	retry, err := tb.EnsureAssignment(t.Context(), dispatchRef(retryChain))
	if err != nil {
		t.Fatalf("retry attempt: %v", err)
	}
	if retry.OrcaTaskID == first.OrcaTaskID || tb.client.taskCreates != 2 {
		t.Fatalf("retry must be a distinct orca task: %+v creates=%d", retry, tb.client.taskCreates)
	}
}

func TestEnsureAssignmentFailsClosedWhenDispatchEntryFails(t *testing.T) {
	tb := newAssignmentBridge(t)
	tb.assignment.fail = errors.New("authority rejected")
	if _, err := tb.EnsureAssignment(t.Context(), dispatchRef(validChain())); err == nil {
		t.Fatal("dispatch entry failure must fail closed")
	}
	// No Orca objects may exist after the failed dispatch.
	if tb.client.runCreates != 0 || tb.client.taskCreates != 0 {
		t.Fatalf("orca objects created after failed dispatch: runs=%d tasks=%d", tb.client.runCreates, tb.client.taskCreates)
	}
}

func TestRunClaimedTaskDispatchesWorkerAndStartsHiveCrewTask(t *testing.T) {
	tb := newAssignmentBridge(t)
	chain := validChain()
	mapping0, err := tb.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	claimed := DaemonTask{
		ID:          mapping0.Chain.TaskID,
		WorkspaceID: chain.WorkspaceID,
		IssueID:     mapping0.Chain.IssueID,
	}
	mapping, err := tb.RunClaimedTask(t.Context(), claimed)
	if err != nil {
		t.Fatalf("run claimed: %v", err)
	}
	if mapping.OrcaDispatchID == "" || mapping.WorkerTerminal == "" || mapping.WorktreeID == "" {
		t.Fatalf("dispatch mapping incomplete: %+v", mapping)
	}
	if tb.client.lastWorkerInput.WorktreeMode != "new-child" || tb.client.lastWorkerInput.WorktreeName != "hivecrew-a1" {
		t.Fatalf("worker placement drifted: %+v", tb.client.lastWorkerInput)
	}
	if len(tb.daemon.started) != 1 || tb.daemon.started[0] != mapping0.Chain.TaskID {
		t.Fatalf("hivecrew task not started once: %v", tb.daemon.started)
	}
	// Replay converges: no second worker, no second start.
	again, err := tb.RunClaimedTask(t.Context(), claimed)
	if err != nil {
		t.Fatalf("replay run: %v", err)
	}
	if again.OrcaDispatchID != mapping.OrcaDispatchID || tb.client.workerStarts != 1 || len(tb.daemon.started) != 1 {
		t.Fatalf("replay duplicated work: dispatch=%s/%s starts=%d started=%v",
			mapping.OrcaDispatchID, again.OrcaDispatchID, tb.client.workerStarts, tb.daemon.started)
	}
}

func TestRunClaimedTaskRejectsUnmanagedAndMismatched(t *testing.T) {
	tb := newAssignmentBridge(t)
	// Unmanaged task: no bridge linkage.
	unmanaged := DaemonTask{ID: "c05a0000-0000-4000-8000-0000000000aa"}
	if _, err := tb.RunClaimedTask(t.Context(), unmanaged); !errors.Is(err, ErrNotBridgeManaged) {
		t.Fatalf("expected ErrNotBridgeManaged, got %v", err)
	}
	if tb.client.workerStarts != 0 {
		t.Fatalf("unmanaged task started a worker: %d", tb.client.workerStarts)
	}
	mapping, err := tb.EnsureAssignment(t.Context(), dispatchRef(validChain()))
	if err != nil {
		t.Fatal(err)
	}
	// Cross-workspace claimed task must fail closed.
	hijacked := DaemonTask{
		ID:          mapping.Chain.TaskID,
		WorkspaceID: "c05a0000-0000-4000-8000-0000000000bb",
	}
	if _, err := tb.RunClaimedTask(t.Context(), hijacked); !errors.Is(err, ErrResultIdentityMismatch) {
		t.Fatalf("expected ErrResultIdentityMismatch, got %v", err)
	}
	// Cancellation acknowledgement mirrors the same guard.
	if err := tb.AcknowledgeCancellation(t.Context(), "c05a0000-0000-4000-8000-0000000000aa"); !errors.Is(err, ErrNotBridgeManaged) {
		t.Fatalf("expected ErrNotBridgeManaged for cancel ack, got %v", err)
	}
	if err := tb.AcknowledgeCancellation(t.Context(), mapping.Chain.TaskID); err != nil {
		t.Fatalf("managed cancel ack: %v", err)
	}
	if len(tb.daemon.cancelAck) != 1 {
		t.Fatalf("cancel ack not forwarded once: %v", tb.daemon.cancelAck)
	}
}

func TestRunClaimedTaskFailsClosedWhenNotReady(t *testing.T) {
	tb := newAssignmentBridge(t)
	mapping, err := tb.EnsureAssignment(t.Context(), dispatchRef(validChain()))
	if err != nil {
		t.Fatal(err)
	}
	tb.client.workerStartFunc = func(input WorkerStartInput) (*WorkerReceipt, error) {
		return &WorkerReceipt{State: "outcome_unknown", Stage: "agent_launch", DispatchID: "ctx_unknown1"}, nil
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: validChain().WorkspaceID, IssueID: mapping.Chain.IssueID}
	if _, err := tb.RunClaimedTask(t.Context(), claimed); err == nil {
		t.Fatal("non-ready worker start must fail closed")
	}
	// The HiveCrew task must not have been started for an unsettled worker.
	if len(tb.daemon.started) != 0 {
		t.Fatalf("unsettled worker still started the task: %v", tb.daemon.started)
	}
}

func TestRunClaimedTaskReconcilesOrphanDispatch(t *testing.T) {
	tb := newAssignmentBridge(t)
	chain := validChain()
	mapping, err := tb.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	// A previous worker-start created a dispatch but crashed before evidence.
	runEntry, ok := tb.memoRunEntry(Chain{WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID})
	if !ok {
		t.Fatal("expected memoized run")
	}
	tb.client.dispatch = &OrcaDispatch{
		ID:             "ctx_orphan1",
		RunID:          runEntry.id,
		TaskID:         mapping.OrcaTaskID,
		AssigneeHandle: "term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e",
		Status:         "dispatched",
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, IssueID: mapping.Chain.IssueID}
	resolved, err := tb.RunClaimedTask(t.Context(), claimed)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if resolved.OrcaDispatchID != "ctx_orphan1" || tb.client.workerStarts != 0 {
		t.Fatalf("expected orphan dispatch reuse: %+v starts=%d", resolved, tb.client.workerStarts)
	}
}

func TestRunClaimedTaskFailsClosedOnUnavailableReconcile(t *testing.T) {
	tb := newAssignmentBridge(t)
	mapping, err := tb.EnsureAssignment(t.Context(), dispatchRef(validChain()))
	if err != nil {
		t.Fatal(err)
	}
	// An unavailable dispatch-show means effects are unknown: creating a
	// second worker could double-dispatch, so the bridge must fail closed.
	tb.client.dispatchShowErr = fmt.Errorf("wrapped: %w", ErrOrcaUnavailable)
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: validChain().WorkspaceID, IssueID: mapping.Chain.IssueID}
	if _, err := tb.RunClaimedTask(t.Context(), claimed); err == nil {
		t.Fatal("expected fail-closed on unavailable reconcile")
	}
	if tb.client.workerStarts != 0 {
		t.Fatalf("bridge must not start a worker after unknown reconcile state, starts=%d", tb.client.workerStarts)
	}
}

func workerDoneMessage(mapping DispatchMap, outcome, body string, files []string) OrcaMessage {
	payload := fmt.Sprintf(
		`{"taskId":%q,"dispatchId":%q,"outcome":%q,"filesModified":%s,"reportPath":"/tmp/report.md"}`,
		mapping.OrcaTaskID, mapping.OrcaDispatchID, outcome, string(mustJSON(files)))
	return OrcaMessage{
		ID:         "msg_done000001",
		RunID:      mapping.OrcaRunID,
		FromHandle: mapping.WorkerTerminal,
		ToHandle:   "run:" + mapping.OrcaRunID,
		Type:       "worker_done",
		Subject:    "done",
		Body:       body,
		Payload:    payload,
	}
}

func TestAcceptWorkerResultWritesGovernedEvidence(t *testing.T) {
	tb := newAssignmentBridge(t)
	chain := validChain()

	mapping, err := tb.EnsureAssignment(context.Background(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := tb.RunClaimedTask(context.Background(), DaemonTask{
		ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, IssueID: mapping.Chain.IssueID,
	})
	if err != nil {
		t.Fatal(err)
	}
	message := workerDoneMessage(dispatch, "succeeded", "implemented the bridge", []string{"server/internal/orcabridge/bridge.go"})

	receipt, err := tb.AcceptWorkerResult(context.Background(), chain, message)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if receipt.WorkspaceID != chain.WorkspaceID || receipt.ProjectID != chain.ProjectID ||
		receipt.IssueID != chain.IssueID || receipt.TaskID != mapping.Chain.TaskID || receipt.AssignmentID != chain.AssignmentID {
		t.Fatalf("receipt lost HiveCrew lineage: %+v", receipt)
	}
	if receipt.Outcome != "succeeded" || receipt.ResultDigest == "" || len(receipt.FilesModified) != 1 {
		t.Fatalf("receipt content wrong: %+v", receipt)
	}
	// The finished evidence must exist on the work chain.
	workRef, err := tb.dispatchWorkRef(context.Background(), dispatch)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := tb.entry.LookupEvidence(context.Background(), workRef, ResultEvidenceKey(dispatch.OrcaDispatchID)); !ok {
		t.Fatal("finished evidence missing on the work chain")
	}
	// The HiveCrew task settled through the existing daemon lifecycle.
	if len(tb.daemon.completed) != 1 || tb.daemon.completed[0].TaskID != mapping.Chain.TaskID {
		t.Fatalf("hivecrew task not completed once: %+v", tb.daemon.completed)
	}

	// Exact replay is idempotent and returns the same receipt digest.
	replay, err := tb.AcceptWorkerResult(context.Background(), chain, message)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.ResultDigest != receipt.ResultDigest {
		t.Fatalf("replay must return the committed digest: %s vs %s", replay.ResultDigest, receipt.ResultDigest)
	}

	// Drifted replay fails closed.
	drifted := workerDoneMessage(dispatch, "succeeded", "different body", []string{"server/internal/orcabridge/bridge.go"})
	if _, err := tb.AcceptWorkerResult(context.Background(), chain, drifted); !errors.Is(err, ErrResultReceiptConflict) {
		t.Fatalf("expected ErrResultReceiptConflict, got %v", err)
	}
}

func TestAcceptWorkerResultAcrossProcessRestart(t *testing.T) {
	// A fresh bridge process (empty memo) must still resolve the dispatch
	// from work-chain evidence and accept the idempotent writeback.
	tb := newAssignmentBridge(t)
	chain := validChain()
	mapping, err := tb.EnsureAssignment(context.Background(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := tb.RunClaimedTask(context.Background(), DaemonTask{
		ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, IssueID: mapping.Chain.IssueID,
	})
	if err != nil {
		t.Fatal(err)
	}

	restarted := NewBridge(newFakeClient(), tb.entry, newFakeAssignmentPort(), &fakeDaemonPort{}, bridgeActor())
	resolved, err := restarted.ResolveDispatch(context.Background(), chain, dispatch.OrcaDispatchID)
	if err != nil {
		t.Fatalf("restart resolve: %v", err)
	}
	if resolved.AssignmentID != chain.AssignmentID || resolved.OrcaTaskID != dispatch.OrcaTaskID {
		t.Fatalf("resolved = %+v", resolved)
	}
	message := workerDoneMessage(dispatch, "succeeded", "done after restart", nil)
	if _, err := restarted.AcceptWorkerResult(context.Background(), chain, message); err != nil {
		t.Fatalf("accept after restart: %v", err)
	}
}

func TestAcceptWorkerResultRejectsUnmappedAndMismatched(t *testing.T) {
	tb := newAssignmentBridge(t)
	chain := validChain()
	mapping, err := tb.EnsureAssignment(context.Background(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := tb.RunClaimedTask(context.Background(), DaemonTask{
		ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, IssueID: mapping.Chain.IssueID,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Unmapped dispatch: worker_done for a dispatch the bridge never made.
	foreign := workerDoneMessage(dispatch, "succeeded", "body", nil)
	foreign.Payload = strings.Replace(foreign.Payload, dispatch.OrcaDispatchID, "ctx_ffffffffffff", 1)
	if _, err := tb.AcceptWorkerResult(context.Background(), chain, foreign); !errors.Is(err, ErrUnmappedDispatch) {
		t.Fatalf("expected ErrUnmappedDispatch, got %v", err)
	}

	// Task identity mismatch.
	mismatched := workerDoneMessage(dispatch, "succeeded", "body", nil)
	mismatched.Payload = strings.Replace(mismatched.Payload, dispatch.OrcaTaskID, "task_ffffffffffff", 1)
	if _, err := tb.AcceptWorkerResult(context.Background(), chain, mismatched); !errors.Is(err, ErrResultIdentityMismatch) {
		t.Fatalf("expected ErrResultIdentityMismatch, got %v", err)
	}

	// Wrong terminal: the result did not come from the mapped worker.
	hijacked := workerDoneMessage(dispatch, "succeeded", "body", nil)
	hijacked.FromHandle = "term_ffffffff-ffff-4fff-8fff-ffffffffffff"
	if _, err := tb.AcceptWorkerResult(context.Background(), chain, hijacked); !errors.Is(err, ErrResultIdentityMismatch) {
		t.Fatalf("expected ErrResultIdentityMismatch for terminal hijack, got %v", err)
	}

	// Non worker_done messages are rejected by contract.
	heartbeat := workerDoneMessage(dispatch, "succeeded", "body", nil)
	heartbeat.Type = "heartbeat"
	if _, err := tb.AcceptWorkerResult(context.Background(), chain, heartbeat); !errors.Is(err, ErrNotWorkerDone) {
		t.Fatalf("expected ErrNotWorkerDone, got %v", err)
	}

	// Invalid outcome.
	badOutcome := workerDoneMessage(dispatch, "maybe", "body", nil)
	if _, err := tb.AcceptWorkerResult(context.Background(), chain, badOutcome); !errors.Is(err, ErrInvalidWorkerResult) {
		t.Fatalf("expected ErrInvalidWorkerResult, got %v", err)
	}

	// A foreign project scope must not accept another project's dispatch.
	otherChain := chain
	otherChain.ProjectID = "c05a0000-0000-4000-8000-000000000009"
	if _, err := tb.AcceptWorkerResult(context.Background(), otherChain, workerDoneMessage(dispatch, "succeeded", "body", nil)); !errors.Is(err, ErrUnmappedDispatch) {
		t.Fatalf("expected ErrUnmappedDispatch for foreign project scope, got %v", err)
	}
}

func TestIngestWorkerResultsFiltersAndClassifies(t *testing.T) {
	tb := newAssignmentBridge(t)
	chain := validChain()
	mapping, err := tb.EnsureAssignment(context.Background(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := tb.RunClaimedTask(context.Background(), DaemonTask{
		ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, IssueID: mapping.Chain.IssueID,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := workerDoneMessage(dispatch, "failed", "could not finish", nil)
	tb.client.inbox = []OrcaMessage{
		{ID: "msg_heartbeat01", Type: "heartbeat", Payload: "{}"},
		done,
		{ID: "msg_foreign0001", Type: "worker_done", Payload: `{"taskId":"task_ffffffffffff","dispatchId":"ctx_ffffffffffff","outcome":"succeeded"}`},
	}
	accepted, rejected, err := tb.IngestWorkerResults(context.Background(), chain)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if len(accepted) != 1 || accepted[0].Outcome != "failed" || accepted[0].AssignmentID != chain.AssignmentID {
		t.Fatalf("accepted = %+v", accepted)
	}
	if len(tb.daemon.failed) != 1 || tb.daemon.failed[0].TaskID != mapping.Chain.TaskID {
		t.Fatalf("failed hivecrew task not settled once: %+v", tb.daemon.failed)
	}
	if len(rejected) != 1 || !errors.Is(rejected[0], ErrUnmappedDispatch) {
		t.Fatalf("rejected = %v", rejected)
	}
}

func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

// ---------------------------------------------------------------------------
// Finding 2: no implicit Issue creation
// ---------------------------------------------------------------------------

func TestEnsureProjectRunRequiresIssueAnchor(t *testing.T) {
	client, entry := newFakeClient(), newFakeEntry()
	tb := &testBridge{Bridge: NewBridge(client, entry, newFakeAssignmentPort(), &fakeDaemonPort{}, bridgeActor()), client: client, entry: entry}
	chain := validChain()
	chain.IssueID = "" // project/workspace only

	if _, err := tb.EnsureProjectRun(t.Context(), ProjectRef{Chain: chain, DisplayObjective: "objective"}); !errors.Is(err, ErrIssueAnchorRequired) {
		t.Fatalf("expected ErrIssueAnchorRequired for project-only scope, got %v", err)
	}
	// Zero Orca effects and zero linkage registrations may have happened.
	if client.runCreates != 0 || client.taskCreates != 0 {
		t.Fatalf("orca effects after anchor rejection: runs=%d tasks=%d", client.runCreates, client.taskCreates)
	}
	if len(entry.linkages) != 0 {
		t.Fatalf("linkage registered without anchor: %+v", entry.linkages)
	}
}

// ---------------------------------------------------------------------------
// Finding 1: bridge-level credential redaction end to end
// ---------------------------------------------------------------------------

func TestAcceptWorkerResultRedactsCredentialsEndToEnd(t *testing.T) {
	tb := newAssignmentBridge(t)
	chain := validChain()
	mapping, err := tb.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := tb.RunClaimedTask(t.Context(), DaemonTask{
		ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, IssueID: mapping.Chain.IssueID,
	})
	if err != nil {
		t.Fatal(err)
	}
	message := workerDoneMessage(dispatch, "succeeded",
		"used Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig plus sk-ant-api03-AAABBBCCCDDDEEE and password=hunter2hunter2hunter2",
		[]string{"server/internal/orcabridge/bridge.go"})
	message.ID = "msg_redact00001"

	receipt, err := tb.AcceptWorkerResult(t.Context(), chain, message)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	for _, leaked := range []string{"eyJhbGciOiJIUzI1NiJ9", "sk-ant-api03-AAABBBCCC", "hunter2hunter2hunter2"} {
		if strings.Contains(receipt.Body, leaked) {
			t.Fatalf("credential leaked into receipt body: %q", receipt.Body)
		}
	}
	// Stored evidence must be clean.
	workRef, err := tb.dispatchWorkRef(t.Context(), dispatch)
	if err != nil {
		t.Fatal(err)
	}
	stored, ok, _ := tb.entry.LookupEvidence(t.Context(), workRef, ResultEvidenceKey(dispatch.OrcaDispatchID))
	if !ok {
		t.Fatal("result evidence missing")
	}
	storedBody, _ := stored.Payload["body"].(string)
	for _, leaked := range []string{"eyJhbGciOiJIUzI1NiJ9", "sk-ant-api03-AAABBBCCC", "hunter2hunter2hunter2"} {
		if strings.Contains(storedBody, leaked) {
			t.Fatalf("credential leaked into stored evidence: %q", storedBody)
		}
	}
	// Daemon settlement output must be clean too.
	if len(tb.daemon.completed) != 1 {
		t.Fatalf("expected one daemon complete, got %d", len(tb.daemon.completed))
	}
	if strings.Contains(tb.daemon.completed[0].Output, "eyJhbGciOiJIUzI1NiJ9") {
		t.Fatalf("credential leaked into daemon output: %q", tb.daemon.completed[0].Output)
	}
	// Replay with the same dirty message digests identically (idempotent).
	if _, err := tb.AcceptWorkerResult(t.Context(), chain, message); err != nil {
		t.Fatalf("replay after redaction must be idempotent: %v", err)
	}
	if len(tb.daemon.completed) != 2 {
		t.Fatalf("replay must resettle through the daemon lifecycle: %d", len(tb.daemon.completed))
	}
}

// ---------------------------------------------------------------------------
// Finding 3: concurrent single-writer behavior
// ---------------------------------------------------------------------------

func TestEnsureAssignmentConcurrentSingleWriter(t *testing.T) {
	tb := newAssignmentBridge(t)
	chain := validChain()
	ref := dispatchRef(chain)
	const goroutines = 8

	results := make([]AssignmentMapping, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			<-start
			mapping, err := tb.EnsureAssignment(t.Context(), ref)
			results[slot], errs[slot] = mapping, err
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d failed: %v", i, err)
		}
		if results[i] != results[0] {
			t.Fatalf("goroutine %d mapping diverged: %+v vs %+v", i, results[i], results[0])
		}
	}
	if tb.client.runCreates != 1 || tb.client.taskCreates != 1 {
		t.Fatalf("concurrent ensure created duplicates: runs=%d tasks=%d", tb.client.runCreates, tb.client.taskCreates)
	}
}

func TestRunClaimedTaskConcurrentSingleWriter(t *testing.T) {
	tb := newAssignmentBridge(t)
	chain := validChain()
	mapping, err := tb.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, IssueID: mapping.Chain.IssueID}
	const goroutines = 8

	results := make([]DispatchMap, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			<-start
			resolved, err := tb.RunClaimedTask(t.Context(), claimed)
			results[slot], errs[slot] = resolved, err
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d failed: %v", i, err)
		}
		if results[i].OrcaDispatchID != results[0].OrcaDispatchID {
			t.Fatalf("goroutine %d dispatch diverged: %s vs %s", i, results[i].OrcaDispatchID, results[0].OrcaDispatchID)
		}
	}
	if tb.client.workerStarts != 1 {
		t.Fatalf("concurrent claim started %d workers, want exactly 1", tb.client.workerStarts)
	}
	if len(tb.daemon.started) != 1 {
		t.Fatalf("concurrent claim started hivecrew task %d times, want 1", len(tb.daemon.started))
	}
}

func TestAcceptWorkerResultConcurrentSingleWriter(t *testing.T) {
	tb := newAssignmentBridge(t)
	chain := validChain()
	mapping, err := tb.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := tb.RunClaimedTask(t.Context(), DaemonTask{
		ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, IssueID: mapping.Chain.IssueID,
	})
	if err != nil {
		t.Fatal(err)
	}
	message := workerDoneMessage(dispatch, "succeeded", "implemented", []string{"a.go"})
	const goroutines = 8

	receipts := make([]ResultReceipt, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			<-start
			receipt, err := tb.AcceptWorkerResult(t.Context(), chain, message)
			receipts[slot], errs[slot] = receipt, err
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d failed: %v", i, err)
		}
		if receipts[i].ResultDigest != receipts[0].ResultDigest {
			t.Fatalf("goroutine %d digest diverged: %s vs %s", i, receipts[i].ResultDigest, receipts[0].ResultDigest)
		}
	}
	// Exactly one settled completion: the daemon lifecycle ran once per
	// delivery attempt in order, but the evidence key landed exactly once.
	workRef, err := tb.dispatchWorkRef(t.Context(), dispatch)
	if err != nil {
		t.Fatal(err)
	}
	if !tb.entry.evidenceStored(workRef, ResultEvidenceKey(dispatch.OrcaDispatchID)) {
		t.Fatal("result evidence missing after concurrent writeback")
	}
	if len(tb.daemon.completed) != goroutines {
		t.Fatalf("expected one settlement per concurrent delivery (all on the same evidence), got %d", len(tb.daemon.completed))
	}
	for _, completion := range tb.daemon.completed {
		if completion.TaskID != mapping.Chain.TaskID {
			t.Fatalf("settlement hit the wrong task: %+v", completion)
		}
	}
}

// ---------------------------------------------------------------------------
// Finding 4: recovery after worker-start + StartTask succeed but evidence fails
// ---------------------------------------------------------------------------

func TestRunClaimedTaskRecoveryAfterEvidenceFailure(t *testing.T) {
	tb := newAssignmentBridge(t)
	chain := validChain()
	mapping, err := tb.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, IssueID: mapping.Chain.IssueID}

	// Inject an evidence append failure for the dispatch linkage key.
	linkageKey := DispatchLinkageKey(mapping.Chain)
	tb.entry.injectAppendFailure(linkageKey, errors.New("ledger unavailable"))

	first, err := tb.RunClaimedTask(t.Context(), claimed)
	if err == nil {
		t.Fatal("evidence failure must surface an error")
	}
	if first.OrcaDispatchID == "" {
		t.Fatalf("failed call must still return the committed mapping: %+v", first)
	}
	// Both real effects must have happened exactly once.
	if tb.client.workerStarts != 1 {
		t.Fatalf("worker starts after first attempt = %d, want 1", tb.client.workerStarts)
	}
	if len(tb.daemon.started) != 1 {
		t.Fatalf("hivecrew task starts after first attempt = %d, want 1", len(tb.daemon.started))
	}

	// Retry while the ledger is still failing: no second worker-start, no
	// second StartTask, and the committed mapping is returned.
	retry, err := tb.RunClaimedTask(t.Context(), claimed)
	if err == nil {
		t.Fatal("still-failing ledger must keep surfacing the error")
	}
	if retry.OrcaDispatchID != first.OrcaDispatchID {
		t.Fatalf("retry diverged: %+v vs %+v", retry, first)
	}
	if tb.client.workerStarts != 1 {
		t.Fatalf("retry started a second worker: %d", tb.client.workerStarts)
	}
	if len(tb.daemon.started) != 1 {
		t.Fatalf("retry restarted the hivecrew task: %d", len(tb.daemon.started))
	}

	// Ledger recovers: retry appends the evidence, still without a second
	// worker-start, and clears the pending state.
	tb.entry.clearAppendFailure()
	final, err := tb.RunClaimedTask(t.Context(), claimed)
	if err != nil {
		t.Fatalf("recovered retry: %v", err)
	}
	if final.OrcaDispatchID != first.OrcaDispatchID {
		t.Fatalf("recovered retry diverged: %+v vs %+v", final, first)
	}
	if tb.client.workerStarts != 1 {
		t.Fatalf("recovered retry started a second worker: %d", tb.client.workerStarts)
	}
	workRef, err := tb.dispatchWorkRef(t.Context(), final)
	if err != nil {
		t.Fatal(err)
	}
	if !tb.entry.evidenceStored(workRef, linkageKey) {
		t.Fatal("recovered retry did not append the dispatch evidence")
	}
	// A further replay is clean with no pending evidence re-attempt side effects.
	if _, err := tb.RunClaimedTask(t.Context(), claimed); err != nil {
		t.Fatalf("post-recovery replay: %v", err)
	}
	if tb.client.workerStarts != 1 || len(tb.daemon.started) != 1 {
		t.Fatalf("post-recovery replay duplicated effects: workers=%d starts=%d", tb.client.workerStarts, len(tb.daemon.started))
	}
}

// ---------------------------------------------------------------------------
// R3: cross-instance coordination (two independent Bridge objects sharing
// the same Orca client and WorkEntry ledger)
// ---------------------------------------------------------------------------

// twinBridges builds two independent Bridge instances over one shared Orca
// client, one shared WorkEntry ledger, and one shared dispatch entry, with
// tight claim timing so barrier tests stay fast and deterministic.
func twinBridges(t *testing.T) (*testBridge, *testBridge) {
	t.Helper()
	client, entry := newFakeClient(), newFakeEntry()
	assignment, daemon := newFakeAssignmentPort(), &fakeDaemonPort{}
	actorA := bridgeActor()
	actorB := bridgeActor()
	actorB.SessionID = "bridge-session-2"
	mk := func(actor ActorIdentity) *testBridge {
		bridge := NewBridge(client, entry, assignment, daemon, actor)
		bridge.InstanceID = "bridge-" + actor.SessionID
		bridge.ClaimPoll = time.Millisecond
		bridge.ClaimMaxWait = 2 * time.Second
		bridge.LeaseTTL = 5 * time.Second
		return &testBridge{Bridge: bridge, client: client, entry: entry, assignment: assignment, daemon: daemon}
	}
	return mk(actorA), mk(actorB)
}

// runBarrier launches fn on both bridges behind one start barrier and
// returns both results.
func runBarrier[A any](t *testing.T, a, b *testBridge, fn func(*testBridge) (A, error)) (A, A, error, error) {
	t.Helper()
	type outcome struct {
		value A
		err   error
	}
	outcomes := make([]outcome, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for slot, tb := range []*testBridge{a, b} {
		wg.Add(1)
		go func(slot int, tb *testBridge) {
			defer wg.Done()
			<-start
			value, err := fn(tb)
			outcomes[slot] = outcome{value: value, err: err}
		}(slot, tb)
	}
	close(start)
	wg.Wait()
	return outcomes[0].value, outcomes[1].value, outcomes[0].err, outcomes[1].err
}

func TestTwoBridgesOneRun(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	ref := ProjectRef{Chain: chain, DisplayObjective: "shared objective"}

	runA, runB, errA, errB := runBarrier(t, a, b, func(tb *testBridge) (string, error) {
		return tb.EnsureProjectRun(t.Context(), ref)
	})
	if errA != nil || errB != nil {
		t.Fatalf("bridge errors: %v / %v", errA, errB)
	}
	if runA != runB {
		t.Fatalf("two bridges observed different runs: %s vs %s", runA, runB)
	}
	if a.client.runCreates != 1 {
		t.Fatalf("two bridges created %d runs, want exactly 1", a.client.runCreates)
	}
	// Replays after both committed still converge without new creates.
	again, err := b.EnsureProjectRun(t.Context(), ref)
	if err != nil || again != runA {
		t.Fatalf("post-barrier replay: %s vs %s (err %v)", again, runA, err)
	}
	if a.client.runCreates != 1 {
		t.Fatalf("replay created another run: %d", a.client.runCreates)
	}
}

func TestTwoBridgesOneTask(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	ref := TaskRef{Chain: chain, Instructions: instructions()}

	type mapping struct {
		run, task string
	}
	mapA, mapB, errA, errB := runBarrier(t, a, b, func(tb *testBridge) (mapping, error) {
		run, task, err := tb.EnsureIssueTask(t.Context(), ref)
		return mapping{run: run, task: task}, err
	})
	if errA != nil || errB != nil {
		t.Fatalf("bridge errors: %v / %v", errA, errB)
	}
	if mapA != mapB {
		t.Fatalf("two bridges observed different mappings: %+v vs %+v", mapA, mapB)
	}
	if a.client.taskCreates != 1 || a.client.runCreates != 1 {
		t.Fatalf("creates: runs=%d tasks=%d, want 1/1", a.client.runCreates, a.client.taskCreates)
	}
}

func TestTwoBridgesOneWorker(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	// Both bridges register the assignment (idempotent through the shared
	// dispatch entry and work chain) so both can resolve the claimed task;
	// the worker-start race is what the claim must arbitrate.
	mapping, err := a.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.EnsureAssignment(t.Context(), dispatchRef(chain)); err != nil {
		t.Fatalf("second bridge assignment replay: %v", err)
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, IssueID: mapping.Chain.IssueID}

	dispatchA, dispatchB, errA, errB := runBarrier(t, a, b, func(tb *testBridge) (DispatchMap, error) {
		return tb.RunClaimedTask(t.Context(), claimed)
	})
	if errA != nil || errB != nil {
		t.Fatalf("bridge errors: %v / %v", errA, errB)
	}
	if dispatchA.OrcaDispatchID != dispatchB.OrcaDispatchID {
		t.Fatalf("two bridges observed different dispatches: %s vs %s", dispatchA.OrcaDispatchID, dispatchB.OrcaDispatchID)
	}
	if a.client.workerStarts != 1 {
		t.Fatalf("two bridges started %d workers, want exactly 1", a.client.workerStarts)
	}
	if len(a.daemon.started) != 1 {
		t.Fatalf("hivecrew task started %d times across bridges, want 1", len(a.daemon.started))
	}
	if a.client.runCreates != 1 || a.client.taskCreates != 1 {
		t.Fatalf("creates under contention: runs=%d tasks=%d, want 1/1", a.client.runCreates, a.client.taskCreates)
	}
}

// Restart path: a fresh Bridge instance (empty memo, new session) must
// observe the committed mapping and never re-create Orca objects.
func TestSecondBridgeRestartNeverRecreates(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	runA, err := a.EnsureProjectRun(t.Context(), ProjectRef{Chain: chain, DisplayObjective: "restart objective"})
	if err != nil {
		t.Fatal(err)
	}
	// Bridge B is a "restarted" instance: fresh memo, different session.
	runB, err := b.EnsureProjectRun(t.Context(), ProjectRef{Chain: chain, DisplayObjective: "restart objective"})
	if err != nil {
		t.Fatalf("restart bridge: %v", err)
	}
	if runB != runA {
		t.Fatalf("restart bridge saw a different run: %s vs %s", runB, runA)
	}
	if a.client.runCreates != 1 {
		t.Fatalf("restart path created %d runs, want 1", a.client.runCreates)
	}
}

// Lease takeover: a holder that crashed between claiming and creating (no
// evidence, no Orca object) must be taken over after lease expiry so exactly
// one RunCreate still happens across both instances.
func TestLeaseExpiryTakeoverAfterCrashedHolder(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	// Bridge A claims with a short lease, then dies before creating.
	a.LeaseTTL = 5 * time.Millisecond
	a.client.runCreateErr = errors.New("crashed after claim")
	if _, err := a.EnsureProjectRun(t.Context(), ProjectRef{Chain: chain, DisplayObjective: "takeover objective"}); err == nil {
		t.Fatal("crashed holder should surface its create error")
	}
	a.client.runCreateErr = nil
	// Short lease so B's acquire observes expiry quickly.
	b.LeaseTTL = 5 * time.Millisecond
	b.ClaimPoll = time.Millisecond
	b.ClaimMaxWait = 2 * time.Second

	runB, err := b.EnsureProjectRun(t.Context(), ProjectRef{Chain: chain, DisplayObjective: "takeover objective"})
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if runB == "" {
		t.Fatal("takeover returned no run")
	}
	// The fake counter counts attempts: A's failed attempt plus exactly one
	// successful create by the takeover winner.
	if len(a.client.runs) != 1 || a.client.runs[0].ID != runB {
		t.Fatalf("takeover left %d runs (%v), want exactly %s", len(a.client.runs), a.client.runs, runB)
	}
	if a.client.runCreates != 2 {
		t.Fatalf("takeover attempts = %d, want A(failed)+B(success)=2", a.client.runCreates)
	}
}

// Live lease: while the holder's lease is valid and nothing is committed, a
// peer fails closed instead of creating a duplicate.
func TestLiveLeaseFailsClosed(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	// A claims then crashes (create error), leaving a live long lease.
	a.client.runCreateErr = errors.New("holder crashed")
	if _, err := a.EnsureProjectRun(t.Context(), ProjectRef{Chain: chain, DisplayObjective: "held objective"}); err == nil {
		t.Fatal("crashed holder should error")
	}
	a.client.runCreateErr = nil
	// B: long lease, short max wait -> must fail closed with ErrScopeHeld.
	b.LeaseTTL = 5 * time.Second
	b.ClaimPoll = time.Millisecond
	b.ClaimMaxWait = 30 * time.Millisecond

	if _, err := b.EnsureProjectRun(t.Context(), ProjectRef{Chain: chain, DisplayObjective: "held objective"}); !errors.Is(err, ErrScopeHeld) {
		t.Fatalf("expected ErrScopeHeld, got %v", err)
	}
	if len(a.client.runs) != 0 {
		t.Fatalf("fail-closed path still produced %d run objects", len(a.client.runs))
	}
}
