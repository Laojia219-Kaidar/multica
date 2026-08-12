-- ReviewPipelineV2 (HIV-326 contract C4): review/repair task kinds on the
-- existing task queue — no second task truth source is created.
--
-- task_kind: 'work' is the default and preserves every existing insert's
-- behavior; 'review' marks a review task whose agent_id carries the reviewer;
-- 'repair' marks a rework delivery round.
--
-- review_target_task_id pins the exact candidate task under review (the
-- delivery comment's source_task_id). The CHECK makes NULL bypass
-- structurally impossible for review rows: a task_kind='review' row MUST carry
-- a candidate reference, so the partial unique index below can never see NULL
-- columns (Postgres treats NULLs as distinct in unique indexes — this CHECK
-- closes that hole, per HIV-323 blocker B1).
ALTER TABLE agent_task_queue ADD COLUMN task_kind TEXT NOT NULL DEFAULT 'work';
ALTER TABLE agent_task_queue ADD CONSTRAINT agent_task_queue_task_kind_closed_enum
    CHECK (task_kind IN ('work', 'review', 'repair'));

ALTER TABLE agent_task_queue ADD COLUMN review_target_task_id UUID NULL;

ALTER TABLE agent_task_queue ADD CONSTRAINT agent_task_review_target_required
    CHECK (task_kind <> 'review' OR review_target_task_id IS NOT NULL);
