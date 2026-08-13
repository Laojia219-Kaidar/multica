# RESUME-AND-OPS-V1 — W4 工作墙/工作流/记忆 操作与跨窗口恢复手册

> 分支：`work/hivecrew-w4-slice-w2`（base = 集成 mainline `afc4ad7d0`，fast-forward 可合并）
> 本文件是操作/恢复说明，不是执行真源；执行真源 = 主线 CHECKLIST.yaml。

## 1. 已交付（全部验证）

| 模块 | 代码 | 验证 |
|---|---|---|
| 工作墙后端 | `internal/liveactivity`（派生/DTO/脱敏/事件协议）、`internal/workwall`（聚合/活动桥/工作流桥）、`internal/handler/workwall.go`+`workwall_stream.go` | API E2E(401→200,33员工)、SSE 实测、真实 DB 集成 |
| 工作墙前端 | `packages/core/api/workwall.ts`、`packages/views/workwall/`、`apps/web/.../work-wall/page.tsx` | 浏览器 E2E(33 终端卡)、typecheck/build |
| 工作流内核 | `internal/workflow`（引擎/SLA/并发/生命周期模板/bridge/repository/hydrate） | 13 用例含 -race + 集成 |
| 员工记忆 | `internal/memory`（候选层/岗位/Skill投影/检索/撤销/repository/hydrate） | 15 用例含 -race |
| 持久化 | `migrations/342_workflow_memory_persistence.{up,down}.sql` + sqlc queries | 迁移 up/down/up + round-trip + hydrate |

## 2. 合并（1 命令，fast-forward 已确认无冲突）

```bash
cd /Users/jiawei/hivecosm-worktrees/hivecrew-product-integration-mainline
git merge --ff-only work/hivecrew-w4-slice-w2
```

## 3. 部署（gated：W3 + Owner 授权，本分支不自行部署）

1. 统一镜像构建（W3，含 /work-wall 运行面）。
2. 切换生产 :8080（Owner 授权）。
3. 部署后按 `E2E-VERIFICATION-PLAN.md` 做 runtime API + 浏览器验收。

## 4. 操作要点

- **工作墙**：`GET /api/work-wall/snapshot`（RequireWorkspaceMember）；`GET /api/work-wall/stream`（workspace 级 SSE，5s snapshot 推送）。页面 `/hivecosm/work-wall`，5s 轮询兜底。
- **工作流**：`Engine`（内存）+ `Repository`（持久化）+ `Hydrate`（恢复）。风险门 FAST/STANDARD/OWNER；STANDARD 需独立 review，OWNER 需 owner 批准。
- **记忆**：`Store`（候选）→ Validate → Promote（proposal receipt，独立 reviewer）→ Retrieve（仅已验证）→ Revoke（撤销不命中）。四类型：工作/经历/经验/Skill-proposal。
- **集成测试 DB**：`hivecrew-w4-slicew2-db`（55443，migrations 342）；复跑命令见下。

```bash
# 复跑集成测试
cd server
DATABASE_URL="postgres://multica:multica@127.0.0.1:55443/multica?sslmode=disable"   go test -tags integration ./internal/workflow/ ./internal/memory/ -count=1
```

## 5. 剩余决策/动作

1. **合并**：§2 命令（W3/Owner）。
2. **部署**：§3（W3 + Owner）。
3. **D5 知识晋升**：记忆 promotion 到正式公司知识/Skill，proposal-only 已建，正式写入需 HiveCosm Knowledge/Harness 适配器 + Owner 授权。

## 6. 边界声明

全程零共享路径改动（除 SSE 路由 1 行在本分支 router.go）、零生产触碰、零 secret。代码完成 ≠ 落地。
