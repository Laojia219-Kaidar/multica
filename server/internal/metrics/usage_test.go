package metrics

import (
	"testing"
	"time"
)

func obs(provider, model, taskID, issueID, agentID, agentName, rtProvider, rtName, rtMode string, in, out, cacheRead, cacheWrite int64) UsageObservation {
	var issue *string
	if issueID != "" {
		issue = &issueID
	}
	return UsageObservation{
		Provider:         provider,
		Model:            model,
		TaskID:           taskID,
		IssueID:          issue,
		AgentID:          agentID,
		AgentName:        agentName,
		RuntimeProvider:  rtProvider,
		RuntimeName:      rtName,
		RuntimeMode:      rtMode,
		InputTokens:      in,
		OutputTokens:     out,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
	}
}

func TestClassifyProviderPlan_LocalModel(t *testing.T) {
	c := ClassifyProviderPlan("qwen3.6-27b-nvfp4", "dgx-local", "qwen", "local")
	if c.Provider != "本地模型" && c.Provider != "本地模型 · DGX" {
		t.Fatalf("local model classified under wrong provider: %q", c.Provider)
	}
	if !c.LocalModel {
		t.Fatalf("local model not flagged local")
	}

	// DGX checkpoint carried over a qwen carrier, cloud runtime mode.
	c2 := ClassifyProviderPlan("qwen3.6-27b-nvfp4", "dgx-local", "qwen", "cloud")
	if c2.Provider != "本地模型 · DGX" {
		t.Fatalf("DGX checkpoint classified under %q", c2.Provider)
	}
}

func TestClassifyProviderPlan_ProviderFamilies(t *testing.T) {
	cases := []struct {
		name            string
		model           string
		runtimeName     string
		runtimeProvider string
		wantProvider    string
	}{
		{"codex", "gpt-5.6-sol", "codex", "codex", "OpenAI · Codex"},
		{"claude", "claude-sonnet-4", "claude", "claude", "Anthropic · Claude"},
		{"kimi", "k3", "secure kimi", "kimi", "月之暗面 · Kimi"},
		{"deepseek", "deepseek-v4-flash", "secure deepseek", "qwen", "DeepSeek"},
		{"zhipu", "glm-5.2", "secure zhipu", "qwen", "智谱 · GLM"},
		{"mimo", "mimo-v2.5-pro", "secure mimo", "qwen", "小米 · MiMo"},
		{"unknown-empty", "", "", "", "未识别提供商"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := ClassifyProviderPlan(tc.model, tc.runtimeName, tc.runtimeProvider, "cloud")
			if c.Provider != tc.wantProvider {
				t.Fatalf("provider = %q, want %q", c.Provider, tc.wantProvider)
			}
		})
	}
}

