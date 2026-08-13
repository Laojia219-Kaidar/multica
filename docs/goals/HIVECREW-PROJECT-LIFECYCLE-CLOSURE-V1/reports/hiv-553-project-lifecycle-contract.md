## HIV-553 项目持续经营、停滞恢复与成果归结合同

审计时点：2026-08-13T20:43:03+08:00  
审计方式：只读 Multica CLI 快照；覆盖 11 个 Project、518 个 Issue、33 个 Agent 的 787 条 Task 记录。  
变更边界：未修改任何被审计 Project、Issue、Task、Run、Outcome、源码、数据库、服务或生产环境；只对 HIV-553 本身执行了 Ownership 工作流要求的状态流转。

## 结论

当前 11/11 Project 均标记为 `in_progress`，但只有 `HIVECREW-OWNER-OPERATING-WORKBENCH-V1` 存在真实 nonterminal Task，共 3 条 `running` Task。其余项目中，8 个仍有 nonterminal Issue 但 live Task=0，必须显示为停滞或审核/返修阻塞；2 个全部 Issue terminal，但都不能仅凭此自动认定成果完成或关闭。

严格应用本合同后，当前分类为：A=1、B=2、C=5、D=0、E=1、G=2；其中两个无 lead 的项目同时带 F 决策门。没有任何项目已经满足完整关闭门。

分类定义：

- A `active_with_frontier`：至少一个与该项目 Issue 关联的 nonterminal Task/Run。
- B `stalled_no_open_task`：仍有 nonterminal Issue，但没有 nonterminal Task/Run，且无更具体的审核/返修阻塞。
- C `review_or_repair_blocked`：审核、REVISE、blocked 或失败后的 repair/re-review 尚未形成 live Task。
- D `ready_for_closure`：所有 Issue 有 disposition，关键 Outcome 全部 accepted/waived/failed-with-reason，Closure Package 已生成并可提交。当前为 0。
- E `duplicate_or_superseded`：与另一 Project 指向同一 canonical authority，必须由 Owner 决定保留、归并或 supersede。
- F `owner_decision_required`：缺 accountable lead 或存在无法由自动化裁决的业务决策；可作为主分类或附加门。
- G `source_gap`：关闭所需的来源、Artifact、Outcome、receipt 或资源不可回读。

## Task-linked 项目组合表

`WIP` 统一表示 `nonterminal Issues / live Tasks`；Issue 状态不等于正在执行。`last_progress_at` 取最近成功 Task 的 `completed_at`，不以 Project/Issue 的普通更新时间冒充进展。时间均为 UTC。

