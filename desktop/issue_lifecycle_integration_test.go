package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func expandIssueDetails(t *testing.T, tr *ui.Tester) {
	t.Helper()
	if err := tr.Click("More issue actions"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
}

func TestCancelWhileIssueSendPendingLatchesToAcceptedTask(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := newGatedProvider("cancel-latch")
	defer provider.release("cancel-latch")
	h := startRoutingHarnessWithHandler(t, binary, provider.handler(), 1)
	manual := make(chan func(), 8)
	var issueID string
	h.pump.do(func() {
		if !h.app.createIssue("cancel-latch issue", "Do not run after cancel.") {
			t.Fatalf("createIssue: %s", h.app.issueFormError)
		}
		issueID = h.app.selectedContact().SelectedIssueID
		h.app.setUIHook(func(fn func()) { manual <- fn })
		if !h.app.runIssue(issueID) {
			t.Fatalf("runIssue: %s", h.app.issueFormError)
		}
	})
	var mapping func()
	select {
	case mapping = <-manual:
	case <-time.After(15 * time.Second):
		t.Fatal("mapping callback did not arrive")
	}
	h.pump.do(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		if issue.Work.SendState != sendSubmitting {
			t.Fatalf("expected submitting issue work, got %+v", issue.Work)
		}
		if !h.app.cancelIssueProduct(issueID) {
			t.Fatal("cancel during pending acceptance was rejected")
		}
		if issue.Status != issueStatusCanceled || !issue.Work.cancelAfterAccept {
			t.Fatalf("cancel intent was not latched: %+v", issue)
		}
		h.app.addDefaultContact()
		h.app.setUIHook(func(fn func()) { h.pump.q <- fn })
	})
	h.pump.do(mapping)
	h.pump.wait(func() bool {
		issue := h.app.contacts[0].issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionInterrupted
	})
	issue := h.app.contacts[0].issueByID(issueID)
	if issue.Status != issueStatusCanceled || issue.Work.cancelAfterAccept {
		t.Fatalf("cancel latch did not clear after real task stop: %+v", issue)
	}
	if provider.count() > 1 {
		t.Fatalf("cancel caused a retry: requests=%d", provider.count())
	}
	if len(issue.Executions) != 1 || issue.Executions[0].TaskID == "" {
		t.Fatalf("accepted task did not get a real task identity before cancellation: %+v", issue.Executions)
	}
	if other := h.app.contacts[1]; len(other.Issues) != 0 || other.ActiveTaskID != "" {
		t.Fatalf("cancel on contact X changed contact Y: %+v / active=%q", other.Issues, other.ActiveTaskID)
	}
	provider.release("cancel-latch")
	h.shutdown(t)
}

func TestIssueLifecycleIllegalBoundariesDoNotRewriteState(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	issue.Status = issueStatusCompleted
	issue.UpdatedAt = 11
	if a.markIssueWaiting(issue.ID) || a.completeIssue(issue.ID, "again") || a.resumeIssue(issue.ID) {
		t.Fatal("completed issue accepted an invalid lifecycle action")
	}
	if issue.Status != issueStatusCompleted || issue.UpdatedAt != 11 {
		t.Fatalf("invalid action changed state: %+v", issue)
	}
	issue.Status = issueStatusInProgress
	issue.Work.ActiveTaskID = "t1"
	issue.Work.SendState = sendAccepted
	if a.completeIssue(issue.ID, "while active") || a.markIssueWaiting(issue.ID) || a.reopenIssue(issue.ID) {
		t.Fatal("active issue accepted a terminal/waiting action")
	}
	if issue.Status != issueStatusInProgress || issue.Work.ActiveTaskID != "t1" {
		t.Fatalf("active issue state changed: %+v", issue)
	}
}

func TestIssueRequirementSaveFailureRollsBackFullState(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	a.store = &contactStore{path: t.TempDir()} // directory path makes save fail
	requirement := issue.Requirements[0]
	oldState, oldEvidence, oldRequirementUpdated := requirement.State, requirement.Evidence, requirement.UpdatedAt
	oldIssueUpdated := issue.UpdatedAt
	if a.setIssueRequirementState(issue.ID, requirement.ID, requirementSatisfied, "new evidence") {
		t.Fatal("requirement save unexpectedly succeeded")
	}
	if requirement.State != oldState || requirement.Evidence != oldEvidence ||
		requirement.UpdatedAt != oldRequirementUpdated || issue.UpdatedAt != oldIssueUpdated {
		t.Fatalf("failed requirement save left partial state: requirement=%+v issue.updated=%d", requirement, issue.UpdatedAt)
	}
}

