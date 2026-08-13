**VERDICT: PASS**

复验完成。候选 tip: `b852af8a`（b852af8a5cfa06b81ef7f9cbd80afece44f9ba7b，work/hivecrew-project-lifecycle-closure，Repair #6，2 文件 +51/−2）。

## 修复核对（对上轮 re-review #5 的 phase_critical，确认闭环）
1. **Resume 不再重复建 task → 已修复**：`Resume` 复用 Continue 的 `activeTaskForIssue` 预检（project_lifecycle_control.go:243-247），frontier 已有 live task（`ListActiveTasksByIssue` 覆盖 queued/dispatched/running/waiting_local_directory，running 在内）→ `Replayed=true + 既有 task_id`，不入队；`ErrDuplicatePendingTask` 分支亦补 TaskID 回显（248-252）。旧唯一索引 `idx_one_pending_task_per_issue_agent` 只拦 queued/dispatched 的盲区被预检封住。
2. **独立探针（临时用例已删，工作树净）**：continue→置 running→pause→resume → receipt `Replayed=true`、TaskID 与首次 continue **同一 id**、任务数=1、项目状态回 `in_progress`。PASS。
3. **新增用例** `TestResumeDoesNotDuplicateRunningTask` 精确复现上轮探针场景，断言 Replayed + 任务数=1；建议后续补一行 `*r.TaskID==*first.TaskID` 断言把「同 task」钉死（非阻断）。
4. PreviewPause/PreviewResume：25ead4d0 已存在、当前 tip handler 接线完好、`TestProjectLifecycleAction_PausePreviewIsReadOnly` PASS——误删发生在编辑过程中、提交前已恢复，无可观测净差异。

## 验证证据（全部独立执行）
- 环境：隔离 Postgres 17.10 @ 127.0.0.1:55433，296 migrations，`977e3e8b..b852af8a` 仅 7 文件 +460/−52，**零 migration/schema/enum 改动**，数据真源维持。
- 纯逻辑：`TestClassifyProject|TestValidateProjectControl` → **14/14 PASS**（validateProjectControl 7 门全绿）。
- 服务层 DB：`TestResumeDoesNotDuplicateRunningTask|TestContinueCreatesSingleTaskAndNoDuplicate|TestEnqueueTaskForIssueReturnsDuplicateSentinel|TestPausedProject|TestPauseDispatchReplay|TestValidateProjectControl` → **15/15 PASS**。
- handler 层（隔离 DB）：`TestListProjectLifecycle|TestGetProjectLifecycle|TestProjectLifecycleAction` → **9/9 PASS**。
- 全量 service 包 PASS；`go build ./...` OK；go vet 仅剩既有 copylocks（task.go:1104/1257，非 Slice 2 引入，out of scope）。
- 负向矩阵：C2 缺 lead（`ContinueMissingLead`）/ C4 resume 需 paused（`ResumeRequiresPaused`）/ C12 duplicate（`DuplicateBlocks`）/ 幂等 replay（continue/pause/resume 三向）均有对应用例。

## Findings
**owner_decision**（多轮遗留，未变化，非阻断）
- idempotency_key 只回显不存储（C11 同 key 异 digest 无 409）；收据不持久化（C4 pause receipt 缺失门未实现）；`RecoveryOf` 永不填充；handler 所有服务错误一律 404 "project not found"。

**optional_improvement**（未变化）
- 暂停窗口内 `PromoteDueDeferredTasksForRuntime` 在 fireAt 到达时不复查项目状态；测试辅助 `validateProjectControlAt`/`parseTestUUID` 仍在生产文件。

## 结论
Repair #6 对上轮唯一 phase_critical（resume 重复建 task）修复到位：预检 + TaskID 回显 + 回归用例 + 独立探针四重证据一致，C1「已有等价 live task 幂等返回、不重复建 task」不变量对 resume 路径成立。修复量小、无新引入问题，本轮 **PASS**。