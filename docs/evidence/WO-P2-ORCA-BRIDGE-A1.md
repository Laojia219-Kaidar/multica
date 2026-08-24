# WO-P2-ORCA-BRIDGE A1 — HiveCrew ⇄ Orca bridge mapping and governed writeback (schema-free)

- Work order: `WO-P2-ORCA-BRIDGE` A1
- Revision: `hcops-v3-orca-a1-bridge` (isolated Orca worktree, branch `hcops-v3-orca-a1-bridge`)
- Status: candidate, awaiting independent review
- Boundary corrections applied mid-run (both honored):
  1. A1 is **schema-free** — no migrations, no bridge-owned tables, no direct
     database writes. The 416–422 migration files drafted first were deleted
     before commit and stay deleted (repo remains at migration 415).
  2. The thin adapter reuses the **existing** workentry service/API, exactly
     one existing dispatch entry (`CompanyOpsAssignmentService.Dispatch` via
     `AssignmentDispatchPort`), and the **existing Daemon** task lifecycle
     (`claim` / `start` / `complete` / `fail` / `cancel-ack` via
     `DaemonLifecyclePort`). No second Project/Issue/Task/Assignment/Run/
     Employee/Runtime registry is created, and no handler or daemon wiring
     was added.

## What was built

One new self-contained Go package: `server/internal/orcabridge/`. Nothing
else in the repository is modified.

| File | Role |
| --- | --- |
| `contract.go` | Mapping contract: `Chain` validation, provenance markers, canonical digests, placement validation, Orca handle-grammar + HiveCrew task-id guards |
| `cli.go` | Explicit-executable Orca CLI adapter (`run-create/run-list/task-create/task-list/worker-start/dispatch-show/inbox`); `ok:false` → `*CLIError`; timeouts/unusable output → `ErrOrcaUnavailable` (effects unknown ⇒ fail closed) |
| `assignment_port.go` | Thin port over the single existing dispatch entry `CompanyOpsAssignmentService.Dispatch`: uuid parsing + `CompanyOpsHandoffInputDigest` only; authority validation, issue/task/receipt writes, and command-id idempotency stay with the existing service |
| `daemon_port.go` | Thin port over the existing `daemon.Client` lifecycle verbs: `ClaimTask`, `StartTask`, `CompleteTask`, `FailTask`, `AckTaskCancelled`; endpoint semantics, retries, and auth stay with the existing client |
| `workentry_port.go` | Thin port over the existing workentry kernel: `RegisterLinkage` (idempotent receipts), `AppendEvidence` (idempotent structured events), `LookupEvidence` (replay read-back). No store, no SQL |
| `bridge.go` | The idempotent mapping + execution + governed writeback orchestrator |
| `writeback.go` | `worker_done` normalization/validation + result digest |

## Mapping and execution contract (HiveCrew stays control truth)

| HiveCrew object | Orca object | Provenance | Idempotency |
| --- | --- | --- | --- |
| Project | Run (namespace) | marker `[hivecrew-orca-bridge/v1 ws=… prj=…]` last in objective | memo digest guard → workentry receipt replay → `run-list` marker scan |
| Issue | — (no object) | carried inside task spec marker | — |
| Task (run row created by the existing dispatch entry) | Task | marker first line of spec | memo digest guard → receipt replay → `task-list` marker scan |
| Assignment command (one run attempt) | Dispatch + supervised Worker on isolated worktree | assignment + task + dispatch evidence events on the existing work chain | dispatch entry replay (command id) → evidence keys → `dispatch-show` reconcile |
| Run result (`worker_done`) | — (observation) | `finished` event on the existing work chain, digest-frozen | deterministic `result/<dispatchID>` key: exact replay OK, drift → conflict |

Execution flow (all idempotent, all through existing HiveCrew surfaces):

1. `EnsureAssignment` — dispatch through the single existing
   `CompanyOpsAssignmentService.Dispatch` entry; the receipt's initial task is
   mapped to exactly one Orca Task; placement is frozen as a digest on the
   work chain (drifted replay ⇒ `ErrMappingConflict`).
