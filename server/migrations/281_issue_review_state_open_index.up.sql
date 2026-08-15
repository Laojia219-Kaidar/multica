-- Lane B (P2): partial index backing the review-queue projection. Only open
-- states ever appear in the queue view; terminal states (accepted/superseded/
-- archived_history) and the pre-review NULL default are excluded. CONCURRENTLY
-- per repo rule: this index build runs on a live issue table without blocking
-- writes.
CREATE INDEX CONCURRENTLY idx_issue_review_state_open
    ON issue (review_state)
    WHERE review_state IN ('queued', 'triaging', 'evidence_review', 'owner_decision');
