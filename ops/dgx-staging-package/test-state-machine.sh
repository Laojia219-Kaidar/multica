#!/usr/bin/env bash
set -Eeuo pipefail
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tmp=$(realpath "$(mktemp -d)")
trap 'rm -rf "$tmp"' EXIT
governed_live_deploy=/srv/hivecosm/52-staging/multica-dgx-ultra/4ab2c72c27e0ecf38f32cd3f6f1274350a80efca
governed_test_deploy="$tmp/deploy-governed"
operator_root="$tmp/operator"
mkdir -p "$operator_root"
for script in common.sh precheck.sh apply-staging.sh rollback-staging.sh \
  authority-bridge-resolve.sh authority-bridge-port-check.sh authority-bridge-stop.sh; do
  cp -p -- "$root/$script" "$operator_root/$script"
done
cp -p -- "$root/docker-compose.loopback-bridge.yml" "$operator_root/docker-compose.loopback-bridge.yml"
python3 - "$operator_root/common.sh" "$governed_live_deploy" "$governed_test_deploy" <<'PY'
import pathlib, sys
path = pathlib.Path(sys.argv[1])
live, test = sys.argv[2:]
source = path.read_text()
if source.count(live) != 1:
    raise SystemExit("governed deploy constant missing or duplicated")
path.write_text(source.replace(live, test))
PY
grep -F "readonly GOVERNED_STAGING_DEPLOY_DIR=$governed_live_deploy" "$root/common.sh" >/dev/null
if rg -n 'deploy_dir=.*\$\{|deploy_dir=.*:-|resolve_deploy_env_file "?\$|AUTHORITY_BRIDGE_(RESOLVER|COMPOSE|STOP)' \
    "$root/common.sh" "$root/apply-staging.sh" "$root/rollback-staging.sh" >/dev/null; then
  echo governed-deploy-path-is-overridable >&2
  exit 1
fi

candidate_backend_ref=backend:candidate
candidate_backend_id=sha256:$(printf 'e%.0s' {1..64})
candidate_web_ref=web:candidate
candidate_web_id=sha256:$(printf 'f%.0s' {1..64})
previous_backend_ref=multica-backend:dgx-ultra-20260819-353b16c3
previous_backend_id=sha256:f6c5050276263266cf4c08954694d2022f60b942716d12f40f4ef5da2599649a
previous_web_ref=multica-web:dgx-ultra-20260819-4ab2c72c2
previous_web_id=sha256:53b41218f7e0fe2ad3dfa63e9e40a30a21a14a7150579471d107fecc4f861d55
network_id=${FAKE_NETWORK_ID:-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef}
network_gateway=${FAKE_GATEWAY:-172.24.0.1}
network_driver=${FAKE_NETWORK_DRIVER:-bridge}
network_scope=${FAKE_NETWORK_SCOPE:-local}
network_project=${FAKE_NETWORK_PROJECT:-multica-dgx-ultra}
container_project=${FAKE_CONTAINER_PROJECT:-multica-dgx-ultra}
container_service=${FAKE_CONTAINER_SERVICE:-backend}
revision=$(printf 'a%.0s' {1..40})

