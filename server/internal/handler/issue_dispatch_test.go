package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

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

func TestLegacyDispatchReceiptUsesResolvedSquadLeaderAcrossOutcomes(t *testing.T) {
	issue := db.Issue{
		ID:           util.MustParseUUID("00000000-0000-0000-0000-000000000001"),
		AssigneeType: pgtype.Text{String: "squad", Valid: true},
		AssigneeID:   util.MustParseUUID("00000000-0000-0000-0000-000000000002"),
	}
	leaderID := "00000000-0000-0000-0000-000000000003"
	for _, tc := range []struct {
		name           string
		result         *service.DispatchResult
		alreadyPending bool
	}{
		{name: "fresh", result: &service.DispatchResult{Decision: service.DecisionWouldEnqueue, TaskIDs: []string{"task-1"}, TargetAgentID: leaderID, AssigneeType: "squad"}},
		{name: "replay", result: &service.DispatchResult{Decision: service.DecisionWouldEnqueue, TaskIDs: []string{"task-1"}, Replayed: true, TargetAgentID: leaderID, AssigneeType: "squad"}, alreadyPending: true},
		{name: "already-active", result: &service.DispatchResult{Decision: service.DecisionAlreadyActive, TaskIDs: []string{"task-1"}, TargetAgentID: leaderID, AssigneeType: "squad"}, alreadyPending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt := legacyDispatchReceipt(issue, "workspace-1", "member", "member-1", "key-1", tc.result)
			if receipt.TargetAgentID != leaderID {
				t.Fatalf("target_agent_id=%q, want resolved squad leader %q", receipt.TargetAgentID, leaderID)
			}
			if receipt.AlreadyPending != tc.alreadyPending {
				t.Fatalf("already_pending=%v, want %v", receipt.AlreadyPending, tc.alreadyPending)
			}
		})
	}
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

	if w.Code != http.StatusOK {
		t.Fatalf("replay dispatch: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var replayResponse struct {
		service.DispatchResult
		Receipt IssueDispatchReceipt `json:"receipt"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &replayResponse); err != nil {
		t.Fatalf("decode replay dispatch: %v", err)
	}
	result = replayResponse.DispatchResult
	if !result.Replayed {
		t.Fatal("second dispatch should be replayed")
	}
	if len(result.TaskIDs) != len(firstTaskIDs) {
		t.Fatalf("replay should return same task IDs; got %v vs %v", result.TaskIDs, firstTaskIDs)
	}
	for i := range firstTaskIDs {
		if result.TaskIDs[i] != firstTaskIDs[i] {
			t.Fatalf("replay task ID at index %d = %q, want %q", i, result.TaskIDs[i], firstTaskIDs[i])
		}
	}
	if replayResponse.Receipt.TargetAgentID != "" || replayResponse.Receipt.AssigneeType != "" {
		t.Fatalf("replay after task cleanup fabricated authority: receipt=%#v", replayResponse.Receipt)
	}

	// Clean up idempotency row.
	testPool.Exec(ctx, `DELETE FROM dispatch_idempotency WHERE workspace_id = $1 AND idempotency_key = $2`,
		testWorkspaceID, "unique-key-123")
}

func TestDispatch_IdempotencyDigestScopesIssueAndReceiptAuthority(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentID := createHandlerTestAgent(t, "dispatch-cross-issue-agent", nil)
	issueA := createDispatchTestIssue(t, agentID, "todo")
	issueB := createDispatchTestIssue(t, agentID, "todo")
	key := "cross-issue-key-" + strings.ReplaceAll(t.Name(), "/", "-")
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM dispatch_idempotency WHERE workspace_id = $1 AND idempotency_key = $2`, testWorkspaceID, key)
	})
	body := map[string]any{"idempotency_key": key, "expected_status": "todo"}

	dispatch := func(issueID string, requestBody map[string]any) (int, service.DispatchResult, IssueDispatchReceipt) {
		w := httptest.NewRecorder()
		r := withURLParam(newRequest("POST", "/api/issues/"+issueID+"/dispatch", requestBody), "id", issueID)
		testHandler.Dispatch(w, r)
		var response struct {
			service.DispatchResult
			Receipt IssueDispatchReceipt `json:"receipt"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode dispatch response (%d): %v; body=%s", w.Code, err, w.Body.String())
		}
		return w.Code, response.DispatchResult, response.Receipt
	}

	statusA, first, firstReceipt := dispatch(issueA, body)
	if statusA != http.StatusAccepted || first.Decision != service.DecisionWouldEnqueue || first.Replayed {
		t.Fatalf("Issue A first dispatch status=%d result=%#v, want fresh 202", statusA, first)
	}
	if firstReceipt.TargetAgentID != agentID {
		t.Fatalf("fresh receipt target=%q, want actual task agent %q", firstReceipt.TargetAgentID, agentID)
	}

	statusB, crossIssue, _ := dispatch(issueB, body)
	if statusB != http.StatusConflict || crossIssue.Reason != service.BlockReasonIdempotencyConflict {
		t.Fatalf("Issue B reused key status=%d result=%#v, want stable 409 idempotency conflict", statusB, crossIssue)
	}
	var taskCountA, taskCountB int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, issueA).Scan(&taskCountA); err != nil {
		t.Fatalf("count Issue A tasks: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, issueB).Scan(&taskCountB); err != nil {
		t.Fatalf("count Issue B tasks: %v", err)
	}
	if taskCountA != 1 || taskCountB != 0 {
		t.Fatalf("cross-issue task counts A=%d B=%d, want 1/0", taskCountA, taskCountB)
	}

	statusReplay, replay, replayReceipt := dispatch(issueA, body)
	if statusReplay != http.StatusOK || !replay.Replayed || replayReceipt.TargetAgentID != agentID {
		t.Fatalf("same-issue replay status=%d result=%#v receipt=%#v, want 200/replay/original agent", statusReplay, replay, replayReceipt)
	}
	changedBody := map[string]any{"idempotency_key": key, "expected_status": "todo", "handoff_note": "different request"}
	statusChanged, changed, _ := dispatch(issueA, changedBody)
	if statusChanged != http.StatusConflict || changed.Reason != service.BlockReasonIdempotencyConflict {
		t.Fatalf("same-issue different body status=%d result=%#v, want 409 idempotency conflict", statusChanged, changed)
	}
}

func TestDispatch_ReplayReceiptUsesOriginalTaskAfterIssueReassignment(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	agentA := createHandlerTestAgent(t, "dispatch-reassignment-original", nil)
	agentB := createHandlerTestAgent(t, "dispatch-reassignment-new", nil)
	issueID := createDispatchTestIssue(t, agentA, "todo")
	key := "reassignment-replay-key-" + strings.ReplaceAll(t.Name(), "/", "-")
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM dispatch_idempotency WHERE workspace_id = $1 AND idempotency_key = $2`, testWorkspaceID, key)
	})
	body := map[string]any{"idempotency_key": key, "expected_status": "todo"}

	dispatch := func() (int, service.DispatchResult, IssueDispatchReceipt) {
		w := httptest.NewRecorder()
		r := withURLParam(newRequest("POST", "/api/issues/"+issueID+"/dispatch", body), "id", issueID)
		testHandler.Dispatch(w, r)
		var response struct {
			service.DispatchResult
			Receipt IssueDispatchReceipt `json:"receipt"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode reassignment response (%d): %v", w.Code, err)
		}
		return w.Code, response.DispatchResult, response.Receipt
	}

	firstStatus, first, firstReceipt := dispatch()
	if firstStatus != http.StatusAccepted || first.Replayed || firstReceipt.TargetAgentID != agentA || firstReceipt.TaskID == nil {
		t.Fatalf("first dispatch status=%d result=%#v receipt=%#v", firstStatus, first, firstReceipt)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET assignee_type = 'agent', assignee_id = $2 WHERE id = $1`, issueID, agentB); err != nil {
		t.Fatalf("reassign issue between dispatches: %v", err)
	}

	replayStatus, replay, replayReceipt := dispatch()
	if replayStatus != http.StatusOK || !replay.Replayed {
		t.Fatalf("replay status=%d result=%#v, want 200 replay", replayStatus, replay)
	}
	if replayReceipt.TargetAgentID != agentA {
		t.Fatalf("replay receipt target=%q, want original task agent %q after reassignment", replayReceipt.TargetAgentID, agentA)
	}
	if replayReceipt.TaskID == nil || *replayReceipt.TaskID != *firstReceipt.TaskID {
		t.Fatalf("replay receipt task=%v, want original task %v", replayReceipt.TaskID, firstReceipt.TaskID)
	}
}

