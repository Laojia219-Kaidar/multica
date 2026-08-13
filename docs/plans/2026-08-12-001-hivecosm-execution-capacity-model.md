# HIVECOSM 执行容量模型（冻结版）

> 状态：候选（candidate），等待独立复审。本文件冻结 HiveCosm 唯一执行容量模型；
> 不部署正式服务，不写入 3000/5432/DGX/1421。
> 关联：HIV-361（P0-C）。日期：2026-08-12。

## 1. 问题实证（2026-08-12 现场）

- 正式 daemon 曾以 `--max-concurrent-tasks 4 --no-auto-update` 启动（PID 84425），
  配置文件 `max_concurrent_tasks=6`，代码/文档默认 20，运行时 4/4 占满。
- 22:04:17 正式 daemon 被重启（`--max-concurrent-tasks 20`），当场取消 4 个 in-flight
  Task（含本议题上一轮运行），22 个 Runtime 全部注销后重新注册。
- 根因：**显式 flag 覆盖持久化配置**。`--max-concurrent-tasks 4` 的来源是启动命令本身
  （无 launchd plist、无 cron、无 rc 脚本、无 wrapper 脚本），任何后台脚本/agent 重启
  daemon 时都能用 flag 隐式改变容量而不留痕。
- 结论：`max_concurrent_tasks` 的单一真源必须是 `~/.multica/config.json`（持久化配置），
  flag 只能与之一致（或首次引导），禁止隐式覆盖。

## 2. 容量模型（唯一模型，五层）

### L0 机器安全总上限（Machine Safety Ceiling）
- 定义：单台执行机（HiveCosm Mac mini）在同一时刻允许的**总并发放号**。
- 取值：`max_concurrent_tasks`（daemon 级，默认 20，本机当前 20）。
- 约束：`config set max_concurrent_tasks` 为唯一合法变更通道；`daemon start/restart`
  不允许用 flag 越过它（`resolveDaemonMaxConcurrentTasksFlag` fail-closed）。
- 依据：CPU/内存/文件句柄实测基线，非员工数量。

### L1 Provider / Plan / Account / Runtime 容量池
- 定义：按 provider（qwen/kimi/codex/opencode/coze/hermes/…）、plan、account、
  Runtime 各自独立的并发预算池。
- 约束：池间独立、互不挪用；每个池的上限低于 L0，且池之和可超过 L0（由 L0 收口）。
- 本机现状：7 个 qwen-hive-* profile command override + kimi/codex/opencode 等
  9 个 agent 族，22 个 Runtime 在线 —— 池预算按 provider 族核算，不按员工数。

### L2 Agent 上限（单 Agent 并发）
- 定义：单个 agent（数字员工）自身的 `max_concurrent_tasks`（1–50，默认 6）。
- 约束：与 L0 是**双层独立 gate**（daemon 槽位 semaphore + agent 级 gate），
  任一先到即拒绝。员工数量**不得**直接推导并发数。

### L3 Canonical Worktree 单写者租约
- 定义：每个隔离 worktree 同一时刻只允许一个写者（author）持有；其余为只读。
- 约束：写者租约与任务并发解耦；并发升高时禁止两个写者同树互踩。

### L4 只读 Review 槽分层
- 定义：review（复审）占用独立的只读槽层，与 L0 写槽并列但计数分离。
- 约束：reviewer ≠ author；review 槽不挤占写槽，反之亦然。

### 铁律
- 员工数量 ≠ 并发数。并发数由 L0–L4 资源事实决定，与组织人数无关。
- 动态额度路由（未来）只允许在这五层之上做**预算内**调度，不得突破 L0。

## 3. 变更通道（唯一）
1. `multica config set max_concurrent_tasks N`（持久化，L0 唯一真源）。
2. `multica daemon restart`（**不带 flag**）→ daemon 从 config 读取 N。
3. 任何 `daemon start/restart --max-concurrent-tasks N` 与 config 不一致 → 拒绝启动。

## 4. 边界与不改的东西
- 不改 agent 级 `n`（L2 仍走 agent 自身语义）。
- 不改 provider 池的远端限流（L1 只建模，不写 Provider 侧）。
- 不在本工单直接升到 32（32 需走动态额度路由合同，见
  `docs/plans/2026-08-12-003-dynamic-capacity-routing-contract.md`）。