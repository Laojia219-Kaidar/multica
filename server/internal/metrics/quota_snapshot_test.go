package metrics

import (
	"testing"
	"time"
)

func TestBuildQuotaWindows_ManualCapAndTaskUsage(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	obs := []UsageObservation{
		{TaskID: "t1", Model: "glm-5.2", InputTokens: 100, OutputTokens: 50, CreatedAt: now.Add(-2 * time.Hour)},
	}
	quotas := []UsageQuotaRow{
		{ID: "q1", Provider: "智谱 · GLM", Plan: "GLM Coding Max V1", Account: "secure zhipu", Cycle: "5h", TotalTokens: 1000},
	}

	windows := BuildQuotaWindows("智谱 · GLM", "GLM Coding Max V1", "secure zhipu", obs, quotas, nil, now)
	if len(windows) != 3 {
		t.Fatalf("windows = %d", len(windows))
	}
	if windows[0].Kind != "5h" {
		t.Fatalf("first window = %q", windows[0].Kind)
	}
}

func TestBuildQuotaWindows_LiveVendorOverridesRemaining(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	limit := int64(1000)
	remaining := int64(250)
	used := int64(750)
	pct := 75.0
	snapshots := []QuotaSnapshotRow{
		{
			ID: "s1", Provider: "MiniMax", Plan: "TokenPlanPlus", Account: "secure minimax",
			WindowKind: "5h", LimitTokens: &limit, UsedTokens: used, RemainingTokens: &remaining,
			Percentage: &pct, Unit: "percent", ObservedAt: now.Add(-2 * time.Minute), Source: "console",
			SourceRef: "minimax:console:5h",
		},
	}
	windows := BuildQuotaWindows("MiniMax", "TokenPlanPlus", "secure minimax", nil, nil, snapshots, now)
	if windows[0].Source != "console" {
		t.Fatalf("source = %q", windows[0].Source)
	}
	if windows[0].RemainingTokens == nil || *windows[0].RemainingTokens != 250 {
		t.Fatalf("remaining = %v", windows[0].RemainingTokens)
	}
}

func TestBuildWorkQuotaSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	obs := []UsageObservation{
		{
			Model: "minimax-m2.7", RuntimeName: "secure minimax", RuntimeProvider: "qwen",
			RuntimeMode: "cloud", InputTokens: 10, CreatedAt: now,
		},
	}
	snap := BuildWorkQuotaSnapshot("ws-1", obs, nil, nil, now)
	if snap.WorkspaceID != "ws-1" || len(snap.Providers) == 0 {
		t.Fatalf("snapshot empty: %+v", snap)
	}
}
