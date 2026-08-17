-- name: InsertWriterLeaseCompletionReceipt :one
INSERT INTO writer_lease_completion_receipt (
    workspace_id, task_id, target_digest, proof_snapshot,
    proof_digest, receipt_digest
) VALUES (
    @workspace_id, @task_id, @target_digest, @proof_snapshot,
    @proof_digest, @receipt_digest
)
ON CONFLICT (workspace_id, task_id) DO NOTHING
RETURNING *;

-- name: GetWriterLeaseCompletionReceipt :one
SELECT * FROM writer_lease_completion_receipt
WHERE workspace_id = @workspace_id AND task_id = @task_id;