| Project | 分类 / health | accountable lead | 当前 frontier Issue / Task | WIP | last_progress_at | next_action | closure_readiness |
|---|---|---|---|---:|---|---|---|
| HIVECREW-BASES-V1 | B / stalled（新鲜停滞） | William | `∅`；最后 `HIV-552` Task 已完成，Issue 仍 `in_review` | 25 / 0 | 2026-08-13T12:38:59Z | 以 `HIV-542/HIV-552` 为门，创建一条明确 review/disposition Task，或显式暂停；禁止仅保留 17 个 `in_review` + 8 个 `todo` | NO：25 个 Issue 均未 terminal，Outcome 未归结 |
| HIVECREW-OWNER-CONTROL-V1 | C / blocked | Shepherd | `∅`；阻塞点 `HIV-520`，最近成功 Task 为 `HIV-523` | 166 / 0 | 2026-08-13T06:06:46Z | 先为 `HIV-520` 建 repair→re-review Task，再排 132 个 review；不要扩展新 scope | NO：5 blocked、132 in_review、无 terminal disposition |
| HIVE-CAPACITY-ROUTING-V1 | C / review gate | Kai | `∅`；候选 `HIV-293` in_review，独立复审 `HIV-296` backlog | 5 / 0 | 2026-08-12T00:21:22Z | 核对 `HIV-293/294/295` Task receipts 后，只提升 `HIV-296` 独立复审；失败则新建 repair Task | NO：5/5 nonterminal，额度/成本真源 gap 尚未验收 |
| HIVE-ORCHESTRATION-V1 | B / stalled | Raven | `∅`；`HIV-281` Issue 仍 in_progress，但其 Task 已完成 | 11 / 0 | 2026-08-11T23:33:17Z | 先 disposition `HIV-281/282/283`；通过后提升 `HIV-284/285`，否则建立 repair Task | NO：3 in_review、2 in_progress、6 backlog |
| HIVE-ORG-ENGINEERING-V1 | C / fail-closed | Kai | `∅`；`HIV-299` 标记 REVISE，`HIV-258` gate=`fail_closed` | 17 / 0 | 2026-08-12T13:24:03Z | 针对 B1/B2 和 Task-linked delivery gap 建独立 repair Task；三项 review PASS 前不得提升 `HIV-258` | NO：显式 blockers，17/17 nonterminal |
| HIVECREW-OWNER-OPERATING-WORKBENCH-V1 | A / active | Coco | 主 frontier `HIV-553` / Task `5a1ab257-5a9f-45e5-9204-f3c86cd8a8e9`；并行 `HIV-549` / `5277845e-c17e-450a-92d8-fee5bff920c4`、`HIV-550` / `5f488de3-77b7-4652-b696-b708007a52ab` | 58 / 3 | 2026-08-13T12:35:31Z；3 Task 正在运行 | 保持 WIP=3，不再 fan-out；三条 Task 形成 receipt 后由 `HIV-548` 统一验收/返修 | NO：58 nonterminal；59 done+4 cancelled 仅是 disposition proxy，不是 Outcome acceptance |
| Mac Native Digital Employees Canary | G + F / source gap | **UNASSIGNED** | `∅`；2/2 Issue terminal | 0 / 0 | 2026-08-10T15:26:25Z | Owner 指定 closure lead；解释“4 名员工”目标与仅 2 个 Issue 的覆盖差；恢复/替代已丢失的 `/tmp` resource，生成 Closure Package | NO：资源指针已失效，预期 4 人但只有 2 条 Issue receipt，Outcome 未回读 |
| PRJ-HCW-V2 · Founder Sovereign Company Workbench V2 | E + F / duplicate | **UNASSIGNED** | `∅`；候选 `HIV-59` in_review | 115 / 0 | 2026-08-12T15:57:43Z | Owner 决定以新 `HIVECREW-OWNER-OPERATING-WORKBENCH-V1` 为执行投影后，冻结本 Project 新执行并逐 Issue 标记 migrate/supersede/keep；不得自动合并 | NO：与新 Project 同指 `hive://.../PRJ-HCW-V2`，115 nonterminal、缺 lead |
| HC-MULTICA-1421-INTEGRATION v1.1 | C / review backlog | Coco | `∅`；候选 `HIV-26` in_progress，另有 7 in_review | 8 / 0 | 2026-08-12T14:48:14Z | 只恢复一条 `HIV-26` review/disposition Task；逐项核销 7 个 review，或显式暂停项目 | NO：8 nonterminal；9 done+12 cancelled 不能代表集成 Outcome 已接受 |
| HiveCosm Multica Pilot · HiveBuddy A11y | C / failed-repair gap | Coco | `∅`；`HIV-13` in_review，最近一次相关 Task 于 2026-08-12 失败 | 7 / 0 | 2026-08-06T15:31:05Z | 先诊断 `HIV-13` 失败尝试，建立 repair/re-review Task；Stage 2–6 在 Stage 1 disposition 前保持不启动 | NO：Stage 1 未闭合，后续 5 个 todo 无执行 frontier |
| Multica 源码理解与评估 | G（D 候选）/ outcome gap | Coco | `∅`；4/4 Issue terminal | 0 / 0 | 2026-08-12T03:06:03Z | 将四项 Issue 的 receipts/Artifacts 映射到预期 Outcome，生成 Closure Package；完整后才转 D 并关闭 | NO：Issue disposition 完整，但 Outcome Center/Closure Package 尚无可回读证据 |

## Expected outcomes 与 outcome coverage

`terminal disposition coverage` 只回答 Issue 是否已结束；`confirmed Outcome coverage` 才回答成果是否 accepted/waived/failed-with-reason。两者不得互换。本轮 CLI 没有给出可查询的 Outcome Center 关联，因此“confirmed”一律 fail-closed，不用 done_count 推断 acceptance。

