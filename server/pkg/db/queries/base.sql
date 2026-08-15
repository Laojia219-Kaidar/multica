-- name: ListBases :many
SELECT id, workspace_id, code, name, device, machine_title, created_at, updated_at
FROM base WHERE workspace_id = $1 ORDER BY code;

-- name: CountAgentsByBase :many
SELECT b.id AS base_id, count(a.id) AS agent_count
FROM base b
LEFT JOIN agent a ON a.home_base_id = b.id
WHERE b.workspace_id = $1
GROUP BY b.id
ORDER BY b.code;
