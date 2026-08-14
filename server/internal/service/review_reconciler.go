package service

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"
)

// ReviewReconcileState is the bounded decision for one explicit review
// candidate. The reconciler is deliberately a planner: it does not create a
// Task, change an Issue, acquire a lease, or start a worker.
type ReviewReconcileState string

const (
	ReviewReconcileEligible ReviewReconcileState = "eligible"
	ReviewReconcileBlocked  ReviewReconcileState = "blocked"
	ReviewReconcileActive   ReviewReconcileState = "active"
)

// ReviewReconcileReason is a stable explanation code for the review drain.
// These codes are intentionally local to this service so a future UI or
// scheduler can render next_action without treating a reason as a command.
type ReviewReconcileReason string

const (
	ReviewReasonNotInReview        ReviewReconcileReason = "not_in_review"
	ReviewReasonStageMismatch      ReviewReconcileReason = "review_stage_required"
	ReviewReasonSourceEvidence     ReviewReconcileReason = "source_lineage_evidence_missing"
	ReviewReasonExistingActiveTask ReviewReconcileReason = "existing_active_review_task"
	ReviewReasonWIPUnknown         ReviewReconcileReason = "review_wip_evidence_missing"
	ReviewReasonWIPReconcile       ReviewReconcileReason = "review_wip_not_reconciled"
	ReviewReasonWIPFull            ReviewReconcileReason = "review_wip_full"
	ReviewReasonLeaseUnknown       ReviewReconcileReason = "review_lease_evidence_missing"
	ReviewReasonLeaseUnavailable   ReviewReconcileReason = "review_lease_unavailable"
	ReviewReasonAuthorityUnknown   ReviewReconcileReason = "authority_evidence_missing"
	ReviewReasonAuthorityDenied    ReviewReconcileReason = "authority_review_denied"
	ReviewReasonReviewerUnknown    ReviewReconcileReason = "reviewer_evidence_missing"
	ReviewReasonReviewerUnhealthy  ReviewReconcileReason = "reviewer_unhealthy"
	ReviewReasonReviewerNotIndep   ReviewReconcileReason = "reviewer_not_independent"
	ReviewReasonAuthorUnknown      ReviewReconcileReason = "author_evidence_missing"
	ReviewReasonSelfReview         ReviewReconcileReason = "author_reviewer_conflict"
	ReviewReasonInvalidCandidate   ReviewReconcileReason = "candidate_identity_invalid"
)

// ReviewReconcileReviewer is the selected reviewer read model. The caller
// must supply the authoritative selection; this reconciler never invents a
// reviewer from a title, display name, or arbitrary local Agent.
type ReviewReconcileReviewer struct {
	EmployeeID  string `json:"employee_id"`
	AgentID     string `json:"agent_id"`
	Known       bool   `json:"known"`
	Healthy     bool   `json:"healthy"`
	Independent bool   `json:"independent"`
}

// ReviewReconcileTaskEvidence describes the current canonical review Task
// readback. Unknown evidence fails closed. A known open Task is reported as
// active so a later trigger cannot create a duplicate.
type ReviewReconcileTaskEvidence struct {
	Known        bool   `json:"known"`
	Found        bool   `json:"found"`
	Open         bool   `json:"open"`
	TaskID       string `json:"task_id,omitempty"`
	SourceTaskID string `json:"source_task_id,omitempty"`
}

type ReviewReconcileLeaseEvidence struct {
	Required  bool   `json:"required"`
	Known     bool   `json:"known"`
	Available bool   `json:"available"`
	LeaseID   string `json:"lease_id,omitempty"`
}

type ReviewReconcileWIPEvidence struct {
	Required   bool `json:"required"`
	Known      bool `json:"known"`
	Reconciled bool `json:"reconciled"`
	Active     int  `json:"active"`
	Max        int  `json:"max"`
}

// ReviewReconcileCandidate is the complete, explicit evidence snapshot for
// one item. It is intentionally not a generic Issue: source_task_id and
// source_ref must prove the implementation result that is being reviewed.
type ReviewReconcileCandidate struct {
	WorkspaceID       string                       `json:"workspace_id"`
	ProjectID         string                       `json:"project_id"`
	IssueID           string                       `json:"issue_id"`
	Status            string                       `json:"status"`
	Stage             string                       `json:"stage"`
	CandidateRevision string                       `json:"candidate_revision"`
	Generation        string                       `json:"generation"`
	SourceRef         string                       `json:"source_ref"`
	SourceTaskID      string                       `json:"source_task_id"`
	AuthorKnown       bool                         `json:"author_known"`
	AuthorEmployeeID  string                       `json:"author_employee_id,omitempty"`
	AuthorAgentID     string                       `json:"author_agent_id,omitempty"`
	Reviewer          ReviewReconcileReviewer      `json:"reviewer"`
	ExistingTask      ReviewReconcileTaskEvidence  `json:"existing_task"`
	Lease             ReviewReconcileLeaseEvidence `json:"lease"`
	WIP               ReviewReconcileWIPEvidence   `json:"wip"`
	AuthorityKnown    bool                         `json:"authority_known"`
	AuthorityEligible bool                         `json:"authority_eligible"`
}

