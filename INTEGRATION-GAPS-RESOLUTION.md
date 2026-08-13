# INTEGRATION-GAPS-RESOLUTION — 差距三/四/五 处置与收敛建议

> 状态：差距一（接线）+ 差距五（前端入路由）已落地代码；本文档交付差距三/四/五的分析、契约与边界。

## 差距三：迁移错位（双线关系 + 收敛建议）

### 定位
- 两线共同 merge-base = `f7667c8d7`（即 `main`）。
- A 线 `work/hivecrew-product-integration-mainline`（唯一主线，William 确认）：领先 28 commit；迁移 280-285、320、340-342。
- B 线 `work/hivecrew-bases-v1.1`（W1 bases）：领先 5 commit；迁移 400_workroom、401_employee、402_dataset、403_dataset_product_type。

### 错位风险
1. **workroom 特征重复**：A 的 `341_workroom` 与 B 的 `400_workroom` 是**同一张表、SQL 逐字相同、仅编号不同**。合并时必然二选一。
2. **迁移编号冲突**：A 用 342 截止，B 用 400-403，两套单调计数互相打架（D3）。
3. **17 个共享文件需合并**：`workroom.go`、`router.go`、`client.ts`、`models.go`、`workrooms/*`、`app-sidebar.tsx`、locales、`paths.ts`。
4. **生产库已到 402（B 线编号）**：生产库 `schema_migrations` 里是 400-403 的 B 线编号，而生产代码/镜像来自 A 线（342）。当前 workroom 表结构两者一致所以未断，但**编号体系已分裂**，后续迁移会在生产库撞号。

### 收敛建议（不单方面改生产库）
1. **唯一主线 = A 线**（William 已确认）。
2. 将 B 线 5 commit **rebase 到 A 线**：
   - 丢弃 B 的 `400_workroom`（A 已有 341 同物）。
   - B 的 `401_employee`→`343`、`402_dataset`→`344`、`403_dataset_product_type`→`345`。
   - 手工合并 17 个共享文件（尤其 workroom.go / router.go / models.go / client.ts）。
3. **生产库编号回填**：把 `schema_migrations` 里 400/401/402/403 的 B 线记录，按收敛后编号（341/343/344/345）改写；或记录为「历史编号映射」并在收敛分支里加一个 `345_*` 之前的 no-op 对齐迁移。
4. 由主控（codex-unbound / W3）执行合并 + 生产库对齐；本 worker 不单方面改生产库、不单方面 merge B 线。

## 差距四：D5 正式知识写入适配器（contract + 边界）

### 边界声明
- HiveCrew 记忆系统只持有 **candidate 层**（MemoryCandidate/MemoryPromotion/Revocation，migration 342）。
- **正式公司知识/团队 Playbook/Skill 的写入归 HiveCosm Knowledge/Harness 权威**（William 已确认 D5）。
- `MemoryPromotion.Approved=true` 只是 **proposal receipt**，绝不直接写 HiveCosm 知识。

### 适配器 contract（HiveCrew → HiveCosm Knowledge）
```
PromotionOutbox(source=HiveCrew, candidate_id, target, reviewer_id, reason)
  -> HiveCosm Knowledge 入站命令：
     propose_promotion(candidate_id, target ∈ {employee_memory|team_playbook|skill},
                       evidence_refs[], author_id, reviewer_id, reason)
  <- 回执：{status: accepted|rejected, knowledge_id?}
```
- 实现前需要 HiveCosm Knowledge/Harness 的入站 API 契约（当前我无该接口）。
- 待接口到位后，落地为 `internal/memorypromotion` 出站适配器 + 幂等 key = candidate_id，失败重投、不重写正式知识。

## 差距五：SSE Last-Event-ID / 断线补偿评估

- 现状：`/api/work-wall/stream` 每 5s 推全量 snapshot；客户端重连即开新循环，天然去重。
- 评估：当前全量 snapshot 已覆盖断线补偿的正确性（不丢不重），仅带宽非最优。
- 建议：员工数 >~200 或 snapshot 体积 >100KB 时再上 `Last-Event-ID + 增量 delta`（按事件序号补偿）；否则维持全量快照。**优先级低，暂不实现。**
