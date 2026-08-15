# W0 前端考古报告 · 项目页面与 Owner 旅程差距

> **身份**：HiveCrew 开发团队一层 worker `arch-project-owner-journey`（项目页面与 Owner 旅程考古员）。
> **职责**：只读考古前端源码并核对运行中 `:3000` 页面，产出「当前 UX 与 HIV-553 合同 Slice 1 要求」的差距报告。
> **读写权限边界**：对源码仓库与数据库只读，不 checkout、不 commit、不修改任何源码；只写本报告这一个文件。
> **禁止递归**：不派发任何子 Agent（不调用 rlm / 子任务）。
> **审计时点**：2026-08-13（UTC 12:59+）。
> **真源基线**：源码 `/Volumes/HiveData/hivecosm/HQ-50-代码仓库/01-源码下载/multica` @ main `f7667c8d`（`git status --porcelain` 为空）；UI `http://127.0.0.1:3000`（multica-frontend-1）；API `http://127.0.0.1:8080`（multica-backend-1）；DB `multica-postgres-1`。

---

## 0. 执行摘要（一句话结论）

前端已具备完整的 Projects 列表/详情、Issues 列表/详情/五种视图、Inbox、Outcome Center（候选/正式成果 + 审核/晋升动作）等基础交互骨架；但**完全没有** HIV-553 合同 Slice 1 所要求的「项目健康分类（正在推进/待审核返修/已阻塞/已停滞/待关闭）」与「项目卡上的 frontier / 实际 worker / last receipt / next action / outcome coverage」字段，也**没有任何项目级 continue / resume / pause-dispatch / close / generate-closure-package 入口**。当前 11 个 Project 全部 `status=in_progress`，前端唯一能显示的「分类」是 5 值 `ProjectStatus` 枚举徽章，因此全部 11 个项目都会原样显示为「In Progress / 进行中」——与合同的「诚实分类」要求直接冲突。

---

## 1. 前端 source map（路径 + 职责）

### 1.1 路由层 `apps/web/app/[workspaceSlug]/(dashboard)/**`

全部为 App Router `"use client"` 薄壳页面，只做两件事：把 `[workspaceSlug]` 与 `[id]` 参数解出来，转发给 `packages/views` 的组件。

| 路由文件 | 行号 | 渲染内容 |
|---|---|---|
| `apps/web/app/[workspaceSlug]/(dashboard)/projects/page.tsx` | 1–8 | `<ProjectsPage />`（列表/卡片工作台） |
| `apps/web/app/[workspaceSlug]/(dashboard)/projects/[id]/page.tsx` | 1–15 | `<ProjectDetail projectId={id} />` |
| `apps/web/app/[workspaceSlug]/(dashboard)/outcomes/page.tsx` | 1–7 | `<OutcomesPage />` |
| `apps/web/app/[workspaceSlug]/(dashboard)/inbox/page.tsx` | 1–2 | `export { InboxPage as default }` |
| `apps/web/app/[workspaceSlug]/(dashboard)/issues/page.tsx` | 1–15 | `<IssuesPage />`（包 ErrorBoundary） |
| `apps/web/app/[workspaceSlug]/(dashboard)/issues/[id]/page.tsx` | 1–18 | `<IssueDetail issueId={id} />` |
| `apps/web/app/[workspaceSlug]/(dashboard)/my-issues/page.tsx` | — | 我的议题视图 |

侧边栏导航在 `packages/views/layout/app-sidebar.tsx`：personal nav = inbox/chat/my-issues（行 145–149），workspace nav = issues/projects/outcomes/organization/autopilots/agents/squads/bases/usage（行 151–161），configure nav = runtimes/skills/settings（行 163 起）。

> 注意：dashboard 下**不存在** `/review`、`/history` 路由（完整路由清单见 §6.2）。审核发生在 Issue 的 `in_review` 状态与 Outcome Center 的 artifact review/promotion 里，没有独立 Review 页面。

