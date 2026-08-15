package scheduler

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WriteLeaseExpiryJob reclaims canonical_write_lease rows whose holder stopped
// heartbeating. The cadence is shorter than the lease TTL floor (the handler
// itself is TTL-agnostic: it expires every held row whose expires_at has
// passed, so it can never expire a live, heartbeating holder early).
func WriteLeaseExpiryJob(pool *pgxpool.Pool) JobSpec {
	store := NewWriteLeaseStore(pool)
	return JobSpec{
		Name:              "canonical_write_lease_expiry",
		Cadence:           15 * time.Second,
		ScheduleDelay:     0,
		CatchUpMode:       CatchUpLatestOnly,
		CatchUpWindow:     time.Minute,
		MaxPlansPerTick:   1,
		RunTimeout:        30 * time.Second,
		StaleTimeout:      2 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       3,
		RetryBackoff:      []time.Duration{5 * time.Second, 30 * time.Second},
		Scopes:            StaticScopes(ScopeGlobal),
		Handler: func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
			now, err := dbNow(ctx, pool)
			if err != nil {
				return HandlerResult{}, err
			}
			reclaimed, err := store.Expire(ctx, now)
			if err != nil {
				return HandlerResult{}, err
			}
			return HandlerResult{
				RowsAffected: reclaimed,
				Result:       map[string]any{"expired": reclaimed},
			}, nil
		},
	}
}

// WriteLeaseCleanupJob deletes terminal (released/expired) canonical_write_lease
// rows after a grace period. Cancel cleanup must never run before a holder has
// had a chance to observe its release, so the grace period is generous.
func WriteLeaseCleanupJob(pool *pgxpool.Pool, grace time.Duration) JobSpec {
	if grace <= 0 {
		grace = time.Hour
	}
	store := NewWriteLeaseStore(pool)
	return JobSpec{
		Name:              "canonical_write_lease_cancel_cleanup",
		Cadence:           5 * time.Minute,
		ScheduleDelay:     0,
		CatchUpMode:       CatchUpLatestOnly,
		CatchUpWindow:     time.Hour,
		MaxPlansPerTick:   1,
		RunTimeout:        2 * time.Minute,
		StaleTimeout:      10 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       3,
		RetryBackoff:      []time.Duration{30 * time.Second, 2 * time.Minute},
		Scopes:            StaticScopes(ScopeGlobal),
		Handler: func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
			now, err := dbNow(ctx, pool)
			if err != nil {
				return HandlerResult{}, err
			}
			removed, err := store.CleanupCancelled(ctx, now, grace)
			if err != nil {
				return HandlerResult{}, err
			}
			return HandlerResult{
				RowsAffected: removed,
				Result:       map[string]any{"removed": removed},
			}, nil
		},
	}
}
