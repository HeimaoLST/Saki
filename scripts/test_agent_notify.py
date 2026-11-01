#!/usr/bin/env python3
"""Deterministic tests for the bounded notification helper."""

from __future__ import annotations

import io
import sys
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import agent_notify as notify


URL = "https://github.com/HeimaoLST/Saki/issues/236#issuecomment-6556457330"


def pane(
    *,
    agent_id: str = "agent:pm#mira",
    parent: str = "",
    command: str = "node",
    pane_id: str = "%1",
    task: str = "#236",
) -> notify.Pane:
    return notify.Pane(
        pane_id=pane_id,
        agent_id=agent_id,
        parent_agent_id=parent,
        task=task,
        session="0",
        window="@1",
        pane_pid="123",
        command=command,
        cwd="/tmp/worktree",
    )


class InspectTests(unittest.TestCase):
    def test_unique_main_is_accepted(self) -> None:
        target = pane()
        with mock.patch.object(notify, "_tmux_panes", return_value=[target]):
            with mock.patch.object(notify, "_tmux_capture", return_value="ready"):
                result = notify.inspect(target.agent_id)
        self.assertEqual(result["pane_id"], "%1")
        self.assertEqual(result["fingerprint"], target.fingerprint)
        self.assertEqual(result["capture"], "ready")

    def test_duplicate_main_is_rejected(self) -> None:
        with mock.patch.object(
            notify, "_tmux_panes", return_value=[pane(), pane(pane_id="%2")]
        ):
            with self.assertRaisesRegex(notify.NotifyError, "duplicate"):
                notify.inspect(pane().agent_id)

    def test_invalid_agent_id_is_rejected_before_tmux(self) -> None:
        with mock.patch.object(notify, "_tmux_panes") as panes:
            with self.assertRaisesRegex(notify.NotifyError, "stable"):
                notify.inspect("not-an-agent")
        panes.assert_not_called()

    def test_child_is_rejected(self) -> None:
        child = pane(parent="agent:pm#mira")
        with mock.patch.object(notify, "_tmux_panes", return_value=[child]):
            with self.assertRaisesRegex(notify.NotifyError, "child"):
                notify.inspect(child.agent_id)

    def test_shell_target_is_rejected(self) -> None:
        shell = pane(command="zsh")
        with mock.patch.object(notify, "_tmux_panes", return_value=[shell]):
            with self.assertRaisesRegex(notify.NotifyError, "shell"):
                notify.inspect(shell.agent_id)


class SendTests(unittest.TestCase):
    def test_fingerprint_change_sends_nothing(self) -> None:
        target = pane()
        with mock.patch.object(notify, "_tmux_panes", return_value=[target]):
            with mock.patch.object(notify, "_run") as run:
                with self.assertRaisesRegex(notify.NotifyError, "fingerprint"):
                    notify.send(target.agent_id, "stale", URL)
        run.assert_not_called()

    def test_duplicate_notice_sends_nothing(self) -> None:
        target = pane()
        message = notify.fixed_message(target.agent_id, URL)
        with mock.patch.object(notify, "_tmux_panes", return_value=[target]):
            with mock.patch.object(notify, "_tmux_capture", return_value=message):
                with mock.patch.object(notify, "_run") as run:
                    with self.assertRaisesRegex(notify.NotifyError, "already present"):
                        notify.send(target.agent_id, target.fingerprint, URL)
        run.assert_not_called()

    def test_paste_is_verified_before_one_enter(self) -> None:
        target = pane()
        message = notify.fixed_message(target.agent_id, URL)
        calls: list[tuple[str, ...]] = []

        def fake_run(argv: list[str], input_text: str | None = None) -> str:
            calls.append(tuple(argv))
            return ""

        with mock.patch.object(notify, "_tmux_panes", return_value=[target]):
            with mock.patch.object(
                notify,
                "_tmux_capture",
                side_effect=["", message],
            ):
                with mock.patch.object(notify, "_run", side_effect=fake_run):
                    result = notify.send(target.agent_id, target.fingerprint, URL)
        self.assertEqual(result["transport"], "submitted")
        self.assertEqual(result["ack"], "pending")
        self.assertEqual(sum(call[:2] == ("tmux", "send-keys") for call in calls), 1)
        self.assertEqual(sum(call[:2] == ("tmux", "paste-buffer") for call in calls), 1)
        self.assertEqual(len([call for call in calls if call[:2] == ("tmux", "send-keys")]), 1)

    def test_invisible_paste_does_not_send_enter(self) -> None:
        target = pane()
        with mock.patch.object(notify, "_tmux_panes", return_value=[target]):
            with mock.patch.object(notify, "_tmux_capture", return_value=""):
                with mock.patch.object(notify, "_run") as run:
                    with self.assertRaisesRegex(notify.NotifyError, "not visible"):
                        notify.send(target.agent_id, target.fingerprint, URL)
        self.assertFalse(
            any(call.args[0][:2] == ["tmux", "send-keys"] for call in run.call_args_list)
        )


class StatusTests(unittest.TestCase):
    def _ack(self, **overrides: str) -> str:
        fields = {
            "FROM": "agent:pm#mira",
            "KIND": "ack",
            "ACK": URL,
            "SPEC": "r1",
            "SCOPE": "AGENTS.md, scripts/agent_notify.py",
            "NEXT": "read fixed spec",
        }
        fields.update(overrides)
        return "\n".join(f"{key}: {value}" for key, value in fields.items())

    def _comments(self, body: str) -> list[dict[str, str]]:
        return [{"body": body, "html_url": "https://github.com/HeimaoLST/Saki/issues/236#issuecomment-1"}]

    def test_complete_exact_ack_passes(self) -> None:
        with mock.patch.object(notify, "_comments", return_value=self._comments(self._ack())):
            result = notify.status("agent:pm#mira", URL, "r1")
        self.assertEqual(result["status"], "ack")

    def test_old_spec_or_wrong_target_is_pending(self) -> None:
        for overrides in (
            {"SPEC": "r0"},
            {"FROM": "agent:pm#other"},
            {"ACK": URL.replace("6556457330", "1")},
            {"SCOPE": ""},
            {"NEXT": ""},
        ):
            with mock.patch.object(
                notify, "_comments", return_value=self._comments(self._ack(**overrides))
            ):
                result = notify.status("agent:pm#mira", URL, "r1")
            self.assertEqual(result["status"], "pending", overrides)

    def test_quoted_or_fenced_ack_is_not_an_ack(self) -> None:
        for body in ("> " + self._ack(), "```\n" + self._ack() + "\n```"):
            with mock.patch.object(notify, "_comments", return_value=self._comments(body)):
                self.assertEqual(notify.status("agent:pm#mira", URL, "r1")["status"], "pending")

    def test_github_error_is_error_not_empty_success(self) -> None:
        with mock.patch.object(notify, "_comments", side_effect=notify.NotifyError("gh down")):
            with self.assertRaisesRegex(notify.NotifyError, "GitHub"):
                notify.status("agent:pm#mira", URL, "r1")

    def test_comment_url_is_strict(self) -> None:
        for bad in (
            "https://github.com/HeimaoLST/Saki/pull/236",
            "https://github.com/HeimaoLST/Saki/issues/236#discussion_r1",
            "https://example.com/HeimaoLST/Saki/issues/236#issuecomment-1",
        ):
            with self.assertRaises(notify.NotifyError):
                notify.parse_comment_url(bad)


if __name__ == "__main__":
    unittest.main()
