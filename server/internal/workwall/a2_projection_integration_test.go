//go:build integration

package workwall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// a2SeedWorkspace inserts one isolated workspace + runtime + agent + open
// issue + running task and returns their ids. Every A2 integration test uses
// its own workspace so tests stay hermetic against a shared database.
func a2SeedWorkspace(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (wsID, agentID, issueID, taskID string) {
	t.Helper()
	slug := fmt.Sprintf("a2-wall-%d", time.Now().UnixNano())
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ($1, $1) RETURNING id::text`, slug).Scan(&wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	var rtID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider) VALUES ($1, 'a2-rt', 'local', 'prime') RETURNING id::text`, wsID).Scan(&rtID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent (workspace_id, name, runtime_mode, kind, runtime_id) VALUES ($1, 'Shard', 'local', 'user', $2) RETURNING id::text`, wsID, rtID).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO issue (workspace_id, title, status, creator_type, creator_id) VALUES ($1, 'A2 slice', 'in_progress', 'agent', $2) RETURNING id::text`, wsID, wsID).Scan(&issueID); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent_task_queue (agent_id, issue_id, runtime_id, status) VALUES ($1, $2, $3, 'running') RETURNING id::text`, agentID, issueID, rtID).Scan(&taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return wsID, agentID, issueID, taskID
}

func a2InsertEvent(ctx context.Context, t *testing.T, pool *pgxpool.Pool, wsID, workRef, session, eventType string, payload string, occurred, observed time.Time) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO work_event (workspace_id, work_ref, session_id, event_type, event_payload, idempotency_key, occurred_at, observed_at)
		 VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8) RETURNING id::text`,
		wsID, workRef, session, eventType, payload,
		fmt.Sprintf("itest-%d", time.Now().UnixNano()), occurred, observed).Scan(&id); err != nil {
		t.Fatalf("seed work_event(%s): %v", eventType, err)
	}
	return id
}

func a2WsUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return u
}

// TestA2SnapshotActiveFailClosedOnMissingHeartbeat seeds a live progress
// event with no terminal_presence heartbeat and asserts the pane exists,
// stays active, surfaces as event_console, and never counts as working.
func TestA2SnapshotActiveFailClosedOnMissingHeartbeat(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	wsID, agentID, issueID, taskID := a2SeedWorkspace(ctx, t, pool)
	ref := fmt.Sprintf("hivecrew://%s/work/prj/%s/%s", wsID, issueID, taskID)
	now := time.Now().UTC()
	eventID := a2InsertEvent(ctx, t, pool, wsID, ref, "sess-a2", "progress",
		`{"stage":"build","status":"completed","note":"task completed"}`, now.Add(-time.Minute), now.Add(-time.Minute))
	// Full evidence chain so the ONLY missing piece is the heartbeat.
	a2SeedEvidence(ctx, t, pool, wsID, issueID, taskID, agentID)

	svc := NewService(db.New(pool))
	// Pin the clock: a snapshot of unchanged input must be byte-identical.
	fixedNow := now.Add(10 * time.Second)
	svc.Now = func() time.Time { return fixedNow }
	panes, err := svc.A2Snapshot(ctx, a2WsUUID(t, wsID), 0)
	if err != nil {
		t.Fatalf("A2Snapshot: %v", err)
	}
	if len(panes) != 1 {
		t.Fatalf("expected 1 pane, got %d", len(panes))
	}
	p := panes[0]
	if p.SourceEventID != eventID {
		t.Fatalf("source_event_id = %q, want %q", p.SourceEventID, eventID)
	}
	if p.ExecutionState != A2ExecutionActive {
		t.Fatalf("execution_state = %q, want active", p.ExecutionState)
	}
	if p.Working {
		t.Fatalf("missing heartbeat must fail closed: working must be false")
	}
	if p.SurfaceKind != A2SurfaceEventConsole {
		t.Fatalf("surface = %q, want event_console", p.SurfaceKind)
	}
	// Terminal text in the payload must not establish completion.
	if p.CompletedAt != nil {
		t.Fatalf("terminal text must never set completed_at")
	}
	// Dispatch-to-employee ownership falls back to the task assignee when no
	// assignment receipt exists.
	if p.EmployeeID != agentID {
		t.Fatalf("employee_id = %q, want task assignee %q", p.EmployeeID, agentID)
	}
	if p.EmployeeName != "Shard" {
		t.Fatalf("employee_name = %q, want Shard", p.EmployeeName)
	}
	if p.IssueID != issueID || p.TaskID != taskID {
		t.Fatalf("issue/task ids not joined: %q/%q", p.IssueID, p.TaskID)
	}

	// Idempotent replay: an identical second snapshot must be identical.
	again, err := svc.A2Snapshot(ctx, a2WsUUID(t, wsID), 0)
	if err != nil {
		t.Fatalf("A2Snapshot second call: %v", err)
	}
	if !reflect.DeepEqual(panes, again) {
		t.Fatalf("A2Snapshot must be idempotent on unchanged input")
	}
}

