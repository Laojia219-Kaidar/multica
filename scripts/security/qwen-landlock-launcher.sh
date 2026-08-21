#!/usr/bin/env bash
set -euo pipefail
set +x

readonly QWEN_OPENAI_BASE_URL='https://coding.dashscope.aliyuncs.com/v1'
readonly QWEN_OPENAI_MODEL='qwen3.7-plus'
readonly QWEN_CANONICAL_REAL_HOME='/home/williamdev'
readonly QWEN_CANONICAL_SECRET_FILE='/home/williamdev/.qwen/.env'
readonly QWEN_CANONICAL_LANDLOCK_EXEC='/home/williamdev/.local/libexec/hivecrew-landlock-exec'
readonly QWEN_CANONICAL_QWEN_BIN='/home/williamdev/.local/bin/qwen'
readonly QWEN_EXPECTED_UID='1006'
readonly QWEN_EXPECTED_GID='1006'
readonly QWEN_EXPECTED_LANDLOCK_MODE='755'
readonly QWEN_EXPECTED_QWEN_MODE='775'
readonly QWEN_EXPECTED_LANDLOCK_SHA256='fd33b426d166edf6c6ae46acf049707cf1fa9615c5ef7e8e8226bb2ec34c6ffc'
readonly QWEN_EXPECTED_QWEN_SHA256='f8c2e688c63adaac6e2f5792be6481a3520d1337644b9475332250ac8954689e'
readonly QWEN_BOUNDED_READ_ALLOWED_TOOLS='read_file,glob,grep_search,list_directory'
readonly QWEN_BOUNDED_READ_EXCLUDED_TOOLS='zoom_image,write_file,edit,notebook_edit,run_shell_command,todo_write,save_memory,agent,skill,exit_plan_mode,enter_plan_mode,web_fetch,web_search,image_gen,lsp,ask_user_question,cron_create,cron_list,cron_delete,loop_wakeup,create_sub_session,list_agents,task_stop,task_create,task_update,task_list,team_create,team_delete,team_plan_approval,send_message,structured_output,monitor,tool_search,read_mcp_resource,enter_worktree,exit_worktree,workflow,artifact,record_artifact,get_goal,update_goal,display_image,computer_use__*'
readonly QWEN_BOUNDED_WORKSPACE_ALLOWED_TOOLS='read_file,glob,grep_search,list_directory,edit,write_file'
readonly QWEN_BOUNDED_WORKSPACE_EXCLUDED_TOOLS='zoom_image,notebook_edit,run_shell_command,todo_write,save_memory,agent,skill,exit_plan_mode,enter_plan_mode,web_fetch,web_search,image_gen,lsp,ask_user_question,cron_create,cron_list,cron_delete,loop_wakeup,create_sub_session,list_agents,task_stop,task_create,task_update,task_list,team_create,team_delete,team_plan_approval,send_message,structured_output,monitor,tool_search,read_mcp_resource,enter_worktree,exit_worktree,workflow,artifact,record_artifact,get_goal,update_goal,display_image,computer_use__*'
QWEN_CREDENTIAL_API_KEY=''
QWEN_SANDBOX_ROOT=''

cleanup_qwen_sandbox() {
  local target="${QWEN_SANDBOX_ROOT:-}"
  QWEN_SANDBOX_ROOT=''
  [[ -z "$target" ]] || rm -rf -- "$target"
}

