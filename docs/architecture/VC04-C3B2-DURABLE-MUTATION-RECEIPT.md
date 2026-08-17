# VC04 C3b2 Durable Mutation Receipt Contract

Status: frozen for the first C3b2 vertical slice, 2026-08-17.

## Scope and authority boundary

C3b2 consumes a completed task and the existing CompanyOps artifact materializer's
durable candidate object. C3b1b remains the mediated checkout transport slice;
it does not produce the candidate object. `export` materializes an immutable
candidate object from completed task material; it does not perform an authority
write. `promote` is the
Owner-approved Formal Artifact command. `push` is an authenticated Authority
POST/write with a stable `promotion_id`; it is not a Git push. C3a's
`no_push` policy remains in force and is never bypassed by this contract.

The server is authoritative for the task, the persisted writer-lease target
claim, current lease rows, completion receipt, promotion claim, and delivery
outbox. The external Authority is authoritative only for the response and
readback of its accepted Formal Artifact. A daemon-supplied URL, ref, mutex,
holder, generation, or token is never accepted as the authoritative target.

## Completion receipt

For an enforced writer-lease task, `CompleteTaskWithWriterLeaseProof` locks the
task and current lease rows, verifies migration-262 token and generation, and
writes one completion receipt in that same database transaction. The receipt
is append-only and keyed by task identity. It contains:

* the canonical migration-406 target digest;
* a canonical proof snapshot for each resource: resource id, mutex key, fence
  generation, and SHA-256 of the lease token (never the plaintext token);
* an overall receipt digest over the canonical snapshot and task/target
  identity.

An exact replay returns the existing receipt only when every bound value and
digest is identical. Any drift, missing authoritative row, stale token, stale
generation, or malformed proof fails closed. A task without authoritative
targets has an empty canonical proof snapshot and still has a deterministic
receipt when enforcement requires one.

## Promotion claim and delivery state machine

The canonical promotion claim payload binds `source_task_id`, the migration-406
target digest, and the completion-receipt digest in addition to the existing
candidate, approval, actor, and object fields. Its digest is the idempotency
fence. A stable `promotion_id` identifies one logical promotion forever.

The durable delivery outbox uses these states:

```text
pending -> dispatching -> succeeded -> readback_confirmed
                  \-> failed
```

Before an external Authority POST, the server durably claims `pending` as
`dispatching` with compare-and-set semantics. Retries may issue a POST only
with the same stable `promotion_id` and byte-equivalent canonical payload. A
different payload is a conflict and is never sent. A successful POST stores a
sanitized response receipt; a subsequent GET/readback stores the sanitized
readback receipt and advances to `readback_confirmed`. Transport ambiguity
must remain recoverable (a stale `dispatching` row may be explicitly reclaimed
only with the same payload and stable id); it must not be guessed as success.
When a formal reference is available, stale-claim recovery attempts an exact
Authority GET/readback before another POST. A 404 or explicit no-write result
is the only basis for retrying the same claim; other transport/conflict
outcomes remain recoverable. GET validation compares formal reference,
candidate id/revision/digest, approval event, owner/review identity, and
authority lookup snapshots; HTTP 200 alone is insufficient.

The outbox is operational mutable state guarded by CAS. Completion receipts,
promotion claims, request payload digests, response receipts, and readback
receipts are durable evidence and are not deleted or silently overwritten.

## Crash and replay invariants

1. A committed task completion has exactly one matching completion receipt.
2. A committed promotion claim and outbox row cannot be rebound to another
   task, target digest, completion receipt, candidate, or payload.
3. Crash before the completion transaction commits leaves no terminal task and
   no completion receipt.
4. Crash after `dispatching` but before the Authority response is recoverable;
   no second payload may be invented.
5. Exact retries are idempotent. Drift is fail-closed.
6. `succeeded` is not `readback_confirmed`; only verified Authority GET
   readback may establish the latter.

## C3b1b boundary and non-goals

C3b1b remains responsible for mediated checkout transport. The existing
CompanyOps materializer remains responsible for producing the immutable
candidate object consumed here. C3b2 does not redesign lease acquisition,
daemon transport, Git checkout/push, object-store byte upload, approval policy,
or the Authority API. It does not store plaintext lease tokens, perform an
unauthenticated write, or infer success from a lost response. The existing
PromoteArtifact Authority POST and independent GET readback wiring are in C3b2
scope and must pass through this receipt and outbox contract.
