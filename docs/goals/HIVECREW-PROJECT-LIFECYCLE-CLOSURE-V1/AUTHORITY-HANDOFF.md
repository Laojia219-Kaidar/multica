# AUTHORITY-HANDOFF · 真实 Owner→Outcome 纵切（阻塞在外部 HiveCosm Authority）

> 状态：`BLOCKED_EXTERNAL_AUTHORITY_NOT_DEPLOYED`
> 生成时点：2026-08-14T01:09:37.382228+00:00

## 1. 当前状态定性（严格区分）

- **候选完成**：HiveCrew 消费端代码、compose 接线、回滚点、本地/隔离测试。
- **未完成（阻塞）**：真实 HiveCosm Authority 联调、Formal Artifact Promotion、生产验收。
- **不得表述**：本地 fixture/stub 测试（`TestOwnerToOutcomeMinimalSlice` 用 fake formal authority）不得标为「真实正式成果」。

## 2. revision / commit

- 分支：`work/hivecrew-project-lifecycle-closure`
- 最终 revision：`71f21ec3ee7cf82f0dd5ce041df713b6bda9e707`（短 `71f21ec3e`）
- 相对 main（`f7667c8d`）共 **38 个 commit**；已发布 canonical DGX 仓库
  `dgx-hive-dev:/srv/hivecosm/12-development-workspaces/users/williamdev/repos/hivecrew.git`

## 3. 改动文件清单（相对 main）

- 后端：`server/cmd/server/main.go`、`server/cmd/server/router.go`、
  `server/internal/service/project_lifecycle.go`、`project_lifecycle_control.go`、
  `project_lifecycle_reconciler.go`、`task.go`（+ 同目录 `_test.go`）、
  `server/internal/handler/project_lifecycle*.go`（+ tests）、
  `server/internal/scheduler/jobs_project_lifecycle.go`
- 数据：`server/migrations/342_project_lifecycle_receipt.{up,down}.sql`、
  `server/migrations/343_closure_package_review.{up,down}.sql`、
  `server/pkg/db/queries/project_lifecycle.sql`、`project_lifecycle_receipt.sql`、
  `closure_package_review.sql`、`server/pkg/db/generated/*`
- 前端：`packages/core/{api,projects,types}/*`、
  `packages/views/projects/components/{project-health.tsx,project-health.test.ts,project-control-actions.tsx,projects-page.tsx,project-detail.tsx}`、
  `packages/views/locales/*/projects.json`
- 运维：`docker-compose.selfhost.yml`（+3 行 HIVECOSM_* 透传）
- Goal bundle：`docs/goals/HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1/**`

## 4. 测试命令与结果

| 命令 | 结果 |
|---|---|
| `cd server && go build ./...` | 0 |
| `go test ./internal/service -count=1` | 全绿（classifier/control/reconciler/companyops 管线） |
| `go test -race ./internal/service -run 'TestClassifyProject|TestValidateProjectControl|TestProjectLifecycleReceiptConflict|TestPauseDispatchReplay|TestContinueCreatesSingleTaskAndNoDuplicate|TestStopCurrentCancelsLiveTasks|TestReviewClosurePackageSatisfiesReviewGate'` | 绿 |
| `DATABASE_URL=postgres://...:55433/multica go test ./internal/handler -count=1` | 全绿 |
| `DATABASE_URL=postgres://...:55432/multica go test ./internal/service -run TestOwnerToOutcomeMinimalSlice` | 绿（fixture/stub authority，证明链路可写 5 张台账） |
| `pnpm --filter @multica/views typecheck` | 0 |
| `pnpm --filter @multica/views exec vitest run projects/components` | 32/32 |
| `pnpm --filter @multica/web build` | 成功 |

## 5. compose / env 变量名（消费端已接线，等真实值）

- `HIVECOSM_AUTHORITY_BASE_URL`（必填，绝对 HTTP(S)，不带 userinfo/query/fragment）
- `HIVECOSM_AUTHORITY_BEARER_TOKEN`（必填）
- `HIVECOSM_TENANT_ID`（可选，目录服务用）
- 注入方式：`docker-compose.selfhost.yml` backend `environment:` 已透传；`.env` 已留空占位。
- **安全要求**：token 不得在聊天/Issue/提交中明文出现；经受控 env / keychain / secret reference 注入，由进程读取。

## 6. 回滚方法

- 镜像回滚：`docker tag multica-backend:dev-pre-lifecycle multica-backend:dev`、`multica-web:dev-pre-lifecycle multica-web:dev` 后 `docker compose up -d`。
- DB 回滚：快照 `/tmp/hivecrew-live-backup-20260814072111.sql`（pg_dump，live 切换前）。
- 代码回滚：DGX 分支可回到任意历史 commit。

## 7. Authority 需要满足的接口合同（部署后 HiveCrew 会调）

- `GET/POST {BASE_URL}/api/company-ops/owner-work-context`（owner-work-context 权威解析，schema `hivecosm.owner-work-context.authority.v1`）
- `POST {BASE_URL}/api/company-ops/formal-artifacts/promotions`（formal promotion）
- `GET {BASE_URL}/api/company-ops/formal-artifacts/{ref}`（formal readback）

（消费端实现：`server/internal/companyops/hivecosm_authority_client.go`、`hivecosm_formal_artifact_client.go`。）

## 8. 服务部署后的一键验证

```bash
curl -s "http://127.0.0.1:8080/api/company-ops/work-context?work_order_source_ref=<real-wo>&employee_id=<real-emp>&identity_binding_id=<real-bind>&agent_id=<real-agent>&session_id=<real-session>"   -H "Authorization: Bearer $HIVE_CREW_TOKEN" -H "X-Workspace-Slug: hivecosm"
# 期望：不再返回 "HiveCosm authority adapter is not configured"；返回结构化 work-context。
```

## 9. 当前 blocker 与 receiver

- **Blocker**：`BLOCKED_EXTERNAL_AUTHORITY_NOT_DEPLOYED`
- **Receiver**：HiveCosm Authority 建设/集成车道（Authority 候选完成独立验收、合入并部署后，由集成负责人回传：正式 base URL、secret 引用位置、tenant ID、健康检查与联调窗口）。
- 收到后动作：真实重建后端 → 真实纵切（真实 WorkOrder/Employee/Binding）→ 真实 Formal readback 验证。
