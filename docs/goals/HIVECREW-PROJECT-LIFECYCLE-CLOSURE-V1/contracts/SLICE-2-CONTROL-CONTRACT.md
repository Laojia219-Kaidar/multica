# SLICE-2 控制操作合同（frozen · fallback_task 前置交付）

> 状态：本合同为 WAVE-1「合同、状态机与测试先行」的 Slice 2 前置冻结物。
> 实现（写源码）待 W3 lifecycle lane 解除占用后按本合同进行；本合同只定义接口/语义，不改源码。
> 冻结时点：2026-08-13T14:52:12.903736+00:00；来源：HIV-553 合同「Project 页面动作合同」+ 现有 issue_trigger / companyops assignment dispatch receipt 模式。

## 1. 范围

五个项目级控制动作（owner/admin RBAC，preview-first，idempotent，append-only receipt）：

| 动作 | 语义 | preview 必须显示 | commit 唯一效果 | receipt 必须返回 |
|---|---|---|---|---|
| continue | 推进 ready frontier | health、lead、frontier issue、live task、WIP、目标 runtime/agent、预计副作用 | 复用现有 issue，创建/复用一条精确 Task/Run；已有等价 live task 则幂等返回 | project/issue/task/run ids、before/after version、actor、idempotency key、applied/replayed、时间 |
| pause_dispatch | 只停新派发（不终止在跑任务） | 所有 live Task 精确 id、可取消性、子进程/租约风险、不改 Outcome/历史 | 取消或收敛列出的 Task；写显式 pause receipt；不删 issue/历史 | 每 Task 结果、剩余 live count=0、project before/after、失败项；有残留则不宣称 paused |
| resume | 恢复 | pause receipt、建议 frontier issue、依赖/阻塞、目标 assignee/runtime、WIP 预算 | 不复活 terminal Task；在既有 issue 上建新 Task 并链接 recovery_of | 新 Task id、recovery_of、版本、actor、幂等结果 |
| close-preview | 关闭预检 | 六项关闭门、issue disposition matrix、outcome matrix、live task=0、Closure Package hash | 仅在门全绿时写 terminal 状态并 promotion Closure Package；任一 gap fail-closed | close receipt、package/outcome ids、before/after；拒绝时结构化 blockers 且零写 |
| generate_closure_package | 生成成果包 | expected outcomes、issue/task/run/artifact 来源、source gaps、package diff/preview hash | 生成 candidate Closure Package；不自动接受 Outcome、不自动关闭 | package id/hash/version、included/excluded、coverage、review_required |

## 2. 通用不变量

1. `preview` 只读：不创建 Task/Run、不改 Project/Issue 状态。
2. `commit` 必须带 `preview_token` + `expected_version` + `idempotency_key`；版本/frontier/coverage 漂移 → 409 conflict 零部分写入。
3. 同 key 同 digest 重放 → 返回同 receipt 且不重复副作用；同 key 不同 digest → 409。
4. 收据为幂等与审计证据，非第二套状态真源。
5. `stop-current`（终止在跑任务）是独立显式动作，与 `pause_dispatch` 分开（复用 POST /api/issues/{id}/tasks/{taskId}/cancel）。
6. reviewer≠implementer；lead 为空 → continue/resume/close 均拒绝 ACCOUNTABLE_LEAD_REQUIRED。
7. 同 canonical authority 的 duplicate → continue/close/merge 拒绝，进 E/F owner decision。

## 3. API 表面（建议路由，实现时绑定）

- POST /api/projects/{id}/lifecycle/actions/continue  (preview, preview_token?, expected_version?, idempotency_key, target_issue_id?, target_agent_id?)
- POST /api/projects/{id}/lifecycle/actions/pause    (preview, preview_token?, expected_version?, idempotency_key, task_ids[])
- POST /api/projects/{id}/lifecycle/actions/resume   (preview, preview_token?, expected_version?, idempotency_key, issue_id?, assignee?)
- POST /api/projects/{id}/lifecycle/actions/close    (preview, preview_token?, expected_version?, idempotency_key)
- POST /api/projects/{id}/closure-package            (idempotency_key)

（若 chi 路由与既有 /api/projects/{id}/resources 冲突，改用 /api/projects/{id}/lifecycle-actions/{action}，实现时定。）

## 4. 实现落点（不冲突写区）

- server/internal/service/project_lifecycle_control.go（新）
- server/internal/handler/project_lifecycle_control.go（新）
- server/cmd/server/router.go 追加（与 Slice 1 已注册路由同文件，需与 W3 协调单写）
- 复用：TaskService.EnqueueTaskForIssue / CancelTasksForIssue、companyops assignment AppendAssignmentDispatchReceipt 幂等模式、issue_trigger preview 模式。
- 乐观锁：用 project.updated_at 作为 expected_version（零迁移），或用显式 version 列（需迁移 260，仅在 W3 允许时）。

## 5. 关闭门（供 close-preview 复用，冻结自 HIV-553）

1. accountable lead 存在；canonical authority 唯一或 duplicate/supersede 有 Owner receipt。
2. 每个 issue 有 disposition（done / cancelled(reason) / superseded_by / migrated_to / waived(reason)）。
3. 无 nonterminal Task/Run，无已取消 Task 的存活子进程。
4. 每个 expected outcome 为 accepted/waived(reason)/failed(reason)，含决策人/时间/来源 receipt。
5. Closure Package 已生成、哈希固定、独立复核、进 Outcome Center。
6. close commit 用 preview token + expected version + idempotency key。
