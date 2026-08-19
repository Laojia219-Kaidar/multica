#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
pkg=${1:?usage: apply-staging.sh CANDIDATE_DIR}
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
project=multica-dgx-ultra
"$root/precheck.sh" "$pkg"
id="$pkg/INTEGRATION-IDENTITY.json"
out="$pkg/receipts"; mkdir -p "$out"
docker_bin=${DOCKER_BIN:-docker}; compose_bin=${COMPOSE_BIN:-docker}; curl_bin=${CURL_BIN:-curl}
snapshot="$out/PRE-APPLY-SNAPSHOT.json"; receipt="$out/OPERATOR-APPLY-RECEIPT.json"
applied=false
rollback(){ rc=$?; trap - ERR; if [[ "$applied" == true ]]; then "$root/rollback-staging.sh" "$pkg" "$rc" || true; fi; exit "$rc"; }
trap rollback ERR
before="$($docker_bin ps --filter "label=com.docker.compose.project=$project" --format '{{.ID}} {{.Image}}' 2>/dev/null || true)"
jq -n --arg p "$project" --arg b "$before" --argjson r "$(jq -c .rollback_predecessor "$id")" '{schema:"HiveCrewPreApplySnapshotV1",compose_project:$p,observed_containers:$b,rollback_predecessor:$r,mutation_started:false}' > "$snapshot"
backend=$(jq -r .backend_image.id "$id"); web=$(jq -r .web_image.id "$id")
[[ -n "$backend" && -n "$web" ]] || exit 78
"$compose_bin" compose -f "$pkg/compose.yaml" -p "$project" up -d --no-deps backend frontend
applied=true
backend_readback="$($docker_bin inspect --format '{{.Config.Image}}' "$backend" 2>/dev/null || true)"
web_readback="$($docker_bin inspect --format '{{.Config.Image}}' "$web" 2>/dev/null || true)"
[[ "$backend_readback" == "$backend" && "$web_readback" == "$web" ]] || { echo 'image readback mismatch' >&2; exit 79; }
health="$($curl_bin -sS -o /dev/null -w '%{http_code}' --max-time 8 http://127.0.0.1:8080/health)"
[[ "$health" == 200 ]] || exit 79
jq -n --arg p "$project" --arg rev "$(jq -r .final_revision "$id")" --arg tree "$(jq -r .final_tree "$id")" --arg b "$backend" --arg w "$web" --arg h "$health" '{schema:"HiveCrewOperatorApplyReceiptV1",compose_project:$p,final_revision:$rev,final_tree:$tree,backend_image_id:$b,web_image_id:$w,health_http:($h|tonumber),rollback_receipt:"ROLLBACK-RECEIPT.json",external_acceptance:"EXTERNAL-ACCEPTANCE.json",secret_values_recorded:false,production_applied:false,run_06:false}' > "$receipt"
trap - ERR
printf 'apply=pass receipt=%s\n' "$receipt"
