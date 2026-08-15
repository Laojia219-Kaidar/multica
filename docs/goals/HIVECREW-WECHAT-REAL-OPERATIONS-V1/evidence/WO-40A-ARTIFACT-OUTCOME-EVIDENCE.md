# WO-40A — Artifact/Outcome 与审批提升桥：候选级证据

- Work order: WO-40A（WO-40 的候选代码半区；WO-40B 提升+Outcome Center 回读证据仍在 WO-50 canary 后执行）
- Controller: Kimi Code（单一 active controller）
- Revision: `ce06b5557`（test commit；此前 HEAD `99fdebfe1`）
- Observed at: 2026-08-15T14:05:00Z（UTC）
- Verdict: **candidate_verified — 无生产代码缺口（minimal-change 结论成立），deliverable = 切片级验证测试**

## 1. 缺口判定（gap determination）

调研对象：`server/internal/companyops/artifact_lifecycle.go`（全文）、`server/internal/service/companyops_artifact_outcome.go` 的 `ReviewArtifact`/`PromoteArtifact` 实现、既有 21 个 outcome 集成测试与 6 个 lifecycle 单测。

既有机制已覆盖（本次逐条复核源码确认）：

| 需求 | 既有机制 | 位置 |
|---|---|---|
| 审批门控提升 | 无 approved 锚事件即拒绝（"approved decision is unavailable"），不 POST | `companyops_artifact_outcome.go:628-631` |
| 退回阻断 | `changes_requested` 后状态机无任何出边；同 revision approved/promotion_requested 均 `ErrInvalidArtifactTransition` | `artifact_lifecycle.go:272-287`（switch 无 changes_requested case） |
| 退回自动备返工 Run | 事务内按 rejection event 备 task，trigger evidence 钉住 event ID，重放塌缩 | `companyops_artifact_outcome.go:506-537` |
| 每节点独立 lineage | candidate 必须等于该 issue 当前候选，跨 issue 即 `ErrArtifactCandidateNotFound` | `companyops_artifact_outcome.go:471, 614-616` |
| 提升与派发回执钉定 | 四快照逐字段比对 outcome.Target（该 issue 最新派发） | `companyops_artifact_outcome.go:684-709` |
| 候选 ID 不匹配 fail-closed | review 与 promote 双入口均先验 candidate 再验其他 | 同上 |

**结论：VC-03/VC-04 的强制机制在既有生产代码中已存在，WO-40A 无需生产代码改动。** 真正的缺口是测试覆盖：service 层 `changes_requested` 决策路径零测试（`companyops_artifact_outcome_test.go` 中该词出现 0 次），跨节点 lineage 隔离无等价断言。故 deliverable 为候选级验证测试。

## 2. 本次新增测试（全部通过）

### 2.1 `server/internal/companyops/artifact_lifecycle_test.go`（纯内存，无 DB）

- `TestArtifactLifecycleChangesRequestedBlocksSameRevisionProgression`：submitted→changes_requested 后，同 revision 的 approved / promotion_requested / promotion_succeeded / 重复 submitted 全部 `ErrInvalidArtifactTransition`，事件账本零增长，投影保持 changes_requested 且 FormalVisible=false；外来 candidate（另一节点 lineage）注入 approved 即 `ErrArtifactCandidateNotFound`。

### 2.2 `server/internal/service/companyops_artifact_outcome_test.go`（隔离候选库集成）

前置：候选 Postgres `127.0.0.1:55432`（共享隔离实例 `hivecrew-b2-postgres`），库 `multica_hivecrew_operations_workflow_v2_512` 新建并跑完 1→370 全部 up 迁移。

- `TestCompanyOpsArtifactOutcome_ChangesRequestedBlocksPromotionAndQueuesRework`（VC-04）：
  - 空 Feedback 的退回被拒（ErrCompanyOpsArtifactConflict）；
  - 退回成功：event type=changes_requested；同一 issue 上恰好备一个返工 Run，`trigger_evidence_kind=artifact_revision`、ref=rejection event ID；
  - 同 IdempotencyID 重放：返回同一 event、同一返工 Run（不重复建）；
  - 退回后 PromoteArtifact → ErrCompanyOpsArtifactConflict，fake 权威 promote/read 计数保持 0（未触碰外部权威）；
  - 退回后同 revision 补 approved → ErrInvalidArtifactTransition；
  - GetIssueOutcome 投影=changes_requested、FormalVisible=false。
