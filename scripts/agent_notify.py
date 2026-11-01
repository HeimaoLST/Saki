#!/usr/bin/env python3
"""Bounded tmux notification helper for Spec-driven collaboration.

Only three operations are supported:

* inspect: discover one unique main pane by stable agent ID and emit metadata,
  current capture and a fingerprint.
* send: revalidate that pane/fingerprint and paste one fixed GitHub comment
  notice, verify the pasted text, then send Enter exactly once.
* status: read GitHub issue/PR comments and accept only a complete top-level
  ACK whose FROM/KIND/ACK/SPEC/SCOPE/NEXT fields match the request.

The helper deliberately has no daemon, database, retry loop or shell strings.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import time
import uuid
from dataclasses import dataclass
from typing import Any, Iterable
from urllib.parse import urlparse


class NotifyError(RuntimeError):
    """A failed transport, validation or GitHub read."""


@dataclass(frozen=True)
class Pane:
    pane_id: str
    agent_id: str
    parent_agent_id: str
    task: str
    session: str
    window: str
    pane_pid: str
    command: str
    cwd: str

    @property
    def is_main(self) -> bool:
        return not self.parent_agent_id.strip()

    @property
    def identity(self) -> tuple[str, ...]:
        return (
            self.pane_id,
            self.agent_id,
            self.parent_agent_id,
            self.task,
            self.session,
            self.window,
            self.pane_pid,
            self.command,
            self.cwd,
        )

    def fingerprint(self, capture: str) -> str:
        value = [*self.identity, _normalized(capture)]
        return hashlib.sha256("\0".join(value).encode("utf-8")).hexdigest()


SUPPORTED_AGENT_COMMANDS = {"codex", "node"}
AGENT_ID = re.compile(r"^agent:[a-z]+#[A-Za-z0-9._-]+$")
COMMENT_URL = re.compile(
    r"^https://github\.com/([^/]+)/([^/]+)/(issues|pull)/([0-9]+)"
    r"#issuecomment-([0-9]+)$"
)
HEADER_LINE = re.compile(r"^([A-Z]+):[ \t]*(.*)$")
REQUIRED_ACK = ("FROM", "KIND", "ACK", "SPEC", "SCOPE", "NEXT")


def _run(argv: list[str], input_text: str | None = None) -> str:
    try:
        result = subprocess.run(
            argv,
            input=input_text,
            text=True,
            capture_output=True,
            check=False,
        )
    except OSError as exc:
        raise NotifyError(f"command unavailable: {argv[0]}: {exc}") from exc
    if result.returncode != 0:
        detail = (result.stderr or result.stdout or "").strip()
        raise NotifyError(
            f"command failed ({result.returncode}): {' '.join(argv)}: {detail[:500]}"
        )
    return result.stdout


def _tmux_panes() -> list[Pane]:
    fmt = "\t".join(
        (
            "#{pane_id}",
            "#{@saki_agent_id}",
            "#{@saki_parent_agent_id}",
            "#{@saki_task}",
            "#{session_name}",
            "#{window_id}",
            "#{pane_pid}",
            "#{pane_current_command}",
            "#{pane_current_path}",
        )
    )
    output = _run(["tmux", "list-panes", "-a", "-F", fmt])
    panes: list[Pane] = []
    for line in output.splitlines():
        fields = line.split("\t")
        if len(fields) != 9:
            raise NotifyError("unexpected tmux pane metadata")
        panes.append(Pane(*fields))
    return panes


def _tmux_capture(pane_id: str, history: int = 80) -> str:
    return _run(
        ["tmux", "capture-pane", "-p", "-t", pane_id, "-S", str(-history)]
    )


def _validate_target(pane: Pane, agent_id: str) -> None:
    if pane.agent_id != agent_id:
        raise NotifyError("target agent mismatch")
    if not pane.is_main:
        raise NotifyError("target is a child pane; only a main pane is allowed")
    command = os.path.basename(pane.command).lower()
    if command not in SUPPORTED_AGENT_COMMANDS:
        raise NotifyError(f"target process is not an allowed agent: {pane.command}")


def discover(agent_id: str) -> Pane:
    if not AGENT_ID.fullmatch(agent_id):
        raise NotifyError("agent ID must be a stable agent:<line>#<name> ID")
    matches = [pane for pane in _tmux_panes() if pane.agent_id == agent_id]
    if not matches:
        raise NotifyError("no pane with that agent ID")
    if len(matches) != 1:
        raise NotifyError(f"duplicate panes for agent ID: {len(matches)}")
    pane = matches[0]
    _validate_target(pane, agent_id)
    return pane


def inspect(agent_id: str) -> dict[str, Any]:
    pane = discover(agent_id)
    capture = _tmux_capture(pane.pane_id)
    return {
        "agent_id": pane.agent_id,
        "pane_id": pane.pane_id,
        "parent_agent_id": pane.parent_agent_id,
        "task": pane.task,
        "session": pane.session,
        "window": pane.window,
        "pane_pid": pane.pane_pid,
        "command": pane.command,
        "cwd": pane.cwd,
        "fingerprint": pane.fingerprint(capture),
        "capture": capture,
    }


def parse_comment_url(url: str) -> tuple[str, str, str, str, str]:
    parsed = urlparse(url)
    match = COMMENT_URL.fullmatch(url)
    if parsed.scheme != "https" or parsed.netloc != "github.com" or not match:
        raise NotifyError("only an exact GitHub issue/PR comment URL is allowed")
    owner, repo, kind, number, comment_id = match.groups()
    return owner, repo, kind, number, comment_id


def fixed_message(agent_id: str, comment_url: str) -> str:
    return f"TO: {agent_id} 固定评论：{comment_url}；请按 AGENTS.md 在 GitHub 回 ACK。"


def _normalized(text: str) -> str:
    return re.sub(r"\s+", " ", text).strip()


def _layout_compact(text: str) -> str:
    """Comparison form for terminal layout whitespace and soft wraps."""
    return re.sub(r"\s+", "", text)


def _wait_for_visible(
    pane_id: str,
    message: str,
    *,
    sleep_fn: Any = None,
    attempts: int = 12,
) -> str:
    sleep_fn = sleep_fn or time.sleep
    for index in range(attempts):
        capture = _tmux_capture(pane_id)
        if _layout_compact(message) in _layout_compact(capture):
            return capture
        if index + 1 < attempts:
            sleep_fn(0.05)
    raise NotifyError("pasted notification was not visible; Enter not sent")


def _assert_comment_belongs(
    owner: str, repo: str, number: str, comment_id: str, comments: list[dict[str, Any]]
) -> None:
    expected_issue = f"/repos/{owner}/{repo}/issues/{number}"
    for comment in comments:
        if str(comment.get("id")) != comment_id:
            continue
        issue_url = comment.get("issue_url")
        if not isinstance(issue_url, str) or not issue_url.rstrip("/").endswith(
            expected_issue
        ):
            raise NotifyError("comment does not belong to the requested issue")
        return
    raise NotifyError("target comment is absent from the issue comments")


def send(
    agent_id: str,
    fingerprint: str,
    comment_url: str,
    *,
    sleep_fn: Any = None,
) -> dict[str, Any]:
    owner, repo, _kind, number, comment_id = parse_comment_url(comment_url)
    comments = _comments(owner, repo, number)
    _assert_comment_belongs(owner, repo, number, comment_id, comments)
    pane = discover(agent_id)
    before = _tmux_capture(pane.pane_id)
    if pane.fingerprint(before) != fingerprint:
        raise NotifyError("pane state fingerprint changed; rerun inspect")
    message = fixed_message(agent_id, comment_url)
    if _layout_compact(message) in _layout_compact(before):
        raise NotifyError("notification is already present in the target pane")

    buffer_name = f"saki-notify-{os.getpid()}-{uuid.uuid4().hex}"
    try:
        _run(
            ["tmux", "load-buffer", "-b", buffer_name, "-"],
            input_text=message,
        )
        _run(
            ["tmux", "paste-buffer", "-p", "-b", buffer_name, "-t", pane.pane_id]
        )
        _wait_for_visible(pane.pane_id, message, sleep_fn=sleep_fn)
        current = discover(agent_id)
        if current.identity != pane.identity:
            raise NotifyError("target identity changed after paste; Enter not sent")
        _run(["tmux", "send-keys", "-t", pane.pane_id, "Enter"])
    finally:
        try:
            _run(["tmux", "delete-buffer", "-b", buffer_name])
        except NotifyError:
            pass
    return {
        "transport": "submitted",
        "ack": "pending",
        "agent_id": agent_id,
        "comment_url": comment_url,
        "pane_id": pane.pane_id,
    }


def _comments(owner: str, repo: str, number: str) -> list[dict[str, Any]]:
    output = _run(
        [
            "gh",
            "api",
            f"repos/{owner}/{repo}/issues/{number}/comments",
            "--paginate",
        ]
    )
    if not output.strip():
        raise NotifyError("empty GitHub comments output")
    comments: list[dict[str, Any]] = []
    decoder = json.JSONDecoder()
    position = 0
    while position < len(output):
        while position < len(output) and output[position].isspace():
            position += 1
        if position >= len(output):
            break
        try:
            payload, position = decoder.raw_decode(output, position)
        except json.JSONDecodeError as exc:
            raise NotifyError("invalid GitHub comments JSON") from exc
        if not isinstance(payload, list):
            raise NotifyError("unexpected non-list GitHub comments payload")
        for item in payload:
            if not isinstance(item, dict):
                raise NotifyError("unexpected non-object GitHub comment")
            comments.append(item)
    return comments


def _ack_fields(body: str) -> dict[str, str] | None:
    lines = body.lstrip("\r\n").splitlines()
    if not lines:
        return None
    header: list[str] = []
    for line in lines:
        if not line.strip():
            break
        header.append(line)
        if len(header) > 12:
            return None
    if not header:
        return None
    fields: dict[str, str] = {}
    for line in header:
        if line.lstrip().startswith((">", "```", "~~~")):
            return None
        match = HEADER_LINE.fullmatch(line)
        if not match:
            return None
        key, value = match.groups()
        if key in fields:
            return None
        fields[key] = value.strip()
    if any(not fields.get(key) for key in REQUIRED_ACK):
        return None
    return fields


def status(agent_id: str, comment_url: str, spec: str) -> dict[str, Any]:
    if not AGENT_ID.fullmatch(agent_id):
        raise NotifyError("agent ID must be a stable agent:<line>#<name> ID")
    owner, repo, _kind, number, comment_id = parse_comment_url(comment_url)
    try:
        comments = _comments(owner, repo, number)
    except NotifyError as exc:
        raise NotifyError(f"GitHub comments read failed: {exc}") from exc
    _assert_comment_belongs(owner, repo, number, comment_id, comments)
    for comment in comments:
        body = comment.get("body")
        if not isinstance(body, str):
            continue
        fields = _ack_fields(body)
        if fields is None:
            continue
        if fields["KIND"].lower() != "ack":
            continue
        if fields["FROM"] != agent_id:
            continue
        if fields["ACK"] != comment_url:
            continue
        if fields["SPEC"] != spec:
            continue
        return {
            "status": "ack",
            "agent_id": agent_id,
            "spec": spec,
            "comment_url": comment_url,
            "ack_comment": comment.get("html_url") or comment.get("url"),
        }
    return {
        "status": "pending",
        "agent_id": agent_id,
        "spec": spec,
        "comment_url": comment_url,
    }


def _emit(payload: dict[str, Any], code: int = 0) -> int:
    print(json.dumps(payload, ensure_ascii=False, sort_keys=True))
    return code


def main(argv: Iterable[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    inspect_parser = sub.add_parser("inspect")
    inspect_parser.add_argument("--agent-id", required=True)

    send_parser = sub.add_parser("send")
    send_parser.add_argument("--agent-id", required=True)
    send_parser.add_argument("--fingerprint", required=True)
    send_parser.add_argument("--comment-url", required=True)

    status_parser = sub.add_parser("status")
    status_parser.add_argument("--agent-id", required=True)
    status_parser.add_argument("--comment-url", required=True)
    status_parser.add_argument("--spec", required=True)

    args = parser.parse_args(list(argv) if argv is not None else None)
    try:
        if args.command == "inspect":
            return _emit(inspect(args.agent_id))
        if args.command == "send":
            return _emit(send(args.agent_id, args.fingerprint, args.comment_url))
        result = status(args.agent_id, args.comment_url, args.spec)
        return _emit(result, 0 if result["status"] == "ack" else 1)
    except NotifyError as exc:
        return _emit({"status": "error", "error": str(exc)}, 2)


if __name__ == "__main__":
    raise SystemExit(main())
