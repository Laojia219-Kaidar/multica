-- Lane B (P2): canonical repo/worktree write lease.
--
-- A single-writer lease over a named scope (e.g. a canonical integration
-- worktree or a published repo path). Exactly one holder owns the lease at a
-- time; every acquire/steal bumps `generation` so a crashed holder can never
-- cancel or heartbeat a newer holder's lease. `expires_at` bounds how long a
-- silent holder keeps the lease; the scheduler expiry job flips stale rows to
-- 'expired' and the cancel-cleanup job removes terminal rows after a grace
-- period. Heartbeats extend `expires_at` under a generation CAS.
CREATE TABLE canonical_write_lease (
    scope        TEXT PRIMARY KEY,
    holder_id    TEXT NOT NULL,
    generation   BIGINT NOT NULL DEFAULT 0,
    status       TEXT NOT NULL DEFAULT 'held'
                 CHECK (status IN ('held', 'cancelling', 'released', 'expired')),
    reason       TEXT NOT NULL DEFAULT '',
    heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_canonical_write_lease_expiry
    ON canonical_write_lease (status, expires_at);
