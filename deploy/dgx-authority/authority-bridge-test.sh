#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
test -s "$root/Dockerfile.loopback-bridge"
test -s "$root/docker-compose.loopback-bridge.yml"
grep -F 'network_mode: host' "$root/docker-compose.loopback-bridge.yml" >/dev/null
grep -F 'HIVECOSM_AUTHORITY_BRIDGE_BIND_ADDR' "$root/docker-compose.loopback-bridge.yml" >/dev/null
! grep -Eq '0\.0\.0\.0|127\.0\.0\.1|localhost' "$root/docker-compose.loopback-bridge.yml" >/dev/null
grep -F 'net.Listen("tcp4",net.JoinHostPort(bind,*port))' "$root/../../server/cmd/authority-loopback-bridge/main.go" >/dev/null
grep -F 'net.Dial("tcp4",t)' "$root/../../server/cmd/authority-loopback-bridge/main.go" >/dev/null
echo authority_loopback_bridge_fake_static=PASS
