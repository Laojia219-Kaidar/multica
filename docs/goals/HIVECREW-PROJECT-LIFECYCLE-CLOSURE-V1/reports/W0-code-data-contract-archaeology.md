# W0 · 代码/数据合同考古报告（backend source-map + DB truth）

> 身份：HiveCrew 开发团队一层 worker `arch-code-data-contract`（代码/数据合同考古员）。
> 职责：只读考古后端源码与数据库，产出精确 source-map 报告。
> 读写权限边界：源码仓库与数据库**只读**；只写本报告文件（`W0-code-data-contract-archaeology.md`），不 checkout、不 commit、不修改任何源码或 DB，不写主仓库，不创建其他文件。
> **禁止递归：不派发任何子 Agent（未调用 rlm / 子任务）。**
> Secret 纪律：本报告只提及 secret 名称（如 `JWT_SECRET`、`HIVECOSM_AUTHORITY_BEARER_TOKEN`、`DATABASE_URL`），不打印、不复制、不传递任何 secret 值。

审计时点：2026-08-13（UTC，与 Prime 校准一致）
代码主线（只读）：`/Volumes/HiveData/hivecosm/HQ-50-代码仓库/01-源码下载/multica`，`main @ f7667c8d`（`git status --porcelain` 干净，与 CHECKLIST 锚点一致）。
数据库（只读）：`docker exec multica-postgres-1 psql -U multica -d multica`。以下所有行数/约束均为**本轮实读**。

---

## 0. 关键结论（先读）

1. **Project 状态真源**：`project.status` 5 值枚举 `planned|in_progress|paused|completed|cancelled`，Go 常量在 `server/internal/handler/project.go:204`，DB CHECK 同名 `project_status_check`。**不存在 `closed`/`superseded`/`stalled` 等状态**——HIV-553 状态机的 `CLOSED/SUPERSEDED` 与 A–G health 都是派生读模型，不能在 `project.status` 上新增同义枚举列。
2. **Issue 状态真源**：`issue.status` 7 值枚举 `backlog|todo|in_progress|in_review|done|blocked|cancelled`，Go 常量在 `server/internal/handler/issue.go:78`，DB CHECK `issue_status_check`。**没有 `superseded`/`waived`/`migrated` 状态**——HIV-553 的 disposition 分类（`superseded_by`/`migrated_to`/`waived(reason)`）在现有 schema 中**无处承载**，必须落到现有 `comment`/receipt 或新派生字段，而不是新 Issue 状态。
3. **Task 状态真源**：`agent_task_queue.status` 8 值枚举 `queued|dispatched|running|completed|failed|cancelled|waiting_local_directory|deferred`（DB CHECK `agent_task_queue_status_check`）。nonterminal = `queued|dispatched|running|waiting_local_directory|deferred`；terminal = `completed|failed|cancelled`。
4. **ReviewPipeline 不存在**：全后端 `rg -i "ReviewPipeline"` 零命中。现实现中“审核”有三条并行路径：(a) Issue 状态 `in_review`（仅状态位，默认不产生独立 Review Task）；(b) companyops 的 `artifact_event` 生命周期（`approved|changes_requested`，即合同中的 PASS/REVISE 语义）；(c) squad leader 评估 `squad_leader_evaluated` 写入 `activity_log`。**没有任何地方生成“独立 Review Task”**——这是 Slice 3 的核心缺口。
5. **outcome/receipt 6 张账本表全部 0 行**（execution_receipt、artifact_candidate、artifact_event、artifact_materialization_intent、artifact_promotion_claim、assignment_dispatch_receipt、external_work_order_link），与 CHECKLIST 锚点一致。
6. **天然落点**：`ProjectLifecycleSnapshot` 应新增 `server/internal/service/project_lifecycle*.go`（只读 service，复用现有 `db.Queries`）→ `server/internal/handler/project_lifecycle*.go` → 在 `server/cmd/server/router.go` 的 `/api/projects` 块（1306–1317 行）旁新增只读路由；SQL 层**复用** `project.sql`、`issue.sql`、`agent.sql`（task）、`companyops.sql`（outcome）既有查询，不建第二张 Project/Issue/Task 表。

---

## 1. 后端 source map

所有路径相对源码根：`server/`。行号以 `main @ f7667c8d` 为准。

### 1(a) Project service / handler

Project **没有独立 service 文件**——业务逻辑直接写在 handler 里，数据访问走 `h.Queries`（sqlc 生成，`server/pkg/db/generated/project.sql.go`）。

`server/internal/handler/project.go`（913 行）：

