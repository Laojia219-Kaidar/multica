package routescore

import (
	"testing"
	"time"
)

// fixtureClock returns FixtureNow for deterministic time in tests.
type fixtureClock struct{}

func (fixtureClock) Now() time.Time { return FixtureNow }

// newTestScorer returns a Scorer wired to the deterministic fixture clock.
func newTestScorer(w Weights) *Scorer {
	return NewScorer(w).WithClock(fixtureClock{})
}

func TestScoreHealthyCandidate_ImplementationTask(t *testing.T) {
	s := newTestScorer(nil)
	c := FixtureCandidateHealthy()
	req := FixtureTaskImplementation()

	r := s.Score(c, req)

	if r.FailClosed {
		t.Fatalf("healthy candidate must not fail closed: %s", r.FailReason)
	}
	if r.TotalScore <= 0 {
		t.Fatalf("healthy candidate must have positive score, got %f", r.TotalScore)
	}
	if r.CandidateID != FixtureAgentKepler {
		t.Errorf("candidate ID mismatch: got %s, want %s", r.CandidateID, FixtureAgentKepler)
	}
	if len(r.Dimensions) != len(AllDimensions()) {
		t.Errorf("expected %d dimensions, got %d", len(AllDimensions()), len(r.Dimensions))
	}

	// Role fit: Kepler has implementation + debugging = 2/2 = 1.0
	for _, ds := range r.Dimensions {
		if ds.Dimension == DimRoleFit {
			if ds.Score != 1.0 {
				t.Errorf("role_fit: got %f, want 1.0 (both roles matched)", ds.Score)
			}
		}
	}
}

func TestScoreHealthyCandidate_ReviewTask(t *testing.T) {
	s := newTestScorer(nil)
	c := FixtureCandidateHealthy()
	req := FixtureTaskReview()

	r := s.Score(c, req)

	if r.FailClosed {
		t.Fatalf("healthy non-author candidate must not fail closed for review: %s", r.FailReason)
	}
	// Role fit: Kepler has code_review = 1/1 = 1.0
	for _, ds := range r.Dimensions {
		if ds.Dimension == DimRoleFit {
			if ds.Score != 1.0 {
				t.Errorf("role_fit: got %f, want 1.0", ds.Score)
			}
		}
	}
}

func TestFailClosed_StaleQuota(t *testing.T) {
	s := newTestScorer(nil)
	c := FixtureCandidateStaleQuota()
	req := FixtureTaskImplementation()

	r := s.Score(c, req)

	if !r.FailClosed {
		t.Fatal("stale quota candidate must fail closed")
	}
	if r.FailReason != "quota_stale" {
		t.Errorf("fail reason: got %q, want %q", r.FailReason, "quota_stale")
	}
	if r.TotalScore != 0 {
		t.Errorf("fail-closed total score must be 0, got %f", r.TotalScore)
	}
}

func TestFailClosed_UnknownQuota(t *testing.T) {
	s := newTestScorer(nil)
	c := FixtureCandidateUnknownQuota()
	req := FixtureTaskImplementation()

	r := s.Score(c, req)

	if !r.FailClosed {
		t.Fatal("unknown quota candidate must fail closed")
	}
	if r.FailReason != "quota_unknown" {
		t.Errorf("fail reason: got %q, want %q", r.FailReason, "quota_unknown")
	}
}

func TestFailClosed_ReviewerIsAuthor(t *testing.T) {
	s := newTestScorer(nil)
	c := FixtureCandidateAuthor()
	req := FixtureTaskReview()

	r := s.Score(c, req)

	if !r.FailClosed {
		t.Fatal("author must fail closed on review task")
	}
	if r.FailReason != "reviewer_is_author" {
		t.Errorf("fail reason: got %q, want %q", r.FailReason, "reviewer_is_author")
	}
}

func TestFailClosed_RuntimeOffline(t *testing.T) {
	s := newTestScorer(nil)
	c := FixtureCandidateOffline()
	req := FixtureTaskImplementation()

	r := s.Score(c, req)

	if !r.FailClosed {
		t.Fatal("offline runtime must fail closed")
	}
	if r.FailReason != "runtime_unavailable" {
		t.Errorf("fail reason: got %q, want %q", r.FailReason, "runtime_unavailable")
	}
}

