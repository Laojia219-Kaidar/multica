#!/usr/bin/env bash
set -Eeuo pipefail
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

candidate_backend_ref=backend:candidate
candidate_backend_id=sha256:$(printf 'e%.0s' {1..64})
candidate_web_ref=web:candidate
candidate_web_id=sha256:$(printf 'f%.0s' {1..64})
previous_backend_ref=multica-backend:dgx-ultra-20260819-353b16c3
previous_backend_id=sha256:f6c5050276263266cf4c08954694d2022f60b942716d12f40f4ef5da2599649a
previous_web_ref=multica-web:dgx-ultra-20260819-4ab2c72c2
previous_web_id=sha256:53b41218f7e0fe2ad3dfa63e9e40a30a21a14a7150579471d107fecc4f861d55
revision=$(printf 'a%.0s' {1..40})

new_candidate() {
  local name=${1:?name required} dir="$tmp/$1"
  mkdir -p "$dir"
  printf '%s\n' 'services:' '  backend: {}' '  frontend: {}' > "$dir/compose.yaml"
  local compose_sha
  compose_sha=$(sha256sum "$dir/compose.yaml" | awk '{print $1}')
  jq -n --arg compose_sha "$compose_sha" --arg revision "$revision" \
    --arg cbref "$candidate_backend_ref" --arg cbid "$candidate_backend_id" \
    --arg cwref "$candidate_web_ref" --arg cwid "$candidate_web_id" \
    --arg pbref "$previous_backend_ref" --arg pbid "$previous_backend_id" \
    --arg pwref "$previous_web_ref" --arg pwid "$previous_web_id" \
    '{schema:"HiveCrewIntegrationIdentityV2", compose_project:"multica-dgx-ultra",
      final_revision:$revision, final_tree:("b"*40), source_archive_sha256:("c"*64),
      authority_overlay_sha256:("d"*64), compose_sha256:$compose_sha,
      api:{server_version:"candidate-version"},
      backend_image:{ref:$cbref,id:$cbid,digest:$cbid},
      web_image:{ref:$cwref,id:$cwid,digest:$cwid},
      rollback_predecessor:{backend:{ref:$pbref,id:$pbid,digest:$pbid},web:{ref:$pwref,id:$pwid,digest:$pwid}}}' \
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
candidate_backend_id="sha256:$(printf 'e%.0s' {1..64})"
candidate_web_id="sha256:$(printf 'f%.0s' {1..64})"
previous_backend_id=sha256:f6c5050276263266cf4c08954694d2022f60b942716d12f40f4ef5da2599649a
previous_web_id=sha256:53b41218f7e0fe2ad3dfa63e9e40a30a21a14a7150579471d107fecc4f861d55
case "${1:-}" in
  ps)
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
    if grep -q 'backend:candidate' "$override" && grep -q 'web:candidate' "$override"; then
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
    if [[ "$target" == *backend* ]]; then ref=$backend_ref; id=$backend_id; else ref=$web_ref; id=$web_id; fi
    case "$format" in
      *Config.Image*) printf '%s\n' "$ref" ;;
      *'.Image'*) printf '%s\n' "$id" ;;
      *) echo unsupported-inspect-format >&2; exit 91 ;;
    esac
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
  *'/health'*)
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

cat > "$tmp/counts" <<'COUNTS'
#!/usr/bin/env bash
set -Eeuo pipefail
[[ "${FAKE_COUNTS_FAIL:-0}" != 1 ]] || exit 95
printf '%s\n' '{"schema_top":415,"agent_count":36,"project_count":13,"status":"collected_read_only"}'
COUNTS
chmod 755 "$tmp/docker" "$tmp/curl" "$tmp/counts"
export DOCKER_BIN="$tmp/docker" CURL_BIN="$tmp/curl" COUNTS_BIN="$tmp/counts"
export FAKE_STATE="$tmp/state" FAKE_DOCKER_LOG="$tmp/docker.log"

reset_fake() {
  local deploy_dir=${1:?deploy directory required}
  export FAKE_EXPECTED_ENV_FILE="$deploy_dir/.env"
  printf predecessor > "$FAKE_STATE"
  : > "$FAKE_DOCKER_LOG"
}

deploy=$(new_deploy governed)
success=$(new_candidate success)
reset_fake "$deploy"
"$root/apply-staging.sh" "$success" "$deploy" >/dev/null
[[ $(cat "$FAKE_STATE") == candidate ]]
jq -e '.schema == "HiveCrewOperatorApplyReceiptV3" and .health_http == 200 and
  .schema_read_only_counts.schema_top == 415 and .schema_read_only_counts.agent_count == 36' \
  "$success/receipts/OPERATOR-APPLY-RECEIPT.json" >/dev/null
jq -e \
  '.rollback_predecessor.backend.ref == "multica-backend:dgx-ultra-20260819-353b16c3" and
   .rollback_predecessor.backend.id == "sha256:f6c5050276263266cf4c08954694d2022f60b942716d12f40f4ef5da2599649a" and
   .rollback_predecessor.web.ref == "multica-web:dgx-ultra-20260819-4ab2c72c2" and
   .rollback_predecessor.web.id == "sha256:53b41218f7e0fe2ad3dfa63e9e40a30a21a14a7150579471d107fecc4f861d55"' \
  "$success/INTEGRATION-IDENTITY.json" >/dev/null
