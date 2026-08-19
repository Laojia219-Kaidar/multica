#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

make_curl() {
  client_dir=$1
  mkdir -p "$client_dir"
  cat > "$client_dir/curl" <<'EOF'
#!/bin/sh
set -eu
request_url=
has_tenant_header=false
while [ "$#" -gt 0 ]; do
  if [ "$1" = -H ]; then
    shift
    case "${1:-}" in X-HiveCosm-Tenant:*) has_tenant_header=true ;; esac
  fi
  request_url=$1
  shift
done
case "$request_url" in
  */bff/health) status=${FAKE_HEALTH_STATUS:-200} ;;
  *)
    [ "$has_tenant_header" = true ] || exit 90
    status=${FAKE_ANONYMOUS_STATUS:-401}
    ;;
esac
printf 'curl\n' >> "${FAKE_HTTP_LOG:?}"
printf '%s' "$status"
EOF
  chmod 755 "$client_dir/curl"
}

make_wget() {
  client_dir=$1
  mkdir -p "$client_dir"
  cat > "$client_dir/wget" <<'EOF'
#!/bin/sh
set -eu
request_url=
has_tenant_header=false
while [ "$#" -gt 0 ]; do
  if [ "$1" = --header ]; then
    shift
    case "${1:-}" in X-HiveCosm-Tenant:*) has_tenant_header=true ;; esac
  fi
  request_url=$1
  shift
done
case "$request_url" in
  */bff/health) status=${FAKE_HEALTH_STATUS:-200} ;;
  *)
    [ "$has_tenant_header" = true ] || exit 90
    status=${FAKE_ANONYMOUS_STATUS:-401}
    ;;
esac
printf 'wget\n' >> "${FAKE_HTTP_LOG:?}"
if [ "${FAKE_WGET_NO_STATUS:-0}" = 1 ]; then
  echo 'fixture transport failed without response' >&2
  exit 1
fi
printf '  HTTP/1.1 %s fixture\n' "$status" >&2
[ "$status" = 200 ]
EOF
  chmod 755 "$client_dir/wget"
}

run_preflight() {
  client_path=$1
  PATH="$client_path" FAKE_HTTP_LOG="$tmp/http.log" \
    HIVECOSM_AUTHORITY_BASE_URL=http://authority.test \
    HIVECOSM_TENANT_ID=tenant-fixture \
    HIVECOSM_AUTHORITY_PREFLIGHT_TIMEOUT_SECONDS=1 \
    /bin/sh "$root/authority-preflight.sh"
}

curl_dir="$tmp/curl"
make_curl "$curl_dir"
make_wget "$curl_dir"
: > "$tmp/http.log"
run_preflight "$curl_dir" | grep -F 'client=curl' >/dev/null
[ "$(grep -c '^curl$' "$tmp/http.log")" -eq 6 ]
! grep -F wget "$tmp/http.log" >/dev/null
echo authority_preflight_curl_preferred=PASS

wget_dir="$tmp/wget"
make_wget "$wget_dir"
: > "$tmp/http.log"
run_preflight "$wget_dir" | grep -F 'client=wget' >/dev/null
[ "$(grep -c '^wget$' "$tmp/http.log")" -eq 6 ]
echo authority_preflight_busybox_wget_fallback=PASS

empty_dir="$tmp/empty"
mkdir -p "$empty_dir"
if run_preflight "$empty_dir" >"$tmp/no-client.out" 2>"$tmp/no-client.err"; then
  exit 1
fi
grep -F 'no supported HTTP client' "$tmp/no-client.err" >/dev/null
echo authority_preflight_no_client_fail_closed=PASS

: > "$tmp/http.log"
if FAKE_HEALTH_STATUS=204 run_preflight "$curl_dir" >"$tmp/health.out" 2>"$tmp/health.err"; then
  exit 1
fi
grep -F 'health returned 204 (expected 200)' "$tmp/health.err" >/dev/null
echo authority_preflight_health_exact_status=PASS

: > "$tmp/http.log"
if FAKE_WGET_NO_STATUS=1 run_preflight "$wget_dir" >"$tmp/no-status.out" 2>"$tmp/no-status.err"; then
  exit 1
fi
grep -F 'authority preflight: HTTP request failed' "$tmp/no-status.err" >/dev/null
echo authority_preflight_busybox_wget_no_status_fail_closed=PASS

: > "$tmp/http.log"
if FAKE_ANONYMOUS_STATUS=403 run_preflight "$wget_dir" >"$tmp/anonymous.out" 2>"$tmp/anonymous.err"; then
  exit 1
fi
grep -F 'returned 403 (expected 401)' "$tmp/anonymous.err" >/dev/null
echo authority_preflight_anonymous_exact_status=PASS

! grep -F HIVECOSM_AUTHORITY_BEARER_TOKEN "$root/authority-preflight.sh" >/dev/null
echo authority_preflight_fake_matrix=PASS