func TestFailClosed_ExpiredQuotaCheck(t *testing.T) {
	s := newTestScorer(nil)
	c := FixtureCandidateHealthy()
	c.QuotaCheckedAt = FixtureNow.Add(-20 * time.Minute) // beyond threshold
	req := FixtureTaskImplementation()

	r := s.Score(c, req)

	if !r.FailClosed {
		t.Fatal("expired quota check must fail closed")
	}
	if r.FailReason != "quota_check_expired" {
		t.Errorf("fail reason: got %q, want %q", r.FailReason, "quota_check_expired")
	}
}

func TestAuthorNotBlockedOnNonReviewTask(t *testing.T) {
	s := newTestScorer(nil)
	c := FixtureCandidateAuthor()
	req := FixtureTaskImplementation() // not a review

	r := s.Score(c, req)

	if r.FailClosed {
		t.Fatalf("author must NOT fail closed on non-review task: %s", r.FailReason)
	}
}

func TestScoreLatency_NoBudget(t *testing.T) {
	s := newTestScorer(nil)
	c := FixtureCandidateHealthy()
	req := TaskRequirement{MaxLatencyMs: 0}

	for _, ds := range s.Score(c, req).Dimensions {
		if ds.Dimension == DimLatency {
			if ds.Score != 0.5 {
				t.Errorf("no-budget latency: got %f, want 0.5", ds.Score)
			}
			return
		}
	}
	t.Error("latency dimension not found")
}

func TestScoreLatency_OverBudget(t *testing.T) {
	s := newTestScorer(nil)
	c := Candidate{
		AgentID:        FixtureAgentKepler,
		AgentName:      "Kepler",
		RuntimeHealth:  RuntimeOnline,
		Quota:          QuotaFresh,
		QuotaCheckedAt: FixtureNow,
		AvgLatencyMs:   15000,
	}
	req := TaskRequirement{MaxLatencyMs: 10000}

	r := s.Score(c, req)
	for _, ds := range r.Dimensions {
		if ds.Dimension == DimLatency {
			if ds.Score != 0 {
				t.Errorf("over-budget latency: got %f, want 0", ds.Score)
			}
			return
		}
	}
	t.Error("latency dimension not found")
}

func TestScoreCost_NoBudget(t *testing.T) {
	s := newTestScorer(nil)
	c := FixtureCandidateHealthy()
	req := TaskRequirement{MaxCostUSD: 0}

	for _, ds := range s.Score(c, req).Dimensions {
		if ds.Dimension == DimCost {
			if ds.Score != 0.5 {
				t.Errorf("no-budget cost: got %f, want 0.5", ds.Score)
			}
			return
		}
	}
	t.Error("cost dimension not found")
}

func TestScoreHistory_NoHistory(t *testing.T) {
	s := newTestScorer(nil)
	c := Candidate{
		AgentID:        FixtureAgentWillow,
		AgentName:      "Willow",
		RuntimeHealth:  RuntimeOnline,
		Quota:          QuotaFresh,
		QuotaCheckedAt: FixtureNow,
	}
	req := TaskRequirement{}

	for _, ds := range s.Score(c, req).Dimensions {
		if ds.Dimension == DimHistory {
			if ds.Score != 0.5 {
				t.Errorf("no-history: got %f, want 0.5", ds.Score)
			}
			return
		}
	}
	t.Error("history dimension not found")
}

func TestRank_FailClosedLast(t *testing.T) {
	s := newTestScorer(nil)
	req := FixtureTaskReview()

	pool := FixtureCandidatePool()
	var results []Result
	for _, c := range pool {
		results = append(results, s.Score(c, req))
	}

	ranked := Rank(results)

	// First entry must not be fail-closed.
	if ranked[0].FailClosed {
		t.Error("top-ranked candidate must not be fail-closed")
	}

	// All fail-closed entries must be after all non-fail-closed.
	seenBlocked := false
	for _, r := range ranked {
		if r.FailClosed {
			seenBlocked = true
		} else if seenBlocked {
			t.Errorf("non-blocked candidate %s appears after blocked ones", r.CandidateID)
		}
	}
}

