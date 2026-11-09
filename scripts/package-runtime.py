#!/usr/bin/env python3
"""Package an already built Saki binary with its exact CI source identity."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tomllib


def sha256(path: Path) -> str:
    with path.open("rb") as handle:
        return hashlib.file_digest(handle, "sha256").hexdigest()


def package(binary: Path, output: Path, provenance: dict) -> dict:
    output.mkdir(parents=True, exist_ok=False)
    staged = output / "Saki"
    shutil.copyfile(binary, staged)
    staged.chmod(0o755)
    manifest = {
        "schema": 1,
        "product": "Saki",
        **provenance,
        "protocol_version": 1,
        "binary": {"name": "Saki", "sha256": sha256(staged)},
    }
    manifest_path = output / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
    (output / "SHA256SUMS").write_text(
        "".join(f"{sha256(output / name)}  {name}\n" for name in ("Saki", "manifest.json"))
    )
    return manifest


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent

    def git(*argv: str) -> str:
        return subprocess.check_output(["git", *argv], cwd=root, text=True).strip()

    source_sha = git("rev-parse", "HEAD")
    if git("status", "--porcelain"):
        parser.error("runtime artifact requires a clean source checkout")
    if source_sha != os.environ.get("SAKI_SOURCE_SHA"):
        parser.error("SAKI_SOURCE_SHA must match the checked-out commit")
    target = subprocess.check_output(["rustc", "-vV"], text=True)
    host = next(line.removeprefix("host: ") for line in target.splitlines() if line.startswith("host: "))
    if host != "aarch64-apple-darwin":
        parser.error("this artifact contract currently supports aarch64-apple-darwin")
    repository = os.environ["GITHUB_REPOSITORY"]
    run_id = int(os.environ["GITHUB_RUN_ID"])
    attempt = int(os.environ["GITHUB_RUN_ATTEMPT"])
    if run_id <= 0 or attempt <= 0:
        parser.error("a real GitHub run and attempt are required")
    version = tomllib.loads((root / "Cargo.toml").read_text())["package"]["version"]
    binary = args.binary.resolve(strict=True)
    reported = subprocess.check_output([str(binary), "--version"], text=True).strip()
    if reported != f"Saki {version}":
        parser.error("runtime --version does not match Cargo.toml")
    output = args.output.resolve()
    if output.is_relative_to(root):
        parser.error("artifact output must be outside the source checkout")
    manifest = package(binary, output, {
        "version": version,
        "repository": repository,
        "source_sha": source_sha,
        "source_tree": git("rev-parse", "HEAD^{tree}"),
        "dirty": False,
        "run_id": run_id,
        "run_attempt": attempt,
        "target": host,
        "rustc": target,
    })
    print(json.dumps(manifest, sort_keys=True))


if __name__ == "__main__":
    main()
