#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
sh -n "$root"/*bridge*.sh
grep -F 'NetworkSettings.Networks' "$root/authority-bridge-resolve.sh" >/dev/null
grep -F 'authority-bridge-gateway-not-unique' "$root/authority-bridge-resolve.sh" >/dev/null
grep -F 'rm -sf authority-loopback-bridge' "$root/authority-bridge-stop.sh" >/dev/null
grep -F 'healthcheck:' "$root/docker-compose.loopback-bridge.yml" >/dev/null
! grep -Eq 'volumes:|secrets:' "$root/docker-compose.loopback-bridge.yml" >/dev/null
echo authority_loopback_bridge_state_machine_fake=PASS
