package service

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// newOperationalModePool opens the isolated worktree DB (127.0.0.1:55432)
// only. The operational-mode claim tests are real claim-path integration
// tests: they must never fall back to localhost:5432 or any other database.
func newOperationalModePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL not set; skipping operational-mode claim integration test")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	if parsed.Port() == "5432" {
		t.Skip("refusing to connect operational-mode claim test to port 5432")
	}
	if parsed.Port() != "55432" {
		t.Skipf("operational-mode claim test requires isolated worktree port 55432, got %q", parsed.Port())
	}
	if host := parsed.Hostname(); host != "127.0.0.1" && host != "localhost" && host != "::1" {
		t.Skipf("operational-mode claim test requires a loopback database host, got %q", host)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open isolated DATABASE_URL: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping isolated DATABASE_URL: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// operationalModeClaimService builds the TaskService used by the claim-gate
// tests. Following the pattern of the surrounding claim tests, the events
// bus is a fresh no-op bus and the pool is the shared test pool.
func operationalModeClaimService(pool *pgxpool.Pool) *TaskService {
	return NewTaskService(db.New(pool), pool, nil, events.New())
}

// modeClaimIssueNumber returns a per-workspace-unique issue number. Each test
// creates its own workspace, but within one test the number must still be
// unique (issue has UNIQUE (workspace_id, number)).
var modeIssueCounter int64

func modeClaimIssueNumber() int {
	modeIssueCounter++
	return 700000 + int(modeIssueCounter)
}

// modeClaimFixture provisions a workspace, member, runtime, and agent whose
// operational_mode is set to mode. max_concurrent_tasks=2 so a single queued
// task never trips the capacity gate: the mode tests exercise the mode gate,
// not capacity. Cleanup is LIFO: the CHECK-constraint restore of the unknown
// mode test (registered later) runs before this fixture teardown.
func modeClaimFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, mode string) (agentID, runtimeID, workspaceID, userID string) {
	t.Helper()
	suffix := time.Now().UnixNano()

	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ($1,$2) RETURNING id`,
		"Mode Claim Test", fmt.Sprintf("mode-claim-%d@multica.ai", suffix)).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspace (name, slug, description, issue_prefix) VALUES ($1,$2,$3,$4) RETURNING id`,
		"Mode Claim Test", fmt.Sprintf("mode-claim-%d", suffix), "temp mode claim test workspace", "MCM").Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1,$2,'owner')`, workspaceID, userID); err != nil {
		t.Fatalf("create member: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, visibility, owner_id)
		VALUES ($1, NULL, $2, 'cloud', 'mode_claim_test', 'online', 'test runtime', '{}'::jsonb, now(), 'private', $3)
		RETURNING id`, workspaceID, "Mode Claim Runtime", userID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, runtime_id, visibility, max_concurrent_tasks, owner_id, operational_mode)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 2, $4, $5)
		RETURNING id`, workspaceID, "Mode Claim Agent", runtimeID, userID, mode).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM agent_task_queue WHERE agent_id = $1`, agentID)
		pool.Exec(c, `DELETE FROM issue WHERE workspace_id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM agent WHERE id = $1`, agentID)
		pool.Exec(c, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
		pool.Exec(c, `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, workspaceID, userID)
		pool.Exec(c, `DELETE FROM workspace WHERE id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM "user" WHERE id = $1`, userID)
	})
	return agentID, runtimeID, workspaceID, userID
}

func modeClaimEnqueueIssueTask(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agentID, runtimeID, workspaceID, userID string) {
	t.Helper()
	var issueID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
		VALUES ($1, $2, 'in_progress', 'none', $3, 'member', $4, 0)
		RETURNING id`, workspaceID, "mode claim issue", userID, modeClaimIssueNumber()).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, issue_id, status, priority, context, runtime_id)
		VALUES ($1, $2, 'queued', 0, '{}'::jsonb, $3)`, agentID, issueID, runtimeID); err != nil {
		t.Fatalf("create issue task: %v", err)
	}
}

func modeClaimEnqueueQuickCreateTask(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agentID, runtimeID string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, status, priority, context, runtime_id)
		VALUES ($1, 'queued', 0, '{}'::jsonb, $2)`, agentID, runtimeID); err != nil {
		t.Fatalf("create quick-create task: %v", err)
	}
}

func modeClaimEnqueueChatTask(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agentID, runtimeID, workspaceID, userID string) {
	t.Helper()
	var chatSessionID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title)
		VALUES ($1, $2, $3, 'mode claim chat')
		RETURNING id`, workspaceID, agentID, userID).Scan(&chatSessionID); err != nil {
		t.Fatalf("create chat session: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, chat_session_id, status, priority, context, runtime_id)
		VALUES ($1, $2, 'queued', 0, '{}'::jsonb, $3)`, agentID, chatSessionID, runtimeID); err != nil {
		t.Fatalf("create chat task: %v", err)
	}
}

