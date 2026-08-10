# WO-HIVECREW-BOOTSTRAP-V0

## Objective

Establish HiveCrew as an independent, reproducible and DGX-checkpointed source
project without changing the running HiveCosm production system.

## In scope

- One-time import of the licensed fixed source revision.
- Independent Mac Git repository with no inherited remotes.
- HiveCrew product charter, provenance and authority boundary.
- Independent DGX bare repository and isolated development checkout.
- Exact revision, no-secret and no-upstream verification evidence.

## Out of scope

- Mass product rename or package rename.
- Database migration, registry activation or runtime-data import.
- Port assignment, service installation, production deployment or restart.
- 1421 source modification or UI integration.
- Upstream tracking, compatibility work or patch ingestion.

## Exit criteria

- Mac repository is clean and has no upstream remote.
- DGX repository and checkout are present under William's developer workspace.
- Mac and DGX resolve to the exact same HiveCrew revision.
- No tracked `.env` or secret material is introduced.
- Project archive and evidence record state `production_applied=false`.

## Next work order

`WO-HIVECREW-REIDENTITY-B1` may start only after this bootstrap is verified. It will
inventory and replace product identity in bounded slices while preserving database
and API compatibility until an explicit migration removes legacy identifiers.
