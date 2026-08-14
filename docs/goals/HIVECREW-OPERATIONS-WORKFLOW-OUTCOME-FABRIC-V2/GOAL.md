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

- A published visual graph is immutable, workspace-scoped and versioned. It is
  a design and governance artifact today; the existing stage engine remains the
  only executor. Publishing a graph never silently creates Task, Run, Outcome,
  platform publication, NAS write or cloud write.
- An L4 Project may own multiple independent workflow definitions. The chosen
  definition identity is in the workflow page URL; L3/L4 never become action
  nodes.
- The workflow `项目成果` tab is an L4-filtered read projection of the existing
  CompanyOps Outcome Center. It links back to that center for detail, review
  and promotion; it does not own a second artifact/status lifecycle.
- Storage capability is candidate-only fixture coverage: local cache, NAS
  primary, offline copy and cloud replica records are assessed by metadata.
  No real Synology, removable disk or cloud object was mounted or written.
