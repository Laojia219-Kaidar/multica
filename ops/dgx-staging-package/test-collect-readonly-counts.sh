#!/usr/bin/env bash
set -Eeuo pipefail

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT
fake_docker="$tmp/docker"
docker_log="$tmp/docker.log"

cat > "$fake_docker" <<'DOCKER'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%q ' "$@" >> "${FAKE_DOCKER_LOG:?}"
printf '\n' >> "$FAKE_DOCKER_LOG"
[[ "${1:-}" == exec ]]
[[ "${2:-}" == -e ]]
[[ "${3:-}" == 'PGOPTIONS=-c default_transaction_read_only=on' ]]
sql=${@: -1}
[[ "$sql" == *"substring(version FROM '^[0-9]+')"* ]]
[[ "$sql" != *'max(version)'* ]]
version=${FAKE_SCHEMA_VERSION:?}
if [[ "$version" =~ ^([0-9]+) ]]; then
  schema_top=$((10#${BASH_REMATCH[1]}))
else
  schema_top=0
fi
jq -cn --argjson schema_top "$schema_top" \
  '{schema_top:$schema_top,agent_count:36,project_count:13,status:"collected_read_only"}'
DOCKER
chmod 755 "$fake_docker"
export FAKE_DOCKER_LOG="$docker_log"

run_case() {
  local version=${1:?version fixture required}
  local expected=${2:?expected numeric schema required}
  local output
  : > "$docker_log"
  output=$(DOCKER_BIN="$fake_docker" FAKE_SCHEMA_VERSION="$version" \
    "$root/collect-readonly-counts.sh")
  printf '%s\n' "$output" | jq -e \
    --argjson expected "$expected" \
    '.schema_top == $expected and .agent_count == 36 and .project_count == 13 and
     .status == "collected_read_only"' >/dev/null
  grep -F 'default_transaction_read_only=on' "$docker_log" >/dev/null
  printf 'case=text_schema_version_%s PASS\n' "$version"
}

run_case 415_project_revision 415
run_case 009_legacy_revision 9
run_case project_revision_without_prefix 0
printf 'READONLY_COUNTS_TEXT_VERSION_TEST_PASS\n'
