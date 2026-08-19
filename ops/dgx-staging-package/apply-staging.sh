#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
pkg=${1:?usage: apply-staging.sh CANDIDATE_DIR}
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
project=multica-dgx-ultra
"$root/precheck.sh" "$pkg"
id="$pkg/INTEGRATION-IDENTITY.json"
out="$pkg/receipts"
mkdir -p "$out"
docker_bin=${DOCKER_BIN:-docker}
curl_bin=${CURL_BIN:-curl}
snapshot="$out/PRE-APPLY-SNAPSHOT.json"
receipt="$out/OPERATOR-APPLY-RECEIPT.json"
applied=false
rollback(){ rc=$?; trap - EXIT; if [[ "$rc" -ne 0 && "$applied" == true ]]; then "$root/rollback-staging.sh" "$pkg" "$rc" || true; fi; exit "$rc"; }
trap rollback EXIT
compose_hash=$(sha256sum "$pkg/compose.yaml"|awk '{print $1}')
before=$($docker_bin ps --filter "label=com.docker.compose.project=$project" --format '{{.Names}}\t{{.ID}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}' 2>/dev/null || true)
containers=$(printf '%s\n' "$before" | jq -Rsc 'split("\n")|map(select(length>0))|map(split("\t")|{name:.[0],id:.[1],image:.[2],status:.[3],ports:.[4]})')
jq -n --arg p "$project" --arg h "$compose_hash" --argjson c "$containers" --arg path "$id" --argjson rb "$(jq -c .rollback_predecessor "$id")" '{schema:"HiveCrewPreApplySnapshotV2",compose_project:$p,compose_config_sha256:$h,containers:$c,schema_read_only_counts:{schema_top:null,active_rows:null,status:"not_collected_pre_mutation"},rollback_artifact_path:$path,rollback_predecessor:$rb,mutation_started:false,secret_values_recorded:false}' > "$snapshot"
backend=$(jq -r .backend_image.id "$id")
web=$(jq -r .web_image.id "$id")
rev=$(jq -r .final_revision "$id")
[[ -n "$backend" && -n "$web" ]] || exit 78
$docker_bin compose -f "$pkg/compose.yaml" -p "$project" up -d --no-deps backend frontend
applied=true
br=$($docker_bin inspect --format '{{.Config.Image}}' "$backend" 2>/dev/null || true)
wr=$($docker_bin inspect --format '{{.Config.Image}}' "$web" 2>/dev/null || true)
bd=$($docker_bin inspect --format '{{index .RepoDigests 0}}' "$backend" 2>/dev/null || true)
wd=$($docker_bin inspect --format '{{index .RepoDigests 0}}' "$web" 2>/dev/null || true)
[[ "$br" == "$backend" && "$wr" == "$web" && "$bd" == "$(jq -r .backend_image.digest "$id")" && "$wd" == "$(jq -r .web_image.digest "$id")" ]] || { echo image-readback-mismatch >&2; exit 79; }
health=$($curl_bin -sS -o /dev/null -w '%{http_code}' --max-time 8 http://127.0.0.1:8080/health)
[[ "$health" == 200 ]] || exit 79
version=$($curl_bin -fsS --max-time 8 http://127.0.0.1:8080/api/config)
[[ "$version" == "$rev" ]] || { echo version-readback-mismatch >&2; exit 79; }
jq -n --arg p "$project" --arg rev "$rev" --arg tree "$(jq -r .final_tree "$id")" --arg b "$backend" --arg w "$web" --arg bd "$bd" --arg wd "$wd" --arg h "$health" '{schema:"HiveCrewOperatorApplyReceiptV2",compose_project:$p,final_revision:$rev,final_tree:$tree,backend:{id:$b,digest:$bd},web:{id:$w,digest:$wd},health_http:($h|tonumber),version_readback:true,pre_apply_snapshot:"PRE-APPLY-SNAPSHOT.json",rollback_receipt:"ROLLBACK-RECEIPT.json",external_acceptance:"EXTERNAL-ACCEPTANCE.json",secret_values_recorded:false,production_applied:false,run_06:false}' > "$receipt"
trap - EXIT
printf '%s\n' "apply=pass receipt=$receipt"
