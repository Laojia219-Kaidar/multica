package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestListTasksByIssue_ProjectGithubReposOverrideWorkspaceRepos guards the
// issue task-runs repo projection: every run of an issue inside a project must
// surface the project's current github_repo resources (repo_source=project),
// overriding the workspace repositories exactly as claim-time resolution does.
func TestListTasksByIssue_ProjectGithubReposOverrideWorkspaceRepos(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	setHandlerTestWorkspaceRepos(t, []map[string]string{
		{"url": "https://github.com/example/workspace-repo-a", "description": "ws a"},
		{"url": "https://github.com/example/workspace-repo-b", "description": "ws b"},
	})

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, repo_inheritance_policy) VALUES ($1, $2, 'project_only') RETURNING id
	`, testWorkspaceID, "Task-runs project override").Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID) })

	const projectRepoURL = "https://github.com/example/task-runs-project-repo"
	const projectRepoRef = "release/v2"
	if _, err := testPool.Exec(ctx, `
		INSERT INTO project_resource (project_id, workspace_id, resource_type, resource_ref, position)
		VALUES ($1, $2, 'github_repo', $3::jsonb, 0)
	`, projectID, testWorkspaceID, `{"url":"`+projectRepoURL+`","ref":"`+projectRepoRef+`"}`); err != nil {
		t.Fatalf("create project github_repo resource: %v", err)
	}

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, project_id, title, status, priority, creator_id, creator_type)
		VALUES ($1, $2, 'task-runs project repo', 'todo', 'medium', $3, 'member')
		RETURNING id
	`, testWorkspaceID, projectID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

	agentID := createHandlerTestAgent(t, "TaskRunProjectionAgent", []byte("[]"))
	taskIDs := make([]string, 2)
	for i := range taskIDs {
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
			VALUES ($1, (SELECT runtime_id FROM agent WHERE id = $1), $2, 'completed', 0)
			RETURNING id
		`, agentID, issueID).Scan(&taskIDs[i]); err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
	}
	t.Cleanup(func() {
		for _, id := range taskIDs {
			testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, id)
		}
	})

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues/"+issueID+"/task-runs", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.ListTasksByIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListTasksByIssue: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp []AgentTaskResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode task list: %v", err)
	}
	if len(resp) != len(taskIDs) {
		t.Fatalf("expected %d runs, got %d: %s", len(taskIDs), len(resp), w.Body.String())
	}
	for _, run := range resp {
		if run.RepoSource != "project" {
			t.Errorf("run %s repo_source = %q, want project", run.ID, run.RepoSource)
		}
		if run.RepoInheritancePolicy != projectRepoInheritancePolicyProjectOnly {
			t.Errorf("run %s repo_inheritance_policy = %q, want %q", run.ID, run.RepoInheritancePolicy, projectRepoInheritancePolicyProjectOnly)
		}
		if len(run.Repos) != 1 || run.Repos[0].URL != projectRepoURL {
			t.Fatalf("run %s repos = %+v, want only project repo %s", run.ID, run.Repos, projectRepoURL)
		}
		if run.Repos[0].Ref != projectRepoRef {
			t.Errorf("run %s project repo ref = %q, want %q", run.ID, run.Repos[0].Ref, projectRepoRef)
		}
		for _, r := range run.Repos {
			if strings.Contains(r.URL, "workspace-repo-a") || strings.Contains(r.URL, "workspace-repo-b") {
				t.Errorf("workspace repo %q leaked into task-runs despite project override", r.URL)
			}
		}
	}
}

// TestListTasksByIssue_NoProjectUsesWorkspaceFallback verifies that an issue
// with no project still gets the workspace repositories (repo_source =
// workspace_fallback), matching claim-time fallback behavior, and that the
// projection is applied to every run.
func TestListTasksByIssue_NoProjectUsesWorkspaceFallback(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	setHandlerTestWorkspaceRepos(t, []map[string]string{
		{"url": "https://github.com/example/workspace-repo-a", "description": "ws a"},
	})

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type)
		VALUES ($1, 'task-runs no-project', 'todo', 'medium', $2, 'member')
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

	agentID := createHandlerTestAgent(t, "TaskRunNoProjectAgent", []byte("[]"))
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, (SELECT runtime_id FROM agent WHERE id = $1), $2, 'completed', 0)
		RETURNING id
	`, agentID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("create task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues/"+issueID+"/task-runs", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.ListTasksByIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListTasksByIssue: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp []AgentTaskResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode task list: %v", err)
	}
	if len(resp) != 1 {
		t.Fatalf("expected 1 run, got %d", len(resp))
	}
	run := resp[0]
	if run.RepoSource != projectRepoInheritancePolicyWorkspaceFallback {
		t.Errorf("run repo_source = %q, want %q", run.RepoSource, projectRepoInheritancePolicyWorkspaceFallback)
	}
	if run.RepoInheritancePolicy != "" {
		t.Errorf("no-project run repo_inheritance_policy = %q, want empty", run.RepoInheritancePolicy)
	}
	if len(run.Repos) != 1 || run.Repos[0].URL != "https://github.com/example/workspace-repo-a" {
		t.Fatalf("run repos = %+v, want the workspace repo", run.Repos)
	}
}

