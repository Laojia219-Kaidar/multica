package scheduler

import (
	"context"
	"net/url"
	"os"
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
//   - cadence <=30s: with a healthy, unblocked scheduler the cadence plus
//     the manager's default 30s TickInterval keeps the NOMINAL drift
//     window near 60s. It is not a hard bound — the manager runs the
//     registered jobs serially within each tick, so a long-running
//     handler ahead of this job can delay a reconcile past that window
//     until the scheduler is healthy again;
//   - latest_only catch-up: convergence is a fixed point, not a per-bucket
//     replay, so only the most recent due plan is ever claimed;
//   - handler fails closed without queries instead of reporting a no-op.
func TestAgentStatusReconcileJobSpec(t *testing.T) {
	job := AgentStatusReconcileJob(nil)

	if job.Name != JobNameAgentStatusReconcile {
		t.Fatalf("job name = %q, want %q", job.Name, JobNameAgentStatusReconcile)
	}
	if job.Cadence > 30*time.Second {
		t.Fatalf("cadence = %s; must be <=30s so a healthy, unblocked scheduler keeps the nominal convergence window near cadence + one 30s tick", job.Cadence)
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

// dedicatedIsolatedPool connects ONLY to TEST_DATABASE_URL, and only when
// that URL names an explicit loopback host and an explicit non-5432 port —
// the dedicated ephemeral test-instance convention. The scheduler
// package's fallback DSN is the SHARED live dev database on
// localhost:5432; a test whose handler issues a global UPDATE and whose
// cleanup deletes sys_cron_executions rows must never run there, so
// without a qualifying TEST_DATABASE_URL the test skips instead of
// guessing.
func dedicatedIsolatedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("requires TEST_DATABASE_URL pointing at a dedicated ephemeral Postgres (loopback host, explicit non-5432 port); refusing to fall back to the shared dev DB")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Skipf("TEST_DATABASE_URL is not a valid URL: %v", err)
	}
	switch host := u.Hostname(); host {
	case "localhost", "127.0.0.1", "::1":
	default:
		t.Skipf("TEST_DATABASE_URL host %q is not loopback; refusing to run a global-statement test against it", host)
	}
	if port := u.Port(); port == "" || port == "5432" {
		t.Skipf("TEST_DATABASE_URL port %q must be an explicit non-5432 port (dedicated ephemeral instance), not the shared dev DB", port)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Skipf("dedicated pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("dedicated pool ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
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

	// Register cleanup IMMEDIATELY after the first durable row, so a
	// failure in any later seed step cannot leak this user or the
	// workspaces created so far. The closure reads the up-to-date fix
	// fields at cleanup time; workspace deletes cascade to the members,
	// issues, runtimes, agents and queued tasks seeded below.
	t.Cleanup(func() {
		ctx := context.Background()
		for _, wsID := range fix.workspaceIDs {
			pool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, wsID)
		}
		pool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, fix.userID)
	})

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
// Manager tick path (lease claim in sys_cron_executions -> handler)
// against seeded dirty rows and asserts:
//
//  1. ONE tick corrects BOTH directions — stale-working->idle (B1 crash
//     window) and stale-idle->working (B5 missed recompute);
//  2. archived agents are out of scope;
//  3. already-correct rows are not rewritten (no updated_at churn) — the
//     steady-state tick is a zero-row no-op;
//  4. a repeated tick is idempotent (statuses stable, no further rows).
//
// Isolation contract (HIV-982 review repair): the test runs ONLY against
// a dedicated ephemeral database selected by TEST_DATABASE_URL with an
// explicit loopback host and an explicit non-5432 port; the job spec is
// cloned under a uniqueJobName so every sys_cron_executions query and
// cleanup is partitioned to that name and can never delete a formal
// agent_status_reconcile receipt; no global pre-converge UPDATE is issued
// against data this test does not own, and the global rows_affected
// assertions run only when a read-only population check establishes that
// the agent table holds exactly this test's fixtures.
func TestAgentStatusReconcileJobTickConvergesBothDirections(t *testing.T) {
	pool := dedicatedIsolatedPool(t)
	ctx := context.Background()

	queries := db.New(pool)

	// Clone the production spec under a unique job name: this run's
	// audit rows live in their own (job_name, ...) partition, and every
	// sys_cron_executions query and cleanup below uses exactly this
	// name — a formal agent_status_reconcile receipt can never be
	// selected or deleted by this test.
	spec := AgentStatusReconcileJob(queries)
	spec.Name = uniqueJobName(t, JobNameAgentStatusReconcile)
	t.Cleanup(func() { cleanupExecutions(t, pool, spec.Name) })

	fix := seedAgentStatusReconcileFixture(t, pool)

	// Read-only hermeticity check: the dedicated-instance guard plus a
	// fixture-only agent population is what licenses the exact global
	// rows_affected assertions below. Nothing outside the fixture is
	// mutated to force this — if the dedicated DB already holds other
	// agents, the global-count assertions are simply relaxed while the
	// per-fixture status/no-churn/idempotency checks stay exact.
	var liveAgents, archivedAgents int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE archived_at IS NULL),
		       count(*) FILTER (WHERE archived_at IS NOT NULL)
		  FROM agent
	`).Scan(&liveAgents, &archivedAgents); err != nil {
		t.Fatalf("hermeticity check: %v", err)
	}
	fixtureOnly := liveAgents == 3 && archivedAgents == 1
	if !fixtureOnly {
		t.Logf("agent population = %d live / %d archived (fixtures are 3/1); relaxing global rows_affected assertions to fixture-scoped checks",
			liveAgents, archivedAgents)
	}

	mgr := NewManager(pool, Options{RunnerID: "agent-status-reconcile-test"})
	if err := mgr.Register(spec); err != nil {
		t.Fatalf("register: %v", err)
	}

	var cleanUpdatedAtBefore time.Time
	if err := pool.QueryRow(ctx,
		`SELECT updated_at FROM agent WHERE id = $1`, fix.cleanIdle).Scan(&cleanUpdatedAtBefore); err != nil {
		t.Fatalf("read clean agent updated_at: %v", err)
	}

	// Tick 1: one claim of the current plan bucket must fix both dirty
	// fixture rows.
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
	`, spec.Name).Scan(&rows1); err != nil {
		t.Fatalf("read audit row: %v", err)
	}
	if fixtureOnly && rows1 != 2 {
		t.Fatalf("tick 1 rows_affected = %d, want exactly 2 (both dirty fixtures, nothing else in scope)", rows1)
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

	// Tick 2 on the same bucket (drop this test's own audit rows so the
	// lease is claimable again): idempotent — statuses stable, and zero
	// further rows once the population has converged.
	cleanupExecutions(t, pool, spec.Name)
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
	`, spec.Name).Scan(&rows2); err != nil {
		t.Fatalf("read audit row 2: %v", err)
	}
	if fixtureOnly && rows2 != 0 {
		t.Fatalf("tick 2 rows_affected = %d, want 0 (converged state is a no-op)", rows2)
	}
}
