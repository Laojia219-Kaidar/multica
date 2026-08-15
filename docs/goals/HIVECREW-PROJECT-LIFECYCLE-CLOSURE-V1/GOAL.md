/goal
Goal ID: HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1
Version: 1.0
Title: HiveCrew 项目持续经营、自动续办、审核返修与成果归结闭环
Owner: William
Accountable orchestrator/integrator: Prime Agent
Execution projection: HiveCrew Project 3b0330e7-a2da-4f41-94ab-61c911af2820
Parent Issue: HIV-548 / 10b279d3-5cc2-4a76-8d2c-329e39d270fb
Current contract Issue: HIV-553 / f6fcfff0-dfa3-4519-a44b-e153bd8a7e8b

## 使命 / Mission

把 HiveCrew 从“Project、Issue、Task、审核、成果相互断开且长期挂起”的工作台，升级为能持续经营项目、自动恢复停滞、自动衔接审核与返修、形成可追溯成果并关闭项目的自我进化操作台。

最终用户必须能在当前 HiveCrew 页面看到：每个项目谁负责、当前做到哪里、谁正在执行、为何停滞、下一步是什么、如何继续/暂停/恢复/关闭、产生了哪些成果，以及完成项目如何进入成果中心和历史库。

不要另建第二套 Project、Issue、Task、员工、成果或治理真源。复用并连接现有 Project、Issue、Task/Run、Comment/receipt、Review/Repair、Artifact/Outcome Center。

## Verified starting facts and truth anchors

1. HiveCrew 当前正式入口为本机 UI `http://127.0.0.1:3000` 与 API `http://127.0.0.1:8080`；运行时状态必须重新实读，不得从旧报告推断。
2. 当前项目盘点曾观察到 11 个 Project 全部为 `in_progress`；其中至少两个项目全部 Issue 已完成但 Project 未关闭，至少两个 Project 无 lead，多个 Project 无 non-terminal Task 却长期显示进行中。Prime 启动时必须重新读取并记录最新快照。
3. `in_review` 目前可能只是 Issue 状态，不等于已产生独立 Review Task；`completed` Task 不等于独立验收或正式成果。
4. 现有 P0-F 父议题 HIV-548 是本 Goal 的可见治理主线；HIV-553 正在产出逐项目健康、frontier、next_action、outcome_coverage 与 closure_readiness 合同。复用其 Task-linked 结果，不重复另造合同。
5. 代码真源必须由 Prime 从当前 :3000/:8080 进程启动路径、Git worktree、HEAD、release pointer 和浏览器响应共同确认。候选仓库路径可能为 `/Volumes/HiveData/hivecosm/HQ-50-代码仓库/01-源码下载/multica`，但不得仅凭路径名认定当前主线。
6. HiveCosm 保留公司 Employee、正式 Project/Policy/Formal Outcome 权威；HiveCrew 是交互与执行投影。

## Goal-wide standing autonomy（全周期一次性授权）

William 已授权 Prime 为本 Goal 持续执行以下可逆、范围内工作，无需逐步等待确认：

Automatically allowed / 自动允许：Team/subagent 派发、隔离 worktree、范围内源码编辑、测试与 build、浏览器验收、CHECKLIST 更新、细粒度 JSONL 记录、owner-facing 日记；GPTPro 已被 Owner 免除而不实际调用。

- 读取当前源码、Git、服务、数据库 schema、API、浏览器页面、HiveCrew Project/Issue/Task/Run/receipt；
- 在明确基线之上创建隔离 worktree/branch；
- 使用 Prime Dynamic Workflow 组织一层子 Agent Team 并行工作；
- 修改本 Goal 范围内后端、前端、迁移源码、测试、fixture、文档、CHECKLIST 和证据；
- 运行格式化、lint、typecheck、单测、集成测试、race、构建、隔离数据库迁移和浏览器验收；
- 在备用端口启动候选 UI/API，验证通过后对本机开发环境进行可回滚切换；
- 创建/更新 HIV-548 下必要的可见子 Issue、Task/Run 和 Task-linked 交付回执；
- 对普通缺陷自动创建 Repair 工作并重新测试。

只有以下情况需要 Owner 决策：目标发生实质改变、需要明文秘密或新凭据、不可逆删除、外部生产发布、真实公司权威或正式成果写入、无法调和的代码所有权冲突、会造成现有数据不可恢复的迁移。

若某个风险节点 blocked/held，Prime 必须隔离该节点并继续其他安全的 ready 工作，不得停止其他 ready frontier。

