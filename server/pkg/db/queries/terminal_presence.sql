-- 372 terminal presence: host collector upsert + work wall read-back.

-- name: UpsertTerminalPresence :exec
INSERT INTO terminal_presence
    (workspace_id, host, session_name, window_index, pane_index, pane_pid,
     current_command, agent_hint, tail_text, heartbeat_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
ON CONFLICT (host, session_name, window_index, pane_index) DO UPDATE SET
    workspace_id = EXCLUDED.workspace_id,
    pane_pid = EXCLUDED.pane_pid,
    current_command = EXCLUDED.current_command,
    agent_hint = EXCLUDED.agent_hint,
    tail_text = EXCLUDED.tail_text,
    heartbeat_at = now();

-- name: ListFreshTerminalPresence :many
SELECT * FROM terminal_presence
WHERE workspace_id = $1
  AND heartbeat_at > now() - interval '15 minutes'
ORDER BY heartbeat_at DESC;
