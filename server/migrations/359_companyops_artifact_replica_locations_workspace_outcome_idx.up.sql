CREATE INDEX CONCURRENTLY IF NOT EXISTS artifact_replica_location_workspace_outcome_idx
    ON artifact_replica_location (workspace_id, outcome_id, created_at DESC, id);
