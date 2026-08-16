package service

import "testing"

// These are the contract-first red tests for the Slice 1 project-health
// projection, mapped from the HIV-553 negative-test table. They exercise only
// the pure ClassifyProject function so they run without a database.

func TestClassifyProject_ActiveWithFrontier(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, ActiveTaskCount: 1, NonterminalIssueCount: 5,
	})
	if c.Health != HealthActiveWithFrontier {
		t.Fatalf("health = %q, want %q", c.Health, HealthActiveWithFrontier)
	}
	if c.OwnerDecisionRequired {
		t.Fatalf("owner_decision_required = true, want false for an active project with a lead")
	}
}

func TestClassifyProject_CompletedWithOpenWorkIsInconsistent(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", ProjectStatus: "completed", HasLead: true,
		NonterminalIssueCount: 2,
	})
	if c.TerminalProjectionFinding != TerminalProjectionCompletedWithOpenWork {
		t.Fatalf("finding = %q, want completed_with_nonterminal_or_active", c.TerminalProjectionFinding)
	}
	if !containsStr(c.Flags, "terminal_projection_inconsistent") {
		t.Fatalf("flags = %v, want terminal_projection_inconsistent", c.Flags)
	}
}

func TestClassifyProject_CancelledWithActiveTaskNeverReopens(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", ProjectStatus: "cancelled", HasLead: true,
		ActiveTaskCount: 1,
	})
	if c.TerminalProjectionFinding != TerminalProjectionCancelledWithActive {
		t.Fatalf("finding = %q, want cancelled_with_active", c.TerminalProjectionFinding)
	}
	if c.TerminalProjectionNextAction == "" {
		t.Fatal("cancelled finding must include a stop/disposition next action")
	}
}

func TestClassifyProject_CompletedWithoutOpenWorkIsConsistent(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", ProjectStatus: "completed", HasLead: true,
		ConfirmedOutcomeCount: 1,
	})
	if c.TerminalProjectionFinding != TerminalProjectionNone {
		t.Fatalf("finding = %q, want none", c.TerminalProjectionFinding)
	}
}

// Contract negative #1: in_progress with no nonterminal task but open issues
// must be stalled_no_open_task, never shown as "executing".
func TestClassifyProject_StalledNoOpenTask(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, ActiveTaskCount: 0, NonterminalIssueCount: 4,
	})
	if c.Health != HealthStalledNoOpenTask {
		t.Fatalf("health = %q, want %q", c.Health, HealthStalledNoOpenTask)
	}
	if containsStr(c.Flags, "active") {
		t.Fatalf("flags contain active, want stalled flags: %v", c.Flags)
	}
}

// Contract negative #4: an in_progress issue whose only task completed leaves
// frontier empty; the project must be stalled, not active.
func TestClassifyProject_FrontierEmptyWhenTaskCompleted(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, ActiveTaskCount: 0, NonterminalIssueCount: 1,
	})
	if c.Health != HealthStalledNoOpenTask {
		t.Fatalf("health = %q, want %q (in_progress issue + completed task != live work)", c.Health, HealthStalledNoOpenTask)
	}
}

// Contract negative #2: all issues terminal but outcome coverage missing must
// fail closed with source_gap, never ready_for_closure.
func TestClassifyProject_AllTerminalWithoutOutcomeIsSourceGap(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, NonterminalIssueCount: 0, ConfirmedOutcomeCount: 0,
	})
	if c.Health != HealthSourceGap {
		t.Fatalf("health = %q, want %q", c.Health, HealthSourceGap)
	}
	if !containsStr(c.ClosureBlockers, "OUTCOME_COVERAGE_INCOMPLETE") || !containsStr(c.ClosureBlockers, "CLOSURE_PACKAGE_MISSING") {
		t.Fatalf("closure blockers = %v, want OUTCOME_COVERAGE_INCOMPLETE + CLOSURE_PACKAGE_MISSING", c.ClosureBlockers)
	}
}

