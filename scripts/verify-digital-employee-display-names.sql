-- Read-only guard: scoped workforce rows must not carry multiple_active_conflict
-- after display-name sync. Run against HiveCosm adapter DB or via:
--   node scripts/sync-digital-employee-display-names.mjs verify
--
-- When executed against HiveCrew local DB this query is a no-op placeholder;
-- binding_state lives in HiveCosm read models, not HiveCrew Postgres.

-- Local agent name spot-check (post migration 260):
SELECT
    a.id::text AS agent_id,
    a.name AS local_agent_name,
    a.description,
    a.archived_at IS NOT NULL AS archived
FROM agent a
WHERE a.id IN (
    '7f1d98a5-307f-40d8-893b-82d9fe09f33e'::uuid,
    '876e2514-ee7c-4fc3-85f2-861baa464a45'::uuid,
    'c5ab7e4f-8c34-4fee-90e7-634d42c4edee'::uuid,
    'bf6f658f-b8d6-45d8-8f57-e9444ce9b4e4'::uuid,
    '708a49ba-7e7d-4363-8b9b-e6a4aeb5d980'::uuid,
    'bfea5417-eb5e-4e67-99a8-5609824c663d'::uuid,
    '3f6fb05c-cf49-4446-8906-13e1f17794a3'::uuid
)
ORDER BY a.name;

-- Workspace name uniqueness among the seven (should return zero rows):
SELECT workspace_id, name, count(*) AS duplicates
FROM agent
WHERE id IN (
    '7f1d98a5-307f-40d8-893b-82d9fe09f33e'::uuid,
    '876e2514-ee7c-4fc3-85f2-861baa464a45'::uuid,
    'c5ab7e4f-8c34-4fee-90e7-634d42c4edee'::uuid,
    'bf6f658f-b8d6-45d8-8f57-e9444ce9b4e4'::uuid,
    '708a49ba-7e7d-4363-8b9b-e6a4aeb5d980'::uuid,
    'bfea5417-eb5e-4e67-99a8-5609824c663d'::uuid,
    '3f6fb05c-cf49-4446-8906-13e1f17794a3'::uuid
)
GROUP BY workspace_id, name
HAVING count(*) > 1;
