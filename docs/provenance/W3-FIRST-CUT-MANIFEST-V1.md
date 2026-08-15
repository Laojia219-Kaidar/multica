# W3 Clean First-Cut Manifest — WO-P2F-06A-PRIME-JOIN-PREP (Stage 2A)

## Provenance

- Goal node: `WO-P2F-06A-PRIME-JOIN-PREP`
- Employee: Shard｜后端与数据库工程师 (agent `86b86d76-09f4-42e3-bb7a-8113c81775e0`)
- Runtime/model: Prime Agent · DeepSeek V4 Pro 0813 / `deepseek/deepseek-v4-pro`
- Source (W3 clean HEAD): `8c45692399bc460edc1aefbfdf10ee7dc8a6344f` (branch `work/hivecrew-product-integration-mainline`)
- Baseline (canonical main): `f7667c8d7c540217c345d98beac33794e1f3e6d0`
- Integration target: `7787e721ccd128e1bfd35cb14fdf970dda4238f0` (branch `work/hivecrew-four-lane-formal-convergence-codex`)
- Cut rule: `git diff f7667c8d7..8c456923` (clean W3 HEAD vs canonical main), minus hard exclusions below.

## Ancestry verification (`git merge-base --is-ancestor`)

| Check | Result |
| --- | --- |
| W4 `9836644f` is ancestor of W3 `8c456923` | YES |
| W1 `83ebb681` is ancestor of W3 `8c456923` | NO |
| W2 `577f6cc` is ancestor of W3 `8c456923` | NO |
| Baseline `f7667c8d7` is ancestor of W3 `8c456923` | YES |
| Integration target `7787e721c` is ancestor of W3 `8c456923` | NO (docs-only divergence above baseline) |

## Selection summary

- Clean W3 diff paths: 274
- Excluded clean paths: 4
- Selected and applied: 270
- Content fidelity: all 270 applied files byte-identical to W3 clean HEAD blobs (`git hash-object` == `8c456923:<path>`).
- Working tree: 76 modified tracked files (3795 insertions / 243 deletions) + 194 new files.

## Hard exclusions applied

1. **W2 lifecycle migrations/numbering** — excluded the 4 W2 lifecycle migration files (exact list below). W2 owns lifecycle migration numbering; deferred to Stage 2B Join.
2. **Keep Prime adapter/runtime files** — none present in the clean W3 diff (no Prime adapter/runtime files among the 274 clean paths); nothing to exclude.
3. **deployment/ports** — none present in the clean W3 diff (no docker-compose / deploy / port files); nothing to exclude.
4. **production DB** — no migration applied, no production database connection or mutation performed in this prep.
5. **secrets** — no secret files or values touched (no `.env`/credential paths in the diff).
6. **W1 dirty migration 400-405** — not present in clean W3 HEAD (400-405 numbering exists only in the W1 dirty tree); excluded by the clean-only rule.
7. **W3 dirty/untracked paths** — 76 paths in the W3 worktree (2 modified + 74 untracked) excluded; none consumed.

## Shard corrections (build-enabling, transparent)

The W3 clean HEAD itself contains one unbuildable defect: `server/pkg/db/generated/models.go` has a stray commit-message fragment appended to the `Base` struct (`} (feat(bases): decision A base migration — ...)` plus a duplicated closing brace), which is invalid Go. To keep this candidate truthful AND buildable, Shard applied the minimal correction of removing that stray fragment (the file then matches the intended sqlc output; `git hash-object` equals `c629dcccad33558e8080564622e90c180ba3a659`).

- This is a one-line defect correction to a committed clean path, not a wholesale consumption of the W3 dirty/untracked tree.
- `server/pkg/db/generated/*` remains a Join-mutex zone: generated SQL is regenerated in Stage 2B only after the final migration/query set is frozen. This correction is a build-enabling stopgap, not a hand-merge of generated content.

## Verification (narrowest build/typecheck; no DB, no migrations)

