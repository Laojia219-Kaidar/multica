#!/bin/sh
set -eu

token_file=${HIVECOSM_AUTHORITY_BEARER_TOKEN_FILE:-}
if [ -n "$token_file" ]; then
  [ -r "$token_file" ] || { echo "authority wiring: configured token file is not readable" >&2; exit 2; }
  token=$(cat "$token_file")
  [ -n "$token" ] || { echo "authority wiring: configured token file is empty" >&2; exit 2; }
  export HIVECOSM_AUTHORITY_BEARER_TOKEN="$token"
elif [ -z "${HIVECOSM_AUTHORITY_BEARER_TOKEN:-}" ]; then
  echo "authority wiring: bearer token file/reference is required when authority URL is configured" >&2
  exit 2
fi

exec /app/entrypoint.sh