type ReviewReconcileInput struct {
	WorkspaceID string                     `json:"workspace_id"`
	ProjectID   string                     `json:"project_id"`
	Candidates  []ReviewReconcileCandidate `json:"candidates"`
	MaxDispatch int                        `json:"max_dispatch"`
}

type ReviewReconcileItem struct {
	IssueID        string                  `json:"issue_id"`
	State          ReviewReconcileState    `json:"state"`
	Eligible       bool                    `json:"eligible"`
	ReviewerID     string                  `json:"reviewer_id,omitempty"`
	ExistingTaskID string                  `json:"existing_task_id,omitempty"`
	Reasons        []ReviewReconcileReason `json:"reasons,omitempty"`
	NextAction     string                  `json:"next_action"`
}

type ReviewReconcilePlan struct {
	SchemaVersion string                `json:"schema_version"`
	Items         []ReviewReconcileItem `json:"items"`
	Eligible      int                   `json:"eligible"`
	Blocked       int                   `json:"blocked"`
	Active        int                   `json:"active"`
}

const reviewReconcileSchemaV1 = "hivecrew.review-reconcile-plan/v1"

// ReviewReconciler is pure until Dispatch is explicitly called. It has no
// timers, goroutines, persistence, or implicit trigger.
type ReviewReconciler struct{}

func NewReviewReconciler() *ReviewReconciler { return &ReviewReconciler{} }

// Plan evaluates every candidate independently. One blocked candidate does
// not prevent another candidate from becoming eligible.
func (r *ReviewReconciler) Plan(in ReviewReconcileInput) ReviewReconcilePlan {
	plan := ReviewReconcilePlan{SchemaVersion: reviewReconcileSchemaV1, Items: make([]ReviewReconcileItem, 0, len(in.Candidates))}
	for _, candidate := range in.Candidates {
		item := reconcileReviewCandidate(in, candidate)
		plan.Items = append(plan.Items, item)
		switch item.State {
		case ReviewReconcileEligible:
			plan.Eligible++
		case ReviewReconcileActive:
			plan.Active++
		default:
			plan.Blocked++
		}
	}
	// Stable issue ordering keeps previews and receipts easy to compare and
	// avoids making input ordering an accidental scheduling policy.
	sort.SliceStable(plan.Items, func(i, j int) bool { return plan.Items[i].IssueID < plan.Items[j].IssueID })
	return plan
}

