// Package runtimehealth is a candidate, preview-only health read model and
// replacement-recommendation flow for runtime failover (HIVECREW self-evolution
// slice HIV-406).
//
// It exists to answer one question: when a digital Employee's current
// Agent/Runtime binding goes unhealthy, which replacement binding should the
// Employee move to — without changing the Employee's stable identity, and
// without flapping between bindings?
//
// The model is deliberately decoupled from the live dispatch path. It reads
// runtime health through injectable ProbeFuncs and writes recommendations
// through an in-memory candidate surface only. It does NOT change live
// agent.runtime_id bindings, restart the daemon, apply a DB migration, or
// touch the Goal/Registry/Employee/Formal-Outcome authorities. Formal
// promotion of a replacement requires a reviewed change through the write
// authority matrix (see docs/architecture/WRITE-AUTHORITY-MATRIX.md); this
// package only computes and previews the candidate.
//
// The probe dimensions mirror the real failure surface observed in the
// Lighthouse openclaw incident and codified in the existing agent backends:
//
//	executable    — is command_name resolvable on PATH? (exec.LookPath)
//	version       — does the detected version meet the runtime floor?
//	model_endpoint — can the bound model endpoint serve a request?
//	quota         — is model/endpoint quota available (not exhausted)?
//	task_start    — can a task actually start (runtime online, daemon alive)?
//	readback      — can a started task return a parseable result?
//
// Each dimension is independently failable, which is what makes a binding
// "unhealthy but repairable by replacement" rather than "unhealthy, give up".
package runtimehealth

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Status is the health status of a single probe dimension or an aggregate
// binding. It is a stable, client-localizable enum in the style of
// dispatch.ReasonCode — never reverse-engineered from a human-readable string.
type Status string

const (
	// StatusHealthy: the dimension passed its check.
	StatusHealthy Status = "healthy"
	// StatusDegraded: the dimension answered but with a warning (slow, near
	// quota ceiling, version at floor). Degraded dimensions do not on their
	// own trigger a replacement, but they lower a binding's fitness score.
	StatusDegraded Status = "degraded"
	// StatusUnhealthy: the dimension failed. A binding is unhealthy overall
	// when any hard dimension (executable/version/task_start/readback) is
	// unhealthy, or when model_endpoint+quota are both unhealthy.
	StatusUnhealthy Status = "unhealthy"
	// StatusUnknown: the probe could not determine a status (not configured,
	// timed out without a definitive answer). Treated conservatively as
	// unhealthy for hard dimensions, degraded for soft ones.
	StatusUnknown Status = "unknown"
)

// ProbeKind names one dimension of the runtime health read model.
type ProbeKind string

const (
	ProbeExecutable    ProbeKind = "executable"
	ProbeVersion       ProbeKind = "version"
	ProbeModelEndpoint ProbeKind = "model_endpoint"
	ProbeQuota         ProbeKind = "quota"
	ProbeTaskStart     ProbeKind = "task_start"
	ProbeReadback      ProbeKind = "readback"
)

// hardProbes are the dimensions whose failure makes a binding definitively
// unusable regardless of the other dimensions. Soft dimensions (model_endpoint,
// quota) only fail the binding when BOTH are down — a model endpoint that is
// reachable but quota-exhausted is repairable by retry, not replacement.
var hardProbes = map[ProbeKind]bool{
	ProbeExecutable: true,
	ProbeVersion:    true,
	ProbeTaskStart:  true,
	ProbeReadback:   true,
}

// Binding is one (Employee, Agent, Runtime, Profile) quadruple. It is the unit
// of replacement: an Employee keeps its stable identity while the Agent/Runtime
// half is swapped. This mirrors the B2 separation (Employee ≠ Agent ≠ Runtime)
// recorded in docs/architecture/HIVECREW-B2-COMPANY-OBJECT-WORKFORCE-MODEL.md.
type Binding struct {
	EmployeeID     string `json:"employee_id"`
	EmployeeName   string `json:"employee_name,omitempty"`
	AgentID        string `json:"agent_id"`
	AgentName      string `json:"agent_name,omitempty"`
	RuntimeID      string `json:"runtime_id"`
	RuntimeName    string `json:"runtime_name,omitempty"`
	ProfileID      string `json:"profile_id"`
	ProtocolFamily string `json:"protocol_family"`
	CommandName    string `json:"command_name"`
	Model          string `json:"model,omitempty"`
}

