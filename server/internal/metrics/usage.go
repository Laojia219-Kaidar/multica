// Package metrics owns the P3 usage aggregation and capacity-routing
// projections for Lane D. It reads only the canonical usage tables
// (task_usage) and the Lane D quota table (provider_usage_quota); it never
// writes task/agent authority state and never fabricates a token total that is
// not present in the source rows.
package metrics

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// errUsageQuerierRequired is returned when a usage read is attempted without a
// database seam. Fail closed rather than returning an empty hierarchy.
var errUsageQuerierRequired = errors.New("usage querier is required")

// UsageSQLQuerier is the minimal DB seam the usage aggregation needs. It is
// satisfied by *db.Queries, *pgxpool.Pool and the handler's dbExecutor so the
// aggregation can run against any canonical Postgres handle without importing
// the handler or generated-query packages.
type UsageSQLQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// PlanClassification is the billing identity of one (provider, model,
// runtime) observation.
type PlanClassification struct {
	Provider   string `json:"provider"`
	Plan       string `json:"plan"`
	Account    string `json:"account"`
	LocalModel bool   `json:"local_model"`
}

// ClassifyProviderPlan classifies one usage observation into its provider /
// plan / billing-account identity. It mirrors the client-side inventory rules
// (packages/views/dashboard/model-quota-inventory.ts) so the server response
// and the operator-configured quota rows key on the same vocabulary. The
// model wins over the runtime carrier where the model identifies a distinct
// billing plan (e.g. a local DGX checkpoint carried over a qwen runtime).
func ClassifyProviderPlan(model, runtimeName, runtimeProvider, runtimeMode string) PlanClassification {
	modelLower := strings.ToLower(strings.TrimSpace(model))
	runtimeLower := strings.ToLower(strings.TrimSpace(runtimeName))
	providerLower := strings.ToLower(strings.TrimSpace(runtimeProvider))
	_ = runtimeMode // carrier locality is a distinct axis; see localModel below.

	// localModel is true only for models that actually run on local compute
	// (e.g. a DGX checkpoint). runtime_mode = 'local' means the CARRIER runs
	// locally (Claude Desktop, Codex CLI) but the model is still a cloud model
	// and keeps its provider/plan identity — see the live-data note in
	// docs/lane-d/usage-aggregation-live-evidence.json.
	localModel := strings.Contains(modelLower, "nvfp4") ||
		strings.Contains(modelLower, "qwen3.6-27b")

	account := strings.TrimSpace(runtimeName)
	if account == "" {
		account = modelLower
	}
	if account == "" {
		account = "unbound"
	}

	classified := func(provider, plan string) PlanClassification {
		return PlanClassification{
			Provider:   provider,
			Plan:       plan,
			Account:    account,
			LocalModel: localModel,
		}
	}

	if strings.Contains(modelLower, "qwen3.6-27b-nvfp4") {
		return PlanClassification{
			Provider:   "本地模型 · DGX",
			Plan:       "qwen3.6-27B NVFP4",
			Account:    account,
			LocalModel: true,
		}
	}
	if strings.HasPrefix(modelLower, "bailian-token-plan-personal/") {
		return classified("阿里云百炼", "Token Plan Personal")
	}
	if strings.Contains(runtimeLower, "volcengine-agent") {
		return classified("火山引擎 · Doubao", "Volcengine Agent Plan")
	}
	if strings.Contains(runtimeLower, "volcengine-coding") {
		return classified("火山引擎 · Doubao", "Volcengine Coding Plan")
	}
	if strings.Contains(runtimeLower, "qwen-token") {
		return classified("阿里云 · Qwen", "Qwen Token Plan")
	}
	if strings.Contains(runtimeLower, "qwen-coding") {
		return classified("阿里云 · Qwen", "Qwen Coding Plan")
	}
	if strings.Contains(runtimeLower, "secure zhipu") {
		return classified("智谱 · GLM", "GLM API")
	}
	if strings.Contains(runtimeLower, "secure deepseek") {
		return classified("DeepSeek", "DeepSeek API")
	}
	if strings.Contains(runtimeLower, "secure kimi") {
		return classified("月之暗面 · Kimi", "Kimi API")
	}
	if strings.Contains(runtimeLower, "secure mimo") {
		return classified("小米 · MiMo", "MiMo API")
	}
	if strings.Contains(runtimeLower, "secure minimax") {
		return classified("MiniMax", "MiniMax API")
	}
	if strings.HasPrefix(modelLower, "gpt-") || providerLower == "codex" {
		return classified("OpenAI · Codex", "Codex Plan")
	}
	if strings.HasPrefix(modelLower, "claude-") || providerLower == "claude" {
		return classified("Anthropic · Claude", "Claude Plan")
	}
	if strings.HasPrefix(modelLower, "glm-") {
		return classified("智谱 · GLM", "GLM API")
	}
	if modelLower == "k3" || providerLower == "kimi" {
		return classified("月之暗面 · Kimi", "Kimi CLI")
	}
	if strings.HasPrefix(modelLower, "qwen") {
		return classified("阿里云 · Qwen", "Qwen Code")
	}
	if strings.HasPrefix(modelLower, "deepseek") {
		return classified("DeepSeek", "DeepSeek API")
	}
	if strings.HasPrefix(modelLower, "minimax") {
		return classified("MiniMax", model)
	}
	if strings.HasPrefix(modelLower, "mimo") {
		return classified("小米 · MiMo", model)
	}

	switch providerLower {
	case "coze":
		return classified("Coze", "Coze CLI")
	case "hermes":
		return classified("Hermes", "Hermes Runtime")
	case "openclaw":
		return classified("OpenClaw", "OpenClaw Runtime")
	case "opencode":
		return classified("OpenCode", "OpenCode Runtime")
	case "qwen":
		return classified("阿里云 · Qwen", "Qwen Runtime")
	case "":
		if modelLower != "" {
			return classified("未识别提供商", model)
		}
		return classified("未识别提供商", "未绑定计费账户")
	default:
		return classified(runtimeProvider, account)
	}
}

