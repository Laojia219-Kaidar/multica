# WO-C1-04 · Closure D1 CEO Review Workbench

Status: `SOURCE_ONLY_ACCEPTANCE_FROZEN / NOT_STAGED / NOT_VISUALLY_ACCEPTED`

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
- The query key is workspace-scoped and the queue response is never persisted in a client store.
- Every row carries the canonical Issue UUID, identifier, review state, exact state reason, reviewer identity when present, and task references when present.
- Owner navigation uses the shared workspace path builder. No hardcoded workspace route is introduced.
- `owner_decision` is discoverable as ready for an authorized Owner decision, but this batch performs no verdict, dispatch, promotion, Task, Event, Review, Outcome, API, or database write.
- A missing Review Cell, missing Authority evidence, request failure, or response drift is visibly blocked and cannot be mistaken for zero work.
- The existing Outcome Center remains the single artifact review/promotion surface; D1 links to it and does not create a second outcome store.
- Focused core and view tests cover success, empty, blocked, malformed, and navigation states.

## Non-goals

- No backend route, schema, migration, database record, or live configuration change.
- No review verdict or formal artifact promotion implementation.
- No staging apply, daemon/model invocation, production, RUN-06, GPU, training, SGLang, or ports 8000/8001.
- HTTP or unit-test success is not browser visual acceptance. A connected in-app browser remains a separate gate.

## Verification

- Focused core contract tests: `140/140 PASS` (`api/client.test.ts`, `issues/queries.test.ts`).
- Focused view tests: `5/5 PASS` (`command-review-frontier.test.tsx`, `command-page.test.ts`).
- Package type checks: `@multica/core PASS`; `@multica/views PASS`.
- Changed-file ESLint: `PASS` for every changed TypeScript/TSX file.
- Full `@multica/core` suite: `1348/1349 PASS`; the single `diagnostics/diagnostic-context.test.ts` command-route coverage failure reproduces byte-for-byte on the untouched `80ccd` archive and is not introduced by this batch.
- Full `@multica/views` suite: `3487/3488 PASS`; the single timing-sensitive `use-composer-submit` failure passed immediately when rerun alone (`14/14`) and does not touch this batch's files.
- Full package-wide views ESLint is not green on the baseline (`297` errors and `17` warnings, predominantly pre-existing literal-string findings outside this diff). Changed-file lint is the batch gate.
- Browser visual acceptance: `NOT_RUN`; unit and DOM evidence do not substitute for an attached browser against a staged build.
