#!/usr/bin/env bash
set -euo pipefail
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd); out=${1:?archive path}; list=$(mktemp); trap 'rm -f "$list"' EXIT
find "$root" -maxdepth 1 -type f -printf '%f\n' | sort > "$list"
tar --sort=name --mtime='UTC 1970-01-01' --owner=0 --group=0 --numeric-owner -cf "$out" -C "$root" -T "$list"
sha256sum "$out" > "$out.sha256"
