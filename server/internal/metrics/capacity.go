// Capacity routing (Lane D). Implements the executable half of the frozen
// dynamic-capacity-routing contract (see prior art
// hivecrew-p0c-capacity-keep/docs/plans/2026-08-12-003-dynamic-capacity-routing-contract.md):
// route within budget using remaining quota / health / role capability / base
// / concurrency slots — never a static model name.
package metrics

import (
	"context"
	"sort"
	"strings"
)

// Capacity decision verbs mirror the frozen contract: grant, defer, reject.
const (
	CapacityDecisionGrant  = "grant"
	CapacityDecisionDefer  = "defer"
	CapacityDecisionReject = "reject"
)

// CapacityRouteRequest is one routing decision. RoleRequired and BaseRequired
// are optional: empty means "no constraint".
type CapacityRouteRequest struct {
	TaskID       string
	EmployeeRef  string
	RoleRequired string
	BaseRequired string
	Candidates   []CapacityCandidate
}

// CapacityCandidate is one observable execution target (a HiveCrew-local agent
// on a runtime/base). All fields are read-only observations; the router never
// mutates them.
type CapacityCandidate struct {
	AgentID          string
	Provider         string
	Plan             string
	Account          string
	Base             string   // 基地
	Roles            []string // 岗位能力
	Health           string   // healthy | degraded | unhealthy
	QuotaRemaining   int64    // tokens remaining this cycle (negative = exhausted)
	QuotaUnmetered   bool     // no hard quota
	LocalModel       bool
	ConcurrencyUsed  int
	ConcurrencyLimit int // <=0 means unknown -> treated as full
}

// CapacityRouteDecision is the router's only output shape.
type CapacityRouteDecision struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
	AgentID  string `json:"agent_id,omitempty"`
	Provider string `json:"provider,omitempty"`
	Plan     string `json:"plan,omitempty"`
}

// CapacityRouter is the seam the assignment service consults before opening a
// write transaction. Nil routers are treated as "grant always" by the caller.
type CapacityRouter interface {
	RouteCapacity(ctx context.Context, req CapacityRouteRequest) CapacityRouteDecision
}

// StaticCapacityRouter is the deterministic, pure-function router used in
// production and tests. It makes no network or DB calls; callers supply the
// candidate observations.
type StaticCapacityRouter struct{}

// NewStaticCapacityRouter returns the shared router.
func NewStaticCapacityRouter() *StaticCapacityRouter {
	return &StaticCapacityRouter{}
}

// RouteCapacity implements CapacityRouter.
func (r *StaticCapacityRouter) RouteCapacity(_ context.Context, req CapacityRouteRequest) CapacityRouteDecision {
	return RouteCapacity(req)
}

// RouteCapacity applies the frozen routing contract in order:
//
//  1. Filter: health must be healthy (degraded is allowed only if no healthy
//     candidate exists — see step 2 fallback), role capability must include
//     the required role, base must match the required base.
//  2. If nothing passes, reject (fail closed): no healthy candidate matches
//     role/base.
//  3. Drop candidates with no free concurrency slot or exhausted quota.
//  4. If none grantable, defer: the pool is full or every matching candidate is
//     quota-exhausted — do not cancel or overwrite.
//  5. Grant the best-fit candidate by (remaining-quota headroom, free slots,
//     then deterministic AgentID).
func RouteCapacity(req CapacityRouteRequest) CapacityRouteDecision {
	if len(req.Candidates) == 0 {
		return CapacityRouteDecision{Decision: CapacityDecisionReject, Reason: "no_candidates"}
	}

	type scored struct {
		c         CapacityCandidate
		freeSlots int
		headroom  int64
		degraded  bool
	}

	healthy := make([]scored, 0)
	degraded := make([]scored, 0)
	for _, c := range req.Candidates {
		if !roleMatches(c.Roles, req.RoleRequired) {
			continue
		}
		if req.BaseRequired != "" && !strings.EqualFold(strings.TrimSpace(c.Base), strings.TrimSpace(req.BaseRequired)) {
			continue
		}
		s := scored{c: c}
		if c.ConcurrencyLimit > 0 {
			s.freeSlots = c.ConcurrencyLimit - c.ConcurrencyUsed
			if s.freeSlots < 0 {
				s.freeSlots = 0
			}
		}
		if c.QuotaUnmetered {
			s.headroom = 1<<62 - 1
		} else {
			s.headroom = c.QuotaRemaining
		}
		switch strings.ToLower(strings.TrimSpace(c.Health)) {
		case "healthy":
			healthy = append(healthy, s)
		case "degraded":
			s.degraded = true
			degraded = append(degraded, s)
		default:
			// unhealthy / empty / unknown are not grantable.
		}
	}

	pool := healthy
	if len(healthy) == 0 {
		pool = degraded
	}
	if len(pool) == 0 {
		return CapacityRouteDecision{Decision: CapacityDecisionReject, Reason: "no_healthy_candidate_matches_role_or_base"}
	}

	grantable := make([]scored, 0, len(pool))
	for _, s := range pool {
		if s.c.ConcurrencyLimit > 0 && s.freeSlots <= 0 {
			continue
		}
		if !s.c.QuotaUnmetered && s.headroom <= 0 {
			continue
		}
		grantable = append(grantable, s)
	}
	if len(grantable) == 0 {
		reason := "concurrency_slots_exhausted"
		allQuotaExhausted := true
		for _, s := range pool {
			if s.c.QuotaUnmetered || s.headroom > 0 {
				allQuotaExhausted = false
				break
			}
		}
		if allQuotaExhausted {
			reason = "quota_exhausted"
		}
		return CapacityRouteDecision{Decision: CapacityDecisionDefer, Reason: reason}
	}

	sort.SliceStable(grantable, func(i, j int) bool {
		a, b := grantable[i], grantable[j]
		// Prefer healthy over degraded.
		if a.degraded != b.degraded {
			return !a.degraded
		}
		// Prefer the most free concurrency slots (least contended target).
		if a.freeSlots != b.freeSlots {
			return a.freeSlots > b.freeSlots
		}
		// Prefer the largest quota headroom for unmetered, else the most
		// remaining quota.
		if a.headroom != b.headroom {
			return a.headroom > b.headroom
		}
		return a.c.AgentID < b.c.AgentID
	})

	best := grantable[0]
	decision := CapacityRouteDecision{
		Decision: CapacityDecisionGrant,
		AgentID:  best.c.AgentID,
		Provider: best.c.Provider,
		Plan:     best.c.Plan,
		Reason:   "best_fit_capacity",
	}
	if best.degraded {
		decision.Reason = "best_fit_degraded_fallback"
	}
	return decision
}

func roleMatches(roles []string, required string) bool {
	if strings.TrimSpace(required) == "" {
		return true
	}
	for _, r := range roles {
		if strings.EqualFold(strings.TrimSpace(r), strings.TrimSpace(required)) {
			return true
		}
	}
	return false
}