func TestIssueEditClearsCompletionEvidenceButKeepsExecutionSnapshot(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue.Status = issueStatusInProgress
	issue.Evidence = "old completion proof"
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work", TaskID: "t1", Prompt: "old exact prompt", Status: issueExecutionCompleted, RequirementsRevision: 1}}
	if !a.editIssue(issue.ID, "new goal", "support SMS\nshow errors") {
		t.Fatal("goal edit failed")
	}
	if issue.Evidence != "" {
		t.Fatalf("old completion evidence survived edit: %q", issue.Evidence)
	}
	if issue.Executions[0].Prompt != "old exact prompt" || issue.Executions[0].RequirementsRevision != 1 {
		t.Fatalf("old execution snapshot changed: %+v", issue.Executions[0])
	}
}

func TestIssueTerminalLifecycleDoesNotAutoComplete(t *testing.T) {
	issue := &productIssue{ID: "i1", Status: issueStatusInProgress, Work: issueWorkSession{StopRequestedTasks: map[string]bool{}}}
	a := &appModel{}
	a.applyIssueTerminalLifecycle(issue, "t1", "completed")
	if issue.Status != issueStatusInProgress {
		t.Fatalf("completed execution changed issue lifecycle: %q", issue.Status)
	}
	a.applyIssueTerminalLifecycle(issue, "t2", "failed")
	if issue.Status != issueStatusNeedsAttention {
		t.Fatalf("failed execution status = %q", issue.Status)
	}
	issue.Status = issueStatusInProgress
	issue.Work.StopRequestedTasks["t3"] = true
	a.applyIssueTerminalLifecycle(issue, "t3", "interrupted")
	if issue.Status != issueStatusInProgress {
		t.Fatalf("user-stopped execution status = %q", issue.Status)
	}
	issue.Status = issueStatusInProgress
	a.applyIssueTerminalLifecycle(issue, "t4", "interrupted")
	if issue.Status != issueStatusNeedsAttention {
		t.Fatalf("external interrupted status = %q", issue.Status)
	}
	issue.Status = issueStatusCanceled
	a.applyIssueTerminalLifecycle(issue, "t5", "failed")
	if issue.Status != issueStatusCanceled {
		t.Fatalf("terminal event overwrote canceled issue: %q", issue.Status)
	}
}

func TestIssueUITargetsLifecycleActions(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "lifecycle", Status: issueStatusInProgress, RequirementsRevision: 1,
		Requirements: parseIssueRequirements("one requirement"),
		Executions:   []*issueExecution{{ID: "e1", SessionID: "chat", TaskID: "t1", Status: issueExecutionCompleted}}}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	a.rt = &runtimeClient{}
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	for _, want := range []string{"In progress", "Requirements", "Unverified", "Continue issue"} {
		if !tr.HasText(want) {
			t.Errorf("lifecycle default UI missing %q: %v", want, tr.Texts())
		}
	}
	expandIssueDetails(t, tr)
	for _, want := range []string{"Satisfied", "Unmet", "Unverified", "Mark waiting", "Cancel issue", "Archive", "Mark complete"} {
		if !tr.HasText(want) {
			t.Errorf("lifecycle details UI missing %q: %v", want, tr.Texts())
		}
	}
	if err := tr.Click("Mark waiting"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if issue.Status != issueStatusWaitingUser {
		t.Fatalf("Mark waiting status = %q", issue.Status)
	}
	if !tr.HasText("Waiting for you") || !tr.HasText("Resume") {
		t.Fatal("waiting state controls not visible")
	}
}

