package metrics

import (
	"testing"
	"time"
)

func TestWindowSince(t *testing.T) {
	now := time.Date(2026, 8, 22, 15, 30, 0, 0, time.UTC)
	s5h := WindowSince(now, "5h")
	if now.Sub(s5h) != 5*time.Hour {
		t.Fatalf("5h since = %v", now.Sub(s5h))
	}
	s7d := WindowSince(now, "7d")
	if now.Sub(s7d) != 7*24*time.Hour {
		t.Fatalf("7d since = %v", now.Sub(s7d))
	}
	sMonthly := WindowSince(now, "monthly")
	if sMonthly.Day() != 1 || sMonthly.Month() != now.Month() {
		t.Fatalf("monthly since = %v", sMonthly)
	}
}

func TestBuildQuotaWindows_ManualCapAndTaskUsage(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	obs := []UsageObservation{
		{TaskID: "t1", Model: "glm-5.2", InputTokens: 100, OutputTokens: 50, CreatedAt: now.Add(-2 * time.Hour)},
		{TaskID: "t2", Model: "glm-5.2", InputTokens: 200, OutputTokens: 0, CreatedAt: now.Add(-10 * 24 * time.Hour)},
	}
	quotas := []UsageQuotaRow{
		{ID: "q1", Provider: "智谱 · GLM", Plan: "GLM API", Account: "secure zhipu", Cycle: "5h", TotalTokens: 1000},
		{ID: "q2", Provider: "智谱 · GLM", Plan: "GLM API", Account: "secure zhipu", Cycle: "7d", TotalTokens: 5000},
		{ID: "q3", Provider: "智谱 · GLM", Plan: "GLM API", Account: "secure zhipu", Cycle: "monthly", TotalTokens: 20000},
	}

	windows := BuildQuotaWindows("智谱 · GLM", "GLM API", "secure zhipu", obs, quotas, nil, now)
	if len(windows) != 3 {
		t.Fatalf("windows = %d", len(windows))
	}
	if windows[0].Kind != "5h" || windows[0].UsedTokens != 150 {
		t.Fatalf("5h used = %d", windows[0].UsedTokens)
	}
	if windows[0].TotalTokens == nil || *windows[0].TotalTokens != 1000 {
		t.Fatalf("5h total = %v", windows[0].TotalTokens)
	}
	if windows[0].RemainingTokens == nil || *windows[0].RemainingTokens != 850 {
		t.Fatalf("5h remaining = %v", windows[0].RemainingTokens)
	}
	if windows[0].Source != "manual_cap" {
		t.Fatalf("5h source = %q", windows[0].Source)
	}
}

func TestBuildQuotaWindows_LiveVendorOverridesRemaining(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	limit := int64(1000)
	remaining := int64(250)
	used := int64(750)
	snapshots := []QuotaSnapshotRow{
		{
			ID: "s1", Provider: "MiniMax", Plan: "MiniMax API", Account: "secure minimax",
			WindowKind: "5h", LimitTokens: &limit, UsedTokens: used, RemainingTokens: &remaining,
			ObservedAt: now.Add(-2 * time.Minute), Source: "live_vendor", SourceRef: "minimax:token_plan/remains",
		},
	}
	windows := BuildQuotaWindows("MiniMax", "MiniMax API", "secure minimax", nil, nil, snapshots, now)
	if len(windows) != 3 {
		t.Fatalf("windows = %d", len(windows))
	}
	if windows[0].Source != "live_vendor" {
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

func TestMaskSecret(t *testing.T) {
	if MaskSecret("abcd1234wxyz9876") != "abcd…9876" {
		t.Fatalf("unexpected mask: %q", MaskSecret("abcd1234wxyz9876"))
	}
	if MaskSecret("short") != "****" {
		t.Fatalf("short mask wrong")
	}
}
