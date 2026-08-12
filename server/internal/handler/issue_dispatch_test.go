package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/service"
)

// createDispatchTestSquadIssue creates an issue assigned to the given squad.
func createDispatchTestSquadIssue(t *testing.T, squadID string, status string) string {
	t.Helper()
	ctx := context.Background()
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number, assignee_type, assignee_id)
		VALUES ($1, $2, $3, 'medium', 'member', $4, COALESCE((SELECT MAX(number) FROM issue WHERE workspace_id = $1), 0) + 1, 'squad', $5)
		RETURNING id
	`, testWorkspaceID, "Squad dispatch test issue", status, testUserID, squadID).Scan(&issueID); err != nil {
		t.Fatalf("create squad dispatch test issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
	})
	return issueID
}

// createTestSquad creates a squad with the given agent as leader.
func createTestSquad(t *testing.T, leaderAgentID string, name string) string {
	t.Helper()
	ctx := context.Background()
	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, $2, '', $3, $4)
		RETURNING id
	`, testWorkspaceID, name, leaderAgentID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create test squad: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM squad WHERE id = $1`, squadID)
	})
	return squadID
}

// createDispatchTestIssue creates an issue assigned to the given agent.
func createDispatchTestIssue(t *testing.T, agentID string, status string) string {
	t.Helper()
	ctx := context.Background()
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number, assignee_type, assignee_id)
		VALUES ($1, $2, $3, 'medium', 'member', $4, COALESCE((SELECT MAX(number) FROM issue WHERE workspace_id = $1), 0) + 1, 'agent', $5)
		RETURNING id
	`, testWorkspaceID, "Dispatch test issue", status, testUserID, agentID).Scan(&issueID); err != nil {
		t.Fatalf("create dispatch test issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
	})
	return issueID
}

func TestDispatchPreview_WouldEnqueue(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-preview-agent", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch-preview", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.DispatchPreview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result service.PreviewResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Decision != service.DecisionWouldEnqueue {
		t.Fatalf("expected would_enqueue, got %s (reason=%s)", result.Decision, result.Reason)
	}
	if result.IssueStatus != "todo" {
		t.Fatalf("expected issue_status=todo, got %s", result.IssueStatus)
	}
	if result.Assignee == nil {
		t.Fatal("expected assignee to be populated")
	}
	if !result.Assignee.CanInvoke {
		t.Fatal("expected can_invoke=true for workspace member on public_to agent")
	}
	if cache := w.Header().Get("Cache-Control"); !strings.Contains(cache, "no-store") {
		t.Fatalf("expected Cache-Control: private, no-store; got %q", cache)
	}
}

func TestDispatchPreview_BlockedTerminal(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-preview-terminal", nil)
	issueID := createDispatchTestIssue(t, agentID, "done")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch-preview", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.DispatchPreview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result service.PreviewResult
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.Decision != service.DecisionBlocked {
		t.Fatalf("expected blocked, got %s", result.Decision)
	}
	if result.Reason != service.BlockReasonTerminalStatus {
		t.Fatalf("expected terminal_status, got %s", result.Reason)
	}
}

func TestDispatchPreview_NeedsAssignment(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	ctx := context.Background()
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number)
		VALUES ($1, $2, 'todo', 'medium', 'member', $3, COALESCE((SELECT MAX(number) FROM issue WHERE workspace_id = $1), 0) + 1)
		RETURNING id
	`, testWorkspaceID, "No assignee issue", testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
	})

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch-preview", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.DispatchPreview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result service.PreviewResult
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.Decision != service.DecisionNeedsAssignment {
		t.Fatalf("expected needs_assignment, got %s", result.Decision)
	}
}

func TestDispatchPreview_AlreadyActive(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-preview-active", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	// Seed an active task.
	ctx := context.Background()
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, issue_id, started_at)
		VALUES ($1, $2, 'running', 0, $3, now())
		RETURNING id
	`, agentID, testRuntimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("seed active task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch-preview", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.DispatchPreview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result service.PreviewResult
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.Decision != service.DecisionAlreadyActive {
		t.Fatalf("expected already_active, got %s", result.Decision)
	}
	if len(result.ActiveTasks) != 1 {
		t.Fatalf("expected 1 active task, got %d", len(result.ActiveTasks))
	}
}

