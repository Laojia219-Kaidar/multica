package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Single Writer Lease with fencing generation (HIV-410)
// -------------------------------------------------------------------
//
// This service implements a canonical write lease backed by a Postgres row
// per mutex_key. Each successful acquire issues a fresh lease_token (UUID)
// and bumps a monotonically increasing fence_generation. Renew, release,
// and readback all compare-and-swap against (lease_token, fence_generation),
// so a stale writer whose lease was revoked (by expiry + reacquire, or by
// force-cancel) is permanently rejected.
//
// The design mirrors two proven in-repo patterns:
//
//   - agent_task_queue.prepare_lease_expires_at (migration 124): an expiry
//     timestamp that a daemon heartbeat extends, with reclaim eligibility
//     when the timestamp lapses.
//
//   - webhook_delivery.lease_token (webhook_delivery.sql): a UUID token
//     generated on claim and checked on every subsequent mutation.
//
// The fencing generation is the addition that closes the gap neither
// existing pattern covers: even if a stale writer observes its lease_token
// is still "valid" from its perspective, a newer generation proves a
// different writer now holds the lease.
//
// Schema authority belongs exclusively to migration 260. This service never
// creates or alters its own table; an unapplied migration fails loudly.

// DefaultLeaseDuration is the standard lease TTL when none is specified.
const DefaultLeaseDuration = 45 * time.Second

// WriteLeaseStatus enumerates the lifecycle states of a lease row.
type WriteLeaseStatus string

const (
	WriteLeaseFree    WriteLeaseStatus = "free"
	WriteLeaseHeld    WriteLeaseStatus = "held"
	WriteLeaseExpired WriteLeaseStatus = "expired"
)

