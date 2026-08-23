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

可选 Prime 观测（opt-in，只读投影）：
- 仅当非机密配置 TERMINAL_PRESENCE_PRIME_SESSIONS 明确列出短会话 id 时，
  才以有界、超时保护的方式读取官方 `prime-agent list --json`；
  缺省为空 = 从不调用 Prime CLI。
  TERMINAL_PRESENCE_PRIME_BIN 提供可执行文件绝对路径覆盖（launchd 的
  PATH 只有系统目录）；TERMINAL_PRESENCE_PRIME_TIMEOUT 秒数被钳制在
  [1, 30]，非法值回落到默认 5。
- 官方 live JSON 可能携带完整 id（activeSessionId/sessionId/id 任一字段），
  配置值是短显示 id。匹配规则：精确优先（exact-first），否则小写后的完整
  id 以某个配置短 id 结尾即视为候选；仅当该解析在整个 payload 中唯一匹配
  一个会话时才采用，歧义（精确或后缀）不匹配任何会话（fail closed，不猜
  测），且同一会话绝不投影两次。activeSessionId 推导活跃同样要求全
  payload 唯一，歧义短后缀不把任何会话标为活跃。
- 只投影短会话 id、model id、cwd、taskState、workerState、isSessionActive、
  isRunningTools、isStreaming、unfinishedActionCount、lastActivityAt；
  绝不投影 prompt、summary、firstMessage、诊断、端点/配置、会话文件、
  环境、工具参数、terminal 尾部、凭据或思维链。Prime pane 的 tail_text
  恒为空，agent_hint 标注 carrier=prime|non-authoritative，不声明员工身份。
- Fail open：CLI 缺失/启动失败/超时/非零退出（即使 stdout 是合法 JSON）/
  超大输出/坏 UTF-8/非法 payload 一律产出零个 Prime pane、不记录任何
  原始 JSON，且不压制有效的 tmux pane。