func reconcileReviewCandidate(in ReviewReconcileInput, c ReviewReconcileCandidate) ReviewReconcileItem {
	item := ReviewReconcileItem{IssueID: c.IssueID, NextAction: "hold"}
	if !canonicalNonEmpty(in.WorkspaceID, in.ProjectID, c.WorkspaceID, c.ProjectID, c.IssueID, c.CandidateRevision, c.Generation) ||
		in.WorkspaceID != c.WorkspaceID || in.ProjectID != c.ProjectID || !parseDispatchUUID(c.IssueID).Valid {
		return blockedReviewItem(item, ReviewReasonInvalidCandidate, "repair_source_identity")
	}
	if c.Status != "in_review" {
		return blockedReviewItem(item, ReviewReasonNotInReview, "leave_non_review_issue_untouched")
	}
	if c.Stage != "review" {
		return blockedReviewItem(item, ReviewReasonStageMismatch, "reconcile_review_stage")
	}
	if !canonicalNonEmpty(c.SourceRef, c.SourceTaskID) || !parseDispatchUUID(c.SourceTaskID).Valid {
		return blockedReviewItem(item, ReviewReasonSourceEvidence, "restore_source_task_lineage")
	}
	if _, ok := parseContinuousDispatchReviewCommentRef(c.SourceRef); !ok {
		return blockedReviewItem(item, ReviewReasonSourceEvidence, "restore_source_task_lineage")
	}
	if c.ExistingTask.Known && c.ExistingTask.Found && c.ExistingTask.Open {
		if !canonicalNonEmpty(c.ExistingTask.TaskID) {
			return blockedReviewItem(item, ReviewReasonExistingActiveTask, "read_back_existing_review_task")
		}
		item.State, item.NextAction, item.ExistingTaskID = ReviewReconcileActive, "observe_existing_review_task", c.ExistingTask.TaskID
		item.Reasons = []ReviewReconcileReason{ReviewReasonExistingActiveTask}
		return item
	}
	if !c.ExistingTask.Known {
		return blockedReviewItem(item, ReviewReasonExistingActiveTask, "read_back_review_task_evidence")
	}
	if !c.WIP.Required || !c.WIP.Known {
		return blockedReviewItem(item, ReviewReasonWIPUnknown, "refresh_review_wip")
	}
	if !c.WIP.Reconciled {
		return blockedReviewItem(item, ReviewReasonWIPReconcile, "reconcile_review_wip")
	}
	if c.WIP.Max <= 0 || c.WIP.Active < 0 || c.WIP.Active >= c.WIP.Max {
		return blockedReviewItem(item, ReviewReasonWIPFull, "wait_or_assign_review_capacity")
	}
	if !c.Lease.Required || !c.Lease.Known {
		return blockedReviewItem(item, ReviewReasonLeaseUnknown, "read_back_review_lease")
	}
	if !c.Lease.Available {
		return blockedReviewItem(item, ReviewReasonLeaseUnavailable, "wait_for_review_lease")
	}
	if !c.AuthorityKnown {
		return blockedReviewItem(item, ReviewReasonAuthorityUnknown, "read_back_authority_dispatch_evidence")
	}
	if !c.AuthorityEligible {
		return blockedReviewItem(item, ReviewReasonAuthorityDenied, "wait_for_authority_review_eligibility")
	}
	if !c.AuthorKnown || !canonicalNonEmpty(c.AuthorEmployeeID, c.AuthorAgentID) {
		return blockedReviewItem(item, ReviewReasonAuthorUnknown, "restore_author_identity_evidence")
	}
	if !c.Reviewer.Known || !canonicalNonEmpty(c.Reviewer.EmployeeID, c.Reviewer.AgentID) {
		return blockedReviewItem(item, ReviewReasonReviewerUnknown, "resolve_independent_reviewer")
	}
	if !c.Reviewer.Healthy {
		return blockedReviewItem(item, ReviewReasonReviewerUnhealthy, "repair_or_replace_reviewer_runtime")
	}
	if !c.Reviewer.Independent {
		return blockedReviewItem(item, ReviewReasonReviewerNotIndep, "resolve_independent_reviewer")
	}
	if c.Reviewer.AgentID == c.AuthorAgentID || c.Reviewer.EmployeeID == c.AuthorEmployeeID {
		return blockedReviewItem(item, ReviewReasonSelfReview, "resolve_reviewer_author_conflict")
	}
	item.State, item.Eligible, item.ReviewerID, item.NextAction = ReviewReconcileEligible, true, c.Reviewer.EmployeeID, "dispatch_review_through_controlled_interface"
	return item
}

func blockedReviewItem(item ReviewReconcileItem, reason ReviewReconcileReason, next string) ReviewReconcileItem {
	item.State, item.Eligible, item.Reasons, item.NextAction = ReviewReconcileBlocked, false, []ReviewReconcileReason{reason}, next
	return item
}

// ReviewReconcileDispatcher is satisfied by the existing server-side review
// trigger. It deliberately exposes no generic Issue rerun method.
type ReviewReconcileDispatcher interface {
	DispatchReviewIssue(context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID, pgtype.UUID, string, pgtype.UUID) (ContinuousDispatchTriggerResult, error)
}

type ReviewReconcileDispatchResult struct {
	Plan     ReviewReconcilePlan         `json:"plan"`
	Receipts []ContinuousDispatchReceipt `json:"receipts"`
}

// Dispatch re-plans from the supplied snapshot, then calls only the existing
// review trigger for eligible candidates. It is bounded by MaxDispatch and
// has no background scheduling behavior.
func (r *ReviewReconciler) Dispatch(ctx context.Context, in ReviewReconcileInput, actorUserID string, dispatcher ReviewReconcileDispatcher) (ReviewReconcileDispatchResult, error) {
	if dispatcher == nil || !parseDispatchUUID(actorUserID).Valid {
		return ReviewReconcileDispatchResult{}, fmt.Errorf("review reconciler dispatcher and actor are required")
	}
	plan := r.Plan(in)
	max := in.MaxDispatch
	if max <= 0 || max > plan.Eligible {
		max = plan.Eligible
	}
	result := ReviewReconcileDispatchResult{Plan: plan, Receipts: make([]ContinuousDispatchReceipt, 0, max)}
	count := 0
	for _, candidate := range in.Candidates {
		item := reconcileReviewCandidate(in, candidate)
		if !item.Eligible || count >= max {
			continue
		}
		receipt, err := dispatcher.DispatchReviewIssue(ctx, parseDispatchUUID(candidate.WorkspaceID), parseDispatchUUID(candidate.ProjectID), parseDispatchUUID(candidate.IssueID), parseDispatchUUID(actorUserID), candidate.SourceRef, parseDispatchUUID(candidate.SourceTaskID))
		if err != nil {
			return result, err
		}
		result.Receipts = append(result.Receipts, receipt.Receipt)
		count++
	}
	return result, nil
}
