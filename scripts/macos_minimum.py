#!/usr/bin/env python3
"""Read and validate the macOS deployment minimum from a Mach-O via vtool.

This is intentionally limited to the current arm64 LC_BUILD_VERSION form used
by the Saki bundle. It does not build a general Mach-O or dynamic-library tool.
"""
from __future__ import annotations

import json
import re
import subprocess
import sys

VERSION_RE = re.compile(r"^\d+(?:\.\d+){0,2}$")


def version_tuple(value: str) -> tuple[int, ...]:
    if not VERSION_RE.fullmatch(value):
        raise ValueError(f"invalid macOS version: {value!r}")
    parts = [int(part) for part in value.split(".")]
    while len(parts) < 3:
        parts.append(0)
    return tuple(parts)


def parse_vtool_output(output: str) -> str:
    lines = output.splitlines()
    for index, line in enumerate(lines):
        fields = line.split()
        if fields[:2] != ["cmd", "LC_BUILD_VERSION"]:
            continue
        platform = None
        minimum = None
        for block_line in lines[index + 1:index + 8]:
            block_fields = block_line.split()
            if block_fields[:1] == ["platform"] and len(block_fields) >= 2:
                platform = block_fields[1]
            elif block_fields[:1] == ["minos"]:
                if len(block_fields) < 2:
                    raise ValueError("missing value after LC_BUILD_VERSION minos")
                minimum = block_fields[1]
                break
        if platform is None:
            raise ValueError("LC_BUILD_VERSION block has no platform")
        if platform != "MACOS":
            raise ValueError(f"LC_BUILD_VERSION platform is {platform!r}, expected MACOS")
        if minimum is None:
            raise ValueError("missing LC_BUILD_VERSION minos from vtool")
        version_tuple(minimum)
        return minimum
    raise ValueError("missing LC_BUILD_VERSION block from vtool")


def read_minimum(path: str) -> str:
    try:
        result = subprocess.run(
            ["vtool", "-show-build", path],
            text=True,
            capture_output=True,
            check=True,
        )
    except FileNotFoundError as exc:
        raise ValueError("vtool is unavailable") from exc
    except subprocess.CalledProcessError as exc:
        detail = (exc.stderr or exc.stdout or "").strip()
        raise ValueError(f"vtool failed for {path}: {detail}") from exc
    return parse_vtool_output(result.stdout)


def select_minimum(values: list[str]) -> str:
    if not values:
        raise ValueError("no Mach-O macOS minima supplied")
    return max(values, key=version_tuple)


def validate_minimum(bundle: str, values: list[str]) -> None:
    bundle_version = version_tuple(bundle)
    for value in values:
        if bundle_version < version_tuple(value):
            raise ValueError(f"bundle minimum {bundle} is below binary minimum {value}")


def validate_manifest(path: str, gui: str, helper: str, bundle: str) -> None:
    try:
        with open(path, encoding="utf-8") as handle:
            manifest = json.load(handle)
    except OSError as exc:
        raise ValueError(f"cannot read manifest {path}: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise ValueError(f"manifest is not valid JSON: {exc}") from exc
    minimum = manifest.get("minimum_os")
    if not isinstance(minimum, dict):
        raise ValueError("manifest minimum_os object is missing")
    expected = {"gui": gui, "helper": helper, "bundle": bundle}
    for key, value in expected.items():
        if minimum.get(key) != value:
            raise ValueError(f"manifest minimum_os.{key}={minimum.get(key)!r}, expected {value!r}")
    if manifest.get("bundle", {}).get("minimum_os") != bundle:
        raise ValueError("manifest bundle.minimum_os does not match selected bundle minimum")
    validate_minimum(bundle, [gui, helper])


def main(argv: list[str]) -> int:
    try:
        if len(argv) == 2 and argv[0] == "read":
            print(read_minimum(argv[1]))
            return 0
        if len(argv) >= 3 and argv[0] == "select":
            print(select_minimum(argv[1:]))
            return 0
        if len(argv) >= 3 and argv[0] == "validate":
            validate_minimum(argv[1], argv[2:])
            return 0
        if len(argv) == 5 and argv[0] == "validate-manifest":
            validate_manifest(argv[1], argv[2], argv[3], argv[4])
            return 0
    except ValueError as exc:
        print(f"macos-minimum: {exc}", file=sys.stderr)
        return 1
    print("usage: macos_minimum.py read PATH | select VERSION... | validate BUNDLE VERSION... | validate-manifest PATH GUI HELPER BUNDLE", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