### 1.2 `packages/views/projects/**`（项目页面核心）

| 文件 | 职责 |
|---|---|
| `components/index.ts` | 导出 `ProjectsPage / ProjectDetail / ProjectPicker / ProjectChip / LocalDirectoryHint` |
| `components/projects-page.tsx`（49 KB） | 项目列表工作台：紧凑表格 + 舒适卡片双视图、搜索、筛选（status/priority/lead）、排序、列显隐、批量 pin/delete、新建项目入口 |
| `components/project-detail.tsx`（26 KB） | 项目详情：右栏属性（status/priority/lead/起止日期）、进度条（done/issue）、描述编辑器、资源区；主栏是 `IssueSurface`（board/list/table/swimlane/gantt 五视图） |
| `components/project-badge.tsx` | `ProjectStatusBadge`（行 21）、`ProjectPriorityBadge`：可点击下拉直接改 status/priority |
| `components/project-lead-picker.tsx` | `ProjectLeadPicker`（行 15）：成员/Agent 双选负责人（member/agent），可置空 |
| `components/labels.ts` | status/priority 的 i18n 标签 hook + 相对日期 |
| `components/project-issue-metrics.ts` | `getProjectIssueMetrics`：只返回 `{totalCount=issue_count, completedCount=done_count}`（行 3–9） |
| `components/project-resources-section.tsx` | 项目资源（github_repo / local_directory）列表 |
| `components/project-picker.tsx` / `project-chip.tsx` / `project-icon.tsx` | 项目选择器、chip、图标 |
| `components/local-directory-hint.tsx` | 本地目录资源提示 |
| `components/project-start-date-picker.tsx` / `project-due-date-picker.tsx` | 起止日期选择器 |

### 1.3 `packages/views/outcomes/**`（Outcome Center）

| 文件 | 职责 |
|---|---|
| `index.ts` | 导出页面/列表/详情/session-gate/动作 |
| `outcomes-page.tsx` | `OutcomesPage`（行 39）：桌面双栏（列表+详情）、移动单栏；URL 参数 `outcome / q / status / session_id` |
| `outcome-list.tsx` | `OutcomeList`（行 60）：搜索 + 状态筛选（execution_state 与 artifact status 两个维度）+ 列表项（issue 标题、command id、状态徽章、formal 徽章） |
| `outcome-detail.tsx` | 详情：execution_state / candidate artifact / formal 状态、issue/project 回链、candidate 预览、版本与事件、session gate + 审核/晋升动作 |
| `outcome-session-gate.tsx` | 审核动作门：先选 session，再 `request_rework`（changes_requested）/ `approve` / `promote` / `reread` 按钮（行 177–212） |
| `outcome-actions.ts` | `useOutcomeActions`（行 128）：`onReviewArtifact` / `onPromoteArtifact`，浏览器端稳定 UUID 作幂等键，收 receipt 后校验回显一致 |
| `outcome-queries.ts` | `outcomesListOptions`（行 26 → `api.listCompanyOpsOutcomes`）、`outcomeDetailOptions`（行 35 → `api.getCompanyOpsOutcome`） |
| `use-outcomes-compact.ts` | 响应式单/双栏切换 |

### 1.4 `packages/views/inbox/**`

| 文件 | 职责 |
|---|---|
| `components/inbox-page.tsx` | `InboxPage`（行 67）：收件箱/归档双视图（URL `view=archived`、`issue=<id>`），选中项内嵌 `IssueDetail`，批量已读/归档 |
| `components/inbox-list.tsx` / `inbox-list-item.tsx` / `inbox-display.ts` / `inbox-view.ts` / `inbox-detail-label.tsx` | 列表、项、标题/类型显示、视图枚举、标签 |

### 1.5 `packages/views/issues/**`（Issues 页面与项目内嵌 issue 面板）