func TestIssueUIFilterArchiveAndRestore(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "archive me", Status: issueStatusInProgress, RequirementsRevision: 1}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	expandIssueDetails(t, tr)
	if err := tr.Click("Archive"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !issue.Archived || ct.issueNavigation().Filter != issueFilterUnfinished || !tr.HasText("Restore issue") || !tr.HasText("Archived") {
		t.Fatalf("archive UI = archived=%v filter=%q texts=%v", issue.Archived, ct.issueNavigation().Filter, tr.Texts())
	}
	if err := tr.Click("Restore issue"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if issue.Archived || ct.issueNavigation().Filter != issueFilterUnfinished {
		t.Fatalf("restore UI changed the current filter: archived=%v filter=%q", issue.Archived, ct.issueNavigation().Filter)
	}
}

func TestIssueUIEditPrefillsRequirementsAndSavesRevision(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "old goal", Status: issueStatusInProgress, RequirementsRevision: 1,
		Requirements: parseIssueRequirements("old requirement")}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	expandIssueDetails(t, tr)
	if err := tr.Click("Edit issue"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("Edit issue") || a.issueFormEditingID != issue.ID || a.issueGoal != "old goal" ||
		a.issueRequirements != "old requirement" {
		t.Fatalf("edit form prefill = %q / %q", a.issueGoal, a.issueRequirements)
	}
	a.issueGoal = "new goal"
	a.issueRequirements = "old requirement\nnew requirement"
	if err := tr.Click("Save issue"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if issue.Goal != "new goal" || issue.RequirementsRevision != 2 || len(issue.Requirements) != 2 ||
		issue.Evidence != "" || issue.Requirements[1].State != requirementUnverified {
		t.Fatalf("edited UI issue = %+v", issue)
	}
}

func TestIssueRuntimeStopContinueAndExplicitCompletion(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := newGatedProvider("life-second", "life-first")
	defer provider.release("life-first")
	defer provider.release("life-second")
	h := startRoutingHarnessWithHandler(t, binary, provider.handler(), 1)
	var issueID string
	var createOK, runOK, cancelOK, editOK, continueOK, completeOK, reopenOK bool
	var createErr, runErr, editErr, continueErr string
	h.pump.do(func() {
		createOK = h.app.createIssue("life-first", "first requirement")
		createErr = h.app.issueFormError
		if !createOK {
			return
		}
		issueID = h.app.selectedContact().SelectedIssueID
		runOK = h.app.runIssue(issueID)
		runErr = h.app.issueFormError
	})
	if !createOK {
		t.Fatalf("createIssue: %s", createErr)
	}
	if !runOK {
		t.Fatalf("first run: %s", runErr)
	}
	select {
	case <-provider.arrived["life-first"]:
	case <-time.After(15 * time.Second):
		t.Fatal("first run did not reach provider")
	}
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID != ""
	})
	h.pump.do(func() { cancelOK = h.app.cancelIssue(issueID) })
	if !cancelOK {
		t.Fatal("stop did not accept active issue")
	}
	// Keep the provider held until the real cancel terminal is visible. This
	// avoids the opposite-order race where the model completes before the
	// queued task.cancel reaches Runtime. The terminal itself is the proof;
	// a transient "Cancelling" label is not.
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionInterrupted &&
			h.app.canStartIssue(ct, issue) == ""
	})
	provider.release("life-first")
	var firstStatus, firstIssueStatus string
	h.snapshot(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		if issue != nil {
			firstIssueStatus = issue.Status
			if len(issue.Executions) == 1 {
				firstStatus = issue.Executions[0].Status
			}
		}
	})
	if firstStatus != issueExecutionInterrupted || firstIssueStatus != issueStatusInProgress || firstIssueStatus == issueStatusCanceled {
		t.Fatalf("stop was treated as completion/cancel: execution=%q issue=%q", firstStatus, firstIssueStatus)
	}
	h.pump.do(func() {
		editOK = h.app.editIssue(issueID, "life-second", "second requirement")
		editErr = h.app.issueFormError
		if !editOK {
			return
		}
		continueOK = h.app.runIssue(issueID)
		continueErr = h.app.issueFormError
	})
	if !editOK {
		t.Fatalf("edit before continue: %s", editErr)
	}
	if !continueOK {
		t.Fatalf("continue run: %s", continueErr)
	}
	select {
	case <-provider.arrived["life-second"]:
	case <-time.After(15 * time.Second):
		t.Fatal("continued run did not reach provider")
	}
	provider.release("life-second")
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 2 && issue.Executions[1].Status == issueExecutionCompleted
	})
	var continueIssueStatus string
	var sameSession bool
	var promptsEqual bool
	h.snapshot(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		if issue != nil {
			continueIssueStatus = issue.Status
			if len(issue.Executions) == 2 {
				sameSession = issue.Executions[0].SessionID == issue.Executions[1].SessionID
				promptsEqual = issue.Executions[0].Prompt == issue.Executions[1].Prompt
			}
		}
	})
	if continueIssueStatus != issueStatusInProgress || !sameSession {
		t.Fatalf("continue did not stay in the same issue/work session: issue=%q sameSession=%v", continueIssueStatus, sameSession)
	}
	if promptsEqual {
		t.Fatal("continued run did not preserve distinct sent snapshots")
	}
	h.pump.do(func() { completeOK = h.app.completeIssue(issueID, "user reviewed both runs") })
	if !completeOK {
		t.Fatal("explicit completion failed")
	}
	var completedStatus string
	h.snapshot(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		if issue != nil {
			completedStatus = issue.Status
		}
	})
	if completedStatus != issueStatusCompleted {
		t.Fatalf("explicit completion status = %q", completedStatus)
	}
	h.pump.do(func() { reopenOK = h.app.reopenIssue(issueID) })
	if !reopenOK {
		t.Fatal("reopen failed")
	}
	var reopenedStatus string
	h.snapshot(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		if issue != nil {
			reopenedStatus = issue.Status
		}
	})
	if reopenedStatus != issueStatusInProgress {
		t.Fatalf("reopen status = %q", reopenedStatus)
	}
	if provider.count() != 2 {
		t.Fatalf("unexpected retries: %d", provider.count())
	}
	h.shutdown(t)
}

func TestIssueEditSaveFailureRestoresCompleteVisibleState(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	a.store = &contactStore{path: t.TempDir()}
	issue.Evidence = "old completion proof"
	oldGoal, oldRequirements, oldRevision := issue.Goal, issue.Requirements, issue.RequirementsRevision
	oldEvidence, oldUpdated := issue.Evidence, issue.UpdatedAt
	if a.editIssue(issue.ID, "new goal", "new requirement") {
		t.Fatal("edit unexpectedly succeeded with a failing store")
	}
	if issue.Goal != oldGoal || issue.RequirementsRevision != oldRevision || issue.Evidence != oldEvidence ||
		issue.UpdatedAt != oldUpdated || len(issue.Requirements) != len(oldRequirements) {
		t.Fatalf("failed edit left partial state: %+v evidence=%q updated=%d", issue, issue.Evidence, issue.UpdatedAt)
	}
}

