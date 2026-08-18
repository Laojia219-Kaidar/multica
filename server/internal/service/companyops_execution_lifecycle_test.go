package service

import (
	"bytes"
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
	"github.com/multica-ai/multica/server/internal/readyfrontier"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
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

func TestCompanyOpsExecutionLifecycle_Provider429StaysFailedThroughReceiptAndProjection(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	const (
		rawError      = "API Error: 429 Too Many Requests"
		refinedReason = "agent_error.provider_capacity_or_rate_limit"
	)
	failed, err := fixture.service.FailTask(ctx, task.ID, rawError, "", "", "", false, "")
	if err != nil {
		t.Fatalf("FailTask raw 429: %v", err)
	}
	if failed.Status != "failed" || !failed.CompletedAt.Valid {
		t.Fatalf("failed task terminal = %+v", failed)
	}
	if !failed.Error.Valid || failed.Error.String != rawError {
		t.Fatalf("failed task error = %+v, want %q", failed.Error, rawError)
	}
	if !failed.FailureReason.Valid || failed.FailureReason.String != refinedReason {
		t.Fatalf("failed task failure_reason = %+v, want %q", failed.FailureReason, refinedReason)
	}
	if len(failed.Result) != 0 {
		t.Fatalf("failed task unexpectedly persisted a completion result: %s", failed.Result)
	}

	repository := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries)
	receipt, err := repository.GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt after raw 429: %v", err)
	}
	if receipt.Terminal == nil || receipt.Terminal.Status != "failed" || receipt.Terminal.Error != rawError {
		t.Fatalf("failed receipt terminal = %+v", receipt.Terminal)
	}
	if !receipt.Terminal.CompletedAt.Equal(failed.CompletedAt.Time.UTC()) {
		t.Fatalf("receipt completed_at = %s, task completed_at = %s", receipt.Terminal.CompletedAt, failed.CompletedAt.Time)
	}
	var snapshot companyOpsFailedSnapshot
	if err := json.Unmarshal(receipt.Terminal.ResultSnapshot, &snapshot); err != nil {
		t.Fatalf("unmarshal failed receipt snapshot: %v", err)
	}
	if snapshot.SchemaVersion != companyOpsTerminalSnapshotSchema || snapshot.Status != "failed" ||
		snapshot.Error != rawError || snapshot.FailureReason != refinedReason {
		t.Fatalf("failed receipt snapshot = %+v", snapshot)
	}
	originalTerminal := *receipt.Terminal

	if _, err := fixture.service.CompleteTask(ctx, task.ID, []byte(`{"output":"late success"}`), "", "", false, ""); !errors.Is(err, ErrExecutionReceiptConflict) {
		t.Fatalf("late CompleteTask error = %v, want ErrExecutionReceiptConflict", err)
	}
	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask after late complete: %v", err)
	}
	if stored.Status != "failed" || !stored.FailureReason.Valid || stored.FailureReason.String != refinedReason || len(stored.Result) != 0 {
		t.Fatalf("late complete changed failed task: %+v", stored)
	}
	receipt, err = repository.GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt after late complete: %v", err)
	}
	if receipt.Terminal == nil || !executionTerminalsEqual(*receipt.Terminal, originalTerminal) {
		t.Fatalf("late complete changed failed receipt: before=%+v after=%+v", originalTerminal, receipt.Terminal)
	}

	issue, err := fixture.queries.GetIssue(ctx, fixture.company.issueID)
	if err != nil {
		t.Fatalf("GetIssue after raw 429: %v", err)
	}
	if issue.Status != "todo" {
		t.Fatalf("FailTask changed non-terminal Issue status to %q, want todo", issue.Status)
	}

	// Reproduce a same-created_at tie with an older completed Run. The fixed
	// all-zero UUID is strictly lower than the v4 UUID assigned to the real
	// failed Run, so ListTasksByIssue must use id DESC as its deterministic
	// tie-breaker and keep the failed Run at the projection head.
	historicalTaskID := pgtype.UUID{Valid: true}
	if task.ID == historicalTaskID {
		t.Fatal("random task unexpectedly used the all-zero UUID")
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO agent_task_queue (
			id, agent_id, runtime_id, issue_id, status, priority, completed_at, created_at
		)
		SELECT $2, agent_id, runtime_id, issue_id, 'completed', priority, completed_at, created_at
		FROM agent_task_queue WHERE id = $1`, task.ID, historicalTaskID); err != nil {
		t.Fatalf("seed tied historical completed task: %v", err)
	}
	tasks, err := fixture.queries.ListTasksByIssue(ctx, fixture.company.issueID)
	if err != nil {
		t.Fatalf("ListTasksByIssue: %v", err)
	}
	if len(tasks) != 2 || tasks[0].ID != task.ID || tasks[0].Status != "failed" {
		t.Fatalf("deterministic latest task ordering = %+v, want failed task %v first", tasks, task.ID)
	}
	frontier := composeFrontier(db.ListIssuesRow{
		ID:           issue.ID,
		Status:       issue.Status,
		AssigneeType: issue.AssigneeType,
		AssigneeID:   issue.AssigneeID,
		Metadata:     issue.Metadata,
	}, tasks, nil, nil, nil, time.Now())
	classification := readyfrontier.ClassifyIssue(frontier)
	if classification.State != readyfrontier.StateBlocked || len(classification.Reasons) != 1 ||
		classification.Reasons[0] != readyfrontier.ReasonFailed {
		t.Fatalf("raw 429 Issue projection = %q (%v), want blocked/failed", classification.State, classification.Reasons)
	}

	var retryChildren int
	if err := fixture.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE retry_of_task_id = $1`,
		task.ID,
	).Scan(&retryChildren); err != nil {
		t.Fatalf("count raw 429 retry children: %v", err)
	}
	if retryChildren != 0 {
		t.Fatalf("raw 429 created %d retry children, want 0", retryChildren)
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

func TestCompanyOpsExecutionLifecycle_ArchiveAgentReceiptConflictRollsBackArchiveAndCancel(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE execution_receipt SET runtime_digest = $2 WHERE task_id = $1`,
		task.ID, assignmentDigest("0"),
	); err != nil {
		t.Fatalf("seed receipt conflict: %v", err)
	}

	if _, _, err := fixture.service.ArchiveAgentAndCancelTasks(ctx, fixture.company.agentID, fixture.company.userID); !errors.Is(err, ErrExecutionReceiptConflict) {
		t.Fatalf("ArchiveAgentAndCancelTasks error = %v, want ErrExecutionReceiptConflict", err)
	}
	agent, err := fixture.queries.GetAgent(ctx, fixture.company.agentID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if agent.ArchivedAt.Valid {
		t.Fatalf("receipt conflict committed agent archive: %+v", agent.ArchivedAt)
	}
	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask: %v", err)
	}
	if stored.Status != "running" || stored.CompletedAt.Valid {
		t.Fatalf("receipt conflict committed task cancel: %+v", stored)
	}
	receipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries).GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt: %v", err)
	}
	if receipt.Terminal != nil {
		t.Fatalf("receipt conflict wrote terminal: %+v", receipt.Terminal)
	}
}

func TestCompanyOpsExecutionLifecycle_ManualRerunReceiptConflictRollsBackCancelAndNewTask(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE execution_receipt SET runtime_digest = $2 WHERE task_id = $1`,
		task.ID, assignmentDigest("0"),
	); err != nil {
		t.Fatalf("seed receipt conflict: %v", err)
	}

	var cancelledEvents, queuedEvents int
	fixture.service.Bus.Subscribe(protocol.EventTaskCancelled, func(events.Event) { cancelledEvents++ })
	fixture.service.Bus.Subscribe(protocol.EventTaskQueued, func(events.Event) { queuedEvents++ })
	_, err := fixture.service.RerunIssue(
		ctx,
		fixture.company.issueID,
		task.ID,
		pgtype.UUID{},
		fixture.company.userID,
		nil,
	)
	if !errors.Is(err, ErrExecutionReceiptConflict) {
		t.Fatalf("RerunIssue error = %v, want ErrExecutionReceiptConflict", err)
	}
	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask: %v", err)
	}
	if stored.Status != "running" || stored.CompletedAt.Valid {
		t.Fatalf("receipt conflict committed rerun cancel: %+v", stored)
	}
	var rerunCount int
	if err := fixture.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE rerun_of_task_id = $1`, task.ID,
	).Scan(&rerunCount); err != nil {
		t.Fatalf("count rerun tasks: %v", err)
	}
	if rerunCount != 0 {
		t.Fatalf("receipt conflict committed %d rerun tasks, want 0", rerunCount)
	}
	if cancelledEvents != 0 || queuedEvents != 0 {
		t.Fatalf("receipt conflict emitted events cancelled=%d queued=%d", cancelledEvents, queuedEvents)
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

func markCompanyOpsRuntimeOffline(
	t *testing.T,
	ctx context.Context,
	fixture companyOpsExecutionTestFixture,
) {
	t.Helper()
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE agent_runtime SET status = 'offline', last_seen_at = now() - interval '1 hour' WHERE id = $1`,
		fixture.company.runtimeID,
	); err != nil {
		t.Fatalf("mark runtime offline: %v", err)
	}
}