// Ready for closure requires all terminal + at least one confirmed outcome.
func TestClassifyProject_ReadyForClosure(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, NonterminalIssueCount: 0, ConfirmedOutcomeCount: 1,
	})
	if c.Health != HealthReadyForClosure {
		t.Fatalf("health = %q, want %q", c.Health, HealthReadyForClosure)
	}
	if c.OwnerDecisionRequired {
		t.Fatalf("owner_decision_required = true, want false")
	}
}

// Review backlog with no live task is review_or_repair_blocked (C), not a
// plain stalled.
func TestClassifyProject_ReviewBacklogBlocks(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, ActiveTaskCount: 0, ReviewIssueCount: 17, NonterminalIssueCount: 25,
	})
	if c.Health != HealthReviewOrRepairBlocked {
		t.Fatalf("health = %q, want %q", c.Health, HealthReviewOrRepairBlocked)
	}
}

// Blocked issues are review_or_repair_blocked (C).
func TestClassifyProject_BlockedIssuesBlock(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, ActiveTaskCount: 0, BlockedIssueCount: 5, NonterminalIssueCount: 166,
	})
	if c.Health != HealthReviewOrRepairBlocked {
		t.Fatalf("health = %q, want %q", c.Health, HealthReviewOrRepairBlocked)
	}
}

// Contract negative #9: a missing lead forces owner_decision_required with the
// ACCOUNTABLE_LEAD_REQUIRED blocker on top of the underlying health.
func TestClassifyProject_MissingLeadForcesOwnerDecision(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: false, ActiveTaskCount: 0, NonterminalIssueCount: 3,
	})
	if !c.OwnerDecisionRequired {
		t.Fatalf("owner_decision_required = false, want true for missing lead")
	}
	if !containsStr(c.ClosureBlockers, "ACCOUNTABLE_LEAD_REQUIRED") {
		t.Fatalf("closure blockers = %v, want ACCOUNTABLE_LEAD_REQUIRED", c.ClosureBlockers)
	}
}

// Contract negative #8: two projects pointing at the same canonical authority
// must classify the seed duplicate as duplicate_or_superseded with an owner
// decision, never auto-continue/close/merge.
func TestClassifyProject_DuplicateOrSuperseded(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: false, DuplicateOfProjectID: "p2",
	})
	if c.Health != HealthDuplicateOrSuperseded {
		t.Fatalf("health = %q, want %q", c.Health, HealthDuplicateOrSuperseded)
	}
	if !c.OwnerDecisionRequired {
		t.Fatalf("owner_decision_required = false, want true for duplicate authority")
	}
	if !containsStr(c.ClosureBlockers, "DUPLICATE_AUTHORITY_OWNER_DECISION") {
		t.Fatalf("closure blockers = %v, want DUPLICATE_AUTHORITY_OWNER_DECISION", c.ClosureBlockers)
	}
}

// Active with a missing lead keeps the lead blocker attached but still shows
// the real live work.
func TestClassifyProject_ActiveButMissingLead(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: false, ActiveTaskCount: 1, NonterminalIssueCount: 5,
	})
	if c.Health != HealthActiveWithFrontier {
		t.Fatalf("health = %q, want %q", c.Health, HealthActiveWithFrontier)
	}
	if !c.OwnerDecisionRequired {
		t.Fatalf("owner_decision_required = false, want true")
	}
}

func containsStr(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// Gauss F3: a failed task whose issue is still open is a repair gate (C), not
// a plain stalled (B). The contract's C definition includes "failed repair /
// re-review has not yet formed a live task".
func TestClassifyProject_FailedRepairGapBlocks(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, ActiveTaskCount: 0,
		FailedRepairGapCount: 1, NonterminalIssueCount: 1,
	})
	if c.Health != HealthReviewOrRepairBlocked {
		t.Fatalf("health = %q, want %q (failed repair gap)", c.Health, HealthReviewOrRepairBlocked)
	}
	if !containsStr(c.ClosureBlockers, "FAILED_REPAIR_GAP") {
		t.Fatalf("closure blockers = %v, want FAILED_REPAIR_GAP", c.ClosureBlockers)
	}
}
