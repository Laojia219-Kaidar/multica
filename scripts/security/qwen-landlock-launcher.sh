#!/usr/bin/env bash
set -euo pipefail

real_home="${HIVECREW_QWEN_REAL_HOME:-$(getent passwd "$(id -u)" | cut -d: -f6)}"
landlock_exec="${HIVECREW_LANDLOCK_EXEC:-${real_home}/.local/libexec/hivecrew-landlock-exec}"
qwen_bin="${HIVECREW_QWEN_BIN:-${real_home}/.local/bin/qwen}"
secret_file="${HIVECREW_QWEN_SECRET_FILE:-${real_home}/.qwen/.env}"

if [[ ! -x "${landlock_exec}" ]]; then
  echo "HiveCrew Landlock executor is unavailable" >&2
  exit 127
fi
if [[ ! -x "${qwen_bin}" ]]; then
  echo "Qwen executable is unavailable" >&2
  exit 127
fi
if [[ ! -f "${secret_file}" || "$(stat -c '%a' "${secret_file}")" != "600" ]]; then
  echo "Qwen credential reference is unavailable or has an unsafe mode" >&2
  exit 78
fi

sandbox_root="$(mktemp -d "/tmp/hivecrew-qwen-landlock.XXXXXX")"
cleanup() {
  rm -rf -- "${sandbox_root}"
}
trap cleanup EXIT HUP INT TERM

sandbox_home="${sandbox_root}/home"
mkdir -p "${sandbox_home}/.qwen" "${sandbox_root}/tmp" "${sandbox_root}/xdg-config" "${sandbox_root}/xdg-cache" "${sandbox_root}/xdg-data"
ln -s "${secret_file}" "${sandbox_home}/.qwen/.env"
chmod 700 "${sandbox_root}" "${sandbox_home}" "${sandbox_home}/.qwen" "${sandbox_root}/tmp"

export HOME="${sandbox_home}"
export TMPDIR="${sandbox_root}/tmp"
export XDG_CONFIG_HOME="${sandbox_root}/xdg-config"
export XDG_CACHE_HOME="${sandbox_root}/xdg-cache"
export XDG_DATA_HOME="${sandbox_root}/xdg-data"
unset QWEN_SANDBOX

set +e
"${landlock_exec}" \
  --write "$(pwd -P)" \
  --write "${sandbox_root}" \
  -- "${qwen_bin}" --model qwen3.7-plus "$@"
status=$?
set -e
exit "${status}"
