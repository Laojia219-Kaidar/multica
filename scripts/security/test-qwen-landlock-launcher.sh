#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
source_file="${repo_root}/scripts/security/hivecrew-landlock-exec.c"
launcher="${repo_root}/scripts/security/qwen-landlock-launcher.sh"
renderer="${repo_root}/scripts/security/render-qwen-landlock-launcher-for-test.py"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/hivecrew-qwen-launcher-test.XXXXXX")"
cleanup() {
  rm -rf -- "${test_root}"
}
trap cleanup EXIT HUP INT TERM

mkdir -p "${test_root}/bin" "${test_root}/allowed" "${test_root}/forbidden" "${test_root}/real-home/.qwen"
synthetic_api_key='SYNTHETIC_QWEN_CREDENTIAL_DO_NOT_USE_0102030405'
synthetic_api_key_sha256="$(printf '%s' "${synthetic_api_key}" | sha256sum | awk '{print $1}')"
cat > "${test_root}/real-home/.qwen/.env" <<EOF
BAILIAN_CODING_PLAN_API_KEY=${synthetic_api_key}
OPENAI_BASE_URL=https://coding.dashscope.aliyuncs.com/v1
OPENAI_MODEL=qwen3.7-plus
EOF
chmod 600 "${test_root}/real-home/.qwen/.env"

cc -std=c11 -O2 -Wall -Wextra -Werror "${source_file}" -o "${test_root}/bin/hivecrew-landlock-exec"

cat > "${test_root}/bin/fake-qwen" <<'FAKE_QWEN'
#!/bin/sh
set -eu
test "${SANDBOX:-}" = landlock
test -z "${QWEN_SANDBOX:-}"
test "${HOME}" != "${REAL_HOME}"
test ! -e "${HOME}/.qwen/.env"
test -n "${OPENAI_API_KEY:-}"
test -z "${BAILIAN_CODING_PLAN_API_KEY:-}"
test "${OPENAI_BASE_URL:-}" = 'https://coding.dashscope.aliyuncs.com/v1'
test "${OPENAI_MODEL:-}" = 'qwen3.7-plus'
test -z "${QWEN_MODEL:-}"
actual_key_sha256="$(printf '%s' "${OPENAI_API_KEY}" | sha256sum | awk '{print $1}')"
test "${actual_key_sha256}" = "${EXPECTED_API_KEY_SHA256:?}"
printf 'openai_api_key_present=true\nprovider_request_count=0\n' > "${PWD}/auth-observed"
printf state > "${HOME}/state"
printf work > "${PWD}/launcher-created"
if printf denied > "${FORBIDDEN}/launcher-created" 2>/dev/null; then
  echo "launcher allowed a write outside the task and temporary home" >&2
  exit 41
fi
printf '%s\n' "$@" > "${PWD}/launcher-args"
FAKE_QWEN
chmod 755 "${test_root}/bin/fake-qwen"

cat > "${test_root}/bin/forbidden-landlock" <<'FORBIDDEN_LANDLOCK'
#!/bin/sh
set -eu
printf called > "${LANDLOCK_CALL_MARKER:?}"
exit 99
FORBIDDEN_LANDLOCK
chmod 755 "${test_root}/bin/forbidden-landlock"

render_launcher() {
  local output="$1"
  local home="$2"
  local secret="$3"
  local landlock="$4"
  local qwen="$5"
  python3 "${renderer}" "${launcher}" "${output}" "${home}" "${secret}" "${landlock}" "${qwen}"
}

rendered_launcher="${test_root}/bin/qwen-landlock-launcher-test-only"
render_launcher \
  "${rendered_launcher}" \
  "${test_root}/real-home" \
  "${test_root}/real-home/.qwen/.env" \
  "${test_root}/bin/hivecrew-landlock-exec" \
  "${test_root}/bin/fake-qwen"

