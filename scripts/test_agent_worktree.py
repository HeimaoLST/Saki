#!/usr/bin/env python3
"""Isolated git tests for the fail-closed worktree helper."""

from __future__ import annotations

import os
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "scripts" / "agent-worktree.sh"


def run(argv: list[str], cwd: Path, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        argv,
        cwd=cwd,
        env={**os.environ, **(env or {})},
        text=True,
        encoding="utf-8",
        errors="replace",
        capture_output=True,
        check=False,
    )


class WorktreeSafetyTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.repo = Path(self.temp.name) / "repo"
        self.repo.mkdir()
        self.assertEqual(run(["git", "init", "-q"], self.repo).returncode, 0)
        self.assertEqual(run(["git", "config", "user.email", "test@example.invalid"], self.repo).returncode, 0)
        self.assertEqual(run(["git", "config", "user.name", "test"], self.repo).returncode, 0)
        (self.repo / "README").write_text("test\n")
        self.assertEqual(run(["git", "add", "README"], self.repo).returncode, 0)
        self.assertEqual(run(["git", "commit", "-q", "-m", "base"], self.repo).returncode, 0)
        self.worktrees = Path(self.temp.name) / "worktrees"
        self.env = {"SAKI_WORKTREE_ROOT": str(self.worktrees)}

    def tearDown(self) -> None:
        self.temp.cleanup()

    def add(self, name: str = "agent/test/236-safety") -> Path:
        result = run([str(SCRIPT), "add", name, "HEAD"], self.repo, self.env)
        self.assertEqual(result.returncode, 0, result.stderr)
        return self.worktrees / name.replace("/", "-")

    def test_add_list_path_and_remove_are_available(self) -> None:
        path = self.add()
        self.assertTrue((path / "README").exists())
        listed = run([str(SCRIPT), "list"], self.repo, self.env)
        self.assertEqual(listed.returncode, 0)
        self.assertIn(str(path), listed.stdout)
        found = run([str(SCRIPT), "path", "agent/test/236-safety"], self.repo, self.env)
        self.assertEqual(found.stdout.strip(), str(path))
        removed = run([str(SCRIPT), "remove", "agent/test/236-safety"], self.repo, self.env)
        self.assertEqual(removed.returncode, 0, removed.stderr)
        self.assertFalse(path.exists())
        self.assertEqual(run(["git", "rev-parse", "--verify", "refs/heads/agent/test/236-safety"], self.repo).returncode, 0)

    def test_slug_collision_remove_is_rejected(self) -> None:
        path = self.add("agent/test/236-a/b")
        result = run([str(SCRIPT), "remove", "agent/test/236-a-b"], self.repo, self.env)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("分支身份不符", result.stderr)
        self.assertTrue(path.exists())
        self.assertEqual(
            run(["git", "rev-parse", "--verify", "refs/heads/agent/test/236-a/b"], self.repo).returncode,
            0,
        )

    def test_dirty_worktree_remove_fails_and_preserves_tree(self) -> None:
        path = self.add()
        (path / "dirty.txt").write_text("do not remove\n")
        result = run([str(SCRIPT), "remove", "agent/test/236-safety"], self.repo, self.env)
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(path.exists())
        self.assertTrue((path / "dirty.txt").exists())

    def test_detached_worktree_remove_is_rejected(self) -> None:
        path = self.add()
        detached = run(["git", "-C", str(path), "checkout", "--detach", "-q"], self.repo)
        self.assertEqual(detached.returncode, 0, detached.stderr)
        result = run([str(SCRIPT), "remove", "agent/test/236-safety"], self.repo, self.env)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("detached", result.stderr)
        self.assertTrue(path.exists())

    def test_finish_and_prune_fail_without_touching_worktree(self) -> None:
        path = self.add()
        for command in ("finish", "prune"):
            result = run([str(SCRIPT), command, "agent/test/236-safety"], self.repo, self.env)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("自动清理入口", result.stderr)
            self.assertTrue(path.exists())
            self.assertEqual(run(["git", "rev-parse", "--verify", "refs/heads/agent/test/236-safety"], self.repo).returncode, 0)

    def test_force_remove_is_rejected_without_touching_worktree(self) -> None:
        path = self.add()
        result = run([str(SCRIPT), "remove", "agent/test/236-safety", "--force"], self.repo, self.env)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("不接受额外参数", result.stderr)
        self.assertTrue(path.exists())
        self.assertEqual(run(["git", "rev-parse", "--verify", "refs/heads/agent/test/236-safety"], self.repo).returncode, 0)


if __name__ == "__main__":
    unittest.main()
