package metrics

import (
	"context"
	"sort"
	"strings"
	"time"
)

// StandardQuotaWindows is the legacy fallback when no plan profile exists.
var StandardQuotaWindows = []string{"5h", "7d", "monthly"}

// QuotaSnapshotRow is one persisted observation from provider_quota_snapshot.
type QuotaSnapshotRow struct {
	ID              string
	WorkspaceID     string
	Provider        string
	Plan            string
	Account         string
	APIKeyLabel     string
	WindowKind      string
	LimitTokens     *int64
	UsedTokens      int64
	RemainingTokens *int64
	Percentage      *float64
	Unit            string
	ResetAt         *time.Time
	ObservedAt      time.Time
	Source          string
	SourceRef       string
}

// QuotaWindowView is one rendered quota window for a plan/account.
type QuotaWindowView struct {
	Kind            string   `json:"kind"`
	Label           string   `json:"label,omitempty"`
	Unit            string   `json:"unit,omitempty"`
	TotalTokens     *int64   `json:"total_tokens,omitempty"`
	UsedTokens      int64    `json:"used_tokens"`
	RemainingTokens *int64   `json:"remaining_tokens,omitempty"`
	Percentage      *float64 `json:"percentage,omitempty"`
	ResetAt         *string  `json:"reset_at,omitempty"`
	Source          string   `json:"source"`
	ObservedAt      *string  `json:"observed_at,omitempty"`
	Unlimited       bool     `json:"unlimited,omitempty"`
	LocalOnly       bool     `json:"local_only,omitempty"`
}

const (
	quotaSnapshotFreshness       = 15 * time.Minute
	quotaConsoleSnapshotMaxAge   = 7 * 24 * time.Hour
)

// WindowSince returns the UTC instant from which task_usage rows should be
// counted for a quota window kind.
func WindowSince(now time.Time, kind string) time.Time {
	now = now.UTC()
	switch kind {
	case "5h":
		return now.Add(-5 * time.Hour)
	case "7d":
		return now.AddDate(0, 0, -7)
	case "30d", "30d_cost":
		return now.AddDate(0, 0, -30)
	case "daily":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	case "monthly":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	default:
		return now.AddDate(0, 0, -30)
	}
}

// SumObservationsSince sums token totals for observations created at or after
// since. Observations passed in must include CreatedAt — we use a lightweight
// filter on task IDs is not enough; callers pass pre-filtered rows or we need
// created_at on UsageObservation.

// SumObservationTokens sums tokens from observations (already scoped to the page
// or quota window query).
func SumObservationTokens(observations []UsageObservation) int64 {
	var total int64
	for _, o := range observations {
		total += o.TotalTokens()
	}
	return total
}

func snapshotKey(provider, plan, account, windowKind string) string {
	return quotaKey(provider, plan, account) + "\x00" + windowKind
}

func quotaByCycle(quotas []UsageQuotaRow) map[string]UsageQuotaRow {
	out := make(map[string]UsageQuotaRow, len(quotas))
	for _, q := range quotas {
		cycle := strings.TrimSpace(q.Cycle)
		if cycle == "" {
			cycle = "monthly"
		}
		out[quotaKey(q.Provider, q.Plan, q.Account)+"\x00"+cycle] = q
	}
	return out
}

func snapshotsByKey(snapshots []QuotaSnapshotRow) map[string]QuotaSnapshotRow {
	out := make(map[string]QuotaSnapshotRow, len(snapshots))
	for _, s := range snapshots {
		out[snapshotKey(s.Provider, s.Plan, s.Account, s.WindowKind)] = s
	}
	return out
}

