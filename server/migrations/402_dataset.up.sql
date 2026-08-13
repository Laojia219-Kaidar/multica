-- 402_dataset.up.sql — Dataset/Knowledge (local truth; World Library is the canonical knowledge authority, source_available_runtime_unavailable)
CREATE TABLE IF NOT EXISTS dataset (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    name text NOT NULL,
    domain text NOT NULL,
    version integer NOT NULL DEFAULT 1,
    authorized_agent_ids uuid[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS dataset_workspace_id_idx ON dataset (workspace_id);
