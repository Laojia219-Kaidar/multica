# 动态额度路由可执行合同（候选）

> 状态：候选，仅定义合同，**不实现**。本工单不升 32。
> 消费方：后续动态额度路由实现（如 HIV-362 CodeTeam 引擎、P0-D 并行施工）。
> 关联：HIV-361（P0-C）。日期：2026-08-12。

## 1. 目标
在 L0–L4 容量模型（见 `2026-08-12-001-hivecosm-execution-capacity-model.md`）之上，
为「按需把并发额度路由给具体 provider/plan/account/Runtime/agent」定义可执行、
幂等、可审计的接口合同。本工单只冻结合同，不实现路由本体。

## 2. 合同形态：Config 派生 + 只读观测，不新增第二控制面
- 唯一写入通道仍是 `multica config set max_concurrent_tasks N`（L0 真源）。
- 路由器（未来）只做**预算内调度**：在 L0 已分配额度内，决定「哪个 Run 用哪个
  provider 池」，不改变 daemon 槽位总数。
- 所有观测数据（active_task_count、per-provider 并发、429 计数、取消残留）来自
  正式 daemon 的 `status`/日志与 server 的 Task/Run 真源，不另建状态库。

## 3. 输入（路由决策所需，全部只读）
```json
{
  "as_of": "2026-08-12T22:00:00Z",
  "machine": {
    "max_concurrent_tasks": 8,
    "active_task_count": 3,
    "cpu_percent": 12.4,
    "mem_percent": 31.2,
    "open_fds": 214
  },
  "providers": [
    {"provider": "qwen", "running": 2, "rate_limited_last_10m": 0},
    {"provider": "kimi", "running": 1, "rate_limited_last_10m": 0}
  ],
  "worktrees": [
    {"path": "hivecrew-p0c-capacity-keep", "writer": "task:abc", "readers": ["task:def"]}
  ]
}
```

## 4. 决策输出（路由器的唯一合法输出）
```json
{
  "decision": "defer" | "grant" | "reject",
  "reason": "stable reason string",
  "grant": {
    "task_id": "…", "provider": "qwen", "pool_slots": 1,
    "expires_at": "2026-08-12T22:15:00Z", "idempotency_key": "…"
  }
}
```
- `defer`：池满/无槽，进入队列（不取消、不覆盖）。
- `grant`：仅当 L0 未满 **且** 目标 provider 池未满 **且** worktree 无写者冲突。
- `reject`：fail-closed（如 worktree 被写者租约占用、provider 限流中）。
- 幂等：同 task_id + provider 的重复决策返回同一 grant（复用 idempotency_key）。

## 5. 验收标准（实现方必须满足）
1. 不改变 daemon 槽位总数（L0 由 config 真源独占）。
2. 不创建第二任务/审核权威；Task/Run 回执仍走正式真源。
3. reviewer ≠ author；review 槽走 L4 分层。
4. 全部决策日志可审计（who/when/why/outcome）。
5. 覆盖 32 之前必须先通过 4→8→16 的受控验证（每档观察窗 ≥ 30 分钟）。

## 6. 本工单明确不做
- 不实现路由器本体、不写 provider 侧、不升 32。
- 不触碰 DGX/1421/Goal/Registry、默认 5432、正式 3000 部署。