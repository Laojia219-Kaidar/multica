#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec "$root/../../ops/dgx-staging-package/authority-bridge-resolve.sh" "$@"
