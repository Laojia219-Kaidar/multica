package orcabridge

import (
	"errors"
	"strings"
	"testing"
)

func validChain() Chain {
	return Chain{
		WorkspaceID:  "c05a0000-0000-4000-8000-000000000001",
		ProjectID:    "c05a0000-0000-4000-8000-000000000002",
		IssueID:      "c05a0000-0000-4000-8000-000000000003",
		TaskID:       "c05a0000-0000-4000-8000-000000000004",
		AssignmentID: "c05a0000-0000-4000-8000-000000000005",
	}
}

func TestChainValidationFailClosed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Chain)
		scope  func(Chain) error
	}{
		{"empty workspace on project scope", func(c *Chain) { c.WorkspaceID = "" }, Chain.ValidateProjectScope},
		{"uppercase workspace", func(c *Chain) { c.WorkspaceID = strings.ToUpper(c.WorkspaceID) }, Chain.ValidateProjectScope},
		{"unknown UUID version rejected", func(c *Chain) { c.ProjectID = "c05a0000-0000-9000-8000-000000000002" }, Chain.ValidateProjectScope},
		{"missing issue on task scope", func(c *Chain) { c.IssueID = "" }, Chain.ValidateTaskScope},
		{"missing task on assignment scope", func(c *Chain) { c.TaskID = "" }, Chain.ValidateAssignmentScope},
		{"missing assignment on assignment scope", func(c *Chain) { c.AssignmentID = "" }, Chain.ValidateAssignmentScope},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chain := validChain()
			tc.mutate(&chain)
			if err := tc.scope(chain); !errors.Is(err, ErrInvalidChain) {
				t.Fatalf("expected ErrInvalidChain, got %v", err)
			}
		})
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	chain := validChain()
	objective := ProjectRunObjective("Ship the A1 bridge", chain)
	ws, prj, ok := RunMarkerScan(objective)
	if !ok || ws != chain.WorkspaceID || prj != chain.ProjectID {
		t.Fatalf("run marker scan failed on %q: %q %q %v", objective, ws, prj, ok)
	}
	if !strings.HasSuffix(objective, chain.RunMarker()) {
		t.Fatalf("objective must end with the marker: %q", objective)
	}

	spec := TaskSpec("Implement X", chain)
	ws2, prj2, issue, task, ok := TaskMarkerScan(spec)
	if !ok || ws2 != chain.WorkspaceID || prj2 != chain.ProjectID || issue != chain.IssueID || task != chain.TaskID {
		t.Fatalf("task marker scan failed: %+v", map[string]any{"ws": ws2, "prj": prj2, "issue": issue, "task": task, "ok": ok})
	}
	if !strings.HasPrefix(spec, chain.TaskMarker()) {
		t.Fatalf("spec must start with the marker: %q", spec)
	}
}

func TestMarkerScanRejectsForeignText(t *testing.T) {
	if _, _, ok := RunMarkerScan("plain objective without marker"); ok {
		t.Fatal("foreign objective must not scan")
	}
	if _, _, _, _, ok := TaskMarkerScan("no marker\nbody"); ok {
		t.Fatal("foreign spec must not scan")
	}
	// A marker with missing fields must not scan.
	broken := MarkerPrefix + " ws=" + validChain().WorkspaceID + "]"
	if _, _, ok := RunMarkerScan("objective " + broken); ok {
		t.Fatal("marker without prj must not scan")
	}
}

func TestProjectRunObjectiveDefaults(t *testing.T) {
	chain := validChain()
	objective := ProjectRunObjective("   ", chain)
	if !strings.Contains(objective, "HiveCrew project orchestration") {
		t.Fatalf("default objective missing: %q", objective)
	}
}

func TestTaskSpecDefaults(t *testing.T) {
	spec := TaskSpec("", validChain())
	if !strings.Contains(spec, "Execute the HiveCrew task") {
		t.Fatalf("default instructions missing: %q", spec)
	}
}

