# WO-C1-04 · Closure D1 CEO Review Workbench

Status: `SOURCE_ONLY_R2_ACCEPTANCE_FROZEN / R1_REVIEW_REVISE / NOT_STAGED / NOT_VISUALLY_ACCEPTED`

Work ref: `hivecrew://1b2a1f07-3050-4d47-aca5-6e6fdbd393d9/work/3b0330e7-a2da-4f41-94ab-61c911af2820/1c382cef-9bb7-4a30-8b72-c3d63370708c`

Baseline revision: `80ccdba3b368e7fb07114c219a1a70c5aca21d67`

Baseline tree: `d06e65131be1bfe5f2ae143dc1020b2f0be67c6e`

## Owner workflow

1. The CEO command surface shows the exact open Review Queue total from the canonical read API.
2. The Owner can open an exact review item and its Issue detail without creating or changing any record.
3. The surface exposes where authorized review decisions live: Issue review state and the formal Outcome Center.
4. If the Review Cell or Authority-backed read model is absent, malformed, or unavailable, the surface renders an explicit blocked state. It never converts an error into an empty queue or an enabled action.

## Acceptance

- Network JSON is schema-validated and malformed queue responses raise an error instead of becoming an empty queue.
- `authority_ready` is true only when the server wired a real Authority identity plus eligibility evidence provider; local reviewer/task rows never substitute for it.
- `outcome_center_ready` is true only when the canonical Outcome Center read provider is present. A missing capability disables the Outcome link and prevents an empty-queue claim.
- The query key is workspace-scoped and the queue response is never persisted in a client store.
- Every row carries the canonical Issue UUID, identifier, review state, exact state reason, reviewer identity when present, and task references when present.
- Owner navigation uses the shared workspace path builder. No hardcoded workspace route is introduced.
- `owner_decision` is evaluated before its explanatory reason, so a canonical reason-bearing Owner decision remains discoverable when both provider capabilities are ready.
- A non-Owner review row is active only when both provider capabilities are ready, the local reviewer plus target Task references are complete, and the Task status is one of the canonical active states (`queued`, `dispatched`, `running`, or `waiting_local_directory`). Missing, terminal, or unknown task states fail closed.
- This batch performs no verdict, dispatch, promotion, Task, Event, Review, Outcome, API, or database write.
- A missing Review Cell, missing Authority evidence, request failure, or response drift is visibly blocked and cannot be mistaken for zero work.
- The existing Outcome Center remains the single artifact review/promotion surface; D1 links to it and does not create a second outcome store.
- Focused core and view tests cover success, empty, blocked, malformed, and navigation states.

## Non-goals

- No backend route, schema, migration, database record, or live configuration change.
- No review verdict or formal artifact promotion implementation.
- No staging apply, daemon/model invocation, production, RUN-06, GPU, training, SGLang, or ports 8000/8001.
- HTTP or unit-test success is not browser visual acceptance. A connected in-app browser remains a separate gate.

## Verification

- R2 focused core contract tests: `146/146 PASS` (`api/client.test.ts`, `issues/queries.test.ts`).
- R2 focused view tests: `8/8 PASS` (`command-review-frontier.test.tsx`, `command-page.test.ts`).
- Package type checks: `@multica/core PASS`; `@multica/views PASS`.
- Changed-file ESLint: `PASS` for every changed TypeScript/TSX file.
- Go handler/server test binaries compile from an exact source archive with the R2 overlay: `PASS`. Executing the package suites requires their PostgreSQL fixture; the unconfigured local database stopped at `relation "workspace" does not exist`, so no database-backed Go test success is claimed.
- R1 full `@multica/core` suite: `1348/1349 PASS`; the single `diagnostics/diagnostic-context.test.ts` command-route coverage failure reproduces byte-for-byte on the untouched `80ccd` archive and is not introduced by this batch.
- R1 full `@multica/views` suite: `3487/3488 PASS`; the single timing-sensitive `use-composer-submit` failure passed immediately when rerun alone (`14/14`) and does not touch this batch's files.
- Full package-wide views ESLint is not green on the baseline (`297` errors and `17` warnings, predominantly pre-existing literal-string findings outside this diff). Changed-file lint is the batch gate.
- Browser visual acceptance: `NOT_RUN`; unit and DOM evidence do not substitute for an attached browser against a staged build.

## Independent review history

- R1 candidate `df393be698a93e7793ff3118c52baf865dbf6315` was sealed `REVISE` in `review-closure-d1-independent-codex-df393-r1`.
- R1 finding F1: reason-bearing `owner_decision` rows were incorrectly classified as blocked.
- R1 finding F2: local reviewer/Task fields were incorrectly treated as Authority evidence because the wire lacked provider readiness.
- R2 keeps R1 immutable and closes both findings with provider-backed top-level readiness fields and fail-closed client/UI handling.