new_candidate() {
  local name=${1:?name required} dir="$tmp/$1"
  mkdir -p "$dir"
  printf '%s\n' 'services:' '  backend: {}' '  frontend: {}' > "$dir/compose.yaml"
  cp "$dir/compose.yaml" "$dir/rollback-compose.yaml"
  local compose_sha rollback_compose_sha
  compose_sha=$(sha256sum "$dir/compose.yaml" | awk '{print $1}')
  rollback_compose_sha=$(sha256sum "$dir/rollback-compose.yaml" | awk '{print $1}')
  local resolver_sha port_check_sha stop_sha bridge_compose_sha
  resolver_sha=$(sha256sum "$operator_root/authority-bridge-resolve.sh" | awk '{print $1}')
  port_check_sha=$(sha256sum "$operator_root/authority-bridge-port-check.sh" | awk '{print $1}')
  stop_sha=$(sha256sum "$operator_root/authority-bridge-stop.sh" | awk '{print $1}')
  bridge_compose_sha=$(sha256sum "$operator_root/docker-compose.loopback-bridge.yml" | awk '{print $1}')
  jq -n --arg compose_sha "$compose_sha" --arg revision "$revision" \
    --arg resolver_sha "$resolver_sha" --arg port_check_sha "$port_check_sha" \
    --arg stop_sha "$stop_sha" --arg bridge_compose_sha "$bridge_compose_sha" \
    --arg cbref "$candidate_backend_ref" --arg cbid "$candidate_backend_id" \
    --arg cwref "$candidate_web_ref" --arg cwid "$candidate_web_id" \
    --arg pbref "$previous_backend_ref" --arg pbid "$previous_backend_id" \
    --arg pwref "$previous_web_ref" --arg pwid "$previous_web_id" \
    --arg rollback_compose_sha "$rollback_compose_sha" \
    '{schema:"HiveCrewIntegrationIdentityV3", compose_project:"multica-dgx-ultra",
      final_revision:$revision, final_tree:("b"*40), source_archive_sha256:("c"*64),
      authority_overlay_sha256:("d"*64), compose_sha256:$compose_sha,
      authority_bridge:{resolver_sha256:$resolver_sha,port_check_sha256:$port_check_sha,
        stop_sha256:$stop_sha,compose_sha256:$bridge_compose_sha,
        binary_path:"/app/authority-loopback-bridge",bind_port:3151,target:"127.0.0.1:3150"},
      api:{server_version:"candidate-version"},
      backend_image:{ref:$cbref,id:$cbid,digest:$cbid},
      web_image:{ref:$cwref,id:$cwid,digest:$cwid},
      rollback_predecessor:{backend:{ref:$pbref,id:$pbid,digest:$pbid},web:{ref:$pwref,id:$pwid,digest:$pwid},compose_sha256:$rollback_compose_sha}}' \
      > "$dir/INTEGRATION-IDENTITY.json"
  printf '%s\n' "$dir"
}

new_deploy() {
  local name=${1:?name required} dir="$tmp/deploy-$1"
  mkdir -p "$dir"
  printf '%s\n' \
    'POSTGRES_DB=multica' 'POSTGRES_USER=multica' 'POSTGRES_PASSWORD=fake-db-only' \
    'JWT_SECRET=fake-jwt-only' 'MULTICA_DEV_VERIFICATION_CODE=000000' \
    > "$dir/.env"
  printf '%s\n' "$dir"
}

