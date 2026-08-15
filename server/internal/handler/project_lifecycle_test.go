package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Contract: GET /api/projects/lifecycle returns a portfolio envelope with the
// derived health projection and never mutates project.status.
func TestListProjectLifecycleReturnsPortfolio(t *testing.T) {
	// Seed a project with no lead and no tasks: it must surface as
	// owner_decision_required + (stalled | source_gap), never "active".
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects?workspace_id="+testWorkspaceID, map[string]any{
		"title":  "lifecycle portfolio seed",
		"status": "in_progress",
	})
	testHandler.CreateProject(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("seed CreateProject: %d %s", w.Code, w.Body.String())
	}
	var project ProjectResponse
	if err := json.NewDecoder(w.Body).Decode(&project); err != nil {
		t.Fatalf("decode CreateProject: %v", err)
	}
	t.Cleanup(func() {
		req := newRequest("DELETE", "/api/projects/"+project.ID, nil)
		req = withURLParam(req, "id", project.ID)
		testHandler.DeleteProject(httptest.NewRecorder(), req)
	})

	w = httptest.NewRecorder()
	req = newRequest("GET", "/api/projects/lifecycle?workspace_id="+testWorkspaceID, nil)
	testHandler.ListProjectLifecycle(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Projects []struct {
			ProjectID             string   `json:"project_id"`
			Health                string   `json:"health"`
			OwnerDecisionRequired bool     `json:"owner_decision_required"`
			ClosureBlockers       []string `json:"closure_blockers"`
		} `json:"projects"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode portfolio: %v", err)
	}
	if body.Total == 0 {
		t.Fatalf("portfolio total = 0, want >= 1")
	}
	found := false
	for _, p := range body.Projects {
		if p.ProjectID == project.ID {
			found = true
			if !p.OwnerDecisionRequired {
				t.Fatalf("seed project without lead: owner_decision_required = false, want true")
			}
			if p.Health == "active_with_frontier" {
				t.Fatalf("seed project without tasks classified active, want stalled/source_gap")
			}
		}
	}
	if !found {
		t.Fatalf("seed project %s not present in portfolio", project.ID)
	}
}

// Contract: GET /api/projects/{id}/lifecycle returns a single snapshot, 404
// for unknown id.
func TestGetProjectLifecycleNotFound(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/projects/00000000-0000-0000-0000-000000000000/lifecycle?workspace_id="+testWorkspaceID, nil)
	req = withURLParam(req, "id", "00000000-0000-0000-0000-000000000000")
	testHandler.GetProjectLifecycle(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// Contract: a project from a different workspace must NOT leak into this
// workspace's lifecycle portfolio (cross-workspace isolation).
func TestGetProjectLifecycleCrossWorkspaceIsolation(t *testing.T) {
	ctx := t.Context()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	email := "lifecycle-other-" + suffix + "@example.com"
	slug := "lifecycle-other-ws-" + suffix

	// Create a second workspace + owner member + project.
	var otherWsID, otherUserID, otherProjID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ('lifecycle-other', $1) RETURNING id
	`, email).Scan(&otherUserID); err != nil {
		t.Fatalf("insert other user: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix) VALUES ('Lifecycle Other',$1,'x','LCO') RETURNING id
	`, slug).Scan(&otherWsID); err != nil {
		t.Fatalf("insert other workspace: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1,$2,'owner')`, otherWsID, otherUserID); err != nil {
		t.Fatalf("insert other member: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, status) VALUES ($1,'other workspace project','in_progress') RETURNING id
	`, otherWsID).Scan(&otherProjID); err != nil {
		t.Fatalf("insert other project: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM project WHERE id=$1`, otherProjID)
		testPool.Exec(ctx, `DELETE FROM member WHERE workspace_id=$1`, otherWsID)
		testPool.Exec(ctx, `DELETE FROM workspace WHERE id=$1`, otherWsID)
		testPool.Exec(ctx, `DELETE FROM "user" WHERE id=$1`, otherUserID)
	})

	// The portfolio of THIS workspace must not contain the other project.
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/projects/lifecycle?workspace_id="+testWorkspaceID, nil)
	testHandler.ListProjectLifecycle(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body struct {
		Projects []struct {
			ProjectID string `json:"project_id"`
		} `json:"projects"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, p := range body.Projects {
		if p.ProjectID == otherProjID {
			t.Fatalf("cross-workspace project %s leaked into portfolio", otherProjID)
		}
	}

	// Direct single-project lookup from this workspace is 404.
	w2 := httptest.NewRecorder()
	req2 := newRequest("GET", "/api/projects/"+otherProjID+"/lifecycle?workspace_id="+testWorkspaceID, nil)
	req2 = withURLParam(req2, "id", otherProjID)
	testHandler.GetProjectLifecycle(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-workspace project, got %d", w2.Code)
	}
}

// Gauss F6: single-project lifecycle returns 200 with the correct project and
// does NOT mutate project.status (read-only contract).
func TestGetProjectLifecycleSingleProjectAndNoStatusMutation(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects?workspace_id="+testWorkspaceID, map[string]any{
		"title":  "lifecycle single seed",
		"status": "in_progress",
	})
	testHandler.CreateProject(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("seed CreateProject: %d %s", w.Code, w.Body.String())
	}
	var project ProjectResponse
	if err := json.NewDecoder(w.Body).Decode(&project); err != nil {
		t.Fatalf("decode CreateProject: %v", err)
	}
	t.Cleanup(func() {
		req := newRequest("DELETE", "/api/projects/"+project.ID, nil)
		req = withURLParam(req, "id", project.ID)
		testHandler.DeleteProject(httptest.NewRecorder(), req)
	})

	// Single-project lifecycle -> 200 with matching project_id.
	w2 := httptest.NewRecorder()
	req2 := newRequest("GET", "/api/projects/"+project.ID+"/lifecycle?workspace_id="+testWorkspaceID, nil)
	req2 = withURLParam(req2, "id", project.ID)
	testHandler.GetProjectLifecycle(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	var snap struct {
		ProjectID string `json:"project_id"`
		Status    string `json:"status"`
	}
	if err := json.NewDecoder(w2.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.ProjectID != project.ID {
		t.Fatalf("snapshot project_id = %q, want %q", snap.ProjectID, project.ID)
	}

	// Read back the project: GET lifecycle must not have changed status.
	w3 := httptest.NewRecorder()
	req3 := newRequest("GET", "/api/projects/"+project.ID+"?workspace_id="+testWorkspaceID, nil)
	req3 = withURLParam(req3, "id", project.ID)
	testHandler.GetProject(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("expected 200 re-read, got %d", w3.Code)
	}
	var after ProjectResponse
	if err := json.NewDecoder(w3.Body).Decode(&after); err != nil {
		t.Fatalf("decode re-read: %v", err)
	}
	if after.Status != "in_progress" {
		t.Fatalf("lifecycle GET mutated project.status to %q, want in_progress", after.Status)
	}
}
