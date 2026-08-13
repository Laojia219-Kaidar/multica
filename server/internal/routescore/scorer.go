package routescore

import (
	"fmt"
	"math"
	"time"
)

// QuotaStalenessThreshold is the maximum age of a quota check before
// the quota is considered stale. Exported for test overrides.
var QuotaStalenessThreshold = 15 * time.Minute

// Clock abstracts time retrieval so tests can inject a deterministic
// time source instead of relying on the real wall clock.
type Clock interface {
	Now() time.Time
}

// realClock is the production Clock that delegates to time.Now.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Scorer evaluates candidates against task requirements.
type Scorer struct {
	weights Weights
	clock   Clock
}

// NewScorer constructs a Scorer with the supplied weights. If weights
// is nil, DefaultWeights is used. All weights must be non-negative and
// every dimension in AllDimensions must be present.
func NewScorer(w Weights) *Scorer {
	if w == nil {
		w = DefaultWeights()
	}
	for _, dim := range AllDimensions() {
		v, ok := w[dim]
		if !ok {
			panic(fmt.Sprintf("routescore: missing weight for dimension %q", dim))
		}
		if v < 0 {
			panic(fmt.Sprintf("routescore: negative weight %f for dimension %q", v, dim))
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			panic(fmt.Sprintf("routescore: non-finite weight %f for dimension %q", v, dim))
		}
	}
	return &Scorer{weights: w, clock: realClock{}}
}

// WithClock returns a copy of the Scorer that uses the supplied Clock
// for time-dependent gate checks. Production code uses the real wall
// clock; tests inject a fixed clock for determinism.
func (s *Scorer) WithClock(c Clock) *Scorer {
	cp := *s
	cp.clock = c
	return &cp
}

// Score evaluates one candidate against the task requirement. It
// returns a Result with per-dimension breakdown and a total weighted
// score. If a hard gate fires (unknown/stale quota, offline runtime,
// independence violation), FailClosed is set and TotalScore is zero.
func (s *Scorer) Score(c Candidate, req TaskRequirement) Result {
	r := Result{
		CandidateID:   c.AgentID,
		CandidateName: c.AgentName,
		Dimensions:    make([]DimensionScore, 0, len(AllDimensions())),
	}

	// Hard gates — fail closed.
	if gate, blocked := s.hardGate(c, req); blocked {
		r.FailClosed = true
		r.FailReason = gate
		r.TotalScore = 0
		for _, dim := range AllDimensions() {
			r.Dimensions = append(r.Dimensions, DimensionScore{
				Dimension: dim,
				Score:     0,
				Weight:    s.weights[dim],
				Reason:    "blocked by hard gate: " + gate,
			})
		}
		return r
	}

	var total float64
	for _, dim := range AllDimensions() {
		ds := s.scoreDimension(dim, c, req)
		ds.Weight = s.weights[dim]
		r.Dimensions = append(r.Dimensions, ds)
		total += ds.Score * ds.Weight
	}
	r.TotalScore = math.Round(total*10000) / 10000
	return r
}

// hardGate checks the three fail-closed conditions. Returns (reason,
// true) if the candidate must be rejected.
func (s *Scorer) hardGate(c Candidate, req TaskRequirement) (string, bool) {
	// Quota unknown or stale → fail closed.
	if c.Quota == QuotaUnknown {
		return "quota_unknown", true
	}
	if c.Quota == QuotaStale {
		return "quota_stale", true
	}
	if s.clock.Now().Sub(c.QuotaCheckedAt) > QuotaStalenessThreshold {
		return "quota_check_expired", true
	}

	// Runtime offline → fail closed.
	if c.RuntimeHealth == RuntimeOffline || c.RuntimeHealth == RuntimeUnresponsive {
		return "runtime_unavailable", true
	}

	// Independence: reviewer must not be the author.
	if req.NeedsReview && c.IsAuthor {
		return "reviewer_is_author", true
	}

	return "", false
}

