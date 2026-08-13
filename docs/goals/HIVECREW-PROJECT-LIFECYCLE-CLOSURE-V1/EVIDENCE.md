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
