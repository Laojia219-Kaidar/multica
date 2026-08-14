package service

import (
	"context"
	"fmt"
	"strings"

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

type reviewDispatchFilteredInspector interface {
	InspectReviewProject(context.Context, pgtype.UUID, pgtype.UUID, int, int) (*ContinuousDispatchShadowResult, error)
}

// ReviewDispatchBatchService drains only the existing review frontier. It
// delegates each write to ContinuousDispatchTriggerService, which recomputes
// the route and commits the existing Task+receipt idempotently. No queue,
// scheduler, employee registry, or workflow state is introduced here.
type ReviewDispatchBatchService struct {
	inspector ContinuousDispatchProjectInspector
	trigger   *ContinuousDispatchTriggerService
}

func NewReviewDispatchBatchService(
	inspector ContinuousDispatchProjectInspector,
	trigger *ContinuousDispatchTriggerService,
) *ReviewDispatchBatchService {
	return &ReviewDispatchBatchService{inspector: inspector, trigger: trigger}
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
	preview := ReviewDispatchPreview{SchemaVersion: reviewDispatchPreviewSchema, Items: make([]ReviewDispatchPreviewItem, 0, len(page.Items)), Total: page.Total}
	for _, item := range page.Items {
		proposal := ReviewDispatchPreviewItem{
			IssueID: item.IssueID, IssueTitle: item.IssueTitle, SourceRef: item.SourceRef, SourceTaskID: item.SourceTaskID,
			State: item.NextAction.State, Reasons: append([]continuousdispatch.Reason(nil), item.NextAction.Reasons...),
			Selected: item.NextAction.Selected,
		}
		if (strings.TrimSpace(item.SourceRef) == "" || strings.TrimSpace(item.SourceTaskID) == "") && proposal.Selected != nil {
			proposal.State = continuousdispatch.StateBlocked
			proposal.Reasons = []continuousdispatch.Reason{continuousdispatch.ReasonReviewSourceTaskMissing}
			proposal.Selected = nil
		}
		if proposal.State == continuousdispatch.StateReady || proposal.State == continuousdispatch.StateFallback {
			preview.Eligible++
		} else {
			preview.Skipped++
		}
		preview.Items = append(preview.Items, proposal)
	}
	return preview, nil
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
	for scanOffset := 0; ; scanOffset += reviewDispatchScanPageSize {
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
			break
		}
	}
	if first == nil {
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
	preview, err := s.PreviewProject(ctx, workspaceID, projectID, limit, offset)
	if err != nil {
		return ReviewDispatchBatchResult{}, err
	}
	result := ReviewDispatchBatchResult{Preview: preview, Receipts: make([]ContinuousDispatchReceipt, 0, preview.Eligible)}
	for _, item := range preview.Items {
		if item.State != continuousdispatch.StateReady && item.State != continuousdispatch.StateFallback {
			continue
		}
		dispatch, dispatchErr := s.trigger.DispatchReviewIssue(
			ctx, workspaceID, projectID, parseDispatchUUID(item.IssueID), actorUserID, item.SourceRef, parseDispatchUUID(item.SourceTaskID),
		)
		if dispatchErr != nil {
			return result, dispatchErr
		}
		result.Receipts = append(result.Receipts, dispatch.Receipt)
	}
	return result, nil
}
