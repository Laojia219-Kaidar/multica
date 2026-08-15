-- HiveCrew storage-location ledger for a candidate replica. These rows are
-- operational placement metadata only; Artifact lifecycle and formal Outcome
-- authority remain owned by the existing candidate/event and external
-- authority contracts. Deliberately no foreign keys or cascades.
CREATE TABLE artifact_replica_location (
    id                UUID NOT NULL,
    workspace_id      UUID NOT NULL,
    outcome_id        UUID NOT NULL,
    candidate_id      UUID NOT NULL,
    candidate_revision INT NOT NULL CHECK (candidate_revision > 0),
    location_class    TEXT NOT NULL CHECK (location_class IN (
        'local-cache', 'nas-primary', 'offline-copy', 'cloud-replica'
    )),
    location_id       TEXT NOT NULL CHECK (btrim(location_id) <> ''),
    storage_id        TEXT NOT NULL CHECK (btrim(storage_id) <> ''),
    object_ref        TEXT NOT NULL CHECK (btrim(object_ref) <> ''),
    state             TEXT NOT NULL CHECK (state IN (
        'fixture', 'registered', 'pending', 'verified', 'failed'
    )),
    digest            TEXT NOT NULL DEFAULT '',
    metadata_digest   TEXT NOT NULL DEFAULT '',
    size_bytes        BIGINT NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    retention_hint    TEXT NOT NULL DEFAULT '',
    metadata          JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(metadata) = 'object'),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
