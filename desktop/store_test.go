package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContactStoreCreatesDefaultAndStableID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(path, "/tmp/workspace")
	if err != nil {
		t.Fatal(err)
	}
	if len(store.state.Contacts) != 1 {
		t.Fatalf("contacts = %d", len(store.state.Contacts))
	}
	first := store.state.Contacts[0]
	if first.Name != "Saki" || first.SessionID != "" || first.Workspace != "/tmp/workspace" {
		t.Fatalf("default contact = %+v", first)
	}
	if store.state.Selected != first.ID {
		t.Fatalf("selected = %q, want %q", store.state.Selected, first.ID)
	}
	reloaded, err := openContactStore(path, "/ignored")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Contacts[0].ID != first.ID {
		t.Fatalf("default contact id changed: %q -> %q", first.ID, reloaded.state.Contacts[0].ID)
	}
}

func TestContactStoreRoundTripKeepsProductState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "contacts.json")
	store, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	contacts := []*contact{
		{ID: "a", Name: "Saki", Role: "Assistant", Purpose: "p", Instructions: "i", Workspace: "/work", SessionID: "s-1", Draft: "draft a"},
		{ID: "b", Name: "New contact 2", Role: "Assistant", Draft: "draft b"},
	}
	if err := store.save(contacts, "b"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Selected != "b" || len(reloaded.state.Contacts) != 2 {
		t.Fatalf("state = %+v", reloaded.state)
	}
	got := reloaded.state.Contacts[0]
	if got.SessionID != "s-1" || got.Draft != "draft a" || got.Workspace != "/work" {
		t.Fatalf("contact = %+v", got)
	}
}

func TestContactStoreRejectsCorruptFileWithoutOverwriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	original := []byte("{not json")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := openContactStore(path, "/tmp")
	if err == nil || !strings.Contains(err.Error(), "not readable") {
		t.Fatalf("error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("corrupt store must not be replaced")
	}
}

func TestContactStoreRejectsDuplicateIDsAndVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contacts.json")
	state := storedState{Version: contactStoreVersion, Selected: "a", Contacts: []storedContact{{ID: "a"}, {ID: "a"}}}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openContactStore(path, "/tmp"); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate error = %v", err)
	}

	state = storedState{Version: 99, Selected: "a", Contacts: []storedContact{{ID: "a"}}}
	data, _ = json.Marshal(state)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openContactStore(path, "/tmp"); err == nil || !strings.Contains(err.Error(), "unsupported version") {
		t.Fatalf("version error = %v", err)
	}
}
