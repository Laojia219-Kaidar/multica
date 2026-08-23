#!/usr/bin/env bash
set -Eeuo pipefail
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
source "$root/common.sh"

[[ $# -eq 2 ]] || { echo 'usage: authority-bridge-stop.sh COMPOSE_PROJECT GATEWAY' >&2; exit 64; }
project=$1
gateway=$2
docker_bin=$(resolve_executable "${DOCKER_BIN:-docker}")
ids=$($docker_bin ps -a \
  --filter "label=com.docker.compose.project=$project" \
  --filter 'label=com.docker.compose.service=authority-loopback-bridge' \
  --format '{{.ID}}' | sed '/^$/d')
id_count=$(awk 'NF {count++} END {print count+0}' <<<"$ids")
(( id_count <= 1 )) || { echo authority-bridge-not-unique >&2; exit 78; }
removed=false
removed_id=''
if (( id_count == 1 )); then
  removed_id=$ids
  $docker_bin rm -f "$removed_id" >/dev/null
  removed=true
fi
remaining=$($docker_bin ps -a \
  --filter "label=com.docker.compose.project=$project" \
  --filter 'label=com.docker.compose.service=authority-loopback-bridge' \
  --format '{{.ID}}')
[[ -z "$remaining" ]] || { echo authority-bridge-orphan-remains >&2; exit 78; }
DOCKER_BIN="$docker_bin" "$root/authority-bridge-port-check.sh" "$gateway" >/dev/null
jq -cn --argjson removed "$removed" --arg id "$removed_id" \
  '{authority_bridge_removed:$removed,removed_container_id:$id,orphan_count:0,listener_absent:true}'
