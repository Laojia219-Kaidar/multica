#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
package_root=$root/../../ops/dgx-staging-package
sh -n "$root"/*bridge*.sh "$package_root"/authority-bridge-*.sh
grep -F 'exec "$root/../../ops/dgx-staging-package/authority-bridge-resolve.sh"' "$root/authority-bridge-resolve.sh" >/dev/null
grep -F 'exec "$root/../../ops/dgx-staging-package/authority-bridge-stop.sh"' "$root/authority-bridge-stop.sh" >/dev/null
grep -F 'NetworkSettings.Networks' "$package_root/authority-bridge-resolve.sh" >/dev/null
grep -F 'authority-bridge-network-identity-mismatch' "$package_root/authority-bridge-resolve.sh" >/dev/null
grep -F 'authority-bridge-port-in-use' "$package_root/authority-bridge-port-check.sh" >/dev/null
grep -F 'authority-bridge-orphan-remains' "$package_root/authority-bridge-stop.sh" >/dev/null
grep -F 'healthcheck:' "$root/docker-compose.loopback-bridge.yml" >/dev/null
grep -F 'user: "65532:65532"' "$root/docker-compose.loopback-bridge.yml" >/dev/null
! grep -Eq 'volumes:|secrets:' "$root/docker-compose.loopback-bridge.yml" >/dev/null
! grep -Eq '^\s*build:' "$root/docker-compose.loopback-bridge.yml" "$package_root/docker-compose.loopback-bridge.yml" >/dev/null
cmp "$root/docker-compose.loopback-bridge.yml" "$package_root/docker-compose.loopback-bridge.yml"
echo authority_loopback_bridge_candidate_assets=PASS
