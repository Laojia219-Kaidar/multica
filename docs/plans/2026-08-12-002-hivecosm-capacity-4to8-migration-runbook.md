# 4→8 零中断容量迁移方案（候选 runbook）

> 状态：候选。**未执行**。执行前置：`active_task_count == 0`，且由 owner 明确授权。
> 违反任一条件即中止，不重启 daemon、不取消 Task。
> 关联：HIV-361（P0-C）。日期：2026-08-12。

## 0. 一次性前提（本工单候选已交付）
- 代码修复：`resolveDaemonMaxConcurrentTasksFlag` fail-closed（config 为真源）。
- 容量模型冻结：`docs/plans/2026-08-12-001-hivecosm-execution-capacity-model.md`。
- 动态路由合同：`docs/plans/2026-08-12-003-dynamic-capacity-routing-contract.md`。

## 1. 当前状态快照（2026-08-12 22:06）
- daemon PID 93116，`--max-concurrent-tasks 20`（重启后），active_task_count=8。
- config.json `max_concurrent_tasks=20`（被隐式覆盖过，多次变更：6→8→20）。
- 22 个 Runtime 在线。本次迁移目标：**先把真源钉到 8**（受控验证），失败自动回 6。

## 2. 执行步骤（仅当 active_task_count == 0）

```bash
# 0) 门禁：必须 0 个 active task，否则中止
multica daemon status --output json | jq -e '.active_task_count == 0'

# 1) 基线采样（CPU/内存/句柄/版本）
ps -o pid,%cpu,%mem,nlwp -p $(cat ~/.multica/daemon.pid)
lsof -p $(cat ~/.multica/daemon.pid) | wc -l
multica daemon status --output json

# 2) 持久化真源：8（唯一合法变更通道）
multica config set max_concurrent_tasks 8

# 3) 受控重启（不带 flag —— flag 与 config 一致才允许，否则 fail-closed）
multica daemon restart
multica daemon status --output json | jq -e '.status == "running"'

# 4) 观察窗（建议 30–60 分钟），记录：
#    - CPU/内存/文件句柄（同上）
#    - Task 成功率：daemon.log 中 status=completed / (completed+cancelled+failed)
#    - 取消残留：grep 'status=cancelled' ~/.multica/daemon.log | wc -l
#    - provider 限流：grep -iE '429|rate.?limit|throttl' ~/.multica/daemon.log
#    - worktree 冲突：grep -iE 'worktree.*(lock|conflict|busy)' ~/.multica/daemon.log
```

## 3. 自动回退（失败触发，回 6）

```bash
# 任一信号触发：成功率 < 90%、取消残留突增、429 频发、句柄/内存超基线上限
multica config set max_concurrent_tasks 6
multica daemon restart
multica daemon status --output json | jq -e '.status == "running"'
```

## 4. 回滚（回 4 或恢复原状）
```bash
# 回 4：同样走 config 真源
multica config set max_concurrent_tasks 4 && multica daemon restart
# 或整文件回滚（先备份）：
cp ~/.multica/config.json ~/.multica/config.json.bak.$(date +%Y%m%d%H%M%S)
```

## 5. 验收证据（完工后必须回帖）
- 迁移前后 `daemon status` JSON（pid、max_concurrent_tasks、active_task_count）。
- 基线 vs 观察窗的 CPU/内存/句柄。
- 观察窗内 Task 成功率、取消残留、429、worktree 冲突计数。
- 若回退/回滚：触发信号与最终落点。

## 6. 明确不做
- 不升 32（动态额度路由合同批准前）。
- 不在 active_task_count>0 时执行任何步骤。
- 不取消任何现有 Task。