package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Drain classification buckets for the legacy in_review queue. The drain job
// never instantiates a review task for anything but directly_reviewable, and it
// never fans out more than batch_size rows per tick.
const (
	DrainClassNoCandidate        = "no_candidate"
	DrainClassMissingEvidence    = "missing_evidence"
	DrainClassDirectlyReviewable = "directly_reviewable"
	DrainClassNeedsRepair        = "needs_repair"
	DrainClassSuperseded         = "superseded"
)

// DrainClassification is one classified in_review issue.
type DrainClassification struct {
	IssueID pgtype.UUID
	Class   string
	Reason  string
}

// DrainClassificationSummary aggregates a classification pass.
type DrainClassificationSummary struct {
	Total              int
	NoCandidate        int
	MissingEvidence    int
	DirectlyReviewable int
	NeedsRepair        int
	Superseded         int
}

// DrainBatchReceipt is the durable result of one drain batch.
type DrainBatchReceipt struct {
	BatchSize   int
	Processed   int
	Skipped     int
	ReviewTasks int
}

// ReviewDrainService classifies and batch-drains the legacy in_review queue.
// Classification is strictly read-only with respect to review tasks; only the
// bounded DrainBatch step creates them.
type ReviewDrainService struct {
	Queries    *db.Queries
	ReviewCell *ReviewCellService
}

func NewReviewDrainService(q *db.Queries, cell *ReviewCellService) *ReviewDrainService {
	return &ReviewDrainService{Queries: q, ReviewCell: cell}
}

// ClassifyInReview classifies every in_review issue in the workspace and
// upserts the result into review_drain_progress. Directly-reviewable rows stay
// pending; every other bucket is terminal (skipped/superseded) and will never
// create a review task.
func (s *ReviewDrainService) ClassifyInReview(ctx context.Context, workspaceID pgtype.UUID) (DrainClassificationSummary, error) {
	if s.Queries == nil {
		return DrainClassificationSummary{}, ErrCompanyOpsArtifactUnavailable
	}
	ids, err := s.Queries.ListInReviewIssueIDs(ctx, workspaceID)
	if err != nil {
		return DrainClassificationSummary{}, fmt.Errorf("list in_review issues: %w", err)
	}
	var summary DrainClassificationSummary
	summary.Total = len(ids)
	for _, id := range ids {
		class, reason := s.classifyIssue(ctx, workspaceID, id)
		status := "pending"
		if class != DrainClassDirectlyReviewable {
			status = "skipped"
			if class == DrainClassSuperseded {
				status = "superseded"
			}
		}
		now := time.Now().UTC()
		_, err := s.Queries.UpsertReviewDrainProgress(ctx, db.UpsertReviewDrainProgressParams{
			IssueID:        id,
			WorkspaceID:    workspaceID,
			Classification: class,
			Status:         status,
			Reason:         reason,
			ProcessedAt:    pgtype.Timestamptz{Time: now, Valid: true},
		})
		if err != nil {
			return summary, fmt.Errorf("persist drain classification: %w", err)
		}
		switch class {
		case DrainClassNoCandidate:
			summary.NoCandidate++
		case DrainClassMissingEvidence:
			summary.MissingEvidence++
		case DrainClassDirectlyReviewable:
			summary.DirectlyReviewable++
		case DrainClassNeedsRepair:
			summary.NeedsRepair++
		case DrainClassSuperseded:
			summary.Superseded++
		}
	}
	return summary, nil
}

