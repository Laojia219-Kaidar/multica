package service

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
)

// ReviewDispatchPreviewItem is a bounded, read-only proposal for one review
// Issue. The selected reviewer is always produced by the existing Shadow
// planner; callers cannot provide an Agent, Runtime, model, account, or
// generation.
type ReviewDispatchPreviewItem struct {
	IssueID      string                                `json:"issue_id"`
	IssueTitle   string                                `json:"issue_title"`
	SourceRef    string                                `json:"source_ref,omitempty"`
	SourceTaskID string                                `json:"source_task_id,omitempty"`
	State        continuousdispatch.State              `json:"state"`
	Reasons      []continuousdispatch.Reason           `json:"reasons,omitempty"`
	Selected     *continuousdispatch.CandidateDecision `json:"selected,omitempty"`
}

type ReviewDispatchPreview struct {
	SchemaVersion string                      `json:"schema_version"`
	Items         []ReviewDispatchPreviewItem `json:"items"`
	// Total is the number of review issues in the project, independent of the
	// requested page. It prevents a mixed-status project page from looking
	// empty when review work exists beyond the first general issue page.
	Total    int `json:"total"`
	Eligible int `json:"eligible"`
	Skipped  int `json:"skipped"`
}

type ReviewDispatchBatchResult struct {
	Preview  ReviewDispatchPreview       `json:"preview"`
	Receipts []ContinuousDispatchReceipt `json:"receipts"`
}

const reviewDispatchPreviewSchema = "hivecrew.review-dispatch-preview/v1"

const reviewDispatchScanPageSize = 200

// A legacy adapter may return a non-empty page while reporting Total=0. Keep
// the compatibility scan bounded and fail closed if it never exposes a
// terminating total or empty page.
const reviewDispatchMaxScanPages = 10000

type reviewDispatchFilteredInspector interface {
	InspectReviewProject(context.Context, pgtype.UUID, pgtype.UUID, int, int) (*ContinuousDispatchShadowResult, error)
}

// ReviewReconcileAuthorityEvidence is an internal, read-only seam for the
// future canonical Authority mapping. It is deliberately separate from the
// browser-facing preview and from the route selected by Shadow. A provider
// must prove the author and independent reviewer identity for this exact
// candidate; it must not infer either identity from names or local Tasks.
type ReviewReconcileAuthorityEvidence struct {
	AuthorityKnown    bool
	AuthorityEligible bool
	AuthorKnown       bool
	AuthorEmployeeID  string
	AuthorAgentID     string
	Reviewer          ReviewReconcileReviewer
}

// ReviewReconcileAuthorityEvidenceProvider is intentionally optional. Until
// the canonical Authority mapping and the review gate are wired together, a
// nil provider keeps every item fail-closed as authority_evidence_missing.
// Implementations must not perform HTTP writes, create Tasks, or mutate the
// Shadow projection.
type ReviewReconcileAuthorityEvidenceProvider interface {
	ResolveReviewReconcileEvidence(context.Context, pgtype.UUID, pgtype.UUID, ReviewReconcileCandidate) (ReviewReconcileAuthorityEvidence, error)
}

// ReviewDispatchBatchService drains only the existing review frontier. It
// delegates each write to ContinuousDispatchTriggerService, which recomputes
// the route and commits the existing Task+receipt idempotently. No queue,
// scheduler, employee registry, or workflow state is introduced here.
type ReviewDispatchBatchService struct {
	inspector         ContinuousDispatchProjectInspector
	trigger           *ContinuousDispatchTriggerService
	reconciler        *ReviewReconciler
	authorityEvidence ReviewReconcileAuthorityEvidenceProvider
}

func NewReviewDispatchBatchService(
	inspector ContinuousDispatchProjectInspector,
	trigger *ContinuousDispatchTriggerService,
) *ReviewDispatchBatchService {
	return &ReviewDispatchBatchService{inspector: inspector, trigger: trigger, reconciler: NewReviewReconciler()}
}

