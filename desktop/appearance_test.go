package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestAppearanceModeResolutionUsesDarkTheme(t *testing.T) {
	light := resolveWorkbenchTheme(false)
	dark := resolveWorkbenchTheme(true)
	if light.Theme.Dark {
		t.Fatal("light workbench theme is marked dark")
	}
	if !dark.Theme.Dark {
		t.Fatal("dark workbench theme is not marked dark")
	}
	if resolveAppearanceDark(appearanceSystem, true) != true ||
		resolveAppearanceDark(appearanceSystem, false) != false ||
		resolveAppearanceDark(appearanceLight, true) != false ||
		resolveAppearanceDark(appearanceDark, false) != true {
		t.Fatal("appearance mode did not resolve to the requested/system appearance")
	}
}

func TestAppearanceFirstLaunchCreatesExistingStoreWithoutLosingCurrentContact(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "contacts.json")
	t.Setenv("SAKI_GUI_STORE", storePath)
	t.Setenv("SAKI_GUI_WORKDIR", dir)
	t.Setenv("SAKI_SESSION_DIR", filepath.Join(dir, "sessions"))
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if a.store != nil {
		t.Fatal("unconfigured first launch unexpectedly had a contact store")
	}
	ct := a.selectedContact()
	id := ct.ID
	ct.Name = "First launch contact"
	ct.Draft = "keep this draft across appearance save"
	a.setAppearanceMode(appearanceDark)
	if a.store == nil {
		t.Fatal("appearance save did not lazily initialize the existing GUI store")
	}
	if a.appearanceError != "" {
		t.Fatalf("first-launch appearance save error = %q", a.appearanceError)
	}

	restarted, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if restarted.appearanceMode != appearanceDark {
		t.Fatalf("restarted appearance = %q", restarted.appearanceMode)
	}
	got := restarted.selectedContact()
	if got.ID != id || got.Draft != ct.Draft || got.Name != ct.Name {
		t.Fatalf("lazy store changed contact = id=%q draft=%q name=%q", got.ID, got.Draft, got.Name)
	}
	got.Name = "Renamed after appearance"
	if err := restarted.persist(); err != nil {
		t.Fatal(err)
	}
	afterContactSave, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if afterContactSave.appearanceMode != appearanceDark || afterContactSave.selectedContact().Name != got.Name {
		t.Fatalf("contact save lost appearance/name = mode=%q name=%q", afterContactSave.appearanceMode, afterContactSave.selectedContact().Name)
	}
}

func TestAppearanceSwitchKeepsComposerFocusAndActiveTask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{store: store, storePath: path, appearanceMode: appearanceSystem}
	applyStoreSelection(a, store)
	ct := a.selectedContact()
	ct.Draft = "focus and task survive"
	ct.ActiveTaskID = "task-keep"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	focusComposer(t, tr, "Composer for "+ct.Name)
	a.setAppearanceMode(appearanceDark)
	tr.Frame()
	if !tr.Focused("Composer for " + ct.Name) {
		t.Fatal("appearance switch moved focus away from the composer")
	}
	if a.selectedContact().Draft != "focus and task survive" || a.selectedContact().ActiveTaskID != "task-keep" {
		t.Fatal("appearance switch changed the draft or active task")
	}
}

func TestAppearanceFailedSaveKeepsModeAndRetryClearsWarning(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &contactStore{path: filepath.Join(blocked, "contacts.json")}
	a := &appModel{
		store:            store,
		selected:         0,
		appearanceMode:   appearanceSystem,
		contacts:         []*contact{{ID: "contact-keep", Name: "Saki", Draft: "draft-keep"}},
		issueToolLedger:  &IssueToolOperationLedger{},
		issueToolScopes:  map[string]IssueToolScope{},
		issueToolWaiters: map[string]chan IssueToolReceipt{},
	}
	a.setAppearanceMode(appearanceDark)
	if a.appearanceMode != appearanceDark {
		t.Fatalf("failed save reverted mode = %q", a.appearanceMode)
	}
	if !strings.Contains(a.appearanceError, "Applied but not saved") {
		t.Fatalf("failed save warning = %q", a.appearanceError)
	}
	if a.selectedContact().Draft != "draft-keep" {
		t.Fatal("failed appearance save changed the draft")
	}

	store.path = filepath.Join(t.TempDir(), "contacts.json")
	a.setAppearanceMode(appearanceDark)
	if a.appearanceMode != appearanceDark || a.appearanceError != "" {
		t.Fatalf("retry did not clear warning: mode=%q error=%q", a.appearanceMode, a.appearanceError)
	}
}