before_count="$(find /tmp -maxdepth 1 -type d -name 'hivecrew-qwen-landlock.*' | wc -l)"
(
  cd "${test_root}/allowed"
  EXPECTED_API_KEY_SHA256="${synthetic_api_key_sha256}" \
  REAL_HOME="${test_root}/real-home" \
  FORBIDDEN="${test_root}/forbidden" \
  OPENAI_API_KEY='UNTRUSTED_PARENT_SENTINEL' \
  BAILIAN_CODING_PLAN_API_KEY='UNTRUSTED_PARENT_SENTINEL' \
  OPENAI_BASE_URL='https://untrusted.invalid/v1' \
  OPENAI_MODEL='wrong-model' \
  QWEN_MODEL='wrong-model' \
  QWEN_SANDBOX=true \
    "${rendered_launcher}"
)
after_count="$(find /tmp -maxdepth 1 -type d -name 'hivecrew-qwen-landlock.*' | wc -l)"

test "$(cat "${test_root}/allowed/launcher-created")" = work
test ! -e "${test_root}/forbidden/launcher-created"
test "${before_count}" = "${after_count}"
grep -Fx -- 'openai_api_key_present=true' "${test_root}/allowed/auth-observed" >/dev/null
grep -Fx -- 'provider_request_count=0' "${test_root}/allowed/auth-observed" >/dev/null
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

mkdir -p "${test_root}/bounded"
(
  cd "${test_root}/bounded"
  EXPECTED_API_KEY_SHA256="${synthetic_api_key_sha256}" \
  REAL_HOME="${test_root}/real-home" \
  FORBIDDEN="${test_root}/forbidden" \
  HIVECREW_QWEN_TOOL_POLICY=bounded_read \
    "${rendered_launcher}"
)
bounded_args="$(paste -sd ' ' "${test_root}/bounded/launcher-args")"
case "${bounded_args}" in
  *'--approval-mode plan --max-tool-calls 8 --sandbox --safe-mode --allowed-tools read_file,glob,grep_search,list_directory --exclude-tools '*) ;;
  *) echo "bounded-read launcher args mismatch: ${bounded_args}" >&2; exit 1 ;;
esac
case "${bounded_args}" in *'--yolo'*) exit 1 ;; esac
for forbidden_tool in write_file edit run_shell_command web_fetch web_search create_sub_session agent; do
  case "${bounded_args}" in *"${forbidden_tool}"*) ;; *) exit 1 ;; esac
done

mkdir -p "${test_root}/bounded-workspace"
(
  cd "${test_root}/bounded-workspace"
  EXPECTED_API_KEY_SHA256="${synthetic_api_key_sha256}" \
  REAL_HOME="${test_root}/real-home" \
  FORBIDDEN="${test_root}/forbidden" \
  HIVECREW_QWEN_TOOL_POLICY=bounded_workspace_noshell \
    "${rendered_launcher}"
)
workspace_args="$(paste -sd ' ' "${test_root}/bounded-workspace/launcher-args")"
case "${workspace_args}" in
  *'--approval-mode auto-edit --max-tool-calls 12 --sandbox --allowed-tools read_file,glob,grep_search,list_directory,edit,write_file --exclude-tools '*) ;;
  *) echo "bounded-workspace launcher args mismatch: ${workspace_args}" >&2; exit 1 ;;
esac
case "${workspace_args}" in *'--yolo'*|*'--safe-mode'*) exit 1 ;; esac
for forbidden_tool in run_shell_command web_fetch web_search read_mcp_resource create_sub_session agent; do
  case "${workspace_args}" in *"${forbidden_tool}"*) ;; *) exit 1 ;; esac
done

mkdir -p "${test_root}/workspace-development"
(
  cd "${test_root}/workspace-development"
  EXPECTED_API_KEY_SHA256="${synthetic_api_key_sha256}" \
  REAL_HOME="${test_root}/real-home" \
  FORBIDDEN="${test_root}/forbidden" \
  HIVECREW_QWEN_TOOL_POLICY=bounded_workspace \
    "${rendered_launcher}"
)
development_args="$(paste -sd ' ' "${test_root}/workspace-development/launcher-args")"
case "${development_args}" in
  *'--approval-mode auto-edit --sandbox --allowed-tools read_file,glob,grep_search,list_directory,edit,write_file,run_shell_command --exclude-tools '*) ;;
  *) echo "workspace-development launcher args mismatch: ${development_args}" >&2; exit 1 ;;
