#!/usr/bin/env bash
set -Eeuo pipefail

# Runs the continuous-dispatch integration proof only against an ephemeral
# loopback PostgreSQL. It never reuses the shared 5432 development database.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_port="${HIVECREW_ISOLATED_TEST_PORT:-}"

if [[ -z "$test_port" ]]; then
  for candidate_port in $(seq 55435 55460); do
    if ! nc -z 127.0.0.1 "$candidate_port" 2>/dev/null; then
      test_port="$candidate_port"
      break
    fi
  done
fi

if [[ -z "$test_port" || "$test_port" == "5432" ]]; then
  echo "select a free non-5432 HIVECREW_ISOLATED_TEST_PORT" >&2
  exit 2
fi

if nc -z 127.0.0.1 "$test_port" 2>/dev/null; then
  echo "isolated test port $test_port is already in use" >&2
  exit 2
fi

suffix="${RANDOM}_$$"
container_name="hivecrew-continuous-dispatch-test-db-${suffix}"
database_name="hivecrew_continuous_dispatch_test_${suffix}"
database_url="postgres://hivecrew_test:hivecrew_test_only@127.0.0.1:${test_port}/${database_name}?sslmode=disable"
container_id=""

cleanup() {
  docker rm -f "$container_name" >/dev/null 2>&1 || true
  if [[ -n "$container_id" ]] && docker container inspect "$container_id" >/dev/null 2>&1; then
    echo "isolated test container $container_name remained after cleanup" >&2
    return 1
  fi
}
trap cleanup EXIT

container_id="$(docker run -d --rm --name "$container_name" \
  -e POSTGRES_DB="$database_name" \
  -e POSTGRES_USER=hivecrew_test \
  -e POSTGRES_PASSWORD=hivecrew_test_only \
  -p "127.0.0.1:${test_port}:5432" \
  pgvector/pgvector:pg17)"

for _ in $(seq 1 30); do
  if docker exec "$container_name" pg_isready -U hivecrew_test -d "$database_name" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

if ! docker exec "$container_name" pg_isready -U hivecrew_test -d "$database_name" >/dev/null 2>&1; then
  echo "isolated PostgreSQL did not become ready" >&2
  exit 1
fi

cd "$repo_root/server"
DATABASE_URL="$database_url" go run ./cmd/migrate up
DATABASE_URL="$database_url" HIVECREW_ISOLATED_TEST_PORT="$test_port" \
  go test -race ./internal/service -run 'Test(ContinuousDispatchReceiptRepositoryExactReplayAndConflict|ContinuousDispatchReceiptRepositoryConcurrentExactReplayCreatesOneRow|WriteLease_)' -count=1 -v
DATABASE_URL="$database_url" HIVECREW_ISOLATED_TEST_PORT="$test_port" \
  go test -race ./internal/service -run '^TestProductionContinuousDispatch' -count=1 -v
DATABASE_URL="$database_url" HIVECREW_ISOLATED_TEST_PORT="$test_port" \
  go test -race ./internal/service -run '^TestProductionCompanyOps' -count=1 -v
