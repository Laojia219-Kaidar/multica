#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
pkg=${1:?usage: rollback-staging.sh CANDIDATE_DIR}; reason=${2:-automatic}
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd); id="$pkg/INTEGRATION-IDENTITY.json"; out="$pkg/receipts"; mkdir -p "$out"
"$root/precheck.sh" "$pkg"
docker_bin=${DOCKER_BIN:-docker}; project=multica-dgx-ultra
old_backend=$(jq -r .rollback_predecessor.backend_image "$id"); old_web=$(jq -r .rollback_predecessor.web_image "$id")
[[ -n "$old_backend" && -n "$old_web" ]] || exit 78
"$docker_bin" compose -f "$pkg/compose.yaml" -p "$project" up -d --no-deps backend frontend
jq -n --arg p "$project" --arg b "$old_backend" --arg w "$old_web" --arg r "$reason" '{schema:"HiveCrewRollbackReceiptV1",compose_project:$p,restored_backend_image:$b,restored_web_image:$w,reason:$r,external_verification_required:true,secret_values_recorded:false}' > "$out/ROLLBACK-RECEIPT.json"
printf 'rollback=pass receipt=%s\n' "$out/ROLLBACK-RECEIPT.json"