cat > "$tmp/docker" <<'DOCKER'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%q ' "$@" >> "${FAKE_DOCKER_LOG:?}"
printf '\n' >> "$FAKE_DOCKER_LOG"
state=$(cat "${FAKE_STATE:?}")
bridge_state=$(cat "${FAKE_BRIDGE_STATE:?}")
candidate_backend_id="sha256:$(printf 'e%.0s' {1..64})"
candidate_web_id="sha256:$(printf 'f%.0s' {1..64})"
previous_backend_id=sha256:f6c5050276263266cf4c08954694d2022f60b942716d12f40f4ef5da2599649a
previous_web_id=sha256:53b41218f7e0fe2ad3dfa63e9e40a30a21a14a7150579471d107fecc4f861d55
network_id=${FAKE_NETWORK_ID:-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef}
network_gateway=${FAKE_GATEWAY:-172.24.0.1}
network_driver=${FAKE_NETWORK_DRIVER:-bridge}
network_scope=${FAKE_NETWORK_SCOPE:-local}
network_project=${FAKE_NETWORK_PROJECT:-multica-dgx-ultra}
container_project=${FAKE_CONTAINER_PROJECT:-multica-dgx-ultra}
container_service=${FAKE_CONTAINER_SERVICE:-backend}
case "${1:-}" in
  ps)
    if [[ "$*" == *'com.docker.compose.service=authority-loopback-bridge'* ]]; then
      [[ "$bridge_state" == present ]] && printf 'abc123def456\n'
      exit 0
    fi
    if [[ "$state" == candidate ]]; then
      printf 'multica-dgx-ultra-backend-1\tb1\tbackend:candidate\tUp\t127.0.0.1:8080->8080/tcp\n'
      printf 'multica-dgx-ultra-frontend-1\tw1\tweb:candidate\tUp\t127.0.0.1:3000->3000/tcp\n'
    else
      printf 'multica-dgx-ultra-backend-1\tb0\tmultica-backend:dgx-ultra-20260819-353b16c3\tUp\t127.0.0.1:8080->8080/tcp\n'
      printf 'multica-dgx-ultra-frontend-1\tw0\tmultica-web:dgx-ultra-20260819-4ab2c72c2\tUp\t127.0.0.1:3000->3000/tcp\n'
    fi
    ;;
  compose)
    env_file=''
    override=''
    previous=''
    for arg in "$@"; do
      if [[ "$previous" == --env-file ]]; then env_file=$arg; fi
      if [[ "$previous" == -f ]]; then override=$arg; fi
      previous=$arg
    done
    [[ "$env_file" == "${FAKE_EXPECTED_ENV_FILE:?}" && -f "$env_file" ]] || {
      echo wrong-or-missing-env-file >&2
      exit 96
    }
    if [[ "$*" == *' config --quiet' ]]; then exit 0; fi
    [[ -n "$override" && -r "$override" ]] || { echo missing-override >&2; exit 90; }
    if [[ "$*" == *'up -d --no-deps authority-loopback-bridge'* ]]; then
      printf present > "$FAKE_BRIDGE_STATE"
    elif grep -q 'backend:candidate' "$override" && grep -q 'web:candidate' "$override"; then
      printf candidate > "$FAKE_STATE"
    elif grep -q 'multica-backend:dgx-ultra-20260819-353b16c3' "$override" && \
         grep -q 'multica-web:dgx-ultra-20260819-4ab2c72c2' "$override"; then
      printf predecessor > "$FAKE_STATE"
    else
      echo unknown-override >&2
      exit 90
    fi
    ;;
  inspect)
    format=${3:?format required}
    target=${4:?target required}
    state=$(cat "$FAKE_STATE")
    if [[ "$state" == candidate ]]; then
      backend_ref=backend:candidate; backend_id=$candidate_backend_id
      web_ref=web:candidate; web_id=$candidate_web_id
    else
      backend_ref=multica-backend:dgx-ultra-20260819-353b16c3; backend_id=$previous_backend_id
      web_ref=multica-web:dgx-ultra-20260819-4ab2c72c2; web_id=$previous_web_id
    fi
    if [[ "$target" == abc123def456 ]]; then ref=backend:candidate; id=$candidate_backend_id
    elif [[ "$target" == *backend* ]]; then ref=$backend_ref; id=$backend_id
    else ref=$web_ref; id=$web_id; fi
    case "$format" in
      *'com.docker.compose.project'*) printf '%s\n' "$container_project" ;;
      *'com.docker.compose.service'*) printf '%s\n' "$container_service" ;;
      *NetworkSettings.Networks*)
        if [[ "${FAKE_MULTIPLE_NETWORKS:-0}" == 1 ]]; then
          printf '{"multica-dgx-ultra_default":{"NetworkID":"%s","Gateway":"%s"},"unexpected":{"NetworkID":"%s","Gateway":"172.29.0.1"}}\n' "$network_id" "$network_gateway" "$network_id"
        else
          printf '{"multica-dgx-ultra_default":{"NetworkID":"%s","Gateway":"%s"}}\n' "$network_id" "$network_gateway"
        fi
        ;;
      *State.Health*) [[ "${FAKE_BRIDGE_HEALTH_FAIL:-0}" == 1 ]] && printf 'unhealthy\n' || printf 'healthy\n' ;;
      *Config.Image*) printf '%s\n' "$ref" ;;
      *'.Image'*) printf '%s\n' "$id" ;;
      *) echo unsupported-inspect-format >&2; exit 91 ;;
    esac
    ;;
  network)
    [[ "${2:-}" == inspect && "${3:-}" == multica-dgx-ultra_default ]] || exit 91
    printf '[{"Name":"multica-dgx-ultra_default","Id":"%s","Driver":"%s","Scope":"%s","Labels":{"com.docker.compose.project":"%s"},"IPAM":{"Config":[{"Subnet":"172.16.0.0/12","Gateway":"%s"}]}}]\n' \
      "$network_id" "$network_driver" "$network_scope" "$network_project" "$network_gateway"
    ;;
  rm)
    [[ "${2:-}" == -f && "${3:-}" == abc123def456 ]] || exit 91
    printf absent > "$FAKE_BRIDGE_STATE"
    printf 'abc123def456\n'
    ;;
  exec)
    echo unexpected-real-collector >&2
    exit 92
    ;;
  *) echo unsupported-docker-call >&2; exit 93 ;;
esac
DOCKER

cat > "$tmp/curl" <<'CURL'
#!/usr/bin/env bash
set -Eeuo pipefail
case "$*" in
  *'172.24.0.1:3151/bff/health'*)
    if [[ "${FAKE_BRIDGE_HTTP_FAIL:-0}" == 1 ]]; then printf 503; else printf 200; fi
    ;;
  *'/health'*)
    counter_file=${FAKE_CURL_COUNTER:?}
    attempt=$(cat "$counter_file")
    attempt=$((attempt + 1))
    printf '%s\n' "$attempt" > "$counter_file"
    if (( attempt <= ${FAKE_HEALTH_REFUSED_COUNT:-0} )); then
      exit 7
    fi
    if [[ "${FAKE_HEALTH_FAIL:-0}" == 1 ]]; then printf 503; else printf 200; fi
    ;;
  *'/api/config'*)
    if [[ "${FAKE_VERSION_FAIL:-0}" == 1 ]]; then
      printf '%s\n' '{"server_version":"wrong-version"}'
    else
      printf '%s\n' '{"server_version":"candidate-version"}'
    fi
    ;;
  *) echo unsupported-curl-call >&2; exit 94 ;;