func TestArchivedActiveIssueStillBlocksApply(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "hidden active", Status: issueStatusInProgress, Archived: true, WorkSessionID: "work"}
	issue.Work.ActiveTaskID = "t1"
	issue.Work.SendState = sendAccepted
	ct.Issues = []*productIssue{issue}
	a.connectionSaved = &connectionConfig{}
	a.rt = &runtimeClient{}
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	reason := a.connectionReloadBlocked()
	if !strings.Contains(reason, "Wait for all conversation and issue work") {
		t.Fatalf("archived active issue was missed by Apply protection: %q", reason)
	}
}

func TestIssuePersistentActionsRollbackOnSaveFailure(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	a.store = &contactStore{path: t.TempDir()}
	issue.Status = issueStatusInProgress
	oldUpdated := issue.UpdatedAt
	if a.completeIssue(issue.ID, "evidence") || a.archiveIssue(issue.ID) {
		t.Fatal("save-failing actions unexpectedly succeeded")
	}
	if issue.Status != issueStatusInProgress || issue.Archived || issue.UpdatedAt != oldUpdated {
		t.Fatalf("failed actions left partial state: %+v", issue)
	}
}

func TestIssueUICompleteAndReopenActions(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "finish me", Status: issueStatusInProgress, RequirementsRevision: 1}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	expandIssueDetails(t, tr)
	completion := a.completionDraft(issue)
	*completion = "user verified the saved result"
	if err := tr.Click("Mark complete"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if issue.Status != issueStatusCompleted || issue.Evidence != "user verified the saved result" {
		t.Fatalf("UI completion = %q evidence=%q", issue.Status, issue.Evidence)
	}
	if !tr.HasText("Completion evidence") || !tr.HasText("user verified the saved result") {
		t.Fatalf("completion evidence is not readable after completion: %v", tr.Texts())
	}
	if err := tr.Click("Reopen issue"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if issue.Status != issueStatusNotStarted {
		t.Fatalf("UI reopen = %q", issue.Status)
	}
	if !tr.HasText("Previous completion evidence: user verified the saved result") {
		t.Fatalf("previous completion evidence is not readable after reopen: %v", tr.Texts())
	}
}

func TestIssueUIRequirementEvidenceActionPersists(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "goal", Status: issueStatusInProgress, RequirementsRevision: 1,
		Requirements: parseIssueRequirements("one checked requirement")}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	openIssueDetailForTest(a, ct, issue.ID)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	expandIssueDetails(t, tr)
	requirement := issue.Requirements[0]
	draft := a.requirementDraft(requirement)
	*draft = "checked by the user"
	clickAfterBoundedScroll(t, tr, "Satisfied", 1040, 720)
	tr.Frame()
	if requirement.State != requirementSatisfied || requirement.Evidence != "checked by the user" {
		t.Fatalf("requirement UI evidence = %+v", requirement)
	}
}

func TestIssueUIStatusFilterSelection(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "filtered issue", Status: issueStatusCompleted, RequirementsRevision: 1}
	ct.Issues = []*productIssue{issue}
	openIssueDetailForTest(a, ct, issue.ID)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Back to issues"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Unfinished"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Completed"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.issueNavigation().Filter != issueFilterCompleted {
		t.Fatalf("status filter = %q", ct.issueNavigation().Filter)
	}
	if !tr.HasText("filtered issue") || !tr.HasText("Completed") {
		t.Fatalf("completed filter did not list issue: %v", tr.Texts())
	}
}

