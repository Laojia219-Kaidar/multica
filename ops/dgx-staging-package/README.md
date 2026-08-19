# DGX staging operator package

This package is a fail-closed, candidate-scoped operator contract for compose project `multica-dgx-ultra`.

Input is one candidate directory containing `INTEGRATION-IDENTITY.json` and `compose.yaml`. The identity must bind final source revision/tree/archive SHA, backend/web image IDs and digests, Authority overlay SHA, compose SHA and rollback predecessor. Missing or mismatched identity exits before mutation.

`apply-staging.sh` and `rollback-staging.sh` are intentionally parameter-light and use `DOCKER_BIN`/`CURL_BIN` only for fake tests or an existing governed operator environment. They never print or persist secret values. Receipts are written under the candidate `receipts/` directory. External acceptance is a separate artifact contract and is not faked by the apply script.

The package does not grant Docker access, sudo, operator identity, or staging authorization. A governed `jiawei219` operator must execute the final candidate after independent review and exact Owner authorization.
