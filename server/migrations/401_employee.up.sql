-- 401_employee.up.sql — Employee identity (local truth; external HiveCosm authority is the canonical company truth, this is the HiveCrew execution projection)
CREATE TABLE IF NOT EXISTS employee (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    name text NOT NULL,
    position text,
    department text,
    agent_id uuid,
    status text NOT NULL DEFAULT 'draft',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS employee_workspace_id_idx ON employee (workspace_id);
