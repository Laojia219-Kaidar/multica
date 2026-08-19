#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
[[ $# -eq 1 ]] || {
  echo 'usage: apply-staging.sh CANDIDATE_DIR' >&2
  exit 64
}
pkg=$1
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
source "$root/common.sh"
project=multica-dgx-ultra
backend_container=${BACKEND_CONTAINER:-multica-dgx-ultra-backend-1}
web_container=${WEB_CONTAINER:-multica-dgx-ultra-frontend-1}
id="$pkg/INTEGRATION-IDENTITY.json"
out="$pkg/receipts"
snapshot="$out/PRE-APPLY-SNAPSHOT.json"
receipt="$out/OPERATOR-APPLY-RECEIPT.json"
override="$out/candidate-compose.override.yaml"

"$root/precheck.sh" "$pkg" >/dev/null
env_file=$(resolve_deploy_env_file)
docker_bin=$(resolve_executable "${DOCKER_BIN:-docker}")
curl_bin=$(resolve_executable "${CURL_BIN:-curl}")
counts_bin=$(resolve_executable "${COUNTS_BIN:-$root/collect-readonly-counts.sh}")
readiness_attempts=${HIVECREW_READINESS_ATTEMPTS:-30}
readiness_delay_seconds=${HIVECREW_READINESS_DELAY_SECONDS:-1}
[[ "$readiness_attempts" =~ ^[1-9][0-9]*$ && "$readiness_attempts" -le 120 &&
   "$readiness_delay_seconds" =~ ^[0-9]+$ && "$readiness_delay_seconds" -le 30 ]] || {
  echo invalid-readiness-policy >&2
  exit 78
}
$docker_bin compose --env-file "$env_file" -f "$pkg/compose.yaml" -p "$project" config --quiet
mkdir -p "$out"
rm -f -- "$receipt"

wait_for_health() {
  local attempt health_status
  for ((attempt = 1; attempt <= readiness_attempts; attempt++)); do
    if health_status=$($curl_bin -sS -o /dev/null -w '%{http_code}' --max-time 8 \
        http://127.0.0.1:8080/health); then
      if [[ "$health_status" == 200 ]]; then
        printf '%s\n' "$health_status"
        return 0
      fi
    fi
    if (( attempt < readiness_attempts )); then
      sleep "$readiness_delay_seconds"
    fi
  done
  echo health-readback-mismatch >&2
  return 79
}

OPERATOR_ROLLBACK_STATE=pre_mutation
rollback_on_exit() {
  local exit_rc=$?
  trap - EXIT
  if [[ "$exit_rc" -ne 0 && "${OPERATOR_ROLLBACK_STATE:-uninitialized}" == mutation_started ]]; then
    if ! DOCKER_BIN="$docker_bin" CURL_BIN="$curl_bin" COUNTS_BIN="$counts_bin" \
      "$root/rollback-staging.sh" "$pkg" "automatic-apply-failure-$exit_rc"; then
      exit 80
    fi
  fi
  exit "$exit_rc"
}
trap rollback_on_exit EXIT

counts=$($counts_bin)
printf '%s\n' "$counts" | jq -e '
  .status == "collected_read_only" and
  (.schema_top | type == "number") and
  (.agent_count | type == "number") and
  (.project_count | type == "number")
' >/dev/null || { echo invalid-read-only-counts >&2; exit 79; }

old_backend_ref=$(jq -r .rollback_predecessor.backend.ref "$id")
old_backend_id=$(jq -r .rollback_predecessor.backend.id "$id")
old_backend_digest=$(jq -r .rollback_predecessor.backend.digest "$id")
old_web_ref=$(jq -r .rollback_predecessor.web.ref "$id")
old_web_id=$(jq -r .rollback_predecessor.web.id "$id")
old_web_digest=$(jq -r .rollback_predecessor.web.digest "$id")
assert_container_image "$docker_bin" "$backend_container" "$old_backend_ref" "$old_backend_id" "$old_backend_digest"
assert_container_image "$docker_bin" "$web_container" "$old_web_ref" "$old_web_id" "$old_web_digest"

before=$($docker_bin ps --filter "label=com.docker.compose.project=$project" \
  --format '{{.Names}}\t{{.ID}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}')
containers=$(printf '%s\n' "$before" | jq -Rsc '
  split("\n") | map(select(length > 0)) |
  map(split("\t") | {name:.[0], id:.[1], image:.[2], status:.[3], ports:.[4]})
')
compose_hash=$(sha256sum "$pkg/compose.yaml" | awk '{print $1}')
jq -n --arg project "$project" --arg compose_hash "$compose_hash" \
  --arg identity_path "$id" --argjson containers "$containers" \
  --argjson counts "$counts" --argjson predecessor "$(jq -c .rollback_predecessor "$id")" \
  '{schema:"HiveCrewPreApplySnapshotV3", compose_project:$project,
    compose_config_sha256:$compose_hash, containers:$containers,
    schema_read_only_counts:$counts, rollback_artifact_path:$identity_path,
    rollback_predecessor:$predecessor, mutation_started:false,
    secret_values_recorded:false}' > "$snapshot"

backend_ref=$(jq -r .backend_image.ref "$id")
backend_id=$(jq -r .backend_image.id "$id")
backend_digest=$(jq -r .backend_image.digest "$id")
web_ref=$(jq -r .web_image.ref "$id")
web_id=$(jq -r .web_image.id "$id")
web_digest=$(jq -r .web_image.digest "$id")
write_image_override "$backend_ref" "$web_ref" "$override"

OPERATOR_ROLLBACK_STATE=mutation_started
$docker_bin compose --env-file "$env_file" -f "$pkg/compose.yaml" -f "$override" -p "$project" \
  up -d --no-deps backend frontend
assert_container_image "$docker_bin" "$backend_container" "$backend_ref" "$backend_id" "$backend_digest"
assert_container_image "$docker_bin" "$web_container" "$web_ref" "$web_id" "$web_digest"

health=$(wait_for_health)
config=$($curl_bin -fsS --max-time 8 http://127.0.0.1:8080/api/config)
actual_version=$(printf '%s\n' "$config" | jq -er .server_version)
expected_version=$(jq -r .api.server_version "$id")
[[ "$actual_version" == "$expected_version" ]] || { echo version-readback-mismatch >&2; exit 79; }

jq -n --arg project "$project" --arg revision "$(jq -r .final_revision "$id")" \
  --arg tree "$(jq -r .final_tree "$id")" --arg backend_ref "$backend_ref" \
  --arg backend_id "$backend_id" --arg backend_digest "$backend_digest" \
  --arg web_ref "$web_ref" --arg web_id "$web_id" --arg web_digest "$web_digest" \
  --arg version "$actual_version" --argjson counts "$counts" \
  '{schema:"HiveCrewOperatorApplyReceiptV3", compose_project:$project,
    final_revision:$revision, final_tree:$tree,
    backend:{ref:$backend_ref,id:$backend_id,digest:$backend_digest},
    web:{ref:$web_ref,id:$web_id,digest:$web_digest},
    health_http:200, server_version:$version, schema_read_only_counts:$counts,
    pre_apply_snapshot:"PRE-APPLY-SNAPSHOT.json",
    rollback_receipt:"ROLLBACK-RECEIPT.json",
    external_acceptance:"EXTERNAL-ACCEPTANCE.json",
    secret_values_recorded:false, production_applied:false, run_06:false}' > "$receipt"
OPERATOR_ROLLBACK_STATE=complete
trap - EXIT
printf 'apply=pass receipt=%s\n' "$receipt"
