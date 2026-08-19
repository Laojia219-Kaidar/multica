#!/usr/bin/env bash
set -Eeuo pipefail
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
for script in common.sh precheck.sh collect-readonly-counts.sh apply-staging.sh rollback-staging.sh \
  authority-bridge-resolve.sh authority-bridge-port-check.sh authority-bridge-stop.sh \
  package-archive.sh test-collect-readonly-counts.sh test-state-machine.sh; do
  bash -n "$root/$script"
done
jq -e '."$schema" == "https://json-schema.org/draft/2020-12/schema" and .additionalProperties == false and .properties.accepted.const == false and .properties.production_unchanged.const == true and .properties.run_06_unchanged.const == true' "$root/EXTERNAL-ACCEPTANCE.schema.json" >/dev/null
"$root/test-collect-readonly-counts.sh"
"$root/test-state-machine.sh"
echo fake_static_and_state_machine=PASS
