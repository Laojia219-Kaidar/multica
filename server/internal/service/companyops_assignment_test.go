package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/companyops"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	assignmentWorkOrderRef = "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-ASSIGNMENT-001"
	assignmentEmployeeRef  = "hivecosm://employees/EMP-ASSIGNMENT-001"
	assignmentBindingRef   = "hivecosm://identity-bindings/BIND-ASSIGNMENT-001"
	assignmentAgentRef     = "/api/agents/00000000-0000-4000-8000-000000000004"
)

var (
	errAssignmentLock    = errors.New("assignment command lock failed")
	errAssignmentLookup  = errors.New("assignment receipt lookup failed")
	errAssignmentWrite   = errors.New("exact issue assignment failed")
	errAssignmentEnqueue = errors.New("assignment task enqueue failed")
	errAssignmentReceipt = errors.New("assignment receipt append failed")
	errAssignmentCommit  = errors.New("assignment transaction commit failed")
)

func assignmentUUID(seed byte) pgtype.UUID {
	var value [16]byte
	value[6] = 0x40
	value[8] = 0x80
	value[15] = seed
	return pgtype.UUID{Bytes: value, Valid: true}
}

func assignmentDigest(ch string) string {
	return "sha256:" + strings.Repeat(ch, 64)
}

func assignmentAuthority(kind, sourceRef, revision, digestChar string) companyops.AuthoritySnapshot {
	return companyops.AuthoritySnapshot{
		Kind:          kind,
		SourceRef:     sourceRef,
		Revision:      revision,
		ContentDigest: assignmentDigest(digestChar),
		Freshness:     "current",
	}
}

func validCompanyOpsAssignmentRequest() CompanyOpsAssignmentRequest {
	return CompanyOpsAssignmentRequest{
		CommandID:           assignmentUUID(1),
		WorkspaceID:         assignmentUUID(2),
		IssueID:             assignmentUUID(3),
		LocalAgentID:        assignmentUUID(4),
		LocalAgentSourceRef: assignmentAgentRef,
		ActorUserID:         assignmentUUID(5),
		HandoffNote:         "Build the bounded P2 outcome and preserve exact receipts.",
		WorkOrder: assignmentAuthority(
			"WorkOrder",
			assignmentWorkOrderRef,
			"wo-rev-7",
			"a",
		),
		InputDigest: CompanyOpsHandoffInputDigest("Build the bounded P2 outcome and preserve exact receipts."),
		Employee: assignmentAuthority(
			"Employee",
			assignmentEmployeeRef,
			"employee-rev-5",
			"c",
		),
		Bindings: []companyops.IdentityBinding{
			{
				Authority: assignmentAuthority(
					"IdentityBinding",
					assignmentBindingRef,
					"binding-rev-11",
					"d",
				),
				EmployeeRef: assignmentEmployeeRef,
				AgentRef:    assignmentAgentRef,
				Active:      true,
			},
		},
		Agents: []companyops.AuthoritySnapshot{
			assignmentAuthority(
				"Agent",
				assignmentAgentRef,
				"agent-rev-19",
				"e",
			),
		},
	}
}

type fakeCompanyOpsAssignmentBackend struct {
	committedReceipts map[[16]byte]AssignmentDispatchReceipt
	committedTasks    map[[16]byte]db.AgentTaskQueue
	log               []string
	failAt            string
	beginCount        int
	assignCount       int
	enqueueCount      int
	appendCount       int
	publishCount      int
	notifyCount       int
	lastEvidenceKind  string
	lastEvidenceRefID pgtype.UUID
	ensureIssue       db.Issue
	ensureErr         error
	finishCount       int
}

func newFakeCompanyOpsAssignmentBackend() *fakeCompanyOpsAssignmentBackend {
	return &fakeCompanyOpsAssignmentBackend{
		committedReceipts: make(map[[16]byte]AssignmentDispatchReceipt),
		committedTasks:    make(map[[16]byte]db.AgentTaskQueue),
	}
}

