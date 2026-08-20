#!/usr/bin/env python3
"""Render an explicit test-only copy of the live P3 preflight.

The live operator script has no dependency-injection surface. This harness is
kept under tests/, rewrites only a temporary copy, and changes the PASS status
so its output cannot be mistaken for an operator receipt.
"""

from __future__ import annotations

import argparse
import hashlib
import os
import pathlib


def sha256(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def replace_once(source: str, old: str, new: str) -> str:
    if source.count(old) != 1:
        raise SystemExit(f"expected exactly one source binding: {old}")
    return source.replace(old, new, 1)


def bind_line(source: str, name: str, value: str) -> str:
    prefix = f'readonly {name}="'
    matches = [line for line in source.splitlines() if line.startswith(prefix)]
    if len(matches) != 1:
        raise SystemExit(f"expected exactly one {name} binding")
    return replace_once(source, matches[0], f'readonly {name}="{value}"')


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--operator", required=True, type=pathlib.Path)
    parser.add_argument("--output", required=True, type=pathlib.Path)
    parser.add_argument("--cli", required=True, type=pathlib.Path)
    parser.add_argument("--git", required=True, type=pathlib.Path)
    parser.add_argument("--cli-canonical")
    parser.add_argument("--git-canonical")
    parser.add_argument("--cli-sha256")
    parser.add_argument("--git-sha256")
    parser.add_argument("--cli-version-line-1", default="test hivecrew b884")
    parser.add_argument("--cli-version-line-2", default="test linux/arm64")
    parser.add_argument("--git-version", default="git version test-p3")
    args = parser.parse_args()

    operator = args.operator.resolve(strict=True)
    cli_lstat = args.cli.lstat()
    git_lstat = args.git.lstat()
    source = operator.read_text(encoding="utf-8")
    if "P3_PILOT_CLI" in source or "P3_PILOT_GIT" in source or "P3_TEST_" in source:
        raise SystemExit("live operator contains a test or ambient override surface")

    bindings = {
        "CLI_PATH": str(args.cli.absolute()),
        "CLI_CANONICAL": args.cli_canonical or str(args.cli.resolve(strict=True)),
        "CLI_UID": str(cli_lstat.st_uid),
        "CLI_GID": str(cli_lstat.st_gid),
        "CLI_MODE": format(cli_lstat.st_mode & 0o777, "o"),
        "CLI_SHA256": args.cli_sha256 or sha256(args.cli),
        "CLI_VERSION_LINE_1": args.cli_version_line_1,
        "CLI_VERSION_LINE_2": args.cli_version_line_2,
        "GIT_PATH": str(args.git.absolute()),
        "GIT_CANONICAL": args.git_canonical or str(args.git.resolve(strict=True)),
        "GIT_UID": str(git_lstat.st_uid),
        "GIT_GID": str(git_lstat.st_gid),
        "GIT_MODE": format(git_lstat.st_mode & 0o777, "o"),
        "GIT_SHA256": args.git_sha256 or sha256(args.git),
        "GIT_VERSION": args.git_version,
    }
    for name, value in bindings.items():
        source = bind_line(source, name, value)
    source = replace_once(
        source,
        '--arg status "PASS_READY_FOR_SEPARATELY_AUTHORIZED_MUTATION"',
        '--arg status "PASS_TEST_ONLY_NOT_OPERATOR_RECEIPT"',
    )
    source = replace_once(
        source,
        "#!/usr/bin/env bash\n",
        "#!/usr/bin/env bash\n# GENERATED TEST-ONLY COPY; NOT AN OPERATOR RECEIPT\n",
    )
    args.output.write_text(source, encoding="utf-8")
    os.chmod(args.output, 0o755)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
