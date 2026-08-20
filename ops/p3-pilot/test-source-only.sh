#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
preflight="$root/ops/p3-pilot/pilot-preflight.sh"
overlay="$root/ops/p3-pilot/review-cell.staging.override.yaml"
harness="$root/ops/p3-pilot/tests/pilot-preflight-test-harness.py"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

calls="$tmp/calls.log"
stdout="$tmp/stdout"
stderr="$tmp/stderr"
fake_cli="$tmp/fake-hivecrew"
fake_git="$tmp/fake-git"
generated="$tmp/pilot-preflight.TEST-ONLY.sh"
/bin/cp -- "$root/ops/p3-pilot/tests/fake-hivecrew" "$fake_cli"
/bin/cp -- "$root/ops/p3-pilot/tests/fake-git" "$fake_git"
/bin/chmod 755 "$fake_cli" "$fake_git"

render() {
  /usr/bin/python3 "$harness" \
    --operator "$preflight" \
    --output "$generated" \
    --cli "$fake_cli" \
    --git "$fake_git" \
    "$@"
}

run_test_copy() {
  P3_TEST_CALLS="$calls" "$generated"
}

assert_no_mutation_calls() {
  if /usr/bin/grep -E 'issue (create|assign)|dispatch|worktree (add|remove)|runtime (create|update)|agent (create|update)' "$calls"; then
    echo "preflight attempted a mutation" >&2
    exit 1
  fi
}

expect_reject() {
  local label="$1"
  shift
  : >"$calls"
  set +e
  "$@" >"$stdout" 2>"$stderr"
  local rc=$?
  set -e
  if [[ "$rc" -ne 42 ]]; then
    printf 'expected %s to reject with rc42, got %s\n' "$label" "$rc" >&2
    exit 1
  fi
  assert_no_mutation_calls
}

# The live operator contains no test/ambient injection contract. The harness
# renders a temporary copy whose success status is visibly test-only.
! /usr/bin/grep -Eq 'P3_PILOT_(CLI|GIT)|P3_TEST_' "$preflight"
render
: >"$calls"
run_test_copy | /usr/bin/jq -e '
  .status == "PASS_TEST_ONLY_NOT_OPERATOR_RECEIPT" and
  .mutations == 0 and
  .source_ref_resolution_count == 1 and
  .tool_binding_mode == "open_fd_exact"
' >/dev/null
[[ "$(/usr/bin/grep -Fc 'refs/heads/owner/william/william-macstudio-ultra/codex/wo-c1-04-hivecrew-integration-v1^{commit}' "$calls")" == "1" ]]
[[ "$(/usr/bin/grep -Fc 'refs/heads/owner/william/william-macstudio-ultra/codex/wo-c1-04-hivecrew-integration-v1^{tree}' "$calls")" == "0" ]]
assert_no_mutation_calls

for mode in stale wrong-url duplicate; do
  expect_reject "resource mode $mode" env P3_TEST_CALLS="$calls" P3_TEST_RESOURCE_MODE="$mode" "$generated"
done

expect_reject "revision mismatch" env P3_TEST_CALLS="$calls" P3_TEST_REVISION=deadbeef "$generated"
expect_reject "tree mismatch" env P3_TEST_CALLS="$calls" P3_TEST_TREE=deadbeef "$generated"

# Canonical path, symlink, digest and version failures all stop before the
# authenticated project resource read.
render --cli-canonical "$tmp/not-the-cli"
expect_reject "wrong canonical CLI path" run_test_copy
! /usr/bin/grep -Fq 'project resource list' "$calls"

/bin/ln -s "$fake_cli" "$tmp/fake-hivecrew-link"
/usr/bin/python3 "$harness" \
  --operator "$preflight" --output "$generated" \
  --cli "$tmp/fake-hivecrew-link" --git "$fake_git"
expect_reject "CLI symlink" run_test_copy
[[ ! -s "$calls" ]]

render --cli-sha256 0000000000000000000000000000000000000000000000000000000000000000
expect_reject "wrong CLI digest" run_test_copy
[[ ! -s "$calls" ]]

render
expect_reject "wrong CLI version" env P3_TEST_CALLS="$calls" P3_TEST_CLI_VERSION_LINE_1="wrong version" "$generated"
! /usr/bin/grep -Fq 'project resource list' "$calls"

render --git-sha256 0000000000000000000000000000000000000000000000000000000000000000
expect_reject "wrong Git digest" run_test_copy
[[ ! -s "$calls" ]]

render
expect_reject "wrong Git version" env P3_TEST_CALLS="$calls" P3_TEST_GIT_VERSION="wrong version" "$generated"
! /usr/bin/grep -Fq 'project resource list' "$calls"

# Replace the canonical CLI pathname while its exact original fd is open.
# The post-version identity recheck must reject before any resource call.
/bin/cp -- "$root/ops/p3-pilot/tests/fake-hivecrew" "$tmp/toctou-cli"
/bin/cp -- "$root/ops/p3-pilot/tests/fake-hivecrew" "$tmp/toctou-replacement"
/bin/chmod 755 "$tmp/toctou-cli" "$tmp/toctou-replacement"
fake_cli="$tmp/toctou-cli"
render
expect_reject "TOCTOU-like CLI replacement" env \
  P3_TEST_CALLS="$calls" \
  P3_TEST_REPLACE_PATH="$tmp/toctou-cli" \
  P3_TEST_REPLACEMENT="$tmp/toctou-replacement" \
  "$generated"
! /usr/bin/grep -Fq 'project resource list' "$calls"

# The actual live entrypoint ignores ambient overrides and, with the current
# stale project resource ref, still fails closed with rc42. The committed fakes
# are not invoked and therefore cannot forge a PASS receipt.
: >"$calls"
set +e
P3_TEST_CALLS="$calls" \
P3_PILOT_CLI="$root/ops/p3-pilot/tests/fake-hivecrew" \
P3_PILOT_GIT="$root/ops/p3-pilot/tests/fake-git" \
  "$preflight" >"$stdout" 2>"$stderr"
live_rc=$?
set -e
[[ "$live_rc" -eq 42 ]]
/usr/bin/grep -Fq 'project repository ref is stale or ambiguous' "$stderr"
[[ ! -s "$calls" ]]

assert_no_mutation_calls

[[ "$(/usr/bin/grep -c 'REVIEW_CELL_ENABLED: "true"' "$overlay")" == "1" ]]
[[ "$(/usr/bin/grep -c 'REVIEW_CELL_L1_AGENT_ID: "708a49ba-7e7d-4363-8b9b-e6a4aeb5d980"' "$overlay")" == "1" ]]
! /usr/bin/grep -Eq '(^|[[:space:]])(image|build|ports|volumes|network_mode):' "$overlay"

echo "P3_SOURCE_ONLY_R2_TESTS_PASS"
