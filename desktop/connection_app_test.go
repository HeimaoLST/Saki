package main

import (
	"os"
	"path/filepath"
	"testing"
)

func isolateConnectionApp(t *testing.T, root, binary string) {
	t.Helper()
	for key := range filteredConnectionEnv {
		t.Setenv(key, "")
	}
	t.Setenv("SAKI_GUI_STORE", filepath.Join(root, "gui", "contacts.json"))
	t.Setenv("SAKI_GUI_WORKDIR", root)
	t.Setenv("SAKI_SESSION_DIR", filepath.Join(root, "sessions"))
	t.Setenv("SAKI_RUNTIME_BIN", binary)
}

func TestConnectionFirstUseDoesNotPersistWrongDefaultWorkspace(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if a.connectionForm == nil || a.aux != "configuration" {
		t.Fatal("an empty profile must open the usable configuration form")
	}
	if _, err := os.Stat(guiStorePath()); !os.IsNotExist(err) {
		t.Fatal("first use persisted a contact before the user selected a working directory")
	}
	if a.rt != nil {
		t.Fatal("first use started an unconfigured helper")
	}
}

func TestConnectionLateReadyDoesNotReopenDrainingApp(t *testing.T) {
	a := &appModel{rt: &runtimeClient{}, runtimeStatus: runtimeStateDraining, shutdownStarted: true}
	a.handleRuntimeReady(0, runtimeReady{API: connectionAPIResponses})
	if a.runtimeStatus != runtimeStateDraining || a.runtimeHandshake {
		t.Fatal("a late ready notification reopened the app during quit")
	}
}

func TestConnectionStoreCreationFailureCanBeRepaired(t *testing.T) {
	isolateConnectionApp(t, t.TempDir(), "")
	h := openConnectionHarness(t)
	if err := os.MkdirAll(guiStorePath(), 0o700); err != nil {
		t.Fatal(err)
	}
	config := testConnectionConfig(t)
	h.pump.do(func() { h.app.saveConnection(config) })
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	h.pump.do(func() {
		if h.app.connectionForm.Error == "" || h.app.connectionReloadBlocked() == "" {
			t.Error("a failed contact store did not stop Apply")
		}
		h.app.applyConnection()
		if h.app.connectionApplying || h.app.rt != nil {
			t.Error("Apply started work without a usable contact store")
		}
	})
	if _, err := loadConnectionConfig(connectionConfigPath(guiStorePath())); err != nil {
		t.Fatal("the successfully saved configuration was lost")
	}
	if err := os.Remove(guiStorePath()); err != nil {
		t.Fatal(err)
	}
	saveAppConnection(t, h, config)
	h.pump.do(func() {
		if h.app.store == nil || h.app.connectionReloadBlocked() != "" {
			t.Error("Save did not recover after repairing the contact store path")
		}
	})
}

func TestConnectionSavedDefaultsDoNotRewriteExistingContact(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	oldWorkspace := t.TempDir()
	store, err := openContactStore(guiStorePath(), oldWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	store.state.Contacts[0].Draft = "retain the existing draft"
	if err := store.saveState(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(guiStorePath())
	if err != nil {
		t.Fatal(err)
	}
	config := testConnectionConfig(t)
	if err := saveConnectionConfig(filepath.Join(filepath.Dir(guiStorePath()), "connection.json"), config); err != nil {
		t.Fatal(err)
	}
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if a.workDir != config.WorkingDirectory || a.contacts[0].Workspace != oldWorkspace || a.contacts[0].Draft != "retain the existing draft" {
		t.Fatal("loading defaults changed an existing contact instead of retaining its workspace and draft")
	}
	after, err := os.ReadFile(guiStorePath())
	if err != nil || string(after) != string(before) {
		t.Fatal("reading settings rewrote the contact store")
	}
}

func TestConnectionInvalidSavedSourceRemainsRepairable(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	config := testConnectionConfig(t)
	path := filepath.Join(filepath.Dir(guiStorePath()), "connection.json")
	if err := saveConnectionConfig(path, config); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(config.WorkingDirectory); err != nil {
		t.Fatal(err)
	}
	t.Setenv("API_KEY", "external-fixture-key")
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if a.connectionForm == nil || a.connectionForm.Draft.Model != config.Model || a.connectionForm.Draft.WorkingDirectory != config.WorkingDirectory || a.connectionForm.Error == "" {
		t.Fatal("invalid saved settings must open for correction with their previous values")
	}
	if a.rt != nil || a.aux != "configuration" {
		t.Fatal("invalid GUI configuration fell back to an unrelated external connection")
	}
}
