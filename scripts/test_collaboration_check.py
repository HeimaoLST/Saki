#!/usr/bin/env python3
"""collaboration_check.py 的确定性离线测试（stdlib unittest）。"""

import json
import os
import subprocess
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import collaboration_check as cc  # noqa: E402


def done(args, stdout="", rc=0, stderr=""):
    return subprocess.CompletedProcess(list(args), rc, stdout, stderr)


def prefix(*parts):
    parts = tuple(parts)
    return lambda args: tuple(args[: len(parts)]) == parts


class FakeRunner:
    """按匹配器返回预设结果，并记录实际发出的命令。"""

    def __init__(self, responses):
        self.responses = responses
        self.calls = []

    def __call__(self, args):
        args = tuple(args)
        self.calls.append(args)
        for matcher, canned in self.responses:
            if matcher(args):
                return canned
        return done(args, rc=1, stderr="no stub for " + " ".join(args))


class AnalyzeTest(unittest.TestCase):
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

    def test_triage_allows_missing_type_and_agent(self):
        issues = [
            {"number": 20, "title": "bare triage", "labels": [{"name": "status:triage"}]},
            {"number": 21, "title": "partial triage", "labels": [
                {"name": "status:triage"}, {"name": "type:bug"}]},
            {"number": 22, "title": "full triage", "labels": [
                {"name": "status:triage"}, {"name": "type:bug"}, {"name": "agent:pm"}]},
        ]
        self.assertEqual(cc.analyze_open_issues(issues), [])

    def test_triage_still_reports_duplicates(self):
        issues = [
            {"number": 30, "title": "dup type", "labels": [
                {"name": "status:triage"}, {"name": "type:bug"}, {"name": "type:docs"}]},
            {"number": 31, "title": "dup agent", "labels": [
                {"name": "status:triage"}, {"name": "agent:pm"}, {"name": "agent:tui"}]},
            {"number": 32, "title": "two status", "labels": [
                {"name": "status:triage"}, {"name": "status:ready"}]},
        ]
        findings = cc.analyze_open_issues(issues)
        by_number = {f["number"]: f["problems"] for f in findings}
        self.assertEqual(by_number[30], ["type=2"])
        self.assertEqual(by_number[31], ["agent=2"])
        # 多个 status 时不满足“唯一 triage”，type/agent 缺失同时上报
        self.assertEqual(by_number[32], ["status=2", "type=0", "agent=0"])

    def test_ready_missing_type_and_agent_reported(self):
        issues = [
            {"number": 40, "title": "ready no type/agent", "labels": [{"name": "status:ready"}]},
            {"number": 41, "title": "ready missing agent", "labels": [
                {"name": "status:ready"}, {"name": "type:bug"}]},
        ]
        findings = cc.analyze_open_issues(issues)
        self.assertEqual([f["number"] for f in findings], [40, 41])
        self.assertEqual(findings[0]["problems"], ["type=0", "agent=0"])
        self.assertEqual(findings[1]["problems"], ["agent=0"])

    def test_closed_leftover_normal_and_anomaly(self):
        issues = [
            {"number": 10, "title": "leftover", "stateReason": "COMPLETED",
             "labels": [{"name": "status:review"}, {"name": "type:bug"}]},
            {"number": 11, "title": "dup", "stateReason": "NOT_PLANNED",
             "labels": [{"name": "status:triage"}, {"name": "duplicate"}]},
            {"number": 12, "title": "clean done", "stateReason": "COMPLETED",
             "labels": [{"name": "status:done"}]},
            {"number": 13, "title": "no status", "stateReason": "COMPLETED",
             "labels": [{"name": "type:bug"}]},
            {"number": 14, "title": "two status", "stateReason": "COMPLETED",
             "labels": [{"name": "status:done"}, {"name": "status:review"}]},
        ]
        report = cc.analyze_closed_issues(issues)
        self.assertEqual([e["number"] for e in report["leftover"]], [10])
        self.assertEqual([e["number"] for e in report["normal"]], [11, 12])
        self.assertEqual([e["number"] for e in report["anomaly"]], [13, 14])
        self.assertEqual(report["anomaly"][0]["problem"], "status=0（应恰好 1 个）")

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
    def make_runner(self, clean=True, ancestor=True):
        status_line = "" if clean else " M src/x.rs\n"
        return FakeRunner([
            (prefix("git", "--no-optional-locks", "-C", "/wt", "status", "--porcelain"),
             done(["git"], stdout=status_line)),
            (prefix("git", "--no-optional-locks", "merge-base"), done(["git"], rc=0 if ancestor else 1)),
        ])

    def test_clean_ancestor_is_candidate(self):
        runner = self.make_runner(clean=True, ancestor=True)
        out = cc.analyze_worktrees(
            runner, [{"path": "/wt", "branch": "agent/pm/73-x"}], "pm", "dev"
        )
        self.assertEqual(len(out["candidates"]), 1)
        self.assertEqual(out["candidates"][0]["branch"], "agent/pm/73-x")

    def test_dirty_is_skipped(self):
        runner = self.make_runner(clean=False)
        out = cc.analyze_worktrees(
            runner, [{"path": "/wt", "branch": "agent/pm/73-x"}], "pm", "dev"
        )
        self.assertEqual(out["candidates"], [])
        self.assertEqual(out["skipped"][0]["reason"], "dirty worktree")

    def test_non_agent_branch_excluded_even_if_ancestor(self):
        runner = self.make_runner(clean=True, ancestor=True)
        out = cc.analyze_worktrees(
            runner, [{"path": "/wt", "branch": "fix/temp-thing"}], None, "dev"
        )
        self.assertEqual(out["candidates"], [])
        self.assertEqual(out["manual"], [])
        self.assertEqual(out["skipped"][0]["reason"], "非 agent 工作分支")

    def test_non_ancestor_goes_manual_not_candidate(self):
        # 历史 PR 曾 merged 不足以作为最终集成证明；这里不传任何 PR 信息。
        runner = self.make_runner(clean=True, ancestor=False)
        out = cc.analyze_worktrees(
            runner, [{"path": "/wt", "branch": "agent/pm/73-x"}], "pm", "dev"
        )
        self.assertEqual(out["candidates"], [])
        self.assertEqual(len(out["manual"]), 1)
        self.assertIn("人工核对", out["manual"][0]["reason"])

    def test_line_filter_only_narrows_worktrees(self):
        runner = self.make_runner(clean=True, ancestor=True)
        out = cc.analyze_worktrees(
            runner,
            [
                {"path": "/wt", "branch": "agent/pm/73-x"},
                {"path": "/wt2", "branch": "agent/tui/9-y"},
                {"path": "/wt3", "branch": "fix/temp-thing"},
            ],
            "pm",
            "dev",
        )
        self.assertEqual([c["branch"] for c in out["candidates"]], ["agent/pm/73-x"])
        reasons = {s["branch"]: s["reason"] for s in out["skipped"]}
        self.assertEqual(reasons["agent/tui/9-y"], "不在 --line pm 范围")
        self.assertEqual(reasons["fix/temp-thing"], "非 agent 工作分支")


