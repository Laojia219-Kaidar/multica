身份：`arch-test-concurrency-matrix`（HiveCrew 开发团队一层 worker，测试/并发/迁移矩阵考古员）。职责：只读考古现有 Go/TS 测试、并发/race 模式与迁移编号，产出「红测优先」的测试矩阵与命令配方。读写权限边界：只读源码仓库（`/Volumes/HiveData/hivecosm/HQ-50-代码仓库/01-源码下载/multica`，main @ f7667c8d，git status 干净）与只读数据库（`docker exec multica-postgres-1 psql ...`）；**只写本文件**（`docs/goals/HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1/reports/W0-test-concurrency-migration-matrix.md`），不 checkout、不 commit、不修改任何源码/迁移/数据库、不写主仓库。**禁止递归：不派发任何子 Agent（未调用 rlm / 子任务）。** 未打印/复制/传递任何 secret（仅引用环境变量名 DATABASE_URL，未输出其值）。

# W0 · 测试 / 并发 / 迁移矩阵考古报告


---

## 0. 只读验证的源码/DB 事实锚点（本轮实测）

- 源码根：`/Volumes/HiveData/hivecosm/HQ-50-代码仓库/01-源码下载/multica`
  - `git rev-parse HEAD` = `f7667c8d7c540217c345d98beac33794e1f3e6d0`；`git status --porcelain` 为空（干净）。
- 测试规模（实测计数）：
  - Go `*_test.go`：**549** 个文件（`server/` 全树）。
  - TS `*.test.ts|tsx`（`packages/views` + `apps/web`）：**309** 个。
  - Playwright E2E `*.spec.ts`：**10** 个（`e2e/`）。
- Go 工具链：`go version go1.26.5 darwin/arm64`（go.mod 声明 `go 1.26.1`）；`pnpm@10.28.2`（node 26.5.0）。
- 只读 DB 实读（`multica-postgres-1` / db `multica`，schema_migrations 最新 = `259_agent_operational_mode`）：
  - `project` 表：`status` 枚举 = `planned | in_progress | paused | completed | cancelled`（**已有 paused/completed/cancelled**）；`lead_type`（member|agent）+ `lead_id`（uuid，可空）= 现有「lead」；**无 health / frontier / next_action / outcome_coverage / closure_readiness / optimistic version 列**。
  - `issue` 表：`status` 枚举（handler 侧常量）= `backlog | todo | in_progress | in_review | done | blocked | cancelled`；**无 disposition 列**（disposition 需要新读模型/元数据派生）。
  - `agent_task_queue` 表：`status` 枚举 = `queued | dispatched | running | completed | failed | cancelled | waiting_local_directory | deferred`；唯一约束 `idx_one_pending_task_per_issue_agent`（同一 (issue,agent) 仅一条 pending）。
  - 已有公司化/成果账本表（7 张）：`artifact_candidate`、`artifact_event`、`artifact_materialization_intent`、`artifact_promotion_claim`、`assignment_dispatch_receipt`、`execution_receipt`、`external_work_order_link`。
- 结论：`ProjectLifecycle / health / frontier / closure_readiness / next_action / generate_closure_package` 在 Go/TS 源码中 **零命中**——健康投影与控制操作（continue/pause/resume/close）是全新工作；但「成果/artifact/review/promotion/receipt/幂等」已有大量既有实现与测试可复用。

---

## 1. 现有测试盘点

### 1.1 Go — `server/internal/service`（与 project/issue/task/review/outcome 相关）

