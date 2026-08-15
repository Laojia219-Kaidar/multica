package wavescheduler

import (
	"testing"
	"time"
)

// fixture helpers

func mkIssue(id, title, status, priority string) Issue {
	return Issue{
		ID:       id,
		Title:    title,
		Status:   status,
		Priority: priority,
	}
}

func mkDep(issueID, dependsOnID string) Dependency {
	return Dependency{
		IssueID:     issueID,
		DependsOnID: dependsOnID,
		Type:        "blocked_by",
	}
}

var fixedNow = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)

// --- Normal fixture: A -> B -> C linear chain, D independent ---

func TestSchedule_NormalLinearChain(t *testing.T) {
	issues := []Issue{
		mkIssue("A", "Setup DB", "todo", "high"),
		mkIssue("B", "Write API", "todo", "high"),
		mkIssue("C", "Write tests", "todo", "medium"),
		mkIssue("D", "Update docs", "todo", "low"),
	}
	deps := []Dependency{
		mkDep("B", "A"),
		mkDep("C", "B"),
	}

	result := Schedule(ScheduleInput{
		ProjectID:    "proj-1",
		Issues:       issues,
		Dependencies: deps,
		Now:          fixedNow,
	})

	if result.CycleDetected {
		t.Fatal("unexpected cycle")
	}
	if result.TotalIssues != 4 {
		t.Fatalf("total issues: got %d, want 4", result.TotalIssues)
	}
	if len(result.Waves) != 3 {
		t.Fatalf("waves: got %d, want 3", len(result.Waves))
	}

	// Wave 0: A (no deps) + D (no deps) — both ready.
	w0 := result.Waves[0]
	if len(w0.Nodes) != 2 {
		t.Fatalf("wave 0 nodes: got %d, want 2 (A + D)", len(w0.Nodes))
	}
	for _, n := range w0.Nodes {
		if !n.Ready {
			t.Errorf("wave 0 node %s should be ready", n.IssueID)
		}
		if n.IdempotencyKey == "" {
			t.Errorf("wave 0 node %s missing idempotency key", n.IssueID)
		}
	}

	// Wave 1: B (depends on A).
	w1 := result.Waves[1]
	if len(w1.Nodes) != 1 || w1.Nodes[0].IssueID != "B" {
		t.Fatalf("wave 1: expected [B], got %v", nodeIDs(w1.Nodes))
	}

	// Wave 2: C (depends on B).
	w2 := result.Waves[2]
	if len(w2.Nodes) != 1 || w2.Nodes[0].IssueID != "C" {
		t.Fatalf("wave 2: expected [C], got %v", nodeIDs(w2.Nodes))
	}

	// Critical path should be A -> B -> C (length 3).
	if len(result.CriticalPath) != 3 {
		t.Fatalf("critical path length: got %d, want 3", len(result.CriticalPath))
	}
	if result.CriticalPath[0] != "A" || result.CriticalPath[1] != "B" || result.CriticalPath[2] != "C" {
		t.Fatalf("critical path: got %v, want [A B C]", result.CriticalPath)
	}

	if result.ReadyNow != 2 {
		t.Fatalf("ready_now: got %d, want 2", result.ReadyNow)
	}
}

// --- Duplicate fixture: two issues with identical deps ---

func TestSchedule_DuplicateDependencies(t *testing.T) {
	issues := []Issue{
		mkIssue("X", "Foundation", "todo", "high"),
		mkIssue("Y1", "Variant A", "todo", "medium"),
		mkIssue("Y2", "Variant B", "todo", "medium"),
	}
	deps := []Dependency{
		mkDep("Y1", "X"),
		mkDep("Y2", "X"),
		// Duplicate edge — should be handled gracefully.
		{IssueID: "Y1", DependsOnID: "X", Type: "blocked_by"},
	}

	result := Schedule(ScheduleInput{
		ProjectID:    "proj-2",
		Issues:       issues,
		Dependencies: deps,
		Now:          fixedNow,
	})

	if result.CycleDetected {
		t.Fatal("unexpected cycle from duplicate deps")
	}
	if len(result.Waves) != 2 {
		t.Fatalf("waves: got %d, want 2", len(result.Waves))
	}

	// Wave 0: X only.
	if len(result.Waves[0].Nodes) != 1 || result.Waves[0].Nodes[0].IssueID != "X" {
		t.Fatalf("wave 0: expected [X], got %v", nodeIDs(result.Waves[0].Nodes))
	}

	// Wave 1: Y1 + Y2 (both depend on X, can run in parallel).
	w1Nodes := nodeIDs(result.Waves[1].Nodes)
	if len(w1Nodes) != 2 {
		t.Fatalf("wave 1: expected 2 nodes, got %d", len(w1Nodes))
	}

	// Idempotency keys must differ between Y1 and Y2.
	keys := make(map[string]bool)
	for _, n := range result.Waves[1].Nodes {
		if keys[n.IdempotencyKey] {
			t.Errorf("duplicate idempotency key: %s", n.IdempotencyKey)
		}
		keys[n.IdempotencyKey] = true
	}
}

