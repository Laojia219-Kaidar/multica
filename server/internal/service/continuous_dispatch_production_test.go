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
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func cleanupProductionContinuousDispatchReceipt(t *testing.T, pool *pgxpool.Pool, workspaceID pgtype.UUID) {
	t.Helper()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM continuous_dispatch_receipt WHERE workspace_id = $1`, workspaceID); err != nil {
			t.Errorf("cleanup production continuous dispatch receipt: %v", err)
		}
	})
}

func TestProductionContinuousDispatchConcurrentGenerationCreatesOneTaskAndReceipt(t *testing.T) {
	pool := newProductionCompanyOpsPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedProductionCompanyOpsFixture(t, ctx, pool)
	cleanupProductionContinuousDispatchReceipt(t, pool, fixture.workspaceID)

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
	cleanupProductionContinuousDispatchReceipt(t, pool, fixture.workspaceID)
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
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, fixture.issueID).Scan(&taskCount); err != nil {
		t.Fatalf("count route-drift tasks: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM continuous_dispatch_receipt WHERE issue_id = $1`, fixture.issueID).Scan(&receiptCount); err != nil {
		t.Fatalf("count route-drift receipts: %v", err)
	}
	if taskCount != 0 || receiptCount != 0 {
		t.Fatalf("route drift wrote task/receipt = %d/%d, want 0/0", taskCount, receiptCount)
	}
}

