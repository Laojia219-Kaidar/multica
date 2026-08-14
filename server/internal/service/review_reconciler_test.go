package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func reviewReconcileFixture(seed int) ReviewReconcileCandidate {
	workspace := "00000000-0000-0000-0000-000000000001"
	project := "00000000-0000-0000-0000-000000000002"
	issue := fmt.Sprintf("00000000-0000-0000-0000-%012d", seed)
	sourceTask := fmt.Sprintf("00000000-0000-0000-0000-%012d", 100+seed)
	comment := fmt.Sprintf("00000000-0000-0000-0000-%012d", 200+seed)
	return ReviewReconcileCandidate{
		WorkspaceID: workspace, ProjectID: project, IssueID: issue,
		Status: "in_review", Stage: "review", CandidateRevision: fmt.Sprintf("sha256:candidate-%d", seed), Generation: fmt.Sprintf("generation-%d", seed),
		SourceRef: continuousDispatchReviewCommentRef(parseDispatchUUID(comment)), SourceTaskID: sourceTask,
		AuthorityKnown: true, AuthorityEligible: true,
		AuthorKnown: true, AuthorEmployeeID: "employee-author", AuthorAgentID: "agent-author",
		Reviewer:     ReviewReconcileReviewer{Known: true, Healthy: true, Independent: true, EmployeeID: "employee-reviewer", AgentID: "agent-reviewer"},
		ExistingTask: ReviewReconcileTaskEvidence{Known: true},
		Lease:        ReviewReconcileLeaseEvidence{Required: true, Known: true, Available: true, LeaseID: "lease-1"},
		WIP:          ReviewReconcileWIPEvidence{Required: true, Known: true, Reconciled: true, Active: 0, Max: 2},
	}
}

func reviewReconcileInput(candidates ...ReviewReconcileCandidate) ReviewReconcileInput {
	return ReviewReconcileInput{
		WorkspaceID: "00000000-0000-0000-0000-000000000001",
		ProjectID:   "00000000-0000-0000-0000-000000000002",
		Candidates:  candidates,
		MaxDispatch: 2,
	}
}

type reviewReconcileDispatcherFake struct {
	calls []string
	errs  map[string]error
}

func (f *reviewReconcileDispatcherFake) DispatchReviewIssue(
	_ context.Context, _ pgtype.UUID, _ pgtype.UUID, issueID pgtype.UUID, _ pgtype.UUID, _ string, _ pgtype.UUID,
) (ContinuousDispatchTriggerResult, error) {
	issue := shadowUUIDString(issueID)
	f.calls = append(f.calls, issue)
	if err := f.errs[issue]; err != nil {
		return ContinuousDispatchTriggerResult{}, err
	}
	return ContinuousDispatchTriggerResult{Receipt: ContinuousDispatchReceipt{TaskID: parseDispatchUUID("00000000-0000-0000-0000-000000000999")}}, nil
}

func TestReviewReconcilerDispatchZeroCallsForBlockedEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ReviewReconcileCandidate)
		want   ReviewReconcileReason
	}{
		{"missing lineage", func(c *ReviewReconcileCandidate) { c.SourceTaskID = "" }, ReviewReasonSourceEvidence},
		{"unknown WIP", func(c *ReviewReconcileCandidate) { c.WIP.Known = false }, ReviewReasonWIPUnknown},
		{"unknown lease", func(c *ReviewReconcileCandidate) { c.Lease.Known = false }, ReviewReasonLeaseUnknown},
		{"missing authority", func(c *ReviewReconcileCandidate) { c.AuthorityKnown = false }, ReviewReasonAuthorityUnknown},
		{"unhealthy reviewer", func(c *ReviewReconcileCandidate) { c.Reviewer.Healthy = false }, ReviewReasonReviewerUnhealthy},
		{"reviewer not independent", func(c *ReviewReconcileCandidate) { c.Reviewer.Independent = false }, ReviewReasonReviewerNotIndep},
		{"self review", func(c *ReviewReconcileCandidate) { c.Reviewer.AgentID = c.AuthorAgentID }, ReviewReasonSelfReview},
		{"open review task", func(c *ReviewReconcileCandidate) {
			c.ExistingTask = ReviewReconcileTaskEvidence{Known: true, Found: true, Open: true, TaskID: "task-existing"}
		}, ReviewReasonExistingActiveTask},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := reviewReconcileFixture(10)
			tt.mutate(&candidate)
			dispatcher := &reviewReconcileDispatcherFake{}
			result, err := NewReviewReconciler().Dispatch(context.Background(), reviewReconcileInput(candidate), "00000000-0000-0000-0000-000000000003", dispatcher)
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if len(dispatcher.calls) != 0 || len(result.Receipts) != 0 {
				t.Fatalf("calls=%v receipts=%v, want zero", dispatcher.calls, result.Receipts)
			}
			item := result.Plan.Items[0]
			if tt.want == ReviewReasonExistingActiveTask {
				if item.State != ReviewReconcileActive || item.NextAction == "" || len(item.Reasons) == 0 || item.Reasons[0] != tt.want {
					t.Fatalf("item=%+v, want explainable active review task", item)
				}
				return
			}
			if item.State != ReviewReconcileBlocked || item.NextAction == "" || len(item.Reasons) == 0 || item.Reasons[0] != tt.want {
				t.Fatalf("item=%+v, want blocked %q", item, tt.want)
			}
		})
	}
}