// --- Blocked dependency fixture: a blocked issue blocks downstream ---

func TestSchedule_BlockedDependency(t *testing.T) {
	issues := []Issue{
		mkIssue("P", "Prerequisite", "blocked", "high"),
		mkIssue("Q", "Depends on P", "todo", "high"),
		mkIssue("R", "Independent", "todo", "medium"),
	}
	deps := []Dependency{
		mkDep("Q", "P"),
	}

	result := Schedule(ScheduleInput{
		ProjectID:    "proj-3",
		Issues:       issues,
		Dependencies: deps,
		Now:          fixedNow,
	})

	if result.CycleDetected {
		t.Fatal("unexpected cycle")
	}
	if len(result.Waves) < 2 {
		t.Fatalf("waves: got %d, want >= 2", len(result.Waves))
	}

	// Wave 0: P (blocked but open) + R (independent, ready).
	w0IDs := nodeIDs(result.Waves[0].Nodes)
	if len(w0IDs) != 2 {
		t.Fatalf("wave 0: expected 2 nodes, got %d: %v", len(w0IDs), w0IDs)
	}

	// Q should be in wave 1 and annotated with blocked_by P.
	var qNode *WaveNode
	for i := range result.Waves[1].Nodes {
		if result.Waves[1].Nodes[i].IssueID == "Q" {
			qNode = &result.Waves[1].Nodes[i]
			break
		}
	}
	if qNode == nil {
		t.Fatal("Q not found in wave 1")
	}
	if len(qNode.BlockedBy) != 1 || qNode.BlockedBy[0] != "P" {
		t.Fatalf("Q blocked_by: got %v, want [P]", qNode.BlockedBy)
	}
}

// --- Burst fixture: many independent issues, one wave ---

func TestSchedule_BurstAllIndependent(t *testing.T) {
	issues := make([]Issue, 20)
	for i := range issues {
		issues[i] = mkIssue(
			string(rune('A'+i)),
			"task-"+string(rune('a'+i)),
			"todo",
			"medium",
		)
	}

	result := Schedule(ScheduleInput{
		ProjectID:    "proj-4",
		Issues:       issues,
		Dependencies: nil,
		Now:          fixedNow,
	})

	if result.CycleDetected {
		t.Fatal("unexpected cycle")
	}
	if len(result.Waves) != 1 {
		t.Fatalf("waves: got %d, want 1 (all independent)", len(result.Waves))
	}
	if len(result.Waves[0].Nodes) != 20 {
		t.Fatalf("wave 0 nodes: got %d, want 20", len(result.Waves[0].Nodes))
	}
	if result.ReadyNow != 20 {
		t.Fatalf("ready_now: got %d, want 20", result.ReadyNow)
	}
}

// --- Cycle detection ---

func TestSchedule_CycleDetection(t *testing.T) {
	issues := []Issue{
		mkIssue("A", "Task A", "todo", "high"),
		mkIssue("B", "Task B", "todo", "high"),
		mkIssue("C", "Task C", "todo", "high"),
	}
	deps := []Dependency{
		mkDep("A", "C"), // A depends on C
		mkDep("B", "A"), // B depends on A
		mkDep("C", "B"), // C depends on B -> cycle: A->C->B->A
	}

	result := Schedule(ScheduleInput{
		ProjectID:    "proj-5",
		Issues:       issues,
		Dependencies: deps,
		Now:          fixedNow,
	})

	if !result.CycleDetected {
		t.Fatal("expected cycle detection")
	}
	// All nodes should still appear in some wave.
	total := 0
	for _, w := range result.Waves {
		total += len(w.Nodes)
	}
	if total != 3 {
		t.Fatalf("total nodes in waves: got %d, want 3", total)
	}
}