GPTPro 复核按 Owner 已有决定省略；以 HiveCrew 不同员工/模型的独立测试、审查和浏览器验收替代。

## GPTPro Owner-waived review contract

- GPTPro 规划冻结前 / plan freeze review：`OWNER_WAIVED`，不实际调用。
- GPTPro 每个 major Phase 评审：`OWNER_WAIVED`，由不同 HiveCrew 员工的独立 review 替代。
- GPTPro 最终胜利复核 / final victory review：`OWNER_WAIVED`，由完整测试、浏览器验收和 Owner readback 替代。
- 该免除来自 Owner 明确决定，不得把普通模型回答标为 GPTPro。
- 如果 Owner 将来重新启用，必须在 Codex 内置 in-app 浏览器确认模型选择器显示 Pro 能力，并记录 label/标签、conversation URL、timestamp/时间和 screenshot/visual evidence；无法证明则记录 `PRO_CAPABILITY_UNVERIFIED`，不得标记 GPTPro。
- GPTPro 即使启用也只是顾问 advisory，不授予生产、发布、验收或完成权限。
- 复核最多一次主审和最多一次聚焦质询/challenge；发现项仅归类为 `phase_critical_correction`、`evidence_gap`、`optional_improvement`、`out_of_scope` 或 `owner_decision`。

## Prime operating model

Prime 是唯一总实施负责人、架构整合者和最终候选提交者。允许使用 Dynamic Workflow 启动有用的一层子 Agent，禁止递归 Team。Prime 自主决定并发数量，但必须满足：

- 一个文件同一时间只有一个 writer；
- 后端、前端、迁移、测试的写入范围互不重叠；
- 每个子 Agent 首行声明身份、职责、读写权限和禁止递归；
- 子 Agent 结果是候选，Prime 必须检查 diff、集成并运行父级测试；
- 不打印、复制或传递任何 secret；使用已有登录态、credential-owning adapter 或环境引用；
- 不在非 Git 的 Run 目录 `git init`，不把任意绝对路径开放给写子 Agent；
- 只允许 Task lifecycle 与一条 Task-linked 最终交付记录，不用顶层评论制造重复 Task。

Prime 不是账本，也不得以自身记忆代替状态真源；Prime 只统筹和整合，CHECKLIST 持有控制状态。子 Agent/worker 失败时，保留其他独立成果，仅重新分配/reassign 失败范围给 fallback worker；父级 Prime 失败时从 Resume Package 恢复并由可用集成员工接管。

推荐并行角色（Prime 可按真实能力合并或增加，但不得重叠写）：

1. Contract/Data 子 Agent：状态机、read model、migration、idempotency 和负向 fixture。
2. Backend 子 Agent：项目健康投影、控制命令、reconciler、review/repair router、Outcome bridge。
3. Frontend 子 Agent：Projects 活动工作台、项目详情、动作预览/回执、历史分页、Outcome 链路。
4. Test 子 Agent：API/DB/并发/权限/迁移测试，先写合同反例再跟随实现。
5. Browser/Performance 子 Agent：真实 11 个项目数据、页面旅程、分页与大列表性能。

## Single execution truth and durable bundle

在确认的集成工作树内建立或更新：

- `docs/goals/HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1/GOAL.md`
- `docs/goals/HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1/CHECKLIST.yaml`
- `docs/goals/HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1/HANDOFF.md`
- `docs/goals/HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1/EVIDENCE.md`
- `docs/goals/HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1/graphs/overview.mmd`

`CHECKLIST.yaml` 是本 Goal 唯一执行状态真源；HiveCrew Project/Issue/Task/Run 是可见执行与回执投影；Mermaid、页面、日记和聊天摘要不能反写为完成状态。

## Goal Graph nodes、边与当前 frontier

Goal Graph nodes:
- `OUTCOME-PROJECT-CLOSURE`：人类可见结果；
- `PHASE-W0` 至 `PHASE-W5`：六个执行阶段；
- `WO-HEALTH`、`WO-CONTROL`、`WO-REVIEW`、`WO-OUTCOME`、`WO-UI`、`WO-TEST`：并行 Work Orders；
- `ART-CANDIDATE`、`EV-TEST`、`EV-BROWSER`、`REVIEW-INDEPENDENT`、`JOURNAL-PHASE`、`PROJECTION-MERMAID`：产物与证据节点；
- `VC-01` 至 `VC-12`：胜利条件节点。

