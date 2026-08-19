# DGX staging operator package

This package is a fail-closed, candidate-scoped operator contract for compose project `multica-dgx-ultra`.

Input is one candidate directory containing `INTEGRATION-IDENTITY.json` and `compose.yaml`. The identity must bind final source revision/tree/archive SHA, backend/web image IDs and digests, Authority overlay SHA, compose SHA, rollback predecessor, and the hashes plus locked endpoint contract of every packaged Authority bridge asset. Missing or mismatched identity exits before mutation.

`apply-staging.sh` and `rollback-staging.sh` are intentionally parameter-light and use `DOCKER_BIN`/`CURL_BIN` only for fake tests or an existing governed operator environment. They never print or persist secret values. Receipts are written under the candidate `receipts/` directory. External acceptance is a separate artifact contract and is not faked by the apply script.

Apply and rollback bind the single governed deploy directory internally:
`/srv/hivecosm/52-staging/multica-dgx-ultra/4ab2c72c27e0ecf38f32cd3f6f1274350a80efca`.
The caller cannot provide or override it. Every Compose config or mutation uses
exactly that directory's `/.env`; canonical path equality and a regular,
non-empty env file are validated before receipts, overrides, or container
changes. Env content is never read or copied by the operator package. The
read-only count collector keeps
`default_transaction_read_only=on` and converts a textual migration version such
as `415_project_revision` to the numeric schema top `415`.
After Compose mutation, backend readiness is bounded to 30 attempts with a
one-second delay by default, so a startup connection refusal is retried without
weakening persistent-failure rollback. Test environments may reduce the validated
attempt/delay bounds. The EXIT trap uses explicit script-level rollback state so
all post-mutation command, health, and version failures restore the exact predecessor.

The Authority bridge is candidate-contained and uses the exact backend image;
there is no third image or package-external source build. Before mutation, the
operator verifies the backend's exact Compose project/service, unique Docker
bridge network ID, dynamic 172.16/12 gateway, and absence of every host listener
on port 3151. It then starts only the sidecar, waits for Docker health and an
HTTP 200 at `<gateway>:3151/bff/health`, and only then starts backend/frontend.
Rollback restores the exact predecessor, removes the unique label-bound sidecar,
and proves both zero orphan containers and zero port-3151 listeners.

The package does not grant Docker access, sudo, operator identity, or staging authorization. A governed operator may execute only a reviewed candidate under the active Owner/Goal staging authority.