esac
case "${development_args}" in *'--max-tool-calls'*|*'--yolo'*|*'--safe-mode'*) exit 1 ;; esac
for forbidden_tool in web_fetch web_search read_mcp_resource create_sub_session agent; do
  case "${development_args}" in *"${forbidden_tool}"*) ;; *) exit 1 ;; esac
done

while IFS= read -r candidate; do
  if grep -Fq -- "${synthetic_api_key}" "${candidate}"; then
    echo "synthetic credential escaped canonical reference: ${candidate}" >&2
    exit 42
  fi
done < <(find "${test_root}" -type f ! -path "${test_root}/real-home/.qwen/.env" -print)

assert_credential_rejected() {
  case_name="$1"
  fixture="$2"
  case_root="${test_root}/credential-${case_name}"
  case_home="${case_root}/home"
  mkdir -p "${case_home}/.qwen" "${case_root}/run"
  case_secret="${case_home}/.qwen/.env"
  case_launcher="${case_root}/launcher-test-only"
  case "${fixture}" in
    missing)
      ;;
    empty)
      cat > "${case_secret}" <<'EOF'
BAILIAN_CODING_PLAN_API_KEY=
OPENAI_BASE_URL=https://coding.dashscope.aliyuncs.com/v1
OPENAI_MODEL=qwen3.7-plus
EOF
      ;;
    malformed)
      cat > "${case_secret}" <<'EOF'
BAILIAN_CODING_PLAN_API_KEY
OPENAI_BASE_URL=https://coding.dashscope.aliyuncs.com/v1
OPENAI_MODEL=qwen3.7-plus
EOF
      ;;
    symlink)
      cat > "${case_root}/outside.env" <<EOF
BAILIAN_CODING_PLAN_API_KEY=${synthetic_api_key}
OPENAI_BASE_URL=https://coding.dashscope.aliyuncs.com/v1
OPENAI_MODEL=qwen3.7-plus
EOF
      chmod 600 "${case_root}/outside.env"
      ln -s "${case_root}/outside.env" "${case_secret}"
      ;;
    wrong-mode)
      cat > "${case_secret}" <<EOF
BAILIAN_CODING_PLAN_API_KEY=${synthetic_api_key}
OPENAI_BASE_URL=https://coding.dashscope.aliyuncs.com/v1
OPENAI_MODEL=qwen3.7-plus
EOF
      chmod 640 "${case_secret}"
      ;;
    wrong-group)
      cat > "${case_secret}" <<EOF
BAILIAN_CODING_PLAN_API_KEY=${synthetic_api_key}
OPENAI_BASE_URL=https://coding.dashscope.aliyuncs.com/v1
OPENAI_MODEL=qwen3.7-plus
EOF
      chmod 600 "${case_secret}"
      chgrp "${alternate_gid}" "${case_secret}"
      ;;
    extra-key)
      cat > "${case_secret}" <<EOF
BAILIAN_CODING_PLAN_API_KEY=${synthetic_api_key}
OPENAI_BASE_URL=https://coding.dashscope.aliyuncs.com/v1
OPENAI_MODEL=qwen3.7-plus
UNEXPECTED_KEY=value
EOF
      ;;
    wrong-base)
      cat > "${case_secret}" <<EOF
BAILIAN_CODING_PLAN_API_KEY=${synthetic_api_key}
OPENAI_BASE_URL=https://untrusted.invalid/v1
OPENAI_MODEL=qwen3.7-plus
EOF
      ;;
    wrong-model)
      cat > "${case_secret}" <<EOF
BAILIAN_CODING_PLAN_API_KEY=${synthetic_api_key}
OPENAI_BASE_URL=https://coding.dashscope.aliyuncs.com/v1
OPENAI_MODEL=wrong-model
EOF
      ;;
    duplicate-key)
      cat > "${case_secret}" <<EOF
