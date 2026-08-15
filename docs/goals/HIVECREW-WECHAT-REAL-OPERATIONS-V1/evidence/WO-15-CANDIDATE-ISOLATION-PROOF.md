# WO-15 · P0-GATE-04 候选存储与正式权威隔离证明

> Work order: WO-15（read-only proof，write scope 仅本 goal 目录）
> Verdict: **PROVEN_ISOLATED**（代码路径 + 候选环境变量名枚举 + 两条 go test 实测；live 进程/canary 期断言延至 WO-50）
> Base revision: `62e6cf517f9c45123d0c792e20710184da994dcb`；证明文档写于 WO-10R commit `eb65d94c6` 之后
> 调研方式：独立只读调研（explore subagent）+ controller 亲跑可执行断言；未读/未打印任何 secret 值
> 时间：2026-08-15T10:05:00Z（UTC）

## 1. 结论（VERDICT）

**PROVEN_ISOLATED**，附配置纪律性注意事项（第 6 节 OPEN RISKS）。

理由：全部本地持久化路径（artifact_candidate / artifact_event / artifact_promotion_claim / artifact_materialization_intent / artifact_replica_location）可证明地运行在由唯一 `DATABASE_URL` 构建的 pgx pool 上（`server/cmd/server/main.go:216` 是全二进制唯一 DSN 入口；全 server 仅 `dbstats.go:83,222` 两个 pool 构造器，同一 DSN），候选环境把该 DSN 绑定到候选库 `multica_hivecrew_operations_workflow_v2_512`（本地 Docker Postgres）。全代码库唯一的正式权威写出是一个 HTTP POST，目标恰好等于 `HIVECOSM_AUTHORITY_BASE_URL`（`server/internal/companyops/hivecosm_formal_artifact_client.go:219`）；该变量在候选环境中不存在（子系统当前 fail-closed 禁用，返回 503 `writer_unavailable`），且 client 构造函数可指向任意 loopback fake。生产 1421 是另一套栈（noah-ark-4 cockpit，engine-3104/bff-3150），不出现在本仓库任何 DSN、URL 常量或代码路径中。

## 2. 写入路径图（PATH MAP）

### A. 候选物化（创建 artifact candidate）
- 入口：`daemon.go:3017-3018` → `CompanyOpsArtifactService.MaterializeCompletedTask`（`server/internal/service/companyops_artifact_outcome.go:149-219`）
- → `ArtifactMaterializer.Materialize`（`artifact_materializer.go:80-173`）
- → `DurableArtifactMaterializationRepository`（`artifact_runtime_repository.go:20-105`），每步一个 committed tx，`txStarter.Begin` = 主 `*pgxpool.Pool`
- → `ArtifactPersistenceRepository`（`artifact_persistence.go`）：
  - `RecordArtifactMaterializationIntent` → sqlc `InsertArtifactMaterializationIntent`/`GetArtifactMaterializationIntent` → 表 **`artifact_materialization_intent`**（`server/pkg/db/queries/companyops_artifact.sql:63-80`；调用点 `artifact_persistence.go:90-101`）
  - 字节 → `store.Upload`（`artifact_materializer.go:134`）→ 仅当 `S3_BUCKET` 设置时走 S3，否则 `LocalStorage` 落 `./data/uploads`（相对 server CWD；`router.go:292-300`、`storage/local.go:45-63`）
  - `CommitArtifactCandidate` → `LockArtifactLineage`（advisory lock，`companyops_artifact.sql:29-30`）、`InsertArtifactCandidate` → **`artifact_candidate`**（sql:1-14）、`NextArtifactEventSequence`+`InsertArtifactEvent` → **`artifact_event`**（sql:32-48）、`DeleteCommittedArtifactMaterializationIntent`（sql:104-112）——同一 child tx 内（`artifact_persistence.go:128-235`）

### B. Owner 审校（lifecycle transition）
- `companyops.go:315` → `ReviewArtifact`（`companyops_artifact_outcome.go:449-548`）→ `AppendArtifactEvent`（`artifact_persistence.go:264-325`）→ **`artifact_event`**；rework task 经 `taskService` → `agent_task_queue` 族（同 tx 同 pool）

### C. 提升进正式 Outcome Center（全子系统唯一外部写）
- `companyops.go:400` → `PromoteArtifact`（`companyops_artifact_outcome.go:589-682`）→ `attemptArtifactPromotion`（`:711-773`）：
  1. `ClaimPromotion` → **`artifact_promotion_claim`**（sql:126-137）
  2. `AppendArtifactEvent(promotion_requested)` → **`artifact_event`**
  3. **唯一外部写**：`formalAuthority.PromoteFormalArtifact` → HTTP POST `{HIVECOSM_AUTHORITY_BASE_URL}/api/company-ops/formal-artifacts/promotions`（`companyops_artifact_outcome.go:731`；`hivecosm_formal_artifact_client.go:18,190-242`）
  4. `AppendArtifactEvent(promotion_succeeded|promotion_failed)` → **`artifact_event`**
  5. `runArtifactReadback` → GET `{base}/api/company-ops/formal-artifacts/{id}`（`:807-856`）→ `AppendArtifactEvent(authority_readback_confirmed)`
