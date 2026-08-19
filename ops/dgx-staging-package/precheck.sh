#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
pkg=${1:?usage: precheck.sh CANDIDATE_DIR}
id="$pkg/INTEGRATION-IDENTITY.json"
compose="$pkg/compose.yaml"
command -v jq >/dev/null || { echo missing-jq >&2; exit 127; }
[[ -r "$id" && -r "$compose" ]] || { echo missing-candidate-files >&2; exit 78; }
jq -e '(.schema == "HiveCrewIntegrationIdentityV1") and (.compose_project == "multica-dgx-ultra") and (.final_revision|type == "string" and test("^[0-9a-f]{40}$")) and (.final_tree|type == "string" and test("^[0-9a-f]{40}$")) and (.source_archive_sha256|test("^[0-9a-f]{64}$")) and (.authority_overlay_sha256|test("^[0-9a-f]{64}$")) and (.compose_sha256|test("^[0-9a-f]{64}$")) and (.backend_image.id|type == "string" and length>0) and (.backend_image.digest|test("^sha256:[0-9a-f]{64}$")) and (.web_image.id|type == "string" and length>0) and (.web_image.digest|test("^sha256:[0-9a-f]{64}$")) and (.rollback_predecessor.backend_image|type == "string" and length>0) and (.rollback_predecessor.backend_digest|test("^sha256:[0-9a-f]{64}$")) and (.rollback_predecessor.web_image|type == "string" and length>0) and (.rollback_predecessor.web_digest|test("^sha256:[0-9a-f]{64}$"))' "$id" >/dev/null || { echo invalid-identity >&2; exit 78; }
actual=$(sha256sum "$compose"|awk '{print $1}')
expected=$(jq -r .compose_sha256 "$id")
[[ "$actual" == "$expected" ]] || { echo compose-hash-mismatch >&2; exit 78; }
printf '%s\n' 'precheck=pass package_valid=true runtime_mutation=false'
