#!/usr/bin/env python3
"""Run Go through the temporary pinned Split release-before-frame patch.

Usage: python3 scripts/gui-go.py test|vet|build|run [Go arguments].
The real desktop/go.mod, module cache, and source tree are not rewritten.
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DESKTOP = ROOT / "desktop"
PATCH = DESKTOP / "patches" / "split-release.patch"
MYGO = "github.com/egoist/mygo"
VERSION = "v0.4.9-0.20261014025635-fea932a7a2a9"
SPLIT_BEFORE = "7703d313943d0e9a46c405daa2bdbe13b50c7c835d3e4d31c86f826b926ed149"
SPLIT_AFTER = "f8d10a9f46c2dd10cdfdabc230f960c625b18c45df560c49357de8ff3c4c0de3"


def fail(message: str, code: int) -> int:
    print("gui-go: " + message, file=sys.stderr)
    return code


def sha256(path: Path) -> str:
    import hashlib

    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def selected_module() -> dict:
    """Return Go's selected module metadata, including any Replace."""
    raw = subprocess.check_output(["go", "list", "-m", "-json", MYGO], cwd=DESKTOP, text=True)
    return json.loads(raw)


def module_dir() -> Path:
    raw = subprocess.check_output(
        ["go", "mod", "download", "-json", MYGO + "@" + VERSION],
        cwd=DESKTOP,
        text=True,
    )
    return Path(json.loads(raw)["Dir"])


def make_writable(path: Path) -> None:
    for root, dirs, files in os.walk(path):
        os.chmod(root, 0o755)
        for name in dirs:
            os.chmod(Path(root) / name, 0o755)
        for name in files:
            os.chmod(Path(root) / name, 0o644)


def main(argv: list[str]) -> int:
    if not argv or argv[0] in ("-h", "--help"):
        print(__doc__, file=sys.stderr)
        return 0 if argv else 2
    if argv[0] not in ("test", "vet", "build", "run"):
        return fail("this launcher only forwards test, vet, build and run", 2)
    if any(arg == "-modfile" or arg.startswith("-modfile=") for arg in argv[1:]):
        return fail("caller may not override the prepared -modfile", 2)
    if not DESKTOP.is_dir() or not PATCH.is_file():
        return fail("desktop or split patch is missing", 2)
    selected = selected_module()
    if selected.get("Version") != VERSION or selected.get("Replace") is not None:
        return fail(
            "selected MyGo is %s with replace=%r; update or remove the Split patch"
            % (selected.get("Version"), selected.get("Replace")),
            2,
        )

    src = module_dir()
    with tempfile.TemporaryDirectory(prefix="saki-gui-go-") as td:
        tmp = Path(td)
        module = tmp / "mygo"
        shutil.copytree(src, module)
        make_writable(module)

        before = sha256(module / "ui" / "split.go")
        if before != SPLIT_BEFORE:
            return fail(
                "unexpected MyGo Split source sha256=%s, expected %s; refusing to patch"
                % (before, SPLIT_BEFORE),
                2,
            )

        patch_result = subprocess.run(
            ["patch", "-p1", "--fuzz=0", "--batch"],
            cwd=module,
            stdin=PATCH.open("rb"),
            stdout=sys.stderr,
            stderr=sys.stderr,
        )
        if patch_result.returncode != 0:
            return fail("Split patch failed; refusing to run unpatched source", 3)
        after = sha256(module / "ui" / "split.go")
        if after != SPLIT_AFTER:
            return fail(
                "unexpected patched Split source sha256=%s, expected %s"
                % (after, SPLIT_AFTER),
                4,
            )

        # A module file distinct from the real desktop/go.mod; Go uses the
        # matching app.sum next to it. The source checkout is never edited.
        modfile = tmp / "app.mod"
        sumfile = tmp / "app.sum"
        shutil.copyfile(DESKTOP / "go.mod", modfile)
        shutil.copyfile(DESKTOP / "go.sum", sumfile)
        edit = subprocess.run(
            ["go", "mod", "edit", "-modfile=" + str(modfile), "-replace=" + MYGO + "=" + str(module)],
            cwd=DESKTOP,
            stdout=sys.stderr,
            stderr=sys.stderr,
        )
        if edit.returncode != 0:
            return fail("could not add the prepared module replace", 5)

        cmd = ["go", argv[0], "-modfile=" + str(modfile), *argv[1:]]
        rc = subprocess.run(cmd, cwd=DESKTOP).returncode
        return 128 + (-rc) if rc < 0 else rc


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    except subprocess.CalledProcessError as exc:
        print("gui-go: preparation command failed: %s" % exc, file=sys.stderr)
        sys.exit(exc.returncode or 1)
