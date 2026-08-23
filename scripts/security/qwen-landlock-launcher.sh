#!/usr/bin/env bash
set -euo pipefail
real_home="${HIVECREW_QWEN_REAL_HOME:-$(getent passwd "$(id -u)" | cut -d: -f6)}"
landlock_exec="${HIVECREW_LANDLOCK_EXEC:-${real_home}/.local/libexec/hivecrew-landlock-exec}"
qwen_bin="${HIVECREW_QWEN_BIN:-${real_home}/.local/bin/qwen}"
secret_file="${HIVECREW_QWEN_SECRET_FILE:-${real_home}/.qwen/.env}"
[[ "${HIVECREW_QWEN_LANDLOCK_REQUIRED:-1}" == 1 ]] || { echo 'sandbox required' >&2; exit 77; }
[[ -x "$landlock_exec" && -x "$qwen_bin" ]] || exit 127
[[ "$secret_file" == /* && -f "$secret_file" && "$(stat -c '%a' "$secret_file")" == 600 ]] || exit 78
[[ "$(stat -c '%u' "$secret_file")" == "$(id -u)" ]] || exit 78
[[ "$(readlink -f "$secret_file")" == "$real_home/.qwen/.env" ]] || exit 78
for arg in "$@"; do
  case "$arg" in
    --model|--model=*|--approval-mode|--approval-mode=*|--max-tool-calls|--max-tool-calls=*|--sandbox|--no-sandbox|--sandbox=*) echo 'reserved model/sandbox/tool flag' >&2; exit 77 ;;
  esac
done
[[ -z "${HIVECREW_QWEN_CHAIN_TRACE:-}" ]] || printf '%s\n' landlock-launcher >> "$HIVECREW_QWEN_CHAIN_TRACE"
sandbox_root=$(mktemp -d "/tmp/hivecrew-qwen-landlock.XXXXXX")
cleanup(){ rm -rf -- "$sandbox_root"; }
trap cleanup EXIT HUP INT TERM
sandbox_home="$sandbox_root/home"; mkdir -p "$sandbox_home/.qwen" "$sandbox_root/tmp" "$sandbox_root/xdg-config" "$sandbox_root/xdg-cache" "$sandbox_root/xdg-data"
ln -s "$secret_file" "$sandbox_home/.qwen/.env"; chmod 700 "$sandbox_root" "$sandbox_home" "$sandbox_home/.qwen" "$sandbox_root/tmp"
export HOME="$sandbox_home" TMPDIR="$sandbox_root/tmp" XDG_CONFIG_HOME="$sandbox_root/xdg-config" XDG_CACHE_HOME="$sandbox_root/xdg-cache" XDG_DATA_HOME="$sandbox_root/xdg-data"
unset QWEN_SANDBOX
set +e
"$landlock_exec" --write "$(pwd -P)" --write "$sandbox_root" -- "$qwen_bin" --model qwen3.7-plus --approval-mode plan --max-tool-calls 0 --sandbox "$@"
status=$?
set -e
exit "$status"
