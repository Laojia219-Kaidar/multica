#!/usr/bin/env python3
"""terminal-presence-collector — 宿主 Terminal 现场采集器（只读投影）。

每 10 秒抓取本机所有 tmux pane 的尾部输出（默认 25 行），做控制字符与
敏感信息脱敏后上报 HiveCrew `/api/work-wall/terminal-presence`。工作墙
"Terminal 现场"区据此展示每位数字员工此刻在 terminal 里实际做什么。

安全边界：
- 只读 tmux capture-pane，不发送按键、不创建会话。
- 采集前脱敏：控制序列丢弃、常见 secret 形状（sk-/gla_/mul_/AKIA/Bearer）
  替换为 [REDACTED]；服务端入库前做第二遍 sanitize。
- PAT 从 ~/.multica/config.json 读取，不落日志、不入库。
- 15 分钟无心跳的 pane 由服务端判定过期，不在此删除。

用法：nohup python3 terminal-presence-collector.sh.py >/tmp/terminal-presence.log 2>&1 &
"""
import json
import os
import re
import shutil
import signal
import socket
import subprocess
import sys
import time
import urllib.request

INTERVAL = int(os.environ.get("TERMINAL_PRESENCE_INTERVAL", "10"))
TAIL_LINES = int(os.environ.get("TERMINAL_PRESENCE_TAIL_LINES", "25"))
API = os.environ.get("TERMINAL_PRESENCE_API", "http://127.0.0.1:8080/api/work-wall/terminal-presence")
# workspace 中间件从 X-Workspace-Slug header 或 workspace_slug query 参数解析工作区，
# 不读 JSON body，所以这里用 query 参数带上。
WORKSPACE_SLUG = os.environ.get("TERMINAL_PRESENCE_WORKSPACE", "hivecosm")
CONFIG = os.environ.get("TERMINAL_PRESENCE_CONFIG", os.path.expanduser("~/.multica/config.json"))

SECRET_PATTERNS = [
    re.compile(r"\b(sk[-_][A-Za-z0-9_\-]{8,})"),
    re.compile(r"\b(gla[-_][A-Za-z0-9_\-]{8,})"),
    re.compile(r"\b((?:mul|mdt|mat)[-_][A-Za-z0-9_\-]{8,})"),
    re.compile(r"\b(AKIA[0-9A-Z]{16})"),
    re.compile(r"(bearer\s+)[A-Za-z0-9._\-]{16,}", re.IGNORECASE),
    re.compile(r"(?i)(password|passwd|secret|token)\s*[=:]\s*\S+"),
]

ANSI_RE = re.compile(r"\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))")

def _find_tmux() -> str:
    """launchd 环境的 PATH 只有系统目录，homebrew 的 tmux 需要显式解析。"""
    candidates = [shutil.which("tmux"),
                  "/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"]
    for c in candidates:
        if c and os.access(c, os.X_OK):
            return c
    return "tmux"

TMUX_BIN = _find_tmux()

def sanitize(text: str) -> str:
    text = ANSI_RE.sub("", text)
    for pat in SECRET_PATTERNS:
        text = pat.sub("[REDACTED]", text)
    # 控制字符（保留换行/制表）
    text = "".join(ch if ch in "\n\t" or ord(ch) >= 0x20 else "" for ch in text)
    return text[-20000:]

KNOWN_CARRIERS = (
    "codex", "zcode", "opencode", "claude", "cursor", "qoder",
    "kimi", "nova", "qwen", "glm", "hermes", "prime",
)

KNOWN_EMPLOYEES = (
    "kai", "raven", "atlas", "pixel", "gauss",
    "michael", "prism", "coco", "emory", "william",
)

EMPLOYEE_SESSION_MARKERS = {
    "api", "backend", "codex", "dev", "dgx", "frontend", "glm", "hermes",
    "kimi", "opencode", "orchestrator", "prime", "qwen", "review", "run",
    "task", "test", "work",
}

ISSUE_ID_RE = re.compile(r"\b(HIV|MUL|HDEO)-(\d{2,6})\b", re.IGNORECASE)

def _tokens(text: str) -> list[str]:
    return [token for token in re.split(r"[^a-z0-9]+", text.lower()) if token]