func (b *fakeCompanyOpsAssignmentBackend) RunInCompanyOpsAssignmentTx(
	ctx context.Context,
	fn func(CompanyOpsAssignmentTx) error,
) error {
	b.beginCount++
	b.log = append(b.log, "begin")
	tx := &fakeCompanyOpsAssignmentTx{backend: b}
	if err := fn(tx); err != nil {
		b.log = append(b.log, "rollback")
		return err
	}
	if b.failAt == "commit" {
		b.log = append(b.log, "commit_failed")
		return errAssignmentCommit
	}
	for key, receipt := range tx.receipts {
		b.committedReceipts[key] = receipt
	}
	for key, task := range tx.tasks {
		b.committedTasks[key] = task
	}
	b.assignCount += tx.assignCount
	b.enqueueCount += tx.enqueueCount
	b.appendCount += tx.appendCount
	b.log = append(b.log, "commit")
	return nil
}

func (b *fakeCompanyOpsAssignmentBackend) PublishAssignmentDispatched(_ context.Context, _ AssignmentDispatchReceipt) {
	b.publishCount++
	b.log = append(b.log, "publish")
}

func (b *fakeCompanyOpsAssignmentBackend) NotifyAssignmentTaskAvailable(_ context.Context, _ db.AgentTaskQueue) {
	b.notifyCount++
	b.log = append(b.log, "notify")
}

func (b *fakeCompanyOpsAssignmentBackend) FinishWorkOrderProjection(_ context.Context, _ CompanyOpsWorkOrderProjection) {
	b.finishCount++
}

type fakeCompanyOpsAssignmentTx struct {
	backend           *fakeCompanyOpsAssignmentBackend
	receipts          map[[16]byte]AssignmentDispatchReceipt
	tasks             map[[16]byte]db.AgentTaskQueue
	assignCount       int
	enqueueCount      int
	appendCount       int
	createdProjection *CompanyOpsWorkOrderProjection
}

func (tx *fakeCompanyOpsAssignmentTx) LockAssignmentCommand(
	_ context.Context,
	_, _ pgtype.UUID,
) error {
	tx.backend.log = append(tx.backend.log, "lock")
	if tx.backend.failAt == "lock" {
		return errAssignmentLock
	}
	return nil
}

func (tx *fakeCompanyOpsAssignmentTx) GetAssignmentDispatchReceipt(
	_ context.Context,
	_, commandID pgtype.UUID,
) (AssignmentDispatchReceipt, bool, error) {
	tx.backend.log = append(tx.backend.log, "get_receipt")
	if tx.backend.failAt == "lookup" {
		return AssignmentDispatchReceipt{}, false, errAssignmentLookup
	}
	receipt, ok := tx.backend.committedReceipts[commandID.Bytes]
	return receipt, ok, nil
}

func (tx *fakeCompanyOpsAssignmentTx) EnsureWorkOrderIssue(
	_ context.Context,
	req CompanyOpsAssignmentRequest,
) (CompanyOpsWorkOrderProjection, error) {
	tx.backend.log = append(tx.backend.log, "ensure_issue")
	if tx.backend.failAt == "ensure" {
		return CompanyOpsWorkOrderProjection{}, errAssignmentWrite
	}
	if tx.backend.ensureErr != nil {
		return CompanyOpsWorkOrderProjection{}, tx.backend.ensureErr
	}
	var issue db.Issue
	if tx.backend.ensureIssue.ID.Valid {
		issue = tx.backend.ensureIssue
	} else {
		issue = db.Issue{ID: assignmentUUID(6), WorkspaceID: req.WorkspaceID, ProjectID: req.ProjectID}
	}
	projection := CompanyOpsWorkOrderProjection{Issue: issue, Created: true}
	tx.createdProjection = &projection
	return projection, nil
}

func (tx *fakeCompanyOpsAssignmentTx) CreatedWorkOrderProjection() *CompanyOpsWorkOrderProjection {
	return tx.createdProjection
}

