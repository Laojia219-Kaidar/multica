package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type companyOpsExecutionTestFixture struct {
	pool       *pgxpool.Pool
	queries    *db.Queries
	service    *TaskService
	company    productionCompanyOpsFixture
	assignment AssignmentDispatchReceipt
}

func newCompanyOpsExecutionTestFixture(t *testing.T) (context.Context, companyOpsExecutionTestFixture) {
	t.Helper()
	pool := newProductionCompanyOpsPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	company := seedProductionCompanyOpsFixture(t, ctx, pool)
	queries := db.New(pool)
	taskService := NewTaskService(queries, pool, nil, events.New())
	backend, err := NewProductionCompanyOpsAssignmentBackend(queries, pool, taskService)
	if err != nil {
		t.Fatalf("NewProductionCompanyOpsAssignmentBackend: %v", err)
	}
	request := productionCompanyOpsRequest(company, company.issueID, util.MustParseUUID(uuid.NewString()))
	receipt, err := NewCompanyOpsAssignmentService(backend).Dispatch(ctx, request)
	if err != nil {
		t.Fatalf("Dispatch CompanyOps assignment: %v", err)
	}
	return ctx, companyOpsExecutionTestFixture{
		pool:       pool,
		queries:    queries,
		service:    taskService,
		company:    company,
		assignment: receipt,
	}
}

func finalizeCompanyOpsExecutionTestClaim(
	t *testing.T,
	ctx context.Context,
	fixture companyOpsExecutionTestFixture,
	task db.AgentTaskQueue,
) error {
	t.Helper()
	agent, err := fixture.queries.GetAgent(ctx, task.AgentID)
	if err != nil {
		return err
	}
	runtime, err := fixture.queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		return err
	}
	var customEnv map[string]string
	if len(agent.CustomEnv) > 0 {
		if err := json.Unmarshal(agent.CustomEnv, &customEnv); err != nil {
			return err
		}
	}
	evidence, err := BuildCompanyOpsExecutionPayloadEvidence(CompanyOpsExecutionPayloadObservation{
		TaskID:          util.UUIDToString(task.ID),
		AgentID:         util.UUIDToString(task.AgentID),
		RuntimeID:       util.UUIDToString(task.RuntimeID),
		AgentName:       agent.Name,
		Instructions:    agent.Instructions,
		CustomEnv:       customEnv,
		AgentModel:      agent.Model.String,
		ThinkingLevel:   agent.ThinkingLevel.String,
		ServiceTier:     agent.ServiceTier.String,
		RuntimeName:     runtime.Name,
		RuntimeMode:     runtime.RuntimeMode,
		RuntimeProvider: runtime.Provider,
	})
	if err != nil {
		return err
	}
	_, err = fixture.service.FinalizeTaskClaim(ctx, task, db.CreateTaskTokenParams{
		TokenHash:   "companyops-execution-" + uuid.NewString(),
		TaskID:      task.ID,
		AgentID:     task.AgentID,
		WorkspaceID: fixture.company.workspaceID,
		UserID:      fixture.company.userID,
		ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}, nil, false, &evidence)
	return err
}

func claimAndFinalizeCompanyOpsExecutionTestTask(
	t *testing.T,
	ctx context.Context,
	fixture companyOpsExecutionTestFixture,
) db.AgentTaskQueue {
	t.Helper()
	task, err := fixture.service.ClaimTask(ctx, fixture.company.agentID)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if task == nil {
		t.Fatal("ClaimTask returned nil")
	}
	if err := finalizeCompanyOpsExecutionTestClaim(t, ctx, fixture, *task); err != nil {
		t.Fatalf("FinalizeTaskClaim: %v", err)
	}
	return *task
}