func TestCompanyOpsExecutionLifecycle_CancelFinalizesReceipt(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	cancelled, err := fixture.service.CancelTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("cancelled status = %q, want cancelled", cancelled.Status)
	}
	if !cancelled.CompletedAt.Valid {
		t.Fatalf("cancelled task missing completed_at")
	}

	receipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries).GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt after cancel: %v", err)
	}
	if receipt.Terminal == nil || receipt.Terminal.Status != "cancelled" ||
		!receipt.Terminal.CompletedAt.Equal(cancelled.CompletedAt.Time.UTC()) {
		t.Fatalf("cancelled terminal = %+v, task completed_at=%s", receipt.Terminal, cancelled.CompletedAt.Time)
	}
}

func TestCompanyOpsExecutionLifecycle_CancelMissingReceiptRollsBack(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	if _, err := fixture.pool.Exec(ctx,
		`DELETE FROM execution_receipt WHERE task_id = $1`, task.ID,
	); err != nil {
		t.Fatalf("delete receipt: %v", err)
	}

	if _, err := fixture.service.CancelTask(ctx, task.ID); !errors.Is(err, ErrExecutionReceiptNotFound) {
		t.Fatalf("CancelTask error = %v, want ErrExecutionReceiptNotFound", err)
	}
	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask after rolled-back cancel: %v", err)
	}
	if stored.Status != "running" || stored.CompletedAt.Valid {
		t.Fatalf("missing-receipt cancel committed terminal task: %+v", stored)
	}
}

func TestCompanyOpsExecutionLifecycle_CancelReplayIdempotent(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	if _, err := fixture.service.CancelTask(ctx, task.ID); err != nil {
		t.Fatalf("first CancelTask: %v", err)
	}
	if _, err := fixture.service.CancelTask(ctx, task.ID); err != nil {
		t.Fatalf("idempotent cancel replay: %v", err)
	}

	receipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries).GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt after replay: %v", err)
	}
	if receipt.Terminal == nil || receipt.Terminal.Status != "cancelled" {
		t.Fatalf("terminal after replay = %+v, want cancelled", receipt.Terminal)
	}
}

