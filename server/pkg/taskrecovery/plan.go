package taskrecovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Action is the recovery action the planner recommends for a failure class.
type Action string

const (
	// ActionRetrySameEmployee: retry the task on the same employee/runtime,
	// preserving lineage (retry_of_task_id). Used for transient, resume-safe
	// failures.
	ActionRetrySameEmployee Action = "retry_same_employee"
	// ActionRepairWorktree: clean/commit the dirty worktree, then retry.
	ActionRepairWorktree Action = "repair_worktree"
	// ActionWaitForRuntime: wait for the runtime to update/come online, then
	// retry.
	ActionWaitForRuntime Action = "wait_for_runtime"
	// ActionReassignEmployee: hand the task to an alternate healthy employee
	// (recommendation only — the planner never writes).
	ActionReassignEmployee Action = "reassign_employee"
	// ActionRecreateReceipt: re-claim/re-finalize the missing execution
	// receipt, then retry.
	ActionRecreateReceipt Action = "recreate_receipt"
	// ActionEscalateManual: hand to a human owner; no automatic retry.
	ActionEscalateManual Action = "escalate_manual"
	// ActionNoop: nothing to do.
	ActionNoop Action = "noop"
)

// CircuitState is the circuit-breaker state for a task lineage.
type CircuitState string

const (
	// CircuitClosed: normal operation; retries allowed.
	CircuitClosed CircuitState = "closed"
	// CircuitOpen: too many consecutive failures; retries suspended, escalate.
	CircuitOpen CircuitState = "open"
	// CircuitHalfOpen: a single trial retry is allowed to test recovery.
	CircuitHalfOpen CircuitState = "half_open"
)

// CircuitBreaker is the per-lineage failure budget. It is a pure value: the
// planner reads it and returns an updated recommendation; it never persists.
type CircuitBreaker struct {
	State        CircuitState
	FailureCount int
	Threshold    int
	Cooldown     time.Duration
}

// Lineage is the task-lineage recommendation. It is a recommendation only: the
// planner never creates a task, so these IDs describe what a caller SHOULD
// link a retry/repair to, never a second write path.
type Lineage struct {
	// ParentTaskID is the failed task the recovery acts on.
	ParentTaskID string
	// RetryOfTaskID is the task a retry should link via retry_of_task_id
	// (system retry, lineage-preserving).
	RetryOfTaskID string
	// RerunOfTaskID is the task a manual rerun should link via
	// rerun_of_task_id (distinct from a system retry).
	RerunOfTaskID string
	// FreshSession reports whether the retry must NOT resume the parent's
	// session (crash/poisoned history).
	FreshSession bool
}

// EmployeeRef is a read-only reference to a healthy employee from the org
// directory projection. The planner never writes it; it is a recommendation.
type EmployeeRef struct {
	EmployeeID  string
	AgentID     string
	DisplayName string
}

// Plan is the recovery recommendation for a failed task. It is a pure value:
// deterministic for a given (Signals, Options), never written by this package.
type Plan struct {
	Class             Class
	Action            Action
	Retryable         bool
	MaxAttempts       int
	NextBackoff       time.Duration
	Lineage           Lineage
	Circuit           CircuitBreaker
	AlternateEmployee *EmployeeRef
	Reason            string
}

// Options carries the caller's current task state into the planner.
type Options struct {
	// Attempt is the current attempt number (1-based).
	Attempt int
	// MaxAttempts is the task's configured max_attempts (0 = use class
	// default).
	MaxAttempts int
	// HealthyEmployees is the read-only list of healthy employees to pick an
	// alternate from (may be nil).
	HealthyEmployees []EmployeeRef
	// FailedEmployeeID is the employee that failed (excluded from alternates).
	FailedEmployeeID string
	// Circuit is the current circuit-breaker state for the lineage.
	Circuit CircuitBreaker
}

// classPolicy is the per-class recovery policy. backoff is indexed by attempt
// (0 = delay before the first retry after the initial failure).
type classPolicy struct {
	action      Action
	retryable   bool
	maxAttempts int
	backoff     []time.Duration
	reason      string
}

var classPolicies = map[Class]classPolicy{
	ClassRuntimeOutdated: {
		action:      ActionWaitForRuntime,
		retryable:   true,
		maxAttempts: 2,
		backoff:     []time.Duration{0, 5 * time.Minute},
		reason:      "runtime below minimum supported version; wait for update then retry same employee",
	},
	ClassWaitingLocalDirectory: {
		action:      ActionRetrySameEmployee,
		retryable:   true,
		maxAttempts: 3,
		backoff:     []time.Duration{0, 30 * time.Second, 2 * time.Minute},
		reason:      "task parked on a local-directory lock; retry after the lock clears",
	},
	ClassQuotaExhausted: {
		action:      ActionEscalateManual,
		retryable:   false,
		maxAttempts: 1,
		reason:      "provider quota exhausted; no automatic retry until the account is topped up",
	},
	ClassReviewerMissing: {
		action:      ActionReassignEmployee,
		retryable:   false,
		maxAttempts: 1,
		reason:      "review task has no configured or claimable reviewer; reassign or escalate",
	},
	ClassDirtyWorktree: {
		action:      ActionRepairWorktree,
		retryable:   true,
		maxAttempts: 2,
		backoff:     []time.Duration{0, time.Minute},
		reason:      "candidate worktree has uncommitted changes; repair then retry same employee",
	},
	ClassMissingReceipt: {
		action:      ActionRecreateReceipt,
		retryable:   true,
		maxAttempts: 2,
		backoff:     []time.Duration{0, 30 * time.Second},
		reason:      "terminal task has no execution receipt; re-claim/re-finalize then retry",
	},
	ClassCrash: {
		action:      ActionRetrySameEmployee,
		retryable:   true,
		maxAttempts: 3,
		backoff:     []time.Duration{0, time.Minute, 5 * time.Minute},
		reason:      "agent/runner crashed; retry with a fresh session",
	},
	ClassUnknown: {
		action:      ActionEscalateManual,
		retryable:   false,
		maxAttempts: 1,
		reason:      "unclassified failure; escalate to a human owner",
	},
}

