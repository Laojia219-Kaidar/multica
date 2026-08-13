package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
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

// withFrontierEvidence attaches an immutable whole-frontier evidence snapshot
// to a request's context. When GetIssueFrontier sees the snapshot it classifies
// the snapshot directly with no DB, auth, load, prerequisite, health or
// ListTasks query. It exists solely for the hermetic handler-response fixtures
// (HIVECREW_DB_FREE_FRONTIER); production requests never carry this key.
func withFrontierEvidence(req *http.Request, ev *frontierEvidence) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), frontierEvidenceKey{}, ev))
}

// healthyFrontierEvidence returns an evidence snapshot that simulates the
// canonical happy path except capacity, which is driven by countFn: a todo
// issue the caller can access, prerequisites clear, an assigned non-archived
// agent bound to an online runtime, and no task. The three fixture scenarios
// vary only countFn (error / full / free).
func healthyFrontierEvidence(countFn frontierCountFn) *frontierEvidence {
	return &frontierEvidence{
		issue:              db.Issue{Status: "todo"},
		hasAssignee:        true,
		runtimeBound:       true,
		runtimeOnline:      true,
		agentMaxConcurrent: 1,
		countFn:            countFn,
	}
}

// fixtureFrontierRequest builds a GET /api/issues/{id}/frontier request that
// carries the given evidence snapshot. It uses no testHandler/testPool state.
func fixtureFrontierRequest(ev *frontierEvidence) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/issues/00000000-0000-0000-0000-000000000000/frontier", nil)
	req = withURLParam(req, "id", "00000000-0000-0000-0000-000000000000")
	return withFrontierEvidence(req, ev)
}

// callGetIssueFrontierWith invokes GetIssueFrontier through httptest with the
// given (snapshot-carrying) request. Returns the HTTP status and decoded body.
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

// TestFrontierFixture_CountError_BlockedMissingEvidence verifies that when
// CountRunningTasks returns an error (everything else a success), the handler
// responds HTTP 200 with state=blocked and reason=missing_evidence — never
// ready. This is the fail-closed property: an evidence gap must not produce an
// optimistic ready classification. Runs under HIVECREW_DB_FREE_FRONTIER=1 with
// no DB, socket, DATABASE_URL, or credential read.
func TestFrontierFixture_CountError_BlockedMissingEvidence(t *testing.T) {
	req := fixtureFrontierRequest(healthyFrontierEvidence(func(ctx context.Context, agentID pgtype.UUID) (int64, error) {
		return 0, context.DeadlineExceeded
	}))

	code, resp := callGetIssueFrontierWith(t, req)
	if code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", code)
	}
	if resp.FrontierState != "blocked" {
		t.Fatalf("CountRunningTasks error: expected state=blocked, got %q (%v)", resp.FrontierState, resp.FrontierReasons)
	}
	if !frontierHasReason(resp, "missing_evidence") {
		t.Fatalf("expected reason missing_evidence, got %v", resp.FrontierReasons)
	}
}

// TestFrontierFixture_CapacityFull_Waiting verifies that when the agent's
// concurrent-task slot is fully occupied, the classification is
// waiting/capacity_unavailable — the issue is healthy but transiently held by
// a known capacity constraint.
func TestFrontierFixture_CapacityFull_Waiting(t *testing.T) {
	req := fixtureFrontierRequest(healthyFrontierEvidence(func(ctx context.Context, agentID pgtype.UUID) (int64, error) {
		return 5, nil // 5 running, max 1 -> full
	}))

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

// TestFrontierFixture_HealthyFree_Ready verifies the full happy path through
// the handler: a todo issue with a healthy agent, runtime online, and a free
// capacity slot returns state=ready with no reasons.
func TestFrontierFixture_HealthyFree_Ready(t *testing.T) {
	req := fixtureFrontierRequest(healthyFrontierEvidence(func(ctx context.Context, agentID pgtype.UUID) (int64, error) {
		return 0, nil // 0 running, max 1 -> free
	}))

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

// TestFrontierOverrideIsAdditive is a static regression: when a request carries
// no evidence snapshot, frontierEvidenceFromContext returns nil, so
// GetIssueFrontier selects the canonical production path
// (loadIssueForUser/resolveFrontier*/ListTasksByIssue). The override is purely
// additive and never alters production requests. It runs DB-free.
func TestFrontierOverrideIsAdditive(t *testing.T) {
	if frontierEvidenceFromContext(context.Background()) != nil {
		t.Fatal("absent override, frontierEvidenceFromContext must return nil so the canonical production path runs unchanged")
	}
}