func TestIssueContinuationCapturesRequirementsRevisionBeforeDispatch(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{}
	h := startGUIHarness(t, binary, provider, "issue-revision-117")
	defer func() {
		if !h.closed {
			h.shutdown(t)
		}
	}()
	var issueID string
	var setupErr error
	h.pump.do(func() {
		if !h.app.createIssue("first goal", "first requirement") {
			setupErr = fmt.Errorf("createIssue: %s", h.app.issueFormError)
			return
		}
		issueID = h.app.selectedContact().SelectedIssueID
		if !h.app.runIssue(issueID) {
			setupErr = fmt.Errorf("first run: %s", h.app.issueFormError)
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted &&
			h.app.canStartIssue(ct, issue) == ""
	})
	var first issueExecution
	h.pump.do(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		first = *issue.Executions[0]
	})
	if first.RequirementsRevision != 1 {
		t.Fatalf("first execution revision = %d, want 1", first.RequirementsRevision)
	}
	firstBody := taskContextLastUserText(t, provider.body(0))
	if !strings.Contains(firstBody, "first goal") || !strings.Contains(firstBody, "first requirement") || strings.Contains(firstBody, "second goal") {
		t.Fatalf("first provider input = %q", firstBody)
	}
	h.pump.do(func() {
		if !h.app.editIssue(issueID, "second goal", "second requirement") {
			setupErr = fmt.Errorf("edit issue: %s", h.app.issueFormError)
			return
		}
		if !h.app.runIssue(issueID) {
			setupErr = fmt.Errorf("continue run: %s", h.app.issueFormError)
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 2 && issue.Executions[1].Status == issueExecutionCompleted
	})
	var second issueExecution
	h.pump.do(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		second = *issue.Executions[1]
	})
	if second.RequirementsRevision != 2 {
		t.Fatalf("second execution revision = %d, want 2", second.RequirementsRevision)
	}
	secondBody := taskContextLastUserText(t, provider.body(1))
	if !strings.Contains(secondBody, "second goal") || !strings.Contains(secondBody, "second requirement") || strings.Contains(secondBody, "first goal") {
		t.Fatalf("second provider input = %q", secondBody)
	}
	if provider.count() != 2 {
		t.Fatalf("provider requests before restart = %d", provider.count())
	}
	h.shutdown(t)

	h2 := restartGUIHarness(t, binary)
	defer func() {
		if !h2.closed {
			h2.shutdown(t)
		}
	}()
	h2.pump.wait(func() bool {
		issue := h2.app.contacts[0].issueByID(issueID)
		return issue != nil && len(issue.Executions) == 2 && issue.Work.Loaded
	})
	var restartedFirst, restartedSecond issueExecution
	h2.pump.do(func() {
		issue := h2.app.selectedContact().issueByID(issueID)
		restartedFirst = *issue.Executions[0]
		restartedSecond = *issue.Executions[1]
	})
	if restartedFirst.RequirementsRevision != 1 || restartedSecond.RequirementsRevision != 2 {
		t.Fatalf("restarted revisions = %d/%d", restartedFirst.RequirementsRevision, restartedSecond.RequirementsRevision)
	}
	if restartedFirst.Prompt != first.Prompt || restartedFirst.SessionID != first.SessionID ||
		restartedFirst.TaskID != first.TaskID || restartedFirst.MessageID != first.MessageID {
		t.Fatalf("first execution identity changed: before=%+v after=%+v", first, restartedFirst)
	}
	if restartedSecond.Prompt != second.Prompt || restartedSecond.SessionID != second.SessionID ||
		restartedSecond.TaskID != second.TaskID || restartedSecond.MessageID != second.MessageID {
		t.Fatalf("second execution identity changed: before=%+v after=%+v", second, restartedSecond)
	}
	if provider.count() != 2 {
		t.Fatalf("restart resent a model request: %d", provider.count())
	}
	h2.shutdown(t)
}

func TestIssueRuntimeCompletedBeforeLateCancelStaysCompleted(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := newGatedProvider("late-cancel")
	h := startRoutingHarnessWithHandler(t, binary, provider.handler(), 1)
	defer func() {
		provider.release("late-cancel")
		if !h.closed {
			h.shutdown(t)
		}
	}()
	var issueID string
	var createOK, runOK bool
	var errText string
	h.pump.do(func() {
		createOK = h.app.createIssue("late-cancel", "complete before cancel is observed")
		errText = h.app.issueFormError
		if !createOK {
			return
		}
		issueID = h.app.selectedContact().SelectedIssueID
		runOK = h.app.runIssue(issueID)
		errText = h.app.issueFormError
	})
	if !createOK || !runOK {
		t.Fatalf("setup = create:%v run:%v err:%q", createOK, runOK, errText)
	}
	select {
	case <-provider.arrived["late-cancel"]:
	case <-time.After(15 * time.Second):
		t.Fatal("provider did not receive late-cancel task")
	}
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID != ""
	})
	provider.release("late-cancel")
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && len(issue.Executions) == 1 &&
			issue.Executions[0].Status == issueExecutionCompleted
	})
	var sessionID, taskID string
	var contactID string
	h.snapshot(func() {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		contactID = ct.ID
		if issue != nil && len(issue.Executions) == 1 {
			sessionID = issue.Executions[0].SessionID
			taskID = issue.Executions[0].TaskID
		}
	})
	if sessionID == "" || taskID == "" {
		t.Fatalf("completed execution has no trusted identity: session=%q task=%q", sessionID, taskID)
	}
	status, cancelErr := h.app.rt.cancelTask(sessionID, taskID)
	if cancelErr != nil {
		t.Fatalf("late task.cancel failed: %v", cancelErr)
	}
	if status != "not_running" {
		t.Fatalf("late task.cancel status = %q, want the Runtime terminal contract \"not_running\"", status)
	}
	h.pump.do(func() { h.app.applyIssueCancelResult(contactID, issueID, taskID, status, nil) })
	var completedStatus, activeTask string
	h.snapshot(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		if issue != nil {
			activeTask = issue.Work.ActiveTaskID
			if len(issue.Executions) == 1 {
				completedStatus = issue.Executions[0].Status
			}
		}
	})
	if completedStatus != issueExecutionCompleted || activeTask != "" {
		t.Fatalf("late cancel changed a completed task: status=%q active=%q", completedStatus, activeTask)
	}
}

