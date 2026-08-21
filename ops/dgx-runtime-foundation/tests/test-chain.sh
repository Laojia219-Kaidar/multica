#!/usr/bin/env bash
set -Eeuo pipefail
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH= cd -- "$root/../../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/home/.qwen" "$tmp/bin"; chmod 700 "$tmp/home" "$tmp/home/.qwen"
synthetic_api_key='SYNTHETIC_QWEN_CHAIN_CREDENTIAL_DO_NOT_USE_0102030405'
export EXPECTED_API_KEY_SHA256="$(printf '%s' "$synthetic_api_key" | sha256sum | awk '{print $1}')"
cat > "$tmp/home/.qwen/.env" <<EOF
BAILIAN_CODING_PLAN_API_KEY=${synthetic_api_key}
OPENAI_BASE_URL=https://coding.dashscope.aliyuncs.com/v1
OPENAI_MODEL=qwen3.7-plus
EOF
chmod 600 "$tmp/home/.qwen/.env"
cat > "$tmp/bin/qwen" <<'SH'
#!/bin/sh
set -eu
if [ "${1:-}" = --version ]; then
  test -z "${VERSION_MARKER:-}" || printf called > "${VERSION_MARKER}"
  printf 'qwen 0.21.14\n'
  exit 0
fi
test -n "${OPENAI_API_KEY:-}"
test -z "${BAILIAN_CODING_PLAN_API_KEY:-}"
test "${OPENAI_BASE_URL:-}" = 'https://coding.dashscope.aliyuncs.com/v1'
test "${OPENAI_MODEL:-}" = 'qwen3.7-plus'
test -z "${QWEN_MODEL:-}"
test ! -e "${HOME}/.qwen/.env"
actual_key_sha256="$(printf '%s' "${OPENAI_API_KEY}" | sha256sum | awk '{print $1}')"
test "${actual_key_sha256}" = "${EXPECTED_API_KEY_SHA256:?}"
printf 'openai_api_key_present=true\nprovider_request_count=0\n' > "${AUTH_LOG:?}"
printf '%s\n' "$*" > "${CHAIN_LOG:?}"
SH
cat > "$tmp/bin/landlock" <<'SH'
#!/bin/sh
set -eu
while [ "$#" -gt 0 ]; do
  if [ "$1" = -- ]; then shift; exec "$@"; fi
  shift
done
exit 1
SH
cat > "$tmp/bin/forbidden-landlock" <<'SH'
#!/bin/sh
set -eu
printf called > "${LANDLOCK_CALL_MARKER:?}"
exit 99
SH
chmod 755 "$tmp/bin/qwen" "$tmp/bin/landlock" "$tmp/bin/forbidden-landlock"
launcher_renderer="$repo/scripts/security/render-qwen-landlock-launcher-for-test.py"
foundation_renderer="$repo/scripts/security/render-qwen-foundation-for-test.py"
source_foundation="$tmp/source-foundation-test-only"
python3 "$foundation_renderer" "$repo/ops/dgx-runtime-foundation" "$source_foundation" "$repo/scripts/security/qwen-landlock-launcher.sh" "$tmp/home" "$tmp/home/.qwen/.env" "$tmp/bin/landlock" "$tmp/bin/qwen"
PATH="$tmp/bin:$PATH" HIVECREW_CANARY_MODE=1 HIVECREW_AUTH_TOKEN_REF=ref HIVECREW_WORK_ORDER=WO-TEST HIVECREW_QWEN_CHAIN_TRACE="$tmp/trace" AUTH_LOG="$tmp/auth" CHAIN_LOG="$tmp/argv" "$source_foundation/bin/qwen-chain" qwen-hive-qwen malicious\;argv
test "$(paste -sd, "$tmp/trace")" = resolver,runtime-wrapper,qwen-preflight,landlock-launcher
grep -- '--auth-type openai --model qwen3.7-plus --approval-mode plan --max-tool-calls 0 --sandbox malicious;argv' "$tmp/argv"
grep -Fx -- 'openai_api_key_present=true' "$tmp/auth" >/dev/null
grep -Fx -- 'provider_request_count=0' "$tmp/auth" >/dev/null