// UsageQuotaRow is one operator-configured quota row from provider_usage_quota.
type UsageQuotaRow struct {
	ID          string
	WorkspaceID string
	Provider    string
	Plan        string
	Account     string
	APIKeyLabel string
	Cycle       string
	TotalTokens int64
	ResetDay    *int
	LocalModel  bool
}

// UsageObservation is one raw (task, provider, model) token row lifted from
// task_usage joined to its task/agent/runtime identity.
type UsageObservation struct {
	Provider         string
	Model            string
	TaskID           string
	IssueID          *string
	AgentID          string
	AgentName        string
	RuntimeProvider  string
	RuntimeName      string
	RuntimeMode      string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CostUSDTicks     int64
}

// TotalTokens returns the token figure used everywhere on the usage page:
// input + output + cache read + cache write.
func (o UsageObservation) TotalTokens() int64 {
	return o.InputTokens + o.OutputTokens + o.CacheReadTokens + o.CacheWriteTokens
}

// UsageService aggregates live usage and merges it with configured quotas.
type UsageService struct {
	Querier UsageSQLQuerier
}

// NewUsageService returns a nil-safe usage service.
func NewUsageService(querier UsageSQLQuerier) *UsageService {
	return &UsageService{Querier: querier}
}

// SinceBound returns the UTC instant `days` days before now, calendar-day
// aligned in UTC. The caller may choose another boundary; this helper exists
// so handler and service share one definition.
func SinceBound(days int) time.Time {
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		days = 365
	}
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, -(days - 1))
}

