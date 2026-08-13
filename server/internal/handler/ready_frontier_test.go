package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// ready_frontier_test.go — HIV-404 ready-frontier queue sensor integration tests.
//
// These tests exercise the read-only classifier end-to-end against the real
// migrated test Postgres (loopback only), mirroring pipeline_test.go:
//   - a todo issue with a healthy assigned agent and no task -> ready;
//   - a running/queued task -> running/waiting;
//   - a failed task -> blocked;
//   - an explicit blocked status -> blocked;
//   - a superseded review_state -> superseded;
//   - cross-workspace issue UUID -> honest 404 (not an enumeration oracle).
//
// They skip cleanly when testHandler/testPool is nil so CI without a DB does
// not fail. The pure classifier logic itself is unit-tested in
// internal/readyfrontier (no DB required).

// seedFrontierAgent creates an online runtime + agent in the test workspace and
// returns the agent id. Self-contained so it does not depend on global fixtures.
func seedFrontierAgent(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider,
			status, device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, NULL, $2, 'cloud', 'frontier_test_runtime', 'online',
		        'frontier fixture', '{}'::jsonb, now(), 'private', $3)
		RETURNING id
	`, testWorkspaceID, "frontier runtime "+t.Name(), testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID) })

	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 1, $4)
		RETURNING id
	`, testWorkspaceID, "frontier agent "+t.Name(), runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID) })
	return agentID
}

// seedFrontierIssue creates an issue with the given status, optional agent
// assignee, and optional review_state. Returns the issue id.
func seedFrontierIssue(t *testing.T, projectID, status, agentID, reviewState string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := testPool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, project_id, title, status, priority,
			creator_type, creator_id, assignee_type, assignee_id, review_state
		)
		VALUES ($1, $2, $3, $4, 'medium', 'member', $5, $6, $7, $8)
		RETURNING id
	`,
		testWorkspaceID, projectID, "frontier issue "+t.Name(), status, testUserID,
		nullableText(assigneeTypeFor(agentID)), nullableUUID(agentID), nullableText(reviewState),
	).Scan(&id)
	if err != nil {
		t.Fatalf("seed issue (%s): %v", status, err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id) })
	return id
}

func assigneeTypeFor(agentID string) string {
	if agentID == "" {
		return ""
	}
	return "agent"
}

// nullableUUID returns a NULLable uuid value for SQL insertion: nil when empty.
func nullableUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// callGetIssueFrontier invokes the handler and returns the decoded response.
func callGetIssueFrontier(t *testing.T, issueID string) (int, IssueFrontierResponse) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues/"+issueID+"/frontier", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.GetIssueFrontier(w, req)
	var resp IssueFrontierResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode frontier response: %v", err)
		}
	}
	return w.Code, resp
}

func TestFrontier_TodoHealthyAgent_Ready(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := seedPipelineProject(t)
	agentID := seedFrontierAgent(t)
	issueID := seedFrontierIssue(t, projectID, "todo", agentID, "")

	code, resp := callGetIssueFrontier(t, issueID)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if resp.FrontierState != "ready" {
		t.Fatalf("todo + healthy agent + no task: expected ready, got %q (%v)", resp.FrontierState, resp.FrontierReasons)
	}
	if len(resp.FrontierReasons) != 0 {
		t.Fatalf("ready must carry no reasons, got %v", resp.FrontierReasons)
	}
}

func TestFrontier_RunningTask_Running(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := seedPipelineProject(t)
	agentID := seedFrontierAgent(t)
	issueID := seedFrontierIssue(t, projectID, "in_progress", agentID, "")
	seedPipelineTask(t, issueID, "running", "")

	code, resp := callGetIssueFrontier(t, issueID)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if resp.FrontierState != "running" {
		t.Fatalf("in_progress + running task: expected running, got %q (%v)", resp.FrontierState, resp.FrontierReasons)
	}
	if resp.LatestTaskID == "" {
		t.Fatalf("expected latest_task_id to be set")
	}
}

func TestFrontier_FailedTask_Blocked(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := seedPipelineProject(t)
	agentID := seedFrontierAgent(t)
	issueID := seedFrontierIssue(t, projectID, "in_progress", agentID, "")
	seedPipelineTask(t, issueID, "failed", "runtime crashed")

	code, resp := callGetIssueFrontier(t, issueID)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if resp.FrontierState != "blocked" || !frontierHasReason(resp, "failed") {
		t.Fatalf("failed task: expected blocked/failed, got %q (%v)", resp.FrontierState, resp.FrontierReasons)
	}
}

func TestFrontier_BlockedStatus_Blocked(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := seedPipelineProject(t)
	issueID := seedFrontierIssue(t, projectID, "blocked", "", "")

	code, resp := callGetIssueFrontier(t, issueID)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if resp.FrontierState != "blocked" || !frontierHasReason(resp, "blocked_status") {
		t.Fatalf("blocked status: expected blocked/blocked_status, got %q (%v)", resp.FrontierState, resp.FrontierReasons)
	}
}

func TestFrontier_SupersededReviewState_Superseded(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := seedPipelineProject(t)
	issueID := seedFrontierIssue(t, projectID, "in_progress", "", "superseded")

	code, resp := callGetIssueFrontier(t, issueID)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if resp.FrontierState != "superseded" || !frontierHasReason(resp, "superseded") {
		t.Fatalf("superseded review_state: expected superseded/superseded, got %q (%v)", resp.FrontierState, resp.FrontierReasons)
	}
}

func TestFrontier_CrossWorkspace_Returns404(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	var foreignWSID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Frontier Foreign WS", "frontier-foreign-ws", "cross-tenant frontier test", "FFX").Scan(&foreignWSID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWSID) })

	var foreignIssueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id)
		VALUES ($1, 'foreign issue', 'todo', 'medium', 'member', $2)
		RETURNING id
	`, foreignWSID, testUserID).Scan(&foreignIssueID); err != nil {
		t.Fatalf("seed foreign issue: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues/"+foreignIssueID+"/frontier", nil)
	req = withURLParam(req, "id", foreignIssueID)
	testHandler.GetIssueFrontier(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace frontier: expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFrontier_InvalidIssueID_Returns404(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues/not-a-uuid/frontier", nil)
	req = withURLParam(req, "id", "not-a-uuid")
	testHandler.GetIssueFrontier(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("invalid issue id: expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func frontierHasReason(resp IssueFrontierResponse, want string) bool {
	for _, r := range resp.FrontierReasons {
		if r == want {
			return true
		}
	}
	return false
}

// withFrontierCountOverride returns a shallow copy of req whose context carries
// a CountRunningTasks seam. Production code never sets this key; it exists
// solely for handler-response fixture tests that must simulate DB query
// outcomes (success, error, capacity-full) without a real database round-trip
// for that one query.
func withFrontierCountOverride(req *http.Request, fn frontierCountFn) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), frontierCountKey{}, frontierCountFn(fn)))
}