func enableCompanyOpsExecutionTestRetry(
	t *testing.T,
	ctx context.Context,
	fixture companyOpsExecutionTestFixture,
) {
	t.Helper()
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE agent_task_queue SET max_attempts = 2 WHERE id = $1`,
		fixture.assignment.InitialTaskID,
	); err != nil {
		t.Fatalf("enable CompanyOps test retry: %v", err)
	}
}

func TestCompanyOpsExecutionLifecycle_SingleClaimStartCompleteReplayAndConflict(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE agent SET custom_env = $2::jsonb WHERE id = $1`,
		fixture.company.agentID,
		`{"PRIVATE_TOKEN":"must-not-enter-receipt"}`,
	); err != nil {
		t.Fatalf("seed secret-bearing custom env: %v", err)
	}
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)

	repository := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries)
	claimed, err := repository.GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt after claim: %v", err)
	}
	if claimed.Terminal != nil {
		t.Fatalf("claim unexpectedly has terminal: %+v", claimed.Terminal)
	}
	if claimed.Claim.AssignmentCommandID != fixture.assignment.CommandID ||
		claimed.Claim.Target != fixture.assignment.Target {
		t.Fatalf("claim target = %+v, want assignment %+v", claimed.Claim, fixture.assignment)
	}
	if claimed.Claim.RuntimeDigest != companyOpsDigest(claimed.Claim.RuntimeSnapshot) {
		t.Fatalf("runtime digest = %q, want digest of canonical snapshot", claimed.Claim.RuntimeDigest)
	}
	if strings.Contains(string(claimed.Claim.RuntimeSnapshot), "must-not-enter-receipt") {
		t.Fatalf("runtime snapshot contains a custom environment value: %s", claimed.Claim.RuntimeSnapshot)
	}
	var runtimeFacts map[string]any
	if err := json.Unmarshal(claimed.Claim.RuntimeSnapshot, &runtimeFacts); err != nil {
		t.Fatalf("unmarshal runtime snapshot: %v", err)
	}
	for _, required := range []string{"task_id", "assignment_root_task_id", "assignment_command_id", "payload"} {
		if _, ok := runtimeFacts[required]; !ok {
			t.Fatalf("runtime snapshot missing %q: %s", required, claimed.Claim.RuntimeSnapshot)
		}
	}

	// A stale response reclaim refreshes dispatched_at. Exact receipt replay
	// must retain the first claimed_at rather than conflict on observation time.
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE agent_task_queue SET dispatched_at = dispatched_at + interval '2 minutes' WHERE id = $1`,
		task.ID,
	); err != nil {
		t.Fatalf("refresh dispatched_at for stale replay: %v", err)
	}
	staleReplayTask, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("reload stale replay task: %v", err)
	}
	if err := finalizeCompanyOpsExecutionTestClaim(t, ctx, fixture, staleReplayTask); err != nil {
		t.Fatalf("stale exact claim replay: %v", err)
	}
	replayed, err := repository.GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt after stale replay: %v", err)
	}
	if !replayed.Claim.ClaimedAt.Equal(claimed.Claim.ClaimedAt) {
		t.Fatalf("stale replay claimed_at = %s, want original %s", replayed.Claim.ClaimedAt, claimed.Claim.ClaimedAt)
	}

	var tokenCountBefore int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM task_token WHERE task_id = $1`, task.ID).Scan(&tokenCountBefore); err != nil {
		t.Fatalf("count tokens before drift: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE agent_runtime SET provider = 'drifted-provider' WHERE id = $1`, task.RuntimeID); err != nil {
		t.Fatalf("drift runtime provider: %v", err)
	}
	driftTask, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("reload drift task: %v", err)
	}
	if err := finalizeCompanyOpsExecutionTestClaim(t, ctx, fixture, driftTask); !errors.Is(err, ErrExecutionReceiptConflict) {
		t.Fatalf("runtime drift error = %v, want ErrExecutionReceiptConflict", err)
	}
	var tokenCountAfter int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM task_token WHERE task_id = $1`, task.ID).Scan(&tokenCountAfter); err != nil {
		t.Fatalf("count tokens after drift: %v", err)
	}
	if tokenCountAfter != tokenCountBefore {
		t.Fatalf("receipt conflict committed token count %d, want %d", tokenCountAfter, tokenCountBefore)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE agent_runtime SET provider = 'codex' WHERE id = $1`, task.RuntimeID); err != nil {
		t.Fatalf("restore runtime provider: %v", err)
	}

	started, err := fixture.service.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask with receipt: %v", err)
	}
	if started.Status != "running" {
		t.Fatalf("started status = %q, want running", started.Status)
	}

	result := []byte(`{"output":"built","meta":{"b":2,"a":1}}`)
	completed, err := fixture.service.CompleteTask(ctx, task.ID, result, "", "", false, "")
	if err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	stored, err := repository.GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt after complete: %v", err)
	}
	if stored.Terminal == nil || stored.Terminal.Status != "completed" ||
		!stored.Terminal.CompletedAt.Equal(completed.CompletedAt.Time.UTC()) {
		t.Fatalf("completed terminal = %+v, task completed_at=%s", stored.Terminal, completed.CompletedAt.Time)
	}

	// Object key order and whitespace are canonicalized before hashing.
	exactReplay := []byte(" { \"meta\" : { \"a\" : 1, \"b\" : 2 }, \"output\" : \"built\" } ")
	if _, err := fixture.service.CompleteTask(ctx, task.ID, exactReplay, "", "", false, ""); err != nil {
		t.Fatalf("exact completion replay: %v", err)
	}
	conflict := []byte(`{"output":"different","meta":{"a":1,"b":2}}`)
	if _, err := fixture.service.CompleteTask(ctx, task.ID, conflict, "", "", false, ""); !errors.Is(err, ErrExecutionReceiptConflict) {
		t.Fatalf("completion conflict error = %v, want ErrExecutionReceiptConflict", err)
	}
}