| 函数 | 行号 | 职责 |
|---|---|---|
| `ProjectResponse`（struct） | 28–47 | GET/LIST 响应体：`id, workspace_id, title, description, icon, status, priority, lead_type, lead_id, start_date, due_date, created_at, updated_at, issue_count, done_count, resource_count` |
| `projectToResponse` | 48–64 | `db.Project` → wire |
| `loadProjectIssueStats` | 66–72 | 单项目 issue 统计（复用 `GetProjectIssueStats`） |
| `loadProjectResourceCount` | 74–81 | 单项目 resource 数 |
| `ListProjects` | 117–173 | `GET /api/projects/`；支持 `?status`/`?priority` 过滤；批量回填 `issue_count`/`done_count`/`resource_count` |
| `GetProject` | 175–207 | `GET /api/projects/{id}` |
| `validProjectStatuses` | 204 | **Project status 枚举常量** |
| `validProjectPriorities` | 205 | Project priority 枚举常量 |
| `validateProjectEnum` | 209–223 | 枚举预校验（400 + 允许值列表） |
| `CreateProject` | 234–436 | `POST /api/projects/`；title 必填；默认 `status=planned`；可原子批量建 `project_resource`（tx 路径）；发布 `EventProjectCreated` |
| `UpdateProject` | 438–565 | `PUT /api/projects/{id}`；partial update（用 `rawFields` 区分“字段缺席”与“显式 null”）；发布 `EventProjectUpdated` |
| `DeleteProject` | 567–637 | `DELETE /api/projects/{id}`；owner/admin 门禁；`LockProjectForDelete` + 清 chat 引用 + 删项目；发布 `EventProjectDeleted` |
| `buildProjectSearchQuery` | 639–766 | 动态搜索 SQL（title/description 分词排名，`include_closed` 过滤 `status NOT IN ('completed','cancelled')`） |
| `SearchProjects` | 768–913 | `GET /api/projects/search?q=`；`X-Total-Count` |

请求体：`CreateProjectRequest`（86–100，内嵌 `resources`）、`UpdateProjectRequest`（109–122）。
响应体：`ProjectResponse`（28–47）、`SearchProjectResponse`（634–638，加 `match_source`/`matched_snippet`）。

`server/internal/handler/project_resource.go`（19 KB）：`project_resource` CRUD（`ListProjectResources`/`CreateProjectResource`/`UpdateProjectResource`/`DeleteProjectResource`），资源类型多态（`resource_type` 自由串 + `resource_ref` JSONB）。

CLI 侧枚举镜像：`server/cmd/multica/cmd_project.go:94`（`validProjectStatuses`，与 handler 重复定义，用于 CLI 校验）。

### 1(b) Issue service / handler

Service：`server/internal/service/issue.go`（约 640 行）。**创建 Issue 的真相路径**：

| 符号 | 行号 | 职责 |
|---|---|---|
| `IssueService`（struct） | 27–38 | 持有 `q *db.Queries`、tx、bus、analytics、`ts *TaskService` |
| `IssueCreateParams` | 53–82 | 创建参数 |
| `IssueCreateOpts` | 84–120 | 选项（`AllowDuplicate`、origin、trigger 等） |
| `ErrActiveDuplicate` | 122 | 活跃重复 Issue 拒绝 |
| `IssueCreateResult` | 149–179 | 返回 Issue + attachments + labels |
| `Create` | 181–205 | 创建入口 |
| `createInTransaction` | 206–347 | tx 内：分配 `number`、去重、insert、labels、attachments |
| `finishCreate` | 348–377 | 提交后事件/分析 |
| `maybeEnqueueOnAssign` | 514–533 | 指派后自动入队 Task |
| `shouldEnqueueAgentTask` | 535–541 | 是否给 agent 入队（agent 在线、私有权限等） |
| `shouldEnqueueSquadLeaderOnAssign` | 553–559 | squad 场景 |
| `enqueueSquadLeaderTask` | 582–641 | squad leader Task 入队 |

Handler：`server/internal/handler/issue.go`（3567 行）：

| 符号 | 行号 | 职责 |
|---|---|---|
| `IssueResponse`（struct） | 32–77 | 响应体：`id, workspace_id, number, identifier, title, description, status, priority, assignee_type/id, creator_type/id, parent_issue_id, project_id, position, stage, start_date, due_date, created_at, updated_at, metadata, properties, reactions, attachments, labels` |
| **`validIssueStatuses`** | **78** | **Issue status 枚举常量** |
| `validIssuePriorities` | 79 | Issue priority 枚举 |
| `ListIssues` | 778–1274 | `GET /api/issues/`；过滤器（priority/assignee/project/creator/status/…）；分页 |
| `QueryIssues` | 764–776 | `POST /api/issues/query`（GET 的 POST 双胞胎，超长过滤器集） |
| `SearchIssues` | 625–763 | 搜索 + 排序 |
| `ListGroupedIssues` | 1430–1863 | `GET /api/issues/grouped`（泳道） |
| `GetIssue` | 1864–1901 | `GET /api/issues/{id}` |
| `ListChildIssues` | 1903–1945 | 子议题 |
| `ListChildrenByParents` | 1947–2011 | 批量子议题（泳道挂载） |
| `ChildIssueProgress` | 2013–2065 | 子议题进度 |
| `QuickCreateIssue` | 2084–2282 | agent 快速建 Issue |
| `CreateIssue` | 2405–2674 | `POST /api/issues/` |
| `UpdateIssue` | 2707–2997 | `PUT /api/issues/{id}`；支持 `suppress_run`/`handoff_note`；状态/指派变更可能触发 Task 入队 |
| `DeleteIssue` | 3160–3197 | `DELETE /api/issues/{id}` |
| `BatchUpdateIssues` | 3204–3501 | `POST /api/issues/batch-update` |
| `BatchDeleteIssues` | 3507–3566 | `POST /api/issues/batch-delete` |

请求体：`CreateIssueRequest`（2373–2400）、`UpdateIssueRequest`（2676–2705）、`QuickCreateIssueRequest`（2067–2079）。
Issue 表格读模型：`issue_table_query.go`（动态列过滤/排序）、`issue_table_rows.go`、`issue_table_group.go`、`issue_table_facets.go`（`POST /api/issues/table/*`）。

### 1(c) Task / Run（agent_task_queue）

Service：`server/internal/service/task.go`（241 KB，约 5600 行）。生命周期核心函数：