BAILIAN_CODING_PLAN_API_KEY=${synthetic_api_key}
BAILIAN_CODING_PLAN_API_KEY=${synthetic_api_key}
OPENAI_BASE_URL=https://coding.dashscope.aliyuncs.com/v1
OPENAI_MODEL=qwen3.7-plus
EOF
      ;;
    *)
      exit 43
      ;;
  esac
  if [[ "${fixture}" != missing && "${fixture}" != symlink && "${fixture}" != wrong-mode ]]; then
    chmod 600 "${case_secret}"
  fi
  render_launcher \
    "${case_launcher}" \
    "${case_home}" \
    "${case_secret}" \
    "${test_root}/bin/forbidden-landlock" \
    "${test_root}/bin/fake-qwen"
  set +e
  (
    cd "${case_root}/run"
    HIVECREW_QWEN_CHAIN_TRACE="${case_root}/trace" \
    LANDLOCK_CALL_MARKER="${case_root}/landlock-called" \
      "${case_launcher}"
  ) >"${case_root}/stdout" 2>"${case_root}/stderr"
  status=$?
  set -e
  test "${status}" = 78
  grep -Fx -- 'qwen credential reference invalid' "${case_root}/stderr" >/dev/null
  test ! -e "${case_root}/landlock-called"
  test ! -e "${case_root}/trace"
  test ! -e "${case_root}/run/auth-observed"
}

assert_credential_rejected missing missing
assert_credential_rejected empty empty
assert_credential_rejected malformed malformed
assert_credential_rejected symlink symlink
assert_credential_rejected wrong-mode wrong-mode
alternate_gid="$(id -G | tr ' ' '\n' | grep -vx "$(id -g)" | head -n 1)"
test -n "${alternate_gid}"
assert_credential_rejected wrong-group wrong-group
assert_credential_rejected extra-key extra-key
assert_credential_rejected wrong-base wrong-base
assert_credential_rejected wrong-model wrong-model
assert_credential_rejected duplicate-key duplicate-key

(
  # Unit-level wrong-owner coverage uses the same production validator with a
  # deliberately mismatched expected uid. Production supplies fixed uid/gid
  # constants and exposes no caller-controlled identity override.
  source "${launcher}"
  QWEN_CREDENTIAL_API_KEY=''
  if load_qwen_credential_reference \
    "${test_root}/real-home/.qwen/.env" \
    "${test_root}/real-home" \
    "$(( $(id -u) + 1 ))" \
    "$(id -g)"; then
    exit 44
  fi
  test -z "${QWEN_CREDENTIAL_API_KEY}"
)

assert_wrong_group_rejected() {
  local component="$1"
  local case_root="${test_root}/wrong-group-${component}"
  local case_home="${case_root}/home"
  local case_landlock="${case_root}/landlock"
  local case_qwen="${case_root}/qwen"
  local case_launcher="${case_root}/launcher-test-only"
  local expected_error=''
  mkdir -p "${case_home}/.qwen" "${case_root}/run"
  cp "${test_root}/real-home/.qwen/.env" "${case_home}/.qwen/.env"
  cp "${test_root}/bin/forbidden-landlock" "${case_landlock}"
  cp "${test_root}/bin/fake-qwen" "${case_qwen}"
  chmod 600 "${case_home}/.qwen/.env"
  chmod 755 "${case_landlock}" "${case_qwen}"
  render_launcher \
    "${case_launcher}" \
    "${case_home}" \
    "${case_home}/.qwen/.env" \
    "${case_landlock}" \
    "${case_qwen}"
  case "${component}" in
    launcher)
      chgrp "${alternate_gid}" "${case_launcher}"
      expected_error='qwen launcher identity invalid'
      ;;
    landlock)
      chgrp "${alternate_gid}" "${case_landlock}"
      expected_error='qwen landlock identity invalid'
      ;;
    qwen)
      chgrp "${alternate_gid}" "${case_qwen}"
      expected_error='qwen executable identity invalid'
      ;;
    *) return 64 ;;
  esac
  set +e
  (
    cd "${case_root}/run"
    HIVECREW_QWEN_CHAIN_TRACE="${case_root}/trace" \
    LANDLOCK_CALL_MARKER="${case_root}/landlock-called" \
      "${case_launcher}"
  ) >"${case_root}/stdout" 2>"${case_root}/stderr"
  status=$?
  set -e
  test "${status}" = 78
  grep -Fx -- "${expected_error}" "${case_root}/stderr" >/dev/null
  test ! -e "${case_root}/landlock-called"
  test ! -e "${case_root}/trace"
  test ! -e "${case_root}/run/auth-observed"
}