load_qwen_credential_reference() {
  local secret_file="$1"
  local real_home="$2"
  local expected_uid="$3"
  local expected_gid="$4"
  local credential_fd=''
  local fd_metadata=''
  local path_metadata=''
  local path_metadata_after=''
  local line=''
  local key=''
  local value=''
  local api_key=''
  local base_url=''
  local model=''
  local line_count=0

  QWEN_CREDENTIAL_API_KEY=''
  [[ "$real_home" == /* && "$secret_file" == "$real_home/.qwen/.env" ]] || return 1
  [[ -d "$real_home" && ! -L "$real_home" ]] || return 1
  [[ "$(/usr/bin/readlink -f -- "$real_home")" == "$real_home" ]] || return 1
  [[ -e "$secret_file" && ! -L "$secret_file" && -f "$secret_file" ]] || return 1
  [[ "$(/usr/bin/readlink -f -- "$secret_file")" == "$secret_file" ]] || return 1

  exec {credential_fd}<"$secret_file" || return 1
  fd_metadata="$(/usr/bin/stat -Lc '%F|%a|%u|%g|%h|%s|%d|%i|%y|%z' "/proc/self/fd/${credential_fd}")" || {
    exec {credential_fd}<&-
    return 1
  }
  path_metadata="$(/usr/bin/stat -c '%F|%a|%u|%g|%h|%s|%d|%i|%y|%z' "$secret_file")" || {
    exec {credential_fd}<&-
    return 1
  }
  [[ "$fd_metadata" == "$path_metadata" ]] || {
    exec {credential_fd}<&-
    return 1
  }
  [[ "$fd_metadata" == "regular file|600|${expected_uid}|${expected_gid}|1|"* ]] || {
    exec {credential_fd}<&-
    return 1
  }
  [[ "${fd_metadata#regular file|600|${expected_uid}|${expected_gid}|1|}" != "$fd_metadata" ]] || {
    exec {credential_fd}<&-
    return 1
  }
  local credential_size="${fd_metadata#regular file|600|${expected_uid}|${expected_gid}|1|}"
  credential_size="${credential_size%%|*}"
  [[ "$credential_size" =~ ^[0-9]+$ && "$credential_size" -gt 0 && "$credential_size" -le 4096 ]] || {
    exec {credential_fd}<&-
    return 1
  }

  while IFS= read -r line <&"${credential_fd}" || [[ -n "$line" ]]; do
    line_count=$((line_count + 1))
    [[ -n "$line" && "$line" != *$'\r'* && "$line" == *=* ]] || {
      exec {credential_fd}<&-
      return 1
    }
    key="${line%%=*}"
    value="${line#*=}"
    [[ -n "$value" && "$value" != *[[:space:]]* ]] || {
      exec {credential_fd}<&-
      return 1
    }
    case "$key" in
      BAILIAN_CODING_PLAN_API_KEY)
        [[ -z "$api_key" ]] || { exec {credential_fd}<&-; return 1; }
        api_key="$value"
        ;;
      OPENAI_BASE_URL)
        [[ -z "$base_url" ]] || { exec {credential_fd}<&-; return 1; }
        base_url="$value"
        ;;
      OPENAI_MODEL)
        [[ -z "$model" ]] || { exec {credential_fd}<&-; return 1; }
        model="$value"
        ;;
      *)
        exec {credential_fd}<&-
        return 1
        ;;
    esac
  done
  exec {credential_fd}<&-

  [[ "$line_count" == 3 ]] || return 1
  [[ -n "$api_key" && "$base_url" == "$QWEN_OPENAI_BASE_URL" && "$model" == "$QWEN_OPENAI_MODEL" ]] || return 1
  [[ ! -L "$secret_file" && "$(/usr/bin/readlink -f -- "$secret_file")" == "$secret_file" ]] || return 1
  path_metadata_after="$(/usr/bin/stat -c '%F|%a|%u|%g|%h|%s|%d|%i|%y|%z' "$secret_file")" || return 1
  [[ "$path_metadata_after" == "$path_metadata" ]] || return 1

  QWEN_CREDENTIAL_API_KEY="$api_key"
  api_key=''
  return 0
}

validate_trusted_executable() {
  local path="$1"
  local expected_mode="$2"
  local expected_uid="$3"
  local expected_gid="$4"
  local expected_sha256="$5"
  local metadata=''
  local observed_sha256=''

  [[ "$path" == /* && -e "$path" && ! -L "$path" && -f "$path" && -x "$path" ]] || return 1
  [[ "$(/usr/bin/readlink -f -- "$path")" == "$path" ]] || return 1
  metadata="$(/usr/bin/stat -c '%F|%a|%u|%g|%h' "$path")" || return 1
  [[ "$metadata" == "regular file|${expected_mode}|${expected_uid}|${expected_gid}|1" ]] || return 1
  observed_sha256="$(/usr/bin/sha256sum "$path" | /usr/bin/awk '{print $1}')" || return 1
  [[ "$observed_sha256" == "$expected_sha256" ]] || return 1
}

validate_launcher_identity() {
  local launcher_path="$1"
  local expected_uid="$2"
  local expected_gid="$3"
  local metadata=''

  [[ "$launcher_path" == /* && -e "$launcher_path" && ! -L "$launcher_path" && -f "$launcher_path" && -x "$launcher_path" ]] || return 1
  [[ "$(/usr/bin/readlink -f -- "$launcher_path")" == "$launcher_path" ]] || return 1
  metadata="$(/usr/bin/stat -c '%F|%a|%u|%g|%h' "$launcher_path")" || return 1
  [[ "$metadata" == "regular file|755|${expected_uid}|${expected_gid}|1" ]]
}

run_qwen_landlock_launcher() {
  local launcher_path="$1"
  local real_home="$2"
  local secret_file="$3"
  local landlock_exec="$4"
  local qwen_bin="$5"
  local expected_uid="$6"
  local expected_gid="$7"
  local expected_landlock_mode="$8"
  local expected_qwen_mode="$9"
  local expected_landlock_sha256="${10}"
  local expected_qwen_sha256="${11}"
  shift 11
  local api_key=''
  local sandbox_root=''
  local sandbox_home=''
  local status=0
  local governed_policy="${HIVECREW_QWEN_TOOL_POLICY:-deny}"
  local -a governed_args=()

  unset OPENAI_API_KEY BAILIAN_CODING_PLAN_API_KEY OPENAI_BASE_URL OPENAI_MODEL QWEN_MODEL
  [[ "${HIVECREW_QWEN_LANDLOCK_REQUIRED:-1}" == 1 ]] || { echo 'sandbox required' >&2; return 77; }
  if ! validate_launcher_identity "$launcher_path" "$expected_uid" "$expected_gid"; then
    printf '%s\n' 'qwen launcher identity invalid' >&2
    return 78
  fi
  if ! validate_trusted_executable "$landlock_exec" "$expected_landlock_mode" "$expected_uid" "$expected_gid" "$expected_landlock_sha256"; then
    printf '%s\n' 'qwen landlock identity invalid' >&2
    return 78
  fi
  if ! validate_trusted_executable "$qwen_bin" "$expected_qwen_mode" "$expected_uid" "$expected_gid" "$expected_qwen_sha256"; then
    printf '%s\n' 'qwen executable identity invalid' >&2
    return 78
  fi
  case "$governed_policy" in
    deny)
      governed_args=(--approval-mode plan --max-tool-calls 0 --sandbox)
      ;;
    bounded_read)
      governed_args=(
        --approval-mode plan
        --max-tool-calls 8
        --sandbox
        --safe-mode
        --allowed-tools "$QWEN_BOUNDED_READ_ALLOWED_TOOLS"
        --exclude-tools "$QWEN_BOUNDED_READ_EXCLUDED_TOOLS"
      )
      ;;
    bounded_workspace_noshell)
      governed_args=(
        --approval-mode auto-edit
        --max-tool-calls 12
        --sandbox
        --allowed-tools "$QWEN_BOUNDED_WORKSPACE_ALLOWED_TOOLS"
        --exclude-tools "$QWEN_BOUNDED_WORKSPACE_EXCLUDED_TOOLS"
      )
      ;;
    *)
      printf '%s\n' 'qwen governed tool policy invalid' >&2
      return 77
      ;;
  esac
  for arg in "$@"; do
    case "$arg" in
      --auth-type|--auth-type=*|--authType|--authType=*|--model|--model=*|--approval-mode|--approval-mode=*|--max-tool-calls|--max-tool-calls=*|--sandbox|--no-sandbox|--sandbox=*|--safe-mode|--safe-mode=*|--bare|--bare=*|--allowed-tools|--allowed-tools=*|--core-tools|--core-tools=*|--include-tools|--include-tools=*|--exclude-tools|--exclude-tools=*|--mcp-config|--mcp-config=*) echo 'reserved auth/model/sandbox/tool flag' >&2; return 77 ;;
    esac
  done
  if ! load_qwen_credential_reference "$secret_file" "$real_home" "$expected_uid" "$expected_gid"; then
    printf '%s\n' 'qwen credential reference invalid' >&2
    return 78
  fi
  api_key="$QWEN_CREDENTIAL_API_KEY"
  QWEN_CREDENTIAL_API_KEY=''
  unset QWEN_CREDENTIAL_API_KEY

  [[ -z "${HIVECREW_QWEN_CHAIN_TRACE:-}" ]] || printf '%s\n' landlock-launcher >> "$HIVECREW_QWEN_CHAIN_TRACE"
  sandbox_root=$(mktemp -d "/tmp/hivecrew-qwen-landlock.XXXXXX")
  QWEN_SANDBOX_ROOT="$sandbox_root"
  trap cleanup_qwen_sandbox EXIT HUP INT TERM
  sandbox_home="$sandbox_root/home"
  mkdir -p "$sandbox_home/.qwen" "$sandbox_root/tmp" "$sandbox_root/xdg-config" "$sandbox_root/xdg-cache" "$sandbox_root/xdg-data"
  chmod 700 "$sandbox_root" "$sandbox_home" "$sandbox_home/.qwen" "$sandbox_root/tmp"
  export HOME="$sandbox_home" TMPDIR="$sandbox_root/tmp" XDG_CONFIG_HOME="$sandbox_root/xdg-config" XDG_CACHE_HOME="$sandbox_root/xdg-cache" XDG_DATA_HOME="$sandbox_root/xdg-data"
  unset QWEN_SANDBOX
  set +e
  (
    export OPENAI_API_KEY="$api_key"
    export OPENAI_BASE_URL="$QWEN_OPENAI_BASE_URL"
    export OPENAI_MODEL="$QWEN_OPENAI_MODEL"
    unset BAILIAN_CODING_PLAN_API_KEY QWEN_MODEL
    exec "$landlock_exec" --write "$(pwd -P)" --write "$sandbox_root" -- "$qwen_bin" --auth-type openai --model "$QWEN_OPENAI_MODEL" "${governed_args[@]}" "$@"
  )
  status=$?
  set -e
  api_key=''
  unset api_key
  cleanup_qwen_sandbox
  trap - EXIT HUP INT TERM
  return "$status"
}

main() {
  local override=''
  local launcher_path=''
  for override in HIVECREW_QWEN_REAL_HOME HIVECREW_QWEN_SECRET_FILE HIVECREW_LANDLOCK_EXEC HIVECREW_QWEN_BIN; do
    if [[ -n "${!override+x}" ]]; then
      printf '%s\n' 'qwen path override rejected' >&2
      return 77
    fi
  done
  [[ "$(/usr/bin/id -u)" == "$QWEN_EXPECTED_UID" && "$(/usr/bin/id -g)" == "$QWEN_EXPECTED_GID" ]] || {
    printf '%s\n' 'qwen launcher principal invalid' >&2
    return 78
  }
  launcher_path="$(/usr/bin/readlink -f -- "${BASH_SOURCE[0]}")" || return 78
  run_qwen_landlock_launcher \
    "$launcher_path" \
    "$QWEN_CANONICAL_REAL_HOME" \
    "$QWEN_CANONICAL_SECRET_FILE" \
    "$QWEN_CANONICAL_LANDLOCK_EXEC" \
    "$QWEN_CANONICAL_QWEN_BIN" \
    "$QWEN_EXPECTED_UID" \
    "$QWEN_EXPECTED_GID" \
    "$QWEN_EXPECTED_LANDLOCK_MODE" \
    "$QWEN_EXPECTED_QWEN_MODE" \
    "$QWEN_EXPECTED_LANDLOCK_SHA256" \
    "$QWEN_EXPECTED_QWEN_SHA256" \
    "$@"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
