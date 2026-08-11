# HiveCrew

**HiveCrew — HiveCosm 数字员工协作与执行系统**

## Product identity

HiveCrew is a first-party HiveCosm product. Its purpose is to give William one
operational workspace for communicating with digital employees, assigning work,
organizing teams, configuring execution resources, observing runs, reviewing
artifacts, and promoting accepted outcomes back into the governed HiveCosm system.

HiveCrew is developed independently. The initial code is a one-time licensed
source baseline; there is no upstream relationship, update channel, merge route,
or compatibility promise with the former product.

## Place in the HiveCosm system

```text
1421 Owner Console
        |
        v
HiveCrew interaction and execution kernel
        |
        +--> runtime / model / harness adapters
        +--> conversation / assignment / run / receipt
        +--> temporary artifacts and review
        |
        v
HiveCosm anti-corruption layer
        |
        v
Company registries, governance, projects, knowledge, QM and accepted artifacts
```

HiveCrew is the operating surface and execution kernel. It does not become a
second company database. The employee registry remains the employee identity
authority; project lifecycle remains the project authority; governance remains
the policy and decision authority; the knowledge layer remains the knowledge
authority; QM remains the human/digital-worker joint-work environment.

## First complete vertical slice

The first product slice must prove this real loop:

1. Select an employee from the HiveCosm employee registry.
2. Resolve the employee's exact executable Agent binding.
3. Create or open a HiveCosm WorkOrder.
4. Converse with the employee and assign the WorkOrder in HiveCrew.
5. Execute through the configured Runtime, Model, API credential reference and Harness.
6. Produce a temporary artifact plus an execution receipt.
7. Review, request revision, or promote through an explicit HiveCosm command.
8. Record the accepted formal artifact and preserve rollback/provenance.

No graph-wide redesign, QM rewrite, or production 1421 replacement is required to
complete this slice.

## Development phases

The accepted company-object, workforce and execution-resource design direction for
B2 is recorded in
[`docs/architecture/HIVECREW-B2-COMPANY-OBJECT-WORKFORCE-MODEL.md`](docs/architecture/HIVECREW-B2-COMPANY-OBJECT-WORKFORCE-MODEL.md).
It is a design baseline, not an implementation or production-state claim.

- **B0 — Independent custody:** source baseline, independent Git, project charter,
  provenance, DGX development workspace, no upstream and no secrets.
- **B1 — Product re-identity:** HiveCrew names, packages, assets, URLs, service units,
  migration compatibility map, and product documentation.
- **B2 — HiveCosm domain pack:** employee, department, position, WorkOrder, project,
  governance and artifact read models through the anti-corruption layer.
- **B3 — Owner workbench:** three-level navigation, resizable panes, inbox, employee
  conversations, work queues, interactive dossiers and owner feedback.
- **B4 — Execution plane:** runtime/model/API/harness separation, routing, quotas,
  concurrent workers, receipts, retries, pause/resume and failure recovery.
- **B5 — Promotion loop:** temporary-to-formal artifact promotion, owner review,
  revision, rollback and audit chain.
- **B6 — 1421 integration:** embed HiveCrew as the digital-employee operating domain
  of the 1421 owner console; candidate port first, then one governed release transaction.

## Current state

B0 independent custody is complete. B1 product re-identity has a verified
candidate baseline recorded in `docs/evidence/WO-HIVECREW-REIDENTITY-B1-RESULT.md`;
it is not a production deployment, registry activation, or 1421 release.

William activated `HIVECREW-OWNER-OPERATING-WORKBENCH-V1` on 2026-08-11. P1
froze the source-backed operating-loop contract; the current frontier is P2's
first real Owner-to-Outcome vertical slice. Execution state is owned only by
`docs/goals/HIVECREW-OWNER-OPERATING-WORKBENCH-V1/CHECKLIST.yaml`; the first
implementation target remains one real Owner-to-Outcome loop rather than a
graph-wide UI redesign.
