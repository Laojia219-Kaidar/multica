# W2 · 存量 11 项目 disposition 预处理（VC-08 dry-run，只读）

> 冻结时点：2026-08-13T14:52:43.845599+00:00。本报告为逐项目处置建议（preview/建议，不 apply），为 Slice 4/5 的 Closure Package 与回填做前置输入。
> 数据源：live DB 只读实读（11 项目 / 553 issue / agent_task_queue）。health 取自 Slice 1 读模型口径。

## 逐项目 disposition 建议

| Project | 当前 health | lead | disposition 建议 | 下一可执行动作 |
|---|---|---|---|---|
| HIVECREW-OWNER-OPERATING-WORKBENCH-V1 | review_or_repair_blocked（2 blocked + 41 in_review + repair gap） | Coco(agent) | keep（active 主执行投影） | 先为 blocked issue 建 repair→re-review Task，再核销 review backlog |
| HIVECREW-OWNER-CONTROL-V1 | review_or_repair_blocked（5 blocked + 132 in_review） | Shepherd(agent) | keep | 阻塞点 HIV-520 先建 repair Task；132 review 逐条核销，不扩 scope |
| HIVECREW-BASES-V1 | review_or_repair_blocked（17 in_review + 8 todo） | William(member) | keep | 以 HIV-542/552 为门建 review/disposition Task 或显式 pause |
| HIVE-ORG-ENGINEERING-V1 | review_or_repair_blocked（11 in_review + fail-closed） | Kai(agent) | keep | HIV-299 REVISE → repair；三项 review PASS 前不提升 HIV-258 |
| HIVE-ORCHESTRATION-V1 | review_or_repair_blocked（3 in_review + 2 in_progress + 6 backlog） | Raven(agent) | keep | disposition HIV-281/282/283；通过后提升 284/285，否则 repair |
| HIVE-CAPACITY-ROUTING-V1 | review_or_repair_blocked（3 in_review review gate） | Kai(agent) | keep | 核验 293/294/295 receipts 后提升 HIV-296 独立复审 |
| HC-MULTICA-1421-INTEGRATION v1.1 | review_or_repair_blocked（7 in_review + 1 in_progress） | Coco(agent) | keep | 恢复 HIV-26 review/disposition；逐项核销 7 review 或显式 pause |
| HiveCosm Multica Pilot · HiveBuddy A11y | review_or_repair_blocked（1 in_review failed-repair gap） | Coco(agent) | keep | 诊断 HIV-13 失败尝试，建 repair/re-review；Stage 2-6 不提前启动 |
| Multica 源码理解与评估 | source_gap（4/4 terminal，无 Outcome/Closure Package） | Coco(agent) | ready_for_closure 候选 | 把 4 条 Issue receipt/artifact 映射到 expected outcome，生成 Closure Package 后转 D |
| Mac Native Digital Employees Canary | source_gap + F（2/2 terminal，resource 丢失，缺 lead） | UNASSIGNED | owner_decision_required | Owner 指定 closure lead；解释 4 员工 vs 2 issue 覆盖差；恢复/替代 /tmp resource |
| PRJ-HCW-V2 · Founder Sovereign Company Workbench V2 | duplicate_or_superseded + F（115 nonterminal，缺 lead，与 OWNER-WORKBENCH 同 canonical authority） | UNASSIGNED | owner_decision_required（supersede 候选） | Owner 决定 keep/merge/supersede；逐 issue 标记 migrate/supersede/keep；冻结新执行 |

## 汇总统计（dry-run 建议，不 apply）

- keep（继续推进，需 review/repair 排障）: 8
- ready_for_closure 候选（生成 Closure Package 后转 D）: 1（Multica 源码理解与评估）
- owner_decision_required（缺 lead / duplicate，需 Owner 决策）: 2（Mac Canary、PRJ-HCW-V2）

## 门与依赖

- 本报告为 preprocessing，不写任何 Project/Issue/Task 状态。
- 实际 apply 需：Slice 2 控制操作（continue/pause/resume）+ Slice 4 Closure Package 落地后，逐项目走 preview→commit→receipt。
- duplicate/supersede 与缺 lead 的 F 门必须由 Owner 显式决策（不可自动化）。
