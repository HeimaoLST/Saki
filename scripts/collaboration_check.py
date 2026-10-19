#!/usr/bin/env python3
"""Saki 多 agent 协作只读检查。

复用 `gh` CLI 与 `git` 做只读查询：不修改 GitHub、不改标签、不关闭 issue、
不删除分支或 worktree、不提交、不读取环境里的密钥。

退出码：
  0  已生成报告（报告只是候选，不代表自动验收）
  2  某条只读命令失败；不会把 API 失败当成空队列成功

用法：
  python3 scripts/collaboration_check.py [--limit N] [--line agent-line] [--json]
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from dataclasses import dataclass
from typing import Callable, Sequence

TYPE_PREFIX = "type:"
STATUS_PREFIX = "status:"
AGENT_PREFIX = "agent:"
NORMAL_CLOSE_LABELS = {"duplicate", "invalid", "wontfix"}
LONG_LIVED_BRANCHES = {"master", "dev"}
READ_ONLY_GIT_SUBCOMMANDS = {
    "status",
    "rev-parse",
    "merge-base",
    "log",
    "rev-list",
    "for-each-ref",
    "show-ref",
    "ls-remote",
    "diff",
    "symbolic-ref",
}


@dataclass(frozen=True)
class CommandResult:
    args: tuple
    returncode: int
    stdout: str
    stderr: str


class CommandError(RuntimeError):
    """A read command failed; callers must not treat this as empty output."""

    def __init__(self, result: CommandResult):
        self.result = result
        super().__init__(
            "command failed (rc={rc}): {cmd}\n{err}".format(
                rc=result.returncode,
                cmd=" ".join(result.args),
                err=result.stderr.strip(),
            )
        )


def assert_read_only(args: Sequence[str]) -> None:
    """Reject anything that could mutate the repo or GitHub."""
    if not args:
        raise ValueError("empty command")
    program = args[0]
    if program == "git":
        rest = list(args[1:])
        if len(rest) >= 2 and rest[0] == "-C":
            rest = rest[2:]
        if len(rest) >= 2 and rest[0] == "worktree" and rest[1] == "list":
            return
        if not rest or rest[0] not in READ_ONLY_GIT_SUBCOMMANDS:
            raise ValueError("refusing non read-only git command: " + " ".join(args))
        return
    if program == "gh":
        if len(args) >= 3 and args[1] in {"issue", "pr"} and args[2] in {"list", "view"}:
            return
        raise ValueError("refusing non read-only gh command: " + " ".join(args))
    raise ValueError("unsupported program: " + program)


def subprocess_runner(args: Sequence[str]) -> CommandResult:
    checked = tuple(args)
    assert_read_only(checked)
    proc = subprocess.run(checked, capture_output=True, text=True, check=False)
    return CommandResult(checked, proc.returncode, proc.stdout, proc.stderr)


Runner = Callable[[Sequence[str]], CommandResult]


def run_checked(runner: Runner, args: Sequence[str]) -> str:
    result = runner(args)
    if result.returncode != 0:
        raise CommandError(result)
    return result.stdout


def gh_json(runner: Runner, args: Sequence[str]):
    raw = run_checked(runner, args)
    try:
        return json.loads(raw or "[]")
    except json.JSONDecodeError as exc:
        raise CommandError(CommandResult(tuple(args), 1, raw, "invalid JSON: %s" % exc))


def label_names(issue) -> list:
    return [label["name"] for label in issue.get("labels", [])]


def prefixed(names, prefix) -> list:
    return [name for name in names if name.startswith(prefix)]


def analyze_open_issues(issues) -> list:
    findings = []
    for issue in issues:
        names = label_names(issue)
        counts = {
            "type": len(prefixed(names, TYPE_PREFIX)),
            "status": len(prefixed(names, STATUS_PREFIX)),
            "agent": len(prefixed(names, AGENT_PREFIX)),
        }
        problems = ["%s=%d" % (key, value) for key, value in counts.items() if value != 1]
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
    leftovers, normal = [], []
    for issue in issues:
        names = label_names(issue)
        status_labels = [n for n in names if n.startswith(STATUS_PREFIX) and n != "status:done"]
        if not status_labels:
            continue
        entry = {
            "number": issue["number"],
            "title": issue.get("title", ""),
            "status_labels": status_labels,
            "state_reason": issue.get("stateReason"),
            "url": issue.get("url", ""),
        }
        normal_close = issue.get("stateReason") == "NOT_PLANNED" or any(
            name in NORMAL_CLOSE_LABELS for name in names
        )
        (normal if normal_close else leftovers).append(entry)
    return {"leftover": leftovers, "normal": normal}


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


def is_clean(runner: Runner, path: str) -> bool:
    return run_checked(runner, ["git", "-C", path, "status", "--porcelain"]).strip() == ""


def branch_tip(runner: Runner, branch: str) -> str:
    return run_checked(runner, ["git", "rev-parse", "refs/heads/" + branch]).strip()


def is_ancestor(runner: Runner, branch: str, integration: str) -> bool:
    result = runner(
        ["git", "merge-base", "--is-ancestor", "refs/heads/" + branch, "origin/" + integration]
    )
    if result.returncode == 0:
        return True
    if result.returncode == 1:
        return False
    raise CommandError(result)


def analyze_worktrees(runner: Runner, worktrees, line, integration, merged_by_branch) -> dict:
    candidates, manual, skipped = [], [], []
    prefix = "agent/%s/" % line if line else None
    for tree in worktrees:
        path = tree.get("path", "")
        branch = tree.get("branch")
        if tree.get("detached") or not branch:
            skipped.append({"path": path, "branch": branch, "reason": "detached / no branch"})
            continue
        if branch in LONG_LIVED_BRANCHES or branch == integration:
            skipped.append({"path": path, "branch": branch, "reason": "long-lived branch"})
            continue
        if prefix and not branch.startswith(prefix):
            skipped.append({"path": path, "branch": branch, "reason": "outside --line %s" % line})
            continue
        if not is_clean(runner, path):
            skipped.append({"path": path, "branch": branch, "reason": "dirty worktree"})
            continue
        if is_ancestor(runner, branch, integration):
            candidates.append(
                {
                    "path": path,
                    "branch": branch,
                    "reason": "clean；已合入 origin/%s（祖先关系）" % integration,
                }
            )
            continue
        pr = merged_by_branch.get(branch)
        if not pr:
            skipped.append(
                {"path": path, "branch": branch, "reason": "未合入 origin/%s" % integration}
            )
            continue
        pr_sha = (pr.get("mergeCommit") or {}).get("oid")
        try:
            tip = branch_tip(runner, branch)
        except CommandError:
            tip = None
        if pr_sha and tip and pr_sha == tip:
            candidates.append(
                {
                    "path": path,
                    "branch": branch,
                    "reason": "clean；分支 tip 等于已合并 PR #%s 的 merge commit" % pr["number"],
                }
            )
        else:
            manual.append(
                {
                    "path": path,
                    "branch": branch,
                    "reason": "PR #%s 已合并但分支 tip 不同或为 squash；只提示人工核对，不判断可删"
                    % pr["number"],
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
    worktrees = parse_worktrees(run_checked(runner, ["git", "worktree", "list", "--porcelain"]))
    merged_by_branch = {
        pr.get("headRefName"): pr for pr in merged_prs if pr.get("headRefName")
    }
    closed = analyze_closed_issues(closed_issues)
    return {
        "window": {
            "limit": limit,
            "note": "gh --limit 窗口，非全量；按 gh 默认排序取前 N 条",
            "line_filter": line or "all agent/* branches",
            "integration": integration,
        },
        "label_constraints": analyze_open_issues(open_issues),
        "handoff_pending": analyze_handoff_pending(open_issues),
        "closed_leftover": closed["leftover"],
        "closed_normal": closed["normal"],
        "merged_unclosed_candidates": analyze_merged_unclosed(
            merged_prs, {issue["number"] for issue in open_issues}
        ),
        "worktrees": analyze_worktrees(runner, worktrees, line, integration, merged_by_branch),
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
        "窗口：limit=%s（%s）；业务线过滤=%s；集成分支=%s"
        % (window["limit"], window["note"], window["line_filter"], window["integration"])
    )
    print("说明：以下均为候选，不自动验收；脚本不会改标签、关闭 issue、ACK、删除或提交。")

    _print_section(
        "open issue 标签约束（type/status/agent 各应为 1）",
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
        "已正常关闭（duplicate/invalid/wontfix 或 NOT_PLANNED，信息）",
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
        "工作区回收候选（clean 且已集成，仍建议人工确认归属）",
        worktrees["candidates"],
        lambda e: "%s [%s] %s" % (e["branch"], e["path"], e["reason"]),
    )
    _print_section(
        "工作区需人工核对（squash／tip 不同或未合入）",
        worktrees["manual"],
        lambda e: "%s [%s] %s" % (e["branch"], e["path"], e["reason"]),
    )
    _print_section(
        "已跳过的工作区",
        worktrees["skipped"],
        lambda e: "%s [%s] %s" % (e.get("branch"), e["path"], e["reason"]),
    )


def main(argv=None, runner: Runner = subprocess_runner) -> int:
    parser = argparse.ArgumentParser(description="Saki 协作只读检查")
    parser.add_argument("--limit", type=int, default=100, help="gh list 的窗口大小（默认 100）")
    parser.add_argument("--line", default=None, help="只检查 agent/<line>/... worktree 分支")
    parser.add_argument("--integration", default="dev", help="集成分支（默认 dev）")
    parser.add_argument("--json", action="store_true", help="输出 JSON")
    args = parser.parse_args(argv)

    try:
        report = build_report(runner, args.limit, args.line, args.integration)
    except (CommandError, ValueError) as exc:
        print("只读检查失败：%s" % exc, file=sys.stderr)
        return 2
    if args.json:
        print(json.dumps(report, ensure_ascii=False, indent=2))
    else:
        print_text(report)
    return 0


if __name__ == "__main__":
    sys.exit(main())
