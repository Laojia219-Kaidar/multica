# P3 four-role pilot P0 source-only candidate R2

Work identifier: `WO-C1-04-P3-P0-REPAIR-002`.

R2 preserves R1 commit `28731a3ad7f9ebcce0c48a72362aa36b0c945ff5`
and its independent `REVISE` review. It closes that review's only blocker:
the live operator entrypoint no longer reads `P3_PILOT_CLI`,
`P3_PILOT_GIT`, or any test-only environment variable.

## Operator contract

Only these two files are operator assets:

1. `pilot-preflight.sh` performs authenticated project-resource reads and
   local Git object reads. It opens the exact HiveCrew CLI and Git binaries
   once, validates canonical non-symlink regular paths, owner, mode, SHA-256
   and exact version, executes through the validated file descriptors, and
   rechecks path identity between operations. It resolves the integration ref
   once and derives the tree from that resolved commit object.
2. `review-cell.staging.override.yaml` is an inert staging-only overlay. It
   enables the Review Cell with Quinn's exact HiveCrew Agent UUID, WIP 1 and
   the existing Authority-only server boundary. It deliberately does not set
   a coordinator, so PASS remains restricted to a member Owner.

The committed fakes, harness and source-only suite under `tests/` are explicitly
excluded from the packaged operator contract. The harness rewrites only a
temporary copy and changes its success status to
`PASS_TEST_ONLY_NOT_OPERATOR_RECEIPT`; it cannot produce a live PASS receipt.

The overlay is not a deployment claim. An operator may use it only after a
separate package review and staging apply authorization. The default server
state remains fail-closed (`REVIEW_CELL_ENABLED` missing or anything other
than the exact string `true` means disabled).

## Required order for a future pilot operator

1. P2 has independently succeeded.
2. Apply and accept an exact project repository-resource binding transaction.
3. Run the reviewed `pilot-preflight.sh` without arguments and archive its
   zero-mutation PASS receipt.
4. Only then request separate authorization to create the four Issues and
   isolated worktrees and call dispatch.

The current live resource remains stale, so the default live preflight exits
42 before Git resolution or any pilot mutation. No step in this source-only
candidate creates pilot objects.

## Rollback

Before staging apply, rollback is rejection of the R2 branch; there is no
runtime state to undo. After a future separately authorized apply, rollback
removes the overlay from the exact compose file set and recreates the backend
with the accepted predecessor configuration, then proves Review Cell routes
and listeners are absent. Database rows and artifact history are never deleted
by this rollback.
