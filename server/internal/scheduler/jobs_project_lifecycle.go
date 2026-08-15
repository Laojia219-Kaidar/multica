package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// JobNameProjectLifecycleReconciler is the canonical audit-row name.
const JobNameProjectLifecycleReconciler = "project_lifecycle_reconciler"

// ProjectLifecycleReconcilerJob returns the periodic VC-12 self-operation job:
// every cycle it diagnoses the four broken-chain detectors across workspaces
// and creates one dedup'd traceable Issue per finding (idempotent, append-only).
func ProjectLifecycleReconcilerJob(pool *pgxpool.Pool, queries *db.Queries, issueSvc *service.IssueService) JobSpec {
	return JobSpec{
		Name:              JobNameProjectLifecycleReconciler,
		Cadence:           15 * time.Minute,
		ScheduleDelay:     1 * time.Minute,
		CatchUpMode:       CatchUpLatestOnly,
		CatchUpWindow:     24 * time.Hour,
		RunTimeout:        10 * time.Minute,
		StaleTimeout:      15 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       2,
		RetryBackoff:      []time.Duration{1 * time.Minute},
		Scopes:            StaticScopes(ScopeGlobal),
		Handler:           makeProjectLifecycleReconcilerHandler(pool, queries, issueSvc),
	}
}

func makeProjectLifecycleReconcilerHandler(pool *pgxpool.Pool, queries *db.Queries, issueSvc *service.IssueService) Handler {
	return func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
		if issueSvc == nil {
			return HandlerResult{}, fmt.Errorf("issue service is required")
		}
		rows, err := pool.Query(ctx, `
			SELECT w.id, COALESCE(m.user_id, '00000000-0000-0000-0000-000000000000'::uuid)
			FROM workspace w
			LEFT JOIN member m ON m.workspace_id = w.id AND m.role = 'owner'
			ORDER BY w.created_at`)
		if err != nil {
			return HandlerResult{}, fmt.Errorf("list workspaces: %w", err)
		}
		defer rows.Close()

		reconciler := service.NewProjectLifecycleReconciler(queries)
		var total int64
		for rows.Next() {
			var wsID, ownerID pgtype.UUID
			if err := rows.Scan(&wsID, &ownerID); err != nil {
				return HandlerResult{}, fmt.Errorf("scan workspace: %w", err)
			}
			n, err := reconciler.ReconcileWorkspace(ctx, wsID, issueSvc, "member", ownerID)
			if err != nil {
				return HandlerResult{}, fmt.Errorf("reconcile workspace: %w", err)
			}
			total += int64(n)
		}
		return HandlerResult{RowsAffected: total}, nil
	}
}