| 阶段 | 函数 | 行号 | 说明 |
|---|---|---|---|
| 入队 | `EnqueueTaskForIssue` | 939 | issue 指派/评论触发 → 建 `agent_task_queue` |
| 入队(带交接) | `EnqueueTaskForIssueWithHandoff` | 953 | `handoff_note` |
| 入队内部 | `enqueueIssueTask` | 1002 | 幂等（`ErrDuplicatePendingTask`，577 行）；绑定 issue+agent+trigger |
| 入队(计划) | `enqueueIssueTaskWithCommentPlan` | 1015 | 合并评论计划 |
| 准备 | `prepareIssueTaskWithCommentPlan` | 1054 | 组 context/attribution |
| claim | `ClaimTask` | 2449 | agent 视角单任务 claim |
| claim | `ClaimTaskForRuntime` | 2564 | runtime 视角单任务 claim |
| claim | `ClaimTasksForRuntimes` | 2763 | **批量 claim**（协调器） |
| claim 收尾 | `FinalizeTaskClaim` | 2681 | claim 成功后 mint task token / 广播 |
| claim 失败回退 | `RequeueTaskAfterClaimFailure` | 2722 | 失败回 queued |
| 开始 | `StartTask` | 2966 | `dispatched→running` |
| 完成 | `CompleteTask` | 3080 | `running→completed`；写 chat outcome / 收尾 |
| 失败 | `FailTask` | 3460 | `running→failed`；`failure_reason` |
| 重试 | `MaybeRetryFailedTask` | 3891 | 自动重试（`retryableReasons` 3773；`attempt/max_attempts` 上限） |
| 手动 rerun | `RerunIssue` | 4030 | 人类重跑（`force_fresh_session`） |
| 取消 | `CancelTask` | 2078 | 取消单任务 |
| 取消(带回执) | `CancelTaskWithResult` | 2094 | 取消 + 结构化结果（chat/issue 分支） |
| 按 issue 取消 | `CancelTasksForIssue` | 1846 | issue 级批量取消 |
| 按 agent 取消 | `CancelTasksForAgent` | 1865 | agent 级批量取消 |
| 派发停止 | `BroadcastCancelledTasks` | 1918 | 取消广播 |
| 离线清理 | `FailTasksForOfflineRuntimes` | 4472 | 离线 runtime 任务失败 |
| 过期清理 | `FailStaleTasks` | 4478 | 陈旧任务失败 |
| 孤儿恢复 | `RecoverOrphanedTasksForRuntime` | 4490 | 守护进程重启孤儿恢复 |

状态迁移（事实上的状态机，分散在 `agent.sql` 的 `ClaimAgentTask`/`StartAgentTask`/`CompleteAgentTask`/`FailAgentTask`/`CancelAgentTask` 等 sqlc query 里）：

```
queued --claim--> dispatched --start--> running
running --complete--> completed
running --fail--> failed
queued/dispatched/running --cancel--> cancelled
claim 失败 --RequeueTaskAfterClaimFailure--> queued
failed --MaybeRetryFailedTask(自动)--> 新 queued（parent_task_id 回链）
```

关键幂等护栏：`idx_one_pending_task_per_issue`（迁移 022：同 issue 最多一个 queued/dispatched）；`ErrDuplicatePendingTask`（service/task.go:577）。

Handler（daemon 视角）：`server/internal/handler/daemon.go`
- `ClaimTasksByRuntime` 1387、`ClaimTaskByRuntime` 2549、`ExtendTaskPrepareLease` 2848、`StartTask` 2876、`MarkTaskWaitingLocalDirectory` 2910、`ReportTaskProgress` 2943、`CompleteTask` 2986、`GetTaskStatus` 3624、`FailTask` 3653、`AckTaskCancelled` 3786、`GetActiveTaskForIssue` 3862、`CancelTask` 3889、`ListTasksByIssue` 3919。
- `TaskCompleteRequest`（2970–2984）、`TaskFailRequest`（3637–3653）。

Handler（用户视角）：
- `server/internal/handler/task_lifecycle.go`：`RecoverOrphanedTasks`（26）、`PinTaskSession`（62）、`RerunIssue`（110，`POST /api/issues/{id}/rerun`）。
- `server/internal/handler/chat.go:1387` `CancelTaskByUser`（`POST /api/tasks/{taskId}/cancel`，1520 行注册）。

Task 响应体：`server/internal/handler/agent.go:273` `AgentTaskResponse`（含 `Agent`、`ProjectID/Title/Description`、`ProjectResources`、`Repos`、`ConnectedApps`、attribution 等）；`taskToResponse` 在 `agent.go:597`。

### 1(d) Review pipeline（真相：不存在 ReviewPipeline）

`rg -i "ReviewPipeline"` 全 `server/` **零命中**。`in_review` 只是 `issue.status` 的一个枚举值，不产生独立 Review Task。三条实际审核路径：

**(1) Issue 状态 `in_review`（人工/agent 状态翻转）**
- 枚举定义：`internal/handler/issue.go:78`。
- 翻转点：`UpdateIssue`（issue.go:2707）、CLI `multica issue status <id> in_review`、squad leader 完成时（`daemon/execenv/runtime_config_sections.go:499` 提示词）。
- **服务端没有「task 完成自动置 in_review」的钩子**。`UpdateIssueStatus` 全后端仅两处调用：`task.go:4657`（失败任务把卡住的 `in_progress` 重置回 `todo`）、`github.go:1718`（PR 状态同步）。`in_review` 必须由 agent 自己（CLI）或 HTTP `UpdateIssue` 显式设置——印证合同「completed Task ≠ 已进入独立审核」，**无独立 reviewer、无 verdict 回执**。

