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
)

func TestMarkerCanBeConstructedBeforeDispatchAndAcceptedAfterTaskCreation(t *testing.T) {
	worktree := WorktreeRoot + "pilot"
	marker, err := CanonicalMarker(WorkspaceToolPolicy, testIssueID, testWorkspaceID, "Edit the named pilot fixture.", worktree, testRequest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(marker, testTaskID) || strings.Contains(marker, `"task_id"`) {
		t.Fatalf("pre-dispatch marker unexpectedly requires the future task UUID: %s", marker)
	}
	// Simulate dispatch assigning an ID after the external handoff marker has
	// already been constructed. Parse must validate the real server ID rather
	// than trusting a client-supplied task identity.
	state, contract := Parse(marker, Provider, TaskKind, testTaskID, testIssueID, testWorkspaceID)
	if state != Valid || contract.Worktree != worktree || contract.ToolPolicy != WorkspaceToolPolicy ||
		contract.TaskBinding != ServerGeneratedTaskBinding {
		t.Fatalf("state=%v contract=%+v", state, contract)
	}
	for _, forbidden := range []string{"run_shell_command", "multica issue", "comment add"} {
		if strings.Contains(RuntimeBrief(state, contract), forbidden) && forbidden != "run_shell_command" {
			t.Fatalf("runtime brief contains %q", forbidden)
		}
	}
}

func TestReadOnlyMarkerUsesClosedQuinnRoute(t *testing.T) {
	marker, err := CanonicalMarker(ReadOnlyToolPolicy, testIssueID, testWorkspaceID, "Inspect the named pilot fixture.", WorktreeRoot+"pilot", testRequest)
	if err != nil {
		t.Fatal(err)
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

func TestMarkerContextAndTaskUUIDMismatchFailClosed(t *testing.T) {
	marker, err := CanonicalMarker(WorkspaceToolPolicy, testIssueID, testWorkspaceID, "Edit the named pilot fixture.", WorktreeRoot+"pilot", testRequest)
	if err != nil {
		t.Fatal(err)
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
		"issue":     {WorkspaceToolPolicy, "not-an-issue-uuid", testWorkspaceID, "Edit fixture.", validWorktree, testRequest},
		"workspace": {WorkspaceToolPolicy, testIssueID, "not-a-workspace-uuid", "Edit fixture.", validWorktree, testRequest},
		"worktree":  {WorkspaceToolPolicy, testIssueID, testWorkspaceID, "Edit fixture.", "/tmp/pilot", testRequest},
		"policy":    {"bounded_workspace_shell", testIssueID, testWorkspaceID, "Edit fixture.", validWorktree, testRequest},
	} {
		t.Run(name, func(t *testing.T) {
			if marker, err := CanonicalMarker(args[0], args[1], args[2], args[3], args[4], args[5]); err == nil {
				t.Fatalf("invalid %s accepted: %s", name, marker)
			}
		})
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
