package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func createProjectOnlyClaimProject(t *testing.T, title string) string {
	t.Helper()
	var projectID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO project (workspace_id, title, repo_inheritance_policy)
		VALUES ($1, $2, 'project_only')
		RETURNING id
	`, testWorkspaceID, title).Scan(&projectID); err != nil {
		t.Fatalf("create project_only project: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID)
	})
	return projectID
}

func TestClaimTask_ProjectOnlySuppressesWorkspaceRepoFallbacks(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	setHandlerTestWorkspaceRepos(t, []map[string]string{
		{"url": "https://github.com/example/must-not-inherit", "description": "workspace fallback sentinel"},
	})

	t.Run("issue", func(t *testing.T) {
		ctx := context.Background()
		agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)
		projectID := createProjectOnlyClaimProject(t, "Project-only issue claim")

		var issueID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO issue (
				workspace_id, project_id, title, status, priority,
				creator_id, creator_type, number, position
			) VALUES ($1, $2, 'project-only issue claim', 'todo', 'medium', $3, 'member', 99101, 0)
			RETURNING id
		`, testWorkspaceID, projectID, testUserID).Scan(&issueID); err != nil {
			t.Fatalf("create issue: %v", err)
		}
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

		var taskID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
			VALUES ($1, $2, $3, 'queued', 1000)
			RETURNING id
		`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
			t.Fatalf("create issue task: %v", err)
		}
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

		claimed := claimTaskForRuntimeGuard(t, runtimeID, daemonID)
		if claimed.ProjectID != projectID {
			t.Fatalf("project_id = %q, want %q", claimed.ProjectID, projectID)
		}
		if len(claimed.Repos) != 0 {
			t.Fatalf("project_only issue inherited workspace repos: %+v", claimed.Repos)
		}
		if claimed.RepoInheritancePolicy != projectRepoInheritancePolicyProjectOnly || claimed.RepoSource != "none" {
			t.Fatalf("issue repo policy/source = %q/%q, want project_only/none", claimed.RepoInheritancePolicy, claimed.RepoSource)
		}
	})

	t.Run("chat", func(t *testing.T) {
		ctx := context.Background()
		agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)
		projectID := createProjectOnlyClaimProject(t, "Project-only chat claim")
		sessionID := createChatSessionWithProjectForTest(t, agentID, projectID)
		if _, err := testPool.Exec(ctx, `
			INSERT INTO chat_message (chat_session_id, role, content)
			VALUES ($1, 'user', 'Run without a repository checkout')
		`, sessionID); err != nil {
			t.Fatalf("create chat message: %v", err)
		}

		var taskID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, chat_session_id)
			VALUES ($1, $2, 'queued', 1000, $3)
			RETURNING id
		`, agentID, runtimeID, sessionID).Scan(&taskID); err != nil {
			t.Fatalf("create chat task: %v", err)
		}
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

		claimed := claimTaskForRuntimeGuard(t, runtimeID, daemonID)
		if claimed.ProjectID != projectID {
			t.Fatalf("project_id = %q, want %q", claimed.ProjectID, projectID)
		}
		if len(claimed.Repos) != 0 {
			t.Fatalf("project_only chat inherited workspace repos: %+v", claimed.Repos)
		}
		if claimed.RepoInheritancePolicy != projectRepoInheritancePolicyProjectOnly || claimed.RepoSource != "none" {
			t.Fatalf("chat repo policy/source = %q/%q, want project_only/none", claimed.RepoInheritancePolicy, claimed.RepoSource)
		}
	})

	t.Run("quick_create", func(t *testing.T) {
		ctx := context.Background()
		agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)
		projectID := createProjectOnlyClaimProject(t, "Project-only quick-create claim")
		quickContext, err := json.Marshal(map[string]any{
			"type":         "quick_create",
			"prompt":       "create a repo-free follow-up issue",
			"requester_id": testUserID,
			"workspace_id": testWorkspaceID,
			"project_id":   projectID,
		})
		if err != nil {
			t.Fatalf("marshal quick-create context: %v", err)
		}

		var taskID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, context)
			VALUES ($1, $2, 'queued', 1000, $3)
			RETURNING id
		`, agentID, runtimeID, quickContext).Scan(&taskID); err != nil {
			t.Fatalf("create quick-create task: %v", err)
		}
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

		claimed := claimTaskForRuntimeGuard(t, runtimeID, daemonID)
		if claimed.ProjectID != projectID {
			t.Fatalf("project_id = %q, want %q", claimed.ProjectID, projectID)
		}
		if len(claimed.Repos) != 0 {
			t.Fatalf("project_only quick-create inherited workspace repos: %+v", claimed.Repos)
		}
		if claimed.RepoInheritancePolicy != projectRepoInheritancePolicyProjectOnly || claimed.RepoSource != "none" {
			t.Fatalf("quick-create repo policy/source = %q/%q, want project_only/none", claimed.RepoInheritancePolicy, claimed.RepoSource)
		}
	})
}

func TestClaimTask_ProjectOnlyRequiresDaemonCapability(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)
	projectID := createProjectOnlyClaimProject(t, "Project-only capability gate")

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, project_id, title, status, priority,
			creator_id, creator_type, number, position
		) VALUES ($1, $2, 'project-only capability gate', 'todo', 'medium', $3, 'member', 99102, 0)
		RETURNING id
	`, testWorkspaceID, projectID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 1000)
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("create issue task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/claim", nil, testWorkspaceID, daemonID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("runtimeId", runtimeID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("legacy daemon claim: expected 409, got %d: %s", w.Code, w.Body.String())
	}

	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status); err != nil {
		t.Fatalf("read requeued task: %v", err)
	}
	if status != "queued" {
		t.Fatalf("legacy daemon capability failure left task status %q, want queued", status)
	}

	// The current daemon capability can claim the same exact task after the
	// compatibility failure; no duplicate task or fresh enqueue is required.
	capableReq := newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/claim", nil, testWorkspaceID, daemonID)
	capableReq.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityProjectRepoScopeV1)
	capableCtx := chi.NewRouteContext()
	capableCtx.URLParams.Add("runtimeId", runtimeID)
	capableReq = capableReq.WithContext(context.WithValue(capableReq.Context(), chi.RouteCtxKey, capableCtx))
	capableW := httptest.NewRecorder()
	testHandler.ClaimTaskByRuntime(capableW, capableReq)
	if capableW.Code != http.StatusOK {
		t.Fatalf("capable daemon claim: expected 200, got %d: %s", capableW.Code, capableW.Body.String())
	}
}

func TestClaimTask_StaleProjectContextRequeuesFailClosed(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)
	projectID := createProjectOnlyClaimProject(t, "Stale project claim context")

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, project_id, title, status, priority,
			creator_id, creator_type, number, position
		) VALUES ($1, $2, 'stale project claim context', 'todo', 'medium', $3, 'member', 99103, 0)
		RETURNING id
	`, testWorkspaceID, projectID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })
	var foreignWorkspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Foreign claim context', $1, '', 'FCC')
		RETURNING id
	`, "foreign-claim-context-"+uuid.NewString()).Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE project SET workspace_id = $1 WHERE id = $2`, foreignWorkspaceID, projectID); err != nil {
		t.Fatalf("move project to foreign workspace before claim: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID)
		testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID)
	})

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 1000)
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("create issue task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/claim", nil, testWorkspaceID, daemonID)
	req.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityProjectRepoScopeV1)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("runtimeId", runtimeID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("stale project claim: expected 409, got %d: %s", w.Code, w.Body.String())
	}

	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status); err != nil {
		t.Fatalf("read requeued task: %v", err)
	}
	if status != "queued" {
		t.Fatalf("stale project claim left task status %q, want queued", status)
	}
}
