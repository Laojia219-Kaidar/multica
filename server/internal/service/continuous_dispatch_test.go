package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeContinuousDispatchBackend struct {
	mu          sync.Mutex
	issue       db.Issue
	receipt     ContinuousDispatchReceipt
	hasReceipt  bool
	prepareN    int
	appendN     int
	notifyN     int
	nextTaskID  pgtype.UUID
	preparedRun pgtype.UUID
}

func (b *fakeContinuousDispatchBackend) RunInContinuousDispatchTx(
	ctx context.Context,
	fn func(ContinuousDispatchTx) error,
) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return fn((*fakeContinuousDispatchTx)(b))
}

func (b *fakeContinuousDispatchBackend) NotifyContinuousDispatchTask(context.Context, db.AgentTaskQueue) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.notifyN++
}

type fakeContinuousDispatchTx fakeContinuousDispatchBackend

func (tx *fakeContinuousDispatchTx) LockIdentity(context.Context, continuousdispatch.DispatchIdentity) error {
	return nil
}

func (tx *fakeContinuousDispatchTx) GetReceipt(
	context.Context,
	continuousdispatch.DispatchIdentity,
) (ContinuousDispatchReceipt, bool, error) {
	return tx.receipt, tx.hasReceipt, nil
}

func (tx *fakeContinuousDispatchTx) LoadIssue(
	context.Context,
	continuousdispatch.DispatchIdentity,
) (db.Issue, error) {
	return tx.issue, nil
}

func (tx *fakeContinuousDispatchTx) PrepareTask(
	_ context.Context,
	issue db.Issue,
	req ContinuousDispatchRequest,
) (db.AgentTaskQueue, error) {
	tx.prepareN++
	tx.preparedRun = tx.nextTaskID
	return db.AgentTaskQueue{
		ID: tx.nextTaskID, IssueID: issue.ID, AgentID: req.Route.LocalAgentID,
		RuntimeID: req.Route.RuntimeID, Status: "queued",
	}, nil
}

func (tx *fakeContinuousDispatchTx) StampTaskIdentity(
	_ context.Context,
	task db.AgentTaskQueue,
	identity continuousdispatch.DispatchIdentity,
) (db.AgentTaskQueue, error) {
	if task.ID != tx.preparedRun || !identity.Complete() {
		return db.AgentTaskQueue{}, fmt.Errorf("unexpected stamp input")
	}
	task.Context = []byte(`{"continuous_dispatch":{"stage":"implementation"}}`)
	return task, nil
}

func (tx *fakeContinuousDispatchTx) AppendReceipt(
	_ context.Context,
	receipt ContinuousDispatchReceipt,
) (ContinuousDispatchReceipt, error) {
	tx.appendN++
	tx.receipt = receipt
	tx.hasReceipt = true
	return receipt, nil
}

func continuousDispatchRequestFixture(seed byte) (ContinuousDispatchRequest, db.Issue) {
	workspaceID := dispatchReceiptUUID(seed)
	issueID := dispatchReceiptUUID(seed + 1)
	request := ContinuousDispatchRequest{
		Identity: continuousdispatch.DispatchIdentity{
			WorkspaceID: shadowUUIDString(workspaceID), IssueID: shadowUUIDString(issueID),
			Stage: "implementation", CandidateRevision: "candidate-abc123", Generation: "generation-1",
		},
		Route: ContinuousDispatchRoute{
			EmployeeRef:  continuousDispatchEmployeeRefPrefix + "EMP-001",
			LocalAgentID: dispatchReceiptUUID(seed + 2), RuntimeID: dispatchReceiptUUID(seed + 3),
			Model: "glm-5.2", AccountRef: "glm-capacity-1",
		},
		ActorUserID: dispatchReceiptUUID(seed + 4),
		HandoffNote: "Implement the exact accepted frontier item.",
	}
	issue := db.Issue{
		ID: issueID, WorkspaceID: workspaceID, Status: "todo",
		Metadata: []byte(`{"stage":"implementation","candidate_revision":"candidate-abc123","generation":"generation-1"}`),
	}
	return request, issue
}

