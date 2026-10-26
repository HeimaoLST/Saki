package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func openContactManagementGUI(t *testing.T, provider *gatedProvider) *guiHarness {
	t.Helper()
	binary := requireRuntimeBinary(t)
	server := httptest.NewServer(provider.handler())
	t.Cleanup(server.Close)
	isolateConnectionApp(t, t.TempDir(), binary)
	h := openConnectionHarness(t)
	config := testConnectionConfig(t)
	config.API, config.Endpoint = connectionAPIResponses, server.URL+"/v1"
	saveAppConnection(t, h, config)
	applyAppConnection(t, h)
	h.pump.do(func() { h.app.closeAux() })
	return h
}

func clickContactManagementAction(t *testing.T, h *guiHarness, action string) {
	t.Helper()
	var err error
	h.pump.do(func() {
		tr := ui.NewTester(h.app.view, 1280, 720)
		tr.Frame()
		err = clickConnectionAction(tr, action)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func saveContactManagementProfile(t *testing.T, h *guiHarness, draft profileDraft, startNew bool) {
	t.Helper()
	h.pump.do(func() {
		h.app.openSettings()
		h.app.contactProfile.Draft = draft
	})
	action := "Save changes"
	if startNew {
		action = "Save and start new conversation"
	}
	clickContactManagementAction(t, h, action)
	var problem string
	h.pump.do(func() {
		problem = h.app.contactProfile.Error
		if problem == "" && h.app.selectedContact().Name != strings.TrimSpace(draft.Name) {
			problem = "Save did not update the selected contact"
		}
	})
	if problem != "" {
		t.Fatal(problem)
	}
}

func TestContactManagementGUIProfileNewConversationHistoryAndRestart(t *testing.T) {
	provider := newGatedProvider()
	h := openContactManagementGUI(t, provider)
	original := profileDraft{Name: "Research", Purpose: "original-purpose-152", Instructions: "  original-instructions-152\n  retain indentation  ", Workspace: t.TempDir()}
	saveContactManagementProfile(t, h, original, false)
	var contactID, issueID string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		contactID = ct.ID
		if _, err := ct.Context.AddSnippet("Old material", "old-material-152"); err != nil {
			t.Error(err)
		}
	})
	oldSession := sendConnectionConversation(t, h, "first saved budget-152", "gated result")
	h.pump.do(func() {
		if !h.app.createIssue("Preserved issue", "Preserve this requirement") {
			t.Error(h.app.issueFormError)
			return
		}
		issue := h.app.selectedContact().selectedIssue()
		issueID = issue.ID
		if _, err := issue.Context.AddSnippet("Issue input", "immutable-issue-input-152"); err != nil {
			t.Error(err)
		}
		if !h.app.runIssue(issueID) {
			t.Error(h.app.issueFormError)
		}
	})
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted && !issue.Work.HistoryDirty
	})
	var workSession string
	h.pump.do(func() { workSession = h.app.selectedContact().issueByID(issueID).WorkSessionID })
	next := profileDraft{Name: "Research renamed", Purpose: "next-purpose-152", Instructions: "  next-instructions-152\n    exact content  ", Workspace: t.TempDir()}
	saveContactManagementProfile(t, h, next, false)
	if session := sendConnectionConversation(t, h, "Continue the existing context", "gated result"); session != oldSession {
		t.Fatal("saving future defaults replaced the existing conversation")
	}
	if !strings.Contains(provider.body(2), original.Purpose) || strings.Contains(provider.body(2), next.Purpose) {
		t.Fatal("future profile changes altered an existing Runtime request")
	}
	var pending taskContextItem
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "unsent draft survives new conversation"
		var err error
		pending, err = ct.Context.AddSnippet("Next input", "pending-next-context-152")
		if err != nil {
			t.Error(err)
		}
	})
	saveContactManagementProfile(t, h, next, true)
	h.pump.do(func() {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		if ct.ID != contactID || ct.SessionID != "" || !reflect.DeepEqual(ct.PreviousSessions, []string{oldSession}) || ct.Draft != "unsent draft survives new conversation" || !reflect.DeepEqual(ct.Context.snapshot(), []taskContextItem{pending}) {
			t.Error("starting a new conversation lost contact identity, saved history or unsent input")
		}
		if issue == nil || issue.WorkSessionID != workSession || len(issue.Executions) != 1 || issue.Executions[0].RequirementsRevision != 1 || len(issue.Executions[0].Context) != 1 || issue.Executions[0].Context[0].Text != "immutable-issue-input-152" {
			t.Error("new conversation changed the existing issue execution snapshot")
		}
	})
	if provider.count() != 3 {
		t.Fatal("Save and new called the model before a message was submitted")
	}
	newSession := sendConnectionConversation(t, h, "Send in the new context", "gated result")
	if newSession == oldSession || newSession == workSession || !strings.Contains(provider.body(3), next.Purpose) || strings.Contains(provider.body(3), original.Purpose) {
		t.Fatal("new conversation did not use its own configured Runtime context")
	}
	var rt *runtimeClient
	h.pump.do(func() { rt = h.app.rt })
	oldConfig, err := rt.connectionConfig(oldSession)
	if err != nil {
		t.Fatal(err)
	}
	newConfig, err := rt.connectionConfig(newSession)
	if err != nil {
		t.Fatal(err)
	}
	oldDir, _ := filepath.EvalSymlinks(original.Workspace)
	newDir, _ := filepath.EvalSymlinks(next.Workspace)
	if oldConfig.EffectiveConfig.WorkingDirectory != oldDir || newConfig.EffectiveConfig.WorkingDirectory != newDir {
		t.Fatal("the saved/new workspace boundary did not reach Runtime")
	}
	h.shutdown(t)
	restarted := openConnectionHarness(t)
	restarted.pump.wait(func() bool {
		ct := restarted.app.selectedContact()
		issue := ct.issueByID(issueID)
		return ct.Loaded && issue != nil && issue.Work.Loaded
	})
	restarted.pump.do(func() {
		ct := restarted.app.selectedContact()
		if ct.ID != contactID || ct.SessionID != newSession || ct.Instructions != next.Instructions || !reflect.DeepEqual(ct.PreviousSessions, []string{oldSession}) {
			t.Error("restart lost current/previous session mapping or exact profile text")
		}
		restarted.app.addDefaultContact()
	})
	sendConnectionConversation(t, restarted, "foreign saved budget-152", "gated result")
	restarted.pump.do(func() {
		restarted.app.selectContact(0)
		restarted.app.openHistory()
		restarted.app.historyQuery = "saved budget-152"
	})
	clickContactManagementAction(t, restarted, "Search saved contexts")
	restarted.pump.wait(func() bool { return !restarted.app.contactHistoryBusy && restarted.app.contactHistory != nil })
	var matches []contactHistoryMatch
	restarted.pump.do(func() { matches = append(matches, restarted.app.contactHistory.Matches...) })
	if len(matches) != 1 || matches[0].SessionID != oldSession {
		t.Fatalf("saved-history search lost the old conversation or included a different contact: %+v", matches)
	}
	clickContactManagementAction(t, restarted, "View full message")
	restarted.pump.wait(func() bool { return restarted.app.contactHistoryFull != nil && !restarted.app.contactHistoryDetailBusy })
	restarted.pump.do(func() {
		if restarted.app.contactHistoryFull.Text != "first saved budget-152" || restarted.app.selectedContact().SessionID != newSession {
			t.Error("reading old history replaced the executable conversation or failed to locate its full message")
		}
		if context := restarted.app.contactHistoryFull.Context; len(context) != 1 || context[0].Text != "old-material-152" {
			t.Errorf("full saved message lost its submitted Context: %+v", context)
		}
	})
	if provider.count() != 5 {
		t.Fatal("restart/history read dispatched an unexpected model request")
	}
}

