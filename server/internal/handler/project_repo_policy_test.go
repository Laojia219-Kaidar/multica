package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func currentProjectRevision(t *testing.T, projectID string) int64 {
	t.Helper()
	var revision int64
	if err := testPool.QueryRow(context.Background(), `SELECT revision FROM project WHERE id = $1`, projectID).Scan(&revision); err != nil {
		t.Fatalf("read project revision: %v", err)
	}
	return revision
}

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
		"revision":                currentProjectRevision(t, isolated.ID),
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
		"revision":                currentProjectRevision(t, isolated.ID),
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
	if updated.Revision != isolated.Revision+1 {
		t.Fatalf("updated revision = %d, want %d", updated.Revision, isolated.Revision+1)
	}
}

func TestUpdateProjectRequiresAndChecksRevision(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	project := createRepoPolicyProject(t, map[string]any{"title": "Project CAS"})

	missingW := httptest.NewRecorder()
	missingReq := withURLParam(newRequest(http.MethodPut, "/api/projects/"+project.ID, map[string]any{
		"title": "must not write",
	}), "id", project.ID)
	testHandler.UpdateProject(missingW, missingReq)
	if missingW.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing revision: got %d, want 428: %s", missingW.Code, missingW.Body.String())
	}

	invalidW := httptest.NewRecorder()
	invalidReq := withURLParam(newRequest(http.MethodPut, "/api/projects/"+project.ID, map[string]any{
		"title":    "must not write",
		"revision": 0,
	}), "id", project.ID)
	testHandler.UpdateProject(invalidW, invalidReq)
	if invalidW.Code != http.StatusBadRequest {
		t.Fatalf("invalid revision: got %d, want 400: %s", invalidW.Code, invalidW.Body.String())
	}
	overflowW := httptest.NewRecorder()
	overflowReq := withURLParam(newRequest(http.MethodPut, "/api/projects/"+project.ID, map[string]any{
		"title":    "must not write",
		"revision": maxSafeProjectRevision + 1,
	}), "id", project.ID)
	testHandler.UpdateProject(overflowW, overflowReq)
	if overflowW.Code != http.StatusBadRequest {
		t.Fatalf("unsafe revision: got %d, want 400: %s", overflowW.Code, overflowW.Body.String())
	}

	firstW := httptest.NewRecorder()
	firstReq := withURLParam(newRequest(http.MethodPut, "/api/projects/"+project.ID, map[string]any{
		"title":    "first write",
		"revision": project.Revision,
	}), "id", project.ID)
	testHandler.UpdateProject(firstW, firstReq)
	if firstW.Code != http.StatusOK {
		t.Fatalf("first revisioned update: got %d: %s", firstW.Code, firstW.Body.String())
	}
	var first ProjectResponse
	if err := json.NewDecoder(firstW.Body).Decode(&first); err != nil {
		t.Fatalf("decode first update: %v", err)
	}

	staleW := httptest.NewRecorder()
	staleReq := withURLParam(newRequest(http.MethodPut, "/api/projects/"+project.ID, map[string]any{
		"title":                   "stale write",
		"repo_inheritance_policy": projectRepoInheritancePolicyProjectOnly,
		"revision":                project.Revision,
	}), "id", project.ID)
	testHandler.UpdateProject(staleW, staleReq)
	if staleW.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale revision: got %d, want 412: %s", staleW.Code, staleW.Body.String())
	}
	var title, policy string
	var revision int64
	if err := testPool.QueryRow(context.Background(), `SELECT title, repo_inheritance_policy, revision FROM project WHERE id = $1`, project.ID).Scan(&title, &policy, &revision); err != nil {
		t.Fatalf("read project after stale update: %v", err)
	}
	if title != "first write" || policy != projectRepoInheritancePolicyWorkspaceFallback || revision != first.Revision {
		t.Fatalf("stale update changed project: title=%q policy=%q revision=%d, want %q/%q/%d", title, policy, revision, "first write", projectRepoInheritancePolicyWorkspaceFallback, first.Revision)
	}

}

func TestUpdateProjectConcurrentSameRevisionAllowsExactlyOneWriter(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	project := createRepoPolicyProject(t, map[string]any{"title": "Project CAS race"})

	start := make(chan struct{})
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for _, title := range []string{"race writer a", "race writer b"} {
		title := title
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			req := withURLParam(newRequest(http.MethodPut, "/api/projects/"+project.ID, map[string]any{
				"title":    title,
				"revision": project.Revision,
			}), "id", project.ID)
			<-start
			testHandler.UpdateProject(w, req)
			codes <- w.Code
		}()
	}
	close(start)
	wg.Wait()
	close(codes)

	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusPreconditionFailed] != 1 {
		t.Fatalf("concurrent CAS status counts = %#v, want one 200 and one 412", counts)
	}

	var title string
	var revision int64
	if err := testPool.QueryRow(context.Background(), `SELECT title, revision FROM project WHERE id = $1`, project.ID).Scan(&title, &revision); err != nil {
		t.Fatalf("read project after concurrent CAS: %v", err)
	}
	if (title != "race writer a" && title != "race writer b") || revision != project.Revision+1 {
		t.Fatalf("concurrent CAS result title/revision = %q/%d, want one writer and revision %d", title, revision, project.Revision+1)
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