| Project | expected_outcomes | terminal disposition coverage | confirmed outcome coverage |
|---|---|---:|---|
| BASES | 5 个受管理执行基地；33 员工主/备基地映射；基地控制/用量/故障恢复闭环 | 0/25 | 0 confirmed；25 个均 nonterminal |
| OWNER-CONTROL | 启动/继续；员工/部门派工 preview+receipt；精确停止；独立审核；WIP/SLA/容量视图 | 0/166 | 0 confirmed；5 blocked、132 review |
| CAPACITY | usage/cost 审计读模型；5h/7d/月窗口与可信度；安全建议路由 | 0/5 | 0 confirmed；独立复审未执行 |
| ORCHESTRATION | 阶段 DAG/角色分离；REVISE→repair→re-review；租约/取消回收/证据门 | 0/11 | 0 confirmed；Stage 1 未 disposition |
| ORG-ENGINEERING | 组织/Employee/Runtime/Harness 注册；团队/WorkContract/策略；绩效进化与控制面 | 0/17 | 0 confirmed；显式 fail-closed |
| OWNER-WORKBENCH | Owner需求→WorkOrder→Employee/Agent→Run→审核/返修→Promotion→正式 Outcome | 63/121（59 done + 4 cancelled） | 未知；terminal Issue 不可计作 accepted Outcome |
| MAC-CANARY | 4 名 Mac-native 员工隔离验收与可追溯 receipt | 2/2 Issue | 仅 2/4 目标有 Issue 映射；resource missing，G |
| PRJ-HCW-V2 | Founder Workbench V2 / 1421 Owner 工作台纵切与 Company Runtime 证据 | 16/131（10 done + 6 cancelled） | 未知；且 canonical authority 与新 Project 重叠 |
| 1421-INTEGRATION | 三模型只读 canary；HQ-06 adapter；1421 多 Agent 控制面集成验收 | 21/29（9 done + 12 cancelled） | 未知；8 nonterminal |
| HIVEBUDDY | Kimi 前检、GLM 实现、Qwen 测试、Coco 集成与终验 | 0/7 | 0 confirmed；Stage 1 未闭合 |
| SOURCE-UNDERSTAND | 源码架构理解、安全/隔离评估、异构模型协作结论与后续集成建议 | 4/4 | 未知；需 Closure Package 才可认定完整 |

## 唯一生命周期合同

### 派生字段

生命周期视图是现有 Project/Issue/Task/Run/Outcome 的派生读模型，不创建第二套 Project 真源。

- `frontier_tasks`：属于该 Project Issue 且 Task/Run 为 nonterminal 的精确 ID 集；可能为空或多条。
- `frontier_issue`：由 `frontier_tasks.issue_id` 回链，不允许用“最新 Issue”替代。
- `wip`：`count(nonterminal Task/Run)`；另列 `nonterminal_issue_count`，两者不得混为一谈。
- `last_progress_at`：`max(successful Task.completed_at, accepted/waived/failed Outcome decision_at, applied lifecycle receipt time)`；失败/取消可列为 activity，但不计成功 progress。
- `health`：A–G 的确定性派生结果；Project.status=`in_progress` 只表示意图，不覆盖执行真值。
- `outcome_coverage`：每个 expected outcome 必须是 `accepted | waived(reason) | failed(reason) | pending | source_gap`。
- `closure_readiness=true`：仅在全部关闭门通过时成立。

### 状态机

```text
PLANNED --continue--> ACTIVE
ACTIVE --all live Task/Run terminal, Issue remains--> STALLED
ACTIVE/STALLED --review|REVISE|failure gate--> REVIEW_OR_REPAIR_BLOCKED
REVIEW_OR_REPAIR_BLOCKED --repair/re-review Task starts--> ACTIVE
ACTIVE/STALLED/BLOCKED --pause--> PAUSED
PAUSED --resume exact frontier--> ACTIVE
ACTIVE/STALLED/BLOCKED/PAUSED --all dispositions complete--> CLOSURE_PENDING
CLOSURE_PENDING --outcomes complete + Closure Package accepted--> CLOSED
ANY NON-CLOSED --owner duplicate decision--> SUPERSEDED
```

