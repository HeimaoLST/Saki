package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func builtinToolsForTest() []runtimeBuiltinTool {
	return []runtimeBuiltinTool{
		{Name: "bash", Description: "Run a command."},
		{Name: "edit", Description: "Edit an existing file."},
		{Name: "read", Description: "Read a file."},
		{Name: "write", Description: "Write a file."},
	}
}

func storedBuiltinToolsForTest(t *testing.T, path string) (present bool, raw string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Contacts []map[string]json.RawMessage `json:"contacts"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Contacts) != 1 {
		t.Fatalf("contacts = %d", len(state.Contacts))
	}
	value, ok := state.Contacts[0]["builtin_tools"]
	return ok, string(value)
}

func clickProfileAction(t *testing.T, tr *ui.Tester, label string) {
	t.Helper()
	if err := clickSettingsAction(tr, label); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
}

// This regression is expressed through the actual profile UI, not by assigning
// the draft field. The old UI fails because it has no built-in-tool entry.
func TestContactProfileShowsBuiltinToolSelection(t *testing.T) {
	a, ct := taskContextApp(t)
	a.connectionManaged = true
	a.runtimeStatus = runtimeStateReady
	a.runtimeBuiltinTools = builtinToolsForTest()
	a.runtimeBuiltinToolsLoaded = true
	a.openSettingsTab(settingsTabContact, settingsSourceRail)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Built-in tools") || !tr.HasText("Use host defaults (bash, edit, read, write)") {
		t.Fatalf("contact profile has no built-in tool selection entry: %v", tr.Texts())
	}
	if ct.BuiltinTools != nil {
		t.Fatalf("default contact unexpectedly has an explicit selection: %v", ct.BuiltinTools)
	}

	// Save the omitted/default value: the store must keep the field absent.
	clickProfileAction(t, tr, "Save changes")
	if ct.BuiltinTools != nil {
		t.Fatalf("saving default changed it into an explicit selection: %v", ct.BuiltinTools)
	}
	if present, raw := storedBuiltinToolsForTest(t, a.store.path); present {
		t.Fatalf("default selection was persisted as explicit %s", raw)
	}

	// Turn the host default into an explicit subset through real checkbox hits.
	clickProfileAction(t, tr, "Use host defaults (bash, edit, read, write)")
	for _, name := range []string{"bash", "edit", "write"} {
		clickProfileAction(t, tr, name)
	}
	if got := a.settingsContactEditor().Draft.BuiltinTools; got == nil || len(*got) != 1 || (*got)[0] != "read" {
		t.Fatalf("UI subset draft = %v", got)
	}
	clickProfileAction(t, tr, "Save changes")
	if ct.BuiltinTools == nil || len(*ct.BuiltinTools) != 1 || (*ct.BuiltinTools)[0] != "read" {
		t.Fatalf("saved subset = %v", ct.BuiltinTools)
	}

	// Empty the explicit subset. This is not the host default and must persist
	// as a non-nil empty slice.
	clickProfileAction(t, tr, "read")
	if got := a.settingsContactEditor().Draft.BuiltinTools; got == nil || len(*got) != 0 {
		t.Fatalf("UI empty draft = %v", got)
	}
	clickProfileAction(t, tr, "Save changes")
	if ct.BuiltinTools == nil || len(*ct.BuiltinTools) != 0 {
		t.Fatalf("saved empty selection = %v", ct.BuiltinTools)
	}
	if present, raw := storedBuiltinToolsForTest(t, a.store.path); !present || strings.TrimSpace(raw) != "[]" {
		t.Fatalf("explicit empty selection persisted as present=%v raw=%q", present, raw)
	}
	reloaded, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.state.Contacts) != 1 || reloaded.state.Contacts[0].BuiltinTools == nil || len(*reloaded.state.Contacts[0].BuiltinTools) != 0 {
		t.Fatalf("reloaded empty selection = %+v", reloaded.state.Contacts)
	}
}

func TestContactProfileBuiltinChecksAreKeyboardReachable(t *testing.T) {
	editor := newContactProfileEditor(profileDraftForTest(t))
	editor.BuiltinTools = builtinToolsForTest()
	editor.BuiltinToolsLoaded = true
	editor.BuiltinToolsSupported = true
	tr := contactProfileTester(editor, contactProfileActions{}, 560, 1200)
	tr.Frame()
	for i := 0; i < 20 && !tr.Focused("Use host defaults (bash, edit, read, write)"); i++ {
		tr.Key(0, ui.KeyTab)
		tr.Frame()
	}
	if !tr.Focused("Use host defaults (bash, edit, read, write)") {
		t.Fatalf("host-default checkbox was not keyboard reachable: %v", tr.Texts())
	}
	tr.Key(0, ui.KeySpace)
	tr.Frame()
	if editor.Draft.BuiltinTools == nil || len(*editor.Draft.BuiltinTools) != 4 {
		t.Fatalf("Space did not turn the default into explicit all: %v", editor.Draft.BuiltinTools)
	}
	for i := 0; i < 8 && !tr.Focused("read"); i++ {
		tr.Key(0, ui.KeyTab)
		tr.Frame()
	}
	if !tr.Focused("read") {
		t.Fatal("individual tool checkbox is not keyboard reachable")
	}
	tr.Key(0, ui.KeySpace)
	tr.Frame()
	if builtinToolSelectionContains(editor.Draft.BuiltinTools, "read") {
		t.Fatal("Space did not disable the focused read tool")
	}
}

func TestContactStorePreservesDefaultAndExplicitEmptyBuiltinTools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	legacy := storedState{Version: 9, Selected: "c1", Contacts: []storedContact{{ID: "c1", Name: "Saki"}}}
	if err := writeStoredState(path, legacy); err != nil {
		t.Fatal(err)
	}
	store, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	ct := contactsFromStore(store)[0]
	if ct.BuiltinTools != nil {
		t.Fatalf("missing field did not use Runtime default: %v", ct.BuiltinTools)
	}
	if err := store.save([]*contact{ct}, "c1"); err != nil {
		t.Fatal(err)
	}
	if present, raw := storedBuiltinToolsForTest(t, path); present {
		t.Fatalf("default save wrote explicit built-ins: %s", raw)
	}

	empty := []string{}
	ct.BuiltinTools = &empty
	if err := store.save([]*contact{ct}, "c1"); err != nil {
		t.Fatal(err)
	}
	if present, raw := storedBuiltinToolsForTest(t, path); !present || strings.TrimSpace(raw) != "[]" {
		t.Fatalf("explicit empty store shape = present=%v raw=%q", present, raw)
	}
	reloaded, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	got := contactsFromStore(reloaded)[0].BuiltinTools
	if got == nil || len(*got) != 0 {
		t.Fatalf("reloaded explicit empty = %#v", got)
	}
}

func TestContactProfileBuiltinSelectionIsPerContact(t *testing.T) {
	a, first := taskContextApp(t)
	a.connectionManaged = true
	second := &contact{ID: "second", Name: "Mio", Role: "Assistant"}
	read := []string{"read"}
	second.BuiltinTools = &read
	a.contacts = append(a.contacts, second)
	a.runtimeStatus = runtimeStateReady
	a.runtimeBuiltinTools = builtinToolsForTest()
	a.runtimeBuiltinToolsLoaded = true
	empty := []string{}
	if !a.saveContactProfileDraftFor(first.ID, profileDraft{Name: first.Name, BuiltinTools: &empty}, false) {
		t.Fatalf("save first = %s", a.profileEditorFor(first.ID).Error)
	}
	if second.BuiltinTools == nil || len(*second.BuiltinTools) != 1 || (*second.BuiltinTools)[0] != "read" {
		t.Fatalf("saving first changed second: %v", second.BuiltinTools)
	}
	if first.BuiltinTools == nil || len(*first.BuiltinTools) != 0 {
		t.Fatalf("first selection = %#v", first.BuiltinTools)
	}
}

func TestContactProfileBuiltinSaveFailureKeepsDraft(t *testing.T) {
	a, ct := taskContextApp(t)
	a.connectionManaged = true
	a.runtimeStatus = runtimeStateReady
	a.runtimeBuiltinTools = builtinToolsForTest()
	a.runtimeBuiltinToolsLoaded = true
	a.store.path = t.TempDir()
	a.openSettingsTab(settingsTabContact, settingsSourceRail)
	editor := a.settingsContactEditor()
	read := []string{"read"}
	editor.Draft.BuiltinTools = &read
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := clickSettingsAction(tr, "Save changes"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if ct.BuiltinTools != nil {
		t.Fatalf("save failure committed selection: %v", ct.BuiltinTools)
	}
	if editor.Draft.BuiltinTools == nil || len(*editor.Draft.BuiltinTools) != 1 || (*editor.Draft.BuiltinTools)[0] != "read" {
		t.Fatalf("save failure lost selection draft: %v", editor.Draft.BuiltinTools)
	}
	if !strings.Contains(editor.Error, "Could not save") {
		t.Fatalf("save failure error = %q", editor.Error)
	}
}

func TestBuiltinToolCatalogErrorDoesNotOfferDeadRetry(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.connectionManaged = true
	a.runtimeStatus = runtimeStateFailed
	a.runtimeBuiltinToolsErr = "Runtime tool list unavailable."
	a.openSettingsTab(settingsTabContact, settingsSourceRail)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Runtime tool list unavailable.") || tr.HasText("Retry built-in tools") {
		t.Fatalf("unavailable runtime rendered a dead retry: %v", tr.Texts())
	}

	a.runtimeStatus = runtimeStateReady
	a.rt = &runtimeClient{}
	a.runtimeBuiltinToolsLoaded = false
	a.runtimeBuiltinToolsErr = "Catalog could not be read."
	tr.Frame()
	if !tr.HasText("Catalog could not be read.") || !tr.HasText("Retry built-in tools") {
		t.Fatalf("available runtime did not expose retry: %v", tr.Texts())
	}
}

func TestModelPanelShowsEffectiveBuiltinTools(t *testing.T) {
	render := func(builtin []string) *ui.Tester {
		config := runtimeConnectionConfig{SessionID: "s1", ConfigRevision: 3}
		config.EffectiveConfig.Model = "fixture"
		config.EffectiveConfig.ToolSelection.Builtin = builtin
		selection := newModelSelection(modelSelectionTarget{Kind: "conversation", Label: "Saki", Config: config}, runtimeModelCatalog{})
		return ui.NewTester(func(c *ui.Context) {
			theme := resolveWorkbenchTheme(false).Theme
			c.SetTheme(theme)
			ui.Scroll(c).Fill().Padding(16).Children(func() {
				selection.view(c, theme, modelSelectionActions{})
			})
		}, 1040, 720)
	}
	tr := render([]string{"read"})
	tr.Frame()
	if !tr.HasText("Effective built-in tools: read") {
		t.Fatalf("effective tool selection missing: %v", tr.Texts())
	}
	tr = render(nil)
	tr.Frame()
	if !tr.HasText("Effective built-in tools: none") {
		t.Fatalf("empty effective tool selection missing: %v", tr.Texts())
	}
}

func TestFirstUseBuiltinSelectionRequiresManagedConnection(t *testing.T) {
	a, ct := taskContextApp(t)
	a.connectionManaged = false
	a.runtimeStatus = runtimeStateReady
	a.runtimeBuiltinTools = builtinToolsForTest()
	a.runtimeBuiltinToolsLoaded = true
	read := []string{"read"}
	if a.saveContactProfileDraftFor(ct.ID, profileDraft{Name: ct.Name, BuiltinTools: &read}, false) {
		t.Fatal("legacy connection accepted an explicit built-in selection")
	}
	if ct.BuiltinTools != nil {
		t.Fatalf("legacy rejection changed profile: %v", ct.BuiltinTools)
	}
	if got := a.capturedBuiltinSelection(ct); got != nil {
		t.Fatalf("legacy nil default became explicit: %v", got)
	}
	a.openSettingsTab(settingsTabContact, settingsSourceRail)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Save and Apply a managed Nagi connection") || tr.HasText("Use host defaults") {
		t.Fatalf("first use did not explain the managed-connection requirement: %v", tr.Texts())
	}
}

func TestPreviewBuiltinToolSelection(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, size := range []struct {
			name string
			w, h int
		}{{"1040x720", 1040, 720}, {"720x640", 720, 640}} {
			a, _ := taskContextApp(t)
			a.connectionManaged = true
			a.windowWidth = size.w
			a.runtimeStatus = runtimeStateReady
			a.runtimeBuiltinTools = builtinToolsForTest()
			a.runtimeBuiltinToolsLoaded = true
			a.openSettingsTab(settingsTabContact, settingsSourceRail)
			read := []string{"read"}
			a.settingsContactEditor().Draft.BuiltinTools = &read
			tr := ui.NewTester(a.view, size.w, size.h)
			tr.SetDark(dark)
			tr.Frame()
			for attempt := 0; attempt < 32; attempt++ {
				header, headerOK := tr.Find("Built-in tools")
				last, lastOK := tr.Find("write")
				if headerOK && header.Y >= 0 && header.Y < 120 && lastOK && last.Y >= 0 && last.Y+last.H <= float32(size.h) {
					break
				}
				tr.Scroll(float32(size.w)/2, float32(size.h)/2, 0, 120)
				tr.Frame()
			}
			writePreviewPNG(t, filepath.Join(dir, "builtin-tool-selection-"+theme+"-"+size.name+".png"), tr.Image())
		}
	}
}

func TestBuiltinCatalogRetryUsesRuntimeAndKeepsDraft(t *testing.T) {
	a, ct := taskContextApp(t)
	a.connectionManaged = true
	a.runtimeStatus = runtimeStateReady
	selected := []string{"fixture-read"}
	ct.BuiltinTools = &selected
	a.openSettingsTab(settingsTabContact, settingsSourceRail)
	editor := a.settingsContactEditor()
	editor.Draft.Name = "Unsaved profile"
	callbacks := make(chan func(), 8)
	a.setUIHook(func(fn func()) { callbacks <- fn })
	runtime := startTestRuntime(t, "RUNTIME_TOOLS_FAIL_FIRST=1")
	a.rt = runtime.client
	consume := func() {
		t.Helper()
		select {
		case fn := <-callbacks:
			fn()
		case <-time.After(5 * time.Second):
			t.Fatal("tool catalog UI callback did not arrive")
		}
	}
	request := func(want string) {
		t.Helper()
		select {
		case got := <-runtime.stderr:
			if got != "tools-fixture:"+want {
				t.Fatalf("unexpected RPC: %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("tool catalog RPC trace did not arrive")
		}
	}
	a.loadBuiltinTools(runtime.client, a.connectionGeneration)
	consume()
	request("tool.list")
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if a.runtimeBuiltinToolsLoaded || !tr.HasText("controlled tool catalog failure") || !tr.HasText("Retry built-in tools") {
		t.Fatalf("catalog failure is not recoverable: %v", tr.Texts())
	}
	if err := clickSettingsAction(tr, "Retry built-in tools"); err != nil {
		t.Fatal(err)
	}
	consume()
	request("tool.list")
	tr.Frame()
	if !a.runtimeBuiltinToolsLoaded || a.runtimeBuiltinToolsErr != "" || !tr.HasText("fixture-read") || tr.HasText("Retry built-in tools") {
		t.Fatalf("retry did not use the returned catalog: %v", tr.Texts())
	}
	if editor.Draft.Name != "Unsaved profile" || !builtinToolsEqual(editor.Draft.BuiltinTools, &selected) || ct.Name == editor.Draft.Name {
		t.Fatal("catalog failure/retry changed the draft or saved profile")
	}
	if err := runtime.client.shutdown(); err != nil {
		t.Fatal(err)
	}
	request("runtime.shutdown")
}