func TestCompanyOpsExecutionLifecycle_SweeperOfflineFailFinalizesReceiptAndRetry(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	enableCompanyOpsExecutionTestRetry(t, ctx, fixture)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	markCompanyOpsRuntimeOffline(t, ctx, fixture)
	failedTasks, retried, err := fixture.service.FailTasksForOfflineRuntimes(ctx)
	if err != nil {
		t.Fatalf("FailTasksForOfflineRuntimes: %v", err)
	}
	if len(failedTasks) != 1 || failedTasks[0].ID != task.ID {
		t.Fatalf("failed tasks = %+v, want task %s", failedTasks, util.UUIDToString(task.ID))
	}
	if retried != 1 {
		t.Fatalf("FailTasksForOfflineRuntimes retried = %d, want 1", retried)
	}

	receipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries).GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt after sweeper: %v", err)
	}
	if receipt.Terminal == nil || receipt.Terminal.Status != "failed" {
		t.Fatalf("terminal after sweeper = %+v, want failed", receipt.Terminal)
	}
	var childCount int
	if err := fixture.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE retry_of_task_id = $1`, task.ID,
	).Scan(&childCount); err != nil {
		t.Fatalf("count retry children: %v", err)
	}
	if childCount != 1 {
		t.Fatalf("sweeper retry children = %d, want 1", childCount)
	}
}

func TestCompanyOpsExecutionLifecycle_SweeperTimeoutFinalizesReceipt(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	enableCompanyOpsExecutionTestRetry(t, ctx, fixture)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	markCompanyOpsRuntimeOffline(t, ctx, fixture)
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE agent_task_queue SET started_at = now() - interval '1 hour' WHERE id = $1`, task.ID,
	); err != nil {
		t.Fatalf("backdate running task: %v", err)
	}
	if _, _, err := fixture.service.FailStaleTasks(ctx, db.FailStaleTasksParams{
		DispatchTimeoutSecs: 1,
		RunningTimeoutSecs:  1,
		RuntimeStaleSecs:    1,
	}); err != nil {
		t.Fatalf("FailStaleTasks: %v", err)
	}

	receipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries).GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt after timeout: %v", err)
	}
	if receipt.Terminal == nil || receipt.Terminal.Status != "failed" {
		t.Fatalf("terminal after timeout = %+v, want failed", receipt.Terminal)
	}
}

func TestCompanyOpsExecutionLifecycle_RecoverOrphanedFinalizesReceipt(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	enableCompanyOpsExecutionTestRetry(t, ctx, fixture)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	failedTasks, retried, err := fixture.service.RecoverOrphanedTasksForRuntime(ctx, fixture.company.runtimeID)
	if err != nil {
		t.Fatalf("RecoverOrphanedTasksForRuntime: %v", err)
	}
	if len(failedTasks) != 1 || failedTasks[0].ID != task.ID {
		t.Fatalf("recovered tasks = %+v, want task %s", failedTasks, util.UUIDToString(task.ID))
	}
	if retried != 1 {
		t.Fatalf("RecoverOrphanedTasksForRuntime retried = %d, want 1", retried)
	}

	receipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries).GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt after recover: %v", err)
	}
	if receipt.Terminal == nil || receipt.Terminal.Status != "failed" {
		t.Fatalf("terminal after recover = %+v, want failed", receipt.Terminal)
	}
}

func TestCompanyOpsExecutionLifecycle_QueuedExpiredFinalizesReceiptNoRetry(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)

	if _, err := fixture.pool.Exec(ctx,
		`UPDATE agent_task_queue
		 SET status = 'queued', created_at = now() - interval '3 hours', dispatched_at = NULL
		 WHERE id = $1`, task.ID,
	); err != nil {
		t.Fatalf("backdate queued task: %v", err)
	}
	failedTasks, retried, err := fixture.service.ExpireStaleQueuedTasks(ctx, db.ExpireStaleQueuedTasksParams{
		TtlSecs:    1,
		MaxPerTick: 10,
	})
	if err != nil {
		t.Fatalf("ExpireStaleQueuedTasks: %v", err)
	}
	if len(failedTasks) != 1 || failedTasks[0].ID != task.ID {
		t.Fatalf("expired tasks = %+v, want task %s", failedTasks, util.UUIDToString(task.ID))
	}
	if retried != 0 {
		t.Fatalf("ExpireStaleQueuedTasks retried = %d, want 0 (queued_expired not retryable)", retried)
	}

	receipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries).GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt after queued expiry: %v", err)
	}
	if receipt.Terminal == nil || receipt.Terminal.Status != "failed" {
		t.Fatalf("terminal after queued expiry = %+v, want failed", receipt.Terminal)
	}
	var childCount int
	if err := fixture.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE retry_of_task_id = $1`, task.ID,
	).Scan(&childCount); err != nil {
		t.Fatalf("count retry children: %v", err)
	}
	if childCount != 0 {
		t.Fatalf("queued-expired retry children = %d, want 0", childCount)
	}
}

func TestCompanyOpsExecutionLifecycle_SweeperReceiptConflictBlocksRetry(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	enableCompanyOpsExecutionTestRetry(t, ctx, fixture)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	// Corrupt the immutable claim digest to force the terminal lifecycle to
	// reject. The retry child must NOT be created.
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE execution_receipt SET runtime_digest = $2 WHERE task_id = $1`,
		task.ID,
		assignmentDigest("0"),
	); err != nil {
		t.Fatalf("seed receipt conflict: %v", err)
	}

	if _, _, err := fixture.service.RecoverOrphanedTasksForRuntime(ctx, fixture.company.runtimeID); err == nil {
		t.Fatal("recover with receipt conflict unexpectedly succeeded")
	}
	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask after failed recover: %v", err)
	}
	if stored.Status != "running" || stored.CompletedAt.Valid {
		t.Fatalf("receipt-conflict recover committed terminal task: %+v", stored)
	}

	var childCount int
	if err := fixture.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE retry_of_task_id = $1`, task.ID,
	).Scan(&childCount); err != nil {
		t.Fatalf("count retry children: %v", err)
	}
	if childCount != 0 {
		t.Fatalf("receipt-conflict sweeper committed %d retry children, want 0", childCount)
	}
}

func TestCompanyOpsExecutionLifecycle_RecoverMissingReceiptRollsBack(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	enableCompanyOpsExecutionTestRetry(t, ctx, fixture)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `DELETE FROM execution_receipt WHERE task_id = $1`, task.ID); err != nil {
		t.Fatalf("delete receipt: %v", err)
	}

	if _, _, err := fixture.service.RecoverOrphanedTasksForRuntime(ctx, fixture.company.runtimeID); !errors.Is(err, ErrExecutionReceiptNotFound) {
		t.Fatalf("recover missing receipt error = %v, want ErrExecutionReceiptNotFound", err)
	}
	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask: %v", err)
	}
	if stored.Status != "running" || stored.CompletedAt.Valid {
		t.Fatalf("missing-receipt recover committed terminal task: %+v", stored)
	}
	var childCount int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE retry_of_task_id = $1`, task.ID).Scan(&childCount); err != nil {
		t.Fatalf("count retry children: %v", err)
	}
	if childCount != 0 {
		t.Fatalf("missing-receipt recover committed %d retry children, want 0", childCount)
	}
}

