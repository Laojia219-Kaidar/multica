-- Lane B (HIVE-PRIME-PORTFOLIO-CONVERGENCE P2): the review acceptance axis.
--
-- issue.review_state is the review-cell-owned acceptance substate, written
-- exclusively by the review cell (listener + verdict/requeue handlers).
-- Ordinary agents never write it; the CLI/daemon delivery flow only moves
-- issue.status (the delivery axis). NULL means "not in review" (the default
-- for every pre-existing row). Closed enum enforced by CHECK.
--
-- review_state_reason carries a stable machine-readable escalation reason
-- (e.g. "missing_candidate_lineage/no_source_task_id") while the issue sits in
-- owner_decision, so the review queue can render a reason badge without
-- inventing a second truth source in issue.metadata.
ALTER TABLE issue ADD COLUMN review_state TEXT NULL;
ALTER TABLE issue ADD COLUMN review_state_reason TEXT NULL;

ALTER TABLE issue ADD CONSTRAINT issue_review_state_closed_enum
    CHECK (review_state IS NULL OR review_state IN (
        'queued',
        'triaging',
        'evidence_review',
        'revise_requested',
        'owner_decision',
        'accepted',
        'superseded',
        'archived_history'
    ));