func TestCompanyOpsExecutionLifecycle_BatchClaimUsesSharedFinalizer(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	tasks, err := fixture.service.ClaimTasksForRuntimes(ctx, []pgtype.UUID{fixture.company.runtimeID}, 1)
	if err != nil {
		t.Fatalf("ClaimTasksForRuntimes: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("batch claimed %d tasks, want 1", len(tasks))
	}
	if err := finalizeCompanyOpsExecutionTestClaim(t, ctx, fixture, tasks[0]); err != nil {
		t.Fatalf("batch FinalizeTaskClaim: %v", err)
	}
	receipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries).GetExecutionReceipt(ctx, tasks[0].ID)
	if err != nil {
		t.Fatalf("batch claim execution receipt: %v", err)
	}
	if receipt.Claim.AssignmentCommandID != fixture.assignment.CommandID {
		t.Fatalf("batch claim command = %v, want %v", receipt.Claim.AssignmentCommandID, fixture.assignment.CommandID)
	}
}

func TestCompanyOpsExecutionLifecycle_StartWithoutReceiptRollsBack(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	task, err := fixture.service.ClaimTask(ctx, fixture.company.agentID)
	if err != nil || task == nil {
		t.Fatalf("ClaimTask = %+v, %v", task, err)
	}
	if _, err := fixture.service.StartTask(ctx, task.ID); !errors.Is(err, ErrExecutionReceiptNotFound) {
		t.Fatalf("StartTask missing receipt error = %v, want ErrExecutionReceiptNotFound", err)
	}
	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask after rejected start: %v", err)
	}
	if stored.Status != "dispatched" || stored.StartedAt.Valid {
		t.Fatalf("rejected start committed task = status %q started_at %+v", stored.Status, stored.StartedAt)
	}
}