Edges / 边：
- `OUTCOME-PROJECT-CLOSURE decomposes_to PHASE-W0..PHASE-W5`；
- `PHASE-W0 depends_on verified current truth`；
- `PHASE-W1 depends_on PHASE-W0`；
- `WO-HEALTH/CONTROL/REVIEW/OUTCOME/UI/TEST parallel_with`，共同 `produces ART-CANDIDATE`；
- `ART-CANDIDATE verified_by EV-TEST/EV-BROWSER`；
- `EV-TEST/EV-BROWSER reviewed_by REVIEW-INDEPENDENT`；
- `REVIEW-INDEPENDENT satisfies VC-01..VC-12`；
- `REVISE corrects ART-CANDIDATE`，不构成硬依赖环。

Current ready frontier：`WAVE-0 current truth`、HIV-553 Task-linked 合同读取、代码/页面/测试三条只读考古；其 join/integrator 是 Prime。

Critical path / 关键路径：`当前真相 -> 合同与红测 -> Prime 并行开发 -> Prime 纵切集成 -> HiveCrew 独立验收 -> 候选部署 -> 项目自我运行`。

## Goal Graph and waves

### WAVE-0 · 当前真相与安全基线

Deliverables:
- 当前 UI/API 进程、源码 worktree、HEAD、dirty files、DB migration、测试数据库、已有 P0-F Tasks 的快照；
- 11 个现有 Project 的逐项目清单：lead、Issue counts、non-terminal Task/Run、last_progress_at、frontier、health、next_action、expected_outcomes、outcome_coverage、closure_readiness；
- 读取 HIV-553 Task-linked 交付并冻结可实施合同；未完成时可并行做代码考古和测试设计，但不得发明冲突 schema。

Exit:
- 真正当前主线、写入边界、互斥文件和首个可执行纵切明确；
- 未产生第二套状态真源。

### WAVE-1 · 合同、状态机与测试先行

并行 Work Orders:

A. Project Health Projection
- 定义 `active_with_frontier | stalled_no_open_task | review_or_repair_blocked | ready_for_closure | duplicate_or_superseded | owner_decision_required | source_gap`；
- 每个 Project 投影 accountable_manager、frontier_issue、active_task、next_action、last_progress_at、WIP、blockers、expected_outcomes、outcome_coverage、closure_readiness；
- `status=in_progress` 且无真实 non-terminal Task 必须显示 stalled，不得显示“进行中”。

B. Control Operation Contract
- 项目 `continue | pause_dispatch | resume | close | generate_closure_package` 均先 preview 后 execute；
- append-only operation receipt；idempotency key 同 key 同 digest 返回原结果、不同 digest 409；
- Project 动作只能调已有 Issue/Task 服务，不建第二任务引擎；
- “暂停”只停止新派发；终止正在运行 Task 必须是单独且明确的动作。

C. Review/Repair Routing Contract
- `in_review` 必须绑定 candidate/source Task、证据与独立 reviewer；reviewer 不得等于 implementer；
- PASS/REVISE/UNKNOWN 是结构化回执；REVISE 自动形成同一父 Issue 下的 bounded Repair；
- 历史无证据的 in_review 进入 classify/Owner decision，不能自动假 PASS。

D. Outcome/Closure Contract
- Issue 完成必须有 disposition：contributed_to_outcome、no_outcome_expected、superseded、failed_with_reason 或 owner_waived；
- Project 关闭前要求所有 Issue 有 disposition，所有 expected outcome 已 accepted/waived/failed_with_reason；
- 生成 Project Closure Package，引用 Project、Issues、Tasks/Runs、reviews、repairs、artifacts、formal refs；
- 只有已经存在的 authoritative formal ref 才能作为 Formal Outcome 展示，candidate 不得冒充正式成果。

E. Red tests
- 缺 lead、无 Task 的 in_progress、completed-but-unaccepted、重复 dispatch、同作者自审、REVISE 无 repair、空 Outcome、重复 daemon、跨 workspace、未知字段、分页漂移、并发点击等负向测试先落红。

Exit:
- exact schemas、API、权限、迁移和测试矩阵冻结；
- Prime 记录 source hash 并更新 CHECKLIST frontier。

### WAVE-2 · 五个可运行纵切并行开发、逐个集成

不要等所有代码写完才测试。按以下纵切滚动集成，每一片都先测试再进入下一片：

Slice 1 — Project Health + 页面可见性
- 后端只读 Project health/portfolio API；
- Projects 页面分为“正在推进、待审核/返修、已阻塞、已停滞、待关闭”；
- 项目卡显示负责人、frontier、实际 worker、last receipt、next action、outcome coverage；
- 现有 11 个项目均有诚实分类。

