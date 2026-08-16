# HiveCrew Repository Instructions

HiveCrew is an independently developed HiveCosm product. It was created from a
one-time licensed source import, but it does not track, merge, fetch, or rebase
from the former product or any other upstream project.

Before changing this repository, read these files in order:

1. `HIVECREW.md` — product identity, authority boundaries, and development policy.
2. `docs/architecture/WRITE-AUTHORITY-MATRIX.md` — which system may write each kind of truth.
3. `CLAUDE.md` — inherited technical architecture and code-quality rules.
4. The nearest directory-specific `AGENTS.md` or `CLAUDE.md`, if present.

## Non-negotiable rules

- Do not add a remote that points to Multica or configure any upstream-sync automation.
- Do not copy commits, patches, releases, or migrations from an upstream source unless
  William explicitly authorizes a new, independently reviewed source import.
- Use the product name `HiveCrew` for all newly created product surfaces.
- Preserve `docs/provenance/INITIAL-SOURCE-BASELINE.md` and the original license file.
- Never turn HiveCrew into a second authority for employees, departments, positions,
  company projects, governance, knowledge, or accepted artifacts.
- HiveCrew owns interaction and execution state: conversations, assignments, task runs,
  runtime bindings, temporary artifacts, and execution receipts.
- HiveCosm registries and governed APIs own company truth. Consume them through an
  anti-corruption layer and write back only through explicit commands.
- Keep production port 1421 and current DGX services unchanged unless a separate release
  work order explicitly authorizes deployment.
- Use isolated branches/worktrees, explicit file staging, narrow tests, and atomic commits.
- Never use `git add .` or `git add -A`.
- Never commit `.env`, secret values, access tokens, callback URLs, or local credentials.

## Product change sequence

1. Define the owner-facing workflow and its source of truth.
2. Write or update a behavioral test.
3. Implement the smallest complete vertical slice.
4. Verify interaction, persistence, execution receipt, and rollback separately.
5. Record the exact revision and limitations before any candidate release.

The active execution contract is `HIVECREW-OWNER-OPERATING-WORKBENCH-V1`
under project `PRJ-G71-HIVECREW`. Its sole progress-state authority is
`docs/goals/HIVECREW-OWNER-OPERATING-WORKBENCH-V1/CHECKLIST.yaml`. The B1
re-identity result remains a verified candidate baseline, not the active work
frontier. Product-facing code must continue to follow the compatibility classes
in `docs/architecture/PRODUCT-IDENTITY-COMPATIBILITY-MAP.md`.

## Production deploy lock (2026-08-16)

- This line currently HOLDS the production deploy lock for compose project
  `multica`. See `/Volumes/HiveData/hivecosm/.prod-multica-deploy/PROTOCOL.md`
  and `LOCK`. Keep following the protocol: verify image binary symbols before
  deploy, never retag another line's tags, and expect other agents to read
  this protocol before touching prod.
