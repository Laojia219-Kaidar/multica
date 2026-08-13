-- 342_workflow_memory_persistence.up.sql — W4 workflow + employee memory candidate persistence.
-- HiveCrew owns workflow orchestration state and memory CANDIDATES only. Formal
-- company knowledge / playbook / skill remains the HiveCosm authority; these
-- tables never become a second source of that truth.

CREATE TABLE IF NOT EXISTS workflow_definition (
    id         text PRIMARY KEY,
    version    integer NOT NULL CHECK (version > 0),
    risk       text NOT NULL CHECK (risk IN ('fast', 'standard', 'owner')),
    stages     jsonb NOT NULL DEFAULT '[]',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS workflow_instance (
    id                 text PRIMARY KEY,
    definition_id      text NOT NULL,
    definition_version integer NOT NULL,
    context            jsonb NOT NULL DEFAULT '{}',
    stage_index        integer NOT NULL DEFAULT 0,
    status             text NOT NULL DEFAULT 'running'
                       CHECK (status IN ('running', 'paused', 'stopped', 'completed', 'failed')),
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS workflow_instance_status_idx ON workflow_instance (status);

CREATE TABLE IF NOT EXISTS workflow_event (
    id              bigserial PRIMARY KEY,
    instance_id     text NOT NULL,
    kind            text NOT NULL,
    source_ref      text NOT NULL DEFAULT '',
    actor           text NOT NULL DEFAULT '',
    occurred_at     timestamptz NOT NULL,
    observed_at     timestamptz NOT NULL,
    idempotency_key text NOT NULL,
    UNIQUE (instance_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS workflow_event_instance_idx ON workflow_event (instance_id);

CREATE TABLE IF NOT EXISTS memory_candidate (
    id          text PRIMARY KEY,
    employee_id text NOT NULL,
    position_id text NOT NULL DEFAULT '',
    kind        text NOT NULL CHECK (kind IN ('episodic', 'experience')),
    content     text NOT NULL,
    evidence    jsonb NOT NULL DEFAULT '[]',
    source_refs jsonb NOT NULL DEFAULT '[]',
    author_id   text NOT NULL,
    status      text NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'validated', 'rejected', 'promoted', 'revoked')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS memory_candidate_employee_idx ON memory_candidate (employee_id);
CREATE INDEX IF NOT EXISTS memory_candidate_position_idx ON memory_candidate (position_id);

CREATE TABLE IF NOT EXISTS memory_promotion (
    id           bigserial PRIMARY KEY,
    candidate_id text NOT NULL,
    target       text NOT NULL CHECK (target IN ('employee_memory', 'team_playbook', 'skill')),
    reviewer_id  text NOT NULL,
    approved     boolean NOT NULL,
    reason       text NOT NULL DEFAULT '',
    promoted_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS memory_promotion_candidate_idx ON memory_promotion (candidate_id);

CREATE TABLE IF NOT EXISTS memory_revocation (
    id           bigserial PRIMARY KEY,
    candidate_id text NOT NULL,
    reviewer_id  text NOT NULL,
    reason       text NOT NULL DEFAULT '',
    revoked_at   timestamptz NOT NULL DEFAULT now()
);