func TestDispatch_RejectsUnknownFields(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-unknown-fields", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch", map[string]any{
		"idempotency_key": "test-key",
		"unknown_field":   "should fail",
	})
	req = withURLParam(req, "id", issueID)
	testHandler.Dispatch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDispatch_RequiresIdempotencyKey(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-no-key", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch", map[string]any{
		"expected_status": "todo",
	})
	req = withURLParam(req, "id", issueID)
	testHandler.Dispatch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "idempotency_key") {
		t.Fatalf("expected idempotency_key error, got %s", w.Body.String())
	}
}

func TestDispatch_EnqueueAndReplay(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-enqueue", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	body := map[string]any{
		"idempotency_key": "unique-key-123",
		"expected_status": "todo",
	}

	// First dispatch: should enqueue.
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch", body)
	req = withURLParam(req, "id", issueID)
	testHandler.Dispatch(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("first dispatch: expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var result service.DispatchResult
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.Decision != service.DecisionWouldEnqueue {
		t.Fatalf("expected would_enqueue, got %s (reason=%s)", result.Decision, result.Reason)
	}
	if len(result.TaskIDs) == 0 {
		t.Fatal("expected at least one task ID")
	}
	if result.Replayed {
		t.Fatal("first dispatch should not be replayed")
	}
	firstTaskIDs := result.TaskIDs

	// Clean up the task so second dispatch can also enqueue.
	ctx := context.Background()
	for _, tid := range firstTaskIDs {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, tid)
	}

	// Second dispatch with same key + same body: should replay.
	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/issues/"+issueID+"/dispatch", body)
	req = withURLParam(req, "id", issueID)
	testHandler.Dispatch(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("replay dispatch: expected 202, got %d: %s", w.Code, w.Body.String())
	}

	json.Unmarshal(w.Body.Bytes(), &result)
	if !result.Replayed {
		t.Fatal("second dispatch should be replayed")
	}
	if len(result.TaskIDs) != len(firstTaskIDs) {
		t.Fatalf("replay should return same task IDs; got %v vs %v", result.TaskIDs, firstTaskIDs)
	}

	// Clean up idempotency row.
	testPool.Exec(ctx, `DELETE FROM dispatch_idempotency WHERE workspace_id = $1 AND idempotency_key = $2`,
		testWorkspaceID, "unique-key-123")
}