// modeClaimEnqueueAutopilotTask creates an autopilot-shaped task: an issue
// linked to an autopilot_run. It is neither a direct-chat nor a quick-create
// source, so training mode must reject it.
func modeClaimEnqueueAutopilotTask(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agentID, runtimeID, workspaceID, userID string) {
	t.Helper()
	var issueID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
		VALUES ($1, $2, 'in_progress', 'none', $3, 'member', $4, 0)
		RETURNING id`, workspaceID, "mode claim autopilot issue", userID, modeClaimIssueNumber()).Scan(&issueID); err != nil {
		t.Fatalf("create issue for autopilot: %v", err)
	}
	var autopilotID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO autopilot (workspace_id, title, assignee_id, created_by_type, created_by_id)
		VALUES ($1, 'mode claim autopilot', $2, 'member', $3)
		RETURNING id`, workspaceID, agentID, userID).Scan(&autopilotID); err != nil {
		t.Fatalf("create autopilot: %v", err)
	}
	var runID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO autopilot_run (autopilot_id, source, status, issue_id)
		VALUES ($1, 'manual', 'completed', $2)
		RETURNING id`, autopilotID, issueID).Scan(&runID); err != nil {
		t.Fatalf("create autopilot run: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, issue_id, autopilot_run_id, status, priority, context, runtime_id)
		VALUES ($1, $2, $3, 'queued', 0, '{}'::jsonb, $4)`, agentID, issueID, runID, runtimeID); err != nil {
		t.Fatalf("create autopilot task: %v", err)
	}
}

func modeClaimQueuedStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agentID string) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE agent_id = $1`, agentID).Scan(&status); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	return status
}

// TestClaimTask_OperationalMode_ActiveClaims verifies that an active agent
// claims a queued issue-backed task — the regression baseline for the mode
// gate.
func TestClaimTask_OperationalMode_ActiveClaims(t *testing.T) {
	ctx := context.Background()
	pool := newOperationalModePool(t)
	svc := operationalModeClaimService(pool)

	agentID, runtimeID, workspaceID, userID := modeClaimFixture(t, ctx, pool, "active")
	modeClaimEnqueueIssueTask(t, ctx, pool, agentID, runtimeID, workspaceID, userID)

	task, err := svc.ClaimTask(ctx, util.MustParseUUID(agentID))
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if task == nil {
		t.Fatal("expected active agent to claim the task, got nil")
	}
}

// TestClaimTask_OperationalMode_RestingDenies verifies that a resting agent
// never claims, regardless of queued work, and that the denied task stays
// queued (mode denial does not consume work).
func TestClaimTask_OperationalMode_RestingDenies(t *testing.T) {
	ctx := context.Background()
	pool := newOperationalModePool(t)
	svc := operationalModeClaimService(pool)

	agentID, runtimeID, workspaceID, userID := modeClaimFixture(t, ctx, pool, "resting")
	modeClaimEnqueueIssueTask(t, ctx, pool, agentID, runtimeID, workspaceID, userID)

	task, err := svc.ClaimTask(ctx, util.MustParseUUID(agentID))
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if task != nil {
		t.Fatalf("resting agent must not claim, got task %s", util.UUIDToString(task.ID))
	}
	if status := modeClaimQueuedStatus(t, ctx, pool, agentID); status != "queued" {
		t.Fatalf("task status = %q, want 'queued' (mode denial must not consume)", status)
	}
}

// TestClaimTask_OperationalMode_DisabledDenies verifies that a disabled agent
// never claims.
func TestClaimTask_OperationalMode_DisabledDenies(t *testing.T) {
	ctx := context.Background()
	pool := newOperationalModePool(t)
	svc := operationalModeClaimService(pool)

	agentID, runtimeID, workspaceID, userID := modeClaimFixture(t, ctx, pool, "disabled")
	modeClaimEnqueueIssueTask(t, ctx, pool, agentID, runtimeID, workspaceID, userID)

	task, err := svc.ClaimTask(ctx, util.MustParseUUID(agentID))
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if task != nil {
		t.Fatalf("disabled agent must not claim, got task %s", util.UUIDToString(task.ID))
	}
}

// TestClaimTask_OperationalMode_TrainingDeniesIssueTask verifies that a
// training agent does NOT claim an issue-backed task (not a direct-chat or
// quick-create source).
func TestClaimTask_OperationalMode_TrainingDeniesIssueTask(t *testing.T) {
	ctx := context.Background()
	pool := newOperationalModePool(t)
	svc := operationalModeClaimService(pool)

	agentID, runtimeID, workspaceID, userID := modeClaimFixture(t, ctx, pool, "training")
	modeClaimEnqueueIssueTask(t, ctx, pool, agentID, runtimeID, workspaceID, userID)

	task, err := svc.ClaimTask(ctx, util.MustParseUUID(agentID))
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if task != nil {
		t.Fatalf("training agent must not claim issue-backed task, got task %s", util.UUIDToString(task.ID))
	}
	if status := modeClaimQueuedStatus(t, ctx, pool, agentID); status != "queued" {
		t.Fatalf("task status = %q, want 'queued' (mode denial must not consume)", status)
	}
}

// TestClaimTask_OperationalMode_TrainingClaimsQuickCreate verifies that a
// training agent claims a quick-create task (all FK link columns NULL).
func TestClaimTask_OperationalMode_TrainingClaimsQuickCreate(t *testing.T) {
	ctx := context.Background()
	pool := newOperationalModePool(t)
	svc := operationalModeClaimService(pool)

	agentID, runtimeID, _, _ := modeClaimFixture(t, ctx, pool, "training")
	modeClaimEnqueueQuickCreateTask(t, ctx, pool, agentID, runtimeID)

	task, err := svc.ClaimTask(ctx, util.MustParseUUID(agentID))
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if task == nil {
		t.Fatal("expected training agent to claim the quick-create task, got nil")
	}
}

// TestClaimTask_OperationalMode_TrainingClaimsChatTask verifies that a
// training agent claims a direct-chat task (chat_session_id IS NOT NULL).
func TestClaimTask_OperationalMode_TrainingClaimsChatTask(t *testing.T) {
	ctx := context.Background()
	pool := newOperationalModePool(t)
	svc := operationalModeClaimService(pool)

	agentID, runtimeID, workspaceID, userID := modeClaimFixture(t, ctx, pool, "training")
	modeClaimEnqueueChatTask(t, ctx, pool, agentID, runtimeID, workspaceID, userID)

	task, err := svc.ClaimTask(ctx, util.MustParseUUID(agentID))
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if task == nil {
		t.Fatal("expected training agent to claim the chat task, got nil")
	}
}

// TestClaimTask_OperationalMode_TrainingDeniesAutopilotTask verifies that a
// training agent does NOT claim an autopilot-shaped task (has
// autopilot_run_id, so it is neither direct-chat nor quick-create).
func TestClaimTask_OperationalMode_TrainingDeniesAutopilotTask(t *testing.T) {
	ctx := context.Background()
	pool := newOperationalModePool(t)
	svc := operationalModeClaimService(pool)

	agentID, runtimeID, workspaceID, userID := modeClaimFixture(t, ctx, pool, "training")
	modeClaimEnqueueAutopilotTask(t, ctx, pool, agentID, runtimeID, workspaceID, userID)

	task, err := svc.ClaimTask(ctx, util.MustParseUUID(agentID))
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if task != nil {
		t.Fatalf("training agent must not claim autopilot task, got task %s", util.UUIDToString(task.ID))
	}
}

// TestClaimTask_OperationalMode_UnknownFailsClosed verifies that an agent
// whose operational_mode holds a value outside the known set is denied. The
// CHECK constraint normally prevents such a value; this test exercises the
// Go-level fail-closed defence by temporarily relaxing the constraint for
// the fixture. The constraint is restored in cleanup (LIFO: before fixture
// teardown).
func TestClaimTask_OperationalMode_UnknownFailsClosed(t *testing.T) {
	ctx := context.Background()
	pool := newOperationalModePool(t)
	svc := operationalModeClaimService(pool)

	agentID, runtimeID, workspaceID, userID := modeClaimFixture(t, ctx, pool, "resting")
	modeClaimEnqueueIssueTask(t, ctx, pool, agentID, runtimeID, workspaceID, userID)

	if _, err := pool.Exec(ctx, `ALTER TABLE agent DROP CONSTRAINT agent_operational_mode_check`); err != nil {
		t.Fatalf("drop constraint: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `UPDATE agent SET operational_mode = 'active' WHERE operational_mode NOT IN ('active','resting','disabled','training')`)
		pool.Exec(c, `ALTER TABLE agent ADD CONSTRAINT agent_operational_mode_check CHECK (operational_mode IN ('active', 'resting', 'disabled', 'training'))`)
	})
	if _, err := pool.Exec(ctx, `UPDATE agent SET operational_mode = 'hibrid' WHERE id = $1::uuid`, agentID); err != nil {
		t.Fatalf("set unknown mode: %v", err)
	}

	task, err := svc.ClaimTask(ctx, util.MustParseUUID(agentID))
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if task != nil {
		t.Fatalf("unknown operational mode must fail-closed (no claim), got task %s", util.UUIDToString(task.ID))
	}
}

// TestClaimTask_OperationalMode_ActiveRespectsCapacity verifies that the mode
// gate composes with the capacity gate independently: an active agent at
// capacity still does not claim.
func TestClaimTask_OperationalMode_ActiveRespectsCapacity(t *testing.T) {
	ctx := context.Background()
	pool := newOperationalModePool(t)
	svc := operationalModeClaimService(pool)

	agentID, runtimeID, workspaceID, userID := modeClaimFixture(t, ctx, pool, "active")
	modeClaimEnqueueIssueTask(t, ctx, pool, agentID, runtimeID, workspaceID, userID)

	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO agent_task_queue (agent_id, issue_id, status, priority, context, runtime_id, dispatched_at)
			VALUES ($1, $2, 'dispatched', 0, '{}'::jsonb, $3, now())`, agentID, modeClaimNewIssue(ctx, pool, workspaceID, userID), runtimeID); err != nil {
			t.Fatalf("seed dispatched task %d: %v", i+1, err)
		}
	}

	task, err := svc.ClaimTask(ctx, util.MustParseUUID(agentID))
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if task != nil {
		t.Fatalf("active agent at capacity must not claim, got task %s", util.UUIDToString(task.ID))
	}
}

