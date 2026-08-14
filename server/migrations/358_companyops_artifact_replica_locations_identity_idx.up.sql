CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS artifact_replica_location_identity_idx
    ON artifact_replica_location (
        workspace_id, outcome_id, candidate_id, location_class, location_id
    );
