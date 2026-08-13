package runtimehealth

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- Fixture: the Lighthouse openclaw failure ------------------------------
//
// The fixture encodes the real-world incident that motivated this slice: an
// Employee bound to an openclaw Agent/Runtime whose executable is present but
// whose version is below the runtime floor (minOpenclawVersion 2026.5.5), and
// whose gateway model endpoint is unreachable. The binding can start tasks but
// cannot read back parseable output — so task_start is healthy while readback
// is unhealthy. A healthy claude candidate binding exists as a replacement.

func lighthouseOpenclawBinding() Binding {
	return Binding{
		EmployeeID:     "EMP-LIGHTHOUSE-001",
		EmployeeName:   "Lighthouse Keeper",
		AgentID:        "agt-openclaw-lighthouse",
		AgentName:      "openclaw-lighthouse",
		RuntimeID:      "rt-openclaw-01",
		RuntimeName:    "dgx-openclaw",
		ProfileID:      "prof-openclaw",
		ProtocolFamily: "openclaw",
		CommandName:    "openclaw",
		Model:          "openclaw/anthropic/claude-sonnet",
	}
}

func lighthouseClaudeCandidate() Binding {
	b := lighthouseOpenclawBinding()
	b.AgentID = "agt-claude-lighthouse"
	b.AgentName = "claude-lighthouse"
	b.RuntimeID = "rt-claude-01"
	b.RuntimeName = "mac-claude"
	b.ProfileID = "prof-claude"
	b.ProtocolFamily = "claude"
	b.CommandName = "claude"
	b.Model = "claude-sonnet-4"
	return b
}

// openclawFailureProber returns a Prober wired with fixture ProbeFuncs that
// reproduce the Lighthouse openclaw failure: executable present, version below
// floor, model endpoint unreachable, quota unknown, task starts, readback
// fails (the defining symptom — tasks spawn but produce no parseable output,
// matching agent.openclawNoParseableOutput).
func openclawFailureProber() *Prober {
	return &Prober{
		Executable: func(_ context.Context, _ Binding) ProbeResult {
			return ProbeResult{Kind: ProbeExecutable, Status: StatusHealthy, Detail: "found at /usr/local/bin/openclaw", Latency: 3 * time.Millisecond}
		},
		Version: func(_ context.Context, _ Binding) ProbeResult {
			return ProbeResult{Kind: ProbeVersion, Status: StatusUnhealthy, Detail: "detected 2026.4.1, minimum 2026.5.5", Latency: 180 * time.Millisecond}
		},
		ModelEndpoint: func(_ context.Context, _ Binding) ProbeResult {
			return ProbeResult{Kind: ProbeModelEndpoint, Status: StatusUnhealthy, Detail: "gateway host unreachable: dial tcp 10.0.0.42:443: i/o timeout", Latency: 5 * time.Second}
		},
		Quota: func(_ context.Context, _ Binding) ProbeResult {
			return ProbeResult{Kind: ProbeQuota, Status: StatusUnknown, Detail: "quota probe not configured for openclaw gateway"}
		},
		TaskStart: func(_ context.Context, _ Binding) ProbeResult {
			return ProbeResult{Kind: ProbeTaskStart, Status: StatusHealthy, Detail: "runtime online, daemon heartbeat fresh", Latency: 12 * time.Millisecond}
		},
		Readback: func(_ context.Context, _ Binding) ProbeResult {
			return ProbeResult{Kind: ProbeReadback, Status: StatusUnhealthy, Detail: "openclaw returned no parseable output", Latency: 2 * time.Second}
		},
		now: func() time.Time { return fixedNow },
	}
}

// healthyClaudeProber returns a Prober that reports every dimension healthy —
// the shape of a good replacement candidate.
func healthyClaudeProber() *Prober {
	healthy := func(_ context.Context, _ Binding) ProbeResult {
		return ProbeResult{Status: StatusHealthy, Latency: 10 * time.Millisecond}
	}
	return &Prober{
		Executable:    healthy,
		Version:       healthy,
		ModelEndpoint: healthy,
		Quota:         healthy,
		TaskStart:     healthy,
		Readback:      healthy,
		now:           func() time.Time { return fixedNow },
	}
}

var fixedNow = time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)

// --- foldResults ----------------------------------------------------------