| 文件（相对 `server/`） | 覆盖内容 |
|---|---|
| `internal/service/task_claim_race_test.go` | DB 集成并发 claim：`TestClaimTaskConcurrentCapacityRespected` 用 `pg_sleep(0.2)` 触发器放大竞态窗口，2 worker 并发 `ClaimTask`，断言 `max_concurrent_tasks=1` 时**恰好 1 条**被 claim 且 active=1。 |
| `internal/service/task_complete_race_test.go` | mock `pgx` DBTX；CompleteTask/FailTask「已终态」幂等（重复 complete/fail 保持原状态）；nil-tx 终态前置读错误在 CAS 前传播、**零写**；provider_network 三层重试调度；taskfailure 分类器；旧 daemon 原因归一化。 |
| `internal/service/task_channel_media_race_test.go` | `raceInjectTxStarter` 在 deadline 读取与 `LinkUnownedChannelChatMessagesToTask` seal 之间注入一条 media 消息并提交，断言任务保持 `deferred`（不可 claim）直到 media 绑定——确定性复现 READ COMMITTED 竞态窗口。 |
| `internal/service/task_claim_operational_mode_test.go` | 20 个测试：agent `operational_mode` gate（active/resting/disabled/training 对 issue/quick-create/chat/autopilot/delegation/cross-workspace 的 claim 允许与拒绝）、`unknown` fail-closed、`TestClaimTask_OperationalMode_ConcurrentRestingDenies` 并发拒绝。仅允许连隔离 worktree DB（`127.0.0.1:55432`），拒绝 5432。 |
| `internal/service/task_batch_claim_test.go` | 多 runtime drain、max tasks cap、空输入。 |
| `internal/service/task_cancel_finalize_test.go` | 取消/延迟定案聊天任务：未开始同步恢复草稿、广播丢失可恢复、channel 消息封存等。 |
| `internal/service/task_dedup_head_sha_test.go` | PR head SHA 去重、repush 失效、无 PR 回退 legacy key。 |
| `internal/service/duplicate_pending_task_test.go` | 23505 唯一索引冲突识别：只认 `idx_one_pending_task_per_issue_agent`；`ErrDuplicatePendingTask` sentinel 可 `errors.Is`；**不泄漏 SQLSTATE/约束名**；二次 enqueue 后 pending 恰好 1 条。 |
| `internal/service/task_finalize_failure_test.go` / `task_issue_prepare_test.go` / `task_issue_broadcast_test.go` / `task_notify_test.go` | 终态失败路径、issue 任务准备、issue 广播、任务通知。 |
| `internal/service/cron_test.go` / `retry_deferred_test.go` / `rerun_workdir_reuse_test.go` / `resume_unsafe_test.go` / `trivial_done_test.go` / `resolve_originator_test.go` | cron、延迟重试、rerun 工作目录复用、unsafe resume 拒绝、originator 解析。 |

**CompanyOps（成果/artifact/review/promotion/回执）——本轮最相关的既有实现：**

