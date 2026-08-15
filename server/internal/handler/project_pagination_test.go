package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// seedProject seeds a project and returns it for pagination tests.
func seedProjectForPagination(t *testing.T, title string) ProjectResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects?workspace_id="+testWorkspaceID, map[string]any{
		"title": title,
	})
	testHandler.CreateProject(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("seed CreateProject: %d %s", w.Code, w.Body.String())
	}
	var p ProjectResponse
	if err := json.NewDecoder(w.Body).Decode(&p); err != nil {
		t.Fatalf("decode seed: %v", err)
	}
	t.Cleanup(func() {
		r := newRequest("DELETE", "/api/projects/"+p.ID, nil)
		r = withURLParam(r, "id", p.ID)
		testHandler.DeleteProject(httptest.NewRecorder(), r)
	})
	return p
}

func listProjectsPage(t *testing.T, query string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/projects?workspace_id="+testWorkspaceID+query, nil)
	testHandler.ListProjects(w, req)
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return w.Code, body
}

func TestListProjectsPagination(t *testing.T) {
	seedProjectForPagination(t, "pagination project 1")
	seedProjectForPagination(t, "pagination project 2")
	seedProjectForPagination(t, "pagination project 3")

	code, body := listProjectsPage(t, "&limit=2&offset=0")
	if code != http.StatusOK {
		t.Fatalf("list: %d", code)
	}
	if body["total"].(float64) < 3 {
		t.Errorf("total should be >= 3, got %v", body["total"])
	}
	if got := len(body["projects"].([]any)); got != 2 {
		t.Errorf("expected 2 projects, got %d", got)
	}
	if body["has_more"] != true {
		t.Errorf("expected has_more=true, got %v", body["has_more"])
	}

	_, body2 := listProjectsPage(t, "&limit=200&offset=0")
	if body2["has_more"] != false {
		t.Errorf("expected has_more=false with large limit, got %v", body2["has_more"])
	}
}

func TestListProjectsPaginationRejectsInvalidLimit(t *testing.T) {
	code, _ := listProjectsPage(t, "&limit=abc")
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid limit, got %d", code)
	}
	code, _ = listProjectsPage(t, "&offset=-1")
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid offset, got %d", code)
	}
}