| 文件 | 职责 |
|---|---|
| `components/issues-page.tsx` | `IssuesPage`：工作区级 `IssueSurface`（board/list/table/swimlane） |
| `components/issue-detail.tsx`（122 KB） | 议题详情：右栏 StatusPicker/StagePicker/AssigneePicker/PriorityPicker/日期/标签；动作菜单 `IssueActionsDropdown`；执行日志；评论/回复；子议题树 |
| `surface/issue-surface.tsx` | `IssueSurface`（行 51）：统一议题面板（项目详情与 workspace 共用），board/list/table/swimlane/gantt 五模式 |
| `surface/use-issue-surface-data.ts` | 面板数据装配：issue 列表/分组/子进度/项目 map |
| `actions/*` | `use-issue-actions.ts`、`issue-actions-menu-items.tsx`（状态/优先级/负责人/子议题/删除/复制链接/工作目录路径）、`issue-actions-dropdown.tsx`、`issue-actions-context-menu.tsx` |
| `components/terminate-task-confirm-dialog.tsx` | 终止单个 agent Task 的二次确认（stop-current 的 UI 载体） |
| `components/execution-log-section.tsx` | 执行日志，行 386 调 `api.rerunIssue`；含 cancel/rerun 行内动作 |
| `components/comment-card.tsx` | 行 278 调 `api.rerunIssue`（任务重跑） |
| `hooks/use-issue-trigger-preview.ts` | 行 71 调 `api.previewIssueTrigger`（派发预览，无副作用） |
| `components/pickers/assignee-picker.tsx` 等 | 负责人/状态/阶段/优先级/日期/标签/属性选择器 |

### 1.6 `packages/core/**`（数据层，详见 §4）

- `packages/core/projects/{queries,mutations,config,resource-queries,stores/view-store}.ts`
- `packages/core/api/client.ts`（147 KB，唯一 API 客户端）+ `schemas.ts`
- `packages/core/types/project.ts`、`types/issue.ts`、`types/companyops.ts`
- `packages/core/issues/{queries,config,surface/query-plan,surface/repository}.ts`

---

## 2. 当前 Owner 在 :3000 看到的真实旅程

> 运行态核实：`GET /` 与 `/login` 均返回 200 的 Next.js 客户端壳 HTML（无凭据时 SSR 不含业务内容，业务数据全部由浏览器端 react-query 拉取）；`GET /api/health` = `{"status":"ok"}`；`GET /api/projects`（无凭据）= `401 {"error":"missing authorization"}`。因此「真实旅程」按源码组件树 + 已核实运行态端点还原。

### 2.1 登录 → 工作区 → Projects 列表

1. `/login`（邮箱验证码 / Google）→ 选择/创建 workspace → 进入 `/{workspaceSlug}/issues`（`paths.root()` 指向 issues，`packages/core/paths/paths.ts:20`）。
2. 侧边栏点 **Projects** → `/{workspaceSlug}/projects` → `ProjectsPage`。
3. 页面今天**已经展示**：
   - 标题栏 + 数量 + 「New project」按钮（`projects-page.tsx` 行 ~790–800）。
   - 工具栏：搜索、筛选（status 5 值 / priority 5 值 / lead）、排序（name/priority/status/progress/created）、紧凑表格列显隐、视图切换（table/cards）。
   - 紧凑表格列：名称、**状态徽章**（可下拉直接改）、优先级、**进度环**（`done_count/issue_count`）、**负责人**（`ProjectLeadPicker`，可改）、议题数、创建时间、行操作菜单（pin/unpin、delete，admin-only）。
   - 卡片视图：标题/图标、状态徽章、进度环、负责人、优先级、创建时间。
   - 批量：pin 选中、删除选中（admin-only）；空态/加载骨架。
4. 页面今天**缺少**：任何健康分类（无「正在推进/待审核返修/已阻塞/已停滞/待关闭」分组或标签）、frontier Issue/Task、实际执行 worker、last receipt、next action、outcome coverage、closed/history 视图。