func TestIssueSubmitFailureCallbackPendingBlocksShutdown(t *testing.T) {
	binary := requireRuntimeBinary(t)
	h := startGUIHarness(t, binary, &providerFixture{ToolFirst: false}, "shutdown-submit-failure")
	h.pump.wait(func() bool { return h.app.runtimeStatus == runtimeStateReady && h.app.runtimeCapabilitiesLoaded })
	callbacks := make(chan func(), 8)
	var issueID string
	var setupErr string
	var failure func()
	var storePath string
	var storePathSaved bool
	cleaned := false
	cleanup := func() {
		if cleaned {
			return
		}
		cleaned = true
		if storePathSaved {
			h.pump.do(func() { h.app.store.path = storePath })
			storePathSaved = false
		}
		h.pump.do(func() { h.app.setUIHook(func(fn func()) { h.pump.q <- fn }) })
		if failure != nil {
			callback := failure
			failure = nil
			h.pump.do(callback)
		}
	}
	defer func() {
		if !h.closed {
			cleanup()
		}
	}()
	h.pump.do(func() {
		h.app.setUIHook(func(fn func()) { callbacks <- fn })
		if !h.app.createIssue("shutdown submit failure", "Do not admit the task after the store failure.") {
			setupErr = "createIssue: " + h.app.issueFormError
			return
		}
		issueID = h.app.selectedContact().SelectedIssueID
		storePath = h.app.storePath
		if !h.app.runIssue(issueID) {
			setupErr = "runIssue: " + h.app.issueFormError
		}
	})
	if setupErr != "" {
		t.Fatal(setupErr)
	}
	var mapping func()
	select {
	case mapping = <-callbacks:
	case <-time.After(15 * time.Second):
		t.Fatal("issue submit mapping callback did not arrive")
	}
	h.pump.do(func() {
		storePath = h.app.store.path
		h.app.store.path = t.TempDir() // force the mapping save to fail
	})
	h.pump.do(mapping)
	select {
	case failure = <-callbacks:
	case <-time.After(15 * time.Second):
		t.Fatal("issue submit failure callback did not arrive")
	}
	var state sendState
	var active, pending string
	h.pump.do(func() {
		h.app.store.path = storePath
		issue := h.app.contactByID(h.app.contacts[0].ID).issueByID(issueID)
		state, active, pending = issue.Work.SendState, issue.Work.ActiveTaskID, issue.Work.pendingExecutionID
	})
	if state != sendSubmitting || active != "" || pending == "" {
		t.Fatalf("unexpected pending submit state: state=%v active=%q pending=%q", state, active, pending)
	}
	var quitCount int
	var status string
	var draining, ready bool
	var snapshotQuit int
	h.pump.do(func() {
		h.app.setUIHook(func(fn func()) { h.pump.q <- fn })
		h.app.requestQuit = func() { quitCount++ }
		h.app.beginShutdown()
		status = h.app.runtimeStatus
		draining, ready = h.app.shutdownDrainStarted, h.app.quitReady
		snapshotQuit = quitCount
	})
	t.Logf("pending-submit stage: state=%v active=%q pending=%q status=%q draining=%v quitReady=%v quitCount=%d",
		state, active, pending, status, draining, ready, snapshotQuit)
	early := draining || status == runtimeStateDraining || ready || snapshotQuit != 0
	if early {
		// This is the baseline bug: shutdown starts before the final submit
		// failure callback has run. Mark the failure immediately; the held
		// callback remains in `failure` for the controlled quit observation below.
		t.Errorf("shutdown began while issue submit failure callback was pending: status=%q draining=%v quitReady=%v quitCount=%d",
			status, draining, ready, snapshotQuit)
		h.pump.wait(func() bool { return h.app.quitReady })
		var finalStatus string
		var finalReady, stillHeld bool
		var finalQuit int
		h.pump.do(func() {
			finalStatus, finalReady = h.app.runtimeStatus, h.app.quitReady
			finalQuit = quitCount
			stillHeld = failure != nil
		})
		t.Logf("PREMATURE QUIT FACT: status=%q quitReady=%v quitCount=%d callbackStillHeld=%v",
			finalStatus, finalReady, finalQuit, stillHeld)
	}
	// Fixed behavior: the pending callback must be delivered before shutdown
	// can start. Exercise it and let the existing harness complete normally.
	cleanup()
	h.shutdown(t)
}

