#!/usr/bin/env python3
"""Create a non-installable Qwen foundation copy with synthetic trust anchors."""

from __future__ import annotations

import hashlib
import os
import shutil
import stat
import subprocess
import sys
from pathlib import Path


def shell_single_quote(value: str) -> str:
    if "\n" in value or "\r" in value or "\x00" in value:
        raise ValueError("unsafe fixture value")
    return "'" + value.replace("'", "'\"'\"'") + "'"


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


def regular_metadata(path: Path) -> tuple[str, str, str, str]:
    info = path.lstat()
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
        raise ValueError(f"fixture is not regular: {path}")
    return (
        format(stat.S_IMODE(info.st_mode), "o"),
        str(info.st_uid),
        str(info.st_gid),
        hashlib.sha256(path.read_bytes()).hexdigest(),
    )


def main() -> int:
    if len(sys.argv) != 8:
        print(
            "usage: render-foundation SOURCE OUTPUT LAUNCHER REAL_HOME SECRET LANDLOCK QWEN",
            file=sys.stderr,
        )
        return 64
    source, output, launcher, real_home, secret, landlock, qwen = map(Path, sys.argv[1:])
    if output.exists() or output.is_symlink():
        raise ValueError("test-only foundation output must be absent")
    if not source.is_dir() or source.is_symlink():
        raise ValueError("foundation source must be a real directory")
    shutil.copytree(source, output, symlinks=True)

    qwen_mode, uid, gid, qwen_sha = regular_metadata(qwen)
    home_info = real_home.lstat()
    if stat.S_ISLNK(home_info.st_mode) or not stat.S_ISDIR(home_info.st_mode):
        raise ValueError("fixture home must be a real directory")
    if (str(home_info.st_uid), str(home_info.st_gid)) != (uid, gid):
        raise ValueError("fixture home owner/group mismatch")

    preflight = output / "bin" / "qwen-preflight"
    data = preflight.read_text(encoding="utf-8")
    for name, value in {
        "QWEN_PREFLIGHT_REAL_HOME": str(real_home),
        "QWEN_PREFLIGHT_SECRET_FILE": str(secret),
        "QWEN_PREFLIGHT_BIN": str(qwen),
        "QWEN_PREFLIGHT_EXPECTED_UID": uid,
        "QWEN_PREFLIGHT_EXPECTED_GID": gid,
        "QWEN_PREFLIGHT_EXPECTED_MODE": qwen_mode,
        "QWEN_PREFLIGHT_EXPECTED_SHA256": qwen_sha,
    }.items():
        data = replace_once(data, name, value)
    preflight.write_text(data, encoding="utf-8")
    os.chmod(preflight, 0o755)

    rendered_launcher = output / "bin" / "qwen-landlock-launcher.sh"
    if rendered_launcher.exists() or rendered_launcher.is_symlink():
        rendered_launcher.unlink()
    launcher_renderer = Path(__file__).with_name(
        "render-qwen-landlock-launcher-for-test.py"
    )
    subprocess.run(
        [
            sys.executable,
            str(launcher_renderer),
            str(launcher),
            str(rendered_launcher),
            str(real_home),
            str(secret),
            str(landlock),
            str(qwen),
        ],
        check=True,
        env={"PATH": "/usr/bin:/bin"},
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
