#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
pkg=${1:?usage: rollback-staging.sh CANDIDATE_DIR}; reason=${2:-automatic}
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
project=multica-dgx-ultra
id="$pkg/INTEGRATION-IDENTITY.json"
out="$pkg/receipts"
mkdir -p "$out"
"$root/precheck.sh" "$pkg"
docker_bin=${DOCKER_BIN:-docker}
oldb=$(jq -r .rollback_predecessor.backend_image "$id")
oldw=$(jq -r .rollback_predecessor.web_image "$id")
oldbd=$(jq -r .rollback_predecessor.backend_digest "$id")
oldwd=$(jq -r .rollback_predecessor.web_digest "$id")
cat > "$out/rollback-compose.override.yaml" <<EOF
services:
  backend:
    image: $oldb
  frontend:
    image: $oldw
EOF
$docker_bin compose -f "$pkg/compose.yaml" -f "$out/rollback-compose.override.yaml" -p "$project" up -d --no-deps backend frontend
br=$($docker_bin inspect --format '{{.Config.Image}}' "$oldb" 2>/dev/null || true)
wr=$($docker_bin inspect --format '{{.Config.Image}}' "$oldw" 2>/dev/null || true)
bd=$($docker_bin inspect --format '{{index .RepoDigests 0}}' "$oldb" 2>/dev/null || true)
wd=$($docker_bin inspect --format '{{index .RepoDigests 0}}' "$oldw" 2>/dev/null || true)
[[ "$br" == "$oldb" && "$wr" == "$oldw" && "$bd" == "$oldbd" && "$wd" == "$oldwd" ]] || { echo rollback-readback-mismatch >&2; exit 79; }
jq -n --arg p "$project" --arg b "$br" --arg w "$wr" --arg bd "$bd" --arg wd "$wd" --arg r "$reason" '{schema:"HiveCrewRollbackReceiptV2",compose_project:$p,restored_backend:{id:$b,digest:$bd},restored_web:{id:$w,digest:$wd},reason:$r,exact_predecessor:true,external_verification_required:true,secret_values_recorded:false}' > "$out/ROLLBACK-RECEIPT.json"
printf '%s\n' "rollback=pass receipt=$out/ROLLBACK-RECEIPT.json"