### 2.2 项目详情

1. 点击项目行/卡 → `/{workspaceSlug}/projects/{id}` → `ProjectDetail`。
2. 已经展示：
   - 右栏属性（可编辑）：status（5 值下拉）、priority、lead（member/agent，可置空）、start date、due date；进度条（`completed/total` 议题数）；描述（Markdown）；项目资源（github_repo/local_directory）。
   - 主栏 `IssueSurface`：`modes={["board","list","table","swimlane","gantt"]}`（`project-detail.tsx:545–547`），即该项目的全部议题按五种视图展开。
3. 缺少：项目级 health/frontier/next action/outcome matrix/关闭门；任何项目级 continue/resume/pause/close 动作（只有 pin、复制链接、删除，`project-detail.tsx` 头部动作区）。

### 2.3 Issues（列表与详情）

1. `/{workspaceSlug}/issues` → `IssuesPage` → 工作区级 `IssueSurface`（board/list/table/swimlane）。
2. `/{workspaceSlug}/issues/{id}` → `IssueDetail`：状态（7 值 backlog/todo/in_progress/**in_review**/done/blocked/cancelled）、阶段、负责人（member/agent/squad）、优先级、起止日期、标签、子议题、评论、执行日志。
3. 已经具备的「派发/终止」入口（议题级）：给 agent/squad 指派会弹 `issue-run-confirm` 模态（先 `previewIssueTrigger` 再确认派发）；执行日志与评论里有 `cancelTask`（终止 Task）与 `rerunIssue`（重跑）。
4. 缺少：议题上的 continue/resume/pause-dispatch 语义化动作、reviewer≠implementer 的显式独立审核 Task 展示（in_review 只是状态列，不强制显示 reviewer/证据）。

### 2.4 Outcomes（Outcome Center）

1. `/{workspaceSlug}/outcomes` → `OutcomesPage`：左侧列表（search + status 筛选）、右侧详情。
2. 已经展示：execution_state（awaiting_claim/running/completed/failed/cancelled）、active artifact 状态（submitted/changes_requested/approved/promotion_requested/promotion_succeeded/promotion_failed/authority_readback_confirmed）、formal 徽章、candidate 预览、版本/事件/run 历史、issue/project 回链。
3. 已经具备的动作：`request_rework`(changes_requested) / `approve` / `promote` / `reread`（`outcome-session-gate.tsx:177–212`）。
4. 缺少：与 Project 的 expected-outcomes / coverage 矩阵联动；项目页看不到 Outcome Center 的覆盖情况（两个页面各自独立，没有 outcome coverage 投影回项目）。

### 2.5 Inbox

`/{workspaceSlug}/inbox` → `InboxPage`：收件箱/归档双视图、批量已读/归档、内嵌议题详情。与 Slice 1 关系较弱，属既有能力。

---

## 3. 对照 HIV-553 合同 Slice 1 的逐条差距

Slice 1 原文要求（`prime-goal.md` WAVE-2）：
> 后端只读 Project health/portfolio API；Projects 页面分为「正在推进、待审核/返修、已阻塞、已停滞、待关闭」；项目卡显示负责人、frontier、实际 worker、last receipt、next action、outcome coverage；现有 11 个项目均有诚实分类。

### 3.1 Projects 是否按「正在推进 / 待审核返修 / 已阻塞 / 已停滞 / 待关闭」分类？当前用什么字段分类？

**否。完全没有健康分类。**

- 当前唯一可用的分类字段是 `Project.status`，其类型为 5 值枚举：
  `packages/core/types/project.ts:1` → `"planned" | "in_progress" | "paused" | "completed" | "cancelled"`；
  `packages/core/projects/config.ts:3` `PROJECT_STATUS_ORDER` 同 5 值。
