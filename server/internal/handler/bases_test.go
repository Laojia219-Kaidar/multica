package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestResolveCanonicalMachine(t *testing.T) {
	tests := []struct {
		name       string
		registered []string
		observed   string
		want       string
		wantOK     bool
	}{
		{"exact registered title", []string{"HiveCosm Mac mini"}, "HiveCosm Mac mini", "HiveCosm Mac mini", true},
		{"exact with surrounding whitespace", []string{"HiveCosm Mac mini"}, "  HiveCosm Mac mini  ", "HiveCosm Mac mini", true},
		{"detail suffix", []string{"HiveCosm Mac mini"}, "HiveCosm Mac mini · 2.1.221 (Claude Code)", "HiveCosm Mac mini", true},
		{"longest registered title wins", []string{"DGX Spark", "DGX Spark · Lab"}, "DGX Spark · Lab · v1.0", "DGX Spark · Lab", true},
		{"shorter title when longer does not match", []string{"DGX Spark", "DGX Spark · Lab"}, "DGX Spark · v1.0", "DGX Spark", true},
		{"registered title containing separator", []string{"DGX Spark · Lab"}, "DGX Spark · Lab", "DGX Spark · Lab", true},
		{"registered entry trimmed", []string{"  HiveCosm Mac mini  "}, "HiveCosm Mac mini · v9", "HiveCosm Mac mini", true},
		{"parenthesis variant rejected", []string{"HiveCosm Mac mini"}, "HiveCosm Mac mini (2)", "", false},
		{"parenthesis variant with suffix rejected", []string{"HiveCosm Mac mini"}, "HiveCosm Mac mini (2) · v1", "", false},
		{"raw prefix rejected", []string{"HiveCosm Mac mini"}, "HiveCosm", "", false},
		{"substring rejected", []string{"HiveCosm Mac mini"}, "Mac mini", "", false},
		{"superstring without separator rejected", []string{"HiveCosm"}, "HiveCosm Mac mini", "", false},
		{"unregistered title rejected", []string{"HiveCosm Mac mini"}, "Mystery Box · v1", "", false},
		{"case mismatch rejected", []string{"HiveCosm Mac mini"}, "hivecosm mac mini", "", false},
		{"separator without detail rejected", []string{"HiveCosm Mac mini"}, "HiveCosm Mac mini · ", "", false},
		{"empty observed rejected", []string{"HiveCosm Mac mini"}, "", "", false},
		{"whitespace observed rejected", []string{"HiveCosm Mac mini"}, "   ", "", false},
		{"empty registry rejected", nil, "HiveCosm Mac mini", "", false},
		{"blank registered entries skipped", []string{"", "  "}, "HiveCosm Mac mini", "", false},
	}
	for _, tt := range tests {
		got, ok := resolveCanonicalMachine(tt.registered, tt.observed)
		if got != tt.want || ok != tt.wantOK {
			t.Fatalf("%s: resolveCanonicalMachine(%v, %q) = (%q, %v), want (%q, %v)",
				tt.name, tt.registered, tt.observed, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestRegisteredCanonicalTitleAdmitsOnlyExactRegistryEntries(t *testing.T) {
	registered := []string{"HiveCosm Mac mini", "DGX Spark · Lab"}

	for _, requested := range []string{"HiveCosm Mac mini", " HiveCosm Mac mini ", "DGX Spark · Lab"} {
		if !registeredCanonicalTitle(registered, requested) {
			t.Fatalf("registeredCanonicalTitle must admit %q", requested)
		}
	}
	rejected := []string{
		"HiveCosm Mac mini · 2.1.221", // suffixed request form is not a canonical title
		"HiveCosm",                    // raw prefix
		"Mac mini",                    // substring
		"HiveCosm Mac mini (2)",       // parenthesis variant
		"Mystery Box",                 // unregistered
		"DGX Spark",                   // prefix of a longer registered title
		"",
		"   ",
	}
	for _, requested := range rejected {
		if registeredCanonicalTitle(registered, requested) {
			t.Fatalf("registeredCanonicalTitle must reject %q", requested)
		}
	}
}

func TestBaseMachineTitlesTrimsAndDeduplicates(t *testing.T) {
	bases := []db.Base{
		{Code: "A", MachineTitle: " HiveCosm Mac mini "},
		{Code: "B", MachineTitle: "HiveCosm Mac mini"},
		{Code: "C", MachineTitle: ""},
		{Code: "D", MachineTitle: "   "},
		{Code: "E", MachineTitle: "DGX Spark · Lab"},
	}
	got := baseMachineTitles(bases)
	want := []string{"HiveCosm Mac mini", "DGX Spark · Lab"}
	if len(got) != len(want) {
		t.Fatalf("baseMachineTitles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("baseMachineTitles = %v, want %v", got, want)
		}
	}
}

func TestBuildCanonicalBaseOverviewsAggregatesUnderCanonicalTitles(t *testing.T) {
	workspace := "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee"
	miniExact := "22222222-3333-4444-8555-666666666666"
	miniSuffixA := "33333333-4444-4555-8666-777777777777"
	miniSuffixB := "44444444-5555-4666-8777-888888888888"
	mbp := "55555555-6666-4777-8888-999999999999"
	runtimes := []db.AgentRuntime{
		baseRuntime(miniExact, workspace, "mini-exact", "online", "HiveCosm Mac mini", "daemon-mini"),
		baseRuntime(miniSuffixA, workspace, "mini-suffix-a", "online", "HiveCosm Mac mini · 2.1.221 (Claude Code)", "daemon-mini"),
		baseRuntime(miniSuffixB, workspace, "mini-suffix-b", "offline", "HiveCosm Mac mini · 2.2.0 (Qwen Code)", "daemon-mini"),
		baseRuntime(mbp, workspace, "mbp", "online", "HiveCrew MBP M5X · 1.0.0", "daemon-mbp"),
	}
	draining := baseAgent("11111111-2222-4333-8444-555555555555", workspace, miniSuffixA, "idle")
	draining.OperationalMode = "resting"
	mbpOnly := baseAgent("66666666-7777-4888-8999-aaaaaaaaaaaa", workspace, mbp, "idle")
	mbpOnly.OperationalMode = "resting"
	agents := []db.Agent{
		baseAgent("11111111-2222-4333-8444-555555555554", workspace, miniExact, "idle"),
		draining,
		baseAgent("11111111-2222-4333-8444-555555555556", workspace, miniSuffixB, "working"),
		mbpOnly,
	}

	overviews := buildCanonicalBaseOverviews([]string{"HiveCosm Mac mini", "HiveCrew MBP M5X"}, runtimes, agents)
	if len(overviews) != 2 {
		t.Fatalf("overviews = %d, want 2", len(overviews))
	}

	mini := overviews[0]
	if mini.MachineTitle != "HiveCosm Mac mini" ||
		mini.RuntimeRegistered != 3 ||
		mini.RuntimeOnline != 2 ||
		mini.Employees != 3 ||
		mini.Drained {
		t.Fatalf("unexpected canonical mini base: %#v", mini)
	}
	if mbpBase := overviews[1]; mbpBase.MachineTitle != "HiveCrew MBP M5X" ||
		mbpBase.RuntimeRegistered != 1 ||
		mbpBase.RuntimeOnline != 1 ||
		mbpBase.Employees != 1 ||
		!mbpBase.Drained {
		t.Fatalf("unexpected mbp base: %#v", mbpBase)
	}
}

func TestBuildCanonicalBaseOverviewsDrainedCounts(t *testing.T) {
	workspace := "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee"
	runtimeID := "22222222-3333-4444-8555-666666666666"
	runtimes := []db.AgentRuntime{
		baseRuntime(runtimeID, workspace, "base-a", "online", "Base A", ""),
	}
	registered := []string{"Base A"}

	cases := []struct {
		name  string
		modes []string
		want  bool
	}{
		{"all resting and disabled", []string{"resting", "disabled"}, true},
		{"mixed modes not drained", []string{"resting", "active"}, false},
		{"single disabled", []string{"disabled"}, true},
		{"no agents not drained", nil, false},
	}
	for _, tc := range cases {
		agents := make([]db.Agent, 0, len(tc.modes))
		for i, mode := range tc.modes {
			agent := baseAgent(agentIDForIndex(i), workspace, runtimeID, "idle")
			agent.OperationalMode = mode
			agents = append(agents, agent)
		}
		overviews := buildCanonicalBaseOverviews(registered, runtimes, agents)
		if len(overviews) != 1 || overviews[0].Drained != tc.want {
			t.Fatalf("%s: overviews = %#v, want Drained=%v", tc.name, overviews, tc.want)
		}
	}
}

func agentIDForIndex(index int) string {
	ids := []string{
		"11111111-2222-4333-8444-555555555551",
		"11111111-2222-4333-8444-555555555552",
		"11111111-2222-4333-8444-555555555553",
	}
	return ids[index]
}

func TestBuildCanonicalBaseOverviewsKeepsUnresolvedRowsVisible(t *testing.T) {
	workspace := "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee"
	exactRuntime := "22222222-3333-4444-8555-666666666666"
	parenRuntime := "33333333-4444-4555-8666-777777777777"
	emptyRuntime := "44444444-5555-4666-8777-888888888888"
	runtimes := []db.AgentRuntime{
		baseRuntime(exactRuntime, workspace, "exact", "online", "HiveCosm Mac mini", ""),
		baseRuntime(parenRuntime, workspace, "paren", "online", "HiveCosm Mac mini (2) · v1", ""),
		baseRuntime(emptyRuntime, workspace, "empty", "offline", "", ""),
	}
	agents := []db.Agent{
		baseAgent("11111111-2222-4333-8444-555555555555", workspace, parenRuntime, "idle"),
	}

	overviews := buildCanonicalBaseOverviews([]string{"HiveCosm Mac mini"}, runtimes, agents)
	if len(overviews) != 3 {
		t.Fatalf("overviews = %d, want 3", len(overviews))
	}
	canonical := overviews[0]
	if canonical.MachineTitle != "HiveCosm Mac mini" || canonical.RuntimeRegistered != 1 ||
		canonical.Employees != 0 || canonical.Drained {
		t.Fatalf("unresolved rows leaked into canonical base: %#v", canonical)
	}
	paren := overviews[1]
	if paren.MachineTitle != "HiveCosm Mac mini (2) · v1" || paren.RuntimeRegistered != 1 ||
		paren.Employees != 1 || paren.Drained {
		t.Fatalf("parenthesis variant must stay visible under its unmodified observed title: %#v", paren)
	}
	if unknown := overviews[2]; unknown.MachineTitle != "unknown" || unknown.RuntimeRegistered != 1 {
		t.Fatalf("empty device_info must fall back to unknown: %#v", unknown)
	}
}

func TestSelectBaseDrainAgentsCommandsExactAndSuffixRows(t *testing.T) {
	workspace := "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee"
	otherWorkspace := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	exactRuntime := "22222222-3333-4444-8555-666666666666"
	suffixRuntime := "33333333-4444-4555-8666-777777777777"
	parenRuntime := "44444444-5555-4666-8777-888888888888"
	crossRuntime := "55555555-6666-4777-8888-999999999999"
	runtimes := []db.AgentRuntime{
		baseRuntime(exactRuntime, workspace, "exact", "online", "HiveCosm Mac mini", ""),
		baseRuntime(suffixRuntime, workspace, "suffix", "online", "HiveCosm Mac mini · 2.1.221 (Claude Code)", ""),
		baseRuntime(parenRuntime, workspace, "paren", "online", "HiveCosm Mac mini (2)", ""),
		baseRuntime(crossRuntime, otherWorkspace, "cross", "online", "HiveCosm Mac mini · 9.9.9", ""),
	}
	inFlight := baseAgent("11111111-2222-4333-8444-555555555552", workspace, suffixRuntime, "working")
	agents := []db.Agent{
		baseAgent("11111111-2222-4333-8444-555555555551", workspace, exactRuntime, "idle"),
		inFlight,
		baseAgent("11111111-2222-4333-8444-555555555553", workspace, parenRuntime, "idle"),
		baseAgent("11111111-2222-4333-8444-555555555554", otherWorkspace, crossRuntime, "idle"),
		baseAgent("11111111-2222-4333-8444-555555555555", otherWorkspace, exactRuntime, "idle"),
		baseAgent("11111111-2222-4333-8444-555555555556", workspace, "66666666-7777-4888-8999-aaaaaaaaaaaa", "idle"),
	}

	selected := selectBaseDrainAgents([]string{"HiveCosm Mac mini"}, runtimes, agents, workspace, "HiveCosm Mac mini")
	if len(selected) != 2 {
		t.Fatalf("selected %d agents, want exactly the exact-row and suffix-row agents: %#v", len(selected), selected)
	}
	if uuidToString(selected[0].ID) != "11111111-2222-4333-8444-555555555551" {
		t.Fatalf("exact-row agent not selected first: %#v", selected[0])
	}
	if uuidToString(selected[1].ID) != "11111111-2222-4333-8444-555555555552" {
		t.Fatalf("suffix-row agent not selected: %#v", selected[1])
	}
	// In-flight safety: draining flips the claim gate for future claims; the
	// selection is read-only and never touches the running agent's state.
	if selected[1].Status != "working" {
		t.Fatalf("in-flight agent state must be untouched: %#v", selected[1])
	}
}

func TestSelectBaseDrainAgentsLongestTitleCommandsOnlyItsRows(t *testing.T) {
	workspace := "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee"
	sparkRuntime := "22222222-3333-4444-8555-666666666666"
	labRuntime := "33333333-4444-4555-8666-777777777777"
	runtimes := []db.AgentRuntime{
		baseRuntime(sparkRuntime, workspace, "spark", "online", "DGX Spark · v9", ""),
		baseRuntime(labRuntime, workspace, "lab", "online", "DGX Spark · Lab · v1", ""),
	}
	agents := []db.Agent{
		baseAgent("11111111-2222-4333-8444-555555555551", workspace, sparkRuntime, "idle"),
		baseAgent("11111111-2222-4333-8444-555555555552", workspace, labRuntime, "idle"),
	}
	registered := []string{"DGX Spark", "DGX Spark · Lab"}

	lab := selectBaseDrainAgents(registered, runtimes, agents, workspace, "DGX Spark · Lab")
	if len(lab) != 1 || uuidToString(lab[0].ID) != "11111111-2222-4333-8444-555555555552" {
		t.Fatalf("canonical longest title must command only its own rows: %#v", lab)
	}
	spark := selectBaseDrainAgents(registered, runtimes, agents, workspace, "DGX Spark")
	if len(spark) != 1 || uuidToString(spark[0].ID) != "11111111-2222-4333-8444-555555555551" {
		t.Fatalf("shorter canonical title must not command the longer title's rows: %#v", spark)
	}
}

func TestSelectBaseDrainAgentsRejectsUncommandableTitles(t *testing.T) {
	workspace := "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee"
	mysteryRuntime := "22222222-3333-4444-8555-666666666666"
	runtimes := []db.AgentRuntime{
		baseRuntime(mysteryRuntime, workspace, "mystery", "online", "Mystery Box", ""),
		baseRuntime("33333333-4444-4555-8666-777777777777", workspace, "mini", "online", "HiveCosm Mac mini", ""),
	}
	agents := []db.Agent{
		baseAgent("11111111-2222-4333-8444-555555555551", workspace, mysteryRuntime, "idle"),
	}
	registered := []string{"HiveCosm Mac mini"}

	for _, canonical := range []string{
		"Mystery Box",            // observed but unregistered: no authority
		"HiveCosm Mac mini · v1", // suffixed request form is not canonical
		"HiveCosm Mac mini (2)",  // parenthesis variant
		"",                       // empty
	} {
		if selected := selectBaseDrainAgents(registered, runtimes, agents, workspace, canonical); len(selected) != 0 {
			t.Fatalf("canonical %q must select no agents, got %#v", canonical, selected)
		}
	}
}
