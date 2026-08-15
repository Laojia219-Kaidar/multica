package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
)

// project_autostart_test.go — HIV-405 Project start/continue control tests.
//
// DB_BACKED: these tests require a live Postgres (handler TestMain skips the
// suite when DATABASE_URL is unreachable). In lanes forbidden from touching a
// database they remain DB_UNVERIFIED and are independently re-verified by the
// isolated DB gate.
//
// Covers:
//   1. Repeat click (idempotency) — same idempotency_key replays safely.
//   2. No-ready-work — project with no assignable issues returns empty wave.
//   3. Failed employee replacement — archived agent → non-empty blocked with
//      agent_archived reason (no vacuous pass).
//   4. Receipt readback — dispatched issues return task IDs.
//   5. Negative fixtures must assert non-empty (HIV-473): offline runtime →
//      runtime_offline, zero capacity → capacity_full, unmet prerequisite →
//      missing_prerequisite, duplicate selection → duplicate_in_batch with no
//      task row created.

// seedAutoStartProject creates a project for autostart tests.
func seedAutoStartProject(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, status, priority)
		VALUES ($1, $2, 'in_progress', 'high')
		RETURNING id
	`, testWorkspaceID, "AutoStart Test "+t.Name()).Scan(&id); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, id)
	})
	return id
}

// seedAutoStartIssue creates an issue with an agent assignee in a project.
func seedAutoStartIssue(t *testing.T, projectID, agentID, status string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, project_id, title, status, priority, creator_type, creator_id, number, assignee_type, assignee_id)
		VALUES ($1, $2, $3, $4, 'medium', 'member', $5, COALESCE((SELECT MAX(number) FROM issue WHERE workspace_id = $1), 0) + 1, 'agent', $6)
		RETURNING id
	`, testWorkspaceID, projectID, "autostart-issue", status, testUserID, agentID).Scan(&id); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id)
	})
	return id
}

// seedAutoStartAgent creates an agent with an online runtime for dispatch.
func seedAutoStartAgent(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider,
			status, device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, NULL, $2, 'cloud', 'autostart_test', 'online',
		        'test', '{}'::jsonb, now(), 'private', $3)
		RETURNING id
	`, testWorkspaceID, "autostart-rt-"+name, testUserID).Scan(&runtimeID); err != nil {
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
	`, testWorkspaceID, "autostart-agent-"+name, runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID) })
	return agentID
}

// seedAutoStartOfflineAgent creates an agent whose runtime exists but is NOT
// online — the runtime_offline negative fixture (HIV-473: runtime unbound /
// missing must stay separate from offline).
func seedAutoStartOfflineAgent(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider,
			status, device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, NULL, $2, 'cloud', 'autostart_test', 'offline',
		        'test', '{}'::jsonb, now(), 'private', $3)
		RETURNING id
	`, testWorkspaceID, "autostart-rt-offline-"+name, testUserID).Scan(&runtimeID); err != nil {
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
	`, testWorkspaceID, "autostart-agent-offline-"+name, runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("seed offline agent: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID) })
	return agentID
}

// seedAutoStartZeroCapacityAgent creates an agent with max_concurrent_tasks=0.
// Per the canonical claim path (running >= max_concurrent_tasks blocks), 0
// slots is ALWAYS capacity full — never unbounded (HIV-473 item 3).
func seedAutoStartZeroCapacityAgent(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider,
			status, device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, NULL, $2, 'cloud', 'autostart_test', 'online',
		        'test', '{}'::jsonb, now(), 'private', $3)
		RETURNING id
	`, testWorkspaceID, "autostart-rt-zcap-"+name, testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID) })

	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 0, $4)
		RETURNING id
	`, testWorkspaceID, "autostart-agent-zcap-"+name, runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("seed zero-capacity agent: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID) })
	return agentID
}

