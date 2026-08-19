# WO-C1-04 · Luna Authority Wiring Wave 2

- state: implemented + verified locally; not staged/applied/accepted
- actor: luna-authority-wiring; human_principal=William; carrier=Codex/Luna
- host: `williamdev@spark-b398` via `dgx-hive-dev`
- source: `/srv/hivecosm/12-development-workspaces/users/williamdev/worktrees/william-macstudio-ultra/codex/luna-authority-wiring-wave2`
- base revision: `9ca3429fcd23a5d213916ba2b7d75d00a09e03c2`
- candidate revision: `d7b7d3e4ced492e689954f881ffb0e98ed2c1c7a`

## Changed scope

`deploy/dgx-authority/` adds a compose override using Docker `host-gateway` to reach the DGX host BFF on port 3150, a runtime token-file entrypoint, an anonymous health/401 preflight, and an operator preview script.

The override requires an operator-owned absolute `HIVECOSM_AUTHORITY_TOKEN_FILE` and `HIVECOSM_TENANT_ID`; token contents are never printed, committed, or rendered into the candidate package. Missing URL/token/tenant fails closed.

## Verification

- `sh -n deploy/dgx-authority/*.sh` PASS
- static scan for hardcoded secret/IP/Mac path PASS
- `docker compose ... config` PASS with temporary empty token-file path; rendered host-gateway and secret mount confirmed
- preflight missing URL exits 2 PASS
- live DGX BFF preflight: health 200 and five anonymous routes 401 PASS
- `git diff --check` PASS

## Boundaries and rollback

No staging apply, container start, service restart, database/registry mutation, production change, RUN-06, GPU/model/SGLang/8000/8001 action occurred. Rollback is to retain the prior staging revision; candidate is isolated to the task branch and can be omitted from operator apply.

## Next action

An authorized operator should review the manifest and run the printed compose commands with a governed credential reference, then verify staging externally; this task does not authorize that apply.