esac
CURL

cat > "$tmp/ss" <<'SS'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ "${FAKE_PORT_CONFLICT:-0}" == 1 ]]; then
  printf '%s\n' 'LISTEN 0 4096 0.0.0.0:3151 0.0.0.0:*'
fi
SS

cat > "$tmp/counts" <<'COUNTS'
#!/usr/bin/env bash
set -Eeuo pipefail
[[ "${FAKE_COUNTS_FAIL:-0}" != 1 ]] || exit 95
printf '%s\n' '{"schema_top":415,"agent_count":36,"project_count":13,"status":"collected_read_only"}'
COUNTS
chmod 755 "$tmp/docker" "$tmp/curl" "$tmp/counts" "$tmp/ss"
export DOCKER_BIN="$tmp/docker" CURL_BIN="$tmp/curl" COUNTS_BIN="$tmp/counts"
export SS_BIN="$tmp/ss"
export FAKE_STATE="$tmp/state" FAKE_DOCKER_LOG="$tmp/docker.log"
export FAKE_BRIDGE_STATE="$tmp/bridge.state"
export FAKE_CURL_COUNTER="$tmp/curl.count"
export HIVECREW_READINESS_ATTEMPTS=3 HIVECREW_READINESS_DELAY_SECONDS=0

reset_fake() {
  local deploy_dir=${1:?deploy directory required}
  export FAKE_EXPECTED_ENV_FILE="$deploy_dir/.env"
  printf predecessor > "$FAKE_STATE"
  printf absent > "$FAKE_BRIDGE_STATE"
  : > "$FAKE_DOCKER_LOG"
  printf '0\n' > "$FAKE_CURL_COUNTER"
}

deploy=$(new_deploy governed)
success=$(new_candidate success)
reset_fake "$deploy"
"$operator_root/apply-staging.sh" "$success" >/dev/null
[[ $(cat "$FAKE_STATE") == candidate ]]
jq -e '.schema == "HiveCrewOperatorApplyReceiptV3" and .health_http == 200 and
  .authority_bridge.health == "healthy" and .authority_bridge.gateway == "172.24.0.1" and
  .schema_read_only_counts.schema_top == 415 and .schema_read_only_counts.agent_count == 36' \
  "$success/receipts/OPERATOR-APPLY-RECEIPT.json" >/dev/null
jq -e '.schema == "HiveCrewPreApplySnapshotV3" and
  .authority_bridge.network_id == "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" and
  .authority_bridge.gateway == "172.24.0.1" and .authority_bridge.bind == "172.24.0.1:3151"' \
  "$success/receipts/PRE-APPLY-SNAPSHOT.json" >/dev/null
jq -e \
  '.rollback_predecessor.backend.ref == "multica-backend:dgx-ultra-20260819-353b16c3" and
   .rollback_predecessor.backend.id == "sha256:f6c5050276263266cf4c08954694d2022f60b942716d12f40f4ef5da2599649a" and
   .rollback_predecessor.web.ref == "multica-web:dgx-ultra-20260819-4ab2c72c2" and
   .rollback_predecessor.web.id == "sha256:53b41218f7e0fe2ad3dfa63e9e40a30a21a14a7150579471d107fecc4f861d55"' \
  "$success/INTEGRATION-IDENTITY.json" >/dev/null
[[ ! -e "$success/receipts/ROLLBACK-RECEIPT.json" ]]
grep -F "compose --env-file $deploy/.env -f $success/compose.yaml -f $operator_root/docker-compose.loopback-bridge.yml -f $success/receipts/candidate-compose.override.yaml -p multica-dgx-ultra config --quiet" \
  "$FAKE_DOCKER_LOG" >/dev/null
grep -F "compose --env-file $deploy/.env -f $success/compose.yaml -f $operator_root/docker-compose.loopback-bridge.yml -f $success/receipts/candidate-compose.override.yaml -p multica-dgx-ultra up -d --no-deps backend frontend" \
  "$FAKE_DOCKER_LOG" >/dev/null
echo success=PASS

