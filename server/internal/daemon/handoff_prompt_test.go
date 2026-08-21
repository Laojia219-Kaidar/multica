package daemon

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/notoolcanary"
)

var ordinaryPromptSHA256 = map[string]string{
	"assignment": "7e3d9089c73b5c80ecf158554dab5c0731c36b8b4c55c52c1dfe9a9e0b78eff2",
	"comment":    "60e3bc3e129f4ed342b987a932080e399fd68d51564ee3ba40e8b0cf27fa0530",
	"autopilot":  "398d45a5e536c85f03d4ede6983534329a615dd04ffbe7a03b3489462c988b9f",
	"quick":      "a44d54eea4e85decf8a02fe4392e24b753eb7b7d25d2f7a54858b94274c979ac",
	"chat":       "38522ee9d2ef55f7cdf0a1cf94de8421d7466e2d4c72c17a18859294052261dd",
	"review":     "6a41a6ce339dcbff347d3b049aec2b92504588869ac37f3dc2f89505744e0478",
	"repair":     "fdc0584470220a44915548bdfd7fd55afd04cd7cae9d6e0e90a90c947165f10c",
}

// TestBuildPrompt_HandoffNote_AssignmentBranch verifies a handoff note on an
// issue-assignment task renders through the assignment branch — it appears in
// the prompt, framed as a handoff (not a comment to reply to), and does not
// trip the quick-create branch.
func TestBuildPrompt_HandoffNote_AssignmentBranch(t *testing.T) {
	note := "Only touch the login flow; do not change payments."
	out := BuildPrompt(Task{IssueID: "issue-123", HandoffNote: note}, "claude")

	if !strings.Contains(out, note) {
		t.Fatalf("handoff note missing from prompt:\n%s", out)
	}
	if !strings.Contains(out, "handoff note") {
		t.Fatalf("expected handoff framing in prompt:\n%s", out)
	}
	if strings.Contains(out, "quick-create assistant") {
		t.Fatalf("handoff task must not use the quick-create prompt branch:\n%s", out)
	}
	// Still an assignment task: should point the agent at `multica issue get`.
	if !strings.Contains(out, "multica issue get issue-123") {
		t.Fatalf("expected assignment prompt body:\n%s", out)
	}
}

// TestBuildPrompt_NoHandoffNote_Unchanged verifies the assignment prompt is
// unchanged when no handoff note is present (no stray handoff framing).
func TestBuildPrompt_NoHandoffNote_Unchanged(t *testing.T) {
	out := BuildPrompt(Task{IssueID: "issue-123"}, "claude")
	if strings.Contains(out, "handoff note") {
		t.Fatalf("unexpected handoff framing when no note set:\n%s", out)
	}
}

func TestBuildPrompt_NoToolCanaryUsesClosedReasoningRoute(t *testing.T) {
	marker, err := notoolcanary.CanonicalMarker("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	out := BuildPrompt(Task{
		IssueID:     notoolcanary.IssueID,
		TaskKind:    notoolcanary.TaskKind,
		HandoffNote: marker,
	}, "qwen")
	for _, required := range []string{notoolcanary.Instruction, notoolcanary.DeliveryPrefix, "Do not call, request, or simulate any tool"} {
		if !strings.Contains(out, required) {
			t.Fatalf("required no-tool instruction %q missing:\n%s", required, out)
		}
	}
	for _, forbidden := range []string{"multica ", "issue get", "comment list", "comment add", "MCP", "repository"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("closed no-tool prompt contains forbidden mandate %q:\n%s", forbidden, out)
		}
	}
}

func TestBuildPrompt_NoToolMarkerMismatchRejectsWithoutFallback(t *testing.T) {
	marker, err := notoolcanary.CanonicalMarker("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	out := BuildPrompt(Task{
		IssueID:     notoolcanary.IssueID,
		TaskKind:    "review",
		HandoffNote: marker,
	}, "qwen")
	if out != notoolcanary.InvalidPrompt() {
		t.Fatalf("mismatch did not take fixed rejection path:\n%s", out)
	}
	if strings.Contains(out, "multica ") || strings.Contains(out, "issue get") {
		t.Fatalf("mismatch fell through to ordinary prompt:\n%s", out)
	}
}

func TestBuildPrompt_OrdinaryAssignmentBytesUnchanged(t *testing.T) {
	want := "You are running as a local coding agent for a Multica workspace.\n\n" +
		"Your assigned issue ID is: issue-123\n\n" +
		turnModeOwnership +
		"You were handed this issue with a handoff note. Treat it as the assigner's scoping instruction for this run; follow it before doing anything broader, and do not reply to it as if it were a comment:\n\n" +
		"> ordinary handoff\n\n" +
		"Start by running `multica issue get issue-123 --output json` to understand your task, then complete it.\n" +
		"For comment history, follow the rule in your runtime workflow file (assignment-triggered tasks treat the read as mandatory). Scan the threads first with `multica issue comment list issue-123 --roots-only --summary --output json`, then expand only what matters with `--thread <thread-id> --tail 30`. Your runtime workflow file documents the rest of the read surface, including pagination and `--since` for incremental polling.\n"
	got := BuildPrompt(Task{IssueID: "issue-123", HandoffNote: "ordinary handoff"}, "qwen")
	if got != want {
		t.Fatalf("ordinary assignment prompt bytes drifted:\n%s", got)
	}
}

func TestBuildPrompt_OrdinaryModeBytesPinned(t *testing.T) {
	fixtures := map[string]Task{
		"assignment": {IssueID: "issue-1", HandoffNote: "ordinary handoff"},
		"comment":    {IssueID: "issue-1", TriggerCommentID: "comment-1", TriggerCommentContent: "reply to this"},
		"autopilot":  {AutopilotRunID: "run-1", AutopilotTitle: "routine", AutopilotDescription: "summarize state"},
		"quick":      {QuickCreatePrompt: "create a bounded issue"},
		"chat":       {ChatSessionID: "chat-1", ChatMessage: "hello"},
		"review":     {IssueID: "issue-1", TaskKind: "review", HandoffNote: "review candidate"},
		"repair":     {IssueID: "issue-1", TaskKind: "repair", HandoffNote: "repair candidate"},
	}
	for name, task := range fixtures {
		got := fmt.Sprintf("%x", sha256.Sum256([]byte(BuildPrompt(task, "qwen"))))
		want := ordinaryPromptSHA256[name]
		if want == "" {
			t.Logf("ordinary prompt %s sha256=%s", name, got)
			continue
		}
		if got != want {
			t.Fatalf("ordinary %s prompt bytes drifted: got %s want %s", name, got, want)
		}
	}
}