// seedAutoStartChildIssue creates an issue that is a child of parentID —
// its prerequisite gate depends on its siblings under the same parent.
func seedAutoStartChildIssue(t *testing.T, projectID, parentID, agentID, status string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, project_id, parent_issue_id, title, status, priority, creator_type, creator_id, number, assignee_type, assignee_id)
		VALUES ($1, $2, $3, $4, $5, 'medium', 'member', $6, COALESCE((SELECT MAX(number) FROM issue WHERE workspace_id = $1), 0) + 1, 'agent', $7)
		RETURNING id
	`, testWorkspaceID, projectID, parentID, "autostart-child-"+t.Name(), status, testUserID, agentID).Scan(&id); err != nil {
		t.Fatalf("seed child issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id)
	})
	return id
}

// seedAutoStartArchivedAgent creates an archived agent (for failed employee test).
func seedAutoStartArchivedAgent(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider,
			status, device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, NULL, $2, 'cloud', 'autostart_test_archived', 'online',
		        'test', '{}'::jsonb, now(), 'private', $3)
		RETURNING id
	`, testWorkspaceID, "autostart-rt-archived-"+name, testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID) })

	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id, archived_at
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 1, $4, now())
		RETURNING id
	`, testWorkspaceID, "autostart-agent-archived-"+name, runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("seed archived agent: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID) })
	return agentID
}

// TestProjectStartPreview_NoReadyWork verifies that a project with no
// assigned issues returns an empty ready wave.
func TestProjectStartPreview_NoReadyWork(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	projectID := seedAutoStartProject(t)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+projectID+"/start-preview", nil)
	req = withURLParam(req, "id", projectID)
	testHandler.ProjectStartPreview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result service.ProjectAutoStartPreviewResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.ReadyCount != 0 {
		t.Fatalf("expected 0 ready issues, got %d", result.ReadyCount)
	}
}

// TestProjectStartPreview_WithReadyIssues verifies that assigned, non-terminal
// issues appear in the ready wave.
func TestProjectStartPreview_WithReadyIssues(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	projectID := seedAutoStartProject(t)
	agentID := seedAutoStartAgent(t, "preview-ready")
	seedAutoStartIssue(t, projectID, agentID, "todo")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+projectID+"/start-preview", nil)
	req = withURLParam(req, "id", projectID)
	testHandler.ProjectStartPreview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result service.ProjectAutoStartPreviewResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.ReadyCount != 1 {
		t.Fatalf("expected 1 ready issue, got %d", result.ReadyCount)
	}
}

// TestProjectStart_Idempotency verifies that repeating the same start request
// with the same idempotency_key replays safely without double-enqueueing.
func TestProjectStart_Idempotency(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	projectID := seedAutoStartProject(t)
	agentID := seedAutoStartAgent(t, "idem")
	seedAutoStartIssue(t, projectID, agentID, "todo")

	body := map[string]any{
		"idempotency_key": "idem-key-repeat-click",
	}

	// First call.
	w1 := httptest.NewRecorder()
	req1 := newRequest("POST", "/api/projects/"+projectID+"/start", body)
	req1 = withURLParam(req1, "id", projectID)
	testHandler.ProjectStart(w1, req1)

	if w1.Code != http.StatusAccepted {
		t.Fatalf("first start: expected 202, got %d: %s", w1.Code, w1.Body.String())
	}

	var result1 service.ProjectAutoStartResult
	if err := json.Unmarshal(w1.Body.Bytes(), &result1); err != nil {
		t.Fatalf("unmarshal first: %v", err)
	}

	// Second call with same idempotency key.
	w2 := httptest.NewRecorder()
	req2 := newRequest("POST", "/api/projects/"+projectID+"/start", body)
	req2 = withURLParam(req2, "id", projectID)
	testHandler.ProjectStart(w2, req2)

	if w2.Code != http.StatusAccepted {
		t.Fatalf("second start: expected 202, got %d: %s", w2.Code, w2.Body.String())
	}

	var result2 service.ProjectAutoStartResult
	if err := json.Unmarshal(w2.Body.Bytes(), &result2); err != nil {
		t.Fatalf("unmarshal second: %v", err)
	}

	// The second call should replay or report already_active, not create new tasks.
	if result2.Dispatched > result1.Dispatched {
		t.Fatalf("idempotency violated: first dispatched=%d, second dispatched=%d",
			result1.Dispatched, result2.Dispatched)
	}
}

// TestProjectStart_FailedEmployeeReplacement verifies that an issue assigned
// to an archived agent results in a blocked decision. Since the wave SQL keeps
// assigned non-terminal issues in the row set (the archive gate lives in the
// Go classification), the fixture MUST yield a non-empty blocked result with
// reason agent_archived — a vacuous pass ("nothing was ready, that's fine") is
// a failure (HIV-473: negative fixtures must assert non-empty).
func TestProjectStart_FailedEmployeeReplacement(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	projectID := seedAutoStartProject(t)
	archivedAgentID := seedAutoStartArchivedAgent(t, "blocked")
	seedAutoStartIssue(t, projectID, archivedAgentID, "todo")

	body := map[string]any{
		"idempotency_key": "failed-employee-key",
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+projectID+"/start", body)
	req = withURLParam(req, "id", projectID)
	testHandler.ProjectStart(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var result service.ProjectAutoStartResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if result.Blocked < 1 {
		t.Fatalf("archived agent must yield blocked >= 1, got dispatched=%d blocked=%d",
			result.Dispatched, result.Blocked)
	}
	foundArchived := false
	for _, r := range result.Results {
		if r.Reason == "agent_archived" {
			foundArchived = true
		}
	}
	if !foundArchived {
		t.Fatalf("expected at least one result with reason agent_archived, got %+v", result.Results)
	}
}

// TestProjectStartPreview_OfflineRuntime verifies the runtime_offline negative
// fixture is non-empty: a bound-but-offline runtime must block in preview with
// runtime_offline — never treated as ready on Agent.status/registry alone
// (HIV-473 item 5).
func TestProjectStartPreview_OfflineRuntime(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	projectID := seedAutoStartProject(t)
	agentID := seedAutoStartOfflineAgent(t, "preview-offline")
	seedAutoStartIssue(t, projectID, agentID, "todo")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+projectID+"/start-preview", nil)
	req = withURLParam(req, "id", projectID)
	testHandler.ProjectStartPreview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result service.ProjectAutoStartPreviewResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if result.ReadyCount != 0 {
		t.Fatalf("offline runtime must not be ready, got ready_count=%d %+v", result.ReadyCount, result.Ready)
	}
	if len(result.Blocked) < 1 {
		t.Fatalf("offline runtime fixture must yield non-empty blocked, got %+v", result.Blocked)
	}
	found := false
	for _, b := range result.Blocked {
		if b.Reason == "runtime_offline" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected reason runtime_offline in blocked, got %+v", result.Blocked)
	}
}

// TestProjectStartPreview_ZeroCapacity verifies the zero-capacity negative
// fixture: max_concurrent_tasks=0 is capacity_full per the canonical claim
// comparison (running >= max), never unbounded (HIV-473 item 3).
func TestProjectStartPreview_ZeroCapacity(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	projectID := seedAutoStartProject(t)
	agentID := seedAutoStartZeroCapacityAgent(t, "preview-zcap")
	seedAutoStartIssue(t, projectID, agentID, "todo")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+projectID+"/start-preview", nil)
	req = withURLParam(req, "id", projectID)
	testHandler.ProjectStartPreview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result service.ProjectAutoStartPreviewResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if result.ReadyCount != 0 {
		t.Fatalf("zero-capacity agent must not be ready, got ready_count=%d %+v", result.ReadyCount, result.Ready)
	}
	if len(result.Blocked) < 1 {
		t.Fatalf("zero-capacity fixture must yield non-empty blocked, got %+v", result.Blocked)
	}
	found := false
	for _, b := range result.Blocked {
		if b.Reason == "capacity_full" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected reason capacity_full in blocked, got %+v", result.Blocked)
	}
}

// TestProjectStartPreview_MissingPrerequisite verifies the missing-prerequisite
// negative fixture: a child issue whose sibling under the same parent is not
// done/cancelled stays VISIBLE in the preview as blocked with reason
// missing_prerequisite instead of vanishing from the SQL result set
// (HIV-473 item 4).
func TestProjectStartPreview_MissingPrerequisite(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	projectID := seedAutoStartProject(t)
	agentID := seedAutoStartAgent(t, "preview-prereq")
	parentID := seedAutoStartIssue(t, projectID, agentID, "todo")
	child := seedAutoStartChildIssue(t, projectID, parentID, agentID, "todo")
	seedAutoStartChildIssue(t, projectID, parentID, agentID, "in_progress")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+projectID+"/start-preview", nil)
	req = withURLParam(req, "id", projectID)
	testHandler.ProjectStartPreview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result service.ProjectAutoStartPreviewResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	found := false
	for _, b := range result.Blocked {
		if b.IssueID == child && b.Reason == "missing_prerequisite" {
			found = true
		}
	}
	if !found {
		t.Fatalf("child with unmet prerequisite must surface as blocked missing_prerequisite, got ready=%+v blocked=%+v",
			result.Ready, result.Blocked)
	}
}

// TestProjectStart_DuplicateSelectionFailsClosed verifies that the same issue
// id sent twice in the selection is an explicit fail-closed outcome
// (duplicate_in_batch), never a silent Set-dedup that dispatches once
// (HIV-473 item 1). No task row may be created.
func TestProjectStart_DuplicateSelectionFailsClosed(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	projectID := seedAutoStartProject(t)
	agentID := seedAutoStartAgent(t, "dup-selection")
	issueID := seedAutoStartIssue(t, projectID, agentID, "todo")

	body := map[string]any{
		"idempotency_key": "dup-selection-key",
		"issue_ids":       []string{issueID, issueID},
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+projectID+"/start", body)
	req = withURLParam(req, "id", projectID)
	testHandler.ProjectStart(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var result service.ProjectAutoStartResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if result.Blocked < 1 {
		t.Fatalf("duplicate selection must yield blocked >= 1, got dispatched=%d blocked=%d",
			result.Dispatched, result.Blocked)
	}
	if result.Dispatched != 0 || result.AlreadyActive != 0 {
		t.Fatalf("duplicate selection must never dispatch, got dispatched=%d already_active=%d",
			result.Dispatched, result.AlreadyActive)
	}
	found := false
	for _, r := range result.Results {
		if r.Reason == "duplicate_in_batch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected reason duplicate_in_batch in results, got %+v", result.Results)
	}

	var taskCount int
	if err := testPool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM agent_task_queue WHERE issue_id = $1`, issueID).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if taskCount != 0 {
		t.Fatalf("duplicate selection must create no task row, got %d", taskCount)
	}
}