func TestDispatch_IdempotencyConflict(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-conflict", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")
	ctx := context.Background()

	// Seed an idempotency row with a different digest.
	testPool.Exec(ctx, `
		INSERT INTO dispatch_idempotency (workspace_id, idempotency_key, request_digest, task_ids)
		VALUES ($1, $2, $3, '{}')
	`, testWorkspaceID, "conflict-key", "different-digest")
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM dispatch_idempotency WHERE idempotency_key = $1`, "conflict-key")
	})

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch", map[string]any{
		"idempotency_key": "conflict-key",
		"expected_status": "todo",
	})
	req = withURLParam(req, "id", issueID)
	testHandler.Dispatch(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDispatch_ExpectedStateMismatch(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-state-mismatch", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch", map[string]any{
		"idempotency_key": "state-mismatch-key",
		"expected_status": "in_progress", // issue is actually "todo"
	})
	req = withURLParam(req, "id", issueID)
	testHandler.Dispatch(w, req)

	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDispatchPreview_RejectsUnknownFields(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "preview-unknown-fields", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch-preview", map[string]any{
		"unknown_field": "should fail",
	})
	req = withURLParam(req, "id", issueID)
	testHandler.DispatchPreview(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// assertCacheControl verifies the F3 contract: every dispatch response
// (including error paths) carries Cache-Control: private, no-store.
func assertCacheControl(t *testing.T, w *httptest.ResponseRecorder, label string) {
	t.Helper()
	cc := w.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-store") {
		t.Fatalf("%s: expected Cache-Control: private, no-store; got %q", label, cc)
	}
}

func TestDispatch_CacheControl_On400UnknownField(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-cc-400", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch", map[string]any{
		"idempotency_key": "cc-test",
		"bogus":           true,
	})
	req = withURLParam(req, "id", issueID)
	testHandler.Dispatch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	assertCacheControl(t, w, "dispatch 400 unknown field")
}

func TestDispatch_CacheControl_On400MissingKey(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-cc-nokey", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch", map[string]any{
		"expected_status": "todo",
	})
	req = withURLParam(req, "id", issueID)
	testHandler.Dispatch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	assertCacheControl(t, w, "dispatch 400 missing key")
}

func TestDispatch_CacheControl_On404(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/00000000-0000-0000-0000-000000000099/dispatch", map[string]any{
		"idempotency_key": "cc-404",
	})
	req = withURLParam(req, "id", "00000000-0000-0000-0000-000000000099")
	testHandler.Dispatch(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	assertCacheControl(t, w, "dispatch 404")
}

func TestDispatchPreview_CacheControl_On404(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/00000000-0000-0000-0000-000000000099/dispatch-preview", nil)
	req = withURLParam(req, "id", "00000000-0000-0000-0000-000000000099")
	testHandler.DispatchPreview(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	assertCacheControl(t, w, "preview 404")
}

func TestDispatch_CacheControl_On409Conflict(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-cc-409", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")
	ctx := context.Background()

	testPool.Exec(ctx, `
		INSERT INTO dispatch_idempotency (workspace_id, idempotency_key, request_digest, task_ids)
		VALUES ($1, $2, $3, '{}')
	`, testWorkspaceID, "cc-conflict-key", "different-digest")
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM dispatch_idempotency WHERE idempotency_key = $1`, "cc-conflict-key")
	})

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch", map[string]any{
		"idempotency_key": "cc-conflict-key",
		"expected_status": "todo",
	})
	req = withURLParam(req, "id", issueID)
	testHandler.Dispatch(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	assertCacheControl(t, w, "dispatch 409")
}

func TestDispatch_CacheControl_On412Mismatch(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-cc-412", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch", map[string]any{
		"idempotency_key": "cc-412-key",
		"expected_status": "in_progress",
	})
	req = withURLParam(req, "id", issueID)
	testHandler.Dispatch(w, req)

	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d: %s", w.Code, w.Body.String())
	}
	assertCacheControl(t, w, "dispatch 412")
}

func TestDispatchPreview_CacheControl_OnSuccess(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "preview-cc-200", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch-preview", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.DispatchPreview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	assertCacheControl(t, w, "preview 200")
}

// TestDispatch_RaceConcurrentDifferentKeys fires concurrent dispatches with
// different idempotency keys at the same issue. The F2 contract requires that
// at most one wins the enqueue race and all others either see already_active
// or replay — none may return 500.
func TestDispatch_RaceConcurrentDifferentKeys(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-race-keys", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	const goroutines = 5
	var wg sync.WaitGroup
	results := make([]int, goroutines)
	statuses := make([]int, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			body := map[string]any{
				"idempotency_key": fmt.Sprintf("race-key-%d", idx),
				"expected_status": "todo",
			}
			req := newRequest("POST", "/api/issues/"+issueID+"/dispatch", body)
			req = withURLParam(req, "id", issueID)
			testHandler.Dispatch(w, req)
			statuses[idx] = w.Code

			var res service.DispatchResult
			json.Unmarshal(w.Body.Bytes(), &res)
			switch res.Decision {
			case service.DecisionWouldEnqueue:
				results[idx] = 1
			case service.DecisionAlreadyActive:
				results[idx] = 2
			default:
				results[idx] = 0
			}
		}(i)
	}
	wg.Wait()

	for i, code := range statuses {
		if code == http.StatusInternalServerError {
			t.Fatalf("goroutine %d returned 500 — F2 contract violated", i)
		}
	}

	enqueueCount := 0
	alreadyActiveCount := 0
	for _, r := range results {
		switch r {
		case 1:
			enqueueCount++
		case 2:
			alreadyActiveCount++
		}
	}
	if enqueueCount == 0 {
		t.Fatal("expected at least one enqueue winner")
	}
	if enqueueCount+alreadyActiveCount != goroutines {
		t.Fatalf("unexpected results: enqueue=%d already_active=%d total=%d (expected %d)",
			enqueueCount, alreadyActiveCount, enqueueCount+alreadyActiveCount, goroutines)
	}

	// Clean up tasks and idempotency rows.
	ctx := context.Background()
	for i := 0; i < goroutines; i++ {
		testPool.Exec(ctx, `DELETE FROM dispatch_idempotency WHERE idempotency_key = $1`,
			fmt.Sprintf("race-key-%d", i))
	}
}

