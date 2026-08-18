package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func createRepoPolicyProject(t *testing.T, body map[string]any) ProjectResponse {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.CreateProject(w, newRequest(http.MethodPost, "/api/projects", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateProject: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var project ProjectResponse
	if err := json.NewDecoder(w.Body).Decode(&project); err != nil {
		t.Fatalf("decode CreateProject: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, project.ID)
	})
	return project
}

func TestProjectRepoInheritancePolicyAPIContract(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	legacy := createRepoPolicyProject(t, map[string]any{"title": "Repo policy legacy default"})
	if legacy.RepoInheritancePolicy != projectRepoInheritancePolicyWorkspaceFallback {
		t.Fatalf("default repo_inheritance_policy = %q, want %q", legacy.RepoInheritancePolicy, projectRepoInheritancePolicyWorkspaceFallback)
	}

	isolated := createRepoPolicyProject(t, map[string]any{
		"title":                   "Repo policy project only",
		"repo_inheritance_policy": projectRepoInheritancePolicyProjectOnly,
	})
	if isolated.RepoInheritancePolicy != projectRepoInheritancePolicyProjectOnly {
		t.Fatalf("explicit repo_inheritance_policy = %q, want %q", isolated.RepoInheritancePolicy, projectRepoInheritancePolicyProjectOnly)
	}

	getW := httptest.NewRecorder()
	getReq := withURLParam(newRequest(http.MethodGet, "/api/projects/"+isolated.ID, nil), "id", isolated.ID)
	testHandler.GetProject(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("GetProject: expected 200, got %d: %s", getW.Code, getW.Body.String())
	}
	var got ProjectResponse
	if err := json.NewDecoder(getW.Body).Decode(&got); err != nil {
		t.Fatalf("decode GetProject: %v", err)
	}
	if got.RepoInheritancePolicy != projectRepoInheritancePolicyProjectOnly {
		t.Fatalf("GET repo_inheritance_policy = %q, want %q", got.RepoInheritancePolicy, projectRepoInheritancePolicyProjectOnly)
	}

	updateW := httptest.NewRecorder()
	updateReq := withURLParam(newRequest(http.MethodPut, "/api/projects/"+isolated.ID, map[string]any{
		"repo_inheritance_policy": projectRepoInheritancePolicyWorkspaceFallback,
	}), "id", isolated.ID)
	testHandler.UpdateProject(updateW, updateReq)
	if updateW.Code != http.StatusOK {
		t.Fatalf("UpdateProject: expected 200, got %d: %s", updateW.Code, updateW.Body.String())
	}
	var updated ProjectResponse
	if err := json.NewDecoder(updateW.Body).Decode(&updated); err != nil {
		t.Fatalf("decode UpdateProject: %v", err)
	}
	if updated.RepoInheritancePolicy != projectRepoInheritancePolicyWorkspaceFallback {
		t.Fatalf("updated repo_inheritance_policy = %q, want %q", updated.RepoInheritancePolicy, projectRepoInheritancePolicyWorkspaceFallback)
	}

	invalidW := httptest.NewRecorder()
	invalidReq := withURLParam(newRequest(http.MethodPut, "/api/projects/"+isolated.ID, map[string]any{
		"repo_inheritance_policy": "workspace_and_project",
	}), "id", isolated.ID)
	testHandler.UpdateProject(invalidW, invalidReq)
	if invalidW.Code != http.StatusBadRequest {
		t.Fatalf("invalid repo_inheritance_policy: expected 400, got %d: %s", invalidW.Code, invalidW.Body.String())
	}
	var stored string
	if err := testPool.QueryRow(context.Background(), `SELECT repo_inheritance_policy FROM project WHERE id = $1`, isolated.ID).Scan(&stored); err != nil {
		t.Fatalf("read repo_inheritance_policy after invalid update: %v", err)
	}
	if stored != projectRepoInheritancePolicyWorkspaceFallback {
		t.Fatalf("invalid update changed repo_inheritance_policy to %q", stored)
	}
}

func TestProjectAllowsWorkspaceRepoFallbackFailsClosed(t *testing.T) {
	if !projectAllowsWorkspaceRepoFallback(projectRepoInheritancePolicyWorkspaceFallback) {
		t.Fatal("workspace_fallback must allow the legacy fallback")
	}
	for _, policy := range []string{projectRepoInheritancePolicyProjectOnly, "", "future_unknown"} {
		if projectAllowsWorkspaceRepoFallback(policy) {
			t.Fatalf("policy %q unexpectedly enabled workspace fallback", policy)
		}
	}
}
