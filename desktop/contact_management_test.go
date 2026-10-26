package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContactManagementSaveUpdatesFutureProfileWithoutReplacingSession(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	workspace := t.TempDir()
	if _, err := bound.Context.AddSnippet("Keep", "keep context"); err != nil {
		t.Fatal(err)
	}
	before := len(bound.Messages)

	a.openSettings()
	if !a.saveContactProfileDraft(profileDraft{
		Name: "  Renamed  ", Purpose: "future purpose",
		Instructions: "line one\n  indented line", Workspace: workspace,
	}, false) {
		t.Fatalf("save failed: %+v", a.contactProfile)
	}
	if bound.Name != "Renamed" || bound.Purpose != "future purpose" || bound.Workspace != workspace {
		t.Fatalf("profile = %+v", bound)
	}
	if bound.Instructions != "line one\n  indented line" {
		t.Fatalf("instructions were rewritten: %q", bound.Instructions)
	}
	if bound.SessionID != "s1" || len(bound.Messages) != before || bound.Draft != "keep me" || len(bound.Context.snapshot()) != 1 {
		t.Fatalf("session-owned state changed: %+v", bound)
	}
	reloaded, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	stored := reloaded.state.Contacts[0]
	if stored.Name != "Renamed" || stored.Purpose != "future purpose" || stored.Instructions != bound.Instructions || stored.SessionID != "s1" {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestContactManagementInstructionsKeepOriginalWhitespaceInPrompt(t *testing.T) {
	ct := &contact{Name: "Saki", Instructions: "  Keep indentation\n\tand this line exactly.  "}
	prompt := profileSystemPrompt(ct)
	if !strings.Contains(prompt, "Instructions: "+ct.Instructions) {
		t.Fatalf("profile prompt rewrote instructions: %q", prompt)
	}
}

func TestContactManagementSaveAndNewDetachesOnlyCurrentConversation(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	workspace := t.TempDir()
	if _, err := bound.Context.AddSnippet("Keep", "unsent context"); err != nil {
		t.Fatal(err)
	}
	issue := &productIssue{ID: "issue-1", ContactID: bound.ID, Goal: "keep issue", WorkSessionID: "issue-session"}
	bound.Issues = []*productIssue{issue}
	bound.SelectedWorkTaskID = "old-work"

	a.openSettings()
	if !a.saveContactProfileDraft(profileDraft{Name: "Renamed", Purpose: "new purpose", Workspace: workspace}, true) {
		t.Fatalf("save and new failed: %+v", a.contactProfile)
	}
	if bound.SessionID != "" || len(bound.Messages) != 0 || len(bound.History) != 0 {
		t.Fatalf("current conversation was not detached: %+v", bound)
	}
	if len(bound.PreviousSessions) != 1 || bound.PreviousSessions[0] != "s1" {
		t.Fatalf("old session reference = %+v", bound.PreviousSessions)
	}
	if bound.Draft != "keep me" || len(bound.Context.snapshot()) != 1 {
		t.Fatal("save and new consumed the unsent draft or context")
	}
	if len(bound.Issues) != 1 || bound.Issues[0].WorkSessionID != "issue-session" || bound.Issues[0].Goal != "keep issue" {
		t.Fatalf("issues changed: %+v", bound.Issues)
	}
	if bound.SelectedWorkTaskID != "" || bound.SendState != sendIdle || bound.Status != statusReady {
		t.Fatalf("stale conversation state kept: %+v", bound)
	}
	reloaded, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	stored := reloaded.state.Contacts[0]
	if stored.SessionID != "" || len(stored.PreviousSessions) != 1 || stored.PreviousSessions[0] != "s1" {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestContactManagementSaveFailureRollsBackFieldsAndKeepsDraft(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	oldPurpose := bound.Purpose
	oldWorkspace := bound.Workspace
	broken := t.TempDir()
	a.store.path = broken // rename onto a directory must fail after the in-memory snapshot changes
	workspace := t.TempDir()

	a.openSettings()
	draft := profileDraft{Name: "Cannot Save", Purpose: "new purpose", Workspace: workspace}
	if a.saveContactProfileDraft(draft, false) {
		t.Fatal("save unexpectedly succeeded with an unusable store")
	}
	if bound.Name != "Saki" || bound.Purpose != oldPurpose || bound.Workspace != oldWorkspace {
		t.Fatalf("failed save changed the contact: %+v", bound)
	}
	if a.contactProfile == nil || a.contactProfile.Error == "" || a.contactProfile.Draft.Name != "Cannot Save" {
		t.Fatalf("failed save did not report an error or preserve the draft: %+v", a.contactProfile)
	}
	if a.contactProfile.Status != "" {
		t.Fatal("failed save reported success")
	}
	if a.store.state.Contacts[0].Name != "Saki" {
		t.Fatal("failed save left the store's in-memory snapshot changed")
	}
}

func TestContactManagementSaveAndNewFailureKeepsOldSessionReference(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	broken := t.TempDir()
	a.store.path = broken
	workspace := t.TempDir()
	a.openSettings()
	if a.saveContactProfileDraft(profileDraft{Name: "Renamed", Workspace: workspace}, true) {
		t.Fatal("save and new unexpectedly succeeded")
	}
	if bound.SessionID != "s1" || len(bound.PreviousSessions) != 0 || len(bound.Messages) != 1 {
		t.Fatalf("failed save and new changed the current conversation: %+v", bound)
	}
}

func TestContactManagementArchiveRestorePersistsAndBlocksActiveWork(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	bound.SendState = sendSubmitting
	a.openSettings()
	if a.archiveContact(bound.ID) {
		t.Fatal("archive unexpectedly succeeded with a loaded conversation")
	}
	if bound.Archived || a.contactArchiveBlocked(bound) == "" {
		t.Fatal("archive did not explain the active work")
	}

	bound.SessionID = ""
	bound.Loaded = false
	bound.SendState = sendIdle
	if !a.archiveContact(bound.ID) {
		t.Fatalf("archive failed: %+v", a.contactProfile)
	}
	if !bound.Archived || bound.ArchivedAt == 0 {
		t.Fatal("contact was not archived")
	}
	if !a.contactProfile.Archived || a.contactScope != "archived" {
		t.Fatal("archive did not switch the visible read-only state")
	}
	reloaded, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.state.Contacts[0].Archived || reloaded.state.Contacts[0].ArchivedAt == 0 {
		t.Fatal("archive was not persisted")
	}
	if reason := a.canSend(bound); !strings.Contains(reason, "archived") {
		t.Fatalf("send guard = %q", reason)
	}
	if !a.restoreContact(bound.ID) || bound.Archived || bound.ArchivedAt != 0 {
		t.Fatalf("restore failed: %+v", bound)
	}
	if a.contactScope != "active" {
		t.Fatal("restore did not return to the active scope")
	}
}

func TestContactManagementSidebarScopeAndSearchKeepStableIndices(t *testing.T) {
	a, bound, unbound := testRuntimeModel(t)
	archive := &contact{ID: "archive", Name: "Archive", Purpose: "old budget notes", Archived: true, Status: statusReady}
	a.contacts = append(a.contacts, archive)
	a.contactScope = "active"
	a.contactSearch = "purpose"
	if got := a.visibleContactIndices(); len(got) != 1 || a.contacts[got[0]] != bound {
		t.Fatalf("active search = %v", got)
	}
	a.contactSearch = "budget"
	if got := a.visibleContactIndices(); len(got) != 0 {
		t.Fatalf("archived contact leaked into active search: %v", got)
	}
	a.setContactScope("archived")
	if got := a.visibleContactIndices(); len(got) != 1 || a.contacts[got[0]] != archive {
		t.Fatalf("archived search = %v", got)
	}
	a.contactScope = "all"
	a.contactSearch = "new contact"
	if got := a.visibleContactIndices(); len(got) != 1 || a.contacts[got[0]] != unbound {
		t.Fatalf("all search = %v", got)
	}
}

func TestContactStoreReadsVersionSixWithoutNewFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contacts.json")
	legacy := `{"version":6,"selected":"c1","contacts":[{"id":"c1","name":"Old","session_id":"s1"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if store.state.Version != 6 || len(store.state.Contacts) != 1 {
		t.Fatalf("state = %+v", store.state)
	}
	ct := store.state.Contacts[0]
	if ct.Archived || len(ct.PreviousSessions) != 0 || ct.SessionID != "s1" {
		t.Fatalf("legacy contact = %+v", ct)
	}
}

func TestContactManagementNonDemoWithoutStoreNeverClaimsPersistence(t *testing.T) {
	ct := &contact{ID: "c1", Name: "Saki", Workspace: t.TempDir(), Status: statusReady}
	a := &appModel{contacts: []*contact{ct}, selected: 0, scrollTo: -1}
	a.openSettings()
	if a.saveContactProfileDraft(profileDraft{Name: "Renamed", Workspace: ct.Workspace}, false) {
		t.Fatal("save without a contact store claimed success")
	}
	if ct.Name != "Saki" || a.contactProfile == nil || a.contactProfile.Error == "" {
		t.Fatalf("save without a store mutated or failed to report: ct=%+v editor=%+v", ct, a.contactProfile)
	}
	ct.Archived = true
	if a.restoreContact(ct.ID) || !ct.Archived {
		t.Fatal("restore without a contact store claimed success")
	}
	ct.Archived = false
	if a.archiveContact(ct.ID) || ct.Archived {
		t.Fatal("archive without a contact store claimed success")
	}
}

func TestContactManagementShutdownWaitsForHistoryCallback(t *testing.T) {
	ct := &contact{ID: "c1", Name: "Saki"}
	quit := false
	a := &appModel{
		contacts: []*contact{ct}, selected: 0, shutdownStarted: true,
		contactHistoryInFlight: 1, requestQuit: func() { quit = true },
	}
	a.finishShutdown()
	if a.quitReady || quit {
		t.Fatal("shutdown closed the UI while a history callback was still in flight")
	}
	a.historyAsyncFinished()
	if !a.quitReady || !quit {
		t.Fatal("the final history callback did not finish shutdown")
	}
}
