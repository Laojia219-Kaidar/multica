#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
source_file="${repo_root}/scripts/security/hivecrew-landlock-exec.c"
launcher="${repo_root}/scripts/security/qwen-landlock-launcher.sh"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/hivecrew-qwen-launcher-test.XXXXXX")"
cleanup() {
  rm -rf -- "${test_root}"
}
trap cleanup EXIT HUP INT TERM

mkdir -p "${test_root}/bin" "${test_root}/allowed" "${test_root}/forbidden" "${test_root}/real-home/.qwen"
printf 'synthetic credential placeholder\n' > "${test_root}/real-home/.qwen/.env"
chmod 600 "${test_root}/real-home/.qwen/.env"

cc -std=c11 -O2 -Wall -Wextra -Werror "${source_file}" -o "${test_root}/bin/hivecrew-landlock-exec"

cat > "${test_root}/bin/fake-qwen" <<'FAKE_QWEN'
#!/bin/sh
set -eu
test "${SANDBOX:-}" = landlock
test -z "${QWEN_SANDBOX:-}"
test "${HOME}" != "${REAL_HOME}"
test -L "${HOME}/.qwen/.env"
test "$(readlink "${HOME}/.qwen/.env")" = "${EXPECTED_SECRET}"
printf state > "${HOME}/state"
printf work > "${PWD}/launcher-created"
if printf denied > "${FORBIDDEN}/launcher-created" 2>/dev/null; then
  echo "launcher allowed a write outside the task and temporary home" >&2
  exit 41
fi
printf '%s\n' "$@" > "${PWD}/launcher-args"
FAKE_QWEN
chmod 755 "${test_root}/bin/fake-qwen"

before_count="$(find /tmp -maxdepth 1 -type d -name 'hivecrew-qwen-landlock.*' | wc -l)"
(
  cd "${test_root}/allowed"
  EXPECTED_SECRET="${test_root}/real-home/.qwen/.env" \
  REAL_HOME="${test_root}/real-home" \
  FORBIDDEN="${test_root}/forbidden" \
  HIVECREW_QWEN_REAL_HOME="${test_root}/real-home" \
  HIVECREW_LANDLOCK_EXEC="${test_root}/bin/hivecrew-landlock-exec" \
  HIVECREW_QWEN_BIN="${test_root}/bin/fake-qwen" \
  HIVECREW_QWEN_SECRET_FILE="${test_root}/real-home/.qwen/.env" \
  QWEN_SANDBOX=true \
    "${launcher}"
)
after_count="$(find /tmp -maxdepth 1 -type d -name 'hivecrew-qwen-landlock.*' | wc -l)"

test "$(cat "${test_root}/allowed/launcher-created")" = work
test ! -e "${test_root}/forbidden/launcher-created"
test "${before_count}" = "${after_count}"
grep -Fx -- '--model' "${test_root}/allowed/launcher-args" >/dev/null
grep -Fx -- 'qwen3.7-plus' "${test_root}/allowed/launcher-args" >/dev/null
grep -Fx -- '--approval-mode' "${test_root}/allowed/launcher-args" >/dev/null
grep -Fx -- 'plan' "${test_root}/allowed/launcher-args" >/dev/null
grep -Fx -- '--max-tool-calls' "${test_root}/allowed/launcher-args" >/dev/null
grep -Fx -- '0' "${test_root}/allowed/launcher-args" >/dev/null
grep -Fx -- '--sandbox' "${test_root}/allowed/launcher-args" >/dev/null
test "$(grep -Fxc -- '--auth-type' "${test_root}/allowed/launcher-args")" = 1
test "$(grep -Fxc -- 'openai' "${test_root}/allowed/launcher-args")" = 1
test "$(paste -sd ' ' "${test_root}/allowed/launcher-args")" = '--auth-type openai --model qwen3.7-plus --approval-mode plan --max-tool-calls 0 --sandbox'

assert_auth_type_rejected() {
  case_name="$1"
  shift
  case_root="${test_root}/${case_name}"
  mkdir -p "${case_root}"
  set +e
  (
    cd "${case_root}"
    EXPECTED_SECRET="${test_root}/real-home/.qwen/.env" \
    REAL_HOME="${test_root}/real-home" \
    FORBIDDEN="${test_root}/forbidden" \
    HIVECREW_QWEN_REAL_HOME="${test_root}/real-home" \
    HIVECREW_LANDLOCK_EXEC="${test_root}/bin/hivecrew-landlock-exec" \
    HIVECREW_QWEN_BIN="${test_root}/bin/fake-qwen" \
    HIVECREW_QWEN_SECRET_FILE="${test_root}/real-home/.qwen/.env" \
      "${launcher}" "$@"
  ) >"${case_root}/stdout" 2>"${case_root}/stderr"
  status=$?
  set -e
  test "${status}" = 77
  grep -Fx -- 'reserved auth/model/sandbox/tool flag' "${case_root}/stderr" >/dev/null
  test ! -e "${case_root}/launcher-args"
  test ! -e "${case_root}/launcher-created"
}

assert_auth_type_rejected task-explicit-openai --auth-type openai
assert_auth_type_rejected task-inline-openai --auth-type=openai
assert_auth_type_rejected task-explicit-other --auth-type qwen-oauth
assert_auth_type_rejected task-inline-other --auth-type=qwen-oauth
assert_auth_type_rejected task-duplicate --auth-type openai --auth-type openai

echo "HIVECREW_QWEN_LANDLOCK_LAUNCHER_TEST_PASS"
