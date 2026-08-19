#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
compose_root=$(CDPATH= cd -- "$root/../.." && pwd)

sh -n "$root"/*.sh
"$root/authority-preflight-test.sh"
! grep -Eq 'HIVECOSM_AUTHORITY_BEARER_TOKEN:-|HIVECOSM_AUTHORITY_BEARER_TOKEN:=|\$\{HIVECOSM_AUTHORITY_BEARER_TOKEN\}' "$root/authority-entrypoint.sh"
grep -F 'HIVECOSM_AUTHORITY_BASE_URL:?Set' "$root/docker-compose.dgx-authority.yml" >/dev/null
grep -F 'HIVECOSM_TENANT_ID:?Set' "$root/docker-compose.dgx-authority.yml" >/dev/null
grep -F 'HIVECOSM_AUTHORITY_TOKEN_FILE:?Set' "$root/docker-compose.dgx-authority.yml" >/dev/null
grep -F 'preflight_script' "$root/authority-entrypoint.sh" >/dev/null

token_fixture=$(mktemp)
rendered=$(mktemp)
preflight_fixture=$(mktemp)
entrypoint_fixture=$(mktemp)
marker=$(mktemp)
trap 'rm -f "$token_fixture" "$rendered" "$preflight_fixture" "$entrypoint_fixture" "$marker"' EXIT
chmod 0600 "$token_fixture"
printf 'x' >"$token_fixture"

if (cd "$compose_root" && env -u HIVECOSM_AUTHORITY_BASE_URL -u HIVECOSM_TENANT_ID \
  HIVECOSM_AUTHORITY_TOKEN_FILE="$token_fixture" docker compose \
  -f docker-compose.selfhost.yml -f deploy/dgx-authority/docker-compose.dgx-authority.yml config >/dev/null 2>&1); then
  echo "compose negative gate unexpectedly passed" >&2
  exit 1
fi

(cd "$compose_root" && HIVECOSM_AUTHORITY_BASE_URL=http://host.docker.internal:3150 \
  HIVECOSM_TENANT_ID=tenant-fixture HIVECOSM_AUTHORITY_TOKEN_FILE="$token_fixture" \
  docker compose -f docker-compose.selfhost.yml \
  -f deploy/dgx-authority/docker-compose.dgx-authority.yml config >"$rendered")
grep -F 'host.docker.internal:3150' "$rendered" >/dev/null
grep -F '/run/secrets/hivecosm_authority_bearer_token' "$rendered" >/dev/null
! grep -F 'HIVECOSM_AUTHORITY_BEARER_TOKEN: x' "$rendered" >/dev/null

cat >"$preflight_fixture" <<'EOF'
#!/bin/sh
set -eu
touch "${PREFLIGHT_MARKER:?}"
EOF
cat >"$entrypoint_fixture" <<'EOF'
#!/bin/sh
set -eu
[ -f "${PREFLIGHT_MARKER:?}" ]
[ -n "${HIVECOSM_AUTHORITY_BEARER_TOKEN:-}" ]
echo authority-entrypoint-compatibility-pass
EOF
chmod 0700 "$preflight_fixture" "$entrypoint_fixture"
PREFLIGHT_MARKER="$marker" HIVECOSM_AUTHORITY_BEARER_TOKEN_FILE="$token_fixture" \
  HIVECOSM_AUTHORITY_PREFLIGHT_SCRIPT="$preflight_fixture" \
  HIVECREW_ORIGINAL_ENTRYPOINT="$entrypoint_fixture" \
  "$root/authority-entrypoint.sh" | grep -F authority-entrypoint-compatibility-pass >/dev/null

if HIVECOSM_AUTHORITY_BEARER_TOKEN=x HIVECOSM_AUTHORITY_PREFLIGHT_SCRIPT="$preflight_fixture" \
  HIVECREW_ORIGINAL_ENTRYPOINT="$entrypoint_fixture" "$root/authority-entrypoint.sh" >/dev/null 2>&1; then
  echo "direct token env fallback unexpectedly passed" >&2
  exit 1
fi

echo "authority wiring tests: shell/static/compose/negative/entrypoint PASS"
