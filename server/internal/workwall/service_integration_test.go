//go:build integration

package workwall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
	// Execution-chain reads (HIV-797) must also be valid against the schema.
	// The probes use a workspace that holds no rows, so pgx.ErrNoRows is the
	// expected healthy outcome: it proves the query compiled and ran. The
	// workspace prefix, profile and receipt probes are the Work Wall's narrow
	// projections — they select only the columns the card renders.
	if _, err := q.GetWorkspaceIssuePrefix(ctx, ws); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetWorkspaceIssuePrefix: %v", err)
	}
	if _, err := q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: ws, WorkspaceID: ws}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetIssueInWorkspace: %v", err)
	}
	if _, err := q.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: ws, WorkspaceID: ws}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetProjectInWorkspace: %v", err)
	}
	if _, err := q.GetRuntimeProfileForWorkWall(ctx, db.GetRuntimeProfileForWorkWallParams{ID: ws, WorkspaceID: ws}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetRuntimeProfileForWorkWall: %v", err)
	}
	if _, err := q.GetExecutionReceiptForWorkWall(ctx, ws); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetExecutionReceiptForWorkWall: %v", err)
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

	// Full chain seed: workspace prefix, runtime profile, issue (in a
	// project) and a running task for the agent. No receipt row exists.
	if _, err := pool.Exec(ctx,
		`UPDATE workspace SET issue_prefix = 'HIV' WHERE id = $1`, wsID); err != nil {
		t.Fatalf("seed issue prefix: %v", err)
	}
	var profileID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO runtime_profile (workspace_id, display_name, protocol_family, command_name, enabled) VALUES ($1, 'glm-5.3 运行档案', 'http', 'glm', true) RETURNING id::text`,
		wsID).Scan(&profileID); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE agent_runtime SET profile_id = $2 WHERE id = $1`, rtID, profileID); err != nil {
		t.Fatalf("bind profile: %v", err)
	}
	var projectID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO project (workspace_id, title) VALUES ($1, 'HIVECREW 自我开发项目') RETURNING id::text`,
		wsID).Scan(&projectID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	var issueID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO issue (workspace_id, title, status, number, project_id) VALUES ($1, '[DEV] Work Wall complete execution-chain projection', 'in_progress', 797, $2) RETURNING id::text`,
		wsID, projectID).Scan(&issueID); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_task_queue (agent_id, issue_id, status) VALUES ($1, $2, 'running')`,
		agentID, issueID); err != nil {
		t.Fatalf("seed task: %v", err)
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
	if dto.IssueID != issueID || dto.IssueIdentifier != "HIV-797" || dto.IssueTitle != "[DEV] Work Wall complete execution-chain projection" {
		t.Fatalf("issue chain = %q / %q / %q", dto.IssueID, dto.IssueIdentifier, dto.IssueTitle)
	}
	if dto.ProjectID != projectID || dto.ProjectTitle != "HIVECREW 自我开发项目" {
		t.Fatalf("project chain = %q / %q", dto.ProjectID, dto.ProjectTitle)
	}
	if dto.RuntimeProfileID != profileID || dto.RuntimeProfileName != "glm-5.3 运行档案" {
		t.Fatalf("profile chain = %q / %q", dto.RuntimeProfileID, dto.RuntimeProfileName)
	}
	if dto.RunID != "" {
		t.Fatalf("direct task must have no separate Run ID in this version, got %q", dto.RunID)
	}
	if dto.ExecutionReceiptRef != "" || dto.ExecutionReceiptStatus != "" {
		t.Fatalf("unseeded receipt must stay absent, got %q / %q", dto.ExecutionReceiptRef, dto.ExecutionReceiptStatus)
	}
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