func TestFoldResults(t *testing.T) {
	tests := []struct {
		name       string
		results    []ProbeResult
		wantStatus Status
		wantScore  int
	}{
		{
			name: "all healthy",
			results: []ProbeResult{
				{Kind: ProbeExecutable, Status: StatusHealthy},
				{Kind: ProbeVersion, Status: StatusHealthy},
				{Kind: ProbeTaskStart, Status: StatusHealthy},
				{Kind: ProbeReadback, Status: StatusHealthy},
			},
			wantStatus: StatusHealthy,
			wantScore:  100,
		},
		{
			name: "single degraded stays degraded",
			results: []ProbeResult{
				{Kind: ProbeExecutable, Status: StatusHealthy},
				{Kind: ProbeVersion, Status: StatusDegraded},
			},
			wantStatus: StatusDegraded,
			wantScore:  92,
		},
		{
			name: "hard unhealthy fails the binding",
			results: []ProbeResult{
				{Kind: ProbeExecutable, Status: StatusHealthy},
				{Kind: ProbeReadback, Status: StatusUnhealthy},
			},
			wantStatus: StatusUnhealthy,
			wantScore:  60,
		},
		{
			name: "both soft unhealthy fails the binding",
			results: []ProbeResult{
				{Kind: ProbeModelEndpoint, Status: StatusUnhealthy},
				{Kind: ProbeQuota, Status: StatusUnhealthy},
			},
			wantStatus: StatusUnhealthy,
			wantScore:  60,
		},
		{
			name: "one soft unhealthy alone is degraded not unhealthy",
			results: []ProbeResult{
				{Kind: ProbeModelEndpoint, Status: StatusUnhealthy},
				{Kind: ProbeQuota, Status: StatusHealthy},
			},
			wantStatus: StatusDegraded,
			wantScore:  80,
		},
		{
			name: "unknown hard probe treated as unhealthy",
			results: []ProbeResult{
				{Kind: ProbeExecutable, Status: StatusUnknown},
			},
			wantStatus: StatusUnhealthy,
			wantScore:  70,
		},
		{
			name: "score clamps to zero",
			results: []ProbeResult{
				{Kind: ProbeExecutable, Status: StatusUnhealthy},
				{Kind: ProbeVersion, Status: StatusUnhealthy},
				{Kind: ProbeTaskStart, Status: StatusUnhealthy},
				{Kind: ProbeReadback, Status: StatusUnhealthy},
			},
			wantStatus: StatusUnhealthy,
			wantScore:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStatus, gotScore := foldResults(tt.results)
			if gotStatus != tt.wantStatus {
				t.Errorf("status = %q, want %q", gotStatus, tt.wantStatus)
			}
			if gotScore != tt.wantScore {
				t.Errorf("score = %d, want %d", gotScore, tt.wantScore)
			}
		})
	}
}

// --- Prober ---------------------------------------------------------------

func TestProberRecordsUnknownForUnconfiguredDimensions(t *testing.T) {
	p := &Prober{now: func() time.Time { return fixedNow }}
	snap := p.Probe(context.Background(), lighthouseOpenclawBinding())
	if len(snap.Results) != 6 {
		t.Fatalf("got %d results, want 6 dimensions", len(snap.Results))
	}
	for _, r := range snap.Results {
		if r.Status != StatusUnknown {
			t.Errorf("dimension %q = %q, want unknown (no probe configured)", r.Kind, r.Status)
		}
	}
}

func TestProberOpenclawFailureFixtureIsUnhealthy(t *testing.T) {
	p := openclawFailureProber()
	snap := p.Probe(context.Background(), lighthouseOpenclawBinding())

	if snap.Overall != StatusUnhealthy {
		t.Fatalf("overall = %q, want unhealthy", snap.Overall)
	}
	// The defining symptoms of the Lighthouse incident:
	wantReadback := resultByKind(snap.Results, ProbeReadback)
	if wantReadback.Status != StatusUnhealthy {
		t.Errorf("readback = %q, want unhealthy", wantReadback.Status)
	}
	wantVersion := resultByKind(snap.Results, ProbeVersion)
	if wantVersion.Status != StatusUnhealthy {
		t.Errorf("version = %q, want unhealthy (below 2026.5.5 floor)", wantVersion.Status)
	}
	// task_start is the trap: the binding LOOKS alive because tasks spawn.
	wantTaskStart := resultByKind(snap.Results, ProbeTaskStart)
	if wantTaskStart.Status != StatusHealthy {
		t.Errorf("task_start = %q, want healthy (the misleading signal)", wantTaskStart.Status)
	}
}

func resultByKind(results []ProbeResult, kind ProbeKind) ProbeResult {
	for _, r := range results {
		if r.Kind == kind {
			return r
		}
	}
	return ProbeResult{}
}

// --- Recommender: the openclaw → claude replacement -----------------------