用法：nohup python3 terminal-presence-collector.sh.py >/tmp/terminal-presence.log 2>&1 &
"""
import json
import math
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

# --- Prime observation (opt-in, read-only projection) ----------------------

PRIME_BIN_ENV = "TERMINAL_PRESENCE_PRIME_BIN"
PRIME_SESSIONS_ENV = "TERMINAL_PRESENCE_PRIME_SESSIONS"
PRIME_TIMEOUT_ENV = "TERMINAL_PRESENCE_PRIME_TIMEOUT"
PRIME_TIMEOUT_DEFAULT = 5.0
PRIME_TIMEOUT_MIN = 1.0
PRIME_TIMEOUT_MAX = 30.0
# Bounded read: larger payloads are treated as malformed and dropped.
PRIME_OUTPUT_MAX_BYTES = 1_000_000
# Short display ids: ASCII alphanumerics plus '-'/'_', 4-64 chars,
# alphanumeric edges. Anything else is rejected, never widened.
PRIME_ID_RE = re.compile(r"^[a-z0-9][a-z0-9_-]{2,62}[a-z0-9]$")
PRIME_PANE_PREFIX = "prime-"
PRIME_STATE_MAX = 12
PRIME_TIMESTAMP_MAX = 20
PRIME_PENDING_MAX = 999999
# current_command carries "<model> <cwd> <lastActivityAt>"; the caps keep
# the joined value inside the pane shape's 120-char bound (40+57+20+2).
PRIME_MODEL_MAX = 40
PRIME_CWD_MAX = 57

def normalize_prime_session_id(raw):
    """Normalize one configured Prime session id; "" when invalid."""
    if not isinstance(raw, str):
        return ""
    value = raw.strip().lower()
    if not PRIME_ID_RE.fullmatch(value):
        return ""
    return value

def configured_prime_session_ids(environ=None):
    """Parse the non-secret TERMINAL_PRESENCE_PRIME_SESSIONS allowlist.

    Comma/space separated short display ids. Empty/unset means no Prime
    observation at all. Invalid entries are dropped, duplicates collapse,
    order is preserved.
    """
    env = os.environ if environ is None else environ
    ids = []
    for part in re.split(r"[,\s]+", env.get(PRIME_SESSIONS_ENV, "")):
        normalized = normalize_prime_session_id(part)
        if normalized and normalized not in ids:
            ids.append(normalized)
    return ids

def prime_binary_candidates(environ=None):
    """Ordered executable candidates: env override, PATH, common locations.

    TERMINAL_PRESENCE_PRIME_BIN is the absolute override for macOS launchd,
    whose PATH only contains system directories.
    """
    env = os.environ if environ is None else environ
    candidates = []
    override = env.get(PRIME_BIN_ENV, "").strip()
    if override:
        candidates.append(override)
    which = shutil.which("prime-agent", path=env.get("PATH", os.defpath))
    if which:
        candidates.append(which)
    candidates.extend([
        "/opt/homebrew/bin/prime-agent",
        "/usr/local/bin/prime-agent",
        os.path.expanduser("~/.local/bin/prime-agent"),
    ])
    return candidates

def find_prime_binary(environ=None):
    """First existing executable candidate, or None (callers fail open)."""
    for candidate in prime_binary_candidates(environ):
        if candidate and os.path.isfile(candidate) and os.access(candidate, os.X_OK):
            return candidate
    return None

def prime_timeout_seconds(environ=None):
    """Bounded Prime CLI timeout; invalid config falls back to the default."""
    env = os.environ if environ is None else environ
    try:
        value = float(env.get(PRIME_TIMEOUT_ENV, ""))
    except ValueError:
        return PRIME_TIMEOUT_DEFAULT
    if math.isnan(value):
        return PRIME_TIMEOUT_DEFAULT
    return max(PRIME_TIMEOUT_MIN, min(PRIME_TIMEOUT_MAX, value))

def run_prime_cli(binary, timeout_seconds):
    """Run `<binary> list --json`; return (returncode, stdout_bytes)."""
    proc = subprocess.run(
        [binary, "list", "--json"],
        capture_output=True,
        timeout=timeout_seconds,
    )
    return proc.returncode, proc.stdout

def parse_prime_payload(text):
    """Parse official list JSON into (sessions, active_session_id).

    Accepts a bare session list or an object wrapping the list under
    "sessions" (with optional top-level "activeSessionId"). Returns
    (None, None) for anything else so callers fail open.
    """
    try:
        payload = json.loads(text)
    except ValueError:
        return None, None
    active = None
    if isinstance(payload, dict):
        raw_active = payload.get("activeSessionId")
        if isinstance(raw_active, str) and raw_active.strip():
            active = raw_active.strip().lower()
        sessions = payload.get("sessions")
    else:
        sessions = payload
    if not isinstance(sessions, list):
        return None, None
    return [entry for entry in sessions if isinstance(entry, dict)], active

def prime_session_ids(session):
    """Normalized official id fields of one session, priority ordered.

    Official live entries carry `activeSessionId` and `id` (the live agent
    id) alongside a separate persisted `sessionId`; inactive entries carry
    `id`/`sessionId` only. A configured short id is matched against any of
    the three fields, lowercased and deduplicated, empties dropped.
    """
    ids = []
    for key in ("activeSessionId", "sessionId", "id"):
        value = session.get(key)
        if not isinstance(value, str):
            continue
        normalized = value.strip().lower()
        if normalized and normalized not in ids:
            ids.append(normalized)
    return ids

def unique_prime_session_index(id_lists, short_id):
    """Resolve one short id against every session's ids; fail closed.

    Exact-first: a configured id equal to some session's full id field wins
    over any longer suffix collision. Failing that, only a suffix match
    that is unambiguous across the entire payload qualifies. Zero matches,
    ambiguous exact matches or ambiguous suffixes all return None — never
    guess.
    """
    exact = [i for i, ids in enumerate(id_lists) if short_id in ids]
    if len(exact) == 1:
        return exact[0]
    if exact:
        return None
    suffix = [
        i for i, ids in enumerate(id_lists)
        if any(value.endswith(short_id) for value in ids)
    ]
    if len(suffix) == 1:
        return suffix[0]
    return None

def match_prime_sessions(sessions, configured):
    """Apply the documented exact-first, unambiguous suffix rule.

    Returns an ordered {dedup_id: (session, short_id)} map keyed by the
    session's first normalized id field, so one official session is never
    projected twice even if several configured ids resolve to it.
    """
    id_lists = [prime_session_ids(entry) for entry in sessions]
    matched = {}
    for short_id in configured:
        index = unique_prime_session_index(id_lists, short_id)
        if index is None:
            continue
        dedup_id = id_lists[index][0]
        if dedup_id not in matched:
            matched[dedup_id] = (sessions[index], short_id)
    return matched

def prime_session_is_active(session, sessions, id_lists, active_session_id):
    """Activity from the official bool or activeSessionId; never widened.

    The official isSessionActive bool needs no resolution. An
    activeSessionId — top-level or the session's own — resolves through
    the same exact-first, unambiguous-suffix rule applied across the
    entire payload, and only activates the session it uniquely resolves
    to. An ambiguous short suffix marks no session active.
    """
    if session.get("isSessionActive") is True:
        return True
    for candidate in (active_session_id, session.get("activeSessionId")):
        if not (isinstance(candidate, str) and candidate.strip()):
            continue
        normalized = candidate.strip().lower()
        index = unique_prime_session_index(id_lists, normalized)
        if index is not None and sessions[index] is session:
            return True
    return False

def _prime_clean_str(session, keys, limit):
    for key in keys:
        value = session.get(key)
        if isinstance(value, str) and value.strip():
            return sanitize(value.strip())[:limit]
    return ""

def prime_model_id(session):
    """Bounded model id for projection; "" when no admitted shape exists.

    Official live `model` is an object carrying `provider` and `id`; only
    its `id` string is ever read — provider and every other object field
    stay unread. Legacy string shapes stay supported in order: a direct
    string `modelId`, then a direct string `model`.
    """
    for key in ("modelId", "model"):
        value = session.get(key)
        if isinstance(value, str) and value.strip():
            return value.strip()
    model = session.get("model")
    if isinstance(model, dict):
        value = model.get("id")
        if isinstance(value, str) and value.strip():
            return value.strip()
    return ""

def _prime_flag(value):
    return "1" if value is True else "0"

def project_prime_session(session, short_id, active):
    """Project one whitelisted, bounded pane in the existing tmux shape.

    Projected: short session id, model id, cwd, taskState, workerState,
    isSessionActive, isRunningTools, isStreaming, unfinishedActionCount and
    lastActivityAt — nothing else ever reaches the pane shape. Field layout:
    session_name carries the short id, current_command carries
    "<model> <cwd> <lastActivityAt>", agent_hint carries the bounded state
    flags. tail_text is always empty; agent_hint stays a non-authoritative
    clue with no Employee claim.
    """
    model = sanitize(prime_model_id(session))[:PRIME_MODEL_MAX]
    cwd = _prime_clean_str(session, ("cwd", "workingDirectory"), PRIME_CWD_MAX)
    task_state = _prime_clean_str(session, ("taskState",), PRIME_STATE_MAX)
    worker_state = _prime_clean_str(session, ("workerState",), PRIME_STATE_MAX)
    last_activity = _prime_clean_str(session, ("lastActivityAt",), PRIME_TIMESTAMP_MAX)
    pending = session.get("unfinishedActionCount")
    if isinstance(pending, bool) or not isinstance(pending, int):
        pending = 0
    pending = max(0, min(PRIME_PENDING_MAX, pending))
    hint = "|".join([
        "carrier=prime",
        "non-authoritative",
        f"task={task_state}",
        f"worker={worker_state}",
        f"active={_prime_flag(active)}",
        f"tools={_prime_flag(session.get('isRunningTools'))}",
        f"stream={_prime_flag(session.get('isStreaming'))}",
        f"pending={pending}",
    ])[:120]
    return {
        "session_name": (PRIME_PANE_PREFIX + short_id)[:255],
        "window_index": 0,
        "pane_index": 0,
        "pane_pid": 0,
        "current_command": sanitize(f"{model} {cwd} {last_activity}".strip())[:120],
        "agent_hint": hint,
        "tail_text": "",
    }

def collect_prime_sessions(environ=None):
    """Collect Prime panes; every failure mode yields [] (fail open).

    Missing/broken CLI, spawn error, timeout, non-zero exit (even with
    valid JSON on stdout), oversized output, bad UTF-8, malformed JSON or
    unexpected payload shape all produce zero Prime panes and log no raw
    JSON.
    """
    configured = configured_prime_session_ids(environ)
    if not configured:
        return []
    binary = find_prime_binary(environ)
    if binary is None:
        return []
    try:
        returncode, raw = run_prime_cli(binary, prime_timeout_seconds(environ))
    except (FileNotFoundError, OSError, ValueError, subprocess.TimeoutExpired):
        return []
    if returncode != 0:
        return []
    if not isinstance(raw, bytes) or len(raw) > PRIME_OUTPUT_MAX_BYTES:
        return []
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError:
        return []
    sessions, active_id = parse_prime_payload(text)
    if sessions is None:
        return []
    id_lists = [prime_session_ids(entry) for entry in sessions]
    panes = []
    for _, (session, short_id) in match_prime_sessions(sessions, configured).items():
        active = prime_session_is_active(session, sessions, id_lists, active_id)
        panes.append(project_prime_session(session, short_id, active))
    return panes

def collect_all():
    """Merge tmux and Prime panes; either source may fail open on its own.

    tmux collection returning None (tmux missing or unreadable) must not
    suppress valid Prime-only observations. When nothing is observable at
    all, keep the legacy skip-report behaviour so a tmux-only host never
    wipes its wall on a transient tmux failure.
    """
    tmux_panes = collect()
    prime_panes = collect_prime_sessions()
    if tmux_panes is None:
        return prime_panes or None
    return tmux_panes + prime_panes

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
        panes = collect_all()
        if panes is not None:
            report(panes)
        time.sleep(INTERVAL)
    print("collector stopped")

if __name__ == "__main__":
    main()
