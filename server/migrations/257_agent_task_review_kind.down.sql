ALTER TABLE agent_task_queue DROP CONSTRAINT IF EXISTS agent_task_review_target_required;
ALTER TABLE agent_task_queue DROP COLUMN IF EXISTS review_target_task_id;
ALTER TABLE agent_task_queue DROP CONSTRAINT IF EXISTS agent_task_queue_task_kind_closed_enum;
ALTER TABLE agent_task_queue DROP COLUMN IF EXISTS task_kind;
