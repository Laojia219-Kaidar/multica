#!/usr/bin/env bash
set -euo pipefail

# This live operator entrypoint is deliberately read-only and accepts neither
# arguments nor ambient executable overrides. A future pilot operator must
# archive its PASS receipt before any Issue/worktree/dispatch mutation.
readonly WORKSPACE_ID="1b2a1f07-3050-4d47-aca5-6e6fdbd393d9"
readonly PROJECT_ID="3b0330e7-a2da-4f41-94ab-61c911af2820"
readonly REPOSITORY="/srv/hivecosm/12-development-workspaces/users/williamdev/repos/hivecrew.git"
readonly RESOURCE_URL="dgx-hive-dev:/srv/hivecosm/12-development-workspaces/users/williamdev/repos/hivecrew.git"
readonly SOURCE_REF="refs/heads/owner/william/william-macstudio-ultra/codex/wo-c1-04-hivecrew-integration-v1"
readonly REVISION="b884ac5b7df37ffd77272ba917341fb64868e702"
readonly TREE="7c6361f46df49156f03ff68d5292ef9aa820e3a3"
readonly PROFILE="wo-c1-04-ultra-dgx-qwen-canary"

readonly CLI_PATH="/home/williamdev/.local/bin/hivecrew-wo-c1-04-b884ac5b7df3"
readonly CLI_CANONICAL="/home/williamdev/.local/bin/hivecrew-wo-c1-04-b884ac5b7df3"
readonly CLI_UID="1006"
readonly CLI_GID="1006"
readonly CLI_MODE="755"
readonly CLI_SHA256="e4eac92e4133effe85984285159d6194c8f6894aecbbdadac4d06c1622596ce9"
readonly CLI_VERSION_LINE_1="multica 0.4.24-b884ac5b7df3-candidate (commit: b884ac5b7df37ffd77272ba917341fb64868e702, built: 2026-08-20T06:15:00Z)"
readonly CLI_VERSION_LINE_2="go: go1.26.6, os/arch: linux/arm64"

readonly GIT_PATH="/usr/bin/git"
readonly GIT_CANONICAL="/usr/bin/git"
readonly GIT_UID="0"
readonly GIT_GID="0"
readonly GIT_MODE="755"
readonly GIT_SHA256="aa6540695d076182256dd6e96c8b302e4d56381e3000bbfd5c71bbdfe94a4942"
readonly GIT_VERSION="git version 2.43.0"

fail() {
  printf 'P3_PILOT_PREFLIGHT_REJECTED: %s\n' "$1" >&2
  exit 42
}

sha256_of() {
  local digest ignored
  IFS=' ' read -r digest ignored < <(/usr/bin/sha256sum -- "$1") || fail "tool digest read failed"
  [[ "$digest" =~ ^[0-9a-f]{64}$ ]] || fail "tool digest is invalid"
  printf '%s\n' "$digest"
}

assert_tool_path_bound() {
  local label="$1" path="$2" canonical="$3" fd_path="$4"
  local expected_uid="$5" expected_gid="$6" expected_mode="$7" expected_sha="$8"
  local path_meta fd_meta

  [[ -e "$path" && -f "$path" && ! -L "$path" ]] || fail "$label path is not an exact regular file"
  [[ "$(/usr/bin/realpath -e -- "$path")" == "$canonical" ]] || fail "$label canonical path mismatch"
  path_meta="$(/usr/bin/stat -Lc '%u:%g:%a:%d:%i' -- "$path")" || fail "$label path metadata read failed"
  fd_meta="$(/usr/bin/stat -Lc '%u:%g:%a:%d:%i' -- "$fd_path")" || fail "$label fd metadata read failed"
  [[ "$path_meta" == "$fd_meta" ]] || fail "$label path identity changed"
  [[ "$fd_meta" == "$expected_uid:$expected_gid:$expected_mode:"* ]] || fail "$label owner or mode mismatch"
  [[ "$(sha256_of "$fd_path")" == "$expected_sha" ]] || fail "$label digest mismatch"
}