// TestProjectStart_ReceiptReadback verifies that dispatched issues return
// task IDs in the result.
func TestProjectStart_ReceiptReadback(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	projectID := seedAutoStartProject(t)
	agentID := seedAutoStartAgent(t, "receipt")
	seedAutoStartIssue(t, projectID, agentID, "todo")

	body := map[string]any{
		"idempotency_key": "receipt-readback-key",
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+projectID+"/start", body)
	req = withURLParam(req, "id", projectID)
	testHandler.ProjectStart(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var result service.ProjectAutoStartResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if result.Dispatched < 1 {
		t.Fatalf("expected at least 1 dispatched, got %d (blocked=%d)", result.Dispatched, result.Blocked)
	}

	// At least one result should have task IDs.
	hasTaskIDs := false
	for _, r := range result.Results {
		if len(r.TaskIDs) > 0 {
			hasTaskIDs = true
			break
		}
	}
	if !hasTaskIDs {
		t.Fatal("expected at least one result with task_ids for receipt readback")
	}
}

// TestProjectStart_MissingIdempotencyKey verifies that a start request
// without an idempotency_key returns 400.
func TestProjectStart_MissingIdempotencyKey(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	projectID := seedAutoStartProject(t)

	body := map[string]any{}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+projectID+"/start", body)
	req = withURLParam(req, "id", projectID)
	testHandler.ProjectStart(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestProjectStartPreview_UnavailableService verifies 503 when the service
// is not wired.
func TestProjectStartPreview_UnavailableService(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}

	origSvc := testHandler.ProjectAutoStartService
	testHandler.ProjectAutoStartService = nil
	defer func() { testHandler.ProjectAutoStartService = origSvc }()

	projectID := seedAutoStartProject(t)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+projectID+"/start-preview", nil)
	req = withURLParam(req, "id", projectID)
	testHandler.ProjectStartPreview(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}