// WithAuthorityEvidenceProvider adds the explicit canonical mapping seam. It
// does not enable production Authority or perform any network operation.
// Dispatch still requires the Trigger's Authority gate and identity provider
// to be composed; otherwise candidates remain blocked per item.
func (s *ReviewDispatchBatchService) WithAuthorityEvidenceProvider(provider ReviewReconcileAuthorityEvidenceProvider) *ReviewDispatchBatchService {
	if s == nil {
		return nil
	}
	cp := *s
	cp.authorityEvidence = provider
	return &cp
}

func (s *ReviewDispatchBatchService) PreviewProject(
	ctx context.Context, workspaceID, projectID pgtype.UUID, limit, offset int,
) (ReviewDispatchPreview, error) {
	if s == nil || s.inspector == nil {
		return ReviewDispatchPreview{}, fmt.Errorf("review dispatch inspector is required")
	}
	if limit <= 0 || limit > 25 || offset < 0 {
		return ReviewDispatchPreview{}, fmt.Errorf("review dispatch page must be 1..25 and offset non-negative")
	}
	page, err := s.inspectReviewPage(ctx, workspaceID, projectID, limit, offset)
	if err != nil {
		return ReviewDispatchPreview{}, err
	}
	if page == nil || page.SchemaVersion != ContinuousDispatchShadowSchemaV1 ||
		page.WorkspaceID != shadowUUIDString(workspaceID) || page.ProjectID != shadowUUIDString(projectID) {
		return ReviewDispatchPreview{}, ErrContinuousDispatchSourceGap
	}
	input, err := s.reconcileInput(ctx, workspaceID, projectID, page)
	if err != nil {
		return ReviewDispatchPreview{}, err
	}
	plan := s.reconcilerOrNew().Plan(input)
	return reviewDispatchPreviewFromPlan(page, plan), nil
}

func (s *ReviewDispatchBatchService) reconcilerOrNew() *ReviewReconciler {
	if s != nil && s.reconciler != nil {
		return s.reconciler
	}
	return NewReviewReconciler()
}

func reviewDispatchPreviewFromPlan(page *ContinuousDispatchShadowResult, plan ReviewReconcilePlan) ReviewDispatchPreview {
	preview := ReviewDispatchPreview{SchemaVersion: reviewDispatchPreviewSchema, Items: make([]ReviewDispatchPreviewItem, 0, len(plan.Items)), Total: page.Total}
	byIssue := make(map[string]ContinuousDispatchShadowItem, len(page.Items))
	for _, item := range page.Items {
		byIssue[item.IssueID] = item
	}
	for _, reconciled := range plan.Items {
		shadow := byIssue[reconciled.IssueID]
		proposal := ReviewDispatchPreviewItem{
			IssueID: reconciled.IssueID, IssueTitle: shadow.IssueTitle, SourceRef: shadow.SourceRef, SourceTaskID: shadow.SourceTaskID,
			Selected: nil,
		}
		switch reconciled.State {
		case ReviewReconcileEligible:
			proposal.State = continuousdispatch.StateReady
			proposal.Selected = shadow.NextAction.Selected
			preview.Eligible++
		case ReviewReconcileActive:
			proposal.State = continuousdispatch.StateAlreadyDispatched
			preview.Skipped++
		default:
			proposal.State = continuousdispatch.StateBlocked
			preview.Skipped++
		}
		proposal.Reasons = make([]continuousdispatch.Reason, 0, len(reconciled.Reasons))
		for _, reason := range reconciled.Reasons {
			proposal.Reasons = append(proposal.Reasons, continuousdispatch.Reason(reason))
		}
		preview.Items = append(preview.Items, proposal)
	}
	return preview
}

