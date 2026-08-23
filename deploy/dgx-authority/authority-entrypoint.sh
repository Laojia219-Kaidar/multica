#!/bin/sh
set -eu

token_file=${HIVECOSM_AUTHORITY_BEARER_TOKEN_FILE:-}
[ -n "$token_file" ] || { echo "authority wiring: bearer token file/reference is required" >&2; exit 2; }
[ -r "$token_file" ] || { echo "authority wiring: configured token file is not readable" >&2; exit 2; }
token=$(cat "$token_file")
[ -n "$token" ] || { echo "authority wiring: configured token file is empty" >&2; exit 2; }

preflight_script=${HIVECOSM_AUTHORITY_PREFLIGHT_SCRIPT:-/opt/hivecrew-authority/authority-preflight.sh}
[ -x "$preflight_script" ] || { echo "authority wiring: preflight script is not executable" >&2; exit 2; }
"$preflight_script"

# The current Go authority client accepts only HIVECOSM_AUTHORITY_BEARER_TOKEN.
# Keep the value out of logs/artifacts and pass it only across this exec boundary.
# The resulting backend process can expose its environment to same-UID /proc
# readers; replace the client with file-aware secret loading before claiming that
# exposure is eliminated.
exec env "HIVECOSM_AUTHORITY_BEARER_TOKEN=$token" \
  "${HIVECREW_ORIGINAL_ENTRYPOINT:-/app/entrypoint.sh}"