func TestCompanyOpsExecutionLifecycle_NonCompanyOpsRecoverRegression(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	secondIssueID := insertProductionCompanyOpsIssue(t, ctx, fixture.pool, fixture.company, "in_progress", "non-companyops recover")
	taskUUID := uuid.NewString()
	if _, err := fixture.pool.Exec(ctx,
		`INSERT INTO agent_task_queue (id, agent_id, runtime_id, issue_id, status, attempt, max_attempts, originator_user_id, accountable_user_id)
		 VALUES ($1, $2, $3, $4, 'running', 1, 1, $5, $5)`,
		taskUUID, fixture.company.agentID, fixture.company.runtimeID, secondIssueID, fixture.company.userID,
	); err != nil {
		t.Fatalf("insert non-companyops task: %v", err)
	}

	failed, retried, err := fixture.service.RecoverOrphanedTasksForRuntime(ctx, fixture.company.runtimeID)
	if err != nil {
		t.Fatalf("RecoverOrphanedTasksForRuntime non-companyops: %v", err)
	}
	if len(failed) != 1 || retried != 0 {
		t.Fatalf("non-companyops recover = failed:%d retried:%d, want 1/0", len(failed), retried)
	}
	var status string
	if err := fixture.pool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskUUID).Scan(&status); err != nil {
		t.Fatalf("query non-companyops task: %v", err)
	}
	if status != "failed" {
		t.Fatalf("non-companyops recover status = %q, want failed", status)
	}
}

func TestCompanyOpsExecutionLifecycle_CancelFinalizesReceiptTerminalSnapshot(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	cancelled, err := fixture.service.CancelTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	receipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries).GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt: %v", err)
	}
	if receipt.Terminal == nil {
		t.Fatal("missing terminal")
	}

	// Verify the typed cancelled terminal snapshot round-trips through the
	// receipt and matches the digest independently.
	var snapshot companyOpsCancelledSnapshot
	if err := json.Unmarshal(receipt.Terminal.ResultSnapshot, &snapshot); err != nil {
		t.Fatalf("unmarshal cancelled snapshot: %v", err)
	}
	if snapshot.SchemaVersion != companyOpsTerminalSnapshotSchema {
		t.Fatalf("schema = %q, want %q", snapshot.SchemaVersion, companyOpsTerminalSnapshotSchema)
	}
	if snapshot.Status != "cancelled" {
		t.Fatalf("snapshot status = %q, want cancelled", snapshot.Status)
	}
	canonical, digest, err := canonicalSnapshot(snapshot)
	if err != nil {
		t.Fatalf("canonical snapshot: %v", err)
	}
	if digest != receipt.Terminal.OutputDigest {
		t.Fatalf("digest = %q, receipt output_digest = %q", digest, receipt.Terminal.OutputDigest)
	}
	if !bytes.Equal(canonical, receipt.Terminal.ResultSnapshot) {
		t.Fatalf("canonical snapshot does not match stored result_snapshot")
	}
	if !receipt.Terminal.CompletedAt.Equal(cancelled.CompletedAt.Time.UTC()) {
		t.Fatalf("completed_at mismatch: terminal %s vs task %s",
			receipt.Terminal.CompletedAt, cancelled.CompletedAt.Time)
	}
}

// newNilTxCompanyOpsFixture creates a fixture whose TaskService has a nil
// TxStarter (mimicking a test-only wiring). The underlying pool and queries
// are still live so the test can observe row state.
func newNilTxCompanyOpsFixture(t *testing.T) (context.Context, companyOpsExecutionTestFixture) {
	t.Helper()
	pool := newProductionCompanyOpsPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	company := seedProductionCompanyOpsFixture(t, ctx, pool)
	queries := db.New(pool)
	taskService := NewTaskService(queries, nil, nil, events.New())
	taskService.nonTxDB = pool
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

func TestCompanyOpsExecutionLifecycle_NilTxCancelWithReceiptIsZeroWrite(t *testing.T) {
	ctx, fixture := newNilTxCompanyOpsFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	_, err := fixture.service.CancelTask(ctx, task.ID)
	if !errors.Is(err, ErrCompanyOpsTxStarterRequired) {
		t.Fatalf("nil-tx cancel error = %v, want ErrCompanyOpsTxStarterRequired", err)
	}

	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask: %v", err)
	}
	if stored.Status != "running" || stored.CompletedAt.Valid {
		t.Fatalf("nil-tx cancel wrote terminal state: %+v", stored)
	}
}

func TestCompanyOpsExecutionLifecycle_NilTxFailWithReceiptIsZeroWrite(t *testing.T) {
	ctx, fixture := newNilTxCompanyOpsFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	_, err := fixture.service.FailTask(ctx, task.ID, "provider error", "", "", "agent_error.provider_network", false, "")
	if !errors.Is(err, ErrCompanyOpsTxStarterRequired) {
		t.Fatalf("nil-tx fail error = %v, want ErrCompanyOpsTxStarterRequired", err)
	}

	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask: %v", err)
	}
	if stored.Status != "running" || stored.CompletedAt.Valid {
		t.Fatalf("nil-tx fail wrote terminal state: %+v", stored)
	}
}