// callGetIssueFrontierWith invokes GetIssueFrontier through httptest with an
// optionally overridden request (e.g. context seam). Returns the HTTP status
// and decoded response body.
func callGetIssueFrontierWith(t *testing.T, req *http.Request) (int, IssueFrontierResponse) {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.GetIssueFrontier(w, req)
	var resp IssueFrontierResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode frontier response: %v", err)
		}
	}
	return w.Code, resp
}

// TestFrontier_CountRunningTasksError_BlockedMissingEvidence verifies that when
// ListTasksByIssue succeeds but CountRunningTasks returns an error, the handler
// responds HTTP 200 with state=blocked and reason=missing_evidence — never
// ready. This is the fail-closed property: an evidence gap must not produce an
// optimistic ready classification.
func TestFrontier_CountRunningTasksError_BlockedMissingEvidence(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := seedPipelineProject(t)
	agentID := seedFrontierAgent(t)
	issueID := seedFrontierIssue(t, projectID, "todo", agentID, "")

	req := newRequest("GET", "/api/issues/"+issueID+"/frontier", nil)
	req = withURLParam(req, "id", issueID)
	req = withFrontierCountOverride(req, func(ctx context.Context, agentID pgtype.UUID) (int64, error) {
		return 0, context.DeadlineExceeded
	})

	code, resp := callGetIssueFrontierWith(t, req)
	if code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", code, resp.FrontierState)
	}
	if resp.FrontierState != "blocked" {
		t.Fatalf("CountRunningTasks error: expected state=blocked, got %q (%v)", resp.FrontierState, resp.FrontierReasons)
	}
	if !frontierHasReason(resp, "missing_evidence") {
		t.Fatalf("expected reason missing_evidence, got %v", resp.FrontierReasons)
	}
}

// TestFrontier_CapacityFull_WaitingCapacity verifies that when the agent's
// concurrent-task slot is fully occupied, the classification is
// waiting/capacity_unavailable — the issue is healthy but transiently held by
// a known capacity constraint.
func TestFrontier_CapacityFull_WaitingCapacity(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := seedPipelineProject(t)
	agentID := seedFrontierAgent(t)
	issueID := seedFrontierIssue(t, projectID, "todo", agentID, "")

	req := newRequest("GET", "/api/issues/"+issueID+"/frontier", nil)
	req = withURLParam(req, "id", issueID)
	req = withFrontierCountOverride(req, func(ctx context.Context, agentID pgtype.UUID) (int64, error) {
		return 5, nil
	})

	code, resp := callGetIssueFrontierWith(t, req)
	if code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", code)
	}
	if resp.FrontierState != "waiting" {
		t.Fatalf("capacity full: expected state=waiting, got %q (%v)", resp.FrontierState, resp.FrontierReasons)
	}
	if !frontierHasReason(resp, "capacity_unavailable") {
		t.Fatalf("expected reason capacity_unavailable, got %v", resp.FrontierReasons)
	}
}

// TestFrontier_HealthyFree_Ready verifies the full happy path through the
// handler: a todo issue with a healthy agent, runtime online, and a free
// capacity slot returns state=ready with no reasons.
func TestFrontier_HealthyFree_Ready(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := seedPipelineProject(t)
	agentID := seedFrontierAgent(t)
	issueID := seedFrontierIssue(t, projectID, "todo", agentID, "")

	req := newRequest("GET", "/api/issues/"+issueID+"/frontier", nil)
	req = withURLParam(req, "id", issueID)
	req = withFrontierCountOverride(req, func(ctx context.Context, agentID pgtype.UUID) (int64, error) {
		return 0, nil
	})

	code, resp := callGetIssueFrontierWith(t, req)
	if code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", code)
	}
	if resp.FrontierState != "ready" {
		t.Fatalf("healthy + free capacity: expected state=ready, got %q (%v)", resp.FrontierState, resp.FrontierReasons)
	}
	if len(resp.FrontierReasons) != 0 {
		t.Fatalf("ready must carry no reasons, got %v", resp.FrontierReasons)
	}
}
