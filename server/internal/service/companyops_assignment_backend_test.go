package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/companyops"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	errCanonicalBackendQTx      = errors.New("companyops backend used a non-transactional query handle")
	errCanonicalBackendLock     = errors.New("companyops advisory command lock failed")
	errCanonicalBackendRead     = errors.New("companyops receipt read failed")
	errCanonicalBackendAssign   = errors.New("canonical issue assignment failed closed")
	errCanonicalBackendPrepare  = errors.New("canonical task prepare failed")
	errCanonicalBackendReceipt  = errors.New("assignment receipt append failed")
	errCanonicalBackendCommit   = errors.New("companyops assignment commit failed")
	errCanonicalBackendMismatch = errors.New("canonical issue writer returned a changed row")
)

type canonicalAssignmentStage struct {
	qtx     *db.Queries
	issue   *db.Issue
	task    *db.AgentTaskQueue
	receipt *AssignmentDispatchReceipt
}

type canonicalAssignmentBackendSpy struct {
	issue            db.Issue
	committedTask    *db.AgentTaskQueue
	committedReceipt *AssignmentDispatchReceipt
	active           *canonicalAssignmentStage
	log              []string
	failAt           string
	assignCalls      int
	prepareCalls     int
	appendCalls      int
	publishCalls     int
	wakeupCalls      int
	evidenceKind     string
	evidenceRef      pgtype.UUID
}

func newCanonicalAssignmentBackendSpy(req CompanyOpsAssignmentRequest) *canonicalAssignmentBackendSpy {
	return &canonicalAssignmentBackendSpy{
		issue: db.Issue{
			ID:          req.IssueID,
			WorkspaceID: req.WorkspaceID,
			Priority:    "medium",
			CreatorType: "member",
			CreatorID:   assignmentUUID(31),
		},
	}
}

func (s *canonicalAssignmentBackendSpy) withQTx(
	_ context.Context,
	fn func(pgx.Tx, *db.Queries) error,
) error {
	s.log = append(s.log, "begin")
	stage := &canonicalAssignmentStage{qtx: db.New(nil)}
	s.active = stage
	defer func() { s.active = nil }()

	if err := fn(nil, stage.qtx); err != nil {
		s.log = append(s.log, "rollback")
		return err
	}
	if s.failAt == "commit" {
		s.log = append(s.log, "commit_failed")
		return errCanonicalBackendCommit
	}
	if stage.issue != nil {
		s.issue = *stage.issue
	}
	if stage.task != nil {
		value := *stage.task
		s.committedTask = &value
	}
	if stage.receipt != nil {
		value := *stage.receipt
		s.committedReceipt = &value
	}
	s.log = append(s.log, "commit")
	return nil
}

func (s *canonicalAssignmentBackendSpy) requireQTx(qtx *db.Queries) error {
	if s.active == nil || qtx == nil || qtx != s.active.qtx {
		return errCanonicalBackendQTx
	}
	return nil
}

func (s *canonicalAssignmentBackendSpy) lockAssignmentCommand(
	_ context.Context,
	qtx *db.Queries,
	workspaceID, commandID pgtype.UUID,
) error {
	s.log = append(s.log, "lock")
	if err := s.requireQTx(qtx); err != nil {
		return err
	}
	if s.failAt == "lock" {
		return errCanonicalBackendLock
	}
	if !workspaceID.Valid || !commandID.Valid {
		return errCanonicalBackendLock
	}
	return nil
}

func (s *canonicalAssignmentBackendSpy) getAssignmentDispatchReceipt(
	_ context.Context,
	qtx *db.Queries,
	workspaceID, commandID pgtype.UUID,
) (AssignmentDispatchReceipt, bool, error) {
	s.log = append(s.log, "read_receipt")
	if err := s.requireQTx(qtx); err != nil {
		return AssignmentDispatchReceipt{}, false, err
	}
	if s.failAt == "read" {
		return AssignmentDispatchReceipt{}, false, errCanonicalBackendRead
	}
	if s.committedReceipt == nil || s.committedReceipt.CommandID != commandID {
		return AssignmentDispatchReceipt{}, false, nil
	}
	if s.committedReceipt.WorkspaceID != workspaceID {
		return AssignmentDispatchReceipt{}, false, errCanonicalBackendRead
	}
	return *s.committedReceipt, true, nil
}

