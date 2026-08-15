## VERDICT: PASS（Repair #1 · ca6a50f2b）

验收对象：`work/hivecrew-project-lifecycle-closure` @ `ca6a50f2b790c344c43e6c643dd671d0c604fbf7`（FETCH_HEAD 与声明 tip 一致）。复验范围：Repair #1 相对 `528020fde` 的 6 文件 diff + 合同四要件再验证。

### 通过项（独立验证）

1. **指定测试全部 PASS**：`go test ./internal/service -run 'TestCloseFailsClosedWithoutOutcomes|TestClosurePackageDigestDeterministic|TestValidateProjectControl|TestClassifyProject' -count=1` → 20/20；handler 6/6（含新增 `TestProjectClosurePackageEndpoint`）；`go build ./...` 通过（vet 告警为 task.go 既有问题，非本提交引入）。本轮启动本机 PG17 复跑 DB 测试，结束后已停止服务。
2. **F1 已修复**：`ClassifyProject` A 分支追加 `ACTIVE_TASKS_PRESENT`，贯通 snapshot → package → Close，close-preview 不再空 blockers。我用临时同包测试端到端独立验证（文件已删除、工作树复原）：live task>0 时 package `active_task_count=1`、`blockers=[ACTIVE_TASKS_PRESENT]`、`closure_ready=false`；Close 拒绝、`Applied=false`、status 不变（零写）。
3. **F2 已修复**：completed 项目重关 → `Replayed=true` 零写（死代码消除，terminal 检查顺序正确，DB 验证）。
4. **P1 按上轮给出的修复方向（显式 fail-closed stub + 挂起 C8）落地**：`ReviewRequired` 恒 true 且有代码注释显式声明（依赖 Slice 3 review-cell 集成）；其他门全绿时 Close 返回 `CLOSURE_PACKAGE_REVIEW_REQUIRED`，`review_required` 未过不得关闭成立。
5. **次要项已修**：digest 指纹化 lead/lead_id/duplicate/blockers/status（确定性与对 LeadID/Blockers/Duplicate 敏感均独立验证）；closure-package handler 严格 JSON 400；测试钉死具体码 `OUTCOME_COVERAGE_INCOMPLETE`+`CLOSURE_PACKAGE_MISSING`、digest 确定性、handler 端点。
6. **数据真源/负向**：零 migration、零新表新列；`project.status='completed'` 为既有 CHECK 枚举值；无 lead → `ACCOUNTABLE_LEAD_REQUIRED`、终态 → `PROJECT_TERMINAL`，均零写且状态不变（DB 验证）。

### 残留 findings（非阻断，建议后续轮处理）

- **[test_gap·P2]** F1/F2 修复行为无配套回归测试：`TestClassifyProject_ActiveWithFrontier` 未断言 `ACTIVE_TASKS_PRESENT`；无 close-with-live-task（C7）与 re-close-replayed 测试。行为我独立验证过，但套件未钉死，防回归建议补。
- **[doc_gap·P2]** C8 挂起只记录在代码注释与本议题评论，CHECKLIST.yaml / 红测矩阵未落账；且 stub 注释引用的 W3 review-cell（`review_cell`/`review_drain` + migrations 280–285）在本分支不存在（migrations 止于 259、无 review 表），依赖落点需在 review 集成轮确认后解除 stub。
- **[minor]** closure-package 回执仍缺合同表的 version/included/excluded/coverage 字段；`package_id` 仍为调用方 idempotency_key（无稳定包身份）。

### 边界与风险

- 未改动任何源码/测试（临时验证文件已删除，工作树 clean）；本地 PG 已停止；前端不在本议题范围（HIV-562 Pixel）。
- 全量 handler 套件 1 例失败 `TestDashboardFailuresByAgentUsesExactWindow`：已在 base `528020fde` 复现同样失败，属既有时间窗环境问题（dashboard 文件本候选未触碰），与本次修复无关。