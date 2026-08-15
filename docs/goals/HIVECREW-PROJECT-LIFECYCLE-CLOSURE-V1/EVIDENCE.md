# Evidence index — HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1

Append-only. Each entry: id, what, how verified, revision/source, timestamp, status.

## WAVE-0 current-truth evidence

- EV-W0-01 : goal file hash match : sha256(prime-goal.md) == 0452c877794512bc9c2f0dc7acde72fb9a32642c9f481921d0b62753bcb05aaf : verified 2026-08-13T12:54:30.905411+00:00
- EV-W0-02 : runtime/ports : docker compose project `multica`; 3000=multica-frontend-1, 8080=multica-backend-1, postgres=multica-postgres-1 : lsof + docker ps
- EV-W0-03 : source truth : main @ f7667c8d7c540217c345d98beac33794e1f3e6d0, clean tree : git rev-parse/status
- EV-W0-04 : portfolio : 11 projects all in_progress, 2 without lead, 553 issues, 1 live task : psql via docker exec (read-only)
- EV-W0-05 : HIV-548/HIV-553 : parent/contract issues located, HIV-553 task completed, contract attachment recovered : psql + workdir file
- EV-W0-06 : contract freeze : hiv-553-project-lifecycle-contract.md recovered (12855 chars) and mirrored to reports/ : file copy

## Contract mirror

- reports/hiv-553-project-lifecycle-contract.md — verbatim copy of the HIV-553 Task-linked contract attachment.


## Slice 1 evidence (project health + page visibility)

- EV-S1-01 : backend commit : 0eeb7a05b feat(projects): project-lifecycle health read model : git log
- EV-S1-02 : frontend commit : 74bb1fe31 feat(projects): honest health buckets + card fields : git log
- EV-S1-03 : Go classifier tests : 11 contract cases green (incl FailedRepairGapBlocks) (A/B/C/D/E/G + lead/duplicate/source-gap) : `go test ./internal/service -run TestClassifyProject`
- EV-S1-04 : Go handler tests : portfolio 200 envelope + 404, isolated DB : `go test ./internal/handler -run 'TestListProjectLifecycle|TestGetProjectLifecycle'`
- EV-S1-05 : portfolio diagnostic : 11 real projects -> 8 review_or_repair_blocked, 1 duplicate_or_superseded, 2 source_gap, 2 owner_decision_required, 0 active (honest live truth; running task had completed) : PROJECT_LIFECYCLE_SMOKE=1 diagnostic
- EV-S1-06 : TS bucket tests : 6 cases green : vitest project-health.test.ts
- EV-S1-07 : TS projects-page suite : 25/25 green, no regression : vitest projects/components
- EV-S1-08 : typecheck + go build : packages/views tsc --noEmit clean; go build ./... clean
- EV-S1-09 : candidate API acceptance : server binary on :18090 -> /health 200, /api/projects/lifecycle 401 (unauth) + 200 (auth, 11 projects), /api/projects/{id}/lifecycle 200 + 404 : curl evidence
- EV-S1-10 : isolated test DB : hivecrew-lifecycle-testdb @127.0.0.1:55433 (pg_dump copy of live, read-only on live)


## Slice 1 Repair #1 (Quinn REVISE)

- EV-S1-11 : Quinn independent review REVISE (5 invariants PASS; findings F1-F3 evidence_gap + O1-O4) : evidence/EV-S1-QUINN-REVISE.md (HIV-555)
- EV-S1-12 : Repair #1 commit b954e7e5c : F1 outcome-total conflation fix, F2 C-trigger operationalization documented, F3 cross-workspace isolation test, O1 source_gap->blocked bucket : git log
- EV-S1-13 : Repair re-verification : go test (classifier + handler incl cross-workspace) green; pnpm typecheck + vitest 32/32 green


## Slice 1 Repair #2 (Gauss REVISE)

- EV-S1-14 : Gauss independent review REVISE (4/5 PASS, 1 env-limited; F1 phase_critical + F2/F3/F6 evidence_gap + F4/F5 optional) : evidence/EV-S1-GAUSS-REVISE.md (HIV-554)
- EV-S1-15 : Repair #2 commit 90d7b50af : F2 outcome-ledger query, F3 repair-gap->C, F6 single-200 + read-only + idempotent tests : git log
- EV-S1-16 : Repair #2 re-verification : go build ./... green; classifier 11 tests green; handler lifecycle tests green; vitest 32/32; typecheck clean; portfolio diagnostic 11 projects unchanged (8 C / 1 E / 2 G)


## Slice 1 Repair #3 (Gauss REVISE re-review, F2 residual)

- EV-S1-17 : Gauss re-review of 90d7b50af : F1/F3/F6 PASS, F2 residual (outcome_confirmed still hardcoded 0 in wire struct) : HIV-554
- EV-S1-18 : Repair #3 : OutcomeConfirmed <- confirmedByProject[pid]; stale comment fixed; EVIDENCE/CHECKLIST repaired : git log (this commit)


## WAVE-4 candidate API acceptance + WAVE-5 reconciler

- EV-W4-01 : candidate server (:18090, isolated DB 55433) full-stack API verified: portfolio 11 projects honest (8 C/1 E/2 G), reconciler 15 findings (8 review_no_reviewer/5 repair_no_repair/2 terminal_no_package), closure package review_required+digest+blockers, pause preview read-only, auth 401 : curl evidence
- EV-W5-01 : reconciler diagnostic commit 23615f2e4 (4 detectors, read-only) + live 15 findings
- EV-S4-* : Slice 4 3x PASS (Gauss/Quinn/Pixel) — closure package + close gate accepted
