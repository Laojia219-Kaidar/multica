package routescore

import (
	"time"

	"github.com/google/uuid"
)

// Deterministic fixture UUIDs. Using well-known values makes test
// output stable across runs and easy to diff.
var (
	FixtureAgentKepler = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	FixtureAgentRaven  = uuid.MustParse("00000000-0000-0000-0000-000000000002")
	FixtureAgentWillow = uuid.MustParse("00000000-0000-0000-0000-000000000003")
	FixtureAgentAtlas  = uuid.MustParse("00000000-0000-0000-0000-000000000004")

	FixtureRuntimeA = uuid.MustParse("00000000-0000-0000-0000-000000000010")
	FixtureRuntimeB = uuid.MustParse("00000000-0000-0000-0000-000000000020")
)

// FixtureNow is the deterministic "now" for all fixture constructions.
var FixtureNow = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)

// FixtureCandidateHealthy returns a candidate that passes all gates
// and scores well across all dimensions.
func FixtureCandidateHealthy() Candidate {
	return Candidate{
		AgentID:        FixtureAgentKepler,
		AgentName:      "Kepler",
		Roles:          []string{"code_review", "implementation", "debugging"},
		RuntimeID:      FixtureRuntimeA,
		RuntimeHealth:  RuntimeOnline,
		Quota:          QuotaFresh,
		QuotaCheckedAt: FixtureNow.Add(-2 * time.Minute),
		SuccessCount:   42,
		FailureCount:   3,
		AvgLatencyMs:   1200,
		CostPerTaskUSD: 0.05,
		IsAuthor:       false,
		IsReviewer:     false,
	}
}

// FixtureCandidateStaleQuota returns a candidate whose quota is stale.
// The scorer must fail closed.
func FixtureCandidateStaleQuota() Candidate {
	return Candidate{
		AgentID:        FixtureAgentRaven,
		AgentName:      "Raven",
		Roles:          []string{"implementation"},
		RuntimeID:      FixtureRuntimeA,
		RuntimeHealth:  RuntimeOnline,
		Quota:          QuotaStale,
		QuotaCheckedAt: time.Now().Add(-30 * time.Minute),
		SuccessCount:   10,
		FailureCount:   1,
		AvgLatencyMs:   800,
		CostPerTaskUSD: 0.03,
	}
}

// FixtureCandidateUnknownQuota returns a candidate whose quota is
// unknown. The scorer must fail closed.
func FixtureCandidateUnknownQuota() Candidate {
	return Candidate{
		AgentID:        FixtureAgentWillow,
		AgentName:      "Willow",
		Roles:          []string{"code_review"},
		RuntimeID:      FixtureRuntimeB,
		RuntimeHealth:  RuntimeOnline,
		Quota:          QuotaUnknown,
		QuotaCheckedAt: FixtureNow.Add(-1 * time.Minute),
		SuccessCount:   5,
		FailureCount:   0,
		AvgLatencyMs:   500,
		CostPerTaskUSD: 0.02,
	}
}

// FixtureCandidateAuthor returns a candidate that authored the content
// being reviewed. Must fail closed on review tasks.
func FixtureCandidateAuthor() Candidate {
	return Candidate{
		AgentID:        FixtureAgentAtlas,
		AgentName:      "Atlas",
		Roles:          []string{"implementation", "code_review"},
		RuntimeID:      FixtureRuntimeA,
		RuntimeHealth:  RuntimeOnline,
		Quota:          QuotaFresh,
		QuotaCheckedAt: FixtureNow.Add(-1 * time.Minute),
		SuccessCount:   20,
		FailureCount:   2,
		AvgLatencyMs:   1000,
		CostPerTaskUSD: 0.04,
		IsAuthor:       true,
	}
}

// FixtureCandidateOffline returns a candidate whose runtime is
// offline. Must fail closed.
func FixtureCandidateOffline() Candidate {
	return Candidate{
		AgentID:        FixtureAgentRaven,
		AgentName:      "Raven",
		Roles:          []string{"implementation"},
		RuntimeID:      FixtureRuntimeB,
		RuntimeHealth:  RuntimeOffline,
		Quota:          QuotaFresh,
		QuotaCheckedAt: time.Now().Add(-1 * time.Minute),
		SuccessCount:   15,
		FailureCount:   0,
		AvgLatencyMs:   600,
		CostPerTaskUSD: 0.02,
	}
}

// FixtureTaskReview returns a review task requirement.
func FixtureTaskReview() TaskRequirement {
	return TaskRequirement{
		RequiredRoles: []string{"code_review"},
		NeedsReview:   true,
		MaxLatencyMs:  5000,
		MaxCostUSD:    0.10,
	}
}

// FixtureTaskImplementation returns a standard implementation task.
func FixtureTaskImplementation() TaskRequirement {
	return TaskRequirement{
		RequiredRoles: []string{"implementation", "debugging"},
		NeedsReview:   false,
		MaxLatencyMs:  10000,
		MaxCostUSD:    0.20,
	}
}

// FixtureCandidatePool returns a mixed pool of candidates for ranking
// tests.
func FixtureCandidatePool() []Candidate {
	return []Candidate{
		FixtureCandidateHealthy(),
		FixtureCandidateStaleQuota(),
		FixtureCandidateUnknownQuota(),
		FixtureCandidateAuthor(),
		FixtureCandidateOffline(),
	}
}
