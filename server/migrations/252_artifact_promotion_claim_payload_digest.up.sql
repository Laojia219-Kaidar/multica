-- Forward upgrade from the already-applied 251 schema. Existing claim rows
-- intentionally remain NULL because their original authority payload cannot be
-- reconstructed safely; the application treats them as unverifiable conflicts.
ALTER TABLE artifact_promotion_claim
    ADD COLUMN payload_digest TEXT;