// TestA2SnapshotMismatchReplayAndCancellation covers the remaining required
// states end-to-end against the real schema: a finished claim on an open
// issue projects as issue_state_mismatch, a late pre-terminal delivery after
// it projects as replay, and an issue cancellation projects as cancelled.
func TestA2SnapshotMismatchReplayAndCancellation(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	wsID, _, issueID, taskID := a2SeedWorkspace(ctx, t, pool)
	ref := fmt.Sprintf("hivecrew://%s/work/prj/%s/%s", wsID, issueID, taskID)
	now := time.Now().UTC()

	// Phase 1: finished claim, issue still open, no receipt -> mismatch.
	a2InsertEvent(ctx, t, pool, wsID, ref, "sess-a2", "finished", `{}`,
		now.Add(-3*time.Minute), now.Add(-3*time.Minute))
	panes, err := NewService(db.New(pool)).A2Snapshot(ctx, a2WsUUID(t, wsID), 0)
	if err != nil {
		t.Fatalf("A2Snapshot: %v", err)
	}
	if len(panes) != 1 || panes[0].ExecutionState != A2ExecutionIssueMismatch {
		t.Fatalf("expected issue_state_mismatch, got %+v", panes)
	}
	if panes[0].Working {
		t.Fatalf("mismatch must never count as working")
	}

	// Phase 2: late pre-terminal delivery observed after the finish -> replay.
	a2InsertEvent(ctx, t, pool, wsID, ref, "sess-a2", "progress", `{}`,
		now.Add(-5*time.Minute), now.Add(-1*time.Minute))
	panes, err = NewService(db.New(pool)).A2Snapshot(ctx, a2WsUUID(t, wsID), 0)
	if err != nil {
		t.Fatalf("A2Snapshot: %v", err)
	}
	if len(panes) != 1 || panes[0].ExecutionState != A2ExecutionReplay {
		t.Fatalf("expected replay, got %+v", panes)
	}
	if panes[0].Working {
		t.Fatalf("replay must never count as working")
	}

	// Phase 3: cancel the issue (canonical authority) -> cancelled, never
	// working, and the late delivery still cannot resurrect the work.
	if _, err := pool.Exec(ctx, `UPDATE issue SET status = 'cancelled' WHERE id = $1`, issueID); err != nil {
		t.Fatalf("cancel issue: %v", err)
	}
	panes, err = NewService(db.New(pool)).A2Snapshot(ctx, a2WsUUID(t, wsID), 0)
	if err != nil {
		t.Fatalf("A2Snapshot: %v", err)
	}
	if len(panes) != 1 || panes[0].ExecutionState != A2ExecutionCancelled {
		t.Fatalf("expected cancelled, got %+v", panes)
	}
	if panes[0].Working {
		t.Fatalf("cancelled must never count as working")
	}
}

