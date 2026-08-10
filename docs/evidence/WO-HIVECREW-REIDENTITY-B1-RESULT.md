# WO-HIVECREW-REIDENTITY-B1 · Result

Date: 2026-08-11 (Asia/Shanghai)  
Actor: Coco (KT-002)  
Carrier: Codex  
Project: PRJ-G71-HIVECREW  
Branch: `work/wo-hivecrew-reidentity-b1`

## Outcome

HiveCrew now has an independent active product identity on the isolated Mac/DGX development branch. New visible product surfaces use HiveCrew; inherited external cloud, website, GitHub release, issue tracker, and Discord defaults have been removed or fail closed. Existing `@multica/*`, CLI, data, config, and deep-link compatibility is retained only where the B1 compatibility map explicitly requires it.

## Candidate revisions

- `eff0033` — B1 scope and compatibility boundaries.
- `c8fa703` — canonical HiveCrew product identity.
- `87d1441` — active Desktop/Web identity and local-first configuration.
- `9184a6d` — active UI, onboarding, helper, documentation, external ownership, and multilingual reidentity.
- `d56665b` — browser-found landing preview corrections and visible HiveCrew command-center mockup.

DGX remote branch:

`/srv/hivecosm/12-development-workspaces/users/williamdev/repos/hivecrew.git#work/wo-hivecrew-reidentity-b1`

## Verified evidence

- `pnpm check:hivecrew-identity`: PASS; 25 active product identity files checked.
- Desktop recovery and route-error tests: 2 files / 27 tests PASS.
- Targeted Views behavior tests: 10 files / 84 tests PASS, then 6 files / 43 tests PASS after network-boundary changes.
- Web landing/login/release tests: 11 files / 80 tests PASS; follow-up CTA test 1 file / 6 tests PASS.
- Desktop `typecheck`: PASS.
- Web `typecheck`: PASS.
- Views `typecheck`: PASS.
- Full Views suite: 274/275 files PASS; 3248/3249 tests PASS. The sole failure is the pre-existing Node test environment limitation in `layout/sidebar-resize.test.tsx`, where `localStorage.setItem` is unavailable and is not caused by the B1 diff.
- Browser acceptance on `http://127.0.0.1:18320/homepage`: PASS for HiveCrew title, HiveCrew Command Center, removal of Gemini CLI, removal of inherited `Multica Demo`, inherited `MUL-*` identifiers, old source paths, and empty GitHub CTA.
- Browser acceptance on `http://127.0.0.1:18320/login`: PASS for HiveCrew identity, no visible legacy product name, and available email input.

## Boundaries not changed

- No merge to `main`.
- No production release or production service restart.
- No DGX Engine/BFF/App1421 apply.
- No write to the 1421 production database or registries.
- No removal of compatibility ABI/data needed by the inherited implementation.
- Historical Multica changelog and use-case content is preserved and explicitly labeled as inherited baseline provenance rather than rewritten as HiveCrew history.

## Next executable milestone

B2 should establish the HiveCrew-native company model and registry: department, position, employee identity, runtime, harness, model, endpoint, credential-pool reference, capacity, assignment, memory, skill, and work-result relationships. It should then connect those truth objects to the operational inbox, conversation, work, project, automation, and employee-dossier surfaces.
