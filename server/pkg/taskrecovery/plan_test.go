package taskrecovery

import (
	"testing"
	"time"
)

// TestPlanForClassPolicies pins the per-class recovery policy: action,
// retryability, attempt budget, and first-retry backoff.
func TestPlanForClassPolicies(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		signals     Signals
		wantAction  Action
		wantRetry   bool
		wantMax     int
		wantBackoff time.Duration
	}{
		{
			name:        "runtime_outdated waits then retries",
			signals:     Signals{TaskID: "t1", FailureReason: "agent_error.runtime_version_unsupported"},
			wantAction:  ActionWaitForRuntime,
			wantRetry:   true,
			wantMax:     2,
			wantBackoff: 0,
		},
		{
			name:        "waiting_local_directory retries same employee",
			signals:     Signals{TaskID: "t2", TaskStatus: "waiting_local_directory"},
			wantAction:  ActionRetrySameEmployee,
			wantRetry:   true,
			wantMax:     3,
			wantBackoff: 0,
		},
		{
			name:        "quota_exhausted fails closed without alternate",
			signals:     Signals{TaskID: "t3", FailureReason: "agent_error.provider_quota_limit"},
			wantAction:  ActionEscalateManual,
			wantRetry:   false,
			wantMax:     1,
			wantBackoff: 0,
		},
		{
			name:        "reviewer_missing fails closed without alternate",
			signals:     Signals{TaskID: "t4", TaskKind: "review", ReviewerConfigured: false},
			wantAction:  ActionEscalateManual,
			wantRetry:   false,
			wantMax:     1,
			wantBackoff: 0,
		},
		{
			name:        "dirty_worktree repairs then retries",
			signals:     Signals{TaskID: "t5", WorktreeDirty: true},
			wantAction:  ActionRepairWorktree,
			wantRetry:   true,
			wantMax:     2,
			wantBackoff: 0,
		},
		{
			name:        "missing_receipt recreates then retries",
			signals:     Signals{TaskID: "t6", TaskStatus: "failed", ReceiptPresent: false},
			wantAction:  ActionRecreateReceipt,
			wantRetry:   true,
			wantMax:     2,
			wantBackoff: 0,
		},
		{
			name:        "crash retries with fresh session",
			signals:     Signals{TaskID: "t7", FailureReason: "agent_error.process_failure"},
			wantAction:  ActionRetrySameEmployee,
			wantRetry:   true,
			wantMax:     3,
			wantBackoff: 0,
		},
		{
			name:        "unknown escalates",
			signals:     Signals{TaskID: "t8", TaskStatus: "failed", ReceiptPresent: true, ErrorText: "mystery"},
			wantAction:  ActionEscalateManual,
			wantRetry:   false,
			wantMax:     1,
			wantBackoff: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := PlanFor(c.signals, Options{Attempt: 1})
			if p.Class != Classify(c.signals) {
				t.Errorf("plan class = %q, want %q", p.Class, Classify(c.signals))
			}
			if p.Action != c.wantAction {
				t.Errorf("action = %q, want %q", p.Action, c.wantAction)
			}
			if p.Retryable != c.wantRetry {
				t.Errorf("retryable = %v, want %v", p.Retryable, c.wantRetry)
			}
			if p.MaxAttempts != c.wantMax {
				t.Errorf("max_attempts = %d, want %d", p.MaxAttempts, c.wantMax)
			}
			if p.NextBackoff != c.wantBackoff {
				t.Errorf("next_backoff = %v, want %v", p.NextBackoff, c.wantBackoff)
			}
		})
	}
}

// TestPlanForBackoffSchedule pins the attempt-indexed backoff: the delay
// before the next attempt grows as attempts are consumed, and past the
// schedule the last delay is reused.
func TestPlanForBackoffSchedule(t *testing.T) {
	t.Parallel()

	signals := Signals{TaskID: "t", TaskStatus: "waiting_local_directory"}
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 0},
		{attempt: 2, want: 30 * time.Second},
		{attempt: 3, want: 2 * time.Minute},
		// Past the schedule: reuse the last delay.
		{attempt: 4, want: 2 * time.Minute},
	}
	for _, c := range cases {
		t.Run("attempt "+string(rune('0'+c.attempt)), func(t *testing.T) {
			p := PlanFor(signals, Options{Attempt: c.attempt})
			if p.NextBackoff != c.want {
				t.Errorf("attempt %d next_backoff = %v, want %v", c.attempt, p.NextBackoff, c.want)
			}
		})
	}
}