// sameEmployee reports whether two bindings refer to the same stable Employee
// identity (the half that must NOT change during a replacement).
func (b Binding) sameEmployee(other Binding) bool {
	return b.EmployeeID != "" && b.EmployeeID == other.EmployeeID
}

// ProbeResult is the outcome of checking one dimension of a binding.
type ProbeResult struct {
	Kind      ProbeKind     `json:"kind"`
	Status    Status        `json:"status"`
	Detail    string        `json:"detail,omitempty"`
	Latency   time.Duration `json:"latency,omitempty"`
	CheckedAt time.Time     `json:"checked_at"`
}

// HealthSnapshot is the aggregate read model for one binding at one moment.
// It is the input to the Recommender.
type HealthSnapshot struct {
	Binding    Binding       `json:"binding"`
	Results    []ProbeResult `json:"results"`
	Overall    Status        `json:"overall"`
	Score      int           `json:"score"` // 0–100 fitness score; higher = healthier
	ComputedAt time.Time     `json:"computed_at"`
}

// ProbeFunc checks one dimension of a binding and returns its result. It is
// injected per-dimension so the Prober can run against real exec/HTTP calls in
// production and against fixtures in tests. A nil ProbeFunc for a kind means
// "this dimension is not applicable"; the Prober records StatusUnknown for it.
type ProbeFunc func(ctx context.Context, b Binding) ProbeResult

// Prober runs the configured probe dimensions against a binding and folds the
// per-dimension results into one HealthSnapshot. The zero value probes nothing;
// callers assign the ProbeFunc for each dimension they want measured.
type Prober struct {
	Executable    ProbeFunc
	Version       ProbeFunc
	ModelEndpoint ProbeFunc
	Quota         ProbeFunc
	TaskStart     ProbeFunc
	Readback      ProbeFunc
	// now returns the current time; replaced in tests. Defaults to time.Now.
	now func() time.Time
}

func (p *Prober) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// Probe runs every configured dimension against b and returns the aggregate
// snapshot. Dimensions with a nil ProbeFunc are recorded as StatusUnknown and
// do not contribute latency. The aggregate Overall status and Score are
// computed by foldResults.
func (p *Prober) Probe(ctx context.Context, b Binding) HealthSnapshot {
	now := p.clock()
	kinds := []struct {
		kind ProbeKind
		fn   ProbeFunc
	}{
		{ProbeExecutable, p.Executable},
		{ProbeVersion, p.Version},
		{ProbeModelEndpoint, p.ModelEndpoint},
		{ProbeQuota, p.Quota},
		{ProbeTaskStart, p.TaskStart},
		{ProbeReadback, p.Readback},
	}
	results := make([]ProbeResult, 0, len(kinds))
	for _, k := range kinds {
		if k.fn == nil {
			results = append(results, ProbeResult{Kind: k.kind, Status: StatusUnknown, CheckedAt: now})
			continue
		}
		r := k.fn(ctx, b)
		if r.Kind == "" {
			r.Kind = k.kind
		}
		if r.CheckedAt.IsZero() {
			r.CheckedAt = now
		}
		results = append(results, r)
	}
	overall, score := foldResults(results)
	return HealthSnapshot{
		Binding:    b,
		Results:    results,
		Overall:    overall,
		Score:      score,
		ComputedAt: now,
	}
}