// WriteLease is the in-memory representation of a write_lease row.
type WriteLease struct {
	ID                uuid.UUID
	MutexKey          string
	HolderID          string
	LeaseToken        uuid.UUID
	FenceGeneration   int64
	Status            WriteLeaseStatus
	AcquiredAt        time.Time
	ExpiresAt         *time.Time
	RenewedCount      int32
	ReleasedAt        *time.Time
	LastCancelReason  *string
	LastCancelledAt   *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ErrLeaseBusy is returned when a lease cannot be acquired because another
// unexpired holder owns it.
var ErrLeaseBusy = errors.New("write lease: mutex key is held by another writer")

// ErrLeaseNotHeld is returned when a renew/release targets a lease that the
// caller does not currently own (wrong token, wrong generation, or the row
// was already released/expired).
var ErrLeaseNotHeld = errors.New("write lease: caller does not hold this lease")

// ErrLeaseNotFound is returned when a readback targets a mutex_key that has
// no lease row.
var ErrLeaseNotFound = errors.New("write lease: no lease row for mutex key")

// ---------------------------------------------------------------------------
// SQL constants are kept local until the service is wired into a shared DB
// query surface. Schema ownership remains exclusively in migration 260.
// ---------------------------------------------------------------------------

const acquireWriteLeaseSQL = `
INSERT INTO write_lease (
    mutex_key, holder_id, lease_token, fence_generation,
    status, acquired_at, expires_at, renewed_count,
    released_at, last_cancel_reason, last_cancelled_at
)
VALUES (
    $1, $2, gen_random_uuid(), 1,
    'held', now(), now() + make_interval(secs => $3::double precision),
    0, NULL, NULL, NULL
)
ON CONFLICT (mutex_key) DO UPDATE
SET holder_id           = EXCLUDED.holder_id,
    lease_token         = gen_random_uuid(),
    fence_generation    = write_lease.fence_generation + 1,
    status              = 'held',
    acquired_at         = now(),
    expires_at          = now() + make_interval(secs => $3::double precision),
    renewed_count       = 0,
    released_at         = NULL,
    last_cancel_reason  = NULL,
    last_cancelled_at   = NULL,
    updated_at          = now()
WHERE write_lease.status IN ('free', 'expired')
   OR (write_lease.status = 'held' AND write_lease.expires_at IS NOT NULL
       AND write_lease.expires_at < now())
RETURNING *;
`

const renewWriteLeaseSQL = `
UPDATE write_lease
SET expires_at    = now() + make_interval(secs => $3::double precision),
    renewed_count = renewed_count + 1,
    updated_at    = now()
WHERE mutex_key        = $1
  AND lease_token      = $2
  AND fence_generation = $4
  AND status           = 'held'
RETURNING *;
`

const releaseWriteLeaseSQL = `
UPDATE write_lease
SET status           = 'free',
    holder_id        = NULL,
    lease_token      = NULL,
    expires_at       = NULL,
    released_at      = now(),
    updated_at       = now()
WHERE mutex_key        = $1
  AND lease_token      = $2
  AND fence_generation = $3
  AND status           = 'held'
RETURNING *;
`

const forceCancelWriteLeaseSQL = `
UPDATE write_lease
SET status             = 'expired',
    holder_id          = NULL,
    lease_token        = NULL,
    expires_at         = NULL,
    fence_generation   = fence_generation + 1,
    last_cancel_reason = $2,
    last_cancelled_at  = now(),
    updated_at         = now()
WHERE mutex_key  = $1
  AND status     = 'held'
RETURNING *;
`

const sweepExpiredWriteLeasesSQL = `
UPDATE write_lease
SET status     = 'expired',
    updated_at = now()
WHERE status     = 'held'
  AND expires_at IS NOT NULL
  AND expires_at < now()
RETURNING *;
`

const readWriteLeaseSQL = `
SELECT id, mutex_key, holder_id, lease_token, fence_generation,
       status, acquired_at, expires_at, renewed_count,
       released_at, last_cancel_reason, last_cancelled_at,
       created_at, updated_at
FROM write_lease
WHERE mutex_key = $1;
`

const readWriteLeaseByTokenSQL = `
SELECT id, mutex_key, holder_id, lease_token, fence_generation,
       status, acquired_at, expires_at, renewed_count,
       released_at, last_cancel_reason, last_cancelled_at,
       created_at, updated_at
FROM write_lease
WHERE mutex_key        = $1
  AND lease_token      = $2
  AND fence_generation = $3
  AND status           = 'held';
`

// ---------------------------------------------------------------------------
// Service
// ---------------------------------------------------------------------------

// WriteLeaseService manages single-writer leases backed by Postgres.
type WriteLeaseService struct {
	pool *pgxpool.Pool
}

// NewWriteLeaseService creates a service bound to the given connection pool.
func NewWriteLeaseService(pool *pgxpool.Pool) *WriteLeaseService {
	return &WriteLeaseService{pool: pool}
}

// Acquire attempts to obtain a write lease on the given mutex_key.
//
// On success the caller receives the lease including lease_token and
// fence_generation, which it must persist and present on subsequent
// Renew / Release calls.
//
// Returns ErrLeaseBusy if the mutex is currently held and unexpired.
func (s *WriteLeaseService) Acquire(ctx context.Context, mutexKey, holderID string, ttl time.Duration) (*WriteLease, error) {
	if ttl <= 0 {
		ttl = DefaultLeaseDuration
	}

	row := s.pool.QueryRow(ctx, acquireWriteLeaseSQL,
		mutexKey, holderID, ttl.Seconds())

	lease, err := scanWriteLease(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrLeaseBusy
		}
		return nil, fmt.Errorf("acquire write lease: %w", err)
	}
	return lease, nil
}

// Renew extends the lease expiry. The caller must present the lease_token
// and fence_generation obtained from Acquire (or a prior Renew).
//
// Returns ErrLeaseNotHeld if the caller no longer owns the lease.
func (s *WriteLeaseService) Renew(ctx context.Context, mutexKey string, token uuid.UUID, generation int64, ttl time.Duration) (*WriteLease, error) {
	if ttl <= 0 {
		ttl = DefaultLeaseDuration
	}

	row := s.pool.QueryRow(ctx, renewWriteLeaseSQL,
		mutexKey, token, ttl.Seconds(), generation)

	lease, err := scanWriteLease(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrLeaseNotHeld
		}
		return nil, fmt.Errorf("renew write lease: %w", err)
	}
	return lease, nil
}

