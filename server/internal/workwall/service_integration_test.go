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
	companyopsapi "github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/liveactivity"
	"github.com/multica-ai/multica/server/internal/service"
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
	// The probes use a workspace that holds no rows, so pgx.ErrNoRows is the
	// expected healthy outcome: it proves the query compiled and ran. The
	// snapshot flow itself needs a real workspace row (the issue-prefix read
	// is :one), so seed an empty workspace with a prefix for it.
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

	probeSlug := fmt.Sprintf("w4-probe-%d", time.Now().UnixNano())
	var probeWsID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug, issue_prefix) VALUES ($1, $1, 'HIV') RETURNING id::text`,
		probeSlug).Scan(&probeWsID); err != nil {
		t.Fatalf("seed probe workspace: %v", err)
	}
	var probeWs pgtype.UUID
	if err := probeWs.Scan(probeWsID); err != nil {
		t.Fatalf("parse probe ws uuid: %v", err)
	}
	svc := NewService(q, nil)
	snap, err := svc.Snapshot(ctx, probeWs)
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
		`INSERT INTO runtime_profile (workspace_id, display_name, protocol_family, command_name, enabled) VALUES ($1, 'glm-5.3 运行档案', 'codex', 'codex', true) RETURNING id::text`,
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
		`INSERT INTO issue (workspace_id, title, status, number, project_id, creator_type, creator_id) VALUES ($1, '[DEV] Work Wall complete execution-chain projection', 'in_progress', 797, $2, 'agent', gen_random_uuid()) RETURNING id::text`,
		wsID, projectID).Scan(&issueID); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status) VALUES ($1, $3, $2, 'running')`,
		agentID, issueID, rtID); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	var wsUUID pgtype.UUID
	if err := wsUUID.Scan(wsID); err != nil {
		t.Fatalf("parse ws uuid: %v", err)
	}

	// No CompanyOps directory is bound: the formal Employee authority is
	// unavailable, so the card must fail closed to the authority-gap state.
	svc := NewService(db.New(pool), nil)
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
	// Without directory authority the formal Employee fields are cleared:
	// employee_id no longer mirrors the agent UUID (HIV-854).
	if dto.EmployeeID != "" {
		t.Fatalf("employee_id = %q, want empty without Employee authority", dto.EmployeeID)
	}
	if dto.AgentID != agentID {
		t.Fatalf("agent_id = %q, want %q", dto.AgentID, agentID)
	}
	if dto.PresenceState != liveactivity.PresenceOffline {
		t.Fatalf("presence = %q, want offline (no runtime)", dto.PresenceState)
	}
	// The authority gap can never look fresh: the runtime row is present and
	// offline (fresh by heartbeat), so the generic gap state must surface.
	if dto.FreshnessState != liveactivity.FreshnessConflict {
		t.Fatalf("freshness = %q, want conflict (authority gap cannot look fresh)", dto.FreshnessState)
	}
	if dto.RuntimeID != rtID {
		t.Fatalf("runtime_id = %q, want %q", dto.RuntimeID, rtID)
	}
}

// TestSnapshotWithDirectoryOverlay runs the full SQL -> directory seam -> DTO
// flow: a stubbed CompanyOps Employee directory that exactly matches the
// seeded agent must overlay the formal Employee identity, a second seeded
// agent the authority does not name stays an agent-only card, and a failing
// seam degrades every card to the authority gap without dropping any card.
func TestSnapshotWithDirectoryOverlay(t *testing.T) {
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

	slug := fmt.Sprintf("w4-dir-%d", time.Now().UnixNano())
	var wsID, rtID, agentID, agent2ID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug, issue_prefix) VALUES ($1, $1, 'HIV') RETURNING id::text`,
		slug).Scan(&wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, last_seen_at) VALUES ($1, 'rt1', 'local', 'prime', 'online', now()) RETURNING id::text`,
		wsID).Scan(&rtID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent (workspace_id, name, runtime_mode, kind, runtime_id) VALUES ($1, 'Emory', 'local', 'user', $2) RETURNING id::text`,
		wsID, rtID).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent (workspace_id, name, runtime_mode, kind, runtime_id) VALUES ($1, 'Unbound', 'local', 'user', $2) RETURNING id::text`,
		wsID, rtID).Scan(&agent2ID); err != nil {
		t.Fatalf("seed agent 2: %v", err)
	}

	var wsUUID pgtype.UUID
	if err := wsUUID.Scan(wsID); err != nil {
		t.Fatalf("parse ws uuid: %v", err)
	}

	snapshotFor := func(dir EmployeeDirectory) map[string]liveactivity.EmployeeLiveActivityV1 {
		svc := NewService(db.New(pool), nil)
		svc.Directory = dir
		snap, err := svc.Snapshot(ctx, wsUUID)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(snap) != 2 {
			t.Fatalf("expected 2 cards, got %d", len(snap))
		}
		byAgent := make(map[string]liveactivity.EmployeeLiveActivityV1, len(snap))
		for _, dto := range snap {
			byAgent[dto.AgentID] = dto
		}
		return byAgent
	}

	// (a) Verified exact match: the stub directory binds the seeded agent to
	// a formal employee on both reads.
	verified := snapshotFor(&fakeEmployeeDirectory{
		joinRows: []service.WorkforceBaseRuntimeRow{joinRowFor("DE-KAI-01", agentID, "HiveCosm Mac mini")},
		pages:    []*service.EmployeesResult{employeesPage(1, summaryFor("DE-KAI-01", agentID, "Kai｜后端与全栈工程师"))},
	})
	bound := verified[agentID]
	if bound.EmployeeID != "DE-KAI-01" || bound.DisplayName != "Kai｜后端与全栈工程师" {
		t.Fatalf("verified overlay = %q / %q", bound.EmployeeID, bound.DisplayName)
	}
	if bound.DepartmentName != "工程部" || bound.PositionName != "后端与全栈工程师" || bound.BaseName != "HiveCosm Mac mini" {
		t.Fatalf("formal organization/base fields = %+v", bound)
	}
	if bound.FreshnessState != liveactivity.FreshnessFresh {
		t.Fatalf("verified authority + online runtime = %q, want fresh", bound.FreshnessState)
	}
	// The agent the authority does not name stays a healthy agent-only card.
	unbound := verified[agent2ID]
	if unbound.EmployeeID != "" || unbound.DisplayName != "Unbound" {
		t.Fatalf("unmatched agent = %q / %q, want agent-only card", unbound.EmployeeID, unbound.DisplayName)
	}
	if unbound.FreshnessState != liveactivity.FreshnessFresh {
		t.Fatalf("unmatched agent freshness = %q, want fresh (authority healthy)", unbound.FreshnessState)
	}

	// (b) Authority unavailable: every card survives, formal fields stay
	// cleared, and no card looks fresh.
	gap := snapshotFor(&fakeEmployeeDirectory{
		joinErr: fmt.Errorf("%w: HTTP 503", companyopsapi.ErrAdapterSourceGap),
	})
	for _, aid := range []string{agentID, agent2ID} {
		card := gap[aid]
		if card.EmployeeID != "" || card.DepartmentName != "" || card.BaseName != "" {
			t.Fatalf("gap card must keep formal fields cleared: %+v", card)
		}
		if card.FreshnessState == liveactivity.FreshnessFresh {
			t.Fatalf("authority-unavailable card must never look fresh: %+v", card)
		}
		if card.PresenceState != liveactivity.PresenceIdle {
			t.Fatalf("presence derivation must be untouched by the gap, got %q", card.PresenceState)
		}
	}
}