**(2) companyops artifact 审核（最接近合同 PASS/REVISE/UNKNOWN 语义）**
- 事件枚举：`internal/companyops/artifact_lifecycle.go:119–128`：
  `submitted | changes_requested | approved | promotion_requested | promotion_succeeded | promotion_failed | authority_readback_confirmed`。
  → `approved` = PASS；`changes_requested` = REVISE；**没有 UNKNOWN**。DB CHECK（`artifact_event.event_type`，迁移 240）多列一个 `rejected`，但 Go 状态机（`validateTransitionLocked`，artifact_lifecycle.go:260）不使用 `rejected` —— 遗留偏差。
- 状态机（`validateTransitionLocked`，260–305）：
  `submitted → changes_requested|approved → promotion_requested → promotion_succeeded|promotion_failed → (promotion_failed→promotion_requested 重试) → (promotion_succeeded→authority_readback_confirmed)`。
- service 入口：`service/companyops_artifact_outcome.go:449` `ReviewArtifact`。输入 `CompanyOpsArtifactReview{Decision, IdempotencyID, Feedback, ActorUserID}`（43–49）。
  - 校验（456–471）：Decision 必须是 `changes_requested|approved`；`ActorUserID.Valid` 必须（**Owner identity required**）；`changes_requested` 必须带 feedback。
  - `changes_requested` → 在同一 issue 下准备一条 `artifact_revision` 证据的 repair Task（505–540，调用 `prepareIssueTaskWithCommentPlan`）——**这是 REVISE→repair 的唯一现成实现**，可作为 Slice 3 复用模板。
- handler 入口：`handler/companyops.go:314` `CreateCompanyOpsArtifactReview`（`POST /api/company-ops/artifact-reviews`）。Decision 白名单校验在 367–370。

**(3) squad leader 评估**
- `handler/squad.go:896` `RecordSquadLeaderEvaluation`（`POST /api/issues/{id}/squad-evaluated`，router 1338）。outcome 只允许 `action|no_action|failed`，写入 `activity_log`，仅 squad leader agent 可写（897–940）。

**Reviewer 独立性如何表达（现状 vs 合同）**：
- HiveCrew 本地 `ReviewArtifact` **只要求 `ActorUserID.Valid`（Owner）**，**不检查 reviewer != implementer**。独立性由**外部 HiveCosm Formal Artifact authority** 强制：`internal/companyops/hivecosm_formal_artifact_client.go:420` 要求 `OwnerReview.ReviewDecisionID`、`OwnerReview.ReviewerID` 非空且 `Decision == "accept"`；authority 侧用独立 `ReviewerID` 表达独立复核。
- 结论：合同负向测试「reviewer=author 拒绝」目前**在 HiveCrew 本地服务层不成立**，需要在 `ReviewArtifact`（或新的 project_lifecycle review 服务）显式加 guard，否则只能靠 HiveCosm authority 兜底。

### 1(e) companyops 成果 / Outcome

Service 层：

| 文件 | 关键函数（行号） | 职责 |
|---|---|---|
| `service/companyops_outcome_center.go` | `NewCompanyOpsOutcomeCenterService`(231)；`ListOutcomes`(237)；`GetOutcome`(321)；`IsValidCompanyOpsArtifactEventType`(86)；`IsValidCompanyOpsOutcomeStatus`(49) | Outcome Center 读模型：从 `assignment_dispatch_receipt` + issue/workspace/agent/task/receipt/artifact_event 汇总，生成 `CompanyOpsOutcomeSummary/Detail` |
| `service/companyops_workorder_projection.go` | `Project`(72)；`NewCompanyOpsWorkOrderProjectionService`(56)；`validate…`(193) | 外部 WorkOrder → 本地 Issue 投影（锁定 `external_work_order_link`，不复制 authority） |
| `service/companyops_artifact_outcome.go` | `NewCompanyOpsArtifactService`(129)；`MaterializeCompletedTask`(149)；`GetIssueOutcome`(221)；`ReviewArtifact`(449)；`PromoteArtifact`(589)；`attemptArtifactPromotion`(711)；`runArtifactReadback`(807) | Temporary Artifact candidate 物化 + review + Formal promotion + GET readback |
| `service/companyops_execution_lifecycle.go` | `ensureCompanyOpsExecutionClaim`(130)；`finalizeCompanyOpsExecutionCompleted`(225)/`Failed`(242)/`Cancelled`(259)；`replay…`(278/291/304) | `execution_receipt` 幂等写（claim/complete/fail/cancel 的 append-only 回执）。钩子：`task.go:2707`（claim）、`3102/3165`（complete）、`3556/3651`（fail）、`1953/2003/2127/2140`（cancel） |
| `service/companyops_assignment.go` | `Dispatch`(93) | assignment 派发（preview→commit 语义雏形），写 `assignment_dispatch_receipt` |
| `service/companyops_persistence.go` | `EnsureExternalWorkOrderLink`(84)；`AppendAssignmentDispatchReceipt`(118)；`CreateExecutionReceiptClaim`(160)；`FinalizeExecutionReceipt`(186) | 6 张账本表的仓储封装（append-only，无 UPDATE/DELETE） |
| `service/companyops_authority.go` | `Resolve`(54)；`ResolvedCompanyOpsAuthority`(31) | 解析 HiveCosm authority（WorkOrder/Employee/Binding/Agent） |
| `service/companyops_org.go` | `GetOrganization`(70)；`GetEmployees`(134)；`GetEmployee`(184) | 组织目录读模型 |

