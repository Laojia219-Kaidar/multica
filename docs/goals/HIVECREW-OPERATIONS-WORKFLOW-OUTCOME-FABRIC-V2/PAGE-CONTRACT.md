# Workflow production-operations page contract

## Entry and identity

- Route: `/{workspaceSlug}/workflow`.
- Selection is URL-backed: `program`, `project`, `workflow`, `section`.
- L3 is an explicit read-only OperatingProgram projection only until a formal
  program registry exists. L4 is always an existing Project ID; actions are
  graph nodes, never navigation entries.
- The route does not substitute an invalid Project deep link with a first
  project. It renders a source-state instead.

## Source ownership

| Page concern | Read source | Writer / authority | Page behavior |
| --- | --- | --- | --- |
| L4 operating project | existing Project API | Project authority | read-only projection |
| workflow version | `workflow_definition_version` | workflow publish endpoint | immutable version receipt |
| runtime instance | existing workflow instance store | existing stage engine | read-only runtime projection |
| project outcome / artifact | CompanyOps Outcome Center | existing Outcome/Artifact lifecycle | filtered read and deep-link only |
| storage locations | CompanyOps artifact-storage fixture | no external storage write in V1 | capability/durability assessment only |

## Workflow contract

- A project can select or create multiple independent visual workflow drafts.
- Supported graph nodes: Agent task, human task, approval and decision.
- Agent tasks must bind a fixed formal Employee ID, a nonblank role pool, or a
  project default. Fixed employee without an ID is not publishable.
- Node position and edge condition are part of the immutable graph payload.
- Publication is workspace-scoped and idempotent. The repository, not a client,
  allocates the next version; concurrent publishers retry a definition/version
  uniqueness collision.
- The visual graph is not yet a graph-execution adapter. `stages: []` is
  intentional for this candidate; it does not claim to dispatch Agents.

## Failure behavior

- Project, definition or instance read errors are explicit source states, not
  empty-data fallbacks.
- Outcome Center failure in the `项目成果` tab is explicit and never displayed as
  zero results.
- Publish failure preserves the local draft and shows no success receipt.
- Codex In-app Browser independently reached the candidate login page on
  2026-08-14 (`127.0.0.1:13512`, HiveCrew title and login form observed).
  Authenticated workflow-page review remains pending: no verification code,
  user account or user session was used merely to complete this candidate
  check. Candidate API, migrations and component tests remain independently
  recorded in the Goal CHECKLIST.

## Candidate-only receipt

- Candidate worktree: `/Users/jiawei/hivecosm-worktrees/hivecrew-operations-workflow-v2`.
- Candidate services: frontend `13512`, backend `18592`, candidate database
  only.
- API canary created a candidate workspace and a candidate Project, published
  `content.wechat` and `content.wechat.short-video` once each, then read two
  definitions and zero existing outcomes from `hivecrew.outcome-center.v1`.
- No main merge, production deployment, real platform post, trading action,
  NAS mount/write or cloud storage write occurred.
