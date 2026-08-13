# B/C 边界 operationalization 正式记录（#4 reconcile）

> 冻结时点：2026-08-13T23:20:26.952696+00:00。解决审计发现 #4「classifyProject 的 B/C 边界与冻结合同偏离」。

## 问题
冻结合同（HIV-553 组合表）把 BASES（17 in_review）与 ORCHESTRATION（3 in_review）判为 B/stalled，
而确定性读模型把「存在 in_review 且无 live task」判为 C/review_or_repair_blocked。

## 判定（正式接受为确定性 operationalization）
1. 合同 C 的文本定义是「审核、REVISE、blocked 或失败后的 repair/re-review 尚未形成 live Task」。
   `in_review` 状态即「审核尚未形成 live Task」的明确信号，因此「in_review → C」是对合同文本的
   保守且可辩护的读法（fail-closed：宁可把审核积压显式为待审核，也不静默标为「已停滞」）。
2. 冻结合同对 BASES/ORCHESTRATION 的 B 判定是基于「最近一次 review Task 已完成、仅 Issue 滞留在
   in_review」这一**历史语义**，该语义依赖任务历史时序，当前结构化读模型不采集此信号。
3. **接受偏差**：BASES/ORCHESTRATION 会被投影为 C（review_or_repair_blocked）而非 B。这是
   「保守显式化」的偏差，方向是「把需要关注的审核积压显示出来」，不产生错误的「进行中/可关闭」结论。
4. UI 通过 `review_issue_count` + `next_action`（"review backlog: N"）把积压数显式暴露，管理方可据此
   判断是「停滞」还是「审核积压」，不依赖单一 health 字母。

## 后续（Slice 3 review-cell 接入后）
当 review verdict 结构化数据可用时，把 C 触发细化为「REVISE/blocked/failed-repair → C；纯 in_review
无 verdict → B」，即可与冻结合同逐项对齐。此升级已登记为 CHECKLIST 后续项，不改动当前已验收行为。
