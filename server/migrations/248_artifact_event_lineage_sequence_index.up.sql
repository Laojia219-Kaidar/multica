CREATE UNIQUE INDEX CONCURRENTLY artifact_event_lineage_sequence_uidx
    ON artifact_event (workspace_id, lineage_id, sequence);