| Command | Result |
| --- | --- |
| `cd server && go build ./...` | PASS |
| `cd server && go test -run='^$' ./...` (compile all packages incl. tests) | PASS |
| `cd server && go vet ./internal/liveactivity/... ./internal/memory/... ./internal/workflow/... ./internal/workwall/... ./internal/metrics/... ./internal/scheduler/...` | PASS |
| `pnpm --filter @multica/core typecheck` | PASS |
| `pnpm --filter @multica/views typecheck` | PASS |
| `pnpm --filter @multica/web typecheck` | PASS |

Behavioral / DB-backed tests are deferred to Stage 2B: they require migrations that are excluded from this prep.

## Excluded clean paths (exact)

- `server/migrations/343_project_lifecycle_receipt.down.sql`
- `server/migrations/343_project_lifecycle_receipt.up.sql`
- `server/migrations/344_closure_package_review.down.sql`
- `server/migrations/344_closure_package_review.up.sql`

## Skipped conflict list

- `server/migrations/343_project_lifecycle_receipt.*` and `server/migrations/344_closure_package_review.*` — W2 lifecycle migration numbering mutex, serialized only at the Join.
- Receiver: Shard (Stage 2B Join). Wake condition: W2 Stage 1 intake handoff (HIV-645) delivered and final migration numbering frozen.

## Selected paths (270) — full list

### (root) (3)

- `INTEGRATION-GAPS-RESOLUTION.md`
- `RESUME-AND-OPS-V1.md`
- `e2e-browser-check.cjs`

### apps/web (13)

- `apps/web/app/[workspaceSlug]/(dashboard)/datasets/page.tsx`
- `apps/web/app/[workspaceSlug]/(dashboard)/employees/page.tsx`
- `apps/web/app/[workspaceSlug]/(dashboard)/issues/review/page.tsx`
- `apps/web/app/[workspaceSlug]/(dashboard)/memory/page.tsx`
- `apps/web/app/[workspaceSlug]/(dashboard)/runtimes/bases/page.tsx`
- `apps/web/app/[workspaceSlug]/(dashboard)/usage/page.tsx`
- `apps/web/app/[workspaceSlug]/(dashboard)/usage/usage-api.ts`
- `apps/web/app/[workspaceSlug]/(dashboard)/usage/usage-page.test.tsx`
- `apps/web/app/[workspaceSlug]/(dashboard)/usage/usage-page.tsx`
- `apps/web/app/[workspaceSlug]/(dashboard)/work-wall/page.tsx`
- `apps/web/app/[workspaceSlug]/(dashboard)/workflow/page.tsx`
- `apps/web/app/[workspaceSlug]/(dashboard)/workrooms/page.tsx`
- `apps/web/next-env.d.ts`

### docs/goals (10)

- `docs/goals/HIVE-PRIME-PORTFOLIO-CONVERGENCE/CHECKLIST.yaml`
- `docs/goals/HIVE-PRIME-PORTFOLIO-CONVERGENCE/SELF-ASSESSMENT-GAP-ANALYSIS.md`
- `docs/goals/HIVE-PRIME-PORTFOLIO-CONVERGENCE/evidence/migration-fusion-20260814T010313Z.json`
- `docs/goals/HIVE-PRIME-PORTFOLIO-CONVERGENCE/evidence/self-audit-evidence-20260814T004608Z.json`
- `docs/goals/HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1/CHECKLIST.yaml`
- `docs/goals/HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1/EVIDENCE.md`
- `docs/goals/HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1/GOAL.md`
- `docs/goals/HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1/HANDOFF.md`
- `docs/goals/HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1/graphs/overview.mmd`
- `docs/goals/HIVECREW-PROJECT-LIFECYCLE-CLOSURE-V1/reports/hiv-553-project-lifecycle-contract.md`

### docs/plans (3)

- `docs/plans/2026-08-12-001-hivecosm-execution-capacity-model.md`
- `docs/plans/2026-08-12-002-hivecosm-capacity-4to8-migration-runbook.md`
- `docs/plans/2026-08-12-003-dynamic-capacity-routing-contract.md`

### packages/core (23)

