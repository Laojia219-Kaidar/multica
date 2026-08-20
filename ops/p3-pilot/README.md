# P3 four-role pilot P0 source-only candidate

Work identifier: `WO-C1-04-P3-P0-REPAIR-001`.

This directory contains two inert assets:

1. `pilot-preflight.sh` performs only authenticated project-resource reads and
   local Git object reads. It requires one exact project repository resource
   bound to b884/ref/tree and exits 42 before any Issue, worktree or dispatch
   mutation when the resource is stale, duplicated or mismatched.
2. `review-cell.staging.override.yaml` is an explicit staging-only compose
   overlay. It enables the Review Cell with Quinn's exact HiveCrew Agent UUID,
   WIP 1 and the existing Authority-only server boundary. It deliberately does
   not set a coordinator; PASS therefore remains restricted to a member Owner.

The overlay is not a deployment claim. An operator may use it only after a
separate package review and staging apply authorization. The default server
state remains fail-closed (`REVIEW_CELL_ENABLED` missing or anything other than
the exact string `true` means disabled).

## Required order for a future pilot operator

1. P2 has independently succeeded.
2. Apply/accept any exact repository-resource binding transaction.
3. Run `pilot-preflight.sh` and archive its zero-mutation PASS receipt.
4. Only then request separate authorization to create the four Issues and
   isolated worktrees and call dispatch.

No step in this source-only candidate creates those objects.

## Rollback

Before staging apply, rollback is removal/rejection of this candidate branch;
there is no runtime state to undo. After a future separately authorized apply,
rollback removes this overlay from the exact compose file set and recreates the
backend with the accepted predecessor configuration, then proves the Review
Cell routes/listeners are absent. Database rows and artifact history are never
deleted by this rollback.