// TestListTasksByIssue_ProjectOnlyNoReposFailsClosed verifies the project_only
// policy with no github_repo resources yields an empty repos list on every run
// (repo_source=none) and never falls back to the workspace repositories.
func TestListTasksByIssue_ProjectOnlyNoReposFailsClosed(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	// Workspace repos exist, but must NOT leak for a project_only project.
	setHandlerTestWorkspaceRepos(t, []map[string]string{
		{"url": "https://github.com/example/workspace-repo-a", "description": "ws a"},
	})

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, repo_inheritance_policy) VALUES ($1, $2, 'project_only') RETURNING id
	`, testWorkspaceID, "Task-runs project-only empty").Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID) })

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, project_id, title, status, priority, creator_id, creator_type)
		VALUES ($1, $2, 'task-runs project-only empty', 'todo', 'medium', $3, 'member')
		RETURNING id
	`, testWorkspaceID, projectID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

	agentID := createHandlerTestAgent(t, "TaskRunNoProjectReposAgent", []byte("[]"))
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, (SELECT runtime_id FROM agent WHERE id = $1), $2, 'completed', 0)
		RETURNING id
	`, agentID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("create task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues/"+issueID+"/task-runs", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.ListTasksByIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListTasksByIssue: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp []AgentTaskResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode task list: %v", err)
	}
	if len(resp) != 1 {
		t.Fatalf("expected 1 run, got %d", len(resp))
	}
	run := resp[0]
	if run.RepoSource != "none" {
		t.Errorf("run repo_source = %q, want none", run.RepoSource)
	}
	if run.RepoInheritancePolicy != projectRepoInheritancePolicyProjectOnly {
		t.Errorf("run repo_inheritance_policy = %q, want %q", run.RepoInheritancePolicy, projectRepoInheritancePolicyProjectOnly)
	}
	if len(run.Repos) != 0 {
		t.Fatalf("run repos = %+v, want empty (workspace fallback must not leak for project_only)", run.Repos)
	}
}

// TestListTasksByIssue_ProjectRepos_CrossWorkspaceResourceIsolated verifies
// that a github_repo project_resource row carrying a FOREIGN workspace_id is
// never projected into the task-runs of an issue in the caller's workspace.
// The projection must preserve workspace isolation exactly like claim-time
// resource loading (listProjectResourcesForProject).
func TestListTasksByIssue_ProjectRepos_CrossWorkspaceResourceIsolated(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, repo_inheritance_policy) VALUES ($1, $2, 'project_only') RETURNING id
	`, testWorkspaceID, "Task-runs cross-ws resource").Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID) })

	// A github_repo row owned by a REAL foreign workspace on the SAME project
	// + resource_type (unique key is (project_id, resource_type,
	// resource_ref), so a distinct URL is a distinct row). It must not be
	// projected into the test workspace's task-runs.
	var foreignWS string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Foreign resource workspace", "foreign-task-runs-ws", "Cross-workspace project resource isolation", "FRW").Scan(&foreignWS); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWS) })
	if _, err := testPool.Exec(ctx, `
		INSERT INTO project_resource (project_id, workspace_id, resource_type, resource_ref, position)
		VALUES ($1, $2, 'github_repo', $3::jsonb, 0)
	`, projectID, foreignWS, `{"url":"https://github.com/example/foreign-workspace-repo"}`); err != nil {
		t.Fatalf("create foreign workspace project_resource: %v", err)
	}

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, project_id, title, status, priority, creator_id, creator_type)
		VALUES ($1, $2, 'task-runs cross-ws resource', 'todo', 'medium', $3, 'member')
		RETURNING id
	`, testWorkspaceID, projectID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

	agentID := createHandlerTestAgent(t, "TaskRunCrossWSRepoAgent", []byte("[]"))
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, (SELECT runtime_id FROM agent WHERE id = $1), $2, 'completed', 0)
		RETURNING id
	`, agentID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("create task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues/"+issueID+"/task-runs", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.ListTasksByIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListTasksByIssue: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp []AgentTaskResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode task list: %v", err)
	}
	if len(resp) != 1 {
		t.Fatalf("expected 1 run, got %d", len(resp))
	}
	run := resp[0]
	if run.RepoSource != "none" {
		t.Errorf("run repo_source = %q, want none (only a foreign-workspace resource exists)", run.RepoSource)
	}
	if len(run.Repos) != 0 {
		t.Fatalf("run repos = %+v, want empty (foreign-workspace resource must be isolated)", run.Repos)
	}
}
