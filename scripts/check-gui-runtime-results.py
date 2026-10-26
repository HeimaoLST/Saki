#!/usr/bin/env python3
"""Validate go test -json results for the offline desktop GUI/runtime suite.

This is a CI result contract, not a product test framework. The full desktop
package runs in the rust job; this checker makes missing/extra-skipped key
scenarios fail instead of letting a broad test count or cached run hide them.
"""
from __future__ import annotations

import json
import sys

REQUIRED = (
    # Original #87 five-test gate.
    "TestRuntimeProcessIntegration",
    "TestGUIAppRuntimeFlow",
    "TestGUIFirstCreateUsesProfileSnapshot",
    "TestGUIAppPersistsMappingBeforeSend",
    "TestGUIAppSaveFailureStopsBeforeSend",
    # Current Nagi integration/key scenarios.
    "TestConnectionGUIFirstSaveApplyTestSendAndRestart",
    "TestConnectionGUISaveDuringIssueAndReloadFailurePreservesHelper",
    "TestConnectionGUIFailedTestCanBeCorrectedWithoutSaving",
    "TestConnectionGUISaveFailureAndUnknownExecutionRecovery",
    "TestConnectionGUIEndpointChangePreservesOldContextAndNewDefaults",
    "TestConnectionProcessProbeBothProtocolsAndRepair",
    "TestConnectionProcessSavedChangeKeepsActiveSnapshotAndHistory",
    "TestIndependentIssueWorkAndConversationCapacityTwo",
    "TestIndependentExecutionCapacityOneSerializesConversation",
    "TestStoppingIssueWorkDoesNotStopConversation",
    "TestCapacityTwoReservesOneSlotForConversation",
    "TestUnknownIssueExecutionIsRecoveredFromHistoryAfterRestart",
    "TestIssueWorkStopButtonStopsIssueNotConversation",
)

# Explicit opt-in preview/online tests. All other skips are failures.
ALLOWED_SKIPS = frozenset({
    "TestGUIOnlineSmoke",
    "TestIntegratedMarkdownPreview",
    "TestMarkdownReaderScreenshot",
    "TestPreviewConnectionSettings",
    "TestPreviewImages",
    "TestPreviewIssueStates",
    "TestPreviewRuntimeStates",
    "TestPreviewStartupDiagnostics",
})


def main(path: str) -> int:
    results: dict[str, str] = {}
    package_actions: list[tuple[str, str]] = []
    try:
        with open(path, encoding="utf-8") as handle:
            for line in handle:
                line = line.strip()
                if not line:
                    continue
                try:
                    event = json.loads(line)
                except json.JSONDecodeError as exc:
                    print(f"checker: invalid JSON line: {exc}", file=sys.stderr)
                    return 1
                action = event.get("Action")
                test = event.get("Test")
                package = event.get("Package")
                if action in ("pass", "fail", "skip"):
                    if test:
                        results[test] = action
                    elif package:
                        package_actions.append((package, action))
    except OSError as exc:
        print(f"checker: cannot read {path}: {exc}", file=sys.stderr)
        return 1

    errors: list[str] = []
    passes = [test for test, action in results.items() if action == "pass"]
    skips = sorted(test for test, action in results.items() if action == "skip")
    fails = sorted(test for test, action in results.items() if action == "fail")

    if not results:
        errors.append("no test result events (zero tests executed)")
    if not passes:
        errors.append("no PASS events")
    if fails:
        errors.append("FAIL tests: " + ", ".join(fails))
    for test in REQUIRED:
        action = results.get(test)
        if action != "pass":
            errors.append(f"required test {test}: {action or 'missing'}")
    for test in skips:
        if test not in ALLOWED_SKIPS:
            errors.append(f"unexpected SKIP: {test}")

    desktop_package = [action for package, action in package_actions if package == "saki/desktop"]
    if "pass" not in desktop_package:
        errors.append("desktop package did not finish PASS")
    if "fail" in desktop_package:
        errors.append("desktop package finished FAIL")

    print(f"checker: tests={len(results)} pass={len(passes)} skip={len(skips)} fail={len(fails)}")
    print("checker: skips=" + (", ".join(skips) if skips else "<none>"))
    if errors:
        for error in errors:
            print("checker: ERROR " + error, file=sys.stderr)
        return 1
    print("checker: required scenarios present and PASS; all skips are allowlisted")
    return 0


if __name__ == "__main__":
    if len(sys.argv) != 2:
        print(f"usage: {sys.argv[0]} <go-test-json>", file=sys.stderr)
        sys.exit(2)
    sys.exit(main(sys.argv[1]))