// TestDispatch_SquadRaceConcurrentDifferentKeys is the F2 squad-assignee
// regression (HIV-375): concurrent dispatches with different idempotency keys
// against a squad-assigned issue must never return 500. The loser(s) must see
// already_active (via ErrDuplicatePendingTask sentinel or pg constraint), not
// a raw error. Run with -race -count=3.
func TestDispatch_SquadRaceConcurrentDifferentKeys(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	leaderAgentID := createHandlerTestAgent(t, "squad-dispatch-race-leader", nil)
	squadID := createTestSquad(t, leaderAgentID, "Squad Race Test")
	issueID := createDispatchTestSquadIssue(t, squadID, "todo")

	const goroutines = 5
	var wg sync.WaitGroup
	results := make([]int, goroutines)
	statuses := make([]int, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			body := map[string]any{
				"idempotency_key": fmt.Sprintf("squad-race-key-%d", idx),
				"expected_status": "todo",
			}
			req := newRequest("POST", "/api/issues/"+issueID+"/dispatch", body)
			req = withURLParam(req, "id", issueID)
			testHandler.Dispatch(w, req)
			statuses[idx] = w.Code

			var res service.DispatchResult
			json.Unmarshal(w.Body.Bytes(), &res)
			switch res.Decision {
			case service.DecisionWouldEnqueue:
				results[idx] = 1
			case service.DecisionAlreadyActive:
				results[idx] = 2
			default:
				results[idx] = 0
			}
		}(i)
	}
	wg.Wait()

	for i, code := range statuses {
		if code == http.StatusInternalServerError {
			t.Fatalf("goroutine %d returned 500 — F2 squad contract violated (HIV-375)", i)
		}
	}

	enqueueCount := 0
	alreadyActiveCount := 0
	for _, r := range results {
		switch r {
		case 1:
			enqueueCount++
		case 2:
			alreadyActiveCount++
		}
	}
	if enqueueCount == 0 {
		t.Fatal("expected at least one squad enqueue winner")
	}
	if enqueueCount+alreadyActiveCount != goroutines {
		t.Fatalf("squad race: enqueue=%d already_active=%d total=%d (expected %d)",
			enqueueCount, alreadyActiveCount, enqueueCount+alreadyActiveCount, goroutines)
	}

	// Clean up tasks and idempotency rows.
	ctx := context.Background()
	testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	for i := 0; i < goroutines; i++ {
		testPool.Exec(ctx, `DELETE FROM dispatch_idempotency WHERE idempotency_key = $1`,
			fmt.Sprintf("squad-race-key-%d", i))
	}
}

// TestDispatch_CacheControl_On401 verifies the F3 contract: a 401 response
// (missing auth) still carries Cache-Control: private, no-store.
func TestDispatch_CacheControl_On401(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/issues/00000000-0000-0000-0000-000000000099/dispatch", nil)
	req.Header.Set("Content-Type", "application/json")
	// No X-User-ID → 401.
	req = withURLParam(req, "id", "00000000-0000-0000-0000-000000000099")
	testHandler.Dispatch(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	assertCacheControl(t, w, "dispatch 401")
}

// TestDispatch_CacheControl_On403 verifies the F3 contract: a 403 response
// (non-owner/admin) still carries Cache-Control: private, no-store.
func TestDispatch_CacheControl_On403(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-cc-403", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	ctx := context.Background()
	var nonOwnerUserID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ('Non-Owner Dispatch Test', 'nonowner-dispatch-test@multica.ai')
		ON CONFLICT (email) DO UPDATE SET name = EXCLUDED.name
		RETURNING id
	`).Scan(&nonOwnerUserID); err != nil {
		t.Fatalf("create non-owner user: %v", err)
	}
	t.Cleanup(func() {
		if _, err := testPool.Exec(ctx, `DELETE FROM "user" WHERE email = 'nonowner-dispatch-test@multica.ai'`); err != nil {
			t.Logf("cleanup user: %v", err)
		}
	})

	// Insert into the real member table (not workspace_member) so that
	// GetMemberByUserAndWorkspace finds the row and requireWorkspaceRole
	// can evaluate the role → 'member' is not in ['owner','admin'] → 403.
	var memberID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'member')
		ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = 'member'
		RETURNING id
	`, testWorkspaceID, nonOwnerUserID).Scan(&memberID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	t.Cleanup(func() {
		if _, err := testPool.Exec(ctx, `DELETE FROM member WHERE id = $1`, memberID); err != nil {
			t.Logf("cleanup member: %v", err)
		}
	})

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch", map[string]any{
		"idempotency_key": "cc-403-test",
	})
	req.Header.Set("X-User-ID", nonOwnerUserID)
	req = withURLParam(req, "id", issueID)
	testHandler.Dispatch(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
	assertCacheControl(t, w, "dispatch 403")
}

