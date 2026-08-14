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
	SourceTaskID string                                `json:"source_task_id,omitempty"`
	State        continuousdispatch.State              `json:"state"`
	Reasons      []continuousdispatch.Reason           `json:"reasons,omitempty"`
	Selected     *continuousdispatch.CandidateDecision `json:"selected,omitempty"`
}

type ReviewDispatchPreview struct {
	SchemaVersion string                      `json:"schema_version"`
	Items         []ReviewDispatchPreviewItem `json:"items"`
	Eligible      int                         `json:"eligible"`
	Skipped       int                         `json:"skipped"`
}

type ReviewDispatchBatchResult struct {
	Preview  ReviewDispatchPreview       `json:"preview"`
	Receipts []ContinuousDispatchReceipt `json:"receipts"`
}

const reviewDispatchPreviewSchema = "hivecrew.review-dispatch-preview/v1"

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
	page, err := s.inspector.InspectProject(ctx, workspaceID, projectID, limit, offset)
	if err != nil {
		return ReviewDispatchPreview{}, err
	}
	if page == nil || page.SchemaVersion != ContinuousDispatchShadowSchemaV1 ||
		page.WorkspaceID != shadowUUIDString(workspaceID) || page.ProjectID != shadowUUIDString(projectID) {
		return ReviewDispatchPreview{}, ErrContinuousDispatchSourceGap
	}
	preview := ReviewDispatchPreview{SchemaVersion: reviewDispatchPreviewSchema, Items: make([]ReviewDispatchPreviewItem, 0, len(page.Items))}
	for _, item := range page.Items {
		if item.Status != "in_review" {
			continue
		}
		proposal := ReviewDispatchPreviewItem{
			IssueID: item.IssueID, IssueTitle: item.IssueTitle, SourceTaskID: item.SourceTaskID,
			State: item.NextAction.State, Reasons: append([]continuousdispatch.Reason(nil), item.NextAction.Reasons...),
			Selected: item.NextAction.Selected,
		}
		if strings.TrimSpace(item.SourceTaskID) == "" && proposal.Selected != nil {
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
		note := fmt.Sprintf("review_dispatch source_issue_id=%s source_task_id=%s", item.IssueID, item.SourceTaskID)
		dispatch, dispatchErr := s.trigger.DispatchIssue(ctx, workspaceID, projectID, parseDispatchUUID(item.IssueID), actorUserID, note)
		if dispatchErr != nil {
			return result, dispatchErr
		}
		result.Receipts = append(result.Receipts, dispatch.Receipt)
	}
	return result, nil
}
