package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/service"
)

// ReviewDrainConfig controls the batch-drain job's cadence and cap. BatchSize
// is the hard per-workspace-per-tick fan-out cap: the legacy in_review queue is
// drained in small deterministic batches, never all at once.
type ReviewDrainConfig struct {
	BatchSize        int32
	ClassifyEachTick bool
}

// ReviewDrainJob is the scheduler wrapper for the legacy in_review batch drain.
// Each tick classifies every workspace that has in_review issues (if enabled),
// then drains at most BatchSize directly-reviewable rows per workspace.
func ReviewDrainJob(pool *pgxpool.Pool, drainSvc *service.ReviewDrainService, cfg ReviewDrainConfig) JobSpec {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 20
	}
	return JobSpec{
		Name:              "review_drain",
		Cadence:           1 * time.Minute,
		ScheduleDelay:     30 * time.Second,
		CatchUpMode:       CatchUpLatestOnly,
		CatchUpWindow:     10 * time.Minute,
		MaxPlansPerTick:   1,
		RunTimeout:        5 * time.Minute,
		StaleTimeout:      15 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       3,
		RetryBackoff:      []time.Duration{30 * time.Second, 2 * time.Minute},
		Scopes: func(ctx context.Context, now time.Time) ([]Scope, error) {
			if drainSvc == nil || drainSvc.Queries == nil {
				return nil, nil
			}
			ids, err := drainSvc.Queries.ListWorkspacesWithInReviewIssues(ctx)
			if err != nil {
				return nil, fmt.Errorf("review drain: list workspaces: %w", err)
			}
			scopes := make([]Scope, 0, len(ids))
			for _, id := range ids {
				scopes = append(scopes, Scope{Kind: "workspace", ID: id.String()})
			}
			return scopes, nil
		},
		Handler: func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
			if drainSvc == nil {
				return HandlerResult{Result: map[string]any{"drained": 0, "skipped": 0}}, nil
			}
			var workspaceID pgtype.UUID
			if err := workspaceID.Scan(in.Scope.ID); err != nil {
				return HandlerResult{}, fmt.Errorf("review drain: parse workspace scope %q: %w", in.Scope.ID, err)
			}
			if cfg.ClassifyEachTick {
				if _, err := drainSvc.ClassifyInReview(ctx, workspaceID); err != nil {
					return HandlerResult{}, fmt.Errorf("review drain: classify: %w", err)
				}
			}
			receipt, err := drainSvc.DrainBatch(ctx, workspaceID, cfg.BatchSize)
			if err != nil {
				return HandlerResult{}, fmt.Errorf("review drain: batch: %w", err)
			}
			return HandlerResult{
				RowsAffected: int64(receipt.Processed + receipt.Skipped),
				Result: map[string]any{
					"drained": receipt.Processed,
					"skipped": receipt.Skipped,
					"batch":   receipt.BatchSize,
				},
			}, nil
		},
	}
}
