#!/usr/bin/env bash
set -Eeuo pipefail
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH= cd -- "$root/../../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/home/.qwen" "$tmp/bin"; chmod 700 "$tmp/home" "$tmp/home/.qwen"
printf '%s\n' placeholder > "$tmp/home/.qwen/.env"; chmod 600 "$tmp/home/.qwen/.env"
cat > "$tmp/bin/qwen" <<'SH'
#!/bin/sh
set -eu
if [ "${1:-}" = --version ]; then printf 'qwen 0.21.14\n'; exit 0; fi
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
chmod 755 "$tmp/bin/qwen" "$tmp/bin/landlock"
PATH="$tmp/bin:$PATH" HIVECREW_CANARY_MODE=1 HIVECREW_AUTH_TOKEN_REF=ref HIVECREW_WORK_ORDER=WO-TEST HIVECREW_QWEN_REAL_HOME="$tmp/home" HIVECREW_QWEN_BIN="$tmp/bin/qwen" HIVECREW_QWEN_SECRET_FILE="$tmp/home/.qwen/.env" HIVECREW_LANDLOCK_EXEC="$tmp/bin/landlock" HIVECREW_QWEN_CHAIN_TRACE="$tmp/trace" CHAIN_LOG="$tmp/argv" "$repo/ops/dgx-runtime-foundation/bin/qwen-chain" qwen-hive-qwen malicious\;argv
test "$(paste -sd, "$tmp/trace")" = resolver,runtime-wrapper,qwen-preflight,landlock-launcher
grep -- '--model qwen3.7-plus --approval-mode plan --max-tool-calls 0 --sandbox malicious;argv' "$tmp/argv"
if PATH="$tmp/bin:$PATH" HIVECREW_CANARY_MODE=1 HIVECREW_AUTH_TOKEN_REF=ref HIVECREW_WORK_ORDER=WO-TEST HIVECREW_QWEN_REAL_HOME="$tmp/home" HIVECREW_QWEN_BIN="$tmp/bin/qwen" HIVECREW_QWEN_SECRET_FILE="$tmp/home/.qwen/.env" HIVECREW_LANDLOCK_EXEC="$tmp/bin/landlock" CHAIN_LOG="$tmp/argv2" "$repo/ops/dgx-runtime-foundation/bin/qwen-chain" qwen-hive-qwen --max-tool-calls 9 >/dev/null 2>&1; then exit 1; fi
test ! -e "$tmp/argv2"
chmod 644 "$tmp/home/.qwen/.env"
if PATH="$tmp/bin:$PATH" HIVECREW_CANARY_MODE=1 HIVECREW_AUTH_TOKEN_REF=ref HIVECREW_WORK_ORDER=WO-TEST HIVECREW_QWEN_REAL_HOME="$tmp/home" HIVECREW_QWEN_BIN="$tmp/bin/qwen" HIVECREW_QWEN_SECRET_FILE="$tmp/home/.qwen/.env" HIVECREW_LANDLOCK_EXEC="$tmp/bin/landlock" CHAIN_LOG="$tmp/argv3" "$repo/ops/dgx-runtime-foundation/bin/qwen-chain" qwen-hive-qwen safe-arg >/dev/null 2>&1; then exit 1; fi
test ! -e "$tmp/argv3"
chmod 600 "$tmp/home/.qwen/.env"
prefix="$tmp/installed"; backup="$tmp/backup"
HIVECREW_RUNTIME_PREFIX="$prefix" HIVECREW_BACKUP_DIR="$backup" "$repo/ops/dgx-runtime-foundation/bin/install" --apply
PATH="$tmp/bin:$PATH" HIVECREW_CANARY_MODE=1 HIVECREW_AUTH_TOKEN_REF=ref HIVECREW_WORK_ORDER=WO-TEST HIVECREW_QWEN_REAL_HOME="$tmp/home" HIVECREW_QWEN_BIN="$tmp/bin/qwen" HIVECREW_QWEN_SECRET_FILE="$tmp/home/.qwen/.env" HIVECREW_LANDLOCK_EXEC="$tmp/bin/landlock" CHAIN_LOG="$tmp/installed-argv" "$prefix/bin/qwen-chain" qwen-hive-qwen installed-arg
grep -- '--model qwen3.7-plus --approval-mode plan --max-tool-calls 0 --sandbox installed-arg' "$tmp/installed-argv"
test -x "$prefix/bin/qwen-landlock-launcher.sh"
PATH="$tmp/bin:$PATH" HIVECREW_CANARY_MODE=1 HIVECREW_AUTH_TOKEN_REF=ref HIVECREW_WORK_ORDER=WO-TEST HIVECREW_QWEN_REAL_HOME="$tmp/home" HIVECREW_QWEN_BIN="$tmp/bin/qwen" HIVECREW_QWEN_SECRET_FILE="$tmp/home/.qwen/.env" HIVECREW_LANDLOCK_EXEC="$tmp/bin/landlock" CHAIN_LOG="$tmp/daemon-argv" "$prefix/bin/qwen-hive-qwen" -p prompt --output-format stream-json --model qwen3.7-plus --approval-mode plan --max-tool-calls 0 --sandbox
grep -- '--model qwen3.7-plus --approval-mode plan --max-tool-calls 0 --sandbox -p prompt --output-format stream-json' "$tmp/daemon-argv"
test -x "$prefix/bin/qwen-hive-qwen-landlock"
echo 'qwen-chain-v6=pass daemon_entrypoint_preflight_launcher_qwen_no_tool_sandbox'
