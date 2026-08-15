package readyfrontier

import "testing"

// ready_frontier_test.go — pure unit tests for the ready-frontier classifier.
//
// These tests exercise the classification contract without a database:
//   - every state (ready/running/waiting/blocked/superseded) is reachable and
//     carries the expected stable reason;
//   - evaluation precedence (superseded review_state > terminal status > active
//     task > blocked status > backlog > gates) is respected;
//   - fail-closed: an unrecognized issue/task status is blocked/missing_evidence,
//     never optimistically ready.

func TestClassifyIssue_Ready(t *testing.T) {
	got := ClassifyIssue(IssueInput{
		Status:        "todo",
		HasAssignee:   true,
		RuntimeBound:  true,
		RuntimeOnline: true,
		CapacityKnown: true,
		CapacityFree:  true,
	})
	if got.State != StateReady {
		t.Fatalf("todo with healthy agent + free capacity: expected ready, got %q (%v)", got.State, got.Reasons)
	}
	if len(got.Reasons) != 0 {
		t.Fatalf("ready must carry no reasons, got %v", got.Reasons)
	}
}

func TestClassifyIssue_Running(t *testing.T) {
	got := ClassifyIssue(IssueInput{
		Status:     "in_progress",
		HasTask:    true,
		TaskStatus: "running",
	})
	if got.State != StateRunning || !hasReason(got, ReasonRunning) {
		t.Fatalf("in_progress + running task: expected running/running, got %q (%v)", got.State, got.Reasons)
	}

	// dispatched is also an active attempt.
	got = ClassifyIssue(IssueInput{Status: "in_progress", HasTask: true, TaskStatus: "dispatched"})
	if got.State != StateRunning {
		t.Fatalf("dispatched task: expected running, got %q", got.State)
	}
}

func TestClassifyIssue_Waiting(t *testing.T) {
	cases := []struct {
		name   string
		in     IssueInput
		state  State
		reason Reason
	}{
		{"queued task", IssueInput{Status: "in_progress", HasTask: true, TaskStatus: "queued"}, StateWaiting, ReasonQueued},
		{"deferred task", IssueInput{Status: "in_progress", HasTask: true, TaskStatus: "deferred"}, StateWaiting, ReasonDeferred},
		{"waiting_local_directory", IssueInput{Status: "in_progress", HasTask: true, TaskStatus: "waiting_local_directory"}, StateWaiting, ReasonWaitingLocalDir},
		{"backlog", IssueInput{Status: "backlog"}, StateWaiting, ReasonBacklog},
		{"prerequisite unmet", IssueInput{Status: "todo", PrerequisiteUnmet: true, HasAssignee: true, RuntimeBound: true, RuntimeOnline: true}, StateWaiting, ReasonPrerequisiteUnmet},
		{"unassigned", IssueInput{Status: "todo"}, StateWaiting, ReasonUnassigned},
		{"capacity full", IssueInput{Status: "todo", HasAssignee: true, RuntimeBound: true, RuntimeOnline: true, CapacityKnown: true, CapacityFree: false}, StateWaiting, ReasonCapacity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyIssue(tc.in)
			if got.State != tc.state || !hasReason(got, tc.reason) {
				t.Fatalf("expected %s/%s, got %q (%v)", tc.state, tc.reason, got.State, got.Reasons)
			}
		})
	}
}

func TestClassifyIssue_Blocked(t *testing.T) {
	cases := []struct {
		name   string
		in     IssueInput
		reason Reason
	}{
		{"failed task", IssueInput{Status: "in_progress", HasTask: true, TaskStatus: "failed"}, ReasonFailed},
		{"blocked status", IssueInput{Status: "blocked"}, ReasonBlockedStatus},
		{"prerequisite blocked", IssueInput{Status: "todo", PrerequisiteBlocked: true, HasAssignee: true, RuntimeBound: true, RuntimeOnline: true}, ReasonPrerequisiteBlocked},
		{"agent archived", IssueInput{Status: "todo", HasAssignee: true, AgentArchived: true}, ReasonAgentArchived},
		{"runtime unbound", IssueInput{Status: "todo", HasAssignee: true}, ReasonRuntimeUnavailable},
		{"runtime offline", IssueInput{Status: "todo", HasAssignee: true, RuntimeBound: true, RuntimeOnline: false}, ReasonRuntimeUnavailable},
		{"lease expired", IssueInput{Status: "in_progress", HasTask: true, TaskStatus: "dispatched", PrepareLeaseExpired: true}, ReasonLeaseExpired},
		{"unknown issue status", IssueInput{Status: "mystery"}, ReasonMissingEvidence},
		{"unknown task status", IssueInput{Status: "in_progress", HasTask: true, TaskStatus: "mystery"}, ReasonMissingEvidence},
		{"capacity unknown (query failed)", IssueInput{Status: "todo", HasAssignee: true, RuntimeBound: true, RuntimeOnline: true, CapacityKnown: false}, ReasonMissingEvidence},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyIssue(tc.in)
			if got.State != StateBlocked || !hasReason(got, tc.reason) {
				t.Fatalf("expected blocked/%s, got %q (%v)", tc.reason, got.State, got.Reasons)
			}
		})
	}
}

