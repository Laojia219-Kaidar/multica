# W1-W4 source intake snapshot v2

Observed at `2026-08-14T11:03:00+08:00` against canonical main `f7667c8d7c540217c345d98beac33794e1f3e6d0`.

| Lane | Branch | HEAD | Changed paths vs main | Dirty paths | Relationship to W3 |
| --- | --- | --- | ---: | ---: | --- |
| W1 | `work/hivecrew-bases-v1.1` | `83ebb6819b20f52d2306fea1843cfd19684848fb` | 54 | 4 | not ancestor; W3 has explicit W1 integration commits |
| W2 | `work/hivecrew-project-lifecycle-closure` | `577f6cc968cc33239bee2f7cd91be8dea42b4d12` | 74 | 1 | not ancestor; W3 has explicit W2 lifecycle join evidence |
| W3 | `work/hivecrew-product-integration-mainline` | `8c45692399bc460edc1aefbfdf10ee7dc8a6344f` | 274 | 65 | first code cut; dirty/untracked excluded by default |
| W4 | `work/hivecrew-w4-slice-w2` | `9836644fdc95e8db00b264a2d20f80e7ebc12669` | 230 | 0 | exact ancestor of W3 |

## Exact cross-lane mutexes

W1 and W2 both change `packages/core/api/client.ts`, `server/cmd/server/router.go` and `server/pkg/db/generated/models.go`.

W1 and W4 share the workroom vertical slice plus global client/paths/sidebar/i18n/package/router/generated models. W2 and W4 share the full project lifecycle slice plus global client/types/router/main/task/generated models.

Therefore the following are serialized only at the Join:

- `packages/core/api/client.ts`
- `packages/core/paths/paths.ts`
- `packages/core/types/*`
- `packages/views/layout/app-sidebar.tsx` and locale layout files
- `packages/views/projects/*` and `packages/views/workrooms/*`
- `server/cmd/server/main.go` and `server/cmd/server/router.go`
- `server/internal/service/task.go`
- all migration numbering
- all `server/pkg/db/generated/*`

All other source audits, tests, fixtures, visual journeys and Task-linked manifests remain parallel.

## Consumption rule

1. Diff clean W3 HEAD against canonical main as the initial cut.
2. Never consume W3 dirty/untracked paths wholesale.
3. Use W1/W2/W4 manifests to check required behavior, tests and excluded changes.
4. Regenerate generated SQL only after the final migration/query set is frozen.
5. Record every selected source revision and the resulting integration revision.