// TestPlanForAttemptBudget pins the no-blind-retry contract: once the attempt
// budget is exhausted the plan escalates even for a retryable class.
func TestPlanForAttemptBudget(t *testing.T) {
	t.Parallel()

	signals := Signals{TaskID: "t", TaskStatus: "waiting_local_directory"} // retryable, max 3
	cases := []struct {
		attempt    int
		wantRetry  bool
		wantAction Action
	}{
		{attempt: 1, wantRetry: true, wantAction: ActionRetrySameEmployee},
		{attempt: 2, wantRetry: true, wantAction: ActionRetrySameEmployee},
		{attempt: 3, wantRetry: false, wantAction: ActionEscalateManual},
		{attempt: 4, wantRetry: false, wantAction: ActionEscalateManual},
	}
	for _, c := range cases {
		t.Run("attempt "+string(rune('0'+c.attempt)), func(t *testing.T) {
			p := PlanFor(signals, Options{Attempt: c.attempt})
			if p.Retryable != c.wantRetry {
				t.Errorf("attempt %d retryable = %v, want %v", c.attempt, p.Retryable, c.wantRetry)
			}
			if p.Action != c.wantAction {
				t.Errorf("attempt %d action = %q, want %q", c.attempt, p.Action, c.wantAction)
			}
		})
	}
}

// TestPlanForCallerMaxAttempts pins that a caller-supplied max_attempts wins
// over the class default.
func TestPlanForCallerMaxAttempts(t *testing.T) {
	t.Parallel()

	signals := Signals{TaskID: "t", TaskStatus: "waiting_local_directory"} // class default 3
	p := PlanFor(signals, Options{Attempt: 1, MaxAttempts: 5})
	if p.MaxAttempts != 5 {
		t.Errorf("max_attempts = %d, want 5 (caller override)", p.MaxAttempts)
	}
	// Budget is now 5, so attempt 3 still retries.
	p = PlanFor(signals, Options{Attempt: 3, MaxAttempts: 5})
	if !p.Retryable {
		t.Errorf("attempt 3 with max 5 should still be retryable")
	}
}

// TestPlanForCircuitBreaker pins the circuit-breaker contract: an open
// circuit suspends automatic retry and escalates regardless of class.
func TestPlanForCircuitBreaker(t *testing.T) {
	t.Parallel()

	signals := Signals{TaskID: "t", TaskStatus: "waiting_local_directory"} // retryable class
	open := PlanFor(signals, Options{Attempt: 1, Circuit: CircuitBreaker{State: CircuitOpen, FailureCount: 3, Threshold: 3}})
	if open.Retryable {
		t.Errorf("open circuit must not retry")
	}
	if open.Action != ActionEscalateManual {
		t.Errorf("open circuit action = %q, want %q", open.Action, ActionEscalateManual)
	}
	if open.Circuit.State != CircuitOpen {
		t.Errorf("circuit state = %q, want open", open.Circuit.State)
	}

	// A closed circuit with a default threshold still retries.
	closed := PlanFor(signals, Options{Attempt: 1, Circuit: CircuitBreaker{State: CircuitClosed, FailureCount: 0}})
	if !closed.Retryable {
		t.Errorf("closed circuit should retry")
	}
	if closed.Circuit.Threshold != defaultCircuitThreshold {
		t.Errorf("default threshold = %d, want %d", closed.Circuit.Threshold, defaultCircuitThreshold)
	}
}

