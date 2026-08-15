#!/usr/bin/env bash
# work-entry-canary.sh — headless vertical-slice canary against the built
# `multica` binary (in-memory/offline ledger via --state; no live DB, no HTTP).
#
# Demonstrates the full work-entry verb journey with real assertions:
#   1. resolve   (no exact match -> classification_required, VC-07 no auto-create)
#   2. register  (external_agent WITHOUT employee_id -> work_ref, VC-02)
#   3. register  (again, same key+digest -> replayed same work_ref, VC-03)
#   4. start     (append started event for the work_ref)
#   5. event     (append a structured progress event)
#   6. finish    (candidate -> review routed, auto_passed=false, never auto-pass)
#   7. doctor    (unclaimed inbox diagnostic -> empty array)
#   8. status    (work_ref found)
#
# No secrets or chain-of-thought are emitted. Timestamps are fixed so the run
# is deterministic and replayable.
set -euo pipefail

BIN="${MULTICA_BIN:-multica}"
STATE="$(mktemp -d)/ledger.json"
WS="00000000-0000-0000-0000-000000000001"
SESSION_ID="canary-s1"
RUN_ID="canary-r1"
ACTOR_ID="EXT-canary-001"
OBS_AT="2026-08-15T23:30:00Z"

fail() { echo "ASSERT FAIL: $*" >&2; exit 1; }

# assert_json <json> <jq-filter> <expected-string>
assert_json() {
  local got
  got="$(printf '%s' "$1" | jq -r "$2" 2>/dev/null)"
  if [ "$got" != "$3" ]; then
    fail "jq '$2' => '$got' (want '$3')"
  fi
}

# Actor + intent shared by resolve and register (external_agent, no employee_id).
REQ='{
  "actor_identity": {
    "actor_type": "external_agent",
    "actor_id": "'"$ACTOR_ID"'",
    "carrier_id": "prime",
    "runtime_id": "prime-agent-runtime",
    "model_ref": "deepseek-v4-pro",
    "host_id": "jiaweis-Mac-mini.local",
    "session_id": "'"$SESSION_ID"'",
    "workspace_id": "'"$WS"'",
    "observed_at": "'"$OBS_AT"'"
  },
  "intent": {
    "owner_intent": "canary slice",
    "goal_ref": "HIVECREW-UNIVERSAL-DEVELOPMENT-ENTRY-PROJECT-OS-V1",
    "objective": "validate work entry kernel verbs",
    "expected_human_result": "work_ref receipt and review-routed finish",
    "repo": "/Volumes/HiveData/hivecosm/HQ-50-代码仓库/01-源码下载/multica",
    "baseline_revision": "bd7b9a28b",
    "branch_or_worktree": "work/hivecrew-universal-development-entry-project-os-v1",
    "read_scope": ["server/internal/workentry"],
    "write_scope": ["server/internal/workentry"],
    "expected_outcomes": ["receipt", "review_routed_finish"],
    "candidate_formal_boundary": "candidate"
  }
}'

echo "== 1. resolve (no exact match -> classification_required) =="
R="$(printf '%s' "$REQ" | "$BIN" work resolve --state "$STATE" --request-stdin --output json)"
assert_json "$R" '.resolution_decision' 'classification_required'
echo "  resolution_decision=classification_required (ok)"

echo "== 2. register as external_agent WITHOUT employee_id (VC-02) =="
R1="$(printf '%s' "$REQ" | "$BIN" work register --state "$STATE" --request-stdin --confirm-create --output json)"
assert_json "$R1" '.created' 'true'
REF="$(printf '%s' "$R1" | jq -r '.work_ref')"
[ -n "$REF" ] || fail "register did not return a work_ref"
echo "  work_ref=$REF (created=true, ok)"

echo "== 3. register again (same key+digest -> replayed same work_ref, VC-03) =="
R2="$(printf '%s' "$REQ" | "$BIN" work register --state "$STATE" --request-stdin --confirm-create --output json)"
assert_json "$R2" '.replay.replayed' 'true'
assert_json "$R2" '.work_ref' "$REF"
echo "  replayed=true, work_ref unchanged (ok)"

echo "== 4. start $REF =="
S="$("$BIN" work start "$REF" --state "$STATE" --session-id "$SESSION_ID" --run-id "$RUN_ID" --actor-id "$ACTOR_ID" --workspace-id "$WS" --output json)"
assert_json "$S" '.event_id | length > 0' 'true'
echo "  started event appended (ok)"

echo "== 5. event (progress, no chain-of-thought) =="
EV_BODY='{"work_ref":"'"$REF"'","session_id":"'"$SESSION_ID"'","run_id":"'"$RUN_ID"'","event_type":"progress","event_payload":{"step":"verify_kernel_verbs"},"idempotency_key":"canary-progress-1","occurred_at":"'"$OBS_AT"'","observed_at":"'"$OBS_AT"'"}'
EV="$(printf '%s' "$EV_BODY" | "$BIN" work event --state "$STATE" --request-stdin --output json)"
assert_json "$EV" '.event_id | length > 0' 'true'
echo "  progress event appended (ok)"

echo "== 6. finish (candidate -> review routed, never auto-pass) =="
FIN_BODY='{"work_ref":"'"$REF"'","completion_candidate":{"artifact_ref":"artifact://canary/1","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","revision":"r1"},"review":{},"project_lifecycle_consequence":"continue"}'
FIN="$(printf '%s' "$FIN_BODY" | "$BIN" work finish --state "$STATE" --request-stdin --output json)"
assert_json "$FIN" '.review_routed' 'true'
assert_json "$FIN" '.auto_passed' 'false'
echo "  review_routed=true, auto_passed=false (ok)"

echo "== 7. doctor (unclaimed inbox diagnostic) =="
DOC="$("$BIN" work doctor --state "$STATE" --workspace-id "$WS" --output json)"
assert_json "$DOC" 'type' 'array'
echo "  inbox=$(printf '%s' "$DOC" | jq -c '.') (ok)"

echo "== 8. status $REF =="
ST="$("$BIN" work status "$REF" --state "$STATE" --workspace-id "$WS" --output json)"
assert_json "$ST" '.found' 'true'
assert_json "$ST" '.resolution_decision' 'created'
echo "  found=true (ok)"

echo ""
echo "CANARY_COMPLETE"