func TestCompanyOpsExecutionLifecycle_NilTxRecoverCompanyOpsIsZeroWrite(t *testing.T) {
	ctx, fixture := newNilTxCompanyOpsFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	failed, retried, err := fixture.service.RecoverOrphanedTasksForRuntime(ctx, fixture.company.runtimeID)
	if !errors.Is(err, ErrCompanyOpsTxStarterRequired) {
		t.Fatalf("nil-tx recover error = %v, want ErrCompanyOpsTxStarterRequired", err)
	}
	if len(failed) != 0 || retried != 0 {
		t.Fatalf("nil-tx recover result = (%+v, %d), want no failed tasks or retries", failed, retried)
	}

	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask: %v", err)
	}
	if stored.Status != "running" || stored.CompletedAt.Valid {
		t.Fatalf("nil-tx recover wrote terminal state: %+v", stored)
	}
	receipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries).GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt: %v", err)
	}
	if receipt.Terminal != nil {
		t.Fatalf("nil-tx recover wrote terminal receipt: %+v", receipt.Terminal)
	}
	var childCount int
	if err := fixture.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE retry_of_task_id = $1`, task.ID,
	).Scan(&childCount); err != nil {
		t.Fatalf("count retry children: %v", err)
	}
	if childCount != 0 {
		t.Fatalf("nil-tx recover created %d retry children, want 0", childCount)
	}
}

func assertNilTxCompanyOpsFailureIsZeroWrite(
	t *testing.T,
	ctx context.Context,
	fixture companyOpsExecutionTestFixture,
	task db.AgentTaskQueue,
	err error,
) {
	t.Helper()
	if !errors.Is(err, ErrCompanyOpsTxStarterRequired) {
		t.Fatalf("nil-tx bulk failure error = %v, want ErrCompanyOpsTxStarterRequired", err)
	}
	stored, loadErr := fixture.queries.GetAgentTask(ctx, task.ID)
	if loadErr != nil {
		t.Fatalf("GetAgentTask: %v", loadErr)
	}
	if stored.Status != task.Status || stored.CompletedAt.Valid {
		t.Fatalf("nil-tx bulk failure wrote task state: before=%+v after=%+v", task, stored)
	}
	receipt, receiptErr := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries).GetExecutionReceipt(ctx, task.ID)
	if receiptErr != nil {
		t.Fatalf("GetExecutionReceipt: %v", receiptErr)
	}
	if receipt.Terminal != nil {
		t.Fatalf("nil-tx bulk failure wrote receipt terminal: %+v", receipt.Terminal)
	}
	var childCount int
	if queryErr := fixture.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE retry_of_task_id = $1`, task.ID,
	).Scan(&childCount); queryErr != nil {
		t.Fatalf("count retry children: %v", queryErr)
	}
	if childCount != 0 {
		t.Fatalf("nil-tx bulk failure created %d retry children, want 0", childCount)
	}
}

func TestCompanyOpsExecutionLifecycle_NilTxOfflineCompanyOpsIsZeroWrite(t *testing.T) {
	ctx, fixture := newNilTxCompanyOpsFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	task, _ = fixture.queries.GetAgentTask(ctx, task.ID)
	markCompanyOpsRuntimeOffline(t, ctx, fixture)
	_, _, err := fixture.service.FailTasksForOfflineRuntimes(ctx)
	assertNilTxCompanyOpsFailureIsZeroWrite(t, ctx, fixture, task, err)
}

func TestCompanyOpsExecutionLifecycle_NilTxTimeoutCompanyOpsIsZeroWrite(t *testing.T) {
	ctx, fixture := newNilTxCompanyOpsFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE agent_task_queue SET started_at = now() - interval '1 hour' WHERE id = $1`, task.ID,
	); err != nil {
		t.Fatalf("backdate task: %v", err)
	}
	markCompanyOpsRuntimeOffline(t, ctx, fixture)
	task, _ = fixture.queries.GetAgentTask(ctx, task.ID)
	_, _, err := fixture.service.FailStaleTasks(ctx, db.FailStaleTasksParams{
		DispatchTimeoutSecs: 1,
		RunningTimeoutSecs:  1,
		RuntimeStaleSecs:    1,
	})
	assertNilTxCompanyOpsFailureIsZeroWrite(t, ctx, fixture, task, err)
}

func TestCompanyOpsExecutionLifecycle_NilTxQueuedExpiredCompanyOpsIsZeroWrite(t *testing.T) {
	ctx, fixture := newNilTxCompanyOpsFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE agent_task_queue SET status = 'queued', dispatched_at = NULL, created_at = now() - interval '1 hour' WHERE id = $1`, task.ID,
	); err != nil {
		t.Fatalf("backdate queued task: %v", err)
	}
	task, _ = fixture.queries.GetAgentTask(ctx, task.ID)
	_, _, err := fixture.service.ExpireStaleQueuedTasks(ctx, db.ExpireStaleQueuedTasksParams{TtlSecs: 1, MaxPerTick: 10})
	assertNilTxCompanyOpsFailureIsZeroWrite(t, ctx, fixture, task, err)
}

