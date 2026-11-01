#!/usr/bin/env python3
"""Focused regression tests for the macOS bundle minimum selection."""
from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import macos_minimum as mm


class MinimumTests(unittest.TestCase):
    def test_vtool_output_extracts_real_field(self) -> None:
        output = """
Saki:
Load command 6
      cmd LC_BUILD_VERSION
 platform MACOS
    minos 13.0
      sdk 26.2
"""
        self.assertEqual(mm.parse_vtool_output(output), "13.0")

    def test_selects_max_of_gui_and_helper(self) -> None:
        self.assertEqual(mm.select_minimum(["11.0", "13.0"]), "13.0")
        self.assertEqual(mm.version_tuple("13.0"), mm.version_tuple("13.0.0"))
        self.assertEqual(mm.select_minimum(["13.0", "13.0.0"]), "13.0")

    def test_validate_rejects_below_each_binary(self) -> None:
        with self.assertRaises(ValueError):
            mm.validate_minimum("11.0", ["13.0"])
        mm.validate_minimum("13.0", ["13.0", "11.0"])

    def test_missing_minos_is_error(self) -> None:
        with self.assertRaises(ValueError):
            mm.parse_vtool_output("Load command 1\n  cmd LC_BUILD_VERSION\n")

    def test_invalid_minos_is_error(self) -> None:
        with self.assertRaises(ValueError):
            mm.parse_vtool_output("cmd LC_BUILD_VERSION\nplatform MACOS\nminos not-a-version\n")

    def test_minos_outside_build_version_is_error(self) -> None:
        with self.assertRaises(ValueError):
            mm.parse_vtool_output("minos 13.0\n")

    def test_wrong_platform_is_error(self) -> None:
        with self.assertRaises(ValueError):
            mm.parse_vtool_output("cmd LC_BUILD_VERSION\nplatform IOS\nminos 13.0\n")

    def test_read_missing_binary_is_error(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            with self.assertRaises(ValueError):
                mm.read_minimum(str(Path(td) / "missing"))

    def test_manifest_fields_are_checked(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            path = Path(td) / "manifest.json"
            good = {
                "minimum_os": {"gui": "13.0", "helper": "11.0", "bundle": "13.0"},
                "bundle": {"minimum_os": "13.0"},
            }
            path.write_text(json.dumps(good))
            mm.validate_manifest(str(path), "13.0", "11.0", "13.0")
            good["bundle"]["minimum_os"] = "11.0"
            path.write_text(json.dumps(good))
            with self.assertRaises(ValueError):
                mm.validate_manifest(str(path), "13.0", "11.0", "13.0")



if __name__ == "__main__":
    unittest.main()
