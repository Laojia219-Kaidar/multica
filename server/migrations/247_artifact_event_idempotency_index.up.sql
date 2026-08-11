CREATE UNIQUE INDEX CONCURRENTLY artifact_event_idempotency_uidx
    ON artifact_event (workspace_id, idempotency_key);

