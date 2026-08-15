# W3 集成消费只读验证 · Slice 3/4/5（不重复实现）

> 冻结时点：2026-08-13T16:10:14.533082+00:00。对 W3 集成分支 `work/hivecrew-product-integration-mainline` 做只读核对，确认哪些 VC 已由 W3 覆盖、哪些仍是本 Goal 的缺口。

## Slice 3 · 审核/返修路由（VC-05）——W3 已覆盖

- 路由: `POST /api/issues/{id}/review-verdict`、`POST /api/issues/{id}/review-requeue`、`GET /api/issues/review-queue`
- 文件: server/internal/service/{review_cell.go,review_drain.go}、server/internal/handler/review.go、server/internal/scheduler/{write_lease.go,write_lease_jobs.go,review_drain_job.go}、migrations 280-285
- 合同核对: review.go 强制「verdict 需 agent reviewer 或 member owner」（reviewer 身份）、reviewer≠writer（write lease + drain 分离）、PASS/REVISE/repair/re-review。
- 结论: VC-05 主体由 W3 提供，Prime 只做证据回读，不重建。

## Slice 4 · 成果中心（VC-06 部分）——W3 已覆盖；Closure Package 缺口

- 已覆盖: `GET /api/company-ops/outcomes`(+cursor)、`/outcomes/{commandId}`；companyops_outcomes.go + companyops_outcome_center.go + migration 340。
- **缺口（本 Goal 未完成）**: Project Closure Package 生成（generate_closure_package）与 close 动作未实现。这是原 Goal WAVE-2 Slice 4 的「Closure Package generator、独立复核门、Outcome Center promotion adapter」部分，W3 明示「尚未实现」。
- 结论: VC-06 的 Outcome lineage 读模型已有；VC-07（Project closure）仍缺 Closure Package + close 门。

## Slice 5 · 历史/分页（VC-09）——W3 已覆盖

- `GET /api/projects`(limit/offset/total/has_more, JOIN-8)、`GET /api/inbox`(limit/offset, JOIN-10)、`GET /api/company-ops/outcomes`(cursor, JOIN-2)、`GET /api/issues`(limit/offset)；前端 `packages/views/outcomes/use-cursor-page.ts`。
- 结论: VC-09 服务端分页由 W3 提供。

## 剩余本 Goal 独有工作

1. **Closure Package 生成 + close 门（VC-07）**——W3 未实现，属本 Goal Slice 4 剩余项；合同已冻结于 contracts/SLICE-2-CONTROL-CONTRACT.md 的 close-preview / generate_closure_package 两行。
2. **WAVE-4 候选部署 + 运行时验收（VC-10）**。
3. **WAVE-5 组合处置 + 周期性 reconciler 自我运行（VC-08 收尾 / VC-12）**。
