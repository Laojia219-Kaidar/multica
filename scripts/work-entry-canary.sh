#!/usr/bin/env bash
# work-entry-canary.sh — headless vertical-slice canary (in-memory/offline ledger, no live DB).
# Demonstrates VC-02 (external_agent without employee_id) and VC-03 (idempotent replay).
set -euo pipefail

BIN="${MULTICA_BIN:-multica}"
STATE="$(mktemp -d)/ledger.json"
WS="00000000-0000-0000-0000-000000000001"

REQ='{
  "actor_identity": {
    "actor_type": "external_agent",
    "actor_id": "EXT-canary-001",
    "carrier_id": "prime",
    "runtime_id": "prime-agent-runtime",
    "model_ref": "deepseek-v4-pro",
    "host_id": "jiaweis-Mac-mini.local",
    "session_id": "canary-s1",
    "workspace_id": "'"$WS"'",
    "observed_at": "2026-08-15T23:30:00+08:00"
  },
  "intent": {
    "owner_intent": "canary slice",
    "goal_ref": "HIVECREW-UNIVERSAL-DEVELOPMENT-ENTRY-PROJECT-OS-V1",
    "objective": "validate work entry kernel",
    "expected_human_result": "work_ref receipt",
    "repo": "/Volumes/HiveData/hivecosm/HQ-50-代码仓库/01-源码下载/multica",
    "baseline_revision": "bd7b9a28b",
    "branch_or_worktree": "work/hivecrew-universal-development-entry-project-os-v1",
    "read_scope": ["server/internal/workentry"],
    "write_scope": ["server/internal/workentry"],
    "expected_outcomes": ["receipt"],
    "candidate_formal_boundary": "candidate"
  }
}'

echo "== 1. resolve (no exact match -> classification_required) =="
echo "$REQ" | "$BIN" work resolve --state "$STATE" --request-stdin --output json || true

echo ""
echo "== 2. register as external_agent WITHOUT employee_id (VC-02) =="
R1=$(echo "$REQ" | "$BIN" work register --state "$STATE" --request-stdin --confirm-create --output json)
echo "$R1"
REF=$(echo "$R1" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("work_ref",""))' 2>/dev/null || true)
echo "work_ref=$REF"

echo ""
echo "== 3. register again (same key+digest -> replayed same work_ref, VC-03) =="
echo "$REQ" | "$BIN" work register --state "$STATE" --request-stdin --confirm-create --output json

echo ""
echo "== 4. status $REF =="
"$BIN" work status "$REF" --state "$STATE" --workspace-id "$WS" --output json || true

echo ""
echo "== 5. doctor (unclaimed inbox diagnostic) =="
"$BIN" work doctor --state "$STATE" --workspace-id "$WS" --output json || true

echo ""
echo "CANARY_COMPLETE"
