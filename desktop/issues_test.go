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
		stored.Requirements != "" || len(stored.RequirementItems) != 1 ||
		stored.RequirementItems[0].Text != "Use SMS verification." ||
		stored.Status != issueStatusNotStarted {
		t.Fatalf("stored issue = %+v", stored)
	}
	if reloaded.state.Contacts[0].Draft != "preserved draft" || reloaded.state.Contacts[0].SessionID != "session-old" {
		t.Fatalf("v1 fields changed after issue save: %+v", reloaded.state.Contacts[0])
	}
	if reloaded.state.Contacts[0].SelectedIssueID != stored.ID {
		t.Fatalf("reloaded selected issue = %q, want %q", reloaded.state.Contacts[0].SelectedIssueID, stored.ID)
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
	if issue.ContactID != "c1" || issue.Goal != "Implement login" || issueRequirementsText(issue) != "Must support SMS." {
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
	issue := &productIssue{ID: "i1", Goal: "draft", Requirements: parseIssueRequirements("needs approval")}
	text := issueEvidenceText(issue)
	if !strings.Contains(text, "Completion not verified") {
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
	issue := &productIssue{ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusNotStarted}
	execution := &issueExecution{ID: "e1", Prompt: "work", Status: issueExecutionPending}
	issue.Executions = append(issue.Executions, execution)
	ct.Issues = append(ct.Issues, issue)
	ct.pendingIssueExecution = execution.ID
	a := &appModel{store: &contactStore{path: t.TempDir()}, contacts: []*contact{ct}}
	a.acceptTask(ct, "work", runtimeSendResult{TaskID: "t1", MessageID: "m1"})
	if execution.Status != issueExecutionUnknown || execution.Prompt != "work" || execution.TaskID != "t1" {
		t.Fatalf("accepted save failure lost snapshot: %+v", execution)
	}
	if ct.pendingIssueExecution != "" || issue.Status != issueStatusNotStarted || ct.Draft != "later draft" {
		t.Fatalf("accepted save failure changed item/draft: issue=%+v pending=%q draft=%q", issue, ct.pendingIssueExecution, ct.Draft)
	}
}

func TestStoredPendingExecutionIsUnknownAfterRestart(t *testing.T) {
	issues := issuesForStoredContact([]storedIssue{{
		ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusNotStarted,
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
	a.workspaceOpen = true
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
	if issue.Goal != "Implement login" || issueRequirementsText(issue) != "Must use SMS." {
		t.Fatalf("issue fields = %+v", issue)
	}
	if ct.Draft != "ordinary composer draft" {
		t.Fatalf("issue form changed composer draft: %q", ct.Draft)
	}
	if !tr.Focused("Back to tasks") {
		t.Fatal("saving the issue did not open detail with Back focus")
	}
	for _, want := range []string{"Implement login", "Must use SMS.", "Requirements", "Unverified", "Start task"} {
		if !tr.HasText(want) {
			t.Errorf("workspace missing %q", want)
		}
	}
}

func TestIssueRunPreservesComposerWhenDraftMatchesPrompt(t *testing.T) {
	ct := &contact{ID: "c1", Name: "Saki", Draft: "work"}
	issue := &productIssue{ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusNotStarted}
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
	issue := &productIssue{ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusNotStarted}
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
		{ID: "i1", ContactID: "saki", Goal: "First issue", Status: issueStatusInProgress, Executions: []*issueExecution{{ID: "e1", TaskID: "t1", Status: issueExecutionCompleted}}},
		{ID: "i2", ContactID: "saki", Goal: "Second issue", Status: issueStatusInProgress, Executions: []*issueExecution{{ID: "e2", TaskID: "t2", Status: issueExecutionRunning}}},
	}
	openIssueDetailForTest(a, ct, "i1")
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
	if w := ct.issueWork(ct.Issues[0]); w == nil || w.TaskID != "t1" {
		t.Fatalf("selected issue chose unrelated work: %+v", w)
	}
	a.windowWidth = 320
	tr := ui.NewTester(a.view, 320, 640)
	tr.Frame()
	if !tr.HasText("Result preview: first result") {
		t.Fatal("selected issue did not show its own known result")
	}
	if tr.HasText("No current work") {
		t.Fatal("selected issue fell back to unrelated empty work")
	}
	if err := tr.Click("Back to tasks"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Second issue"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if w := ct.issueWork(ct.Issues[1]); w == nil || w.TaskID != "t2" {
		t.Fatalf("second selected issue chose unrelated work: %+v", w)
	}
	if !tr.HasText("Current response (execution still running): second result") {
		t.Fatal("selecting the second issue did not switch result attribution")
	}
}

func TestIssueNotDispatchedIsPersistedAndRerunnable(t *testing.T) {
	ct := &contact{ID: "c1", Name: "Saki"}
	issue := &productIssue{ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusNotStarted}
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
	a.workspaceOpen = true
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
			ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusNotStarted,
			Executions: []storedIssueExecution{{ID: "e1", Prompt: "work", Status: status}},
		}}, "c1")
		execution := issues[0].Executions[0]
		if execution.Status != issueExecutionUnknown || issueRunBlocked(issues[0]) == "" {
			t.Fatalf("%s without task restored as %q", status, execution.Status)
		}
	}
	issues := issuesForStoredContact([]storedIssue{{
		ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusNotStarted,
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

func TestStoreV2IssuesRemainReadableAndGainWorkSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	legacy := storedState{
		Version: 2, Selected: "c1",
		Contacts: []storedContact{{ID: "c1", Name: "Saki", SessionID: "conversation-old"}},
		Issues: []storedIssue{{
			ID: "i1", ContactID: "c1", Goal: "legacy issue", Status: issueStatusNotStarted,
			Executions: []storedIssueExecution{{ID: "e1", SessionID: "conversation-old", TaskID: "t1", Status: issueExecutionCompleted}},
		}},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	issues := issuesForStoredContact(store.state.Issues, "c1")
	if len(issues) != 1 || issues[0].WorkSessionID != "" {
		t.Fatalf("v2 issue gained an invented work session: %+v", issues)
	}
	if issues[0].Executions[0].SessionID != "conversation-old" || issues[0].Executions[0].TaskID != "t1" {
		t.Fatalf("v2 execution was rewritten: %+v", issues[0].Executions[0])
	}
	ct := &contact{ID: "c1", Name: "Saki", SessionID: "conversation-old", Issues: issues}
	issues[0].WorkSessionID = "work-new"
	issues[0].WorkSessionID = "work-new"
	if err := store.save([]*contact{ct}, "c1"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Version != contactStoreVersion {
		t.Fatalf("saved store version = %d", reloaded.state.Version)
	}
	restored := issuesForStoredContact(reloaded.state.Issues, "c1")[0]
	if restored.WorkSessionID != "work-new" || restored.Executions[0].SessionID != "conversation-old" {
		t.Fatalf("v3 work session or old execution changed: %+v", restored)
	}
}

func TestContactActivityObservesIssueWorkTarget(t *testing.T) {
	ct := &contact{ID: "x", Name: "X", Loaded: true}
	a := &appModel{demo: false, contacts: []*contact{ct}, runtimeStatus: runtimeStateReady, rt: &runtimeClient{}}
	if active, unknown := a.contactActivity(ct); active || unknown {
		t.Fatal("contact with no issue work was active")
	}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "work", WorkSessionID: "work-session"}
	issue.Work.SendState = sendAccepted
	issue.Work.ActiveTaskID = "task-1"
	ct.Issues = []*productIssue{issue}
	if active, unknown := a.contactActivity(ct); !active || unknown {
		t.Fatalf("active issue work was not observed: active=%v unknown=%v", active, unknown)
	}
}

func TestRuntimeExitMarksAllActiveIssueWorkUnknown(t *testing.T) {
	ct := &contact{ID: "x", Name: "X", SessionID: "chat-x", Loaded: true, SendState: sendIdle}
	issue := &productIssue{ID: "i1", ContactID: "x", Goal: "work"}
	issue.WorkSessionID = "work-i1"
	issue.Work.ActiveTaskID = "t1"
	issue.Work.SendState = sendAccepted
	issue.Executions = []*issueExecution{{ID: "e1", SessionID: "work-i1", TaskID: "t1", Status: issueExecutionRunning}}
	ct.Issues = []*productIssue{issue}
	ct.Works = []*workItem{{TaskID: "t1", State: workRunning}}
	a := &appModel{contacts: []*contact{ct}}
	a.clearActiveTasksAfterExit()
	if issue.Work.ActiveTaskID != "" || issue.Work.SendState != sendUnknown || issue.Executions[0].Status != issueExecutionUnknown {
		t.Fatalf("issue work survived exit as active: work=%+v exec=%+v", issue.Work, issue.Executions[0])
	}
	if ct.ActiveTaskID != "" || ct.SendState != sendIdle {
		t.Fatalf("conversation without active task was rewritten: %+v", ct)
	}
}

func TestUnknownIssueExecutionCanBeConfirmedNotDispatched(t *testing.T) {
	ct := &contact{ID: "c1", Name: "Saki"}
	issue := &productIssue{ID: "i1", ContactID: "c1", Goal: "work", Status: issueStatusNotStarted}
	execution := &issueExecution{ID: "e1", SessionID: "work", Prompt: "work", Status: issueExecutionUnknown}
	issue.Executions = append(issue.Executions, execution)
	ct.Issues = append(ct.Issues, issue)
	ct.SelectedIssueID = issue.ID
	a := &appModel{contacts: []*contact{ct}, selected: 0}
	if !a.confirmIssueExecutionNotDispatched(issue.ID, execution.ID) {
		t.Fatal("unknown execution without task id was not confirmable")
	}
	if execution.Status != issueExecutionNotDispatched || !strings.Contains(execution.Detail, "no task was dispatched") {
		t.Fatalf("not-dispatched confirmation = %+v", execution)
	}
	if issueRunBlocked(issue) != "" {
		t.Fatal("confirmed not-dispatched execution still blocks explicit retry")
	}
}

func TestIssueWorkNoticeKeepsUnseenOlderExceptionAfterSuccess(t *testing.T) {
	ct := &contact{ID: "x", Name: "X", Loaded: true}
	issue := &productIssue{ID: "i-old-exception", ContactID: ct.ID, Goal: "older failed work"}
	issue.Executions = []*issueExecution{
		{ID: "e-old", TaskID: "t-old", Status: issueExecutionFailed, Detail: "old unseen failure"},
		{ID: "e-new", TaskID: "t-new", Status: issueExecutionCompleted},
	}
	ct.Issues = []*productIssue{issue}
	a := &appModel{contacts: []*contact{ct}, selected: 0}
	notice := a.issueWorkNotice(ct)
	if !notice.Attention || notice.IssueID != issue.ID || !strings.Contains(notice.Text, "old unseen failure") {
		t.Fatalf("later success hid an unseen older exception: %+v", notice)
	}
	issue.Executions[0].AttentionSeenStatus = issueExecutionFailed
	if notice := a.issueWorkNotice(ct); notice.Text != "" {
		t.Fatalf("seen older exception was re-lit after success: %+v", notice)
	}
}

func TestIssueSeenUnknownWithActiveTaskIDDoesNotReAttention(t *testing.T) {
	ct := &contact{ID: "contact-seen-active-unknown", Name: "Seen Unknown", Loaded: true}
	execution := &issueExecution{
		ID: "run-seen-active-unknown", TaskID: "task-seen-active-unknown",
		Status: issueExecutionUnknown, Detail: "unknown already seen",
		AttentionSeenStatus: issueExecutionUnknown,
	}
	issue := &productIssue{
		ID: "issue-seen-active-unknown", ContactID: ct.ID, Goal: "seen unknown with active mapping",
		Status: issueStatusInProgress,
		Work: issueWorkSession{Loaded: true, ActiveTaskID: "task-seen-active-unknown", LastTaskID: "task-seen-active-unknown",
			SendState: sendUnknown, SendError: "delivery not confirmed"},
		Executions: []*issueExecution{execution},
	}
	ct.Issues = []*productIssue{issue}
	display := issueDisplayStatusFor(issue)
	if display.Attention {
		t.Fatalf("seen unknown with retained ActiveTaskID re-raised attention: %+v", display)
	}
	notice := (&appModel{}).issueWorkNotice(ct)
	if notice.Attention || notice.Text != "" {
		t.Fatalf("seen unknown with retained ActiveTaskID re-raised notice: %+v", notice)
	}
	if issue.Work.ActiveTaskID != "task-seen-active-unknown" || issue.Work.SendState != sendUnknown ||
		execution.AttentionSeenStatus != issueExecutionUnknown || execution.Status != issueExecutionUnknown {
		t.Fatalf("seen unknown state was rewritten: work=%+v run=%+v", issue.Work, execution)
	}
}

func TestIssueActiveAcceptedSendFailureStaysAttention(t *testing.T) {
	a, ct := taskContextApp(t)
	issue := &productIssue{
		ID: "issue-accepted-save-failure", ContactID: ct.ID, Goal: "accepted context failure",
		Status: issueStatusInProgress, WorkSessionID: "work-accepted-save-failure",
		RequirementsRevision: 1,
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	if err := a.addIssueSupplement(ct, issue.ID, "accepted supplement"); err != nil {
		t.Fatal(err)
	}
	execution := &issueExecution{
		ID: "run-accepted-save-failure", SessionID: issue.WorkSessionID,
		Prompt: "accepted prompt", Status: issueExecutionPending, RequirementsRevision: 1,
	}
	issue.Executions = []*issueExecution{execution}
	issue.Work.SendState = sendSubmitting
	issue.Work.pendingExecutionID = execution.ID
	target := issueSupplementTargetFor(ct, issue)
	if _, err := issue.Supplements.PrepareDispatch(target, execution, a.persist); err != nil {
		t.Fatal(err)
	}
	// Force the accepted supplement receipt save to fail after the real Runtime
	// acceptance is already represented in memory.
	a.store = &contactStore{path: t.TempDir()}
	a.acceptIssueTask(ct, issue, execution, "accepted prompt", runtimeSendResult{
		TaskID: "task-accepted-save-failure", MessageID: "message-accepted-save-failure",
	})
	if issue.Work.ActiveTaskID != "task-accepted-save-failure" || issue.Work.SendState != sendAccepted ||
		issue.Work.SendError == "" || execution.Status != issueExecutionAccepted ||
		issue.Status != issueStatusInProgress {
		t.Fatalf("accepted supplement failure state = work=%+v execution=%+v issue=%q",
			issue.Work, execution, issue.Status)
	}
	display := issueDisplayStatusFor(issue)
	if !display.Attention || !display.Active || display.Reason != issue.Work.SendError ||
		display.Status != issueStatusNeedsAttention {
		t.Fatalf("accepted supplement failure projection = %+v", display)
	}
	notice := a.issueWorkNotice(ct)
	if !notice.Attention || notice.IssueID != issue.ID || !strings.Contains(notice.Text, issue.Work.SendError) {
		t.Fatalf("accepted supplement failure notice = %+v", notice)
	}
	if issue.Work.ActiveTaskID != "task-accepted-save-failure" || issue.Work.SendState != sendAccepted ||
		execution.Status != issueExecutionAccepted || strings.Contains(issue.Work.SendError, "unknown") {
		t.Fatalf("accepted supplement failure was misclassified as unknown/success: issue=%+v execution=%+v", issue.Work, execution)
	}
}

func TestIssueDisplayProjectionUsesExistingFacts(t *testing.T) {
	ct := &contact{ID: "contact-projection", Name: "Projection", Loaded: true}
	completed := &productIssue{ID: "issue-completed", ContactID: ct.ID, Goal: "completed goal", Status: issueStatusCompleted,
		Executions: []*issueExecution{{ID: "run-completed-old", Status: issueExecutionFailed, Detail: "old failure"}}}
	canceled := &productIssue{ID: "issue-canceled", ContactID: ct.ID, Goal: "canceled goal", Status: issueStatusCanceled,
		Executions: []*issueExecution{{ID: "run-canceled-old", Status: issueExecutionUnknown, Detail: "old unknown"}}}
	archived := &productIssue{ID: "issue-archived", ContactID: ct.ID, Goal: "archived goal", Status: issueStatusInProgress, Archived: true,
		Work: issueWorkSession{Loaded: true, ActiveTaskID: "run-archived-active", SendState: sendAccepted}}
	waiting := &productIssue{ID: "issue-waiting", ContactID: ct.ID, Goal: "waiting goal", Status: issueStatusInProgress,
		Questions: []*issueQuestion{{Ref: "q-waiting", Text: "answer this", State: issueQuestionOpen}}}
	attention := &productIssue{ID: "issue-attention", ContactID: ct.ID, Goal: "attention goal", Status: issueStatusInProgress,
		Executions: []*issueExecution{{ID: "run-attention", Status: issueExecutionFailed, Detail: "unseen failure"}}}
	active := &productIssue{ID: "issue-active", ContactID: ct.ID, Goal: "active goal", Status: issueStatusInProgress,
		Work: issueWorkSession{Loaded: true, ActiveTaskID: "run-active", SendState: sendAccepted}}
	ct.Issues = []*productIssue{completed, canceled, archived, waiting, attention, active}
	notice := (&appModel{}).issueWorkNotice(ct)
	if got := issueDisplayStatusFor(completed).Label; got != "Completed" {
		t.Fatalf("completed label = %q", got)
	}
	if got := issueDisplayStatusFor(canceled).Label; got != "Canceled" {
		t.Fatalf("canceled label = %q", got)
	}
	if got := issueDisplayStatusFor(archived); got.Label != "Archived" || got.Attention || got.Waiting || got.Active {
		t.Fatalf("archived projection = %+v", got)
	}
	if got := issueDisplayStatusFor(waiting); got.Label != issueStatusLabel(&productIssue{Status: issueStatusWaitingUser}) || !got.Waiting {
		t.Fatalf("waiting projection = %+v", got)
	}
	if got := issueDisplayStatusFor(attention); got.Label != "Needs attention" || !got.Attention {
		t.Fatalf("attention projection = %+v", got)
	}
	if got := issueDisplayStatusFor(active); got.Label != "In progress" || got.Status != issueStatusInProgress || !got.Active {
		t.Fatalf("active projection = %+v", got)
	}
	if notice.AttentionCount != 1 || notice.WaitingCount != 1 || notice.ActiveCount != 1 ||
		!notice.Attention || notice.IssueID != attention.ID || notice.IssueName != attention.Goal {
		t.Fatalf("notice aggregation = %+v", notice)
	}
}

func TestIssueExplicitNeedsAttentionSurvivesSeenRun(t *testing.T) {
	ct := &contact{ID: "contact-explicit-attention", Name: "Explicit", Loaded: true}
	execution := &issueExecution{ID: "run-explicit", Status: issueExecutionFailed, Detail: "seen failure", AttentionSeenStatus: issueExecutionFailed}
	issue := &productIssue{ID: "issue-explicit", ContactID: ct.ID, Goal: "explicit attention", Status: issueStatusNeedsAttention,
		Work: issueWorkSession{Loaded: true}, Executions: []*issueExecution{execution}}
	ct.Issues = []*productIssue{issue}
	display := issueDisplayStatusFor(issue)
	if !display.Attention || display.Label != "Needs attention" {
		t.Fatalf("explicit needs_attention was cleared by a seen run: %+v", display)
	}
	notice := (&appModel{}).issueWorkNotice(ct)
	if !notice.Attention || notice.AttentionCount != 1 || notice.IssueID != issue.ID {
		t.Fatalf("explicit needs_attention was cleared from aggregation: %+v", notice)
	}
	if issue.Status != issueStatusNeedsAttention || execution.AttentionSeenStatus != issueExecutionFailed {
		t.Fatalf("seen field or persistent status was rewritten: issue=%+v run=%+v", issue, execution)
	}
}

func TestIssueWorkNoticeBaselineTerminalAndWaiting(t *testing.T) {
	ct := &contact{ID: "contact-baseline", Name: "Baseline", Loaded: true}
	completed := &productIssue{ID: "issue-baseline-completed", ContactID: ct.ID, Goal: "completed old",
		Status: issueStatusCompleted, Executions: []*issueExecution{{ID: "run-baseline-old", Status: issueExecutionFailed, Detail: "old failure"}}}
	archived := &productIssue{ID: "issue-baseline-archived", ContactID: ct.ID, Goal: "archived active",
		Status: issueStatusInProgress, Archived: true, Work: issueWorkSession{Loaded: true, ActiveTaskID: "run-baseline-active", SendState: sendAccepted}}
	waiting := &productIssue{ID: "issue-baseline-waiting", ContactID: ct.ID, Goal: "waiting without run",
		Status: issueStatusInProgress, Questions: []*issueQuestion{{Ref: "question-baseline", Text: "please answer", State: issueQuestionOpen}}}
	ct.Issues = []*productIssue{completed, archived, waiting}
	notice := (&appModel{}).issueWorkNotice(ct)
	if notice.Attention || notice.Text == "" || notice.IssueID != waiting.ID ||
		!strings.Contains(notice.Text, waiting.Goal) {
		t.Fatalf("baseline notice = %+v, want waiting candidate", notice)
	}
	if got := issueWorkActiveCount(ct); got != 0 {
		t.Fatalf("terminal/archived active count = %d, want 0", got)
	}
}

func TestIssueWorkNoticeWinnerScopeAndActive(t *testing.T) {
	ct := &contact{ID: "contact-winner", Name: "Winner", Loaded: true}
	attention := &productIssue{ID: "issue-winner-attention", ContactID: ct.ID, Goal: "attention active",
		Status: issueStatusInProgress, Work: issueWorkSession{Loaded: true, ActiveTaskID: "run-winner-attention", SendState: sendAccepted},
		Executions: []*issueExecution{{ID: "run-winner-attention", Status: issueExecutionFailed, Detail: "attention reason"}}}
	waiting := &productIssue{ID: "issue-winner-waiting", ContactID: ct.ID, Goal: "waiting no run",
		Status: issueStatusInProgress, Questions: []*issueQuestion{{Ref: "question-winner", Text: "answer", State: issueQuestionOpen}}}
	active := &productIssue{ID: "issue-winner-active", ContactID: ct.ID, Goal: "active only",
		Status: issueStatusInProgress, Work: issueWorkSession{Loaded: true, ActiveTaskID: "run-winner-active", SendState: sendAccepted}}
	otherContact := &productIssue{ID: "issue-other-contact", ContactID: "other-contact", Goal: "other failure",
		Status: issueStatusInProgress, Executions: []*issueExecution{{ID: "run-other", Status: issueExecutionFailed, Detail: "other reason"}}}
	ct.Issues = []*productIssue{attention, waiting, active, otherContact}
	notice := (&appModel{}).issueWorkNotice(ct)
	if !notice.Attention || notice.Waiting || !notice.Active ||
		notice.IssueID != attention.ID || notice.IssueName != attention.Goal ||
		!strings.Contains(notice.Text, attention.Goal) || !strings.Contains(notice.Text, "attention reason") {
		t.Fatalf("attention winner = %+v", notice)
	}
	if notice.AttentionCount != 1 || notice.WaitingCount != 1 || notice.ActiveCount != 2 {
		t.Fatalf("winner counts = %+v", notice)
	}
	if got := issueWorkActiveCount(ct); got != 2 {
		t.Fatalf("active count scope = %d, want 2", got)
	}

	ct.Issues = []*productIssue{waiting, active}
	notice = (&appModel{}).issueWorkNotice(ct)
	if notice.Attention || !notice.Waiting || notice.Active ||
		notice.IssueID != waiting.ID || notice.IssueName != waiting.Goal ||
		!strings.Contains(notice.Text, waiting.Goal) {
		t.Fatalf("waiting winner = %+v", notice)
	}
	if notice.WaitingCount != 1 || notice.ActiveCount != 1 {
		t.Fatalf("waiting winner counts = %+v", notice)
	}

	inProgress := &productIssue{ID: "issue-in-progress", ContactID: ct.ID, Goal: "in progress no run", Status: issueStatusInProgress}
	display := issueDisplayStatusFor(inProgress)
	if display.Status != issueStatusInProgress || display.Label != "In progress" || display.Active {
		t.Fatalf("in-progress without run = %+v", display)
	}
	ct.Issues = []*productIssue{inProgress}
	if got := issueWorkActiveCount(ct); got != 0 {
		t.Fatalf("in-progress without active count = %d", got)
	}
	notStartedActive := &productIssue{ID: "issue-not-started-active", ContactID: ct.ID, Goal: "not started active",
		Status: issueStatusNotStarted, Work: issueWorkSession{Loaded: true, ActiveTaskID: "run-not-started-active", SendState: sendAccepted}}
	display = issueDisplayStatusFor(notStartedActive)
	if display.Status != issueStatusInProgress || display.Label != "In progress" || !display.Active {
		t.Fatalf("not-started with active work = %+v", display)
	}
	ct.Issues = []*productIssue{notStartedActive}
	if got := issueWorkActiveCount(ct); got != 1 {
		t.Fatalf("not-started active count = %d", got)
	}
	if notice := (&appModel{}).issueWorkNotice(ct); !notice.Active || notice.Attention || notice.Waiting ||
		notice.IssueID != notStartedActive.ID || !strings.Contains(notice.Text, notStartedActive.Goal) {
		t.Fatalf("not-started active notice = %+v", notice)
	}

	unknown := &productIssue{ID: "issue-unknown", ContactID: ct.ID, Goal: "unknown dispatch", Status: issueStatusInProgress,
		Work: issueWorkSession{Loaded: true, SendState: sendUnknown, SendError: "delivery not confirmed"}}
	display = issueDisplayStatusFor(unknown)
	if !display.Attention || display.Status != issueStatusNeedsAttention || display.Reason != "delivery not confirmed" {
		t.Fatalf("unknown dispatch projection = %+v", display)
	}
}

func TestIssueAttentionLegacyStoreDefaultsUnseen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	legacy := storedState{
		Version:  contactStoreVersion,
		Selected: "contact-legacy",
		Contacts: []storedContact{{ID: "contact-legacy", Name: "Saki"}},
		Issues: []storedIssue{{
			ID: "issue-legacy", ContactID: "contact-legacy", Goal: "legacy failure", Status: issueStatusNeedsAttention,
			Executions: []storedIssueExecution{{
				ID: "exec-legacy", TaskID: "task-legacy", Status: issueExecutionFailed, Detail: "legacy failure",
			}},
		}},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{}
	applyStoreSelection(a, store)
	issue := a.selectedContact().issueByID("issue-legacy")
	if issue == nil || len(issue.Executions) != 1 || issue.Executions[0].AttentionSeenStatus != "" {
		t.Fatalf("legacy execution did not load unseen: %+v", issue)
	}
	if notice := a.issueWorkNotice(a.selectedContact()); !notice.Attention || notice.IssueID != issue.ID {
		t.Fatalf("legacy exception did not raise attention: %+v", notice)
	}
}

func TestIssueRecoveryRejectsAnotherIssuesExecution(t *testing.T) {
	ct := &contact{ID: "c1", Name: "Saki"}
	issueA := &productIssue{ID: "iA", ContactID: "c1", Goal: "A"}
	execA := &issueExecution{ID: "eA", SessionID: "work-A", TaskID: "tA", Status: issueExecutionCompleted}
	issueA.Executions = []*issueExecution{execA}
	issueB := &productIssue{ID: "iB", ContactID: "c1", Goal: "B"}
	execB := &issueExecution{ID: "eB", SessionID: "work-B", Prompt: "B", Status: issueExecutionUnknown}
	issueB.Executions = []*issueExecution{execB}
	ct.Issues = []*productIssue{issueA, issueB}
	ct.SelectedIssueID = issueB.ID
	a := &appModel{contacts: []*contact{ct}, selected: 0}
	if a.reconcileIssueExecution(issueB.ID, execA.ID) {
		t.Fatal("reconcile accepted another issue's execution")
	}
	if a.confirmIssueExecutionNotDispatched(issueB.ID, execA.ID) {
		t.Fatal("confirm accepted another issue's execution")
	}
	if execA.Status != issueExecutionCompleted || execB.Status != issueExecutionUnknown {
		t.Fatalf("wrong-owner recovery changed executions: A=%+v B=%+v", execA, execB)
	}
	issueB.Work.ActiveTaskID = "tB"
	if a.confirmIssueExecutionNotDispatched(issueB.ID, execB.ID) {
		t.Fatal("confirm marked not dispatched while work was active")
	}
}

func TestIssueWorkNoticeSurfacesFailureWithoutChatUnread(t *testing.T) {
	ct := &contact{ID: "x", Name: "X", Loaded: true, Unread: 3}
	issue := &productIssue{ID: "i1", ContactID: "x", Goal: "build login"}
	issue.Work.ActiveTaskID = ""
	issue.Executions = []*issueExecution{{ID: "e1", Status: issueExecutionFailed, Detail: "provider failed"}}
	ct.Issues = []*productIssue{issue}
	a := &appModel{contacts: []*contact{ct}, selected: 0}
	notice := a.issueWorkNotice(ct)
	if !notice.Attention || notice.IssueID != issue.ID || !strings.Contains(notice.Text, "build login") ||
		!strings.Contains(notice.Text, "provider failed") {
		t.Fatalf("issue work notice = %+v", notice)
	}
	if ct.Unread != 3 || ct.ActiveTaskID != "" {
		t.Fatalf("notice changed unread/active conversation: unread=%d active=%q", ct.Unread, ct.ActiveTaskID)
	}
	issue.Executions[0].Status = issueExecutionRunning
	notice = a.issueWorkNotice(ct)
	if notice.Text != "" {
		t.Fatalf("running execution without active task produced a false notice: %+v", notice)
	}
}