func TestBuildUsageHierarchy_RollsUpCorrectly(t *testing.T) {
	ws := "11111111-1111-4111-8111-111111111111"
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	rows := []UsageObservation{
		// Two tasks from the same qwen employee+model; one cache-read token.
		obs("qwen", "qwen3.7-plus", "t1", "i1", "a1", "Alice", "qwen", "qwen-token", "cloud", 100, 20, 5, 0),
		obs("qwen", "qwen3.7-plus", "t2", "i1", "a1", "Alice", "qwen", "qwen-token", "cloud", 50, 10, 0, 0),
		// A different employee, different model, same provider+plan.
		obs("qwen", "glm-5.2", "t3", "i2", "a2", "Bob", "qwen", "qwen-token", "cloud", 200, 40, 0, 0),
		// A codex task.
		obs("codex", "gpt-5.6-sol", "t4", "i3", "a1", "Alice", "codex", "codex", "cloud", 300, 60, 0, 0),
		// A local model task.
		obs("qwen", "qwen3.6-27b-nvfp4", "t5", "i4", "a3", "Coco", "qwen", "dgx-local", "local", 500, 100, 0, 0),
	}

	quotas := []UsageQuotaRow{
		{
			ID:          "q1",
			WorkspaceID: ws,
			Provider:    "阿里云 · Qwen",
			Plan:        "Qwen Token Plan",
			Account:     "qwen-token",
			Cycle:       "monthly",
			TotalTokens: 1000,
		},
	}

	h := BuildUsageHierarchy(ws, since, rows, quotas)

	if h.Totals.UsedTokens != 100+20+5+50+10+200+40+300+60+500+100 {
		t.Fatalf("total used tokens = %d", h.Totals.UsedTokens)
	}
	if h.Totals.TaskCount != 5 {
		t.Fatalf("task count = %d, want 5", h.Totals.TaskCount)
	}
	if h.Totals.EmployeeCount != 3 {
		t.Fatalf("employee count = %d, want 3", h.Totals.EmployeeCount)
	}

	byProvider := map[string]ProviderUsage{}
	for _, p := range h.Providers {
		byProvider[p.Provider] = p
	}

	qwen := byProvider["阿里云 · Qwen"]
	if qwen.UsedTokens != 100+20+5+50+10+200+40 {
		t.Fatalf("qwen provider used = %d", qwen.UsedTokens)
	}
	if len(qwen.Plans) != 1 {
		t.Fatalf("qwen plan count = %d, want 1", len(qwen.Plans))
	}
	plan := qwen.Plans[0]
	if plan.Plan != "Qwen Token Plan" {
		t.Fatalf("plan = %q", plan.Plan)
	}
	if plan.Quota == nil {
		t.Fatalf("configured quota not merged")
	}
	if plan.Quota.TotalTokens == nil || *plan.Quota.TotalTokens != 1000 {
		t.Fatalf("quota total = %v", plan.Quota.TotalTokens)
	}
	if plan.Quota.Remaining == nil || *plan.Quota.Remaining != 1000-plan.UsedTokens {
		t.Fatalf("quota remaining = %v", plan.Quota.Remaining)
	}
	if plan.Quota.Percentage == nil || *plan.Quota.Percentage <= 0 {
		t.Fatalf("quota percentage = %v", plan.Quota.Percentage)
	}
	if plan.Quota.ResetAt == nil {
		t.Fatalf("monthly quota reset time missing")
	}

	// Provider-level model/employee/task counts must sum to the plan totals.
	var planModels int64
	for _, m := range plan.Models {
		planModels += m.UsedTokens
		if m.Model == "qwen3.7-plus" {
			if m.EmployeeCount != 1 || m.TaskCount != 2 {
				t.Fatalf("qwen3.7-plus employee=%d task=%d", m.EmployeeCount, m.TaskCount)
			}
		}
	}
	if planModels != plan.UsedTokens {
		t.Fatalf("plan model tokens %d != plan used %d", planModels, plan.UsedTokens)
	}

	// Local model provider present and flagged.
	local := byProvider["本地模型 · DGX"]
	if !local.LocalModel {
		t.Fatalf("local provider not flagged local")
	}
	if local.UsedTokens != 600 {
		t.Fatalf("local used = %d", local.UsedTokens)
	}

	// Codex has no configured quota -> quota is nil, page renders "未配置".
	codex := byProvider["OpenAI · Codex"]
	if codex.Plans[0].Quota != nil {
		t.Fatalf("codex should have nil quota")
	}
}

func TestBuildUsageHierarchy_EmptyDataGap(t *testing.T) {
	h := BuildUsageHierarchy("w", time.Now(), nil, nil)
	if len(h.Providers) != 0 {
		t.Fatalf("empty input should produce no providers")
	}
	foundUsage := false
	foundQuota := false
	for _, g := range h.DataGaps {
		if g == "usage_no_rows" {
			foundUsage = true
		}
		if g == "quota_unconfigured" {
			foundQuota = true
		}
	}
	if !foundUsage || !foundQuota {
		t.Fatalf("data gaps not reported: %v", h.DataGaps)
	}
}

func TestBuildUsageHierarchy_UnknownProviderBucket(t *testing.T) {
	rows := []UsageObservation{
		obs("", "some-model", "t1", "", "a1", "X", "", "", "cloud", 10, 0, 0, 0),
	}
	h := BuildUsageHierarchy("w", time.Now(), rows, nil)
	if len(h.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(h.Providers))
	}
	if h.Providers[0].Provider != "未识别提供商" {
		t.Fatalf("provider = %q", h.Providers[0].Provider)
	}
}

func TestQuotaResetAt(t *testing.T) {
	if q := quotaResetAt("never", nil); q != nil {
		t.Fatalf("never cycle should have no reset, got %v", q)
	}
	d := quotaResetAt("daily", nil)
	if d == nil || !d.After(time.Now().UTC()) {
		t.Fatalf("daily reset must be in the future")
	}
	w := quotaResetAt("weekly", nil)
	if w == nil || !w.After(time.Now().UTC()) {
		t.Fatalf("weekly reset must be in the future")
	}
	m := quotaResetAt("monthly", intPtr(15))
	if m == nil || !m.After(time.Now().UTC()) {
		t.Fatalf("monthly reset must be in the future")
	}
}

func intPtr(v int) *int { return &v }