- `packages/core/api/client.ts`
- `packages/core/api/fixtures/memory-fixtures.json`
- `packages/core/api/memory.test.ts`
- `packages/core/api/memory.ts`
- `packages/core/api/workflow.test.ts`
- `packages/core/api/workflow.ts`
- `packages/core/api/workwall.test.ts`
- `packages/core/api/workwall.ts`
- `packages/core/inbox/queries.ts`
- `packages/core/issues/dispatch.ts`
- `packages/core/issues/index.ts`
- `packages/core/package.json`
- `packages/core/paths/paths.ts`
- `packages/core/projects/index.ts`
- `packages/core/projects/pipeline-types.ts`
- `packages/core/projects/queries.ts`
- `packages/core/runtimes/bases.ts`
- `packages/core/runtimes/index.ts`
- `packages/core/types/bases.ts`
- `packages/core/types/companyops.ts`
- `packages/core/types/index.ts`
- `packages/core/types/issue.ts`
- `packages/core/types/project.ts`

### packages/views (57)

- `packages/views/bases/components/bases-page.tsx`
- `packages/views/datasets/datasets-page.tsx`
- `packages/views/datasets/index.ts`
- `packages/views/employees/employees-page.tsx`
- `packages/views/employees/index.ts`
- `packages/views/issues/components/issue-detail.tsx`
- `packages/views/issues/components/issue-dispatch-controls.tsx`
- `packages/views/layout/app-sidebar.tsx`
- `packages/views/locales/en/layout.json`
- `packages/views/locales/en/outcomes.json`
- `packages/views/locales/en/projects.json`
- `packages/views/locales/en/settings.json`
- `packages/views/locales/ja/layout.json`
- `packages/views/locales/ja/outcomes.json`
- `packages/views/locales/ja/projects.json`
- `packages/views/locales/ja/settings.json`
- `packages/views/locales/ko/layout.json`
- `packages/views/locales/ko/outcomes.json`
- `packages/views/locales/ko/projects.json`
- `packages/views/locales/ko/settings.json`
- `packages/views/locales/zh-Hans/layout.json`
- `packages/views/locales/zh-Hans/outcomes.json`
- `packages/views/locales/zh-Hans/projects.json`
- `packages/views/locales/zh-Hans/settings.json`
- `packages/views/memory/index.ts`
- `packages/views/memory/memory-section.test.tsx`
- `packages/views/memory/memory-section.tsx`
- `packages/views/outcomes/outcome-list.tsx`
- `packages/views/outcomes/outcome-queries.ts`
- `packages/views/outcomes/outcomes-page.tsx`
- `packages/views/outcomes/use-cursor-page.test.ts`
- `packages/views/outcomes/use-cursor-page.ts`
- `packages/views/outcomes/use-outcomes-cursor.ts`
- `packages/views/package.json`
- `packages/views/projects/components/pipeline-projection.test.tsx`
- `packages/views/projects/components/pipeline-projection.tsx`
- `packages/views/projects/components/project-control-actions.tsx`
- `packages/views/projects/components/project-detail.tsx`
- `packages/views/projects/components/project-health.test.ts`
- `packages/views/projects/components/project-health.tsx`
- `packages/views/projects/components/projects-page.test.tsx`
- `packages/views/projects/components/projects-page.tsx`
- `packages/views/runtimes/components/bases-page.tsx`
- `packages/views/runtimes/components/index.ts`
- `packages/views/runtimes/components/runtimes-page.tsx`
- `packages/views/runtimes/index.ts`
- `packages/views/settings/components/ia-tab.tsx`
- `packages/views/settings/components/settings-page.tsx`
- `packages/views/workflow/index.ts`
- `packages/views/workflow/workflow-workbench.test.tsx`
- `packages/views/workflow/workflow-workbench.tsx`
- `packages/views/workrooms/index.ts`
- `packages/views/workrooms/workrooms-page.tsx`
- `packages/views/workwall/index.ts`
- `packages/views/workwall/work-wall-page.tsx`
- `packages/views/workwall/work-wall.test.tsx`
- `packages/views/workwall/work-wall.tsx`

### server/cmd (4)

- `server/cmd/server/main.go`
- `server/cmd/server/review_cell_config.go`
- `server/cmd/server/review_cell_listeners.go`
- `server/cmd/server/router.go`

### server/internal (93)