func (s *canonicalAssignmentBackendSpy) ensureWorkOrderIssue(
	_ context.Context,
	_ pgx.Tx,
	qtx *db.Queries,
	req CompanyOpsAssignmentRequest,
) (CompanyOpsWorkOrderProjection, error) {
	if err := s.requireQTx(qtx); err != nil {
		return CompanyOpsWorkOrderProjection{}, err
	}
	issue := s.issue
	issue.ID = req.IssueID
	issue.WorkspaceID = req.WorkspaceID
	issue.ProjectID = req.ProjectID
	return CompanyOpsWorkOrderProjection{Issue: issue, Created: true}, nil
}

func (s *canonicalAssignmentBackendSpy) createdWorkOrderProjection() *CompanyOpsWorkOrderProjection {
	return nil
}

func (s *canonicalAssignmentBackendSpy) assignIssueExact(
	_ context.Context,
	qtx *db.Queries,
	req CompanyOpsAssignmentRequest,
	_ companyops.ExecutionTargetSnapshot,
) (db.Issue, error) {
	s.log = append(s.log, "assign_issue_exact")
	if err := s.requireQTx(qtx); err != nil {
		return db.Issue{}, err
	}
	s.assignCalls++
	if s.failAt == "assign" {
		return db.Issue{}, errCanonicalBackendAssign
	}
	if s.issue.WorkspaceID != req.WorkspaceID || s.issue.ID != req.IssueID ||
		s.issue.AssigneeType.Valid || s.issue.AssigneeID.Valid {
		return db.Issue{}, errCanonicalBackendAssign
	}

	assigned := s.issue
	assigned.AssigneeType = pgtype.Text{String: "agent", Valid: true}
	assigned.AssigneeID = req.LocalAgentID
	if s.failAt == "assign_changed" {
		assigned.AssigneeID = assignmentUUID(99)
	}
	s.active.issue = &assigned
	return assigned, nil
}

func (s *canonicalAssignmentBackendSpy) prepareAssignmentTask(
	_ context.Context,
	qtx *db.Queries,
	issue db.Issue,
	req CompanyOpsAssignmentRequest,
	_ companyops.ExecutionTargetSnapshot,
	evidenceKind string,
	evidenceRef pgtype.UUID,
) (db.AgentTaskQueue, error) {
	s.log = append(s.log, "prepare_task")
	if err := s.requireQTx(qtx); err != nil {
		return db.AgentTaskQueue{}, err
	}
	s.prepareCalls++
	if s.failAt == "prepare" {
		return db.AgentTaskQueue{}, errCanonicalBackendPrepare
	}
	if issue.WorkspaceID != req.WorkspaceID || issue.ID != req.IssueID ||
		issue.AssigneeType != (pgtype.Text{String: "agent", Valid: true}) ||
		issue.AssigneeID != req.LocalAgentID {
		return db.AgentTaskQueue{}, errCanonicalBackendMismatch
	}
	s.evidenceKind = evidenceKind
	s.evidenceRef = evidenceRef
	task := db.AgentTaskQueue{
		ID:                   assignmentUUID(32),
		AgentID:              req.LocalAgentID,
		IssueID:              req.IssueID,
		Status:               "queued",
		TriggerEvidenceKind:  pgtype.Text{String: evidenceKind, Valid: evidenceKind != ""},
		TriggerEvidenceRefID: evidenceRef,
	}
	s.active.task = &task
	return task, nil
}

func (s *canonicalAssignmentBackendSpy) appendAssignmentDispatchReceipt(
	_ context.Context,
	qtx *db.Queries,
	receipt AssignmentDispatchReceipt,
) error {
	s.log = append(s.log, "append_receipt")
	if err := s.requireQTx(qtx); err != nil {
		return err
	}
	s.appendCalls++
	if s.failAt == "receipt" {
		return errCanonicalBackendReceipt
	}
	value := receipt
	s.active.receipt = &value
	return nil
}

func (s *canonicalAssignmentBackendSpy) publishAssignmentDispatched(
	_ context.Context,
	_ AssignmentDispatchReceipt,
) {
	s.publishCalls++
	s.log = append(s.log, "publish")
}

func (s *canonicalAssignmentBackendSpy) notifyAssignmentTaskAvailable(
	_ context.Context,
	_ db.AgentTaskQueue,
) {
	s.wakeupCalls++
	s.log = append(s.log, "wakeup")
}

