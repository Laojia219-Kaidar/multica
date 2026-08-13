package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ownerDispatchFixture creates a public runtime + agent owned by testUserID and
// an agent-assigned todo issue, then returns (agentID, issueID). The agent is
// owned by the acting member, so the MUL-4525 invoke gate admits the dispatch.
func ownerDispatchFixture(t *testing.T) (string, string) {
	t.Helper()
	ctx := context.Background()

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at)
		VALUES ($1, NULL, 'owner dispatch runtime', 'cloud', 'lane_a_test', 'online', 'test', '{}'::jsonb, now())
		RETURNING id
	`, testWorkspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})

	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, runtime_id, visibility, max_concurrent_tasks, owner_id, instructions, custom_env, custom_args)
		VALUES ($1, 'owner-dispatch-agent', '', 'cloud', '{}'::jsonb, $2, 'workspace', 1, $3, '', '{}'::jsonb, '[]'::jsonb)
		RETURNING id
	`, testWorkspaceID, runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID)
	})

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number, assignee_type, assignee_id)
		VALUES ($1, 'owner dispatch issue', 'todo', 'medium', 'member', $2,
		        COALESCE((SELECT MAX(number) FROM issue WHERE workspace_id = $1), 0) + 1,
		        'agent', $3)
		RETURNING id
	`, testWorkspaceID, testUserID, agentID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	return agentID, issueID
}

func TestIssueDispatchPreviewThenExecuteThenIdempotent(t *testing.T) {
	agentID, issueID := ownerDispatchFixture(t)

	// Preview: dispatchable, no pending task yet.
	pw := httptest.NewRecorder()
	preq := newRequest("POST", "/api/issues/"+issueID+"/dispatch-preview", nil)
	preq = withURLParam(preq, "id", issueID)
	testHandler.PreviewIssueDispatch(pw, preq)
	if pw.Code != http.StatusOK {
		t.Fatalf("dispatch-preview: got %d: %s", pw.Code, pw.Body.String())
	}
	var previewBody struct {
		Preview IssueDispatchPreview `json:"preview"`
	}
	if err := json.NewDecoder(pw.Body).Decode(&previewBody); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if !previewBody.Preview.Dispatchable {
		t.Fatalf("preview dispatchable=false, want true (reason=%q)", previewBody.Preview.Reason)
	}
	if previewBody.Preview.TargetAgentID != agentID {
		t.Fatalf("preview target agent = %q, want %q", previewBody.Preview.TargetAgentID, agentID)
	}
	if previewBody.Preview.AlreadyPending {
		t.Fatalf("preview already_pending=true before any dispatch")
	}

	// Execute: enqueue a run.
	ew := httptest.NewRecorder()
	ereq := newRequest("POST", "/api/issues/"+issueID+"/dispatch", map[string]any{"idempotency_key": "d1"})
	ereq = withURLParam(ereq, "id", issueID)
	testHandler.DispatchIssue(ew, ereq)
	if ew.Code != http.StatusCreated {
		t.Fatalf("dispatch: got %d: %s", ew.Code, ew.Body.String())
	}
	var execBody struct {
		Receipt IssueDispatchReceipt `json:"receipt"`
	}
	if err := json.NewDecoder(ew.Body).Decode(&execBody); err != nil {
		t.Fatalf("decode dispatch: %v", err)
	}
	if execBody.Receipt.TaskID == nil || *execBody.Receipt.TaskID == "" {
		t.Fatalf("dispatch receipt has no task_id")
	}
	if execBody.Receipt.AlreadyPending {
		t.Fatalf("dispatch receipt already_pending=true on first dispatch")
	}
	if execBody.Receipt.TargetAgentID != agentID {
		t.Fatalf("dispatch receipt target agent = %q, want %q", execBody.Receipt.TargetAgentID, agentID)
	}

	// Execute again: idempotent already-pending receipt (no new task).
	ew2 := httptest.NewRecorder()
	ereq2 := newRequest("POST", "/api/issues/"+issueID+"/dispatch", map[string]any{"idempotency_key": "d2"})
	ereq2 = withURLParam(ereq2, "id", issueID)
	testHandler.DispatchIssue(ew2, ereq2)
	if ew2.Code != http.StatusOK {
		t.Fatalf("dispatch (2nd): got %d: %s", ew2.Code, ew2.Body.String())
	}
	var execBody2 struct {
		Receipt IssueDispatchReceipt `json:"receipt"`
	}
	if err := json.NewDecoder(ew2.Body).Decode(&execBody2); err != nil {
		t.Fatalf("decode dispatch 2: %v", err)
	}
	if !execBody2.Receipt.AlreadyPending {
		t.Fatalf("2nd dispatch receipt already_pending=false, want true")
	}

	// Stop cancels the active task and reports its id.
	sw := httptest.NewRecorder()
	sreq := newRequest("POST", "/api/issues/"+issueID+"/stop", nil)
	sreq = withURLParam(sreq, "id", issueID)
	testHandler.StopIssue(sw, sreq)
	if sw.Code != http.StatusOK {
		t.Fatalf("stop: got %d: %s", sw.Code, sw.Body.String())
	}
	var stopBody struct {
		Receipt IssueStopReceipt `json:"receipt"`
	}
	if err := json.NewDecoder(sw.Body).Decode(&stopBody); err != nil {
		t.Fatalf("decode stop: %v", err)
	}
	if len(stopBody.Receipt.Cancelled) != 1 {
		t.Fatalf("stop cancelled %d task(s), want 1: %v", len(stopBody.Receipt.Cancelled), stopBody.Receipt.Cancelled)
	}
}

func TestIssueSendToReview(t *testing.T) {
	_, issueID := ownerDispatchFixture(t)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/send-to-review", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.SendIssueToReview(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("send-to-review: got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Receipt IssueReviewReceipt `json:"receipt"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode send-to-review: %v", err)
	}
	if body.Receipt.FromStatus != "todo" || body.Receipt.ToStatus != "in_review" {
		t.Fatalf("receipt %s->%s, want todo->in_review", body.Receipt.FromStatus, body.Receipt.ToStatus)
	}
}