Slice 2 — 继续/恢复/暂停派发
- Project 和 Issue 页面提供 Owner/管理人员可见的 continue/resume/pause-dispatch；
- 所有批量动作必须 preview + exact target + receipt；
- 已有 active Task 不重复创建；并发点击保持幂等；
- 项目不是“一键把全部 Issue 乱派”，而是派发当前 ready frontier。

Slice 3 — 自动审核与返修
- 新 candidate 进入 in_review 时生成独立 Review Task；
- Review WIP、SLA、reviewer independence、source lineage 可见；
- REVISE 自动生成/恢复 Repair，修复后回到复审；
- 审核队列不再靠人工观察栏目，也不允许无人 Task 的假审核。

Slice 4 — Outcome Center 与 Project Closure Package
- 普通 Project/Issue/Task 结果可形成 Temporary/Candidate Artifact lineage；
- 经过审核与既有 authority readback 后才进入 Formal Outcome；
- Project 页面显示 expected outcomes、coverage 和 closure readiness；
- 关闭项目生成 Closure Package 并在 Outcome Center 可查看项目、议题、版本、证据和正式成果关系。

Slice 5 — 历史、分页、回填与运营视图
- Projects、Issues、Tasks、Inbox 默认显示活动工作，历史使用 cursor pagination；
- completed/superseded/closed 项目进入 History，不删除；
- 对现有 11 个 Project 做 dry-run 回填和逐项处置建议；
- 两个全 Issue 完成但仍 in_progress 的项目进入 ready_for_closure，而不是静默关闭；
- 无 lead 项目进入 owner_decision_required，并提供负责人选择操作。

每个 Slice 的硬循环：
`实现 -> focused tests -> 独立 HiveCrew 测试 -> Prime 修复 -> API/browser acceptance -> evidence -> 合入集成分支`。

### WAVE-3 · HiveCrew 独立滚动验收

测试不是最后才开始。Prime 每完成一个 Slice，就在现有 HiveCrew Project/HIV-548 下创建或复用一条独立验收 Issue：

- Gauss / 测试与独立审查工程师：DB、API、并发、迁移、错误路径；
- Quinn / 质量守护者：权限、隐私、状态机、重复 Task、错误完成；
- Pixel / 前端与交互工程师：真实浏览器、分页、动作回执、空/错/加载状态；
- Sage / 首席架构师：只在跨模块合同和第二真源风险上复核。

实现者不得验收自己的 Slice。Reviewer 返回 REVISE 时，Prime 自动创建最小 Repair 并复跑同一验收；不得把修复藏在 Prime 私有工作区。

### WAVE-4 · 集成、候选部署与真实数据验收

1. 在隔离 DB 与备用端口跑全量 migrations、Go/TS tests、race、lint、typecheck、build。
2. 在备用 UI/API 完成浏览器旅程，不先覆盖当前 :3000/:8080。
3. 对现有数据运行 backfill dry-run，输出将变更/不变/冲突/Owner decision 统计；禁止直接批量改 Project 状态。
4. 候选完整通过后，允许对本机开发环境做有回滚点的切换；切换后重新验证 :3000/:8080、登录、Projects、Issues、Task/Run、Review、Outcome Center。
5. 记录 implemented、verified_local、integrated、local_runtime_applied、owner_accepted 五种状态，禁止压成 done。

### WAVE-5 · 项目组合处置与自我运行

- 对 11 个现有 Project 逐个确认 accountable manager 与 disposition；
- stalled 项目恢复 ready frontier；review/repair 项目进入真实任务；ready_for_closure 生成预览关闭包；duplicate/superseded 保留历史关联；
- 启用周期性 reconciler 只做诊断和安全调度：发现无 Task 停滞、审核无 reviewer、REVISE 无 repair、完成无 closure package 时创建可追溯动作；
- 管理团队从项目页面即可持续操作，不再依赖 Prime/Codex 聊天窗口人工逐项维修。

## Coordination log、owner journal 与 Resume Package

- 细粒度协调、锁、子 Agent 状态、失败与修复写入项目规定的 append-only JSONL，不写入 Owner 日记。
- 每个 major Phase 关闭时必须使用 `hivecosm-work-journal`，以唯一 marker 追加 owner-facing Obsidian 日记，随后回读/print 并运行 verify；未 append、回读和验证不得关闭阶段。
- 每次 frontier 或控制者变化都更新 Resume Package，至少包含：Goal/version、current controller、当前 Phase、ready/running/blocked、evidence index/证据索引、dirty boundaries、latest review/journal 和 next action/下一动作。
- `failed`、`blocked/held`、`rollback_pending`、`superseded`、`abandoned` 必须诚实保留；不得通过改写历史伪装成功。