sidecar_line=$(grep -n 'up -d --no-deps authority-loopback-bridge' "$FAKE_DOCKER_LOG" | head -1 | cut -d: -f1)
backend_line=$(grep -n 'up -d --no-deps backend frontend' "$FAKE_DOCKER_LOG" | head -1 | cut -d: -f1)
(( sidecar_line < backend_line ))
echo sidecar_healthy_before_backend_start=PASS

port_conflict=$(new_candidate port-conflict)
reset_fake "$deploy"
if FAKE_PORT_CONFLICT=1 "$operator_root/apply-staging.sh" "$port_conflict" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor && $(cat "$FAKE_BRIDGE_STATE") == absent ]]
if grep -F 'up -d --no-deps' "$FAKE_DOCKER_LOG" >/dev/null; then exit 1; fi
echo authority_bridge_port_conflict_zero_mutation=PASS

lan_gateway=$(new_candidate lan-gateway)
reset_fake "$deploy"
if FAKE_GATEWAY=192.168.1.108 "$operator_root/apply-staging.sh" "$lan_gateway" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor && $(cat "$FAKE_BRIDGE_STATE") == absent ]]
echo authority_bridge_lan_gateway_rejected=PASS

tailnet_gateway=$(new_candidate tailnet-gateway)
reset_fake "$deploy"
if FAKE_GATEWAY=100.99.164.115 "$operator_root/apply-staging.sh" "$tailnet_gateway" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor && $(cat "$FAKE_BRIDGE_STATE") == absent ]]
echo authority_bridge_tailnet_gateway_rejected=PASS

wrong_driver=$(new_candidate wrong-driver)
reset_fake "$deploy"
if FAKE_NETWORK_DRIVER=macvlan "$operator_root/apply-staging.sh" "$wrong_driver" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor && $(cat "$FAKE_BRIDGE_STATE") == absent ]]
echo authority_bridge_wrong_driver_rejected=PASS

wrong_project=$(new_candidate wrong-project)
reset_fake "$deploy"
if FAKE_CONTAINER_PROJECT=other-project "$operator_root/apply-staging.sh" "$wrong_project" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor && $(cat "$FAKE_BRIDGE_STATE") == absent ]]
echo authority_bridge_wrong_project_rejected=PASS

wrong_service=$(new_candidate wrong-service)
reset_fake "$deploy"
if FAKE_CONTAINER_SERVICE=frontend "$operator_root/apply-staging.sh" "$wrong_service" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor && $(cat "$FAKE_BRIDGE_STATE") == absent ]]
echo authority_bridge_wrong_service_rejected=PASS

wrong_scope=$(new_candidate wrong-scope)
reset_fake "$deploy"
if FAKE_NETWORK_SCOPE=swarm "$operator_root/apply-staging.sh" "$wrong_scope" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor && $(cat "$FAKE_BRIDGE_STATE") == absent ]]
echo authority_bridge_wrong_scope_rejected=PASS

wrong_network_project=$(new_candidate wrong-network-project)
reset_fake "$deploy"
if FAKE_NETWORK_PROJECT=other-project "$operator_root/apply-staging.sh" "$wrong_network_project" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor && $(cat "$FAKE_BRIDGE_STATE") == absent ]]
echo authority_bridge_wrong_network_project_rejected=PASS

multiple_networks=$(new_candidate multiple-networks)
reset_fake "$deploy"
if FAKE_MULTIPLE_NETWORKS=1 "$operator_root/apply-staging.sh" "$multiple_networks" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor && $(cat "$FAKE_BRIDGE_STATE") == absent ]]
echo authority_bridge_multiple_networks_rejected=PASS

bridge_health_fail=$(new_candidate bridge-health-fail)
reset_fake "$deploy"
if FAKE_BRIDGE_HEALTH_FAIL=1 "$operator_root/apply-staging.sh" "$bridge_health_fail" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor && $(cat "$FAKE_BRIDGE_STATE") == absent ]]
jq -e '.authority_bridge.stop.orphan_count == 0 and .authority_bridge.stop.listener_absent == true' \
  "$bridge_health_fail/receipts/ROLLBACK-RECEIPT.json" >/dev/null
echo authority_bridge_health_failure_zero_orphan_rollback=PASS

bridge_http_fail=$(new_candidate bridge-http-fail)
reset_fake "$deploy"
if FAKE_BRIDGE_HTTP_FAIL=1 "$operator_root/apply-staging.sh" "$bridge_http_fail" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor && $(cat "$FAKE_BRIDGE_STATE") == absent ]]
echo authority_bridge_http_failure_zero_orphan_rollback=PASS

