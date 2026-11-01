#!/usr/bin/env python3
"""Validate go test -json results for the offline desktop GUI/runtime suite.

This is a CI result contract, not a product test framework. The full desktop
package runs in the rust job; this checker makes missing/extra-skipped key
scenarios fail instead of letting a broad test count or cached run hide them.
"""
from __future__ import annotations

import json
import sys

DESKTOP = "saki/desktop"
REQUIRED_TESTS = (
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
REQUIRED = tuple((DESKTOP, test) for test in REQUIRED_TESTS)
ALLOWED_SKIP_TESTS = frozenset({
    "TestGUIOnlineSmoke",
    "TestIntegratedMarkdownPreview",
    "TestMarkdownReaderScreenshot",
    "TestPreviewConnectionSettings",
    "TestPreviewImages",
    "TestPreviewIssueStates",
    "TestPreviewRuntimeStates",
    "TestPreviewStartupDiagnostics",
})
ALLOWED_SKIPS = frozenset((DESKTOP, test) for test in ALLOWED_SKIP_TESTS)


def main(path: str) -> int:
    results: dict[tuple[str, str], str] = {}
    fail_keys: set[tuple[str, str]] = set()
    skip_keys: set[tuple[str, str]] = set()
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
                    if test and package:
                        key = (package, test)
                        results[key] = action
                        if action == "fail":
                            fail_keys.add(key)
                        if action == "skip":
                            skip_keys.add(key)
                    elif package:
                        package_actions.append((package, action))
    except OSError as exc:
        print(f"checker: cannot read {path}: {exc}", file=sys.stderr)
        return 1

    errors: list[str] = []
    passes = sorted(key for key, action in results.items() if action == "pass")
    skips = sorted(key for key in skip_keys)
    fails = sorted(key for key in fail_keys)

    if not results:
        errors.append("no test result events (zero tests executed)")
    if not passes:
        errors.append("no PASS events")
    if fails:
        errors.append("FAIL tests: " + ", ".join(f"{pkg}::{test}" for pkg, test in fails))
    for package, test in REQUIRED:
        key = (package, test)
        action = results.get(key)
        if action != "pass" or key in fail_keys:
            errors.append(f"required test {package}::{test}: {action or 'missing'}")
    for package, test in skips:
        if (package, test) not in ALLOWED_SKIPS:
            errors.append(f"unexpected SKIP: {package}::{test}")

    desktop_package = [action for package, action in package_actions if package == "saki/desktop"]
    if "pass" not in desktop_package:
        errors.append("saki/desktop package did not finish PASS")
    if "fail" in desktop_package or any(pkg == "saki/desktop" for pkg, _ in fails):
        errors.append("saki/desktop package finished FAIL")
    if any(action == "fail" for _, action in package_actions):
        errors.append("a package finished FAIL")

    for package, test in REQUIRED:
        key = (package, test)
        action = "fail" if key in fail_keys else results.get(key, "missing")
        print(f"checker: MUST {package}::{test} = {action}")
    print(f"checker: tests={len(results)} pass={len(passes)} skip={len(skips)} fail={len(fails)}")
    print("checker: skips=" + (", ".join(f"{pkg}::{test}" for pkg, test in skips) if skips else "<none>"))
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