// foldResults computes the aggregate Overall status and a 0–100 fitness Score
// from the per-dimension results.
//
// Overall is Unhealthy when any hard probe is unhealthy/unknown, or when both
// model_endpoint and quota are unhealthy. It is Degraded when no dimension is
// unhealthy but at least one is degraded/unknown. Otherwise Healthy.
//
// Score starts at 100 and is reduced per dimension: hard unhealthy −40, hard
// unknown −30, soft unhealthy −20, soft unknown −10, any degraded −8. The
// score is clamped to [0,100]. A replacement is only recommended when the
// current binding is Unhealthy AND a candidate scores meaningfully higher.
func foldResults(results []ProbeResult) (Status, int) {
	score := 100
	hardDown := false
	softDown := false // any soft probe unhealthy/unknown (degrades but does not alone fail)
	anyDegraded := false
	modelEndpointDown := false
	quotaDown := false
	for _, r := range results {
		isHard := hardProbes[r.Kind]
		switch r.Status {
		case StatusHealthy:
			// no penalty
		case StatusDegraded:
			score -= 8
			anyDegraded = true
		case StatusUnhealthy:
			if isHard {
				score -= 40
				hardDown = true
			} else {
				score -= 20
				softDown = true
			}
		case StatusUnknown:
			if isHard {
				score -= 30
				hardDown = true
			} else {
				score -= 10
				softDown = true
			}
		}
		if r.Kind == ProbeModelEndpoint && (r.Status == StatusUnhealthy || r.Status == StatusUnknown) {
			modelEndpointDown = true
		}
		if r.Kind == ProbeQuota && (r.Status == StatusUnhealthy || r.Status == StatusUnknown) {
			quotaDown = true
		}
	}
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	switch {
	case hardDown, modelEndpointDown && quotaDown:
		return StatusUnhealthy, score
	case anyDegraded, softDown:
		return StatusDegraded, score
	default:
		return StatusHealthy, score
	}
}

// --- Recommendation -------------------------------------------------------

// Recommendation is a preview-only proposal to move an Employee from one
// binding to another. It is never applied by this package; the caller (a
// future reviewed promotion command) is responsible for the actual binding
// change through the write authority matrix.
type Recommendation struct {
	EmployeeID  string    `json:"employee_id"`
	From        Binding   `json:"from"`
	To          Binding   `json:"to"`
	Reason      string    `json:"reason"`
	Confidence  float64   `json:"confidence"`            // 0–1
	RollbackTo  Binding   `json:"rollback_to,omitempty"` // the binding to restore if To also fails
	GeneratedAt time.Time `json:"generated_at"`
}

// Recommender turns health snapshots into replacement recommendations. It is
// the pure decision core: given the current binding's health and a set of
// candidate replacement bindings (each already probed), it picks the
// highest-scoring healthy candidate — subject to a cooldown that prevents
// flapping and a rollback rule that reverts to the prior binding if the
// replacement is itself unhealthy on its first evaluation.
type Recommender struct {
	// Cooldown is the minimum interval between two replacements for the same
	// Employee. Zero disables cooldown (tests only).
	Cooldown time.Duration
	// MinImprovement is the minimum score delta (candidate − current) required
	// to recommend a replacement. Prevents churning on marginal differences.
	MinImprovement int
}

// DefaultMinImprovement is used when Recommender.MinImprovement is zero.
const DefaultMinImprovement = 25

// Decide evaluates whether to recommend replacing current with one of the
// candidates. Candidates must all be probed (carry their own HealthSnapshot).
// The current binding's Employee identity is preserved on the recommendation;
// only the Agent/Runtime half changes.
//
// Returns (recommendation, nil) when a replacement is warranted, or
// (zero, nil) when the current binding should be kept. Returns an error only
// on a precondition violation (e.g. candidate identity mismatch).
func (r *Recommender) Decide(current HealthSnapshot, candidates []HealthSnapshot, cd *Cooldown, now time.Time) (Recommendation, error) {
	if current.Binding.EmployeeID == "" {
		return Recommendation{}, errors.New("runtimehealth: current binding has no employee identity")
	}
	for _, c := range candidates {
		if !c.Binding.sameEmployee(current.Binding) {
			return Recommendation{}, fmt.Errorf("runtimehealth: candidate %q is not the same employee as current %q", c.Binding.AgentID, current.Binding.AgentID)
		}
	}

	// Only an unhealthy current binding triggers a replacement search.
	// Degraded bindings are monitored, not replaced.
	if current.Overall != StatusUnhealthy {
		return Recommendation{}, nil
	}

	minImprovement := r.MinImprovement
	if minImprovement == 0 {
		minImprovement = DefaultMinImprovement
	}

	// Rank healthy candidates by score desc, then by name for determinism.
	eligible := make([]HealthSnapshot, 0, len(candidates))
	for _, c := range candidates {
		if c.Overall == StatusHealthy && c.Score-current.Score >= minImprovement {
			eligible = append(eligible, c)
		}
	}
	if len(eligible) == 0 {
		return Recommendation{}, nil
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].Score != eligible[j].Score {
			return eligible[i].Score > eligible[j].Score
		}
		return eligible[i].Binding.AgentID < eligible[j].Binding.AgentID
	})
	pick := eligible[0]

	// Cooldown gate: don't flap. If the employee switched recently, hold.
	if cd != nil && r.Cooldown > 0 {
		if !cd.Allow(current.Binding.EmployeeID, now, r.Cooldown) {
			return Recommendation{}, nil
		}
	}

	toBinding := pick.Binding
	// Preserve the Employee's stable identity half on the destination.
	toBinding.EmployeeID = current.Binding.EmployeeID
	toBinding.EmployeeName = current.Binding.EmployeeName

	rec := Recommendation{
		EmployeeID:  current.Binding.EmployeeID,
		From:        current.Binding,
		To:          toBinding,
		Reason:      summarizeReason(current),
		Confidence:  confidenceFor(current, pick),
		RollbackTo:  current.Binding,
		GeneratedAt: now,
	}
	if cd != nil && r.Cooldown > 0 {
		cd.Record(current.Binding.EmployeeID, now)
	}
	return rec, nil
}