Handler 层：

| 文件 | 关键函数（行号） | 路由 |
|---|---|---|
| `handler/companyops.go` | `GetCompanyOpsWorkContext`(190)；`CreateCompanyOpsAssignment`(255)；`CreateCompanyOpsArtifactReview`(314)；`CreateCompanyOpsFormalArtifactPromotion`(399)；`resolveCompanyOpsRequest`(481) | `/api/company-ops/work-context`、`/assignments`、`/artifact-reviews`、`/formal-artifact-promotions` |
| `handler/companyops_outcomes.go` | `GetCompanyOpsOutcomes`(130)；`GetCompanyOpsOutcome`(253) | `/api/company-ops/outcomes`、`/outcomes/{commandId}` |
| `handler/companyops_org.go` | `GetCompanyOpsOrganization`(83)；`GetCompanyOpsEmployees`(117)；`GetCompanyOpsEmployee`(191) | `/api/company-ops/organization`、`/employees`、`/employees/{employeeId}` |

company-ops 路由组（router.go 1209–1220）统一挂 `middleware.RequireWorkspaceMember` + `handler.RequireHumanActor`（1210–1211）。

### 1(f) router.go 路由注册行号段

`server/cmd/server/router.go`（1792 行）：

| 域 | 行号段 | 路由 |
|---|---|---|
| daemon task（claim/start/complete/fail/cancel） | 946–978 | `/runtimes/{runtimeId}/tasks/claim`(946)、`/tasks/claim`+`/claim`(950–951)、`/runtimes/{runtimeId}/tasks/{taskId}/prepare-lease`(952)、`/tasks/{taskId}/status|start|wait-local-directory|progress|complete|fail|usage|messages|cancel-ack`(960–969)、`/runtimes/{runtimeId}/recover-orphans`(977)、`/tasks/{taskId}/session`(978) |
| company-ops（outcome/review/promotion） | 1209–1220 | 见上 1(e) |
| issue | 1231–1277 | `/api/issues` 集合(1232–1247)、`/api/issues/{id}` 详情(1249–1277)：`GET/PUT/DELETE`(1250–1252)、`/comments/trigger-preview`(1253)、`/active-task`(1260)、`/tasks/{taskId}/cancel`(1261)、`/rerun`(1262)、`/task-runs`(1263)、`/usage`(1264)、`/children`(1268)、`/metadata`(1272–1274)、`/properties`(1275–1276) |
| task messages（用户视角） | 1281–1284 | `/api/tasks/{taskId}/messages` |
| **project** | **1306–1317** | `/api/projects`：`/search`(1307)、`GET/POST /`(1308–1309)、`/{id}` 详情(1310–1317)：`GET/PUT/DELETE`(1311–1313)、`/resources`(1314–1317) |
| squad 评估 | 1338 | `/api/issues/{id}/squad-evaluated` |
| agent 任务取消/列表 | 1409–1410 | `/api/agents/{id}/cancel-tasks`、`/tasks` |
| 用户视角任务取消 | 1520 | `/api/tasks/{taskId}/cancel` |
| agent task 快照 | 1524 | `/api/agent-task-snapshot` |

---

## 2. 数据库真值（本轮只读实读）

容器：`multica-postgres-1`（pgvector/pgvector:pg17），库 `multica`。

### 2.1 关键表关键列（与 Go model 一一对应）

- **project**：`id, workspace_id, title, description, icon, status, lead_type, lead_id, created_at, updated_at, priority, start_date, due_date`。
  - Go model：`pkg/db/generated/models.go:960`。约束：`project_status_check`（5 值）、`project_priority_check`（5 值）、`project_lead_type_check`（member|agent）。
- **issue**：`id, workspace_id, title, description, status, priority, assignee_type, assignee_id, creator_type, creator_id, parent_issue_id, acceptance_criteria, context_refs, position, due_date, created_at, updated_at, number, project_id, origin_type, origin_id, first_executed_at, start_date, metadata, stage, properties`。
  - Go model：`models.go:738`。约束：`issue_status_check`（7 值）、`issue_priority_check`、`issue_assignee_type_check`（member|agent|squad）、`issue_creator_type_check`（member|agent）、`issue_origin_type_check`、`issue_stage_check`（NULL 或 ≥1）。
- **agent_task_queue**：`id, agent_id, issue_id, status, priority, dispatched_at, started_at, completed_at, result, error, created_at, context, runtime_id, session_id, work_dir, trigger_comment_id, chat_session_id, autopilot_run_id, attempt, max_attempts, parent_task_id, failure_reason, trigger_summary, force_fresh_session, is_leader_task, wait_reason, initiator_user_id, handoff_note, prepare_lease_expires_at, squad_id, runtime_mcp_overlay, escalation_for_task_id, fire_at, originator_user_id, runtime_connected_apps, coalesced_comment_ids, delivered_comment_ids, chat_input_task_id, chat_finalize_deferred_at, originator_source, delegated_from_task_id, retry_of_task_id, rerun_of_task_id, rule_version_id, trigger_evidence_kind, trigger_evidence_ref_id, accountable_user_id, session_rollout_missing, retired_session_id`。
  - Go model：`models.go:95`。约束：`agent_task_queue_status_check`（8 值）、`agent_task_queue_accountable_matches_originator`。
