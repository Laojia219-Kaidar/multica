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

// opLog records the ordered sequence of coordination/side-effect operations
// so tests can assert the claim -> barrier -> client -> marker order.
type opLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *opLog) record(op string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, op)
}

func (l *opLog) snapshot() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.entries...)
}

// indexOf returns the position of the first entry equal to op, or -1.
func (l *opLog) indexOf(op string) int {
	for i, entry := range l.snapshot() {
		if entry == op {
			return i
		}
	}
	return -1
}

// fakeClient records every Orca call and answers from a scriptable state.
type fakeClient struct {
	order *opLog
	mu    sync.Mutex

	runs  []OrcaRun
	tasks map[string][]OrcaTask // run id -> tasks
	// dispatch is the single shared orphan (legacy scenarios).
	dispatch *OrcaDispatch
	// dispatchByTask is a task-keyed, mutex-guarded dispatch table for
	// concurrent scenarios; when non-empty it wins over the single dispatch.
	dispatchByTask map[string]*OrcaDispatch
	inbox          []OrcaMessage

	runCreateErr     error
	runCreateBlock   chan struct{} // when set, RunCreate blocks until closed
	runListErr       error
	taskCreateErr    error
	taskCreateBlock  chan struct{} // when set, TaskCreate blocks until closed
	taskListErr      error
	dispatchShowErr  error
	workerStartBlock chan struct{} // when set, WorkerStart blocks until closed

	workerStartFunc func(input WorkerStartInput) (*WorkerReceipt, error)

	runCreates, taskCreates, workerStarts int
	lastWorkerInput                       WorkerStartInput
}

func newFakeClient() *fakeClient {
	return &fakeClient{tasks: map[string][]OrcaTask{}, dispatchByTask: map[string]*OrcaDispatch{}, order: &opLog{}}
}