// TestClassifyIssue_CapacityUnknownFailClosed is the HIV-459 advisory regression
// test: when the caller could not determine capacity (CapacityKnown=false, e.g.
// the CountRunningTasks query failed), the classifier must NOT bypass the
// capacity gate and return ready. It must fail closed to
// blocked/missing_evidence. This test fails on the pre-fix candidate (which
// treated CapacityKnown=false as "skip gate → ready") and passes after the fix.
func TestClassifyIssue_CapacityUnknownFailClosed(t *testing.T) {
	got := ClassifyIssue(IssueInput{
		Status:        "todo",
		HasAssignee:   true,
		RuntimeBound:  true,
		RuntimeOnline: true,
		CapacityKnown: false, // capacity query failed — capacity indeterminate
		CapacityFree:  false,
	})
	if got.State != StateBlocked || !hasReason(got, ReasonMissingEvidence) {
		t.Fatalf("capacity unknown: expected blocked/missing_evidence (fail-closed), got %q (%v)", got.State, got.Reasons)
	}
}

func TestClassifyIssue_Superseded(t *testing.T) {
	cases := []struct {
		name   string
		in     IssueInput
		reason Reason
	}{
		{"review_state superseded", IssueInput{Status: "in_progress", ReviewState: "superseded", HasTask: true, TaskStatus: "running"}, ReasonSuperseded},
		{"review_state archived_history", IssueInput{Status: "blocked", ReviewState: "archived_history"}, ReasonArchivedHistory},
		{"done", IssueInput{Status: "done"}, ReasonTerminal},
		{"cancelled", IssueInput{Status: "cancelled"}, ReasonTerminal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyIssue(tc.in)
			if got.State != StateSuperseded || !hasReason(got, tc.reason) {
				t.Fatalf("expected superseded/%s, got %q (%v)", tc.reason, got.State, got.Reasons)
			}
		})
	}
}

// TestClassifyIssue_SupersededReviewStateWins verifies precedence: a superseded
// review_state must win even when an active task exists — the historical
// terminal state outranks the live execution signal.
func TestClassifyIssue_SupersededReviewStateWins(t *testing.T) {
	got := ClassifyIssue(IssueInput{
		Status:      "in_progress",
		ReviewState: "superseded",
		HasTask:     true,
		TaskStatus:  "running",
	})
	if got.State != StateSuperseded {
		t.Fatalf("superseded review_state must win over a running task, got %q", got.State)
	}
}

func TestClassifyTask(t *testing.T) {
	cases := []struct {
		name   string
		in     TaskInput
		state  State
		reason Reason
	}{
		{"running", TaskInput{Status: "running"}, StateRunning, ReasonRunning},
		{"dispatched", TaskInput{Status: "dispatched"}, StateRunning, ReasonRunning},
		{"queued", TaskInput{Status: "queued"}, StateWaiting, ReasonQueued},
		{"deferred", TaskInput{Status: "deferred"}, StateWaiting, ReasonDeferred},
		{"waiting_local_directory", TaskInput{Status: "waiting_local_directory"}, StateWaiting, ReasonWaitingLocalDir},
		{"failed", TaskInput{Status: "failed"}, StateBlocked, ReasonFailed},
		{"completed", TaskInput{Status: "completed"}, StateSuperseded, ReasonTerminal},
		{"cancelled", TaskInput{Status: "cancelled"}, StateSuperseded, ReasonTerminal},
		{"superseded by successor", TaskInput{Status: "completed", SupersededByNewer: true}, StateSuperseded, ReasonSupersededBySuccessor},
		{"lease expired", TaskInput{Status: "dispatched", PrepareLeaseExpired: true}, StateBlocked, ReasonLeaseExpired},
		{"unknown status", TaskInput{Status: "mystery"}, StateBlocked, ReasonMissingEvidence},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyTask(tc.in)
			if got.State != tc.state || !hasReason(got, tc.reason) {
				t.Fatalf("expected %s/%s, got %q (%v)", tc.state, tc.reason, got.State, got.Reasons)
			}
		})
	}
}

func hasReason(c Classification, want Reason) bool {
	for _, r := range c.Reasons {
		if r == want {
			return true
		}
	}
	return false
}