func TestContactManagementGUIRejectedNewWorkspacePreservesRetryAndHistory(t *testing.T) {
	provider := newGatedProvider()
	h := openContactManagementGUI(t, provider)
	oldSession := sendConnectionConversation(t, h, "saved before a rejected new conversation", "gated result")
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	var draft profileDraft
	var pending taskContextItem
	h.pump.do(func() {
		ct := h.app.selectedContact()
		draft = contactProfileDraft(ct)
		draft.Workspace = workspace
		ct.Draft = "retry after fixing the new workspace"
		var err error
		pending, err = ct.Context.AddSnippet("Retry material", "retain-on-rejection-152")
		if err != nil {
			t.Error(err)
		}
	})
	saveContactManagementProfile(t, h, draft, true)
	if err := os.Remove(workspace); err != nil {
		t.Fatal(err)
	}
	h.pump.do(func() {
		h.app.closeAux()
		ct := h.app.selectedContact()
		if !h.app.send(ct, ct.Draft) {
			t.Error("the valid saved profile never reached Runtime create")
		}
	})
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.SendState == sendIdle && ct.SendError != ""
	})
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if ct.SessionID != "" || ct.Draft != "retry after fixing the new workspace" || !reflect.DeepEqual(ct.Context.snapshot(), []taskContextItem{pending}) || ct.pendingSend != nil {
			t.Error("rejected create lost retry input or claimed a current session")
		}
		if !reflect.DeepEqual(ct.PreviousSessions, []string{oldSession}) {
			t.Error("rejected create lost the previous conversation reference")
		}
		h.app.openHistory()
		h.app.searchContactHistoryAsync("saved before a rejected")
	})
	h.pump.wait(func() bool { return h.app.contactHistory != nil && !h.app.contactHistoryBusy })
	h.pump.do(func() {
		matches := h.app.contactHistory.Matches
		if len(matches) != 1 || matches[0].SessionID != oldSession {
			t.Error("rejected create prevented reading the retained old conversation")
		}
	})
	if provider.count() != 1 {
		t.Fatal("invalid new workspace dispatched a model request")
	}
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	newSession := sendConnectionConversation(t, h, "retry after fixing the new workspace", "gated result")
	if newSession == oldSession || provider.count() != 2 || !strings.Contains(provider.body(1), "retain-on-rejection-152") {
		t.Fatal("explicit retry did not create a new context with the preserved input")
	}
}