- **execution_receipt**：`task_id(PK), workspace_id, issue_id, assignment_command_id, work_order_*/employee_*/binding_*/agent_*(ref/revision/digest), input_digest, runtime_snapshot, runtime_digest, claimed_at, terminal_status(completed|failed|cancelled), completed_at, output_digest, result_snapshot, terminal_error, finalized_at, created_at`。
  - Go model：`models.go:582`。
- **artifact_candidate**：`id, workspace_id, lineage_id, revision, supersedes_id, storage_key, durable_object_ref, digest, filename, content_type, size_bytes, source_attachment_id, source_comment_id, idempotency_key, created_at`。append-only（trigger 禁止 UPDATE/DELETE）。Go model：`models.go:164`。
- **artifact_event**：`id, workspace_id, lineage_id, sequence, event_type, candidate_id, candidate_revision, candidate_digest, candidate_object_ref, formal_artifact_ref, idempotency_key, created_at`。append-only。Go model：`models.go:186`。
- **artifact_promotion_claim**：`workspace_id, promotion_id, candidate_id, lineage_id, created_at, payload_digest`。append-only。Go model：`models.go:216`。
- **assignment_dispatch_receipt**：`command_id(PK), workspace_id, issue_id, local_agent_id, initial_task_id, work_order_*/employee_*/binding_*/agent_*(ref/revision/digest), input_digest, created_at`。Go model：`models.go:225`。
- **external_work_order_link**：`workspace_id, work_order_ref(PK 成分), linked_revision, linked_digest, source_observed_at, freshness_at_link, issue_id, linked_at`。Go model：`models.go:612`。
- **comment**：`id, issue_id, author_type, author_id, content, type, created_at, updated_at, parent_id, workspace_id, resolved_at, resolved_by_type, resolved_by_id, source_task_id`。Go model：`models.go:518`。
- **project_resource**：`id, project_id, workspace_id, resource_type, resource_ref, label, position, created_at, created_by`。Go model：`models.go:976`。
- **squad**：`id, workspace_id, name, description, leader_id, creator_id, created_at, updated_at, archived_at, archived_by, avatar_url, instructions`。Go model：`models.go:1030`。
- **agent**：`id, workspace_id, name, avatar_url, runtime_mode, runtime_config, visibility, status, max_concurrent_tasks, owner_id, created_at, updated_at, description, runtime_id, instructions, archived_at, archived_by, custom_env, custom_args, mcp_config, model, thinking_level, composio_toolkit_allowlist, permission_mode, kind, system_key, disabled_runtime_skills, service_tier, operational_mode`。Go model：`models.go:24`。
- **member**：`id, workspace_id, user_id, role(owner|admin|member), created_at`。Go model：`models.go:922`。

### 2.2 枚举常量精确定位（Go 代码）

| 枚举 | Go 定义位置 | DB CHECK |
|---|---|---|
| **Project.status** | `server/internal/handler/project.go:204`：`var validProjectStatuses = []string{"planned","in_progress","paused","completed","cancelled"}` | `project_status_check`（迁移 034） |
| **Issue.status** | `server/internal/handler/issue.go:78`：`var validIssueStatuses = []string{"backlog","todo","in_progress","in_review","done","blocked","cancelled"}` | `issue_status_check`（迁移 001） |
| **Task.status** | 无 Go 常量（由 DB CHECK + `agent.sql` 各状态 query 承载）。nonterminal = `queued|dispatched|running|waiting_local_directory|deferred`；`deferred` 到点后由 `service/task.go:2925` `PromoteDueDeferredTasksForRuntime` 提升为 `queued`。`agent.sql:1362` `ListActiveTasksByIssue` 只覆盖 4 个 active 状态 `('queued','dispatched','running','waiting_local_directory')` | `agent_task_queue_status_check`：`queued|dispatched|running|completed|failed|cancelled|waiting_local_directory|deferred` |
| artifact event_type | `internal/companyops/artifact_lifecycle.go:121–128`（无 `rejected`；DB CHECK 有 `rejected`，见迁移 240） | `artifact_event.event_type` CHECK |

### 2.3 行数快照（本轮实读）

| 表 | 行数 |
|---|---:|
| project | 11（全部 `in_progress`） |
| issue | 553（`in_review` 304、`done` 98、`in_progress` 43、`todo` 39、`cancelled` 23、`backlog` 23、`blocked` 23） |
| agent_task_queue | 787（`completed` 614、`cancelled` 137、`failed` 35、`running` 1） |
| agent | 33 |
| agent_runtime | 105 |
| member | 1 |
| comment | 713 |
| project_resource | 7 |
| squad | 9 |
| squad_member | 51 |
| inbox_item | 1972 |

**0 行表（与生命周期/成果/回执相关，全部为空）**：`execution_receipt`、`artifact_candidate`、`artifact_event`、`artifact_materialization_intent`、`artifact_promotion_claim`、`assignment_dispatch_receipt`、`external_work_order_link`。
（另：`issue_label`、`issue_to_label`、`issue_property`、`issue_reaction`、`comment_reaction`、`agent_invocation_target`、`notification_preference`、`pinned_item`、`daemon_connection`、`daemon_token`、`feedback`、channel/lark/slack 绑定表、github 表等也均为 0 行 —— 完整清单见 psql `count(*)=0` 遍历，共 57 张。）

---

## 3. `ProjectLifecycleSnapshot` BFF/read-model 天然落点（只定位，不实现）