// BuildQuotaWindows merges console snapshots (ground truth), manual caps, and
// local task_usage. Console/live observations take precedence over local math.
func BuildQuotaWindows(
	provider, plan, account string,
	planObservations []UsageObservation,
	quotas []UsageQuotaRow,
	snapshots []QuotaSnapshotRow,
	now time.Time,
) []QuotaWindowView {
	specs := PlanWindowsFor(provider, plan)
	if len(specs) == 0 {
		return nil
	}
	quotaMap := quotaByCycle(quotas)
	snapMap := snapshotsByKey(snapshots)

	views := make([]QuotaWindowView, 0, len(specs))
	for _, spec := range specs {
		kind := spec.Kind
		localUsed := sumObservationsForWindow(planObservations, WindowSince(now, windowKindToSince(kind)), now)
		view := QuotaWindowView{
			Kind:       kind,
			Label:      spec.Label,
			Unit:       spec.Unit,
			UsedTokens: localUsed,
			Source:     "task_usage",
			LocalOnly:  true,
		}
		if kind == "unlimited" {
			view.Unlimited = true
			view.Source = "console"
			view.LocalOnly = false
		}

		if snap, ok := snapMap[snapshotKey(provider, plan, account, kind)]; ok && snap.ID != "" {
			applySnapshotToView(&view, snap, now)
		}

		if !view.isAuthoritative() {
			if cap, ok := quotaMap[quotaKey(provider, plan, account)+"\x00"+kind]; ok && cap.ID != "" {
				applyManualCapToView(&view, cap, localUsed)
			} else if cap, ok := quotaMap[quotaKey(provider, plan, account)+"\x00"+legacyCycleForWindow(kind)]; ok && cap.ID != "" {
				applyManualCapToView(&view, cap, localUsed)
			}
		}

		finalizeQuotaWindowView(&view)
		if view.ResetAt == nil {
			if reset := windowResetAt(kind, now); reset != nil && kind != "unlimited" {
				s := reset.UTC().Format(time.RFC3339)
				view.ResetAt = &s
			}
		}
		views = append(views, view)
	}
	return views
}

func windowKindToSince(kind string) string {
	switch kind {
	case "30d", "30d_cost", "session":
		return "30d"
	case "mcp_monthly", "monthly", "package":
		return "monthly"
	case "code_5h", "5h":
		return "5h"
	case "code_7d", "7d":
		return "7d"
	default:
		return kind
	}
}

func (v QuotaWindowView) isAuthoritative() bool {
	return isAuthoritativeQuotaSource(v.Source) && !v.LocalOnly
}

func finalizeQuotaWindowView(view *QuotaWindowView) {
	if view.Unlimited {
		view.Percentage = nil
		view.RemainingTokens = nil
		return
	}
	if view.Percentage != nil && view.Unit == "percent" {
		if view.RemainingTokens == nil && view.TotalTokens == nil {
			remainingPct := 100 - *view.Percentage
			if remainingPct < 0 {
				remainingPct = 0
			}
			// Expose remaining headroom as pseudo-percent for employees.
			view.RemainingTokens = int64Ptr(int64(remainingPct))
		}
		return
	}
	if view.TotalTokens != nil && *view.TotalTokens > 0 {
		remaining := *view.TotalTokens - view.UsedTokens
		if view.RemainingTokens != nil && view.isAuthoritative() {
			remaining = *view.RemainingTokens
		}
		if remaining < 0 {
			remaining = 0
		}
		pct := float64(view.UsedTokens) / float64(*view.TotalTokens) * 100
		if view.isAuthoritative() && view.Percentage != nil {
			pct = *view.Percentage
		} else if view.isAuthoritative() && view.RemainingTokens != nil {
			pct = float64(*view.TotalTokens-*view.RemainingTokens) / float64(*view.TotalTokens) * 100
		}
		if pct > 100 {
			pct = 100
		}
		view.RemainingTokens = &remaining
		view.Percentage = &pct
	}
}

func int64Ptr(v int64) *int64 { return &v }

func legacyCycleForWindow(kind string) string {
	switch kind {
	case "5h":
		return "daily"
	case "7d":
		return "weekly"
	default:
		return "monthly"
	}
}

func sumObservationsForWindow(observations []UsageObservation, since, now time.Time) int64 {
	var total int64
	for _, o := range observations {
		if o.CreatedAt.IsZero() {
			total += o.TotalTokens()
			continue
		}
		t := o.CreatedAt.UTC()
		if !t.Before(since) && !t.After(now) {
			total += o.TotalTokens()
		}
	}
	return total
}

func snapshotStillFresh(snap QuotaSnapshotRow, now time.Time) bool {
	age := now.Sub(snap.ObservedAt.UTC())
	switch snap.Source {
	case "live_vendor":
		return age <= quotaSnapshotFreshness
	case "console", "hivecosm":
		return age <= quotaConsoleSnapshotMaxAge
	default:
		return age <= quotaConsoleSnapshotMaxAge
	}
}

