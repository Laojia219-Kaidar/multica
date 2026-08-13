-- =====================
-- Write Lease (Single Writer + Fencing Generation)
-- =====================
-- HIV-410 provisional candidate. Queries mirror the webhook_delivery
-- lease-token + fence pattern but add a monotonically increasing
-- fence_generation for stale-writer rejection.
--
-- These queries are sqlc-annotated for future code generation once the
-- migration is promoted from provisional. The Go service layer
-- (write_lease.go) uses the same SQL via raw pgx until sqlc regeneration
-- is wired.

-- name: AcquireWriteLease :one
-- Atomic acquire via UPSERT. If the row is new, fence_generation starts
-- at 1. If the row exists and is 'free' or 'expired', the generation
-- increments and a fresh lease_token is issued. A currently 'held' and
-- unexpired lease causes the UPDATE to match zero rows (returning NULL),
-- which the caller interprets as "lease busy".
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

-- name: RenewWriteLease :one
-- Extends the lease expiry. Requires both the correct lease_token AND the
-- matching fence_generation — a stale writer whose generation lags is
-- rejected (zero rows). renewed_count tracks heartbeat ticks for
-- diagnostics.
UPDATE write_lease
SET expires_at    = now() + make_interval(secs => $3::double precision),
    renewed_count = renewed_count + 1,
    updated_at    = now()
WHERE mutex_key        = $1
  AND lease_token      = $2
  AND fence_generation = $4
  AND status           = 'held'
RETURNING *;

-- name: ReleaseWriteLease :one
-- Marks the lease as free. Requires token + generation match for fencing.
-- Generation is NOT incremented on release — only on the next acquire.
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

-- name: ForceCancelWriteLease :one
-- Admin/recovery path: forcibly revokes a held lease (e.g. after crash
-- detection or manual cancel). Bumps fence_generation so the deposed
-- holder's token becomes permanently invalid. Does NOT require the old
-- token — the caller must be privileged (owner/admin sweeper).
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

-- name: SweepExpiredWriteLeases :many
-- Marks held leases whose expiry has passed as 'expired'. Does NOT bump
-- fence_generation here — that happens on the next AcquireWriteLease.
-- Returns the swept rows so the caller can log or trigger recovery.
UPDATE write_lease
SET status     = 'expired',
    updated_at = now()
WHERE status     = 'held'
  AND expires_at IS NOT NULL
  AND expires_at < now()
RETURNING *;

-- name: ReadWriteLease :one
-- Readback of the current lease state by mutex_key.
SELECT * FROM write_lease
WHERE mutex_key = $1;

-- name: ReadWriteLeaseByToken :one
-- Readback for a holder verifying it still owns the lease.
SELECT * FROM write_lease
WHERE mutex_key        = $1
  AND lease_token      = $2
  AND fence_generation = $3
  AND status           = 'held';