// TestDispatchPreview_CacheControl_On401 verifies F3 on the preview endpoint.
func TestDispatchPreview_CacheControl_On401(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/issues/00000000-0000-0000-0000-000000000099/dispatch-preview", nil)
	req.Header.Set("Content-Type", "application/json")
	req = withURLParam(req, "id", "00000000-0000-0000-0000-000000000099")
	testHandler.DispatchPreview(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	assertCacheControl(t, w, "preview 401")
}

// dispatchFailingTxStarter is a test double for service.TxStarter.
type dispatchFailingTxStarter struct {
	beginErr  error
	commitErr error
}

func (f *dispatchFailingTxStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	return &dispatchFailingTxWithCommit{commitErr: f.commitErr}, nil
}

type dispatchFailingTxWithCommit struct {
	pgx.Tx
	commitErr error
}

func (t *dispatchFailingTxWithCommit) Commit(ctx context.Context) error {
	return t.commitErr
}

func (t *dispatchFailingTxWithCommit) Rollback(ctx context.Context) error {
	return nil
}

// withFailingTxStarter swaps the handler's OwnerDispatchService.TxStarter
// with a failing one and restores it on cleanup.
func withFailingTxStarter(t *testing.T, beginErr, commitErr error) {
	t.Helper()
	orig := testHandler.OwnerDispatchService
	svc := &service.OwnerDispatchService{
		Queries:     orig.Queries,
		TxStarter:   &dispatchFailingTxStarter{beginErr: beginErr, commitErr: commitErr},
		TaskService: orig.TaskService,
	}
	testHandler.OwnerDispatchService = svc
	t.Cleanup(func() { testHandler.OwnerDispatchService = orig })
}

// TestDispatch_BeginFailure_HTTP500 injects a failing TxStarter (Begin error)
// and verifies: HTTP 500, zero writes to agent_task_queue and
// dispatch_idempotency, Cache-Control: private, no-store.
func TestDispatch_BeginFailure_HTTP500(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-begin-fail", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	withFailingTxStarter(t, errors.New("connection pool exhausted"), nil)

	idemKey := "begin-fail-key"
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch", map[string]any{
		"idempotency_key": idemKey,
		"expected_status": "todo",
	})
	req = withURLParam(req, "id", issueID)
	testHandler.Dispatch(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	assertCacheControl(t, w, "dispatch 500 begin failure")

	ctx := context.Background()
	var queueCount int
	if err := testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM agent_task_queue WHERE issue_id = $1`, issueID,
	).Scan(&queueCount); err != nil {
		t.Fatalf("count agent_task_queue: %v", err)
	}
	if queueCount != 0 {
		t.Fatalf("expected 0 agent_task_queue rows, got %d", queueCount)
	}

	var idemCount int
	if err := testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM dispatch_idempotency WHERE workspace_id = $1 AND idempotency_key = $2`,
		testWorkspaceID, idemKey,
	).Scan(&idemCount); err != nil {
		t.Fatalf("count dispatch_idempotency: %v", err)
	}
	if idemCount != 0 {
		t.Fatalf("expected 0 dispatch_idempotency rows, got %d", idemCount)
	}
}