func TestContactManagementVersionSixUpgradePreservesOwnedExecutionData(t *testing.T) {
	contactInput := taskContextItem{ID: "contact-input", Name: "Contact material", Text: "unsent contact material", Revision: 2}
	issueInput := taskContextItem{ID: "issue-input", Name: "Issue material", Text: "execution material", Revision: 4}
	legacy := storedState{
		Version: 6, Selected: "contact-legacy",
		Contacts: []storedContact{{
			ID: "contact-legacy", Name: "Legacy", SessionID: "conversation-legacy", Draft: "unsent draft",
			TaskContext: []taskContextItem{contactInput},
			PendingSend: &pendingSend{Draft: "unconfirmed send", Context: []taskContextItem{contactInput}, State: "unknown"},
		}},
		Issues: []storedIssue{{
			ID: "issue-legacy", ContactID: "contact-legacy", Goal: "Keep ownership", Status: issueStatusNeedsAttention,
			WorkSessionID: "work-legacy", RequirementsRevision: 3,
			RequirementItems: []storedRequirement{{ID: "requirement-legacy", Text: "Keep evidence", State: "unverified"}},
			TaskContext:      []taskContextItem{issueInput},
			Executions: []storedIssueExecution{{
				ID: "execution-legacy", SessionID: "work-legacy", TaskID: "task-legacy", Status: issueExecutionCompleted,
				RequirementsRevision: 2, Context: []taskContextItem{issueInput}, SupplementBatchID: "batch-legacy",
			}},
			Supplements: &issueSupplementLedger{
				Items: []*issueSupplement{{
					ID: "supplement-legacy", ContactID: "contact-legacy", IssueID: "issue-legacy", Text: "submitted material",
					RequirementsRevision: 2, State: supplementStateSubmitted, BatchID: "batch-legacy",
					SessionID: "work-legacy", TaskID: "task-legacy", MessageID: "message-legacy", ExecutionID: "execution-legacy",
				}},
				Batches: []*issueSupplementBatch{{
					ID: "batch-legacy", Target: issueSupplementTarget{ContactID: "contact-legacy", IssueID: "issue-legacy", RequirementsRevision: 2},
					Snapshot: []issueSupplementSnapshot{{ID: "supplement-legacy", Text: "submitted material", RequirementsRevision: 2}},
					State:    supplementBatchAccepted, ExecutionID: "execution-legacy", SessionID: "work-legacy", TaskID: "task-legacy", MessageID: "message-legacy",
				}},
			},
		}},
	}
	path := filepath.Join(t.TempDir(), "contacts.json")
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
	a := &appModel{store: store, contacts: contactsFromStore(store), selected: 0}
	a.loadStoredTaskContexts(store)
	a.bindTaskContexts()
	a.openSettings()
	if !a.saveContactProfileDraft(profileDraft{Name: "Legacy renamed"}, false) {
		t.Fatalf("could not save future defaults over v6: %+v", a.contactProfile)
	}
	reloaded, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.state
	if got.Version != 7 || got.Selected != legacy.Selected || len(got.Contacts) != 1 || len(got.Issues) != 1 {
		t.Fatalf("v6 upgrade lost ownership: %+v", got)
	}
	ct := got.Contacts[0]
	if ct.Name != "Legacy renamed" || ct.SessionID != legacy.Contacts[0].SessionID || ct.Draft != legacy.Contacts[0].Draft ||
		!reflect.DeepEqual(ct.TaskContext, legacy.Contacts[0].TaskContext) || !reflect.DeepEqual(ct.PendingSend, legacy.Contacts[0].PendingSend) {
		t.Error("profile save over v6 changed the current session, draft, Context or unconfirmed send")
	}
	issue := got.Issues[0]
	want := legacy.Issues[0]
	if issue.ID != want.ID || issue.ContactID != want.ContactID || issue.WorkSessionID != want.WorkSessionID || issue.RequirementsRevision != want.RequirementsRevision ||
		!reflect.DeepEqual(issue.RequirementItems, want.RequirementItems) || !reflect.DeepEqual(issue.TaskContext, want.TaskContext) ||
		!reflect.DeepEqual(issue.Executions, want.Executions) || !reflect.DeepEqual(issue.Supplements, want.Supplements) {
		t.Error("v6 upgrade changed requirements, execution snapshots or supplement receipt associations")
	}
}

