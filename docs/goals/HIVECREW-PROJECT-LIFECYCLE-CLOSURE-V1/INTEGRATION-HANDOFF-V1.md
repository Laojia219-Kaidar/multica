# INTEGRATION-HANDOFF-V1 · Slice 1 → W3 集成交接

> 目的：把已通过三方独立验收的 Slice 1 候选，以冻结形式交接给 W3（集成/候选部署/运行时验收）。本文件是 Slice 1 的只读冻结快照，不再随 Slice 2 演进。

## 1. 冻结候选 revision

- **分支**: `work/hivecrew-project-lifecycle-closure`
- **最终 revision**: `726bcca4a11c702a112514834fe1b86d5afe4142`（短 `726bcca4a`）
- **修复链**: `0eeb7a05b`(后端) + `74bb1fe31`(前端) → `b954e7e5c`(Repair #1) → `90d7b50af`(Repair #2) → `726bcca4a`(Repair #3)
- **基线**: `f7667c8d7c540217c345d98beac33794e1f3e6d0`（main，干净）
- **候选已发布**: `dgx-hive-dev:/srv/hivecosm/12-development-workspaces/users/williamdev/repos/hivecrew.git`（分支 `work/hivecrew-project-lifecycle-closure`）

## 2. 冻结 diff hash

- 全量 diff（main...726bcca4a，含 goal bundle）: `d41e1f1239e5e0fae551393446dd2dc3c2f74473fef6aaf6fe7304fa3be3dd4c`
- 源码 diff（仅 server/ + packages/）: `2ca633401085bdd006ec4b8c9a83316609a1a313545d9bc76f69467e9103d80b`
- 变更规模: 27 files changed, +2364 / -6

## 3. 测试命令与 exit code（全部 0，本会话实测）

| 命令 | exit code |
|---|---|
| `cd server && go build ./...` | 0 |
| `cd server && go test ./internal/service -run TestClassifyProject -count=1` | 0（11/11） |
| `cd server && DATABASE_URL=postgres://...:55433/multica go test ./internal/handler -run 'TestListProjectLifecycle|TestGetProjectLifecycle' -count=1` | 0（4/4） |
| `pnpm --filter @multica/views typecheck`（tsc --noEmit） | 0 |
| `pnpm --filter @multica/views exec vitest run projects/components` | 0（32/32） |

（隔离 DB：`hivecrew-lifecycle-testdb` @127.0.0.1:55433，pg_dump 只读副本；Gauss 验收时也自建了独立隔离库跑通 handler 4/4）

## 4. 独立三方验收（全部 PASS）

- **Gauss（HIV-554，测试与独立审查）**: VERDICT: PASS（复验 tip 726bcca4a；service 11/11、handler 4/4 自建隔离库实跑、API 契约、负向 #1/#2/#4/#8/#9、数据真源零迁移零 schema 改动）— 证据 `evidence/EV-S1-GAUSS-PASS.md`
- **Quinn（HIV-555，质量守护）**: VERDICT: PASS（5 项质量不变量 + 修复链复验）— `evidence/EV-S1-QUINN-PASS.md`
- **Pixel（HIV-556，前端）**: VERDICT: PASS（前端/浏览器）— `evidence/EV-S1-PIXEL-PASS.md`

## 5. API / 浏览器证据

- API（candidate :18090 实测）: `/health` 200；`GET /api/projects/lifecycle` 未认证 401、认证 200（11 项目）、单项目 200、未知 id 404。
- 诚实分类（11 项目实读）: 8 `review_or_repair_blocked`、1 `duplicate_or_superseded`、2 `source_gap`、2 `owner_decision_required`、0 `active`（无 live Task 时不再显示「进行中」）。
- 浏览器: Pixel PASS；前端 vitest 32/32；tsc 干净。

## 6. 给 W3 的集成边界

- Slice 1 只新增**只读派生读模型**：无 migration、无 schema/enum 改动、无第二真源表、不写 project.status。
- 新路由: `GET /api/projects/lifecycle`、`GET /api/projects/<built-in function id>/lifecycle`（Auth + RequireWorkspaceMember）。
- 前端: 五桶分类 + 健康徽章 + 项目卡字段（负责人/frontier/worker/last_receipt/next_action/terminal issues）。
- 已知后续项（非阻断，已记 EVIDENCE）: O2 `frozenSupersessions` 硬编码种子 → 迁配置/派生；O3 portfolio 全量扫 issue → Slice 5 分页；O4 `deferred` 计入 frontier 口径注明；F4 前端桶汇总已复核。

## 7. 交接后不变量

- 不修改 main / 其他窗口工作树 / Owner 主 CHECKLIST。
- 候选部署到备用端口，验证通过再对本机开发环境做可回滚切换（WAVE-4 步骤）。