| 文件（相对 `server/`） | 覆盖内容 |
|---|---|
| `internal/service/companyops_execution_lifecycle_test.go` | 46 个测试：claim/start/complete/replay/conflict、快照篡改回滚、伪造 retry lineage 拒绝、receipt 失败回滚、cancel/sweeper/recover、**NilTx 零写**族（cancel/fail/recover/offline/timeout/queued-expired 在无事务时零副作用）。 |
| `internal/service/companyops_artifact_outcome_test.go` | 20 个测试：Materialize→readback→Owner review；formal promotion 快乐路径/失败重试/**崩溃恢复不重复 POST**；transition 漂移全拒；`PromotionSameIDExactReplayNoDuplicateCalls`（幂等）；`PromotionClaimConcurrentDifferentID`（并发 claim 单赢）；payload 漂移 conflict；legacy 无 claim fail-closed。 |
| `internal/service/companyops_outcome_center_test.go` | 30 个测试：list/detail 读模型、search/filter/limit-offset、canonical employee/binding ref 校验、orphan/non-canonical conflict、rework lineage 一致性、malformed ref 拒绝、缺 rework / 重复 rework / 错 candidate rework conflict。 |
| `internal/service/companyops_assignment_test.go` + `_backend_test.go` + `_persistence_test.go` + `_production_test.go` | 派工幂等：`ExactReplayReturnsSameReceiptWithoutDuplicateTask`、`SameCommandDifferentPayloadFailsClosed`、原子性/回滚、receipt 不可变、ledger 无外键无级联。 |
| `internal/service/companyops_workorder_projection_test.go` | WorkOrder 投影：canonical create/replay/conflict、`ConcurrentExactReplayCreatesOneIssue`、缺 title/非成员拒绝、orphan link fail-closed、link 失败回滚 issue。 |
| `internal/service/companyops_authority_test.go` | HiveCosm authority resolver 精确 join 与 fail-closed。 |
| `internal/service/companyops_org_test.go` | 员工目录：六种 availability 状态映射、workspace 作用域、detail 脱敏、分页/搜索/过滤。 |

### 1.2 Go — `server/internal/handler`（project/issue/task/review/outcome 相关）

| 文件（相对 `server/`） | 覆盖内容 |
|---|---|
| `internal/handler/project_dates_test.go` | 项目 start/due 日期生命周期、search 携带日期、非法日期 400。 |
| `internal/handler/project_validation_test.go` | create/update 非法 status/priority 400、合法 201、delete 需 admin/owner。 |
| `internal/handler/project_resource_test.go` | 项目资源生命周期、SSH 仓库 URL、本地目录、daemon 作用域冲突、资源非法回滚。 |
| `internal/handler/issue_table_query_test.go` | **cursor 分页**：`TestIssueTableCursorRejectsAnotherQuery`、`TestIssueTableHierarchyRootKeysetPagination`、`TestIssueTablePositionCursorIncludesIndexableLowerBound`、跨组不越界、>1000 行状态分组。 |
| `internal/handler/issue_limit_validation_test.go` | list limit 校验与 clamp。 |
| `internal/handler/issue_batch_test.go` / `issue_move_test.go` / `issue_reassign_no_cancel_test.go` / `issue_child_done_test.go` / `issue_child_done_stage_test.go` / `issue_trigger_preview_test.go` | 批量、移动、reassign 不取消、子 issue done/stage、trigger preview。 |
| `internal/handler/task_terminal_wakeup_test.go` / `cancel_task_by_user_test.go` | 任务终态唤醒、用户取消任务。 |
| `internal/handler/agent_concurrency_test.go` | create/update/template 的 `max_concurrent_tasks` 边界与默认值。 |
| `internal/handler/companyops_test.go` | formal promotion 响应序列化、selectors 精确稳定、WorkOrder link 精确 revision/digest、**严格 JSON（拒绝未知/多余字段）**、错误码映射。 |
| `internal/handler/companyops_outcomes_test.go` | Outcome 线缆 schema、unknown query param 拒绝、nil service 503、canonical employee id、`formal_visible` 校验、**抑制 premature formal ref**。 |
| `internal/handler/companyops_org_test.go` | 目录 handler 严格公共信封、非 canonical 查询拒绝、错误稳定不泄漏原始值。 |

### 1.3 并发 / race 既有模式（文件路径 + 写法）

| 模式 | 代表文件 | 写法要点 |
|---|---|---|
| DB 集成 + 触发器放大竞态窗 | `internal/service/task_claim_race_test.go` | `pgxpool` 直连；`CREATE FUNCTION ... pg_sleep(0.2)` + `BEFORE UPDATE OF status` 触发器；`sync.WaitGroup` + `start chan` 同步并发；断言唯一 winner 与 DB 终态计数。 |
| mock `pgx` + CAS/幂等语义 | `internal/service/task_complete_race_test.go` | 手写 `mockDBTX`/`mockRow`，按 SQL 子串路由 QueryRow；断言「已终态」重复调用保持原状态、前置读错误阻止 CAS 写。 |
| 事务内注入竞态提交 | `internal/service/task_channel_media_race_test.go` | 包装 `Begin`/`Exec`，在特定 SQL 执行前回调注入并提交另一条写，确定性复现 READ COMMITTED 窗口。 |
| 幂等 replay / 冲突 fail-closed | `internal/service/companyops_assignment*_test.go`、`companyops_artifact_outcome_test.go`、`companyops_execution_lifecycle_test.go` | 同 command/digest 精确 replay 返回同 receipt 且不重复副作用；不同 payload/digest → conflict；快照/lineage 漂移 → 整体回滚。 |
| 并发单赢（advisory lock / 唯一键） | `internal/scheduler/concurrent_claim_test.go` | N 个 contender 并发 `tryClaim`，断言恰好 1 个 `Won=true`、其余 `Conflicted`，且 `sys_cron_executions` 恰好 1 行。 |
| 并发 claim 容量上界 | `internal/service/task_claim_race_test.go`、`internal/handler/agent_concurrency_test.go` | 唯一约束 `idx_one_pending_task_per_issue_agent` + `max_concurrent_tasks`。 |
| 其他 race/并发文件 | `internal/handler/comment_duplicate_enqueue_race_test.go`、`internal/handler/chat_draft_restore_race_test.go`、`internal/daemon/workdir_race_test.go`、`internal/daemon/runtime_probe_concurrency_test.go`、`cmd/server/runtime_sweeper_race_test.go`、`cmd/migrate/migrate_concurrent_test.go`（并发迁移施加只施加一次，靠 advisory lock + schema_migrations） | 覆盖重复入队、草稿恢复、工作目录、runtime 探测、sweeper 心跳、迁移并发。 |

### 1.4 TS — `packages/views`（projects / outcomes 相关 `*.test.tsx`）

| 文件（相对仓库根） | 覆盖内容 |
|---|---|
| `packages/views/projects/components/projects-page.test.tsx` | 紧凑行导航：名称非 title 链接、行表面导航、行内控件不触发导航、rowLink 修饰键/中键路径。 |
| `packages/views/projects/components/project-issue-metrics.test.ts` | `getProjectIssueMetrics`：从 project 记录取 `issue_count` / `done_count` 合计（**注意：目前 progress = done_count/issue_count，没有 nonterminal Task/Run 执行真值**）。 |
| `packages/views/projects/components/project-picker.test.tsx` + `.open-state.test.tsx` | 项目选择器与展开状态。 |
| `packages/views/projects/components/project-date-pickers.test.tsx` / `local-directory-hint.test.tsx` | 日期选择器、本地目录提示。 |
| `packages/views/outcomes/outcomes-page.test.tsx` | 列表/详情、深链、**未绑定 candidate 不暴露 object ref 预览**、not-found/loading/error 态、search+status 写 URL；review 动作需「显式合格 session」后才启用；promotion 失败保持可回读；**formal 状态仅在三个 formal 条件同时成立时显示**；premature ref 抑制；窄屏单列。 |
| `packages/views/outcomes/outcome-actions.test.tsx` | selectors/`outcomeCandidateId`/`isOutcomePromotable`/`isOutcomeFormal`；重试复用稳定 review UUID；无 candidate 拒绝 review；不可 promotable 状态拒绝 promotion；receipt 未回显命令抛错；review 后失效 detail 缓存。 |
| `packages/views/inbox/components/inbox-page.test.tsx` | 活动/归档视图切换、归档 URL 保持、archive 抽干回退。 |
| `packages/views/organization/organization-page.test.tsx` / `employee-dossier.test.tsx` | 组织树/名册/详情、binding badge、conflict 行 fail-closed、source-gap 横幅。 |
| `packages/views/issues/components/infinite-scroll-sentinel.test.tsx` | 无限滚动 sentinel 单 observer。 |

> 结论：frontend 已有 projects 页（状态列仅用 project.status 枚举 + done_count 进度条）与 outcomes 页（成熟）。**缺**：projects 页的 A–G 健康分类列、lead 缺失/停滞/待关闭分组、continue/pause/resume/close 动作预览与回执、closure package 视图。

---

## 2. 合同负向测试矩阵（hiv-553 负向表逐条映射）

「已有覆盖」指能找到对应断言文件；「缺的新红测」是建议路径（详见第 5 节）。**统一结论：健康投影与控制操作的 15 条负向几乎全部无既有覆盖**；但「幂等/冲突/回滚/成果」方向有大量可复用测试模式。

| # | 合同负向场景（必须结果） | (a) 已有覆盖？ | (b) 缺什么新红测 |
|---|---|---|---|
| 1 | `in_progress` 但无 nonterminal Task 且有 open Issue → health=`stalled_no_open_task`，不得显示「执行中」 | **无**（无 health 读模型） | 新增 service 红测：虚拟 project（status=in_progress、open Issue、live Task=0）→ 断言分类 B/stalled_no_open_task；portfolio API 不得把其标为 active。 |
| 2 | 全部 Issue terminal 但 Outcome 未覆盖/无 Closure Package → close preview 拒绝 `OUTCOME_COVERAGE_INCOMPLETE` / `CLOSURE_PACKAGE_MISSING` | **无**（无 close 操作） | 新增 service 红测：close preview 在缺 disposition/outcome/package 时 fail-closed 且零写。 |
| 3 | Issue=`done` 但无 Task-linked receipt → 不自动完成 Project、不进正式 Outcome | 部分（outcome 侧有 `LegacyTerminalWithoutClaimFailsClosed`、`CompanyOpsOutcomeCenter_ConfirmedEmptyFormalRefConflict`，见 `internal/service/companyops_artifact_outcome_test.go`/`companyops_outcome_center_test.go`） | 新增：project 层 close 门把「裸 done」计为 disposition 缺口。 |
| 4 | Issue=`in_progress`、唯一 Task 已 completed → frontier=`∅`、health=stalled | **无** | 新增：frontier 回链断言（`frontier_tasks` 为空 → `frontier_issue` 为空，不得用最新 Issue 顶替）。 |
| 5 | Task cancelled 但 Run/子进程仍存活 → pause/close 失败并返回 residual Run IDs | **无**（无 pause/close） | 新增：pause/close preview 扫描存活子进程，返回精确 residual IDs，整体不宣称 paused。 |
| 6 | 多个 live Task 超过 WIP policy → health 标红；continue 拒绝（除非 preview 授权） | 部分（`max_concurrent_tasks` 边界在 `agent_concurrency_test.go`、claim 容量在 `task_claim_race_test.go`） | 新增：project WIP 预算投影 + continue 拒绝路径。 |
| 7 | reviewer=author → Outcome accept/promotion 拒绝 | **无**（`ReviewArtifact`/`PromoteArtifact` 只校验 ActorUserID 有效，未比较 originator/implementer；实测 grep originator/reviewer/implementer 在 `companyops_artifact_outcome.go` 零命中） | 新增红测：`ReviewArtifact`/`PromoteArtifact` 在 `ActorUserID == 实现者/originator` 时返回 conflict；Closure Package 独立 reviewer ≠ author。 |
| 8 | 两 Project 指向同一 canonical authority → 自动 continue/close/merge 拒绝，进入 E/F Owner decision | **无** | 新增：health 分类 duplicate_or_superseded（E）+ 控制操作拒绝。 |
| 9 | lead 为空 → continue/resume/close 拒绝 `ACCOUNTABLE_LEAD_REQUIRED` | **无**（`project.lead_id` 可空，无控制操作；`project_validation_test.go` 只测 status/priority） | 新增：三操作在 lead 为空时拒绝且零写；portfolio 标 F gate。 |
| 10 | Artifact/resource 不可回读 → classification=G；不得把 done 当成果证据 | 部分（`VerifyWorkOrderTransitionForGetFailsClosedOnDrift`、`CompanyOpsOutcomeCenter_OrphanIssueConflict`、handler source_gap 503） | 新增：project 层 source_gap（G）读模型断言。 |
| 11 | cancelled Issue 无 reason / superseded 无 target → disposition 不完整，close 拒绝 | **无**（issue 无 disposition 列） | 新增：disposition 派生 + close 门红测。 |
| 12 | preview 后 version/frontier/coverage 变化 → commit conflict、零部分写入、要求重新 preview | 部分（模式：`PromotionPayloadDriftConflict`、`SameCommandDifferentPayloadFailsClosed`、`CoherentSnapshotTamperRollsBackStart`） | 新增：project 层 optimistic version + preview token 红测（replay 同版本成功、版本漂移 409 零写）。 |
| 13 | 同 idempotency key 重放 → 同 receipt、不重复创建 Task/Run/Outcome | **强覆盖**（`ExactReplayReturnsSameReceiptWithoutDuplicateTask`、`PromotionSameIDExactReplayNoDuplicateCalls`、`CancelReplayIdempotent`、`ConcurrentExactReplayCreatesOneIssue`、`duplicate_pending_task_test.go`） | 移植同模式到 project continue/pause/resume/close 的 idempotency key（不同 digest → 409）。 |
| 14 | 批量 close 任一项目失败 → 每项目独立 receipt、不静默部分成功 | **无**（无批量 close） | 新增：批量 close 每项目独立 receipt 红测。 |
| 15 | close 后历史可回读、不物理删除 | 部分（无 close；history 归档语义在 inbox/issue 分页有既有测试） | 新增：close 后 Issue/Task/Outcome/Package 可回读红测。 |

**Slice 1 特别项**（任务点名的映射）：in_progress 无 Task 显示 stalled → #1/#4；全部 Issue terminal 但无 Outcome/Closure Package 拒绝关闭 → #2；lead 缺失拒绝 → #9；同 key 幂等 → #13；分页漂移 → cursor 分页已有 `issue_table_query_test.go` 基线，缺 project portfolio 级分页红测；reviewer=author 拒绝 → #7。

---

## 3. 迁移编号方案（实测 `server/migrations/`）

- **当前最大编号 = 259**（`259_agent_operational_mode.{up,down}.sql`，也是 schema_migrations 最新一行，applied_at 2026-08-13T08:59:24Z）。
- 规模：296 个 `.up.sql`，249 个不同编号前缀；编号区间 1–259，**空缺前缀**：`70, 71, 99, 146, 147, 148, 255, 256, 257, 258`。
- 命名/递增规则（读 `server/internal/migrations/migrations.go` + `migrations_lint_test.go`）：
  - 每个迁移 = `NNN_description.up.sql` + `NNN_description.down.sql`（成对，lint `TestMigrationFilesHaveMatchingDirections` 强制）。
  - 文件名必须 `^(\d+)_` 开头（`TestNewMigrationPrefixesStartAfterLegacyRange` 强制）。
  - **001–148 为冻结 legacy 区间**（`maxLegacyMigrationPrefix = 148`）：30 个历史同号多 stem（如 020/026/029/032/033/035/040/041/043/046/050/060/065/069/079/083/084/091/095/096/098/109/111/112/113/120/122/124/127/128）被 `legacyDuplicateMigrationStems` 固定，**不得增删改**；新迁移编号从 **149 起**且**同一编号唯一**（`TestMigrationNumericPrefixesStayUniqueAfterLegacySet`）。
  - 排序：`Files("up")` 用字符串字典序排序、`Files("down")` 逆序；version = 去掉 `.up.sql`/`.down.sql` 后缀的文件名；`schema_migrations` 表 `(version text PK, applied_at timestamptz)`。
  - 施加命令：`cd server && go run ./cmd/migrate up`（advisory lock + schema_migrations 去重，并发施加一次，见 `cmd/migrate/migrate_concurrent_test.go`）。
- **若需新增生命周期迁移，应如何编号**：
  - 从 **`260`** 开始，`260_project_lifecycle_<name>.up.sql` / `.down.sql`；多条依次 260/261/262/…。
  - 注意 255–258 为空缺但不可复用（legacy 后新编号需唯一且递增；直接取当前 max+1 = 260 最安全，避免与已删编号混淆）。
  - 按合同「不建第二套 Project/Issue/Task/Outcome 真源」：健康/closure 读模型是**派生投影，优先零迁移**；只有在必须加「乐观锁 version 列 / 项目控制回执表 / disposition 存储」时才新建迁移，且只能是 additive（新增列/索引/表），不复制既有 truth 表。
  - 迁移测试范例可直接抄：`server/cmd/migrate/operational_mode_migration_test.go`（对 `259_agent_operational_mode.up/down.sql` 的隔离库往返验证）与 `server/internal/migrations/migrations_lint_test.go`。

---

## 4. 命令配方（标注「实测」 vs 「推断」）

**说明**：DB 相关 Go 测试读取 `DATABASE_URL`（多数测试在 DB 不可达时 `t.Skip`；claim 集成测试直连 localhost:5432 默认，operational_mode 测试要求隔离 55432）。以下「实测」为本轮真实执行并看到退出码/输出。

### 4.1 后端 Go（`server/` 为工作目录）

| 用途 | 命令 | 状态 |
|---|---|---|
| 列包 | `cd server && go list ./...` | ✅ 实测（54 个包） |
| 单包/多包「只编译不跑」 | `cd server && go test -run '^$' ./internal/service ./internal/handler` | ✅ 实测（service/handler 均 ok） |
| 单个 DB-free 单测 | `cd server && go test -run 'TestCompleteTask_AlreadyFinalized\|TestIsDuplicatePendingTaskErr' ./internal/service` | ✅ 实测（ok） |
| race 编译检查 | `cd server && go test -race -run '^$' ./internal/service` | ✅ 实测（ok，2.19s） |
| 全量 Go 测试（含 race，标准入口） | `cd server && bash scripts/test-go.sh --race`（等价 Makefile `make test`；内部把 `pkg/agent` 拆出 `-p 2 -parallel 2`） | 🧭 推断（来自 `scripts/test-go.sh` + `Makefile`，未全量执行） |
| 后端 build | `cd server && go build ./...`（CI 同款）；或 `go build -o /tmp/xxx ./cmd/server` / `./cmd/migrate` | ✅ 实测（migrate/server 单二进制 build 到 /tmp 成功） |
| 施加迁移 | `cd server && go run ./cmd/migrate up`（down 同理） | 🧭 推断（`Makefile migrate-up/down`；CI 也跑 `go run ./cmd/migrate up`） |

### 4.2 TS（仓库根为工作目录，pnpm workspace + turbo）

| 用途 | 命令 | 状态 |
|---|---|---|
| 单文件 vitest（views） | `pnpm --filter @multica/views exec vitest run projects/components/project-issue-metrics.test.ts`（路径相对 `packages/views` 根） | ✅ 实测（1 passed） |
| 全量 TS 测试 | `pnpm test`（turbo）或 CI 同款 `pnpm exec turbo test --filter='!@multica/docs' --filter='!@multica/mobile'` | 🧭 推断（`package.json`/`turbo.json`/`.github/workflows/ci.yml`） |
| typecheck | `pnpm typecheck`（turbo）或单包 `pnpm --filter @multica/views typecheck`（= `tsc --noEmit`） | 🧭 推断 |
| lint | `pnpm lint` 或单包 `pnpm --filter @multica/views lint`（= `eslint .`） | 🧭 推断 |
| build+typecheck+lint 合体 | `pnpm exec turbo build typecheck lint --filter='!@multica/docs' --filter='!@multica/mobile'` | 🧭 推断（CI） |
| Playwright E2E | `pnpm exec playwright test`（需先起 UI:3000 + API:8080；`scripts/check.sh` 会自动起） | 🧭 推断（`scripts/check.sh` + `playwright.config.ts`） |
| 全流水线 | `ENV_FILE=<env> bash scripts/check.sh` 或 `make check`（typecheck → TS 单测 → Go 测试 → E2E） | 🧭 推断（`Makefile`/`scripts/check.sh`） |

---

## 5. 红测优先级清单（建议 Prime 先落地，Slice 1 优先）

统一原则：**先写合同反例、断言要点、可复用既有 fixture 模式；红测文件与实现文件同包同目录**（`internal/service/*_test.go` 用 `package service` 内部可见，可直接测未导出投影函数）。

### Slice 1 — Project Health 投影（先做，纯只读）

1. **stalled_no_open_task**（红）→ `server/internal/service/project_lifecycle_health_test.go`
   断言：project.status=in_progress + 有 open Issue + live Task/Run=0 → health=`stalled_no_open_task`（B），portfolio API 不得显示 active/「执行中」。
2. **active_with_frontier**（正向锚点）→ 同上
   断言：≥1 条 nonterminal Task → health=`active_with_frontier`（A），frontier_issue 由 `frontier_tasks.issue_id` 回链（不得用「最新 Issue」替代）。
3. **frontier 空但 Issue in_progress（唯一 Task 已 completed）** → 同上
   断言：`frontier_tasks=∅` → `frontier_issue=∅`、health=stalled；禁止用 in_progress Issue 制造 live work（合同负向 #4）。
4. **缺 lead → F gate**（红）→ 同上
   断言：lead_id 为空的项目，即使满足 stalled，也必须附加 `owner_decision_required`（F）门，portfolio 返回 `lead=UNASSIGNED` + owner 决策提示。
5. **review/repair 阻塞（C）** → 同上
   断言：有 in_review/REVISE/blocked Issue 但无对应 live Task → `review_or_repair_blocked`；不得归为普通 stalled。
6. **duplicate/superseded（E）与 source_gap（G）** → 同上
   断言：两项目同 canonical authority → E；Artifact/resource 指针不可回读 → G，且 `terminal disposition coverage` 不得冒充 `confirmed outcome coverage`。
7. **last_progress_at 语义** → 同上
   断言：`last_progress_at = max(successful Task.completed_at, outcome decision_at, lifecycle receipt 时间)`；失败/取消只记 activity、不计 progress。
8. **portfolio API 契约（handler）** → `server/internal/handler/project_lifecycle_test.go`
   断言：无 JWT → 401；未知查询字段拒绝；每个 project 返回 lead/frontier/WIP/last_progress/next_action/outcome_coverage/closure_readiness；**分页 cursor 与查询参数绑定**（仿 `issue_table_query_test.go` 的 `TestIssueTableCursorRejectsAnotherQuery`）。

### Slice 2 — 控制操作（continue/pause/resume/close preview+commit）

9. **lead 缺失拒绝**（红）→ `server/internal/service/project_lifecycle_control_test.go`
   断言：continue/resume/close 在 lead 为空 → `ACCOUNTABLE_LEAD_REQUIRED`，零写（合同负向 #9）。
10. **同 key 幂等 replay**（红→绿）→ 同上
    断言：同 idempotency key + 同 digest → 返回同 receipt、不重复建 Task/Run/Outcome；不同 digest → 409（复用 `companyops_assignment_test.go` 模式）。
11. **preview→commit 版本漂移冲突**（红）→ 同上
    断言：preview 后 project version/frontier/coverage 变化 → commit 返回 conflict、零部分写入、要求重新 preview（复用 `PromotionPayloadDriftConflict` 模式）。
12. **pause 残留 Run 拒绝**（红）→ 同上
    断言：Task cancelled 但子进程/Run 存活 → pause/close 失败并返回精确 residual Run IDs，不宣称 paused（合同负向 #5）。
13. **close preview fail-closed**（红）→ `server/internal/service/project_lifecycle_closure_test.go`
    断言：全 Issue terminal 但 Outcome 未覆盖 / 无 Closure Package → `OUTCOME_COVERAGE_INCOMPLETE` / `CLOSURE_PACKAGE_MISSING`，零写（合同负向 #2）。

### Slice 3/4 — review/repair 与 outcome/closure

14. **reviewer=author 拒绝**（红）→ 扩展现有 `server/internal/service/companyops_artifact_outcome_test.go`（或新 `project_lifecycle_review_test.go`）
    断言：ReviewArtifact / PromoteArtifact 的 ActorUserID 等于候选实现者/originator → conflict；Closure Package 独立 reviewer ≠ author（合同负向 #7；当前实现**无此守卫**，是真实缺口）。
15. **批量 close 每项目独立 receipt**（红）→ `server/internal/service/project_lifecycle_control_test.go`
    断言：批量 close 中任一失败，其余项目独立 receipt，禁止用总成功覆盖失败项（合同负向 #14）。

> 落地顺序建议：先 #1–#8（Slice 1 只读投影，可与后端/前端子 Agent 并行且零冲突），再 #9–#13（Slice 2），最后 #14–#15。全部红测落地前可先跑 `go test -run '^$' ./internal/service ./internal/handler` 确认编译基线，DB 集成红测沿用 `newResolveOriginatorPool`/`newTaskClaimRacePool` 的自建 workspace+cleanup fixture 模式。

---

## 附：本轮实测命令输出摘要（证据）

```
git -C <src> rev-parse HEAD            -> f7667c8d7c540217c345d98beac33794e1f3e6d0（status 空）
go list ./...                          -> 54 packages
go test -run '^$' ./internal/service ./internal/handler -> ok / ok
go test -run 'TestCompleteTask_AlreadyFinalized|TestIsDuplicatePendingTaskErr' ./internal/service -> ok
go test -race -run '^$' ./internal/service -> ok
go build -o /tmp/... ./cmd/migrate     -> exit 0
go build -o /tmp/... ./cmd/server      -> exit 0
pnpm --filter @multica/views exec vitest run projects/components/project-issue-metrics.test.ts -> 1 passed
psql: schema_migrations max=259_agent_operational_mode; project.status enum=planned|in_progress|paused|completed|cancelled; project.lead_type/lead_id 可空; agent_task_queue.status enum 含 deferred; 7 张 companyops/ledger 表存在。
```
