package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestIssueWorkCardAndComposerStopLabelsAreSeparate(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Messages = nil
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "work A", Status: issueStatusNotStarted, WorkSessionID: "work-session"}
	issue.Work.ActiveTaskID = "task-A"
	issue.Work.SendState = sendAccepted
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work-session", TaskID: "task-A", Status: issueExecutionRunning}}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	ct.ActiveTaskID = "task-B"
	ct.SendState = sendAccepted
	ct.Works = []*workItem{
		{TaskID: "task-A", State: workRunning, Prompt: "work A"},
		{TaskID: "task-B", State: workRunning, Prompt: "conversation B"},
	}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Stop issue task") || !tr.HasText("Stop conversation") {
		t.Fatalf("separate stop labels missing: %v", tr.Texts())
	}
	if !tr.HasText("Current issue run") {
		t.Fatal("issue work was not presented as current issue work")
	}
}

func TestIssueResultUsesIssueWorkMessages(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Messages = nil
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "work A", Status: issueStatusNotStarted, WorkSessionID: "work-session"}
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work-session", TaskID: "task-A", Status: issueExecutionCompleted}}
	issue.Work.Messages = []*message{{Key: "work-live", Kind: msgContact, TaskID: "task-A", Text: "issue-owned result"}}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	ct.Works = []*workItem{{TaskID: "task-A", State: workCompleted}}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("issue-owned result") {
		t.Fatalf("issue work result missing from issue UI: %v", tr.Texts())
	}
}

func TestRailShowsIssueWorkAttentionWithoutChatUnread(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "failed work", Status: issueStatusNotStarted}
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work-session", TaskID: "task-A", Status: issueExecutionFailed, Detail: "provider failed"}}
	ct.Issues = []*productIssue{issue}
	ct.Unread = 2
	ct.Messages = nil
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Attention") {
		t.Fatalf("rail did not surface issue-work failure: %v", tr.Texts())
	}
	if ct.Unread != 2 {
		t.Fatalf("issue-work notice changed chat unread: %d", ct.Unread)
	}
}

func TestIssueRecoveryControlsRenderForUnknownExecution(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "recover me", Status: issueStatusNotStarted}
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work-session", Status: issueExecutionUnknown, Detail: "unknown"}}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	expandIssueDetails(t, tr)
	if !tr.HasText("Check execution") || !tr.HasText("Confirm not dispatched") {
		t.Fatalf("recovery controls missing: %v", tr.Texts())
	}
}

func TestCapacityUnknownNoticeIsDistinctFromRealSingleSlot(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.runtimeCapabilitiesLoaded = false
	a.runtimeCapabilitiesErr = ""
	ct.ActiveTaskID = ""
	ct.SendState = sendIdle
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("capacity is not loaded") || !tr.HasText("Retry capacity") {
		t.Fatalf("unknown capacity was not disclosed: %v", tr.Texts())
	}
	a.runtimeCapabilitiesLoaded = true
	a.runtimeMaxActiveTasks = 1
	tr.Frame()
	if tr.HasText("capacity is not loaded") || strings.Contains(strings.Join(tr.Texts(), "\n"), "Retry capacity") {
		t.Fatalf("real single-slot capacity still shows unknown state: %v", tr.Texts())
	}
}

func TestRailShowsActiveIssueWorkCount(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "active work", Status: issueStatusNotStarted, WorkSessionID: "work-session"}
	issue.Work.ActiveTaskID = "task-A"
	issue.Work.SendState = sendAccepted
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work-session", TaskID: "task-A", Status: issueExecutionRunning}}
	ct.Issues = []*productIssue{issue}
	ct.Messages = nil
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("1 work") {
		t.Fatalf("rail did not show active work count: %v", tr.Texts())
	}
	if ct.Unread != 0 {
		t.Fatalf("active work count changed chat unread: %d", ct.Unread)
	}
}

func TestIssueConfirmNotDispatchedTargetsSelectedExecution(t *testing.T) {
	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "recover me", Status: issueStatusNotStarted}
	target := &issueExecution{ID: "e1", SessionID: "work-session", Status: issueExecutionUnknown, Prompt: "target"}
	other := &issueExecution{ID: "e2", SessionID: "other-session", TaskID: "task-other", Status: issueExecutionCompleted, Prompt: "other"}
	issue.Executions = []*issueExecution{target, other}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	expandIssueDetails(t, tr)
	if err := tr.Click("Confirm not dispatched"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if target.Status != issueExecutionNotDispatched {
		t.Fatalf("target execution = %+v", target)
	}
	if other.Status != issueExecutionCompleted || other.TaskID != "task-other" {
		t.Fatalf("confirm changed another execution: %+v", other)
	}
}