`STALLED`、`REVIEW_OR_REPAIR_BLOCKED`、`source_gap` 是 health/read-model 结论；只有显式 pause、close、supersede 才写 Project 生命周期状态及不可变 receipt。服务器实际 Project enum 必须在 Stage 2 从现有合同回读后绑定，不能新增同义状态表。

### 关闭门

关闭 Project 必须同时满足：

1. accountable lead 已存在；canonical authority 唯一，或 duplicate/supersede 决策已有 Owner receipt。
2. 每个 Issue 都有 disposition：`done`、`cancelled(reason)`、`superseded_by`、`migrated_to` 或 `waived(reason)`；裸 terminal 状态不够。
3. 不存在 nonterminal Task/Run，也不存在已取消 Task 的存活子进程/Run。
4. 每个 expected outcome 均为 `accepted`、`waived(reason)` 或 `failed(reason)`，且有决策人、时间、来源 Artifact/Task/Run receipt。
5. Project Closure Package 已生成、哈希固定、独立复核并进入 Outcome Center。
6. close commit 使用 preview token、expected version 和 idempotency key；成功后返回状态变更与 Outcome promotion receipt。

### Project Closure Package

Closure Package 必含：

- Project ID、canonical authority、accountable lead、目标、快照版本和审计时点；
- expected outcome matrix 与每项 accepted/waived/failed 的理由、决策人、时间；
- 全量 Issue disposition ledger；
- Task/Run receipt ledger、检查证据、失败/取消/返修链；
- Candidate Artifact、正式 Artifact/Outcome promotion ID、来源 commit/resource/hash；
- 未解决风险、source gaps、保留/销毁策略；
- 独立 reviewer（不得等于作者）与最终 Owner 决策；
- close action receipt。历史只归档、不可删除。

## Project 页面动作合同

所有写动作均为 `preview → explicit confirm → commit → immutable receipt`，owner/admin-only，使用 optimistic version + idempotency key。preview 本身只读，不创建 Task/Run，不改 Project。

| 动作 | preview 必须显示 | commit 允许的唯一效果 | receipt 必须返回 |
|---|---|---|---|
| 继续 | 当前 health、lead、frontier Issue、live Task/Run、WIP、依赖、目标 runtime/agent、预计副作用 | 复用现有 Issue，创建或复用一条精确 Task/Run；若已有等价 live Task 则幂等返回 | project/issue/task/run IDs、before/after version、actor、idempotency key、applied/replayed、时间 |
| 暂停 | 所有 live Task/Run 精确 ID、可取消性、子进程/租约风险、不会被修改的 Outcome/历史 | 取消或收敛列出的 Task/Run；写显式 pause receipt；不得删除 Issue/历史 | 每条 Task/Run 结果、剩余 live count=0、Project before/after、失败项；有残留则整体不宣称 paused |
| 恢复 | pause receipt、建议 frontier Issue、依赖/阻塞、目标 assignee/runtime、WIP 预算 | 不复活 terminal Task；在既有 Issue 上创建新 Task/Run 并链接前次 receipt | 新 Task/Run ID、recovery_of、版本、actor、幂等结果 |
| 关闭 | 六项关闭门、Issue disposition matrix、Outcome matrix、live Task/Run=0、Closure Package hash | 仅在门全绿时写 terminal Project 状态并 promotion Closure Package；任何 gap 均 fail-closed | close receipt、package/Outcome IDs、before/after；拒绝时返回结构化 blockers 且零写入 |
| 生成成果包 | expected outcomes、Issue/Task/Run/Artifact 来源、source gaps、package diff/preview hash | 生成 candidate Closure Package；不自动接受 Outcome、不自动关闭 Project | package ID/hash/version、included/excluded sources、coverage、review_required |

receipt 是幂等与审计证据，不是第二套 Project 状态。Project/Issue/Task/Run/Outcome 仍是事实源；receipt 只记录命令、版本与已发生副作用。

## 负向测试