func TestContactScopeSelectAndComposerModelEntry(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Contact scope") || !tr.HasText("Active") {
		t.Fatalf("contact scope selector is missing: %q", tr.Texts())
	}
	if err := tr.Click("Active"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Archived"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.contactScope != "archived" {
		t.Fatalf("scope select = %q, want archived", a.contactScope)
	}

	a.selectContact(0)
	a.contacts[0].SessionID = ""
	a.contacts[0].Draft = "draft-kept"
	a.connectionEffective = &connectionConfig{Model: "model-default"}
	tr = ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Connection default · model-default") {
		t.Fatalf("unbound conversation default is not explicit: %q", tr.Texts())
	}
	if tr.HasText("Issue model") {
		t.Fatal("issue model entry leaked into the conversation composer")
	}
	if err := tr.Click("Conversation model"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.aux != "model" || !tr.HasText("This context has no Runtime session yet") {
		t.Fatalf("conversation model entry = aux=%q texts=%q", a.aux, tr.Texts())
	}
	if a.selectedContact().Draft != "draft-kept" {
		t.Fatal("opening conversation model changed the draft")
	}
}

func TestAppearanceLaterContactSaveClearsUnsavedWarning(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &contactStore{path: filepath.Join(blocked, "contacts.json")}
	a := &appModel{
		store: store, selected: 0, appearanceMode: appearanceSystem,
		contacts:        []*contact{{ID: "contact-keep", Name: "Saki"}},
		issueToolLedger: &IssueToolOperationLedger{}, issueToolScopes: map[string]IssueToolScope{},
	}
	a.setAppearanceMode(appearanceDark)
	if a.appearanceError == "" {
		t.Fatal("expected the failed appearance save warning")
	}
	store.path = filepath.Join(t.TempDir(), "contacts.json")
	a.contacts[0].Draft = "later contact save"
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	if a.appearanceError != "" || a.appearanceMode != appearanceDark {
		t.Fatalf("later save left stale warning: mode=%q error=%q", a.appearanceMode, a.appearanceError)
	}
}

func TestAppearancePanelSwitchesModeAndKeepsCurrentDraft(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &appModel{store: store, storePath: path, appearanceMode: appearanceSystem}
	applyStoreSelection(a, store)
	ct := a.selectedContact()
	const draftBeforeAppearance = "draft stays while appearance changes"
	ct.Draft = draftBeforeAppearance
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabAppearance || !tr.HasText("Appearance") || !tr.HasText("Effective:") {
		t.Fatalf("appearance panel missing: %q", tr.Texts())
	}
	if err := tr.Click("System"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Dark"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.appearanceMode != appearanceDark || a.appearanceError != "" {
		t.Fatalf("appearance switch = mode=%q error=%q", a.appearanceMode, a.appearanceError)
	}
	if a.workTheme.Theme == nil || !a.workTheme.Theme.Dark {
		t.Fatal("dark appearance did not take effect in the next frame")
	}
	if a.selectedContact().Draft != draftBeforeAppearance {
		t.Fatal("appearance switch changed the current draft")
	}
}

func TestAppearanceRetrySaveUIAndRuntimeErrorOwnership(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &contactStore{path: filepath.Join(blocked, "contacts.json")}
	a := &appModel{
		store: store, selected: 0, appearanceMode: appearanceSystem,
		contacts:        []*contact{{ID: "contact-keep", Name: "Saki", Draft: "keep"}},
		issueToolLedger: &IssueToolOperationLedger{}, issueToolScopes: map[string]IssueToolScope{},
		runtimeErr: "Runtime protocol error: unrelated",
	}
	a.openAppearance()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("System"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Light"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.appearanceMode != appearanceLight || a.appearanceError == "" {
		t.Fatalf("failed UI save = mode=%q error=%q", a.appearanceMode, a.appearanceError)
	}
	if !tr.HasText("Retry save") {
		t.Fatalf("retry action is not discoverable: %q", tr.Texts())
	}
	store.path = filepath.Join(t.TempDir(), "contacts.json")
	if err := tr.Click("Retry save"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.appearanceError != "" || a.appearanceMode != appearanceLight {
		t.Fatalf("retry left appearance state = mode=%q error=%q", a.appearanceMode, a.appearanceError)
	}
	if a.runtimeErr != "Runtime protocol error: unrelated" {
		t.Fatalf("recovery cleared an unrelated runtime error: %q", a.runtimeErr)
	}
	if a.selectedContact().Draft != "keep" {
		t.Fatal("appearance save retry changed the draft")
	}
}
