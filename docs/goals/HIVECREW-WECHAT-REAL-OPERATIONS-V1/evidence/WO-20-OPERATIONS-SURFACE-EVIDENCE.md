# WO-20 — WeChat Production Operations Surface Evidence

- Work order: WO-20 (production request and runtime control surface)
- Revision: `c386c016a` (code), this document recorded 2026-08-15T13:27:34Z (UTC)
- Author: accountable executor (carrier: Kimi Code; no employee identity bound)
- Verdict: **candidate_verified (component level)** — live browser journey
  against the candidate runtime is WO-50 canary scope (EV-UI stays pending
  until then).

## Scope and files

| File | Change |
|---|---|
| `packages/views/workflow/wechat-production-panel.tsx` | New. Pure-view panel: launch form + reconciled monitor + approval controls. |
| `packages/views/workflow/wechat-production-panel.test.tsx` | New. 11 tests. |
| `packages/views/workflow/workflow-operations-page.tsx` | Narrow: optional `wechatProduction` prop rendered on the L4 生产计划 section only. |
| `packages/views/workflow/workflow-operations-page.test.tsx` | Narrow: 2 new tests (plan-section wiring, VC-01 design/ops separation); `@xyflow/react` mock completed with `Position`/`Handle` to match the current designer; `cleanup` import. |
| `packages/views/workflow/index.ts` | Export the panel and its view-model types. |

Write-scope discipline: only `packages/views/workflow` touched. The protected
dirty files (`workflow-designer.tsx`, `workflow-designer.test.tsx`) were NOT
modified. `workflow-workbench.tsx` / `workflow-runtime-graph.tsx` needed no
change: WeChat productions are ordinary kernel instances there, and the
four-node read model has its own panel.

## Behavior pinned by tests

1. **Fail-closed launch.** The form composes the frozen
   `WechatContentProductionRequest` and validates it with the same contract
   validator the server re-runs; invalid input shows the issue list and never
   calls the integrator. The submitted request is test-asserted to contain no
   caller-supplied execution/artifact proof keys.
2. **Idempotency.** One stable UUID idempotency key per draft, kept across
   retries (retry = replay, never a duplicate); the receipt view marks
   idempotent replays; "再发起新生产" rotates the key explicitly.
3. **Fail-closed preconditions.** Unresolved authority context or zero
   published definition pins replace the form with an honest blocked state —
   no submit control exists.
4. **Real status only.** The monitor renders the server-reconciled read model:
   four frozen nodes with pending/dispatched/completed/failed state, live
   state, and read-only command/task/candidate lineage; fail-closed halts show
   the server-side reason (e.g. 服务端执行回执缺失) and never a candidate.
   Loading and error states are distinct from an empty list.
5. **Approval gate.** 审批通过 / 退回修改 controls exist only while
   `status=paused` and `approval_state=awaiting`; each click carries a fresh
   UUID review id; terminal/running productions expose no review controls.
6. **Terminal honesty.** `awaiting_publication` renders "待发布（无平台回执，
   绝不显示为已发布）"; a test asserts no standalone "已发布" text exists.
   Outcome Center jump renders when the integrator supplies `outcomeHref`.
7. **No phantom controls.** A test asserts the absence of 暂停/恢复/重试任务/
   重新执行/发布到公众号/立即发布 buttons — backend-unimplemented controls are
   never shown as available.
8. **VC-01 separation.** The workflow design section renders no production
   launch control; the plan section without the integrator seam stays an
   honest placeholder.

## Commands and results (this revision)

```
pnpm --filter @multica/views exec vitest run workflow/   # 6 files, 43/43 passed
npx tsc --noEmit -p packages/views                       # zero errors in WO-20 files
```

### Known pre-existing issues (NOT introduced by WO-20)

- `tsc --noEmit` reports 6 errors confined to the pre-existing uncommitted
  dirty files `workflow-designer.tsx` / `workflow-designer.test.tsx`
  (SetStateAction inference vs `WorkflowDefinitionDraft`). Those files are a
  protected dirty boundary owned by earlier work; WO-20 did not touch them.
- Full views suite has 2 unrelated failing test files
  (`layout/tab-presentation.test.tsx`,
  `agents/components/agent-activity-hover-content.test.tsx`) in areas WO-20
  does not touch; baseline attribution to be recorded at JOIN-1.

## Explicit slice limits

- The panel is a pure view: fetching, polling/reconcile calls, API client
  methods, and page wiring are WO-50 integrator scope.
- The launch form pins a published definition version selected from
  integrator-supplied pins; it does not itself verify the digest against the
  server (the server fails closed on pin mismatch).
