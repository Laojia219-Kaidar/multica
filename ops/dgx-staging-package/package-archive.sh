#!/usr/bin/env bash
set -Eeuo pipefail
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
out=${1:?usage: package-archive.sh OUTPUT_TAR}
out_dir=$(CDPATH= cd -- "$(dirname -- "$out")" && pwd)
out_base=$(basename -- "$out")
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/package"

while IFS= read -r name; do
  cp -p -- "$root/$name" "$tmp/package/$name"
done < <(find "$root" -maxdepth 1 -type f -printf '%f\n' | sort)

python3 - "$tmp/package" <<'PY'
import hashlib, json, pathlib, sys
root = pathlib.Path(sys.argv[1])
files = []
for path in sorted(p for p in root.iterdir() if p.is_file() and p.name != "MANIFEST.json"):
    files.append({"path": path.name, "sha256": hashlib.sha256(path.read_bytes()).hexdigest()})
(root / "MANIFEST.json").write_text(json.dumps({"schema":"HiveCrewStagingOperatorPackageV1","files":files}, sort_keys=True, separators=(",", ":")) + "\n")
PY

tar --sort=name --mtime='UTC 1970-01-01' --owner=0 --group=0 --numeric-owner \
  -cf "$out_dir/$out_base" -C "$tmp/package" .
(cd "$out_dir" && sha256sum "$out_base" > "$out_base.sha256")
cp -- "$tmp/package/MANIFEST.json" "$out_dir/$out_base.manifest.json"
printf 'archive=%s sha256_sidecar=%s manifest=%s\n' \
  "$out_dir/$out_base" "$out_dir/$out_base.sha256" "$out_dir/$out_base.manifest.json"
