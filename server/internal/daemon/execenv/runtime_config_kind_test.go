package execenv

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/boundedworkspace"
	"github.com/multica-ai/multica/server/internal/notoolcanary"
)

const ordinaryRuntimeBriefSHA256 = "bfb67014018d5b07e2b214e650dcbca809700c325dcd25a708ed55a894345c4d"

var ordinaryRuntimeModeSHA256 = map[string]string{
	"issue":     ordinaryRuntimeBriefSHA256,
	"autopilot": "c49c8d34ac847539c56cc62a52cc4e7d118973ff0c9f659eb8531eca98a707d0",
	"quick":     "5a59b2ba7d04f378a9c3720b11842fe4234f2e272567a3697a8bdc95103d9c16",
	"chat":      "6d5ba263e3f7d7f036a8129997a17b323b43c096aa474956fb9fb03f7f9d574c",
}

// TestClassifyTask pins the precedence rule on classifyTask. All four
// kinds plus tiebreak cases for safety.
func TestClassifyTask(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ctx  TaskContextForEnv
		want taskKind
	}{
		{"chat", TaskContextForEnv{ChatSessionID: "c"}, kindChat},
		{"quick-create", TaskContextForEnv{QuickCreatePrompt: "p"}, kindQuickCreate},
		{"autopilot", TaskContextForEnv{AutopilotRunID: "r"}, kindAutopilotRunOnly},
		{"issue-comment-triggered", TaskContextForEnv{IssueID: "i", TriggerCommentID: "c"}, kindIssue},
		{"issue-assignment-triggered", TaskContextForEnv{IssueID: "i"}, kindIssue},
		{"issue-bare", TaskContextForEnv{}, kindIssue},
		{"tiebreak-chat-vs-quick", TaskContextForEnv{ChatSessionID: "c", QuickCreatePrompt: "p"}, kindChat},
		{"tiebreak-quick-vs-autopilot", TaskContextForEnv{QuickCreatePrompt: "p", AutopilotRunID: "r"}, kindQuickCreate},
		{"tiebreak-autopilot-vs-comment", TaskContextForEnv{AutopilotRunID: "r", IssueID: "i", TriggerCommentID: "c"}, kindAutopilotRunOnly},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyTask(tc.ctx); got != tc.want {
				t.Errorf("classifyTask: got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestBuildMetaSkillContentNoToolCanarySuppressesToolWorkflow(t *testing.T) {
	marker, err := notoolcanary.CanonicalMarker("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	out := buildMetaSkillContent("qwen", TaskContextForEnv{
		IssueID:     notoolcanary.IssueID,
		TaskKind:    notoolcanary.TaskKind,
		HandoffNote: marker,
	})
	if out != notoolcanary.RuntimeBrief(notoolcanary.Valid, notoolcanary.Contract{DeliveryPrefix: notoolcanary.DeliveryPrefix}) {
		t.Fatalf("runtime brief is not the closed no-tool brief:\n%s", out)
	}
	for _, forbidden := range []string{"multica ", "issue get", "comment add", "Available Commands", "Workflow", "MCP", "Repositories", "Skills"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("closed no-tool runtime brief contains forbidden mandate %q:\n%s", forbidden, out)
		}
	}
}

func TestBuildMetaSkillContentNoToolMarkerMismatchRejectsWithoutFallback(t *testing.T) {
	marker, err := notoolcanary.CanonicalMarker("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	out := buildMetaSkillContent("qwen", TaskContextForEnv{
		IssueID:     notoolcanary.IssueID,
		TaskKind:    "review",
		HandoffNote: marker,
	})
	if out != notoolcanary.RuntimeBrief(notoolcanary.Invalid, notoolcanary.Contract{}) {
		t.Fatalf("mismatch did not take fixed rejection brief:\n%s", out)
	}
	if strings.Contains(out, "multica ") || strings.Contains(out, "Available Commands") {
		t.Fatalf("mismatch fell through to ordinary runtime brief:\n%s", out)
	}
}

