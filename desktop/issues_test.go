package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestIssueStoreRoundTripAndV1Compatibility(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contacts.json")
	legacy := storedState{
		Version:  1,
		Selected: "contact-old",
		Contacts: []storedContact{{
			ID: "contact-old", Name: "Saki", Role: "Assistant",
			SessionID: "session-old", Draft: "preserved draft",
		}},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if len(store.state.Contacts) != 1 || store.state.Contacts[0].Draft != "preserved draft" ||
		store.state.Contacts[0].SessionID != "session-old" {
		t.Fatalf("v1 contact was not preserved: %+v", store.state.Contacts)
	}
	if len(store.state.Issues) != 0 {
		t.Fatalf("v1 store unexpectedly gained issues: %+v", store.state.Issues)
	}

	a := &appModel{store: store, contacts: []*contact{{
		ID: "contact-old", Name: "Saki", Workspace: "/tmp", SessionID: "session-old", Draft: "preserved draft",
	}}}
	if !a.createIssue("Implement login", "Use SMS verification.") {
		t.Fatalf("createIssue failed: %s", a.issueFormError)
	}
	if len(a.contacts[0].Issues) != 1 || a.contacts[0].SelectedIssueID == "" {
		t.Fatalf("issue not attached/selected: %+v", a.contacts[0])
	}
	reloaded, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Version != contactStoreVersion {
		t.Fatalf("saved version = %d", reloaded.state.Version)
	}
	if len(reloaded.state.Issues) != 1 {
		t.Fatalf("stored issues = %+v", reloaded.state.Issues)
	}
	stored := reloaded.state.Issues[0]
	if stored.ID == "" || stored.ContactID != "contact-old" || stored.Goal != "Implement login" ||
		stored.Requirements != "Use SMS verification." || stored.Status != issueStatusOpen {
		t.Fatalf("stored issue = %+v", stored)
	}
	if reloaded.state.Contacts[0].Draft != "preserved draft" || reloaded.state.Contacts[0].SessionID != "session-old" {
		t.Fatalf("v1 fields changed after issue save: %+v", reloaded.state.Contacts[0])
	}
}

func TestCreateIssueRequiresGoalAndPersistsBeforeAnyRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{store: store, contacts: []*contact{{ID: "c1", Name: "Saki", Workspace: "/tmp"}}}
	if a.createIssue("", "requirement") {
		t.Fatal("empty goal must fail")
	}
	if len(a.contacts[0].Issues) != 0 || a.issueFormError == "" {
		t.Fatalf("empty goal left state: %+v error=%q", a.contacts[0].Issues, a.issueFormError)
	}
	if !a.createIssue("Implement login", "Must support SMS.") {
		t.Fatalf("createIssue failed: %s", a.issueFormError)
	}
	issue := a.contacts[0].Issues[0]
	if issue.ContactID != "c1" || issue.Goal != "Implement login" || issue.Requirements != "Must support SMS." {
		t.Fatalf("issue fields = %+v", issue)
	}
	if len(issue.Executions) != 0 {
		t.Fatalf("creating an item must not create an execution: %+v", issue.Executions)
	}
	if a.contacts[0].SelectedIssueID != issue.ID || a.issueFormOpen {
		t.Fatalf("issue selection/form = %q/%v", a.contacts[0].SelectedIssueID, a.issueFormOpen)
	}
}

func TestIssueEvidenceDoesNotInferCompletion(t *testing.T) {
	issue := &productIssue{ID: "i1", Goal: "draft", Requirements: "needs approval"}
	text := issueEvidenceText(issue)
	if !strings.Contains(text, "not recorded") || !strings.Contains(text, "not item completion") {
		t.Fatalf("evidence text = %q", text)
	}
	issue.Evidence = "user supplied: reviewer approved"
	if got := issueEvidenceText(issue); got != issue.Evidence {
		t.Fatalf("explicit evidence changed: %q", got)
	}
}

