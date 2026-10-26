package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func lifecycleTestIssue() (*appModel, *contact, *productIssue) {
	ct := &contact{ID: "c1", Name: "Saki", Loaded: true}
	issue := &productIssue{
		ID: "i1", ContactID: "c1", Goal: "implement login", Status: issueStatusNotStarted,
		RequirementsRevision: 1,
		Requirements:         parseIssueRequirements("support SMS\nshow errors"),
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	a := &appModel{contacts: []*contact{ct}, selected: 0, scrollTo: -1, runtimeStatus: runtimeStateReady}
	return a, ct, issue
}

func TestIssueStatusAndRequirementLifecycleFacts(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	if issue.Status != issueStatusNotStarted || len(issue.Requirements) != 2 {
		t.Fatalf("initial issue = %+v", issue)
	}
	path := filepath.Join(t.TempDir(), "contacts.json")
	a.store = &contactStore{path: path}
	oldPrompt := "sent snapshot before edit"
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work", TaskID: "t1", Prompt: oldPrompt, Status: issueExecutionCompleted, RequirementsRevision: 1}}
	oldFirst := issue.Requirements[0]
	if !a.editIssue(issue.ID, "implement SMS login", "support SMS\nshow errors\nhandle expiry") {
		t.Fatal("edit failed")
	}
	if issue.Goal != "implement SMS login" || issue.RequirementsRevision != 2 || len(issue.Requirements) != 3 {
		t.Fatalf("edited issue = %+v", issue)
	}
	if issue.Requirements[0] != oldFirst || issue.Requirements[0].State != requirementUnverified {
		t.Fatal("unchanged requirement lost its identity/state")
	}
	if issue.Executions[0].Prompt != oldPrompt || issue.Executions[0].RequirementsRevision != 1 {
		t.Fatalf("old execution snapshot was rewritten: %+v", issue.Executions[0])
	}
	newRequirement := issue.Requirements[2]
	if newRequirement.State != requirementUnverified || newRequirement.Text != "handle expiry" {
		t.Fatalf("new requirement = %+v", newRequirement)
	}
	if !a.setIssueRequirementState(issue.ID, newRequirement.ID, requirementSatisfied, "verified by test") {
		t.Fatal("requirement state did not save")
	}
	if newRequirement.State != requirementSatisfied || newRequirement.Evidence != "verified by test" {
		t.Fatalf("requirement evidence = %+v", newRequirement)
	}
}

func TestIssueCompletedExecutionDoesNotCompleteIssue(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	issue.Status = issueStatusInProgress
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work", TaskID: "t1", Status: issueExecutionCompleted}}
	if issue.Status != issueStatusInProgress {
		t.Fatal("fixture status")
	}
	if !a.completeIssue(issue.ID, "user checked the delivered result") || issue.Status != issueStatusCompleted {
		t.Fatalf("explicit completion = %+v", issue)
	}
	if !strings.Contains(issueEvidenceText(issue), "user checked") {
		t.Fatalf("completion evidence = %q", issueEvidenceText(issue))
	}
}

func TestIssueStopAndCancelAreDifferentActions(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	issue.Status = issueStatusInProgress
	issue.Work.ActiveTaskID = "t1"
	issue.Work.SendState = sendAccepted
	if a.cancelIssueProduct(issue.ID) {
		t.Fatal("cancel product succeeded without a Runtime for active work")
	}
	issue.Work.ActiveTaskID = ""
	issue.Work.SendState = sendIdle
	if !a.cancelIssueProduct(issue.ID) || issue.Status != issueStatusCanceled {
		t.Fatalf("cancel issue = %+v", issue)
	}
	if issue.Work.ActiveTaskID != "" {
		t.Fatal("cancel invented an active work state")
	}
	if !a.reopenIssue(issue.ID) || issue.Status != issueStatusNotStarted {
		t.Fatalf("reopen canceled issue = %+v", issue)
	}
}

func TestIssueArchiveKeepsActiveWorkAndRestore(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	issue.Status = issueStatusInProgress
	issue.WorkSessionID = "work-i1"
	issue.Work.ActiveTaskID = "t1"
	issue.Work.SendState = sendAccepted
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work-i1", TaskID: "t1", Status: issueExecutionRunning}}
	if !a.archiveIssue(issue.ID) {
		t.Fatal("archive failed")
	}
	if !issue.Archived || issue.Work.ActiveTaskID != "t1" || issue.Status != issueStatusInProgress ||
		issue.WorkSessionID != "work-i1" {
		t.Fatalf("archive lost running identity: %+v", issue)
	}
	if !a.restoreIssue(issue.ID) || issue.Archived {
		t.Fatal("restore failed")
	}
	if issue.Work.ActiveTaskID != "t1" || issue.WorkSessionID != "work-i1" {
		t.Fatal("restore lost running identity")
	}
}

func TestIssueRequirementEditOnlyInvalidatesChangedText(t *testing.T) {
	a, _, issue := lifecycleTestIssue()
	issue.Status = issueStatusInProgress
	first := issue.Requirements[0]
	second := issue.Requirements[1]
	first.State, first.Evidence = requirementSatisfied, "old proof"
	second.State, second.Evidence = requirementUnverified, ""
	if !a.editIssue(issue.ID, issue.Goal, "support SMS\nshow better errors") {
		t.Fatal("edit failed")
	}
	if issue.Requirements[0] != first || first.State != requirementSatisfied || first.Evidence != "old proof" {
		t.Fatalf("unchanged requirement evidence changed: %+v", first)
	}
	if issue.Requirements[1] == second || issue.Requirements[1].State != requirementUnverified || issue.Requirements[1].Evidence != "" {
		t.Fatalf("changed requirement kept old evidence: %+v", issue.Requirements[1])
	}
}

func TestIssueActionsRejectArchivedAndCompletedStart(t *testing.T) {
	_, _, issue := lifecycleTestIssue()
	issue.Archived = true
	if reason := issueLifecycleStartBlocked(issue); !strings.Contains(reason, "Restore") {
		t.Fatalf("archived start guard = %q", reason)
	}
	issue.Archived = false
	issue.Status = issueStatusCompleted
	if reason := issueLifecycleStartBlocked(issue); !strings.Contains(reason, "Reopen") {
		t.Fatalf("completed start guard = %q", reason)
	}
}

func TestLegacyRequirementsBecomeStructuredAtStoreBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	legacy := storedState{
		Version: 3, Selected: "c1",
		Contacts: []storedContact{{ID: "c1", Name: "Saki"}},
		Issues: []storedIssue{{
			ID: "i1", ContactID: "c1", Goal: "legacy", Requirements: "old one\nold two", Status: "open",
			Executions: []storedIssueExecution{{ID: "e1", SessionID: "work", TaskID: "t1", Prompt: "old prompt", Status: issueExecutionCompleted}},
		}},
	}
	if err := writeStoredState(path, legacy); err != nil {
		t.Fatal(err)
	}
	store, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	issues := issuesForStoredContact(store.state.Issues, "c1")
	issue := issues[0]
	if len(issue.Requirements) != 2 || issue.Requirements[0].Text != "old one" ||
		issue.Requirements[1].State != requirementUnverified || issue.Status != issueStatusInProgress {
		t.Fatalf("legacy migration = %+v", issue)
	}
	ct := &contact{ID: "c1", Name: "Saki", Issues: issues}
	ct.SelectedIssueID = issue.ID
	if err := store.save([]*contact{ct}, "c1"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Version != 4 || reloaded.state.Issues[0].Requirements != "" ||
		len(reloaded.state.Issues[0].RequirementItems) != 2 ||
		reloaded.state.Issues[0].Executions[0].Prompt != "old prompt" {
		t.Fatalf("v4 migration boundary = %+v", reloaded.state.Issues[0])
	}
}

func writeStoredState(path string, state storedState) error {
	store := &contactStore{path: path, state: state}
	return store.saveState()
}
