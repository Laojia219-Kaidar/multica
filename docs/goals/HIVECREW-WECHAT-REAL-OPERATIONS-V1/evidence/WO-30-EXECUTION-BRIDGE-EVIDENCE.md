# WO-30 — Task/Run Execution Bridge Evidence

- Work order: WO-30 (real Task/Run dispatch and node output readback)
- Revision: `bacdc389b05b9e0a50d6a1c1640dafc4bc6812d0`
- Recorded at: 2026-08-15T13:07:28Z (UTC)
- Author: accountable executor (carrier: Kimi Code; no employee identity bound)
- Verdict: **candidate_verified (unit/state-machine level)** — live Task/Run
  readback against the candidate runtime is WO-50 canary scope, not WO-30.

## Scope and files

| File | Change |
|---|---|
| `server/internal/workflow/wechat_execution.go` | New. Poll-driven `WechatProductionOrchestrator` over two narrow seams (`WechatProductionStore`, `WechatNodeExecutor`). |
| `server/internal/workflow/wechat_execution_test.go` | New. 28 tests over in-memory fakes pinning the state machine and fail-closed rules. |

Write-scope discipline: only `server/internal/workflow` was touched. No
handler, router, client, migration, queue, table, registry, or Outcome Center
was created or modified. The workflow package still does not import
`service`/`companyops` (no dependency cycle); the concrete executor that wires
these seams to the existing CompanyOps services is WO-50 integrator work.

## Design facts pinned by tests

1. **Deterministic derivation.** Instance id = SHA1-UUID of the idempotency
   key; per-node command id = SHA1-UUID of (key, node); per-node WorkOrder
   source ref = base ref + `--` + node key, validated against the frozen
   pattern (over-length derivation fails closed). Four plans in frozen order
   (research-material-package → article-draft → editorial-review-report →
   wechat-publication-package).
2. **Definition pin (fail-closed).** The request must pin the exact published
   definition version readable in the caller's workspace: missing version,
   digest mismatch, cross-Project version, and unscoped version are all
   rejected with `ErrWechatDefinitionPin`.
3. **Idempotent start/replay.** A replayed start collapses onto the recorded
   instance; no duplicate dispatch, no duplicate start/dispatch events. A
   replay under the same idempotency key with a different pinned payload is
   `ErrWechatProductionConflict`. Reconcile rejects a foreign idempotency key,
   a mismatched definition pin, and an unknown instance (anti-steering).
4. **Completion rule (P0-GATE-01/03).** A node completes only when the fresh
   server-side observation shows `ReceiptCompleted` (from the execution
   receipt, never a client claim) AND the node has its own candidate. A
   missing candidate triggers the existing materialization path; a blank
   output / failed materialization / missing receipt / failed or cancelled
   run / authority rejection each halt the production as failed with the
   exact reason (`materialize_failed`, `receipt_missing`, `run_failed`,
   `run_cancelled`, `authority_rejected`). A failed node never carries a
   candidate; the halt is terminal and idempotent.
5. **Transient vs permanent.** A transient dispatch/ensure error propagates
   as an error and leaves the production running (the next poll retries);
   only `ErrWechatNodeAuthorityRejected` fails the node.
6. **Approval gate.** Completing editorial-review-report pauses the
   production (`awaiting_approval`) and never dispatches the publication
   package while paused. `approved` re-arms (next poll dispatches node 4);
   `changes_requested` keeps the gate halted and blocks downstream; a later
   approval under a new review id re-arms. Review decisions require a
   canonical UUID review id, are idempotent per review id (replay = no-op,
   single recorded call), and are unavailable while the production is
   running (`ErrWechatReviewUnavailable`).
7. **Terminal state.** Completing the publication package completes the
   production with `PublicationState == "awaiting_publication"` — the test
   explicitly asserts it is never `"published"`. The package candidate stays
   reviewable for the WO-40B promotion path.
8. **Handoff notes.** `ComposeWechatNodeHandoffNote` embeds the frozen
   per-node directive, the brief, source references, and — for nodes 2-4 —
   the completed upstream Issue/Task/Candidate lineage; notes over 32 KiB
   fail closed; mismatched/incomplete upstream lineage is rejected. Node 2's
   dispatched note is test-asserted to carry node 1's candidate id.

## Commands and results (this revision)

```
cd server && go test ./internal/workflow/ -count=1   # ok (whole package green)
go vet ./internal/workflow/                          # clean
gofmt -l internal/workflow/                          # clean
WO-30-focused run: 28 passing, 0 failing
```

## Explicit slice limits (recorded, not hidden)

- Advancement is **poll-driven**: `ReconcileProduction` requires the caller to
  resubmit the same validated request (the brief is not stored in the event
  ledger). The daemon completion-hook auto-advance and the HTTP/UI wiring are
  WO-50 integrator work.
- The concrete `WechatNodeExecutor` (EnsureWorkOrderIssue / Dispatch /
  GetIssueOutcome / MaterializeCompletedTask / ReviewArtifact) does not exist
  yet; these tests pin orchestration semantics against in-memory seams only.
  Live server-side Task/Run receipt proof (VC-02) is produced at the WO-50
  canary.
- `WechatNodeLineageRecord.WorkOrderRef` is derivable from the plans but is
  not carried in the event ledger; the read model leaves it empty. Accepted
  for this slice (the ref is deterministic from the recorded request).