// scoreDimension dispatches to the per-axis scorer.
func (s *Scorer) scoreDimension(dim Dimension, c Candidate, req TaskRequirement) DimensionScore {
	switch dim {
	case DimRoleFit:
		return scoreRoleFit(c, req)
	case DimRuntimeHealth:
		return scoreRuntimeHealth(c)
	case DimQuotaFresh:
		return scoreQuotaFreshness(c)
	case DimHistory:
		return scoreHistory(c)
	case DimLatency:
		return scoreLatency(c, req)
	case DimCost:
		return scoreCost(c, req)
	case DimIndependence:
		return scoreIndependence(c, req)
	default:
		return DimensionScore{Dimension: dim, Reason: "unknown dimension"}
	}
}

// scoreRoleFit measures how well the candidate's roles match the
// task's required roles. Score = |intersection| / |required|, or 0 if
// no required roles are specified (neutral).
func scoreRoleFit(c Candidate, req TaskRequirement) DimensionScore {
	if len(req.RequiredRoles) == 0 {
		return DimensionScore{
			Dimension: DimRoleFit,
			Score:     0.5,
			Reason:    "no required roles specified; neutral score",
		}
	}

	required := make(map[string]struct{}, len(req.RequiredRoles))
	for _, r := range req.RequiredRoles {
		required[r] = struct{}{}
	}

	matched := 0
	for _, role := range c.Roles {
		if _, ok := required[role]; ok {
			matched++
		}
	}

	score := float64(matched) / float64(len(required))
	if score > 1 {
		score = 1
	}

	reason := fmt.Sprintf("%d/%d required roles matched", matched, len(required))
	return DimensionScore{
		Dimension: DimRoleFit,
		Score:     math.Round(score*100) / 100,
		Reason:    reason,
	}
}

// scoreRuntimeHealth maps runtime status to a [0, 1] score.
func scoreRuntimeHealth(c Candidate) DimensionScore {
	switch c.RuntimeHealth {
	case RuntimeOnline:
		return DimensionScore{Dimension: DimRuntimeHealth, Score: 1.0, Reason: "runtime online"}
	case RuntimeDegraded:
		return DimensionScore{Dimension: DimRuntimeHealth, Score: 0.5, Reason: "runtime degraded"}
	case RuntimeOffline:
		return DimensionScore{Dimension: DimRuntimeHealth, Score: 0.0, Reason: "runtime offline"}
	case RuntimeUnresponsive:
		return DimensionScore{Dimension: DimRuntimeHealth, Score: 0.0, Reason: "runtime unresponsive"}
	default:
		return DimensionScore{Dimension: DimRuntimeHealth, Score: 0.0, Reason: "unknown runtime status"}
	}
}

// scoreQuotaFreshness maps quota state to a [0, 1] score. Unknown
// and stale are handled by the hard gate; this scores the remaining
// states.
func scoreQuotaFreshness(c Candidate) DimensionScore {
	switch c.Quota {
	case QuotaFresh:
		return DimensionScore{Dimension: DimQuotaFresh, Score: 1.0, Reason: "quota fresh"}
	case QuotaExhausted:
		return DimensionScore{Dimension: DimQuotaFresh, Score: 0.1, Reason: "quota exhausted but verified"}
	case QuotaStale:
		return DimensionScore{Dimension: DimQuotaFresh, Score: 0.0, Reason: "quota stale"}
	case QuotaUnknown:
		return DimensionScore{Dimension: DimQuotaFresh, Score: 0.0, Reason: "quota unknown"}
	default:
		return DimensionScore{Dimension: DimQuotaFresh, Score: 0.0, Reason: "unknown quota state"}
	}
}

// scoreHistory computes a success ratio from historical task data.
// With no history, returns a neutral 0.5 to avoid penalising new
// agents.
func scoreHistory(c Candidate) DimensionScore {
	total := c.SuccessCount + c.FailureCount
	if total == 0 {
		return DimensionScore{
			Dimension: DimHistory,
			Score:     0.5,
			Reason:    "no task history; neutral score",
		}
	}
	ratio := float64(c.SuccessCount) / float64(total)
	reason := fmt.Sprintf("%d/%d tasks succeeded", c.SuccessCount, total)
	return DimensionScore{
		Dimension: DimHistory,
		Score:     math.Round(ratio*100) / 100,
		Reason:    reason,
	}
}