// Release frees the lease, making the mutex immediately available to
// other writers. Requires token + generation match.
//
// Returns ErrLeaseNotHeld if the caller does not own the lease.
func (s *WriteLeaseService) Release(ctx context.Context, mutexKey string, token uuid.UUID, generation int64) (*WriteLease, error) {
	row := s.pool.QueryRow(ctx, releaseWriteLeaseSQL,
		mutexKey, token, generation)

	lease, err := scanWriteLease(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrLeaseNotHeld
		}
		return nil, fmt.Errorf("release write lease: %w", err)
	}
	return lease, nil
}

// ForceCancel revokes a held lease without requiring the holder's token.
// The fence_generation is bumped, permanently invalidating the old
// holder's token. Intended for admin/recovery paths (crash detection,
// manual override).
//
// Returns ErrLeaseNotHeld if the lease is not currently held.
func (s *WriteLeaseService) ForceCancel(ctx context.Context, mutexKey, reason string) (*WriteLease, error) {
	row := s.pool.QueryRow(ctx, forceCancelWriteLeaseSQL,
		mutexKey, reason)

	lease, err := scanWriteLease(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrLeaseNotHeld
		}
		return nil, fmt.Errorf("force cancel write lease: %w", err)
	}
	return lease, nil
}

// SweepExpired marks all held leases whose expiry has passed as 'expired'.
// Returns the swept leases for diagnostics/recovery. The fence_generation
// is NOT bumped here — the next Acquire on that mutex_key bumps it.
func (s *WriteLeaseService) SweepExpired(ctx context.Context) ([]WriteLease, error) {
	rows, err := s.pool.Query(ctx, sweepExpiredWriteLeasesSQL)
	if err != nil {
		return nil, fmt.Errorf("sweep expired write leases: %w", err)
	}
	defer rows.Close()

	var result []WriteLease
	for rows.Next() {
		lease, err := scanWriteLease(rows)
		if err != nil {
			return nil, fmt.Errorf("scan swept lease: %w", err)
		}
		result = append(result, *lease)
	}
	return result, rows.Err()
}

// Read returns the current lease state for the given mutex_key.
// Returns ErrLeaseNotFound if no row exists.
func (s *WriteLeaseService) Read(ctx context.Context, mutexKey string) (*WriteLease, error) {
	row := s.pool.QueryRow(ctx, readWriteLeaseSQL, mutexKey)
	lease, err := scanWriteLease(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrLeaseNotFound
		}
		return nil, fmt.Errorf("read write lease: %w", err)
	}
	return lease, nil
}

// VerifyHeld checks whether the caller (identified by token + generation)
// still owns the lease. Returns the lease if so, ErrLeaseNotHeld otherwise.
func (s *WriteLeaseService) VerifyHeld(ctx context.Context, mutexKey string, token uuid.UUID, generation int64) (*WriteLease, error) {
	row := s.pool.QueryRow(ctx, readWriteLeaseByTokenSQL, mutexKey, token, generation)
	lease, err := scanWriteLease(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrLeaseNotHeld
		}
		return nil, fmt.Errorf("verify held write lease: %w", err)
	}
	return lease, nil
}

// ---------------------------------------------------------------------------
// Row scanning
// ---------------------------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

func scanWriteLease(row rowScanner) (*WriteLease, error) {
	var wl WriteLease
	var status string
	var holderID *string
	var leaseToken *uuid.UUID
	var acquiredAt *time.Time
	var expiresAt *time.Time
	var releasedAt *time.Time
	var lastCancelReason *string
	var lastCancelledAt *time.Time

	if err := row.Scan(
		&wl.ID,
		&wl.MutexKey,
		&holderID,
		&leaseToken,
		&wl.FenceGeneration,
		&status,
		&acquiredAt,
		&expiresAt,
		&wl.RenewedCount,
		&releasedAt,
		&lastCancelReason,
		&lastCancelledAt,
		&wl.CreatedAt,
		&wl.UpdatedAt,
	); err != nil {
		return nil, err
	}

	wl.Status = WriteLeaseStatus(status)
	if holderID != nil {
		wl.HolderID = *holderID
	}
	if leaseToken != nil {
		wl.LeaseToken = *leaseToken
	}
	if acquiredAt != nil {
		wl.AcquiredAt = *acquiredAt
	}
	wl.ExpiresAt = expiresAt
	wl.ReleasedAt = releasedAt
	wl.LastCancelReason = lastCancelReason
	wl.LastCancelledAt = lastCancelledAt

	return &wl, nil
}