- 列表页筛选用同一个枚举：`projects-page.tsx:659` `STATUS_VALUES`、`projects-page.tsx:120` `STATUS_ORDER`（排序）。
- DB 层 `project` 表有 CHECK 约束 `status = ANY(planned,in_progress,paused,completed,cancelled)`，**没有** health/frontier/last_progress_at/outcome_coverage/closure_readiness 任何列（`\d project` 已核实，列见 §6.3）。
- 合同 A–G 健康分类（active_with_frontier / stalled_no_open_task / review_or_repair_blocked / ready_for_closure / duplicate_or_superseded / owner_decision_required / source_gap）在前端**零命中**：全仓 `rg frontier|next_action|outcome_coverage|pause_dispatch|generate_closure` 在 packages/apps 下无任何业务命中（仅 closure 命中为代码闭包注释，与项目生命周期无关）。

### 3.2 项目卡是否显示 lead / frontier Issue / Task / 实际 worker / last receipt / next action / outcome coverage？

| 字段 | 现状 | 证据 |
|---|---|---|
| 负责人 lead | ✅ 有（可编辑 member/agent，可空） | `ProjectLeadPicker`（`project-lead-picker.tsx:15`）；`Project.lead_type/lead_id`（`types/project.ts:13-14`） |
| frontier Issue/Task | ❌ 无 | `Project` 类型无此字段（`types/project.ts:5-24`） |
| 实际 worker | ❌ 无 | 无任何字段/组件 |
| last receipt / last_progress_at | ❌ 无（只有 `created_at` 相对日期） | `Project` 只有 `created_at/updated_at`；卡片显示 `created_at`（`projects-page.tsx` `formatRelativeDate(project.created_at)`） |
| next action | ❌ 无 | 无字段/组件 |
| outcome coverage | ❌ 无 | 无字段/组件；Outcome Center 是独立页面，不回投到项目卡 |

「进度」目前用 `done_count/issue_count` 议题计数（`project-issue-metrics.ts`），这是合同明令不得冒充成果验收的 `done_count` 代理（合同负向测试：「不以 done_count 代替成果验收」）。

### 3.3 11 个项目是否被诚实分类（而不是全部「进行中」）？

**否，且当前架构做不到。**

- DB 实读：`SELECT status, count(*) FROM project GROUP BY status` → `in_progress = 11`（唯一一行）。
- 前端没有 health 字段，唯一呈现方式就是 `ProjectStatusBadge` 把 `in_progress` 显示成「In Progress」徽章。因此 11 个项目今天全部显示为「进行中」。
- HIV-553 合同已判定这 11 个的真实分类为 A=1 / B=2 / C=5 / E=1 / G=2（含两个 F 决策门 + 两个无 lead + 两个全 Issue terminal 但未关闭），当前 UI **无法表达**其中任何一个非 `in_progress` 真相（stalled / review-blocked / duplicate / source_gap 均无 UI）。

### 3.4 其他与 Slice 1 直接相关的差距小结

- 无「Project health/portfolio 只读 API」的前端消费方（client.ts 无对应方法；后端是否已有由兄弟子 Agent 的 code/data 考古报告覆盖）。
- 无分组的 Projects 工作台（列表是单一平铺，无「五个桶」分区/标签页/分组）。实现 Slice 1 需要：新增 health 派生字段 + portfolio 查询 + 分组 UI + 卡片字段扩展。

---

## 4. 数据获取层（前端如何调用后端）

### 4.1 传输与客户端

- 全站统一走 `packages/core/api/client.ts` 的 `ApiClient`（147 KB，单例 `api`）。
- 每个请求带 `Authorization: Bearer <token>`、`X-Workspace-Slug`、`X-CSRF-Token`（从 cookie `multica_csrf`）、`X-Request-ID`、`X-Client-Platform/Version/OS`（`client.ts` 的 `authHeaders()` / `fetchRaw()`）；`credentials: "include"`。
- 401 时清 token 并触发登录跳转（`handleUnauthorized`）。
- 响应走 zod 解析 + `parseWithFallback`（`schemas.ts`），解析失败返回安全空值并记日志。