// DrainBatch processes at most batchSize pending directly-reviewable rows. Each
// row is handed to the review cell (idempotent review-task creation); failures
// mark the row skipped with a stable reason instead of fanning out further.
func (s *ReviewDrainService) DrainBatch(ctx context.Context, workspaceID pgtype.UUID, batchSize int32) (DrainBatchReceipt, error) {
	if s.Queries == nil {
		return DrainBatchReceipt{}, ErrCompanyOpsArtifactUnavailable
	}
	if batchSize <= 0 {
		return DrainBatchReceipt{}, fmt.Errorf("drain batch size must be positive")
	}
	rows, err := s.Queries.ListPendingDrainProgress(ctx, db.ListPendingDrainProgressParams{
		WorkspaceID: workspaceID,
		Limit:       batchSize,
	})
	if err != nil {
		return DrainBatchReceipt{}, fmt.Errorf("list pending drain rows: %w", err)
	}
	var receipt DrainBatchReceipt
	receipt.BatchSize = len(rows)
	for _, row := range rows {
		if row.Classification != DrainClassDirectlyReviewable {
			continue
		}
		if s.ReviewCell == nil {
			s.markSkipped(ctx, row.IssueID, workspaceID, "review_cell_disabled")
			receipt.Skipped++
			continue
		}
		if err := s.ReviewCell.OnIssueEnteredReview(ctx, row.IssueID); err != nil {
			s.markSkipped(ctx, row.IssueID, workspaceID, "review_cell_error:"+err.Error())
			receipt.Skipped++
			continue
		}
		s.markProcessed(ctx, row.IssueID, workspaceID)
		receipt.Processed++
		receipt.ReviewTasks++
	}
	return receipt, nil
}

// classifyIssue resolves the classification bucket for one in_review issue
// without creating any review task.
func (s *ReviewDrainService) classifyIssue(ctx context.Context, workspaceID, issueID pgtype.UUID) (string, string) {
	comment, err := s.Queries.LatestLineageComment(ctx, issueID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !comment.SourceTaskID.Valid) {
		return DrainClassNoCandidate, "no delivery comment with source_task_id"
	}
	if err != nil {
		return DrainClassNoCandidate, "lineage lookup failed"
	}
	candidate, err := s.Queries.GetAgentTask(ctx, comment.SourceTaskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return DrainClassNoCandidate, "candidate task not found"
	}
	if err != nil {
		return DrainClassNoCandidate, "candidate task lookup failed"
	}
	if !uuidEqual(candidate.IssueID, issueID) {
		return DrainClassNoCandidate, "candidate belongs to another issue"
	}

	switch candidate.Status {
	case "completed":
		// A newer completed work delivery supersedes this candidate.
		superseded, reason := s.candidateSuperseded(ctx, issueID, candidate)
		if superseded {
			return DrainClassSuperseded, reason
		}
		return DrainClassDirectlyReviewable, "candidate completed"
	case "failed", "cancelled":
		return DrainClassNeedsRepair, "candidate " + candidate.Status
	default:
		return DrainClassMissingEvidence, "candidate not terminal (" + candidate.Status + ")"
	}
}

func (s *ReviewDrainService) candidateSuperseded(ctx context.Context, issueID pgtype.UUID, candidate db.AgentTaskQueue) (bool, string) {
	tasks, err := s.Queries.ListTasksByIssue(ctx, issueID)
	if err != nil {
		return false, ""
	}
	for _, t := range tasks {
		if t.TaskKind == TaskKindWork && t.Status == "completed" && !uuidEqual(t.ID, candidate.ID) && t.CreatedAt.Time.After(candidate.CreatedAt.Time) {
			return true, "a newer completed work delivery exists"
		}
	}
	return false, ""
}

func (s *ReviewDrainService) markProcessed(ctx context.Context, issueID, workspaceID pgtype.UUID) {
	now := time.Now().UTC()
	_, _ = s.Queries.UpsertReviewDrainProgress(ctx, db.UpsertReviewDrainProgressParams{
		IssueID:        issueID,
		WorkspaceID:    workspaceID,
		Classification: DrainClassDirectlyReviewable,
		Status:         "processed",
		Reason:         "",
		ProcessedAt:    pgtype.Timestamptz{Time: now, Valid: true},
	})
}

func (s *ReviewDrainService) markSkipped(ctx context.Context, issueID, workspaceID pgtype.UUID, reason string) {
	now := time.Now().UTC()
	_, _ = s.Queries.UpsertReviewDrainProgress(ctx, db.UpsertReviewDrainProgressParams{
		IssueID:        issueID,
		WorkspaceID:    workspaceID,
		Classification: DrainClassDirectlyReviewable,
		Status:         "skipped",
		Reason:         reason,
		ProcessedAt:    pgtype.Timestamptz{Time: now, Valid: true},
	})
}
