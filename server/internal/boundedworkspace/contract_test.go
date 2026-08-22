package boundedworkspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testTaskID      = "01234567-89ab-cdef-0123-456789abcdef"
	testIssueID     = "11234567-89ab-cdef-0123-456789abcdef"
	testWorkspaceID = "21234567-89ab-cdef-0123-456789abcdef"
	testRequest     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testDispatchKey = "p3-pilot-003-dispatch"
)

func TestMarkerCanBeConstructedBeforeDispatchAndAcceptedAfterTaskCreation(t *testing.T) {
	worktree := WorktreeRoot + "pilot"
	preMarker, err := CanonicalMarker(WorkspaceToolPolicy, testDispatchKey, testIssueID, testWorkspaceID, "Edit the named pilot fixture.", worktree, testRequest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(preMarker, testTaskID) || !strings.Contains(preMarker, `"task_id":""`) {
		t.Fatalf("pre-dispatch marker unexpectedly requires the future task UUID: %s", preMarker)
	}
	// Simulate dispatch assigning an ID after the external handoff marker has
	// already been constructed. Parse must validate the real server ID rather
	// than trusting a client-supplied task identity.
	state, marker, err := BindTask(preMarker, testDispatchKey, testTaskID, testIssueID, testWorkspaceID)
	if err != nil || state != Valid {
		t.Fatalf("BindTask state=%v err=%v", state, err)
	}
	state, contract := Parse(marker, Provider, TaskKind, testTaskID, testIssueID, testWorkspaceID)
	if state != Valid || contract.Worktree != worktree || contract.ToolPolicy != WorkspaceToolPolicy ||
		contract.TaskBinding != BoundTaskBinding || contract.TaskID != testTaskID {
		t.Fatalf("state=%v contract=%+v", state, contract)
	}
	for _, forbidden := range []string{"run_shell_command", "multica issue", "comment add"} {
		if strings.Contains(RuntimeBrief(state, contract), forbidden) && forbidden != "run_shell_command" {
			t.Fatalf("runtime brief contains %q", forbidden)
		}
	}
}

func TestReadOnlyMarkerUsesClosedQuinnRoute(t *testing.T) {
	preMarker, err := CanonicalMarker(ReadOnlyToolPolicy, testDispatchKey, testIssueID, testWorkspaceID, "Inspect the named pilot fixture.", WorktreeRoot+"pilot", testRequest)
	if err != nil {
		t.Fatal(err)
	}
	state, marker, err := BindTask(preMarker, testDispatchKey, testTaskID, testIssueID, testWorkspaceID)
	if err != nil || state != Valid {
		t.Fatalf("BindTask state=%v err=%v", state, err)
	}
	state, contract := Parse(marker, Provider, TaskKind, testTaskID, testIssueID, testWorkspaceID)
	if state != Valid || contract.ToolPolicy != ReadOnlyToolPolicy || contract.MaxToolCalls != ReadOnlyMaxToolCalls {
		t.Fatalf("state=%v contract=%+v", state, contract)
	}
	prompt := Prompt(contract)
	for _, required := range []string{"read_file", "glob", "grep_search", "list_directory", ReadOnlyDeliveryPrefix} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("read-only prompt missing %q: %s", required, prompt)
		}
	}
	for _, forbidden := range []string{"multica issue", "comment add", "Start by running"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("read-only prompt contains ordinary mandate %q: %s", forbidden, prompt)
		}
	}
}

func TestDevelopmentMarkerHasNoCallBudgetAndAdmitsLocalTests(t *testing.T) {
	preMarker, err := CanonicalMarker(DevelopmentToolPolicy, testDispatchKey, testIssueID, testWorkspaceID, "Edit the exact files and run repository-local tests.", WorktreeRoot+"pilot", testRequest)
	if err != nil {
		t.Fatal(err)
	}
	state, marker, err := BindTask(preMarker, testDispatchKey, testTaskID, testIssueID, testWorkspaceID)
	if err != nil || state != Valid {
		t.Fatalf("BindTask state=%v err=%v", state, err)
	}
	state, contract := Parse(marker, Provider, TaskKind, testTaskID, testIssueID, testWorkspaceID)
	if state != Valid || contract.ToolPolicy != DevelopmentToolPolicy || contract.MaxToolCalls != DevelopmentMaxToolCalls {
		t.Fatalf("state=%v contract=%+v", state, contract)
	}
	prompt := Prompt(contract)
	for _, required := range []string{"read_file", "edit", "write_file", "run_shell_command", "repository-local tests"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("development prompt missing %q: %s", required, prompt)
		}
	}
	for _, forbidden := range []string{"--max-tool-calls", "multica issue", "comment add", "Start by running"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("development prompt contains forbidden mandate %q: %s", forbidden, prompt)
		}
	}
}