| 场景 | 必须结果 |
|---|---|
| Project=`in_progress`，但无 nonterminal Task/Run 且有 open Issue | health=`stalled_no_open_task`，不得显示“执行中” |
| 全部 Issue terminal，但 Outcome 未覆盖或 Closure Package 不存在 | close preview 拒绝：`OUTCOME_COVERAGE_INCOMPLETE` / `CLOSURE_PACKAGE_MISSING` |
| Issue=`done` 但没有 Task-linked receipt | 不自动完成 Project，不进入正式 Outcome |
| Issue=`in_progress`，其唯一 Task 已 completed | frontier=`∅`，health=stalled；不得用 Issue 状态制造 live work |
| Task cancelled，但 Run/子进程仍存活 | pause/close 失败，返回 exact residual Run IDs |
| 多个 live Task 超过 WIP policy | health 标红；continue 拒绝，除非 preview 明确列出并获授权 |
| reviewer=author | Outcome accept/promotion 拒绝 |
| 两 Project 指向同一 canonical authority | 自动继续/关闭/合并均拒绝，进入 E/F Owner decision |
| lead 为空 | continue、resume、close 拒绝：`ACCOUNTABLE_LEAD_REQUIRED` |
| Artifact/resource 不可回读 | classification=G；不得把 Issue done 当成果证据 |
| cancelled Issue 无 reason / superseded Issue 无 target | disposition 不完整，close 拒绝 |
| preview 后 Project version、frontier 或 Outcome coverage 变化 | commit 返回 conflict，零部分写入，要求重新 preview |
| 同 idempotency key 重放 | 返回同 receipt，不重复创建 Task/Run/Outcome |
| 批量 close 中任一项目失败 | 每项目独立 receipt；不得静默部分成功或用总成功覆盖失败项 |
| close 后查询历史 | Issue、Task、Run、Outcome、Closure Package 全部可回读，不允许物理删除 |

## Stage 2 实现边界

### In scope（candidate-only）

1. `ProjectLifecycleSnapshot` BFF/read model：从现有 Project/Issue/Task/Run/Outcome 派生 A–G、lead、frontier、WIP、last_progress、outcome coverage、closure readiness。
2. 单 Project 的 continue/pause/resume/close/generate-package preview + commit API；owner/admin RBAC、optimistic concurrency、idempotency、结构化 blockers。
3. Project 页面展示 accountable lead、frontier Task/Issue、health、WIP、Outcome matrix、关闭门和五个动作；危险动作二次确认。
4. Closure Package candidate generator、独立复核门、Outcome Center promotion adapter。
5. isolated candidate 的 unit/contract/integration/browser tests，覆盖上述负向测试；先对虚拟 Project，再对只读快照做 dry-run。
6. 对这 11 个项目只生成 preview/dry-run 报告；另行授权前不 apply。

### Out of scope

- 不新建 Project/Issue/Task/Run/Outcome 的第二事实表或平行控制面；不复制 Employee/Agent registry。
- 不重写 ReviewPipeline、调度器、成本账本或现有 History/Inbox 项目。
- 不批量改变这 11 个 Project/Issue/Task 状态，不迁移正式 DB，不部署生产，不清空历史。
- 不自动接受 Outcome、不自动决定 duplicate/supersede、不自动指定缺失 lead。
- 不以无限滚动、前端虚拟化、Project.updated_at 或 done_count 代替服务端分页、执行真值或成果验收。

### Stage 2 开工门

进入源码前必须另行绑定：基准 commit、隔离 worktree、allowlist/denylist、现有 Project status enum 与 API/DB 真源、验收命令和回滚点。实现、独立测试、浏览器验收由不同责任人完成；本 HIV-553 未授权源码实现，因此本轮没有 checkout、commit 或 PR。

## 只读校验结果

- Project 数量：11，全部当前为 `in_progress`。
- Task 记录：787；nonterminal Task：3，且全部属于 OWNER-WORKBENCH。
- 有 nonterminal Issue 但 live Task=0：8 个 Project。
- 全部 Issue terminal：MAC-CANARY、SOURCE-UNDERSTAND。
- lead 缺失：MAC-CANARY、PRJ-HCW-V2。
- Issue 分页统计逐项目求和均与 Project `issue_count` 一致。
- MAC-CANARY 的 Project resource 指向已不存在的 `/tmp` 路径；PRJ-HCW-V2 的旧 local resource 仍存在。