- `server/internal/handler/bases.go`
- `server/internal/handler/client_usage.go`
- `server/internal/handler/client_usage_lane_d_test.go`
- `server/internal/handler/companyops.go`
- `server/internal/handler/companyops_org.go`
- `server/internal/handler/companyops_org_join_handler_test.go`
- `server/internal/handler/companyops_outcomes.go`
- `server/internal/handler/companyops_outcomes_cursor_test.go`
- `server/internal/handler/dataset.go`
- `server/internal/handler/employee.go`
- `server/internal/handler/employee_test.go`
- `server/internal/handler/handler.go`
- `server/internal/handler/ia.go`
- `server/internal/handler/inbox.go`
- `server/internal/handler/issue.go`
- `server/internal/handler/issue_dispatch_owner_test.go`
- `server/internal/handler/memory.go`
- `server/internal/handler/memory_workflow_test.go`
- `server/internal/handler/pipeline.go`
- `server/internal/handler/pipeline_test.go`
- `server/internal/handler/project.go`
- `server/internal/handler/project_lifecycle.go`
- `server/internal/handler/project_lifecycle_control.go`
- `server/internal/handler/project_lifecycle_control_close_test.go`
- `server/internal/handler/project_lifecycle_control_test.go`
- `server/internal/handler/project_lifecycle_test.go`
- `server/internal/handler/project_pagination_test.go`
- `server/internal/handler/review.go`
- `server/internal/handler/runtime_bases.go`
- `server/internal/handler/runtime_bases_test.go`
- `server/internal/handler/workflow.go`
- `server/internal/handler/workforce_base_runtime_consistency_test.go`
- `server/internal/handler/workroom.go`
- `server/internal/handler/workroom_test.go`
- `server/internal/handler/workwall.go`
- `server/internal/handler/workwall_stream.go`
- `server/internal/liveactivity/employee_live_activity.go`
- `server/internal/liveactivity/employee_live_activity_test.go`
- `server/internal/liveactivity/event_kind.go`
- `server/internal/liveactivity/event_kind_test.go`
- `server/internal/liveactivity/live_activity.go`
- `server/internal/liveactivity/live_activity_test.go`
- `server/internal/memory/hydrate.go`
- `server/internal/memory/repository.go`
- `server/internal/memory/store.go`
- `server/internal/memory/store_test.go`
- `server/internal/memory/types.go`
- `server/internal/memory/validator.go`
- `server/internal/metrics/capacity.go`
- `server/internal/metrics/capacity_test.go`
- `server/internal/metrics/usage.go`
- `server/internal/metrics/usage_integration_test.go`
- `server/internal/metrics/usage_test.go`
- `server/internal/scheduler/review_drain_job.go`
- `server/internal/scheduler/write_lease.go`
- `server/internal/scheduler/write_lease_jobs.go`
- `server/internal/scheduler/write_lease_test.go`
- `server/internal/service/companyops_assignment.go`
- `server/internal/service/companyops_assignment_capacity_test.go`
- `server/internal/service/companyops_org.go`
- `server/internal/service/companyops_org_join_test.go`
- `server/internal/service/companyops_outcome_center.go`
- `server/internal/service/companyops_outcome_cursor_test.go`
- `server/internal/service/companyops_outcome_pagination_integration_test.go`
- `server/internal/service/issue.go`
- `server/internal/service/project_lifecycle.go`
- `server/internal/service/project_lifecycle_control.go`
- `server/internal/service/project_lifecycle_control_db_test.go`
- `server/internal/service/project_lifecycle_control_test.go`
- `server/internal/service/project_lifecycle_portfolio_test.go`
- `server/internal/service/project_lifecycle_test.go`
- `server/internal/service/review_cell.go`
- `server/internal/service/review_cell_test.go`
- `server/internal/service/review_drain.go`
- `server/internal/service/review_drain_test.go`
- `server/internal/service/task.go`
- `server/internal/workflow/definitions.go`
- `server/internal/workflow/definitions_test.go`
- `server/internal/workflow/engine.go`
- `server/internal/workflow/engine_test.go`
- `server/internal/workflow/hydrate.go`
- `server/internal/workflow/hydrate_integration_test.go`
- `server/internal/workflow/repository.go`
- `server/internal/workflow/repository_integration_test.go`
- `server/internal/workflow/types.go`
- `server/internal/workwall/activity_bridge.go`
- `server/internal/workwall/activity_bridge_test.go`
- `server/internal/workwall/assemble.go`
- `server/internal/workwall/assemble_test.go`
- `server/internal/workwall/service.go`
- `server/internal/workwall/service_integration_test.go`
- `server/internal/workwall/workflow_bridge.go`
- `server/internal/workwall/workflow_bridge_test.go`

