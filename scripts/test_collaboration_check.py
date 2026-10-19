#!/usr/bin/env python3
"""collaboration_check.py 的确定性离线测试（stdlib unittest）。"""

import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import collaboration_check as cc  # noqa: E402


def result(args, stdout="", rc=0, stderr=""):
    return cc.CommandResult(tuple(args), rc, stdout, stderr)


def prefix(*parts):
    parts = tuple(parts)
    return lambda args: tuple(args[: len(parts)]) == parts


class FakeRunner:
    """记录调用并按匹配器返回预设结果；强制只读命令边界。"""

    def __init__(self, responses):
        self.responses = responses
        self.calls = []

    def __call__(self, args):
        args = tuple(args)
        self.calls.append(args)
        cc.assert_read_only(args)  # 任何写操作都会在这里失败
        for matcher, canned in self.responses:
            if matcher(args):
                return canned
        return result(args, rc=1, stderr="no stub for " + " ".join(args))


class ReadOnlyGuardTest(unittest.TestCase):
    def test_accepts_read_only_commands(self):
        cc.assert_read_only(["git", "worktree", "list", "--porcelain"])
        cc.assert_read_only(["git", "-C", "/tmp/x", "status", "--porcelain"])
        cc.assert_read_only(["git", "merge-base", "--is-ancestor", "a", "origin/dev"])
        cc.assert_read_only(["gh", "issue", "list", "--state", "open"])
        cc.assert_read_only(["gh", "pr", "view", "1"])

    def test_rejects_mutating_commands(self):
        for command in (
            ["git", "push", "origin", "dev"],
            ["git", "commit", "-m", "x"],
            ["git", "branch", "-D", "x"],
            ["git", "worktree", "remove", "/tmp/x"],
            ["git", "worktree", "add", "/tmp/x"],
            ["gh", "issue", "edit", "1", "--add-label", "status:done"],
            ["gh", "issue", "close", "1"],
            ["gh", "pr", "merge", "1"],
        ):
            with self.assertRaises(ValueError):
                cc.assert_read_only(command)


class AnalyzeTest(unittest.TestCase):
    def setUp(self):
        self.runner = FakeRunner([])

    def test_open_issue_label_constraints(self):
        issues = [
            {"number": 1, "title": "ok", "labels": [
                {"name": "type:bug"}, {"name": "status:ready"}, {"name": "agent:pm"}]},
            {"number": 2, "title": "missing", "labels": [{"name": "status:ready"}]},
            {"number": 3, "title": "dup", "labels": [
                {"name": "type:bug"}, {"name": "type:docs"},
                {"name": "status:ready"}, {"name": "agent:pm"}]},
        ]
        findings = cc.analyze_open_issues(issues)
        self.assertEqual([f["number"] for f in findings], [2, 3])
        self.assertEqual(findings[0]["problems"], ["type=0", "agent=0"])
        self.assertEqual(findings[1]["problems"], ["type=2"])

    def test_closed_leftover_vs_normal(self):
        issues = [
            {"number": 10, "title": "leftover", "stateReason": "COMPLETED",
             "labels": [{"name": "status:review"}, {"name": "type:bug"}]},
            {"number": 11, "title": "dup", "stateReason": "NOT_PLANNED",
             "labels": [{"name": "status:triage"}, {"name": "duplicate"}]},
            {"number": 12, "title": "clean done", "stateReason": "COMPLETED",
             "labels": [{"name": "status:done"}]},
        ]
        report = cc.analyze_closed_issues(issues)
        self.assertEqual([e["number"] for e in report["leftover"]], [10])
        self.assertEqual([e["number"] for e in report["normal"]], [11])

    def test_handoff_pending(self):
        issues = [
            {"number": 1, "title": "a", "labels": [{"name": "handoff"}]},
            {"number": 2, "title": "b", "labels": [{"name": "type:bug"}]},
        ]
        self.assertEqual([e["number"] for e in cc.analyze_handoff_pending(issues)], [1])

    def test_merged_unclosed_candidate(self):
        prs = [
            {"number": 5, "title": "p", "closingIssuesReferences": [
                {"number": 1}, {"number": 2}], "url": "u", "mergedAt": "t"},
        ]
        out = cc.analyze_merged_unclosed(prs, {2})
        self.assertEqual(out[0]["pr"], 5)
        self.assertEqual(out[0]["issues"], [2])

    def test_parse_worktrees(self):
        porcelain = (
            "worktree /repo\nHEAD abc\nbranch refs/heads/master\n\n"
            "worktree /repo/wt\nHEAD def\nbranch refs/heads/agent/pm/73-x\n\n"
            "worktree /repo/det\nHEAD 123\ndetached\n"
        )
        trees = cc.parse_worktrees(porcelain)
        self.assertEqual(trees[0]["branch"], "master")
        self.assertEqual(trees[1]["branch"], "agent/pm/73-x")
        self.assertTrue(trees[2].get("detached"))


