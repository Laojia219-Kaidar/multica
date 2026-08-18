package daemon

import "testing"

func newProjectRepoScopeTestDaemon() *Daemon {
	const workspaceID = "ws-project-scope"
	return &Daemon{workspaces: map[string]*workspaceState{
		workspaceID: newWorkspaceState(workspaceID, nil, "", []RepoData{{URL: "https://example.test/workspace.git"}}, nil),
	}}
}

func TestTaskRepoScopeSeparatesProjectOnlyAndWorkspaceFallback(t *testing.T) {
	d := newProjectRepoScopeTestDaemon()
	const workspaceID = "ws-project-scope"
	const workspaceRepo = "https://example.test/workspace.git"
	const projectRepo = "https://example.test/project.git"

	if err := d.registerTaskRepoScope(workspaceID, "strict", "project_only", "project", []RepoData{{URL: projectRepo}}); err != nil {
		t.Fatalf("register strict scope: %v", err)
	}
	if err := d.registerTaskRepoScope(workspaceID, "legacy", "workspace_fallback", "workspace_fallback", nil); err != nil {
		t.Fatalf("register legacy scope: %v", err)
	}

	if d.repoAllowedForTask(workspaceID, "strict", workspaceRepo) {
		t.Fatal("project_only task inherited workspace repository")
	}
	if !d.repoAllowedForTask(workspaceID, "strict", projectRepo) {
		t.Fatal("project_only task did not allow its project repository")
	}
	if !d.repoAllowedForTask(workspaceID, "legacy", workspaceRepo) {
		t.Fatal("workspace_fallback task lost historical workspace repository access")
	}
	if d.repoAllowedForTask(workspaceID, "legacy", projectRepo) {
		t.Fatal("workspace_fallback task inherited another task's project repository")
	}
}

func TestTaskRepoScopeProjectSourceOverridesWorkspaceFallback(t *testing.T) {
	d := newProjectRepoScopeTestDaemon()
	const workspaceID = "ws-project-scope"
	const workspaceRepo = "https://example.test/workspace.git"
	const projectRepo = "https://example.test/project.git"

	if err := d.registerTaskRepoScope(workspaceID, "project-source", "workspace_fallback", "project", []RepoData{{URL: projectRepo}}); err != nil {
		t.Fatalf("register project-source scope: %v", err)
	}
	if d.repoAllowedForTask(workspaceID, "project-source", workspaceRepo) {
		t.Fatal("project source inherited workspace repository under fallback policy")
	}
	if !d.repoAllowedForTask(workspaceID, "project-source", projectRepo) {
		t.Fatal("project source did not allow its exact project repository")
	}
}

func TestTaskRepoScopeEmptyAndMissingFailClosed(t *testing.T) {
	d := newProjectRepoScopeTestDaemon()
	const workspaceID = "ws-project-scope"
	const workspaceRepo = "https://example.test/workspace.git"

	if err := d.registerTaskRepoScope(workspaceID, "empty-strict", "project_only", "none", nil); err != nil {
		t.Fatalf("register empty strict scope: %v", err)
	}
	if d.repoAllowedForTask(workspaceID, "empty-strict", workspaceRepo) {
		t.Fatal("empty project_only scope allowed workspace repository")
	}
	if d.repoAllowedForTask(workspaceID, "missing", workspaceRepo) {
		t.Fatal("missing task scope allowed checkout")
	}
	if !d.taskScopeRejectsRepo(workspaceID, "empty-strict", workspaceRepo) {
		t.Fatal("empty project_only scope must reject before workspace refresh")
	}
	if !d.taskScopeRejectsRepo(workspaceID, "missing", workspaceRepo) {
		t.Fatal("missing task scope must reject before workspace refresh")
	}
}

func TestTaskRepoScopeUnknownPolicyAndCleanup(t *testing.T) {
	d := newProjectRepoScopeTestDaemon()
	const workspaceID = "ws-project-scope"
	const projectRepo = "https://example.test/project.git"

	if err := d.registerTaskRepoScope(workspaceID, "unknown", "future_policy", "none", nil); err == nil {
		t.Fatal("unknown repository inheritance policy should fail closed")
	}
	if err := d.registerTaskRepoScope(workspaceID, "strict", "project_only", "project", []RepoData{{URL: projectRepo}}); err != nil {
		t.Fatalf("register strict scope: %v", err)
	}
	d.clearTaskRepoRefs(workspaceID, "strict")
	if d.repoAllowedForTask(workspaceID, "strict", projectRepo) {
		t.Fatal("completed task retained repository authorization scope")
	}
}
