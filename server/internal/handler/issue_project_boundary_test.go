package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestUpdateIssueRejectsCrossWorkspaceProject(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	suffix := uuid.NewString()
	var otherWorkspaceID, foreignProjectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Update cross-workspace project", "update-xwp-"+suffix, "Foreign workspace", "UXP").Scan(&otherWorkspaceID); err != nil {
		t.Fatalf("insert foreign workspace: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, status, priority)
		VALUES ($1, $2, 'planned', 'none')
		RETURNING id
	`, otherWorkspaceID, "Foreign update project").Scan(&foreignProjectID); err != nil {
		t.Fatalf("insert foreign project: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, foreignProjectID)
		testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, otherWorkspaceID)
	})

	createW := httptest.NewRecorder()
	testHandler.CreateIssue(createW, newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "Update project boundary target",
	}))
	if createW.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", createW.Code, createW.Body.String())
	}
	var issue IssueResponse
	if err := json.NewDecoder(createW.Body).Decode(&issue); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issue.ID) })

	updateW := httptest.NewRecorder()
	updateReq := withURLParam(newRequest(http.MethodPut, "/api/issues/"+issue.ID, map[string]any{
		"project_id":   foreignProjectID,
		"suppress_run": true,
	}), "id", issue.ID)
	testHandler.UpdateIssue(updateW, updateReq)
	if updateW.Code != http.StatusBadRequest {
		t.Fatalf("UpdateIssue with foreign project: expected 400, got %d: %s", updateW.Code, updateW.Body.String())
	}
	if !strings.Contains(updateW.Body.String(), "project not found in this workspace") {
		t.Fatalf("UpdateIssue with foreign project: expected boundary message, got %s", updateW.Body.String())
	}

	var storedProjectID *string
	if err := testPool.QueryRow(ctx, `SELECT project_id::text FROM issue WHERE id = $1`, issue.ID).Scan(&storedProjectID); err != nil {
		t.Fatalf("read issue project binding: %v", err)
	}
	if storedProjectID != nil {
		t.Fatalf("rejected update persisted foreign project_id %q", *storedProjectID)
	}

	batchW := httptest.NewRecorder()
	testHandler.BatchUpdateIssues(batchW, newRequest(http.MethodPost, "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{issue.ID},
		"updates": map[string]any{
			"project_id": foreignProjectID,
		},
	}))
	if batchW.Code != http.StatusOK {
		t.Fatalf("BatchUpdateIssues with foreign project: expected 200/updated=0, got %d: %s", batchW.Code, batchW.Body.String())
	}
	var batchResult struct {
		Updated int `json:"updated"`
	}
	if err := json.NewDecoder(batchW.Body).Decode(&batchResult); err != nil {
		t.Fatalf("decode batch update response: %v", err)
	}
	if batchResult.Updated != 0 {
		t.Fatalf("BatchUpdateIssues with foreign project updated %d issues, want 0", batchResult.Updated)
	}
	storedProjectID = nil
	if err := testPool.QueryRow(ctx, `SELECT project_id::text FROM issue WHERE id = $1`, issue.ID).Scan(&storedProjectID); err != nil {
		t.Fatalf("read issue project binding after batch: %v", err)
	}
	if storedProjectID != nil {
		t.Fatalf("rejected batch persisted foreign project_id %q", *storedProjectID)
	}
}
