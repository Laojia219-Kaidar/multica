package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ProjectAutoStartService implements the bounded Project start/continue
// control slice (HIV-405). It composes the existing OwnerDispatchService
// (per-issue idempotent dispatch) with a dependency-ready wave query
// (ListProjectReadyIssues) — no second scheduler, no auto-deploy.
//
// Two commands:
//   - Preview: read-only, returns which issues are ready and why others aren't.
//   - Start:   idempotent batch dispatch over the ready wave. Each issue is
//     dispatched through OwnerDispatchService.Dispatch with a derived
//     idempotency key so repeat clicks replay safely.
type ProjectAutoStartService struct {
	Queries  *db.Queries
	Dispatch *OwnerDispatchService
}

// NewProjectAutoStartService constructs a ProjectAutoStartService.
func NewProjectAutoStartService(q *db.Queries, dispatch *OwnerDispatchService) *ProjectAutoStartService {
	return &ProjectAutoStartService{Queries: q, Dispatch: dispatch}
}

// ReadyIssue is one entry in the dependency-ready wave.
type ReadyIssue struct {
	IssueID       string `json:"issue_id"`
	Status        string `json:"status"`
	Priority      string `json:"priority"`
	Title         string `json:"title"`
	AssigneeType  string `json:"assignee_type"`
	AssigneeID    string `json:"assignee_id"`
	HasActiveTask bool   `json:"has_active_task"`
	ExistingTaskID string `json:"existing_task_id,omitempty"`
}

// BlockedIssue is one entry that is not ready, with a reason.
type BlockedIssue struct {
	IssueID string `json:"issue_id"`
	Status  string `json:"status"`
	Title   string `json:"title"`
	Reason  string `json:"reason"`
}

// PreviewResult is the read-only outcome of project start preview.
type ProjectAutoStartPreviewResult struct {
	ProjectID    string         `json:"project_id"`
	ReadyCount   int            `json:"ready_count"`
	Ready        []ReadyIssue   `json:"ready"`
	Blocked      []BlockedIssue `json:"blocked,omitempty"`
}

