CREATE INDEX CONCURRENTLY IF NOT EXISTS artifact_event_approval_candidate_idx
    ON artifact_event (workspace_id, lineage_id, candidate_id, sequence DESC)
    WHERE event_type IN ('approved', 'changes_requested', 'rejected', 'approval_revoked');
