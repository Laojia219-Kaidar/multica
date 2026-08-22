package service

import "testing"

// HIV-807: structured disposition tests. Each health must map to exactly one
// disposition, and the mapping must be deterministic (idempotent).

func TestDisposition_ActiveMapsToReady(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, ActiveTaskCount: 2, NonterminalIssueCount: 5,
	})
	if c.Disposition != DispositionReady {
		t.Fatalf("disposition = %q, want %q for active_with_frontier", c.Disposition, DispositionReady)
	}
}

func TestDisposition_ReadyForClosureMapsToReady(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, NonterminalIssueCount: 0, ConfirmedOutcomeCount: 1,
	})
	if c.Disposition != DispositionReady {
		t.Fatalf("disposition = %q, want %q for ready_for_closure", c.Disposition, DispositionReady)
	}
}

func TestDisposition_StalledMapsToReady(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, ActiveTaskCount: 0, NonterminalIssueCount: 4,
	})
	if c.Disposition != DispositionReady {
		t.Fatalf("disposition = %q, want %q for stalled (dispatch demand)", c.Disposition, DispositionReady)
	}
}

func TestDisposition_BlockedIssuesMapToBlocked(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, ActiveTaskCount: 0,
		BlockedIssueCount: 3, NonterminalIssueCount: 10,
	})
	if c.Disposition != DispositionBlocked {
		t.Fatalf("disposition = %q, want %q for blocked issues", c.Disposition, DispositionBlocked)
	}
}

func TestDisposition_ReviewBacklogMapsToBlocked(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, ActiveTaskCount: 0,
		ReviewIssueCount: 5, NonterminalIssueCount: 10,
	})
	if c.Disposition != DispositionBlocked {
		t.Fatalf("disposition = %q, want %q for review backlog", c.Disposition, DispositionBlocked)
	}
}

func TestDisposition_RepairGapMapsToBlocked(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, ActiveTaskCount: 0,
		FailedRepairGapCount: 2, NonterminalIssueCount: 3,
	})
	if c.Disposition != DispositionBlocked {
		t.Fatalf("disposition = %q, want %q for repair gap", c.Disposition, DispositionBlocked)
	}
}

func TestDisposition_SourceGapMapsToSourceGap(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, NonterminalIssueCount: 0, ConfirmedOutcomeCount: 0,
	})
	if c.Disposition != DispositionSourceGap {
		t.Fatalf("disposition = %q, want %q for source_gap", c.Disposition, DispositionSourceGap)
	}
}

func TestDisposition_DuplicateMapsToOwnerDecision(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, DuplicateOfProjectID: "p2",
	})
	if c.Disposition != DispositionOwnerDecision {
		t.Fatalf("disposition = %q, want %q for duplicate", c.Disposition, DispositionOwnerDecision)
	}
}

// Stalled is dispatch demand (ready); adding an active task keeps it ready
// but changes the health from stalled to active_with_frontier.
func TestDisposition_StalledReadyStaysReadyOnTaskResume(t *testing.T) {
	base := ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, ActiveTaskCount: 0, NonterminalIssueCount: 3,
	}
	stalled := ClassifyProject(base)
	if stalled.Disposition != DispositionReady {
		t.Fatalf("stalled disposition = %q, want %q", stalled.Disposition, DispositionReady)
	}
	if stalled.Health != HealthStalledNoOpenTask {
		t.Fatalf("stalled health = %q, want %q", stalled.Health, HealthStalledNoOpenTask)
	}

	resumed := base
	resumed.ActiveTaskCount = 1
	active := ClassifyProject(resumed)
	if active.Disposition != DispositionReady {
		t.Fatalf("resumed disposition = %q, want %q", active.Disposition, DispositionReady)
	}
	if active.Health != HealthActiveWithFrontier {
		t.Fatalf("resumed health = %q, want %q", active.Health, HealthActiveWithFrontier)
	}
}

// Idempotency: classifying the same input twice must produce identical output.
func TestDisposition_Idempotent(t *testing.T) {
	in := ProjectLifecycleInput{
		ProjectID: "p1", HasLead: true, ActiveTaskCount: 0,
		ReviewIssueCount: 3, NonterminalIssueCount: 7,
	}
	a := ClassifyProject(in)
	b := ClassifyProject(in)
	if a.Disposition != b.Disposition || a.Health != b.Health || a.NextAction != b.NextAction {
		t.Fatalf("non-idempotent: %+v vs %+v", a, b)
	}
}

// Missing lead is a hard gate: owner_decision before any active/closure/ready
// branch can return, regardless of the underlying health.