func insertNilTxNonCompanyOpsTask(
	t *testing.T,
	ctx context.Context,
	fixture companyOpsExecutionTestFixture,
	status string,
	ageSeconds float64,
) pgtype.UUID {
	t.Helper()
	var taskID pgtype.UUID
	if err := fixture.pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, status, priority, created_at, dispatched_at, started_at,
			attempt, max_attempts, originator_user_id, accountable_user_id
		) VALUES (
			$1, $2, $3, 0, now() - make_interval(secs => $4::double precision),
			CASE WHEN $3 = 'dispatched' THEN now() - make_interval(secs => $4::double precision) END,
			CASE WHEN $3 = 'running' THEN now() - make_interval(secs => $4::double precision) END,
			1, 1, $5, $5
		) RETURNING id`,
		fixture.company.agentID, fixture.company.runtimeID, status, ageSeconds, fixture.company.userID,
	).Scan(&taskID); err != nil {
		t.Fatalf("insert nil-tx non-CompanyOps task: %v", err)
	}
	return taskID
}

func assertNilTxNonCompanyOpsFailed(t *testing.T, ctx context.Context, fixture companyOpsExecutionTestFixture, taskID pgtype.UUID) {
	t.Helper()
	task, err := fixture.queries.GetAgentTask(ctx, taskID)
	if err != nil {
		t.Fatalf("GetAgentTask: %v", err)
	}
	if task.Status != "failed" || !task.CompletedAt.Valid {
		t.Fatalf("nil-tx non-CompanyOps task = %+v, want failed terminal", task)
	}
}

func TestCompanyOpsExecutionLifecycle_NilTxOfflineNonCompanyOpsSucceeds(t *testing.T) {
	ctx, fixture := newNilTxCompanyOpsFixture(t)
	taskID := insertNilTxNonCompanyOpsTask(t, ctx, fixture, "running", 10)
	markCompanyOpsRuntimeOffline(t, ctx, fixture)
	if _, _, err := fixture.service.FailTasksForOfflineRuntimes(ctx); err != nil {
		t.Fatalf("nil-tx offline non-CompanyOps: %v", err)
	}
	assertNilTxNonCompanyOpsFailed(t, ctx, fixture, taskID)
}

func TestCompanyOpsExecutionLifecycle_NilTxTimeoutNonCompanyOpsSucceeds(t *testing.T) {
	ctx, fixture := newNilTxCompanyOpsFixture(t)
	taskID := insertNilTxNonCompanyOpsTask(t, ctx, fixture, "running", 3600)
	markCompanyOpsRuntimeOffline(t, ctx, fixture)
	if _, _, err := fixture.service.FailStaleTasks(ctx, db.FailStaleTasksParams{
		DispatchTimeoutSecs: 1,
		RunningTimeoutSecs:  1,
		RuntimeStaleSecs:    1,
	}); err != nil {
		t.Fatalf("nil-tx timeout non-CompanyOps: %v", err)
	}
	assertNilTxNonCompanyOpsFailed(t, ctx, fixture, taskID)
}

func TestCompanyOpsExecutionLifecycle_NilTxRecoverNonCompanyOpsSucceeds(t *testing.T) {
	ctx, fixture := newNilTxCompanyOpsFixture(t)
	taskID := insertNilTxNonCompanyOpsTask(t, ctx, fixture, "running", 10)
	if _, _, err := fixture.service.RecoverOrphanedTasksForRuntime(ctx, fixture.company.runtimeID); err != nil {
		t.Fatalf("nil-tx recover non-CompanyOps: %v", err)
	}
	assertNilTxNonCompanyOpsFailed(t, ctx, fixture, taskID)
}

func TestCompanyOpsExecutionLifecycle_NilTxQueuedExpiredNonCompanyOpsSucceeds(t *testing.T) {
	ctx, fixture := newNilTxCompanyOpsFixture(t)
	taskID := insertNilTxNonCompanyOpsTask(t, ctx, fixture, "queued", 3600)
	if _, _, err := fixture.service.ExpireStaleQueuedTasks(ctx, db.ExpireStaleQueuedTasksParams{TtlSecs: 1, MaxPerTick: 10}); err != nil {
		t.Fatalf("nil-tx queued expiry non-CompanyOps: %v", err)
	}
	assertNilTxNonCompanyOpsFailed(t, ctx, fixture, taskID)
}

func TestCompanyOpsExecutionLifecycle_NilTxBulkCancelCompanyOpsIsZeroWrite(t *testing.T) {
	ctx, fixture := newNilTxCompanyOpsFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	err := fixture.service.CancelTasksForIssue(ctx, fixture.company.issueID)
	if !errors.Is(err, ErrCompanyOpsTxStarterRequired) {
		t.Fatalf("nil-tx bulk cancel error = %v, want ErrCompanyOpsTxStarterRequired", err)
	}
	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask: %v", err)
	}
	if stored.Status != "running" || stored.CompletedAt.Valid {
		t.Fatalf("nil-tx bulk cancel wrote terminal state: %+v", stored)
	}
	receipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries).GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt: %v", err)
	}
	if receipt.Terminal != nil {
		t.Fatalf("nil-tx bulk cancel wrote terminal receipt: %+v", receipt.Terminal)
	}
}

func TestCompanyOpsExecutionLifecycle_NilTxBulkCancelNonCompanyOpsSucceeds(t *testing.T) {
	ctx, fixture := newNilTxCompanyOpsFixture(t)
	secondIssueID := insertProductionCompanyOpsIssue(t, ctx, fixture.pool, fixture.company, "in_progress", "non-companyops nil-tx bulk")
	taskUUID := uuid.NewString()
	if _, err := fixture.pool.Exec(ctx,
		`INSERT INTO agent_task_queue (id, agent_id, runtime_id, issue_id, status, attempt, max_attempts, originator_user_id, accountable_user_id)
		 VALUES ($1, $2, $3, $4, 'queued', 1, 1, $5, $5)`,
		taskUUID, fixture.company.agentID, fixture.company.runtimeID, secondIssueID, fixture.company.userID,
	); err != nil {
		t.Fatalf("insert non-companyops task: %v", err)
	}

	if err := fixture.service.CancelTasksForIssue(ctx, secondIssueID); err != nil {
		t.Fatalf("nil-tx bulk cancel non-companyops: %v", err)
	}
	var status string
	if err := fixture.pool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskUUID).Scan(&status); err != nil {
		t.Fatalf("query non-companyops task: %v", err)
	}
	if status != "cancelled" {
		t.Fatalf("nil-tx bulk non-companyops status = %q, want cancelled", status)
	}
}

func TestCompanyOpsExecutionLifecycle_NilTxCancelWithoutReceiptIsZeroWrite(t *testing.T) {
	ctx, fixture := newNilTxCompanyOpsFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	if _, err := fixture.pool.Exec(ctx,
		`DELETE FROM execution_receipt WHERE task_id = $1`, task.ID,
	); err != nil {
		t.Fatalf("delete receipt: %v", err)
	}

	// After receipt deletion, the task is no longer detected as CompanyOps by
	// the receipt-based gate. But CancelTask still runs the full receipt-based
	// terminal lifecycle inside runInTx. Since the receipt is missing, the
	// finalize path returns ErrExecutionReceiptNotFound — which is the same
	// behavior as the transactional fixture. The key assertion is that no
	// partial write occurs.
	_, err := fixture.service.CancelTask(ctx, task.ID)
	if err == nil {
		t.Fatal("nil-tx cancel without receipt unexpectedly succeeded")
	}

	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask: %v", err)
	}
	if stored.Status != "running" || stored.CompletedAt.Valid {
		t.Fatalf("nil-tx cancel without receipt wrote terminal state: %+v", stored)
	}
}

func TestCompanyOpsExecutionLifecycle_BulkCancelValidNonCompanyOpsTask(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)

	// Create a second issue with a non-CompanyOps task (no assignment dispatch,
	// no receipt). The CompanyOps fixture issue already has a pending task so
	// the one-pending-task-per-issue-agent constraint would reject a second.
	secondIssueID := insertProductionCompanyOpsIssue(t, ctx, fixture.pool, fixture.company, "in_progress", "non-companyops bulk cancel")
	taskUUID := uuid.NewString()
	if _, err := fixture.pool.Exec(ctx,
		`INSERT INTO agent_task_queue (id, agent_id, runtime_id, issue_id, status, attempt, max_attempts, originator_user_id, accountable_user_id)
		 VALUES ($1, $2, $3, $4, 'queued', 1, 1, $5, $5)`,
		taskUUID, fixture.company.agentID, fixture.company.runtimeID, secondIssueID, fixture.company.userID,
	); err != nil {
		t.Fatalf("insert non-companyops task: %v", err)
	}

	// CancelTasksForIssue should succeed for a non-CompanyOps task.
	if err := fixture.service.CancelTasksForIssue(ctx, secondIssueID); err != nil {
		t.Fatalf("CancelTasksForIssue non-companyops: %v", err)
	}

	var status string
	if err := fixture.pool.QueryRow(ctx,
		`SELECT status FROM agent_task_queue WHERE id = $1`, taskUUID,
	).Scan(&status); err != nil {
		t.Fatalf("query non-companyops task status: %v", err)
	}
	if status != "cancelled" {
		t.Fatalf("non-companyops bulk cancel status = %q, want cancelled", status)
	}
}

func TestCompanyOpsExecutionLifecycle_BulkCancelCompanyOpsConflictRollsBack(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	// Corrupt the receipt to force a conflict during finalization.
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE execution_receipt SET runtime_digest = $2 WHERE task_id = $1`,
		task.ID, assignmentDigest("0"),
	); err != nil {
		t.Fatalf("seed receipt conflict: %v", err)
	}

	// Bulk cancel should roll back entirely — the task must remain running.
	err := fixture.service.CancelTasksForIssue(ctx, fixture.company.issueID)
	if err == nil {
		t.Fatal("bulk cancel with receipt conflict unexpectedly succeeded")
	}

	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask after rolled-back bulk cancel: %v", err)
	}
	if stored.Status != "running" || stored.CompletedAt.Valid {
		t.Fatalf("bulk cancel conflict committed terminal task: %+v", stored)
	}
}

