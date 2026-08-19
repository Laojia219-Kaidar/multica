#!/bin/sh
set -eu
docker_bin=${DOCKER_BIN:-docker}
project=${1:?compose project required}
compose=${2:?compose file required}
deploy_env=${3:?env file required}
"$docker_bin" compose --env-file "$deploy_env" -f "$compose" -p "$project" rm -sf authority-loopback-bridge >/dev/null
remaining=$($docker_bin ps -a --filter "label=com.docker.compose.project=$project" --filter "label=com.docker.compose.service=authority-loopback-bridge" --format '{{.ID}}')
[ -z "$remaining" ] || { echo authority-bridge-orphan-remains >&2; exit 78; }
printf 'authority_bridge_removed=true\n'
