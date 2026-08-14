-- Migration 263: immutable idempotency receipt for one exact continuous-dispatch generation.
--
-- This is execution evidence, not a second Issue, Project, employee, or
-- workflow registry. The canonical identity is deliberately represented as
-- five separate columns so database uniqueness cannot be weakened by a
-- caller-chosen key or an opaque JSON digest.

CREATE TABLE continuous_dispatch_receipt (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id         UUID NOT NULL,
    issue_id             UUID NOT NULL,
    stage                TEXT NOT NULL,
    candidate_revision   TEXT NOT NULL,
    generation           TEXT NOT NULL,
    task_id              UUID NOT NULL UNIQUE,
    employee_ref         TEXT NOT NULL,
    local_agent_id       UUID NOT NULL,
    runtime_id           UUID NOT NULL,
    model                TEXT NOT NULL,
    account_ref          TEXT NOT NULL,
    request_digest       TEXT NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        btrim(stage) <> '' AND
        btrim(candidate_revision) <> '' AND
        btrim(generation) <> '' AND
        btrim(employee_ref) <> '' AND
        btrim(model) <> '' AND
        btrim(account_ref) <> ''
    ),
    UNIQUE (workspace_id, issue_id, stage, candidate_revision, generation)
);

CREATE INDEX idx_continuous_dispatch_receipt_issue_created
    ON continuous_dispatch_receipt (workspace_id, issue_id, created_at DESC);