class WorktreeAnalyzeTest(unittest.TestCase):
    def make_runner(self, clean=True, ancestor=True, tip="aaa"):
        status_line = "" if clean else " M src/x.rs\n"
        return FakeRunner([
            (prefix("git", "-C", "/wt", "status", "--porcelain"),
             result(["git"], stdout=status_line)),
            (prefix("git", "merge-base"), result(["git"], rc=0 if ancestor else 1)),
            (prefix("git", "rev-parse"), result(["git"], stdout=tip + "\n")),
        ])

    def test_clean_ancestor_is_candidate(self):
        runner = self.make_runner(clean=True, ancestor=True)
        out = cc.analyze_worktrees(
            runner, [{"path": "/wt", "branch": "agent/pm/73-x"}], "pm", "dev", {}
        )
        self.assertEqual(len(out["candidates"]), 1)
        self.assertEqual(out["candidates"][0]["branch"], "agent/pm/73-x")

    def test_dirty_is_skipped(self):
        runner = self.make_runner(clean=False)
        out = cc.analyze_worktrees(
            runner, [{"path": "/wt", "branch": "agent/pm/73-x"}], "pm", "dev", {}
        )
        self.assertEqual(out["candidates"], [])
        self.assertEqual(out["skipped"][0]["reason"], "dirty worktree")

    def test_commit_after_merge_is_manual(self):
        runner = self.make_runner(clean=True, ancestor=False, tip="bbb")
        merged = {"agent/pm/73-x": {"number": 9, "mergeCommit": {"oid": "aaa"}}}
        out = cc.analyze_worktrees(
            runner, [{"path": "/wt", "branch": "agent/pm/73-x"}], "pm", "dev", merged
        )
        self.assertEqual(out["candidates"], [])
        self.assertEqual(len(out["manual"]), 1)
        self.assertIn("人工核对", out["manual"][0]["reason"])

    def test_unmerged_is_skipped(self):
        runner = self.make_runner(clean=True, ancestor=False)
        out = cc.analyze_worktrees(
            runner, [{"path": "/wt", "branch": "agent/pm/73-x"}], "pm", "dev", {}
        )
        self.assertEqual(out["candidates"], [])
        self.assertEqual(out["skipped"][0]["reason"], "未合入 origin/dev")


class FailurePropagationTest(unittest.TestCase):
    def test_api_failure_raises_not_empty(self):
        runner = FakeRunner([
            (prefix("gh", "issue", "list"),
             result(["gh"], rc=1, stderr="API 403")),
        ])
        with self.assertRaises(cc.CommandError):
            cc.gh_json(runner, ["gh", "issue", "list", "--state", "open", "--json", "number"])

    def test_invalid_json_raises(self):
        runner = FakeRunner([
            (prefix("gh", "issue", "list"), result(["gh"], stdout="not json")),
        ])
        with self.assertRaises(cc.CommandError):
            cc.gh_json(runner, ["gh", "issue", "list", "--state", "open", "--json", "number"])

    def test_build_report_propagates_failure(self):
        runner = FakeRunner([
            (prefix("gh", "issue", "list", "--state", "open"),
             result(["gh"], rc=1, stderr="boom")),
        ])
        with self.assertRaises(cc.CommandError):
            cc.build_report(runner, 10, None, "dev")

    def test_main_returns_2_on_failure(self):
        runner = FakeRunner([
            (prefix("gh", "issue", "list"), result(["gh"], rc=1, stderr="boom")),
        ])
        self.assertEqual(cc.main([], runner=runner), 2)


class BuildReportTest(unittest.TestCase):
    def test_full_report_shapes(self):
        open_issues = [
            {"number": 1, "title": "a", "url": "u1",
             "labels": [{"name": "type:bug"}, {"name": "status:ready"}, {"name": "agent:pm"}]},
            {"number": 2, "title": "handoff", "url": "u2",
             "labels": [{"name": "handoff"}, {"name": "type:test"},
                        {"name": "status:triage"}, {"name": "agent:pm"}]},
        ]
        closed_issues = [
            {"number": 3, "title": "left", "url": "u3", "stateReason": "COMPLETED",
             "labels": [{"name": "status:review"}]},
        ]
        merged_prs = [
            {"number": 7, "title": "p", "url": "u7", "headRefName": "agent/pm/73-x",
             "mergedAt": "t", "mergeCommit": {"oid": "aaa"},
             "closingIssuesReferences": [{"number": 2}]},
        ]
        runner = FakeRunner([
            (prefix("gh", "issue", "list", "--state", "open"),
             result(["gh"], stdout=json.dumps(open_issues))),
            (prefix("gh", "issue", "list", "--state", "closed"),
             result(["gh"], stdout=json.dumps(closed_issues))),
            (prefix("gh", "pr", "list"),
             result(["gh"], stdout=json.dumps(merged_prs))),
            (prefix("git", "worktree", "list", "--porcelain"),
             result(["git"], stdout=(
                 "worktree /repo\nHEAD abc\nbranch refs/heads/master\n\n"
                 "worktree /wt\nHEAD def\nbranch refs/heads/agent/pm/73-x\n"))),
            (prefix("git", "-C", "/wt", "status", "--porcelain"),
             result(["git"], stdout="")),
            (prefix("git", "merge-base"), result(["git"], rc=0)),
        ])
        report = cc.build_report(runner, 100, "pm", "dev")
        self.assertEqual(report["label_constraints"], [])
        self.assertEqual([e["number"] for e in report["handoff_pending"]], [2])
        self.assertEqual([e["number"] for e in report["closed_leftover"]], [3])
        self.assertEqual([e["pr"] for e in report["merged_unclosed_candidates"]], [7])
        self.assertEqual(len(report["worktrees"]["candidates"]), 1)
        self.assertEqual(report["window"]["line_filter"], "pm")

    def test_all_issued_commands_are_read_only(self):
        runner = FakeRunner([
            (prefix("gh", "issue", "list"), result(["gh"], stdout="[]")),
            (prefix("gh", "pr", "list"), result(["gh"], stdout="[]")),
            (prefix("git", "worktree", "list", "--porcelain"), result(["git"], stdout="")),
        ])
        cc.build_report(runner, 5, None, "dev")
        for call in runner.calls:
            cc.assert_read_only(call)  # 不应抛出


if __name__ == "__main__":
    unittest.main()