func TestBuildMetaSkillContentBoundedWorkspaceSuppressesToolWorkflow(t *testing.T) {
	marker, err := boundedworkspace.CanonicalMarker(
		"01234567-89ab-cdef-0123-456789abcdef",
		"11234567-89ab-cdef-0123-456789abcdef",
		"21234567-89ab-cdef-0123-456789abcdef",
		"Edit the named pilot fixture.",
		boundedworkspace.WorktreeRoot+"pilot",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	)
	if err != nil {
		t.Fatal(err)
	}
	out := buildMetaSkillContent("qwen", TaskContextForEnv{
		TaskID:      "01234567-89ab-cdef-0123-456789abcdef",
		IssueID:     "11234567-89ab-cdef-0123-456789abcdef",
		WorkspaceID: "21234567-89ab-cdef-0123-456789abcdef",
		TaskKind:    boundedworkspace.TaskKind,
		HandoffNote: marker,
	})
	for _, forbidden := range []string{"multica issue", "comment add", "Available Commands", "run_shell_command"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("bounded workspace runtime brief contains %q:\n%s", forbidden, out)
		}
	}
	for _, required := range []string{"edit", "write_file", "fixed trusted runner"} {
		if !strings.Contains(out, required) {
			t.Fatalf("bounded workspace runtime brief missing %q:\n%s", required, out)
		}
	}
}

func TestInjectRuntimeConfigNoToolCanaryWritesOnlyClosedQwenBrief(t *testing.T) {
	marker, err := notoolcanary.CanonicalMarker("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	content, err := InjectRuntimeConfig(dir, "qwen", TaskContextForEnv{
		IssueID:     notoolcanary.IssueID,
		TaskKind:    notoolcanary.TaskKind,
		HandoffNote: marker,
	})
	if err != nil {
		t.Fatalf("InjectRuntimeConfig: %v", err)
	}
	physical, err := os.ReadFile(filepath.Join(dir, "QWEN.md"))
	if err != nil {
		t.Fatalf("read QWEN.md: %v", err)
	}
	if !strings.Contains(string(physical), content) {
		t.Fatal("physical QWEN.md does not contain the exact closed runtime brief")
	}
	for _, forbidden := range []string{"multica ", "issue get", "comment add", "Available Commands", "Workflow", "MCP", "Repositories", "Skills"} {
		if strings.Contains(string(physical), forbidden) {
			t.Fatalf("physical QWEN.md contains forbidden mandate %q:\n%s", forbidden, physical)
		}
	}
}

func TestBuildMetaSkillContentOrdinaryBytesPinned(t *testing.T) {
	out := buildMetaSkillContent("qwen", TaskContextForEnv{IssueID: "issue-1"})
	got := fmt.Sprintf("%x", sha256.Sum256([]byte(out)))
	if ordinaryRuntimeBriefSHA256 == "" {
		t.Logf("ordinary runtime brief sha256=%s", got)
		return
	}
	if got != ordinaryRuntimeBriefSHA256 {
		t.Fatalf("ordinary runtime brief bytes drifted: got %s want %s", got, ordinaryRuntimeBriefSHA256)
	}
}

func TestBuildMetaSkillContentOrdinaryModeBytesPinned(t *testing.T) {
	fixtures := map[string]TaskContextForEnv{
		"issue":     {IssueID: "issue-1"},
		"autopilot": {AutopilotRunID: "run-1", AutopilotTitle: "routine", AutopilotDescription: "summarize state"},
		"quick":     {QuickCreatePrompt: "create a bounded issue"},
		"chat":      {ChatSessionID: "chat-1"},
	}
	for name, ctx := range fixtures {
		got := fmt.Sprintf("%x", sha256.Sum256([]byte(buildMetaSkillContent("qwen", ctx))))
		want := ordinaryRuntimeModeSHA256[name]
		if want == "" {
			t.Logf("ordinary runtime %s sha256=%s", name, got)
			continue
		}
		if got != want {
			t.Fatalf("ordinary %s runtime bytes drifted: got %s want %s", name, got, want)
		}
	}
}

