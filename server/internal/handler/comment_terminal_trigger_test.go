package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// setIssueStatus updates an issue's status directly for test setup.
func setIssueStatus(t *testing.T, issueID, status string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET status = $1 WHERE id = $2`, status, issueID,
	); err != nil {
		t.Fatalf("set issue status: %v", err)
	}
}

// TestTerminalIssue_PlainCommentDoesNotTriggerAssignee verifies that plain
// member comments on done/cancelled issues do not enqueue implicit assignee
// runs. This is the core HIV-803 fix: terminal issue states must not use the
// implicit assignee fallback.
func TestTerminalIssue_PlainCommentDoesNotTriggerAssignee(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	assigneeID := createHandlerTestAgent(t, "Terminal Assignee", nil)

	for _, status := range []string{"done", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			issueID := createCommentTriggerPreviewIssue(
				t, "terminal plain comment "+status, "agent", assigneeID,
			)
			setIssueStatus(t, issueID, status)

			// Preview: no agents should be triggered.
			preview := previewCommentTriggersForTest(t, issueID, map[string]any{
				"content": "Adding a note after completion",
			})
			if len(preview.Agents) != 0 {
				t.Fatalf("preview agents = %+v, want 0 for status %s", preview.Agents, status)
			}

			// Create: no tasks should be enqueued.
			postCommentForTriggerPreviewTest(t, issueID, map[string]any{
				"content": "Adding another note after completion",
			})
			if got := countQueuedCommentTriggerTasks(t, issueID, assigneeID); got != 0 {
				t.Fatalf("queued assignee tasks = %d, want 0 for status %s", got, status)
			}
		})
	}
}

// TestTerminalIssue_ExplicitMentionStillTriggers verifies that explicit
// @agent mentions still enqueue runs even on done/cancelled issues. The
// terminal guard must only suppress the implicit assignee fallback, not
// explicit user intent.
func TestTerminalIssue_ExplicitMentionStillTriggers(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	assigneeID := createHandlerTestAgent(t, "Terminal Mention Assignee", nil)
	otherAgentID := createHandlerTestAgent(t, "Terminal Mention Target", nil)

	for _, status := range []string{"done", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			issueID := createCommentTriggerPreviewIssue(
				t, "terminal explicit mention "+status, "agent", assigneeID,
			)
			setIssueStatus(t, issueID, status)

			content := fmt.Sprintf("[@Target](mention://agent/%s) please review", otherAgentID)

			// Preview: the explicitly mentioned agent should appear.
			preview := previewCommentTriggersForTest(t, issueID, map[string]any{
				"content": content,
			})
			requirePreviewAgents(t, preview, otherAgentID)

			// Create: the mentioned agent should be enqueued.
			postCommentForTriggerPreviewTest(t, issueID, map[string]any{
				"content": content,
			})
			if got := countQueuedCommentTriggerTasks(t, issueID, otherAgentID); got != 1 {
				t.Fatalf("queued mentioned agent tasks = %d, want 1 for status %s", got, status)
			}
			// The implicit assignee should NOT be enqueued (terminal guard).
			if got := countQueuedCommentTriggerTasks(t, issueID, assigneeID); got != 0 {
				t.Fatalf("queued assignee tasks = %d, want 0 for status %s", got, status)
			}
		})
	}
}

// TestNonTerminalIssue_PlainCommentStillTriggersAssignee verifies that the
// fix does not break the normal in_progress path: plain member comments on
// active issues still enqueue the assignee via the implicit fallback.
func TestNonTerminalIssue_PlainCommentStillTriggersAssignee(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	assigneeID := createHandlerTestAgent(t, "Active Assignee", nil)

	for _, status := range []string{"in_progress", "todo", "in_review"} {
		t.Run(status, func(t *testing.T) {
			issueID := createCommentTriggerPreviewIssue(
				t, "active plain comment "+status, "agent", assigneeID,
			)
			setIssueStatus(t, issueID, status)

			// Preview: assignee should be triggered.
			preview := previewCommentTriggersForTest(t, issueID, map[string]any{
				"content": "Following up on this issue",
			})
			requirePreviewAgents(t, preview, assigneeID)
			if preview.Agents[0].Source != string(commentTriggerSourceIssueAssignee) {
				t.Fatalf("source = %q, want %q", preview.Agents[0].Source, commentTriggerSourceIssueAssignee)
			}

			// Create: assignee should be enqueued.
			postCommentForTriggerPreviewTest(t, issueID, map[string]any{
				"content": "Another follow-up",
			})
			if got := countQueuedCommentTriggerTasks(t, issueID, assigneeID); got != 1 {
				t.Fatalf("queued assignee tasks = %d, want 1 for status %s", got, status)
			}
		})
	}
}

// TestAssigneeFallback_OrdinaryAgentSelfCommentDoesNotTrigger verifies that
// an ordinary agent's plain self-comment never re-enters the implicit
// assignee route. This is intentionally tested at the fallback boundary so
// every caller gets the guard, while explicit mention routing remains separate.
func TestAssigneeFallback_OrdinaryAgentSelfCommentDoesNotTrigger(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	assigneeID := createHandlerTestAgent(t, "Ordinary Self Comment Assignee", nil)
	for _, status := range []string{"backlog", "todo", "in_progress", "in_review", "blocked"} {
		t.Run(status, func(t *testing.T) {
			issueID := createCommentTriggerPreviewIssue(t, "ordinary self comment "+status, "agent", assigneeID)
			setIssueStatus(t, issueID, status)
			taskID := createHandlerTestTaskForAgentOnIssue(t, assigneeID, issueID)

			issue, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID))
			if err != nil {
				t.Fatalf("load issue: %v", err)
			}
			if trigger, ok := testHandler.routeAssigneeFallback(ctx, issue, "agent", assigneeID, commentTriggerComputeOptions{}); ok {
				t.Fatalf("self-comment fallback returned trigger %+v for status %s", trigger, status)
			}

			w := httptest.NewRecorder()
			r := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{
				"content": "plain delivery summary without an explicit mention",
			}), "id", issueID)
			r.Header.Set("X-Agent-ID", assigneeID)
			r.Header.Set("X-Task-ID", taskID)
			testHandler.CreateComment(w, r)
			if w.Code != http.StatusCreated {
				t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
			}
			if got := countQueuedCommentTriggerTasks(t, issueID, assigneeID); got != 0 {
				t.Fatalf("plain assignee self-comment queued %d task(s), want 0 for status %s", got, status)
			}
		})
	}
}