func TestIssueSubmitMappingHeldAllowsShutdownToWaitForAccept(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{ToolFirst: false}
	h := startGUIHarness(t, binary, provider, "shutdown-issue-accept")
	h.pump.wait(func() bool { return h.app.runtimeStatus == runtimeStateReady && h.app.runtimeCapabilitiesLoaded })
	callbacks := make(chan func(), 16)
	var issueID string
	var setupErr string
	var mapping func()
	var storePath string
	cleanup := func() {
		h.pump.do(func() { h.app.setUIHook(func(fn func()) { h.pump.q <- fn }) })
		if mapping != nil {
			callback := mapping
			mapping = nil
			h.pump.do(callback)
		}
	}
	defer func() {
		if !h.closed {
			cleanup()
		}
	}()
	h.pump.do(func() {
		h.app.setUIHook(func(fn func()) { callbacks <- fn })
		if !h.app.createIssue("shutdown issue accept", "Ship this after the final callback.") {
			setupErr = "createIssue: " + h.app.issueFormError
			return
		}
		issueID = h.app.selectedContact().SelectedIssueID
		storePath = h.app.storePath
		if !h.app.runIssue(issueID) {
			setupErr = "runIssue: " + h.app.issueFormError
		}
	})
	if setupErr != "" {
		t.Fatal(setupErr)
	}
	select {
	case mapping = <-callbacks:
	case <-time.After(15 * time.Second):
		t.Fatal("issue submit mapping callback did not arrive")
	}
	var state sendState
	var active string
	var pendingInFlight int
	h.pump.do(func() {
		issue := h.app.contactByID(h.app.contacts[0].ID).issueByID(issueID)
		state, active, pendingInFlight = issue.Work.SendState, issue.Work.ActiveTaskID, h.app.submissionInFlight
	})
	if state != sendSubmitting || active != "" || pendingInFlight != 1 {
		t.Fatalf("unexpected held mapping state: state=%v active=%q inFlight=%d", state, active, pendingInFlight)
	}
	var quitCount int
	var status string
	var draining, ready bool
	var snapshotQuit, snapshotInFlight int
	h.pump.do(func() {
		h.app.requestQuit = func() { quitCount++ }
		h.app.beginShutdown()
		h.app.beginShutdown()
		status = h.app.runtimeStatus
		draining, ready = h.app.shutdownDrainStarted, h.app.quitReady
		snapshotQuit = quitCount
		snapshotInFlight = h.app.submissionInFlight
	})
	if draining || status == runtimeStateDraining || ready || snapshotQuit != 0 || snapshotInFlight != 1 {
		t.Fatalf("shutdown began while issue mapping was held: status=%q draining=%v quitReady=%v quitCount=%d inFlight=%d",
			status, draining, ready, snapshotQuit, snapshotInFlight)
	}
	var lateErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "must be rejected after shutdown starts"
		if h.app.sendCurrent() {
			lateErr = "conversation submit was admitted after shutdown began"
		}
		if reason := h.app.canStartIssue(ct, ct.issueByID(issueID)); reason == "" {
			lateErr = "issue submit was admitted after shutdown began"
		}
	})
	if lateErr != "" {
		t.Fatal(lateErr)
	}
	mappingMissing := false
	h.pump.do(func() {
		if mapping == nil {
			mappingMissing = true
			return
		}
		callback := mapping
		mapping = nil
		h.app.setUIHook(func(fn func()) { h.pump.q <- fn })
		callback()
	})
	if mappingMissing {
		t.Fatal("issue mapping callback disappeared")
	}
	h.pump.wait(func() bool {
		issue := h.app.contactByID(h.app.contacts[0].ID).issueByID(issueID)
		return issue != nil && len(issue.Executions) == 1 && issue.Executions[0].TaskID != "" &&
			issue.Executions[0].MessageID != "" && issue.Executions[0].SessionID != ""
	})
	var acceptedSession, acceptedTask, acceptedMessage string
	h.pump.do(func() {
		issue := h.app.contactByID(h.app.contacts[0].ID).issueByID(issueID)
		execution := issue.Executions[0]
		acceptedSession, acceptedTask, acceptedMessage = execution.SessionID, execution.TaskID, execution.MessageID
	})
	saved, err := openContactStore(storePath, h.root)
	if err != nil {
		t.Fatal(err)
	}
	var savedIssue *storedIssue
	for i := range saved.state.Issues {
		if saved.state.Issues[i].ID == issueID {
			savedIssue = &saved.state.Issues[i]
		}
	}
	if savedIssue == nil || savedIssue.WorkSessionID != acceptedSession || len(savedIssue.Executions) != 1 ||
		savedIssue.Executions[0].TaskID != acceptedTask || savedIssue.Executions[0].MessageID != acceptedMessage {
		t.Fatalf("accepted issue association was not durable: issue=%+v", savedIssue)
	}
	h.pump.wait(func() bool { return h.app.quitReady })
	var finalStatus string
	var finalReady bool
	var finalQuit int
	h.pump.do(func() {
		finalStatus, finalReady = h.app.runtimeStatus, h.app.quitReady
		finalQuit = quitCount
	})
	if finalStatus != runtimeStateExited || !finalReady || finalQuit != 1 || provider.count() != 1 {
		t.Fatalf("accepted issue did not finish with one quit: status=%q ready=%v quitCount=%d provider=%d",
			finalStatus, finalReady, finalQuit, provider.count())
	}
	h.shutdown(t)
}