assert_wrong_group_rejected launcher
assert_wrong_group_rejected landlock
assert_wrong_group_rejected qwen

for override in \
  HIVECREW_QWEN_REAL_HOME \
  HIVECREW_QWEN_SECRET_FILE \
  HIVECREW_LANDLOCK_EXEC \
  HIVECREW_QWEN_BIN; do
  override_root="${test_root}/ambient-${override}"
  mkdir -p "${override_root}"
  set +e
  env \
    "${override}=/bin/true" \
    HIVECREW_QWEN_CHAIN_TRACE="${override_root}/trace" \
    LANDLOCK_CALL_MARKER="${override_root}/landlock-called" \
      "${launcher}" >"${override_root}/stdout" 2>"${override_root}/stderr"
  status=$?
  set -e
  test "${status}" = 77
  grep -Fx -- 'qwen path override rejected' "${override_root}/stderr" >/dev/null
  test ! -e "${override_root}/trace"
  test ! -e "${override_root}/landlock-called"
done

assert_auth_type_rejected() {
  case_name="$1"
  shift
  case_root="${test_root}/${case_name}"
  mkdir -p "${case_root}"
  set +e
  (
    cd "${case_root}"
    REAL_HOME="${test_root}/real-home" \
    FORBIDDEN="${test_root}/forbidden" \
    HIVECREW_QWEN_CHAIN_TRACE="${case_root}/trace" \
    LANDLOCK_CALL_MARKER="${case_root}/landlock-called" \
      "${rendered_forbidden_launcher}" "$@"
  ) >"${case_root}/stdout" 2>"${case_root}/stderr"
  status=$?
  set -e
  test "${status}" = 77
  grep -Fx -- 'reserved auth/model/sandbox/tool flag' "${case_root}/stderr" >/dev/null
  test ! -e "${case_root}/landlock-called"
  test ! -e "${case_root}/trace"
  test ! -e "${case_root}/launcher-args"
  test ! -e "${case_root}/launcher-created"
}

rendered_forbidden_launcher="${test_root}/bin/qwen-landlock-launcher-forbidden-test-only"
render_launcher \
  "${rendered_forbidden_launcher}" \
  "${test_root}/real-home" \
  "${test_root}/real-home/.qwen/.env" \
  "${test_root}/bin/forbidden-landlock" \
  "${test_root}/bin/fake-qwen"

assert_auth_type_rejected task-explicit-openai --auth-type openai
assert_auth_type_rejected task-inline-openai --auth-type=openai
assert_auth_type_rejected task-explicit-other --auth-type qwen-oauth
assert_auth_type_rejected task-inline-other --auth-type=qwen-oauth
assert_auth_type_rejected task-duplicate --auth-type openai --auth-type openai
assert_auth_type_rejected task-camel-explicit-openai --authType openai
assert_auth_type_rejected task-camel-inline-openai --authType=openai
assert_auth_type_rejected task-camel-explicit-other --authType qwen-oauth
assert_auth_type_rejected task-camel-inline-other --authType=qwen-oauth
assert_auth_type_rejected task-camel-duplicate --authType openai --authType qwen-oauth
assert_auth_type_rejected task-camel-missing-value --authType
assert_auth_type_rejected task-camel-inline-missing-value --authType=

echo "HIVECREW_QWEN_LANDLOCK_LAUNCHER_TEST_PASS"