func applySnapshotToView(view *QuotaWindowView, snap QuotaSnapshotRow, now time.Time) {
	if !snapshotStillFresh(snap, now) {
		return
	}
	if !isAuthoritativeQuotaSource(snap.Source) {
		return
	}
	view.Source = snap.Source
	view.LocalOnly = false
	if snap.Unit != "" {
		view.Unit = snap.Unit
	}
	obs := snap.ObservedAt.UTC().Format(time.RFC3339)
	view.ObservedAt = &obs
	if snap.LimitTokens != nil {
		view.TotalTokens = snap.LimitTokens
	}
	if snap.RemainingTokens != nil {
		view.RemainingTokens = snap.RemainingTokens
	}
	if snap.Percentage != nil {
		view.Percentage = snap.Percentage
	}
	if snap.UsedTokens > 0 || isAuthoritativeQuotaSource(snap.Source) {
		view.UsedTokens = snap.UsedTokens
	}
	if snap.ResetAt != nil {
		s := snap.ResetAt.UTC().Format(time.RFC3339)
		view.ResetAt = &s
	}
	if snap.WindowKind == "unlimited" {
		view.Unlimited = true
	}
}

func applyManualCapToView(view *QuotaWindowView, cap UsageQuotaRow, used int64) {
	if view.isAuthoritative() {
		return
	}
	if cap.TotalTokens > 0 {
		total := cap.TotalTokens
		view.TotalTokens = &total
	}
	view.Source = "manual_cap"
	view.LocalOnly = false
	view.UsedTokens = used
	if reset := quotaResetAt(cap.Cycle, cap.ResetDay); reset != nil && view.ResetAt == nil {
		s := reset.UTC().Format(time.RFC3339)
		view.ResetAt = &s
	}
}

func windowResetAt(kind string, now time.Time) *time.Time {
	switch kind {
	case "5h":
		next := now.UTC().Add(5 * time.Hour)
		return &next
	case "7d":
		next := now.UTC().AddDate(0, 0, 7)
		return &next
	case "monthly":
		return quotaResetAt("monthly", nil)
	default:
		return nil
	}
}

// QuotaSnapshotService reads and writes provider_quota_snapshot rows.
type QuotaSnapshotService struct {
	Querier UsageWriteQuerier
}

func NewQuotaSnapshotService(querier UsageWriteQuerier) *QuotaSnapshotService {
	return &QuotaSnapshotService{Querier: querier}
}

const listQuotaSnapshotsSQL = `
SELECT
    id::text,
    workspace_id::text,
    provider,
    plan,
    account_label,
    api_key_label,
    window_kind,
    limit_tokens,
    used_tokens,
    remaining_tokens,
    percentage,
    unit,
    reset_at,
    observed_at,
    source,
    source_ref
FROM provider_quota_snapshot
WHERE workspace_id = $1::uuid
ORDER BY provider, plan, account_label, window_kind
`