// summarizeReason produces a stable, machine-greppable reason string from the
// unhealthy dimensions of the current snapshot. It lists each failing
// dimension with an "_unhealthy" suffix, hard probes first then soft ones,
// e.g. "version_unhealthy,readback_unhealthy,model_endpoint_unhealthy".
func summarizeReason(s HealthSnapshot) string {
	var hard, soft []string
	for _, r := range s.Results {
		if r.Status == StatusUnhealthy || r.Status == StatusUnknown {
			if hardProbes[r.Kind] {
				hard = append(hard, string(r.Kind)+"_unhealthy")
			} else {
				soft = append(soft, string(r.Kind)+"_unhealthy")
			}
		}
	}
	parts := append(hard, soft...)
	if len(parts) == 0 {
		return "unhealthy"
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "," + p
	}
	return out
}

// confidenceFor is a simple, transparent confidence heuristic: the bigger the
// health gap between the replacement and the current binding, the higher the
// confidence, capped at 1.0. A replacement that merely edges out a broken
// binding is low-confidence; one that is fully healthy against a hard-down
// binding is high-confidence.
func confidenceFor(current, pick HealthSnapshot) float64 {
	delta := pick.Score - current.Score
	c := float64(delta) / 100.0
	if c < 0 {
		c = 0
	}
	if c > 1 {
		c = 1
	}
	return c
}

// --- Cooldown -------------------------------------------------------------

// Cooldown records the last replacement time per Employee so the Recommender
// does not flap a binding back and forth. It is safe for concurrent use.
type Cooldown struct {
	mu         sync.RWMutex
	lastSwitch map[string]time.Time
}

// NewCooldown returns an empty Cooldown.
func NewCooldown() *Cooldown {
	return &Cooldown{lastSwitch: make(map[string]time.Time)}
}

// Allow reports whether employeeID may be switched at now, given that switches
// must be spaced at least cooldown apart. Allow does not record the switch —
// call Record for that.
func (c *Cooldown) Allow(employeeID string, now time.Time, cooldown time.Duration) bool {
	if c == nil {
		return true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	last, ok := c.lastSwitch[employeeID]
	if !ok {
		return true
	}
	return now.Sub(last) >= cooldown
}

// Record marks a switch for employeeID at now.
func (c *Cooldown) Record(employeeID string, now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastSwitch[employeeID] = now
}

// --- Rollback -------------------------------------------------------------

// ShouldRollback reports whether a completed replacement should be reverted.
// It returns true when the replacement binding's first post-switch health
// evaluation is itself Unhealthy — meaning the replacement did not actually
// fix the problem and the Employee is better off on its prior binding while a
// new candidate is found. The caller restores Recommendation.RollbackTo.
func ShouldRollback(replacement HealthSnapshot) bool {
	return replacement.Overall == StatusUnhealthy
}