func TestContactManagementGUISaveFailureArchiveRestoreAndRestart(t *testing.T) {
	provider := newGatedProvider()
	h := openContactManagementGUI(t, provider)
	original := profileDraft{Name: "Persistent contact", Purpose: "original purpose", Workspace: t.TempDir()}
	saveContactManagementProfile(t, h, original, false)
	sessionID := sendConnectionConversation(t, h, "Archive retains this conversation", "gated result")
	var contactID, issueID, path string
	var stateBefore []byte
	var pending taskContextItem
	h.pump.do(func() {
		ct := h.app.selectedContact()
		contactID = ct.ID
		if !h.app.createIssue("Retained pending issue", "Keep these requirements") {
			t.Error(h.app.issueFormError)
			return
		}
		issueID = ct.SelectedIssueID
		ct.Draft = "unsent archived draft"
		var err error
		pending, err = ct.Context.AddSnippet("Kept input", "unsent archived material")
		if err != nil {
			t.Error(err)
		}
		path = h.app.store.path
		stateBefore, _ = json.Marshal(h.app.store.state)
	})
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	brokenStore := t.TempDir()
	h.pump.do(func() {
		h.app.store.path = brokenStore
		h.app.openSettings()
		h.app.contactProfile.Draft = profileDraft{Name: "Must roll back", Purpose: "unsaved purpose", Workspace: original.Workspace}
	})
	clickContactManagementAction(t, h, "Save and start new conversation")
	h.pump.do(func() {
		ct := h.app.selectedContact()
		stateAfter, _ := json.Marshal(h.app.store.state)
		if ct.Name != original.Name || ct.SessionID != sessionID || len(ct.PreviousSessions) != 0 || string(stateBefore) != string(stateAfter) || h.app.contactProfile.Draft.Name != "Must roll back" || h.app.contactProfile.Error == "" {
			t.Error("save failure changed the contact/store/session or discarded the repairable draft")
		}
		if h.app.archiveContact(contactID) || ct.Archived {
			t.Error("archive succeeded despite the failed store write")
		}
		h.app.store.path = path
	})
	diskAfter, err := os.ReadFile(path)
	if err != nil || string(diskAfter) != string(diskBefore) {
		t.Fatal("a failed save/archive changed the original store file")
	}
	h.pump.do(func() { h.app.openSettings() })
	clickContactManagementAction(t, h, "Archive")
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if !ct.Archived || ct.SessionID != sessionID || ct.Draft != "unsent archived draft" || !reflect.DeepEqual(ct.Context.snapshot(), []taskContextItem{pending}) || ct.issueByID(issueID) == nil {
			t.Error("archive discarded contact-owned data")
		}
		if h.app.send(ct, "must not dispatch") || h.app.runIssue(issueID) {
			t.Error("archived contact accepted a conversation or issue execution")
		}
	})
	h.shutdown(t)
	restarted := openConnectionHarness(t)
	restarted.pump.wait(func() bool {
		return restarted.app.runtimeStatus == runtimeStateReady && restarted.app.selectedContact().Loaded
	})
	restarted.pump.do(func() {
		ct := restarted.app.selectedContact()
		if ct.ID != contactID || !ct.Archived || ct.Draft != "unsent archived draft" || ct.SessionID != sessionID || !reflect.DeepEqual(ct.Context.snapshot(), []taskContextItem{pending}) || ct.issueByID(issueID) == nil {
			t.Error("restart lost the selected archived contact or owned drafts/history")
		}
		restarted.app.setContactScope("archived")
		restarted.app.contactSearch = "Persistent"
		if got := restarted.app.visibleContactIndices(); !reflect.DeepEqual(got, []int{0}) {
			t.Errorf("archived contact is not discoverable: %v", got)
		}
		restarted.app.openSettings()
	})
	clickContactManagementAction(t, restarted, "Restore")
	restarted.pump.do(func() {
		if restarted.app.selectedContact().Archived || restarted.app.selectedContact().ID != contactID {
			t.Error("restore did not keep the same contact")
		}
	})
	if provider.count() != 1 {
		t.Fatal("archive/restore/restart or rejected input called the provider")
	}
	if got := sendConnectionConversation(t, restarted, "Continue after restore", "gated result"); got != sessionID {
		t.Fatal("restore recreated the original conversation")
	}
}