func TestCompanyOpsExecutionLifecycle_OuterTransactionCancelConflictRollsBack(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	if _, err := fixture.pool.Exec(ctx,
		`UPDATE execution_receipt SET runtime_digest = $2 WHERE task_id = $1`,
		task.ID, assignmentDigest("0"),
	); err != nil {
		t.Fatalf("seed receipt conflict: %v", err)
	}

	tx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := db.New(tx)
	cancelled, err := qtx.CancelAgentTasksByIssue(ctx, fixture.company.issueID)
	if err != nil {
		t.Fatalf("CancelAgentTasksByIssue: %v", err)
	}
	if len(cancelled) != 1 || cancelled[0].ID != task.ID {
		t.Fatalf("outer transaction cancelled tasks = %+v, want task %s", cancelled, util.UUIDToString(task.ID))
	}
	if err := fixture.service.FinalizeCancelledTasksInTx(ctx, qtx, cancelled); err == nil {
		t.Fatal("outer transaction receipt conflict unexpectedly succeeded")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask after outer rollback: %v", err)
	}
	if stored.Status != "running" || stored.CompletedAt.Valid {
		t.Fatalf("outer transaction conflict committed terminal task: %+v", stored)
	}
	receipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries).GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt after outer rollback: %v", err)
	}
	if receipt.Terminal != nil {
		t.Fatalf("outer transaction conflict wrote terminal receipt: %+v", receipt.Terminal)
	}
}

func TestCompanyOpsExecutionLifecycle_BulkCancelCompanyOpsMissingReceiptRollsBack(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	if _, err := fixture.pool.Exec(ctx,
		`DELETE FROM execution_receipt WHERE task_id = $1`, task.ID,
	); err != nil {
		t.Fatalf("delete receipt: %v", err)
	}

	err := fixture.service.CancelTasksForIssue(ctx, fixture.company.issueID)
	if err == nil {
		t.Fatal("bulk cancel with missing receipt unexpectedly succeeded")
	}

	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask after rolled-back bulk cancel: %v", err)
	}
	if stored.Status != "running" || stored.CompletedAt.Valid {
		t.Fatalf("bulk cancel missing receipt committed terminal task: %+v", stored)
	}
}

