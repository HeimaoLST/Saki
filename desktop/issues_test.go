package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
