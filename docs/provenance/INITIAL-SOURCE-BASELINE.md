# Initial source baseline

## Custody record

- HiveCrew project: `PRJ-G71-HIVECREW`
- Bootstrap work order: `WO-HIVECREW-BOOTSTRAP-V0`
- Import date: `2026-08-11` (Asia/Shanghai)
- Import actor: `codex-unbound`
- Human principal: `William`
- Source repository path: `/Volumes/HiveData/hivecosm/HQ-50-代码仓库/01-源码下载/multica`
- Source revision: `beb3e9be65023f63bd5dfdbb0231ed41aa9f1cb8`
- Import method: one-time `git archive` of tracked files only
- HiveCrew baseline commit: `1595366ef28c050d70190514ea86ec0916724efe`
- Imported tracked files: `3,892`

## Independence declaration

The source snapshot was imported once under authorization supplied by the product
owner. HiveCrew does not track the former repository and does not configure its
`origin` or `upstream` remotes. The original `.git` directory, untracked files,
local `.env`, credentials, runtime data, and build caches were not imported.

The owner has stated that commercial use and white-label development are authorized.
This record preserves that owner-supplied assertion without publishing confidential
authorization material or independently expanding its scope.

The original `LICENSE` file is retained as source-provenance material. Any future
public licensing, distribution terms, trademark policy, or external publication
requires a separate owner decision and legal packaging review.

## Verification contract

A valid B0 repository must satisfy all of the following:

- `git remote -v` contains no Multica or other upstream remote.
- No tracked file named `.env` exists.
- The source baseline commit is an immutable ancestor of all HiveCrew development.
- Product-specific changes occur in later, atomic commits.
- Mac and DGX repositories resolve to the same declared revision after checkpointing.