// --- Empty input ---

func TestSchedule_EmptyInput(t *testing.T) {
	result := Schedule(ScheduleInput{
		ProjectID: "proj-empty",
		Now:       fixedNow,
	})
	if len(result.Waves) != 0 {
		t.Fatalf("expected 0 waves, got %d", len(result.Waves))
	}
	if result.TotalIssues != 0 {
		t.Fatalf("total issues: got %d, want 0", result.TotalIssues)
	}
}

// --- Terminal issues are excluded ---

func TestSchedule_TerminalIssuesExcluded(t *testing.T) {
	issues := []Issue{
		mkIssue("done-1", "Already done", "done", "high"),
		mkIssue("open-1", "Still open", "todo", "medium"),
	}

	result := Schedule(ScheduleInput{
		ProjectID:    "proj-6",
		Issues:       issues,
		Dependencies: nil,
		Now:          fixedNow,
	})

	if len(result.Waves) != 1 {
		t.Fatalf("waves: got %d, want 1", len(result.Waves))
	}
	if len(result.Waves[0].Nodes) != 1 {
		t.Fatalf("wave 0: got %d nodes, want 1 (only open)", len(result.Waves[0].Nodes))
	}
	if result.Waves[0].Nodes[0].IssueID != "open-1" {
		t.Fatalf("expected open-1, got %s", result.Waves[0].Nodes[0].IssueID)
	}
}

// --- Finding 1 (HIV-462): UNKNOWN / missing dependency must fail closed ---

func TestSchedule_MissingDependency_FailClosed(t *testing.T) {
	issues := []Issue{
		mkIssue("A", "Local foundation", "todo", "high"),
		mkIssue("B", "Depends on foreign issue", "todo", "medium"),
		mkIssue("C", "Depends on B", "todo", "low"),
	}
	deps := []Dependency{
		{IssueID: "B", DependsOnID: "foreign-1", Type: "blocked_by"},
		{IssueID: "C", DependsOnID: "B", Type: "blocked_by"},
	}

	result := Schedule(ScheduleInput{
		ProjectID:    "proj-missing",
		Issues:       issues,
		Dependencies: deps,
		Now:          fixedNow,
	})

	if result.CycleDetected {
		t.Fatal("missing dependency must not be misreported as a cycle")
	}
	if result.ReadyNow != 1 {
		t.Fatalf("ready_now: got %d, want 1 (A only; B/C must fail closed)", result.ReadyNow)
	}
	if len(result.Waves) != 2 {
		t.Fatalf("waves: got %d, want 2 (ready wave + held wave)", len(result.Waves))
	}
	held := result.Waves[1]
	heldIDs := nodeIDs(held.Nodes)
	if len(heldIDs) != 2 || heldIDs[0] != "B" || heldIDs[1] != "C" {
		t.Fatalf("held wave nodes: got %v, want [B C]", heldIDs)
	}
	for i := range held.Nodes {
		if held.Nodes[i].Ready {
			t.Errorf("held node %s must not be ready", held.Nodes[i].IssueID)
		}
	}
	var b *WaveNode
	for i := range held.Nodes {
		if held.Nodes[i].IssueID == "B" {
			b = &held.Nodes[i]
		}
	}
	if b == nil {
		t.Fatal("B missing from held wave")
	}
	if len(b.MissingDependencies) != 1 || b.MissingDependencies[0] != "foreign-1" {
		t.Fatalf("B missing_dependencies: got %v, want [foreign-1]", b.MissingDependencies)
	}
}

func TestSchedule_MissingBlocker_BlocksDirection(t *testing.T) {
	issues := []Issue{
		mkIssue("Y", "Needs a foreign blocker", "todo", "medium"),
		mkIssue("Z", "Independent", "todo", "low"),
	}
	deps := []Dependency{
		// Foreign issue blocks Y: canonical direction means Y depends on it,
		// and the target is outside the issue set -> UNKNOWN, not ready.
		{IssueID: "foreign-blocker", DependsOnID: "Y", Type: "blocks"},
	}

	result := Schedule(ScheduleInput{
		ProjectID:    "proj-missing-blocker",
		Issues:       issues,
		Dependencies: deps,
		Now:          fixedNow,
	})

	if result.CycleDetected {
		t.Fatal("missing blocker must not be misreported as a cycle")
	}
	if result.ReadyNow != 1 {
		t.Fatalf("ready_now: got %d, want 1 (Z only; Y must fail closed)", result.ReadyNow)
	}
	if len(result.Waves) != 2 {
		t.Fatalf("waves: got %d, want 2", len(result.Waves))
	}
	held := nodeIDs(result.Waves[1].Nodes)
	if len(held) != 1 || held[0] != "Y" {
		t.Fatalf("held wave nodes: got %v, want [Y]", held)
	}
	n := result.Waves[1].Nodes[0]
	if n.Ready {
		t.Fatal("Y must not be ready")
	}
	if len(n.MissingDependencies) != 1 || n.MissingDependencies[0] != "foreign-blocker" {
		t.Fatalf("Y missing_dependencies: got %v, want [foreign-blocker]", n.MissingDependencies)
	}
}

