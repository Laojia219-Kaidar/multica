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
- EV-S1-03 : Go classifier tests : 10 contract cases green (A/B/C/D/E/G + lead/duplicate/source-gap) : `go test ./internal/service -run TestClassifyProject`
- EV-S1-04 : Go handler tests : portfolio 200 envelope + 404, isolated DB : `go test ./internal/handler -run 'TestListProjectLifecycle|TestGetProjectLifecycle'`
- EV-S1-05 : portfolio diagnostic : 11 real projects -> 8 review_or_repair_blocked, 1 duplicate_or_superseded, 2 source_gap, 2 owner_decision_required, 0 active (honest live truth; running task had completed) : PROJECT_LIFECYCLE_SMOKE=1 diagnostic
- EV-S1-06 : TS bucket tests : 6 cases green : vitest project-health.test.ts
- EV-S1-07 : TS projects-page suite : 25/25 green, no regression : vitest projects/components
- EV-S1-08 : typecheck + go build : packages/views tsc --noEmit clean; go build ./... clean
- EV-S1-09 : candidate API acceptance : server binary on :18090 -> /health 200, /api/projects/lifecycle 401 (unauth) + 200 (auth, 11 projects), /api/projects/{id}/lifecycle 200 + 404 : curl evidence
- EV-S1-10 : isolated test DB : hivecrew-lifecycle-testdb @127.0.0.1:55433 (pg_dump copy of live, read-only on live)