HIV-553 明确「不建第二套 Project/Issue/Task/Outcome 真源」→ 快照必须是**派生只读聚合**。

1. **Service（新增）**：`server/internal/service/project_lifecycle.go`（按 CHECKLIST write boundary 命名 `project_lifecycle*.go`）。
   - 构造式：只依赖 `db.Queries` + 只读查询，不 new TaskService 引擎、不写账本。
   - 输入：`workspace_id`（+ 可选 `project_id`）。
   - 输出：每 Project 的 `health(A–G 派生)、accountable_lead、frontier_issue、frontier_tasks、wip、nonterminal_issue_count、last_progress_at、next_action、expected_outcomes、outcome_coverage、closure_readiness`。
2. **Handler（新增）**：`server/internal/handler/project_lifecycle.go`，挂 `/api/projects/lifecycle` 或 `/api/projects/{id}/lifecycle-snapshot`，与现有 `project.go` 同包复用 `ProjectResponse` 风格 + `resolveWorkspaceID` + `writeJSON`。注册位置建议紧贴 `router.go:1306–1317` 的 `/api/projects` 块。
3. **SQL/query 层（复用，不新增表）**：
   - `pkg/db/queries/project.sql`：`ListProjects`(1)、`GetProjectInWorkspace`(12)、`GetProjectIssueStats`(61，注意其 `done_count` 只统计 `done|cancelled`)。
   - `pkg/db/queries/issue.sql`：`ListIssues`(1，`project_id` 过滤)、`ListOpenIssues`(208)、`CountIssues`(274)。
   - `pkg/db/queries/agent.sql`：`ListTasksByIssue`(1584，某 issue 全部 task)、`ListActiveTasksByIssue`(1362，nonterminal 集合)、`HasActiveTaskForIssue`(1089)、`CountRunningTasks`(1080)。
   - `pkg/db/queries/companyops.sql`：`ListCompanyOpsOutcomeRows`(100) + `CountCompanyOpsOutcomeRows`(246) —— **outcome coverage 的直接复用点**（已 JOIN issue.project_id、artifact 生命周期状态、rework_task_count）。
   - 若缺「按 project 聚合 nonterminal task」的单条查询，建议新增一条只读 `ListProjectTaskFrontier :many`（`agent_task_queue JOIN issue ON project_id`，`status IN (nonterminal)`），**纯 query，不建表**。

---

## 4. 与 project/issue/task 生命周期相关的关键迁移

| up.sql | 内容 |
|---|---|
| `001_init.up.sql` | 基础表：`issue`（含 `status` CHECK 7 值）、`agent_task_queue`（含 `status` CHECK）、`workspace`、`agent`、`comment`、`activity_log` 等 |
| `020_issue_number.up.sql` | `issue.number` + workspace 前缀（`HIV-xxx` identifier） |
| `022_task_lifecycle_guards.up.sql` | `idx_one_pending_task_per_issue`：同 issue 最多一个 queued/dispatched（**幂等护栏**） |
| `034_projects.up.sql` | **`project` 表**（status 5 值 CHECK、lead_type CHECK）+ `issue.project_id` FK（ON DELETE SET NULL） |
| `035_project_priority.up.sql` | `project.priority`（5 值 CHECK） |
| `037_fix_pending_task_unique_index.up.sql` | 修正 pending task 唯一索引 |
| `055_task_lease_and_retry.up.sql` | task `attempt/max_attempts/parent_task_id/failure_reason/last_heartbeat_at`（重试/租约） |
| `065_project_resources.up.sql` | **`project_resource` 表**（多态 resource_type + JSONB ref，UNIQUE(project,type,ref)） |
| `067_task_queue_claim_candidate_index.up.sql` | claim 候选索引 |
| `080_agent_task_queue_queued_index.up.sql` | queued 索引 |
| `091_issue_start_date.up.sql` | `issue.start_date` |
| `105_issue_metadata.up.sql` | `issue.metadata` JSONB（≤8KB） |
| `109_agent_task_waiting_local_directory.up.sql` | task `waiting_local_directory` 状态 + wait_reason |
| `112_issue_dates_to_date.up.sql` | issue 日期改 DATE（纯日历日） |
| `117/118_agent_task_queue_initiator_user_id*.up.sql` | `initiator_user_id`（117 加列，118 去 FK） |
| `120_comment_source_task_id.up.sql` | `comment.source_task_id`（评论→Task 溯源） |
| `122_task_handoff_note.up.sql` | task `handoff_note` |
| `123_issue_stage.up.sql` | `issue.stage`（阶段屏障，子议题 child-done 门） |
| `124_task_prepare_lease.up.sql` | task `prepare_lease_expires_at` |
| `127_task_squad_id.up.sql` | task `squad_id` |
| `128_agent_task_queue_runtime_mcp_overlay.up.sql` | task `runtime_mcp_overlay` + escalation |
| `129_agent_composio_allowlist_and_task_originator.up.sql` | task `originator` + 归属字段（MUL-4302 系列后续继续加列） |
| `166_project_dates.up.sql` | `project.start_date/due_date`（DATE） |
| `191_issue_properties.up.sql` | `issue_property` 定义表 + `issue.properties` JSONB |
| `235_companyops_execution_persistence_tables.up.sql` | **`external_work_order_link`、`assignment_dispatch_receipt`、`execution_receipt`**（append-only provenance，无 FK） |
| `236–239_*` | 上述三表唯一索引 + PK（`workspace_id+work_order_ref` / `command_id` / `task_id`） |
| `240_companyops_artifact_persistence.up.sql` | **`artifact_candidate`、`artifact_event`、`artifact_materialization_intent`** + 不可变 trigger |
| `241–244_*` | artifact_candidate 索引/PK/lineage-revision/idempotency |
| `251_artifact_promotion_claim.up.sql` | **`artifact_promotion_claim`** + 不可变 trigger |
| `252–254_*` | promotion claim `payload_digest` + 校验 |

