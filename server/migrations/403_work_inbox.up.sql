-- Universal Work Registration unregistered-work inbox (VC-05). Unclaimed
-- actions discovered by the reconcile source; attach/ignore moves them out.
CREATE TABLE work_inbox (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    work_ref     text,
    path         text,
    branch       text,
    head         text,
    reason       text,
    state        text NOT NULL DEFAULT 'unclaimed',
    project_id   uuid,
    issue_id     uuid,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_inbox_state_check CHECK (state IN ('unclaimed','attached','ignored'))
);
CREATE INDEX work_inbox_workspace_state_idx ON work_inbox (workspace_id, state);
