#!/usr/bin/env bash
set -Eeuo pipefail
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
source "$root/common.sh"

[[ $# -eq 1 ]] || { echo 'usage: authority-bridge-port-check.sh GATEWAY' >&2; exit 64; }
gateway=$1
ss_bin=$(resolve_executable "${SS_BIN:-/usr/bin/ss}")
listeners=$($ss_bin -H -ltn)
if awk '
  {
    endpoint=$4
    sub(/^\[/, "", endpoint); sub(/\]$/, "", endpoint)
    if (endpoint ~ /:3151$/) found=1
  }
  END { exit(found ? 0 : 1) }
' <<<"$listeners"; then
  echo "authority-bridge-port-in-use:$gateway:3151" >&2
  exit 78
fi
printf 'authority_bridge_port_free=%s:3151\n' "$gateway"