func TestCompanyOpsExecutionLifecycle_CoherentSnapshotTamperRollsBackStart(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)

	receipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries).GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt: %v", err)
	}
	var snapshot companyOpsRuntimeSnapshot
	if err := json.Unmarshal(receipt.Claim.RuntimeSnapshot, &snapshot); err != nil {
		t.Fatalf("unmarshal runtime snapshot: %v", err)
	}
	snapshot.TaskID = uuid.NewString()
	forgedSnapshot, forgedDigest, err := canonicalSnapshot(snapshot)
	if err != nil {
		t.Fatalf("canonical forged snapshot: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE execution_receipt SET runtime_snapshot = $2, runtime_digest = $3 WHERE task_id = $1`,
		task.ID,
		forgedSnapshot,
		forgedDigest,
	); err != nil {
		t.Fatalf("seed coherent snapshot tamper: %v", err)
	}

	if _, err := fixture.service.StartTask(ctx, task.ID); !errors.Is(err, ErrExecutionReceiptConflict) {
		t.Fatalf("StartTask coherent tamper error = %v, want ErrExecutionReceiptConflict", err)
	}
	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask after coherent tamper: %v", err)
	}
	if stored.Status != "dispatched" || stored.StartedAt.Valid {
		t.Fatalf("coherent tamper committed start: %+v", stored)
	}
}

func TestCompanyOpsExecutionLifecycle_RejectsForgedRetryLineage(t *testing.T) {
	for _, test := range []struct {
		name  string
		forge func(context.Context, companyOpsExecutionTestFixture, pgtype.UUID) error
	}{
		{
			name: "root loses assignment evidence",
			forge: func(ctx context.Context, fixture companyOpsExecutionTestFixture, _ pgtype.UUID) error {
				_, err := fixture.pool.Exec(ctx,
					`UPDATE agent_task_queue SET trigger_evidence_kind = NULL, trigger_evidence_ref_id = NULL WHERE id = $1`,
					fixture.assignment.InitialTaskID,
				)
				return err
			},
		},
		{
			name: "retry loses exact parent id",
			forge: func(ctx context.Context, fixture companyOpsExecutionTestFixture, retryID pgtype.UUID) error {
				_, err := fixture.pool.Exec(ctx,
					`UPDATE agent_task_queue SET parent_task_id = NULL WHERE id = $1`,
					retryID,
				)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, fixture := newCompanyOpsExecutionTestFixture(t)
			enableCompanyOpsExecutionTestRetry(t, ctx, fixture)
			root := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
			if _, err := fixture.service.StartTask(ctx, root.ID); err != nil {
				t.Fatalf("StartTask: %v", err)
			}
			if _, err := fixture.service.FailTask(ctx, root.ID, "retry fixture", "", "", "agent_error.provider_network", false, ""); err != nil {
				t.Fatalf("FailTask: %v", err)
			}
			var retryID pgtype.UUID
			if err := fixture.pool.QueryRow(ctx,
				`SELECT id FROM agent_task_queue WHERE retry_of_task_id = $1`,
				root.ID,
			).Scan(&retryID); err != nil {
				t.Fatalf("load retry child: %v", err)
			}
			if err := test.forge(ctx, fixture, retryID); err != nil {
				t.Fatalf("forge retry lineage: %v", err)
			}
			retry, err := fixture.service.ClaimTask(ctx, fixture.company.agentID)
			if err != nil || retry == nil {
				t.Fatalf("ClaimTask retry = %+v, %v", retry, err)
			}
			if err := finalizeCompanyOpsExecutionTestClaim(t, ctx, fixture, *retry); !errors.Is(err, ErrExecutionReceiptConflict) {
				t.Fatalf("FinalizeTaskClaim forged retry error = %v, want ErrExecutionReceiptConflict", err)
			}
			var tokenCount int
			if err := fixture.pool.QueryRow(ctx,
				`SELECT count(*) FROM task_token WHERE task_id = $1`,
				retry.ID,
			).Scan(&tokenCount); err != nil {
				t.Fatalf("count retry tokens: %v", err)
			}
			if tokenCount != 0 {
				t.Fatalf("forged retry committed %d token(s)", tokenCount)
			}
		})
	}
}

