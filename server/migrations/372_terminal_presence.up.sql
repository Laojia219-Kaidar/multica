-- 372_terminal_presence.up.sql — 员工 Terminal 现场只读投影。
-- 宿主采集器把每个 tmux pane 的尾部输出（脱敏后）写入此表；工作墙据此展示
-- "每位数字员工在 terminal 里此刻正在做什么"。行由 (host, session, window_index,
-- pane_index) 唯一标识，采集器每次心跳整行 upsert；超过 15 分钟未心跳的行由
-- 后端读取时过滤（视为过期现场），不删除历史以保留审计痕迹。
CREATE TABLE IF NOT EXISTS terminal_presence (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    host          TEXT NOT NULL,
    session_name  TEXT NOT NULL,
    window_index  INT  NOT NULL DEFAULT 0,
    pane_index    INT  NOT NULL DEFAULT 0,
    pane_pid      INT  NOT NULL DEFAULT 0,
    current_command TEXT NOT NULL DEFAULT '',
    agent_hint    TEXT NOT NULL DEFAULT '',
    tail_text     TEXT NOT NULL DEFAULT '',
    heartbeat_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (host, session_name, window_index, pane_index)
);
CREATE INDEX IF NOT EXISTS terminal_presence_workspace_heartbeat_idx
    ON terminal_presence (workspace_id, heartbeat_at DESC);
