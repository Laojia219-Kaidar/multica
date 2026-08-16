package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateIssueInvalidStatusReturns400(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":  "invalid status issue",
		"status": "active",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid status, got %d: %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "backlog") {
		t.Errorf("expected error to list valid statuses, got: %s", body)
	}
}

func TestCreateIssueInvalidPriorityReturns400(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":    "invalid priority issue",
		"priority": "P1",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid priority, got %d: %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "urgent") {
		t.Errorf("expected error to list valid priorities, got: %s", body)
	}
}

func TestUpdateIssueInvalidStatusReturns400(t *testing.T) {
	issueID := createTestIssue(t, "update invalid status issue", "todo", "none")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID, map[string]any{"status": "active"})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid status, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdateIssueInvalidPriorityReturns400(t *testing.T) {
	issueID := createTestIssue(t, "update invalid priority issue", "todo", "none")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID, map[string]any{"priority": "P1"})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid priority, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBatchUpdateIssuesInvalidStatusReturns400(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{"not-needed"},
		"updates": map[string]any{
			"status": "active",
		},
	})
	testHandler.BatchUpdateIssues(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid status, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBatchUpdateIssuesInvalidPriorityReturns400(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{"not-needed"},
		"updates": map[string]any{
			"priority": "P1",
		},
	})
	testHandler.BatchUpdateIssues(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid priority, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRepairTaskCannotMutateIssueStatus(t *testing.T) {
	if testPool == nil {
		t.Skip("database unavailable")
	}
	issueID := createTestIssue(t, "repair status guard issue", "in_review", "none")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })
	agentID := createHandlerTestAgent(t, "Repair Status Guard Agent", nil)
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET task_kind = 'repair' WHERE id = $1`, taskID); err != nil {
		t.Fatalf("mark task as repair: %v", err)
	}

	request := func(path string, body any, batch bool) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := newRequest("PUT", path, body)
		if batch {
			req.Method = "POST"
		} else {
			req = withURLParam(req, "id", issueID)
		}
		req.Header.Set("X-Actor-Source", "task_token")
		req.Header.Set("X-Task-ID", taskID)
		if batch {
			testHandler.BatchUpdateIssues(w, req)
		} else {
			testHandler.UpdateIssue(w, req)
		}
		return w
	}

	w := request("/api/issues/"+issueID, map[string]any{"status": "in_progress"}, false)
	if w.Code != http.StatusConflict {
		t.Fatalf("single repair status update: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	w = request("/api/issues/batch-update", map[string]any{
		"issue_ids": []string{issueID},
		"updates":   map[string]any{"status": "blocked"},
	}, true)
	if w.Code != http.StatusConflict {
		t.Fatalf("batch repair status update: expected 409, got %d: %s", w.Code, w.Body.String())
	}

	var status string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil {
		t.Fatalf("read issue status: %v", err)
	}
	if status != "in_review" {
		t.Fatalf("issue status changed to %q, want in_review", status)
	}
}
