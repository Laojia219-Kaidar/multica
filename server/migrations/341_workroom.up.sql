-- 341_workroom.up.sql — QM Workroom (collaboration context, NOT a second Project/Task truth source)
CREATE TABLE IF NOT EXISTS workroom (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    name text NOT NULL,
    project_id uuid,
    issue_id uuid,
    work_order_id text,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS workroom_workspace_id_idx ON workroom (workspace_id);