// TestDispatch_CommitFailure_HTTP500 injects a TxStarter that succeeds on
// Begin but fails on Commit. Verifies: HTTP 500, zero durable writes to
// agent_task_queue and dispatch_idempotency (rollback holds), Cache-Control.
func TestDispatch_CommitFailure_HTTP500(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-commit-fail", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	withFailingTxStarter(t, nil, errors.New("commit failed: disk full"))

	idemKey := "commit-fail-key"
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch", map[string]any{
		"idempotency_key": idemKey,
		"expected_status": "todo",
	})
	req = withURLParam(req, "id", issueID)
	testHandler.Dispatch(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	assertCacheControl(t, w, "dispatch 500 commit failure")

	ctx := context.Background()
	var queueCount int
	if err := testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM agent_task_queue WHERE issue_id = $1`, issueID,
	).Scan(&queueCount); err != nil {
		t.Fatalf("count agent_task_queue: %v", err)
	}
	if queueCount != 0 {
		t.Fatalf("expected 0 agent_task_queue rows after rollback, got %d", queueCount)
	}

	var idemCount int
	if err := testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM dispatch_idempotency WHERE workspace_id = $1 AND idempotency_key = $2`,
		testWorkspaceID, idemKey,
	).Scan(&idemCount); err != nil {
		t.Fatalf("count dispatch_idempotency: %v", err)
	}
	if idemCount != 0 {
		t.Fatalf("expected 0 dispatch_idempotency rows after rollback, got %d", idemCount)
	}
}

// TestDispatch_CacheControl_On503 verifies the F3 contract: when the dispatch
// service is nil (misconfigured server), the 503 response carries
// Cache-Control: private, no-store.
func TestDispatch_CacheControl_On503(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-cc-503", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	orig := testHandler.OwnerDispatchService
	testHandler.OwnerDispatchService = nil
	t.Cleanup(func() { testHandler.OwnerDispatchService = orig })

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch", map[string]any{
		"idempotency_key": "cc-503-test",
	})
	req = withURLParam(req, "id", issueID)
	testHandler.Dispatch(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
	assertCacheControl(t, w, "dispatch 503")
}

// TestDispatchPreview_CacheControl_On403 verifies the F3 contract on the
// preview endpoint: a non-owner/admin member gets 403 with no-store.
func TestDispatchPreview_CacheControl_On403(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "preview-cc-403", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	ctx := context.Background()
	var nonOwnerUserID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ('Preview-403 Test', 'preview-403-test@multica.ai')
		ON CONFLICT (email) DO UPDATE SET name = EXCLUDED.name
		RETURNING id
	`).Scan(&nonOwnerUserID); err != nil {
		t.Fatalf("create non-owner user: %v", err)
	}
	t.Cleanup(func() {
		if _, err := testPool.Exec(ctx, `DELETE FROM "user" WHERE email = 'preview-403-test@multica.ai'`); err != nil {
			t.Logf("cleanup user: %v", err)
		}
	})

	var memberID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'member')
		ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = 'member'
		RETURNING id
	`, testWorkspaceID, nonOwnerUserID).Scan(&memberID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	t.Cleanup(func() {
		if _, err := testPool.Exec(ctx, `DELETE FROM member WHERE id = $1`, memberID); err != nil {
			t.Logf("cleanup member: %v", err)
		}
	})

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch-preview", nil)
	req.Header.Set("X-User-ID", nonOwnerUserID)
	req = withURLParam(req, "id", issueID)
	testHandler.DispatchPreview(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
	assertCacheControl(t, w, "preview 403")
}

// TestDispatchPreview_CacheControl_On503 verifies F3 when the dispatch
// service is nil → 503.
func TestDispatchPreview_CacheControl_On503(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "preview-cc-503", nil)
	issueID := createDispatchTestIssue(t, agentID, "todo")

	orig := testHandler.OwnerDispatchService
	testHandler.OwnerDispatchService = nil
	t.Cleanup(func() { testHandler.OwnerDispatchService = orig })

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/dispatch-preview", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.DispatchPreview(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
	assertCacheControl(t, w, "preview 503")
}
