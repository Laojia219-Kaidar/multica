package service

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
)

// ReviewReconcileState is the bounded outcome for one explicit in_review
// candidate. A reconciler has no timer, goroutine, persistence, or Issue
// mutation; it is a service-level planning seam only.
type ReviewReconcileState string

const (
	ReviewReconcileEligible ReviewReconcileState = "eligible"
	ReviewReconcileBlocked  ReviewReconcileState = "blocked"
	ReviewReconcileActive   ReviewReconcileState = "active"
	ReviewReconcileFailed   ReviewReconcileState = "failed"
)

// ReviewReconcileReason is a stable, renderable explanation. A reason is not
// a command: the caller must obtain fresh evidence before retrying a blocked
// or failed candidate.
type ReviewReconcileReason string

const (
	ReviewReasonNotInReview        ReviewReconcileReason = "not_in_review"
	ReviewReasonStageMismatch      ReviewReconcileReason = "review_stage_required"
	ReviewReasonInvalidCandidate   ReviewReconcileReason = "candidate_identity_invalid"
	ReviewReasonDuplicateIdentity  ReviewReconcileReason = "duplicate_review_dispatch_identity"
	ReviewReasonSourceEvidence     ReviewReconcileReason = "source_lineage_evidence_missing"
	ReviewReasonTaskEvidence       ReviewReconcileReason = "active_review_task_evidence_missing"
	ReviewReasonExistingActiveTask ReviewReconcileReason = "existing_active_review_task"
	ReviewReasonWIPUnknown         ReviewReconcileReason = "review_wip_evidence_missing"
	ReviewReasonWIPReconcile       ReviewReconcileReason = "review_wip_not_reconciled"
	ReviewReasonWIPFull            ReviewReconcileReason = "review_wip_full"
	ReviewReasonLeaseUnknown       ReviewReconcileReason = "review_lease_evidence_missing"
	ReviewReasonLeaseUnavailable   ReviewReconcileReason = "review_lease_unavailable"
	ReviewReasonAuthorityUnknown   ReviewReconcileReason = "authority_evidence_missing"
	ReviewReasonAuthorityDenied    ReviewReconcileReason = "authority_review_denied"
	ReviewReasonAuthorUnknown      ReviewReconcileReason = "author_evidence_missing"
	ReviewReasonReviewerUnknown    ReviewReconcileReason = "reviewer_evidence_missing"
	ReviewReasonReviewerUnhealthy  ReviewReconcileReason = "reviewer_unhealthy"
	ReviewReasonReviewerNotIndep   ReviewReconcileReason = "reviewer_not_independent"
	ReviewReasonSelfReview         ReviewReconcileReason = "author_reviewer_conflict"
	ReviewReasonDispatchFailed     ReviewReconcileReason = "review_dispatch_failed"
)

type ReviewReconcileReviewer struct {
	EmployeeID  string `json:"employee_id"`
	AgentID     string `json:"agent_id"`
	Known       bool   `json:"known"`
	Healthy     bool   `json:"healthy"`
	Independent bool   `json:"independent"`
}

