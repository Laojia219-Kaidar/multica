package metrics

import (
	"testing"
	"time"
)

func TestPlanQuotaProfile_DualArkPlans(t *testing.T) {
	agent, ok := LookupPlanQuotaProfile("火山引擎 · Doubao", "Ark Agent Plan")
	if !ok || len(agent.Windows) != 3 {
		t.Fatalf("agent profile = %+v", agent)
	}
	coding, ok := LookupPlanQuotaProfile("火山引擎 · Doubao", "Ark Coding Plan")
	if !ok || len(coding.Windows) != 3 {
		t.Fatalf("coding profile = %+v", coding)
	}
	if agent.Windows[0].Kind != "5h" || coding.Windows[0].Kind != "session" {
		t.Fatalf("agent=%v coding=%v", agent.Windows[0], coding.Windows[0])
	}
}

func TestPlanQuotaProfile_KimiThreeSurfaces(t *testing.T) {
	membership, ok := LookupPlanQuotaProfile("月之暗面 · Kimi", "Kimi Membership Allegro")
	if !ok || len(membership.Windows) != 3 {
		t.Fatalf("membership = %+v", membership)
	}
	code, ok := LookupPlanQuotaProfile("月之暗面 · Kimi", "Kimi Code Console")
	if !ok || len(code.Windows) != 2 {
		t.Fatalf("code = %+v", code)
	}
	prepaid, ok := LookupPlanQuotaProfile("月之暗面 · Kimi", "Kimi Prepaid CNY")
	if !ok || len(prepaid.Windows) != 1 || prepaid.Windows[0].Unit != "cny" {
		t.Fatalf("prepaid = %+v", prepaid)
	}
}

func TestBuildQuotaWindows_ConsoleOverridesLocalTaskUsage(t *testing.T) {
	now := time.Date(2026, 8, 23, 4, 0, 0, 0, time.UTC)
	localObs := []UsageObservation{
		{InputTokens: 103_700_000, CreatedAt: now.Add(-24 * time.Hour)},
	}
	pct := 72.5
	used := int64(337_200_000)
	limit := int64(465_000_000)
	remaining := int64(127_800_000)
	snapshots := []QuotaSnapshotRow{
		{
			ID: "s1", Provider: "智谱 · GLM", Plan: "GLM Coding Max V1", Account: "secure zhipu",
			WindowKind: "7d", LimitTokens: &limit, UsedTokens: used, RemainingTokens: &remaining,
			Percentage: &pct, Unit: "tokens", ObservedAt: now, Source: "console",
			SourceRef: "zhipu:console:7d-tokens",
		},
	}
	windows := BuildQuotaWindows("智谱 · GLM", "GLM Coding Max V1", "secure zhipu", localObs, nil, snapshots, now)
	var sevenDay *QuotaWindowView
	for i := range windows {
		if windows[i].Kind == "7d" {
			sevenDay = &windows[i]
			break
		}
	}
	if sevenDay == nil {
		t.Fatal("7d window missing")
	}
	if sevenDay.Source != "console" {
		t.Fatalf("source = %q", sevenDay.Source)
	}
	if sevenDay.UsedTokens != used {
		t.Fatalf("console used = %d, want %d (local was 103.7M)", sevenDay.UsedTokens, used)
	}
	if sevenDay.RemainingTokens == nil || *sevenDay.RemainingTokens != remaining {
		t.Fatalf("remaining = %v", sevenDay.RemainingTokens)
	}
}

func TestBuildQuotaWindows_ZhipuNoWeeklyWindow(t *testing.T) {
	now := time.Now().UTC()
	windows := BuildQuotaWindows("智谱 · GLM", "GLM Coding Max V1", "secure zhipu", nil, nil, nil, now)
	kinds := map[string]bool{}
	for _, w := range windows {
		kinds[w.Kind] = true
	}
	if kinds["7d"] != true || kinds["5h"] != true || kinds["mcp_monthly"] != true {
		t.Fatalf("windows = %v", kinds)
	}
	if len(windows) != 3 {
		t.Fatalf("expected 3 windows, got %d", len(windows))
	}
}

func TestPlanWindowsFor_ExcludesCodexClaude(t *testing.T) {
	if w := PlanWindowsFor("OpenAI · Codex", "Codex Plan"); w != nil {
		t.Fatalf("codex should have no quota windows")
	}
	if w := PlanWindowsFor("Anthropic · Claude", "Claude Plan"); w != nil {
		t.Fatalf("claude should have no quota windows")
	}
}

func TestClassifyProviderPlan_ArkAndKimiPlans(t *testing.T) {
	agent := ClassifyProviderPlan("doubao-pro", "volcengine-agent", "qwen", "cloud")
	if agent.Plan != "Ark Agent Plan" {
		t.Fatalf("agent plan = %q", agent.Plan)
	}
	coding := ClassifyProviderPlan("doubao-coder", "volcengine-coding", "qwen", "cloud")
	if coding.Plan != "Ark Coding Plan" {
		t.Fatalf("coding plan = %q", coding.Plan)
	}
	kimi := ClassifyProviderPlan("k3", "secure kimi", "kimi", "cloud")
	if kimi.Plan != "Kimi Membership Allegro" {
		t.Fatalf("kimi plan = %q", kimi.Plan)
	}
}
