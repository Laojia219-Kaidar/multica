CREATE UNIQUE INDEX CONCURRENTLY artifact_materialization_intent_key_uidx
    ON artifact_materialization_intent (workspace_id, storage_key);

