package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestIssueDetailDefaultsToResultAndKeepsSecondaryDetailsCollapsed(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.rt = &runtimeClient{}
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
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	for _, want := range []string{"Latest result", "readable result body", "Requirements", "Continue issue", "More issue actions"} {
		if !tr.HasText(want) {
			t.Fatalf("default issue detail missing %q: %v", want, tr.Texts())
		}
	}
	if tr.HasText("Execution history") || tr.HasText("Sent snapshot:") || tr.HasText("Sent task context") {
		t.Fatalf("secondary execution details visible by default: %v", tr.Texts())
	}
	goal, _ := tr.Find("prepare the readable summary")
	latest, _ := tr.Find("Latest result")
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
	a.selectContact(1)
	tr.Frame()
	if tr.HasText("Execution history") || a.issueDetailOpen[secondIssue.ID] {
		t.Fatalf("second contact inherited first edit/detail state: %v", tr.Texts())
	}
	a.selectContact(0)
	tr.Frame()
	if !a.issueDetailOpen[firstIssue.ID] || !tr.HasText("Hide issue actions") {
		t.Fatal("first issue details were lost after contact switch")
	}
}
