#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
source_file="${repo_root}/scripts/security/hivecrew-landlock-exec.c"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/hivecrew-landlock-test.XXXXXX")"
cleanup() {
  rm -rf -- "${test_root}"
}
trap cleanup EXIT HUP INT TERM

mkdir -p "${test_root}/allowed" "${test_root}/denied"
ln -s "${test_root}/denied" "${test_root}/allowed/escape"

cc -std=c11 -O2 -Wall -Wextra -Werror "${source_file}" -o "${test_root}/hivecrew-landlock-exec"

ALLOWED="${test_root}/allowed" DENIED="${test_root}/denied" \
  QWEN_SANDBOX=true \
  "${test_root}/hivecrew-landlock-exec" --write "${test_root}/allowed" -- /bin/sh -c '
    set -eu
    test "${SANDBOX:-}" = landlock
    test -z "${QWEN_SANDBOX:-}"
    printf allowed > "${ALLOWED}/created"
    if printf denied > "${DENIED}/created" 2>/dev/null; then
      echo "write outside allowlist unexpectedly succeeded" >&2
      exit 31
    fi
    if printf escaped > "${ALLOWED}/escape/created" 2>/dev/null; then
      echo "symlink escape unexpectedly succeeded" >&2
      exit 32
    fi
  '

test "$(cat "${test_root}/allowed/created")" = allowed
test ! -e "${test_root}/denied/created"

set +e
"${test_root}/hivecrew-landlock-exec" --write "${test_root}/allowed" -- /bin/sh -c 'exit 37'
status=$?
set -e
test "${status}" -eq 37

echo "HIVECREW_LANDLOCK_TEST_PASS"
