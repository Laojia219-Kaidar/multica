# Workflow production-operations page contract

## Entry and identity

- Route: `/{workspaceSlug}/workflow`.
- Selection is URL-backed: `program`, `project`, `workflow`, `section`.
- L3 is the workspace-scoped `workflow_operating_program` registry. It stores
  only a name, description and membership references to existing formal L4
  Projects; it is not a second Project store. L4 is always an existing Project
  ID; actions are graph nodes, never navigation entries.
- A formal Project with no L3 membership is visibly `未归类正式项目`, not placed
  into a synthetic holding program. One L4 may have one L3 membership within
  its workspace; assignment from another workspace fails closed.
- The route does not substitute an invalid Project deep link with a first
  project. It renders a source-state instead.

## Source ownership

| Page concern | Read source | Writer / authority | Page behavior |
| --- | --- | --- | --- |
| L3 operating program | `workflow_operating_program` + membership ledger | guarded workflow operating-program endpoint | create/edit/delete L3 and classify existing L4; never owns Project lifecycle |
| L4 operating project | existing Project API | Project authority | read-only projection |
| workflow version | `workflow_definition_version` | workflow publish endpoint | immutable version receipt |
| runtime instance | existing workflow instance store | existing stage engine | read-only runtime projection |
| project outcome / artifact | CompanyOps Outcome Center | existing Outcome/Artifact lifecycle | filtered read and deep-link only |
| storage locations | `artifact_replica_location` placement ledger, after Outcome Center identity read | existing Outcome/Artifact lifecycle | read-only candidate placement observation |

## Workflow contract

- A project can select or create multiple independent visual workflow drafts.
- Supported graph nodes: Agent task, human task, approval and decision.
- Agent tasks must bind a fixed formal Employee ID, a nonblank role pool, or a
  project default. Fixed employee without an ID is not publishable.
- Node position and edge condition are part of the immutable graph payload.
- Publication is workspace-scoped and idempotent. The repository, not a client,
  allocates the next version; concurrent publishers retry a definition/version
  uniqueness collision.
- Publishing creates an immutable visual graph version. Starting it compiles
  only the guarded linear V1 subset (one root/sink, no fan-in, fan-out or
  conditional edges) into the existing stage engine; unsupported graph
  semantics return `409` and never silently flatten into a run.
- Starting a published graph requires its exact formal L4 Project context.
  Start and advance commands return control receipts; their idempotency is
  replayed from the existing workspace-scoped event ledger after restart.
- Graph execution records no real Agent Task/Run dispatch. `project_default`
  and other bindings are retained as graph configuration only until a governed
  Task/Run adapter is separately authorized.
- L3 create carries an idempotency key; update and delete remain explicit,
  workspace-scoped guarded commands. L3 deletion removes only its membership
  ledger rows. The UI requires a second explicit confirmation and says that
  formal Projects, workflows, Outcomes and files are retained.
- Program assignment/unassignment and Program deletion serialize the Program
  row; assignment then locks the native Project. The native Project-delete
  transaction removes only its membership mapping before Project deletion.

## Failure behavior

- Project, definition or instance read errors are explicit source states, not
  empty-data fallbacks.
- Outcome Center failure in the `项目成果` tab is explicit and never displayed as
  zero results.
- Storage-location failure is explicit per Outcome and never displayed as zero
  locations. An absent authoritative Outcome is `404`; an authoritative
  Outcome with no placement rows is an explicit empty location list.
- Publish failure preserves the local draft and shows no success receipt.
- L3 registry read failure is explicit; the page never recreates the old
  synthetic “运营科目待建档” projection. A stale or invalid mapping is cleaned by
  migrations 369–370 and native Project deletion handling, rather than shown
  as a phantom L4 project.
- Codex In-app Browser independently reached the candidate login page on
  2026-08-14 (`127.0.0.1:13512`, HiveCrew title and login form observed), but
  its automation input did not update the controlled React email field. The
  login component regression suite passes (34 tests); authenticated browser
  workflow-page review remains pending rather than being claimed as passed.

## Candidate-only receipt

- Candidate worktree: `/Users/jiawei/hivecosm-worktrees/hivecrew-operations-workflow-v2`.
- Candidate services: frontend `13512`, backend `18592`, candidate database
  only.
- API canaries used candidate-only workspaces and Projects. The final canary
  published and ran `content.wechat-production-package.v1`, replayed start and
  advance after a backend restart, rejected missing approval/decision evidence,
  then completed only a simulated publication node. Database readback was
  stage 4/completed with seven events and two rejections.
- No main merge, production deployment, real platform post, trading action,
  NAS mount/write or cloud storage write occurred.
- Candidate service health on `18592`, database migrations 362–370 and zero
  L3/L4 orphan mappings were independently read back. Authenticated browser
  interaction remains pending because the in-app browser automation cannot
  currently change the controlled login email field; this is not treated as a
  passed visual review.