func TestReviewReconcilerDuplicateCanonicalIdentityDispatchesOnce(t *testing.T) {
	first := reviewReconcileFixture(20)
	duplicate := first
	dispatcher := &reviewReconcileDispatcherFake{}
	result, err := NewReviewReconciler().Dispatch(context.Background(), reviewReconcileInput(first, duplicate), "00000000-0000-0000-0000-000000000003", dispatcher)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(dispatcher.calls) != 1 || len(result.Receipts) != 1 || result.Plan.Eligible != 1 || result.Plan.Blocked != 1 {
		t.Fatalf("calls=%v receipts=%d plan=%+v, want exact one dispatch and one duplicate block", dispatcher.calls, len(result.Receipts), result.Plan)
	}
	duplicateSeen := false
	for _, item := range result.Plan.Items {
		if len(item.Reasons) > 0 && item.Reasons[0] == ReviewReasonDuplicateIdentity {
			duplicateSeen = true
		}
	}
	if !duplicateSeen {
		t.Fatalf("plan=%+v, want duplicate identity explanation", result.Plan)
	}
}

func TestReviewReconcilerDispatchFailureDoesNotStopIndependentCandidate(t *testing.T) {
	failing := reviewReconcileFixture(30)
	ready := reviewReconcileFixture(31)
	dispatcher := &reviewReconcileDispatcherFake{errs: map[string]error{failing.IssueID: errors.New("temporary worker conflict")}}
	result, err := NewReviewReconciler().Dispatch(context.Background(), reviewReconcileInput(failing, ready), "00000000-0000-0000-0000-000000000003", dispatcher)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(dispatcher.calls) != 2 || len(result.Receipts) != 1 || result.Plan.Failed != 1 || result.Plan.Eligible != 1 {
		t.Fatalf("calls=%v receipts=%d plan=%+v, want one failed item and one independent receipt", dispatcher.calls, len(result.Receipts), result.Plan)
	}
	for _, item := range result.Plan.Items {
		if item.IssueID == failing.IssueID && (item.State != ReviewReconcileFailed || item.NextAction == "" || len(item.Reasons) == 0 || item.Reasons[0] != ReviewReasonDispatchFailed) {
			t.Fatalf("failed item=%+v, want explainable dispatch failure", item)
		}
	}
}

func TestReviewReconcilerRequiresExplicitPositiveMaxDispatch(t *testing.T) {
	input := reviewReconcileInput(reviewReconcileFixture(40))
	input.MaxDispatch = 0
	dispatcher := &reviewReconcileDispatcherFake{}
	_, err := NewReviewReconciler().Dispatch(context.Background(), input, "00000000-0000-0000-0000-000000000003", dispatcher)
	if !errors.Is(err, ErrReviewReconcileMaxDispatchRequired) || len(dispatcher.calls) != 0 {
		t.Fatalf("err=%v calls=%v, want max-dispatch error and zero calls", err, dispatcher.calls)
	}
}

func TestReviewReconcilerBlockedItemDoesNotBlockOtherCandidate(t *testing.T) {
	blocked := reviewReconcileFixture(50)
	blocked.WIP.Active = blocked.WIP.Max
	ready := reviewReconcileFixture(51)
	dispatcher := &reviewReconcileDispatcherFake{}
	result, err := NewReviewReconciler().Dispatch(context.Background(), reviewReconcileInput(blocked, ready), "00000000-0000-0000-0000-000000000003", dispatcher)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(dispatcher.calls) != 1 || len(result.Receipts) != 1 || result.Plan.Blocked != 1 || result.Plan.Eligible != 1 {
		t.Fatalf("calls=%v receipts=%d plan=%+v, want blocked item isolated from ready item", dispatcher.calls, len(result.Receipts), result.Plan)
	}
}
