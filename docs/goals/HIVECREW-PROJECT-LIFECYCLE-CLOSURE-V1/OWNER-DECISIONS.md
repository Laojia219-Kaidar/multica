# Owner 决策回执（VC-08 组合处置，Contract 关闭门 1）

> 决策时点：2026-08-14T01:35:44.116984+00:00。以下为 William 的显式处置决定，作为 Owner receipt 记录，满足关闭门 1「duplicate/supersede 决策已有 Owner receipt」。

## 决策 1 · PRJ-HCW-V2（Founder Sovereign Company Workbench V2）

- **项目**: 1bae6f35-44ae-4052-8c2d-2d2d01638875
- **决定**: (a) supersede —— 归并/替代到新项目 `HIVECREW-OWNER-OPERATING-WORKBENCH-V1`（3b0330e7）。
- **处置含义**: 冻结旧项目新执行；其未终结 Issue（当前 ~107 条）逐条标记 `migrate_to`(迁移到新项目) / `superseded_by` / `keep`，保留历史关联、不删除。
- **执行状态**: 项目 status 已为 cancelled（冻结新执行）；health 投影的 `duplicate_or_superseded` 由 frozenSupersessions 种子（1bae6f35 → 3b0330e7）承载；逐 Issue disposition 迁移待 Slice 4/5 disposition 机制落地后回填（schema 现无 disposition 列）。

## 决策 2 · Mac Native Digital Employees Canary

- **项目**: 5c557bd2-9c13-4839-abe4-ce4f29cc3bfa
- **决定**: (a) 补跑剩下 2 名员工验收，凑满 4/4 后再关闭。
- **已执行**: 创建 2 条 canary 验收 Issue（GLM Analysis Specialist → Drake；Qwen Engineering Assistant → Raven），触发 daemon 派发 read-write-test，补齐 4/4 覆盖。
- **closure lead**: 待补（4/4 验收完成后由集成负责人或 Owner 指定 closure lead 再关闭）。

## 回执性质

以上为 Owner 显式决定，非自动化推断；不得被系统自动覆盖。
