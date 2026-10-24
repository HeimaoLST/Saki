#!/usr/bin/env python3
"""Saki 多 agent 协作只读检查。

复用 `gh` CLI 与 `git` 做只读查询：不修改 GitHub、不改标签、不关闭 issue、
不删除分支或 worktree、不提交、不读取环境里的密钥。

报告只是候选，不是验收结论，也不是删除／关闭授权。

worktree 判定只依据**本地已有的** `origin/<integration>` 引用：脚本不自动 fetch，
执行回收前由主责先 `git fetch origin` 再核对。

退出码：
  0  已生成报告
  2  只读命令失败、输出为空／空白／无效 JSON，或找不到 gh／git；
     不会把失败当成空队列成功

用法：
  python3 scripts/collaboration_check.py [--limit N] [--line <line>] [--integration dev] [--json]

`--line` 只过滤 worktree 分支（`agent/<line>/...`）；issue 与 PR 分类始终覆盖整个仓库。
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from typing import Callable, Sequence

TYPE_PREFIX = "type:"
STATUS_PREFIX = "status:"
AGENT_PREFIX = "agent:"
NORMAL_CLOSE_LABELS = {"duplicate", "invalid", "wontfix"}
LONG_LIVED_BRANCHES = {"master", "dev"}


class CommandError(RuntimeError):
    """A read command failed; callers must not treat this as empty output."""

    def __init__(self, args: Sequence[str], returncode: int, stderr: str):
        self.args_used = tuple(args)
        self.returncode = returncode
        self.stderr = stderr
        super().__init__(
            "command failed (rc={rc}): {cmd}\n{err}".format(
                rc=returncode,
                cmd=" ".join(self.args_used),
                err=(stderr or "").strip(),
            )
        )


def subprocess_runner(args: Sequence[str]) -> subprocess.CompletedProcess:
    return subprocess.run(list(args), capture_output=True, text=True, check=False)


Runner = Callable[[Sequence[str]], subprocess.CompletedProcess]


def run_checked(runner: Runner, args: Sequence[str]) -> str:
    try:
        result = runner(args)
    except OSError as exc:
        raise CommandError(args, 127, "无法执行 %s：%s" % (args[0], exc))
    if result.returncode != 0:
        raise CommandError(args, result.returncode, result.stderr)
    return result.stdout


def gh_json(runner: Runner, args: Sequence[str]):
    """Parse gh JSON; empty/whitespace output is a failure, not an empty list."""
    raw = run_checked(runner, args)
    if not raw.strip():
        raise CommandError(args, 1, "gh 返回空输出（不是空队列）")
    try:
        return json.loads(raw)
    except json.JSONDecodeError as exc:
        raise CommandError(args, 1, "无效 JSON：%s" % exc)


def label_names(issue) -> list:
    return [label["name"] for label in issue.get("labels", [])]


def prefixed(names, prefix) -> list:
    return [name for name in names if name.startswith(prefix)]


def analyze_open_issues(issues) -> list:
    """status 必须恰好 1 个；仅唯一 status:triage 允许 type／agent 为 0 或 1。"""
    findings = []
    for issue in issues:
        names = label_names(issue)
        statuses = prefixed(names, STATUS_PREFIX)
        types = prefixed(names, TYPE_PREFIX)
        agents = prefixed(names, AGENT_PREFIX)
        problems = []
        if len(statuses) != 1:
            problems.append("status=%d" % len(statuses))
        triage = len(statuses) == 1 and statuses[0] == "status:triage"
        for key, values in (("type", types), ("agent", agents)):
            if len(values) != 1 and not (triage and not values):
                problems.append("%s=%d" % (key, len(values)))
        if problems:
            findings.append(
                {
                    "number": issue["number"],
                    "title": issue.get("title", ""),
                    "problems": problems,
                    "labels": names,
                    "url": issue.get("url", ""),
                }
            )
    return findings


def analyze_closed_issues(issues) -> dict:
    """Split closed issues into leftover status, missing/duplicate status, and normal closes."""
    leftover, anomaly, normal = [], [], []
    for issue in issues:
        names = label_names(issue)
        status_labels = prefixed(names, STATUS_PREFIX)
        entry = {
            "number": issue["number"],
            "title": issue.get("title", ""),
            "status_labels": status_labels,
            "state_reason": issue.get("stateReason"),
            "url": issue.get("url", ""),
        }
        if len(status_labels) != 1:
            entry["problem"] = "status=%d（应恰好 1 个）" % len(status_labels)
            anomaly.append(entry)
            continue
        if status_labels[0] == "status:done":
            normal.append(entry)
            continue
        normal_close = issue.get("stateReason") == "NOT_PLANNED" or any(
            name in NORMAL_CLOSE_LABELS for name in names
        )
        (normal if normal_close else leftover).append(entry)
    return {"leftover": leftover, "anomaly": anomaly, "normal": normal}


def analyze_handoff_pending(issues) -> list:
    return [
        {"number": i["number"], "title": i.get("title", ""), "url": i.get("url", "")}
        for i in issues
        if "handoff" in label_names(i)
    ]


def analyze_merged_unclosed(prs, open_numbers: set) -> list:
    candidates = []
    for pr in prs:
        refs = [ref.get("number") for ref in pr.get("closingIssuesReferences", [])]
        still_open = [number for number in refs if number in open_numbers]
        if still_open:
            candidates.append(
                {
                    "pr": pr["number"],
                    "title": pr.get("title", ""),
                    "issues": still_open,
                    "merged_at": pr.get("mergedAt"),
                    "url": pr.get("url", ""),
                }
            )
    return candidates


def parse_worktrees(porcelain: str) -> list:
    entries, current = [], {}
    for line in porcelain.splitlines():
        if line.startswith("worktree "):
            if current:
                entries.append(current)
            current = {"path": line[len("worktree ") :]}
        elif line.startswith("branch refs/heads/"):
            current["branch"] = line[len("branch refs/heads/") :]
        elif line.startswith("HEAD "):
            current["head"] = line[len("HEAD ") :]
        elif line.strip() == "detached":
            current["detached"] = True
    if current:
        entries.append(current)
    return entries


def git(*args: str) -> list:
    """Build a git command with global --no-optional-locks.

    Some git queries (notably `status`) otherwise refresh `.git/index`, which
    makes an ostensibly read-only check write to the repository.
    """
    return ["git", "--no-optional-locks", *args]


def is_clean(runner: Runner, path: str) -> bool:
    return run_checked(runner, git("-C", path, "status", "--porcelain")).strip() == ""


def is_ancestor(runner: Runner, branch: str, integration: str) -> bool:
    result = runner(git("merge-base", "--is-ancestor", "refs/heads/" + branch, "origin/" + integration))
    if result.returncode == 0:
        return True
    if result.returncode == 1:
        return False
    raise CommandError(
        git("merge-base", "--is-ancestor", branch, "origin/" + integration),
        result.returncode,
        result.stderr,
    )


def analyze_worktrees(runner: Runner, worktrees, line, integration) -> dict:
    """Only agent/* branches are considered; integration is proven by ancestry alone."""
    candidates, manual, skipped = [], [], []
    prefix = "agent/%s/" % line if line else "agent/"
    for tree in worktrees:
        path = tree.get("path", "")
        branch = tree.get("branch")
        if tree.get("detached") or not branch:
            skipped.append({"path": path, "branch": branch, "reason": "detached / no branch"})
            continue
        if branch in LONG_LIVED_BRANCHES or branch == integration:
            skipped.append({"path": path, "branch": branch, "reason": "long-lived branch"})
            continue
        if not branch.startswith("agent/"):
            skipped.append({"path": path, "branch": branch, "reason": "非 agent 工作分支"})
            continue
        if not branch.startswith(prefix):
            skipped.append(
                {"path": path, "branch": branch, "reason": "不在 --line %s 范围" % line}
            )
            continue
        if not is_clean(runner, path):
            skipped.append({"path": path, "branch": branch, "reason": "dirty worktree"})
            continue
        if is_ancestor(runner, branch, integration):
            candidates.append(
                {
                    "path": path,
                    "branch": branch,
                    "reason": "clean；分支为本地 origin/%s 的祖先（未自动 fetch）" % integration,
                }
            )
        else:
            manual.append(
                {
                    "path": path,
                    "branch": branch,
                    "reason": "clean；不是本地 origin/%s 的祖先（squash／stacked／未合入），"
                    "只提示人工核对，不判断可删" % integration,
                }
            )
    return {"candidates": candidates, "manual": manual, "skipped": skipped}


SCHEMA_ISSUES = "number,title,labels,stateReason,url"
SCHEMA_PRS = "number,title,headRefName,mergedAt,mergeCommit,closingIssuesReferences,url"


def build_report(runner: Runner, limit: int, line, integration: str) -> dict:
    open_issues = gh_json(
        runner,
        ["gh", "issue", "list", "--state", "open", "--limit", str(limit), "--json", SCHEMA_ISSUES],
    )
    closed_issues = gh_json(
        runner,
        ["gh", "issue", "list", "--state", "closed", "--limit", str(limit), "--json", SCHEMA_ISSUES],
    )
    merged_prs = gh_json(
        runner,
        ["gh", "pr", "list", "--state", "merged", "--limit", str(limit), "--json", SCHEMA_PRS],
    )
    worktrees = parse_worktrees(run_checked(runner, git("worktree", "list", "--porcelain")))
    closed = analyze_closed_issues(closed_issues)
    return {
        "window": {
            "limit": limit,
            "note": "gh --limit 窗口，非全量；按 gh 默认排序取前 N 条",
            "line_filter": "worktree 仅看 agent/%s/..." % line if line else "worktree 看 agent/*",
            "issue_scope": "issue／PR 分类始终覆盖整个仓库",
            "integration": integration,
            "refs_note": "worktree 集成判断依据本地 origin/%s 引用；脚本不自动 fetch，"
            "回收前由主责 fetch 后核对" % integration,
        },
        "label_constraints": analyze_open_issues(open_issues),
        "handoff_pending": analyze_handoff_pending(open_issues),
        "closed_leftover": closed["leftover"],
        "closed_anomaly": closed["anomaly"],
        "closed_normal": closed["normal"],
        "merged_unclosed_candidates": analyze_merged_unclosed(
            merged_prs, {issue["number"] for issue in open_issues}
        ),
        "worktrees": analyze_worktrees(runner, worktrees, line, integration),
    }


def _print_section(title: str, entries, formatter) -> None:
    print("\n== %s ==" % title)
    if not entries:
        print("  （无）")
        return
    for entry in entries:
        print("  - " + formatter(entry))


def print_text(report: dict) -> None:
    window = report["window"]
    print("== Saki 协作只读检查 ==")
    print(
        "窗口：limit=%s（%s）；%s；%s；集成分支=%s"
        % (
            window["limit"],
            window["note"],
            window["line_filter"],
            window["issue_scope"],
            window["integration"],
        )
    )
    print("引用：%s" % window["refs_note"])
    print("说明：以下均为候选，不自动验收；脚本不会改标签、关闭 issue、ACK、删除或提交。")

    _print_section(
        "open issue 标签约束（status 恰好 1；仅 triage 时 type/agent 可为 0 或 1，其余各为 1）",
        report["label_constraints"],
        lambda e: "#%s %s -> %s [%s]" % (e["number"], e["title"], ", ".join(e["problems"]), e["url"]),
    )
    _print_section(
        "open handoff 待接收确认",
        report["handoff_pending"],
        lambda e: "#%s %s [%s]" % (e["number"], e["title"], e["url"]),
    )
    _print_section(
        "已关闭但状态标签遗留（需人工核对）",
        report["closed_leftover"],
        lambda e: "#%s %s 遗留=%s state_reason=%s [%s]"
        % (e["number"], e["title"], ",".join(e["status_labels"]), e["state_reason"], e["url"]),
    )
    _print_section(
        "已关闭但 status 缺失或重复（需人工核对）",
        report["closed_anomaly"],
        lambda e: "#%s %s %s [%s]" % (e["number"], e["title"], e["problem"], e["url"]),
    )
    _print_section(
        "已正常关闭（status:done 或 duplicate/invalid/wontfix/NOT_PLANNED，信息）",
        report["closed_normal"],
        lambda e: "#%s %s [%s]" % (e["number"], e["title"], e["url"]),
    )
    _print_section(
        "已合并 PR 关联但仍 open 的 issue 候选",
        report["merged_unclosed_candidates"],
        lambda e: "PR #%s %s -> issues %s [%s]"
        % (e["pr"], e["title"], e["issues"], e["url"]),
    )

    worktrees = report["worktrees"]
    _print_section(
        "工作区回收候选（clean 且为本地 origin 集成分支祖先；仍需人工确认归属）",
        worktrees["candidates"],
        lambda e: "%s [%s] %s" % (e["branch"], e["path"], e["reason"]),
    )
    _print_section(
        "工作区需人工核对（非祖先／squash／stacked）",
        worktrees["manual"],
        lambda e: "%s [%s] %s" % (e["branch"], e["path"], e["reason"]),
    )
    _print_section(
        "已跳过的工作区",
        worktrees["skipped"],
        lambda e: "%s [%s] %s" % (e.get("branch"), e["path"], e["reason"]),
    )


def main(argv=None, runner: Runner = subprocess_runner) -> int:
    parser = argparse.ArgumentParser(description="Saki 协作只读检查（候选，不自动验收）")
    parser.add_argument("--limit", type=int, default=100, help="gh list 的窗口大小（默认 100）")
    parser.add_argument(
        "--line", default=None, help="只过滤 worktree 分支为 agent/<line>/...；issue／PR 仍全仓库"
    )
    parser.add_argument("--integration", default="dev", help="集成分支（默认 dev）")
    parser.add_argument("--json", action="store_true", help="输出 JSON")
    args = parser.parse_args(argv)

    try:
        report = build_report(runner, args.limit, args.line, args.integration)
    except CommandError as exc:
        print("只读检查失败：%s" % exc, file=sys.stderr)
        return 2
    if args.json:
        print(json.dumps(report, ensure_ascii=False, indent=2))
    else:
        print_text(report)
    return 0


if __name__ == "__main__":
    sys.exit(main())
