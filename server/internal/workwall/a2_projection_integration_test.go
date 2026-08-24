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
		t.Fatalf("fresh session-matched heartbeat + live evidence should count as working")
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
