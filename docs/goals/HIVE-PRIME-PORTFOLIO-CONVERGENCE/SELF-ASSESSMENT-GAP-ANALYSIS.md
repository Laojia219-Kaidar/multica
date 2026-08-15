# SELF-ASSESSMENT / GAP ANALYSIS — HIVE-PRIME-PORTFOLIO-CONVERGENCE

> generated_at: 2026-08-14T00:30:10.751827+00:00（William 要求：对比目标、评估质量、找差距、启动修复）

## 1. 逐 VC 诚实对比（目标要求 vs 实际交付）

| VC | 目标原文要点 | 实际交付 | 差距判定 |
|---|---|---|---|
| VC-01 | Projects 页面 start/pause/stop/close + 预览/receipt | 后端 5 动作(continue/pause_dispatch/resume/close/supersede) + ProjectControlActions 前端面板 | **部分差距**：①"停止"(stop-current 终止在跑任务)未实现，只有 pause_dispatch(停新派发)；②仅 API 验证，未做浏览器点击旅程 |
| VC-02 | Issues 页面派给员工/部门 + 看 Task/Run/Runtime/Base | 后端 dispatch/stop/send-to-review + IssueDispatchControls | **部分差距**：①派工=派给当前 assignee(agent/squad)，非"派给明确员工/部门"(权威依赖)；②未做浏览器点击验证 |
| VC-03 | 候选自动进 Review、REVISE→Repair→再审、不再"审核无人工作"积压 | review cell 上线 + HIV-100 queued + REVISE→repair task | **部分差距**：验证到 REVISE→Repair(repair task 创建)，未验证 repair 完成后的自动 re-review→PASS；旧积压 drain 的"不再新增积压"未量化验证 |
| VC-04 | 单写者 lease + 取消后清理/恢复 | write lease(generation/expiry/cleanup/crash resume) + 单测 | **部分差距**：并发冲突只在单测验证，未做 live 并发 writer 冲突+清理的实跑验证 |
| VC-05 | 员工/组织/Agent/Runtime/基地映射 | 本地 base/runtime/agent + 故障迁移 + work-wall | **owner 已决策降级权威 readback**(选项 C) |
| VC-06 | 用量聚合 | /api/company-ops/usage 4.6B tokens | 达标 |
| VC-07 | Closure Package + Outcome、成果中心可追溯 | outcomes 游标 + generate_closure_package(仅 summary) | **差距**：Closure Package 只产出 summary(无 hash/version/独立复核)，close 不自动生成/promote Closure Package |
| VC-08 | Formal 只在 PASS+promotion+readback 后现 | fail-closed 503 | 达标(不变量) |
| VC-09 | 五列表分页 | Projects/Issues/Outcomes/Inbox 分页 | 达标 |
| VC-10 | 11 Project+非终态+未归属 disposition+执行 | 9 superseded+1 closed+36 归属(0 游离) | 达标 |
| VC-11 | 边界清晰+无第二权威 | 全 503 source_gap | 达标(不变量) |
| VC-12 | 3 项目浏览器端到端验收 | API 级 3 场景 + 浏览器页面抽查 | **关键差距**：浏览器页面抽查发现 work-wall 404 + 多数页面空渲染(auth/数据加载问题)，未真正浏览器点击完成 3 场景 |

## 2. 质量评估结论

- **后端(Go)**：14 JOIN 集成 + 生产部署 + VC-10 执行 + review cell 上线，质量较好（build/test/typecheck 全绿 + 真实数据回读）。
- **前端(Next.js)**：**质量不足**。浏览器实测发现：① work-wall 页面 404（镜像缺该路由）；② 多个页面 bodyLen 极小(266-1173)，数据未渲染（auth cookie 注入或等待时间问题）；③ 未做真正的浏览器点击 E2E。
- **证据充分性**：API 级证据充分，浏览器级证据不足（VC-01/02/12 的"浏览器"验收实际是 API 级）。

## 3. 待修复差距（按优先级）

1. **[P0] 前端镜像 stale**：work-wall 页 404 + dispatch UI/memory/workflow 页缺失 → 需从当前 HEAD 重建前端镜像 + 重新部署。
2. **[P1] 浏览器 E2E**：修好前端后，用 Playwright+Chrome 真实验证 3 场景（点击 continue/dispatch/REVISE verdict 等），补浏览器级证据。
3. **[P2] VC-01 stop-current**：实现"停止"(终止在跑任务)独立动作（复用 CancelTasksForIssue）。
4. **[P3] VC-07 Closure Package**：把 generate_closure_package 从 summary 升级为含 hash/version 的 candidate 包 + close 时关联。
5. **[P4] VC-03 re-review→PASS 实跑**：repair 完成后验证自动 re-review。

## 4. 当前阻塞

- 主机过载(Load 曾达 61，现回落 9-10)导致前端 docker 构建超时；需等负载回落后重试。


---

## 5. 修复进展（本轮）

> updated_at: 2026-08-14T00:38:18.910530+00:00

| 差距 | 状态 | 证据/commit |
|---|---|---|
| P3 Closure Package 仅 summary 无 hash | **已修复** | `d1ec7f8dd` — SHA-256 真 digest + `review_required` + `terminal_issues`/`coverage`；go build + `TestCloseSupersedeClosurePackage` 通过 |
| P4 repair→re-review 未达 PASS（re-review 任务被 cancel、issue 卡 in_review/review_state=NULL） | **已修复（根因+回归测试）** | 根因：`OnIssueEnteredReview` 在 `revise_requested`（repair pending）时误调 `handleReentry`，创建针对未完成 repair 的 re-review 并把 `revise_requested`→`queued`/`owner_decision`，导致 `OnRepairTaskCompleted` 跳过正式 re-review。修复 `6241aa8ee` + 新增 `TestReviewCell_ReviseRequestedReentryDoesNotPreemptRepair`（已验证：buggy 版本 FAIL owner_decision，fixed 版本 PASS）。 |
| P0 前端镜像 stale（work-wall 404、dispatch UI/memory/workflow 缺） | **修复中** | 前端镜像从当前 HEAD 重建（background，pid 89926）；主机过载导致首轮构建超时 |
| P1 浏览器 E2E 证据 | **待 P0 后** | Playwright+Chrome 脚本已就绪（`e2e-browser-check.cjs`），待前端重建部署后重跑 3 场景 |
| P2 stop-current | **不存在（误报）** | `POST /api/tasks/{taskId}/cancel` 已存在；VC-01"停止"= pause_dispatch + cancel-task 均已具备 |

## 6. 生产现场复现证据（P4 根因）

HIV-100（`3bcd9afb-51e3-482a-b447-13c27faff32c`）真实任务链：
- `4d3e45d8` review completed（23:18→23:21，REVISE 判决）
- `493fef72` repair completed（23:21→23:40）
- `cbde05ec` review **cancelled**（23:21→23:26）← 误触发的提前 re-review
- `a91212a9` review **cancelled**（23:28→23:29）← 误触发的提前 re-review
- 结果：issue 卡 `in_review` + `review_state=NULL` + 无活动 review task
