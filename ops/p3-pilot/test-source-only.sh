#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
preflight="$root/ops/p3-pilot/pilot-preflight.sh"
overlay="$root/ops/p3-pilot/review-cell.staging.override.yaml"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

calls="$tmp/calls.log"
fake_cli="$root/ops/p3-pilot/tests/fake-hivecrew"
fake_git="$root/ops/p3-pilot/tests/fake-git"

run_preflight() {
  P3_TEST_CALLS="$calls" P3_PILOT_CLI="$fake_cli" P3_PILOT_GIT="$fake_git" "$preflight"
}

run_preflight | jq -e '.status == "PASS_READY_FOR_SEPARATELY_AUTHORIZED_MUTATION" and .mutations == 0' >/dev/null

for mode in stale wrong-url duplicate; do
  if P3_TEST_RESOURCE_MODE="$mode" run_preflight >/dev/null 2>&1; then
    echo "expected resource mode $mode to fail closed" >&2
    exit 1
  fi
done

if P3_TEST_REVISION=deadbeef run_preflight >/dev/null 2>&1; then
  echo "expected revision mismatch to fail closed" >&2
  exit 1
fi
if P3_TEST_TREE=deadbeef run_preflight >/dev/null 2>&1; then
  echo "expected tree mismatch to fail closed" >&2
  exit 1
fi

if grep -E 'issue (create|assign)|dispatch|worktree (add|remove)' "$calls"; then
  echo "preflight attempted a mutation" >&2
  exit 1
fi

[[ "$(grep -c 'REVIEW_CELL_ENABLED: "true"' "$overlay")" == "1" ]]
[[ "$(grep -c 'REVIEW_CELL_L1_AGENT_ID: "708a49ba-7e7d-4363-8b9b-e6a4aeb5d980"' "$overlay")" == "1" ]]
! grep -Eq '(^|[[:space:]])(image|build|ports|volumes|network_mode):' "$overlay"

echo "P3_SOURCE_ONLY_TESTS_PASS"