// TestClaimTask_OperationalMode_RestingRetryDenies verifies retry semantics:
// a second claim attempt after a mode denial is still denied and the queued
// task is still not consumed.
func TestClaimTask_OperationalMode_RestingRetryDenies(t *testing.T) {
	ctx := context.Background()
	pool := newOperationalModePool(t)
	svc := operationalModeClaimService(pool)

	agentID, runtimeID, workspaceID, userID := modeClaimFixture(t, ctx, pool, "resting")
	modeClaimEnqueueIssueTask(t, ctx, pool, agentID, runtimeID, workspaceID, userID)

	for attempt := 1; attempt <= 2; attempt++ {
		task, err := svc.ClaimTask(ctx, util.MustParseUUID(agentID))
		if err != nil {
			t.Fatalf("ClaimTask attempt %d: %v", attempt, err)
		}
		if task != nil {
			t.Fatalf("resting agent must not claim on attempt %d, got task %s", attempt, util.UUIDToString(task.ID))
		}
	}
	if status := modeClaimQueuedStatus(t, ctx, pool, agentID); status != "queued" {
		t.Fatalf("task status = %q, want 'queued' after repeated mode denials", status)
	}
}

// TestClaimTask_OperationalMode_ConcurrentRestingDenies verifies concurrency:
// with a resting agent and queued work, concurrent claim attempts all come
// back empty and no task is consumed.
func TestClaimTask_OperationalMode_ConcurrentRestingDenies(t *testing.T) {
	ctx := context.Background()
	pool := newOperationalModePool(t)
	svc := operationalModeClaimService(pool)

	agentID, runtimeID, workspaceID, userID := modeClaimFixture(t, ctx, pool, "resting")
	modeClaimEnqueueIssueTask(t, ctx, pool, agentID, runtimeID, workspaceID, userID)

	const workers = 4
	start := make(chan struct{})
	claimed := make(chan string, workers)
	errs := make(chan error, workers)

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			task, err := svc.ClaimTask(ctx, util.MustParseUUID(agentID))
			if err != nil {
				errs <- err
				return
			}
			if task != nil {
				claimed <- util.UUIDToString(task.ID)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(claimed)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("claim task: %v", err)
		}
	}
	if n := len(claimed); n != 0 {
		t.Fatalf("resting agent produced %d claims under concurrency, want 0", n)
	}
	if status := modeClaimQueuedStatus(t, ctx, pool, agentID); status != "queued" {
		t.Fatalf("task status = %q, want 'queued' after concurrent mode denials", status)
	}
}

func modeClaimNewIssue(ctx context.Context, pool *pgxpool.Pool, workspaceID, userID string) string {
	var issueID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
		VALUES ($1, $2, 'in_progress', 'none', $3, 'member', $4, 0)
		RETURNING id`, workspaceID, "mode capacity seed issue", userID, modeClaimIssueNumber()).Scan(&issueID); err != nil {
		panic(fmt.Sprintf("create seed issue: %v", err))
	}
	return issueID
}
