# HIVECREW-OPERATIONS-WORKFLOW-OUTCOME-FABRIC-V2

## Activated candidate objective

Build the HiveCrew production-operations workflow surface in the isolated
candidate worktree. The owner can enter Workflow from the formal workspace
navigation, select an operating program and operating project, edit and publish
a versioned workflow graph, observe persisted runtime instances, review outcome
artifacts, and inspect their registered storage locations.

## Exact construction site

- Repository: `/Volumes/HiveData/hivecosm/HQ-50-代码仓库/01-源码下载/multica`
- Candidate worktree: `/Users/jiawei/hivecosm-worktrees/hivecrew-operations-workflow-v2`
- Candidate branch: `work/hivecrew-operations-workflow-v2`
- Baseline: `8c45692399bc460edc1aefbfdf10ee7dc8a6344f`
- Existing workflow/outcome source: this baseline; the historical W4 worktree is
  read-only reference only.

## Permanent boundaries

- HiveCrew remains an interaction/execution authority. It must not become a
  second authority for Project, Task, Run, Employee, governance, formal outcome,
  or knowledge state.
- Extend existing `server/internal/workflow` and CompanyOps Artifact/Outcome
  code. Do not create parallel engines, artifact stores, or outcome centers.
- Candidate worktree and candidate ports only. Do not modify `main`, port 3000,
  port 8080, 1421, DGX published source, production databases, NAS data, cloud
  storage, or platform accounts.
- No real publication, trading, destructive storage operation, production merge,
  or deployment is authorized.

## Workstreams and mutexes

| Workstream | Owned paths | Shared paths held for integrator |
| --- | --- | --- |
| Workflow kernel | `server/internal/workflow/**`, workflow SQL/migrations, `server/internal/handler/workflow.go` | `server/cmd/server/router.go`, `packages/core/api/client.ts` |
| Operations UI | `packages/core/workflow/**`, `packages/views/workflow/**`, path tests | `packages/core/paths/**`, sidebar, routes, locales, `packages/views/package.json`, lockfile |
| Outcome/storage | `server/internal/companyops/artifact_*`, `server/internal/service/companyops_*`, `packages/views/outcomes/**` | migrations/queries touching shared CompanyOps surfaces |
| Integrator | Goal control, shared paths, API client/router joins, generated sqlc output, final browser proof | all shared files |

## Required product shape

- L2: 运营项目, 工作流模板, 运行中心, 决策中心, 成果中心, 数据复盘.
- L3: OperatingProgram, such as 蜂巢创科品牌运营.
- L4: independently operable OperatingProject bound to an existing Project ID,
  such as 微信公众号运营.
- Workflow actions remain graph nodes; never place 选题, 写作, 审核, 发布,
  回测, or 风控 in L3/L4 navigation.
- Use a shared Web/Desktop page; server data uses TanStack Query; URL stores
  stable program/project/definition/instance identity; local preference storage
  stores only pane layout.
- The first canary is `content.wechat-production-package.v1`, including a
  simulated publication receipt only.

## Critical path

Candidate environment and persisted instance list -> formal navigation and
operations context -> versioned graph designer and DAG runtime -> artifact and
storage-location integration -> end-to-end canary -> independent review and
candidate handoff.

## Success state

`CANDIDATE_READY_WAITING_OWNER_ACCEPTANCE`: focused tests, build, database
readback, candidate browser journey, failure-path checks, exact revision, and
handoff all exist. This is not production or Owner acceptance.

## Candidate implementation boundary (2026-08-14)

- A published visual graph is immutable, workspace-scoped and versioned. The
  existing stage engine now executes only its guarded linear V1 subset: exactly
  one root and sink, no fan-in, fan-out or conditional edge. Unsupported graph
  semantics fail closed with `409`; they are never flattened or silently run.
  A graph start creates the existing workspace-scoped WorkflowInstance/Event
  records and control receipts, but never silently creates Task, Run, Outcome,
  platform publication, NAS write or cloud write.
- Start and advance command idempotency replays from the persisted event ledger
  after a server restart. A rejected approval or decision remains an explicit
  receipt and append-only `workflow.advance_rejected` event rather than a
  silent no-op. This ledger remains orchestration evidence, not a second
  approval or Outcome authority.
- An L4 Project may own multiple independent workflow definitions. The chosen
  definition identity is in the workflow page URL; L3/L4 never become action
  nodes.
- The workflow `项目成果` tab is an L4-filtered read projection of the existing
  CompanyOps Outcome Center. It links back to that center for detail, review
  and promotion; it does not own a second artifact/status lifecycle.
- Storage placement is a workspace-scoped `artifact_replica_location` ledger
  for an existing Outcome/candidate revision. The read-only
  `/api/company-ops/outcomes/{commandId}/artifact-locations` route first
  validates the Outcome Center authority, then returns registered placement
  observations (or an explicit source error). It never moves bytes or changes
  artifact lifecycle. Local cache, NAS primary, offline copy and cloud replica
  are candidate location classes only; no real Synology, removable disk or
  cloud object was mounted or written.

## Current candidate evidence (2026-08-14)

- Candidate tip: `897374606` on
  `work/hivecrew-operations-workflow-v2`.
- API canary, using an isolated candidate user/workspace/Project, published
  and ran `content.wechat-production-package.v1`: valid start `201`, durable
  start replay `200`, durable advance replay `200`, wrong Project `400`, and
  a conditional graph `409`. After restart the run reached stage 4
  `completed`, with seven events including two durable rejection events. The
  terminal node was a simulated publication confirmation only.
- Candidate DB readback confirmed that completed instance, seven events/two
  rejections, the `artifact_replica_location` ledger, and migrations 357–361.
- Focused Go, race, core API, views, login (34 tests), web typecheck and web
  production build pass. The final build retains two pre-existing CSS
  `::highlight` optimizer warnings but produces a build output.
- The in-app browser reaches the candidate login page. Its automation input
  currently fails to update this controlled React email field, while the
  login component's 34-test suite passes. Therefore the authenticated browser
  journey is still an explicit pending verification; this Goal is not yet
  `CANDIDATE_READY_WAITING_OWNER_ACCEPTANCE` and no Owner acceptance is
  implied.