// TestPlanForAlternateEmployee pins the read-only alternate-employee
// recommendation: it is only offered for classes where a different employee
// can help, the failed employee is excluded, and it is never written.
func TestPlanForAlternateEmployee(t *testing.T) {
	t.Parallel()

	healthy := []EmployeeRef{
		{EmployeeID: "emp-failed", AgentID: "ag-failed", DisplayName: "Failed"},
		{EmployeeID: "emp-2", AgentID: "ag-2", DisplayName: "Healthy 2"},
		{EmployeeID: "emp-3", AgentID: "ag-3", DisplayName: "Healthy 3"},
	}

	// Quota exhaustion (non-retryable) offers an alternate.
	p := PlanFor(
		Signals{TaskID: "t", FailureReason: "agent_error.provider_quota_limit"},
		Options{Attempt: 1, HealthyEmployees: healthy, FailedEmployeeID: "emp-failed"},
	)
	if p.AlternateEmployee == nil {
		t.Fatalf("quota plan should recommend an alternate employee")
	}
	if p.AlternateEmployee.EmployeeID != "emp-2" {
		t.Errorf("alternate = %q, want emp-2 (failed employee excluded)", p.AlternateEmployee.EmployeeID)
	}
	if p.Action != ActionReassignEmployee {
		t.Errorf("quota action = %q, want %q", p.Action, ActionReassignEmployee)
	}

	// Missing reviewer follows the same rule: reassignment is executable only
	// when a verified alternate reviewer exists.
	p = PlanFor(
		Signals{TaskID: "review", TaskKind: "review", ReviewerConfigured: false},
		Options{Attempt: 1, HealthyEmployees: healthy, FailedEmployeeID: "emp-failed"},
	)
	if p.AlternateEmployee == nil || p.AlternateEmployee.EmployeeID != "emp-2" {
		t.Fatalf("reviewer plan alternate = %+v, want emp-2", p.AlternateEmployee)
	}
	if p.Action != ActionReassignEmployee {
		t.Errorf("reviewer action = %q, want %q", p.Action, ActionReassignEmployee)
	}

	// A retryable class with budget remaining does not reassign.
	p = PlanFor(
		Signals{TaskID: "t", TaskStatus: "waiting_local_directory"},
		Options{Attempt: 1, HealthyEmployees: healthy, FailedEmployeeID: "emp-failed"},
	)
	if p.AlternateEmployee != nil {
		t.Errorf("retryable plan must not recommend reassignment, got %+v", p.AlternateEmployee)
	}

	// No healthy alternate available: nil recommendation, no panic.
	p = PlanFor(
		Signals{TaskID: "t", FailureReason: "agent_error.provider_quota_limit"},
		Options{Attempt: 1, HealthyEmployees: nil, FailedEmployeeID: "emp-failed"},
	)
	if p.AlternateEmployee != nil {
		t.Errorf("no healthy employees should yield nil alternate")
	}
	if p.Action != ActionEscalateManual {
		t.Errorf("missing alternate action = %q, want %q", p.Action, ActionEscalateManual)
	}
}

// TestPlanForLineage pins the lineage-preserving recommendation: the retry
// links back to the parent via retry_of_task_id, and a crash forces a fresh
// session.
func TestPlanForLineage(t *testing.T) {
	t.Parallel()

	p := PlanFor(Signals{TaskID: "task-42", FailureReason: "agent_error.process_failure"}, Options{Attempt: 1})
	if p.Lineage.ParentTaskID != "task-42" {
		t.Errorf("parent = %q, want task-42", p.Lineage.ParentTaskID)
	}
	if p.Lineage.RetryOfTaskID != "task-42" {
		t.Errorf("retry_of = %q, want task-42", p.Lineage.RetryOfTaskID)
	}
	if !p.Lineage.FreshSession {
		t.Errorf("crash retry must force a fresh session")
	}

	// A non-crash retry resumes the session.
	p = PlanFor(Signals{TaskID: "task-43", TaskStatus: "waiting_local_directory"}, Options{Attempt: 1})
	if p.Lineage.FreshSession {
		t.Errorf("waiting_local_directory retry must not force a fresh session")
	}
}

// TestPlanForIdempotent pins the idempotency contract: identical
// (Signals, Options) always yield an identical plan and fingerprint.
func TestPlanForIdempotent(t *testing.T) {
	t.Parallel()

	signals := Signals{TaskID: "task-1", TaskStatus: "waiting_local_directory", WaitReason: "lock"}
	opts := Options{Attempt: 2, MaxAttempts: 3, Circuit: CircuitBreaker{State: CircuitClosed, FailureCount: 1, Threshold: 3}}

	a := PlanFor(signals, opts)
	b := PlanFor(signals, opts)
	if a.Fingerprint() != b.Fingerprint() {
		t.Errorf("same inputs produced different fingerprints: %q vs %q", a.Fingerprint(), b.Fingerprint())
	}
	if a.Action != b.Action || a.Retryable != b.Retryable || a.NextBackoff != b.NextBackoff {
		t.Errorf("same inputs produced different plans: %+v vs %+v", a, b)
	}
}

// TestPlanForFingerprintSensitive pins that the fingerprint changes when the
// actionable decision changes, so a caller can rely on it for dedup.
func TestPlanForFingerprintSensitive(t *testing.T) {
	t.Parallel()

	base := Signals{TaskID: "task-1", TaskStatus: "waiting_local_directory"}
	p1 := PlanFor(base, Options{Attempt: 1})
	p2 := PlanFor(base, Options{Attempt: 3}) // budget exhausted -> escalate
	if p1.Fingerprint() == p2.Fingerprint() {
		t.Errorf("fingerprints must differ when the decision changes")
	}
	if p1.Fingerprint() == "" || p2.Fingerprint() == "" {
		t.Errorf("fingerprint must be non-empty")
	}
}