2. `RunClaimedTask` — a task claimed through the existing daemon claim loop
   resolves to its assignment linkage, maps to one Orca supervised worker on
   an isolated worktree (single Orca entry: `worker-start`), and advances the
   HiveCrew task via the existing Daemon `StartTask`.
3. `AcceptWorkerResult` — a `worker_done` resolves back to the HiveCrew chain
   (linkage required, identity fields must match), appends governed
   `finished` evidence on the existing work chain, and settles the HiveCrew
   task through the existing Daemon `CompleteTask` / `FailTask`.
4. `AcknowledgeCancellation` — mirrors the same linkage guard before the
   existing Daemon `cancel-ack`.

## Fail-closed behaviors proven by tests

- Unknown CLI effects (timeout / unusable output) never fall through to a
  second `worker-start` (`ErrOrcaUnavailable` guard before dispatch).
- Non-ready worker start (`failed`/`outcome_unknown`) commits no dispatch
  evidence and never starts the HiveCrew task.
- Orca-side orphans (run/task/dispatch created before a crash) are recovered
  by marker/evidence scans, never duplicated.
- Unmanaged HiveCrew tasks are refused (`ErrNotBridgeManaged`); cross-workspace
  claimed tasks fail closed (`ErrResultIdentityMismatch`).
- Drifted placement, spec, objective, or result payloads conflict instead of
  silently re-mapping.
- All Orca handles pass grammar checks before interpolation into argv; no
  shell, no PATH lookup, flag-like values rejected.
- Bridge registers as `automation_service`, never impersonates a digital
  employee; actor snapshot time frozen at construction so kernel receipts
  replay identically.

## Verification (run in this worktree, `go1.26.6 darwin/arm64`)

- `cd server && gofmt -l internal/orcabridge/` — clean
- `cd server && go vet ./internal/orcabridge/` — pass
- `cd server && go build ./...` — pass
- `cd server && go test ./internal/orcabridge/` — **ok, 50/50 tests** pass
  (contract 14, CLI 13, bridge 18, workentry port 5, writeback 8 —
  subtest-counted)
- `cd server && go test ./internal/workentry/` — 2 pre-existing
  **environmental** failures: `TestReconcileWorktreesReadOnly` and
  `TestCallMCPTool` depend on a hardcoded
  `/Volumes/HiveData/hivecosm/HQ-50-代码仓库/01-源码下载/multica` repo path that
  does not exist on this host. The `workentry` package is byte-identical to
  the baseline (`git diff HEAD -- server/internal/workentry/` is empty); the
  failures are not caused by this work order and reproduce without these
  changes.
- `cd server && go test ./internal/migrations/` — ok (confirms no new
  migrations; repo stays at 415).

## Limitations / explicitly out of A1 scope

- No handler/daemon wiring: production wiring must construct
  `NewBridge(orcaCLI, workEntryPort, dispatchPort, daemonPort, actor)` with an
  explicit Orca executable path and an authenticated daemon client.
- Cold-start task-id-only reconciliation (resolving a claimed task after a
  process restart without prior memo state) lands with the executor wiring in
  A2; A1 resolves claims from the linkage evidence recorded at assignment
  time plus the in-process memo.
- Cross-project fan-in ingest is out of scope; `AcceptWorkerResult` requires
  the HiveCrew project scope.
- Orca CLI envelope shapes are defensive (multi-key probing); tighten once
  the Orca CLI publishes a frozen JSON schema.
