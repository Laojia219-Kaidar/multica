package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func reviewReconcileFixture(seed byte) ReviewReconcileCandidate {
	workspace := "00000000-0000-0000-0000-000000000001"
	project := "00000000-0000-0000-0000-000000000002"
	// Use valid UUIDs for all scenario fields while keeping each issue stable.
	issue := fmt.Sprintf("00000000-0000-0000-0000-%012d", seed)
	sourceTask := fmt.Sprintf("00000000-0000-0000-0000-%012d", 100+seed)
	comment := fmt.Sprintf("00000000-0000-0000-0000-%012d", 200+seed)
	return ReviewReconcileCandidate{
		WorkspaceID: workspace, ProjectID: project, IssueID: issue, Status: "in_review", Stage: "review",
		CandidateRevision: "sha256:candidate-" + string([]byte{'0' + seed}), Generation: "generation-" + string([]byte{'0' + seed}),
		SourceRef: continuousDispatchReviewCommentRef(parseDispatchUUID(comment)), SourceTaskID: sourceTask,
		AuthorKnown: true, AuthorEmployeeID: "employee-author", AuthorAgentID: "agent-author",
		Reviewer:       ReviewReconcileReviewer{Known: true, Healthy: true, Independent: true, EmployeeID: "employee-reviewer", AgentID: "agent-reviewer"},
		ExistingTask:   ReviewReconcileTaskEvidence{Known: true},
		Lease:          ReviewReconcileLeaseEvidence{Required: true, Known: true, Available: true, LeaseID: "lease-1"},
		WIP:            ReviewReconcileWIPEvidence{Required: true, Known: true, Reconciled: true, Active: 0, Max: 2},
		AuthorityKnown: true, AuthorityEligible: true,
	}
}

func reviewReconcileInput(candidates ...ReviewReconcileCandidate) ReviewReconcileInput {
	return ReviewReconcileInput{WorkspaceID: "00000000-0000-0000-0000-000000000001", ProjectID: "00000000-0000-0000-0000-000000000002", Candidates: candidates, MaxDispatch: 10}
}

func TestReviewReconcilerExistingActiveTaskIsNotDuplicated(t *testing.T) {
	candidate := reviewReconcileFixture(1)
	candidate.ExistingTask = ReviewReconcileTaskEvidence{Known: true, Found: true, Open: true, TaskID: "task-existing"}
	plan := NewReviewReconciler().Plan(reviewReconcileInput(candidate))
	if got := plan.Items[0]; got.State != ReviewReconcileActive || got.Eligible || got.ExistingTaskID != "task-existing" {
		t.Fatalf("plan = %+v, want active existing task", got)
	}
}

func TestReviewReconcilerBlocksFullWIP(t *testing.T) {
	candidate := reviewReconcileFixture(2)
	candidate.WIP.Active = 2
	plan := NewReviewReconciler().Plan(reviewReconcileInput(candidate))
	if got := plan.Items[0]; got.State != ReviewReconcileBlocked || got.Reasons[0] != ReviewReasonWIPFull {
		t.Fatalf("plan = %+v, want WIP full", got)
	}
}

func TestReviewReconcilerRejectsSelfReview(t *testing.T) {
	candidate := reviewReconcileFixture(3)
	candidate.Reviewer.AgentID = candidate.AuthorAgentID
	plan := NewReviewReconciler().Plan(reviewReconcileInput(candidate))
	if got := plan.Items[0]; got.State != ReviewReconcileBlocked || got.Reasons[0] != ReviewReasonSelfReview {
		t.Fatalf("plan = %+v, want self-review block", got)
	}
}

func TestReviewReconcilerRequiresSourceLineage(t *testing.T) {
	candidate := reviewReconcileFixture(4)
	candidate.SourceTaskID = ""
	plan := NewReviewReconciler().Plan(reviewReconcileInput(candidate))
	if got := plan.Items[0]; got.State != ReviewReconcileBlocked || got.Reasons[0] != ReviewReasonSourceEvidence {
		t.Fatalf("plan = %+v, want source evidence block", got)
	}
}

type reviewReconcileDispatcherFake struct{ calls int }

func (f *reviewReconcileDispatcherFake) DispatchReviewIssue(
	context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID, pgtype.UUID, string, pgtype.UUID,
) (ContinuousDispatchTriggerResult, error) {
	f.calls++
	return ContinuousDispatchTriggerResult{Receipt: ContinuousDispatchReceipt{TaskID: pgtype.UUID{}}}, nil
}

func TestReviewReconcilerDispatchesOneEligibleThroughExistingReviewInterface(t *testing.T) {
	ready := reviewReconcileFixture(5)
	blocked := reviewReconcileFixture(6)
	blocked.WIP.Active = blocked.WIP.Max
	dispatcher := &reviewReconcileDispatcherFake{}
	result, err := NewReviewReconciler().Dispatch(context.Background(), reviewReconcileInput(ready, blocked), "00000000-0000-0000-0000-000000000003", dispatcher)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if dispatcher.calls != 1 || len(result.Receipts) != 1 || result.Plan.Eligible != 1 || result.Plan.Blocked != 1 {
		t.Fatalf("calls=%d receipts=%d plan=%+v, want one eligible dispatch and one blocked", dispatcher.calls, len(result.Receipts), result.Plan)
	}
}

func TestReviewReconcilerConflictOnlyBlocksItsItem(t *testing.T) {
	conflict := reviewReconcileFixture(7)
	conflict.Reviewer.Independent = false
	ready := reviewReconcileFixture(8)
	plan := NewReviewReconciler().Plan(reviewReconcileInput(conflict, ready))
	if plan.Blocked != 1 || plan.Eligible != 1 {
		t.Fatalf("plan=%+v, want one blocked and one eligible", plan)
	}
	for _, item := range plan.Items {
		if item.IssueID == conflict.IssueID && item.State != ReviewReconcileBlocked {
			t.Fatalf("conflict item=%+v, want blocked", item)
		}
		if item.IssueID == ready.IssueID && item.State != ReviewReconcileEligible {
			t.Fatalf("ready item=%+v, want eligible", item)
		}
	}
}

func TestReviewReconcilerMissingEvidenceDoesNotBlockOtherCandidates(t *testing.T) {
	missing := reviewReconcileFixture(9)
	missing.Reviewer.Known = false
	ready := reviewReconcileFixture(1)
	plan := NewReviewReconciler().Plan(reviewReconcileInput(missing, ready))
	if plan.Blocked != 1 || plan.Eligible != 1 {
		t.Fatalf("plan=%+v, want one blocked and one eligible", plan)
	}
}
