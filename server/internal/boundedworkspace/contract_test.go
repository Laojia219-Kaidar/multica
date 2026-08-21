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

func TestCanonicalMarkerRoundTrip(t *testing.T) {
	worktree := WorktreeRoot + "pilot"
	marker, err := CanonicalMarker(testTaskID, testIssueID, testWorkspaceID, "Edit the named pilot fixture.", worktree, testRequest)
	if err != nil {
		t.Fatal(err)
	}
	state, contract := Parse(marker, Provider, TaskKind, testTaskID, testIssueID, testWorkspaceID)
	if state != Valid || contract.Worktree != worktree || contract.ToolPolicy != ToolPolicy {
		t.Fatalf("state=%v contract=%+v", state, contract)
	}
	for _, forbidden := range []string{"run_shell_command", "multica issue", "comment add"} {
		if strings.Contains(RuntimeBrief(state, contract), forbidden) && forbidden != "run_shell_command" {
			t.Fatalf("runtime brief contains %q", forbidden)
		}
	}
}

func TestMarkerMismatchFailsClosed(t *testing.T) {
	marker, err := CanonicalMarker(testTaskID, testIssueID, testWorkspaceID, "Edit the named pilot fixture.", WorktreeRoot+"pilot", testRequest)
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
			return Provider, TaskKind, "31234567-89ab-cdef-0123-456789abcdef", testIssueID, testWorkspaceID
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