func TestMarkerContextAndTaskUUIDMismatchFailClosed(t *testing.T) {
	preMarker, err := CanonicalMarker(WorkspaceToolPolicy, testDispatchKey, testIssueID, testWorkspaceID, "Edit the named pilot fixture.", WorktreeRoot+"pilot", testRequest)
	if err != nil {
		t.Fatal(err)
	}
	state, marker, err := BindTask(preMarker, testDispatchKey, testTaskID, testIssueID, testWorkspaceID)
	if err != nil || state != Valid {
		t.Fatalf("BindTask state=%v err=%v", state, err)
	}
	for _, mutate := range []func() (string, string, string, string, string){
		func() (string, string, string, string, string) {
			return "codex", TaskKind, testTaskID, testIssueID, testWorkspaceID
		},
		func() (string, string, string, string, string) {
			return Provider, "review", testTaskID, testIssueID, testWorkspaceID
		},
		func() (string, string, string, string, string) {
			return Provider, TaskKind, "not-a-task-uuid", testIssueID, testWorkspaceID
		},
		func() (string, string, string, string, string) {
			return Provider, TaskKind, testTaskID, "31234567-89ab-cdef-0123-456789abcdef", testWorkspaceID
		},
		func() (string, string, string, string, string) {
			return Provider, TaskKind, testTaskID, testIssueID, "41234567-89ab-cdef-0123-456789abcdef"
		},
	} {
		provider, kind, taskID, issueID, workspaceID := mutate()
		if state, _ := Parse(marker, provider, kind, taskID, issueID, workspaceID); state != Invalid {
			t.Fatalf("mismatch state=%v", state)
		}
	}
	if state, _ := Parse("ordinary handoff", Provider, TaskKind, testTaskID, testIssueID, testWorkspaceID); state != NotPresent {
		t.Fatalf("ordinary state=%v", state)
	}
}

func TestInvalidPreDispatchBindingsFailClosed(t *testing.T) {
	validWorktree := WorktreeRoot + "pilot"
	for name, args := range map[string][]string{
		"dispatch":  {WorkspaceToolPolicy, "", testIssueID, testWorkspaceID, "Edit fixture.", validWorktree, testRequest},
		"issue":     {WorkspaceToolPolicy, testDispatchKey, "not-an-issue-uuid", testWorkspaceID, "Edit fixture.", validWorktree, testRequest},
		"workspace": {WorkspaceToolPolicy, testDispatchKey, testIssueID, "not-a-workspace-uuid", "Edit fixture.", validWorktree, testRequest},
		"worktree":  {WorkspaceToolPolicy, testDispatchKey, testIssueID, testWorkspaceID, "Edit fixture.", "/tmp/pilot", testRequest},
		"policy":    {"bounded_workspace_shell", testDispatchKey, testIssueID, testWorkspaceID, "Edit fixture.", validWorktree, testRequest},
	} {
		t.Run(name, func(t *testing.T) {
			if marker, err := CanonicalMarker(args[0], args[1], args[2], args[3], args[4], args[5], args[6]); err == nil {
				t.Fatalf("invalid %s accepted: %s", name, marker)
			}
		})
	}
}

func TestFinalMarkerCannotBeReplayedForSecondTask(t *testing.T) {
	preMarker, err := CanonicalMarker(WorkspaceToolPolicy, testDispatchKey, testIssueID, testWorkspaceID, "Edit fixture.", WorktreeRoot+"pilot", testRequest)
	if err != nil {
		t.Fatal(err)
	}
	state, marker, err := BindTask(preMarker, testDispatchKey, testTaskID, testIssueID, testWorkspaceID)
	if err != nil || state != Valid {
		t.Fatalf("BindTask state=%v err=%v", state, err)
	}
	secondTaskID := "31234567-89ab-cdef-0123-456789abcdef"
	if state, _ := Parse(marker, Provider, TaskKind, secondTaskID, testIssueID, testWorkspaceID); state != Invalid {
		t.Fatalf("task-bound marker replay state=%v", state)
	}
	if state, _, err := BindTask(preMarker, "different-dispatch-key", secondTaskID, testIssueID, testWorkspaceID); state != Invalid || err == nil {
		t.Fatalf("different dispatch key state=%v err=%v", state, err)
	}
}

func TestIndependentRawPreDispatchWireFinalizesToReturnedTask(t *testing.T) {
	// This literal models the external Owner dispatch client and deliberately
	// does not use CanonicalMarker, so producer and consumer cannot agree by
	// sharing the same construction code.
	preMarker := `HIVECREW_BOUNDED_WORKSPACE_V3 {"delivery_prefix":"P3-BOUNDED-WORKSPACE-DELIVERY:","dispatch_key":"p3-pilot-003-dispatch","issue_id":"11234567-89ab-cdef-0123-456789abcdef","max_tool_calls":12,"objective":"Edit fixture.","pilot_id":"WO-C1-04-HIV719-QWEN-P3-BOUNDED-WORKSPACE-PILOT-003","provider":"qwen","request_sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","task_binding":"pending_server_generated_uuid","task_id":"","task_kind":"work","tool_policy":"bounded_workspace_noshell","workspace_id":"21234567-89ab-cdef-0123-456789abcdef","worktree":"/srv/hivecosm/12-development-workspaces/users/williamdev/worktrees/p3-pilot-003"}`
	state, marker, err := BindTask(preMarker, testDispatchKey, testTaskID, testIssueID, testWorkspaceID)
	if err != nil || state != Valid {
		t.Fatalf("BindTask state=%v err=%v", state, err)
	}
	if state, contract := Parse(marker, Provider, TaskKind, testTaskID, testIssueID, testWorkspaceID); state != Valid || contract.TaskID != testTaskID {
		t.Fatalf("final state=%v contract=%+v", state, contract)
	}
}

func TestValidateAssignedWorktree(t *testing.T) {
	if os.Getuid() != ExpectedUID || os.Getgid() != ExpectedGID {
		t.Skip("principal-specific staging contract")
	}
	worktree, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	contract := Contract{Worktree: worktree}
	if err := ValidateAssignedWorktree(contract, worktree, true); err != nil {
		t.Fatalf("exact assigned worktree rejected: %v", err)
	}
	if err := ValidateAssignedWorktree(contract, WorktreeRoot+"missing", true); err == nil {
		t.Fatal("missing worktree accepted")
	}
	if err := ValidateAssignedWorktree(contract, worktree, false); err == nil {
		t.Fatal("non-local assignment accepted")
	}
}
