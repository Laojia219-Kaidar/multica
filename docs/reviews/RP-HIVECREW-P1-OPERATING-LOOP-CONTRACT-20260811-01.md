# HiveCrew P1 operating-loop contract review package

## Review identity

- Goal: `HIVECREW-OWNER-OPERATING-WORKBENCH-V1`
- Phase: `P1 — 真实链路与最小合同`
- Review type: phase drift/evidence review
- Baseline revision: `43d7c95dfbf50e3b53a328b2c35a6bd36a5ddf1d`
- Contract: `docs/goals/HIVECREW-OWNER-OPERATING-WORKBENCH-V1/PHASE-1-OPERATING-LOOP-CONTRACT.md`
- Submitted at: `2026-08-11T11:40:00+08:00`
- Visible conversation: `https://chatgpt.com/c/6a79f352-24d0-83e8-bec1-69d7b36595e3`
- Capability proof: the visible model selector showed `Pro, 5 of 5`, `Model GPT-5.6 Sol`, `Effort Pro`; account UI showed `Jia Wei Pro`.
- Reviewer authority: advisory only; no merge, implementation, deployment, production or completion authority.

## Evidence submitted

The review received the P1 source-audit facts and frozen P2 boundary:

- current Chat and Issue-to-daemon execution paths are real;
- current canonical writers are reusable but do not constitute WorkOrder, Assignment receipt, immutable Execution Receipt or formal outcome authority;
- mutable task/result/usage/message/comment/attachment records cannot be promoted into receipts by naming;
- WorkOrder, Employee, Company Project and Formal Artifact remain HiveCosm authorities;
- HiveCrew owns conversation, assignment/run, append-only receipt, durable ArtifactCandidate and append-only review/promotion events;
- P2 reuses Chat and implements one stable-ID, fail-closed, receipt-visible vertical slice.

No unprovided repository access, runtime test, database readback or production state was claimed.

## Reviewer verdict

`PASS WITH CORRECTIONS`

The reviewer judged the contract narrow enough to enter P2 test design and found no second WorkOrder, Employee, Project or Formal Artifact authority in the proposed boundary.

### Phase-critical corrections

1. Freeze the exact WorkOrder source ref, revision, digest, observed time and input digest at Assignment creation; a later WorkOrder revision cannot rewrite a Run receipt.
2. Enforce `Employee -> active IdentityBinding -> Agent UUID -> Assignment -> Run`; any missing or ambiguous binding fails closed before Assignment.
3. Separate `promotion_succeeded` from `authority_readback_confirmed`; the UI may show a Formal Artifact only after exact authority ref/revision/digest readback.

All three corrections were written into the P1 contract before phase close.

### Evidence gaps deferred to P2

- runtime claim/retry/callback/terminal behavior under the new receipt contract;
- receipt retention and query performance;
- durable object namespace, digest and lifecycle implementation;
- stable deep link and fail-closed browser journey;
- real pilot authority source and Promotion readback.

These gaps block product victory claims but do not block writing the P2 behavioral tests. Full navigation, Graph, QM, 100–200 Agent scheduling, full organization and production release remain non-blocking for P2.

## Orchestrator disposition

- `phase_critical_correction`: all three accepted and applied to the contract.
- `evidence_gap`: moved to the P2 test and browser acceptance matrix.
- `optional_improvement`: none added to the critical path.
- `owner_decision`: none required for P1 close.

Current truth after review: the contract is verified; no P2 software implementation, real pilot, DGX/1421 apply, database migration or Formal Artifact promotion has occurred yet.