[[ ! -e "$success/receipts/ROLLBACK-RECEIPT.json" ]]
grep -F "compose --env-file $deploy/.env -f $success/compose.yaml -p multica-dgx-ultra config --quiet" \
  "$FAKE_DOCKER_LOG" >/dev/null
grep -F "compose --env-file $deploy/.env -f $success/compose.yaml -f $success/receipts/candidate-compose.override.yaml -p multica-dgx-ultra up -d --no-deps backend frontend" \
  "$FAKE_DOCKER_LOG" >/dev/null
echo success=PASS

health_fail=$(new_candidate health-fail)
reset_fake "$deploy"
if FAKE_HEALTH_FAIL=1 "$root/apply-staging.sh" "$health_fail" "$deploy" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor ]]
[[ ! -e "$health_fail/receipts/OPERATOR-APPLY-RECEIPT.json" ]]
jq -e '.exact_predecessor == true and (.reason | startswith("automatic-apply-failure-"))' \
  "$health_fail/receipts/ROLLBACK-RECEIPT.json" >/dev/null
echo health_fail_autorollback=PASS

version_fail=$(new_candidate version-fail)
reset_fake "$deploy"
if FAKE_VERSION_FAIL=1 "$root/apply-staging.sh" "$version_fail" "$deploy" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor ]]
[[ ! -e "$version_fail/receipts/OPERATOR-APPLY-RECEIPT.json" ]]
jq -e '.exact_predecessor == true' "$version_fail/receipts/ROLLBACK-RECEIPT.json" >/dev/null
echo version_fail_autorollback=PASS

manual=$(new_candidate manual-rollback)
reset_fake "$deploy"
"$root/apply-staging.sh" "$manual" "$deploy" >/dev/null
"$root/rollback-staging.sh" "$manual" "$deploy" manual-test >/dev/null
[[ $(cat "$FAKE_STATE") == predecessor ]]
jq -e '.reason == "manual-test" and
  .restored_backend.ref == "multica-backend:dgx-ultra-20260819-353b16c3" and
  .restored_backend.id == "sha256:f6c5050276263266cf4c08954694d2022f60b942716d12f40f4ef5da2599649a" and
  .restored_web.ref == "multica-web:dgx-ultra-20260819-4ab2c72c2" and
  .restored_web.id == "sha256:53b41218f7e0fe2ad3dfa63e9e40a30a21a14a7150579471d107fecc4f861d55"' \
  "$manual/receipts/ROLLBACK-RECEIPT.json" >/dev/null
grep -F "compose --env-file $deploy/.env -f $manual/compose.yaml -f $manual/receipts/rollback-compose.override.yaml -p multica-dgx-ultra up -d --no-deps backend frontend" \
  "$FAKE_DOCKER_LOG" >/dev/null
echo manual_rollback_exact_predecessor=PASS

collector_fail=$(new_candidate collector-fail)
reset_fake "$deploy"
if FAKE_COUNTS_FAIL=1 "$root/apply-staging.sh" "$collector_fail" "$deploy" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor ]]
if grep -F 'up -d --no-deps' "$FAKE_DOCKER_LOG" >/dev/null; then exit 1; fi
[[ ! -e "$collector_fail/receipts/OPERATOR-APPLY-RECEIPT.json" ]]
echo collector_failure_zero_mutation=PASS

missing_env=$(new_candidate missing-env)
missing_deploy="$tmp/deploy-missing"
mkdir -p "$missing_deploy"
reset_fake "$missing_deploy"
if "$root/apply-staging.sh" "$missing_env" "$missing_deploy" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor && ! -s "$FAKE_DOCKER_LOG" ]]
[[ ! -e "$missing_env/receipts/OPERATOR-APPLY-RECEIPT.json" ]]
echo missing_env_file_zero_mutation=PASS

wrong_env=$(new_candidate wrong-env)
reset_fake "$deploy"
export FAKE_EXPECTED_ENV_FILE="$tmp/not-the-governed-env/.env"
if "$root/apply-staging.sh" "$wrong_env" "$deploy" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor ]]
if grep -F 'up -d --no-deps' "$FAKE_DOCKER_LOG" >/dev/null; then exit 1; fi
[[ ! -e "$wrong_env/receipts/OPERATOR-APPLY-RECEIPT.json" ]]
echo wrong_env_file_zero_mutation=PASS

symlink_env=$(new_candidate symlink-env)
symlink_deploy="$tmp/deploy-symlink"
mkdir -p "$symlink_deploy"
ln -s "$deploy/.env" "$symlink_deploy/.env"
reset_fake "$symlink_deploy"
if "$root/apply-staging.sh" "$symlink_env" "$symlink_deploy" >/dev/null 2>&1; then exit 1; fi
[[ $(cat "$FAKE_STATE") == predecessor && ! -s "$FAKE_DOCKER_LOG" ]]
[[ ! -e "$symlink_env/receipts/OPERATOR-APPLY-RECEIPT.json" ]]
echo symlink_env_file_zero_mutation=PASS

injection=$(new_candidate injection)
jq '.backend_image.ref="evil\nservices: {}"' "$injection/INTEGRATION-IDENTITY.json" > "$injection/identity.tmp"
mv "$injection/identity.tmp" "$injection/INTEGRATION-IDENTITY.json"
reset_fake "$deploy"
if "$root/apply-staging.sh" "$injection" "$deploy" >/dev/null 2>&1; then exit 1; fi
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