func TestProductionContinuousDispatchReviewSourceLineageAndDrift(t *testing.T) {
	pool := newProductionCompanyOpsPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedProductionCompanyOpsFixture(t, ctx, pool)
	cleanupProductionContinuousDispatchReceipt(t, pool, fixture.workspaceID)
	if _, err := pool.Exec(ctx, `UPDATE agent SET model = 'glm-5.2' WHERE id = $1`, fixture.agentID); err != nil {
		t.Fatalf("seed implementation model: %v", err)
	}

	reviewerID := seedProductionContinuousDispatchReviewAgent(t, ctx, pool, fixture)
	identity := continuousdispatch.DispatchIdentity{
		WorkspaceID: util.UUIDToString(fixture.workspaceID), IssueID: util.UUIDToString(fixture.issueID),
		Stage: "review", CandidateRevision: "candidate-review-production", Generation: "generation-review-production-1",
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET status = 'in_review', metadata = $2::jsonb WHERE id = $1`, fixture.issueID,
		`{"stage":"review","candidate_revision":"candidate-review-production","generation":"generation-review-production-1"}`); err != nil {
		t.Fatalf("seed review dispatch identity: %v", err)
	}

	queries := db.New(pool)
	bus := events.New()
	wakeupEffects := []string{}
	backend, err := NewProductionContinuousDispatchBackend(queries, pool, &TaskService{
		Queries: queries, TxStarter: pool, Bus: bus, Wakeup: &productionCompanyOpsWakeup{effects: &wakeupEffects},
	})
	if err != nil {
		t.Fatalf("NewProductionContinuousDispatchBackend: %v", err)
	}
	dispatcher := NewContinuousDispatchService(backend)

	newRequest := func(sourceCommentID, sourceTaskID pgtype.UUID) ContinuousDispatchRequest {
		return ContinuousDispatchRequest{
			Identity: identity,
			Route: ContinuousDispatchRoute{
				EmployeeRef:  continuousDispatchEmployeeRefPrefix + "EMP-PRODUCTION-REVIEWER",
				LocalAgentID: reviewerID, RuntimeID: fixture.runtimeID,
				Model: "glm-5.2", AccountRef: "glm-production-account",
			},
			ActorUserID:     fixture.userID,
			HandoffNote:     "review_dispatch production lineage proof",
			requireInReview: true,
			reviewProvenance: &ContinuousDispatchReviewProvenance{
				SourceRef:       continuousDispatchReviewCommentRef(sourceCommentID),
				SourceIssueID:   util.UUIDToString(fixture.issueID),
				SourceTaskID:    util.UUIDToString(sourceTaskID),
				InitiatorSource: continuousDispatchReviewInitiatorSourceV1,
			},
		}
	}

	t.Run("persists exact source and exact replay", func(t *testing.T) {
		sourceTaskID, sourceCommentID := seedProductionContinuousDispatchReviewSource(t, ctx, pool, fixture, identity)
		req := newRequest(sourceCommentID, sourceTaskID)
		receipt, dispatchErr := dispatcher.Dispatch(ctx, req)
		if dispatchErr != nil {
			t.Fatalf("Dispatch review: %v", dispatchErr)
		}
		if receipt.ReviewProvenance == nil || !continuousDispatchReviewProvenanceEqual(receipt.ReviewProvenance, req.reviewProvenance) {
			t.Fatalf("receipt review provenance = %+v, want %+v", receipt.ReviewProvenance, req.reviewProvenance)
		}
		if receipt.LocalAgentID == fixture.agentID {
			t.Fatal("review Task reused implementation author; want independent reviewer")
		}
		storedTask, getErr := queries.GetAgentTask(ctx, receipt.TaskID)
		if getErr != nil {
			t.Fatalf("read review task: %v", getErr)
		}
		var contextValue shadowTaskContext
		if err := json.Unmarshal(storedTask.Context, &contextValue); err != nil {
			t.Fatalf("decode review task context: %v", err)
		}
		if storedTask.AgentID != reviewerID {
			t.Fatalf("stored review task agent = %s, want selected independent reviewer %s", util.UUIDToString(storedTask.AgentID), util.UUIDToString(reviewerID))
		}
		if !continuousDispatchReviewProvenanceEqual(contextValue.ReviewDispatch, req.reviewProvenance) {
			t.Fatalf("stored review provenance = %+v, want %+v", contextValue.ReviewDispatch, req.reviewProvenance)
		}
		replayed, replayErr := dispatcher.Dispatch(ctx, req)
		if replayErr != nil {
			t.Fatalf("exact review replay: %v", replayErr)
		}
		if !continuousDispatchReceiptsEqual(receipt, replayed) {
			t.Fatalf("replayed review receipt = %+v, want %+v", replayed, receipt)
		}
		var taskCount, receiptCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, fixture.issueID).Scan(&taskCount); err != nil {
			t.Fatalf("count review tasks: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM continuous_dispatch_receipt WHERE workspace_id = $1 AND issue_id = $2`, fixture.workspaceID, fixture.issueID).Scan(&receiptCount); err != nil {
			t.Fatalf("count review receipts: %v", err)
		}
		if taskCount != 2 || receiptCount != 1 {
			t.Fatalf("review task/receipt rows = %d/%d, want 2/1 including implementation source", taskCount, receiptCount)
		}
	})

	for _, drift := range []struct {
		name   string
		mutate func(*testing.T, *ContinuousDispatchRequest, pgtype.UUID)
	}{
		{
			name: "source comment changed",
			mutate: func(t *testing.T, _ *ContinuousDispatchRequest, sourceCommentID pgtype.UUID) {
				t.Helper()
				if _, err := pool.Exec(ctx, `UPDATE comment SET source_task_id = NULL WHERE id = $1`, sourceCommentID); err != nil {
					t.Fatalf("mutate source comment: %v", err)
				}
			},
		},
		{
			name: "source comment deleted",
			mutate: func(t *testing.T, _ *ContinuousDispatchRequest, sourceCommentID pgtype.UUID) {
				t.Helper()
				if _, err := pool.Exec(ctx, `DELETE FROM comment WHERE id = $1`, sourceCommentID); err != nil {
					t.Fatalf("delete source comment: %v", err)
				}
			},
		},
		{
			name: "reviewer is implementation author",
			mutate: func(t *testing.T, req *ContinuousDispatchRequest, _ pgtype.UUID) {
				t.Helper()
				req.Route.LocalAgentID = fixture.agentID
				req.Route.EmployeeRef = continuousDispatchEmployeeRefPrefix + "EMP-PRODUCTION-IMPLEMENTER"
			},
		},
	} {
		t.Run(drift.name, func(t *testing.T) {
			issueID := insertProductionCompanyOpsIssue(t, ctx, pool, fixture, "in_review", "review source drift "+uuid.NewString())
			identityForDrift := identity
			identityForDrift.IssueID = util.UUIDToString(issueID)
			if _, err := pool.Exec(ctx, `UPDATE issue SET metadata = $2::jsonb WHERE id = $1`, issueID,
				`{"stage":"review","candidate_revision":"candidate-review-production","generation":"generation-review-production-1"}`); err != nil {
				t.Fatalf("seed drift issue identity: %v", err)
			}
			driftFixture := fixture
			driftFixture.issueID = issueID
			sourceTaskID, sourceCommentID := seedProductionContinuousDispatchReviewSource(t, ctx, pool, driftFixture, identityForDrift)
			req := newRequest(sourceCommentID, sourceTaskID)
			req.Identity = identityForDrift
			req.reviewProvenance.SourceIssueID = util.UUIDToString(issueID)
			drift.mutate(t, &req, sourceCommentID)
			if _, err := dispatcher.Dispatch(ctx, req); !errors.Is(err, ErrContinuousDispatchReviewLineageDrift) {
				t.Fatalf("Dispatch drift error = %v, want ErrContinuousDispatchReviewLineageDrift", err)
			}
			var taskCount, receiptCount int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, issueID).Scan(&taskCount); err != nil {
				t.Fatalf("count drift tasks: %v", err)
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM continuous_dispatch_receipt WHERE workspace_id = $1 AND issue_id = $2`, fixture.workspaceID, issueID).Scan(&receiptCount); err != nil {
				t.Fatalf("count drift receipts: %v", err)
			}
			if taskCount != 1 || receiptCount != 0 {
				t.Fatalf("drift task/receipt rows = %d/%d, want source-only 1/0", taskCount, receiptCount)
			}
		})
	}
}

func seedProductionContinuousDispatchReviewAgent(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture productionCompanyOpsFixture,
) pgtype.UUID {
	t.Helper()
	var agentID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, runtime_mode, runtime_config, runtime_id, visibility,
			max_concurrent_tasks, owner_id, instructions, custom_env, custom_args, model
		) VALUES ($1, $2, 'cloud', '{}'::jsonb, $3, 'workspace', 1, $4, '', '{}'::jsonb, '[]'::jsonb, 'glm-5.2')
		RETURNING id`,
		fixture.workspaceID, "companyops-reviewer-"+uuid.NewString(), fixture.runtimeID, fixture.userID,
	).Scan(&agentID); err != nil {
		t.Fatalf("seed independent review agent: %v", err)
	}
	return agentID
}

func seedProductionContinuousDispatchReviewSource(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture productionCompanyOpsFixture,
	reviewIdentity continuousdispatch.DispatchIdentity,
) (pgtype.UUID, pgtype.UUID) {
	t.Helper()
	implementationIdentity := reviewIdentity
	implementationIdentity.Stage = "implementation"
	contextValue, err := json.Marshal(shadowTaskContext{ContinuousDispatch: implementationIdentity})
	if err != nil {
		t.Fatalf("encode implementation source context: %v", err)
	}
	var sourceTaskID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority, context, handoff_note, completed_at
		) VALUES ($1, $2, $3, 'completed', 0, $4::jsonb, 'implementation source', now())
		RETURNING id`,
		fixture.agentID, fixture.runtimeID, fixture.issueID, string(contextValue),
	).Scan(&sourceTaskID); err != nil {
		t.Fatalf("seed completed implementation task: %v", err)
	}
	var sourceCommentID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content, source_task_id)
		VALUES ($1, $2, 'agent', $3, 'implementation result', $4)
		RETURNING id`,
		fixture.workspaceID, fixture.issueID, fixture.agentID, sourceTaskID,
	).Scan(&sourceCommentID); err != nil {
		t.Fatalf("seed implementation source comment: %v", err)
	}
	return sourceTaskID, sourceCommentID
}
