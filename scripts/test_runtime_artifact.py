"""Exercise the published runtime artifact layout and content checksums."""
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("package_runtime", Path(__file__).with_name("package-runtime.py"))
module = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(module)


class RuntimeArtifactTests(unittest.TestCase):
    def test_package_preserves_binary_and_records_source_and_content(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "input"
            binary.write_bytes(b"runtime fixture\0")
            output = root / "artifact"
            provenance = {"source_sha": "a" * 40, "run_id": 12, "run_attempt": 2}
            manifest = module.package(binary, output, provenance)
            self.assertEqual((output / "Saki").read_bytes(), binary.read_bytes())
            self.assertTrue((output / "Saki").stat().st_mode & 0o111)
            self.assertEqual(json.loads((output / "manifest.json").read_text()), manifest)
            self.assertEqual(manifest["source_sha"], provenance["source_sha"])
            for line in (output / "SHA256SUMS").read_text().splitlines():
                digest, filename = line.split("  ")
                self.assertEqual(digest, hashlib.sha256((output / filename).read_bytes()).hexdigest())
            binary.write_bytes(b"changed after staging")
            self.assertNotEqual(module.sha256(binary), manifest["binary"]["sha256"])

    def test_existing_output_is_not_reused_as_a_new_artifact(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaises(FileExistsError):
                module.package(root / "missing", root, {})


if __name__ == "__main__":
    unittest.main()