func TestRecommenderReplacesOpenclawWithClaude(t *testing.T) {
	current := openclawFailureProber().Probe(context.Background(), lighthouseOpenclawBinding())
	candidate := healthyClaudeProber().Probe(context.Background(), lighthouseClaudeCandidate())

	rec := Recommender{Cooldown: 5 * time.Minute}
	cd := NewCooldown()
	got, err := rec.Decide(current, []HealthSnapshot{candidate}, cd, fixedNow)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got.To.AgentID == "" {
		t.Fatal("expected a replacement recommendation, got none")
	}
	// Employee identity is preserved across the replacement.
	if got.To.EmployeeID != current.Binding.EmployeeID {
		t.Errorf("replacement employee_id = %q, want %q (identity must be stable)", got.To.EmployeeID, current.Binding.EmployeeID)
	}
	if got.To.EmployeeName != current.Binding.EmployeeName {
		t.Errorf("replacement employee_name = %q, want %q", got.To.EmployeeName, current.Binding.EmployeeName)
	}
	// The Agent/Runtime half changes.
	if got.To.AgentID == current.Binding.AgentID {
		t.Error("replacement agent_id equals current; expected a different Agent/Runtime binding")
	}
	if got.To.ProtocolFamily != "claude" {
		t.Errorf("replacement protocol_family = %q, want claude", got.To.ProtocolFamily)
	}
	// Rollback target is the prior (openclaw) binding.
	if got.RollbackTo.AgentID != current.Binding.AgentID {
		t.Errorf("rollback agent_id = %q, want %q", got.RollbackTo.AgentID, current.Binding.AgentID)
	}
	// Reason must mention the failing hard probes for diagnosability.
	if !strings.Contains(got.Reason, "readback_unhealthy") && !strings.Contains(got.Reason, "version_unhealthy") {
		t.Errorf("reason = %q, want it to cite version or readback failure", got.Reason)
	}
	// Cooldown recorded so a second immediate switch is suppressed.
	if !cd.lastSwitch[current.Binding.EmployeeID].Equal(fixedNow) {
		t.Error("cooldown was not recorded for the employee")
	}
}

// --- Recommender: does not recommend when current is healthy --------------

func TestRecommenderKeepsHealthyBinding(t *testing.T) {
	current := healthyClaudeProber().Probe(context.Background(), lighthouseClaudeCandidate())
	candidate := healthyClaudeProber().Probe(context.Background(), lighthouseOpenclawBinding())

	rec := Recommender{Cooldown: 5 * time.Minute}
	got, err := rec.Decide(current, []HealthSnapshot{candidate}, NewCooldown(), fixedNow)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got.To.AgentID != "" {
		t.Errorf("expected no recommendation for a healthy binding, got %+v", got)
	}
}

// --- Recommender: rejects cross-employee candidates -----------------------

func TestRecommenderRejectsCrossEmployeeCandidate(t *testing.T) {
	current := openclawFailureProber().Probe(context.Background(), lighthouseOpenclawBinding())
	foreign := healthyClaudeProber().Probe(context.Background(), Binding{
		EmployeeID:     "EMP-OTHER",
		AgentID:        "agt-claude-other",
		ProtocolFamily: "claude",
		CommandName:    "claude",
	})
	rec := Recommender{Cooldown: 5 * time.Minute}
	_, err := rec.Decide(current, []HealthSnapshot{foreign}, NewCooldown(), fixedNow)
	if err == nil {
		t.Fatal("expected an error for a cross-employee candidate, got nil")
	}
}

// --- Recommender: min improvement gate ------------------------------------

func TestRecommenderRequiresMinImprovement(t *testing.T) {
	current := openclawFailureProber().Probe(context.Background(), lighthouseOpenclawBinding())
	// Build a candidate that is only marginally better (degraded, not healthy).
	marginallyBetter := HealthSnapshot{
		Binding: lighthouseClaudeCandidate(),
		Overall: StatusDegraded,
		Score:   current.Score + 5, // below DefaultMinImprovement (25)
	}
	rec := Recommender{Cooldown: 5 * time.Minute}
	got, err := rec.Decide(current, []HealthSnapshot{marginallyBetter}, NewCooldown(), fixedNow)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got.To.AgentID != "" {
		t.Errorf("expected no recommendation for a marginal candidate, got %+v", got)
	}
}

// --- Cooldown: prevents flapping ------------------------------------------

func TestCooldownPreventsFlapping(t *testing.T) {
	current := openclawFailureProber().Probe(context.Background(), lighthouseOpenclawBinding())
	candidate := healthyClaudeProber().Probe(context.Background(), lighthouseClaudeCandidate())

	rec := Recommender{Cooldown: 5 * time.Minute}
	cd := NewCooldown()

	// First switch at fixedNow succeeds and records the cooldown.
	first, err := rec.Decide(current, []HealthSnapshot{candidate}, cd, fixedNow)
	if err != nil {
		t.Fatalf("first Decide: %v", err)
	}
	if first.To.AgentID == "" {
		t.Fatal("expected first recommendation")
	}

	// A second switch 1 minute later is suppressed by the 5-minute cooldown.
	second, err := rec.Decide(current, []HealthSnapshot{candidate}, cd, fixedNow.Add(1*time.Minute))
	if err != nil {
		t.Fatalf("second Decide: %v", err)
	}
	if second.To.AgentID != "" {
		t.Error("expected cooldown to suppress the second switch, but a recommendation was made")
	}

	// After the cooldown elapses, a switch is allowed again.
	third, err := rec.Decide(current, []HealthSnapshot{candidate}, cd, fixedNow.Add(6*time.Minute))
	if err != nil {
		t.Fatalf("third Decide: %v", err)
	}
	if third.To.AgentID == "" {
		t.Error("expected a recommendation after cooldown elapsed, got none")
	}
}