func newCanonicalAssignmentServiceUnderTest(
	t *testing.T,
	spy *canonicalAssignmentBackendSpy,
) *CompanyOpsAssignmentService {
	t.Helper()
	backend, err := NewCanonicalCompanyOpsAssignmentBackend(CanonicalCompanyOpsAssignmentBackendDeps{
		WithQTx:                         spy.withQTx,
		LockAssignmentCommand:           spy.lockAssignmentCommand,
		GetAssignmentDispatchReceipt:    spy.getAssignmentDispatchReceipt,
		EnsureWorkOrderIssue:            spy.ensureWorkOrderIssue,
		AssignIssueExact:                spy.assignIssueExact,
		PrepareAssignmentTask:           spy.prepareAssignmentTask,
		AppendAssignmentDispatchReceipt: spy.appendAssignmentDispatchReceipt,
		PublishAssignmentDispatched:     spy.publishAssignmentDispatched,
		NotifyAssignmentTaskAvailable:   spy.notifyAssignmentTaskAvailable,
	})
	if err != nil {
		t.Fatalf("NewCanonicalCompanyOpsAssignmentBackend: %v", err)
	}
	return NewCompanyOpsAssignmentService(backend)
}

func TestCompanyOpsAssignmentBackend_UsesOneQTxAndCanonicalWriters(t *testing.T) {
	req := validCompanyOpsAssignmentRequest()
	spy := newCanonicalAssignmentBackendSpy(req)
	svc := newCanonicalAssignmentServiceUnderTest(t, spy)

	receipt, err := svc.Dispatch(t.Context(), req)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	wantLog := []string{
		"begin", "lock", "read_receipt", "assign_issue_exact", "prepare_task",
		"append_receipt", "commit", "publish", "wakeup",
	}
	if !reflect.DeepEqual(spy.log, wantLog) {
		t.Fatalf("operation log = %v, want %v", spy.log, wantLog)
	}
	if spy.issue.WorkspaceID != req.WorkspaceID || spy.issue.ID != req.IssueID ||
		spy.issue.AssigneeType != (pgtype.Text{String: "agent", Valid: true}) ||
		spy.issue.AssigneeID != req.LocalAgentID {
		t.Fatalf("committed canonical issue assignment = %+v, want exact workspace/issue agent assignment", spy.issue)
	}
	if spy.committedTask == nil || spy.committedReceipt == nil {
		t.Fatalf("committed task/receipt = %v/%v, want both", spy.committedTask, spy.committedReceipt)
	}
	if receipt != *spy.committedReceipt || receipt.InitialTaskID != spy.committedTask.ID {
		t.Fatalf("receipt = %+v, committed = %+v task = %+v", receipt, spy.committedReceipt, spy.committedTask)
	}
	if spy.evidenceKind != assignmentDispatchEvidenceKind || spy.evidenceRef != req.CommandID ||
		spy.committedTask.TriggerEvidenceKind != (pgtype.Text{String: assignmentDispatchEvidenceKind, Valid: true}) ||
		spy.committedTask.TriggerEvidenceRefID != req.CommandID {
		t.Fatalf("trigger evidence = %q/%v task=%+v, want assignment_dispatch/%v", spy.evidenceKind, spy.evidenceRef, spy.committedTask, req.CommandID)
	}
	if spy.assignCalls != 1 || spy.prepareCalls != 1 || spy.appendCalls != 1 ||
		spy.publishCalls != 1 || spy.wakeupCalls != 1 {
		t.Fatalf("call counts assign/prepare/append/publish/wakeup = %d/%d/%d/%d/%d, want 1/1/1/1/1",
			spy.assignCalls, spy.prepareCalls, spy.appendCalls, spy.publishCalls, spy.wakeupCalls)
	}
}