func TestCompanyOpsAssignment_ProjectBoundIssueIsCreatedBeforeTaskInOneTx(t *testing.T) {
	backend := newFakeCompanyOpsAssignmentBackend()
	service := NewCompanyOpsAssignmentService(backend)
	req := validCompanyOpsAssignmentRequest()
	req.IssueID = pgtype.UUID{}
	req.ProjectID = assignmentUUID(8)

	receipt, err := service.Dispatch(t.Context(), req)
	if err != nil {
		t.Fatalf("project-bound Dispatch: %v", err)
	}
	if receipt.IssueID != assignmentUUID(6) || backend.lastEvidenceRefID != req.CommandID {
		t.Fatalf("receipt/task lineage = %+v/%v, want generated Issue and command evidence", receipt, backend.lastEvidenceRefID)
	}
	if backend.finishCount != 1 {
		t.Fatalf("project-bound post-commit projection finish count = %d, want 1", backend.finishCount)
	}
	replay, err := service.Dispatch(t.Context(), req)
	if err != nil || replay != receipt {
		t.Fatalf("project-bound replay = %+v/%v, want original receipt", replay, err)
	}
	if backend.finishCount != 1 || backend.enqueueCount != 1 || backend.appendCount != 1 {
		t.Fatalf("project-bound replay effects = finish:%d task:%d receipt:%d, want 1/1/1", backend.finishCount, backend.enqueueCount, backend.appendCount)
	}
	if got := backend.log[:5]; !reflect.DeepEqual(got, []string{"begin", "lock", "get_receipt", "ensure_issue", "assign_exact"}) {
		t.Fatalf("project-bound transaction order = %v", got)
	}
}

func TestCompanyOpsAssignment_ProjectBoundaryAndMissingProjectFailClosed(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fakeCompanyOpsAssignmentBackend, CompanyOpsAssignmentRequest)
	}{
		{
			name: "cross workspace projected issue",
			setup: func(backend *fakeCompanyOpsAssignmentBackend, req CompanyOpsAssignmentRequest) {
				backend.ensureIssue = db.Issue{ID: assignmentUUID(6), WorkspaceID: assignmentUUID(99), ProjectID: req.ProjectID}
			},
		},
		{
			name: "missing project",
			setup: func(backend *fakeCompanyOpsAssignmentBackend, _ CompanyOpsAssignmentRequest) {
				backend.ensureErr = ErrProjectNotFound
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := newFakeCompanyOpsAssignmentBackend()
			req := validCompanyOpsAssignmentRequest()
			req.IssueID = pgtype.UUID{}
			req.ProjectID = assignmentUUID(8)
			tt.setup(backend, req)
			if _, err := NewCompanyOpsAssignmentService(backend).Dispatch(t.Context(), req); err == nil {
				t.Fatal("project-bound Dispatch succeeded across a project boundary")
			}
			if backend.assignCount != 0 || backend.enqueueCount != 0 || len(backend.committedReceipts) != 0 {
				t.Fatalf("failed project-bound Dispatch committed partial state: %+v", backend)
			}
		})
	}
}

func TestCompanyOpsAssignment_ProjectBoundReplayRejectsDifferentProject(t *testing.T) {
	backend := newFakeCompanyOpsAssignmentBackend()
	service := NewCompanyOpsAssignmentService(backend)
	req := validCompanyOpsAssignmentRequest()
	req.IssueID = pgtype.UUID{}
	req.ProjectID = assignmentUUID(8)
	backend.ensureIssue = db.Issue{ID: assignmentUUID(6), WorkspaceID: req.WorkspaceID, ProjectID: req.ProjectID}
	if _, err := service.Dispatch(t.Context(), req); err != nil {
		t.Fatalf("first project-bound Dispatch: %v", err)
	}
	replay := req
	replay.ProjectID = assignmentUUID(9)
	if _, err := service.Dispatch(t.Context(), replay); !errors.Is(err, ErrCompanyOpsAssignmentConflict) {
		t.Fatalf("different-project replay error = %v, want %v", err, ErrCompanyOpsAssignmentConflict)
	}
	replayWithIssue := replay
	replayWithIssue.IssueID = assignmentUUID(6)
	if _, err := service.Dispatch(t.Context(), replayWithIssue); !errors.Is(err, ErrCompanyOpsAssignmentConflict) {
		t.Fatalf("different-project replay with IssueID error = %v, want %v", err, ErrCompanyOpsAssignmentConflict)
	}
	if backend.assignCount != 1 || backend.enqueueCount != 1 || backend.appendCount != 1 || backend.publishCount != 1 || backend.notifyCount != 1 {
		t.Fatalf("different-project replay changed durable/effect counts: %+v", backend)
	}
}