### server/migrations (30)

- `server/migrations/280_issue_review_state.down.sql`
- `server/migrations/280_issue_review_state.up.sql`
- `server/migrations/281_issue_review_state_open_index.down.sql`
- `server/migrations/281_issue_review_state_open_index.up.sql`
- `server/migrations/282_agent_task_queue_review_kind.down.sql`
- `server/migrations/282_agent_task_queue_review_kind.up.sql`
- `server/migrations/283_agent_task_review_open_unique.down.sql`
- `server/migrations/283_agent_task_review_open_unique.up.sql`
- `server/migrations/284_canonical_write_lease.down.sql`
- `server/migrations/284_canonical_write_lease.up.sql`
- `server/migrations/285_review_drain_progress.down.sql`
- `server/migrations/285_review_drain_progress.up.sql`
- `server/migrations/320_lane_d_provider_usage_quota.down.sql`
- `server/migrations/320_lane_d_provider_usage_quota.up.sql`
- `server/migrations/340_lanee_outcome_list_ws_created_index.down.sql`
- `server/migrations/340_lanee_outcome_list_ws_created_index.up.sql`
- `server/migrations/341_workroom.down.sql`
- `server/migrations/341_workroom.up.sql`
- `server/migrations/342_workflow_memory_persistence.down.sql`
- `server/migrations/342_workflow_memory_persistence.up.sql`
- `server/migrations/345_employee.down.sql`
- `server/migrations/345_employee.up.sql`
- `server/migrations/346_dataset.down.sql`
- `server/migrations/346_dataset.up.sql`
- `server/migrations/347_dataset_product_type.down.sql`
- `server/migrations/347_dataset_product_type.up.sql`
- `server/migrations/348_base_table.down.sql`
- `server/migrations/348_base_table.up.sql`
- `server/migrations/349_base_seed_and_migrate.down.sql`
- `server/migrations/349_base_seed_and_migrate.up.sql`

### server/pkg (34)

- `server/pkg/db/generated/agent.sql.go`
- `server/pkg/db/generated/autopilot.sql.go`
- `server/pkg/db/generated/base.sql.go`
- `server/pkg/db/generated/chat.sql.go`
- `server/pkg/db/generated/comment.sql.go`
- `server/pkg/db/generated/companyops.sql.go`
- `server/pkg/db/generated/companyops_outcomes_cursor.sql.go`
- `server/pkg/db/generated/dataset.sql.go`
- `server/pkg/db/generated/employee.sql.go`
- `server/pkg/db/generated/inbox.sql.go`
- `server/pkg/db/generated/issue.sql.go`
- `server/pkg/db/generated/issue_property.sql.go`
- `server/pkg/db/generated/models.go`
- `server/pkg/db/generated/pipeline.sql.go`
- `server/pkg/db/generated/project.sql.go`
- `server/pkg/db/generated/project_lifecycle.sql.go`
- `server/pkg/db/generated/review_drain.sql.go`
- `server/pkg/db/generated/runtime.sql.go`
- `server/pkg/db/generated/workflow_memory.sql.go`
- `server/pkg/db/generated/workroom.sql.go`
- `server/pkg/db/queries/agent.sql`
- `server/pkg/db/queries/base.sql`
- `server/pkg/db/queries/comment.sql`
- `server/pkg/db/queries/companyops_outcomes_cursor.sql`
- `server/pkg/db/queries/dataset.sql`
- `server/pkg/db/queries/employee.sql`
- `server/pkg/db/queries/inbox.sql`
- `server/pkg/db/queries/issue.sql`
- `server/pkg/db/queries/pipeline.sql`
- `server/pkg/db/queries/project.sql`
- `server/pkg/db/queries/project_lifecycle.sql`
- `server/pkg/db/queries/review_drain.sql`
- `server/pkg/db/queries/workflow_memory.sql`
- `server/pkg/db/queries/workroom.sql`