func (s *ReviewDispatchBatchService) reconcileInput(
	ctx context.Context,
	workspaceID, projectID pgtype.UUID,
	page *ContinuousDispatchShadowResult,
) (ReviewReconcileInput, error) {
	if page == nil || s == nil {
		return ReviewReconcileInput{}, ErrContinuousDispatchSourceGap
	}
	input := ReviewReconcileInput{
		WorkspaceID: page.WorkspaceID,
		ProjectID:   page.ProjectID,
		Candidates:  make([]ReviewReconcileCandidate, 0, len(page.Items)),
		MaxDispatch: page.Limit,
	}
	if input.MaxDispatch <= 0 || input.MaxDispatch > 25 {
		return ReviewReconcileInput{}, ErrContinuousDispatchSourceGap
	}
	for _, shadow := range page.Items {
		candidate := reviewReconcileCandidateFromShadow(page, shadow)
		if s.authorityEvidence != nil && s.authorityGateReady() {
			evidence, err := s.authorityEvidence.ResolveReviewReconcileEvidence(ctx, workspaceID, projectID, candidate)
			if err == nil && reviewReconcileAuthorityEvidenceComplete(evidence) {
				candidate.AuthorityKnown = evidence.AuthorityKnown
				candidate.AuthorityEligible = evidence.AuthorityEligible
				candidate.AuthorKnown = evidence.AuthorKnown
				candidate.AuthorEmployeeID = evidence.AuthorEmployeeID
				candidate.AuthorAgentID = evidence.AuthorAgentID
				candidate.Reviewer = evidence.Reviewer
			}
		}
		input.Candidates = append(input.Candidates, candidate)
	}
	return input, nil
}

func (s *ReviewDispatchBatchService) authorityGateReady() bool {
	return s != nil && s.trigger != nil && s.trigger.authorityReviewMode &&
		s.trigger.authorityReviewGate != nil && s.trigger.authorityReviewIDs != nil
}

func reviewReconcileAuthorityEvidenceComplete(evidence ReviewReconcileAuthorityEvidence) bool {
	return evidence.AuthorityKnown && evidence.AuthorityEligible && evidence.AuthorKnown &&
		canonicalNonEmpty(evidence.AuthorEmployeeID, evidence.AuthorAgentID) &&
		evidence.Reviewer.Known && evidence.Reviewer.Healthy && evidence.Reviewer.Independent &&
		canonicalNonEmpty(evidence.Reviewer.EmployeeID, evidence.Reviewer.AgentID)
}

func reviewReconcileCandidateFromShadow(page *ContinuousDispatchShadowResult, shadow ContinuousDispatchShadowItem) ReviewReconcileCandidate {
	selected := shadow.NextAction.Selected
	candidate := ReviewReconcileCandidate{
		WorkspaceID: page.WorkspaceID, ProjectID: page.ProjectID, IssueID: shadow.IssueID,
		Status: shadow.Status, Stage: shadow.DispatchIdentity.Stage,
		CandidateRevision: shadow.DispatchIdentity.CandidateRevision, Generation: shadow.DispatchIdentity.Generation,
		SourceRef: shadow.SourceRef, SourceTaskID: shadow.SourceTaskID,
		SourceAuthorAgentID: shadow.SourceAuthorAgentID,
		ExistingTask:        ReviewReconcileTaskEvidence{Known: page.Sources.Tasks},
		Lease:               ReviewReconcileLeaseEvidence{Required: true, Known: page.Sources.WriteLease, Available: page.Sources.WriteLease, LeaseID: shadow.NextAction.WriteLeaseID},
		WIP:                 ReviewReconcileWIPEvidence{Required: true, Known: page.Sources.WIP, Reconciled: page.Sources.WIP},
	}
	if shadow.NextAction.ExistingTaskID != "" {
		candidate.ExistingTask = ReviewReconcileTaskEvidence{Known: page.Sources.Tasks, Found: true, Open: true, TaskID: shadow.NextAction.ExistingTaskID}
	}
	if selected != nil {
		candidate.WIP.Active = selected.ActiveWIP
		candidate.WIP.Max = selected.MaxWIP
		candidate.PlannedReviewerEmployeeID = selected.EmployeeID
		candidate.PlannedReviewerAgentID = selected.AgentID
	}
	// Authority, author and reviewer evidence intentionally remain unknown
	// unless the explicit provider seam is fully composed above. The Shadow
	// route is not sufficient proof for any of those identities.
	return candidate
}

