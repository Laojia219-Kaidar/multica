#!/usr/bin/env bash
set -Eeuo pipefail
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
source "$root/common.sh"

[[ $# -eq 2 ]] || {
  echo 'usage: authority-bridge-resolve.sh BACKEND_CONTAINER COMPOSE_PROJECT' >&2
  exit 64
}
container=$1
project=$2
docker_bin=$(resolve_executable "${DOCKER_BIN:-docker}")

container_project=$($docker_bin inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "$container")
container_service=$($docker_bin inspect --format '{{index .Config.Labels "com.docker.compose.service"}}' "$container")
[[ "$container_project" == "$project" ]] || { echo authority-bridge-project-mismatch >&2; exit 78; }
[[ "$container_service" == backend ]] || { echo authority-bridge-service-mismatch >&2; exit 78; }

attachment=$($docker_bin inspect --format '{{json .NetworkSettings.Networks}}' "$container" | jq -cer '
  to_entries | map(select(.value.NetworkID != null and .value.NetworkID != "" and .value.Gateway != null and .value.Gateway != "")) |
  if length == 1 then {name:.[0].key,id:.[0].value.NetworkID,gateway:.[0].value.Gateway} else error("network-not-unique") end
') || { echo authority-bridge-network-not-unique >&2; exit 78; }
network_name=$(jq -r .name <<<"$attachment")
network_id=$(jq -r .id <<<"$attachment")
container_gateway=$(jq -r .gateway <<<"$attachment")
[[ "$network_name" == "${project}_default" ]] || { echo authority-bridge-network-name-mismatch >&2; exit 78; }

network_json=$($docker_bin network inspect "$network_name")
jq -e --arg project "$project" --arg id "$network_id" --arg gateway "$container_gateway" '
  length == 1 and
  .[0].Id == $id and .[0].Driver == "bridge" and .[0].Scope == "local" and
  .[0].Labels["com.docker.compose.project"] == $project and
  ([.[0].IPAM.Config[]? | select(.Gateway == $gateway)] | length) == 1
' <<<"$network_json" >/dev/null || { echo authority-bridge-network-identity-mismatch >&2; exit 78; }

# The governed staging network is dynamically allocated from Docker's 172.16/12
# bridge space. Refuse LAN, Tailnet, loopback, wildcard and stale host addresses.
jq -en --arg gateway "$container_gateway" '
  ($gateway | capture("^(?<a>[0-9]+)\\.(?<b>[0-9]+)\\.(?<c>[0-9]+)\\.(?<d>[0-9]+)$")) as $p |
  ($p.a|tonumber) == 172 and ($p.b|tonumber) >= 16 and ($p.b|tonumber) <= 31 and
  ($p.c|tonumber) >= 0 and ($p.c|tonumber) <= 255 and
  ($p.d|tonumber) >= 1 and ($p.d|tonumber) <= 254
' >/dev/null || { echo authority-bridge-gateway-unsafe >&2; exit 78; }

jq -cn --arg name "$network_name" --arg id "$network_id" --arg gateway "$container_gateway" \
  '{network_name:$name,network_id:$id,gateway:$gateway}'