// --- Finding 2 (HIV-462): Dependency.Type semantics, canonical hard edges ---

func TestSchedule_BlocksCanonicalDirection(t *testing.T) {
	issues := []Issue{
		mkIssue("X", "Foundation", "todo", "high"),
		mkIssue("Y", "Depends on X", "todo", "medium"),
	}
	deps := []Dependency{
		// X blocks Y -> canonical hard edge: Y depends on X.
		{IssueID: "X", DependsOnID: "Y", Type: "blocks"},
	}

	result := Schedule(ScheduleInput{
		ProjectID:    "proj-blocks",
		Issues:       issues,
		Dependencies: deps,
		Now:          fixedNow,
	})

	if result.CycleDetected {
		t.Fatal("unexpected cycle")
	}
	if len(result.Waves) != 2 {
		t.Fatalf("waves: got %d, want 2 ([X] then [Y])", len(result.Waves))
	}
	w0 := nodeIDs(result.Waves[0].Nodes)
	w1 := nodeIDs(result.Waves[1].Nodes)
	if len(w0) != 1 || w0[0] != "X" || len(w1) != 1 || w1[0] != "Y" {
		t.Fatalf("waves: got %v / %v, want [X] / [Y]", w0, w1)
	}
	if result.ReadyNow != 1 {
		t.Fatalf("ready_now: got %d, want 1 (X only)", result.ReadyNow)
	}
}

func TestSchedule_RelatedNotHardConstraint(t *testing.T) {
	issues := []Issue{
		mkIssue("A", "Task A", "todo", "high"),
		mkIssue("B", "Task B", "todo", "medium"),
	}
	deps := []Dependency{
		// Reciprocal "related" edges: if treated as ordering constraints
		// these would form a hard cycle. They must not.
		{IssueID: "A", DependsOnID: "B", Type: "related"},
		{IssueID: "B", DependsOnID: "A", Type: "related"},
	}

	result := Schedule(ScheduleInput{
		ProjectID:    "proj-related",
		Issues:       issues,
		Dependencies: deps,
		Now:          fixedNow,
	})

	if result.CycleDetected {
		t.Fatal("related edges must not create a hard cycle")
	}
	if len(result.Waves) != 1 {
		t.Fatalf("waves: got %d, want 1 (all ready)", len(result.Waves))
	}
	if len(result.Waves[0].Nodes) != 2 {
		t.Fatalf("wave 0 nodes: got %v, want 2", nodeIDs(result.Waves[0].Nodes))
	}
	if result.ReadyNow != 2 {
		t.Fatalf("ready_now: got %d, want 2", result.ReadyNow)
	}
}

func TestSchedule_RelatedDoesNotOrder(t *testing.T) {
	issues := []Issue{
		mkIssue("A", "Prerequisite", "todo", "high"),
		mkIssue("B", "Depends on A", "todo", "medium"),
		mkIssue("C", "Related to A", "todo", "medium"),
	}
	deps := []Dependency{
		{IssueID: "B", DependsOnID: "A", Type: "blocked_by"},
		{IssueID: "A", DependsOnID: "C", Type: "related"},
	}

	result := Schedule(ScheduleInput{
		ProjectID:    "proj-related-order",
		Issues:       issues,
		Dependencies: deps,
		Now:          fixedNow,
	})

	if result.CycleDetected {
		t.Fatal("unexpected cycle")
	}
	if len(result.Waves) != 2 {
		t.Fatalf("waves: got %d, want 2 (A+C together, then B)", len(result.Waves))
	}
	w0 := nodeIDs(result.Waves[0].Nodes)
	if len(w0) != 2 || w0[0] != "A" || w0[1] != "C" {
		t.Fatalf("wave 0 nodes: got %v, want [A C] (related must not order C)", w0)
	}
	if len(result.Waves[1].Nodes) != 1 || result.Waves[1].Nodes[0].IssueID != "B" {
		t.Fatalf("wave 1 nodes: got %v, want [B]", nodeIDs(result.Waves[1].Nodes))
	}
}

