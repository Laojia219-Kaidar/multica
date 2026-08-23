#!/bin/sh
set -eu

base_url=${HIVECOSM_AUTHORITY_BASE_URL:-}
tenant=${HIVECOSM_TENANT_ID:-}
timeout=${HIVECOSM_AUTHORITY_PREFLIGHT_TIMEOUT_SECONDS:-5}

case "$base_url" in
  http://*|https://*) ;;
  *) echo "authority preflight: HIVECOSM_AUTHORITY_BASE_URL must be an HTTP(S) URL" >&2; exit 2 ;;
esac
[ -n "$tenant" ] || { echo "authority preflight: HIVECOSM_TENANT_ID is required" >&2; exit 2; }
case "$timeout" in
  ''|*[!0-9]*|0) echo "authority preflight: timeout must be a positive integer" >&2; exit 2 ;;
esac

if command -v curl >/dev/null 2>&1; then
  http_client=curl
elif command -v wget >/dev/null 2>&1; then
  http_client=wget
else
  echo "authority preflight: no supported HTTP client (curl or wget)" >&2
  exit 127
fi

authority_http_status() {
  request_url=$1
  request_header=${2:-}
  status=

  if [ "$http_client" = curl ]; then
    if [ -n "$request_header" ]; then
      status=$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
        --max-time "$timeout" -H "$request_header" "$request_url") || {
        echo "authority preflight: HTTP request failed" >&2
        return 4
      }
    else
      status=$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
        --max-time "$timeout" "$request_url") || {
        echo "authority preflight: HTTP request failed" >&2
        return 4
      }
    fi
  else
    wget_output=
    if [ -n "$request_header" ]; then
      if wget_output=$(wget -S --spider -T "$timeout" --header "$request_header" \
          "$request_url" 2>&1); then
        :
      else
        : # BusyBox wget exits non-zero for expected anonymous HTTP 401.
      fi
    else
      if wget_output=$(wget -S --spider -T "$timeout" "$request_url" 2>&1); then
        :
      else
        :
      fi
    fi
    while IFS= read -r response_line; do
      set -- $response_line
      case "${1:-}" in
        HTTP/*)
          case "${2:-}" in
            [0-9][0-9][0-9]) [ -n "$status" ] || status=$2 ;;
          esac
          ;;
      esac
    done <<EOF
$wget_output
EOF
    [ -n "$status" ] || {
      echo "authority preflight: HTTP request failed" >&2
      return 4
    }
  fi

  case "$status" in
    [0-9][0-9][0-9]) printf '%s\n' "$status" ;;
    *) echo "authority preflight: invalid HTTP status" >&2; return 4 ;;
  esac
}

health_status=$(authority_http_status "$base_url/bff/health") || exit $?
[ "$health_status" = 200 ] || {
  echo "authority preflight: health returned $health_status (expected 200)" >&2
  exit 3
}

for route in \
  /api/company-ops/organization \
  /api/company-ops/employees \
  /api/company-ops/dispatch-authorization \
  /api/company-ops/issue-dispatch-authorization \
  /api/company-ops/owner-work-context
do
  status=$(authority_http_status "$base_url$route" "X-HiveCosm-Tenant: $tenant") || exit $?
  [ "$status" = 401 ] || { echo "authority preflight: $route returned $status (expected 401)" >&2; exit 3; }
done

echo "authority preflight: health=200 anonymous_routes=401 tenant_configured=true client=$http_client"
