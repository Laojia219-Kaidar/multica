#!/usr/bin/env bash
set -euo pipefail

# This program is deliberately read-only. A future pilot operator must require
# its PASS receipt before creating an Issue/worktree or calling dispatch.
readonly WORKSPACE_ID="1b2a1f07-3050-4d47-aca5-6e6fdbd393d9"
readonly PROJECT_ID="3b0330e7-a2da-4f41-94ab-61c911af2820"
readonly REPOSITORY="/srv/hivecosm/12-development-workspaces/users/williamdev/repos/hivecrew.git"
readonly RESOURCE_URL="dgx-hive-dev:/srv/hivecosm/12-development-workspaces/users/williamdev/repos/hivecrew.git"
readonly SOURCE_REF="refs/heads/owner/william/william-macstudio-ultra/codex/wo-c1-04-hivecrew-integration-v1"
readonly REVISION="b884ac5b7df37ffd77272ba917341fb64868e702"
readonly TREE="7c6361f46df49156f03ff68d5292ef9aa820e3a3"
readonly PROFILE="wo-c1-04-ultra-dgx-qwen-canary"

readonly CLI="${P3_PILOT_CLI:-/home/williamdev/.local/bin/hivecrew-wo-c1-04-b884ac5b7df3}"
readonly GIT="${P3_PILOT_GIT:-/usr/bin/git}"

fail() {
  printf 'P3_PILOT_PREFLIGHT_REJECTED: %s\n' "$1" >&2
  exit 42
}

[[ $# -eq 0 ]] || fail "arguments are not accepted"
[[ -x "$CLI" ]] || fail "exact HiveCrew CLI is unavailable"
[[ -x "$GIT" ]] || fail "git is unavailable"
[[ -d "$REPOSITORY" ]] || fail "exact repository is unavailable"

resources="$($CLI --profile "$PROFILE" --workspace-id "$WORKSPACE_ID" project resource list "$PROJECT_ID" --output json)" || fail "project resource read failed"
github_count="$(jq '[.[] | select(.resource_type == "github_repo")] | length' <<<"$resources")" || fail "project resource response is invalid"
[[ "$github_count" == "1" ]] || fail "project must have exactly one github_repo resource"

actual_url="$(jq -r '[.[] | select(.resource_type == "github_repo")][0].resource_ref.url // ""' <<<"$resources")"
actual_ref="$(jq -r '[.[] | select(.resource_type == "github_repo")][0].resource_ref.ref // ""' <<<"$resources")"
[[ "$actual_url" == "$RESOURCE_URL" ]] || fail "project repository URL is not exact"
[[ "$actual_ref" == "$SOURCE_REF" ]] || fail "project repository ref is stale or ambiguous"

resolved_revision="$($GIT --git-dir="$REPOSITORY" rev-parse "${SOURCE_REF}^{commit}")" || fail "source ref cannot be resolved"
resolved_tree="$($GIT --git-dir="$REPOSITORY" rev-parse "${SOURCE_REF}^{tree}")" || fail "source tree cannot be resolved"
[[ "$resolved_revision" == "$REVISION" ]] || fail "source ref revision mismatch"
[[ "$resolved_tree" == "$TREE" ]] || fail "source ref tree mismatch"

jq -n \
  --arg status "PASS_READY_FOR_SEPARATELY_AUTHORIZED_MUTATION" \
  --arg workspace_id "$WORKSPACE_ID" \
  --arg project_id "$PROJECT_ID" \
  --arg repository "$REPOSITORY" \
  --arg resource_url "$RESOURCE_URL" \
  --arg source_ref "$SOURCE_REF" \
  --arg revision "$REVISION" \
  --arg tree "$TREE" \
  '{status:$status,workspace_id:$workspace_id,project_id:$project_id,repository:$repository,resource_url:$resource_url,source_ref:$source_ref,revision:$revision,tree:$tree,mutations:0}'
