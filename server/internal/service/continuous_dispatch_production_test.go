package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestProductionContinuousDispatchConcurrentGenerationCreatesOneTaskAndReceipt(t *testing.T) {
	pool := newProductionCompanyOpsPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedProductionCompanyOpsFixture(t, ctx, pool)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM continuous_dispatch_receipt WHERE workspace_id = $1`, fixture.workspaceID)
	})

	identity := continuousdispatch.DispatchIdentity{
		WorkspaceID: util.UUIDToString(fixture.workspaceID), IssueID: util.UUIDToString(fixture.issueID),
		Stage: "implementation", CandidateRevision: "candidate-production", Generation: "generation-production-1",
	}
	if _, err := pool.Exec(ctx, `UPDATE agent SET model = 'glm-5.2' WHERE id = $1`, fixture.agentID); err != nil {
		t.Fatalf("seed selected model: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET metadata = $2::jsonb WHERE id = $1`, fixture.issueID,
		`{"stage":"implementation","candidate_revision":"candidate-production","generation":"generation-production-1"}`); err != nil {
		t.Fatalf("seed exact dispatch identity: %v", err)
	}

	var (
		effectsMu sync.Mutex
		effects   []string
	)
	bus := events.New()
	bus.Subscribe(protocol.EventTaskQueued, func(events.Event) {
		effectsMu.Lock()
		defer effectsMu.Unlock()
		effects = append(effects, "broadcast")
	})
	wakeupEffects := []string{}
	wakeup := &productionCompanyOpsWakeup{effects: &wakeupEffects}
	queries := db.New(pool)
	taskService := &TaskService{Queries: queries, TxStarter: pool, Bus: bus, Wakeup: wakeup}
	backend, err := NewProductionContinuousDispatchBackend(queries, pool, taskService)
	if err != nil {
		t.Fatalf("NewProductionContinuousDispatchBackend: %v", err)
	}
	dispatcher := NewContinuousDispatchService(backend)
	req := ContinuousDispatchRequest{
		Identity: identity,
		Route: ContinuousDispatchRoute{
			EmployeeRef:  continuousDispatchEmployeeRefPrefix + "EMP-PRODUCTION",
			LocalAgentID: fixture.agentID, RuntimeID: fixture.runtimeID,
			Model: "glm-5.2", AccountRef: "glm-production-account",
		},
		ActorUserID: fixture.userID,
		HandoffNote: "Execute the exact production-style continuous dispatch generation.",
	}

	const workers = 16
	var wg sync.WaitGroup
	receipts := make(chan ContinuousDispatchReceipt, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, dispatchErr := dispatcher.Dispatch(ctx, req)
			receipts <- receipt
			errs <- dispatchErr
		}()
	}
	wg.Wait()
	close(receipts)
	close(errs)
	for dispatchErr := range errs {
		if dispatchErr != nil {
			t.Fatalf("concurrent production dispatch: %v", dispatchErr)
		}
	}

	var committed ContinuousDispatchReceipt
	for receipt := range receipts {
		if !committed.TaskID.Valid {
			committed = receipt
			continue
		}
		if !continuousDispatchReceiptsEqual(committed, receipt) {
			t.Fatalf("concurrent receipt changed: first=%+v got=%+v", committed, receipt)
		}
	}

	var taskCount, receiptCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, fixture.issueID).Scan(&taskCount); err != nil {
		t.Fatalf("count exact generation tasks: %v", err)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM continuous_dispatch_receipt
WHERE workspace_id = $1 AND issue_id = $2 AND stage = $3
  AND candidate_revision = $4 AND generation = $5`,
		fixture.workspaceID, fixture.issueID, identity.Stage, identity.CandidateRevision, identity.Generation,
	).Scan(&receiptCount); err != nil {
		t.Fatalf("count exact generation receipts: %v", err)
	}
	if taskCount != 1 || receiptCount != 1 {
		t.Fatalf("task/receipt rows = %d/%d, want 1/1", taskCount, receiptCount)
	}

	storedTask, err := queries.GetAgentTask(ctx, committed.TaskID)
	if err != nil {
		t.Fatalf("read committed task: %v", err)
	}
	var envelope shadowTaskContext
	if err := json.Unmarshal(storedTask.Context, &envelope); err != nil {
		t.Fatalf("decode committed task context: %v", err)
	}
	if envelope.ContinuousDispatch != identity || storedTask.AgentID != fixture.agentID ||
		storedTask.RuntimeID != fixture.runtimeID || storedTask.Status != "queued" {
		t.Fatalf("committed task does not carry exact route and identity: task=%+v context=%+v", storedTask, envelope)
	}
	effectsMu.Lock()
	gotBroadcasts := append([]string(nil), effects...)
	effectsMu.Unlock()
	if fmt.Sprint(gotBroadcasts) != fmt.Sprint([]string{"broadcast"}) ||
		fmt.Sprint(wakeupEffects) != fmt.Sprint([]string{"notify"}) {
		t.Fatalf("post-commit effects = %v/%v, want one broadcast and one notify", gotBroadcasts, wakeupEffects)
	}

	conflict := req
	conflict.HandoffNote = "A changed payload for the same generation must conflict."
	if _, err := dispatcher.Dispatch(ctx, conflict); !errors.Is(err, ErrContinuousDispatchConflict) {
		t.Fatalf("changed replay error = %v, want ErrContinuousDispatchConflict", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, fixture.issueID).Scan(&taskCount); err != nil {
		t.Fatalf("recount exact generation tasks: %v", err)
	}
	if taskCount != 1 {
		t.Fatalf("conflicting replay task rows = %d, want 1", taskCount)
	}
}

func TestProductionContinuousDispatchRejectsRouteDriftWithoutWrites(t *testing.T) {
	pool := newProductionCompanyOpsPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := seedProductionCompanyOpsFixture(t, ctx, pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM continuous_dispatch_receipt WHERE workspace_id = $1`, fixture.workspaceID)
	})
	if _, err := pool.Exec(ctx, `UPDATE agent SET model = 'glm-5.2' WHERE id = $1`, fixture.agentID); err != nil {
		t.Fatalf("seed selected model: %v", err)
	}
	identity := continuousdispatch.DispatchIdentity{
		WorkspaceID: util.UUIDToString(fixture.workspaceID), IssueID: util.UUIDToString(fixture.issueID),
		Stage: "implementation", CandidateRevision: "candidate-route", Generation: "generation-route-1",
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET metadata = $2::jsonb WHERE id = $1`, fixture.issueID,
		`{"stage":"implementation","candidate_revision":"candidate-route","generation":"generation-route-1"}`); err != nil {
		t.Fatalf("seed exact dispatch identity: %v", err)
	}
	queries := db.New(pool)
	backend, err := NewProductionContinuousDispatchBackend(queries, pool, &TaskService{Queries: queries, TxStarter: pool, Bus: events.New()})
	if err != nil {
		t.Fatalf("NewProductionContinuousDispatchBackend: %v", err)
	}
	req := ContinuousDispatchRequest{
		Identity: identity,
		Route: ContinuousDispatchRoute{
			EmployeeRef:  continuousDispatchEmployeeRefPrefix + "EMP-ROUTE",
			LocalAgentID: fixture.agentID, RuntimeID: util.MustParseUUID(uuid.NewString()),
			Model: "glm-5.2", AccountRef: "glm-production-account",
		},
		ActorUserID: fixture.userID,
	}
	if _, err := NewContinuousDispatchService(backend).Dispatch(ctx, req); !errors.Is(err, ErrContinuousDispatchRouteDrift) {
		t.Fatalf("route drift error = %v, want ErrContinuousDispatchRouteDrift", err)
	}
	var taskCount, receiptCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, fixture.issueID).Scan(&taskCount)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM continuous_dispatch_receipt WHERE issue_id = $1`, fixture.issueID).Scan(&receiptCount)
	if taskCount != 0 || receiptCount != 0 {
		t.Fatalf("route drift wrote task/receipt = %d/%d, want 0/0", taskCount, receiptCount)
	}
}