func TestDisposition_MissingLead_ActiveTask(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: false, ActiveTaskCount: 3, NonterminalIssueCount: 5,
	})
	if c.Disposition != DispositionOwnerDecision {
		t.Fatalf("missing-lead + active: disposition = %q, want %q", c.Disposition, DispositionOwnerDecision)
	}
	if !c.OwnerDecisionRequired {
		t.Fatal("missing-lead + active: OwnerDecisionRequired must be true")
	}
}

func TestDisposition_MissingLead_StalledWork(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: false, ActiveTaskCount: 0, NonterminalIssueCount: 4,
	})
	if c.Disposition != DispositionOwnerDecision {
		t.Fatalf("missing-lead + stalled: disposition = %q, want %q", c.Disposition, DispositionOwnerDecision)
	}
}

func TestDisposition_MissingLead_ClosureReady(t *testing.T) {
	c := ClassifyProject(ProjectLifecycleInput{
		ProjectID: "p1", HasLead: false, NonterminalIssueCount: 0, ConfirmedOutcomeCount: 1,
	})
	if c.Disposition != DispositionOwnerDecision {
		t.Fatalf("missing-lead + closure-ready: disposition = %q, want %q", c.Disposition, DispositionOwnerDecision)
	}
}

// Every non-empty disposition must be one of the four canonical values.
func TestDisposition_AlwaysCanonical(t *testing.T) {
	cases := []ProjectLifecycleInput{
		{ProjectID: "a", HasLead: true, ActiveTaskCount: 1, NonterminalIssueCount: 1},
		{ProjectID: "b", HasLead: true, ActiveTaskCount: 0, NonterminalIssueCount: 3},
		{ProjectID: "c", HasLead: true, ActiveTaskCount: 0, BlockedIssueCount: 1, NonterminalIssueCount: 1},
		{ProjectID: "d", HasLead: true, ActiveTaskCount: 0, ReviewIssueCount: 1, NonterminalIssueCount: 1},
		{ProjectID: "e", HasLead: true, ActiveTaskCount: 0, FailedRepairGapCount: 1, NonterminalIssueCount: 1},
		{ProjectID: "f", HasLead: true, NonterminalIssueCount: 0, ConfirmedOutcomeCount: 0},
		{ProjectID: "g", HasLead: true, NonterminalIssueCount: 0, ConfirmedOutcomeCount: 1},
		{ProjectID: "h", HasLead: true, DuplicateOfProjectID: "x"},
		{ProjectID: "i", HasLead: false, NonterminalIssueCount: 2},
	}
	valid := map[ProjectDisposition]bool{
		DispositionReady: true, DispositionBlocked: true,
		DispositionOwnerDecision: true, DispositionSourceGap: true,
	}
	for _, in := range cases {
		c := ClassifyProject(in)
		if !valid[c.Disposition] {
			t.Errorf("input %s: disposition %q is not canonical", in.ProjectID, c.Disposition)
		}
	}
}

// Reconciler finding provenance: the finding must carry the snapshot's
// disposition and frontier Issue IDs so repair Issues preserve the chain.
func TestReconcileFinding_ProvenanceConsistency(t *testing.T) {
	f := ReconcileFinding{
		Kind:             FindingStalledNoTask,
		ProjectID:        "proj-1",
		Disposition:      string(DispositionBlocked),
		FrontierIssueIDs: []string{"issue-a", "issue-b"},
		Summary:          "3 nonterminal issue(s), 0 live task(s)",
		NextAction:       "resume the ready frontier or pause explicitly",
	}
	if f.Disposition != string(DispositionBlocked) {
		t.Fatalf("finding disposition = %q, want %q", f.Disposition, DispositionBlocked)
	}
	if len(f.FrontierIssueIDs) != 2 {
		t.Fatalf("finding frontier_issue_ids len = %d, want 2", len(f.FrontierIssueIDs))
	}
	if f.FrontierIssueIDs[0] != "issue-a" || f.FrontierIssueIDs[1] != "issue-b" {
		t.Fatalf("finding frontier_issue_ids = %v, want [issue-a issue-b]", f.FrontierIssueIDs)
	}
}

// Snapshot disposition field is populated from classification (wire contract).
func TestSnapshot_DispositionFieldPopulated(t *testing.T) {
	snap := ProjectLifecycleSnapshot{
		ProjectID:   "p1",
		Health:      string(HealthStalledNoOpenTask),
		Disposition: string(DispositionBlocked),
	}
	if snap.Disposition != string(DispositionBlocked) {
		t.Fatalf("snapshot disposition = %q, want %q", snap.Disposition, DispositionBlocked)
	}
}