// ReviewReconcileTaskEvidence is a readback of the current review Task.
// Unknown evidence fails closed; a known, open Task makes the candidate active
// rather than creating another Task.
type ReviewReconcileTaskEvidence struct {
	Known  bool   `json:"known"`
	Found  bool   `json:"found"`
	Open   bool   `json:"open"`
	TaskID string `json:"task_id,omitempty"`
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

// ReviewReconcileCandidate is intentionally not a generic Issue. The source
// task and comment reference prove the implementation output under review;
// callers cannot use this structure to rerun a general Issue.
type ReviewReconcileCandidate struct {
	WorkspaceID       string `json:"workspace_id"`
	ProjectID         string `json:"project_id"`
	IssueID           string `json:"issue_id"`
	Status            string `json:"status"`
	Stage             string `json:"stage"`
	CandidateRevision string `json:"candidate_revision"`
	Generation        string `json:"generation"`
	SourceRef         string `json:"source_ref"`
	SourceTaskID      string `json:"source_task_id"`
	// These fields are populated only from the server-side Shadow projection.
	// They are deliberately excluded from JSON so an Owner/browser request can
	// never supply an author or reviewer identity to the Authority seam.
	SourceAuthorAgentID       string                       `json:"-"`
	PlannedReviewerEmployeeID string                       `json:"-"`
	PlannedReviewerAgentID    string                       `json:"-"`
	AuthorityKnown            bool                         `json:"authority_known"`
	AuthorityEligible         bool                         `json:"authority_eligible"`
	AuthorKnown               bool                         `json:"author_known"`
	AuthorEmployeeID          string                       `json:"author_employee_id,omitempty"`
	AuthorAgentID             string                       `json:"author_agent_id,omitempty"`
	Reviewer                  ReviewReconcileReviewer      `json:"reviewer"`
	ExistingTask              ReviewReconcileTaskEvidence  `json:"existing_task"`
	Lease                     ReviewReconcileLeaseEvidence `json:"lease"`
	WIP                       ReviewReconcileWIPEvidence   `json:"wip"`
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
	// identityKey remains server-local. It is the canonical five-field
	// dispatch identity used to deduplicate input before any trigger call.
	identityKey string
}

type ReviewReconcilePlan struct {
	SchemaVersion string                `json:"schema_version"`
	Items         []ReviewReconcileItem `json:"items"`
	Eligible      int                   `json:"eligible"`
	Blocked       int                   `json:"blocked"`
	Active        int                   `json:"active"`
	Failed        int                   `json:"failed"`
}

const reviewReconcileSchemaV1 = "hivecrew.review-reconcile-plan/v1"

var ErrReviewReconcileMaxDispatchRequired = errors.New("review reconciler max_dispatch must be positive")

// ReviewReconciler has no state and is safe to reuse for bounded snapshots.
type ReviewReconciler struct{}

func NewReviewReconciler() *ReviewReconciler { return &ReviewReconciler{} }

// Plan evaluates each candidate independently. A blocked item does not block
// other candidates; only a duplicate canonical identity is blocked locally.
func (r *ReviewReconciler) Plan(in ReviewReconcileInput) ReviewReconcilePlan {
	plan := ReviewReconcilePlan{SchemaVersion: reviewReconcileSchemaV1, Items: make([]ReviewReconcileItem, 0, len(in.Candidates))}
	seen := make(map[string]struct{}, len(in.Candidates))
	for _, candidate := range in.Candidates {
		item := reconcileReviewCandidate(in, candidate)
		if item.identityKey != "" {
			if _, duplicate := seen[item.identityKey]; duplicate {
				item = blockedReviewItem(item, ReviewReasonDuplicateIdentity, "deduplicate_review_dispatch_identity")
			} else {
				seen[item.identityKey] = struct{}{}
			}
		}
		plan.Items = append(plan.Items, item)
	}
	sort.SliceStable(plan.Items, func(i, j int) bool {
		if plan.Items[i].identityKey != plan.Items[j].identityKey {
			return plan.Items[i].identityKey < plan.Items[j].identityKey
		}
		return plan.Items[i].IssueID < plan.Items[j].IssueID
	})
	countReviewPlan(&plan)
	return plan
}

func reconcileReviewCandidate(in ReviewReconcileInput, c ReviewReconcileCandidate) ReviewReconcileItem {
	item := ReviewReconcileItem{IssueID: c.IssueID, NextAction: "hold"}
	identity, identityOK := reviewReconcileIdentity(c)
	if !reviewReconcileScopeValid(in) || !identityOK || in.WorkspaceID != c.WorkspaceID || in.ProjectID != c.ProjectID {
		return blockedReviewItem(item, ReviewReasonInvalidCandidate, "repair_source_identity")
	}
	item.identityKey = reviewReconcileIdentityKey(identity)
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
	if !c.ExistingTask.Known {
		return blockedReviewItem(item, ReviewReasonTaskEvidence, "read_back_review_task_evidence")
	}
	if c.ExistingTask.Found && c.ExistingTask.Open {
		if !canonicalNonEmpty(c.ExistingTask.TaskID) {
			return blockedReviewItem(item, ReviewReasonTaskEvidence, "read_back_existing_review_task")
		}
		item.State = ReviewReconcileActive
		item.ExistingTaskID = c.ExistingTask.TaskID
		item.Reasons = []ReviewReconcileReason{ReviewReasonExistingActiveTask}
		item.NextAction = "observe_existing_review_task"
		return item
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
	if c.Reviewer.EmployeeID == c.AuthorEmployeeID || c.Reviewer.AgentID == c.AuthorAgentID {
		return blockedReviewItem(item, ReviewReasonSelfReview, "resolve_reviewer_author_conflict")
	}
	item.State = ReviewReconcileEligible
	item.Eligible = true
	item.ReviewerID = c.Reviewer.EmployeeID
	item.NextAction = "dispatch_review_through_controlled_interface"
	return item
}

func reviewReconcileScopeValid(in ReviewReconcileInput) bool {
	workspaceID, projectID := parseDispatchUUID(in.WorkspaceID), parseDispatchUUID(in.ProjectID)
	return canonicalNonEmpty(in.WorkspaceID, in.ProjectID) && workspaceID.Valid && projectID.Valid &&
		shadowUUIDString(workspaceID) == in.WorkspaceID && shadowUUIDString(projectID) == in.ProjectID
}

func reviewReconcileIdentity(c ReviewReconcileCandidate) (continuousdispatch.DispatchIdentity, bool) {
	identity := continuousdispatch.DispatchIdentity{
		WorkspaceID: c.WorkspaceID, IssueID: c.IssueID, Stage: c.Stage,
		CandidateRevision: c.CandidateRevision, Generation: c.Generation,
	}
	workspaceID, projectID, issueID := parseDispatchUUID(c.WorkspaceID), parseDispatchUUID(c.ProjectID), parseDispatchUUID(c.IssueID)
	return identity, identity.Complete() && canonicalNonEmpty(c.ProjectID) && workspaceID.Valid && projectID.Valid && issueID.Valid &&
		shadowUUIDString(workspaceID) == c.WorkspaceID && shadowUUIDString(projectID) == c.ProjectID && shadowUUIDString(issueID) == c.IssueID
}

func reviewReconcileIdentityKey(identity continuousdispatch.DispatchIdentity) string {
	return identity.WorkspaceID + "\x00" + identity.IssueID + "\x00" + identity.Stage + "\x00" + identity.CandidateRevision + "\x00" + identity.Generation
}

func blockedReviewItem(item ReviewReconcileItem, reason ReviewReconcileReason, next string) ReviewReconcileItem {
	item.State = ReviewReconcileBlocked
	item.Eligible = false
	item.Reasons = []ReviewReconcileReason{reason}
	item.NextAction = next
	return item
}

func countReviewPlan(plan *ReviewReconcilePlan) {
	plan.Eligible, plan.Blocked, plan.Active, plan.Failed = 0, 0, 0, 0
	for _, item := range plan.Items {
		switch item.State {
		case ReviewReconcileEligible:
			plan.Eligible++
		case ReviewReconcileActive:
			plan.Active++
		case ReviewReconcileFailed:
			plan.Failed++
		default:
			plan.Blocked++
		}
	}
}

// ReviewReconcileDispatcher is intentionally satisfied by the existing
// server-side review trigger. No generic Issue rerun method is exposed.
type ReviewReconcileDispatcher interface {
	DispatchReviewIssue(context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID, pgtype.UUID, string, pgtype.UUID) (ContinuousDispatchTriggerResult, error)
}

type ReviewReconcileDispatchResult struct {
	Plan     ReviewReconcilePlan         `json:"plan"`
	Receipts []ContinuousDispatchReceipt `json:"receipts"`
}

// Dispatch calls only the existing controlled review interface and only for
// a once-per-canonical-identity eligible item. An ordinary one-item failure is
// converted into a failed plan item and does not stop independent candidates;
// global context cancellation/deadline is the only batch-wide stop condition.
func (r *ReviewReconciler) Dispatch(ctx context.Context, in ReviewReconcileInput, actorUserID string, dispatcher ReviewReconcileDispatcher) (ReviewReconcileDispatchResult, error) {
	plan := r.Plan(in)
	result := ReviewReconcileDispatchResult{Plan: plan}
	if in.MaxDispatch <= 0 {
		return result, ErrReviewReconcileMaxDispatchRequired
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if dispatcher == nil || !parseDispatchUUID(actorUserID).Valid {
		return result, fmt.Errorf("review reconciler dispatcher and actor are required")
	}
	max := in.MaxDispatch
	if max > plan.Eligible {
		max = plan.Eligible
	}
	result.Receipts = make([]ContinuousDispatchReceipt, 0, max)
	eligible := make(map[string]struct{}, plan.Eligible)
	for _, item := range plan.Items {
		if item.Eligible {
			eligible[item.identityKey] = struct{}{}
		}
	}
	processed := make(map[string]struct{}, max)
	attempts := 0
	for _, candidate := range in.Candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if attempts >= max {
			break
		}
		identity, valid := reviewReconcileIdentity(candidate)
		if !valid {
			continue
		}
		key := reviewReconcileIdentityKey(identity)
		if _, alreadyProcessed := processed[key]; alreadyProcessed {
			continue
		}
		if _, shouldDispatch := eligible[key]; !shouldDispatch {
			continue
		}
		processed[key] = struct{}{}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		attempts++
		dispatch, err := dispatcher.DispatchReviewIssue(ctx, parseDispatchUUID(candidate.WorkspaceID), parseDispatchUUID(candidate.ProjectID), parseDispatchUUID(candidate.IssueID), parseDispatchUUID(actorUserID), candidate.SourceRef, parseDispatchUUID(candidate.SourceTaskID))
		if err != nil {
			markReviewDispatchFailure(&result.Plan, key)
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return result, err
			}
			continue
		}
		result.Receipts = append(result.Receipts, dispatch.Receipt)
	}
	return result, nil
}

func markReviewDispatchFailure(plan *ReviewReconcilePlan, key string) {
	for index := range plan.Items {
		if plan.Items[index].identityKey != key {
			continue
		}
		plan.Items[index].State = ReviewReconcileFailed
		plan.Items[index].Eligible = false
		plan.Items[index].Reasons = []ReviewReconcileReason{ReviewReasonDispatchFailed}
		plan.Items[index].NextAction = "replan_after_review_dispatch_failure"
		break
	}
	countReviewPlan(plan)
}
