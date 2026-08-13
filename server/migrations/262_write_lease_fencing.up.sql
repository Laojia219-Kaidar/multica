-- Single Writer Lease with fencing generation (HIV-410, PROVISIONAL — DO NOT APPLY).
--
-- This migration is intentionally provisional and unapplied. Integration
-- ordering with the existing agent_task_queue prepare-lease (migration 124)
-- and the webhook_delivery lease-token pattern (server/pkg/db/queries/
-- webhook_delivery.sql) is unresolved: the decision of whether the write
-- lease is a standalone table (this migration) or columns bolted onto
-- agent_task_queue is deferred to the integration review.
--
-- Migration conflict note: if this table is eventually applied, verify that
-- no concurrent shard has already created a write_lease or writer_lease
-- table, and that migration 262 has not been claimed by another candidate.
-- The fence_generation counter design assumes exactly one row per mutex_key,
-- enforced by the UNIQUE constraint below.
--
-- Design:
--   mutex_key        Unique identifier for the protected resource
--                    (e.g. 'task:{task_id}:write', 'worktree:{branch}').
--   lease_token      Random UUID generated on each successful acquire.
--                    Required on renew/release for compare-and-swap.
--   fence_generation Monotonically increasing BIGINT. Bumped on every
--                    acquire (including crash-recovery reacquire). Stale
--                    writers presenting an older generation are rejected.
--   expires_at       When the lease becomes stale and eligible for sweep.
--                    NULL when the lease is free.
--   status           'free' | 'held' | 'expired'
--                    expired = lease lapsed but row retained for audit
--                    before the next acquire resets it to 'held'.

CREATE TABLE write_lease (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    mutex_key         TEXT NOT NULL UNIQUE,
    holder_id         TEXT,
    lease_token       UUID,
    fence_generation  BIGINT NOT NULL DEFAULT 0,
    status            TEXT NOT NULL DEFAULT 'free'
                      CHECK (status IN ('free', 'held', 'expired')),
    acquired_at       TIMESTAMPTZ,
    expires_at        TIMESTAMPTZ,
    renewed_count     INT NOT NULL DEFAULT 0,
    released_at       TIMESTAMPTZ,
    last_cancel_reason TEXT,
    last_cancelled_at  TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Sweep query scans by (status, expires_at) — index supports both the
-- expired-lease sweeper and the readback-by-key path.
CREATE INDEX idx_write_lease_status_expires
    ON write_lease (status, expires_at);
