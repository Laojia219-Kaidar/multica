# WO-HIVECREW-REIDENTITY-B1

## Objective

Turn the independently held source baseline into a visibly and operationally
distinct HiveCrew candidate while preserving only the minimum compatibility paths
required for an orderly migration.

## Actor and custody

- Human principal: William
- Employee: Coco (`KT-002`)
- Task role: CEO, explicitly assigned by William on 2026-08-11
- Carrier: Codex Desktop / gpt-5.6-terra high
- Base revision: `bc12f04fb076f5f054d36ba7b3d84faea522e14a`
- Branch: `work/wo-hivecrew-reidentity-b1`

## First slice

1. Create a shared HiveCrew product-identity contract.
2. Re-identify Web metadata and desktop packaging/runtime surfaces.
3. Remove inherited upstream publishing and cloud fallback behavior.
4. Add a new HiveCrew config path with bounded legacy read compatibility.
5. Add automated checks for active-surface identity regressions.
6. Run focused tests, type checks and a candidate build where dependencies allow.

## Unchanged boundaries

- No 1421, Engine, BFF, database, registry, QM or production-service changes.
- No mass package namespace, Go module, SQL or environment-variable rename.
- No public domain, app store, release feed or external publishing decision.
- No upstream remote or update channel.

## Exit states

The first slice may reach `verified_local` and a DGX source checkpoint. It does not
become `staged`, `production_applied` or `accepted` without a separate candidate
runtime and owner-facing browser/desktop acceptance.