func (tx *fakeCompanyOpsAssignmentTx) AssignIssueExact(
	_ context.Context,
	_ CompanyOpsAssignmentRequest,
	_ companyops.ExecutionTargetSnapshot,
) error {
	tx.backend.log = append(tx.backend.log, "assign_exact")
	if tx.backend.failAt == "assign" {
		return errAssignmentWrite
	}
	tx.assignCount++
	return nil
}

func (tx *fakeCompanyOpsAssignmentTx) EnqueueAssignmentTask(
	_ context.Context,
	req CompanyOpsAssignmentRequest,
	_ companyops.ExecutionTargetSnapshot,
	evidenceKind string,
	evidenceRefID pgtype.UUID,
) (db.AgentTaskQueue, error) {
	tx.backend.log = append(tx.backend.log, "enqueue_task")
	tx.backend.lastEvidenceKind = evidenceKind
	tx.backend.lastEvidenceRefID = evidenceRefID
	if tx.backend.failAt == "enqueue" {
		return db.AgentTaskQueue{}, errAssignmentEnqueue
	}
	task := db.AgentTaskQueue{
		ID:                   assignmentUUID(5),
		AgentID:              req.LocalAgentID,
		IssueID:              req.IssueID,
		Status:               "queued",
		TriggerEvidenceKind:  pgtype.Text{String: evidenceKind, Valid: evidenceKind != ""},
		TriggerEvidenceRefID: evidenceRefID,
	}
	if tx.tasks == nil {
		tx.tasks = make(map[[16]byte]db.AgentTaskQueue)
	}
	tx.tasks[task.ID.Bytes] = task
	tx.enqueueCount++
	return task, nil
}

func (tx *fakeCompanyOpsAssignmentTx) AppendAssignmentDispatchReceipt(
	_ context.Context,
	receipt AssignmentDispatchReceipt,
) error {
	tx.backend.log = append(tx.backend.log, "append_receipt")
	if tx.backend.failAt == "receipt" {
		return errAssignmentReceipt
	}
	if tx.receipts == nil {
		tx.receipts = make(map[[16]byte]AssignmentDispatchReceipt)
	}
	tx.receipts[receipt.CommandID.Bytes] = receipt
	tx.appendCount++
	return nil
}

func TestCompanyOpsAssignment_InvalidOrStaleTargetDoesNotBeginTransaction(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CompanyOpsAssignmentRequest)
	}{
		{
			name: "stale work order",
			mutate: func(req *CompanyOpsAssignmentRequest) {
				req.WorkOrder.Freshness = "stale"
			},
		},
		{
			name: "missing input digest",
			mutate: func(req *CompanyOpsAssignmentRequest) {
				req.InputDigest = ""
			},
		},
		{
			name: "input digest does not match delivered handoff",
			mutate: func(req *CompanyOpsAssignmentRequest) {
				req.InputDigest = assignmentDigest("b")
			},
		},
		{
			name: "missing accountable human",
			mutate: func(req *CompanyOpsAssignmentRequest) {
				req.ActorUserID = pgtype.UUID{}
			},
		},
		{
			name: "ambiguous active binding",
			mutate: func(req *CompanyOpsAssignmentRequest) {
				req.Bindings = append(req.Bindings, companyops.IdentityBinding{
					Authority:   assignmentAuthority("IdentityBinding", "hivecosm://identity-bindings/BIND-ASSIGNMENT-002", "binding-rev-1", "f"),
					EmployeeRef: assignmentEmployeeRef,
					AgentRef:    "/api/agents/4a000000-0000-4000-8000-000000000000",
					Active:      true,
				})
			},
		},
		{
			name: "local agent ref does not match exact binding target",
			mutate: func(req *CompanyOpsAssignmentRequest) {
				req.LocalAgentSourceRef = "/api/agents/4b000000-0000-4000-8000-000000000000"
			},
		},
		{
			name: "local Agent ID does not match its source ref",
			mutate: func(req *CompanyOpsAssignmentRequest) {
				req.LocalAgentID = assignmentUUID(9)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := newFakeCompanyOpsAssignmentBackend()
			svc := NewCompanyOpsAssignmentService(backend)
			req := validCompanyOpsAssignmentRequest()
			tt.mutate(&req)

			if _, err := svc.Dispatch(t.Context(), req); err == nil {
				t.Fatal("Dispatch accepted an invalid or stale execution target")
			}
			if backend.beginCount != 0 || len(backend.log) != 0 {
				t.Fatalf("invalid target performed writes: begin=%d log=%v", backend.beginCount, backend.log)
			}
		})
	}
}