---

## 5. 可运行的构建/测试命令

**Go（在 `server/` 目录）**
- 全量测试（带 race，走 Makefile，自动 ensure DB + migrate）：`make test`（等价 `bash scripts/test-go.sh --race`）。
- **单包测试**（最小可运行）：
  - `cd server && go test ./internal/handler -run 'TestListProjects|TestUpdateProject' -count=1`
  - `cd server && go test ./internal/service -run TestTask -count=1`（service 包需要 DB 时按 Makefile 先 `go run ./cmd/migrate up` 并确保 `DATABASE_URL`/`.env` 已加载；纯逻辑包可直接跑）
  - `cd server && go test ./internal/companyops/... -count=1`
  - `cd server && go test ./internal/service -run 'TestReviewArtifact|TestArtifact' -count=1`
- 构建：`cd server && go build ./cmd/server`；CLI `go build ./cmd/multica`；migrate `go build ./cmd/migrate`。
- 迁移：`cd server && go run ./cmd/migrate up`（需 env 已加载）。
- sqlc 再生成：`cd server && sqlc generate`。

**后端 dev 启动**
- 一键：`make dev`（`scripts/dev.sh`：ensure-postgres → `cd server && go run ./cmd/migrate up` → `(cd server && go run ./cmd/server) &` + `pnpm dev:web &`）。
- 只起后端：`make server`（`scripts/ensure-postgres.sh` + `cd server && go run ./cmd/server`）。
- Docker（当前运行形态）：`docker compose` project `multica`，容器 `multica-backend-1`（8080）、`multica-frontend-1`（3000）、`multica-postgres-1`。

**pnpm（根目录，workspace + turbo）**
- `packages/views` typecheck：`pnpm --filter @multica/views typecheck`（= `tsc --noEmit`）。
- `packages/views` 测试：`pnpm --filter @multica/views test`（= `vitest run`）。
- 全仓库 typecheck/test：`pnpm typecheck` / `pnpm test`（turbo，`--filter=!@multica/mobile`）。
- 前端 dev：`pnpm dev:web`（`turbo dev --filter=@multica/web` → `next dev --webpack --port ${FRONTEND_PORT:-3000}`）。

---

## 6. 合同 vs 代码 绑定缺口（给 Prime 的实操提示）

1. **Project `closed`/`superseded` 无处落**：DB 只有 `completed|cancelled`。关闭/归并须映射到 `completed`（或 `cancelled(reason)`）+ 不可变 receipt，而非新增状态；`superseded` 只能作为 health/read-model 派生结论（E）。
2. **Issue disposition 分类无处落**：`superseded_by`/`migrated_to`/`waived(reason)` 在 schema 里没有字段。落点候选：`issue.metadata`（JSONB，≤8KB）或新 `comment`/receipt；**禁止**新增 Issue 状态枚举值（会破坏 `validIssueStatuses` 与 CHECK）。
3. **`last_progress_at` 现无字段**：contract 定义取「最近成功 Task.completed_at + Outcome decision_at + lifecycle receipt 时间」，DB 只有 `issue.updated_at`/`project.updated_at`/`agent_task_queue.completed_at`——需要在派生读模型里聚合，不得用 `project.updated_at` 冒充。
4. **`in_review` 无独立 Review Task**：Slice 3 必须新增 review Task 生成器（可参考 `ReviewArtifact` 的 `changes_requested→artifact_revision` repair 模板）。
5. **reviewer!=implementer 本地未强制**：需在 HiveCrew 服务层显式加 guard（现有唯一强制在 HiveCosm authority 的 `OwnerReview.ReviewerID`）。
6. **outcome 6 账本表空**：`external_work_order_link/assignment_dispatch_receipt/execution_receipt` 的写入只发生在 companyops WorkOrder 派发路径（`CreateCompanyOpsAssignment`），普通 Project/Issue/Task 的完成**不写** `execution_receipt`——Slice 4 要决定是否把普通任务完成也接入 receipt 或仅靠 task 行本身作为 receipt。
7. **`GetProjectIssueStats.done_count` 语义**：只算 `done|cancelled`，`blocked` 不计；contract 的「terminal disposition」若包含更多类别需新查询。

---

## 附录：只读校验证据

- 代码校验：`git -C <root> status --porcelain` = 空；`git -C <root> log -1` = `f7667c8d7 feat(bases): add drain/resume control (operational_mode gate + UI)`。
- DB 校验（全部只读 SELECT / information_schema / pg_constraint，无任何写语句）：
  - 行数：project=11（11×`in_progress`）；issue=553；agent_task_queue=787（running=1）。
  - 0 行账本表：`execution_receipt, artifact_candidate, artifact_event, artifact_materialization_intent, artifact_promotion_claim, assignment_dispatch_receipt, external_work_order_link` 全 0。
  - CHECK 约束：`project_status_check`、`issue_status_check`、`agent_task_queue_status_check` 与 Go 枚举一致（artifact_event 的 `rejected` 为 DB-only 遗留值）。

（完）
