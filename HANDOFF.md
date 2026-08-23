# WO-C1-04 · Luna Authority Wiring Integration V1

- state: integrated + verified locally; not built, staged, applied, or accepted
- actor: luna-authority-wiring; human_principal=William; carrier=Codex/Luna
- host: `williamdev@spark-b398` via `dgx-hive-dev`
- source worktree: `/srv/hivecosm/12-development-workspaces/users/williamdev/worktrees/william-macstudio-ultra/codex/wo-c1-04-authority-integration-v1`
- branch: `owner/william/william-macstudio-ultra/codex/wo-c1-04-authority-integration-v1`
- integration base revision: `9ca3429fcd23a5d213916ba2b7d75d00a09e03c2`
- reviewed source candidate: `34a82cd35adb6f713053e7cc77f2d8c5a2361b86`
- reviewed source candidate parent: `943e616bcb97b9feff15d97efa357992c4192717`
- final package revision: external Git HEAD after this metadata commit; not embedded in its own manifest

## Changed scope

The exact reviewed Authority wiring ancestry was cherry-picked in order from `d7b7d3e4ced492e689954f881ffb0e98ed2c1c7`, `a272db856b3a355eaf1adc623b98b8b596cfbeab`, `943e616bcb97b9feff15d97efa357992c4192717`, and `34a82cd35adb6f713053e7cc77f2d8c5a2361b86`. The source change is limited to `deploy/dgx-authority/` and custody metadata. A separate lane evidence package records the DGX-portable Gate-v6 overlay; it is not copied into the HiveCrew source tree or used as a production overlay.

## Verification

- `sh -n deploy/dgx-authority/*.sh`: PASS
- `deploy/dgx-authority/authority-wiring-test.sh`: PASS
- `git diff --check`: PASS
- DGX Gate-v6 overlay validator, compose render, and seven Gate-v6 fixture/guard suites: PASS
- Go targeted tests/build precheck: not run because `go` is unavailable on `spark-b398`; no success claim is made

## Boundaries and rollback

No staging apply, container start, service restart, database/registry mutation, production change, RUN-06, GPU/model/SGLang/8000/8001 action occurred. Rollback is to the base `9ca3429...` branch or omission of this isolated branch from any apply. No secret values were read or recorded. The Authority client's same-UID `/proc` visibility limitation from one-time `exec env` transport remains documented hardening debt.

## Next action

An independent reviewer should review the final clean integration HEAD and lane evidence. After acceptance, build exact immutable Linux/arm64 images from that HEAD; staging remains separately authorized.

## Package digest contract

`SHA256SUMS` covers the five wiring scripts, this HANDOFF, and `run-manifest.json`; the final metadata commit is the external Git HEAD to avoid recursive self-reference.
