#!/usr/bin/env python3
"""Export a product from a fixed Saki commit without rewriting source history."""

import argparse
import json
import os
from pathlib import Path
import subprocess


NAGI_SUPPORT = {
    "scripts/gui-go.py",
    "scripts/build-macos-app.sh",
    "scripts/check-gui-runtime-results.py",
    "scripts/macos_minimum.py",
    "scripts/test_macos_minimum.py",
    "docs/requirements/desktop.md",
    "docs/design/desktop.md",
    "docs/design/desktop-workbench.md",
    "docs/design/experience-to-behavior.md",
    "docs/design/previews/desktop-workbench.html",
    "docs/design/previews/assets/saki-avatar-default.png",
}
ROUS_SUPPORT = {
    "Cargo.toml",
    "Cargo.lock",
    "src/session/channel.rs",
    "src/session/terminal.rs",
    "docs/design/tui.md",
    "docs/design/tui-style.md",
    "docs/design/command-system.md",
    "docs/design/previews/tui-layouts.html",
    "docs/design/previews/tui-style.html",
}


def run(*args, cwd=None):
    return subprocess.check_output(args, cwd=cwd)


def target_path(product, path):
    if product == "nagi":
        if path.startswith("desktop/"):
            return path.removeprefix("desktop/")
        if path in NAGI_SUPPORT:
            return path
    else:
        if path.startswith(("src/frontend/", "src/command/", "tests/tui/", "tests/console/", "tests/command/")):
            return path
        if path in ROUS_SUPPORT:
            return path
    if path in {"LICENSE", "LICENSE.md", "LICENSE-MIT", "LICENSE-APACHE"}:
        return path
    return None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("product", choices=("nagi", "rous"))
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--ref", required=True)
    parser.add_argument("--destination", type=Path, required=True)
    args = parser.parse_args()
    source = args.source.resolve(strict=True)
    commit = run("git", "rev-parse", args.ref + "^{commit}", cwd=source).decode().strip()
    destination = args.destination.expanduser().absolute()
    manifest_path = destination / "docs/design/source-import.json"
    if destination.exists():
        if manifest_path.exists():
            manifest = json.loads(manifest_path.read_text())
            if manifest["source_commit"] == commit and manifest["product"] == args.product:
                print(json.dumps({"destination": str(destination), "result": "already exported"}))
                return
        parser.error(f"destination already exists: {destination}")
    destination.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(
        ["git", "clone", "--no-hardlinks", "--no-checkout", str(source), str(destination)],
        check=True,
    )
    subprocess.run(["git", "remote", "remove", "origin"], cwd=destination, check=True)
    branch = f"agent/{'gui' if args.product == 'nagi' else 'tui'}/333-repository-split"
    subprocess.run(["git", "checkout", "-b", branch, commit], cwd=destination, check=True)
    entries = run("git", "ls-tree", "-rz", commit, cwd=source).split(b"\0")
    selected = []
    for entry in entries:
        if not entry:
            continue
        metadata, raw_path = entry.split(b"\t", 1)
        mode, kind, oid = metadata.decode().split()
        path = raw_path.decode()
        target = target_path(args.product, path)
        if target is not None:
            if kind != "blob" or mode not in {"100644", "100755"}:
                parser.error(f"unsupported product entry: {path}")
            selected.append((path, target, mode, oid))
    subprocess.run(["git", "rm", "-rq", "--", "."], cwd=destination, check=True)
    mapping = []
    for original, target, mode, oid in selected:
        output = destination / target
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_bytes(run("git", "cat-file", "blob", oid, cwd=source))
        os.chmod(output, 0o755 if mode == "100755" else 0o644)
        mapping.append({"source": original, "target": target, "blob": oid})
    manifest_path.parent.mkdir(parents=True, exist_ok=True)
    manifest_path.write_text(json.dumps({
        "product": args.product,
        "source_repository": "https://github.com/HeimaoLST/Saki",
        "source_commit": commit,
        "source_tree": run("git", "rev-parse", commit + "^{tree}", cwd=source).decode().strip(),
        "history": "original commits retained; product adaptation uses ordinary new commits",
        "files": mapping,
    }, indent=2) + "\n")
    subprocess.run(["git", "add", "--all"], cwd=destination, check=True)
    print(json.dumps({"destination": str(destination), "source_commit": commit,
                      "files": len(mapping), "branch": branch}))


if __name__ == "__main__":
    main()