func TestCreateIssueFailureKeepsFormAndSelection(t *testing.T) {
	// A directory at the store path makes the atomic rename fail after the
	// in-memory item was appended.
	badStorePath := t.TempDir()
	a := &appModel{
		store:         &contactStore{path: badStorePath},
		contacts:      []*contact{{ID: "c1", Name: "Saki", SelectedIssueID: "previous"}},
		issueFormOpen: true, issueGoal: "Implement login", issueRequirements: "SMS required",
	}
	if a.createIssue("Implement login", "SMS required") {
		t.Fatal("createIssue unexpectedly succeeded with a failing store")
	}
	ct := a.contacts[0]
	if len(ct.Issues) != 0 || ct.SelectedIssueID != "previous" {
		t.Fatalf("failed save changed issues/selection: %+v", ct)
	}
	if !a.issueFormOpen || a.issueGoal != "Implement login" || a.issueRequirements != "SMS required" {
		t.Fatalf("failed save lost form state: form=%v goal=%q requirements=%q", a.issueFormOpen, a.issueGoal, a.issueRequirements)
	}
	if !strings.Contains(a.issueFormError, "Could not save") {
		t.Fatalf("save error not visible: %q", a.issueFormError)
	}
}

func TestStoreRejectsIssueWithoutValidContact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	state := storedState{
		Version: 2, Selected: "c1",
		Contacts: []storedContact{{ID: "c1", Name: "Saki"}},
		Issues:   []storedIssue{{ID: "i1", Goal: "orphan"}},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openContactStore(path, "/tmp"); err == nil || !strings.Contains(err.Error(), "unknown contact id") {
		t.Fatalf("orphan issue error = %v", err)
	}
}

func TestAcceptedAssociationSaveFailureLeavesUnknownSnapshot(t *testing.T) {
	ct := &contact{ID: "c1", Name: "Saki", Draft: "later draft"}
	issue := &productIssue{ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusOpen}
	execution := &issueExecution{ID: "e1", Prompt: "work", Status: issueExecutionPending}
	issue.Executions = append(issue.Executions, execution)
	ct.Issues = append(ct.Issues, issue)
	ct.pendingIssueExecution = execution.ID
	a := &appModel{store: &contactStore{path: t.TempDir()}, contacts: []*contact{ct}}
	a.acceptTask(ct, "work", runtimeSendResult{TaskID: "t1", MessageID: "m1"})
	if execution.Status != issueExecutionUnknown || execution.Prompt != "work" || execution.TaskID != "t1" {
		t.Fatalf("accepted save failure lost snapshot: %+v", execution)
	}
	if ct.pendingIssueExecution != "" || issue.Status != issueStatusOpen || ct.Draft != "later draft" {
		t.Fatalf("accepted save failure changed item/draft: issue=%+v pending=%q draft=%q", issue, ct.pendingIssueExecution, ct.Draft)
	}
}

func TestStoredPendingExecutionIsUnknownAfterRestart(t *testing.T) {
	issues := issuesForStoredContact([]storedIssue{{
		ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusOpen,
		Executions: []storedIssueExecution{{ID: "e1", TaskID: "task-possible", Prompt: "work", Status: issueExecutionPending}},
	}}, "c1")
	if len(issues) != 1 || len(issues[0].Executions) != 1 {
		t.Fatalf("issues = %+v", issues)
	}
	if got := issues[0].Executions[0].Status; got != issueExecutionUnknown {
		t.Fatalf("restart pending status = %q, want unknown", got)
	}
	if reason := issueRunBlocked(issues[0]); reason == "" {
		t.Fatal("unknown execution must block blind Run")
	}
}