func TestRank_DeterministicTieBreak(t *testing.T) {
	s := newTestScorer(nil)
	req := FixtureTaskImplementation()

	// Two identical candidates except for ID.
	a := FixtureCandidateHealthy()
	b := FixtureCandidateHealthy()
	b.AgentID = FixtureAgentRaven

	results := []Result{s.Score(a, req), s.Score(b, req)}
	ranked1 := Rank(results)
	ranked2 := Rank(results)

	if ranked1[0].CandidateID != ranked2[0].CandidateID {
		t.Error("ranking must be deterministic on ties")
	}
}

func TestExplanationPayload_AllDimensionsHaveReason(t *testing.T) {
	s := newTestScorer(nil)
	c := FixtureCandidateHealthy()
	req := FixtureTaskImplementation()

	r := s.Score(c, req)
	for _, ds := range r.Dimensions {
		if ds.Reason == "" {
			t.Errorf("dimension %s has empty reason", ds.Dimension)
		}
		if ds.Weight <= 0 {
			t.Errorf("dimension %s has non-positive weight %f", ds.Dimension, ds.Weight)
		}
	}
}

func TestExplanationPayload_FailClosedHasReasons(t *testing.T) {
	s := newTestScorer(nil)
	c := FixtureCandidateStaleQuota()
	req := FixtureTaskImplementation()

	r := s.Score(c, req)
	for _, ds := range r.Dimensions {
		if ds.Reason == "" {
			t.Errorf("fail-closed dimension %s has empty reason", ds.Dimension)
		}
	}
}

func TestCustomWeights(t *testing.T) {
	w := Weights{
		DimRoleFit:       0.5,
		DimRuntimeHealth: 0.1,
		DimQuotaFresh:    0.1,
		DimHistory:       0.1,
		DimLatency:       0.1,
		DimCost:          0.1,
		DimIndependence:  0.1,
	}
	s := newTestScorer(w)
	c := FixtureCandidateHealthy()
	req := FixtureTaskImplementation()

	r := s.Score(c, req)
	for _, ds := range r.Dimensions {
		if ds.Dimension == DimRoleFit && ds.Weight != 0.5 {
			t.Errorf("custom weight for role_fit: got %f, want 0.5", ds.Weight)
		}
	}
}

func TestRuntimeDegraded_NotFailClosed(t *testing.T) {
	s := newTestScorer(nil)
	c := FixtureCandidateHealthy()
	c.RuntimeHealth = RuntimeDegraded
	req := FixtureTaskImplementation()

	r := s.Score(c, req)
	if r.FailClosed {
		t.Fatal("degraded runtime must NOT fail closed (only offline/unresponsive do)")
	}

	for _, ds := range r.Dimensions {
		if ds.Dimension == DimRuntimeHealth {
			if ds.Score != 0.5 {
				t.Errorf("degraded health score: got %f, want 0.5", ds.Score)
			}
			return
		}
	}
	t.Error("runtime_health dimension not found")
}

func TestQuotaExhausted_FailsClosed(t *testing.T) {
	s := newTestScorer(nil)
	c := FixtureCandidateHealthy()
	c.Quota = QuotaExhausted
	req := FixtureTaskImplementation()

	r := s.Score(c, req)
	if !r.FailClosed {
		t.Fatal("exhausted quota must fail closed")
	}
	if r.FailReason != "quota_exhausted" {
		t.Fatalf("fail reason = %q, want quota_exhausted", r.FailReason)
	}
	if r.TotalScore != 0 {
		t.Fatalf("total score = %f, want 0", r.TotalScore)
	}
}

func TestIdentityStability(t *testing.T) {
	// Scoring the same candidate twice must produce the same AgentID.
	s := newTestScorer(nil)
	c := FixtureCandidateHealthy()
	req := FixtureTaskImplementation()

	r1 := s.Score(c, req)
	r2 := s.Score(c, req)

	if r1.CandidateID != r2.CandidateID {
		t.Errorf("identity must be stable: %s != %s", r1.CandidateID, r2.CandidateID)
	}
	if r1.CandidateName != r2.CandidateName {
		t.Errorf("name must be stable: %s != %s", r1.CandidateName, r2.CandidateName)
	}
}