### 4.2 状态层

- 数据获取统一 **TanStack React Query（@tanstack/react-query）**，不是裸 fetch。列表/详情用 `queryOptions(...)` 暴露 `useQuery`；变更用 `useMutation` + 乐观更新 + `invalidateQueries`。

### 4.3 Projects 相关 query/hook 文件与接口路径

| 文件 | 导出 | 调用的 API | 接口路径 |
|---|---|---|---|
| `packages/core/projects/queries.ts` | `projectListOptions`（行 14）、`projectDetailOptions`（行 22） | `api.listProjects()` / `api.getProject(id)` | `GET /api/projects`、`GET /api/projects/{id}` |
| `packages/core/projects/mutations.ts` | `useCreateProject` / `useUpdateProject` / `useDeleteProject` | `api.createProject/updateProject/deleteProject` | `POST /api/projects`、`PUT /api/projects/{id}`、`DELETE /api/projects/{id}` |
| `packages/core/projects/resource-queries.ts` | `projectResourcesOptions` + resource CRUD | `api.listProjectResources/...` | `GET|POST /api/projects/{projectId}/resources`、`PUT|DELETE /api/projects/{projectId}/resources/{resourceId}` |
| `packages/core/projects/stores/view-store.ts` | `useProjectViewStore`（viewMode/sort/filters/columns） | 纯客户端 UI 状态 | — |
| `packages/core/api/client.ts:3364-3425` | 上述所有项目方法 | — | — |

### 4.4 Issues / 项目内议题面板的 query 文件与接口

| 文件 | 导出/用途 | 接口路径 |
|---|---|---|
| `packages/core/issues/queries.ts` | `issueListOptions`（工作区状态桶）、`myIssueListOptions`、`issueFlatListOptions`（表格分页）、`issueTableGroupsOptions`（表分组 cursor 分页）、`projectGanttIssuesOptions`（项目甘特全量拉取，`PROJECT_GANTT_PAGE_LIMIT=500`）、`issueDetailOptions`、`issueTimelineOptions` 等 | `GET /api/issues`、`POST /api/issues/query`、`GET /api/issues/grouped`、`POST /api/issues/table/groups|rows|facets`、`GET /api/issues/{id}`、`GET /api/issues/{id}/timeline` |
| `packages/core/issues/surface/repository.ts` | 把 `IssueSurfaceQueryPlan` 解析到上述具体 options（视图层不直接分叉端点） | — |
| `packages/views/issues/surface/use-issue-surface-data.ts` | 面板数据装配（过滤/排序/子进度/项目 map） | — |
| `packages/views/issues/hooks/use-issue-trigger-preview.ts` | 派发预览 | `POST /api/issues/preview-trigger`（`client.ts:1700`） |

### 4.5 Outcome / Inbox 相关接口

| 用途 | 接口路径 | client.ts 行 |
|---|---|---|
| Outcome 列表 / 详情 | `GET /api/company-ops/outcomes`、`GET /api/company-ops/outcomes/{commandId}` | 3019 / 3046 |
| 派工（assignment dispatch） | `POST /api/company-ops/assignments` | 2958 |
| 成果审核 | `POST /api/company-ops/artifact-reviews` | 2977 |
| 成果晋升（formal promotion） | `POST /api/company-ops/formal-artifact-promotions` | 2996 |
| 组织 / 员工 / 员工档案 | `GET /api/company-ops/organization`、`/employees`、`/employees/{id}` | 3068 / 3081 / 3102 |
| Inbox | `GET /api/inbox`、`/api/inbox/archived`、`/api/inbox/{id}/read|archive|unarchive`、`/api/inbox/unread-count|unread-summary`、`mark-all-read`、`archive-all*` | — |

