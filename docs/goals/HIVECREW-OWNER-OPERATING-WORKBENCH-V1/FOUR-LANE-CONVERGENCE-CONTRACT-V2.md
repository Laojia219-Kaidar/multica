# Four-lane formal convergence contract v2

## Authority and carriers

| Item | Canonical value |
| --- | --- |
| Goal | `HIVECREW-OWNER-OPERATING-WORKBENCH-V1` |
| HiveCrew Project | `3b0330e7-a2da-4f41-94ab-61c911af2820` |
| Progress truth | `CHECKLIST.yaml` in this directory |
| Integration worktree | `/Users/jiawei/hivecosm-worktrees/hivecrew-four-lane-formal-convergence-codex` |
| Integration branch | `work/hivecrew-four-lane-formal-convergence-codex` |
| Baseline | `f7667c8d7c540217c345d98beac33794e1f3e6d0` |
| Main integrator | `Shard` / Agent `86b86d76-09f4-42e3-bb7a-8113c81775e0` |
| Main runtime | `Prime Agent · DeepSeek V4 Pro 0813` |
| Model route | `deepseek/deepseek-v4-pro` |
| Primary host | MBP M5X / Runtime `cdad2451-0353-4aa6-b6fb-3672bebfa495` |
| Fallback host | Mac mini / Runtime `e6cec8bd-5e34-4f22-a3eb-81ead2f1fe63` |

## Candidate sources

| Lane | Path | Frozen observation | Consumption rule |
| --- | --- | --- | --- |
| W1 | `/Users/jiawei/hivecosm-worktrees/hivecrew-bases-v1` | `83ebb681` plus four dirty candidate files | consume by exact file/diff; never whole-tree overwrite |
| W2 | `/Users/jiawei/hivecosm-worktrees/hivecrew-project-lifecycle-closure` | `577f6cc` plus coordination log | consume lifecycle writers/tests by exact revision and evidence |
| W3 | `/Users/jiawei/hivecosm-worktrees/hivecrew-product-integration-mainline` | `8c456923` plus dirty/untracked convergence evidence | use clean HEAD as the first code cut, then verify against W1/W2/W4; dirty files are not automatically consumed |
| W4 | `/Users/jiawei/hivecosm-worktrees/hivecrew-w4-slice-w2` | clean `9836644f` | consume workflow/memory/work-wall slice by exact commit |

Each source remains owner-controlled and read-only to this convergence. The Join must record source revision, selected files, resulting revision and excluded changes.

## Fast-path cut decision

Read-only Git calibration on 2026-08-14 established that W4 commit `9836644f` is an ancestor of W3 `8c456923`; W3 history also contains explicit W1 bases and W2 lifecycle integration commits. W1 `83ebb681` and W2 `577f6cc` are not ancestors of W3, so their exact source revisions remain fidelity and completeness references rather than direct whole-branch merges.

Shard therefore starts from the clean W3 HEAD diff as the first code cut, excludes W3 dirty/untracked files until individually classified, and uses the W1/W2/W4 Task-linked manifests to find omissions or semantic drift. This avoids applying the same 200+ files twice while preserving independent source truth.

## Work allocation

| Role | Employee | Output | Write rule |
| --- | --- | --- | --- |
| Prime integrator | Shard | intake, conflict resolution, formal candidate | sole writer in integration worktree |
| Project lifecycle | Orion | W2 contract/patch inventory and tests | read-only source; patch manifest to Shard |
| Organization/base | Rowan | W1 contract/patch inventory and source truth | read-only source; patch manifest to Shard |
| Frontend/interaction | Willow | W4 UI and visual acceptance manifest | no shared-file writes; fixtures/screenshots allowed |
| Independent review | Gauss | contract, negative tests, lineage and regression verdict | reviewer != implementer |
| Repair/integration | Atelier | focused repairs after REVISE | only an isolated repair branch/worktree |
| Runtime/release | Keep | build, runtime provenance and rollback rehearsal | no source feature implementation |
| Portfolio dispatch | Shepherd | visible task status, blocker/receiver/wake-condition report | read-only control projection |

## Conflict control

1. `packages/views` shared shell, router files, generated SQL, migrations, compose files and release configuration are mutex zones.
2. A mutex blocks only the exact file/number/port; all other Goal-aligned work continues.
3. No worker edits a source lane and the Join at the same time.
4. Generated files are regenerated from their source; they are never hand-merged.
5. A candidate without revision, diff, tests and receiver remains `candidate_unknown`, not accepted.
6. A completed Task without output `Comment.source_task_id` or equivalent Task-linked receipt is not a delivered result. `Task.delivered_comment_ids` records claim-time input comments and must not be misused as output lineage.

## Acceptance

- Prime canary completes through a real HiveCrew Task and Task-linked Comment.
- W1–W4 intake matrix names exact revisions, selected files, excluded files, tests and risks.
- One clean integration revision builds and passes focused plus mainline tests.
- Independent reviewer returns a single `PASS` or `REVISE`; every REVISE creates a fresh repair task.
- Browser journeys verify current HiveCrew navigation, data, empty/loading/error states and work-wall live activity.
- Runtime `:3000/:8080` reports the accepted revision and can roll back to the prior revision.
- No second Department, Employee, Project, WorkOrder, Outcome or knowledge authority is introduced.

The canary's output-integrity and lineage defects block final acceptance, not candidate production. Once Mac mini execution is proven, Shard may continue to generate reviewed Git candidates while Keep repairs M5X and delivery lineage in parallel.
