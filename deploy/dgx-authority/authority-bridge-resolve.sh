#!/bin/sh
set -eu
docker_bin=${DOCKER_BIN:-docker}
container=${1:?backend container required}
project=${2:?compose project required}
gateway=$($docker_bin inspect --format '{{json .NetworkSettings.Networks}}' "$container" | jq -r 'to_entries | map(select(.value.Gateway != null and .value.Gateway != "")) | if length == 1 then .[0].value.Gateway else empty end')
[ -n "$gateway" ] || { echo authority-bridge-gateway-not-unique >&2; exit 78; }
printf '%s' "$gateway" | grep -Eq '^[0-9]+(\.[0-9]+){3}$' || { echo authority-bridge-gateway-not-ipv4 >&2; exit 78; }
case "$gateway" in 0.*|127.*|255.255.255.255) echo authority-bridge-gateway-unsafe >&2; exit 78;; esac
printf '%s\n' "$gateway"
