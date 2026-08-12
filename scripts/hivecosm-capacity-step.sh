#!/usr/bin/env bash
# HIV-361 candidate: controlled daemon capacity step (e.g. 4→8) with
# zero-downtime gate, metric capture, and auto-fallback to 6.
# NOT executed in this work order — candidate only. Requires explicit owner
# authorization + active_task_count == 0.
set -eu

MULTICA_BIN="${MULTICA_BIN:-multica}"
TARGET="${1:-8}"
FALLBACK="${2:-6}"
LOGDIR="${HIVECOSM_CAPACITY_LOG_DIR:-/tmp/hivecosm-capacity}"
TS=$(date +%Y%m%dT%H%M%S)
LOGFILE="$LOGDIR/capacity-step-$TS.log"
PIDFILE="${HOME}/.multica/daemon.pid"

mkdir -p "$LOGDIR"
log() { echo "[$(date +%H:%M:%S)] $*" | tee -a "$LOGFILE"; }

# 0) Gate: daemon must be running and idle.
STATUS=$("$MULTICA_BIN" daemon status --output json 2>/dev/null || echo '{"status":"stopped"}')
ACTIVE=$(printf '%s' "$STATUS" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("active_task_count",-1))' 2>/dev/null || echo -1)
if [ "$ACTIVE" != "0" ]; then
  log "ABORT: active_task_count=$ACTIVE (must be 0). No restart, no task cancelled."
  exit 3
fi

# 1) Baseline (CPU/mem/fds of the formal daemon).
DAEMON_PID=$(cat "$PIDFILE" 2>/dev/null || echo 0)
log "baseline: $(ps -o pid,%cpu,%mem,nlwp -p "$DAEMON_PID" -o command= 2>/dev/null | tr -s ' ')"
log "fds: $(lsof -p "$DAEMON_PID" 2>/dev/null | wc -l | tr -d ' ')"
log "config before: $("$MULTICA_BIN" config show 2>/dev/null | rg 'max_concurrent_tasks' || true)"

# 2) Persist the new ceiling (single source of truth).
"$MULTICA_BIN" config set max_concurrent_tasks "$TARGET" >>"$LOGFILE" 2>&1
log "config set max_concurrent_tasks=$TARGET"

# 3) Controlled restart WITHOUT the flag (flag would fail closed if divergent).
"$MULTICA_BIN" daemon restart >>"$LOGFILE" 2>&1
sleep 2
"$MULTICA_BIN" daemon status --output json 2>/dev/null | tee -a "$LOGFILE"
log "config after: $("$MULTICA_BIN" config show 2>/dev/null | rg 'max_concurrent_tasks' || true)"

# 4) Short health observation window (30s smoke test; full window per runbook).
sleep 30
TASKS_OK=$(grep -c 'status=completed' "${HOME}/.multica/daemon.log" || true)
TASKS_CAN=$(grep -c 'status=cancelled' "${HOME}/.multica/daemon.log" || true)
RATE_HITS=$(grep -ciE '429|rate.?limit|throttl' "${HOME}/.multica/daemon.log" || true)
log "smoke: completed=$TASKS_OK cancelled=$TASKS_CAN rate_limit_hits=$RATE_HITS"

# 5) Auto-fallback to 6 when the smoke window shows problems.
if [ "$TASKS_CAN" -gt 0 ] || [ "$RATE_HITS" -gt 0 ]; then
  log "FALLBACK: applying max_concurrent_tasks=$FALLBACK"
  "$MULTICA_BIN" config set max_concurrent_tasks "$FALLBACK"
  "$MULTICA_BIN" daemon restart >>"$LOGFILE" 2>&1
  log "fallback applied; log: $LOGFILE"
  exit 2
fi

log "OK: step to $TARGET applied; log: $LOGFILE"