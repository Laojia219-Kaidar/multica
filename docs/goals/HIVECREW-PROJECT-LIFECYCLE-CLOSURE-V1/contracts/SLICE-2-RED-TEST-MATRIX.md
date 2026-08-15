# SLICE-2 红测矩阵（合同反例优先）

> 冻结时点：2026-08-13T14:52:26.863956+00:00。实现前先落红；每项标注建议文件与断言要点。文件均新写，不与 Slice 1 文件重叠。

## 服务层（server/internal/service/project_lifecycle_control_test.go）

| # | 场景 | 断言（红→绿） |
|---|---|---|
| C1 | continue 且已有等价 live task | 幂等返回同 receipt，不重复建 task（复用 duplicate_pending_task 模式） |
| C2 | continue 且 lead 为空 | 拒绝 ACCOUNTABLE_LEAD_REQUIRED，零写 |
| C3 | pause 且存在不可取消存活子进程 | 失败并返回精确 residual task/run ids，不宣称 paused |
| C4 | resume 且 pause receipt 缺失 | 拒绝或要求显式 recovery_of，不复活 terminal task |
| C5 | close-preview 且 outcome 未覆盖 | OUTCOME_COVERAGE_INCOMPLETE，零写 |
| C6 | close-preview 且无 Closure Package | CLOSURE_PACKAGE_MISSING，零写 |
| C7 | close-preview 且 live task>0 | 拒绝，返回 residual task ids |
| C8 | close 门全绿 | 写 terminal 状态 + promotion receipt，before/after version 正确 |
| C9 | preview 后 version/frontier/coverage 漂移 | commit 409，零部分写入，要求重新 preview |
| C10 | 同 idempotency key 同 digest 重放 | 同 receipt，不重复副作用 |
| C11 | 同 key 不同 digest | 409 |
| C12 | duplicate canonical authority | continue/close/merge 拒绝，E/F owner decision |
| C13 | 批量动作任一失败 | 每项目独立 receipt，禁止总成功覆盖失败项 |

## Handler 层（server/internal/handler/project_lifecycle_control_test.go）

| # | 场景 | 断言 |
|---|---|---|
| C14 | 未认证 | 401 |
| C15 | 非 owner/admin | 403 |
| C16 | preview 请求 | 200 + 无任何 Project/Issue/Task 状态变化 |
| C17 | commit 请求 | 200 + receipt 结构 + 幂等 key 回显 |
| C18 | 未知项目 | 404 |
| C19 | 未知字段 | 400（严格 JSON，仿 companyops_test 模式） |

## 前端（packages/views/projects/**，实现时）

| # | 场景 | 断言 |
|---|---|---|
| C20 | 动作按钮二次确认 | 危险动作（close/暂停派发）需 confirm modal |
| C21 | preview 面板 | 显示 target 精确清单 + receipt 预览，不直接执行 |
| C22 | receipt 回执 | 幂等 key + before/after 回显一致 |
| C23 | 空/错/加载态 | 动作失败可回读，页面不崩溃 |
