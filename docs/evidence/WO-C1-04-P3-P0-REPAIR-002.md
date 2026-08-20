# WO-C1-04 P3 P0 repair R2 source-only evidence

## Scope

This successor preserves R1 commit `28731a3ad7f9ebcce0c48a72362aa36b0c945ff5`
and review evidence with SHA256SUMS SHA
`26a87dd0752b94e7a12ea6297b8768494c30749066c89f70b685b09f58679563`.
It repairs only the R1 executable-override blocker. The Work Entry reviewer
identity, Review Cell staging overlay and Owner promotion boundary remain
unchanged from R1.

## Live tool custody

The live preflight accepts no arguments and ignores ambient
`P3_PILOT_CLI`/`P3_PILOT_GIT`. It binds:

- HiveCrew CLI `/home/williamdev/.local/bin/hivecrew-wo-c1-04-b884ac5b7df3`,
  SHA-256 `e4eac92e4133effe85984285159d6194c8f6894aecbbdadac4d06c1622596ce9`,
  uid/gid `1006:1006`, mode `0755`, exact b884 linux/arm64 version.
- Git `/usr/bin/git`, SHA-256
  `aa6540695d076182256dd6e96c8b302e4d56381e3000bbfd5c71bbdfe94a4942`,
  uid/gid `0:0`, mode `0755`, version `git version 2.43.0`.

Both are required to be canonical non-symlink regular files. Each is opened
once, validated and invoked through its file descriptor. Path identity is
rechecked after version/resource/object reads. The source ref is resolved once;
the expected tree is derived from that exact resolved commit.

## Test boundary

Test substitution is confined to
`ops/p3-pilot/tests/pilot-preflight-test-harness.py`. It renders a temporary
copy with a visibly test-only PASS status. No test hook or executable override
exists in `pilot-preflight.sh`, and all test-only files are excluded from the
operator contract in `CANDIDATE-MANIFEST.json`.

Negative coverage includes ambient override attempts against the real live
entrypoint, symlink/wrong canonical path/digest/version, resource URL/ref/count,
revision/tree drift and a pathname replacement after the exact CLI descriptor
is opened. Every negative exits 42 before any Issue, worktree, dispatch or
database mutation. The current live resource remains stale and returns rc42.

## Unchanged boundaries

This is source-only. It does not change the integration ref, project resource,
Review Cell runtime state, Issue, Agent, Runtime, database, daemon, model,
credential, production, RUN-06, GPU, training, SGLang or ports 8000/8001.
Independent R2 review remains required before integration or staging apply.
