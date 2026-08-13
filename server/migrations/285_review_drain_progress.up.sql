-- Lane B (P2): batch drain progress for the legacy in_review queue.
--
-- One row per classified in_review issue. The drain job advances at most
-- `batch_size` rows per tick and records each classification + terminal
-- disposition here so a crash resumes without re-fanning-out the whole queue
-- and without re-creating already-created review tasks (the review task
-- idempotency index is the second guard).
CREATE TABLE review_drain_progress (
    issue_id       UUID PRIMARY KEY,
    workspace_id   UUID NOT NULL,
    classification TEXT NOT NULL
                   CHECK (classification IN (
                       'no_candidate',
                       'missing_evidence',
                       'directly_reviewable',
                       'needs_repair',
                       'superseded'
                   )),
    status         TEXT NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending', 'processed', 'skipped', 'superseded')),
    reason         TEXT NOT NULL DEFAULT '',
    review_task_id UUID,
    processed_at   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_review_drain_progress_status
    ON review_drain_progress (workspace_id, status);