const usageObservationSQL = `
SELECT
    LOWER(tu.provider) AS provider,
    tu.model,
    tu.task_id::text,
    atq.issue_id::text,
    atq.agent_id::text,
    a.name,
    ar.provider,
    ar.name,
    ar.runtime_mode,
    tu.input_tokens,
    tu.output_tokens,
    tu.cache_read_tokens,
    tu.cache_write_tokens,
    COALESCE(tu.cost_usd_ticks, 0)
FROM task_usage tu
JOIN agent_task_queue atq ON atq.id = tu.task_id
JOIN agent a ON a.id = atq.agent_id
JOIN agent_runtime ar ON ar.id = atq.runtime_id
WHERE a.workspace_id = $1::uuid
  AND tu.created_at >= $2
ORDER BY LOWER(tu.provider), tu.model, a.name, tu.task_id::text
`

// ListUsageObservations loads the raw (provider, model, task, agent, runtime)
// usage rows for a workspace since a UTC boundary. It is the single SQL seam
// for the whole hierarchy: every level (provider -> plan/account -> model ->
// employee -> task) is a pure in-memory rollup of these rows, so the levels
// can never disagree about a token total.
func (s *UsageService) ListUsageObservations(ctx context.Context, workspaceID string, since time.Time) ([]UsageObservation, error) {
	if s == nil || s.Querier == nil {
		return nil, errUsageQuerierRequired
	}
	rows, err := s.Querier.Query(ctx, usageObservationSQL, workspaceID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]UsageObservation, 0)
	for rows.Next() {
		var (
			o                     UsageObservation
			issueID               pgtype.Text
			input, output         int64
			cacheRead, cacheWrite int64
			costTicks             int64
		)
		if err := rows.Scan(
			&o.Provider,
			&o.Model,
			&o.TaskID,
			&issueID,
			&o.AgentID,
			&o.AgentName,
			&o.RuntimeProvider,
			&o.RuntimeName,
			&o.RuntimeMode,
			&input,
			&output,
			&cacheRead,
			&cacheWrite,
			&costTicks,
		); err != nil {
			return nil, err
		}
		o.InputTokens = input
		o.OutputTokens = output
		o.CacheReadTokens = cacheRead
		o.CacheWriteTokens = cacheWrite
		o.CostUSDTicks = costTicks
		if issueID.Valid && issueID.String != "" {
			v := issueID.String
			o.IssueID = &v
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ListUsageQuota loads the operator-configured quota rows for a workspace.
func (s *UsageService) ListUsageQuota(ctx context.Context, workspaceID string) ([]UsageQuotaRow, error) {
	if s == nil || s.Querier == nil {
		return nil, errUsageQuerierRequired
	}
	rows, err := s.Querier.Query(ctx, `
SELECT
    id::text,
    workspace_id::text,
    provider,
    plan,
    account_label,
    api_key_label,
    cycle,
    total_tokens,
    reset_day,
    local_model
FROM provider_usage_quota
WHERE workspace_id = $1::uuid
ORDER BY provider, plan, account_label, api_key_label
`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]UsageQuotaRow, 0)
	for rows.Next() {
		var (
			q          UsageQuotaRow
			total      int64
			resetDay   pgtype.Int4
			localModel bool
		)
		if err := rows.Scan(
			&q.ID,
			&q.WorkspaceID,
			&q.Provider,
			&q.Plan,
			&q.Account,
			&q.APIKeyLabel,
			&q.Cycle,
			&total,
			&resetDay,
			&localModel,
		); err != nil {
			return nil, err
		}
		q.TotalTokens = total
		q.LocalModel = localModel
		if resetDay.Valid {
			v := int(resetDay.Int32)
			q.ResetDay = &v
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// QuotaState is the rendered quota view for one plan/account. Nil means the
// operator has not configured a quota for this plan/account; the page renders
// "配额未配置" instead of inventing remaining/percentage values.
type QuotaState struct {
	Cycle       string   `json:"cycle"`
	TotalTokens *int64   `json:"total_tokens,omitempty"`
	UsedTokens  int64    `json:"used_tokens"`
	Remaining   *int64   `json:"remaining_tokens,omitempty"`
	Percentage  *float64 `json:"percentage,omitempty"`
	ResetAt     *string  `json:"reset_at,omitempty"`
	ResetDay    *int     `json:"reset_day,omitempty"`
	LocalModel  bool     `json:"local_model"`
}

// TaskUsage is one task's token total inside an employee bucket.
type TaskUsage struct {
	TaskID       string  `json:"task_id"`
	IssueID      *string `json:"issue_id,omitempty"`
	Model        string  `json:"model"`
	UsedTokens   int64   `json:"used_tokens"`
	CostUSDTicks int64   `json:"cost_usd_ticks"`
}

// ModelUsage is one model's rollup inside a plan or employee bucket.
type ModelUsage struct {
	Model         string `json:"model"`
	UsedTokens    int64  `json:"used_tokens"`
	EmployeeCount int    `json:"employee_count"`
	TaskCount     int    `json:"task_count"`
}

// EmployeeUsage is one employee's (local agent's) rollup inside a plan bucket.
type EmployeeUsage struct {
	AgentID    string       `json:"agent_id"`
	Name       string       `json:"name"`
	UsedTokens int64        `json:"used_tokens"`
	Models     []ModelUsage `json:"models"`
	Tasks      []TaskUsage  `json:"tasks"`
}

// PlanUsage is one plan/account bucket below a provider.
type PlanUsage struct {
	Plan        string          `json:"plan"`
	Account     string          `json:"account"`
	APIKeyLabel string          `json:"api_key_label,omitempty"`
	LocalModel  bool            `json:"local_model"`
	UsedTokens  int64           `json:"used_tokens"`
	Quota       *QuotaState     `json:"quota,omitempty"`
	Models      []ModelUsage    `json:"models"`
	Employees   []EmployeeUsage `json:"employees"`
}

// ProviderUsage is one provider bucket at the top of the hierarchy.
type ProviderUsage struct {
	Provider   string      `json:"provider"`
	LocalModel bool        `json:"local_model"`
	UsedTokens int64       `json:"used_tokens"`
	Plans      []PlanUsage `json:"plans"`
}

// UsageTotals is the workspace-wide rollup shown above the hierarchy.
type UsageTotals struct {
	UsedTokens      int64 `json:"used_tokens"`
	TaskCount       int   `json:"task_count"`
	EmployeeCount   int   `json:"employee_count"`
	PlanCount       int   `json:"plan_count"`
	LocalModelCount int   `json:"local_model_count"`
}

// UsageHierarchy is the full usage-page projection.
type UsageHierarchy struct {
	WorkspaceID string          `json:"workspace_id"`
	Since       string          `json:"since"`
	GeneratedAt string          `json:"generated_at"`
	DataGaps    []string        `json:"data_gaps"`
	Totals      UsageTotals     `json:"totals"`
	Providers   []ProviderUsage `json:"providers"`
}

// BuildUsageHierarchy rolls observations up into the provider -> plan ->
// model -> employee -> task hierarchy and merges configured quota rows.
func BuildUsageHierarchy(workspaceID string, since time.Time, observations []UsageObservation, quotas []UsageQuotaRow) UsageHierarchy {
	quotaByKey := make(map[string]UsageQuotaRow, len(quotas))
	for _, q := range quotas {
		quotaByKey[quotaKey(q.Provider, q.Plan, q.Account)] = q
	}

	type planBucket struct {
		plan          PlanUsage
		models        map[string]*ModelUsage
		empl          map[string]*EmployeeUsage
		modelEmployee map[string]map[string]struct{}
		modelTask     map[string]map[string]struct{}
	}
	type providerBucket struct {
		provider ProviderUsage
		plans    map[string]*planBucket
	}

	providers := make(map[string]*providerBucket)

	var (
		totals  UsageTotals
		tasks   = make(map[string]struct{})
		agents  = make(map[string]struct{})
		localPV = make(map[string]struct{})
	)

	for _, o := range observations {
		c := ClassifyProviderPlan(o.Model, o.RuntimeName, o.RuntimeProvider, o.RuntimeMode)
		used := o.TotalTokens()
		pk := quotaKey(c.Provider, c.Plan, c.Account)

		pb := providers[c.Provider]
		if pb == nil {
			pb = &providerBucket{
				provider: ProviderUsage{Provider: c.Provider},
				plans:    make(map[string]*planBucket),
			}
			providers[c.Provider] = pb
		}
		pb.provider.UsedTokens += used
		if c.LocalModel {
			pb.provider.LocalModel = true
			localPV[c.Provider] = struct{}{}
		}

		p := pb.plans[pk]
		if p == nil {
			p = &planBucket{
				plan: PlanUsage{
					Plan:       c.Plan,
					Account:    c.Account,
					LocalModel: c.LocalModel,
				},
				models:        make(map[string]*ModelUsage),
				empl:          make(map[string]*EmployeeUsage),
				modelEmployee: make(map[string]map[string]struct{}),
				modelTask:     make(map[string]map[string]struct{}),
			}
			pb.plans[pk] = p
		}
		p.plan.UsedTokens += used
		if c.LocalModel {
			p.plan.LocalModel = true
		}

		m := p.models[o.Model]
		if m == nil {
			m = &ModelUsage{Model: o.Model}
			p.models[o.Model] = m
			p.modelEmployee[o.Model] = make(map[string]struct{})
			p.modelTask[o.Model] = make(map[string]struct{})
		}
		m.UsedTokens += used
		p.modelEmployee[o.Model][o.AgentID] = struct{}{}
		p.modelTask[o.Model][o.TaskID] = struct{}{}

		e := p.empl[o.AgentID]
		if e == nil {
			e = &EmployeeUsage{AgentID: o.AgentID, Name: o.AgentName}
			p.empl[o.AgentID] = e
		}
		e.UsedTokens += used
		e.Tasks = append(e.Tasks, TaskUsage{
			TaskID:       o.TaskID,
			IssueID:      o.IssueID,
			Model:        o.Model,
			UsedTokens:   used,
			CostUSDTicks: o.CostUSDTicks,
		})

		tasks[o.TaskID] = struct{}{}
		agents[o.AgentID] = struct{}{}
		totals.UsedTokens += used
	}

	providerList := make([]ProviderUsage, 0, len(providers))
	for _, pb := range providers {
		prov := pb.provider
		planList := make([]PlanUsage, 0, len(pb.plans))
		for _, p := range pb.plans {
			plan := p.plan
			plan.Models = flattenModelUsage(p.models)
			for i := range plan.Models {
				plan.Models[i].EmployeeCount = len(p.modelEmployee[plan.Models[i].Model])
				plan.Models[i].TaskCount = len(p.modelTask[plan.Models[i].Model])
			}
			plan.Employees = flattenEmployeeUsage(p.empl)
			matchedQuota, hasQuota := quotaByKey[quotaKey(prov.Provider, plan.Plan, plan.Account)]
			if hasQuota {
				plan.APIKeyLabel = matchedQuota.APIKeyLabel
			}
			plan.Quota = buildQuotaState(matchedQuota, plan.UsedTokens)
			planList = append(planList, plan)
		}
		sort.Slice(planList, func(i, j int) bool {
			if planList[i].UsedTokens != planList[j].UsedTokens {
				return planList[i].UsedTokens > planList[j].UsedTokens
			}
			return planList[i].Plan < planList[j].Plan
		})
		prov.Plans = planList
		providerList = append(providerList, prov)
	}
	sort.Slice(providerList, func(i, j int) bool {
		if providerList[i].UsedTokens != providerList[j].UsedTokens {
			return providerList[i].UsedTokens > providerList[j].UsedTokens
		}
		return providerList[i].Provider < providerList[j].Provider
	})

	totals.TaskCount = len(tasks)
	totals.EmployeeCount = len(agents)
	totals.LocalModelCount = len(localPV)
	planCount := 0
	for _, pb := range providers {
		planCount += len(pb.plans)
	}
	totals.PlanCount = planCount

	gaps := make([]string, 0)
	if len(observations) == 0 {
		gaps = append(gaps, "usage_no_rows")
	}
	if len(quotas) == 0 {
		gaps = append(gaps, "quota_unconfigured")
	}

	return UsageHierarchy{
		WorkspaceID: workspaceID,
		Since:       since.UTC().Format(time.RFC3339),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		DataGaps:    gaps,
		Totals:      totals,
		Providers:   providerList,
	}
}

func flattenModelUsage(m map[string]*ModelUsage) []ModelUsage {
	out := make([]ModelUsage, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UsedTokens != out[j].UsedTokens {
			return out[i].UsedTokens > out[j].UsedTokens
		}
		return out[i].Model < out[j].Model
	})
	return out
}

func flattenEmployeeUsage(m map[string]*EmployeeUsage) []EmployeeUsage {
	out := make([]EmployeeUsage, 0, len(m))
	for _, v := range m {
		models := make(map[string]*ModelUsage)
		for _, t := range v.Tasks {
			mm := models[t.Model]
			if mm == nil {
				mm = &ModelUsage{Model: t.Model}
				models[t.Model] = mm
			}
			mm.UsedTokens += t.UsedTokens
			mm.TaskCount++
			mm.EmployeeCount = 1
		}
		v.Models = flattenModelUsage(models)
		sort.Slice(v.Tasks, func(i, j int) bool {
			if v.Tasks[i].UsedTokens != v.Tasks[j].UsedTokens {
				return v.Tasks[i].UsedTokens > v.Tasks[j].UsedTokens
			}
			return v.Tasks[i].TaskID < v.Tasks[j].TaskID
		})
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UsedTokens != out[j].UsedTokens {
			return out[i].UsedTokens > out[j].UsedTokens
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func quotaKey(provider, plan, account string) string {
	return provider + "\x00" + plan + "\x00" + account
}

// buildQuotaState merges a configured quota with observed usage. A zero-value
// (not found) quota row returns nil so the caller renders "配额未配置".
func buildQuotaState(q UsageQuotaRow, used int64) *QuotaState {
	if q.ID == "" {
		return nil
	}
	cycle := q.Cycle
	if cycle == "" {
		cycle = "monthly"
	}
	state := &QuotaState{
		Cycle:      cycle,
		UsedTokens: used,
		ResetDay:   q.ResetDay,
		LocalModel: q.LocalModel,
	}
	if q.TotalTokens > 0 {
		total := q.TotalTokens
		remaining := total - used
		if remaining < 0 {
			remaining = 0
		}
		percentage := float64(used) / float64(total) * 100
		if percentage > 100 {
			percentage = 100
		}
		state.TotalTokens = &total
		state.Remaining = &remaining
		state.Percentage = &percentage
	}
	if reset := quotaResetAt(cycle, q.ResetDay); reset != nil {
		s := reset.UTC().Format(time.RFC3339)
		state.ResetAt = &s
	}
	return state
}

// quotaResetAt returns the next reset boundary for a cycle: daily -> next UTC
// midnight, weekly -> next Monday 00:00 UTC, monthly -> next month anchored on
// reset_day (default 1), never -> nil.
func quotaResetAt(cycle string, resetDay *int) *time.Time {
	now := time.Now().UTC()
	var next time.Time
	switch cycle {
	case "daily":
		next = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	case "weekly":
		offset := (8 - int(now.Weekday())) % 7
		if offset == 0 {
			offset = 7
		}
		next = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, offset)
	case "monthly":
		day := 1
		if resetDay != nil && *resetDay >= 1 && *resetDay <= 28 {
			day = *resetDay
		}
		nextMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
		lastDay := time.Date(nextMonth.Year(), nextMonth.Month(), 0, 0, 0, 0, 0, time.UTC).Day()
		if day > lastDay {
			day = lastDay
		}
		next = time.Date(nextMonth.Year(), nextMonth.Month(), day, 0, 0, 0, 0, time.UTC)
		if !next.After(now) {
			next = next.AddDate(0, 1, 0)
		}
	case "never":
		return nil
	default:
		return nil
	}
	return &next
}
