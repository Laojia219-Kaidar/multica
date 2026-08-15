package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// WeChat content production execution bridge — persistent Go tests
// (HIVECREW-WECHAT-REAL-OPERATIONS-V1 / WO-30).
//
// These tests drive the orchestrator against in-memory fakes of the two
// narrow seams (WechatProductionStore, WechatNodeExecutor). They pin the
// fail-closed rules: completed-receipt + own-candidate completion, the
// approval gate, changes_requested blocking downstream, the terminal
// awaiting-publication state (never "published"), idempotent replays, and
// anti-steering checks.
// ---------------------------------------------------------------------------

const wechatExecTestWorkspace = "ws-wechat-exec-test"

// fakeWechatStore is an in-memory WechatProductionStore. AppendEvent dedups
// on (instance, idempotency key) exactly like the canonical Repository.
type fakeWechatStore struct {
	instances map[string]WorkflowInstance
	events    []Event
	versions  map[string]WorkflowDefinitionVersion
}

func newFakeWechatStore() *fakeWechatStore {
	return &fakeWechatStore{
		instances: map[string]WorkflowInstance{},
		versions:  map[string]WorkflowDefinitionVersion{},
	}
}

func wechatStoreKey(workspaceID, id string) string { return workspaceID + "/" + id }

func (s *fakeWechatStore) LoadInstance(_ context.Context, workspaceID string, id string) (WorkflowInstance, error) {
	inst, ok := s.instances[wechatStoreKey(workspaceID, id)]
	if !ok {
		return WorkflowInstance{}, ErrWechatProductionNotFound
	}
	return inst, nil
}

func (s *fakeWechatStore) SaveInstance(_ context.Context, workspaceID string, inst WorkflowInstance) error {
	s.instances[wechatStoreKey(workspaceID, inst.ID)] = inst
	return nil
}

func (s *fakeWechatStore) UpdateInstance(_ context.Context, workspaceID string, inst WorkflowInstance) error {
	s.instances[wechatStoreKey(workspaceID, inst.ID)] = inst
	return nil
}

func (s *fakeWechatStore) AppendEvent(_ context.Context, ev Event) error {
	for _, existing := range s.events {
		if existing.InstanceID == ev.InstanceID && existing.IdempotencyKey == ev.IdempotencyKey {
			return nil
		}
	}
	ev.Sequence = int64(len(s.events) + 1)
	s.events = append(s.events, ev)
	return nil
}

