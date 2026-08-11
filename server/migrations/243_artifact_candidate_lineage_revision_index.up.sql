CREATE UNIQUE INDEX CONCURRENTLY artifact_candidate_lineage_revision_uidx
    ON artifact_candidate (workspace_id, lineage_id, revision);

