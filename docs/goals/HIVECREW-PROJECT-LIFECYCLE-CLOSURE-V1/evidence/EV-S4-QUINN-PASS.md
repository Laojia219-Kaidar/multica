## VERDICT: PASS

复验对象：`work/hivecrew-project-lifecycle-closure` @ **ca6a50f2b**（`fix(projects): Slice 4 Repair #1`，位于 528020fde 之上；分支顶无其他提交）。只读审查，未改任何源码；临时本地 PG（迁移 1–259）跑测试后清理，工作树干净，HEAD 即 ca6a50f2。

### 上次 REVISE 项复核（F1/F2/P1）

- **F1（live-task 门）已修复** ✓：`ClassifyProject`（server/internal/service/project_lifecycle.go:126）A 分支（ActiveTaskCount>0）现追加 `ACTIVE_TASKS_PRESENT` 到 ClosureBlockers；lead 检查在提前 return 之前，无 lead + live task 时两个 blocker 正确叠加。该 blocker 经 `GenerateClosurePackage → pkg.Blockers` 同时进入 close-preview 与 Close commit 路径——有 live task 的项目 close-preview 不再空 blockers，close 也被独立拦截。
- **F2（replayed 死代码）已修复** ✓：`Close`（project_lifecycle_control.go:475）把 completed 检查移到 `validateProjectControl` 之前，重复关闭 completed 项目 → `Replayed=true`、`Applied=false`、零写入；cancelled 仍走 `PROJECT_TERMINAL` fail-closed。唯一调用方是 handler 与 DB 测试，无其他消费方受影响。
- **P1（review 门）已文档化** ✓：`ReviewRequired` 标注为 hard fail-closed stub（独立复核记录 = Slice 3 review-cell 集成），close 恒返回 `CLOSURE_PACKAGE_REVIEW_REQUIRED`，红测 C8 挂起至 review 机制就绪——与验收点 3「review_required 恒 true」一致。

### 逐项核对（五个验收点）

1. **六门与 §5 一致 — 6/6 成立**：lead（`ACCOUNTABLE_LEAD_REQUIRED`）、权威唯一（`DUPLICATE_AUTHORITY_OWNER_DECISION`，frozen seed）、disposition（`ISSUES_WITHOUT_DISPOSITION`）、无 live task（`ACTIVE_TASKS_PRESENT`，本轮补齐）、outcome（`OUTCOME_COVERAGE_INCOMPLETE`）、package 复核（`CLOSURE_PACKAGE_MISSING` + `CLOSURE_PACKAGE_REVIEW_REQUIRED`）。
2. **Close 幂等 / preview 只读 — ✓**：completed 重关 → replayed 不重写；preview 与 closure-package 端点全读路径（GetProjectInWorkspace + 投影重算）。注：completed 项目 preview 仍显示 `PROJECT_TERMINAL`，commit 返回 `Replayed=true`——preview 语义（为何无需再关）与 commit 语义（幂等重放）分开，可接受。
3. **digest 确定性与 review_required — ✓**：`closurePackageDigest` 为纯函数，指纹 ProjectID/Status/lead/计数/review flag/blockers/duplicate，**不含 PackageID 与 idempotency key** → 同状态同 digest，preview 与 close 两路径同 digest；`ReviewRequired` 在所有包路径恒 true。
4. **派生读模型 — ✓**：本 slice 文件增量仅 4 个前端/类型文件 + router 注册 + handler/service/测试，**无新表、无迁移**；包每次从 live project/issue/task/outcome 重算，无 outcome 写入（不自动接受）、close 门不全绿不写（不自动关闭）。
5. **隐私/权限 — ✓**：两个端点均 workspace 限定 + owner/admin RBAC（`requireWorkspaceRole`），跨工作区 404、非 owner/admin 403、错误统一掩码 "project not found"；closure-package 端点新增 strict JSON 400（body 读失败/解析失败不再静默忽略），错误文案不泄露内部信息。

### 验证证据

- `go build ./...` 通过；`go test ./internal/service/`（含 `TestCloseFailsClosedWithoutOutcomes` 钉死 OUTCOME_COVERAGE_INCOMPLETE + CLOSURE_PACKAGE_MISSING、`TestClosurePackageDigestDeterministic`、classifier/validate 全组、continue/pause/resume 回归）通过；`go test ./internal/handler/`（含 `TestProjectClosurePackageEndpoint`、close preview 只读、非 admin 403）通过。均在隔离本地 PG 上运行。
- 说明：handler 套件中 `TestDashboardFailuresByAgentUsesExactWindow` 在我临时 PG 默认时区（Asia/Shanghai）下失败——该测试用 `CURRENT_DATE` 播种"昨天"而端点按 UTC 算窗口，属环境时区产物，非本 slice 回归；DB 时区置 UTC 后全套件通过。
- `go vet` 仅报 task.go 既有的锁拷贝告警，与本次变更无关。前端 TS 未 typecheck（checkout 无 node_modules）；client.ts `generateClosurePackage` 发送 `JSON.stringify({})`，与 strict 400 兼容。

### 遗留（非阻塞，建议跟进）

- **M1**：F1 修复无钉死测试——`TestClassifyProject_ActiveWithFrontier`/`ActiveButMissingLead` 未断言 `ACTIVE_TASKS_PRESENT`。
- **M2**：F2 修复无钉死测试——completed 重关 → Replayed=true 无回归保护（pause/continue/resume 的 replayed 均有测试）。
- **M3（沿用上次 F4）**：package 仍只返回计数摘要 + digest，缺 §1 要求的 expected outcomes 列表/来源/gaps/diff；receipt 无 package/outcome ids、无 Outcome Center promotion（`outcome_total` 恒 0，注释自认待 Slice 4 mapping；review stub 不可达故无实害）。
- **M4（沿用上次 F5）**：内部 DB 错误也映射 404 "project not found"（不泄露但掩盖故障类别，语义应为 500）。
- **M5（沿用）**：§2/§5 门 6 的 preview_token/expected_version 未实现、receipt 未持久化——与既有 continue/pause/resume 模式一致，非本 slice 回归。

### 结论

两次 REVISE 的功能阻塞项（F1 live-task 门、F2 replayed 死代码）已修复并经代码与测试验证，五个验收点全部成立，fail-closed 边界（RBAC、零泄露、不自动接受/关闭、digest 确定性、review 恒 true）保持完好。**PASS**。建议后续补 M1/M2 两个小测试（各 ~10 行）钉死本轮修复，可在下一 slice 测试矩阵中一并纳入。