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
		restarted.app.searchContactHistoryAsync("saved budget-152")
	})
	restarted.pump.wait(func() bool { return !restarted.app.contactHistoryBusy && restarted.app.contactHistory != nil })
	var matches []contactHistoryMatch
	restarted.pump.do(func() { matches = append(matches, restarted.app.contactHistory.Matches...) })
	if len(matches) != 1 || matches[0].SessionID != oldSession {
		t.Fatalf("saved-history search lost the old conversation or included a different contact: %+v", matches)
	}
	restarted.pump.do(func() {
		if !restarted.app.openContactHistoryMatch(matches[0]) {
			t.Error("could not open a saved match")
		}
	})
	restarted.pump.wait(func() bool { return restarted.app.contactHistoryFull != nil && !restarted.app.contactHistoryBusy })
	restarted.pump.do(func() {
		if restarted.app.contactHistoryFull.Text != "first saved budget-152" || restarted.app.selectedContact().SessionID != newSession {
			t.Error("reading old history replaced the executable conversation or failed to locate its full message")
		}
	})
	if provider.count() != 5 {
		t.Fatal("restart/history read dispatched an unexpected model request")
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
