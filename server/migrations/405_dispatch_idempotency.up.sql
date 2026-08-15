-- Owner explicit dispatch idempotency (HIV-355).
--
-- Records the outcome of each idempotent POST /api/issues/{id}/dispatch call
-- so that replaying the same (workspace, idempotency_key) with the same
-- request digest returns the identical task IDs without creating a duplicate
-- run, while a different digest returns 409 Conflict.
--
-- Rows are immutable after insert: the dispatch handler never updates them.
-- A periodic cleanup can prune rows older than the retention window.
CREATE TABLE dispatch_idempotency (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_digest  TEXT NOT NULL,
    task_ids        UUID[] NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, idempotency_key)
);

-- Garbage-collection scan uses (workspace_id, created_at).
CREATE INDEX idx_dispatch_idempotency_workspace_created
    ON dispatch_idempotency (workspace_id, created_at);