[[ $# -eq 0 ]] || fail "arguments are not accepted"
[[ -d "$REPOSITORY" ]] || fail "exact repository is unavailable"

# Open both exact tools once and execute only through those file descriptors.
# Any path replacement after validation is detected before the next operation
# and the already-open descriptor prevents invoking replacement content.
exec 8<"$CLI_PATH" || fail "exact HiveCrew CLI is unavailable"
exec 9<"$GIT_PATH" || fail "exact Git is unavailable"
readonly CLI_EXEC="/proc/$$/fd/8"
readonly GIT_EXEC="/proc/$$/fd/9"

assert_tool_path_bound "HiveCrew CLI" "$CLI_PATH" "$CLI_CANONICAL" "$CLI_EXEC" "$CLI_UID" "$CLI_GID" "$CLI_MODE" "$CLI_SHA256"
assert_tool_path_bound "Git" "$GIT_PATH" "$GIT_CANONICAL" "$GIT_EXEC" "$GIT_UID" "$GIT_GID" "$GIT_MODE" "$GIT_SHA256"

cli_version="$("$CLI_EXEC" --version)" || fail "HiveCrew CLI version read failed"
[[ "$cli_version" == "$CLI_VERSION_LINE_1"$'\n'"$CLI_VERSION_LINE_2" ]] || fail "HiveCrew CLI version mismatch"
assert_tool_path_bound "HiveCrew CLI" "$CLI_PATH" "$CLI_CANONICAL" "$CLI_EXEC" "$CLI_UID" "$CLI_GID" "$CLI_MODE" "$CLI_SHA256"

git_version="$("$GIT_EXEC" --version)" || fail "Git version read failed"
[[ "$git_version" == "$GIT_VERSION" ]] || fail "Git version mismatch"
assert_tool_path_bound "Git" "$GIT_PATH" "$GIT_CANONICAL" "$GIT_EXEC" "$GIT_UID" "$GIT_GID" "$GIT_MODE" "$GIT_SHA256"

resources="$("$CLI_EXEC" --profile "$PROFILE" --workspace-id "$WORKSPACE_ID" project resource list "$PROJECT_ID" --output json)" || fail "project resource read failed"
assert_tool_path_bound "HiveCrew CLI" "$CLI_PATH" "$CLI_CANONICAL" "$CLI_EXEC" "$CLI_UID" "$CLI_GID" "$CLI_MODE" "$CLI_SHA256"
github_count="$(/usr/bin/jq '[.[] | select(.resource_type == "github_repo")] | length' <<<"$resources")" || fail "project resource response is invalid"
[[ "$github_count" == "1" ]] || fail "project must have exactly one github_repo resource"

actual_url="$(/usr/bin/jq -r '[.[] | select(.resource_type == "github_repo")][0].resource_ref.url // ""' <<<"$resources")"
actual_ref="$(/usr/bin/jq -r '[.[] | select(.resource_type == "github_repo")][0].resource_ref.ref // ""' <<<"$resources")"
[[ "$actual_url" == "$RESOURCE_URL" ]] || fail "project repository URL is not exact"
[[ "$actual_ref" == "$SOURCE_REF" ]] || fail "project repository ref is stale or ambiguous"

# Resolve SOURCE_REF exactly once. The tree lookup is then bound to that exact
# commit object, never to a second read of a potentially moving ref.
resolved_revision="$("$GIT_EXEC" --git-dir="$REPOSITORY" rev-parse --verify "${SOURCE_REF}^{commit}")" || fail "source ref cannot be resolved"
assert_tool_path_bound "Git" "$GIT_PATH" "$GIT_CANONICAL" "$GIT_EXEC" "$GIT_UID" "$GIT_GID" "$GIT_MODE" "$GIT_SHA256"
[[ "$resolved_revision" =~ ^[0-9a-f]{40}$ ]] || fail "resolved revision is invalid"
[[ "$resolved_revision" == "$REVISION" ]] || fail "source ref revision mismatch"

resolved_tree="$("$GIT_EXEC" --git-dir="$REPOSITORY" rev-parse --verify "${resolved_revision}^{tree}")" || fail "source tree cannot be resolved"
assert_tool_path_bound "Git" "$GIT_PATH" "$GIT_CANONICAL" "$GIT_EXEC" "$GIT_UID" "$GIT_GID" "$GIT_MODE" "$GIT_SHA256"
[[ "$resolved_tree" =~ ^[0-9a-f]{40}$ ]] || fail "resolved tree is invalid"
[[ "$resolved_tree" == "$TREE" ]] || fail "source ref tree mismatch"

/usr/bin/jq -n \
  --arg status "PASS_READY_FOR_SEPARATELY_AUTHORIZED_MUTATION" \
  --arg workspace_id "$WORKSPACE_ID" \
  --arg project_id "$PROJECT_ID" \
  --arg repository "$REPOSITORY" \
  --arg resource_url "$RESOURCE_URL" \
  --arg source_ref "$SOURCE_REF" \
  --arg revision "$REVISION" \
  --arg tree "$TREE" \
  --arg cli_sha256 "$CLI_SHA256" \
  --arg git_sha256 "$GIT_SHA256" \
  '{status:$status,workspace_id:$workspace_id,project_id:$project_id,repository:$repository,resource_url:$resource_url,source_ref:$source_ref,revision:$revision,tree:$tree,cli_sha256:$cli_sha256,git_sha256:$git_sha256,source_ref_resolution_count:1,tool_binding_mode:"open_fd_exact",mutations:0}'
