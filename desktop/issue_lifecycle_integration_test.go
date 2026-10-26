package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

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
	a.rt = &runtimeClient{}
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	for _, want := range []string{"In progress", "Requirements · revision 1", "Satisfied", "Unmet", "Unverified", "Continue issue", "Mark waiting", "Cancel issue", "Archive", "Mark complete"} {
		if !tr.HasText(want) {
			t.Errorf("lifecycle UI missing %q: %v", want, tr.Texts())
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
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Archive"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !issue.Archived || a.issueFilter != "archived" || !tr.HasText("Restore issue") || !tr.HasText("Archived") {
		t.Fatalf("archive UI = archived=%v filter=%q texts=%v", issue.Archived, a.issueFilter, tr.Texts())
	}
	if err := tr.Click("Restore issue"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if issue.Archived || a.issueFilter != "active" {
		t.Fatalf("restore UI did not return active: archived=%v filter=%q", issue.Archived, a.issueFilter)
	}
}

func TestIssueUIEditPrefillsRequirementsAndSavesRevision(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "old goal", Status: issueStatusInProgress, RequirementsRevision: 1,
		Requirements: parseIssueRequirements("old requirement")}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
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
	h.pump.do(func() {
		if !h.app.createIssue("life-first", "first requirement") {
			t.Fatalf("createIssue: %s", h.app.issueFormError)
		}
		issueID = h.app.selectedContact().SelectedIssueID
		if !h.app.runIssue(issueID) {
			t.Fatalf("first run: %s", h.app.issueFormError)
		}
	})
	select {
	case <-provider.arrived["life-first"]:
	case <-time.After(15 * time.Second):
		t.Fatal("first run did not reach provider")
	}
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID != ""
	})
	h.pump.do(func() {
		if !h.app.cancelIssue(issueID) {
			t.Fatal("stop did not accept active issue")
		}
	})
	provider.release("life-first")
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionInterrupted
	})
	first := h.app.selectedContact().issueByID(issueID)
	if first.Status != issueStatusInProgress || first.Status == issueStatusCanceled {
		t.Fatalf("stop was treated as issue completion/cancel: %q", first.Status)
	}
	h.pump.do(func() {
		if !h.app.editIssue(issueID, "life-second", "second requirement") {
			t.Fatalf("edit before continue: %s", h.app.issueFormError)
		}
		if !h.app.runIssue(issueID) {
			t.Fatalf("continue run: %s", h.app.issueFormError)
		}
	})
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
	issue := h.app.selectedContact().issueByID(issueID)
	if issue.Status != issueStatusInProgress || issue.Executions[0].SessionID != issue.Executions[1].SessionID {
		t.Fatalf("continue did not stay in the same issue/work session: %+v / %+v", issue, issue.Executions)
	}
	if issue.Executions[0].Prompt == issue.Executions[1].Prompt {
		t.Fatal("continued run did not preserve distinct sent snapshots")
	}
	h.pump.do(func() {
		if !h.app.completeIssue(issueID, "user reviewed both runs") {
			t.Fatal("explicit completion failed")
		}
	})
	if issue.Status != issueStatusCompleted {
		t.Fatalf("explicit completion status = %q", issue.Status)
	}
	h.pump.do(func() {
		if !h.app.reopenIssue(issueID) {
			t.Fatal("reopen failed")
		}
	})
	if issue.Status != issueStatusInProgress {
		t.Fatalf("reopen status = %q", issue.Status)
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
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Mark complete"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if issue.Status != issueStatusCompleted {
		t.Fatalf("UI completion = %q", issue.Status)
	}
	if err := tr.Click("Reopen issue"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if issue.Status != issueStatusNotStarted {
		t.Fatalf("UI reopen = %q", issue.Status)
	}
}

func TestIssueUIRequirementEvidenceActionPersists(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "goal", Status: issueStatusInProgress, RequirementsRevision: 1,
		Requirements: parseIssueRequirements("one checked requirement")}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	requirement := issue.Requirements[0]
	draft := a.requirementDraft(requirement)
	*draft = "checked by the user"
	if err := tr.Click("Satisfied"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if requirement.State != requirementSatisfied || requirement.Evidence != "checked by the user" {
		t.Fatalf("requirement UI evidence = %+v", requirement)
	}
}