// --- Rollback -------------------------------------------------------------

func TestShouldRollbackWhenReplacementIsAlsoUnhealthy(t *testing.T) {
	// The claude replacement turns out to be unhealthy too (e.g. its model
	// endpoint is down). ShouldRollback returns true so the caller restores
	// the prior openclaw binding while a better candidate is found.
	unhealthyReplacement := (&Prober{
		Executable:    healthyFn,
		Version:       healthyFn,
		ModelEndpoint: unhealthyFn, // the new failure
		Quota:         healthyFn,
		TaskStart:     healthyFn,
		Readback:      unhealthyFn, // readback also fails
		now:           func() time.Time { return fixedNow },
	}).Probe(context.Background(), lighthouseClaudeCandidate())

	if !ShouldRollback(unhealthyReplacement) {
		t.Error("ShouldRollback = false for an unhealthy replacement, want true")
	}
}

func TestShouldNotRollbackWhenReplacementRecovered(t *testing.T) {
	healthyReplacement := healthyClaudeProber().Probe(context.Background(), lighthouseClaudeCandidate())
	if ShouldRollback(healthyReplacement) {
		t.Error("ShouldRollback = true for a healthy replacement, want false")
	}
}

// --- Cooldown unit --------------------------------------------------------

func TestCooldownAllowAndRecord(t *testing.T) {
	cd := NewCooldown()
	emp := "EMP-1"
	now := time.Now()

	if !cd.Allow(emp, now, time.Minute) {
		t.Error("first Allow = false, want true (no prior switch)")
	}
	cd.Record(emp, now)
	if cd.Allow(emp, now.Add(30*time.Second), time.Minute) {
		t.Error("Allow within cooldown = true, want false")
	}
	if !cd.Allow(emp, now.Add(time.Minute), time.Minute) {
		t.Error("Allow at cooldown boundary = false, want true")
	}
}

func TestNilCooldownAlwaysAllows(t *testing.T) {
	var cd *Cooldown
	if !cd.Allow("EMP", time.Now(), time.Minute) {
		t.Error("nil Cooldown.Allow = false, want true")
	}
	cd.Record("EMP", time.Now()) // must not panic
}

// TestCooldownConcurrentAccess verifies the "safe for concurrent use"
// contract under the race detector. Multiple goroutines concurrently call
// Allow (read lock) and Record (write lock) on the same Cooldown; -race will
// flag any unsynchronized map access. Run with: go test -race -count=1
func TestCooldownConcurrentAccess(t *testing.T) {
	cd := NewCooldown()
	const goroutines = 64
	const iterations = 200
	employees := []string{"EMP-A", "EMP-B", "EMP-C", "EMP-D"}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(seed int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				emp := employees[(seed+j)%len(employees)]
				now := time.Unix(int64(j), 0)
				cd.Allow(emp, now, time.Second)
				cd.Record(emp, now)
			}
		}(i)
	}
	wg.Wait()
}

// TestRecommenderConcurrentDecide verifies that concurrent Recommender.Decide
// calls sharing one Cooldown do not race on the underlying map. This mirrors
// the real-world scenario where multiple health evaluators run in parallel.
func TestRecommenderConcurrentDecide(t *testing.T) {
	current := openclawFailureProber().Probe(context.Background(), lighthouseOpenclawBinding())
	candidate := healthyClaudeProber().Probe(context.Background(), lighthouseClaudeCandidate())
	rec := Recommender{Cooldown: 5 * time.Minute}
	cd := NewCooldown()

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			now := fixedNow.Add(time.Duration(n) * time.Minute)
			_, _ = rec.Decide(current, []HealthSnapshot{candidate}, cd, now)
		}(i)
	}
	wg.Wait()
}

// --- Probe helpers --------------------------------------------------------

var healthyFn = func(_ context.Context, _ Binding) ProbeResult {
	return ProbeResult{Status: StatusHealthy, Latency: 5 * time.Millisecond}
}

var unhealthyFn = func(_ context.Context, _ Binding) ProbeResult {
	return ProbeResult{Status: StatusUnhealthy, Detail: "fixture failure"}
}