func TestIssueSubmitRefusalCallbackPendingBlocksShutdown(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{ToolFirst: false}
	h := startGUIHarness(t, binary, provider, "shutdown-issue-refusal")
	h.pump.wait(func() bool { return h.app.runtimeStatus == runtimeStateReady && h.app.runtimeCapabilitiesLoaded })
	callbacks := make(chan func(), 8)
	var issueID string
	var setupErr string
	var failure func()
	var storePath string
	cleanup := func() {
		h.pump.do(func() { h.app.setUIHook(func(fn func()) { h.pump.q <- fn }) })
		if failure != nil {
			callback := failure
			failure = nil
			h.pump.do(callback)
		}
	}
	defer func() {
		if !h.closed {
			cleanup()
		}
	}()
	h.pump.do(func() {
		h.app.setUIHook(func(fn func()) { callbacks <- fn })
		if !h.app.createIssue("missing session refusal", "The runtime must reject this task.") {
			setupErr = "createIssue: " + h.app.issueFormError
			return
		}
		ct := h.app.selectedContact()
		issueID = ct.SelectedIssueID
		issue := ct.issueByID(issueID)
		issue.WorkSessionID = "missing-session-for-refusal"
		issue.Work.Loaded = true
		storePath = h.app.storePath
		if err := h.app.persist(); err != nil {
			setupErr = "persist: " + err.Error()
			return
		}
		if !h.app.runIssue(issueID) {
			setupErr = "runIssue: " + h.app.issueFormError
		}
	})
	if setupErr != "" {
		t.Fatal(setupErr)
	}
	select {
	case failure = <-callbacks:
	case <-time.After(15 * time.Second):
		t.Fatal("issue submit refusal callback did not arrive")
	}
	var state sendState
	var active string
	var pendingInFlight int
	h.pump.do(func() {
		issue := h.app.contactByID(h.app.contacts[0].ID).issueByID(issueID)
		state, active, pendingInFlight = issue.Work.SendState, issue.Work.ActiveTaskID, h.app.submissionInFlight
	})
	if state != sendSubmitting || active != "" || pendingInFlight != 1 {
		t.Fatalf("unexpected pending refusal state: state=%v active=%q inFlight=%d", state, active, pendingInFlight)
	}
	var quitCount int
	var status string
	var snapshotQuit, snapshotInFlight int
	h.pump.do(func() {
		h.app.requestQuit = func() { quitCount++ }
		h.app.beginShutdown()
		status = h.app.runtimeStatus
		snapshotQuit, snapshotInFlight = quitCount, h.app.submissionInFlight
	})
	if status == runtimeStateDraining || snapshotQuit != 0 || snapshotInFlight != 1 {
		t.Fatalf("shutdown began while issue refusal callback was pending: status=%q quitCount=%d inFlight=%d",
			status, snapshotQuit, snapshotInFlight)
	}
	failureMissing := false
	h.pump.do(func() {
		if failure == nil {
			failureMissing = true
			return
		}
		callback := failure
		failure = nil
		h.app.setUIHook(func(fn func()) { h.pump.q <- fn })
		callback()
	})
	if failureMissing {
		t.Fatal("issue refusal callback disappeared")
	}
	var rejected bool
	var executionStatus string
	h.pump.do(func() {
		issue := h.app.contactByID(h.app.contacts[0].ID).issueByID(issueID)
		rejected = issue.Work.SendState == sendIdle && issue.Work.SendError != ""
		if len(issue.Executions) == 1 {
			executionStatus = issue.Executions[0].Status
		}
	})
	if !rejected || executionStatus != issueExecutionRejected {
		t.Fatalf("refusal callback did not persist rejection: rejected=%v execution=%q", rejected, executionStatus)
	}
	saved, err := openContactStore(storePath, h.root)
	if err != nil {
		t.Fatal(err)
	}
	var savedIssue *storedIssue
	for i := range saved.state.Issues {
		if saved.state.Issues[i].ID == issueID {
			savedIssue = &saved.state.Issues[i]
		}
	}
	if savedIssue == nil || len(savedIssue.Executions) != 1 || savedIssue.Executions[0].Status != issueExecutionRejected {
		t.Fatalf("refusal was not durable: issue=%+v", savedIssue)
	}
	h.pump.wait(func() bool { return h.app.quitReady })
	var finalStatus string
	var finalReady bool
	var finalQuit int
	h.pump.do(func() {
		finalStatus, finalReady = h.app.runtimeStatus, h.app.quitReady
		finalQuit = quitCount
	})
	if finalStatus != runtimeStateExited || !finalReady || finalQuit != 1 || provider.count() != 0 {
		t.Fatalf("refused submission did not finish with one quit: status=%q ready=%v quitCount=%d provider=%d",
			finalStatus, finalReady, finalQuit, provider.count())
	}
	h.shutdown(t)
}