- Real-Orca interaction during A1 was read-only (`run-list`, `dispatch-show`,
  `inbox`, `worker-list` against this dispatch's own context); the bridge
  created no Orca state.

## Independent-review corrections (R1)

Four findings fixed in the same worktree on top of the candidate, without
scope expansion (still schema-free, no migrations, no handler/daemon wiring):

1. **Credential redaction before writeback.** New `sanitize.go`:
   `RedactCredentials` / `RedactStringMap` / `RedactStringSlice` redact
   Bearer/authorization headers, provider keys (`sk-`, `sk-proj-`, `sk-ant-`,
   `ark-`), and key/value secrets (`api_key`, `access_key`, `secret`,
   `password`, `passwd`, `token`, `credential`, including JSON/YAML/query
   forms and credential-keyed map values) with a fixed deterministic marker.
   Enforced at three layers: bridge (`AcceptWorkerResult` redacts
   subject/body/report path before digest and evidence), WorkEntry port
   (`AppendEvidence` redacts payload values; nested maps walked), and Daemon
   port (`CompleteTask` output, `FailTask` error). Tests cover Bearer, sk-,
   ark-, token, password plus ordinary-prose negatives, determinism, and an
   end-to-end worker_done whose body carries credentials.
2. **No implicit Issue creation.** `EnsureProjectRun` now requires an
   existing Issue anchor and fails closed with `ErrIssueAnchorRequired` for a
   project/workspace-only call; `RegisterLinkage` never passes
   `ConfirmCreate`, passes only the Issue selector (the kernel derives the
   project), maps `ErrClassificationRequired` to `ErrIssueAnchorRequired`,
   and rejects a `Created` receipt defensively. Proven by a kernel-level spy
   store (`creationCountingStore`) asserting zero `CommitWorkRegistration`
   calls for anchor-less, unresolvable, and successful anchored+replayed
   registrations, plus a bridge-level zero-Orca-effects test.
3. **Concurrent single-writer tests.** Per-scope process mutexes
   (`sync.Map` of keyed locks: run/task/assignment/result scopes) serialize
   mapping and writeback; `EnsureAssignment`, `RunClaimedTask`, and
   `AcceptWorkerResult` each have an 8-goroutine test asserting identical
   results, exactly one Orca create/worker-start, one HiveCrew task start,
   and one evidence key, all clean under `-race`.
4. **Recovery after evidence failure.** `RunClaimedTask` memoizes the
   committed dispatch immediately after worker-start + `StartTask` succeed,
   so a failing evidence append (and retries while the ledger is down, and
   the eventual recovery) never triggers a second worker-start; retries
   re-attempt only the evidence via `retryDispatchEvidence`. The committed
   mapping is returned alongside the error. Test covers fail → retry-while-
   failing → recovery → post-recovery replay with `workerStarts == 1`
   throughout.

R1 verification (go1.26.6 darwin/arm64): `gofmt -l internal/orcabridge/`
clean; `go test ./internal/orcabridge/ -count=1` ok (67 tests); `go test
-race ./internal/orcabridge/ -count=1` ok; `go vet ./internal/orcabridge/`
pass; `go build ./...` pass.

## Independent-review corrections (R3)

Two blockers fixed on top of 83f339cca, no scope expansion:

1. **Cross-instance creation coordination.** The per-Bridge mutexes of R1
   only serialized one process. New `coordination.go` moves the arbiter onto
   the shared WorkEntry ledger: `WorkEntryPort.ClaimScope` (the smallest
   interface operation needed) performs an atomic first-writer-wins claim by
   appending a checkpoint event whose idempotency key is the claim key — the
   kernel's unique-key append is the compare-and-swap register, so exactly
   one Bridge instance can hold a creation claim per scope (run, task,
   worker-start). The claim is lease- and generation-backed: a holder that
   crashes mid-create is taken over after lease expiry by appending
   generation+1 (a fresh key, arbitrated by the same append), and a live
   holder past the bounded wait makes the peer fail closed with
   `ErrScopeHeld` — never a duplicate create. Every acquire goes through the
   ledger (deliberately no in-process fast path), and each create section
   double-checks committed evidence plus Orca-side markers inside the claim,
   so waiters return the committed result without creating. Barrier tests
   run two independent Bridge objects sharing one Orca client and one
   WorkEntry ledger and prove exactly one Run, one Task, one Dispatch/Worker
   (`TestTwoBridgesOneRun/OneTask/OneWorker`), plus restart
   (`TestSecondBridgeRestartNeverRecreates`), lease-expiry takeover
   (`TestLeaseExpiryTakeoverAfterCrashedHolder`), and live-lease fail-closed
   (`TestLiveLeaseFailsClosed`). **Honest durability statement**: the
   protocol is lease/CAS-backed and its atomicity is exactly the atomicity
   of the backing workentry store — cross-process when the kernel runs its
   PostgreSQL store (unique append per work_ref+idempotency_key); the
   in-memory test double enforces the same register within one process and
   proves the protocol, not cross-process durability.
2. **Sanitizer hardening.** Dotted provider-key forms
   (`sk-sp-H.ABCDEFGHIJKLMNOP`, `ark-cn-beijing.ABCDEFGHIJK`, `sk-live-x.Y`,
   and `sk-proj-`/`sk-ant-` variants) are redacted: the provider patterns
   now accept dot-separated tokens with an alphanumeric start/end. Nested
   structures are redacted recursively via `RedactValue` — maps, arrays,
   slices of maps/strings, arbitrarily deep — while scalar passthrough and
   input immutability are preserved. Enforced at all three layers (bridge
   message, WorkEntry evidence payload, Daemon complete/fail output); all
   test values are synthetic (AWS-documented example key, alphabet
   sequences, repeated patterns).

R3 verification (go1.26.6 darwin/arm64): `gofmt -l internal/orcabridge/`
clean; `go test ./internal/orcabridge -count=1` ok (79 tests);
`go test -race ./internal/orcabridge -count=1` ok; `go vet
./internal/orcabridge` pass; `go build ./...` pass; `git diff --check` clean.

## Independent-review corrections (R4)

Two revision findings fixed on top of 5d3e074c9:

1. **Slow side effect vs lease TTL.** The R3 lease did not fence the Orca
   creates: if a RunCreate/TaskCreate/WorkerStart ran slower than LeaseTTL, a
   generation+1 takeover could issue a duplicate side effect while the
   original was still in flight. Chosen remedy (smallest, honest):
   fail-closed takeover. `acquireCreateClaim` now returns the acquired lease
   expiry and a new `withinLease` gate refuses to start the unfenced side
   effect once the clock is past it (`ErrClaimLeaseExpired`) — no takeover
   into a create without committed evidence, because the Orca create calls
   are not fenced or idempotent-keyed. Reconciliation (evidence read plus
   Orca marker scan) adopts whatever the expired holder eventually created.
   New tests block the Orca fake inside the create call on bridge A, expire
   A's lease deterministically via an injected clock, run bridge B
   concurrently, then release A — proving exactly one Run
   (`TestSlowRunCreateBeyondLeaseFailsClosedNoDuplicate`), one Task
   (`...TaskCreate...`), and one Dispatch/Worker (`...WorkerStart...`) across
   both bridges, plus a direct `TestWithinLeaseFailsClosedAfterExpiry` unit
   proof. (Renewal/downstream fencing deliberately not added: it would
   require new Orca-side idempotency contract; recorded as an A2 option.)
2. **Claim event timestamps.** `WorkEntryServiceAdapter.ClaimScope` now uses
   the real attempt/observation time for the event's OccurredAt/ObservedAt
   (injectable `ScopeClaimInput.AttemptAt`, defaulting to `time.Now`); the
   lease expiry lives only in the payload as `expires_at`. The bridge passes
   its (test-injectable) clock so claim stamps stay coherent.
   `TestClaimScopeUsesRealAttemptTime` reads the stored event through the
   kernel replay and asserts the stamps and payload separation.

R4 verification (go1.26.6 darwin/arm64): `gofmt -l` clean; `go test
./internal/orcabridge -count=1` ok (84 tests); `go test -race
./internal/orcabridge -count=1` ok; `go vet ./internal/orcabridge` pass;
`go build ./...` pass; `git diff --check` clean. Files touched stay within
the established allowlist (bridge/coordination/workentry ports + tests +
this evidence file).
