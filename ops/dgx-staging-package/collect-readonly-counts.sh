#!/usr/bin/env bash
set -Eeuo pipefail
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
source "$root/common.sh"
docker_bin=$(resolve_executable "${DOCKER_BIN:-docker}")
postgres_container=${POSTGRES_CONTAINER:-multica-dgx-ultra-postgres-1}
postgres_user=${POSTGRES_USER:-multica}
postgres_db=${POSTGRES_DB:-multica}

sql="SELECT json_build_object(
  'schema_top', COALESCE((SELECT max((substring(version FROM '^[0-9]+'))::bigint)
                          FROM schema_migrations WHERE version ~ '^[0-9]+'), 0),
  'agent_count', (SELECT count(*) FROM agent),
  'project_count', (SELECT count(*) FROM project),
  'status', 'collected_read_only');"
result=$($docker_bin exec -e 'PGOPTIONS=-c default_transaction_read_only=on' "$postgres_container" \
  psql -X -qAt -v ON_ERROR_STOP=1 -U "$postgres_user" -d "$postgres_db" -c "$sql")
printf '%s\n' "$result" | jq -e '
  .status == "collected_read_only" and
  (.schema_top | type == "number") and
  (.agent_count | type == "number") and
  (.project_count | type == "number")
' >/dev/null || { echo invalid-read-only-counts >&2; exit 79; }
printf '%s\n' "$result"
