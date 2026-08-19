#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
pkg=${1:?usage: rollback-staging.sh CANDIDATE_DIR DEPLOY_DIR [REASON]}
deploy_dir=${2:?usage: rollback-staging.sh CANDIDATE_DIR DEPLOY_DIR [REASON]}
reason=${3:-manual}
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
source "$root/common.sh"
project=multica-dgx-ultra
backend_container=${BACKEND_CONTAINER:-multica-dgx-ultra-backend-1}
web_container=${WEB_CONTAINER:-multica-dgx-ultra-frontend-1}
id="$pkg/INTEGRATION-IDENTITY.json"
out="$pkg/receipts"
receipt="$out/ROLLBACK-RECEIPT.json"
override="$out/rollback-compose.override.yaml"

"$root/precheck.sh" "$pkg" >/dev/null
env_file=$(resolve_deploy_env_file "$deploy_dir")
docker_bin=$(resolve_executable "${DOCKER_BIN:-docker}")
$docker_bin compose --env-file "$env_file" -f "$pkg/compose.yaml" -p "$project" config --quiet
mkdir -p "$out"
rm -f -- "$receipt"

backend_ref=$(jq -r .rollback_predecessor.backend.ref "$id")
backend_id=$(jq -r .rollback_predecessor.backend.id "$id")
backend_digest=$(jq -r .rollback_predecessor.backend.digest "$id")
web_ref=$(jq -r .rollback_predecessor.web.ref "$id")
web_id=$(jq -r .rollback_predecessor.web.id "$id")
web_digest=$(jq -r .rollback_predecessor.web.digest "$id")
write_image_override "$backend_ref" "$web_ref" "$override"

$docker_bin compose --env-file "$env_file" -f "$pkg/compose.yaml" -f "$override" -p "$project" \
  up -d --no-deps backend frontend
assert_container_image "$docker_bin" "$backend_container" "$backend_ref" "$backend_id" "$backend_digest"
assert_container_image "$docker_bin" "$web_container" "$web_ref" "$web_id" "$web_digest"

jq -n --arg project "$project" --arg reason "$reason" \
  --arg backend_ref "$backend_ref" --arg backend_id "$backend_id" \
  --arg backend_digest "$backend_digest" --arg web_ref "$web_ref" \
  --arg web_id "$web_id" --arg web_digest "$web_digest" \
  '{schema:"HiveCrewRollbackReceiptV3", compose_project:$project,
    restored_backend:{ref:$backend_ref,id:$backend_id,digest:$backend_digest},
    restored_web:{ref:$web_ref,id:$web_id,digest:$web_digest},
    reason:$reason, exact_predecessor:true, external_verification_required:true,
    secret_values_recorded:false}' > "$receipt"
printf 'rollback=pass receipt=%s\n' "$receipt"