func TestCompanyOpsAssignment_ExactReplayReturnsSameReceiptWithoutDuplicateTask(t *testing.T) {
	backend := newFakeCompanyOpsAssignmentBackend()
	svc := NewCompanyOpsAssignmentService(backend)
	req := validCompanyOpsAssignmentRequest()

	first, err := svc.Dispatch(t.Context(), req)
	if err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}
	second, err := svc.Dispatch(t.Context(), req)
	if err != nil {
		t.Fatalf("exact replay Dispatch: %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("exact replay receipt changed:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if backend.assignCount != 1 || backend.enqueueCount != 1 || backend.appendCount != 1 {
		t.Fatalf(
			"exact replay duplicated committed writes: assign=%d enqueue=%d append=%d",
			backend.assignCount,
			backend.enqueueCount,
			backend.appendCount,
		)
	}
	if backend.publishCount != 1 || backend.notifyCount != 1 {
		t.Fatalf("exact replay duplicated post-commit effects: publish=%d notify=%d", backend.publishCount, backend.notifyCount)
	}
	if len(backend.committedTasks) != 1 || len(backend.committedReceipts) != 1 {
		t.Fatalf("exact replay durable counts: tasks=%d receipts=%d, want 1/1", len(backend.committedTasks), len(backend.committedReceipts))
	}
	if backend.lastEvidenceKind != "assignment_dispatch" {
		t.Fatalf("task trigger evidence kind = %q, want assignment_dispatch", backend.lastEvidenceKind)
	}
	if backend.lastEvidenceRefID != req.CommandID {
		t.Fatalf("task trigger evidence ref = %+v, want command UUID %+v", backend.lastEvidenceRefID, req.CommandID)
	}

	wantLog := []string{
		"begin", "lock", "get_receipt", "assign_exact", "enqueue_task", "append_receipt", "commit", "publish", "notify",
		"begin", "lock", "get_receipt", "commit",
	}
	if !reflect.DeepEqual(backend.log, wantLog) {
		t.Fatalf("dispatch/replay order = %v, want %v", backend.log, wantLog)
	}
}

func TestCompanyOpsAssignment_SameCommandDifferentPayloadFailsClosed(t *testing.T) {
	backend := newFakeCompanyOpsAssignmentBackend()
	svc := NewCompanyOpsAssignmentService(backend)
	req := validCompanyOpsAssignmentRequest()
	if _, err := svc.Dispatch(t.Context(), req); err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}

	replay := req
	replay.HandoffNote = "A different handoff must not reuse the same command."
	replay.InputDigest = CompanyOpsHandoffInputDigest(replay.HandoffNote)
	_, err := svc.Dispatch(t.Context(), replay)
	if !errors.Is(err, ErrCompanyOpsAssignmentConflict) {
		t.Fatalf("different-payload replay error = %v, want ErrCompanyOpsAssignmentConflict", err)
	}
	if backend.assignCount != 1 || backend.enqueueCount != 1 || backend.appendCount != 1 {
		t.Fatalf("conflicting replay changed durable counts: assign=%d enqueue=%d append=%d", backend.assignCount, backend.enqueueCount, backend.appendCount)
	}
	if backend.publishCount != 1 || backend.notifyCount != 1 {
		t.Fatalf("conflicting replay emitted effects: publish=%d notify=%d", backend.publishCount, backend.notifyCount)
	}
	wantSuffix := []string{"begin", "lock", "get_receipt", "rollback"}
	if got := backend.log[len(backend.log)-len(wantSuffix):]; !reflect.DeepEqual(got, wantSuffix) {
		t.Fatalf("conflicting replay order suffix = %v, want %v", got, wantSuffix)
	}
}

