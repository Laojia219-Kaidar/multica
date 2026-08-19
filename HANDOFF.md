# WO-C1-04 · Luna Authority Wiring Wave 2

- state: implemented + verified locally; not staged/applied/accepted
- actor: luna-authority-wiring; human_principal=William; carrier=Codex/Luna
- host: `williamdev@spark-b398` via `dgx-hive-dev`
- source: `/srv/hivecosm/12-development-workspaces/users/williamdev/worktrees/william-macstudio-ultra/codex/luna-authority-wiring-wave2`
- base revision: `9ca3429fcd23a5d213916ba2b7d75d00a09e03c2`
- source candidate parent: `943e616bcb97b9feff15d97efa357992c4192717`
- source candidate tree: `6c662e93064802219e1e14d09d29b1feb8c87b1e`
- package revision: the final Git HEAD after this metadata commit; it is intentionally not embedded in its own manifest (see digest contract)

## Changed scope

`deploy/dgx-authority/` adds a compose override using Docker `host-gateway` to reach the DGX host BFF on port 3150, a runtime token-file-only entrypoint, an anonymous health/401 preflight, an operator preview script, and isolated compatibility tests.

The override requires `HIVECOSM_AUTHORITY_BASE_URL`, `HIVECOSM_TENANT_ID`, and an operator-owned absolute `HIVECOSM_AUTHORITY_TOKEN_FILE` together. The mounted container reference is fixed at `/run/secrets/hivecosm_authority_bearer_token`; token contents are never printed, committed, or rendered into the candidate package. Missing URL/tenant/token source fails closed.

The current Go client only accepts the bearer token through its process environment. The entrypoint runs preflight first, then passes the file value across one `exec env` boundary. This avoids logs/artifacts but does not eliminate same-UID `/proc` environment visibility; file-aware client loading is the follow-up hardening action.

## Verification

- `sh -n deploy/dgx-authority/*.sh` PASS
- static scan for hardcoded secret/IP/Mac path PASS
- `docker compose ... config` PASS with temporary empty token-file path; rendered host-gateway and secret mount confirmed
- `deploy/dgx-authority/authority-wiring-test.sh` PASS: shell/static/compose negative/render/entrypoint compatibility with runtime-only no-secret fixtures
- preflight missing URL exits 2 PASS
- live DGX BFF preflight: health 200 and five anonymous routes 401 PASS
- `git diff --check` PASS

## Boundaries and rollback

No staging apply, container start, service restart, database/registry mutation, production change, RUN-06, GPU/model/SGLang/8000/8001 action occurred. Rollback is to retain the prior staging revision; candidate is isolated to the task branch and can be omitted from operator apply.

## Next action

An authorized operator should review the manifest and run the printed compose commands with a governed credential reference, then verify staging externally; this task does not authorize that apply.

## Package digest contract

`SHA256SUMS` covers the five wiring scripts, `HANDOFF.md`, and `run-manifest.json`; verify with `sha256sum -c SHA256SUMS`. The Git commit that envelopes these metadata files is reported as the final HEAD after commit, rather than embedded in its own manifest, avoiding a recursive self-hash claim.