backend_disappears=$(new_candidate backend-disappears)
reset_fake "$deploy"
"$operator_root/apply-staging.sh" "$backend_disappears" >/dev/null
[[ $(cat "$FAKE_STATE") == candidate && $(cat "$FAKE_BRIDGE_STATE") == present ]]
FAKE_CONTAINER_PROJECT=missing "$operator_root/rollback-staging.sh" \
  "$backend_disappears" backend-missing-test >/dev/null
[[ $(cat "$FAKE_STATE") == predecessor && $(cat "$FAKE_BRIDGE_STATE") == absent ]]
echo rollback_uses_pre_mutation_bridge_snapshot_when_backend_unavailable=PASS

transient_refused=$(new_candidate transient-refused)
transient_err="$tmp/transient-refused.err"
reset_fake "$deploy"
FAKE_HEALTH_REFUSED_COUNT=2 HIVECREW_READINESS_ATTEMPTS=5 \
  "$operator_root/apply-staging.sh" "$transient_refused" >/dev/null 2>"$transient_err"
[[ $(cat "$FAKE_STATE") == candidate ]]
[[ $(cat "$FAKE_CURL_COUNTER") == 3 ]]
[[ -e "$transient_refused/receipts/OPERATOR-APPLY-RECEIPT.json" ]]
[[ ! -e "$transient_refused/receipts/ROLLBACK-RECEIPT.json" ]]
if grep -F 'unbound variable' "$transient_err" >/dev/null; then exit 1; fi
echo transient_connection_refused_then_ready_no_rollback=PASS

persistent_refused=$(new_candidate persistent-refused)
persistent_err="$tmp/persistent-refused.err"
reset_fake "$deploy"
if FAKE_HEALTH_REFUSED_COUNT=99 HIVECREW_READINESS_ATTEMPTS=3 \
    "$operator_root/apply-staging.sh" "$persistent_refused" >/dev/null 2>"$persistent_err"; then
  exit 1
fi
[[ $(cat "$FAKE_STATE") == predecessor ]]
[[ $(cat "$FAKE_CURL_COUNTER") == 3 ]]
[[ ! -e "$persistent_refused/receipts/OPERATOR-APPLY-RECEIPT.json" ]]
jq -e '.exact_predecessor == true and
  .restored_backend.id == "sha256:f6c5050276263266cf4c08954694d2022f60b942716d12f40f4ef5da2599649a" and
  .restored_web.id == "sha256:53b41218f7e0fe2ad3dfa63e9e40a30a21a14a7150579471d107fecc4f861d55"' \
  "$persistent_refused/receipts/ROLLBACK-RECEIPT.json" >/dev/null
if grep -F 'unbound variable' "$persistent_err" >/dev/null; then exit 1; fi
echo persistent_connection_refused_exact_autorollback=PASS

health_fail=$(new_candidate health-fail)
reset_fake "$deploy"
if FAKE_HEALTH_FAIL=1 "$operator_root/apply-staging.sh" "$health_fail" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor ]]
[[ ! -e "$health_fail/receipts/OPERATOR-APPLY-RECEIPT.json" ]]
jq -e '.exact_predecessor == true and (.reason | startswith("automatic-apply-failure-"))' \
  "$health_fail/receipts/ROLLBACK-RECEIPT.json" >/dev/null
echo health_fail_autorollback=PASS

version_fail=$(new_candidate version-fail)
reset_fake "$deploy"
if FAKE_VERSION_FAIL=1 "$operator_root/apply-staging.sh" "$version_fail" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor ]]
[[ ! -e "$version_fail/receipts/OPERATOR-APPLY-RECEIPT.json" ]]
jq -e '.exact_predecessor == true' "$version_fail/receipts/ROLLBACK-RECEIPT.json" >/dev/null
echo version_fail_autorollback=PASS

manual=$(new_candidate manual-rollback)
reset_fake "$deploy"
"$operator_root/apply-staging.sh" "$manual" >/dev/null
"$operator_root/rollback-staging.sh" "$manual" manual-test >/dev/null
[[ $(cat "$FAKE_STATE") == predecessor ]]
[[ $(cat "$FAKE_BRIDGE_STATE") == absent ]]
jq -e '.reason == "manual-test" and
  .authority_bridge.stop.orphan_count == 0 and .authority_bridge.stop.listener_absent == true and
  .restored_backend.ref == "multica-backend:dgx-ultra-20260819-353b16c3" and
  .restored_backend.id == "sha256:f6c5050276263266cf4c08954694d2022f60b942716d12f40f4ef5da2599649a" and
  .restored_web.ref == "multica-web:dgx-ultra-20260819-4ab2c72c2" and
  .restored_web.id == "sha256:53b41218f7e0fe2ad3dfa63e9e40a30a21a14a7150579471d107fecc4f861d55"' \
  "$manual/receipts/ROLLBACK-RECEIPT.json" >/dev/null