func TestCompanyOpsExecutionLifecycle_SweeperReceiptConflictBlocksBroadcastAndIssueReset(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	enableCompanyOpsExecutionTestRetry(t, ctx, fixture)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	// Set the issue to in_progress so HandleFailedTasks would normally reset
	// it to todo. With a receipt conflict, the reset must NOT happen.
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE issue SET status = 'in_progress' WHERE id = $1`, fixture.company.issueID,
	); err != nil {
		t.Fatalf("set issue in_progress: %v", err)
	}

	// Capture task:failed events published during HandleFailedTasks.
	var failedEvents []string
	fixture.service.Bus.Subscribe(protocol.EventTaskFailed, func(e events.Event) {
		failedEvents = append(failedEvents, e.TaskID)
	})

	// Corrupt the receipt to force a conflict.
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE execution_receipt SET runtime_digest = $2 WHERE task_id = $1`,
		task.ID, assignmentDigest("0"),
	); err != nil {
		t.Fatalf("seed receipt conflict: %v", err)
	}

	if _, _, err := fixture.service.RecoverOrphanedTasksForRuntime(ctx, fixture.company.runtimeID); err == nil {
		t.Fatal("recover with receipt conflict unexpectedly succeeded")
	}

	// No task:failed event should be published for the conflicted task.
	if len(failedEvents) != 0 {
		t.Fatalf("expected 0 task:failed events, got %d (%v)", len(failedEvents), failedEvents)
	}

	// The issue must NOT be reset to todo.
	issue, err := fixture.queries.GetIssue(ctx, fixture.company.issueID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Status != "in_progress" {
		t.Fatalf("issue status = %q, want in_progress (reset must be blocked on receipt conflict)", issue.Status)
	}

	// No retry child created.
	var childCount int
	if err := fixture.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE retry_of_task_id = $1`, task.ID,
	).Scan(&childCount); err != nil {
		t.Fatalf("count retry children: %v", err)
	}
	if childCount != 0 {
		t.Fatalf("receipt-conflict sweeper committed %d retry children, want 0", childCount)
	}
	stored, err := fixture.queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetAgentTask after receipt conflict: %v", err)
	}
	if stored.Status != "running" || stored.CompletedAt.Valid {
		t.Fatalf("receipt-conflict sweeper committed terminal task: %+v", stored)
	}
	receipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries).GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt after receipt conflict: %v", err)
	}
	if receipt.Terminal != nil {
		t.Fatalf("receipt-conflict sweeper committed terminal receipt: %+v", receipt.Terminal)
	}
}

func TestCompanyOpsExecutionLifecycle_NonCompanyOpsCancelRegression(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)

	// Create a non-CompanyOps task on a second issue (no assignment dispatch,
	// no receipt).
	secondIssueID := insertProductionCompanyOpsIssue(t, ctx, fixture.pool, fixture.company, "in_progress", "non-companyops cancel")
	taskUUID := uuid.NewString()
	if _, err := fixture.pool.Exec(ctx,
		`INSERT INTO agent_task_queue (id, agent_id, runtime_id, issue_id, status, attempt, max_attempts, originator_user_id, accountable_user_id)
		 VALUES ($1, $2, $3, $4, 'queued', 1, 1, $5, $5)`,
		taskUUID, fixture.company.agentID, fixture.company.runtimeID, secondIssueID, fixture.company.userID,
	); err != nil {
		t.Fatalf("insert non-companyops task: %v", err)
	}

	// Cancel must succeed for non-CompanyOps tasks — the receipt gate returns
	// nil and the cancel CAS proceeds normally.
	parsed, err := util.ParseUUID(taskUUID)
	if err != nil {
		t.Fatalf("parse task UUID: %v", err)
	}
	cancelled, err := fixture.service.CancelTask(ctx, parsed)
	if err != nil {
		t.Fatalf("CancelTask non-companyops: %v", err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("non-companyops cancel status = %q, want cancelled", cancelled.Status)
	}
}

func TestCompanyOpsExecutionLifecycle_NilTxCancelNonCompanyOpsSucceeds(t *testing.T) {
	ctx, fixture := newNilTxCompanyOpsFixture(t)

	// Create a non-CompanyOps task on a second issue (no assignment dispatch,
	// no receipt).
	secondIssueID := insertProductionCompanyOpsIssue(t, ctx, fixture.pool, fixture.company, "in_progress", "non-companyops nil-tx")
	taskUUID := uuid.NewString()
	if _, err := fixture.pool.Exec(ctx,
		`INSERT INTO agent_task_queue (id, agent_id, runtime_id, issue_id, status, attempt, max_attempts, originator_user_id, accountable_user_id)
		 VALUES ($1, $2, $3, $4, 'queued', 1, 1, $5, $5)`,
		taskUUID, fixture.company.agentID, fixture.company.runtimeID, secondIssueID, fixture.company.userID,
	); err != nil {
		t.Fatalf("insert non-companyops task: %v", err)
	}

	parsed, err := util.ParseUUID(taskUUID)
	if err != nil {
		t.Fatalf("parse task UUID: %v", err)
	}
	cancelled, err := fixture.service.CancelTask(ctx, parsed)
	if err != nil {
		t.Fatalf("nil-tx cancel non-companyops: %v", err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("nil-tx non-companyops cancel status = %q, want cancelled", cancelled.Status)
	}
}

func TestCompanyOpsExecutionLifecycle_RetryChildClaimableWithExactLineage(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	enableCompanyOpsExecutionTestRetry(t, ctx, fixture)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	// Fail via the sweeper path — the retry child is created in the same
	// transaction as the receipt finalization.
	failedTasks, retried, err := fixture.service.RecoverOrphanedTasksForRuntime(ctx, fixture.company.runtimeID)
	if err != nil {
		t.Fatalf("RecoverOrphanedTasksForRuntime: %v", err)
	}
	if len(failedTasks) != 1 || failedTasks[0].ID != task.ID {
		t.Fatalf("recovered tasks = %+v, want task %s", failedTasks, util.UUIDToString(task.ID))
	}
	if retried != 1 {
		t.Fatalf("RecoverOrphanedTasksForRuntime retried = %d, want 1", retried)
	}

	// The retry child must be claimable and resolve to the same assignment
	// lineage as the root.
	var retryTaskID pgtype.UUID
	if err := fixture.pool.QueryRow(ctx,
		`SELECT id FROM agent_task_queue WHERE retry_of_task_id = $1`, task.ID,
	).Scan(&retryTaskID); err != nil {
		t.Fatalf("load retry child: %v", err)
	}

	retryTask, err := fixture.queries.GetAgentTask(ctx, retryTaskID)
	if err != nil {
		t.Fatalf("GetAgentTask retry child: %v", err)
	}
	if retryTask.Status != "queued" {
		t.Fatalf("retry child status = %q, want queued", retryTask.Status)
	}

	claimed, err := fixture.service.ClaimTask(ctx, fixture.company.agentID)
	if err != nil {
		t.Fatalf("ClaimTask retry child: %v", err)
	}
	if claimed == nil || claimed.ID != retryTaskID {
		t.Fatalf("claimed retry child = %+v, want %s", claimed, util.UUIDToString(retryTaskID))
	}
	if err := finalizeCompanyOpsExecutionTestClaim(t, ctx, fixture, *claimed); err != nil {
		t.Fatalf("FinalizeTaskClaim retry child: %v", err)
	}
	childReceipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(fixture.queries).GetExecutionReceipt(ctx, retryTaskID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt retry child: %v", err)
	}
	if childReceipt.Claim.TaskID != retryTaskID || childReceipt.Claim.ClaimedAt.IsZero() {
		t.Fatalf("retry child receipt missing exact claim: %+v", childReceipt.Claim)
	}

	// Verify the retry child's lineage resolves to the same assignment command.
	lineage, err := resolveCompanyOpsAssignmentLineage(ctx, fixture.queries, retryTask)
	if err != nil {
		t.Fatalf("resolve retry child lineage: %v", err)
	}
	if lineage == nil {
		t.Fatal("retry child lineage is nil, want canonical CompanyOps assignment")
	}
	if lineage.commandID != fixture.assignment.CommandID {
		t.Fatalf("retry child command = %v, want %v", lineage.commandID, fixture.assignment.CommandID)
	}
	if lineage.rootTaskID != fixture.assignment.InitialTaskID {
		t.Fatalf("retry child root = %v, want %v", lineage.rootTaskID, fixture.assignment.InitialTaskID)
	}
}