// TestA2SnapshotTerminalHeartbeatSurfacesTerminal seeds a fresh
// terminal_presence heartbeat for the event session and asserts the pane
// upgrades to a real terminal surface and counts as working.
func TestA2SnapshotTerminalHeartbeatSurfacesTerminal(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	wsID, _, issueID, taskID := a2SeedWorkspace(ctx, t, pool)
	ref := fmt.Sprintf("hivecrew://%s/work/prj/%s/%s", wsID, issueID, taskID)
	now := time.Now().UTC()
	// Unique session per run: terminal_presence has a unique key on
	// (host, session_name, window_index, pane_index) that survives re-runs.
	session := fmt.Sprintf("sess-a2-live-%d", time.Now().UnixNano())
	a2InsertEvent(ctx, t, pool, wsID, ref, session, "progress", `{"stage":"test"}`,
		now.Add(-time.Minute), now.Add(-time.Minute))
	if _, err := pool.Exec(ctx,
		`INSERT INTO terminal_presence (workspace_id, host, session_name, current_command, agent_hint)
		 VALUES ($1, 'mac-a2', $2, 'go test', 'shard')`, wsID, session); err != nil {
		t.Fatalf("seed terminal_presence: %v", err)
	}
	// Full evidence chain for this task: execution-receipt claim + dispatch
	// receipt precisely bound to it (working requires both task and evidence).
	a2SeedEvidence(ctx, t, pool, wsID, issueID, taskID, "")

	panes, err := NewService(db.New(pool)).A2Snapshot(ctx, a2WsUUID(t, wsID), 0)
	if err != nil {
		t.Fatalf("A2Snapshot: %v", err)
	}
	if len(panes) != 1 {
		t.Fatalf("expected 1 pane, got %d", len(panes))
	}
	p := panes[0]
	if p.SurfaceKind != A2SurfaceTerminal {
		t.Fatalf("surface = %q, want terminal", p.SurfaceKind)
	}
	if !p.Working {
		t.Fatalf("task + exact dispatch/receipt evidence + fresh session-matched heartbeat should count as working")
	}
	if p.DispatchCommandID == "" {
		t.Fatalf("task-bound dispatch should be exposed as dispatch_command_id")
	}
	// The sanitized summary carries the structured stage only.
	blob, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(blob) == "" {
		t.Fatal("empty pane json")
	}
}

// --- B2 rework integration: tenant/project isolation + precise ownership ----

