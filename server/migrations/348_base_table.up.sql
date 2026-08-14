-- 348_base_table.up.sql — 基地迁移决策 A: base 从 custom_env 非正式存储升级为正式表 + agent 外键。
CREATE TABLE IF NOT EXISTS base (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    code text NOT NULL,
    name text NOT NULL,
    device text NOT NULL,
    machine_title text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS base_workspace_code_key ON base (workspace_id, code);
CREATE UNIQUE INDEX IF NOT EXISTS base_workspace_machine_key ON base (workspace_id, machine_title);
ALTER TABLE agent ADD COLUMN IF NOT EXISTS home_base_id uuid REFERENCES base(id);
ALTER TABLE agent ADD COLUMN IF NOT EXISTS fallback_base_id uuid REFERENCES base(id);