> 结论：**没有任何** `/api/projects/.../continue|resume|pause|close|closure-package` 或等价 health/portfolio 端点被前端调用；`client.ts` 的 Project 方法只有 CRUD + resources + search。

---

## 5. Owner 动作入口现状（continue / resume / pause / close）

### 5.1 项目级动作 —— 现状几乎为空

| 合同要求（Slice 2 / VC-03） | 现状 | 证据 |
|---|---|---|
| continue（继续） | ❌ 无 | 全仓无此入口 |
| resume（恢复） | ❌ 无 | 无入口 |
| pause-dispatch（暂停派发） | ❌ 无 | `pause_dispatch` 前端 0 命中 |
| close / close-preview（关闭/预览） | ❌ 无 | `close_project`、`generate_closure` 前端 0 命中 |
| generate-closure-package（生成成果包） | ❌ 无 | 前端 0 命中 |

项目上**唯一**的「动作」是：status 5 值下拉直写（`ProjectStatusBadge`，`project-badge.tsx:21`）、priority 下拉、lead 选择器、起止日期、pin/unpin、复制链接、删除（admin）。其中 status 下拉是把 `in_progress→paused/completed/cancelled` **直接 PUT**，没有任何 `preview → confirm → receipt`、乐观版本号、幂等键、关闭门校验——与合同「Project 页面动作合同」的全部要求（preview + 精确 target + receipt + idempotency）不符。

### 5.2 议题级动作 —— 已有部分「派发/终止/重跑」，但没有 continue/resume/pause 语义

| 能力 | 现状 | 证据 |
|---|---|---|
| 派发预览（preview-trigger） | ✅ 有 | `issue-run-confirm` 模态 + `previewIssueTrigger`（`client.ts:1700`；`use-issue-actions.ts:84` 对 agent/squad 指派先弹预览） |
| 终止当前 Task（stop-current） | ✅ 有（议题级） | `terminate-task-confirm-dialog.tsx` + `api.cancelTask` → `POST /api/issues/{issueId}/tasks/{taskId}/cancel`（`client.ts:2588`） |
| 重跑（rerun） | ✅ 有（议题级） | `api.rerunIssue` → `POST /api/issues/{issueId}/rerun`（`client.ts:2594`；`execution-log-section.tsx:386`、`comment-card.tsx:278`） |
| 状态改为 in_review / blocked | ✅ 有（议题状态） | `StatusPicker` 7 值（`issues/config/status.ts:13`） |
| 项目级 continue/resume/pause-dispatch | ❌ 无 | 无任何入口 |

### 5.3 Outcome Center 动作 —— 已具备 artifact 级审核/晋升（部分满足 Slice 3/4 的原子能力）

- `request_rework`（changes_requested）、`approve`、`promote`、`reread`（`outcome-session-gate.tsx:177-212`），幂等键为浏览器端稳定 UUID + 服务器 receipt 校验（`outcome-actions.ts`）。
- 但这是**单条 Outcome/artifact** 维度的动作，未上卷为 Project 的 expected-outcomes / coverage / closure-readiness 视图，也不是项目关闭动作。

---

## 6. 证据与核实记录

### 6.1 运行态核实（本次实读）

```
curl -i http://127.0.0.1:3000/            -> 200, Next.js 客户端壳（无凭据 SSR 无业务内容）
curl -i http://127.0.0.1:3000/login       -> 200, 同上
curl -i http://127.0.0.1:8080/health      -> 200 {"status":"ok"}
curl -i http://127.0.0.1:8080/api/projects -> 401 {"error":"missing authorization"}
git -C <src> status --porcelain           -> （空，工作树干净）
git -C <src> rev-parse HEAD               -> f7667c8d7c540217c345d98beac33794e1f3e6d0
```

### 6.2 dashboard 路由全清单（`apps/web/app/[workspaceSlug]/(dashboard)/`）

