# JOIN-1 — PHASE-2 统一验证证据

- Join: JOIN-1（WO-10R / WO-15 / WO-20 / WO-30 / WO-40A 汇合后的统一验证门）
- Controller: Kimi Code（单一 active controller）
- Revision: `3bb308efe`（WO-40A 账本提交）
- Observed at: 2026-08-15T14:20:00Z（UTC）
- Verdict: **PASS（全部失败项均归因于既有基线，本 goal 提交链零回归）**

## 1. 执行矩阵

| 套件 | 命令 | 结果 |
|---|---|---|
| core 测试 | `pnpm --filter @multica/core test` | 1311/1314 通过；3 个基线失败（§2.1） |
| core 类型 | `pnpm --filter @multica/core exec tsc --noEmit` | 干净 |
| views 测试 | `pnpm --filter @multica/views test` | 3452/3455 通过；2 文件 3 个基线失败（§2.2） |
| views 类型 | `pnpm --filter @multica/views exec tsc --noEmit` | 6 个基线错误，全部局限在两个保护脏文件（§2.3） |
| go workflow | `go test ./internal/workflow/... -count=1` | **ok**（含 WO-30 的 28 测试） |
| go companyops | `go test ./internal/companyops/... -count=1` | **ok**（含 WO-40A 新 lifecycle 测试） |
| go service | `DATABASE_URL=<候选库> go test ./internal/service/ -count=1` | 1 个基线失败（§2.4）；WO-40A 两个新集成测试通过 |
| go handler | `DATABASE_URL=<候选库> go test ./internal/handler/... -count=1` | 1 个基线失败（§2.5） |
| go vet | `go vet ./internal/workflow/... ./internal/companyops/...` | 干净 |
| 空白检查 | `git diff --check` | 干净 |

候选 DB：`hivecrew-b2-postgres`（127.0.0.1:55432 共享隔离实例），库 `multica_hivecrew_operations_workflow_v2_512`，迁移 1→370 全部应用。

## 2. 基线失败归因（逐条）

### 2.1 core：3 个路由覆盖测试失败（与 goal 链无关）
- `paths/consistency.test.ts > exposes the expected parameterless workspace route methods`
- `paths/route-icons.test.ts > every parameterless workspace route segment maps to a page`
- `diagnostics/diagnostic-context.test.ts > recognizes every builder — no builder falls back to the mask`
- 归因：本 goal 提交链（`eb65d94c6~1..3bb308efe`）在 packages/core 只新增 `api/workflow.ts`、`workflow/content-node-contract.{ts,test.ts}`、`workflow/index.ts` 一行导出；paths/diagnostics 区域零触碰（`git diff --name-only` 实证）。失败源于他人新增的 workspace 路由未同步 core 路由表（疑似 Aria `866e83d6a` operating-program 工作）。

### 2.2 views：2 文件 3 测试失败（WO-20 时已记录的基线，未变化）
- `layout/tab-presentation.test.tsx`（2 个：inbox container 两项）
- `agents/components/agent-activity-hover-content.test.tsx`（1 个：中文计数文案）

### 2.3 views tsc：6 错误全部在保护脏文件（WO-20 时已记录，未变化）
- `workflow/workflow-designer.tsx`（2）、`workflow/workflow-designer.test.tsx`（4）——他人未提交工作，本 goal 永不 stage/修改。本 goal 的 views 文件零错误。

### 2.4 service：`TestCompanyOpsOutcomeListCountAndCursorRejectCrossWorkspaceJoins`
- `insert foreign agent: agent.runtime_id not-null (SQLSTATE 23502)`——陈旧测试插入与现行 schema 不兼容。WO-40A 期间已在 HEAD（stash 本 goal 改动）复跑同样失败，证据见 WO-40A-ARTIFACT-OUTCOME-EVIDENCE.md §4。

### 2.5 handler：`TestCanonicalWorkflowOperatingProgramUUIDRejectsNonCanonical`
- 大写十六进制 UUID 用例被 `canonicalWorkflowOperatingProgramUUID` 拒绝。该测试来自 Aria `866e83d6a`（2026-08-14 21:42 +0800，`git merge-base --is-ancestor` 确认先于本 goal 链起点 `eb65d94c6`），非本 goal 改动。

## 3. 程序性说明

- goal txt 允许派只读 explore subagent 做独立核对；当前 harness 约束禁止派生子代理，故 JOIN-1 由 controller 直接逐项执行并保留完整命令与归因证据。每条基线失败均有"文件/提交级"不在本 goal 链内的实证。
- 保护脏文件（`apps/web/next-env.d.ts`、`workflow-designer.tsx`、`workflow-designer.test.tsx`）全程未 stage、未修改（`git status --porcelain` 逐 commit 复查）。

## 4. 结论

PHASE-2 四个工作单（WO-10R 合同冻结、WO-15 隔离证明、WO-20 运营 UI、WO-30 执行桥、WO-40A 审批提升桥验证）在统一验证下零回归。frontier 推进至 PHASE-3：WO-50 候选 canary（含 WO-15 deferred 断言 2/3/4/6/8 与 WO-40B 提升+Outcome Center 回读）。