for override in \
  HIVECREW_QWEN_LAUNCHER \
  HIVECREW_QWEN_REAL_HOME \
  HIVECREW_QWEN_SECRET_FILE \
  HIVECREW_LANDLOCK_EXEC \
  HIVECREW_QWEN_BIN; do
  case_root="$tmp/formal-chain-ambient-$override"
  mkdir -p "$case_root"
  set +e
  env \
    "PATH=$tmp/bin:$PATH" \
    "$override=/bin/true" \
    HIVECREW_CANARY_MODE=1 \
    HIVECREW_AUTH_TOKEN_REF=ref \
    HIVECREW_WORK_ORDER=WO-TEST \
    HIVECREW_QWEN_CHAIN_TRACE="$case_root/trace" \
    VERSION_MARKER="$case_root/qwen-version-called" \
    AUTH_LOG="$case_root/auth" \
    CHAIN_LOG="$case_root/qwen-argv" \
      "$source_foundation/bin/qwen-chain" qwen-hive-qwen safe-arg \
      >"$case_root/stdout" 2>"$case_root/stderr"
  status=$?
  set -e
  test "$status" = 77
  grep -Fx -- 'qwen trust-anchor override rejected' "$case_root/stderr" >/dev/null
  test ! -e "$case_root/qwen-version-called"
  test ! -e "$case_root/auth"
  test ! -e "$case_root/qwen-argv"
  test -f "$case_root/trace"
  if grep -Ex -- 'qwen-preflight|landlock-launcher' "$case_root/trace" >/dev/null; then exit 1; fi
done

if PATH="$tmp/bin:$PATH" HIVECREW_CANARY_MODE=1 HIVECREW_AUTH_TOKEN_REF=ref HIVECREW_WORK_ORDER=WO-TEST CHAIN_LOG="$tmp/argv-auth" "$source_foundation/bin/qwen-chain" qwen-hive-qwen --auth-type oauth >/dev/null 2>&1; then exit 1; fi
test ! -e "$tmp/argv-auth"
if PATH="$tmp/bin:$PATH" HIVECREW_CANARY_MODE=1 HIVECREW_AUTH_TOKEN_REF=ref HIVECREW_WORK_ORDER=WO-TEST CHAIN_LOG="$tmp/argv2" "$source_foundation/bin/qwen-chain" qwen-hive-qwen --max-tool-calls 9 >/dev/null 2>&1; then exit 1; fi
test ! -e "$tmp/argv2"
chmod 644 "$tmp/home/.qwen/.env"
if PATH="$tmp/bin:$PATH" HIVECREW_CANARY_MODE=1 HIVECREW_AUTH_TOKEN_REF=ref HIVECREW_WORK_ORDER=WO-TEST CHAIN_LOG="$tmp/argv3" "$source_foundation/bin/qwen-chain" qwen-hive-qwen safe-arg >/dev/null 2>&1; then exit 1; fi
test ! -e "$tmp/argv3"
chmod 600 "$tmp/home/.qwen/.env"
prefix="$tmp/installed"; backup="$tmp/backup"
HIVECREW_RUNTIME_PREFIX="$prefix" HIVECREW_BACKUP_DIR="$backup" "$repo/ops/dgx-runtime-foundation/bin/install" --apply
installed_foundation="$tmp/installed-foundation-test-only"
python3 "$foundation_renderer" "$prefix" "$installed_foundation" "$prefix/bin/qwen-landlock-launcher.sh" "$tmp/home" "$tmp/home/.qwen/.env" "$tmp/bin/landlock" "$tmp/bin/qwen"
installed_test_launcher="$tmp/bin/qwen-landlock-launcher-installed-test-only"
python3 "$launcher_renderer" "$prefix/bin/qwen-landlock-launcher.sh" "$installed_test_launcher" "$tmp/home" "$tmp/home/.qwen/.env" "$tmp/bin/landlock" "$tmp/bin/qwen"