const a2Digest = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// a2SeedSecondWorkspace seeds a fully independent second tenant with its own
// runtime/agent/issue/task and returns its ids. Used for cross-tenant checks.
func a2SeedSecondWorkspace(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (wsID, agentID, issueID, taskID string) {
	t.Helper()
	slug := fmt.Sprintf("a2-wall-foreign-%d", time.Now().UnixNano())
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ($1, $1) RETURNING id::text`, slug).Scan(&wsID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	var rtID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider) VALUES ($1, 'a2-rt2', 'local', 'prime') RETURNING id::text`, wsID).Scan(&rtID); err != nil {
		t.Fatalf("seed foreign runtime: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent (workspace_id, name, runtime_mode, kind, runtime_id) VALUES ($1, 'Foreign', 'local', 'user', $2) RETURNING id::text`, wsID, rtID).Scan(&agentID); err != nil {
		t.Fatalf("seed foreign agent: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO issue (workspace_id, title, status, creator_type, creator_id) VALUES ($1, 'A2 foreign', 'in_progress', 'agent', $2) RETURNING id::text`, wsID, agentID).Scan(&issueID); err != nil {
		t.Fatalf("seed foreign issue: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent_task_queue (agent_id, issue_id, runtime_id, status) VALUES ($1, $2, $3, 'running') RETURNING id::text`, agentID, issueID, rtID).Scan(&taskID); err != nil {
		t.Fatalf("seed foreign task: %v", err)
	}
	return wsID, agentID, issueID, taskID
}

func a2InsertTask(ctx context.Context, t *testing.T, pool *pgxpool.Pool, agentID, issueID, rtID, status string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent_task_queue (agent_id, issue_id, runtime_id, status) VALUES ($1, $2, $3, $4) RETURNING id::text`,
		agentID, issueID, rtID, status).Scan(&id); err != nil {
		t.Fatalf("seed task(%s): %v", status, err)
	}
	return id
}

func a2RuntimeID(ctx context.Context, t *testing.T, pool *pgxpool.Pool, wsID string) string {
	t.Helper()
	var rtID string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM agent_runtime WHERE workspace_id = $1 ORDER BY created_at DESC LIMIT 1`, wsID).Scan(&rtID); err != nil {
		t.Fatalf("read runtime: %v", err)
	}
	return rtID
}

// TestA2SnapshotCrossWorkspaceNeverLeaks proves two isolation layers: the
// ledger query never returns another tenant's rows, and even a row stored in
// THIS tenant whose work_ref embeds a foreign (or missing) workspace is
// skipped by the projection (fail-closed), never rendered.
func TestA2SnapshotCrossWorkspaceNeverLeaks(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	wsA, _, issueA, taskA := a2SeedWorkspace(ctx, t, pool)
	wsB, _, _, taskB := a2SeedSecondWorkspace(ctx, t, pool)
	now := time.Now().UTC()

	// Foreign tenant's ledger row: invisible to a ws-A snapshot via SQL scope.
	refB := fmt.Sprintf("hivecrew://%s/work/prj/%s/%s", wsB, "", taskB)
	a2InsertEvent(ctx, t, pool, wsB, refB, "sess-b", "progress", `{}`,
		now.Add(-time.Minute), now.Add(-time.Minute))

	// Drift row: stored under ws-A but the work_ref embeds ws-B's uuid.
	driftRef := fmt.Sprintf("hivecrew://%s/work/prj/%s/%s", wsB, issueA, taskA)
	a2InsertEvent(ctx, t, pool, wsA, driftRef, "sess-a", "progress", `{}`,
		now.Add(-time.Minute), now.Add(-time.Minute))

	// Honest row for the same tenant: the only pane that may render.
	goodRef := fmt.Sprintf("hivecrew://%s/work/prj/%s/%s", wsA, issueA, taskA)
	a2InsertEvent(ctx, t, pool, wsA, goodRef, "sess-a", "progress", `{}`,
		now.Add(-2*time.Minute), now.Add(-2*time.Minute))

	panes, err := NewService(db.New(pool)).A2Snapshot(ctx, a2WsUUID(t, wsA), 0)
	if err != nil {
		t.Fatalf("A2Snapshot: %v", err)
	}
	if len(panes) != 1 {
		t.Fatalf("expected exactly 1 pane (foreign + drifted rows must not render), got %d: %+v", len(panes), panes)
	}
	if panes[0].WorkRef != goodRef {
		t.Fatalf("rendered work_ref = %q, want the tenant-honest row", panes[0].WorkRef)
	}
	if panes[0].WorkspaceID != wsA {
		t.Fatalf("pane workspace = %q, want %q", panes[0].WorkspaceID, wsA)
	}
}

// TestA2SnapshotProjectDriftIsMismatch proves a work_ref whose embedded
// project disagrees with the Issue authority renders only as
// issue_state_mismatch (visible drift, never working, never completion).
func TestA2SnapshotProjectDriftIsMismatch(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	wsID, agentID, issueID, taskID := a2SeedWorkspace(ctx, t, pool)
	rtID := a2RuntimeID(ctx, t, pool, wsID)
	now := time.Now().UTC()

	// Two real projects in the same workspace; the issue belongs to P2.
	p1, p2 := "", ""
	if err := pool.QueryRow(ctx,
		`INSERT INTO project (workspace_id, title) VALUES ($1, 'P1') RETURNING id::text`, wsID).Scan(&p1); err != nil {
		t.Fatalf("seed project P1: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO project (workspace_id, title) VALUES ($1, 'P2') RETURNING id::text`, wsID).Scan(&p2); err != nil {
		t.Fatalf("seed project P2: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET project_id = $2 WHERE id = $1`, issueID, p2); err != nil {
		t.Fatalf("set issue project: %v", err)
	}
	_ = agentID

	// work_ref claims P1; Issue authority says P2. Presence session is
	// per-run unique (terminal_presence has a unique key on host+session).
	driftSession := fmt.Sprintf("sess-a2-drift-%d", time.Now().UnixNano())
	driftRef := fmt.Sprintf("hivecrew://%s/work/%s/%s/%s", wsID, p1, issueID, taskID)
	a2InsertEvent(ctx, t, pool, wsID, driftRef, driftSession, "progress", `{}`,
		now.Add(-time.Minute), now.Add(-time.Minute))
	if _, err := pool.Exec(ctx,
		`INSERT INTO terminal_presence (workspace_id, host, session_name, current_command) VALUES ($1, 'mac-a2p', $2, 'go test')`, wsID, driftSession); err != nil {
		t.Fatalf("seed presence: %v", err)
	}

	panes, err := NewService(db.New(pool)).A2Snapshot(ctx, a2WsUUID(t, wsID), 0)
	if err != nil {
		t.Fatalf("A2Snapshot: %v", err)
	}
	if len(panes) != 1 {
		t.Fatalf("expected 1 pane, got %d", len(panes))
	}
	p := panes[0]
	if p.ExecutionState != A2ExecutionIssueMismatch {
		t.Fatalf("execution_state = %q, want issue_state_mismatch (project drift)", p.ExecutionState)
	}
	if p.Working {
		t.Fatalf("project drift must never count as working, even with a fresh heartbeat")
	}

	// Same shape but with a completed canonical receipt: drift still wins.
	task2 := a2InsertTask(ctx, t, pool, agentID, issueID, rtID, "completed")
	ref2 := fmt.Sprintf("hivecrew://%s/work/%s/%s/%s", wsID, p1, issueID, task2)
	a2InsertEvent(ctx, t, pool, wsID, ref2, "sess-b", "progress", `{}`,
		now.Add(-time.Minute), now.Add(-time.Minute))
	if _, err := pool.Exec(ctx,
		`INSERT INTO execution_receipt (task_id, workspace_id, issue_id, assignment_command_id,
		   work_order_ref, work_order_revision, work_order_digest, input_digest,
		   employee_ref, employee_revision, employee_digest,
		   binding_ref, binding_revision, binding_digest,
		   agent_ref, agent_revision, agent_digest,
		   runtime_snapshot, runtime_digest, claimed_at, terminal_status, completed_at, finalized_at)
		 VALUES ($1,$2,$3,'00000000-0000-0000-0000-000000000000',
		   'wo://x','r1',$4,$4,'emp://x','r1',$4,'bind://x','r1',$4,'agent://x','r1',$4,
		   '{}'::json,$4,$5,'completed',$5,$5)`,
		task2, wsID, issueID, a2Digest, now.Add(-time.Minute)); err != nil {
		t.Fatalf("seed execution receipt: %v", err)
	}

	panes2, err := NewService(db.New(pool)).A2Snapshot(ctx, a2WsUUID(t, wsID), 0)
	if err != nil {
		t.Fatalf("A2Snapshot: %v", err)
	}
	for _, pane := range panes2 {
		if pane.WorkRef == ref2 {
			if pane.ExecutionState != A2ExecutionIssueMismatch {
				t.Fatalf("drifted completed work: execution_state = %q, want issue_state_mismatch", pane.ExecutionState)
			}
			if pane.CompletedAt != nil {
				t.Fatalf("drifted pane must never carry completed_at")
			}
		}
	}
}

// TestA2SnapshotLaterRedispatchKeepsOriginalEmployee proves a later
// re-dispatch of the same issue (new command, new task, new agent) never
// re-attributes an earlier work_ref's pane: ownership comes only from a
// dispatch precisely bound to the pane's task.
func TestA2SnapshotLaterRedispatchKeepsOriginalEmployee(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	wsID, agentA, issueID, task1 := a2SeedWorkspace(ctx, t, pool)
	rtID := a2RuntimeID(ctx, t, pool, wsID)
	now := time.Now().UTC()

	// A second employee in the same workspace.
	var agentB string
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent (workspace_id, name, runtime_mode, kind, runtime_id) VALUES ($1, 'Rival', 'local', 'user', $2) RETURNING id::text`, wsID, rtID).Scan(&agentB); err != nil {
		t.Fatalf("seed agent B: %v", err)
	}

	// The pane's work_ref is bound to task-1 (agent A).
	ref1 := fmt.Sprintf("hivecrew://%s/work/prj/%s/%s", wsID, issueID, task1)
	a2InsertEvent(ctx, t, pool, wsID, ref1, "sess-a", "progress", `{}`,
		now.Add(-5*time.Minute), now.Add(-5*time.Minute))

	// task-1's execution receipt references command C1 (per-run id).
	var cmd1 string
	if err := pool.QueryRow(ctx,
		`INSERT INTO execution_receipt (task_id, workspace_id, issue_id, assignment_command_id,
		   work_order_ref, work_order_revision, work_order_digest, input_digest,
		   employee_ref, employee_revision, employee_digest,
		   binding_ref, binding_revision, binding_digest,
		   agent_ref, agent_revision, agent_digest,
		   runtime_snapshot, runtime_digest, claimed_at, terminal_status, completed_at, finalized_at)
		 VALUES ($1,$2,$3,gen_random_uuid(),
		   'wo://x','r1',$4,$4,'emp://x','r1',$4,'bind://x','r1',$4,'agent://x','r1',$4,
		   '{}'::json,$4,$5,'completed',$5,$5) RETURNING assignment_command_id::text`,
		task1, wsID, issueID, a2Digest, now.Add(-4*time.Minute)).Scan(&cmd1); err != nil {
		t.Fatalf("seed receipt(task1): %v", err)
	}

	// C1 dispatch is precisely bound to task-1 and agent A.
	if _, err := pool.Exec(ctx,
		`INSERT INTO assignment_dispatch_receipt (command_id, workspace_id, issue_id, local_agent_id, initial_task_id,
		   work_order_ref, work_order_revision, work_order_digest, input_digest,
		   employee_ref, employee_revision, employee_digest,
		   binding_ref, binding_revision, binding_digest,
		   agent_ref, agent_revision, agent_digest)
		 VALUES ($1,$2,$3,$4,$5,'wo://x','r1',$6,$6,'emp://x','r1',$6,'bind://x','r1',$6,'agent://x','r1',$6)`,
		cmd1, wsID, issueID, agentA, task1, a2Digest); err != nil {
		t.Fatalf("seed dispatch C1: %v", err)
	}

	// LATER re-dispatch C2 for the same issue but task-2 / agent B. This is
	// the row GetLatestAssignmentDispatchReceiptByIssue used to return.
	task2 := a2InsertTask(ctx, t, pool, agentB, issueID, rtID, "running")
	if _, err := pool.Exec(ctx,
		`INSERT INTO assignment_dispatch_receipt (command_id, workspace_id, issue_id, local_agent_id, initial_task_id,
		   work_order_ref, work_order_revision, work_order_digest, input_digest,
		   employee_ref, employee_revision, employee_digest,
		   binding_ref, binding_revision, binding_digest,
		   agent_ref, agent_revision, agent_digest)
		 VALUES (gen_random_uuid(),$1,$2,$3,$4,'wo://x','r2',$5,$5,'emp://x','r2',$5,'bind://x','r2',$5,'agent://x','r2',$5)`,
		wsID, issueID, agentB, task2, a2Digest); err != nil {
		t.Fatalf("seed dispatch C2: %v", err)
	}

	// The re-dispatch's own work lands in the ledger under ref(task-2).
	ref2 := fmt.Sprintf("hivecrew://%s/work/prj/%s/%s", wsID, issueID, task2)
	a2InsertEvent(ctx, t, pool, wsID, ref2, "sess-b", "progress", `{}`,
		now.Add(-1*time.Minute), now.Add(-1*time.Minute))

	panes, err := NewService(db.New(pool)).A2Snapshot(ctx, a2WsUUID(t, wsID), 0)
	if err != nil {
		t.Fatalf("A2Snapshot: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("expected 2 panes, got %d: %+v", len(panes), panes)
	}
	for _, p := range panes {
		switch p.WorkRef {
		case ref1:
			// Original pane keeps agent A and its precisely bound command C1.
			if p.EmployeeID != agentA {
				t.Fatalf("task-1 pane employee = %q, want original agent %q (later re-dispatch must not hijack)", p.EmployeeID, agentA)
			}
			if p.DispatchCommandID != cmd1 {
				t.Fatalf("task-1 pane dispatch = %q, want its own command %q", p.DispatchCommandID, cmd1)
			}
			if p.EmployeeName != "Shard" {
				t.Fatalf("task-1 pane employee_name = %q, want Shard", p.EmployeeName)
			}
			if p.ExecutionState != A2ExecutionCompleted {
				t.Fatalf("task-1 pane execution_state = %q, want completed (canonical receipt)", p.ExecutionState)
			}
		case ref2:
			// New pane owns agent B through its own task (no dispatch bound
			// to task-2 exists via its receipt, so no command is exposed).
			if p.EmployeeID != agentB {
				t.Fatalf("task-2 pane employee = %q, want agent B %q", p.EmployeeID, agentB)
			}
			if p.DispatchCommandID != "" {
				t.Fatalf("task-2 pane must not expose a dispatch command without precise binding, got %q", p.DispatchCommandID)
			}
		default:
			t.Fatalf("unexpected pane work_ref %q", p.WorkRef)
		}
	}
}

// a2SeedEvidence inserts an execution-receipt CLAIM (terminal_status NULL)
// for the task plus a dispatch receipt precisely bound to it. agentID may be
// empty to reuse the task's own agent; the receipt command is returned.
func a2SeedEvidence(ctx context.Context, t *testing.T, pool *pgxpool.Pool, wsID, issueID, taskID, agentID string) string {
	t.Helper()
	if agentID == "" {
		if err := pool.QueryRow(ctx,
			`SELECT agent_id::text FROM agent_task_queue WHERE id = $1`, taskID).Scan(&agentID); err != nil {
			t.Fatalf("read task agent: %v", err)
		}
	}
	var cmd string
	if err := pool.QueryRow(ctx,
		`INSERT INTO execution_receipt (task_id, workspace_id, issue_id, assignment_command_id,
		   work_order_ref, work_order_revision, work_order_digest, input_digest,
		   employee_ref, employee_revision, employee_digest,
		   binding_ref, binding_revision, binding_digest,
		   agent_ref, agent_revision, agent_digest,
		   runtime_snapshot, runtime_digest, claimed_at)
		 VALUES ($1,$2,$3,gen_random_uuid(),
		   'wo://x','r1',$4,$4,'emp://x','r1',$4,'bind://x','r1',$4,'agent://x','r1',$4,
		   '{}'::json,$4,$5) RETURNING assignment_command_id::text`,
		taskID, wsID, issueID, a2Digest, time.Now().UTC()).Scan(&cmd); err != nil {
		t.Fatalf("seed receipt claim: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO assignment_dispatch_receipt (command_id, workspace_id, issue_id, local_agent_id, initial_task_id,
		   work_order_ref, work_order_revision, work_order_digest, input_digest,
		   employee_ref, employee_revision, employee_digest,
		   binding_ref, binding_revision, binding_digest,
		   agent_ref, agent_revision, agent_digest)
		 VALUES ($1,$2,$3,$4,$5,'wo://x','r1',$6,$6,'emp://x','r1',$6,'bind://x','r1',$6,'agent://x','r1',$6)`,
		cmd, wsID, issueID, agentID, taskID, a2Digest); err != nil {
		t.Fatalf("seed dispatch: %v", err)
	}
	return cmd
}

// TestA2SnapshotEventPlusHeartbeatAloneNeverWorking proves end-to-end that a
// live event, a running task, and a fresh session-matched heartbeat are still
// NOT working without exact dispatch/receipt evidence for the task.
func TestA2SnapshotEventPlusHeartbeatAloneNeverWorking(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	wsID, agentID, issueID, taskID := a2SeedWorkspace(ctx, t, pool)
	ref := fmt.Sprintf("hivecrew://%s/work/prj/%s/%s", wsID, issueID, taskID)
	now := time.Now().UTC()
	session := fmt.Sprintf("sess-a2-noev-%d", time.Now().UnixNano())
	a2InsertEvent(ctx, t, pool, wsID, ref, session, "progress", `{"stage":"build"}`,
		now.Add(-time.Minute), now.Add(-time.Minute))
	if _, err := pool.Exec(ctx,
		`INSERT INTO terminal_presence (workspace_id, host, session_name, current_command, agent_hint)
		 VALUES ($1, 'mac-a2n', $2, 'go test', $3)`, wsID, session, agentID); err != nil {
		t.Fatalf("seed terminal_presence: %v", err)
	}
	// No execution_receipt, no assignment_dispatch_receipt: bare task only.

	panes, err := NewService(db.New(pool)).A2Snapshot(ctx, a2WsUUID(t, wsID), 0)
	if err != nil {
		t.Fatalf("A2Snapshot: %v", err)
	}
	if len(panes) != 1 {
		t.Fatalf("expected 1 pane, got %d", len(panes))
	}
	p := panes[0]
	if p.SurfaceKind != A2SurfaceTerminal {
		t.Fatalf("surface = %q, want terminal (heartbeat is real, evidence is not)", p.SurfaceKind)
	}
	if p.Working {
		t.Fatalf("event + heartbeat + bare task without exact dispatch/receipt evidence must never count as working")
	}
	// B3-1: with no receipt evidence the in-flight claim is unconfirmed —
	// non-active AND non-working despite the live event and heartbeat.
	if p.ExecutionState != A2ExecutionIssueMismatch {
		t.Fatalf("execution_state = %q, want issue_state_mismatch", p.ExecutionState)
	}
	if p.EmployeeID != agentID {
		t.Fatalf("employee fallback = %q, want task agent %q", p.EmployeeID, agentID)
	}
	if p.DispatchCommandID != "" {
		t.Fatalf("no dispatch evidence: dispatch_command_id must stay empty")
	}
}

// B3-2 (service half): a work_ref whose task id does not resolve inside the
// requesting workspace (foreign tenant task) must produce a pane WITHOUT any
// receipt evidence — proving the unscoped GetExecutionReceipt read is gated
// behind the workspace-scoped task read — and must never be active/working.
func TestA2SnapshotForeignTaskGatesReceiptRead(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	// Tenant B owns the task; tenant A's ledger carries a work_ref naming it.
	wsA, agentA, _, _ := a2SeedWorkspace(ctx, t, pool)
	wsB, agentB, issueB, taskB := a2SeedSecondWorkspace(ctx, t, pool)
	now := time.Now().UTC()
	// Give tenant B's task a receipt so a leaky read WOULD find evidence.
	a2SeedEvidence(ctx, t, pool, wsB, issueB, taskB, agentB)

	ref := fmt.Sprintf("hivecrew://%s/work/prj/%s/%s", wsA, issueB, taskB)
	session := fmt.Sprintf("sess-b3-%d", time.Now().UnixNano())
	a2InsertEvent(ctx, t, pool, wsA, ref, session, "progress", `{"stage":"build"}`,
		now.Add(-time.Minute), now.Add(-time.Minute))
	if _, err := pool.Exec(ctx,
		`INSERT INTO terminal_presence (workspace_id, host, session_name, current_command, agent_hint)
		 VALUES ($1, 'mac-b3', $2, 'go test', $3)`, wsA, session, agentA); err != nil {
		t.Fatalf("seed presence: %v", err)
	}

	panes, err := NewService(db.New(pool)).A2Snapshot(ctx, a2WsUUID(t, wsA), 0)
	if err != nil {
		t.Fatalf("A2Snapshot: %v", err)
	}
	if len(panes) != 1 {
		t.Fatalf("expected 1 pane, got %d", len(panes))
	}
	p := panes[0]
	if p.TaskID != "" {
		t.Fatalf("foreign task leaked into pane: %q", p.TaskID)
	}
	if p.EmployeeID != "" && p.EmployeeID == agentB {
		t.Fatalf("foreign tenant employee leaked: %q", p.EmployeeID)
	}
	if p.ExecutionState == A2ExecutionActive || p.Working {
		t.Fatalf("foreign task must fail closed (state %q, working %v)", p.ExecutionState, p.Working)
	}
	if p.DispatchCommandID != "" {
		t.Fatalf("no receipt evidence: dispatch must stay empty")
	}
}

// B3-3 (service half): a work_ref whose embedded issue and task disagree
// (task belongs to another issue) must fail closed end-to-end: no task
// fields, no employee, no evidence, never active/working — even though both
// rows live in the SAME workspace.
func TestA2SnapshotCrossIssueTaskFailsClosed(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	wsID, agentID, issueID, _ := a2SeedWorkspace(ctx, t, pool)
	rtID := a2RuntimeID(ctx, t, pool, wsID)
	now := time.Now().UTC()

	// Second issue in the same workspace, with its own task.
	issue2 := ""
	if err := pool.QueryRow(ctx,
		`INSERT INTO issue (workspace_id, title, status, number, creator_type, creator_id) VALUES ($1, 'A2 other', 'in_progress', 2, 'agent', $2) RETURNING id::text`,
		wsID, agentID).Scan(&issue2); err != nil {
		t.Fatalf("seed issue2: %v", err)
	}
	task2 := a2InsertTask(ctx, t, pool, agentID, issue2, rtID, "running")
	// Evidence exists for task2 (receipt + bound dispatch).
	a2SeedEvidence(ctx, t, pool, wsID, issue2, task2, agentID)

	// The work_ref names issue1 + task2: a cross-issue mismatch.
	ref := fmt.Sprintf("hivecrew://%s/work/prj/%s/%s", wsID, issueID, task2)
	session := fmt.Sprintf("sess-b3x-%d", time.Now().UnixNano())
	a2InsertEvent(ctx, t, pool, wsID, ref, session, "progress", `{"stage":"build"}`,
		now.Add(-time.Minute), now.Add(-time.Minute))
	if _, err := pool.Exec(ctx,
		`INSERT INTO terminal_presence (workspace_id, host, session_name, current_command, agent_hint)
		 VALUES ($1, 'mac-b3x', $2, 'go test', $3)`, wsID, session, agentID); err != nil {
		t.Fatalf("seed presence: %v", err)
	}

	panes, err := NewService(db.New(pool)).A2Snapshot(ctx, a2WsUUID(t, wsID), 0)
	if err != nil {
		t.Fatalf("A2Snapshot: %v", err)
	}
	var found bool
	for _, p := range panes {
		if p.WorkRef != ref {
			continue
		}
		found = true
		if p.TaskID != "" {
			t.Fatalf("cross-issue task leaked: %q", p.TaskID)
		}
		if p.EmployeeID != "" {
			t.Fatalf("cross-issue task must not set employee, got %q", p.EmployeeID)
		}
		if p.ExecutionState == A2ExecutionActive || p.Working {
			t.Fatalf("cross-issue task must fail closed (state %q, working %v)", p.ExecutionState, p.Working)
		}
		if p.DispatchCommandID != "" {
			t.Fatalf("cross-issue dispatch leaked: %q", p.DispatchCommandID)
		}
	}
	if !found {
		t.Fatalf("expected the cross-issue pane to render (visible mismatch), got %d panes", len(panes))
	}
}