grep -F "compose --env-file $deploy/.env -f $manual/rollback-compose.yaml -f $manual/receipts/rollback-compose.override.yaml -p multica-dgx-ultra up -d --no-deps backend frontend" \
  "$FAKE_DOCKER_LOG" >/dev/null
echo manual_rollback_exact_predecessor=PASS

collector_fail=$(new_candidate collector-fail)
reset_fake "$deploy"
if FAKE_COUNTS_FAIL=1 "$operator_root/apply-staging.sh" "$collector_fail" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor ]]
if grep -F 'up -d --no-deps' "$FAKE_DOCKER_LOG" >/dev/null; then exit 1; fi
[[ ! -e "$collector_fail/receipts/OPERATOR-APPLY-RECEIPT.json" ]]
echo collector_failure_zero_mutation=PASS

missing_env=$(new_candidate missing-env)
mv "$deploy/.env" "$deploy/.env.saved"
reset_fake "$deploy"
if "$operator_root/apply-staging.sh" "$missing_env" >/dev/null 2>&1; then exit 1; fi
mv "$deploy/.env.saved" "$deploy/.env"
[[ $(cat "$FAKE_STATE") == predecessor && ! -s "$FAKE_DOCKER_LOG" ]]
[[ ! -e "$missing_env/receipts/OPERATOR-APPLY-RECEIPT.json" ]]
echo missing_env_file_zero_mutation=PASS

wrong_env=$(new_candidate wrong-env)
reset_fake "$deploy"
export FAKE_EXPECTED_ENV_FILE="$tmp/not-the-governed-env/.env"
if "$operator_root/apply-staging.sh" "$wrong_env" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor ]]
if grep -F 'up -d --no-deps' "$FAKE_DOCKER_LOG" >/dev/null; then exit 1; fi
[[ ! -e "$wrong_env/receipts/OPERATOR-APPLY-RECEIPT.json" ]]
echo wrong_env_file_zero_mutation=PASS

symlink_env=$(new_candidate symlink-env)
mv "$deploy/.env" "$deploy/.env.saved"
ln -s "$deploy/.env.saved" "$deploy/.env"
reset_fake "$deploy"
if "$operator_root/apply-staging.sh" "$symlink_env" >/dev/null 2>&1; then exit 1; fi
rm "$deploy/.env"
mv "$deploy/.env.saved" "$deploy/.env"
[[ $(cat "$FAKE_STATE") == predecessor && ! -s "$FAKE_DOCKER_LOG" ]]
[[ ! -e "$symlink_env/receipts/OPERATOR-APPLY-RECEIPT.json" ]]
echo symlink_env_file_zero_mutation=PASS

rollback_missing_env=$(new_candidate rollback-missing-env)
mv "$deploy/.env" "$deploy/.env.saved"
reset_fake "$deploy"
if "$operator_root/rollback-staging.sh" "$rollback_missing_env" missing-env-test >/dev/null 2>&1; then exit 1; fi
mv "$deploy/.env.saved" "$deploy/.env"
[[ $(cat "$FAKE_STATE") == predecessor && ! -s "$FAKE_DOCKER_LOG" ]]
[[ ! -e "$rollback_missing_env/receipts/ROLLBACK-RECEIPT.json" ]]
[[ ! -e "$rollback_missing_env/receipts/rollback-compose.override.yaml" ]]
echo rollback_missing_env_file_zero_mutation=PASS

rollback_wrong_env=$(new_candidate rollback-wrong-env)
reset_fake "$deploy"
export FAKE_EXPECTED_ENV_FILE="$tmp/not-the-governed-env/.env"
if "$operator_root/rollback-staging.sh" "$rollback_wrong_env" wrong-env-test >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor ]]
if grep -F 'up -d --no-deps' "$FAKE_DOCKER_LOG" >/dev/null; then exit 1; fi
[[ ! -e "$rollback_wrong_env/receipts/ROLLBACK-RECEIPT.json" ]]
[[ ! -e "$rollback_wrong_env/receipts/rollback-compose.override.yaml" ]]
echo rollback_wrong_env_file_zero_mutation=PASS