// Preview computes the dependency-ready wave for a project.
func (s *ProjectAutoStartService) Preview(ctx context.Context, workspaceID, projectID pgtype.UUID) (*ProjectAutoStartPreviewResult, error) {
	readyRows, err := s.Queries.ListProjectReadyIssues(ctx, db.ListProjectReadyIssuesParams{
		WorkspaceID: workspaceID,
		ProjectID:   projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("list ready issues: %w", err)
	}

	result := &ProjectAutoStartPreviewResult{
		ProjectID: util.UUIDToString(projectID),
	}

	activeTaskStatuses := map[string]bool{
		"queued": true, "dispatched": true, "running": true,
	}

	for _, row := range readyRows {
		ri := ReadyIssue{
			IssueID:      util.UUIDToString(row.IssueID),
			Status:       row.IssueStatus,
			Priority:     row.IssuePriority,
			Title:        row.IssueTitle,
			AssigneeType: row.IssueAssigneeType.String,
			AssigneeID:   util.UUIDToString(row.IssueAssigneeID),
		}
		if row.TaskID.Valid && activeTaskStatuses[row.TaskStatus] {
			ri.HasActiveTask = true
			ri.ExistingTaskID = util.UUIDToString(row.TaskID)
		}
		result.Ready = append(result.Ready, ri)
	}
	result.ReadyCount = len(result.Ready)

	return result, nil
}

// StartParams carries the inputs for the idempotent batch start.
type ProjectAutoStartParams struct {
	WorkspaceID    pgtype.UUID
	ProjectID      pgtype.UUID
	IdempotencyKey string
	RequestDigest  string
	ActorUserID    pgtype.UUID
	// SelectedIssueIDs limits the dispatch to specific issues. Empty = all ready.
	SelectedIssueIDs []pgtype.UUID
}

// IssueDispatchResult is the per-issue outcome within a batch start.
type IssueDispatchResult struct {
	IssueID  string   `json:"issue_id"`
	Decision string   `json:"decision"`
	Reason   string   `json:"reason,omitempty"`
	TaskIDs  []string `json:"task_ids,omitempty"`
	Replayed bool     `json:"replayed,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// ProjectAutoStartResult is the batch outcome.
type ProjectAutoStartResult struct {
	ProjectID      string                `json:"project_id"`
	IdempotencyKey string               `json:"idempotency_key"`
	Dispatched     int                   `json:"dispatched"`
	AlreadyActive  int                   `json:"already_active"`
	Blocked        int                   `json:"blocked"`
	Results        []IssueDispatchResult `json:"results"`
}

// Start performs the idempotent batch dispatch over the ready wave.
func (s *ProjectAutoStartService) Start(ctx context.Context, p ProjectAutoStartParams) (*ProjectAutoStartResult, error) {
	// 1. Compute the ready wave.
	readyRows, err := s.Queries.ListProjectReadyIssues(ctx, db.ListProjectReadyIssuesParams{
		WorkspaceID: p.WorkspaceID,
		ProjectID:   p.ProjectID,
	})
	if err != nil {
		return nil, fmt.Errorf("list ready issues: %w", err)
	}

	// 2. Filter by selection if provided.
	selectedSet := make(map[string]bool, len(p.SelectedIssueIDs))
	for _, id := range p.SelectedIssueIDs {
		selectedSet[util.UUIDToString(id)] = true
	}

	result := &ProjectAutoStartResult{
		ProjectID:      util.UUIDToString(p.ProjectID),
		IdempotencyKey: p.IdempotencyKey,
	}

	for idx, row := range readyRows {
		issueIDStr := util.UUIDToString(row.IssueID)
		if len(selectedSet) > 0 && !selectedSet[issueIDStr] {
			continue
		}

		// Build a per-issue idempotency key derived from the batch key.
		perIssueKey := fmt.Sprintf("%s:%s:%d", p.IdempotencyKey, issueIDStr, idx)

		// Load the full issue for the dispatch service.
		issue, err := s.Queries.GetIssue(ctx, row.IssueID)
		if err != nil {
			result.Results = append(result.Results, IssueDispatchResult{
				IssueID:  issueIDStr,
				Decision: string(DecisionBlocked),
				Error:    "issue lookup failed: " + err.Error(),
			})
			result.Blocked++
			continue
		}

		// Load active tasks for this issue.
		activeTasks, _ := s.Queries.ListActiveTasksByIssue(ctx, issue.ID)

		// Dispatch through the existing OwnerDispatchService.
		dr, err := s.Dispatch.Dispatch(ctx, DispatchParams{
			Issue:          issue,
			WorkspaceID:    p.WorkspaceID,
			IdempotencyKey: perIssueKey,
			RequestDigest:  p.RequestDigest,
			ActiveTasks:    activeTasks,
			ActorUserID:    p.ActorUserID,
		})

		idr := IssueDispatchResult{
			IssueID: issueIDStr,
		}
		if err != nil {
			if errors.Is(err, ErrIdempotencyConflict) {
				idr.Decision = string(DecisionBlocked)
				idr.Reason = "idempotency_conflict"
				result.Blocked++
			} else if errors.Is(err, ErrExpectedStateMismatch) {
				idr.Decision = string(DecisionBlocked)
				idr.Reason = "state_mismatch"
				result.Blocked++
			} else {
				idr.Decision = string(DecisionBlocked)
				idr.Error = err.Error()
				result.Blocked++
			}
		} else {
			idr.Decision = string(dr.Decision)
			idr.Reason = string(dr.Reason)
			idr.TaskIDs = dr.TaskIDs
			idr.Replayed = dr.Replayed
			switch dr.Decision {
			case DecisionWouldEnqueue:
				result.Dispatched++
			case DecisionAlreadyActive:
				result.AlreadyActive++
			default:
				result.Blocked++
			}
		}
		result.Results = append(result.Results, idr)
	}

	slog.Info("project auto-start completed",
		"project_id", result.ProjectID,
		"dispatched", result.Dispatched,
		"already_active", result.AlreadyActive,
		"blocked", result.Blocked,
	)

	return result, nil
}