func (f *fakeClient) RunCreate(ctx context.Context, objective string) (string, error) {
	f.order.record("client:RunCreate")
	f.mu.Lock()
	f.runCreates++
	// Slow side effect support: block until released (or the context ends),
	// simulating an Orca CLI call slower than the claim lease TTL.
	if f.runCreateBlock != nil {
		block := f.runCreateBlock
		f.mu.Unlock()
		select {
		case <-block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		f.mu.Lock()
		defer f.mu.Unlock()
	} else {
		defer f.mu.Unlock()
	}
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
	f.order.record("client:TaskCreate")
	f.mu.Lock()
	f.taskCreates++
	if f.taskCreateBlock != nil {
		block := f.taskCreateBlock
		f.mu.Unlock()
		select {
		case <-block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		f.mu.Lock()
		defer f.mu.Unlock()
	} else {
		defer f.mu.Unlock()
	}
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
	f.order.record("client:WorkerStart")
	f.mu.Lock()
	f.workerStarts++
	if f.workerStartBlock != nil {
		block := f.workerStartBlock
		f.mu.Unlock()
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		f.mu.Lock()
		defer f.mu.Unlock()
	} else {
		defer f.mu.Unlock()
	}
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
	if keyed, ok := f.dispatchByTask[taskID]; ok {
		return keyed, nil
	}
	return f.dispatch, nil
}

// setDispatchForTask installs a task-keyed orphan under the client mutex.
func (f *fakeClient) setDispatchForTask(taskID string, dispatch *OrcaDispatch) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatchByTask[taskID] = dispatch
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
	order       *opLog                               // shared with the client fake

	// blockClaimKey, when set, parks the ClaimScope CAS for that exact key
	// BEFORE the atomic section, letting a test pause a bridge between
	// winning a claim and winning (or losing) the effect barrier.
	blockClaimMu   sync.Mutex
	blockClaimKey  string
	blockClaimOnce chan struct{}

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

func (f *fakeEntry) setClaimBlock(key string) chan struct{} {
	release := make(chan struct{})
	f.blockClaimMu.Lock()
	defer f.blockClaimMu.Unlock()
	f.blockClaimKey = key
	f.blockClaimOnce = release
	return release
}

func (f *fakeEntry) clearClaimBlock() {
	f.blockClaimMu.Lock()
	defer f.blockClaimMu.Unlock()
	f.blockClaimKey = ""
	f.blockClaimOnce = nil
}

func newFakeEntry() *fakeEntry {
	return &fakeEntry{order: &opLog{},
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
	f.order.record("evidence:" + in.IdempotencyKey)
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
	// Pre-atomic pause hook: park before the CAS for the configured key.
	f.blockClaimMu.Lock()
	block := chan struct{}(nil)
	if in.ClaimKey == f.blockClaimKey && f.blockClaimOnce != nil {
		block = f.blockClaimOnce
		f.blockClaimOnce = nil // park exactly once
		f.blockClaimKey = ""
	}
	f.blockClaimMu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ScopeClaimResult{}, ctx.Err()
		}
	}
	f.order.record("claim:" + in.ClaimKey)
	attemptAt := in.AttemptAt
	if attemptAt.IsZero() {
		attemptAt = time.Now()
	}
	attemptStamp := attemptAt.UTC().Format(time.RFC3339Nano)
	payload := map[string]any{
		"claim":       true,
		"instance_id": in.InstanceID,
		"generation":  in.Generation,
		"expires_at":  in.ExpiresAt.UTC().Format(time.RFC3339Nano),
		"attempt_at":  attemptStamp,
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

// injectClaimWin pre-wins a claim CAS for key on behalf of anotherBridge so
// a test can force the waiter path deterministically. When expired is true
// the injected lease is already expired, letting the caller take over the
// claim generation while the barrier epoch stays held by the other bridge.
func (f *fakeEntry) injectClaimWin(key, otherBridge string, expired bool) error {
	lease := time.Now().Add(time.Hour)
	if expired {
		lease = time.Now().Add(-time.Hour)
	}
	payload := map[string]any{
		"claim":       true,
		"instance_id": otherBridge + "#0#1",
		"generation":  0,
		"expires_at":  lease.UTC().Format(time.RFC3339Nano),
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	byKey, ok := f.events["hivecrew://ws/work/prj-forced"]
	if !ok {
		byKey = map[string]map[string]any{}
		f.events["hivecrew://ws/work/prj-forced"] = byKey
	}
	byKey[key+"#0"] = payload
	// The barrier keys are looked up per linkage work ref; store under every
	// known work ref for determinism.
	for workRef := range f.events {
		f.events[workRef][key+"#0"] = payload
	}
	return nil
}

// injectBarrierHeld pre-wins an effect-barrier CAS for the exact barrier
// key on behalf of another bridge, forcing the waiter path deterministically.
func (f *fakeEntry) injectBarrierHeld(barrierKey, otherBridge string) error {
	payload := map[string]any{
		"claim":       true,
		"instance_id": otherBridge + "#0#1",
		"generation":  0,
		"expires_at":  time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for workRef := range f.events {
		f.events[workRef][barrierKey] = payload
	}
	return nil
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

	// startBlock, when set, holds every StartTask call until closed.
	startBlock chan struct{}
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
	// Slow-start support: block until released so tests can hold the verb
	// in flight deterministically.
	f.mu.Lock()
	block := f.startBlock
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Record every attempt (failed or not) so recovery tests can assert
	// retry counts: "failed + succeeded" must be observable.
	f.started = append(f.started, taskID)
	if f.startErr != nil {
		return f.startErr
	}
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

// order exposes the shared operation log of the wired fakes.
func (tb *testBridge) order() *opLog {
	if tb == nil || tb.client == nil {
		return nil
	}
	return tb.client.order
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
	// The orphan objective must be exactly what this caller would build
	// (R6: marker adoption validates the objective), so use the same empty
	// display objective the TaskRef path derives.
	client.runs = []OrcaRun{{ID: "run_orphan1", Objective: ProjectRunObjective("", chain)}}
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
	// R7 ordering: dispatch evidence is committed BEFORE StartTask, so a
	// failing evidence append aborts before any start attempt.
	if len(tb.daemon.started) != 0 {
		t.Fatalf("hivecrew task starts after first attempt = %d, want 0 (evidence failed first)", len(tb.daemon.started))
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
	if len(tb.daemon.started) != 0 {
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
	// The recovered retry starts the task exactly once.
	if len(tb.daemon.started) != 1 {
		t.Fatalf("recovered retry start attempts = %d, want 1", len(tb.daemon.started))
	}
	// A further replay is clean with no new effects.
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
	entry.order = client.order // one shared operation log
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
// R5 contract: a post-call client error — including a structured CLIError —
// is an unknown outcome. The effect barrier stays held, so a peer past TTL
// gets ErrScopeAttemptInFlight and creates nothing.
func TestTakeoverBlockedAfterPostCallCLIError(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	a.LeaseTTL = 5 * time.Millisecond
	a.client.runCreateErr = &CLIError{Code: "conflict", Message: "objective rejected"}
	if _, err := a.EnsureProjectRun(t.Context(), ProjectRef{Chain: chain, DisplayObjective: "post-call objective"}); err == nil {
		t.Fatal("failed holder should surface its create error")
	}
	a.client.runCreateErr = nil
	b.LeaseTTL = 5 * time.Millisecond
	b.ClaimPoll = time.Millisecond
	b.ClaimMaxWait = 2 * time.Second

	if _, err := b.EnsureProjectRun(t.Context(), ProjectRef{Chain: chain, DisplayObjective: "post-call objective"}); !errors.Is(err, ErrScopeAttemptInFlight) {
		t.Fatalf("expected ErrScopeAttemptInFlight after post-call CLI error, got %v", err)
	}
	// No Run object exists and only A's single attempt was made.
	if len(a.client.runs) != 0 || a.client.runCreates != 1 {
		t.Fatalf("blocked takeover still touched the client: objects=%d attempts=%d", len(a.client.runs), a.client.runCreates)
	}
}

// R5 contract: a provably pre-call failure (lease gate, which runs strictly
// before the client call) reopens the barrier, so a peer may take over and
// create exactly once. A is parked inside the barrier CAS; its clock is then
// advanced past the lease, so after winning the barrier its lease gate fails
// and it reopens without ever calling the client.
func TestPreCallLeaseExpiryReopensBarrierForTakeover(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	base := runCreateClaimBase(chain)
	lease := 100 * time.Millisecond

	clock := &slowClock{now: time.Now().Add(-1 * time.Hour)}
	a.Now = clock.Now
	a.LeaseTTL = lease
	releaseA := a.entry.setClaimBlock(effectBarrierKey(base, 0))

	errA := make(chan error, 1)
	go func() {
		_, err := a.EnsureProjectRun(context.Background(), ProjectRef{Chain: chain, DisplayObjective: "pre-call objective"})
		errA <- err
	}()
	// A has won the claim and is parked inside the barrier CAS (its claim is
	// already expired in real time because of the frozen past clock).
	barrierDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(barrierDeadline) {
		if a.order().indexOf("claim:"+claimKeyFor(base, 0)) >= 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// Advance A's clock past its lease, then let it win the barrier.
	clock.advance(lease * 2)
	close(releaseA)
	if err := <-errA; !errors.Is(err, ErrClaimLeaseExpired) {
		t.Fatalf("expected ErrClaimLeaseExpired from the pre-call gate, got %v", err)
	}
	if a.order().indexOf("client:RunCreate") >= 0 {
		t.Fatal("A must not have called the client after its lease expired")
	}

	// B takes over: claim g1, barrier epoch 1 (epoch 0 was reopened), create.
	b.ClaimPoll = time.Millisecond
	b.ClaimMaxWait = 2 * time.Second
	runB, err := b.EnsureProjectRun(context.Background(), ProjectRef{Chain: chain, DisplayObjective: "pre-call objective"})
	if err != nil {
		t.Fatalf("takeover after pre-call reopen: %v", err)
	}
	if len(a.client.runs) != 1 || a.client.runs[0].ID != runB {
		t.Fatalf("want exactly B's run %s, got %+v", runB, a.client.runs)
	}
	if a.client.runCreates != 1 {
		t.Fatalf("RunCreate attempts = %d, want exactly 1", a.client.runCreates)
	}
	// Ordering for B: claim(g1) -> barrier(epoch1) -> client -> marker.
	claimIdx := a.order().indexOf("claim:" + claimKeyFor(base, 1))
	barrierIdx := a.order().indexOf("claim:" + effectBarrierKey(base, 1))
	clientIdx := a.order().indexOf("client:RunCreate")
	markerIdx := a.order().indexOf("evidence:" + RunLinkageKey(chain))
	if !(claimIdx >= 0 && barrierIdx > claimIdx && clientIdx > barrierIdx && markerIdx > clientIdx) {
		t.Fatalf("order wrong (claim=%d barrier=%d client=%d marker=%d)", claimIdx, barrierIdx, clientIdx, markerIdx)
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

// ---------------------------------------------------------------------------
// R4: slow side effect > lease TTL must not duplicate creates
// ---------------------------------------------------------------------------

// slowClock is a Bridge clock whose time only advances when advanced
// explicitly, so a test can make the lease expire deterministically while a
// create call is logically "in flight".
type slowClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *slowClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *slowClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Scenario S1 (run): A is paused before winning the effect barrier (parked
// inside the barrier CAS with an expired claim); B takes over and wins the
// barrier; when A resumes it must NOT call RunCreate.
func TestBarrierScenarioAPausedBBWinsANeverCalls(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	base := runCreateClaimBase(chain)
	ref := ProjectRef{Chain: chain, DisplayObjective: "s1 run objective"}

	// A's frozen past clock makes its claim already expired in real time.
	clock := &slowClock{now: time.Now().Add(-1 * time.Hour)}
	a.Now = clock.Now
	a.LeaseTTL = 100 * time.Millisecond
	releaseA := a.entry.setClaimBlock(effectBarrierKey(base, 0))

	errA := make(chan error, 1)
	go func() {
		_, err := a.EnsureProjectRun(context.Background(), ref)
		errA <- err
	}()
	// Deterministic barrier: A has won the claim and is parked in the
	// barrier CAS (it has NOT won the permit yet).
	parkDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(parkDeadline) {
		if a.order().indexOf("claim:"+claimKeyFor(base, 0)) >= 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// B takes over fully while A is parked: claim g1, barrier epoch 0,
	// client, marker.
	b.ClaimPoll = time.Millisecond
	b.ClaimMaxWait = 2 * time.Second
	runB, err := b.EnsureProjectRun(context.Background(), ref)
	if err != nil {
		t.Fatalf("bridge B takeover: %v", err)
	}

	// Now release A: it loses the barrier CAS, waits bounded, and converges
	// on B's committed run — never calling the client itself. (If nothing
	// were committed it would fail with ErrScopeAttemptInFlight; either way
	// no second create.)
	close(releaseA)
	if err := <-errA; err != nil && !errors.Is(err, ErrScopeAttemptInFlight) {
		t.Fatalf("unexpected error for A after losing the barrier: %v", err)
	}
	if a.client.runCreates != 1 {
		t.Fatalf("RunCreate attempts = %d, want exactly B's single call", a.client.runCreates)
	}
	if len(a.client.runs) != 1 || a.client.runs[0].ID != runB {
		t.Fatalf("want exactly B's run %s, got %+v", runB, a.client.runs)
	}
	// B's order: claim(g1) -> barrier(0) -> client -> marker.
	claimIdx := a.order().indexOf("claim:" + claimKeyFor(base, 1))
	barrierIdx := a.order().indexOf("claim:" + effectBarrierKey(base, 0))
	clientIdx := a.order().indexOf("client:RunCreate")
	markerIdx := a.order().indexOf("evidence:" + RunLinkageKey(chain))
	if !(claimIdx >= 0 && barrierIdx > claimIdx && clientIdx > barrierIdx && markerIdx > clientIdx) {
		t.Fatalf("B order wrong (claim=%d barrier=%d client=%d marker=%d)", claimIdx, barrierIdx, clientIdx, markerIdx)
	}
}

// Scenario S1 (task): same barrier race for TaskCreate.
func TestBarrierScenarioTaskAPausedBBWins(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	base := taskCreateClaimBase(chain)
	ref := TaskRef{Chain: chain, Instructions: instructions()}

	clock := &slowClock{now: time.Now().Add(-1 * time.Hour)}
	a.Now = clock.Now
	a.LeaseTTL = 100 * time.Millisecond
	releaseA := a.entry.setClaimBlock(effectBarrierKey(base, 0))

	errA := make(chan error, 1)
	go func() {
		_, _, err := a.EnsureIssueTask(context.Background(), ref)
		errA <- err
	}()
	parkDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(parkDeadline) {
		if a.order().indexOf("claim:"+claimKeyFor(base, 0)) >= 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	b.ClaimPoll = time.Millisecond
	b.ClaimMaxWait = 2 * time.Second
	_, taskB, err := b.EnsureIssueTask(context.Background(), ref)
	if err != nil {
		t.Fatalf("bridge B takeover: %v", err)
	}

	close(releaseA)
	if err := <-errA; err != nil && !errors.Is(err, ErrScopeAttemptInFlight) {
		t.Fatalf("unexpected error for A after losing the barrier: %v", err)
	}
	if a.client.taskCreates != 1 {
		t.Fatalf("TaskCreate attempts = %d, want exactly B's single call", a.client.taskCreates)
	}
	totalTasks := 0
	for _, tasks := range a.client.tasks {
		totalTasks += len(tasks)
	}
	if totalTasks != 1 || taskB == "" {
		t.Fatalf("want exactly B's task, got %d tasks", totalTasks)
	}
	claimIdx := a.order().indexOf("claim:" + claimKeyFor(base, 1))
	barrierIdx := a.order().indexOf("claim:" + effectBarrierKey(base, 0))
	clientIdx := a.order().indexOf("client:TaskCreate")
	markerIdx := a.order().indexOf("evidence:" + TaskLinkageKey(chain))
	if !(claimIdx >= 0 && barrierIdx > claimIdx && clientIdx > barrierIdx && markerIdx > clientIdx) {
		t.Fatalf("B order wrong (claim=%d barrier=%d client=%d marker=%d)", claimIdx, barrierIdx, clientIdx, markerIdx)
	}
}

// Scenario S1 (worker): same barrier race for WorkerStart.
func TestBarrierScenarioWorkerAPausedBBWins(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	mapping, err := a.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.EnsureAssignment(t.Context(), dispatchRef(chain)); err != nil {
		t.Fatal(err)
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, IssueID: mapping.Chain.IssueID}
	base := workerStartClaimBase(chain)

	clock := &slowClock{now: time.Now().Add(-1 * time.Hour)}
	a.Now = clock.Now
	a.LeaseTTL = 100 * time.Millisecond
	releaseA := a.entry.setClaimBlock(effectBarrierKey(base, 0))

	errA := make(chan error, 1)
	go func() {
		_, err := a.RunClaimedTask(context.Background(), claimed)
		errA <- err
	}()
	parkDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(parkDeadline) {
		if a.order().indexOf("claim:"+claimKeyFor(base, 0)) >= 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	b.ClaimPoll = time.Millisecond
	b.ClaimMaxWait = 2 * time.Second
	if _, err := b.RunClaimedTask(context.Background(), claimed); err != nil {
		t.Fatalf("bridge B takeover: %v", err)
	}

	close(releaseA)
	// After losing the barrier, A waits bounded for the winner's committed
	// dispatch. B already committed it, so A must converge on B's dispatch —
	// and never call the client itself. (If nothing appears in time, A fails
	// with ErrScopeAttemptInFlight; either way A never creates.)
	if err := <-errA; err != nil {
		if !errors.Is(err, ErrScopeAttemptInFlight) {
			t.Fatalf("unexpected error for A after losing the barrier: %v", err)
		}
	}
	if a.client.workerStarts != 1 {
		t.Fatalf("WorkerStart attempts = %d, want exactly B's single call", a.client.workerStarts)
	}
	if len(a.daemon.started) != 1 {
		t.Fatalf("hivecrew task started %d times, want 1", len(a.daemon.started))
	}
	claimIdx := a.order().indexOf("claim:" + claimKeyFor(base, 1))
	barrierIdx := a.order().indexOf("claim:" + effectBarrierKey(base, 0))
	clientIdx := a.order().indexOf("client:WorkerStart")
	markerIdx := a.order().indexOf("evidence:" + DispatchLinkageKey(mapping.Chain))
	if !(claimIdx >= 0 && barrierIdx > claimIdx && clientIdx > barrierIdx && markerIdx > clientIdx) {
		t.Fatalf("B order wrong (claim=%d barrier=%d client=%d marker=%d)", claimIdx, barrierIdx, clientIdx, markerIdx)
	}
}

// Scenario S2 (run): A has WON the barrier and its RunCreate is blocked
// (side effect in flight, lease expired past TTL). B must get
// ErrScopeAttemptInFlight and never call; after A commits, B converges on
// retry. RunCreate count stays exactly 1.
func TestBarrierScenarioAResolvedBBlockedThenConverges(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	base := runCreateClaimBase(chain)
	ref := ProjectRef{Chain: chain, DisplayObjective: "s2 run objective"}

	clock := &slowClock{now: time.Now().Add(-1 * time.Hour)}
	a.Now = clock.Now
	lease := 100 * time.Millisecond
	a.LeaseTTL = lease
	releaseClient := make(chan struct{})
	a.client.runCreateBlock = releaseClient

	aCommitted := make(chan struct{})
	errA := make(chan error, 1)
	go func() {
		_, err := a.EnsureProjectRun(context.Background(), ref)
		errA <- err
		close(aCommitted)
	}()
	// A has entered RunCreate with the barrier won.
	enterDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(enterDeadline) {
		if a.client.currentRunCreates() > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// Lease expires while the unfenced create is in flight.
	clock.advance(lease * 2)

	// B past TTL: no takeover into a second create.
	b.ClaimPoll = time.Millisecond
	b.ClaimMaxWait = 2 * time.Second
	if _, err := b.EnsureProjectRun(context.Background(), ref); !errors.Is(err, ErrScopeAttemptInFlight) {
		t.Fatalf("expected ErrScopeAttemptInFlight while A's create was in flight, got %v", err)
	}
	// B never entered the client during the blocked window.
	probeDeadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(probeDeadline) {
		if a.client.currentRunCreates() > 1 {
			t.Fatal("B called RunCreate while A's unresolved create was in flight")
		}
		time.Sleep(2 * time.Millisecond)
	}

	// A resolves and commits exactly one Run.
	close(releaseClient)
	if err := <-errA; err != nil {
		t.Fatalf("bridge A failed after slow create: %v", err)
	}
	<-aCommitted

	// B retries and converges on the committed result without a new create.
	runB, err := b.EnsureProjectRun(context.Background(), ref)
	if err != nil {
		t.Fatalf("bridge B convergence retry: %v", err)
	}
	if len(a.client.runs) != 1 || a.client.runs[0].ID != runB {
		t.Fatalf("want exactly A's run %s, got %+v", runB, a.client.runs)
	}
	if a.client.runCreates != 1 {
		t.Fatalf("RunCreate attempts = %d, want exactly 1 across both bridges", a.client.runCreates)
	}
	// A's order: claim(0) -> barrier(0) -> client -> marker.
	claimIdx := a.order().indexOf("claim:" + claimKeyFor(base, 0))
	barrierIdx := a.order().indexOf("claim:" + effectBarrierKey(base, 0))
	clientIdx := a.order().indexOf("client:RunCreate")
	markerIdx := a.order().indexOf("evidence:" + RunLinkageKey(chain))
	if !(claimIdx >= 0 && barrierIdx > claimIdx && clientIdx > barrierIdx && markerIdx > clientIdx) {
		t.Fatalf("A order wrong (claim=%d barrier=%d client=%d marker=%d)", claimIdx, barrierIdx, clientIdx, markerIdx)
	}
}

// Scenario S2 (task): A won the barrier and TaskCreate blocks; B stays out;
// after A commits, B converges. TaskCreate count exactly 1.
func TestBarrierScenarioTaskAResolvedBBlockedThenConverges(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	ref := TaskRef{Chain: chain, Instructions: instructions()}

	clock := &slowClock{now: time.Now().Add(-1 * time.Hour)}
	a.Now = clock.Now
	lease := 100 * time.Millisecond
	a.LeaseTTL = lease
	releaseClient := make(chan struct{})
	a.client.taskCreateBlock = releaseClient

	aCommitted := make(chan struct{})
	errA := make(chan error, 1)
	go func() {
		_, _, err := a.EnsureIssueTask(context.Background(), ref)
		errA <- err
		close(aCommitted)
	}()
	enterDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(enterDeadline) {
		if a.client.currentTaskCreates() > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	clock.advance(lease * 2)

	b.ClaimPoll = time.Millisecond
	b.ClaimMaxWait = 2 * time.Second
	if _, _, err := b.EnsureIssueTask(context.Background(), ref); !errors.Is(err, ErrScopeAttemptInFlight) {
		t.Fatalf("expected ErrScopeAttemptInFlight, got %v", err)
	}
	probeDeadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(probeDeadline) {
		if a.client.currentTaskCreates() > 1 {
			t.Fatal("B called TaskCreate while A's unresolved create was in flight")
		}
		time.Sleep(2 * time.Millisecond)
	}

	close(releaseClient)
	if err := <-errA; err != nil {
		t.Fatalf("bridge A failed after slow create: %v", err)
	}
	<-aCommitted

	if _, _, err := b.EnsureIssueTask(context.Background(), ref); err != nil {
		t.Fatalf("bridge B convergence retry: %v", err)
	}
	totalTasks := 0
	for _, tasks := range a.client.tasks {
		totalTasks += len(tasks)
	}
	if totalTasks != 1 || a.client.taskCreates != 1 {
		t.Fatalf("want exactly one task create, got objects=%d attempts=%d", totalTasks, a.client.taskCreates)
	}
}

// Scenario S2 (worker): A won the barrier and WorkerStart blocks; B stays
// out; after A commits, B converges. WorkerStart count exactly 1.
func TestBarrierScenarioWorkerAResolvedBBlockedThenConverges(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	mapping, err := a.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.EnsureAssignment(t.Context(), dispatchRef(chain)); err != nil {
		t.Fatal(err)
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, IssueID: mapping.Chain.IssueID}

	clock := &slowClock{now: time.Now().Add(-1 * time.Hour)}
	a.Now = clock.Now
	lease := 100 * time.Millisecond
	a.LeaseTTL = lease
	releaseClient := make(chan struct{})
	a.client.workerStartBlock = releaseClient

	aCommitted := make(chan struct{})
	errA := make(chan error, 1)
	go func() {
		_, err := a.RunClaimedTask(context.Background(), claimed)
		errA <- err
		close(aCommitted)
	}()
	enterDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(enterDeadline) {
		if a.client.currentWorkerStarts() > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	clock.advance(lease * 2)

	b.ClaimPoll = time.Millisecond
	b.ClaimMaxWait = 2 * time.Second
	if _, err := b.RunClaimedTask(context.Background(), claimed); !errors.Is(err, ErrScopeAttemptInFlight) {
		t.Fatalf("expected ErrScopeAttemptInFlight, got %v", err)
	}
	probeDeadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(probeDeadline) {
		if a.client.currentWorkerStarts() > 1 {
			t.Fatal("B called WorkerStart while A's unresolved start was in flight")
		}
		time.Sleep(2 * time.Millisecond)
	}

	close(releaseClient)
	if err := <-errA; err != nil {
		t.Fatalf("bridge A failed after slow worker start: %v", err)
	}
	<-aCommitted

	if _, err := b.RunClaimedTask(context.Background(), claimed); err != nil {
		t.Fatalf("bridge B convergence retry: %v", err)
	}
	if a.client.workerStarts != 1 {
		t.Fatalf("WorkerStart attempts = %d, want exactly 1 across both bridges", a.client.workerStarts)
	}
	if len(a.daemon.started) != 1 {
		t.Fatalf("hivecrew task started %d times, want 1", len(a.daemon.started))
	}
}

// currentRunCreates and currentTaskCreates report live attempt counters for
// barriers.
func (f *fakeClient) currentRunCreates() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runCreates
}

func (f *fakeClient) currentTaskCreates() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.taskCreates
}

// currentWorkerStarts reports the live attempt counter for barriers.
func (f *fakeClient) currentWorkerStarts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.workerStarts
}

// Direct unit proof: withinLease fails closed once the clock passes expiry.
func TestWithinLeaseFailsClosedAfterExpiry(t *testing.T) {
	a, _ := twinBridges(t)
	clock := &slowClock{now: time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)}
	a.Now = clock.Now
	permit := effectPermit{baseKey: "test/base", generation: 0, epoch: 0, leaseExpiresAt: clock.Now().Add(50 * time.Millisecond)}
	if err := a.withinLease(permit); err != nil {
		t.Fatalf("live lease must pass: %v", err)
	}
	clock.advance(100 * time.Millisecond)
	if err := a.withinLease(permit); !errors.Is(err, ErrClaimLeaseExpired) {
		t.Fatalf("expired lease must fail closed with ErrClaimLeaseExpired, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// R6 blockers
// ---------------------------------------------------------------------------

// Blocker 1: replayed same-claim barrier CAS must never return a second
// Acquired permit. Direct protocol proof with identical caller identity
// (same actor/session/InstanceID) and two barriers sharing one ledger.
func TestBarrierReplayNeverReturnsSecondPermit(t *testing.T) {
	a, _ := twinBridges(t)
	ctx := t.Context()
	workRef := "hivecrew://ws/work/prj/orphan"
	claim := scopeClaim{baseKey: "base/scope", generation: 0, leaseExpiresAt: time.Now().Add(time.Minute)}

	first, err := a.winEffectBarrier(ctx, workRef, claim)
	if err != nil || first.epoch != 0 {
		t.Fatalf("first barrier win: %+v err=%v", first, err)
	}
	// Second call with the identical claim (same holder, same lease
	// deadline): the attempt id changes, the CAS loses, and the permit is
	// NOT handed out again.
	_, err = a.winEffectBarrier(ctx, workRef, claim)
	if !errors.Is(err, ErrScopeAttemptInFlight) {
		t.Fatalf("replayed barrier CAS must fail closed, got %v", err)
	}
	// The stored barrier payload still names the first attempt's identity.
	record, found, _ := a.entry.LookupEvidence(ctx, workRef, effectBarrierKey(claim.baseKey, 0))
	if !found {
		t.Fatal("barrier record missing")
	}
	if holder, _ := record.Payload["instance_id"].(string); holder == a.instanceID() {
		t.Fatalf("stored holder must be the attempt-unique id, not the bare instance id: %q", holder)
	}
}

// Blocker 1: concurrent same-identity callers on ONE bridge — RunCreate,
// TaskCreate, WorkerStart each happen exactly once.
func TestConcurrentSameIdentitySingleCreate(t *testing.T) {
	a, _ := twinBridges(t)
	chain := validChain()

	// Run: 8 goroutines, one Bridge, one identity.
	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	runIDs := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			<-start
			id, err := a.EnsureProjectRun(t.Context(), ProjectRef{Chain: chain})
			runIDs[slot], errs[slot] = id, err
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("run goroutine %d: %v", i, err)
		}
		if runIDs[i] != runIDs[0] {
			t.Fatalf("run goroutine %d diverged: %s vs %s", i, runIDs[i], runIDs[0])
		}
	}
	if a.client.runCreates != 1 {
		t.Fatalf("RunCreate count = %d, want exactly 1", a.client.runCreates)
	}

	// Task: same shape.
	taskIDs := make([]string, n)
	errs = make([]error, n)
	var wg2 sync.WaitGroup
	start2 := make(chan struct{})
	for i := 0; i < n; i++ {
		wg2.Add(1)
		go func(slot int) {
			defer wg2.Done()
			<-start2
			_, id, err := a.EnsureIssueTask(t.Context(), TaskRef{Chain: chain, Instructions: instructions()})
			taskIDs[slot], errs[slot] = id, err
		}(i)
	}
	close(start2)
	wg2.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("task goroutine %d: %v", i, err)
		}
		if taskIDs[i] != taskIDs[0] {
			t.Fatalf("task goroutine %d diverged: %s vs %s", i, taskIDs[i], taskIDs[0])
		}
	}
	if a.client.taskCreates != 1 {
		t.Fatalf("TaskCreate count = %d, want exactly 1", a.client.taskCreates)
	}

	// Worker: assignment first, then 8 concurrent claim runs.
	mapping, err := a.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, IssueID: mapping.Chain.IssueID}
	errs = make([]error, n)
	var wg3 sync.WaitGroup
	start3 := make(chan struct{})
	for i := 0; i < n; i++ {
		wg3.Add(1)
		go func(slot int) {
			defer wg3.Done()
			<-start3
			_, err := a.RunClaimedTask(t.Context(), claimed)
			errs[slot] = err
		}(i)
	}
	close(start3)
	wg3.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker goroutine %d: %v", i, err)
		}
	}
	if a.client.workerStarts != 1 {
		t.Fatalf("WorkerStart count = %d, want exactly 1", a.client.workerStarts)
	}
	if len(a.daemon.started) != 1 {
		t.Fatalf("StartTask count = %d, want exactly 1", len(a.daemon.started))
	}
}

// Blocker 2: WorkerStart succeeds, StartTask fails, replay retries only
// StartTask. WorkerStart count stays 1; daemon start count reaches 2; the
// replay returns success only after the start succeeds.
func TestStartTaskFailureReplayRetriesOnlyStart(t *testing.T) {
	a, _ := twinBridges(t)
	chain := validChain()
	mapping, err := a.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, IssueID: mapping.Chain.IssueID}

	// First attempt: WorkerStart succeeds, StartTask fails.
	a.daemon.startErr = errors.New("daemon unavailable")
	first, err := a.RunClaimedTask(t.Context(), claimed)
	if err == nil {
		t.Fatal("StartTask failure must surface an error, not success")
	}
	if first.OrcaDispatchID == "" {
		t.Fatalf("failed call must still return the committed mapping: %+v", first)
	}
	if a.client.workerStarts != 1 {
		t.Fatalf("worker starts after first attempt = %d, want 1", a.client.workerStarts)
	}
	if len(a.daemon.started) != 1 {
		t.Fatalf("daemon start attempts after first attempt = %d, want 1 (the failed one)", len(a.daemon.started))
	}

	// Replay: daemon recovers; only StartTask is retried.
	a.daemon.startErr = nil
	second, err := a.RunClaimedTask(t.Context(), claimed)
	if err != nil {
		t.Fatalf("replay after daemon recovery: %v", err)
	}
	if second.OrcaDispatchID != first.OrcaDispatchID {
		t.Fatalf("replay diverged: %+v vs %+v", second, first)
	}
	if a.client.workerStarts != 1 {
		t.Fatalf("replay re-ran WorkerStart: count = %d, want 1", a.client.workerStarts)
	}
	if len(a.daemon.started) != 2 {
		t.Fatalf("daemon start attempts = %d, want 2 (failed + succeeded)", len(a.daemon.started))
	}
	// Third replay is fully converged: no new effects at all.
	if _, err := a.RunClaimedTask(t.Context(), claimed); err != nil {
		t.Fatalf("converged replay: %v", err)
	}
	if a.client.workerStarts != 1 || len(a.daemon.started) != 2 {
		t.Fatalf("converged replay duplicated effects: workers=%d starts=%d", a.client.workerStarts, len(a.daemon.started))
	}
}

// Blocker 3: run evidence digest drift fails closed, including on a fresh
// Bridge restart (empty memo).
func TestRunEvidenceDigestDriftFailsClosed(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	if _, err := a.EnsureProjectRun(t.Context(), ProjectRef{Chain: chain, DisplayObjective: "original"}); err != nil {
		t.Fatal(err)
	}
	// Fresh instance (b) requests a drifted objective: same scope, different
	// digest. The committed evidence must not be adopted; the caller fails
	// closed instead of silently reusing or duplicating.
	if _, err := b.EnsureProjectRun(t.Context(), ProjectRef{Chain: chain, DisplayObjective: "drifted"}); !errors.Is(err, ErrMappingConflict) {
		t.Fatalf("expected ErrMappingConflict for drifted objective, got %v", err)
	}
	if a.client.runCreates != 1 {
		t.Fatalf("drifted request must not create a second run: %d", a.client.runCreates)
	}
}

// Blocker 3: run orphan marker drift (same ws/prj marker, different
// objective body) is never adopted.
func TestRunMarkerDriftFailsClosed(t *testing.T) {
	a, _ := twinBridges(t)
	chain := validChain()
	// An orphan run whose marker matches but whose objective body differs.
	a.client.runs = []OrcaRun{{ID: "run_drift1", Objective: ProjectRunObjective("a different objective", chain)}}
	if _, err := a.EnsureProjectRun(t.Context(), ProjectRef{Chain: chain, DisplayObjective: ""}); !errors.Is(err, ErrMappingConflict) {
		t.Fatalf("expected ErrMappingConflict for drifted orphan objective, got %v", err)
	}
	if a.client.runCreates != 0 {
		t.Fatalf("drifted orphan must not be created over: %d", a.client.runCreates)
	}
}

// Blocker 3: task evidence digest drift fails closed across a fresh Bridge.
func TestTaskEvidenceDigestDriftFailsClosed(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	if _, _, err := a.EnsureIssueTask(t.Context(), TaskRef{Chain: chain, Instructions: "original instructions"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.EnsureIssueTask(t.Context(), TaskRef{Chain: chain, Instructions: "drifted instructions"}); !errors.Is(err, ErrMappingConflict) {
		t.Fatalf("expected ErrMappingConflict for drifted instructions, got %v", err)
	}
	if a.client.taskCreates != 1 {
		t.Fatalf("drifted request must not create a second task: %d", a.client.taskCreates)
	}
}

// Blocker 3: task orphan marker drift (same ids, different spec body) is
// never adopted.
func TestTaskMarkerDriftFailsClosed(t *testing.T) {
	a, _ := twinBridges(t)
	chain := validChain()
	runID, err := a.EnsureProjectRun(t.Context(), ProjectRef{Chain: chain})
	if err != nil {
		t.Fatal(err)
	}
	// Orphan task with matching marker ids but a drifted instruction body.
	a.client.tasks[runID] = []OrcaTask{{ID: "task_drift1", RunID: runID, Spec: TaskSpec("drifted body", chain)}}
	if _, _, err := a.EnsureIssueTask(t.Context(), TaskRef{Chain: chain, Instructions: instructions()}); !errors.Is(err, ErrMappingConflict) {
		t.Fatalf("expected ErrMappingConflict for drifted orphan spec, got %v", err)
	}
	if a.client.taskCreates != 0 {
		t.Fatalf("drifted orphan must not be created over: %d", a.client.taskCreates)
	}
}

// Blocker 3: worker placement drift against committed dispatch evidence
// fails closed, including on a fresh Bridge restart.
func TestWorkerPlacementDriftFailsClosed(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	mapping, err := a.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, IssueID: mapping.Chain.IssueID}
	if _, err := a.RunClaimedTask(t.Context(), claimed); err != nil {
		t.Fatal(err)
	}

	// Fresh Bridge, drifted placement for the same assignment: the committed
	// dispatch evidence must not authorize a second, differently-placed worker.
	drifted := dispatchRef(chain)
	drifted.Placement.Agent = "claude"
	if _, err := b.EnsureAssignment(t.Context(), drifted); !errors.Is(err, ErrMappingConflict) {
		t.Fatalf("expected ErrMappingConflict for drifted placement, got %v", err)
	}
	if a.client.workerStarts != 1 {
		t.Fatalf("drifted placement must not start a second worker: %d", a.client.workerStarts)
	}
}

// ---------------------------------------------------------------------------
// R7: fresh-process resume, loser-waiter validation, mismatch, replay
// ---------------------------------------------------------------------------

// Blocker 2: after WorkerStart succeeds and StartTask fails, a FRESH Bridge
// process (empty memo, new session) resumes only Daemon.StartTask and
// reports success only after StartTask and its durable evidence settle;
// WorkerStart is never re-run.
func TestFreshProcessResumesStartTaskOnly(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	mapping, err := a.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID, IssueID: mapping.Chain.IssueID}

	// A claims with a short lease so a fresh process can take over after its
	// StartTask failure (both the creation claim and its start permit expire).
	a.LeaseTTL = 20 * time.Millisecond
	a.daemon.startErr = errors.New("daemon down")
	firstFailed, err := a.RunClaimedTask(t.Context(), claimed)
	if err == nil {
		t.Fatal("StartTask failure must surface an error")
	}
	if firstFailed.OrcaDispatchID == "" {
		t.Fatalf("failed call must still return the committed dispatch: %+v", firstFailed)
	}
	// Let A's leases expire after its failed run.
	time.Sleep(30 * time.Millisecond)
	if a.client.workerStarts != 1 || len(a.daemon.started) != 1 {
		t.Fatalf("after first attempt: workers=%d starts=%d, want 1/1", a.client.workerStarts, len(a.daemon.started))
	}

	// Fresh process: bridge B shares the ledger but has an empty memo.
	b.daemon.startErr = nil
	resumed, err := b.RunClaimedTask(t.Context(), claimed)
	if err != nil {
		t.Fatalf("fresh-process resume: %v", err)
	}
	// Exact dispatch reuse: B's resume must carry the SAME Orca dispatch the
	// failed run committed, not merely any non-empty id.
	if resumed.OrcaDispatchID != firstFailed.OrcaDispatchID {
		t.Fatalf("resume reused a different dispatch: got %s, want %s", resumed.OrcaDispatchID, firstFailed.OrcaDispatchID)
	}
	// WorkerStart never re-run; StartTask retried exactly once by B.
	if a.client.workerStarts != 1 {
		t.Fatalf("fresh process re-ran WorkerStart: %d", a.client.workerStarts)
	}
	if len(a.daemon.started) != 2 {
		t.Fatalf("daemon start attempts = %d, want 2 (A failed + B resumed)", len(a.daemon.started))
	}
}

// Blocker 3: the loser-waiter adopting an Orca orphan must validate the
// placement digest — an adopted mapping never carries a zero digest.
func TestLoserWaiterNeverMemoizesZeroDigest(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	mapping, err := a.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.EnsureAssignment(t.Context(), dispatchRef(chain)); err != nil {
		t.Fatal(err)
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID, IssueID: mapping.Chain.IssueID}

	// Orphan dispatch visible to B's waiter path (A "crashed" after start).
	// Derive the ids from the shared Orca client state rather than B's memo
	// so the test also works on a fresh-process-like bridge.
	var orphanRunID, orphanTaskID string
	if runEntry, ok := b.memoRunEntry(Chain{WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID}); ok {
		orphanRunID = runEntry.id
	}
	if taskEntry, ok := b.memoTaskEntry(chain); ok {
		orphanTaskID = taskEntry.id
	}
	if orphanRunID == "" {
		for _, run := range b.client.runs {
			if ws, prj, ok := RunMarkerScan(run.Objective); ok && ws == chain.WorkspaceID && prj == chain.ProjectID {
				orphanRunID = run.ID
				break
			}
		}
	}
	if orphanTaskID == "" {
		for _, tasks := range b.client.tasks {
			for _, task := range tasks {
				if _, _, issue, taskID, ok := TaskMarkerScan(task.Spec); ok && issue == mapping.Chain.IssueID && taskID == mapping.Chain.TaskID {
					orphanTaskID = task.ID
					break
				}
			}
			if orphanTaskID != "" {
				break
			}
		}
	}
	if orphanRunID == "" || orphanTaskID == "" {
		t.Fatalf("could not derive orphan ids: run=%q task=%q", orphanRunID, orphanTaskID)
	}
	b.client.dispatch = &OrcaDispatch{
		ID:             "ctx_orphanr7",
		RunID:          orphanRunID,
		TaskID:         orphanTaskID,
		AssigneeHandle: "term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e",
		Status:         "dispatched",
	}

	adopted, err := b.RunClaimedTask(t.Context(), claimed)
	if err != nil {
		t.Fatalf("loser adoption: %v", err)
	}
	if adopted.OrcaDispatchID != "ctx_orphanr7" {
		t.Fatalf("expected orphan adoption, got %+v", adopted)
	}
	if adopted.PlacementDigest == "" {
		t.Fatal("adopted mapping carries a zero placement digest")
	}
	if adopted.WorkspaceID != chain.WorkspaceID || adopted.AssignmentID != chain.AssignmentID {
		t.Fatalf("adopted mapping lost assignment lineage: %+v", adopted)
	}
	// Success only after StartTask settled: exactly one start attempt ran.
	if len(b.daemon.started) != 1 {
		t.Fatalf("daemon starts after adoption = %d, want 1", len(b.daemon.started))
	}
	if b.client.workerStarts != 0 {
		t.Fatalf("waiter must not call WorkerStart: %d", b.client.workerStarts)
	}
}

// Blocker 3: an orphan dispatch with a mismatched TaskID is never adopted.
func TestLoserWaiterRejectsMismatchedOrphan(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	mapping, err := a.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.EnsureAssignment(t.Context(), dispatchRef(chain)); err != nil {
		t.Fatal(err)
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID, IssueID: mapping.Chain.IssueID}

	var mismatchRunID string
	if runEntry, ok := b.memoRunEntry(Chain{WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID}); ok {
		mismatchRunID = runEntry.id
	}
	if mismatchRunID == "" {
		for _, run := range b.client.runs {
			if ws, prj, ok := RunMarkerScan(run.Objective); ok && ws == chain.WorkspaceID && prj == chain.ProjectID {
				mismatchRunID = run.ID
				break
			}
		}
	}
	if mismatchRunID == "" {
		t.Fatal("could not derive run id for mismatch fixture")
	}
	// Task id belongs to a different Orca task.
	b.client.dispatch = &OrcaDispatch{
		ID:             "ctx_mismatch1",
		RunID:          mismatchRunID,
		TaskID:         "task_ffffffffffff",
		AssigneeHandle: "term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e",
		Status:         "dispatched",
	}
	// Force B down the waiter path: another instance already holds the
	// effect barrier for this scope (simulating an in-flight worker start),
	// so B cannot win a permit and must wait on the orphan — which mismatches.
	// Expired claim lets B take over the claim generation; the barrier epoch
	// stays held by bridge-other (injected on the barrier key), so B must
	// wait and validate the orphan before adopting.
	if err := b.entry.injectClaimWin(workerStartClaimBase(mapping.Chain), "bridge-other", true); err != nil {
		t.Fatal(err)
	}
	if err := b.entry.injectBarrierHeld(effectBarrierKey(workerStartClaimBase(mapping.Chain), 0), "bridge-other"); err != nil {
		t.Fatal(err)
	}
	b.ClaimPoll = time.Millisecond
	b.ClaimMaxWait = 200 * time.Millisecond
	if _, err := b.RunClaimedTask(t.Context(), claimed); !errors.Is(err, ErrResultIdentityMismatch) {
		t.Fatalf("mismatched orphan must be rejected with ErrResultIdentityMismatch, got %v", err)
	}
	if b.client.workerStarts != 0 {
		t.Fatalf("mismatch must not trigger WorkerStart: %d", b.client.workerStarts)
	}
}

// Blocker 2 replay: after a successful settle, replays (fresh or same
// process) add no effects at all.
func TestSettledReplayAddsNoEffects(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	mapping, err := a.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.EnsureAssignment(t.Context(), dispatchRef(chain)); err != nil {
		t.Fatal(err)
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, IssueID: mapping.Chain.IssueID}

	if _, err := a.RunClaimedTask(t.Context(), claimed); err != nil {
		t.Fatal(err)
	}
	workers, starts := a.client.workerStarts, len(a.daemon.started)

	// Same-process replay and fresh-process replay.
	if _, err := a.RunClaimedTask(t.Context(), claimed); err != nil {
		t.Fatalf("same-process replay: %v", err)
	}
	if _, err := b.RunClaimedTask(t.Context(), claimed); err != nil {
		t.Fatalf("fresh-process replay: %v", err)
	}
	if a.client.workerStarts != workers || len(a.daemon.started) != starts {
		t.Fatalf("replays duplicated effects: workers=%d starts=%d (want %d/%d)",
			a.client.workerStarts, len(a.daemon.started), workers, starts)
	}
}

// ---------------------------------------------------------------------------
// R7 pre-review closers
// ---------------------------------------------------------------------------

// waiterFixture builds two bridges with an assignment committed and returns
// a helper that forces bridge B down the waiter path with an injected
// expired claim plus held barrier.
func waiterFixture(t *testing.T, chain Chain) (*testBridge, *testBridge, DaemonTask, AssignmentMapping) {
	t.Helper()
	a, b := twinBridges(t)
	mapping, err := a.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.EnsureAssignment(t.Context(), dispatchRef(chain)); err != nil {
		t.Fatal(err)
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID, IssueID: mapping.Chain.IssueID}
	// Expired claim (B may take over the claim) + held barrier (B must wait).
	if err := b.entry.injectClaimWin(workerStartClaimBase(mapping.Chain), "bridge-other", true); err != nil {
		t.Fatal(err)
	}
	if err := b.entry.injectBarrierHeld(effectBarrierKey(workerStartClaimBase(mapping.Chain), 0), "bridge-other"); err != nil {
		t.Fatal(err)
	}
	b.ClaimPoll = time.Millisecond
	b.ClaimMaxWait = 200 * time.Millisecond
	return a, b, claimed, mapping
}

func derivedOrcaIDs(tb *testBridge, chain Chain, mapping AssignmentMapping) (runID string, taskID string) {
	tb.client.mu.Lock()
	defer tb.client.mu.Unlock()
	for _, run := range tb.client.runs {
		if ws, prj, ok := RunMarkerScan(run.Objective); ok && ws == chain.WorkspaceID && prj == chain.ProjectID {
			runID = run.ID
			break
		}
	}
	for _, tasks := range tb.client.tasks {
		for _, task := range tasks {
			if _, _, issue, taskID, ok := TaskMarkerScan(task.Spec); ok && issue == mapping.Chain.IssueID && taskID == mapping.Chain.TaskID {
				return runID, task.ID
			}
		}
	}
	return runID, ""
}

// Item 1a: a RunID-mismatched orphan is rejected (branch now reachable).
func TestWaiterRejectsRunMismatchOrphan(t *testing.T) {
	chain := validChain()
	a, b, claimed, mapping := waiterFixture(t, chain)
	orphanRunID, orphanTaskID := derivedOrcaIDs(b, chain, mapping)
	if orphanRunID == "" || orphanTaskID == "" {
		t.Fatalf("derive failed: run=%q task=%q", orphanRunID, orphanTaskID)
	}
	b.client.dispatch = &OrcaDispatch{
		ID:             "ctx_runmismatch",
		RunID:          "run_ffffffffffff", // wrong run
		TaskID:         orphanTaskID,
		AssigneeHandle: "term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e",
		Status:         "dispatched",
	}
	if _, err := b.RunClaimedTask(t.Context(), claimed); !errors.Is(err, ErrResultIdentityMismatch) {
		t.Fatalf("expected ErrResultIdentityMismatch for wrong RunID, got %v", err)
	}
	if b.client.workerStarts != 0 {
		t.Fatalf("RunID mismatch must not start a worker: %d", b.client.workerStarts)
	}
	_ = a
}

// Item 1b: an orphan with an EMPTY TaskID is rejected (unknown identity).
func TestWaiterRejectsEmptyTaskIDOrphan(t *testing.T) {
	chain := validChain()
	a, b, claimed, mapping := waiterFixture(t, chain)
	orphanRunID, _ := derivedOrcaIDs(b, chain, mapping)
	if orphanRunID == "" {
		t.Fatal("no run id")
	}
	b.client.dispatch = &OrcaDispatch{
		ID:             "ctx_emptytask",
		RunID:          orphanRunID,
		TaskID:         "", // absent identity must fail closed
		AssigneeHandle: "term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e",
		Status:         "dispatched",
	}
	if _, err := b.RunClaimedTask(t.Context(), claimed); !errors.Is(err, ErrResultIdentityMismatch) {
		t.Fatalf("expected ErrResultIdentityMismatch for empty TaskID, got %v", err)
	}
	if b.client.workerStarts != 0 {
		t.Fatalf("empty TaskID must not start a worker: %d", b.client.workerStarts)
	}
	_ = a
}

// Item 2: an evidence conflict during waiter adoption must not poison the
// memo — a later replay still surfaces the conflict instead of bypassing it
// through a memoized mapping.
func TestWaiterEvidenceConflictDoesNotPoisonMemo(t *testing.T) {
	chain := validChain()
	a, b, claimed, mapping := waiterFixture(t, chain)
	orphanRunID, orphanTaskID := derivedOrcaIDs(b, chain, mapping)
	if orphanRunID == "" || orphanTaskID == "" {
		t.Fatalf("derive failed")
	}
	b.client.dispatch = &OrcaDispatch{
		ID:             "ctx_conflictev",
		RunID:          orphanRunID,
		TaskID:         orphanTaskID,
		AssigneeHandle: "term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e",
		Status:         "dispatched",
	}
	// Make the dispatch evidence append conflict: pre-store a DIFFERENT
	// payload under the dispatch-evidence key.
	workRef, err := b.dispatchWorkRef(t.Context(), DispatchMap{WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID, IssueID: chain.IssueID, TaskID: chain.TaskID, AssignmentID: chain.AssignmentID})
	if err != nil {
		t.Fatal(err)
	}
	b.entry.mu.Lock()
	b.entry.events[workRef][dispatchEvidenceLookupKey(mapping.Chain, "ctx_conflictev")] = map[string]any{"planted": true}
	b.entry.mu.Unlock()

	first, err := b.RunClaimedTask(t.Context(), claimed)
	if err == nil {
		t.Fatal("conflicting evidence must surface an error")
	}
	// The mapping may be returned with the error, but the memo must stay
	// clean so a replay re-validates instead of bypassing.
	if m, ok := b.memoDispatch(mapping.Chain); ok && m.OrcaDispatchID == first.OrcaDispatchID && m.PlacementDigest == "" {
		t.Fatal("memo was poisoned with an evidence-free mapping")
	}
	// Replay: the conflict must still surface (not silently succeed).
	if _, err := b.RunClaimedTask(t.Context(), claimed); err == nil {
		t.Fatal("replay must not bypass the evidence conflict")
	}
	if b.client.workerStarts != 0 {
		t.Fatalf("conflict path must never start a worker: %d", b.client.workerStarts)
	}
	_ = a
}

// Item 5: a loser observing committed evidence while the winner's StartTask
// is blocked must NOT report success until the start settles.
func TestWaiterDoesNotSucceedWhileStartPending(t *testing.T) {
	a, b := twinBridges(t)
	chain := validChain()
	mapping, err := a.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.EnsureAssignment(t.Context(), dispatchRef(chain)); err != nil {
		t.Fatal(err)
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID, IssueID: mapping.Chain.IssueID}

	// Winner A runs FIRST, unobstructed: it wins its own claim + barrier,
	// starts the worker, commits the dispatch evidence, then blocks inside
	// StartTask.
	releaseStart := make(chan struct{})
	a.daemon.startBlock = releaseStart
	aDone := make(chan error, 1)
	go func() {
		_, err := a.RunClaimedTask(context.Background(), claimed)
		aDone <- err
	}()
	// Deterministic barrier: A's dispatch evidence exists (evidence precedes
	// the start verb) and its StartTask is in flight.
	evDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(evDeadline) {
		if a.client.currentWorkerStarts() > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// Additional settle: give the evidence append a moment to land.
	evDeadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(evDeadline) {
		a.entry.mu.Lock()
		_, ok := a.entry.events["hivecrew://ws/work/prj-dispatch"][DispatchLinkageKey(mapping.Chain)]
		a.entry.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// Loser B sees committed evidence while the start is unresolved. It must
	// not report success before the start settles.
	bDone := make(chan error, 1)
	go func() {
		_, err := b.RunClaimedTask(context.Background(), claimed)
		bDone <- err
	}()
	select {
	case err := <-bDone:
		t.Fatalf("waiter returned while StartTask pending: %v", err)
	case <-time.After(150 * time.Millisecond):
		// still blocked: correct
	}
	if a.client.workerStarts != 1 {
		t.Fatalf("worker starts = %d, want 1", a.client.workerStarts)
	}
	close(releaseStart)
	if err := <-aDone; err != nil {
		t.Fatalf("winner failed: %v", err)
	}
	if err := <-bDone; err != nil {
		t.Fatalf("loser should converge after start settles: %v", err)
	}
}

// Item 6 (R8 form): two assignments validated CONCURRENTLY, each against its
// own task-keyed orphan, through a thread-safe fake. Both must surface their
// own ErrResultIdentityMismatch with zero side effects.
func TestConcurrentTwoAssignmentsProbeIsolation(t *testing.T) {
	a, _ := twinBridges(t)
	chain := validChain()
	m1, err := a.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	chain2 := validChain()
	chain2.WorkspaceID = chain.WorkspaceID
	chain2.ProjectID = chain.ProjectID
	chain2.IssueID = chain.IssueID
	chain2.TaskID = "c05a0000-0000-4000-8000-000000000041"
	chain2.AssignmentID = "c05a0000-0000-4000-8000-000000000051"
	m2, err := a.EnsureAssignment(t.Context(), dispatchRef(chain2))
	if err != nil {
		t.Fatal(err)
	}
	claimed1 := DaemonTask{ID: m1.Chain.TaskID, WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID, IssueID: chain.IssueID}
	claimed2 := DaemonTask{ID: m2.Chain.TaskID, WorkspaceID: chain2.WorkspaceID, ProjectID: chain2.ProjectID, IssueID: chain2.IssueID}

	// Resolve each assignment's own Orca task id, then install per-task
	// mismatched orphans under the client mutex (thread-safe).
	_, task1 := derivedOrcaIDs(a, chain, m1)
	_, task2 := derivedOrcaIDs(a, chain2, m2)
	if task1 == "" || task2 == "" {
		t.Fatalf("derive failed: task1=%q task2=%q", task1, task2)
	}
	a.client.setDispatchForTask(task1, &OrcaDispatch{ID: "ctx_mmaaaaaaaaaaaa1", RunID: "run_ffffffffffff", TaskID: "task_aaaaaaaaaaaa", AssigneeHandle: "term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e", Status: "dispatched"})
	a.client.setDispatchForTask(task2, &OrcaDispatch{ID: "ctx_mmbbbbbbbbbb2", RunID: "run_eeeeeeeeeeee", TaskID: "task_bbbbbbbbbbbb", AssigneeHandle: "term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e", Status: "dispatched"})

	// Expired claims + held barriers force both scopes down the reconcile
	// path concurrently.
	for _, base := range []string{workerStartClaimBase(m1.Chain), workerStartClaimBase(m2.Chain)} {
		if err := a.entry.injectClaimWin(base, "bridge-other", true); err != nil {
			t.Fatal(err)
		}
		if err := a.entry.injectBarrierHeld(effectBarrierKey(base, 0), "bridge-other"); err != nil {
			t.Fatal(err)
		}
	}
	a.ClaimPoll = time.Millisecond
	a.ClaimMaxWait = 200 * time.Millisecond

	errs := make([]error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	go func() { defer wg.Done(); <-start; _, errs[0] = a.RunClaimedTask(t.Context(), claimed1) }()
	go func() { defer wg.Done(); <-start; _, errs[1] = a.RunClaimedTask(t.Context(), claimed2) }()
	close(start)
	wg.Wait()
	for i, err := range errs {
		if !errors.Is(err, ErrResultIdentityMismatch) {
			t.Fatalf("assignment %d: expected ErrResultIdentityMismatch from its own scope, got %v", i, err)
		}
	}
	if a.client.workerStarts != 0 {
		t.Fatalf("no worker may start on mismatch: %d", a.client.workerStarts)
	}
	if len(a.daemon.started) != 0 {
		t.Fatalf("no daemon start may run on mismatch: %d", len(a.daemon.started))
	}
}

// R8: post-claim reconcile with a dispatch whose TaskID is EMPTY fails
// closed before any side effect.
func TestClaimReconcileRejectsEmptyTaskIDDispatch(t *testing.T) {
	a, _ := twinBridges(t)
	chain := validChain()
	mapping, err := a.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID, IssueID: mapping.Chain.IssueID}
	runID, taskID := derivedOrcaIDs(a, chain, mapping)
	if runID == "" {
		t.Fatal("no run id")
	}
	// Existing dispatch with a valid ID and RunID but an EMPTY TaskID.
	a.client.dispatch = &OrcaDispatch{ID: "ctx_emptytaskid01", RunID: runID, TaskID: "", AssigneeHandle: "term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e", Status: "dispatched"}

	if _, err := a.RunClaimedTask(t.Context(), claimed); !errors.Is(err, ErrResultIdentityMismatch) {
		t.Fatalf("expected ErrResultIdentityMismatch for empty TaskID, got %v", err)
	}
	assertNoSideEffects(t, a, chain)
	_ = taskID
}

// R8: a dispatch whose TaskID belongs to a different task fails closed.
func TestClaimReconcileRejectsMismatchedTaskIDDispatch(t *testing.T) {
	a, _ := twinBridges(t)
	chain := validChain()
	mapping, err := a.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID, IssueID: mapping.Chain.IssueID}
	runID, _ := derivedOrcaIDs(a, chain, mapping)
	if runID == "" {
		t.Fatal("no run id")
	}
	a.client.dispatch = &OrcaDispatch{ID: "ctx_othertaskid02", RunID: runID, TaskID: "task_ffffffffffff", AssigneeHandle: "term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e", Status: "dispatched"}

	if _, err := a.RunClaimedTask(t.Context(), claimed); !errors.Is(err, ErrResultIdentityMismatch) {
		t.Fatalf("expected ErrResultIdentityMismatch for mismatched TaskID, got %v", err)
	}
	assertNoSideEffects(t, a, chain)
}

// R8: a dispatch from a different Run fails closed with NO WorkerStart.
func TestClaimReconcileRejectsMismatchedRunIDWithoutWorkerStart(t *testing.T) {
	a, _ := twinBridges(t)
	chain := validChain()
	mapping, err := a.EnsureAssignment(t.Context(), dispatchRef(chain))
	if err != nil {
		t.Fatal(err)
	}
	claimed := DaemonTask{ID: mapping.Chain.TaskID, WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID, IssueID: mapping.Chain.IssueID}
	_, taskID := derivedOrcaIDs(a, chain, mapping)
	if taskID == "" {
		t.Fatal("no task id")
	}
	a.client.dispatch = &OrcaDispatch{ID: "ctx_otherrunid003", RunID: "run_ffffffffffff", TaskID: taskID, AssigneeHandle: "term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e", Status: "dispatched"}

	if _, err := a.RunClaimedTask(t.Context(), claimed); !errors.Is(err, ErrResultIdentityMismatch) {
		t.Fatalf("expected ErrResultIdentityMismatch for mismatched RunID, got %v", err)
	}
	assertNoSideEffects(t, a, chain)
}

// assertNoSideEffects proves a rejected reconcile produced zero downstream
// effects: no worker start, no daemon start, no dispatch evidence, no memo.
func assertNoSideEffects(t *testing.T, tb *testBridge, chain Chain) {
	t.Helper()
	if tb.client.workerStarts != 0 {
		t.Fatalf("worker starts = %d, want 0", tb.client.workerStarts)
	}
	if len(tb.daemon.started) != 0 {
		t.Fatalf("daemon starts = %d, want 0", len(tb.daemon.started))
	}
	// No dispatch evidence may have been appended on the dispatch chain.
	tb.entry.mu.Lock()
	for wf, byKey := range tb.entry.events {
		if wf != "hivecrew://ws/work/prj-dispatch" {
			continue
		}
		for k := range byKey {
			if strings.Contains(k, "dispatch-evidence/") || (strings.Contains(k, "/dispatch/") && !strings.Contains(k, "dispatch-start")) {
				tb.entry.mu.Unlock()
				t.Fatalf("dispatch evidence appended on rejection: %s", k)
			}
		}
	}
	tb.entry.mu.Unlock()
	// No memo adoption for this assignment chain.
	tb.memoMu.Lock()
	_, memoized := tb.memo.dispatch[chain.WorkspaceID+":"+chain.AssignmentID]
	tb.memoMu.Unlock()
	if memoized {
		t.Fatal("rejected dispatch was memoized")
	}
}
