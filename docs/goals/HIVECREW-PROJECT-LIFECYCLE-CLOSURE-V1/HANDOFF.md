# Resume Package — HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1

- Goal/version: HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1 / 1.0
- Current controller: Prime Agent
- Current Phase: Slice 1 ACCEPTED (3x independent PASS) + INTEGRATION-HANDOFF-V1 frozen for W3
- Ready: sub-agent archaeology (code/data contract, project page + owner journey, test matrix)
- Running: (none yet)
- Blocked: (none)
- Evidence index: see EVIDENCE.md
- Dirty boundaries: mainline clean at f7667c8d; integration worktree = /Users/jiawei/hivecosm-worktrees/hivecrew-project-lifecycle-closure
- Latest review/journal: HIV-553 contract report recovered and frozen
- Next action: handoff to W3 (integration/deploy lane); after dependency gate, continue WAVE-2 Slice 2 (control ops) per CHECKLIST

## Truth anchors (re-calibrated 2026-08-13T12:54:30.905411+00:00)

- UI http://127.0.0.1:3000 (multica-frontend-1) / API http://127.0.0.1:8080 (multica-backend-1), docker compose project `multica`.
- Source: /Volumes/HiveData/hivecosm/HQ-50-代码仓库/01-源码下载/multica @ main f7667c8d7 (clean).
- DB: multica-postgres-1, db=multica, workspace=HiveCosm (hivecosm), issue prefix HIV, counter 553.
- 11 projects, all in_progress; 2 without lead; 1 live task (HIV-550 running); 304 in_review issues (backlog).
- receipt/outcome ledger tables exist but are empty (0 rows): execution_receipt, artifact_candidate, artifact_promotion_claim, artifact_materialization_intent, assignment_dispatch_receipt, external_work_order_link.

## Contract (HIV-553, frozen)

The HIV-553 Task-linked contract `hiv-553-project-lifecycle-contract.md` defines A-G health classes, the lifecycle state machine, six closure gates, Closure Package contents, the five project actions (continue/pause/resume/close/generate-closure-package) with preview->commit->receipt, negative tests, and Stage 2 in/out scope. Recovered verbatim from the task workdir and mirrored in this bundle's `reports/` (see evidence).

## Resume rule

CHECKLIST.yaml is the single execution-state source of truth. HiveCrew Project/Issue/Task/Run is the visible execution projection. Never mark done from prose or chat.