// scoreLatency normalises the candidate's average latency against the
// task's budget. Score = 1 - (avg / max), clamped to [0, 1]. If no
// budget is set (MaxLatencyMs <= 0), returns neutral 0.5.
func scoreLatency(c Candidate, req TaskRequirement) DimensionScore {
	if req.MaxLatencyMs <= 0 {
		return DimensionScore{
			Dimension: DimLatency,
			Score:     0.5,
			Reason:    "no latency budget; neutral score",
		}
	}
	if c.AvgLatencyMs <= 0 {
		return DimensionScore{
			Dimension: DimLatency,
			Score:     1.0,
			Reason:    "no recorded latency; best case",
		}
	}
	ratio := c.AvgLatencyMs / req.MaxLatencyMs
	score := 1.0 - ratio
	if score < 0 {
		score = 0
	}
	reason := fmt.Sprintf("avg %.0fms / budget %.0fms", c.AvgLatencyMs, req.MaxLatencyMs)
	return DimensionScore{
		Dimension: DimLatency,
		Score:     math.Round(score*100) / 100,
		Reason:    reason,
	}
}

// scoreCost normalises cost against the task's budget. Score =
// 1 - (cost / max), clamped to [0, 1]. No budget → neutral 0.5.
func scoreCost(c Candidate, req TaskRequirement) DimensionScore {
	if req.MaxCostUSD <= 0 {
		return DimensionScore{
			Dimension: DimCost,
			Score:     0.5,
			Reason:    "no cost budget; neutral score",
		}
	}
	if c.CostPerTaskUSD <= 0 {
		return DimensionScore{
			Dimension: DimCost,
			Score:     1.0,
			Reason:    "no recorded cost; best case",
		}
	}
	ratio := c.CostPerTaskUSD / req.MaxCostUSD
	score := 1.0 - ratio
	if score < 0 {
		score = 0
	}
	reason := fmt.Sprintf("$%.4f / budget $%.4f", c.CostPerTaskUSD, req.MaxCostUSD)
	return DimensionScore{
		Dimension: DimCost,
		Score:     math.Round(score*100) / 100,
		Reason:    reason,
	}
}

// scoreIndependence returns 1.0 when there is no independence
// conflict, 0.0 when there is one. The hard gate catches the
// reviewer-is-author case for review tasks; this dimension captures
// softer independence signals (e.g. self-review attempts on non-review
// tasks, which are merely penalised rather than blocked).
func scoreIndependence(c Candidate, req TaskRequirement) DimensionScore {
	if !req.NeedsReview {
		return DimensionScore{
			Dimension: DimIndependence,
			Score:     1.0,
			Reason:    "no independence constraint for this task",
		}
	}
	if c.IsAuthor {
		// Hard gate catches this; score is moot but included for
		// completeness in the explanation payload.
		return DimensionScore{
			Dimension: DimIndependence,
			Score:     0.0,
			Reason:    "candidate is the author; blocked",
		}
	}
	return DimensionScore{
		Dimension: DimIndependence,
		Score:     1.0,
		Reason:    "candidate is independent of the content",
	}
}

// Rank sorts results by TotalScore descending. FailClosed entries are
// always last. Ties are broken by CandidateID (stable sort).
func Rank(results []Result) []Result {
	sorted := make([]Result, len(results))
	copy(sorted, results)
	// Simple insertion sort — candidate lists are small (< 50).
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && less(sorted[j], sorted[j-1]); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return sorted
}

func less(a, b Result) bool {
	if a.FailClosed != b.FailClosed {
		return !a.FailClosed // non-blocked first
	}
	if a.TotalScore != b.TotalScore {
		return a.TotalScore > b.TotalScore
	}
	return a.CandidateID.String() < b.CandidateID.String()
}