// inspectReviewPage prefers the SQL status-filtered shadow query. The
// complete-scan fallback keeps older read-only adapters correct: it walks all
// general pages, filters every item, and then applies the requested review
// offset/limit. It intentionally never treats one general page as a review
// page.
func (s *ReviewDispatchBatchService) inspectReviewPage(
	ctx context.Context, workspaceID, projectID pgtype.UUID, limit, offset int,
) (*ContinuousDispatchShadowResult, error) {
	if filtered, ok := s.inspector.(reviewDispatchFilteredInspector); ok {
		return filtered.InspectReviewProject(ctx, workspaceID, projectID, limit, offset)
	}

	var first *ContinuousDispatchShadowResult
	reviewItems := make([]ContinuousDispatchShadowItem, 0, limit)
	reviewTotal := 0
	terminated := false
	for pageNumber := 0; pageNumber < reviewDispatchMaxScanPages; pageNumber++ {
		scanOffset := pageNumber * reviewDispatchScanPageSize
		page, err := s.inspector.InspectProject(ctx, workspaceID, projectID, reviewDispatchScanPageSize, scanOffset)
		if err != nil {
			return nil, err
		}
		if page == nil || page.SchemaVersion != ContinuousDispatchShadowSchemaV1 ||
			page.WorkspaceID != shadowUUIDString(workspaceID) || page.ProjectID != shadowUUIDString(projectID) {
			return nil, ErrContinuousDispatchSourceGap
		}
		if first == nil {
			first = page
		}
		for _, item := range page.Items {
			if item.Status != "in_review" {
				continue
			}
			reviewTotal++
			if reviewTotal <= offset || len(reviewItems) >= limit {
				continue
			}
			reviewItems = append(reviewItems, item)
		}
		if len(page.Items) == 0 || (page.Total > 0 && scanOffset+len(page.Items) >= page.Total) {
			terminated = true
			break
		}
	}
	if first == nil || !terminated {
		return nil, ErrContinuousDispatchSourceGap
	}
	result := *first
	result.Items = reviewItems
	result.Total = reviewTotal
	result.Limit = limit
	result.Offset = offset
	return &result, nil
}

// DispatchProject performs at most one bounded page of review dispatches.
// The request contains only owner actor and page controls. Each dispatch is
// re-planned immediately by the existing trigger, so a stale preview cannot
// select a reviewer or bypass active-task/WIP/health gates.
func (s *ReviewDispatchBatchService) DispatchProject(
	ctx context.Context, workspaceID, projectID, actorUserID pgtype.UUID,
	limit, offset int,
) (ReviewDispatchBatchResult, error) {
	if s == nil || s.trigger == nil {
		return ReviewDispatchBatchResult{}, fmt.Errorf("review dispatch trigger is required")
	}
	page, err := s.inspectReviewPage(ctx, workspaceID, projectID, limit, offset)
	if err != nil {
		return ReviewDispatchBatchResult{}, err
	}
	if page == nil || page.SchemaVersion != ContinuousDispatchShadowSchemaV1 ||
		page.WorkspaceID != shadowUUIDString(workspaceID) || page.ProjectID != shadowUUIDString(projectID) {
		return ReviewDispatchBatchResult{}, ErrContinuousDispatchSourceGap
	}
	input, err := s.reconcileInput(ctx, workspaceID, projectID, page)
	if err != nil {
		return ReviewDispatchBatchResult{}, err
	}
	dispatch, err := s.reconcilerOrNew().Dispatch(ctx, input, shadowUUIDString(actorUserID), s.trigger)
	if err != nil {
		return ReviewDispatchBatchResult{}, err
	}
	return ReviewDispatchBatchResult{
		Preview:  reviewDispatchPreviewFromPlan(page, dispatch.Plan),
		Receipts: dispatch.Receipts,
	}, nil
}