for override in HIVECREW_QWEN_REAL_HOME HIVECREW_QWEN_SECRET_FILE HIVECREW_LANDLOCK_EXEC HIVECREW_QWEN_BIN; do
  case_root="$tmp/installed-ambient-$override"
  mkdir -p "$case_root"
  set +e
  env "$override=/bin/true" HIVECREW_QWEN_CHAIN_TRACE="$case_root/trace" \
    "$prefix/bin/qwen-landlock-launcher.sh" >"$case_root/stdout" 2>"$case_root/stderr"
  status=$?
  set -e
  test "$status" = 77
  grep -Fx -- 'qwen path override rejected' "$case_root/stderr" >/dev/null
  test ! -e "$case_root/trace"
done

alternate_gid="$(id -G | tr ' ' '\n' | grep -vx "$(id -g)" | head -n 1)"
test -n "$alternate_gid"
for component in secret launcher landlock qwen; do
  case_root="$tmp/installed-wrong-group-$component"
  mkdir -p "$case_root/home/.qwen" "$case_root/run"
  cp "$tmp/home/.qwen/.env" "$case_root/home/.qwen/.env"
  cp "$tmp/bin/landlock" "$case_root/landlock"
  cp "$tmp/bin/qwen" "$case_root/qwen"
  chmod 600 "$case_root/home/.qwen/.env"
  chmod 755 "$case_root/landlock" "$case_root/qwen"
  case_launcher="$case_root/launcher-test-only"
  python3 "$launcher_renderer" "$prefix/bin/qwen-landlock-launcher.sh" "$case_launcher" "$case_root/home" "$case_root/home/.qwen/.env" "$case_root/landlock" "$case_root/qwen"
  case "$component" in
    secret) chgrp "$alternate_gid" "$case_root/home/.qwen/.env"; expected='qwen credential reference invalid' ;;
    launcher) chgrp "$alternate_gid" "$case_launcher"; expected='qwen launcher identity invalid' ;;
    landlock) chgrp "$alternate_gid" "$case_root/landlock"; expected='qwen landlock identity invalid' ;;
    qwen) chgrp "$alternate_gid" "$case_root/qwen"; expected='qwen executable identity invalid' ;;
  esac
  set +e
  (cd "$case_root/run"; HIVECREW_QWEN_CHAIN_TRACE="$case_root/trace" AUTH_LOG="$case_root/auth" CHAIN_LOG="$case_root/qwen-argv" "$case_launcher") >"$case_root/stdout" 2>"$case_root/stderr"
  status=$?
  set -e
  test "$status" = 78
  grep -Fx -- "$expected" "$case_root/stderr" >/dev/null
  test ! -e "$case_root/trace"
  test ! -e "$case_root/auth"
  test ! -e "$case_root/qwen-argv"
done

assert_installed_auth_type_rejected() {
  case_name="$1"
  shift
  case_root="$tmp/installed-auth-${case_name}"
  mkdir -p "$case_root"
  set +e
  PATH="$tmp/bin:$PATH" \
    HIVECREW_CANARY_MODE=1 \
    HIVECREW_AUTH_TOKEN_REF=ref \
    HIVECREW_WORK_ORDER=WO-TEST \
    HIVECREW_QWEN_CHAIN_TRACE="$case_root/trace" \
    LANDLOCK_CALL_MARKER="$case_root/landlock-called" \
    CHAIN_LOG="$case_root/qwen-argv" \
      "$installed_foundation/bin/qwen-chain" qwen-hive-qwen "$@" \
      >"$case_root/stdout" 2>"$case_root/stderr"
  status=$?
  set -e
  test "$status" = 77
  grep -Fx -- 'reserved auth/model/sandbox/tool flag' "$case_root/stderr" >/dev/null
  test ! -e "$case_root/landlock-called"
  test ! -e "$case_root/qwen-argv"
  test -f "$case_root/trace"
  if grep -Fx -- landlock-launcher "$case_root/trace" >/dev/null; then exit 1; fi
}

