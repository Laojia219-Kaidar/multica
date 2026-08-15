-- Operating programs are HiveCrew's workspace-scoped workflow organization
-- layer. They group existing Projects without copying Project lifecycle truth.
-- Relationships are intentionally application-validated; this repository
-- forbids database foreign keys and cascades for governed cross-domain data.
CREATE TABLE workflow_operating_program (
    id               UUID NOT NULL,
    workspace_id     UUID NOT NULL,
    name             TEXT NOT NULL CHECK (btrim(name) <> ''),
    description      TEXT NOT NULL DEFAULT '',
    idempotency_key  TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workflow_operating_program_project (
    program_id       UUID NOT NULL,
    workspace_id     UUID NOT NULL,
    project_id       UUID NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