// defaultCircuitThreshold is the consecutive-failure count that opens the
// circuit when the caller does not supply one.
const defaultCircuitThreshold = 3

// PlanFor produces the recovery plan for a classified failure. It is pure and
// deterministic: identical (Signals, Options) always yield an identical Plan,
// which is the idempotency contract. The planner never writes; it returns a
// recommendation plus a fingerprint a caller can use to dedupe application.
func PlanFor(s Signals, o Options) Plan {
	cls := Classify(s)
	pol := classPolicies[cls]

	// Circuit breaker: an already-open circuit suspends automatic retry and
	// escalates, regardless of the class policy.
	circuit := o.Circuit
	if circuit.Threshold <= 0 {
		circuit.Threshold = defaultCircuitThreshold
	}
	if circuit.State == CircuitOpen {
		return Plan{
			Class:       cls,
			Action:      ActionEscalateManual,
			Retryable:   false,
			MaxAttempts: 1,
			Circuit:     circuit,
			Reason:      "circuit open: " + pol.reason,
		}
	}

	// Effective attempt budget: the caller's configured max_attempts wins when
	// set, else the class default.
	maxAttempts := pol.maxAttempts
	if o.MaxAttempts > 0 {
		maxAttempts = o.MaxAttempts
	}

	// No blind retry: a retry is only recommended when the class is retryable,
	// the attempt budget is not exhausted, and the circuit is not open.
	attemptsLeft := maxAttempts - o.Attempt
	retryable := pol.retryable && attemptsLeft > 0

	// A retryable class whose budget is spent degrades to manual escalation;
	// a class whose action is already non-retryable (reassign_employee,
	// escalate_manual) keeps that action.
	action := pol.action
	if !retryable && isRetryAction(action) {
		action = ActionEscalateManual
	}

	lineage := Lineage{
		ParentTaskID:  s.TaskID,
		RetryOfTaskID: s.TaskID,
		// A crash poisons the agent session; the retry must start fresh rather
		// than resume a transcript that just crashed.
		FreshSession: cls == ClassCrash,
	}

	// Alternate-employee recommendation (read-only): only for classes where a
	// different employee can actually help, and only when the budget is spent
	// or the class is inherently non-retryable on the same employee.
	var alt *EmployeeRef
	if !retryable && (cls == ClassReviewerMissing || cls == ClassQuotaExhausted || cls == ClassCrash) {
		alt = pickAlternate(o.HealthyEmployees, o.FailedEmployeeID)
	}

	return Plan{
		Class:             cls,
		Action:            action,
		Retryable:         retryable,
		MaxAttempts:       maxAttempts,
		NextBackoff:       backoffFor(pol.backoff, o.Attempt),
		Lineage:           lineage,
		Circuit:           circuit,
		AlternateEmployee: alt,
		Reason:            pol.reason,
	}
}

// isRetryAction reports whether an action is a same-employee retry/repair
// that degrades to manual escalation when the attempt budget is exhausted.
// Non-retryable actions (reassign_employee, escalate_manual, noop) are kept
// as-is.
func isRetryAction(a Action) bool {
	switch a {
	case ActionRetrySameEmployee, ActionRepairWorktree, ActionWaitForRuntime, ActionRecreateReceipt:
		return true
	}
	return false
}

// backoffFor returns the delay before the next attempt after the failure at
// the given 1-based attempt. Attempts past the schedule reuse the last delay.
func backoffFor(schedule []time.Duration, attempt int) time.Duration {
	if len(schedule) == 0 {
		return 0
	}
	idx := attempt - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(schedule) {
		idx = len(schedule) - 1
	}
	return schedule[idx]
}

// pickAlternate returns the first healthy employee that is not the failed one.
// It is a pure recommendation: the planner never writes the reassignment.
func pickAlternate(healthy []EmployeeRef, failedEmployeeID string) *EmployeeRef {
	for i := range healthy {
		if healthy[i].EmployeeID != failedEmployeeID {
			alt := healthy[i]
			return &alt
		}
	}
	return nil
}

// Fingerprint returns a stable sha256 hex digest of the plan's actionable
// content. A caller can persist it and skip re-applying a plan it has already
// applied (idempotency guard). Two plans with the same fingerprint are
// interchangeable; the same (Signals, Options) always produce the same
// fingerprint.
func (p Plan) Fingerprint() string {
	// Canonical JSON: struct field order is fixed, so the digest is stable
	// across runs and processes.
	b, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