func TestCompanyOpsAssignment_AssignTaskReceiptAreAtomicAndEffectsFollowCommit(t *testing.T) {
	tests := []struct {
		name   string
		failAt string
	}{
		{name: "command lock", failAt: "lock"},
		{name: "receipt lookup", failAt: "lookup"},
		{name: "exact assignment", failAt: "assign"},
		{name: "task enqueue", failAt: "enqueue"},
		{name: "receipt append", failAt: "receipt"},
		{name: "commit", failAt: "commit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := newFakeCompanyOpsAssignmentBackend()
			backend.failAt = tt.failAt
			svc := NewCompanyOpsAssignmentService(backend)

			if _, err := svc.Dispatch(t.Context(), validCompanyOpsAssignmentRequest()); err == nil {
				t.Fatalf("Dispatch succeeded when %s failed", tt.failAt)
			}
			if backend.assignCount != 0 || backend.enqueueCount != 0 || backend.appendCount != 0 {
				t.Fatalf(
					"%s failure committed partial writes: assign=%d enqueue=%d append=%d",
					tt.failAt,
					backend.assignCount,
					backend.enqueueCount,
					backend.appendCount,
				)
			}
			if len(backend.committedTasks) != 0 || len(backend.committedReceipts) != 0 {
				t.Fatalf(
					"%s failure left durable task/receipt: tasks=%d receipts=%d",
					tt.failAt,
					len(backend.committedTasks),
					len(backend.committedReceipts),
				)
			}
			if backend.publishCount != 0 || backend.notifyCount != 0 {
				t.Fatalf("%s failure emitted pre-commit effects: publish=%d notify=%d", tt.failAt, backend.publishCount, backend.notifyCount)
			}
		})
	}

	backend := newFakeCompanyOpsAssignmentBackend()
	svc := NewCompanyOpsAssignmentService(backend)
	receipt, err := svc.Dispatch(t.Context(), validCompanyOpsAssignmentRequest())
	if err != nil {
		t.Fatalf("successful Dispatch: %v", err)
	}
	wantTarget, err := companyops.ValidateAndFreezeExecutionTarget(
		validCompanyOpsAssignmentRequest().WorkOrder,
		validCompanyOpsAssignmentRequest().InputDigest,
		validCompanyOpsAssignmentRequest().Employee,
		validCompanyOpsAssignmentRequest().Bindings,
		validCompanyOpsAssignmentRequest().Agents,
	)
	if err != nil {
		t.Fatalf("fixture target validation: %v", err)
	}
	if !reflect.DeepEqual(receipt.Target, wantTarget) {
		t.Fatalf("receipt target = %+v, want frozen %+v", receipt.Target, wantTarget)
	}
	if receipt.CommandID != validCompanyOpsAssignmentRequest().CommandID ||
		receipt.WorkspaceID != validCompanyOpsAssignmentRequest().WorkspaceID ||
		receipt.IssueID != validCompanyOpsAssignmentRequest().IssueID ||
		receipt.LocalAgentID != validCompanyOpsAssignmentRequest().LocalAgentID ||
		!receipt.InitialTaskID.Valid {
		t.Fatalf("receipt did not freeze assignment/run identity: %+v", receipt)
	}

	commitIndex := indexOfAssignmentLog(backend.log, "commit")
	publishIndex := indexOfAssignmentLog(backend.log, "publish")
	notifyIndex := indexOfAssignmentLog(backend.log, "notify")
	if commitIndex < 0 || publishIndex <= commitIndex || notifyIndex <= commitIndex {
		t.Fatalf("post-commit effects order = %v; publish/notify must follow commit", backend.log)
	}
}

func indexOfAssignmentLog(log []string, target string) int {
	for i, entry := range log {
		if entry == target {
			return i
		}
	}
	return -1
}
