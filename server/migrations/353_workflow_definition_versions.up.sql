-- Candidate-only immutable published workflow definition versions.
-- HiveCrew owns this execution configuration; it does not copy project,
-- employee, task, run, outcome, or platform publication authority.
CREATE TABLE IF NOT EXISTS workflow_definition_version (
    definition_id  text NOT NULL,
    workspace_id   uuid NOT NULL,
    version        integer NOT NULL CHECK (version > 0),
    risk           text NOT NULL CHECK (risk IN ('fast', 'standard', 'owner')),
    stages         jsonb NOT NULL DEFAULT '[]',
    graph          jsonb NOT NULL,
    digest         text NOT NULL,
    idempotency_key text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    published_at   timestamptz NOT NULL DEFAULT now()
);
