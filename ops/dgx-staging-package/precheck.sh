#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
pkg=${1:?usage: precheck.sh CANDIDATE_DIR}
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
source "$root/common.sh"

id="$pkg/INTEGRATION-IDENTITY.json"
compose="$pkg/compose.yaml"
rollback_compose="$pkg/rollback-compose.yaml"
bridge_compose="$root/docker-compose.loopback-bridge.yml"
command -v jq >/dev/null || { echo missing-jq >&2; exit 127; }
[[ -r "$id" && -r "$compose" && -r "$rollback_compose" && -r "$bridge_compose" ]] || { echo missing-candidate-files >&2; exit 78; }
for asset in authority-bridge-resolve.sh authority-bridge-port-check.sh authority-bridge-stop.sh; do
  [[ -f "$root/$asset" && -x "$root/$asset" && ! -L "$root/$asset" ]] || {
    echo "missing-authority-bridge-asset:$asset" >&2
    exit 78
  }
done

docker_bin=$(resolve_executable "${DOCKER_BIN:-docker}")
curl_bin=$(resolve_executable "${CURL_BIN:-curl}")
counts_bin=$(resolve_executable "${COUNTS_BIN:-$root/collect-readonly-counts.sh}")

jq -e '
  def sha40: type == "string" and test("^[0-9a-f]{40}$");
  def sha64: type == "string" and test("^[0-9a-f]{64}$");
  def image_ref: type == "string" and length > 0 and length <= 255 and test("^[A-Za-z0-9][A-Za-z0-9._:/-]*$");
  def image_id: type == "string" and test("^sha256:[0-9a-f]{64}$");
  (.schema == "HiveCrewIntegrationIdentityV3") and
  (.compose_project == "multica-dgx-ultra") and
  (.final_revision | sha40) and (.final_tree | sha40) and
  (.source_archive_sha256 | sha64) and
  (.authority_overlay_sha256 | sha64) and
  (.authority_bridge.resolver_sha256 | sha64) and
  (.authority_bridge.port_check_sha256 | sha64) and
  (.authority_bridge.stop_sha256 | sha64) and
  (.authority_bridge.compose_sha256 | sha64) and
  (.authority_bridge.binary_path == "/app/authority-loopback-bridge") and
  (.authority_bridge.bind_port == 3151) and
  (.authority_bridge.target == "127.0.0.1:3150") and
  (.compose_sha256 | sha64) and
  (.api.server_version | type == "string" and test("^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$")) and
  (.backend_image.ref | image_ref) and (.backend_image.id | image_id) and (.backend_image.digest | image_id) and
  (.web_image.ref | image_ref) and (.web_image.id | image_id) and (.web_image.digest | image_id) and
  (.rollback_predecessor.backend.ref | image_ref) and
  (.rollback_predecessor.backend.id | image_id) and
  (.rollback_predecessor.backend.digest | image_id) and
  (.rollback_predecessor.web.ref | image_ref) and
  (.rollback_predecessor.web.id | image_id) and
  (.rollback_predecessor.web.digest | image_id)
  and (.rollback_predecessor.compose_sha256 | sha64)
' "$id" >/dev/null || { echo invalid-identity >&2; exit 78; }

actual=$(sha256sum "$compose" | awk '{print $1}')
expected=$(jq -r .compose_sha256 "$id")
[[ "$actual" == "$expected" ]] || { echo compose-hash-mismatch >&2; exit 78; }
actual=$(sha256sum "$rollback_compose" | awk '{print $1}')
expected=$(jq -r .rollback_predecessor.compose_sha256 "$id")
[[ "$actual" == "$expected" ]] || { echo rollback-compose-hash-mismatch >&2; exit 78; }

for spec in \
  'authority-bridge-resolve.sh:.authority_bridge.resolver_sha256' \
  'authority-bridge-port-check.sh:.authority_bridge.port_check_sha256' \
  'authority-bridge-stop.sh:.authority_bridge.stop_sha256' \
  'docker-compose.loopback-bridge.yml:.authority_bridge.compose_sha256'; do
  asset=${spec%%:*}
  selector=${spec#*:}
  actual=$(sha256sum "$root/$asset" | awk '{print $1}')
  expected=$(jq -r "$selector" "$id")
  [[ "$actual" == "$expected" ]] || { echo "authority-bridge-asset-hash-mismatch:$asset" >&2; exit 78; }
done

printf 'precheck=pass docker=%s curl=%s counts=%s runtime_mutation=false\n' \
  "$docker_bin" "$curl_bin" "$counts_bin"