func (s *fakeWechatStore) ListEvents(_ context.Context, _ string, instanceID string) ([]Event, error) {
	out := make([]Event, 0, len(s.events))
	for _, ev := range s.events {
		if ev.InstanceID == instanceID {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (s *fakeWechatStore) LoadEventByIdempotency(_ context.Context, _ string, instanceID string, key string) (Event, error) {
	for _, ev := range s.events {
		if ev.InstanceID == instanceID && ev.IdempotencyKey == key {
			return ev, nil
		}
	}
	return Event{}, ErrWechatProductionNotFound
}

func (s *fakeWechatStore) LoadPublishedVersion(_ context.Context, workspaceID string, definitionID string, version int) (WorkflowDefinitionVersion, error) {
	v, ok := s.versions[fmt.Sprintf("%s/%s/%d", workspaceID, definitionID, version)]
	if !ok {
		return WorkflowDefinitionVersion{}, fmt.Errorf("published version not found")
	}
	return v, nil
}

func (s *fakeWechatStore) countEvents(kind string) int {
	n := 0
	for _, ev := range s.events {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

// fakeReviewCall records one ReviewNodeCandidate invocation.
type fakeReviewCall struct {
	IssueID     string
	CandidateID string
	Decision    WechatProductionReviewDecision
	ReviewID    string
}

// fakeWechatExecutor is an in-memory WechatNodeExecutor. Issue/task IDs are
// deterministic per node key; observations are set by the test.
type fakeWechatExecutor struct {
	obs              map[string]WechatNodeExecutionObservation // taskID -> observation
	ensureCalls      []WechatContentNodeKey
	dispatchCalls    []WechatContentNodeKey
	dispatchNotes    []string
	materializeCalls []string
	reviewCalls      []fakeReviewCall
	ensureErr        error
	dispatchErr      error
	materializeErr   error
	readErr          error
	reviewErr        error
}

func newFakeWechatExecutor() *fakeWechatExecutor {
	return &fakeWechatExecutor{obs: map[string]WechatNodeExecutionObservation{}}
}

func wechatExecTestTaskID(node WechatContentNodeKey) string  { return "task-" + string(node) }
func wechatExecTestIssueID(node WechatContentNodeKey) string { return "issue-" + string(node) }

func (e *fakeWechatExecutor) EnsureNodeIssue(_ context.Context, _ string, plan WechatNodeExecutionPlan) (string, error) {
	if e.ensureErr != nil {
		return "", e.ensureErr
	}
	e.ensureCalls = append(e.ensureCalls, plan.Node.Key)
	return wechatExecTestIssueID(plan.Node.Key), nil
}

func (e *fakeWechatExecutor) DispatchNode(_ context.Context, _ string, plan WechatNodeExecutionPlan, issueID string) (WechatNodeDispatch, error) {
	if e.dispatchErr != nil {
		return WechatNodeDispatch{}, e.dispatchErr
	}
	e.dispatchCalls = append(e.dispatchCalls, plan.Node.Key)
	e.dispatchNotes = append(e.dispatchNotes, plan.HandoffNote)
	return WechatNodeDispatch{
		CommandID: plan.CommandID,
		IssueID:   issueID,
		TaskID:    wechatExecTestTaskID(plan.Node.Key),
	}, nil
}

func (e *fakeWechatExecutor) ReadNodeExecution(_ context.Context, _ string, _ string, taskID string) (WechatNodeExecutionObservation, error) {
	if e.readErr != nil {
		return WechatNodeExecutionObservation{}, e.readErr
	}
	if obs, ok := e.obs[taskID]; ok {
		return obs, nil
	}
	return WechatNodeExecutionObservation{State: "awaiting_claim"}, nil
}

func (e *fakeWechatExecutor) MaterializeNodeCandidate(_ context.Context, _ string, taskID string) (string, error) {
	e.materializeCalls = append(e.materializeCalls, taskID)
	if e.materializeErr != nil {
		return "", e.materializeErr
	}
	return "cand-" + taskID, nil
}

func (e *fakeWechatExecutor) ReviewNodeCandidate(_ context.Context, _ string, issueID string, candidateID string, decision WechatProductionReviewDecision, reviewID string) error {
	if e.reviewErr != nil {
		return e.reviewErr
	}
	e.reviewCalls = append(e.reviewCalls, fakeReviewCall{
		IssueID:     issueID,
		CandidateID: candidateID,
		Decision:    decision,
		ReviewID:    reviewID,
	})
	return nil
}

// wechatExecTestRig bundles a fresh orchestrator with its fakes.
type wechatExecTestRig struct {
	store *fakeWechatStore
	exec  *fakeWechatExecutor
	orch  *WechatProductionOrchestrator
	req   WechatContentProductionRequest
}

func newWechatExecTestRig(t *testing.T) *wechatExecTestRig {
	t.Helper()
	store := newFakeWechatStore()
	req := validWechatContentRequest()
	store.versions[fmt.Sprintf("%s/%s/%d", wechatExecTestWorkspace, req.Definition.DefinitionID, req.Definition.Version)] = WorkflowDefinitionVersion{
		DefinitionID: req.Definition.DefinitionID,
		WorkspaceID:  wechatExecTestWorkspace,
		ProjectID:    req.ProjectID,
		Version:      req.Definition.Version,
		Digest:       req.Definition.Digest,
	}
	exec := newFakeWechatExecutor()
	orch, err := NewWechatProductionOrchestratorWithStore(store, exec)
	if err != nil {
		t.Fatalf("build orchestrator: %v", err)
	}
	return &wechatExecTestRig{store: store, exec: exec, orch: orch, req: req}
}

func (r *wechatExecTestRig) instanceID() string {
	return deriveWechatProductionInstanceID(r.req.IdempotencyKey)
}

func (r *wechatExecTestRig) start(t *testing.T) WechatProductionView {
	t.Helper()
	view, err := r.orch.StartProduction(context.Background(), wechatExecTestWorkspace, r.req, "owner")
	if err != nil {
		t.Fatalf("start production: %v", err)
	}
	return view
}

func (r *wechatExecTestRig) reconcile(t *testing.T) WechatProductionView {
	t.Helper()
	view, err := r.orch.ReconcileProduction(context.Background(), wechatExecTestWorkspace, r.instanceID(), r.req, "owner")
	if err != nil {
		t.Fatalf("reconcile production: %v", err)
	}
	return view
}

// completeNode marks the given node's task completed server-side (receipt +
// optional pre-existing candidate) and reconciles one poll.
func (r *wechatExecTestRig) completeNode(t *testing.T, node WechatContentNodeKey, candidateID string) WechatProductionView {
	t.Helper()
	r.exec.obs[wechatExecTestTaskID(node)] = WechatNodeExecutionObservation{
		State:            "completed",
		ReceiptCompleted: true,
		CandidateID:      candidateID,
	}
	return r.reconcile(t)
}

func wechatViewNode(t *testing.T, view WechatProductionView, node WechatContentNodeKey) WechatNodeLineageRecord {
	t.Helper()
	rec := view.nodePtr(node)
	if rec == nil {
		t.Fatalf("node %q missing from view", node)
	}
	return *rec
}

// ---------------------------------------------------------------------------
// Plan derivation
// ---------------------------------------------------------------------------

func TestDeriveWechatNodeExecutionPlans(t *testing.T) {
	req := validWechatContentRequest()
	plans, err := DeriveWechatNodeExecutionPlans(req)
	if err != nil {
		t.Fatalf("derive plans: %v", err)
	}
	if len(plans) != 4 {
		t.Fatalf("expected four plans, got %d", len(plans))
	}
	wantOrder := []WechatContentNodeKey{
		WechatContentNodeResearchMaterialPackage,
		WechatContentNodeArticleDraft,
		WechatContentNodeEditorialReviewReport,
		WechatContentNodeWechatPublicationPackage,
	}
	for i, plan := range plans {
		if plan.Node.Key != wantOrder[i] {
			t.Errorf("plan %d node = %q, want %q", i, plan.Node.Key, wantOrder[i])
		}
		wantRef := req.Authority.WorkOrderSourceRef + "--" + string(wantOrder[i])
		if plan.WorkOrderSourceRef != wantRef {
			t.Errorf("plan %d work order ref = %q, want %q", i, plan.WorkOrderSourceRef, wantRef)
		}
		if !wechatWorkOrderSourceRefPattern.MatchString(plan.WorkOrderSourceRef) {
			t.Errorf("plan %d derived ref %q violates the frozen pattern", i, plan.WorkOrderSourceRef)
		}
		if plan.CommandID == "" {
			t.Errorf("plan %d has an empty command id", i)
		}
	}

	// Command IDs are deterministic per (idempotency key, node).
	again, err := DeriveWechatNodeExecutionPlans(req)
	if err != nil {
		t.Fatalf("re-derive plans: %v", err)
	}
	for i := range plans {
		if plans[i].CommandID != again[i].CommandID {
			t.Errorf("plan %d command id is not deterministic", i)
		}
	}
	other := req
	other.IdempotencyKey = "req-2"
	otherPlans, err := DeriveWechatNodeExecutionPlans(other)
	if err != nil {
		t.Fatalf("derive plans for other key: %v", err)
	}
	for i := range plans {
		if plans[i].CommandID == otherPlans[i].CommandID {
			t.Errorf("plan %d command id collides across idempotency keys", i)
		}
	}
}

func TestDeriveWechatNodeExecutionPlansRejectsInvalid(t *testing.T) {
	req := validWechatContentRequest()
	req.Brief.Subject = ""
	if _, err := DeriveWechatNodeExecutionPlans(req); err == nil {
		t.Fatalf("expected an invalid request to be rejected")
	}

	// A base ref at the pattern's length limit passes request validation, but
	// the derived per-node ref would exceed it: derivation must fail closed.
	req = validWechatContentRequest()
	req.Authority.WorkOrderSourceRef = "hive://hivecosm/delivery/project/PRJ-WECHAT-OPS/work-order/" + strings.Repeat("w", 192)
	if _, err := DeriveWechatNodeExecutionPlans(req); err == nil {
		t.Fatalf("expected an over-length derived node work order ref to fail closed")
	}
}

// ---------------------------------------------------------------------------
// Definition pin
// ---------------------------------------------------------------------------

func TestStartProductionDefinitionPin(t *testing.T) {
	t.Run("missing version", func(t *testing.T) {
		rig := newWechatExecTestRig(t)
		rig.req.Definition.Version = 99
		_, err := rig.orch.StartProduction(context.Background(), wechatExecTestWorkspace, rig.req, "owner")
		if !errors.Is(err, ErrWechatDefinitionPin) {
			t.Fatalf("err = %v, want ErrWechatDefinitionPin", err)
		}
	})
	t.Run("digest mismatch", func(t *testing.T) {
		rig := newWechatExecTestRig(t)
		rig.req.Definition.Digest = "sha256:" + strings.Repeat("b", 64)
		_, err := rig.orch.StartProduction(context.Background(), wechatExecTestWorkspace, rig.req, "owner")
		if !errors.Is(err, ErrWechatDefinitionPin) {
			t.Fatalf("err = %v, want ErrWechatDefinitionPin", err)
		}
	})
	t.Run("cross project", func(t *testing.T) {
		// The request itself stays valid; the published version is scoped to a
		// different Project, so pinning it would cross the Project boundary.
		rig := newWechatExecTestRig(t)
		key := fmt.Sprintf("%s/%s/%d", wechatExecTestWorkspace, rig.req.Definition.DefinitionID, rig.req.Definition.Version)
		v := rig.store.versions[key]
		v.ProjectID = "PRJ-OTHER"
		rig.store.versions[key] = v
		_, err := rig.orch.StartProduction(context.Background(), wechatExecTestWorkspace, rig.req, "owner")
		if !errors.Is(err, ErrWechatDefinitionPin) {
			t.Fatalf("err = %v, want ErrWechatDefinitionPin", err)
		}
	})
	t.Run("unscoped version", func(t *testing.T) {
		rig := newWechatExecTestRig(t)
		key := fmt.Sprintf("%s/%s/%d", wechatExecTestWorkspace, rig.req.Definition.DefinitionID, rig.req.Definition.Version)
		v := rig.store.versions[key]
		v.ProjectID = ""
		rig.store.versions[key] = v
		_, err := rig.orch.StartProduction(context.Background(), wechatExecTestWorkspace, rig.req, "owner")
		if !errors.Is(err, ErrWechatDefinitionPin) {
			t.Fatalf("err = %v, want ErrWechatDefinitionPin", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Start, replay, and conflict
// ---------------------------------------------------------------------------

func TestStartProductionDispatchesFirstNode(t *testing.T) {
	rig := newWechatExecTestRig(t)
	view := rig.start(t)

	if view.Status != StatusRunning {
		t.Errorf("status = %q, want running", view.Status)
	}
	if view.CurrentNode != WechatContentNodeResearchMaterialPackage {
		t.Errorf("current node = %q, want %q", view.CurrentNode, WechatContentNodeResearchMaterialPackage)
	}
	rec := wechatViewNode(t, view, WechatContentNodeResearchMaterialPackage)
	if rec.State != "dispatched" {
		t.Errorf("node 1 state = %q, want dispatched", rec.State)
	}
	if rec.CommandID == "" || rec.IssueID == "" || rec.TaskID == "" {
		t.Errorf("node 1 lineage incomplete: %+v", rec)
	}
	if len(rig.exec.dispatchCalls) != 1 || rig.exec.dispatchCalls[0] != WechatContentNodeResearchMaterialPackage {
		t.Fatalf("dispatch calls = %v, want exactly node 1", rig.exec.dispatchCalls)
	}
	note := rig.exec.dispatchNotes[0]
	if !strings.Contains(note, wechatNodeDirectives[WechatContentNodeResearchMaterialPackage]) {
		t.Errorf("node 1 handoff note misses the frozen directive")
	}
	if !strings.Contains(note, rig.req.Brief.Subject) || !strings.Contains(note, rig.req.Brief.HandoffNote) {
		t.Errorf("node 1 handoff note misses the brief")
	}
	if strings.Contains(note, "Upstream node artifact") {
		t.Errorf("node 1 handoff note must not contain an upstream section")
	}
	for _, node := range []WechatContentNodeKey{WechatContentNodeArticleDraft, WechatContentNodeEditorialReviewReport, WechatContentNodeWechatPublicationPackage} {
		if rec := wechatViewNode(t, view, node); rec.State != "pending" {
			t.Errorf("node %q state = %q, want pending", node, rec.State)
		}
	}
	if view.ApprovalState != "none" || view.PublicationState != "none" {
		t.Errorf("approval/publication = %q/%q, want none/none", view.ApprovalState, view.PublicationState)
	}
}

func TestStartProductionReplayDoesNotRedispatch(t *testing.T) {
	rig := newWechatExecTestRig(t)
	rig.start(t)
	view, err := rig.orch.StartProduction(context.Background(), wechatExecTestWorkspace, rig.req, "owner")
	if err != nil {
		t.Fatalf("replayed start: %v", err)
	}
	if len(rig.exec.dispatchCalls) != 1 {
		t.Errorf("dispatch calls = %d, want 1 after replay", len(rig.exec.dispatchCalls))
	}
	if rig.store.countEvents(wechatEventStartKind) != 1 {
		t.Errorf("start events = %d, want 1", rig.store.countEvents(wechatEventStartKind))
	}
	if rig.store.countEvents(WechatEventNodeDispatched) != 1 {
		t.Errorf("dispatch events = %d, want 1", rig.store.countEvents(WechatEventNodeDispatched))
	}
	if view.Status != StatusRunning {
		t.Errorf("status = %q, want running", view.Status)
	}
}

func TestStartProductionIdempotencyConflict(t *testing.T) {
	rig := newWechatExecTestRig(t)
	rig.start(t)

	// A replay under the same idempotency key but pinning a different
	// definition version (with that version published under the same Project,
	// so the pin check itself passes) must conflict, never steer the recorded
	// production.
	replay := rig.req
	replay.Definition.Version = 2
	replay.Definition.Digest = "sha256:" + strings.Repeat("c", 64)
	rig.store.versions[fmt.Sprintf("%s/%s/2", wechatExecTestWorkspace, replay.Definition.DefinitionID)] = WorkflowDefinitionVersion{
		DefinitionID: replay.Definition.DefinitionID,
		WorkspaceID:  wechatExecTestWorkspace,
		ProjectID:    replay.ProjectID,
		Version:      2,
		Digest:       replay.Definition.Digest,
	}

	_, err := rig.orch.StartProduction(context.Background(), wechatExecTestWorkspace, replay, "owner")
	if !errors.Is(err, ErrWechatProductionConflict) {
		t.Fatalf("err = %v, want ErrWechatProductionConflict", err)
	}
}

// ---------------------------------------------------------------------------
// Node completion and advancement
// ---------------------------------------------------------------------------

func TestNodeCompletionAdvancesAndDispatchesNext(t *testing.T) {
	rig := newWechatExecTestRig(t)
	rig.start(t)

	view := rig.completeNode(t, WechatContentNodeResearchMaterialPackage, "cand-research-1")

	node1 := wechatViewNode(t, view, WechatContentNodeResearchMaterialPackage)
	if node1.State != "completed" || node1.CandidateID != "cand-research-1" {
		t.Errorf("node 1 = %+v, want completed with cand-research-1", node1)
	}
	node2 := wechatViewNode(t, view, WechatContentNodeArticleDraft)
	if node2.State != "dispatched" {
		t.Errorf("node 2 state = %q, want dispatched in the same pass", node2.State)
	}
	if len(rig.exec.dispatchCalls) != 2 {
		t.Fatalf("dispatch calls = %v, want nodes 1 and 2", rig.exec.dispatchCalls)
	}
	note := rig.exec.dispatchNotes[1]
	if !strings.Contains(note, "cand-research-1") {
		t.Errorf("node 2 handoff note misses the upstream candidate lineage")
	}
	if !strings.Contains(note, wechatExecTestIssueID(WechatContentNodeResearchMaterialPackage)) ||
		!strings.Contains(note, wechatExecTestTaskID(WechatContentNodeResearchMaterialPackage)) {
		t.Errorf("node 2 handoff note misses the upstream issue/task lineage")
	}
	inst, err := rig.store.LoadInstance(context.Background(), wechatExecTestWorkspace, rig.instanceID())
	if err != nil {
		t.Fatalf("load instance: %v", err)
	}
	if inst.StageIndex != 2 {
		t.Errorf("stage index = %d, want 2", inst.StageIndex)
	}

	// A further poll while node 2 is still running must not duplicate the
	// completion or the dispatch.
	rig.reconcile(t)
	if rig.store.countEvents(WechatEventNodeCompleted) != 1 {
		t.Errorf("completion events = %d, want 1", rig.store.countEvents(WechatEventNodeCompleted))
	}
	if len(rig.exec.dispatchCalls) != 2 {
		t.Errorf("dispatch calls = %d, want 2 after idle poll", len(rig.exec.dispatchCalls))
	}
}

func TestNodeCompletionMaterializesMissingCandidate(t *testing.T) {
	rig := newWechatExecTestRig(t)
	rig.start(t)

	// The daemon hook did not materialize: the bridge must do it through the
	// existing materialization path before accepting completion.
	view := rig.completeNode(t, WechatContentNodeResearchMaterialPackage, "")
	if len(rig.exec.materializeCalls) != 1 ||
		rig.exec.materializeCalls[0] != wechatExecTestTaskID(WechatContentNodeResearchMaterialPackage) {
		t.Fatalf("materialize calls = %v, want node 1 task", rig.exec.materializeCalls)
	}
	node1 := wechatViewNode(t, view, WechatContentNodeResearchMaterialPackage)
	if node1.State != "completed" ||
		node1.CandidateID != "cand-"+wechatExecTestTaskID(WechatContentNodeResearchMaterialPackage) {
		t.Errorf("node 1 = %+v, want completed with the materialized candidate", node1)
	}
}

// ---------------------------------------------------------------------------
// Approval gate
// ---------------------------------------------------------------------------

// driveToApprovalGate completes nodes 1-3 and returns the halted view.
func driveToApprovalGate(t *testing.T, rig *wechatExecTestRig) WechatProductionView {
	t.Helper()
	rig.start(t)
	rig.completeNode(t, WechatContentNodeResearchMaterialPackage, "cand-research-1")
	rig.completeNode(t, WechatContentNodeArticleDraft, "cand-draft-1")
	return rig.completeNode(t, WechatContentNodeEditorialReviewReport, "cand-review-1")
}

func TestApprovalGateHaltsAndApprovalRearms(t *testing.T) {
	rig := newWechatExecTestRig(t)
	view := driveToApprovalGate(t, rig)

	if view.Status != StatusPaused {
		t.Fatalf("status = %q, want paused at the approval gate", view.Status)
	}
	if view.ApprovalState != "awaiting" {
		t.Errorf("approval state = %q, want awaiting", view.ApprovalState)
	}
	if len(rig.exec.dispatchCalls) != 3 {
		t.Fatalf("dispatch calls = %v, want exactly nodes 1-3", rig.exec.dispatchCalls)
	}

	// While paused, reconcile must not dispatch the publication package.
	rig.reconcile(t)
	if len(rig.exec.dispatchCalls) != 3 {
		t.Fatalf("paused reconcile dispatched: %v", rig.exec.dispatchCalls)
	}

	// Owner approval re-arms the production; the next poll dispatches node 4.
	view, err := rig.orch.ReviewProduction(context.Background(), wechatExecTestWorkspace, rig.instanceID(),
		WechatReviewApproved, "33333333-3333-4333-8333-333333333333", "owner")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if view.Status != StatusRunning || view.ApprovalState != "approved" {
		t.Errorf("after approval status/approval = %q/%q, want running/approved", view.Status, view.ApprovalState)
	}
	if len(rig.exec.reviewCalls) != 1 {
		t.Fatalf("review calls = %v, want 1", rig.exec.reviewCalls)
	}
	call := rig.exec.reviewCalls[0]
	if call.IssueID != wechatExecTestIssueID(WechatContentNodeEditorialReviewReport) ||
		call.CandidateID != "cand-review-1" ||
		call.Decision != WechatReviewApproved {
		t.Errorf("review call = %+v, want the node 3 candidate approved", call)
	}

	view = rig.reconcile(t)
	if len(rig.exec.dispatchCalls) != 4 ||
		rig.exec.dispatchCalls[3] != WechatContentNodeWechatPublicationPackage {
		t.Fatalf("dispatch calls = %v, want node 4 dispatched after approval", rig.exec.dispatchCalls)
	}
	if !strings.Contains(rig.exec.dispatchNotes[3], "cand-review-1") {
		t.Errorf("node 4 handoff note misses the approved review lineage")
	}
	node4 := wechatViewNode(t, view, WechatContentNodeWechatPublicationPackage)
	if node4.State != "dispatched" {
		t.Errorf("node 4 state = %q, want dispatched", node4.State)
	}
}

func TestChangesRequestedBlocksDownstream(t *testing.T) {
	rig := newWechatExecTestRig(t)
	driveToApprovalGate(t, rig)

	view, err := rig.orch.ReviewProduction(context.Background(), wechatExecTestWorkspace, rig.instanceID(),
		WechatReviewChangesRequested, "44444444-4444-4444-8444-444444444444", "owner")
	if err != nil {
		t.Fatalf("changes_requested: %v", err)
	}
	if view.Status != StatusPaused || view.ApprovalState != "changes_requested" {
		t.Errorf("status/approval = %q/%q, want paused/changes_requested", view.Status, view.ApprovalState)
	}

	// The publication package stays blocked while the gate is rejected.
	rig.reconcile(t)
	if len(rig.exec.dispatchCalls) != 3 {
		t.Fatalf("changes_requested reconcile dispatched: %v", rig.exec.dispatchCalls)
	}

	// A later approval (new review id) re-arms the gate.
	if _, err := rig.orch.ReviewProduction(context.Background(), wechatExecTestWorkspace, rig.instanceID(),
		WechatReviewApproved, "55555555-5555-4555-8555-555555555555", "owner"); err != nil {
		t.Fatalf("second-decision approval: %v", err)
	}
	rig.reconcile(t)
	if len(rig.exec.dispatchCalls) != 4 {
		t.Fatalf("dispatch calls = %v, want node 4 dispatched after the later approval", rig.exec.dispatchCalls)
	}
}

// ---------------------------------------------------------------------------
// Terminal state: awaiting publication, never published
// ---------------------------------------------------------------------------

func TestPublicationPackageTerminalAwaitingPublication(t *testing.T) {
	rig := newWechatExecTestRig(t)
	driveToApprovalGate(t, rig)
	if _, err := rig.orch.ReviewProduction(context.Background(), wechatExecTestWorkspace, rig.instanceID(),
		WechatReviewApproved, "33333333-3333-4333-8333-333333333333", "owner"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	rig.reconcile(t)

	view := rig.completeNode(t, WechatContentNodeWechatPublicationPackage, "cand-package-1")
	if view.Status != StatusCompleted {
		t.Fatalf("status = %q, want completed", view.Status)
	}
	if view.PublicationState != "awaiting_publication" {
		t.Errorf("publication state = %q, want awaiting_publication", view.PublicationState)
	}
	if view.PublicationState == "published" {
		t.Fatalf("publication state must never be \"published\" without a platform receipt")
	}
	node4 := wechatViewNode(t, view, WechatContentNodeWechatPublicationPackage)
	if node4.State != "completed" || node4.CandidateID != "cand-package-1" {
		t.Errorf("node 4 = %+v, want completed with cand-package-1", node4)
	}
	inst, err := rig.store.LoadInstance(context.Background(), wechatExecTestWorkspace, rig.instanceID())
	if err != nil {
		t.Fatalf("load instance: %v", err)
	}
	if inst.StageIndex != 4 {
		t.Errorf("stage index = %d, want 4", inst.StageIndex)
	}

	// The publication-package candidate stays reviewable for the WO-40B
	// promotion path.
	if _, err := rig.orch.ReviewProduction(context.Background(), wechatExecTestWorkspace, rig.instanceID(),
		WechatReviewApproved, "66666666-6666-4666-8666-666666666666", "owner"); err != nil {
		t.Fatalf("review publication package: %v", err)
	}
	last := rig.exec.reviewCalls[len(rig.exec.reviewCalls)-1]
	if last.CandidateID != "cand-package-1" {
		t.Errorf("publication review candidate = %q, want cand-package-1", last.CandidateID)
	}
}

// ---------------------------------------------------------------------------
// Fail-closed halts
// ---------------------------------------------------------------------------

func TestNodeFailClosedHalts(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(exec *fakeWechatExecutor)
		want    string
		wantErr bool
	}{
		{
			name: "run failed",
			setup: func(exec *fakeWechatExecutor) {
				exec.obs[wechatExecTestTaskID(WechatContentNodeResearchMaterialPackage)] = WechatNodeExecutionObservation{State: "failed"}
			},
			want: "run_failed",
		},
		{
			name: "run cancelled",
			setup: func(exec *fakeWechatExecutor) {
				exec.obs[wechatExecTestTaskID(WechatContentNodeResearchMaterialPackage)] = WechatNodeExecutionObservation{State: "cancelled"}
			},
			want: "run_cancelled",
		},
		{
			name: "receipt missing",
			setup: func(exec *fakeWechatExecutor) {
				exec.obs[wechatExecTestTaskID(WechatContentNodeResearchMaterialPackage)] = WechatNodeExecutionObservation{State: "completed", ReceiptCompleted: false}
			},
			want: "receipt_missing",
		},
		{
			name: "materialize failed",
			setup: func(exec *fakeWechatExecutor) {
				exec.materializeErr = errors.New("blank output")
			},
			want: "materialize_failed",
		},
		{
			name: "dispatch authority rejected",
			setup: func(exec *fakeWechatExecutor) {
				exec.dispatchErr = fmt.Errorf("no active identity binding: %w", ErrWechatNodeAuthorityRejected)
			},
			want: "authority_rejected",
		},
		{
			name: "issue authority rejected",
			setup: func(exec *fakeWechatExecutor) {
				exec.ensureErr = fmt.Errorf("cross-project work order: %w", ErrWechatNodeAuthorityRejected)
			},
			want: "authority_rejected",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newWechatExecTestRig(t)
			tc.setup(rig.exec)

			var view WechatProductionView
			var err error
			if tc.name == "materialize failed" {
				rig.start(t)
				rig.exec.obs[wechatExecTestTaskID(WechatContentNodeResearchMaterialPackage)] = WechatNodeExecutionObservation{
					State: "completed", ReceiptCompleted: true,
				}
				view, err = rig.orch.ReconcileProduction(context.Background(), wechatExecTestWorkspace, rig.instanceID(), rig.req, "owner")
			} else {
				view, err = rig.orch.StartProduction(context.Background(), wechatExecTestWorkspace, rig.req, "owner")
			}
			if err != nil {
				t.Fatalf("fail-closed halt must return a view, not an error: %v", err)
			}
			if view.Status != StatusFailed {
				t.Errorf("status = %q, want failed", view.Status)
			}
			node1 := wechatViewNode(t, view, WechatContentNodeResearchMaterialPackage)
			if node1.State != "failed" || node1.Failure != tc.want {
				t.Errorf("node 1 = %+v, want failed with reason %q", node1, tc.want)
			}
			if node1.CandidateID != "" {
				t.Errorf("a failed node must never carry a candidate, got %q", node1.CandidateID)
			}

			// The halt is terminal and idempotent: further polls change nothing.
			rig.reconcile(t)
			if rig.store.countEvents(WechatEventNodeFailed) != 1 {
				t.Errorf("failed events = %d, want 1", rig.store.countEvents(WechatEventNodeFailed))
			}
		})
	}
}

func TestTransientDispatchErrorDoesNotFailProduction(t *testing.T) {
	rig := newWechatExecTestRig(t)
	rig.exec.dispatchErr = errors.New("transient infrastructure error")

	if _, err := rig.orch.StartProduction(context.Background(), wechatExecTestWorkspace, rig.req, "owner"); err == nil {
		t.Fatalf("expected the transient dispatch error to propagate")
	}
	inst, err := rig.store.LoadInstance(context.Background(), wechatExecTestWorkspace, rig.instanceID())
	if err != nil {
		t.Fatalf("load instance: %v", err)
	}
	if inst.Status != StatusRunning {
		t.Errorf("status = %q, want running after a transient error", inst.Status)
	}
	if rig.store.countEvents(WechatEventNodeFailed) != 0 {
		t.Errorf("a transient error must not record a node failure")
	}

	// The next poll retries the dispatch and succeeds.
	rig.exec.dispatchErr = nil
	view := rig.reconcile(t)
	if len(rig.exec.dispatchCalls) != 1 {
		t.Fatalf("dispatch calls = %v, want the retried node 1 dispatch", rig.exec.dispatchCalls)
	}
	if rec := wechatViewNode(t, view, WechatContentNodeResearchMaterialPackage); rec.State != "dispatched" {
		t.Errorf("node 1 state = %q, want dispatched after retry", rec.State)
	}
}

// ---------------------------------------------------------------------------
// Review validation and idempotency
// ---------------------------------------------------------------------------

func TestReviewProductionValidation(t *testing.T) {
	t.Run("invalid decision", func(t *testing.T) {
		rig := newWechatExecTestRig(t)
		rig.start(t)
		_, err := rig.orch.ReviewProduction(context.Background(), wechatExecTestWorkspace, rig.instanceID(),
			"maybe", "33333333-3333-4333-8333-333333333333", "owner")
		if err == nil {
			t.Fatalf("expected an invalid decision to be rejected")
		}
	})
	t.Run("non uuid review id", func(t *testing.T) {
		rig := newWechatExecTestRig(t)
		rig.start(t)
		_, err := rig.orch.ReviewProduction(context.Background(), wechatExecTestWorkspace, rig.instanceID(),
			WechatReviewApproved, "not-a-uuid", "owner")
		if err == nil {
			t.Fatalf("expected a non-UUID review id to be rejected")
		}
	})
	t.Run("unavailable while running", func(t *testing.T) {
		rig := newWechatExecTestRig(t)
		rig.start(t)
		_, err := rig.orch.ReviewProduction(context.Background(), wechatExecTestWorkspace, rig.instanceID(),
			WechatReviewApproved, "33333333-3333-4333-8333-333333333333", "owner")
		if !errors.Is(err, ErrWechatReviewUnavailable) {
			t.Fatalf("err = %v, want ErrWechatReviewUnavailable", err)
		}
	})
	t.Run("replay does not double record", func(t *testing.T) {
		rig := newWechatExecTestRig(t)
		driveToApprovalGate(t, rig)
		reviewID := "33333333-3333-4333-8333-333333333333"
		if _, err := rig.orch.ReviewProduction(context.Background(), wechatExecTestWorkspace, rig.instanceID(),
			WechatReviewApproved, reviewID, "owner"); err != nil {
			t.Fatalf("approve: %v", err)
		}
		if _, err := rig.orch.ReviewProduction(context.Background(), wechatExecTestWorkspace, rig.instanceID(),
			WechatReviewApproved, reviewID, "owner"); err != nil {
			t.Fatalf("replayed review must succeed as a no-op: %v", err)
		}
		if len(rig.exec.reviewCalls) != 1 {
			t.Errorf("review calls = %d, want 1 after replay", len(rig.exec.reviewCalls))
		}
		if rig.store.countEvents(WechatEventApproved) != 1 {
			t.Errorf("approved events = %d, want 1", rig.store.countEvents(WechatEventApproved))
		}
	})
}

// ---------------------------------------------------------------------------
// Anti-steering and read model
// ---------------------------------------------------------------------------

func TestReconcileProductionAntiSteering(t *testing.T) {
	rig := newWechatExecTestRig(t)
	rig.start(t)

	t.Run("foreign idempotency key", func(t *testing.T) {
		foreign := rig.req
		foreign.IdempotencyKey = "req-foreign"
		_, err := rig.orch.ReconcileProduction(context.Background(), wechatExecTestWorkspace, rig.instanceID(), foreign, "owner")
		if !errors.Is(err, ErrWechatProductionConflict) {
			t.Fatalf("err = %v, want ErrWechatProductionConflict", err)
		}
	})
	t.Run("mismatched definition pin", func(t *testing.T) {
		other := rig.req
		other.Definition.Version = 2
		_, err := rig.orch.ReconcileProduction(context.Background(), wechatExecTestWorkspace, rig.instanceID(), other, "owner")
		if !errors.Is(err, ErrWechatProductionConflict) {
			t.Fatalf("err = %v, want ErrWechatProductionConflict", err)
		}
	})
	t.Run("unknown instance", func(t *testing.T) {
		unknown := deriveWechatProductionInstanceID("req-unknown")
		_, err := rig.orch.ReconcileProduction(context.Background(), wechatExecTestWorkspace, unknown, rig.req, "owner")
		if err == nil {
			t.Fatalf("expected a foreign instance id to be rejected")
		}
	})
}

func TestGetProductionReflectsFreshObservation(t *testing.T) {
	rig := newWechatExecTestRig(t)
	rig.start(t)

	rig.exec.obs[wechatExecTestTaskID(WechatContentNodeResearchMaterialPackage)] = WechatNodeExecutionObservation{State: "running"}
	view, err := rig.orch.GetProduction(context.Background(), wechatExecTestWorkspace, rig.instanceID())
	if err != nil {
		t.Fatalf("get production: %v", err)
	}
	rec := wechatViewNode(t, view, WechatContentNodeResearchMaterialPackage)
	if rec.LiveState != "running" {
		t.Errorf("live state = %q, want the fresh server-side observation", rec.LiveState)
	}
	if len(rig.exec.dispatchCalls) != 1 {
		t.Errorf("get must not write: dispatch calls = %d", len(rig.exec.dispatchCalls))
	}

	_, err = rig.orch.GetProduction(context.Background(), wechatExecTestWorkspace, deriveWechatProductionInstanceID("req-unknown"))
	if !errors.Is(err, ErrWechatProductionNotFound) {
		t.Fatalf("err = %v, want ErrWechatProductionNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Handoff note composition
// ---------------------------------------------------------------------------

func TestComposeWechatNodeHandoffNote(t *testing.T) {
	nodes := WechatContentNodeContracts()
	brief := validWechatContentRequest().Brief

	t.Run("first node without upstream", func(t *testing.T) {
		note, err := ComposeWechatNodeHandoffNote(brief, nodes[0], nil)
		if err != nil {
			t.Fatalf("compose node 1 note: %v", err)
		}
		for _, want := range []string{
			wechatNodeDirectives[nodes[0].Key],
			"- Subject: " + brief.Subject,
			"- Deadline: " + brief.Deadline,
			brief.SourceRefs[0],
			brief.HandoffNote,
		} {
			if !strings.Contains(note, want) {
				t.Errorf("node 1 note misses %q", want)
			}
		}
	})
	t.Run("required upstream missing", func(t *testing.T) {
		if _, err := ComposeWechatNodeHandoffNote(brief, nodes[1], nil); err == nil {
			t.Fatalf("expected node 2 without upstream lineage to be rejected")
		}
	})
	t.Run("wrong upstream node", func(t *testing.T) {
		upstream := &WechatNodeLineageRecord{
			Node:        WechatContentNodeEditorialReviewReport,
			State:       "completed",
			CandidateID: "cand-x",
		}
		if _, err := ComposeWechatNodeHandoffNote(brief, nodes[1], upstream); err == nil {
			t.Fatalf("expected a mismatched upstream lineage to be rejected")
		}
	})
	t.Run("upstream not completed", func(t *testing.T) {
		upstream := &WechatNodeLineageRecord{Node: WechatContentNodeResearchMaterialPackage, State: "dispatched"}
		if _, err := ComposeWechatNodeHandoffNote(brief, nodes[1], upstream); err == nil {
			t.Fatalf("expected an incomplete upstream lineage to be rejected")
		}
	})
	t.Run("completed upstream embeds lineage", func(t *testing.T) {
		upstream := &WechatNodeLineageRecord{
			Node:        WechatContentNodeResearchMaterialPackage,
			State:       "completed",
			IssueID:     "issue-1",
			TaskID:      "task-1",
			CandidateID: "cand-1",
		}
		note, err := ComposeWechatNodeHandoffNote(brief, nodes[1], upstream)
		if err != nil {
			t.Fatalf("compose node 2 note: %v", err)
		}
		for _, want := range []string{"- Candidate: cand-1", "- Issue: issue-1", "- Task: task-1"} {
			if !strings.Contains(note, want) {
				t.Errorf("node 2 note misses %q", want)
			}
		}
	})
	t.Run("unknown node directive", func(t *testing.T) {
		if _, err := ComposeWechatNodeHandoffNote(brief, WechatContentNodeContract{Key: "nope"}, nil); err == nil {
			t.Fatalf("expected an unknown node to be rejected")
		}
	})
	t.Run("oversize note fails closed", func(t *testing.T) {
		big := brief
		big.HandoffNote = strings.Repeat("x", WechatContentHandoffNoteMaxBytes)
		if _, err := ComposeWechatNodeHandoffNote(big, nodes[0], nil); err == nil {
			t.Fatalf("expected an over-limit handoff note to fail closed")
		}
	})
}
