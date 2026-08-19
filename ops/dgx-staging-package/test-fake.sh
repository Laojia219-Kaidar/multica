#!/usr/bin/env bash
set -Eeuo pipefail
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/candidate"
cp "$root"/precheck.sh "$tmp/candidate/"
printf '%s\n' 'name: multica-dgx-ultra' > "$tmp/candidate/compose.yaml"
csha=$(sha256sum "$tmp/candidate/compose.yaml"|awk '{print $1}')
jq -n --arg c "$csha" '{schema:"HiveCrewIntegrationIdentityV1",compose_project:"multica-dgx-ultra",final_revision:("a"*40),final_tree:("b"*40),source_archive_sha256:("c"*64),authority_overlay_sha256:("d"*64),compose_sha256:$c,backend_image:{id:"backend:candidate",digest:("sha256:"+ ("e"*64))},web_image:{id:"web:candidate",digest:("sha256:"+ ("f"*64))},rollback_predecessor:{backend_image:"backend:previous",backend_digest:("sha256:"+ ("1"*64)),web_image:"web:previous",web_digest:("sha256:"+ ("2"*64))}}' > "$tmp/candidate/INTEGRATION-IDENTITY.json"
DOCKER_BIN=true "$tmp/candidate/precheck.sh" "$tmp/candidate" >/dev/null
jq '.compose_project="wrong"' "$tmp/candidate/INTEGRATION-IDENTITY.json" > "$tmp/candidate/bad.json"
mv "$tmp/candidate/bad.json" "$tmp/candidate/INTEGRATION-IDENTITY.json"
if DOCKER_BIN=true "$tmp/candidate/precheck.sh" "$tmp/candidate" >/dev/null 2>&1; then echo bad-fixture-accepted >&2; exit 1; fi
jq -e '."$schema" == "https://json-schema.org/draft/2020-12/schema" and .properties.accepted.const == false and .properties.production_unchanged.const == true' "$root/EXTERNAL-ACCEPTANCE.schema.json" >/dev/null
echo 'fake-static-tests=pass identity_fail_closed schema_draft2020 accepted_false production_unchanged_true docker_calls=0'