- `TestCompanyOpsArtifactOutcome_SliceNodeLineageIsolation`（VC-03）：
  - 同 workspace 双节点（issue A/B，B 的派发携带不同 WorkOrder revision/digest）各自物化候选；
  - 双向跨 lineage review（A 候选过 B issue、B 候选过 A issue）均 ErrArtifactCandidateNotFound；
  - B 候选经 A issue 提升 → ErrArtifactCandidateNotFound，权威零调用；
  - B 在本 issue 审批通过、但携带 A 的派发快照提升 → ErrCompanyOpsArtifactConflict（与 B 最新派发回执不符），权威零 POST；
  - 对照：A、B 各自经本 issue+本派发快照提升至 authority_readback_confirmed，fake 权威 POST 计数恰好 2（每 lineage 一次）。

helper 重构：`companyOpsArtifactPromotionRequest` 委托给新 `companyOpsArtifactPromotionRequestForTarget`，行为不变（既有 21 用例全绿）。

## 3. 测试执行记录

```
go test ./internal/companyops/ -count=1                                    ok 0.310s
DATABASE_URL=<候选库> go test ./internal/service/ -run CompanyOps -count=1  FAIL（1 个既有基线失败，见 §4）
```

## 4. 既有基线失败（归因：非本次改动）

- `TestCompanyOpsOutcomeListCountAndCursorRejectCrossWorkspaceJoins`：`insert foreign agent: null value in column "runtime_id" of relation "agent" violates not-null constraint (SQLSTATE 23502)`。该用例向 agent 表插入行时未给 runtime_id，与当前迁移后的 schema（agent.runtime_id NOT NULL）不兼容。**已在 HEAD（stash 掉本次改动）复跑同样失败**，与本提交无关。列入 JOIN-1 归因清单。
- 既有 gofmt 基线：`internal/service/companyops_outcome_cursor_test.go`、`empty_claim_cache.go`、`issue.go` 在 HEAD 上即未格式化（stash 验证）；`go vet` 对 `task.go:1104,1257` 的 lock-copy 告警同为既有。本次改动文件 gofmt/vet 干净。

## 5. Slice limits（诚实边界，WO-50 接缝说明）

1. **四节点按成对隔离证明**：测试用 2 条 lineage 证明机制（候选绑定 per-issue），四节点 slice 的任意跨对由同一机制约束；未逐一跑 C(4,2)=6 对。
2. **返工 Run 与 orchestrator 视角的接缝**：orchestrator（WO-30）视角 node 已 completed（旧候选），ReviewArtifact 自动备的返工 Run 跑在同一 issue 上、orchestrator 不感知；新 revision 物化后需下一轮 reconcile 才可能接管——此接缝行为属 WO-50 canary 实测范围，本切片不改代码。
3. **重放 payload 不含 Feedback**：幂等键重放仅按 event 载荷（type/candidate/revision/digest/objectRef）匹配，同键不同 Feedback 会塌缩为原事件而不报冲突。属既有设计，记录备查。
4. **live 提升与 Outcome Center 回读**：fake 权威仅计数；真实 loopback 权威 + Outcome Center 跨读回证据 = WO-40B/WO-50。
5. 本证据为候选级；P0-GATE-04 的 deferred 断言 2/3/4/6/8 仍在 WO-50 canary 执行。

## 6. 复现

```bash
docker start hivecrew-b2-postgres   # 共享隔离实例 127.0.0.1:55432
cd server
DATABASE_URL='postgres://multica:multica@127.0.0.1:55432/multica_hivecrew_operations_workflow_v2_512?sslmode=disable' \
  go run ./cmd/migrate up
go test ./internal/companyops/ -count=1
DATABASE_URL='postgres://multica:multica@127.0.0.1:55432/multica_hivecrew_operations_workflow_v2_512?sslmode=disable' \
  go test ./internal/service/ -run 'TestCompanyOpsArtifactOutcome_(ChangesRequestedBlocksPromotionAndQueuesRework|SliceNodeLineageIsolation)' -count=1 -v
```