func TestDigestsAreStableAndInputSensitive(t *testing.T) {
	chain := validChain()
	one, err := ObjectiveInput{WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID, DisplayObjective: "a"}.Digest()
	if err != nil {
		t.Fatal(err)
	}
	same, err := ObjectiveInput{WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID, DisplayObjective: "a"}.Digest()
	if err != nil {
		t.Fatal(err)
	}
	other, err := ObjectiveInput{WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID, DisplayObjective: "b"}.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if one != same {
		t.Fatal("equal inputs must digest equal")
	}
	if one == other {
		t.Fatal("different inputs must digest differently")
	}
	if !strings.HasPrefix(one, "sha256:") || len(one) != len("sha256:")+64 {
		t.Fatalf("digest format wrong: %q", one)
	}
	// Defaulted contract version must not change the digest.
	explicit, err := ObjectiveInput{ContractVersion: ContractVersion, WorkspaceID: chain.WorkspaceID, ProjectID: chain.ProjectID, DisplayObjective: "a"}.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if explicit != one {
		t.Fatal("contract version defaulting must not drift the digest")
	}
}

func TestPlacementValidation(t *testing.T) {
	base := PlacementInput{
		WorkspaceID:  validChain().WorkspaceID,
		AssignmentID: validChain().AssignmentID,
		WorktreeMode: "new-child",
		WorktreeName: "hivecrew-run-1",
		RepoSelector: "path:/repo",
		Agent:        "codex",
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid placement rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*PlacementInput)
	}{
		{"worktree name missing for new-child", func(p *PlacementInput) { p.WorktreeName = "" }},
		{"worktree name with slash", func(p *PlacementInput) { p.WorktreeName = "a/b" }},
		{"repo selector missing", func(p *PlacementInput) { p.RepoSelector = "" }},
		{"agent missing", func(p *PlacementInput) { p.Agent = "" }},
		{"agent looks like flag", func(p *PlacementInput) { p.Agent = "--dangerous" }},
		{"effort without model", func(p *PlacementInput) { p.Effort = "high" }},
		{"unsupported mode", func(p *PlacementInput) { p.WorktreeMode = "new-orbit" }},
		{"current mode with new name", func(p *PlacementInput) { p.WorktreeMode = "current"; p.WorktreeName = "x" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			placement := base
			tc.mutate(&placement)
			if err := placement.Validate(); !errors.Is(err, ErrInvalidChain) {
				t.Fatalf("expected ErrInvalidChain, got %v", err)
			}
		})
	}
	current := base
	current.WorktreeMode = "current"
	current.WorktreeName = ""
	current.RepoSelector = ""
	if err := current.Validate(); err != nil {
		t.Fatalf("current worktree placement rejected: %v", err)
	}
}

func TestOrcaHandleGrammarGuards(t *testing.T) {
	valid := map[string]func(string) error{
		"run_8b4856eda7b4":                          ValidateOrcaRunID,
		"task_6cc7b1e49533":                         ValidateOrcaTaskID,
		"ctx_9ca8c0c81a71":                          ValidateOrcaDispatchID,
		"term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e": ValidateOrcaTerminalHandle,
		"msg_577b8448d366":                          ValidateOrcaMessageID,
		"wtr_908bfe359364":                          ValidateOrcaWorktreeID,
	}
	for handle, validate := range valid {
		if err := validate(handle); err != nil {
			t.Fatalf("valid handle %q rejected: %v", handle, err)
		}
	}
	invalid := []string{"", "--flag", "run_abc task", "RUN_1", "task_", "ctx_9 --x", "term_nothex", "msg_1;rm"}
	for _, handle := range invalid {
		for _, validate := range []func(string) error{ValidateOrcaRunID, ValidateOrcaTaskID, ValidateOrcaDispatchID, ValidateOrcaTerminalHandle, ValidateOrcaMessageID, ValidateOrcaWorktreeID} {
			if err := validate(handle); err == nil {
				t.Fatalf("invalid handle %q accepted", handle)
			}
		}
	}
}
