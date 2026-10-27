package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestIssueDetailDefaultsToResultAndKeepsSecondaryDetailsCollapsed(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.rt = &runtimeClient{}
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	a.runtimeCapabilitiesLoaded = true
	a.runtimeMaxActiveTasks = 2
	issue := &productIssue{
		ID: "i1", ContactID: ct.ID, Goal: "prepare the readable summary", Status: issueStatusInProgress,
		RequirementsRevision: 1, Requirements: parseIssueRequirements("keep the source facts"),
		WorkSessionID: "work-1", Work: issueWorkSession{Loaded: true},
		Executions: []*issueExecution{{
			ID: "e1", SessionID: "work-1", TaskID: "task-1", MessageID: "message-1",
			Prompt: "the exact sent snapshot", Status: issueExecutionCompleted, RequirementsRevision: 1,
		}},
	}
	issue.Work.Messages = []*message{{Key: "result", Kind: msgContact, TaskID: "task-1", Text: "readable result body"}}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	storeBefore, err := os.ReadFile(a.store.path)
	if err != nil {
		t.Fatal(err)
	}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	for _, want := range []string{"Latest execution", "readable result body", "Requirements", "Continue issue", "More issue actions"} {
		if !tr.HasText(want) {
			t.Fatalf("default issue detail missing %q: %v", want, tr.Texts())
		}
	}
	if tr.HasText("Execution history") || tr.HasText("Sent snapshot:") || tr.HasText("Sent task context") {
		t.Fatalf("secondary execution details visible by default: %v", tr.Texts())
	}
	goal, _ := tr.Find("prepare the readable summary")
	latest, _ := tr.Find("Latest execution")
	requirements, _ := tr.Find("Requirements")
	primary, _ := tr.Find("Continue issue")
	more, _ := tr.Find("More issue actions")
	if !(goal.Y < latest.Y && latest.Y < requirements.Y && requirements.Y < primary.Y && primary.Y < more.Y) {
		t.Fatalf("default issue detail order = goal %.1f latest %.1f requirements %.1f primary %.1f more %.1f", goal.Y, latest.Y, requirements.Y, primary.Y, more.Y)
	}
	if err := tr.Click("More issue actions"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("Execution history") || !tr.HasText("Show sent snapshot") {
		t.Fatalf("expanded issue detail did not expose execution details: %v", tr.Texts())
	}
	if err := tr.Click("Show sent snapshot"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("Sent snapshot: the exact sent snapshot") {
		t.Fatalf("sent snapshot was not readable after explicit expansion: %v", tr.Texts())
	}
	storeAfter, err := os.ReadFile(a.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(storeAfter) != string(storeBefore) {
		t.Fatal("browsing/collapsing issue detail changed the contact store")
	}
	if issue.Status != issueStatusInProgress || len(issue.Executions) != 1 || issue.Executions[0].Status != issueExecutionCompleted {
		t.Fatalf("viewing issue detail changed lifecycle: %+v", issue)
	}
}

func TestIssueDetailsOpenStateDoesNotCrossContacts(t *testing.T) {
	a, first := workspaceTestApp(t)
	firstIssue := &productIssue{ID: "i-a", ContactID: first.ID, Goal: "first", Status: issueStatusInProgress, RequirementsRevision: 1,
		Requirements: parseIssueRequirements("first requirement")}
	first.Issues = []*productIssue{firstIssue}
	first.SelectedIssueID = firstIssue.ID
	second := &contact{ID: "mio", Name: "Mio", Role: "Assistant", Loaded: true}
	secondIssue := &productIssue{ID: "i-b", ContactID: second.ID, Goal: "second", Status: issueStatusInProgress, RequirementsRevision: 1,
		Requirements: parseIssueRequirements("second requirement")}
	second.Issues = []*productIssue{secondIssue}
	second.SelectedIssueID = secondIssue.ID
	a.contacts = append(a.contacts, second)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	expandIssueDetails(t, tr)
	if !a.issueDetailOpen[firstIssue.ID] {
		t.Fatal("first issue details did not expand")
	}
	firstDraft := a.requirementDraft(firstIssue.Requirements[0])
	*firstDraft = "first contact evidence draft"
	a.selectContact(1)
	tr.Frame()
	if tr.HasText("Execution history") || a.issueDetailOpen[secondIssue.ID] || tr.HasText("first contact evidence draft") {
		t.Fatalf("second contact inherited first edit/detail state: %v", tr.Texts())
	}
	a.selectContact(0)
	tr.Frame()
	if !a.issueDetailOpen[firstIssue.ID] || !tr.HasText("Hide issue actions") ||
		firstDraft != a.requirementDraft(firstIssue.Requirements[0]) || *firstDraft != "first contact evidence draft" {
		t.Fatal("first issue details/draft were lost after contact switch")
	}
}

func TestIssueViewExecutionOpensAtTopAndBackRestoresCompact(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.rt = &runtimeClient{}
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	a.runtimeCapabilitiesLoaded = true
	a.runtimeMaxActiveTasks = 2
	issue := &productIssue{ID: "i-view", ContactID: ct.ID, Goal: "view execution", Status: issueStatusInProgress,
		RequirementsRevision: 1, Requirements: parseIssueRequirements("result requirement"), WorkSessionID: "work-view",
		Work:       issueWorkSession{Loaded: true},
		Executions: []*issueExecution{{ID: "e-view", SessionID: "work-view", TaskID: "task-view", Prompt: "sent view prompt", Status: issueExecutionCompleted, RequirementsRevision: 1}},
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	ct.Works = []*workItem{{TaskID: "task-view", MessageID: "message-view", State: workCompleted, Prompt: "view work", Tools: []*workTool{{CallID: "call-view", Name: "write", Output: `{"content":"tool raw output"}`}}}}
	issue.Work.Messages = []*message{{Key: "result-view", Kind: msgContact, TaskID: "task-view", Text: "view result"}}
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(a.store.path)
	if err != nil {
		t.Fatal(err)
	}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if tr.HasText("Back to issue") || tr.HasText("Execution history") {
		t.Fatalf("execution detail visible before explicit View execution: %v", tr.Texts())
	}
	if err := tr.Click("View execution"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.SelectedWorkTaskID != "task-view" || !tr.HasText("Back to issue") || !tr.HasText("view result") || !tr.HasText("tool raw output") {
		t.Fatalf("View execution did not open the selected detail: %+v texts=%v", ct.SelectedWorkTaskID, tr.Texts())
	}
	back, ok := tr.Find("Back to issue")
	if !ok || back.Y > 720 {
		t.Fatalf("Back to issue is not reachable: %+v ok=%v", back, ok)
	}
	if err := tr.Click("Back to issue"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.SelectedWorkTaskID != "" || tr.HasText("Back to issue") || tr.HasText("Execution history") {
		t.Fatalf("Back to issue did not restore compact detail: selected=%q texts=%v", ct.SelectedWorkTaskID, tr.Texts())
	}
	after, err := os.ReadFile(a.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("View execution/back browsing changed the contact store")
	}
	if len(issue.Executions) != 1 || issue.Executions[0].Status != issueExecutionCompleted {
		t.Fatalf("View execution changed execution state: %+v", issue.Executions)
	}
}

func TestActiveIssueWorkKeepsStopReachableBeforeRequirements(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.rt = &runtimeClient{}
	a.runtimeCapabilitiesLoaded = true
	a.runtimeMaxActiveTasks = 2
	issue := &productIssue{ID: "i-active", ContactID: ct.ID, Goal: "active work", Status: issueStatusInProgress,
		RequirementsRevision: 1, Requirements: parseIssueRequirements("one\n two\nthree"), WorkSessionID: "work-active",
		Work:       issueWorkSession{Loaded: true, ActiveTaskID: "task-active", SendState: sendAccepted},
		Executions: []*issueExecution{{ID: "e-active", SessionID: "work-active", TaskID: "task-active", Status: issueExecutionRunning}},
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	ct.Works = []*workItem{{TaskID: "task-active", State: workRunning, Prompt: "active prompt"}}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	stop, ok := tr.Find("Stop issue task")
	requirements, reqOK := tr.Find("Requirements")
	if !ok || !reqOK || !tr.HasText("Stop issue task") || stop.Y >= requirements.Y {
		t.Fatalf("active Stop is not reachable before requirements: stop=%+v ok=%v requirements=%+v ok=%v", stop, ok, requirements, reqOK)
	}
}

func clickAfterBoundedScroll(t *testing.T, tr *ui.Tester, label string, width, height int) {
	t.Helper()
	for attempt := 0; attempt < 12; attempt++ {
		if r, ok := tr.Find(label); ok && r.W > 0 && r.H > 0 && r.X >= 0 && r.X+r.W <= float32(width) && r.Y >= 0 && r.Y+r.H <= float32(height) {
			if err := tr.Click(label); err != nil {
				t.Fatal(err)
			}
			return
		}
		tr.Scroll(850, 500, 0, 300)
		tr.Frame()
	}
	t.Fatalf("could not reach %q within the bounded scroll area", label)
}

func TestIssueSupplementAt720ScrollsToSaveAndKeepsOwnerDraft(t *testing.T) {
	a, ct := taskContextApp(t)
	if !a.createIssue("720 supplement goal", "720 supplement requirement") {
		t.Fatal(a.issueFormError)
	}
	issue := ct.selectedIssue()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("More issue actions"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Add supplement"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	*a.issueSupplementDraft(issue.ID) = "720 visible supplement"
	clickAfterBoundedScroll(t, tr, "Save supplement", 1040, 720)
	tr.Frame()
	if issue.ContactID != ct.ID || a.selectedContact() != ct || issue.Supplements.pendingCount() != 1 ||
		a.issueSupplementFormID != "" || *a.issueSupplementDraft(issue.ID) != "" {
		t.Fatalf("720 supplement save = owner=%q selected=%q pending=%d form=%q draft=%q",
			issue.ContactID, a.selectedContact().ID, issue.Supplements.pendingCount(), a.issueSupplementFormID, *a.issueSupplementDraft(issue.ID))
	}
}

func TestIssueExecutionContextExpandsAndCopiesAt720(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.store = &contactStore{path: filepath.Join(t.TempDir(), "contacts.json")}
	issue := &productIssue{ID: "i-ctx", ContactID: ct.ID, Goal: "context at 720", Status: issueStatusInProgress,
		RequirementsRevision: 1, WorkSessionID: "work-ctx", Work: issueWorkSession{Loaded: true},
		Executions: []*issueExecution{{ID: "e-ctx", SessionID: "work-ctx", TaskID: "task-ctx", Status: issueExecutionCompleted,
			Context: []taskContextItem{{ID: "ctx", Name: "fixture", Text: "exact context text", Revision: 1}}}},
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("More issue actions"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	clickAfterBoundedScroll(t, tr, "Sent task context · 1", 1040, 720)
	tr.Frame()
	if !tr.HasText("exact context text") {
		t.Fatalf("folded snapshot context did not expand: %v", tr.Texts())
	}
	tr.SetClipboard("")
	clickAfterBoundedScroll(t, tr, "Copy context text", 1040, 720)
	if got := tr.Clipboard(); got != "exact context text" {
		t.Fatalf("720 context copy = %q", got)
	}
}

func TestIssueLatestExecutionShowsNonSuccessStatesAndToolOnly(t *testing.T) {
	cases := []struct {
		status string
		label  string
		detail string
	}{
		{issueExecutionPending, "Prepared, not dispatched", ""},
		{issueExecutionRejected, "Rejected", "Runtime rejected the task"},
		{issueExecutionFailed, "Execution failed", "provider failed after dispatch"},
		{issueExecutionUnknown, "Result unknown", "no trusted terminal state"},
		{issueExecutionInterrupted, "Execution interrupted", ""},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			a, ct := workspaceTestApp(t)
			issue := &productIssue{ID: "i-" + tc.status, ContactID: ct.ID, Goal: "latest status", Status: issueStatusInProgress,
				RequirementsRevision: 1, Requirements: parseIssueRequirements("remain visible"), WorkSessionID: "work-latest",
				Executions: []*issueExecution{
					{ID: "old", SessionID: "work-latest", TaskID: "old-task", Status: issueExecutionCompleted},
					{ID: "latest", SessionID: "work-latest", TaskID: "latest-task", Status: tc.status, Detail: tc.detail},
				},
			}
			ct.Issues = []*productIssue{issue}
			ct.SelectedIssueID = issue.ID
			ct.Works = []*workItem{{TaskID: "old-task", State: workCompleted}}
			issue.Work.Messages = []*message{{Key: "old-success", Kind: msgContact, TaskID: "old-task", Text: "old success"}}
			tr := ui.NewTester(a.view, 1040, 720)
			tr.Frame()
			if !tr.HasText("Latest execution") || !tr.HasText(tc.label) {
				t.Fatalf("latest status %q not visible: %v", tc.label, tr.Texts())
			}
			if tc.detail != "" && !tr.HasText(tc.detail) {
				t.Fatalf("latest reason %q not visible: %v", tc.detail, tr.Texts())
			}
			if tr.HasText("old success") {
				t.Fatal("older successful execution was presented as latest")
			}
			if tc.status == issueExecutionPending && !tr.HasText("Prepared but not dispatched yet.") {
				t.Fatalf("pending execution lacks actionable short state: %v", tr.Texts())
			}
			if tc.status == issueExecutionInterrupted && (!tr.HasText("Execution interrupted; inspect the execution details for the recorded terminal state.") ||
				tr.HasText("terminal state is unknown")) {
				t.Fatalf("interrupted execution was described as unknown: %v", tr.Texts())
			}
			tr.Click("More issue actions")
			tr.Frame()
			if tr.HasText("Show sent snapshot") {
				t.Fatal("empty sent snapshot rendered a dead action")
			}
		})
	}

	a, ct := workspaceTestApp(t)
	issue := &productIssue{ID: "i-tool-only", ContactID: ct.ID, Goal: "tool only", Status: issueStatusInProgress,
		RequirementsRevision: 1, WorkSessionID: "work-tool-only", Work: issueWorkSession{Loaded: true},
		Executions: []*issueExecution{{ID: "tool", SessionID: "work-tool-only", TaskID: "tool-task", Status: issueExecutionCompleted}},
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	ct.Works = []*workItem{{TaskID: "tool-task", State: workCompleted, Tools: []*workTool{{CallID: "tool", Name: "write", Finished: true, Output: `{"bytes_written":1}`}}}}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("No assistant message was recorded; open the execution to inspect tool output.") || !tr.HasText("View execution") {
		t.Fatalf("tool-only execution lacks honest guidance: %v", tr.Texts())
	}
	if tr.HasText("tool raw output") || tr.HasText(`"bytes_written"`) {
		t.Fatal("tool-only execution leaked raw output into default result")
	}
}
