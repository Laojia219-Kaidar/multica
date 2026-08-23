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
$docker_bin compose --env-file "$env_file" -f "$pkg/compose.yaml" -p "$project" config --quiet
readiness_attempts=${HIVECREW_READINESS_ATTEMPTS:-30}
readiness_delay_seconds=${HIVECREW_READINESS_DELAY_SECONDS:-1}
bridge_resolver=$root/authority-bridge-resolve.sh
bridge_port_check=$root/authority-bridge-port-check.sh
bridge_compose=$root/docker-compose.loopback-bridge.yml
[[ "$readiness_attempts" =~ ^[1-9][0-9]*$ && "$readiness_attempts" -le 120 &&
   "$readiness_delay_seconds" =~ ^[0-9]+$ && "$readiness_delay_seconds" -le 30 ]] || {
  echo invalid-readiness-policy >&2
  exit 78
}
mkdir -p "$out"
rm -f -- "$receipt"

backend_ref=$(jq -r .backend_image.ref "$id")
backend_id=$(jq -r .backend_image.id "$id")
backend_digest=$(jq -r .backend_image.digest "$id")
web_ref=$(jq -r .web_image.ref "$id")
web_id=$(jq -r .web_image.id "$id")
web_digest=$(jq -r .web_image.digest "$id")
write_image_override "$backend_ref" "$web_ref" "$override"

bridge_info=$(DOCKER_BIN="$docker_bin" "$bridge_resolver" "$backend_container" "$project")
bridge_gateway=$(jq -er .gateway <<<"$bridge_info")
bridge_network_name=$(jq -er .network_name <<<"$bridge_info")
bridge_network_id=$(jq -er .network_id <<<"$bridge_info")
DOCKER_BIN="$docker_bin" "$bridge_port_check" "$bridge_gateway" >/dev/null
export HIVECOSM_AUTHORITY_BRIDGE_BIND_ADDR="$bridge_gateway"
export HIVECREW_BACKEND_IMAGE="$backend_ref"
$docker_bin compose --env-file "$env_file" -f "$pkg/compose.yaml" -f "$bridge_compose" -f "$override" \
  -p "$project" config --quiet

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
  --arg bridge_gateway "$bridge_gateway" --arg bridge_network_name "$bridge_network_name" \
  --arg bridge_network_id "$bridge_network_id" \
  '{schema:"HiveCrewPreApplySnapshotV3", compose_project:$project,
    compose_config_sha256:$compose_hash, containers:$containers,
    schema_read_only_counts:$counts, rollback_artifact_path:$identity_path,
    rollback_predecessor:$predecessor, mutation_started:false,
    authority_bridge:{network_name:$bridge_network_name,network_id:$bridge_network_id,
      gateway:$bridge_gateway,bind:($bridge_gateway+":3151"),target:"127.0.0.1:3150"},
    secret_values_recorded:false}' > "$snapshot"

OPERATOR_ROLLBACK_STATE=mutation_started
$docker_bin compose --env-file "$env_file" -f "$pkg/compose.yaml" -f "$bridge_compose" -f "$override" -p "$project" \
  up -d --no-deps authority-loopback-bridge
bridge_container_output=$($docker_bin ps --filter "label=com.docker.compose.project=$project" \
  --filter "label=com.docker.compose.service=authority-loopback-bridge" --format '{{.ID}}' | sed '/^$/d')
bridge_container_count=$(awk 'NF {count++} END {print count+0}' <<<"$bridge_container_output")
(( bridge_container_count == 1 )) || { echo authority-bridge-container-not-unique >&2; exit 79; }
bridge_container=$bridge_container_output
[[ "$bridge_container" =~ ^[a-f0-9]{12,64}$ ]] || { echo authority-bridge-container-invalid >&2; exit 79; }
for ((attempt=1; attempt<=readiness_attempts; attempt++)); do
  health=$($docker_bin inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$bridge_container")
  [[ "$health" == healthy ]] && break
  (( attempt < readiness_attempts )) && sleep "$readiness_delay_seconds"
done
[[ "$health" == healthy ]] || { echo authority-bridge-health-failed >&2; exit 79; }
assert_container_image "$docker_bin" "$bridge_container" "$backend_ref" "$backend_id" "$backend_digest"
bridge_http=$($curl_bin -sS -o /dev/null -w '%{http_code}' --max-time 8 \
  "http://${bridge_gateway}:3151/bff/health")
[[ "$bridge_http" == 200 ]] || { echo authority-bridge-http-readback-mismatch >&2; exit 79; }
$docker_bin compose --env-file "$env_file" -f "$pkg/compose.yaml" -f "$bridge_compose" -f "$override" -p "$project" \
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
  --arg bridge_container "$bridge_container" --arg bridge_gateway "$bridge_gateway" \
  --arg bridge_network_name "$bridge_network_name" --arg bridge_network_id "$bridge_network_id" \
  '{schema:"HiveCrewOperatorApplyReceiptV3", compose_project:$project,
    final_revision:$revision, final_tree:$tree,
    backend:{ref:$backend_ref,id:$backend_id,digest:$backend_digest},
    web:{ref:$web_ref,id:$web_id,digest:$web_digest},
    authority_bridge:{container_id:$bridge_container,image_ref:$backend_ref,image_id:$backend_id,
      network_name:$bridge_network_name,network_id:$bridge_network_id,gateway:$bridge_gateway,
      bind:($bridge_gateway+":3151"),target:"127.0.0.1:3150",health:"healthy",health_http:200},
    health_http:200, server_version:$version, schema_read_only_counts:$counts,
    pre_apply_snapshot:"PRE-APPLY-SNAPSHOT.json",
    rollback_receipt:"ROLLBACK-RECEIPT.json",
    external_acceptance:"EXTERNAL-ACCEPTANCE.json",
    secret_values_recorded:false, production_applied:false, run_06:false}' > "$receipt"
OPERATOR_ROLLBACK_STATE=complete
trap - EXIT
printf 'apply=pass receipt=%s\n' "$receipt"
