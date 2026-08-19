#!/usr/bin/env bash
set -Eeuo pipefail

readonly GOVERNED_STAGING_DEPLOY_DIR=/srv/hivecosm/52-staging/multica-dgx-ultra/4ab2c72c27e0ecf38f32cd3f6f1274350a80efca

resolve_executable() {
  local requested=${1:?executable required} resolved
  if [[ "$requested" == */* ]]; then
    resolved=$(realpath -- "$requested")
  else
    resolved=$(command -v -- "$requested")
    resolved=$(realpath -- "$resolved")
  fi
  [[ "$resolved" == /* && -f "$resolved" && -x "$resolved" ]] || {
    echo "untrusted-executable:$requested" >&2
    return 78
  }
  printf '%s\n' "$resolved"
}

resolve_deploy_env_file() {
  local deploy_dir=$GOVERNED_STAGING_DEPLOY_DIR
  local canonical_dir env_file canonical_env
  canonical_dir=$(realpath -- "$deploy_dir") || {
    echo governed-staging-env-unavailable >&2
    return 78
  }
  [[ "$deploy_dir" == /* && "$canonical_dir" == "$GOVERNED_STAGING_DEPLOY_DIR" &&
     "$canonical_dir" == "$deploy_dir" && -d "$canonical_dir" ]] || {
    echo governed-staging-env-unavailable >&2
    return 78
  }
  env_file="$GOVERNED_STAGING_DEPLOY_DIR/.env"
  [[ -f "$env_file" && -r "$env_file" && -s "$env_file" && ! -L "$env_file" ]] || {
    echo governed-staging-env-unavailable >&2
    return 78
  }
  canonical_env=$(realpath -- "$env_file") || {
    echo governed-staging-env-unavailable >&2
    return 78
  }
  [[ "$canonical_env" == "$GOVERNED_STAGING_DEPLOY_DIR/.env" &&
     "$canonical_env" == "$env_file" && "${canonical_env%/*}" == "$canonical_dir" ]] || {
    echo governed-staging-env-unavailable >&2
    return 78
  }
  printf '%s\n' "$canonical_env"
}

yaml_string() {
  jq -Rn --arg value "$1" '$value'
}

write_image_override() {
  local backend_ref=${1:?backend ref required}
  local web_ref=${2:?web ref required}
  local output=${3:?override output required}
  local backend_yaml web_yaml
  backend_yaml=$(yaml_string "$backend_ref")
  web_yaml=$(yaml_string "$web_ref")
  umask 077
  {
    printf '%s\n' 'services:'
    printf '%s\n' '  authority-loopback-bridge:'
    printf '    image: %s\n' "$backend_yaml"
    printf '%s\n' '  backend:'
    printf '    image: %s\n' "$backend_yaml"
    printf '%s\n' '  frontend:'
    printf '    image: %s\n' "$web_yaml"
  } > "$output"
}

assert_container_image() {
  local docker_bin=${1:?docker required}
  local container=${2:?container required}
  local expected_ref=${3:?image ref required}
  local expected_id=${4:?image id required}
  local expected_digest=${5:?image digest required}
  local actual_ref actual_id
  actual_ref=$($docker_bin inspect --format '{{.Config.Image}}' "$container")
  actual_id=$($docker_bin inspect --format '{{.Image}}' "$container")
  [[ "$actual_ref" == "$expected_ref" ]] || {
    echo "container-ref-mismatch:$container" >&2
    return 79
  }
  [[ "$actual_id" == "$expected_id" && "$actual_id" == "$expected_digest" ]] || {
    echo "container-image-id-mismatch:$container" >&2
    return 79
  }
}