func TestCompanyOpsAssignmentBackend_ExactAssignmentFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*canonicalAssignmentBackendSpy)
	}{
		{
			name: "cross workspace issue",
			mutate: func(spy *canonicalAssignmentBackendSpy) {
				spy.issue.WorkspaceID = assignmentUUID(41)
			},
		},
		{
			name: "issue already has old assignee",
			mutate: func(spy *canonicalAssignmentBackendSpy) {
				spy.issue.AssigneeType = pgtype.Text{String: "agent", Valid: true}
				spy.issue.AssigneeID = assignmentUUID(42)
			},
		},
		{
			name: "canonical writer returned changed assignee",
			mutate: func(spy *canonicalAssignmentBackendSpy) {
				spy.failAt = "assign_changed"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validCompanyOpsAssignmentRequest()
			spy := newCanonicalAssignmentBackendSpy(req)
			tt.mutate(spy)
			before := spy.issue
			svc := newCanonicalAssignmentServiceUnderTest(t, spy)

			if _, err := svc.Dispatch(t.Context(), req); err == nil {
				t.Fatal("Dispatch error = nil, want fail-closed exact-assignment error")
			}
			if !reflect.DeepEqual(spy.issue, before) || spy.committedTask != nil || spy.committedReceipt != nil {
				t.Fatalf("committed issue/task/receipt = %+v/%v/%v, want original issue and zero task/receipt",
					spy.issue, spy.committedTask, spy.committedReceipt)
			}
			if spy.publishCalls != 0 || spy.wakeupCalls != 0 {
				t.Fatalf("post-commit effects = %d/%d, want 0/0", spy.publishCalls, spy.wakeupCalls)
			}
		})
	}
}

func TestCompanyOpsAssignmentBackend_AllWritesRollbackAndEffectsFollowCommit(t *testing.T) {
	for _, failAt := range []string{"lock", "read", "assign", "prepare", "receipt", "commit"} {
		t.Run(failAt, func(t *testing.T) {
			req := validCompanyOpsAssignmentRequest()
			spy := newCanonicalAssignmentBackendSpy(req)
			spy.failAt = failAt
			before := spy.issue
			svc := newCanonicalAssignmentServiceUnderTest(t, spy)

			if _, err := svc.Dispatch(t.Context(), req); err == nil {
				t.Fatalf("Dispatch with %s failure error = nil", failAt)
			}
			if !reflect.DeepEqual(spy.issue, before) || spy.committedTask != nil || spy.committedReceipt != nil {
				t.Fatalf("%s failure committed issue/task/receipt = %+v/%v/%v, want complete rollback",
					failAt, spy.issue, spy.committedTask, spy.committedReceipt)
			}
			if spy.publishCalls != 0 || spy.wakeupCalls != 0 {
				t.Fatalf("%s failure post-commit effects = %d/%d, want 0/0", failAt, spy.publishCalls, spy.wakeupCalls)
			}
		})
	}
}

func TestCompanyOpsAssignmentBackend_ReplayReadsReceiptWithoutDuplicateTaskOrEffects(t *testing.T) {
	req := validCompanyOpsAssignmentRequest()
	spy := newCanonicalAssignmentBackendSpy(req)
	svc := newCanonicalAssignmentServiceUnderTest(t, spy)

	first, err := svc.Dispatch(t.Context(), req)
	if err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}
	spy.log = nil
	second, err := svc.Dispatch(t.Context(), req)
	if err != nil {
		t.Fatalf("replay Dispatch: %v", err)
	}
	if second != first {
		t.Fatalf("replay receipt = %+v, want %+v", second, first)
	}
	if want := []string{"begin", "lock", "read_receipt", "commit"}; !reflect.DeepEqual(spy.log, want) {
		t.Fatalf("replay log = %v, want %v", spy.log, want)
	}
	if spy.assignCalls != 1 || spy.prepareCalls != 1 || spy.appendCalls != 1 ||
		spy.publishCalls != 1 || spy.wakeupCalls != 1 {
		t.Fatalf("replay totals assign/prepare/append/publish/wakeup = %d/%d/%d/%d/%d, want 1/1/1/1/1",
			spy.assignCalls, spy.prepareCalls, spy.appendCalls, spy.publishCalls, spy.wakeupCalls)
	}
}

func TestCompanyOpsAssignmentBackend_DoesNotCopyWorkOrderLifecycle(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(CompanyOpsAssignmentRequest{}),
		reflect.TypeOf(AssignmentDispatchReceipt{}),
		reflect.TypeOf(companyops.ExecutionTargetSnapshot{}),
	} {
		for _, forbidden := range []string{"WorkOrderTitle", "WorkOrderStatus", "ProjectStatus"} {
			if field, found := typ.FieldByName(forbidden); found {
				t.Fatalf("%s contains forbidden copied lifecycle field %s (%s)", typ, field.Name, field.Type)
			}
		}
	}
}