func TestContactManagementGUIActiveIssueKeepsProfileSnapshot(t *testing.T) {
	provider := newGatedProvider("held-work-152")
	h := openContactManagementGUI(t, provider)
	t.Cleanup(func() { provider.release("held-work-152") })
	original := profileDraft{Name: "Active worker", Purpose: "old-active-purpose-152", Workspace: t.TempDir()}
	saveContactManagementProfile(t, h, original, false)
	sendConnectionConversation(t, h, "Initial independent conversation", "gated result")
	var issueID, contactID, sessionID string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		contactID, sessionID = ct.ID, ct.SessionID
		if !h.app.createIssue("held-work-152", "Preserve active snapshot") {
			t.Error(h.app.issueFormError)
			return
		}
		issueID = ct.SelectedIssueID
		if !h.app.runIssue(issueID) {
			t.Error(h.app.issueFormError)
		}
	})
	select {
	case <-provider.arrived["held-work-152"]:
	case <-time.After(10 * time.Second):
		t.Fatal("active issue did not reach the controlled provider")
	}
	h.pump.wait(func() bool { return h.app.selectedContact().issueByID(issueID).Work.ActiveTaskID != "" })
	next := profileDraft{Name: "Renamed active worker", Purpose: "new-active-purpose-152", Workspace: t.TempDir()}
	saveContactManagementProfile(t, h, next, false)
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if h.app.archiveContact(contactID) || h.app.saveContactProfileDraft(next, true) || ct.SessionID != sessionID || ct.Name != next.Name || ct.issueByID(issueID).Work.ActiveTaskID == "" {
			t.Error("editing future defaults interrupted, archived or replaced active work")
		}
	})
	if !strings.Contains(provider.body(1), original.Purpose) || strings.Contains(provider.body(1), next.Purpose) {
		t.Fatal("active request did not keep the original profile")
	}
	provider.release("held-work-152")
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted && !issue.Work.HistoryDirty
	})
	if provider.count() != 2 {
		t.Fatal("profile edit or protected new/archive action dispatched additional work")
	}
}