class GitCommandTest(unittest.TestCase):
    def test_git_commands_disable_optional_locks(self):
        runner = FakeRunner([
            (prefix("git", "--no-optional-locks", "-C", "/wt", "status"),
             done(["git"], stdout="")),
            (prefix("git", "--no-optional-locks", "merge-base"), done(["git"], rc=0)),
            (prefix("git", "--no-optional-locks", "worktree", "list"), done(["git"], stdout="")),
        ])
        cc.is_clean(runner, "/wt")
        cc.is_ancestor(runner, "agent/pm/73-x", "dev")
        cc.parse_worktrees(cc.run_checked(runner, cc.git("worktree", "list", "--porcelain")))
        self.assertTrue(runner.calls)
        for call in runner.calls:
            self.assertEqual(call[:2], ("git", "--no-optional-locks"), call)


class FailurePropagationTest(unittest.TestCase):
    def test_api_failure_raises_not_empty(self):
        runner = FakeRunner([
            (prefix("gh", "issue", "list"), done(["gh"], rc=1, stderr="API 403")),
        ])
        with self.assertRaises(cc.CommandError):
            cc.gh_json(runner, ["gh", "issue", "list", "--state", "open", "--json", "number"])

    def test_empty_output_is_failure(self):
        for stdout in ("", "   \n"):
            runner = FakeRunner([
                (prefix("gh", "issue", "list"), done(["gh"], stdout=stdout)),
            ])
            with self.assertRaises(cc.CommandError) as ctx:
                cc.gh_json(runner, ["gh", "issue", "list", "--state", "open", "--json", "number"])
            self.assertIn("空输出", str(ctx.exception))

    def test_invalid_json_raises(self):
        runner = FakeRunner([
            (prefix("gh", "issue", "list"), done(["gh"], stdout="not json")),
        ])
        with self.assertRaises(cc.CommandError):
            cc.gh_json(runner, ["gh", "issue", "list", "--state", "open", "--json", "number"])

    def test_missing_binary_reports_error(self):
        def raising_runner(args):
            raise FileNotFoundError(2, "No such file or directory", args[0])

        with self.assertRaises(cc.CommandError) as ctx:
            cc.run_checked(raising_runner, ["gh", "issue", "list"])
        self.assertEqual(ctx.exception.returncode, 127)

    def test_build_report_propagates_failure(self):
        runner = FakeRunner([
            (prefix("gh", "issue", "list", "--state", "open"),
             done(["gh"], rc=1, stderr="boom")),
        ])
        with self.assertRaises(cc.CommandError):
            cc.build_report(runner, 10, None, "dev")

    def test_main_returns_2_on_failure(self):
        runner = FakeRunner([
            (prefix("gh", "issue", "list"), done(["gh"], rc=1, stderr="boom")),
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
            {"number": 4, "title": "no status", "url": "u4", "stateReason": "COMPLETED",
             "labels": [{"name": "type:bug"}]},
        ]
        merged_prs = [
            {"number": 7, "title": "p", "url": "u7", "headRefName": "agent/pm/73-x",
             "mergedAt": "t", "mergeCommit": {"oid": "aaa"},
             "closingIssuesReferences": [{"number": 2}]},
        ]
        runner = FakeRunner([
            (prefix("gh", "issue", "list", "--state", "open"),
             done(["gh"], stdout=json.dumps(open_issues))),
            (prefix("gh", "issue", "list", "--state", "closed"),
             done(["gh"], stdout=json.dumps(closed_issues))),
            (prefix("gh", "pr", "list"), done(["gh"], stdout=json.dumps(merged_prs))),
            (prefix("git", "--no-optional-locks", "worktree", "list", "--porcelain"),
             done(["git"], stdout=(
                 "worktree /repo\nHEAD abc\nbranch refs/heads/master\n\n"
                 "worktree /wt\nHEAD def\nbranch refs/heads/agent/pm/73-x\n"))),
            (prefix("git", "--no-optional-locks", "-C", "/wt", "status", "--porcelain"), done(["git"], stdout="")),
            (prefix("git", "--no-optional-locks", "merge-base"), done(["git"], rc=0)),
        ])
        report = cc.build_report(runner, 100, "pm", "dev")
        self.assertEqual(report["label_constraints"], [])
        self.assertEqual([e["number"] for e in report["handoff_pending"]], [2])
        self.assertEqual([e["number"] for e in report["closed_leftover"]], [3])
        self.assertEqual([e["number"] for e in report["closed_anomaly"]], [4])
        self.assertEqual([e["pr"] for e in report["merged_unclosed_candidates"]], [7])
        self.assertEqual(len(report["worktrees"]["candidates"]), 1)
        self.assertIn("不自动 fetch", report["window"]["refs_note"])


class RequiredCIFailureProbe(unittest.TestCase):
    def test_forced_failure_for_ci_propagation(self):
        self.fail("intentional temporary failure probe; reverted after CI propagation check")

if __name__ == "__main__":
    unittest.main()
