VERDICT: REVISE（维持上轮结论；本轮按提示改走代码+证据审查，全部核验完成，不再依赖 docker/DB）

## 复验方式与结果

1. **代码级审查（只读，逐文件）**：
   - `server/internal/handler/project_lifecycle.go`：两个 handler 均只读（Projector 派生，从不写 project.status）；`GetProjectLifecycle` 对未知项目返回 404（ErrProjectLifecycleNotFound 路径）。
   - `server/cmd/server/router.go:1306-1321`：两个路由挂载于 `/api/projects` 组内，该组处于 `middleware.Auth`（router.go:982-983）与 workspace-scoped 组 `RequireWorkspaceMember`（router.go:1224-1225）之下 → 未鉴权 401、非成员 403、成员 200，与 EV-S1-09 一致。
   - `server/internal/service/project_lifecycle.go`：判定纯函数 + 投影器；SQL 均带 `WHERE i.workspace_id = $1`，跨 workspace 隔离靠构造保证（见下）。
   - `server/pkg/db/queries/project_lifecycle.sql`：仅两条只读 SELECT（ListProjectActiveTasks / ListProjectSuccessProgress），无 INSERT/UPDATE/CREATE，无第二真源表，零迁移。
2. **测试实跑**：沙箱有 go，`go test ./internal/service -run TestClassifyProject -count=1` 复跑 **ok（10/10 绿，0.6s）**，与上轮一致。
3. **证据交叉核对（EV-S1-04/05/09）**：
   - **EV-S1-04（handler 测试绿）**：代码支持——`project_lifecycle_test.go` 恰有 2 用例（portfolio 200 + owner_decision/stalled 断言；nil-UUID 404），编译通过；DB 依赖导致本沙箱无法实跑，与 Quinn 同环境限制。**精度修正**：该文件并无「跨 workspace 隔离」显式测试——隔离是构造性的（SQL workspace_id 过滤 + GetSnapshot 在 workspace 内列表筛选，跨 workspace 项目自然 404），建议有库时补一条显式用例。
   - **EV-S1-05（11 项目诊断 8C/1E/2G/2F/0A）**：自洽且可被代码支持——live task 完成后 0 active；MAC-CANARY、SOURCE-UNDERSTAND 全 Issue 终态且账本空→G×2；PRJ-HCW-V2 种子重复→E；MAC-CANARY、PRJ-HCW-V2 无 lead→F×2；其余 8 项目有 in_review/blocked 无 live task→C×8。注：合同审计曾标 BASES/ORCHESTRATION 为 B，而分类器按合同文本「B 需无更具体审核/返修阻塞」给 C（review_backlog），分类器更贴合合同规则，非缺陷。诊断为 `PROJECT_LIFECYCLE_SMOKE=1` 门控 Logf，属证据非断言，可接受。
   - **EV-S1-09（:18090 curl 401/200/404）**：完全可被代码支持——Auth 中间件→401、成员组→200、未知 id→404；实现者本地起服实测，证据链完整。

## Findings（代码级确认，行号为本轮复核）

- **phase_critical_correction — F1 成果覆盖字段语义违反合同（复验确认）**：`project_lifecycle.go:368-369` `OutcomeConfirmed: 0`、`OutcomeTotal: terminalN`（done+cancelled Issue 数），JSON 标签 `outcome_confirmed/outcome_total`（:188-189）；前端卡片渲染 `成果覆盖: {0}/{terminalN}`（locale zh-Hans `projects.json:164` "成果覆盖" + projects-page.tsx 卡片）。OWNER-WORKBENCH 将显示「0/63」，63 即合同审计明示的 disposition proxy。合同原文「两者不得互换」「不得用 done_count 推断 acceptance」——本字段即该违禁互换，任意含 done Issue 的工作区恒可复现。最小修复：改名 `terminal_issue_count`/`confirmed_outcome_count`（后者账本有数据时真查询、暂无则不下发），卡片文案改「已结束 Issue」。
- **evidence_gap — F2**：`ListPortfolio` 硬编码 `ConfirmedOutcomeCount: 0`（:368），Stage 4 账本填充后读模型永远报告 0，无代码变更不失效。最小修复：账本存在时真查询或显式 fail-fast 门。
- **evidence_gap — F3**：失败任务无输入路径——SQL 非终态集排除 failed、ClassifyProject 无失败计数，失败任务+Issue in_progress 会落 B(stalled) 而非合同 C 定义（「失败后的 repair/re-review 尚未形成 live Task」）；当前线上不可观察，若归 Slice 3 请在 CHECKLIST 显式记录延后。最小修复：加 per-project 失败计数，B 前先判 C。
- **optional_improvement — F4**：E 判定为 `frozenSupersessions` 硬编码种子（生产 UUID 对入码），非数据派生；新重复对/canonical authority 变化不浮现。建议配置注入或数据派生。
- **out_of_scope — F5（转 HIV-556 Pixel）**：`healthBucketOf` 将 `source_gap` 计入「ready」桶，5 桶汇总会把 G 项目显示为接近可关闭；badge 本身诚实。
- **evidence_gap — F6**：handler 测试为直调 handler 而非走 router，路由挂载无 HTTP 级自动化覆盖；无单项目 200 正向用例、无「不改变 project.status」显式断言、无跨 workspace 隔离用例。建议有库时补齐。

## 边界声明

本轮零源码修改、零 DB 写入、零迁移执行；未推送任何分支；服务测试已实跑绿。修复 F1（+顺手 F2/F3 最小项）后可复验转 PASS。