func TestContinuousDispatchConcurrentExactReplayCreatesAndNotifiesOnce(t *testing.T) {
	req, issue := continuousDispatchRequestFixture(90)
	backend := &fakeContinuousDispatchBackend{issue: issue, nextTaskID: dispatchReceiptUUID(120)}
	service := NewContinuousDispatchService(backend)

	const workers = 16
	var wg sync.WaitGroup
	receipts := make(chan ContinuousDispatchReceipt, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, err := service.Dispatch(context.Background(), req)
			receipts <- receipt
			errs <- err
		}()
	}
	wg.Wait()
	close(receipts)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent dispatch: %v", err)
		}
	}
	for receipt := range receipts {
		if receipt.TaskID != backend.nextTaskID || receipt.Identity != req.Identity {
			t.Fatalf("receipt = %+v, want exact committed generation", receipt)
		}
	}
	if backend.prepareN != 1 || backend.appendN != 1 || backend.notifyN != 1 {
		t.Fatalf("prepare/append/notify = %d/%d/%d, want 1/1/1", backend.prepareN, backend.appendN, backend.notifyN)
	}
}

func TestContinuousDispatchExactIdentityRejectsChangedRouteOrHandoff(t *testing.T) {
	req, issue := continuousDispatchRequestFixture(100)
	backend := &fakeContinuousDispatchBackend{issue: issue, nextTaskID: dispatchReceiptUUID(130)}
	service := NewContinuousDispatchService(backend)
	if _, err := service.Dispatch(context.Background(), req); err != nil {
		t.Fatalf("initial dispatch: %v", err)
	}

	changedRoute := req
	changedRoute.Route.RuntimeID = dispatchReceiptUUID(131)
	if _, err := service.Dispatch(context.Background(), changedRoute); !errors.Is(err, ErrContinuousDispatchConflict) {
		t.Fatalf("changed route error = %v, want conflict", err)
	}
	changedHandoff := req
	changedHandoff.HandoffNote = "Different work"
	if _, err := service.Dispatch(context.Background(), changedHandoff); !errors.Is(err, ErrContinuousDispatchConflict) {
		t.Fatalf("changed handoff error = %v, want conflict", err)
	}
	if backend.prepareN != 1 || backend.appendN != 1 || backend.notifyN != 1 {
		t.Fatalf("conflicting replay mutated counts: %d/%d/%d", backend.prepareN, backend.appendN, backend.notifyN)
	}
}

func TestContinuousDispatchRejectsIssueDriftAndTerminalState(t *testing.T) {
	req, issue := continuousDispatchRequestFixture(110)
	issue.Metadata = []byte(`{"stage":"implementation","candidate_revision":"other","generation":"generation-1"}`)
	backend := &fakeContinuousDispatchBackend{issue: issue, nextTaskID: dispatchReceiptUUID(140)}
	if _, err := NewContinuousDispatchService(backend).Dispatch(context.Background(), req); !errors.Is(err, ErrContinuousDispatchIssueDrift) {
		t.Fatalf("metadata drift error = %v, want ErrContinuousDispatchIssueDrift", err)
	}

	issue.Metadata = []byte(`{"stage":"implementation","candidate_revision":"candidate-abc123","generation":"generation-1"}`)
	issue.Status = "done"
	backend.issue = issue
	if _, err := NewContinuousDispatchService(backend).Dispatch(context.Background(), req); !errors.Is(err, ErrContinuousDispatchIssueNotReady) {
		t.Fatalf("terminal issue error = %v, want ErrContinuousDispatchIssueNotReady", err)
	}
	if backend.prepareN != 0 || backend.appendN != 0 || backend.notifyN != 0 {
		t.Fatalf("rejected issue mutated counts: %d/%d/%d", backend.prepareN, backend.appendN, backend.notifyN)
	}
}

func TestContinuousDispatchReviewPreconditionRejectsStatusChangedAfterPreview(t *testing.T) {
	req, issue := continuousDispatchRequestFixture(150)
	req.RequireInReview = true
	issue.Status = "in_progress"
	backend := &fakeContinuousDispatchBackend{issue: issue, nextTaskID: dispatchReceiptUUID(151)}
	if _, err := NewContinuousDispatchService(backend).Dispatch(context.Background(), req); !errors.Is(err, ErrContinuousDispatchIssueDrift) {
		t.Fatalf("review status drift error = %v, want ErrContinuousDispatchIssueDrift", err)
	}
	if backend.prepareN != 0 || backend.appendN != 0 || backend.notifyN != 0 {
		t.Fatalf("review status drift mutated counts: %d/%d/%d", backend.prepareN, backend.appendN, backend.notifyN)
	}
}

var _ ContinuousDispatchBackend = (*fakeContinuousDispatchBackend)(nil)
var _ ContinuousDispatchTx = (*fakeContinuousDispatchTx)(nil)