## Permanent invariants

1. Project status 不是执行证据；Task/Run 和 receipt 才证明执行。
2. Task completed 不是独立验收；Issue done 不是 Formal Outcome；所有 Issue terminal 也不自动关闭 Project。
3. 一个 Project 只能有一个当前 accountable manager，但可有多个执行员工。
4. 一个 ready work item 同时只能有一个 canonical execution Task；retry/replace 必须显式。
5. Reviewer != implementer；REVISE 必须有 Repair 或明确 disposition。
6. HiveCrew 不成为 HiveCosm Employee/正式 Project/Policy/Formal Outcome 的第二权威。
7. 所有批量与控制动作必须 preview、exact target、idempotency 和 receipt。
8. 不删除历史；关闭、替代和取消进入分页历史库。
9. 不暴露 secrets、raw malformed payload、其他 workspace 数据或非 available 员工的操作身份。
10. 任何子 Agent、Prime 自述、绿测或页面截图都不能单独宣称 Goal 完成。

## Victory conditions

VC-01 Current truth: 当前全部 Project 在一个页面被诚实分类；`in_progress` 无 Task 不再显示正常进行。

VC-02 Accountability: 每个活跃 Project 显示唯一 accountable manager、frontier Issue、active Task/worker、last_progress_at 和 next_action；缺失项明确 source_gap/Owner decision。

VC-03 Owner controls: Project/Issue 上存在 continue、resume、pause-dispatch、stop-current、rerun、close-preview、generate-closure-package 的准确操作；权限、预览、幂等和回执测试通过。

VC-04 Continuous dispatch: ready frontier 可被安全派发；已有 active Task 不重复；停滞项目能恢复；相同 worktree 不出现并发 writer。

VC-05 Review/repair: 新审核事项自动获得独立 reviewer Task；历史审核得到分类；REVISE 自动产生 Repair；review backlog、WIP 和 SLA 页面可见。

VC-06 Outcome lineage: Project -> Issue -> Task/Run -> Candidate -> Review/Repair -> Formal ref 的链路可查询；不把 candidate 冒充正式成果。

VC-07 Project closure: 满足条件的 Project 可生成 Closure Package 并进入 Outcome Center；未满足条件显示精确缺口；关闭操作可回读且不可因重复点击重复执行。

VC-08 Existing portfolio: 现有全部 Project 获得逐项 disposition；至少两个“全 Issue 完成但仍 in_progress”的反例被正确投影为待关闭；无 lead 项目不再无声挂起。

VC-09 Active/history UX: Projects、Issues、Tasks、Inbox 默认活动视图有限、可分页；历史记录可搜索和恢复上下文，页面不随数据无限增长。

VC-10 Runtime acceptance: 当前本机 :3000/:8080 的真实登录态浏览器旅程通过；API、DB、并发、race、typecheck、build、migration、browser 测试均有 revision-bound evidence。

VC-11 Provenance: 所有候选 commit、changed files、测试、HiveCrew Issue/Task/Run、Task-linked delivery、独立 reviewer 与浏览器证据可追溯。

VC-12 Self-operation: 在无 Prime/Codex 手工逐项推动的情况下，系统能发现并处理“停滞无 Task、审核无 reviewer、REVISE 无 repair、完成无成果包”四类断链；管理团队可从 HiveCrew 页面接管。

## Completion rule

Prime 只有在 VC-01 至 VC-12 均有当前证据、所有必要 Repair 已关闭、候选已集成并在本机正式开发入口完成浏览器回读、Task-linked 交付完整、工作树和后台进程已清理后，才可报告“完成”。

若只完成代码但未运行验证，报告 implemented；若测试通过但未集成，报告 verified_candidate；若已集成但未切换运行环境，报告 integrated_not_applied；不得使用笼统 PASS 掩盖层级。

## Immediate first frontier — start now

1. 执行当前真相校准：确认 :3000/:8080 源码、Git、DB、11 Project、HIV-548/HIV-553 Task/Run。
2. 在隔离 worktree 建立 Goal durable bundle 和 CHECKLIST。
3. 同时启动三个不冲突的一层子 Agent：代码/数据合同考古、项目页面与 Owner journey、测试/并发/迁移矩阵。
4. 等 HIV-553 Task-linked 合同返回后由 Prime 一次集成，立刻开始 Slice 1，不得再次停留在纯规划循环。
5. 每个 Slice 完成即交 HiveCrew 独立测试，不等待五个 Slice 全部完成。
