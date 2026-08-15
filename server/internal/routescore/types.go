// Package routescore provides an advisory route scorer that evaluates
// candidate agents/runtimes for task assignment. It scores along seven
// dimensions — role fit, runtime health, quota freshness, historical
// success, latency, cost, and independence constraints — and returns a
// deterministic explanation payload.
//
// The scorer is advisory only: it does not mutate live route config,
// account state, or dispatch decisions. It fails closed when quota is
// unknown or stale, and enforces that the reviewer cannot route to the
// author.
package routescore

import (
	"time"

	"github.com/google/uuid"
)

// Dimension identifies one scoring axis.
type Dimension string

const (
	DimRoleFit       Dimension = "role_fit"
	DimRuntimeHealth Dimension = "runtime_health"
	DimQuotaFresh    Dimension = "quota_freshness"
	DimHistory       Dimension = "historical_success"
	DimLatency       Dimension = "latency"
	DimCost          Dimension = "cost"
	DimIndependence  Dimension = "independence"
)

// AllDimensions returns the canonical dimension list in weight order.
func AllDimensions() []Dimension {
	return []Dimension{
		DimRoleFit,
		DimRuntimeHealth,
		DimQuotaFresh,
		DimHistory,
		DimLatency,
		DimCost,
		DimIndependence,
	}
}

// QuotaState is the freshness classification of an agent's quota.
type QuotaState string

const (
	QuotaFresh    QuotaState = "fresh"
	QuotaStale    QuotaState = "stale"
	QuotaUnknown  QuotaState = "unknown"
	QuotaExhausted QuotaState = "exhausted"
)

// RuntimeStatus is the health classification of a runtime.
type RuntimeStatus string

const (
	RuntimeOnline       RuntimeStatus = "online"
	RuntimeDegraded     RuntimeStatus = "degraded"
	RuntimeOffline      RuntimeStatus = "offline"
	RuntimeUnresponsive RuntimeStatus = "unresponsive"
)

// Candidate is one agent/runtime being evaluated for routing.
type Candidate struct {
	// AgentID is the stable employee/agent identity. Must not change
	// across scoring runs for the same logical employee.
	AgentID uuid.UUID

	// AgentName is the human-readable display name.
	AgentName string

	// Roles are the capability tags this agent advertises (e.g.
	// "code_review", "implementation", "debugging").
	Roles []string

	// RuntimeID is the bound runtime instance, if any.
	RuntimeID uuid.UUID

	// RuntimeHealth is the current runtime health classification.
	RuntimeHealth RuntimeStatus

	// Quota is the freshness state of the agent's model quota.
	Quota QuotaState

	// QuotaCheckedAt is when the quota state was last verified.
	QuotaCheckedAt time.Time

	// SuccessCount is the number of successfully completed tasks in
	// the evaluation window.
	SuccessCount int

	// FailureCount is the number of failed tasks in the window.
	FailureCount int

	// AvgLatencyMs is the mean task completion latency in milliseconds.
	AvgLatencyMs float64

	// CostPerTaskUSD is the mean cost per task in USD.
	CostPerTaskUSD float64

	// IsAuthor indicates this candidate authored the content being
	// reviewed (independence constraint).
	IsAuthor bool

	// IsReviewer indicates this candidate is being asked to review
	// content they authored (independence constraint).
	IsReviewer bool
}

// TaskRequirement describes what the task needs from a candidate.
type TaskRequirement struct {
	// RequiredRoles are the capability tags the task requires. At
	// least one must match for a non-zero role_fit score.
	RequiredRoles []string

	// NeedsReview indicates this task is a review assignment, so the
	// independence constraint (reviewer != author) applies.
	NeedsReview bool

	// MaxLatencyMs is the task's latency budget. Candidates above
	// this threshold score zero on the latency dimension.
	MaxLatencyMs float64

	// MaxCostUSD is the task's cost budget. Candidates above this
	// threshold score zero on the cost dimension.
	MaxCostUSD float64
}

// DimensionScore is the per-axis result.
type DimensionScore struct {
	// Dimension identifies the axis.
	Dimension Dimension `json:"dimension"`

	// Score is the normalised value in [0, 1]. Higher is better.
	Score float64 `json:"score"`

	// Weight is the multiplier applied to Score in the weighted sum.
	Weight float64 `json:"weight"`

	// Reason is a short human-readable explanation of why this score
	// was assigned.
	Reason string `json:"reason"`
}

// Result is the full scoring outcome for one candidate.
type Result struct {
	// CandidateID is the agent being scored.
	CandidateID uuid.UUID `json:"candidate_id"`

	// CandidateName is the display name at scoring time.
	CandidateName string `json:"candidate_name"`

	// TotalScore is the weighted sum of dimension scores.
	TotalScore float64 `json:"total_score"`

	// Dimensions holds the per-axis breakdown.
	Dimensions []DimensionScore `json:"dimensions"`

	// FailClosed is true when the candidate was rejected by a
	// hard gate (unknown/stale quota, offline runtime, independence
	// violation) regardless of the total score.
	FailClosed bool `json:"fail_closed"`

	// FailReason names the gate that triggered FailClosed, or empty.
	FailReason string `json:"fail_reason,omitempty"`
}

// Weights maps each dimension to its multiplier. The scorer validates
// that all dimensions are present and non-negative.
type Weights map[Dimension]float64

// DefaultWeights returns the reference weight set.
func DefaultWeights() Weights {
	return Weights{
		DimRoleFit:       0.25,
		DimRuntimeHealth: 0.20,
		DimQuotaFresh:    0.20,
		DimHistory:       0.15,
		DimLatency:       0.08,
		DimCost:          0.07,
		DimIndependence:  0.05,
	}
}