- 本地 Outcome Center 本身**只读**：`CompanyOpsOutcomeCenterService` "never writes"（`companyops_outcome_center.go:322-334`）；`formal_visible` 由 `artifact_event` 派生（`:638-641, 852-857`）。

### D. 副本位置账本（相邻）
- `ArtifactReplicaLocationRepository` → **`artifact_replica_location`**（sql:142-178），同一 caller-owned tx 模式。

**不可变性护栏**：trigger `artifact_candidate_reject_mutation`、`artifact_event_reject_mutation`（`migrations/240_companyops_artifact_persistence.up.sql:70-89`）与 `artifact_promotion_claim_reject_mutation`（`migrations/251_artifact_promotion_claim.up.sql:22-24`）只对 UPDATE/DELETE RAISE；无任何 trigger 写其他表。migration 240 头注 "deliberately no foreign keys or cascades"。

## 3. DB 绑定（DB BINDING）

- **全 server 二进制唯一 DSN 来源**：`server/cmd/server/main.go:216` `os.Getenv("DATABASE_URL")`，fallback 为仓库公开 dev 默认 `postgres://multica:multica@localhost:5432/multica`（源码内公开，非 secret）。
- 运行中 server 恰好两个 pool，均由同一 `dbURL` 构建：主 pool（`dbstats.go:55-83`）与 sampler pool（`main.go:348`；`dbstats.go:207-222` 注释 "built from the same DATABASE_URL as the main pool"）。对 `server/cmd/server/*.go`（排除测试）grep `pgxpool.New|pgx.Connect` 仅命中这两个构造器——不存在第二 DSN。
- artifact service 接收同一 pool：`NewRouterWithOptions(pool…)` → `db.New(pool)`（`router.go:283-284`）→ `configureCompanyOps` → `NewCompanyOpsArtifactService(queries, pool, …)`（`router.go:184`）。所有 artifact SQL 经该 pool 的 `pgx.Tx` 执行。
- **候选绑定**：候选 `.env.worktree`（由 `scripts/init-worktree-env.sh` 生成，offset 512 ⇒ 端口 18592/13512、库名 `multica_hivecrew_operations_workflow_v2_512`）含候选库名（实测 grep 计数=2；值未打印）。`scripts/dev.sh` source 该文件启动 server。
- **18592 能否写到生产库？** 无任何代码路径可以：pgx 单库单 pool；artifact 全部 17 条 sqlc 查询为同库 DML，无 dblink/foreign server；Postgres 不能跨库写。唯一残余向量是 `main.go:217-218` 的 fallback DSN（`DATABASE_URL` 为空时落共享 Docker 实例的 `multica` 库），那也不是 1421 生产存储。
- 生产 1421 是另一套栈（noah-ark-4 内部 cockpit：Vite app + runtime proxy，依赖 engine-3104/bff-3150，launchd label `com.hivecosm.noah-ark-4.app`），world-entry registry 指向 SQLite/legacy 存储而非本 Docker Postgres。（标注为推断：未全量审计 engine-3104 持久层。）

## 4. 正式权威（FORMAL AUTHORITY）

- Client：`HiveCosmAuthorityClient`（`hivecosm_authority_client.go:84-90`），端点：`GET /api/company-ops/owner-work-context`、`POST /api/company-ops/formal-artifacts/promotions`、`GET /api/company-ops/formal-artifacts/{id}`。所有请求仅由 `c.baseURL` 构造（`hivecosm_formal_artifact_client.go:312-316`）。
- 配置：base URL 来自 env `HIVECOSM_AUTHORITY_BASE_URL`，token 来自 `HIVECOSM_AUTHORITY_BEARER_TOKEN`（`router.go:117-127`）。构造函数接受任意绝对 http/https URL（拒绝 userinfo/query/fragment；`hivecosm_authority_client.go:101-117`）——不硬编码任何 live HiveCosm host；现有测试已指向 `httptest` fake（`hivecosm_formal_artifact_client_test.go:145`）。**可指向 fake/隔离权威。**
- **Origin pinning**：`companyOpsBearerTransport.RoundTrip` 拒绝任何 scheme/host 与配置权威 origin 不符的请求（`router.go:264-274`）——bearer token 不会经此 client 泄漏到其他 host。
- **未配置即 fail-closed**：缺 base URL 或 token → `configureCompanyOps` 提前返回（`router.go:118-127`）→ `h.CompanyOpsArtifacts == nil` → review/promote/transition 端点返回 503 `writer_unavailable`（`companyops.go:315-318, 400-403, 585-588`）；daemon 物化跳过（`daemon.go:3017`）。`NewCompanyOpsArtifactService` 独立拒绝 nil authority（`companyops_artifact_outcome.go:136-138`）。
- **候选环境实测**：`.env.worktree` 不含任何 `HIVECOSM_*` 变量（变量名枚举实测：`grep -c "^HIVECOSM_" .env.worktree` = 0；全量变量名仅 `DATABASE_URL PORT JWT_SECRET MULTICA_* FRONTEND_* NEXT_PUBLIC_* POSTGRES_* GOOGLE_* HIVECREW_LOCAL_OPERATOR_* CORS_ALLOWED_ORIGINS`）。⇒ 按当前启动方式，候选的 artifact/promotion 子系统整体禁用。