// --- Finding 3 (HIV-462): critical path excludes terminal issues ---

func TestSchedule_CriticalPathExcludesTerminal(t *testing.T) {
	issues := []Issue{
		mkIssue("A", "Old work", "todo", "high"),
		mkIssue("B", "Finished", "done", "high"),
		mkIssue("C", "Needs B (already done)", "todo", "medium"),
		mkIssue("D", "Needs C", "todo", "low"),
	}
	deps := []Dependency{
		{IssueID: "B", DependsOnID: "A", Type: "blocked_by"},
		{IssueID: "C", DependsOnID: "B", Type: "blocked_by"},
		{IssueID: "D", DependsOnID: "C", Type: "blocked_by"},
	}

	result := Schedule(ScheduleInput{
		ProjectID:    "proj-cp-terminal",
		Issues:       issues,
		Dependencies: deps,
		Now:          fixedNow,
	})

	// Wave decomposition: A+C ready (B done), then D.
	if len(result.Waves) != 2 {
		t.Fatalf("waves: got %d, want 2", len(result.Waves))
	}
	if len(result.Waves[0].Nodes) != 2 {
		t.Fatalf("wave 0 nodes: got %v, want [A C]", nodeIDs(result.Waves[0].Nodes))
	}
	if result.ReadyNow != 2 {
		t.Fatalf("ready_now: got %d, want 2", result.ReadyNow)
	}
	// Critical path must match wave decomposition and exclude terminal B.
	if len(result.CriticalPath) != 2 || result.CriticalPath[0] != "C" || result.CriticalPath[1] != "D" {
		t.Fatalf("critical path: got %v, want [C D]", result.CriticalPath)
	}
	for _, id := range result.CriticalPath {
		if id == "B" {
			t.Fatal("critical path must exclude terminal issue B")
		}
	}
}

func TestSchedule_CriticalPathTerminalParentNotIncluded(t *testing.T) {
	issues := []Issue{
		mkIssue("A", "Already done", "done", "high"),
		mkIssue("B", "Only open work", "todo", "medium"),
	}
	deps := []Dependency{
		{IssueID: "B", DependsOnID: "A", Type: "blocked_by"},
	}

	result := Schedule(ScheduleInput{
		ProjectID:    "proj-cp-terminal-parent",
		Issues:       issues,
		Dependencies: deps,
		Now:          fixedNow,
	})

	if len(result.Waves) != 1 || len(result.Waves[0].Nodes) != 1 || result.Waves[0].Nodes[0].IssueID != "B" {
		t.Fatalf("wave 0 nodes: got %v, want [B]", nodeIDs(result.Waves[0].Nodes))
	}
	if len(result.CriticalPath) != 1 || result.CriticalPath[0] != "B" {
		t.Fatalf("critical path: got %v, want [B] (terminal parent A excluded)", result.CriticalPath)
	}
}

// --- Idempotency key stability ---

func TestIdempotencyKey_Stable(t *testing.T) {
	k1 := idempotencyKey("proj", "issue-1", 0)
	k2 := idempotencyKey("proj", "issue-1", 0)
	if k1 != k2 {
		t.Fatalf("idempotency key not stable: %s != %s", k1, k2)
	}

	k3 := idempotencyKey("proj", "issue-1", 1)
	if k1 == k3 {
		t.Fatal("different wave index should produce different key")
	}
}

// --- Mutex key grouping ---

func TestMutexKey_Grouping(t *testing.T) {
	a := &Issue{ID: "a", AssigneeID: "agent-1"}
	b := &Issue{ID: "b", AssigneeID: "agent-1"}
	c := &Issue{ID: "c", AssigneeID: ""}

	if mutexKey(a) != mutexKey(b) {
		t.Fatal("same assignee should share mutex key")
	}
	if mutexKey(a) == mutexKey(c) {
		t.Fatal("unassigned issue should have its own mutex key")
	}
}

// helpers

func nodeIDs(nodes []WaveNode) []string {
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.IssueID
	}
	return ids
}
