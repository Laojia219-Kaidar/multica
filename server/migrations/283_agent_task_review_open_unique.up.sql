-- Lane B (P2): idempotency key for review-task creation. At most ONE open
-- review task per (issue, candidate) — concurrent EventIssueUpdated deliveries,
-- bus at-least-once redelivery, and double consumer races all resolve into a
-- single no-op on the second insert.
--
-- Because of 282's CHECK (task_kind='review' => review_target_task_id NOT
-- NULL), the indexed columns can never be NULL, so Postgres's "NULLs are
-- distinct" semantics cannot let a malformed row slip through.
CREATE UNIQUE INDEX CONCURRENTLY idx_agent_task_review_open_unique
    ON agent_task_queue (issue_id, review_target_task_id)
    WHERE task_kind = 'review' AND status IN ('queued', 'dispatched', 'running', 'waiting_local_directory');
