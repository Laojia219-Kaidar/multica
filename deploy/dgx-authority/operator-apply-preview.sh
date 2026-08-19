#!/bin/sh
set -eu

: "${HIVECOSM_AUTHORITY_TOKEN_FILE:?Set HIVECOSM_AUTHORITY_TOKEN_FILE in the operator environment; its value is never printed}"
: "${HIVECOSM_TENANT_ID:?Set HIVECOSM_TENANT_ID in the operator environment}"

case "$HIVECOSM_AUTHORITY_TOKEN_FILE" in
  /*) ;;
  *) echo "operator preview: token file must be an absolute path" >&2; exit 2 ;;
esac
[ -r "$HIVECOSM_AUTHORITY_TOKEN_FILE" ] || {
  echo "operator preview: token file is not readable (value not displayed)" >&2
  exit 2
}

echo "operator preview only; no deployment was executed"
echo 'docker compose -f docker-compose.selfhost.yml -f deploy/dgx-authority/docker-compose.dgx-authority.yml config'
echo 'docker compose -f docker-compose.selfhost.yml -f deploy/dgx-authority/docker-compose.dgx-authority.yml up -d backend'
echo 'HIVECOSM_AUTHORITY_BASE_URL defaults to http://host.docker.internal:3150; override only with a governed host route'
