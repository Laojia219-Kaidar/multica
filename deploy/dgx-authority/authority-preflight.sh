#!/bin/sh
set -eu

base_url=${HIVECOSM_AUTHORITY_BASE_URL:-}
tenant=${HIVECOSM_TENANT_ID:-}

case "$base_url" in
  http://*|https://*) ;;
  *) echo "authority preflight: HIVECOSM_AUTHORITY_BASE_URL must be an HTTP(S) URL" >&2; exit 2 ;;
esac
[ -n "$tenant" ] || { echo "authority preflight: HIVECOSM_TENANT_ID is required" >&2; exit 2; }

curl --fail --silent --show-error --max-time "${HIVECOSM_AUTHORITY_PREFLIGHT_TIMEOUT_SECONDS:-5}" \
  "$base_url/bff/health" >/dev/null

for route in \
  /api/company-ops/organization \
  /api/company-ops/employees \
  /api/company-ops/dispatch-authorization \
  /api/company-ops/issue-dispatch-authorization \
  /api/company-ops/owner-work-context
do
  status=$(curl --silent --output /dev/null --write-out '%{http_code}' \
    --max-time "${HIVECOSM_AUTHORITY_PREFLIGHT_TIMEOUT_SECONDS:-5}" \
    -H "X-HiveCosm-Tenant: $tenant" "$base_url$route")
  [ "$status" = 401 ] || { echo "authority preflight: $route returned $status (expected 401)" >&2; exit 3; }
done

echo "authority preflight: health=200 anonymous_routes=401 tenant_configured=true"
