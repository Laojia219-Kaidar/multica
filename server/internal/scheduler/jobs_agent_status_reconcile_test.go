package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestAgentStatusReconcileJobSpec pins the contract of the R4 convergence
// job without a database:
//
//   - a single global scope (one lease per tick, whole deployment);
//   - cadence 30s, so cadence + the manager's default 30s TickInterval
//     bounds worst-case status drift to <=60s (audit R4 contract);
//   - latest_only catch-up: convergence is a fixed point, not a per-bucket
//     replay, so only the most recent due plan is ever claimed;
//   - handler fails closed without queries instead of reporting a no-op.
func TestAgentStatusReconcileJobSpec(t *testing.T) {
	job := AgentStatusReconcileJob(nil)

	if job.Name != JobNameAgentStatusReconcile {
		t.Fatalf("job name = %q, want %q", job.Name, JobNameAgentStatusReconcile)
	}
	if job.Cadence > 30*time.Second {
		t.Fatalf("cadence = %s; must be <=30s so cadence + default 30s tick keeps convergence <=60s", job.Cadence)
	}
	if job.CatchUpMode != CatchUpLatestOnly {
		t.Fatalf("catch-up mode = %s, want latest_only (convergence is a fixed point)", job.CatchUpMode)
	}
	if job.Handler == nil {
		t.Fatal("handler must be set")
	}

	scopes, err := job.Scopes(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	if len(scopes) != 1 || scopes[0] != ScopeGlobal {
		t.Fatalf("scopes = %v, want exactly [global]", scopes)
	}

	if _, err := job.Handler(context.Background(), HandlerInput{}); err == nil {
		t.Fatal("handler must fail closed when queries is nil")
	}
}

type agentStatusReconcileFixture struct {
	workspaceIDs []string
	userID       string

	// staleWorkingNoActive: status='working' but only terminal tasks —
	// the B1 crash window (task committed terminal, refresh never ran).
	staleWorkingNoActive string
	// staleIdleWithRunning: status='idle' but a running task exists —
	// the B5 missed-recompute window (bulk cancel/archive path skipped
	// the agent).
	staleIdleWithRunning string
	// archivedWorking: archived agent with drifted status — must stay
	// untouched; it is invisible to every list/presence surface.
	archivedWorking string
	// cleanIdle: status already matches the predicate — the steady-state
	// majority; must not be rewritten (no updated_at churn).
	cleanIdle string
}

func seedAgentStatusReconcileFixture(t *testing.T, pool *pgxpool.Pool) agentStatusReconcileFixture {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	fix := agentStatusReconcileFixture{}

	// A user owning two workspaces: the stale rows live in ws1, the
	// archived row in ws2, proving the set-based sweep spans workspaces
	// while each agent is still evaluated only against its own tasks.
	var userID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ('reconcile-test', $1)
		RETURNING id
	`, "reconcile-test-"+suffix+"@example.invalid").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	fix.userID = userID

	newWorkspace := func(slug string) string {
		var wsID string
		if err := pool.QueryRow(ctx, `
			INSERT INTO workspace (name, slug)
			VALUES ($1, $2)
			RETURNING id
		`, "Reconcile test "+slug, "reconcile-test-"+slug+"-"+suffix).Scan(&wsID); err != nil {
			t.Fatalf("seed workspace: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')
		`, wsID, userID); err != nil {
			t.Fatalf("seed member: %v", err)
		}
		return wsID
	}

	// seedAgent inserts one agent bound to a fresh runtime and returns its id.
	seedAgent := func(wsID, name, status string, archived bool) string {
		var runtimeID, agentID string
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status)
			VALUES ($1, $2, 'local', 'reconcile_test', 'offline')
			RETURNING id
		`, wsID, name+" Runtime").Scan(&runtimeID); err != nil {
			t.Fatalf("seed runtime: %v", err)
		}
		archivedAt := any(nil)
		if archived {
			archivedAt = time.Now()
		}
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent (workspace_id, name, runtime_mode, status, runtime_id, archived_at)
			VALUES ($1, $2, 'local', $3, $4, $5)
			RETURNING id
		`, wsID, name, status, runtimeID, archivedAt).Scan(&agentID); err != nil {
			t.Fatalf("seed agent: %v", err)
		}
		return agentID
	}

	// seedTask inserts one task for the agent in the given status.
	seedTask := func(wsID, agentID, status string) string {
		var issueID, taskID, runtimeID string
		if err := pool.QueryRow(ctx, `
			INSERT INTO issue (workspace_id, number, title, status, priority, creator_type, creator_id, assignee_type, assignee_id)
			VALUES ($1, (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1),
				'Reconcile test issue', 'todo', 'none', 'member',
				(SELECT user_id FROM member WHERE workspace_id = $1 LIMIT 1), 'agent', $2)
			RETURNING id
		`, wsID, agentID).Scan(&issueID); err != nil {
			t.Fatalf("seed issue: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
			t.Fatalf("lookup runtime: %v", err)
		}
		var completedAt any
		if status == "completed" {
			completedAt = time.Now()
		}
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, completed_at)
			VALUES ($1, $2, $3, $4, 0, $5)
			RETURNING id
		`, agentID, runtimeID, issueID, status, completedAt).Scan(&taskID); err != nil {
			t.Fatalf("seed task: %v", err)
		}
		return taskID
	}

	ws1 := newWorkspace("ws1")
	ws2 := newWorkspace("ws2")
	fix.workspaceIDs = []string{ws1, ws2}

	fix.staleWorkingNoActive = seedAgent(ws1, "Stale Working", "working", false)
	seedTask(ws1, fix.staleWorkingNoActive, "completed") // terminal only -> must fall to idle

	fix.staleIdleWithRunning = seedAgent(ws1, "Stale Idle", "idle", false)
	seedTask(ws1, fix.staleIdleWithRunning, "running") // active -> must rise to working

	fix.archivedWorking = seedAgent(ws2, "Archived Working", "working", true)
	seedTask(ws2, fix.archivedWorking, "completed")

	fix.cleanIdle = seedAgent(ws1, "Clean Idle", "idle", false)
	seedTask(ws1, fix.cleanIdle, "completed")

	t.Cleanup(func() {
		ctx := context.Background()
		for _, wsID := range fix.workspaceIDs {
			pool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, wsID)
		}
		pool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, fix.userID)
	})
	return fix
}

func agentStatusByID(t *testing.T, pool *pgxpool.Pool, agentID, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM agent WHERE id = $1`, agentID).Scan(&got); err != nil {
		t.Fatalf("read agent status: %v", err)
	}
	if got != want {
		t.Fatalf("agent %s status = %q, want %q", agentID, got, want)
	}
}