assert_installed_auth_type_rejected camel-explicit-openai --authType openai
assert_installed_auth_type_rejected camel-inline-openai --authType=openai
assert_installed_auth_type_rejected camel-explicit-other --authType qwen-oauth
assert_installed_auth_type_rejected camel-inline-other --authType=qwen-oauth
assert_installed_auth_type_rejected camel-duplicate --authType openai --authType qwen-oauth
assert_installed_auth_type_rejected camel-missing-value --authType
assert_installed_auth_type_rejected camel-inline-missing-value --authType=

PATH="$tmp/bin:$PATH" HIVECREW_CANARY_MODE=1 HIVECREW_AUTH_TOKEN_REF=ref HIVECREW_WORK_ORDER=WO-TEST AUTH_LOG="$tmp/installed-auth" CHAIN_LOG="$tmp/installed-argv" "$installed_foundation/bin/qwen-chain" qwen-hive-qwen installed-arg
grep -- '--auth-type openai --model qwen3.7-plus --approval-mode plan --max-tool-calls 0 --sandbox installed-arg' "$tmp/installed-argv"
grep -Fx -- 'openai_api_key_present=true' "$tmp/installed-auth" >/dev/null
grep -Fx -- 'provider_request_count=0' "$tmp/installed-auth" >/dev/null
test -x "$prefix/bin/qwen-landlock-launcher.sh"
PATH="$tmp/bin:$PATH" HIVECREW_CANARY_MODE=1 HIVECREW_AUTH_TOKEN_REF=ref HIVECREW_WORK_ORDER=WO-TEST AUTH_LOG="$tmp/daemon-auth" CHAIN_LOG="$tmp/daemon-argv" "$installed_foundation/bin/qwen-hive-qwen" -p prompt --output-format stream-json --model qwen3.7-plus --approval-mode plan --max-tool-calls 0 --sandbox
grep -- '--auth-type openai --model qwen3.7-plus --approval-mode plan --max-tool-calls 0 --sandbox -p prompt --output-format stream-json' "$tmp/daemon-argv"
grep -Fx -- 'openai_api_key_present=true' "$tmp/daemon-auth" >/dev/null
grep -Fx -- 'provider_request_count=0' "$tmp/daemon-auth" >/dev/null

PATH="$tmp/bin:$PATH" HIVECREW_CANARY_MODE=1 HIVECREW_AUTH_TOKEN_REF=ref HIVECREW_WORK_ORDER=WO-TEST AUTH_LOG="$tmp/bounded-auth" CHAIN_LOG="$tmp/bounded-argv" "$installed_foundation/bin/qwen-hive-qwen" -p prompt --output-format stream-json --model qwen3.7-plus --approval-mode plan --max-tool-calls 8 --allowed-tools read_file,glob,grep_search,list_directory --sandbox
bounded_argv="$(cat "$tmp/bounded-argv")"
case "$bounded_argv" in
  *'--auth-type openai --model qwen3.7-plus --approval-mode plan --max-tool-calls 8 --sandbox --safe-mode --allowed-tools read_file,glob,grep_search,list_directory --exclude-tools '*'-p prompt --output-format stream-json') ;;
  *) echo "bounded-read argv mismatch: $bounded_argv" >&2; exit 1 ;;
esac
case "$bounded_argv" in *'--yolo'*) exit 1 ;; esac
for forbidden_tool in write_file edit run_shell_command web_fetch web_search create_sub_session agent; do
  case "$bounded_argv" in *"$forbidden_tool"*) ;; *) echo "bounded-read deny complement missing $forbidden_tool" >&2; exit 1 ;; esac
done
grep -Fx -- 'openai_api_key_present=true' "$tmp/bounded-auth" >/dev/null
grep -Fx -- 'provider_request_count=0' "$tmp/bounded-auth" >/dev/null