rollback_symlink_env=$(new_candidate rollback-symlink-env)
mv "$deploy/.env" "$deploy/.env.saved"
ln -s "$deploy/.env.saved" "$deploy/.env"
reset_fake "$deploy"
if "$operator_root/rollback-staging.sh" "$rollback_symlink_env" symlink-env-test >/dev/null 2>&1; then exit 1; fi
rm "$deploy/.env"
mv "$deploy/.env.saved" "$deploy/.env"
[[ $(cat "$FAKE_STATE") == predecessor && ! -s "$FAKE_DOCKER_LOG" ]]
[[ ! -e "$rollback_symlink_env/receipts/ROLLBACK-RECEIPT.json" ]]
[[ ! -e "$rollback_symlink_env/receipts/rollback-compose.override.yaml" ]]
echo rollback_symlink_env_file_zero_mutation=PASS

apply_path_override=$(new_candidate apply-path-override)
reset_fake "$deploy"
if "$operator_root/apply-staging.sh" "$apply_path_override" "$tmp/evil-deploy" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor && ! -s "$FAKE_DOCKER_LOG" ]]
[[ ! -e "$apply_path_override/receipts/OPERATOR-APPLY-RECEIPT.json" ]]
echo apply_path_override_rejected_zero_mutation=PASS

rollback_path_override=$(new_candidate rollback-path-override)
reset_fake "$deploy"
if "$operator_root/rollback-staging.sh" "$rollback_path_override" "$tmp/evil-deploy" override-test >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor && ! -s "$FAKE_DOCKER_LOG" ]]
[[ ! -e "$rollback_path_override/receipts/ROLLBACK-RECEIPT.json" ]]
[[ ! -e "$rollback_path_override/receipts/rollback-compose.override.yaml" ]]
echo rollback_path_override_rejected_zero_mutation=PASS

injection=$(new_candidate injection)
jq '.backend_image.ref="evil\nservices: {}"' "$injection/INTEGRATION-IDENTITY.json" > "$injection/identity.tmp"
mv "$injection/identity.tmp" "$injection/INTEGRATION-IDENTITY.json"
reset_fake "$deploy"
if "$operator_root/apply-staging.sh" "$injection" >/dev/null 2>&1; then exit 1; fi
[[ ! -s "$FAKE_DOCKER_LOG" ]]
echo identity_injection_zero_docker=PASS

valid="$tmp/external-valid.json"
jq -n --arg revision "$revision" --arg bid "$candidate_backend_id" --arg wid "$candidate_web_id" \
  '{schema:"HiveCrewExternalAcceptanceV3",final_revision:$revision,final_tree:("b"*40),
    backend:{ref:"backend:candidate",id:$bid,digest:$bid},web:{ref:"web:candidate",id:$wid,digest:$wid},
    authority_overlay_sha256:("d"*64),compose_sha256:("c"*64),
    api:{health_http:200,ready_http:200,server_version:"candidate-version",verified:true},
    browser:{url:"http://127.0.0.1:13301/",http:200,console_errors:0,verified:true},
    data:{schema_top:415,read_only_counts:{agent_count:36,project_count:13},verified:true},
    degraded:{verified:true,evidence:["degraded-case.json"]},failure_path:{verified:true,evidence:["failure-case.json"]},
    accepted:false,production_unchanged:true,run_06_unchanged:true}' > "$valid"
python3 - "$root/EXTERNAL-ACCEPTANCE.schema.json" "$valid" <<'PY'
import json, jsonschema, sys
with open(sys.argv[1]) as f: schema=json.load(f)
with open(sys.argv[2]) as f: value=json.load(f)
jsonschema.Draft202012Validator.check_schema(schema)
jsonschema.validate(value, schema)
bad=dict(value); bad["accepted"]=True
try: jsonschema.validate(bad, schema)
except jsonschema.ValidationError: pass
else: raise SystemExit("negative acceptance fixture passed")
bad2=dict(value); bad2["api"]={"health_http":200}
try: jsonschema.validate(bad2, schema)
except jsonschema.ValidationError: pass
else: raise SystemExit("negative nested fixture passed")
PY
echo external_schema_positive_negative=PASS

archive="$tmp/operator.tar"
"$root/package-archive.sh" "$archive" >/dev/null
(cd "$tmp" && sha256sum -c operator.tar.sha256 >/dev/null)
mkdir "$tmp/alt"
tar -xf "$archive" -C "$tmp/alt"
python3 - "$tmp/alt" <<'PY'
import hashlib, json, pathlib, sys
root=pathlib.Path(sys.argv[1]); manifest=json.loads((root/"MANIFEST.json").read_text())
for item in manifest["files"]:
    actual=hashlib.sha256((root/item["path"]).read_bytes()).hexdigest()
    if actual != item["sha256"]: raise SystemExit("manifest mismatch")
PY
echo portable_archive_alternate_path=PASS
echo state_machine_matrix=PASS
