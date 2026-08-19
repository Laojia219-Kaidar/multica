#!/bin/sh
set -eu
docker_bin=${DOCKER_BIN:-docker}
project=${1:?compose project required}
compose=${2:?compose file required}
deploy_env=${3:?env file required}
"$docker_bin" compose --env-file "$deploy_env" -f "$compose" -p "$project" rm -sf authority-loopback-bridge >/dev/null 2>&1 || true
printf 'authority_bridge_removed=true\n'