PATH="$tmp/bin:$PATH" HIVECREW_CANARY_MODE=1 HIVECREW_AUTH_TOKEN_REF=ref HIVECREW_WORK_ORDER=WO-TEST AUTH_LOG="$tmp/workspace-auth" CHAIN_LOG="$tmp/workspace-argv" "$installed_foundation/bin/qwen-hive-qwen" -p prompt --output-format stream-json --model qwen3.7-plus --approval-mode auto-edit --max-tool-calls 12 --allowed-tools read_file,glob,grep_search,list_directory,edit,write_file --sandbox
workspace_argv="$(cat "$tmp/workspace-argv")"
case "$workspace_argv" in
  *'--auth-type openai --model qwen3.7-plus --approval-mode auto-edit --max-tool-calls 12 --sandbox --allowed-tools read_file,glob,grep_search,list_directory,edit,write_file --exclude-tools '*'-p prompt --output-format stream-json') ;;
  *) echo "bounded-workspace argv mismatch: $workspace_argv" >&2; exit 1 ;;
esac
case "$workspace_argv" in *'--yolo'*|*'--safe-mode'*) exit 1 ;; esac
for forbidden_tool in run_shell_command web_fetch web_search read_mcp_resource create_sub_session agent; do
  case "$workspace_argv" in *"$forbidden_tool"*) ;; *) echo "bounded-workspace deny complement missing $forbidden_tool" >&2; exit 1 ;; esac
done
grep -Fx -- 'openai_api_key_present=true' "$tmp/workspace-auth" >/dev/null
grep -Fx -- 'provider_request_count=0' "$tmp/workspace-auth" >/dev/null

set +e
PATH="$tmp/bin:$PATH" HIVECREW_CANARY_MODE=1 HIVECREW_AUTH_TOKEN_REF=ref HIVECREW_WORK_ORDER=WO-TEST AUTH_LOG="$tmp/bounded-override-auth" CHAIN_LOG="$tmp/bounded-override-argv" "$installed_foundation/bin/qwen-hive-qwen" -p prompt --output-format stream-json --model qwen3.7-plus --approval-mode plan --max-tool-calls 8 --allowed-tools read_file,run_shell_command --sandbox >/dev/null 2>&1
bounded_override_rc=$?
set -e
test "$bounded_override_rc" = 77
test ! -e "$tmp/bounded-override-auth"
test ! -e "$tmp/bounded-override-argv"

set +e
PATH="$tmp/bin:$PATH" HIVECREW_QWEN_TOOL_POLICY=bounded_read HIVECREW_CANARY_MODE=1 HIVECREW_AUTH_TOKEN_REF=ref HIVECREW_WORK_ORDER=WO-TEST AUTH_LOG="$tmp/bounded-env-auth" CHAIN_LOG="$tmp/bounded-env-argv" "$installed_foundation/bin/qwen-hive-qwen" -p prompt --output-format stream-json --model qwen3.7-plus --approval-mode plan --max-tool-calls 8 --allowed-tools read_file,glob,grep_search,list_directory --sandbox >/dev/null 2>"$tmp/bounded-env-stderr"
bounded_env_rc=$?
set -e
test "$bounded_env_rc" = 77
grep -Fx -- 'governed-qwen-policy-env-override' "$tmp/bounded-env-stderr" >/dev/null
test ! -e "$tmp/bounded-env-auth"
test ! -e "$tmp/bounded-env-argv"
test -x "$prefix/bin/qwen-hive-qwen-landlock"
while IFS= read -r candidate; do
  if grep -Fq -- "$synthetic_api_key" "$candidate"; then
    echo "synthetic credential escaped canonical reference: $candidate" >&2
    exit 45
  fi
done < <(find "$tmp" -type f ! -path '*/.qwen/.env' ! -path '*/tests/test-chain.sh' -print)
echo 'qwen-chain-v8=pass daemon_entrypoint_preflight_launcher_qwen_governed_deny_bounded_read_and_bounded_workspace_noshell'