// TestAgentStatusReconcileJobTickConvergesBothDirections runs the real
// Manager tick path (lease claim in sys_cron_executions -> handler) against
// seeded dirty rows and asserts the audit R4 contract:
//
//  1. ONE tick corrects BOTH directions — stale-working->idle (B1 crash
//     window) and stale-idle->working (B5 missed recompute);
//  2. archived agents are out of scope;
//  3. already-correct rows are not rewritten (no updated_at churn) — the
//     steady-state tick is a zero-row no-op;
//  4. a repeated tick is idempotent (rows_affected=0, statuses stable).
func TestAgentStatusReconcileJobTickConvergesBothDirections(t *testing.T) {
	pool := integrationPool(t)
	t.Cleanup(func() { cleanupExecutions(t, pool, JobNameAgentStatusReconcile) })
	ctx := context.Background()

	queries := db.New(pool)
	// Pre-converge the whole population so the only dirty rows in the
	// database are the ones this test seeds. Without this, a leftover
	// dirty agent from an unrelated run (or concurrent test) would make
	// the exact rows_affected assertions below non-hermetic.
	if _, err := queries.ReconcileAllAgentStatuses(ctx); err != nil {
		t.Fatalf("pre-converge: %v", err)
	}

	fix := seedAgentStatusReconcileFixture(t, pool)

	mgr := NewManager(pool, Options{RunnerID: "agent-status-reconcile-test"})
	if err := mgr.Register(AgentStatusReconcileJob(queries)); err != nil {
		t.Fatalf("register: %v", err)
	}

	var cleanUpdatedAtBefore time.Time
	if err := pool.QueryRow(ctx,
		`SELECT updated_at FROM agent WHERE id = $1`, fix.cleanIdle).Scan(&cleanUpdatedAtBefore); err != nil {
		t.Fatalf("read clean agent updated_at: %v", err)
	}

	// Tick 1: one claim of the current plan bucket must fix both rows.
	if err := mgr.RunOnce(ctx); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	agentStatusByID(t, pool, fix.staleWorkingNoActive, "idle")
	agentStatusByID(t, pool, fix.staleIdleWithRunning, "working")
	agentStatusByID(t, pool, fix.archivedWorking, "working") // untouched
	agentStatusByID(t, pool, fix.cleanIdle, "idle")

	var rows1 int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(rows_affected), -1)
		  FROM sys_cron_executions
		 WHERE job_name = $1 AND scope_kind = 'global' AND scope_id = 'global'
		   AND status = 'SUCCESS'
	`, JobNameAgentStatusReconcile).Scan(&rows1); err != nil {
		t.Fatalf("read audit row: %v", err)
	}
	if rows1 != 2 {
		t.Fatalf("tick 1 rows_affected = %d, want exactly 2 (both dirty rows, nothing else in scope)", rows1)
	}

	var cleanUpdatedAtAfter time.Time
	if err := pool.QueryRow(ctx,
		`SELECT updated_at FROM agent WHERE id = $1`, fix.cleanIdle).Scan(&cleanUpdatedAtAfter); err != nil {
		t.Fatalf("read clean agent updated_at after tick: %v", err)
	}
	if !cleanUpdatedAtAfter.Equal(cleanUpdatedAtBefore) {
		t.Fatalf("clean agent updated_at churned: %s -> %s; the WHERE guard must skip already-correct rows",
			cleanUpdatedAtBefore, cleanUpdatedAtAfter)
	}

	// Tick 2 on the same bucket (drop the audit row so the lease is
	// claimable again): idempotent — zero rows across the ENTIRE
	// population, statuses stable.
	cleanupExecutions(t, pool, JobNameAgentStatusReconcile)
	if err := mgr.RunOnce(ctx); err != nil {
		t.Fatalf("runOnce 2: %v", err)
	}
	agentStatusByID(t, pool, fix.staleWorkingNoActive, "idle")
	agentStatusByID(t, pool, fix.staleIdleWithRunning, "working")

	var rows2 int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(rows_affected), -1)
		  FROM sys_cron_executions
		 WHERE job_name = $1 AND scope_kind = 'global' AND scope_id = 'global'
		   AND status = 'SUCCESS'
	`, JobNameAgentStatusReconcile).Scan(&rows2); err != nil {
		t.Fatalf("read audit row 2: %v", err)
	}
	if rows2 != 0 {
		t.Fatalf("tick 2 rows_affected = %d, want 0 (converged state is a no-op)", rows2)
	}
}
