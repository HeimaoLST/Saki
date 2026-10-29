#!/usr/bin/env python3
"""Run Go through the temporary pinned editor, Split and Markdown link patches.

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
SPLIT_PATCH = DESKTOP / "patches" / "split-release.patch"
EDITOR_PATCH = DESKTOP / "patches" / "editor-release.patch"
MARKDOWN_LINK_PATCH = DESKTOP / "patches" / "markdown-link-focus.patch"
MYGO = "github.com/egoist/mygo"
VERSION = "v0.4.9-0.20261014025635-fea932a7a2a9"
SPLIT_BEFORE = "7703d313943d0e9a46c405daa2bdbe13b50c7c835d3e4d31c86f826b926ed149"
SPLIT_AFTER = "f8d10a9f46c2dd10cdfdabc230f960c625b18c45df560c49357de8ff3c4c0de3"
EDITOR_INPUT_BEFORE = "e13ab0701b013ed68a3374ceedd195bc42f1751621f2a1797f12372a0598ca79"
EDITOR_INPUT_AFTER = "161403ab07ad7d429041f60a6ca33fab513bcbc56f54a6026c970f2d17a32a36"
MARKDOWN_VIEW_BEFORE = "876ab9f9697ac87205db096d5906464a5e252bb382a03b1ed1c0cc2bad36a335"
MARKDOWN_VIEW_AFTER = "454512895c5eb1e55afa6432d3f688451131d0aa8cf742599b15ea990a7e416c"


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
    if not DESKTOP.is_dir() or not SPLIT_PATCH.is_file() or not EDITOR_PATCH.is_file() or not MARKDOWN_LINK_PATCH.is_file():
        return fail("desktop or a pinned patch is missing", 2)
    selected = selected_module()
    if selected.get("Version") != VERSION or selected.get("Replace") is not None:
        return fail(
            "selected MyGo is %s with replace=%r; update or remove the pinned patches"
            % (selected.get("Version"), selected.get("Replace")),
            2,
        )

    src = module_dir()
    with tempfile.TemporaryDirectory(prefix="saki-gui-go-") as td:
        tmp = Path(td)
        module = tmp / "mygo"
        shutil.copytree(src, module)
        make_writable(module)

        split_before = sha256(module / "ui" / "split.go")
        if split_before != SPLIT_BEFORE:
            return fail(
                "unexpected MyGo Split source sha256=%s, expected %s; refusing to patch"
                % (split_before, SPLIT_BEFORE),
                2,
            )
        editor_before = sha256(module / "ui" / "input.go")
        if editor_before != EDITOR_INPUT_BEFORE:
            return fail(
                "unexpected MyGo editor source sha256=%s, expected %s; refusing to patch"
                % (editor_before, EDITOR_INPUT_BEFORE),
                2,
            )
        markdown_before = sha256(module / "ui" / "markdown" / "view.go")
        if markdown_before != MARKDOWN_VIEW_BEFORE:
            return fail(
                "unexpected MyGo Markdown source sha256=%s, expected %s; refusing to patch"
                % (markdown_before, MARKDOWN_VIEW_BEFORE),
                2,
            )
        for label, patch in (("Split", SPLIT_PATCH), ("Editor", EDITOR_PATCH), ("Markdown link", MARKDOWN_LINK_PATCH)):
            patch_result = subprocess.run(
                ["patch", "-p1", "--fuzz=0", "--batch"],
                cwd=module,
                stdin=patch.open("rb"),
                stdout=sys.stderr,
                stderr=sys.stderr,
            )
            if patch_result.returncode != 0:
                return fail("%s patch failed; refusing to run partial source" % label, 3)
        split_after = sha256(module / "ui" / "split.go")
        if split_after != SPLIT_AFTER:
            return fail(
                "unexpected patched Split source sha256=%s, expected %s"
                % (split_after, SPLIT_AFTER),
                4,
            )
        editor_after = sha256(module / "ui" / "input.go")
        if editor_after != EDITOR_INPUT_AFTER:
            return fail(
                "unexpected patched editor source sha256=%s, expected %s"
                % (editor_after, EDITOR_INPUT_AFTER),
                4,
            )
        markdown_after = sha256(module / "ui" / "markdown" / "view.go")
        if markdown_after != MARKDOWN_VIEW_AFTER:
            return fail(
                "unexpected patched Markdown source sha256=%s, expected %s"
                % (markdown_after, MARKDOWN_VIEW_AFTER),
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
