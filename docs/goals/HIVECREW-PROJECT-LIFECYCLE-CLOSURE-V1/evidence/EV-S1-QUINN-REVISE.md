## VERDICT: REVISE

审查对象：`0eeb7a05b89b530b34ca64ad23d35f8733c9b5c4`（后端）+ `74bb1fe31c965db713805744a9d5a1cf60066c4b`（前端），基线 `7f9597576`；合同 `docs/goals/HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1/reports/hiv-553-project-lifecycle-contract.md`。审查方式：只读源码/测试/schema，未改任何文件、未连生产 DB。

### 五项质量不变量核对结果

1. **VC-01 诚实分类** ✅ 通过。`ClassifyProject` 仅在 `ActiveTaskCount>0` 时输出 `active_with_frontier`；`in_progress` 无 nonterminal Task 一律走 stalled/review-blocked/source-gap。纯逻辑红测 `TestClassifyProject_StalledNoOpenTask`、`_FrontierEmptyWhenTaskCompleted` 及 handler 测试（无任务 seed 项目不得为 active）覆盖该不变量。
2. **状态机一致性** ✅ 通过（有边界偏差，见 F2）。判定顺序 E→A→C(blocked)→C(review)→G→B→D 与合同一致；`owner_decision_required` 仅作伴生 flag + `ACCOUNTABLE_LEAD_REQUIRED` blocker，从不作为主分类（合同允许 F 作附加门，候选实现选择了更保守的伴生门，符合议题要求）。
3. **派生读模型** ✅ 通过。diff 无任何 migration；`project.status` enum 未动（`planned/in_progress/paused/completed/cancelled`）；新增 2 条查询均为只读 SELECT。nonterminal 状态集与 schema CHECK 完全一致（issue: backlog/todo/in_progress/in_review/blocked；task: queued/dispatched/running/waiting_local_directory/deferred）。
4. **权限与幂等** ✅ 通过。`/api/projects/lifecycle` 与 `/api/projects/{id}/lifecycle` 挂在 `middleware.RequireWorkspaceMember` 组内（router.go:1210）；纯 GET 无副作用；`GetSnapshot` 以 workspace 过滤，跨 workspace 项目返回 404。
5. **隐私** ✅ 通过。4 条查询全部按 `workspace_id` 过滤；错误信息为固定文案，无数据/参数回显；无 secret 传递。

### 已执行验证（本机复跑）

- `go build ./...` ✅；`gofmt -l` 新增文件 ✅；`go vet ./internal/handler ./internal/service` ✅（仅 task.go 两处既有 lock-copy 告警，非本次改动）。
- `go test ./internal/service -count=1` ✅（含 10 条 ClassifyProject 红测全绿）。
- `pnpm install --frozen-lockfile` → `pnpm --filter @multica/views typecheck`（tsc --noEmit）✅ → `vitest run projects/components`：7 文件 31/31 ✅（含 project-health 6/6、projects-page 5/5）。
- **未能执行**：handler 测试与 11 项目 portfolio 诊断需要 DB，而隔离库 `127.0.0.1:55433` 当前未运行（无本地 postgres、docker daemon 未启动）。EV-S1-04/05 的 DB 证据在本环境不可复现，需在验收环境补跑。

### Findings

- **[evidence_gap] F1 — UI 把 terminal issue 数当作 "Outcome coverage" 展示（可复现，建议最小修复）。** `project_lifecycle.go` 中 `ConfirmedOutcomeCount: 0` 为硬编码（注释"ledger empty"）、`OutcomeTotal: terminalN`；前端卡片渲染 `outcome_coverage: {confirmed}/{total}`。合同明确"terminal disposition ≠ 成果验收、不得以 done_count 代替"；`ready_for_closure(D)` 分支经 projector 永不可达。最小修复：接入真实 expected-outcome/Outcome 台账（Slice 4）前，把 UI 文案改为 terminal issue 口径或隐藏该行，并移除硬编码常量，使 D 在存在 confirmed outcome 时可触发。
- **[evidence_gap] F2 — C 触发面与冻结分类表在 B/C 边界不一致（可复现）。** 候选规则"存在 in_review 即 C"，而合同冻结表将 BASES（17 个 in_review）与 ORCHESTRATION（3 个 in_review）判为 B/stalled；`ClassifyProject{ActiveTaskCount:0, ReviewIssueCount:17, NonterminalIssueCount:25}` 输出 `review_or_repair_blocked`，与冻结表 B 不一致。合同 C 定义还包含 REVISE 标记与 failed-task repair gap，这两类信号当前完全未进入输入。最小修复：在 EVIDENCE/合同镜像中显式记录该确定性规则为接受的 operationalization（Prime 自己的诊断 C=8/B=0 vs 冻结表 B=2/C=5 即此偏差），或细化触发（仅 blocked/REVISE/failed-repair→C，in_review 单独→B）并在隔离库重跑诊断比对。
- **[evidence_gap] F3 — handler 测试覆盖偏薄。** 仅有"无 lead 无任务 seed 项目 ≠ active + owner_decision"与 404 两条；缺跨 workspace 隔离断言、有 active task → active 断言、stalled bucket 渲染断言。建议补齐后随隔离库一起复跑。
- **[optional_improvement] O1 — `source_gap` 归入 "ready/待关闭" bucket。** HealthBucketSummary 的聚合计数会把 MAC-CANARY/SOURCE-UNDERSTAND 计入"待关闭"，与"待关闭"语义冲突（单个 badge 仍诚实显示 source_gap）。建议聚合层单独呈现 source gap 或归入 stalled/blocked。
- **[optional_improvement] O2 — `frozenSupersessions` 为代码内硬编码 project-ID seed。** 合同关闭门 1 要求 duplicate/supersede 决策有 Owner receipt；Slice 1 可接受，后续 slice 应迁移到持久化 receipt。
- **[optional_improvement] O3 — portfolio 请求全量扫 issue（Limit 100000）+ task + progress。** 当前 518 issue 量级无碍，Slice 5 分页需重访。
- **[optional_improvement] O4 — 'deferred' 计入 live frontier，而核心 runtime 的"active task"语义普遍排除 deferred（如 runtime.sql.go:18）。** 合同"nonterminal Task/Run 集"口径下可辩护，但 WIP/active 计数会与 runtime 视图不同，建议在 EVIDENCE 注明。
- **[out_of_scope] WIP policy 标红、close-gate preview、控制操作（continue/pause/resume/close/generate-package）** 属 Slice 2+，本候选未实现属预期。

### 未改变边界

未触碰任何 Project/Issue/Task 状态；未新增表/enum；未改生产 DB；未部署。遗留风险：DB 侧验收（handler 测试 + 11 项目诊断 vs 冻结表 A=1/B=2/C=5/D=0/E=1/G=2）在隔离库恢复后必须复跑，这是 REVISE 落地的前提。