func TestIssueFormCreatesExplicitItemWithoutTouchingComposer(t *testing.T) {
	a := testApp(t)
	ct := a.selectedContact()
	ct.Draft = "ordinary composer draft"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("New issue"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Issue goal") {
		t.Fatal("opening the issue form did not focus Goal")
	}
	if err := tr.Click("Issue goal"); err != nil {
		t.Fatal(err)
	}
	tr.Type("Implement login")
	if err := tr.Click("Issue requirements"); err != nil {
		t.Fatal(err)
	}
	tr.Type("Must use SMS.")
	if err := tr.Click("Save issue"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if len(ct.Issues) != 1 || ct.SelectedIssueID != ct.Issues[0].ID {
		t.Fatalf("issue form did not create/select item: %+v", ct.Issues)
	}
	issue := ct.Issues[0]
	if issue.Goal != "Implement login" || issue.Requirements != "Must use SMS." {
		t.Fatalf("issue fields = %+v", issue)
	}
	if ct.Draft != "ordinary composer draft" {
		t.Fatalf("issue form changed composer draft: %q", ct.Draft)
	}
	if !tr.Focused("New issue") {
		t.Fatal("saving the issue did not return focus to New issue")
	}
	for _, want := range []string{"Implement login", "Must use SMS.", "Original requirements · unverified", "Run issue"} {
		if !tr.HasText(want) {
			t.Errorf("workspace missing %q", want)
		}
	}
}

func TestIssueRunPreservesComposerWhenDraftMatchesPrompt(t *testing.T) {
	ct := &contact{ID: "c1", Name: "Saki", Draft: "work"}
	issue := &productIssue{ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusOpen}
	execution := &issueExecution{ID: "e1", Prompt: "work", Status: issueExecutionPending}
	issue.Executions = append(issue.Executions, execution)
	ct.Issues = append(ct.Issues, issue)
	ct.SelectedIssueID = issue.ID
	ct.pendingIssueExecution = execution.ID
	ct.SubmittedRev = ct.draftRev
	a := &appModel{contacts: []*contact{ct}}
	a.acceptTask(ct, "work", runtimeSendResult{TaskID: "t1", MessageID: "m1"})
	if ct.Draft != "work" {
		t.Fatalf("explicit issue run consumed composer: %q", ct.Draft)
	}

	normal := &contact{ID: "c2", Name: "Saki", Draft: "sent"}
	normal.SubmittedRev = normal.draftRev
	a.contacts = append(a.contacts, normal)
	a.acceptTask(normal, "sent", runtimeSendResult{TaskID: "t2", MessageID: "m2"})
	if normal.Draft != "" {
		t.Fatalf("ordinary accepted send did not clear its draft: %q", normal.Draft)
	}
}

func TestIssueConfirmedRejectIsPersistedAndRerunnable(t *testing.T) {
	ct := &contact{ID: "c1", Name: "Saki"}
	issue := &productIssue{ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusOpen}
	execution := &issueExecution{ID: "e1", Prompt: "work", Status: issueExecutionPending}
	issue.Executions = append(issue.Executions, execution)
	ct.Issues = append(ct.Issues, issue)
	ct.SelectedIssueID = issue.ID
	ct.pendingIssueExecution = execution.ID
	path := filepath.Join(t.TempDir(), "contacts.json")
	a := &appModel{contacts: []*contact{ct}, selected: 0, store: &contactStore{path: path}}
	a.failSubmit(ct.ID, &runtimeError{Code: "invalid_request", Message: "not accepted"}, true)
	if execution.Status != issueExecutionRejected || ct.SendState != sendIdle {
		t.Fatalf("confirmed reject state = %+v / %v", execution, ct.SendState)
	}
	if reason := issueRunBlocked(issue); reason != "" {
		t.Fatalf("rejected execution blocked an explicit rerun: %q", reason)
	}
	loaded, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.state.Issues) != 1 || len(loaded.state.Issues[0].Executions) != 1 {
		t.Fatalf("persisted issues = %+v", loaded.state.Issues)
	}
	if got := loaded.state.Issues[0].Executions[0].Status; got != issueExecutionRejected {
		t.Fatalf("persisted reject status = %q", got)
	}
	restored := issuesForStoredContact(loaded.state.Issues, "c1")
	if len(restored) != 1 || restored[0].Executions[0].Status != issueExecutionRejected || issueRunBlocked(restored[0]) != "" {
		t.Fatalf("restored reject = %+v", restored)
	}
}

func TestIssueFormContactSwitchCannotLeakInput(t *testing.T) {
	a := testApp(t)
	a.workspaceOpen = true
	a.issueFormOpen = true
	a.issueGoal = "only for Saki"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	other := a.contacts[1].Name
	if err := tr.Click(other); err != nil {
		t.Fatal(err)
	}
	if a.selected != 0 || a.issueGoal != "only for Saki" || !strings.Contains(a.issueFormError, "switching contacts") {
		t.Fatalf("contact switch leaked form: selected=%d goal=%q error=%q", a.selected, a.issueGoal, a.issueFormError)
	}
}