func TestCompanyOpsExecutionLifecycle_FailReplayConflictAndRetryLineage(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	enableCompanyOpsExecutionTestRetry(t, ctx, fixture)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	failed, err := fixture.service.FailTask(ctx, task.ID, "provider stream closed", "", "", "agent_error.provider_network", false, "")
	if err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	repository := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries)
	receipt, err := repository.GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt after fail: %v", err)
	}
	if receipt.Terminal == nil || receipt.Terminal.Status != "failed" ||
		!receipt.Terminal.CompletedAt.Equal(failed.CompletedAt.Time.UTC()) {
		t.Fatalf("failed terminal = %+v, task=%+v", receipt.Terminal, failed)
	}
	if _, err := fixture.service.FailTask(ctx, task.ID, "provider stream closed", "", "", "agent_error.provider_network", false, ""); err != nil {
		t.Fatalf("exact failed replay: %v", err)
	}
	if _, err := fixture.service.FailTask(ctx, task.ID, "different failure", "", "", "agent_error.provider_network", false, ""); !errors.Is(err, ErrExecutionReceiptConflict) {
		t.Fatalf("failed replay conflict error = %v, want ErrExecutionReceiptConflict", err)
	}

	var retryTaskID pgtype.UUID
	if err := fixture.pool.QueryRow(ctx,
		`SELECT id FROM agent_task_queue WHERE retry_of_task_id = $1`,
		task.ID,
	).Scan(&retryTaskID); err != nil {
		t.Fatalf("load retry child: %v", err)
	}
	retryTask, err := fixture.service.ClaimTask(ctx, fixture.company.agentID)
	if err != nil || retryTask == nil {
		t.Fatalf("ClaimTask retry = %+v, %v", retryTask, err)
	}
	if retryTask.ID != retryTaskID || retryTask.RetryOfTaskID != task.ID {
		t.Fatalf("claimed retry lineage = %+v, want child %v parent %v", retryTask, retryTaskID, task.ID)
	}
	if err := finalizeCompanyOpsExecutionTestClaim(t, ctx, fixture, *retryTask); err != nil {
		t.Fatalf("FinalizeTaskClaim retry: %v", err)
	}
	retryReceipt, err := repository.GetExecutionReceipt(ctx, retryTask.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt retry: %v", err)
	}
	if retryReceipt.Claim.AssignmentCommandID != fixture.assignment.CommandID ||
		retryReceipt.Claim.Target != fixture.assignment.Target {
		t.Fatalf("retry receipt lost assignment target: %+v", retryReceipt.Claim)
	}
}

func TestCompanyOpsExecutionLifecycle_ReceiptFailureRollsBackFailAndRetryChild(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	enableCompanyOpsExecutionTestRetry(t, ctx, fixture)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	// Corrupt the immutable claim digest to force the terminal lifecycle to
	// reject. The check runs after CreateRetryTask inside the same transaction.
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE execution_receipt SET runtime_digest = $2 WHERE task_id = $1`,
		task.ID,
		assignmentDigest("0"),
	); err != nil {
		t.Fatalf("seed receipt conflict: %v", err)
	}
	if _, err := fixture.service.FailTask(ctx, task.ID, "provider stream closed", "", "", "agent_error.provider_network", false, ""); !errors.Is(err, ErrExecutionReceiptConflict) {
		t.Fatalf("FailTask receipt conflict error = %v, want ErrExecutionReceiptConflict", err)
	}
	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask after rolled-back fail: %v", err)
	}
	if stored.Status != "running" || stored.CompletedAt.Valid {
		t.Fatalf("receipt failure committed terminal task: %+v", stored)
	}
	var childCount int
	if err := fixture.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE retry_of_task_id = $1`,
		task.ID,
	).Scan(&childCount); err != nil {
		t.Fatalf("count rolled-back retry children: %v", err)
	}
	if childCount != 0 {
		t.Fatalf("receipt failure committed %d retry children, want 0", childCount)
	}
}

func TestCompanyOpsExecutionLifecycle_CanonicalJSONRejectsTrailingValues(t *testing.T) {
	if _, err := canonicalJSON([]byte(`{"ok":true} {"not":"one value"}`)); err == nil {
		t.Fatal("canonicalJSON trailing value error = nil")
	}
	canonical, err := canonicalJSON([]byte(`{"z":1,"a":2}`))
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	if got, want := string(canonical), `{"a":2,"z":1}`; got != want {
		t.Fatalf("canonicalJSON = %s, want %s", got, want)
	}
}