func (s *QuotaSnapshotService) ListSnapshots(ctx context.Context, workspaceID string) ([]QuotaSnapshotRow, error) {
	if s == nil || s.Querier == nil {
		return nil, errUsageQuerierRequired
	}
	rows, err := s.Querier.Query(ctx, listQuotaSnapshotsSQL, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]QuotaSnapshotRow, 0)
	for rows.Next() {
		var (
			r          QuotaSnapshotRow
			limit      *int64
			remaining  *int64
			percentage *float64
			resetAt    *time.Time
			observedAt time.Time
			unit       string
		)
		if err := rows.Scan(
			&r.ID, &r.WorkspaceID, &r.Provider, &r.Plan, &r.Account, &r.APIKeyLabel,
			&r.WindowKind, &limit, &r.UsedTokens, &remaining, &percentage, &unit, &resetAt, &observedAt,
			&r.Source, &r.SourceRef,
		); err != nil {
			return nil, err
		}
		r.LimitTokens = limit
		r.RemainingTokens = remaining
		r.Percentage = percentage
		r.Unit = unit
		r.ResetAt = resetAt
		r.ObservedAt = observedAt
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertSnapshotInput is one observation to persist.
type UpsertSnapshotInput struct {
	Provider        string
	Plan            string
	Account         string
	APIKeyLabel     string
	WindowKind      string
	LimitTokens     *int64
	UsedTokens      int64
	RemainingTokens *int64
	Percentage      *float64
	Unit            string
	ResetAt         *time.Time
	ObservedAt      time.Time
	Source          string
	SourceRef       string
}

const upsertQuotaSnapshotSQL = `
INSERT INTO provider_quota_snapshot (
    workspace_id, provider, plan, account_label, api_key_label,
    window_kind, limit_tokens, used_tokens, remaining_tokens,
    percentage, unit, reset_at, observed_at, source, source_ref
) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
ON CONFLICT (workspace_id, provider, plan, account_label, window_kind)
DO UPDATE SET
    api_key_label = EXCLUDED.api_key_label,
    limit_tokens = EXCLUDED.limit_tokens,
    used_tokens = EXCLUDED.used_tokens,
    remaining_tokens = EXCLUDED.remaining_tokens,
    percentage = EXCLUDED.percentage,
    unit = EXCLUDED.unit,
    reset_at = EXCLUDED.reset_at,
    observed_at = EXCLUDED.observed_at,
    source = EXCLUDED.source,
    source_ref = EXCLUDED.source_ref,
    updated_at = now()
`

func (s *QuotaSnapshotService) UpsertSnapshot(ctx context.Context, workspaceID string, in UpsertSnapshotInput) error {
	if s == nil || s.Querier == nil {
		return errUsageQuerierRequired
	}
	observedAt := in.ObservedAt
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	unit := in.Unit
	if unit == "" {
		unit = "tokens"
	}
	_, err := s.Querier.Exec(ctx, upsertQuotaSnapshotSQL,
		workspaceID, in.Provider, in.Plan, in.Account, in.APIKeyLabel,
		in.WindowKind, in.LimitTokens, in.UsedTokens, in.RemainingTokens,
		in.Percentage, unit, in.ResetAt, observedAt, in.Source, in.SourceRef,
	)
	return err
}

// WorkQuotaSnapshot is the machine-readable quota projection for daemons/PAT.
type WorkQuotaSnapshot struct {
	WorkspaceID string            `json:"workspace_id"`
	GeneratedAt string            `json:"generated_at"`
	Providers   []WorkQuotaProvider `json:"providers"`
}

type WorkQuotaProvider struct {
	Provider string           `json:"provider"`
	Plans    []WorkQuotaPlan  `json:"plans"`
}

type WorkQuotaPlan struct {
	Plan        string            `json:"plan"`
	Account     string            `json:"account"`
	APIKeyLabel string            `json:"api_key_label,omitempty"`
	Windows     []QuotaWindowView `json:"windows"`
}

// BuildWorkQuotaSnapshot builds the /api/work/quota response from configured
// quotas, snapshots, and recent task_usage observations.
func BuildWorkQuotaSnapshot(
	workspaceID string,
	observations []UsageObservation,
	quotas []UsageQuotaRow,
	snapshots []QuotaSnapshotRow,
	now time.Time,
) WorkQuotaSnapshot {
	type planKey struct {
		provider, plan, account string
	}
	plans := make(map[planKey]WorkQuotaPlan)
	obsByPlan := make(map[planKey][]UsageObservation)

	for _, o := range observations {
		c := ClassifyProviderPlan(o.Model, o.RuntimeName, o.RuntimeProvider, o.RuntimeMode)
		k := planKey{c.Provider, c.Plan, c.Account}
		obsByPlan[k] = append(obsByPlan[k], o)
		if _, ok := plans[k]; !ok {
			plans[k] = WorkQuotaPlan{Plan: c.Plan, Account: c.Account}
		}
	}

	for _, q := range quotas {
		k := planKey{q.Provider, q.Plan, q.Account}
		p := plans[k]
		if p.Plan == "" {
			p.Plan = q.Plan
			p.Account = q.Account
		}
		if q.APIKeyLabel != "" {
			p.APIKeyLabel = q.APIKeyLabel
		}
		plans[k] = p
	}
	for _, snap := range snapshots {
		k := planKey{snap.Provider, snap.Plan, snap.Account}
		p := plans[k]
		if p.Plan == "" {
			p.Plan = snap.Plan
			p.Account = snap.Account
		}
		if snap.APIKeyLabel != "" {
			p.APIKeyLabel = snap.APIKeyLabel
		}
		plans[k] = p
	}

	byProvider := make(map[string][]WorkQuotaPlan)
	for k, p := range plans {
		p.Windows = BuildQuotaWindows(k.provider, k.plan, k.account, obsByPlan[k], quotas, snapshots, now)
		byProvider[k.provider] = append(byProvider[k.provider], p)
	}

	providerList := make([]WorkQuotaProvider, 0, len(byProvider))
	for provider, planList := range byProvider {
		sort.Slice(planList, func(i, j int) bool {
			return planList[i].Plan < planList[j].Plan
		})
		providerList = append(providerList, WorkQuotaProvider{Provider: provider, Plans: planList})
	}
	sort.Slice(providerList, func(i, j int) bool {
		return providerList[i].Provider < providerList[j].Provider
	})

	return WorkQuotaSnapshot{
		WorkspaceID: workspaceID,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Providers:   providerList,
	}
}
