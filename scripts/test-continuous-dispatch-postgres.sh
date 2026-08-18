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
  if [[ -z "$container_id" ]]; then
    return 0
  fi
  if ! docker rm -f "$container_id" >/dev/null 2>&1; then
    if docker container inspect "$container_id" >/dev/null 2>&1; then
      echo "failed to remove isolated test container $container_id" >&2
      return 1
    fi
  fi
  if [[ -n "$container_id" ]] && docker container inspect "$container_id" >/dev/null 2>&1; then
    echo "isolated test container $container_id remained after cleanup" >&2
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

ready=false
for _ in $(seq 1 30); do
	if docker exec "$container_name" pg_isready -U hivecrew_test -d "$database_name" >/dev/null 2>&1; then
		# The image briefly accepts a local probe during its initdb handoff on
		# some Docker Desktop runs. Require a second clean probe before applying
		# migrations so the integration test never mistakes that transition for a
		# stable database.
		sleep 1
		if docker exec "$container_name" pg_isready -U hivecrew_test -d "$database_name" >/dev/null 2>&1; then
			ready=true
			break
		fi
	fi
	sleep 1
done

if [[ "$ready" != "true" ]]; then
  echo "isolated PostgreSQL did not become ready" >&2
  exit 1
fi

cd "$repo_root/server"
DATABASE_URL="$database_url" go run ./cmd/migrate up

echo "== C3b2 migration rehearsal evidence =="
DATABASE_URL="$database_url" HIVECREW_ISOLATED_TEST_PORT="$test_port" \
  go test -race ./internal/migrations -run '^TestC3b2DurableReceiptMigrationsUpDownUp$' -count=1 -v

echo "== C3b2 Owner gate evidence =="
DATABASE_URL="$database_url" HIVECREW_ISOLATED_TEST_PORT="$test_port" \
  go test -race ./internal/service -run '^(TestCompanyOpsArtifactOwnerGateRejectsNonOwnersBeforeAuthority|TestCompanyOpsArtifactPromotionRejectsApprovalActorAndCandidateDrift|TestCompanyOpsArtifactPromotionRejectsLegacyApprovalWithoutActor|TestCompanyOpsArtifactPromotionRejectsDuplicateAndSupersedingApprovalStates)$' -count=1 -v

echo "== C3b2 artifact delivery repository/SQL evidence =="
DATABASE_URL="$database_url" HIVECREW_ISOLATED_TEST_PORT="$test_port" \
  go test -race ./internal/companyops -run '^TestArtifactPromotionDeliveryPostgres' -count=1 -v

echo "== mediated Linux handler E2E evidence (explicitly required) =="
DATABASE_URL="$database_url" HIVECREW_ISOLATED_TEST_PORT="$test_port" \
  go test -race ./internal/service -run 'Test(ContinuousDispatchReceiptRepositoryExactReplayAndConflict|ContinuousDispatchReceiptRepositoryConcurrentExactReplayCreatesOneRow|WriteLease_|FinalizeTaskClaimFailureRollsBackTokenThenRequeue)' -count=1 -v
DATABASE_URL="$database_url" HIVECREW_ISOLATED_TEST_PORT="$test_port" HIVECREW_ISOLATED_TEST_REQUIRED=1 \
  go test -race ./internal/handler_e2e -run '^TestWriterLeaseHandlerRemoteTerminalE2E$' -count=1 -v
DATABASE_URL="$database_url" HIVECREW_ISOLATED_TEST_PORT="$test_port" \
  go test -race ./internal/service -run '^TestProductionContinuousDispatch' -count=1 -v
DATABASE_URL="$database_url" HIVECREW_ISOLATED_TEST_PORT="$test_port" \
  go test -race ./internal/service -run '^TestProductionCompanyOps' -count=1 -v
echo "== VC03 provider 429 terminal truth evidence =="
DATABASE_URL="$database_url" HIVECREW_ISOLATED_TEST_PORT="$test_port" \
  go test -race ./internal/service -run '^TestCompanyOpsExecutionLifecycle_Provider429StaysFailedThroughReceiptAndProjection$' -count=1 -v
go test -race ./internal/readyfrontier -run '^TestClassifyIssue_TerminalStatusWinsLatestFailedTask$' -count=1 -v
DATABASE_URL="$database_url" HIVECREW_ISOLATED_TEST_PORT="$test_port" \
  go test -race ./internal/service -run '^TestWriterLeaseTerminalPostgresAtomicFence$' -count=1 -v
