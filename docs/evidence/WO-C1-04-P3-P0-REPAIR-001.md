# WO-C1-04-P3-P0-REPAIR-001 source-only evidence

State: `CANDIDATE_ONLY_NOT_REVIEWED_NOT_INTEGRATED_NOT_APPLIED`

## Owner-facing result

This candidate closes three source/wiring gaps discovered by the P3 four-role
readiness review without creating any pilot Issue, changing the project
resource, or starting a Runtime:

1. Work Entry review now resolves both implementer and reviewer from immutable
   registration receipts. `reviewer_actor_id` must match the reviewer's own
   receipt, and self-review is rejected by both actor ID and employee ID.
   Ambiguous multiple receipts for one work ref fail closed.
2. The PostgreSQL artifact-event idempotency key persists the receipt-derived
   reviewer authority identity. The existing Owner promotion boundary is not
   changed.
3. A staging-only Review Cell compose overlay binds Quinn's exact Agent UUID,
   WIP 1 and the existing Authority-only behavior. The default remains off and
   only exact `REVIEW_CELL_ENABLED=true` enables it.
4. The P3 pilot preflight requires one exact project repository resource and
   verifies its ref, commit and tree before a future operator may request any
   Issue/worktree/dispatch mutation.

## Exact baseline and scope

- Base revision: `b884ac5b7df37ffd77272ba917341fb64868e702`
- Base tree: `7c6361f46df49156f03ff68d5292ef9aa820e3a3`
- Source branch: `owner/william/william-macstudio-ultra/codex/wo-c1-04-p3-p0-repair-r1`
- Source worktree: `/srv/hivecosm/12-development-workspaces/users/williamdev/worktrees/william-macstudio-ultra/codex/wo-c1-04-p3-p0-repair-r1`
- Logical work identifier: `WO-C1-04-P3-P0-REPAIR-001`
- HiveCrew Issue: not created because this work order explicitly prohibited API/DB writes.

## Tests actually run

Mac Studio Ultra, Go `go1.26.6 darwin/arm64`, exact archive copy:

- `go test ./internal/workentry -run 'TestReview|TestMemoryStore' -count=1` — PASS.
- `go test ./internal/reviewcellconfig -count=1` — PASS.
- `HIVECREW_DB_FREE_FRONTIER=1 go test ./internal/handler -run 'TestWorkEntryReviewRejectsCrossTenantReviewerWorkRefBeforeWrite' -count=1` — PASS.
- `go test -c ./cmd/server` — PASS compile-only. The package TestMain requires a migrated PostgreSQL fixture, so DB-backed execution was not claimed.
- `go test -c ./internal/handler` — PASS compile-only.

DGX, no Go toolchain in the `williamdev` PATH:

- `bash ops/p3-pilot/test-source-only.sh` — `P3_SOURCE_ONLY_TESTS_PASS`.
- Live read-only `bash ops/p3-pilot/pilot-preflight.sh` — expected rc 42,
  `project repository ref is stale or ambiguous`. This proves the current
  stale project resource is rejected without a mutation.
- `git diff --check` — PASS.

## Negative coverage

- Same actor ID self-review: rejected, zero artifact events.
- Same employee through two runtime projections: rejected, zero artifact events.
- Forged reviewer actor ID: rejected, zero artifact events.
- Ambiguous multiple implementer receipts: rejected, zero artifact events.
- Cross-tenant reviewer work ref: HTTP 403 before service write.
- Missing/case-drifted Review Cell flag: disabled.
- Stale/duplicate/wrong-URL project resource: preflight rc 42.
- Wrong revision or tree: preflight rc 42.
- Fake call log contains no Issue create/assign, dispatch or Git worktree mutation.

## Remaining blockers

- P2 still must succeed before P3.
- Max, Raven, Gauss and Quinn still lack independently accepted DGX-native
  Runtime/credential/policy bindings; this source candidate does not repair
  credentials or invoke a provider.
- Current project resource remains stale. A separate authorized transaction
  must bind the exact accepted post-P2 ref/revision/tree before preflight can
  pass.
- The Review Cell overlay still requires separate package review and staging
  apply authorization. It is not applied by this candidate.
- A different reviewer must review this commit before integration.

## Preserved boundaries

No integration ref, live staging, project resource, Issue, Agent, Runtime,
database, daemon, model/provider call, credential, production, RUN-06, GPU,
training, SGLang or ports 8000/8001 were modified.
