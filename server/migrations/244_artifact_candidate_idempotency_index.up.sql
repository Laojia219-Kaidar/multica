CREATE UNIQUE INDEX CONCURRENTLY artifact_candidate_idempotency_uidx
    ON artifact_candidate (workspace_id, idempotency_key);

