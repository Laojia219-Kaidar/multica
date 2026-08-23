package metrics

import "strings"

// PlanWindowSpec describes one console-aligned quota bar for a billing plan.
type PlanWindowSpec struct {
	Kind  string
	Label string
	Unit  string // tokens | percent | credits | cny
}

// PlanQuotaProfile is the v1 console shape for one (provider, plan) identity.
type PlanQuotaProfile struct {
	Provider string
	Plan     string
	Windows  []PlanWindowSpec
	// QuotaV1 excludes Codex/Claude and other out-of-scope plans from quota bars.
	QuotaV1 bool
}

var planQuotaProfiles = []PlanQuotaProfile{
	{
		Provider: "智谱 · GLM",
		Plan:     "GLM Coding Max V1",
		QuotaV1:  true,
		Windows: []PlanWindowSpec{
			{Kind: "5h", Label: "5 小时", Unit: "percent"},
			{Kind: "7d", Label: "7 天 Tokens", Unit: "tokens"},
			{Kind: "mcp_monthly", Label: "MCP 每月", Unit: "percent"},
		},
	},
	{
		Provider: "阿里云百炼",
		Plan:     "Coding Pro",
		QuotaV1:  true,
		Windows: []PlanWindowSpec{
			{Kind: "5h", Label: "5 小时", Unit: "percent"},
			{Kind: "7d", Label: "7 天", Unit: "percent"},
			{Kind: "30d", Label: "30 天", Unit: "percent"},
		},
	},
	{
		Provider: "阿里云百炼",
		Plan:     "Token Plan Personal",
		QuotaV1:  true,
		Windows: []PlanWindowSpec{
			{Kind: "7d", Label: "7 天", Unit: "percent"},
		},
	},
	{
		Provider: "小米 · MiMo",
		Plan:     "MiMo Pro annual",
		QuotaV1:  true,
		Windows: []PlanWindowSpec{
			{Kind: "credits", Label: "Credits", Unit: "credits"},
		},
	},
	{
		Provider: "MiniMax",
		Plan:     "TokenPlanPlus",
		QuotaV1:  true,
		Windows: []PlanWindowSpec{
			{Kind: "5h", Label: "5 小时", Unit: "percent"},
			{Kind: "unlimited", Label: "每周", Unit: "percent"},
			{Kind: "credits", Label: "Credits 余额", Unit: "credits"},
		},
	},
	{
		Provider: "火山引擎 · Doubao",
		Plan:     "Ark Agent Plan",
		QuotaV1:  true,
		Windows: []PlanWindowSpec{
			{Kind: "5h", Label: "AFP 5 小时", Unit: "tokens"},
			{Kind: "7d", Label: "AFP 7 天", Unit: "tokens"},
			{Kind: "30d", Label: "AFP 30 天", Unit: "tokens"},
		},
	},
	{
		Provider: "火山引擎 · Doubao",
		Plan:     "Ark Coding Plan",
		QuotaV1:  true,
		Windows: []PlanWindowSpec{
			{Kind: "session", Label: "会话", Unit: "percent"},
			{Kind: "7d", Label: "7 天", Unit: "percent"},
			{Kind: "30d", Label: "30 天", Unit: "percent"},
		},
	},
	{
		Provider: "DeepSeek",
		Plan:     "DeepSeek API",
		QuotaV1:  true,
		Windows: []PlanWindowSpec{
			{Kind: "cny_balance", Label: "CNY 余额", Unit: "cny"},
			{Kind: "30d_cost", Label: "30 天用量", Unit: "tokens"},
		},
	},
	{
		Provider: "月之暗面 · Kimi",
		Plan:     "Kimi Membership Allegro",
		QuotaV1:  true,
		Windows: []PlanWindowSpec{
			{Kind: "package", Label: "套餐总量", Unit: "percent"},
			{Kind: "code_5h", Label: "Code 5 小时", Unit: "percent"},
			{Kind: "code_7d", Label: "Code 7 天", Unit: "percent"},
		},
	},
	{
		Provider: "月之暗面 · Kimi",
		Plan:     "Kimi Code Console",
		QuotaV1:  true,
		Windows: []PlanWindowSpec{
			{Kind: "code_5h", Label: "Code 5 小时", Unit: "percent"},
			{Kind: "code_7d", Label: "Code 7 天", Unit: "percent"},
		},
	},
	{
		Provider: "月之暗面 · Kimi",
		Plan:     "Kimi Prepaid CNY",
		QuotaV1:  true,
		Windows: []PlanWindowSpec{
			{Kind: "cny_balance", Label: "预付费 CNY", Unit: "cny"},
		},
	},
}

// LookupPlanQuotaProfile returns the v1 console profile for a plan bucket.
func LookupPlanQuotaProfile(provider, plan string) (PlanQuotaProfile, bool) {
	key := strings.TrimSpace(provider) + "\x00" + strings.TrimSpace(plan)
	for _, p := range planQuotaProfiles {
		if p.Provider+"\x00"+p.Plan == key {
			return p, true
		}
	}
	return PlanQuotaProfile{}, false
}

// PlanWindowsFor returns the window kinds to render for a plan. Falls back to
// legacy 5h/7d/monthly when no profile exists (excluding v1-out-of-scope plans).
func PlanWindowsFor(provider, plan string) []PlanWindowSpec {
	if prof, ok := LookupPlanQuotaProfile(provider, plan); ok {
		return prof.Windows
	}
	if isQuotaV1Excluded(provider, plan) {
		return nil
	}
	return []PlanWindowSpec{
		{Kind: "5h", Label: "5 小时", Unit: "tokens"},
		{Kind: "7d", Label: "7 天", Unit: "tokens"},
		{Kind: "monthly", Label: "每月", Unit: "tokens"},
	}
}

func isQuotaV1Excluded(provider, plan string) bool {
	p := strings.ToLower(provider + " " + plan)
	return strings.Contains(p, "codex") || strings.Contains(p, "claude")
}

func isAuthoritativeQuotaSource(source string) bool {
	switch source {
	case "console", "live_vendor", "hivecosm":
		return true
	default:
		return false
	}
}
