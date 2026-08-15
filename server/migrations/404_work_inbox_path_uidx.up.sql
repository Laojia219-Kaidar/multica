-- Idempotent unregistered-work inbox upsert key: one row per (workspace_id, path).
-- Nullable path (non-worktree entries) remains unconstrained (Postgres treats
-- NULLs as distinct in unique indexes).
CREATE UNIQUE INDEX work_inbox_path_uidx ON work_inbox (workspace_id, path);
