//go:build integration

package workwall

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/liveactivity"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestSnapshotQueriesRunAgainstRealSchema verifies that the four read queries
// used by Service.Snapshot are valid against a real migrated schema and that
// the full snapshot flow runs end-to-end (empty result for an empty workspace).
func TestSnapshotQueriesRunAgainstRealSchema(t *testing.T) {
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

	q := db.New(pool)
	ws := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}

	if _, err := q.ListAgents(ctx, ws); err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if _, err := q.ListAgentRuntimes(ctx, ws); err != nil {
		t.Fatalf("ListAgentRuntimes: %v", err)
	}
	if _, err := q.ListWorkspaceAgentTaskSnapshot(ctx, ws); err != nil {
		t.Fatalf("ListWorkspaceAgentTaskSnapshot: %v", err)
	}
	if _, err := q.ListActivitiesForIssue(ctx, db.ListActivitiesForIssueParams{IssueID: ws, Limit: 5}); err != nil {
		t.Fatalf("ListActivitiesForIssue: %v", err)
	}

	svc := NewService(q)
	snap, err := svc.Snapshot(ctx, ws)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 0 {
		t.Fatalf("empty workspace snapshot should be empty, got %d rows", len(snap))
	}
}

// TestSnapshotWithSeededAgent verifies the full SQL -> DTO data flow: seed one
// workspace + one agent (no runtime, no task), then assert Snapshot returns a
// correct offline EmployeeLiveActivityV1.
func TestSnapshotWithSeededAgent(t *testing.T) {
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

	slug := fmt.Sprintf("w4-int-%d", time.Now().UnixNano())
	var wsID, rtID, agentID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ($1, $2) RETURNING id::text`,
		slug, slug).Scan(&wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider) VALUES ($1, 'rt1', 'local', 'prime') RETURNING id::text`,
		wsID).Scan(&rtID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent (workspace_id, name, runtime_mode, kind, runtime_id) VALUES ($1, 'Emory', 'local', 'user', $2) RETURNING id::text`,
		wsID, rtID).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	var wsUUID pgtype.UUID
	if err := wsUUID.Scan(wsID); err != nil {
		t.Fatalf("parse ws uuid: %v", err)
	}

	svc := NewService(db.New(pool))
	snap, err := svc.Snapshot(ctx, wsUUID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("expected 1 employee, got %d", len(snap))
	}
	dto := snap[0]
	if dto.DisplayName != "Emory" {
		t.Fatalf("display_name = %q", dto.DisplayName)
	}
	if dto.EmployeeID != agentID || dto.AgentID != agentID {
		t.Fatalf("identity mismatch: employee=%q agent=%q want %q", dto.EmployeeID, dto.AgentID, agentID)
	}
	if dto.PresenceState != liveactivity.PresenceOffline {
		t.Fatalf("presence = %q, want offline (no runtime)", dto.PresenceState)
	}
	if dto.FreshnessState != liveactivity.FreshnessFresh {
		t.Fatalf("freshness = %q, want fresh (runtime row present, offline)", dto.FreshnessState)
	}
	if dto.RuntimeID != rtID {
		t.Fatalf("runtime_id = %q, want %q", dto.RuntimeID, rtID)
	}
}
