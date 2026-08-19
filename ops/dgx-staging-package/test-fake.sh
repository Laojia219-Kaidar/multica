#!/usr/bin/env bash
set -Eeuo pipefail
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd); tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/candidate"
cp "$root/precheck.sh" "$tmp/candidate/"
printf 'name: multica-dgx-ultra\n' > "$tmp/candidate/compose.yaml"
csha=$(sha256sum "$tmp/candidate/compose.yaml"|awk '{print $1}')
jq -n --arg c "$csha" '{schema:"HiveCrewIntegrationIdentityV1",compose_project:"multica-dgx-ultra",final_revision:("a"*40),final_tree:("b"*64),source_archive_sha256:("c"*64),backend_image:{id:"backend:candidate",digest:("sha256:"+ ("d"*64))},web_image:{id:"web:candidate",digest:("sha256:"+ ("e"*64))},authority_overlay_sha256:("f"*64),compose_sha256:$c,rollback_predecessor:{backend_image:"backend:previous",web_image:"web:previous"}}' > "$tmp/candidate/INTEGRATION-IDENTITY.json"
DOCKER_BIN=true "$tmp/candidate/precheck.sh" "$tmp/candidate" >/dev/null
jq '.compose_project="wrong"' "$tmp/candidate/INTEGRATION-IDENTITY.json" > "$tmp/candidate/bad.json"; mv "$tmp/candidate/bad.json" "$tmp/candidate/INTEGRATION-IDENTITY.json"
if DOCKER_BIN=true "$tmp/candidate/precheck.sh" "$tmp/candidate" >/dev/null 2>&1; then echo 'bad fixture accepted' >&2; exit 1; fi
echo 'fake-precheck-tests=pass docker_calls=0 service_calls=0'
