#!/usr/bin/env python3
"""Render a test-only launcher from production bytes with fixed fixture identities.

The production launcher never reads path overrides. Tests make a distinct copy
whose canonical constants are replaced exactly once before it is executed.
"""

from __future__ import annotations

import hashlib
import os
import stat
import sys
from pathlib import Path


def shell_single_quote(value: str) -> str:
    if "\n" in value or "\r" in value or "\x00" in value:
        raise ValueError("unsafe fixture path")
    return "'" + value.replace("'", "'\"'\"'") + "'"


def metadata(path: Path) -> tuple[str, str, str, str]:
    info = path.lstat()
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
        raise ValueError(f"fixture executable is not regular: {path}")
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    return format(stat.S_IMODE(info.st_mode), "o"), str(info.st_uid), str(info.st_gid), digest


def replace_once(data: str, name: str, value: str) -> str:
    prefix = f"readonly {name}="
    lines = data.splitlines(keepends=True)
    matches = [index for index, line in enumerate(lines) if line.startswith(prefix)]
    if len(matches) != 1:
        raise ValueError(f"expected one {name} assignment, got {len(matches)}")
    index = matches[0]
    newline = "\n" if lines[index].endswith("\n") else ""
    lines[index] = prefix + shell_single_quote(value) + newline
    return "".join(lines)


def main() -> int:
    if len(sys.argv) != 7:
        print(
            "usage: render SOURCE OUTPUT REAL_HOME SECRET LANDLOCK QWEN",
            file=sys.stderr,
        )
        return 64
    source, output, real_home, secret, landlock, qwen = map(Path, sys.argv[1:])
    if output.exists() or output.is_symlink():
        raise ValueError("test-only output must be absent")
    landlock_mode, uid, gid, landlock_sha = metadata(landlock)
    qwen_mode, qwen_uid, qwen_gid, qwen_sha = metadata(qwen)
    if (qwen_uid, qwen_gid) != (uid, gid):
        raise ValueError("fixture executable owner/group mismatch")
    if real_home.lstat().st_uid != int(uid) or real_home.lstat().st_gid != int(gid):
        raise ValueError("fixture home owner/group mismatch")
    data = source.read_text(encoding="utf-8")
    replacements = {
        "QWEN_CANONICAL_REAL_HOME": str(real_home),
        "QWEN_CANONICAL_SECRET_FILE": str(secret),
        "QWEN_CANONICAL_LANDLOCK_EXEC": str(landlock),
        "QWEN_CANONICAL_QWEN_BIN": str(qwen),
        "QWEN_EXPECTED_UID": uid,
        "QWEN_EXPECTED_GID": gid,
        "QWEN_EXPECTED_LANDLOCK_MODE": landlock_mode,
        "QWEN_EXPECTED_QWEN_MODE": qwen_mode,
        "QWEN_EXPECTED_LANDLOCK_SHA256": landlock_sha,
        "QWEN_EXPECTED_QWEN_SHA256": qwen_sha,
    }
    for name, value in replacements.items():
        data = replace_once(data, name, value)
    output.write_text(data, encoding="utf-8")
    os.chmod(output, 0o755)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
