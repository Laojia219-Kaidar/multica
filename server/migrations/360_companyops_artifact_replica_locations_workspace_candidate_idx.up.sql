CREATE INDEX CONCURRENTLY IF NOT EXISTS artifact_replica_location_workspace_candidate_idx
    ON artifact_replica_location (workspace_id, candidate_id, created_at DESC, id);
