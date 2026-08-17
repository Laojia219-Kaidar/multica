CREATE UNIQUE INDEX CONCURRENTLY writer_lease_completion_receipt_task_uidx
    ON writer_lease_completion_receipt (workspace_id, task_id);