// TestTaskKindHasIssueContext pins the predicate that gates Project
// Context / Issue Metadata / Sub-issue Creation in the slim dispatcher.
func TestTaskKindHasIssueContext(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind taskKind
		want bool
	}{
		{kindIssue, true},
		{kindAutopilotRunOnly, false},
		{kindQuickCreate, false},
		{kindChat, false},
	}
	for _, tc := range cases {
		if got := tc.kind.hasIssueContext(); got != tc.want {
			t.Errorf("kind=%d hasIssueContext: got %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// TestBuildMetaSkillContentBriefContent pins that buildMetaSkillContent
// renders the (now sole) brief: the `issue get` one-liner is present and
// the retired legacy verbose description is not.
func TestBuildMetaSkillContentBriefContent(t *testing.T) {
	t.Parallel()

	out := buildMetaSkillContent("claude", TaskContextForEnv{
		IssueID:          "issue-1",
		TriggerCommentID: "comment-1",
		AgentName:        "Eve",
		AgentID:          "eve-1",
	})

	if !strings.Contains(out, "- `multica issue get <id> --output json` — full issue.\n") {
		t.Errorf("brief is missing the `issue get` one-liner\n---\n%s", out)
	}
	if strings.Contains(out, "Get full issue details.") {
		t.Errorf("brief still carries the retired legacy `issue get` description\n---\n%s", out)
	}
}

// TestBuildMetaSkillContentSlimKindMatrix locks in which sections the
// slim brief emits per task kind, machine-checking the matrix documented
// on `buildMetaSkillContentSlim`. Heading is matched as a discrete line
// (preceded by newline + followed by newline) so inline references like
// "see ## Comment Formatting" do not trip the absence assertions.
func TestBuildMetaSkillContentSlimKindMatrix(t *testing.T) {

	baseRepo := []RepoContextForEnv{{URL: "https://example.com/x.git", Description: "x"}}
	baseSkill := []SkillContextForEnv{{Name: "skill-x", Description: "x"}}

	type sectionCheck struct {
		heading  string
		mustHave map[taskKind]bool
	}
	allKinds := map[taskKind]bool{
		kindIssue: true, kindAutopilotRunOnly: true,
		kindQuickCreate: true, kindChat: true,
	}
	issueKinds := map[taskKind]bool{kindIssue: true}
	checks := []sectionCheck{
		{"# Multica Agent Runtime", allKinds},
		{"## Background Task Safety", allKinds},
		{"## Agent Identity", allKinds},
		{"## Available Commands", allKinds},
		{"### Workflow", allKinds},
		{"## Important: Always Use the `multica` CLI", allKinds},
		{"## Output", allKinds},
		{"## Comment Formatting", issueKinds},
		{"## Repositories", map[taskKind]bool{
			kindIssue: true, kindAutopilotRunOnly: true, kindChat: true,
		}},
		{"## Issue Metadata", issueKinds},
		{"## Instruction Precedence", issueKinds},
		{"## Sub-issue Creation", issueKinds},
		{"## Skills", map[taskKind]bool{
			kindIssue: true, kindAutopilotRunOnly: true, kindChat: true,
		}},
		{"## Mentions", issueKinds},
		{"## Attachments", issueKinds},
	}

	fixtures := map[taskKind]TaskContextForEnv{
		kindChat: {ChatSessionID: "c-1", AgentName: "Eve", AgentID: "eve-1",
			Repos: baseRepo, AgentSkills: baseSkill},
		kindQuickCreate: {QuickCreatePrompt: "p", AgentName: "Eve", AgentID: "eve-1",
			Repos: baseRepo, AgentSkills: baseSkill},
		kindAutopilotRunOnly: {AutopilotRunID: "r-1", AgentName: "Eve", AgentID: "eve-1",
			Repos: baseRepo, AgentSkills: baseSkill},
		kindIssue: {IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1",
			Repos: baseRepo, AgentSkills: baseSkill},
	}

	for kind, ctx := range fixtures {
		out := buildMetaSkillContent("claude", ctx)
		for _, c := range checks {
			needle := "\n" + c.heading + "\n"
			firstLine := c.heading + "\n"
			present := strings.HasPrefix(out, firstLine) || strings.Contains(out, needle)
			want := c.mustHave[kind]
			if want && !present {
				t.Errorf("kind=%d: expected heading %q in slim brief", kind, c.heading)
			}
			if !want && present {
				t.Errorf("kind=%d: heading %q should NOT be in slim brief (matrix gating regression)", kind, c.heading)
			}
		}
	}
}

// TestSlimQuickCreateAvailableCommands locks the minimal-variant content
// for quick-create's Available Commands: `issue create` present, every
// other Core command absent (the hard guardrails forbid the call).
func TestSlimQuickCreateAvailableCommands(t *testing.T) {

	out := buildMetaSkillContent("codex", TaskContextForEnv{
		QuickCreatePrompt: "create an issue about flaky tests",
		AgentName:         "Eve", AgentID: "eve-1",
	})

	for _, want := range []string{
		"## Available Commands",
		"multica issue create --title",
		"`multica --help`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("quick_create slim Available Commands missing %q", want)
		}
	}

	for _, banned := range []string{
		"multica issue get <id>",
		"multica issue comment list <issue-id>",
		"multica issue update <id>",
		"multica issue status <id> <status>",
		"multica issue comment add <issue-id>",
		"multica issue metadata list <issue-id>",
		"multica issue metadata set <issue-id>",
		"multica issue metadata delete <issue-id>",
		"multica issue children <id>",
		"multica repo checkout <url>",
		"### Squad maintenance",
		"multica squad member set-role",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("quick_create slim Available Commands should NOT advertise %q (hard guardrails forbid the call)", banned)
		}
	}
}

// TestBackgroundTaskSafetySlimHardPins asserts the slim brief carries the
// same hardened Background Task Safety pins as the legacy brief (MUL-4140).
// The verbose path is covered by
// TestInjectRuntimeConfigBackgroundTaskSafetyProviderAgnostic; this locks
// the compressed slim path so a future slim-brief trim can't quietly drop
// the no-background-and-yield / no-"standing by" guardrails that address
// the MUL-4091 mechanism.
func TestBackgroundTaskSafetySlimHardPins(t *testing.T) {

	out := buildMetaSkillContent("claude", TaskContextForEnv{
		IssueID: "i-1", TriggerCommentID: "tc-1",
		AgentName: "Eve", AgentID: "eve-1",
	})

	for _, want := range []string{
		"## Background Task Safety",
		"Do NOT end your turn while background tasks",
		"wait for a future notification/reminder",
		"run the work synchronously instead",
		"Never background-and-yield",
		"foreground tool call that blocks",
		// MUL-5274: an explicitly requested persistent local service is a
		// completed handoff, not unfinished run-owned work. Pin the narrow
		// exception and its readiness / cleanup / honesty requirements.
		"persistent service handoff",
		"running service itself is the requested deliverable",
		"stdio redirected to durable logs",
		"PID/profile",
		"verify readiness before replying",
		"survival as best-effort, not guaranteed",
		"does not cover tests, builds, CI polling",
		"are not agent-owned background tasks",
		"GitHub Actions after a successful push",
		"Do not wait for them by default",
		// MUL-5223 pins: named tool-shape bans, merge requirements
		// denied as acceptance criteria, replacement hand-off phrasing,
		// and the scoped escape hatch that keeps an explicitly requested
		// CI result both permitted and executable.
		"do NOT run `gh pr checks --watch`",
		"any sleep / retry loop that polls check status",
		"NOT your delivery acceptance criteria",
		"CI running: <PR link>",
		"unless the explicit exception below applies",
		"The one exception",
		"ONE foreground blocking call (`gh pr checks <pr> --watch`)",
		"running in the background so you can keep working",
		"standing by",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("slim Background Task Safety missing hardened pin %q\n---\n%s", want, out)
		}
	}
	// `gh run watch` may only appear as a banned command, never as the
	// section's example of how to wait properly.
	if strings.Contains(out, "e.g. `gh run watch`") {
		t.Errorf("slim Background Task Safety should not suggest waiting for external GitHub CI\n---\n%s", out)
	}
	// MUL-5274 review: with the persistent-service exception in the list, a
	// "The rules above ..." scoping sentence would sweep in work that is
	// precisely no longer run-owned after handoff.
	if strings.Contains(out, "The rules above") {
		t.Errorf("slim Background Task Safety must not reintroduce the ambiguous \"The rules above\" scoping sentence\n---\n%s", out)
	}
}