func TestDispatch_SquadReceiptUsesLeaderForFreshReplayAndActive(t *testing.T) {
	if testHandler == nil {
		t.Skip("test handler not initialized")
	}
	ctx := context.Background()
	leaderID := createHandlerTestAgent(t, "dispatch-squad-leader", nil)
	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, $2, '', $3, $4)
		RETURNING id
	`, testWorkspaceID, "dispatch-squad-"+strings.ReplaceAll(t.Name(), "/", "-"), leaderID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create dispatch squad: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID) })

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number, assignee_type, assignee_id)
		VALUES ($1, 'Dispatch squad authority', 'todo', 'medium', 'member', $2,
		        COALESCE((SELECT MAX(number) FROM issue WHERE workspace_id = $1), 0) + 1,
		        'squad', $3)
		RETURNING id
	`, testWorkspaceID, testUserID, squadID).Scan(&issueID); err != nil {
		t.Fatalf("create squad issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

	dispatch := func(key string) (int, service.DispatchResult, IssueDispatchReceipt) {
		w := httptest.NewRecorder()
		r := withURLParam(newRequest("POST", "/api/issues/"+issueID+"/dispatch", map[string]any{
			"idempotency_key": key,
			"expected_status": "todo",
		}), "id", issueID)
		testHandler.Dispatch(w, r)
		var response struct {
			service.DispatchResult
			Receipt IssueDispatchReceipt `json:"receipt"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode squad dispatch response (%d): %v", w.Code, err)
		}
		return w.Code, response.DispatchResult, response.Receipt
	}

	freshStatus, fresh, freshReceipt := dispatch("squad-fresh-key")
	if freshStatus != http.StatusAccepted || fresh.Decision != service.DecisionWouldEnqueue || fresh.Replayed {
		t.Fatalf("squad fresh status=%d result=%#v", freshStatus, fresh)
	}
	if freshReceipt.TargetAgentID != leaderID || freshReceipt.AssigneeType != "squad" {
		t.Fatalf("squad fresh receipt=%#v, want leader=%s type=squad", freshReceipt, leaderID)
	}

	replayStatus, replay, replayReceipt := dispatch("squad-fresh-key")
	if replayStatus != http.StatusOK || !replay.Replayed || replayReceipt.TargetAgentID != leaderID || replayReceipt.AssigneeType != "squad" {
		t.Fatalf("squad replay status=%d result=%#v receipt=%#v", replayStatus, replay, replayReceipt)
	}

	activeStatus, active, activeReceipt := dispatch("squad-active-key")
	if activeStatus != http.StatusOK || active.Decision != service.DecisionAlreadyActive || activeReceipt.TargetAgentID != leaderID || activeReceipt.AssigneeType != "squad" {
		t.Fatalf("squad active status=%d result=%#v receipt=%#v", activeStatus, active, activeReceipt)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM dispatch_idempotency WHERE workspace_id = $1 AND idempotency_key IN ('squad-fresh-key', 'squad-active-key')`, testWorkspaceID)
	})
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