def _has_token(text: str, value: str) -> bool:
    return value in _tokens(text)

def detect_carrier(session: str, cmd: str, tail: str) -> str:
    """Identify a carrier from delimiter-bounded tokens, never substrings."""
    for text in (session, cmd, tail[-3000:]):
        for carrier in KNOWN_CARRIERS:
            if _has_token(text, carrier):
                return carrier
    return ""

def detect_employee(session: str) -> str:
    """Infer an employee only from a strict, auditable tmux naming shape."""
    tokens = _tokens(session)
    if len(tokens) >= 2 and tokens[0] == "agent" and tokens[1] in KNOWN_EMPLOYEES:
        return tokens[1]
    if len(tokens) >= 2 and tokens[0] in KNOWN_EMPLOYEES:
        if any(token in EMPLOYEE_SESSION_MARKERS for token in tokens[1:]):
            return tokens[0]
    return ""

def detect_task_clue(session: str, tail: str) -> str:
    """Find only admitted HiveCrew/HiveCosm issue prefixes."""
    for text in (session, tail[-3000:]):
        match = ISSUE_ID_RE.search(text)
        if match:
            return f"{match.group(1).upper()}-{match.group(2)}"
    return ""

def build_agent_hint(session: str, cmd: str, tail: str) -> str:
    """Compose a bounded hint; this remains a display clue, not identity truth."""
    parts = []
    carrier = detect_carrier(session, cmd, tail)
    if carrier:
        parts.append(f"carrier={carrier}")
    employee = detect_employee(session)
    if employee:
        parts.append(f"emp={employee}")
    task = detect_task_clue(session, tail)
    if task:
        parts.append(f"task={task}")
    return "|".join(parts)[:120]

def agent_hint(session: str, cmd: str, tail: str) -> str:
    """Backward-compatible entrypoint for the collector pane shape."""
    return build_agent_hint(session, cmd, tail)

def collect():
    try:
        fmt = subprocess.run(
            [TMUX_BIN, "list-panes", "-a", "-F",
             "#{session_name}\t#{window_index}\t#{pane_index}\t#{pane_pid}\t#{pane_current_command}"],
            capture_output=True, text=True, timeout=5,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return None
    panes = []
    for line in fmt.stdout.splitlines():
        parts = line.split("\t")
        if len(parts) != 5:
            continue
        session, win, pane, pid, cmd = parts
        try:
            cap = subprocess.run(
                [TMUX_BIN, "capture-pane", "-p", "-t", f"{session}:{win}.{pane}", "-S", f"-{TAIL_LINES}"],
                capture_output=True, text=True, timeout=5,
            )
            tail = cap.stdout
        except subprocess.TimeoutExpired:
            tail = ""
        panes.append({
            "session_name": session,
            "window_index": int(win),
            "pane_index": int(pane),
            "pane_pid": int(pid),
            "current_command": sanitize(cmd)[:120],
            "agent_hint": agent_hint(session, cmd, tail)[:120],
            "tail_text": sanitize(tail),
        })
    return panes

def report(panes):
    try:
        cfg = json.load(open(CONFIG))
        token = cfg.get("token", "")
    except (OSError, json.JSONDecodeError):
        return False
    body = json.dumps({
        "workspace_slug": WORKSPACE_SLUG,
        "host": socket.gethostname(),
        "sessions": panes,
    }).encode()
    sep = "&" if "?" in API else "?"
    url = f"{API}{sep}workspace_slug={WORKSPACE_SLUG}"
    req = urllib.request.Request(url, data=body, method="POST",
                                 headers={"Content-Type": "application/json",
                                          "X-Workspace-Slug": WORKSPACE_SLUG,
                                          "Authorization": f"Bearer {token}"})
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return resp.status == 200
    except OSError as e:
        print(f"report failed: {e}", file=sys.stderr)
        return False

def main():
    running = True
    def stop(*_):
        nonlocal running
        running = False
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    print(f"terminal-presence-collector started interval={INTERVAL}s api={API}")
    while running:
        panes = collect()
        if panes is not None:
            report(panes)
        time.sleep(INTERVAL)
    print("collector stopped")

if __name__ == "__main__":
    main()
