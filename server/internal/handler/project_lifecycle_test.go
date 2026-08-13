package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