## 5. 可执行断言（ASSERTIONS）与本次执行结果

| # | 断言 | 验证方法 | 本次结果 |
|---|------|----------|----------|
| 1 | 单一 DSN 入口 | `grep -rn "pgxpool.New\|pgx.Connect\|pgxpool.NewWithConfig" server/cmd/server --include='*.go' \| grep -v _test` 仅 `dbstats.go:83,222`；`grep -n 'os.Getenv("DATABASE_URL")' server/cmd/server/main.go` 仅 line 216 | **PASS**（controller 实测 2026-08-15，输出如上） |
| 2 | 运行进程绑定 | canary 期：`ps eww -p <pid>` 的 `DATABASE_URL` host=`localhost:5432`、库名=`multica_hivecrew_operations_workflow_v2_512`（只查 host+库名，不打印凭据），且无 `HIVECOSM_AUTHORITY_BASE_URL` 或为 loopback/fake；`SELECT current_database()` = 候选库 | DEFERRED → WO-50 canary |
| 3 | 候选库无跨库机械 | canary 期：`pg_foreign_server` count=0；`artifact%` 表 trigger 仅三个 `reject_mutation`；`pg_proc` 无 dblink | DEFERRED → WO-50 canary |
| 4 | fail-closed 闸 | 候选当前态（`HIVECOSM_AUTHORITY_BASE_URL` 未设）：POST promotions 端点 → 503 `writer_unavailable`（`companyops.go:400-403`） | DEFERRED → WO-50 canary（代码路径已核实） |
| 5 | 权威目标恰为配置 origin | `cd server && go test ./internal/companyops/ -run 'FormalArtifact\|AuthorityClient' -count=1`；canary 期加 `lsof -nP -iTCP -a -p <pid>` 出站仅 5432 与 fake 权威端口 | **PASS**（`ok server/internal/companyops 0.605s`）；lsof 部分 DEFERRED → WO-50 |
| 6 | 候选库是唯一 artifact 载体 | canary 后：候选库 `artifact_candidate` count ≥ 1，同实例兄弟库（如 `multica`）= 0 | DEFERRED → WO-50 canary |
| 7 | nil-authority 构造 fail-closed | `cd server && go test ./internal/service/ -run 'CompanyOpsArtifact' -count=1` 覆盖 `NewCompanyOpsArtifactService` 拒绝 nil authority | **PASS**（`ok server/internal/service 0.516s`） |
| 8 | 生产 1421 围栏 | canary 前后 `lsof -iTCP:1421 -sTCP:LISTEN` 进程身份不变；候选 PID 无到 1421 的连接 | DEFERRED → WO-50 canary |

候选 DB 名绑定实测：`.env.worktree` 中 `multica_hivecrew_operations_workflow_v2_512` 出现 2 次（grep 计数，值未打印）。

## 6. OPEN RISKS（不阻塞本 gate，但必须在 WO-50 runbook 与后续硬化中处理）

1. **默认 DSN fallback**（`main.go:217-218`）：若候选环境 `DATABASE_URL` 为空，server 静默绑到共享 Docker 实例的 `multica` 库。仍非 1421 生产存储，但属跨环境污染，使"只写候选库"降级为配置依赖。建议 fail-closed（非生产环境强制要求 `DATABASE_URL`）。当前仅由 `.env.worktree` 纪律保证。
2. **权威 URL 无代码护栏**：没有代码阻止未来操作者把候选环境的 `HIVECOSM_AUTHORITY_BASE_URL` 指向 live 1421 权威。正式路径隔离是启动配置纪律而非代码强制。建议 candidate-mode allowlist（非 loopback 权威 host 需显式 production flag）。
3. **共享实例隔离**：所有 worktree 库共用一个 Docker Postgres 与同一 dev role；库名分隔是唯一边界。
4. **engine-3104 存储未全量审计**：1421 生产存储与候选 Postgres 不相交的结论基于拓扑/registry 证据（独立栈、SQLite/legacy 引用），标注为推断。
5. **对象存储 CWD 敏感**：候选 artifact 字节落 `./data/uploads`（相对 server 进程 CWD）；若从意外目录启动，字节落在 worktree 外（仍是本地、绝非生产，但 canary runbook 应固定 CWD）。

## 7. Gate 决策建议

P0-GATE-04 可由 `frozen_read_only_contract_decision` 转为 **proven**：隔离在代码层与当前候选配置层均已证明；WO-40B/WO-50 解锁的前置是 JOIN-1 通过 + canary 期执行第 2/3/4/6/8 条 deferred 断言并留新鲜证据。WO-50 canary 必须：不设置 `HIVECOSM_AUTHORITY_BASE_URL`（保持 fail-closed 503）或显式指向 loopback fake；禁止指向任何非 loopback 权威。