agents(/new,/\[id\]), autopilots(/\[id\]), bases, billing, chat, inbox, issues(/\[id\]), members/\[id\], my-issues, organization(/employees/\[employeeId\]), outcomes, projects(/\[id\]), runtimes(/\[id\]/runtime/\[runtimeId\]), settings, skills(/\[id\]), squads(/\[id\]), usage。
**无** `/review`、`/history`、`/projects/{id}/close` 等路由。

### 6.3 DB `project` 表列（`\d project`，只读核实）

`id, workspace_id, title, description, icon, status, lead_type, lead_id, created_at, updated_at, priority, start_date, due_date`；
CHECK：`status IN (planned,in_progress,paused,completed,cancelled)`、`lead_type IN (member,agent)`、`priority IN (urgent,high,medium,low,none)`。
**无** health / frontier / last_progress_at / next_action / outcome_coverage / closure_readiness 列。
`SELECT status, count(*) FROM project GROUP BY status` → `in_progress | 11`。

### 6.4 关键源码行号索引（均已核实）

见 §1–§5 各处行号；最关键的：
- 类型无 health 字段：`packages/core/types/project.ts:1,5-24`
- 列表筛选仅 5 值 status：`packages/views/projects/components/projects-page.tsx:120,659`
- 卡片/表格列只有 name/status/priority/progress/lead/issues/created：`projects-page.tsx:346,458,560`
- 项目详情主栏只有 IssueSurface，右栏只有 5 值 status + priority + lead + 日期 + 进度 + 描述 + 资源：`project-detail.tsx:100,545-547`
- 项目 API 只有 CRUD + resources + search：`packages/core/api/client.ts:3364-3425`
- 无 project continue/resume/pause/close 端点或组件：全仓 `rg` 0 命中（见 §3.1/§5.1）

---

## 7. 对 Prime / 后续 Slice 的考古结论（可直接采信）

1. **Slice 1 是净新增**：前端没有任何 health/portfolio 读模型消费方，也没有「五桶分类」或 frontier/next_action/outcome_coverage 组件。写 Slice 1 需要在 `packages/core/projects`（queries/types）加 health 字段与 portfolio 查询、在 `packages/views/projects/components`（projects-page/project-detail/badge/card）加分类 UI 与卡片字段，并在 `apps/web/app/[workspaceSlug]/(dashboard)/projects` 路由下复用现有壳。
2. **复用点**：`ProjectLeadPicker`（lead 已可编辑）、`IssueSurface`（项目内议题五视图）、`ProjectStatusBadge`（可改造成 health 徽章）、Outcome Center 的 `outcome-actions`（审核/晋升幂等 receipt 模式可借鉴给项目动作）、`previewIssueTrigger`（派发预览模式可借鉴给 continue preview）。
3. **必须纠正的现状**：项目 status 5 值下拉直写 `PUT /api/projects/{id}` 是「裸状态写」，会被合同负向测试「in_progress 无 Task 仍显示执行中」命中——Slice 1 的诚实分类 UI 不能继续用该枚举表达执行真相，必须用派生 health（A–G）覆盖展示。
4. **Outcome coverage 是断裂点**：Outcome Center 已有独立数据（`/api/company-ops/outcomes` 支持 `project_id` 过滤参数，`client.ts:3019` 的 `listCompanyOpsOutcomes(params)` 已声明 `project_id` 查询参数），但项目页未消费它；Slice 4 可在项目卡/详情直接复用该查询按 `project_id` 聚合 coverage，无需新建第二真源。
5. **边界提醒**：本报告只考古前端与运行态；后端 health 投影 API、Project status enum 的 Stage 2 回读绑定、迁移矩阵由兄弟子 Agent（arch-code-data-contract / arch-test-concurrency-matrix）的对应报告覆盖，整合时以 Prime 为准。

（本报告未打印、复制或传递任何 secret；所有 DB/API 访问均为只读；未 checkout、未 commit、未创建除本文件外的任何文件；未派发任何子 Agent。）
