package metrics

import (
	"context"
	"testing"
)

func healthyCandidate(agentID, provider, plan, base, role string, quotaRemaining int64, unmetered bool, used, limit int) CapacityCandidate {
	return CapacityCandidate{
		AgentID:          agentID,
		Provider:         provider,
		Plan:             plan,
		Base:             base,
		Roles:            []string{role},
		Health:           "healthy",
		QuotaRemaining:   quotaRemaining,
		QuotaUnmetered:   unmetered,
		ConcurrencyUsed:  used,
		ConcurrencyLimit: limit,
	}
}

func TestRouteCapacity_GrantBestFit(t *testing.T) {
	req := CapacityRouteRequest{
		TaskID:       "t1",
		RoleRequired: "coding",
		BaseRequired: "dgx",
		Candidates: []CapacityCandidate{
			healthyCandidate("a1", "qwen", "plan-a", "mac", "coding", 100, false, 0, 4),
			healthyCandidate("a2", "qwen", "plan-b", "dgx", "coding", 5000, false, 1, 4),
			healthyCandidate("a3", "kimi", "plan-c", "dgx", "coding", 900, false, 0, 2),
		},
	}
	d := RouteCapacity(req)
	if d.Decision != CapacityDecisionGrant {
		t.Fatalf("decision = %s (%s), want grant", d.Decision, d.Reason)
	}
	// a2 has the most free slots (3) among base= dgx candidates (a3 has 2).
	if d.AgentID != "a2" {
		t.Fatalf("granted agent = %s, want a2", d.AgentID)
	}
}

func TestRouteCapacity_QuotaExhaustedDefers(t *testing.T) {
	// Negative routing case: every role/base-matching candidate is out of quota.
	req := CapacityRouteRequest{
		TaskID:       "t1",
		RoleRequired: "coding",
		Candidates: []CapacityCandidate{
			healthyCandidate("a1", "qwen", "plan-a", "dgx", "coding", 0, false, 0, 4),
			healthyCandidate("a2", "qwen", "plan-b", "dgx", "coding", -3, false, 0, 4),
		},
	}
	d := RouteCapacity(req)
	if d.Decision != CapacityDecisionDefer {
		t.Fatalf("decision = %s (%s), want defer", d.Decision, d.Reason)
	}
	if d.Reason != "quota_exhausted" {
		t.Fatalf("reason = %s, want quota_exhausted", d.Reason)
	}
}

func TestRouteCapacity_SlotsFullDefers(t *testing.T) {
	req := CapacityRouteRequest{
		TaskID: "t1",
		Candidates: []CapacityCandidate{
			healthyCandidate("a1", "qwen", "plan-a", "dgx", "coding", 1000, false, 4, 4),
		},
	}
	d := RouteCapacity(req)
	if d.Decision != CapacityDecisionDefer {
		t.Fatalf("decision = %s (%s), want defer", d.Decision, d.Reason)
	}
	if d.Reason != "concurrency_slots_exhausted" {
		t.Fatalf("reason = %s, want concurrency_slots_exhausted", d.Reason)
	}
}

func TestRouteCapacity_UnhealthyRejects(t *testing.T) {
	req := CapacityRouteRequest{
		TaskID:       "t1",
		RoleRequired: "coding",
		Candidates: []CapacityCandidate{
			{
				AgentID: "a1", Base: "dgx", Roles: []string{"coding"},
				Health: "unhealthy", QuotaUnmetered: true, ConcurrencyLimit: 4,
			},
		},
	}
	d := RouteCapacity(req)
	if d.Decision != CapacityDecisionReject {
		t.Fatalf("decision = %s (%s), want reject", d.Decision, d.Reason)
	}
}

func TestRouteCapacity_RoleMismatchRejects(t *testing.T) {
	req := CapacityRouteRequest{
		TaskID:       "t1",
		RoleRequired: "review",
		Candidates: []CapacityCandidate{
			healthyCandidate("a1", "qwen", "plan-a", "dgx", "coding", 100, false, 0, 4),
		},
	}
	d := RouteCapacity(req)
	if d.Decision != CapacityDecisionReject {
		t.Fatalf("decision = %s (%s), want reject", d.Decision, d.Reason)
	}
}

func TestRouteCapacity_BaseMismatchRejects(t *testing.T) {
	req := CapacityRouteRequest{
		TaskID:       "t1",
		BaseRequired: "dgx",
		Candidates: []CapacityCandidate{
			healthyCandidate("a1", "qwen", "plan-a", "mac", "coding", 100, false, 0, 4),
		},
	}
	d := RouteCapacity(req)
	if d.Decision != CapacityDecisionReject {
		t.Fatalf("decision = %s (%s), want reject", d.Decision, d.Reason)
	}
}

func TestRouteCapacity_DegradedFallback(t *testing.T) {
	degraded := healthyCandidate("a1", "qwen", "plan-a", "dgx", "coding", 100, false, 0, 4)
	degraded.Health = "degraded"
	req := CapacityRouteRequest{
		TaskID:       "t1",
		RoleRequired: "coding",
		Candidates:   []CapacityCandidate{degraded},
	}
	d := RouteCapacity(req)
	if d.Decision != CapacityDecisionGrant {
		t.Fatalf("decision = %s (%s), want grant (degraded fallback)", d.Decision, d.Reason)
	}
	if d.Reason != "best_fit_degraded_fallback" {
		t.Fatalf("reason = %s", d.Reason)
	}
}

func TestRouteCapacity_NoCandidatesRejects(t *testing.T) {
	d := RouteCapacity(CapacityRouteRequest{})
	if d.Decision != CapacityDecisionReject {
		t.Fatalf("decision = %s, want reject", d.Decision)
	}
}

func TestStaticCapacityRouter_ImplementsInterface(t *testing.T) {
	r := NewStaticCapacityRouter()
	d := r.RouteCapacity(context.Background(), CapacityRouteRequest{
		Candidates: []CapacityCandidate{
			healthyCandidate("a1", "qwen", "plan-a", "dgx", "coding", 100, false, 0, 4),
		},
	})
	if d.Decision != CapacityDecisionGrant {
		t.Fatalf("decision = %s", d.Decision)
	}
}
