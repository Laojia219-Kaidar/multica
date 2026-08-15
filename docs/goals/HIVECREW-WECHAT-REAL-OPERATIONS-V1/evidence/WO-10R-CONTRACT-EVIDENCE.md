# WO-10R — 合同修复与冻结证据（EV-CONTRACT）

> Goal: HIVECREW-WECHAT-REAL-OPERATIONS-V1 · Work Order: WO-10R
> Actor: accountable executor（Kimi Code carrier, host jiaweis-Mac-mini.local），非 Coco、非 Codex
> Observed at (UTC): 2026-08-15T09:30:00Z
> Worktree: /Users/jiawei/hivecosm-worktrees/hivecrew-operations-workflow-v2
> Branch: work/hivecrew-operations-workflow-v2
> Base revision: 62e6cf517f9c45123d0c792e20710184da994dcb（WO-10R 改动在此基线的工作树上，commit hash 见本节末）

## 修复内容（对照 NEEDS_REPAIR 九项）

1. `handoff_note` 三层必填（trim 非空、≤ 32 KiB UTF-8），与 server/internal/handler/companyops.go:669-674、server/internal/service/companyops_assignment.go:240-246 语义一致；`input_digest` 加入 12 项禁止调用方字段清单并有专项测试。
2. `source_refs` 三层均要求至少一个、每项 trim 非空。
3. TS 纯校验器 `scanForbiddenProofKeysDeep` 递归拒绝任意深度伪造证明；authority/definition/brief/lineage 未知字段 fail-closed（`scanUnknownKeys`）。
4. Lineage 六成员 authority 冻结为 `WECHAT_CONTENT_LINEAGE_AUTHORITIES` 合同常量；TS 逐成员比对、Zod 每成员 `z.literal`、Go 冻结 struct 整体比较。
5. Go `RequiredUpstream *WechatContentNodeKey` 指针可空语义；`TestWechatContentFirstNodeUpstreamIsJSONNull` 双向证明 JSON null ⇔ nil。
6. deadline 三层统一 RFC3339：Zod `z.iso.datetime({ offset: true })` + superRefine 兜底；TS `isValidRfc3339Datetime`（形状 + 真实日历 + offset 范围）；Go 形状正则 + offset 范围（strconv 显式 00:00-23:59）+ time.Parse 三重校验。
7. Go strict decoder `wechat_content_contract_decode.go`：raw 深扫伪造 proof → `DisallowUnknownFields` strict decode → 纯校验器。
8. Go 专项测试 `wechat_content_contract_test.go` 覆盖全部要求项。
9. 请求回执 `WechatContentProductionRequestReceiptSchema` 仅含 request_id/idempotency_key/accepted/replayed/reason，无任何执行/产物证明字段。

## 独立复审记录（REV-WO10R）

- 第一轮（独立只读 reviewer，explore subagent，2026-08-15T09:20Z 前后）：**NEEDS_REPAIR**。唯一阻塞项：Go `time.Parse(time.RFC3339Nano)` 不范围检查数值 offset，Go 层接受 `+24:00`/`+08:60` 而 TS/Zod 拒绝，三层 parity 破裂。复审员用真实 Go 代码副本走 `DecodeWechatContentProductionRequestJSON` 完整管线端到端证实。
- 修复：Go 增加 offset 范围显式校验（`wechatRFC3339OffsetPattern` + strconv 范围检查），三层测试同补 `+24:00`/`+08:60`/`+23:59` 用例。
- 第二轮回归复审（同一 reviewer）：**PASS**。15 值 offset/日历边界矩阵三层一致；合法请求三层均接受；非法请求三层均拒绝。
- 复审员记录的 optional_improvement（不阻塞）：TS plan 条目 omission-tolerant 宽于 wire 两层（接受时返回 canonical plan，非 fail-open）；Zod superRefine 兜底 10 位小数秒是 parity 的一部分；`.trim()` 改写 wire 落库值；Go 尾部 JSON 由 raw unmarshal 覆盖。

## 测试证据（全部本工作树实测）

| 命令 | exit | 结果 |
|---|---|---|
| `cd packages/core && pnpm exec vitest run workflow api/workflow.test.ts` | 0 | 3 files, 44 tests passed |
| `cd packages/core && pnpm exec tsc --noEmit` | 0 | clean |
| `cd server && gofmt -d internal/workflow` | 0 | 0 行 diff |
| `cd server && go vet ./internal/workflow/...` | 0 | clean |
| `cd server && go test ./internal/workflow/... -count=1` | 0 | ok（含 10 个 WeChat 测试函数全 PASS） |
| worktree 根 `git diff --check` | 0 | clean |

## 变更文件（WO-10R 写范围内）

- packages/core/workflow/content-node-contract.ts（修复）
- packages/core/workflow/content-node-contract.test.ts（修复+新增用例）
- packages/core/workflow/index.ts（既有 1 行 export，保留）
- packages/core/api/workflow.ts（WeChat Zod 区段修复）
- server/internal/workflow/types.go（WeChat 区段修复）
- server/internal/workflow/wechat_content_contract_decode.go（新增 strict decoder）
- server/internal/workflow/wechat_content_contract_test.go（新增专项测试）

## 保护边界确认

- apps/web/next-env.d.ts、packages/views/workflow/workflow-designer.tsx、workflow-designer.test.tsx：未触碰（git diff 行数与接班基线逐字一致：2 / 88 / 131）。
- 无 migration、router、client、CompanyOps service 写入；无 queue/table/registry/Outcome Center 新增。

## Commit

见本文件提交后 `git log -1`（WO-10R 原子 commit，stage 仅上述命名文件 + CHECKLIST.yaml + 本证据文件）。
