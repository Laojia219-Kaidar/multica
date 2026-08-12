-- ReviewPipelineV2 (HIV-350): close the waiting_local_directory gap in the
-- open-review-task idempotency key created by 258. The daemon parks a claimed
-- review task in waiting_local_directory while its workdir is prepared, and
-- 258's unique index only covered ('queued','dispatched','running') — so a
-- duplicate EventIssueUpdated delivery could create a SECOND open review task
-- for the same (issue, candidate) once the first had left 'queued'. This index
-- covers the same four in-flight statuses every open-review query uses
-- (GetOpenReviewTaskForIssue / CompleteReviewTask / CancelOpenReviewTasksForIssue
-- / CountOpenReviewTasks / the WIP claim subquery), so the unique key and the
-- query status sets can never drift apart again.
--
-- CONCURRENTLY per repo rule: agent_task_queue is hot, so the build must not
-- take an ACCESS EXCLUSIVE lock; single-statement file per the migration
-- runner's CONCURRENTLY constraint. 258's index is deliberately left in place
-- (it is subsumed by this one) so there is never a window without uniqueness
-- protection; it can be dropped in a consolidation pass before release.
CREATE UNIQUE INDEX CONCURRENTLY idx_agent_task_review_open_unique_v2
    ON agent_task_queue (issue_id, review_target_task_id)
    WHERE task_kind = 'review' AND status IN ('queued', 'dispatched', 'running', 'waiting_local_directory');