func TestIssueResultAttributionFollowsSelectedIssue(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Workspace = t.TempDir()
	ct.Issues = []*productIssue{
		{ID: "i1", ContactID: "saki", Goal: "First issue", Status: issueStatusOpen, Executions: []*issueExecution{{ID: "e1", TaskID: "t1", Status: issueExecutionCompleted}}},
		{ID: "i2", ContactID: "saki", Goal: "Second issue", Status: issueStatusOpen, Executions: []*issueExecution{{ID: "e2", TaskID: "t2", Status: issueExecutionRunning}}},
	}
	ct.SelectedIssueID = "i1"
	ct.ActiveTaskID = "t2"
	ct.LastTaskID = "t2"
	ct.Works = []*workItem{
		{TaskID: "t1", MessageID: "m1", State: workCompleted, Prompt: "first prompt"},
		{TaskID: "t2", MessageID: "m2", State: workRunning, Prompt: "second prompt"},
	}
	ct.Messages = []*message{
		{ID: 1, Kind: msgContact, TaskID: "t1", MessageID: "m1", Text: "first result"},
		{ID: 2, Kind: msgContact, TaskID: "t2", MessageID: "m2", Text: "second result"},
	}
	if w, _ := ct.issueWork(ct.Issues[0]); w == nil || w.TaskID != "t1" {
		t.Fatalf("selected issue chose unrelated work: %+v", w)
	}
	a.windowWidth = 320
	tr := ui.NewTester(a.view, 320, 640)
	tr.Frame()
	if !tr.HasText("Run 1 result · First issue") || !tr.HasText("first result") {
		t.Fatal("selected issue did not show its own known result")
	}
	if tr.HasText("No current work") {
		t.Fatal("selected issue fell back to unrelated empty work")
	}
	if err := tr.Click("Second issue"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if w, _ := ct.issueWork(ct.Issues[1]); w == nil || w.TaskID != "t2" {
		t.Fatalf("second selected issue chose unrelated work: %+v", w)
	}
	if !tr.HasText("Run 1 in progress · Second issue") || !tr.HasText("second result") {
		t.Fatal("selecting the second issue did not switch result attribution")
	}
}

func TestIssueNotDispatchedIsPersistedAndRerunnable(t *testing.T) {
	ct := &contact{ID: "c1", Name: "Saki"}
	issue := &productIssue{ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusOpen}
	execution := &issueExecution{ID: "e1", Prompt: "work", Status: issueExecutionPending}
	issue.Executions = append(issue.Executions, execution)
	ct.Issues = append(ct.Issues, issue)
	ct.SelectedIssueID = issue.ID
	ct.pendingIssueExecution = execution.ID
	path := filepath.Join(t.TempDir(), "contacts.json")
	a := &appModel{contacts: []*contact{ct}, selected: 0, store: &contactStore{path: path}}
	a.failSubmit(ct.ID, &runtimeError{Code: "protocol_error", Message: "sendTask was not issued"}, false)
	if execution.Status != issueExecutionNotDispatched || issueRunBlocked(issue) != "" {
		t.Fatalf("not-dispatched state = %+v", execution)
	}
	loaded, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	restored := issuesForStoredContact(loaded.state.Issues, "c1")
	if len(restored) != 1 || restored[0].Executions[0].Status != issueExecutionNotDispatched || issueRunBlocked(restored[0]) != "" {
		t.Fatalf("restored not-dispatched = %+v", restored)
	}
}

func TestIssueFormCancelReturnsFocusToNewIssue(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("New issue"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.issueFormOpen || !tr.Focused("New issue") {
		t.Fatal("cancel did not close the issue form and return focus")
	}
}

func TestRestoredNonterminalExecutionWithoutTaskIsUnknown(t *testing.T) {
	for _, status := range []string{issueExecutionPending, issueExecutionAccepted, issueExecutionRunning} {
		issues := issuesForStoredContact([]storedIssue{{
			ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusOpen,
			Executions: []storedIssueExecution{{ID: "e1", Prompt: "work", Status: status}},
		}}, "c1")
		execution := issues[0].Executions[0]
		if execution.Status != issueExecutionUnknown || issueRunBlocked(issues[0]) == "" {
			t.Fatalf("%s without task restored as %q", status, execution.Status)
		}
	}
	issues := issuesForStoredContact([]storedIssue{{
		ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusOpen,
		Executions: []storedIssueExecution{{ID: "e1", TaskID: "t1", Prompt: "work", Status: issueExecutionRunning}},
	}}, "c1")
	if issues[0].Executions[0].Status != issueExecutionRunning || issueRunBlocked(issues[0]) == "" {
		t.Fatalf("associated running execution lost its task identity: %+v", issues[0].Executions[0])
	}
}

func TestIssueFormCannotLeakIntoNewContact(t *testing.T) {
	a := testApp(t)
	a.issueFormOpen = true
	a.issueGoal = "only for the current contact"
	before := len(a.contacts)
	a.addDefaultContact()
	if len(a.contacts) != before || !strings.Contains(a.issueFormError, "switching contacts") {
		t.Fatalf("add contact leaked issue form: contacts=%d error=%q", len(a.contacts), a.issueFormError)
	}
}
