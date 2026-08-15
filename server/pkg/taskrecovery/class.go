// Package taskrecovery is a candidate-only failure classifier and recovery
// planner for HiveCrew task runs. It turns a failed task's read-only evidence
// (failure_reason, error text, task/runtime/receipt/review state) into a
// lineage-preserving retry/repair recommendation.
//
// Scope and authority: this package is PURE. It never writes to the database,
// never creates a task, never mutates an execution_receipt, and never touches
// the org directory. It only classifies and plans. A caller (FailTask, a
// sweeper, or a future recovery controller) decides whether and how to apply
// the plan. This is the "no second Task truth" contract: the planner emits a
// recommendation and a fingerprint, never a second write path.
//
// The seven failure classes are the ones the Owner control plane needs to
// reason about:
//
//	runtime_outdated, waiting_local_directory, quota_exhausted,
//	reviewer_missing, dirty_worktree, missing_receipt, crash
//
// plus an `unknown` catchall. Each class maps to a recovery action with a
// bounded attempt budget, a backoff schedule, an optional alternate-employee
// recommendation (read-only), and a circuit-breaker policy.
package taskrecovery

import (
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// Class is a high-level failure class the recovery planner reasons about. It
// is deliberately coarser than taskfailure.Reason: several taskfailure reasons
// collapse into one class (e.g. provider_quota_limit and insufficient_balance
// both mean quota_exhausted), and some classes are structural
// (waiting_local_directory, missing_receipt) rather than error-text-derived.
type Class string

const (
	// ClassRuntimeOutdated: the owning runtime is below the minimum supported
	// version and cannot run the task.
	ClassRuntimeOutdated Class = "runtime_outdated"
	// ClassWaitingLocalDirectory: the task is parked waiting for a local
	// directory lock (wait_reason set).
	ClassWaitingLocalDirectory Class = "waiting_local_directory"
	// ClassQuotaExhausted: the provider account is out of quota/credits.
	ClassQuotaExhausted Class = "quota_exhausted"
	// ClassReviewerMissing: a review task has no configured or claimable
	// reviewer.
	ClassReviewerMissing Class = "reviewer_missing"
	// ClassDirtyWorktree: the candidate worktree has uncommitted changes that
	// would be lost or mixed into a retry.
	ClassDirtyWorktree Class = "dirty_worktree"
	// ClassMissingReceipt: the task reached a terminal state but no
	// execution_receipt exists for it.
	ClassMissingReceipt Class = "missing_receipt"
	// ClassCrash: the agent/runner process crashed or the runtime recovered
	// mid-flight.
	ClassCrash Class = "crash"
	// ClassUnknown: no rule matched.
	ClassUnknown Class = "unknown"
)

// AllClasses returns the seven classes plus unknown in a stable order. Callers
// MUST NOT mutate the returned slice.
func AllClasses() []Class {
	return []Class{
		ClassRuntimeOutdated,
		ClassWaitingLocalDirectory,
		ClassQuotaExhausted,
		ClassReviewerMissing,
		ClassDirtyWorktree,
		ClassMissingReceipt,
		ClassCrash,
		ClassUnknown,
	}
}

// Signals is the read-only evidence the classifier consumes. It is a pure
// value type: the classifier never mutates it and never writes anything.
// Callers assemble it from the task row, the runtime row, the execution
// receipt, and the review configuration.
type Signals struct {
	// TaskID is the failed task the recovery acts on (used for lineage).
	TaskID string
	// FailureReason is the canonical taskfailure.Reason wire value already
	// persisted on the task (may be empty when the task failed without a
	// classified reason).
	FailureReason string
	// ErrorText is the free-form error text from the failed run.
	ErrorText string
	// TaskStatus is the agent_task_queue.status at classification time.
	TaskStatus string
	// TaskKind is the agent_task_queue.task_kind ("work" | "review" | "repair").
	TaskKind string
	// WaitReason is the wait_reason column for waiting_local_directory tasks.
	WaitReason string
	// RuntimeStatus is the agent_runtime.status of the owning runtime.
	RuntimeStatus string
	// RuntimeVersion is the version the runtime reported.
	RuntimeVersion string
	// MinRuntimeVersion is the minimum version the platform supports.
	MinRuntimeVersion string
	// ReceiptPresent reports whether an execution_receipt exists for the task.
	ReceiptPresent bool
	// ReviewerConfigured reports whether an L1 reviewer agent is configured.
	ReviewerConfigured bool
	// ReviewerClaimable reports whether the reviewer has a claimable runtime.
	ReviewerClaimable bool
	// WorktreeDirty reports whether the candidate worktree has uncommitted
	// changes.
	WorktreeDirty bool
}

// httpQuotaCodeRe matches a bare 402 status code with a digit boundary so
// "402913 tokens" or "1402ms" don't spuriously classify as quota exhaustion.
// Mirrors the digit-boundary guard in taskfailure/classify.go.
var httpQuotaCodeRe = regexp.MustCompile(`(^|[^0-9])402([^0-9]|$)`)

// Classify maps read-only task evidence to a failure Class. It is a pure,
// deterministic function: the same Signals always yields the same Class, which
// is the idempotency guarantee the recovery planner relies on.
//
// Precedence is most-actionable-first: unambiguous structural state
// observations (waiting_local_directory, reviewer_missing, dirty_worktree)
// win over error-text-derived reasons (runtime_outdated, quota_exhausted,
// crash), which win over the terminal-state receipt check (missing_receipt),
// which wins over the catchall.
func Classify(s Signals) Class {
	// 1. Structural: task parked on a local-directory lock.
	if s.TaskStatus == "waiting_local_directory" {
		return ClassWaitingLocalDirectory
	}
	// 2. Structural: a review task with no configured or claimable reviewer.
	if s.TaskKind == "review" && (!s.ReviewerConfigured || !s.ReviewerClaimable) {
		return ClassReviewerMissing
	}
	// 3. Structural: dirty candidate worktree.
	if s.WorktreeDirty {
		return ClassDirtyWorktree
	}
	// 4. Runtime below the minimum supported version.
	if s.FailureReason == string(taskfailure.ReasonAgentRuntimeVersionUnsupported) ||
		versionBelow(s.RuntimeVersion, s.MinRuntimeVersion) ||
		containsAnyFold(s.ErrorText, "below the minimum supported version", "requires a newer version") {
		return ClassRuntimeOutdated
	}
	// 5. Provider quota exhausted.
	if s.FailureReason == string(taskfailure.ReasonAgentProviderQuotaLimit) ||
		httpQuotaCodeRe.MatchString(strings.ToLower(s.ErrorText)) ||
		containsAnyFold(s.ErrorText, "insufficient_balance", "balance is too low", "monthly usage limit", "usage limit", "credits", "quota") {
		return ClassQuotaExhausted
	}
	// 6. Agent/runner crash or runtime recovery mid-flight.
	if s.FailureReason == string(taskfailure.ReasonAgentProcessFailure) ||
		s.FailureReason == string(taskfailure.ReasonRuntimeRecovery) ||
		containsAnyFold(s.ErrorText, "exit status", "signal", "panic", "sigsegv", "process exited", "runtime recovery") {
		return ClassCrash
	}
	// 7. Terminal task with no execution receipt.
	if !s.ReceiptPresent && isTerminal(s.TaskStatus) {
		return ClassMissingReceipt
	}
	return ClassUnknown
}

// isTerminal reports whether a task status is a terminal state that should
// carry an execution receipt.
func isTerminal(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}

// versionBelow reports whether a < b for dotted numeric versions like
// "0.5.0" or "1.2.3-beta". Non-numeric segments stop the comparison. An empty
// a is never treated as below an empty b (no minimum required).
func versionBelow(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	as, bs := parseVersion(a), parseVersion(b)
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] != bs[i] {
			return as[i] < bs[i]
		}
	}
	return len(as) < len(bs)
}

// parseVersion splits a dotted version into numeric segments, stopping at the
// first non-numeric segment (e.g. "1.2.3-beta" -> [1 2 3]).
func parseVersion(v string) []int {
	var out []int
	for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' }) {
		n := 0
		for _, c := range part {
			if c < '0' || c > '9' {
				return out
			}
			n = n*10 + int(c-'0')
		}
		out = append(out, n)
	}
	return out
}

// containsAnyFold reports whether s (case-insensitively) contains any of the
// supplied substrings.
func containsAnyFold(s string, subs ...string) bool {
	lower := strings.ToLower(s)
	for _, sub := range subs {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}
