package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestSettingsPageReplacesWorkbenchAndRoutesEntries(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Draft = "keep the conversation draft"
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabAppearance {
		t.Fatalf("settings page = open=%v tab=%q", a.settingsOpen, a.settingsTab)
	}
	if a.settingsScroll.Y != 0 {
		t.Fatalf("settings page opened scrolled: %v", a.settingsScroll.Y)
	}
	for _, forbidden := range []string{"Contacts", "Add contact", "Composer for Saki", "Send", "History"} {
		if tr.HasText(forbidden) {
			t.Fatalf("settings page still rendered workbench text %q: %v", forbidden, tr.Texts())
		}
	}
	for _, want := range []string{"Settings", "Appearance", "Connection & model", "Contact profile", "Advanced", "Selected"} {
		if !tr.HasText(want) {
			t.Fatalf("settings page missing %q: %v", want, tr.Texts())
		}
	}
	if !tr.Focused("← Back to workbench") {
		t.Fatalf("settings page did not focus Back to workbench: %v", tr.Texts())
	}
	if err := tr.Click("Connection & model"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.settingsScroll.Y != 0 {
		t.Fatalf("connection category opened scrolled: %v", a.settingsScroll.Y)
	}
	if a.settingsTab != settingsTabConnection || !tr.HasText("No connection is currently applied.") {
		t.Fatalf("connection category = %q texts=%v", a.settingsTab, tr.Texts())
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.settingsOpen || !tr.Focused("Settings") {
		t.Fatalf("return from settings = open=%v settingsFocus=%v", a.settingsOpen, tr.Focused("Settings"))
	}
	if ct.Draft != "keep the conversation draft" {
		t.Fatalf("settings changed the conversation draft: %q", ct.Draft)
	}
}

func TestSettingsPageHeaderAndErrorEntriesRouteToCategories(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Contact profile"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabContact {
		t.Fatalf("header profile route = open=%v tab=%q", a.settingsOpen, a.settingsTab)
	}
	tr.Click("← Back to workbench")
	tr.Frame()

	ct.LoadErr = "connection_mismatch: session is bound elsewhere"
	tr.Frame()
	if err := tr.Click("Connection settings"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabConnection {
		t.Fatalf("connection entry route = open=%v tab=%q", a.settingsOpen, a.settingsTab)
	}
}

func TestSettingsPageCmdCommaFromWorkbenchHistoryAndResult(t *testing.T) {
	a := testApp(t)
	ct := a.selectedContact()
	for i := 0; i < 40; i++ {
		ct.History = append(ct.History, historyEntry{ID: fmt.Sprintf("history-%d", i), Date: "Oct 1", From: "contact", Text: fmt.Sprintf("history entry %d with enough text to make the history panel scroll", i)})
	}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabAppearance {
		t.Fatalf("Cmd+, workbench = open=%v tab=%q", a.settingsOpen, a.settingsTab)
	}
	tr.Click("← Back to workbench")
	tr.Frame()

	a.openHistory()
	tr.Frame()
	tr.Scroll(520, 400, 0, 600)
	tr.Frame()
	historyScrollBefore := a.historyScroll.Y
	if historyScrollBefore <= 0 {
		t.Fatalf("history did not scroll: %v", historyScrollBefore)
	}
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen || a.screen != viewHistory {
		t.Fatalf("Cmd+, history = open=%v screen=%v", a.settingsOpen, a.screen)
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.screen != viewHistory || a.historyScroll.Y != historyScrollBefore {
		t.Fatalf("history return = screen=%v scroll=%v want=%v", a.screen, a.historyScroll.Y, historyScrollBefore)
	}
	if !tr.Focused("Close history") {
		t.Fatalf("history return did not restore the history close control: %v", tr.Texts())
	}
}

func TestSettingsContactDraftsRemainPerContactAndDiscardIsLocal(t *testing.T) {
	a, ct := workspaceTestApp(t)
	other := &contact{ID: "other", Name: "Mio", Role: "Assistant", Status: statusReady}
	a.contacts = append(a.contacts, other)
	a.openSettingsTab(settingsTabContact, settingsSourceRail)
	editorA := a.settingsContactEditor()
	editorA.Draft.Name = "A draft"
	a.settingsContactID = other.ID
	editorB := a.settingsContactEditor()
	editorB.Draft.Name = "B draft"
	a.settingsContactID = ct.ID
	if got := a.settingsContactEditor().Draft.Name; got != "A draft" {
		t.Fatalf("A draft = %q", got)
	}
	if a.selectedContact().ID != ct.ID {
		t.Fatal("settings contact picker changed the workbench selection")
	}
	tr := ui.NewTester(a.view, 1040, 720)
	if err := clickSettingsAction(tr, "Discard changes"); err != nil {
		t.Fatal(err)
	}
	if a.settingsContactEditor().Draft.Name != "Saki" || editorB.Draft.Name != "B draft" {
		t.Fatalf("discard scope = A=%q B=%q", a.settingsContactEditor().Draft.Name, editorB.Draft.Name)
	}
	if ct.Draft != "" {
		t.Fatalf("settings discard cleared conversation draft: %q", ct.Draft)
	}
}

func TestSettingsCategoryChangeResetsContentScrollAndKeepsDraft(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.openSettingsTab(settingsTabContact, settingsSourceRail)
	editor := a.settingsContactEditor()
	editor.Draft.Name = "unsaved name"
	a.settingsScroll.Y = 200
	a.settingsFocusTab = settingsTabContact
	a.selectSettingsTab(settingsTabAdvanced)
	if a.settingsScroll.Y != 0 || a.settingsTab != settingsTabAdvanced {
		t.Fatalf("category switch = scroll=%v tab=%q", a.settingsScroll.Y, a.settingsTab)
	}
	if !strings.Contains(editor.Draft.Name, "unsaved") {
		t.Fatal("category switch discarded the contact draft")
	}
}

func TestSettingsConnectionDirtyDiscardAndAppliedReopen(t *testing.T) {
	config := connectionConfig{Version: 1, API: connectionAPIChatCompletions, Endpoint: "http://example.test/v1", APIKey: "fixture", Model: "fixture", ContextTokens: 1000, OutputTokens: 100, WorkingDirectory: t.TempDir(), MaxActiveTasks: 2}
	a, _ := workspaceTestApp(t)
	a.connectionForm = newConnectionSettings(&config, config.WorkingDirectory)
	a.connectionSaved = &config
	a.connectionEffective = &config
	a.rt = &runtimeClient{}
	a.runtimeStatus = runtimeStateReady
	a.connectionForm.EffectiveSummary = connectionSummary(&config)
	a.openSettingsTab(settingsTabConnection, settingsSourceRail)
	if !a.connectionApplied() || a.connectionForm.Dirty() {
		t.Fatalf("applied reopen = applied=%v dirty=%v", a.connectionApplied(), a.connectionForm.Dirty())
	}
	a.connectionForm.Draft.Model = "changed"
	if !a.connectionForm.Dirty() {
		t.Fatal("connection edit was not marked dirty")
	}
	tr := ui.NewTester(a.view, 1040, 720)
	if err := clickSettingsAction(tr, "Discard changes"); err != nil {
		t.Fatal(err)
	}
	if a.connectionForm.Dirty() || a.connectionForm.Draft.Model != "fixture" {
		t.Fatalf("connection discard = %+v", a.connectionForm.Draft)
	}
}

func TestSettingsContactPickerFollowsEditingIdentityAfterReturnToA(t *testing.T) {
	a, aContact := workspaceTestApp(t)
	bContact := &contact{ID: "contact-bbbb2222", Name: aContact.Name, Role: "Assistant", Status: statusReady}
	a.contacts = append(a.contacts, bContact)
	a.openSettingsTab(settingsTabContact, settingsSourceRail)
	a.settingsContactID = bContact.ID
	a.settingsPickerChoice = a.settingsContactChoice(bContact)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !strings.Contains(strings.Join(tr.Texts(), "\n"), a.settingsContactChoice(bContact)) {
		t.Fatalf("picker/title did not show B: %v", tr.Texts())
	}
	a.closeSettings()
	tr.Frame()
	if err := tr.Click("Contact profile"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.settingsContactID != aContact.ID || a.settingsPickerChoice != a.settingsContactChoice(aContact) {
		t.Fatalf("A profile entry kept B picker: id=%q choice=%q want=%q", a.settingsContactID, a.settingsPickerChoice, a.settingsContactChoice(aContact))
	}
	texts := strings.Join(tr.Texts(), "\n")
	if !strings.Contains(texts, "Contact profile · "+a.settingsContactChoice(aContact)) {
		t.Fatalf("A profile title/picker mismatch: %v", tr.Texts())
	}
	if a.settingsContactChoice(aContact) == a.settingsContactChoice(bContact) {
		t.Fatal("same-name contacts did not get distinct readable labels")
	}
}

func TestSettingsPageCmdCommaFromIssueResultAndNestedReader(t *testing.T) {
	a := issuePreviewApp("issue-detail")
	issue := a.selectedContact().selectedIssue()
	execution := issue.Executions[0]
	file := issueResultFile{
		ExecutionID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID,
		MessageID: execution.MessageID, CallID: "call-result", ToolName: "write",
		Path: "/tmp/result.txt", WorkingDirectory: "/tmp", RawResult: `{"path":"/tmp/result.txt","bytes_written":1}`,
	}
	key := issueResultFileKey(file)
	a.aux = "issue-result"
	a.issueResult = issueResultPage{
		Open: true, OwnerContactID: a.selectedContact().ID, IssueID: issue.ID, ExecutionID: execution.ID,
		Panel: issueResultPanelState{
			RunLabel: "Run 1", RunStatus: "Completed", Files: []issueResultFile{file},
			ReaderKey: key, FocusReader: true,
		},
	}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("File reading") || !tr.Focused("Close reading") {
		t.Fatalf("nested reader did not receive focus: %v", tr.Texts())
	}
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabAppearance || a.aux != "issue-result" || !a.issueResult.Open {
		t.Fatalf("Cmd+, from reader = open=%v tab=%q aux=%q result=%v", a.settingsOpen, a.settingsTab, a.aux, a.issueResult.Open)
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.issueResult.Open || a.aux != "issue-result" || a.issueResult.Panel.ReaderKey != key {
		t.Fatalf("reader context lost: aux=%q open=%v reader=%q", a.aux, a.issueResult.Open, a.issueResult.Panel.ReaderKey)
	}
	if !tr.Focused("Close reading") {
		t.Fatalf("return did not focus the reader entry: %v", tr.Texts())
	}
}

func TestSettingsPageCmdCommaReturnsToFocusedComposer(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Draft = "before "
	a.focusComposer = true
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.Focused("Composer for Saki") {
		t.Fatal("composer did not receive initial focus")
	}
	tr.Type("settings")
	if ct.Draft != "before settings" {
		t.Fatalf("draft before Cmd+, = %q", ct.Draft)
	}
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen {
		t.Fatal("Cmd+, from composer did not open settings")
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Composer for Saki") {
		t.Fatalf("Back did not return focus to the composer: %v", tr.Texts())
	}
	tr.Type(" again")
	if ct.Draft != "before settings again" {
		t.Fatalf("typing after Back = %q", ct.Draft)
	}
}

func TestSettingsContactPickerDisambiguatesSameLastIDDigitsAndSavesB(t *testing.T) {
	store, err := openContactStore(filepath.Join(t.TempDir(), "contacts.json"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	aContact := &contact{ID: "contact-aaaa1111", Name: "Saki", Role: "Assistant", Status: statusReady, Workspace: t.TempDir()}
	bContact := &contact{ID: "contact-bbbb1111", Name: "Saki", Role: "Assistant", Status: statusReady, Workspace: t.TempDir()}
	a := &appModel{store: store, storePath: store.path, contacts: []*contact{aContact, bContact}, selected: 0, scrollTo: -1, windowWidth: 1040}
	a.openSettingsTab(settingsTabContact, settingsSourceRail)
	if a.settingsContactChoice(aContact) == a.settingsContactChoice(bContact) {
		t.Fatal("same-name contacts did not get distinct labels")
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if texts := strings.Join(tr.Texts(), "\n"); strings.Contains(texts, aContact.ID) || strings.Contains(texts, bContact.ID) {
		t.Fatalf("contact picker exposed raw IDs: %v", tr.Texts())
	}
	if err := tr.Click("Edit contact"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click(a.settingsContactChoice(bContact)); err != nil {
		t.Fatalf("picker B option: %v; texts=%v", err, tr.Texts())
	}
	tr.Frame()
	if a.settingsContactID != bContact.ID {
		t.Fatalf("picker chose id %q, want B %q", a.settingsContactID, bContact.ID)
	}
	if err := tr.Click("Contact name"); err != nil {
		t.Fatal(err)
	}
	tr.Key(ui.Cmd, ui.KeyA)
	tr.Type("Saved B")
	if err := clickSettingsAction(tr, "Save changes"); err != nil {
		t.Fatal(err)
	}
	if r, ok := tr.Find("Save changes"); !ok || r.H < 32 {
		t.Fatal("Save changes does not provide the minimum click target")
	}
	tr.Frame()
	if aContact.Name != "Saki" || bContact.Name != "Saved B" || a.selectedContact().ID != aContact.ID {
		t.Fatalf("B save scope changed: A=%q B=%q selected=%q", aContact.Name, bContact.Name, a.selectedContact().ID)
	}
}

func TestSettingsConnectionApplyAvailabilityFollowsCurrentWork(t *testing.T) {
	a, ct, _ := testRuntimeModel(t)
	config := connectionConfig{Version: 1, API: connectionAPIChatCompletions, Endpoint: "http://example.test/v1", APIKey: "fixture", Model: "fixture", ContextTokens: 1000, OutputTokens: 100, WorkingDirectory: t.TempDir(), MaxActiveTasks: 2}
	a.connectionForm = newConnectionSettings(&config, config.WorkingDirectory)
	a.connectionSaved = &config
	a.connectionEffective = &config
	a.rt = &runtimeClient{}
	a.runtimeStatus = runtimeStateReady
	ct.ActiveTaskID = "active-task"
	ct.SendState = sendAccepted
	a.openSettingsTab(settingsTabConnection, settingsSourceRail)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if a.connectionForm.ReloadBlocked == "" || !tr.HasText("Wait for all conversation and issue work to finish before applying a new connection.") {
		t.Fatalf("active work did not block Apply: %q / %v", a.connectionForm.ReloadBlocked, tr.Texts())
	}
	ct.ActiveTaskID = ""
	ct.SendState = sendIdle
	tr.Frame()
	if a.connectionForm.ReloadBlocked != "" {
		t.Fatalf("Apply did not recover after work completed: %q", a.connectionForm.ReloadBlocked)
	}
	if tr.HasText("Wait for all conversation and issue work to finish before applying a new connection.") {
		t.Fatal("stale Apply block remained after work completed")
	}
}

func TestSettingsPageCmdCommaFromIssueResultReturnsToResultControl(t *testing.T) {
	a := issuePreviewApp("issue-detail")
	issue := a.selectedContact().selectedIssue()
	execution := issue.Executions[0]
	a.aux = "issue-result"
	a.issueResult = issueResultPage{
		Open: true, OwnerContactID: a.selectedContact().ID, IssueID: issue.ID, ExecutionID: execution.ID,
		Panel: issueResultPanelState{RunLabel: "Run 1", RunStatus: "Completed", FocusClose: true},
	}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.Focused("Close results") {
		t.Fatalf("result control was not focused: %v", tr.Texts())
	}
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen {
		t.Fatal("Cmd+, did not open settings from result")
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.issueResult.Open || a.aux != "issue-result" || !tr.Focused("Close results") {
		t.Fatalf("result return lost context or focus: open=%v aux=%q texts=%v", a.issueResult.Open, a.aux, tr.Texts())
	}
}

func TestSettingsPageReturnKeepsChatScrollWorkspaceAndIssueContext(t *testing.T) {
	a, ct := workspaceTestApp(t)
	for i := 0; i < 30; i++ {
		ct.Messages = append(ct.Messages, &message{ID: i + 1, Kind: msgContact, Text: fmt.Sprintf("message %d with enough text to scroll", i)})
	}
	a.workspaceOpen = true
	a.rightPaneWidth = 388
	issue := &productIssue{ID: "issue-keep", ContactID: ct.ID, Goal: "keep context", Status: issueStatusInProgress, RequirementsRevision: 1}
	for i := 0; i < 30; i++ {
		issue.Requirements = append(issue.Requirements, &issueRequirement{ID: fmt.Sprintf("r-%d", i), Text: fmt.Sprintf("requirement %d with enough detail to make the issue pane scroll", i), State: requirementUnverified})
	}
	ct.Issues = []*productIssue{issue}
	openIssueDetailForTest(a, ct, issue.ID)
	nav := ct.issueNavigation()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	tr.Scroll(900, 300, 0, 400)
	tr.Frame()
	if nav.DetailScroll.Y <= 0 {
		t.Fatalf("issue detail did not scroll: %v", nav.DetailScroll.Y)
	}
	detailScrollBefore := nav.DetailScroll.Y
	tr.Scroll(500, 300, 0, -2000)
	tr.Frame()
	first, _ := ct.list.Visible()
	a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
	tr.Frame()
	if !a.settingsOpen {
		t.Fatal("settings did not open")
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	afterFirst, _ := ct.list.Visible()
	if afterFirst != first || a.selectedContact().ID != ct.ID || !a.workspaceOpen || a.rightPaneWidth != 388 || nav.DetailID != issue.ID || nav.DetailScroll.Y != detailScrollBefore {
		t.Fatalf("return state changed: list %d->%d selected=%q work=%v width=%v detail=%q scroll=%v", first, afterFirst, a.selectedContact().ID, a.workspaceOpen, a.rightPaneWidth, nav.DetailID, nav.DetailScroll.Y)
	}
}

func TestPreviewSettingsPage(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	export := dir != ""
	if !export {
		dir = t.TempDir()
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := loadAvatar(); err != nil {
		t.Fatal(err)
	}
	previewStore, err := openContactStore(filepath.Join(t.TempDir(), "contacts.json"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	longPath := "/Users/you/Workspace/a-very-long-project-directory-name/with/deep/nested/segments"
	longURL := "https://provider.example.invalid/v1/organizations/team/projects/workspace/models/default"
	longError := "Connection could not be applied because the saved endpoint is unavailable. Review the endpoint and retry after the current work finishes."
	config := connectionConfig{Version: 1, API: connectionAPIChatCompletions, Endpoint: longURL, Model: "provider/very-long-model-identifier", ContextTokens: 131072, OutputTokens: 8192, WorkingDirectory: longPath, MaxActiveTasks: 2}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, size := range []struct{ width, height int }{{720, 640}, {1040, 720}, {1380, 900}} {
			for _, tab := range []string{settingsTabAppearance, settingsTabConnection, settingsTabContact, settingsTabAdvanced} {
				a := testApp(t)
				a.demo = false
				a.store = previewStore
				a.storePath = previewStore.path
				a.windowWidth = size.width
				switch tab {
				case settingsTabConnection:
					a.connectionForm = newConnectionSettings(&config, longPath)
					a.connectionSaved = &config
					a.connectionEffective = &config
					a.connectionForm.HasSaved = true
					a.connectionForm.SavedSummary = connectionSummary(&config)
					a.connectionForm.Error = longError
					a.connectionForm.EffectiveSummary = connectionSummary(&config)
					a.runtimeStatus = runtimeStateReady
					a.rt = &runtimeClient{}
				case settingsTabContact:
					a.openSettingsTab(settingsTabContact, settingsSourceRail)
					editor := a.settingsContactEditor()
					editor.Draft.Name = "Research partner with a long readable name"
					editor.Draft.Purpose = "Long-running product and research questions that need a clear, durable reference."
					editor.Draft.Instructions = "  Preserve indentation.\n" + strings.Repeat("Verify the actual state before reporting success. ", 4)
					editor.Draft.Workspace = longPath
					a.settingsScroll.Y = 0
				case settingsTabAdvanced:
					a.runtimeStatus = runtimeStateFailed
					a.runtimeStage = "Runtime helper"
					a.runtimeErr = longError
					a.openAdvancedSettings()
				}
				if tab != settingsTabContact && tab != settingsTabAdvanced {
					a.openSettingsTab(tab, settingsSourceRail)
				}
				tr := ui.NewTester(a.view, size.width, size.height)
				tr.SetDark(dark)
				tr.Frame()
				if !tr.HasText("Settings") || !tr.HasText("← Back to workbench") {
					t.Fatalf("settings preview missing page shell for %s %dx%d", tab, size.width, size.height)
				}
				if tab == settingsTabConnection {
					if a.settingsScroll.Y != 0 {
						t.Fatalf("connection preview scrolled at %dx%d: %v", size.width, size.height, a.settingsScroll.Y)
					}
					if r, ok := tr.Find("Save, apply and test are separate actions. Applying never interrupts current work."); !ok || r.Y < 0 || r.Y+r.H > float32(size.height) {
						t.Fatalf("connection content start not visible at %dx%d: %+v ok=%v", size.width, size.height, r, ok)
					}
					if r, ok := tr.Find("Protocol"); !ok || r.X < 0 || r.X+r.W > float32(size.width) || r.Y < 0 || r.Y+r.H > float32(size.height) {
						t.Fatalf("connection first field not visible at %dx%d: %+v ok=%v", size.width, size.height, r, ok)
					}
				}
				if export {
					writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("settings-%s-%s-%dx%d.png", tab, theme, size.width, size.height)), tr.Image())
				}
			}
		}
	}
}

func TestFirstLaunchOpensConnectionSettingsPageWithoutEmptyOverlay(t *testing.T) {
	root := t.TempDir()
	isolateConnectionApp(t, root, "")
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	if !a.settingsOpen || a.settingsTab != settingsTabConnection || a.aux != "" {
		t.Fatalf("first launch route = open=%v tab=%q aux=%q", a.settingsOpen, a.settingsTab, a.aux)
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Connection & model") || !tr.HasText("Save configuration") || tr.HasText("No messages yet") {
		t.Fatalf("first launch did not show the connection page alone: %v", tr.Texts())
	}
	a.openAdvancedSettings()
	tr.Frame()
	if !tr.HasText("not checked yet; configure and apply a connection first") || tr.HasText("Runtime helper not found") {
		t.Fatalf("unconfigured Advanced misreported helper discovery: %v", tr.Texts())
	}
}

func TestSettingsLongConnectionScrollsThenPrimaryActionIsVisible(t *testing.T) {
	a, _ := workspaceTestApp(t)
	config := connectionConfig{Version: 1, API: connectionAPIChatCompletions, Endpoint: "https://provider.example.invalid/v1/very/long/endpoint", APIKey: "fixture", Model: "fixture", ContextTokens: 1000, OutputTokens: 100, WorkingDirectory: t.TempDir(), MaxActiveTasks: 2}
	a.connectionForm = newConnectionSettings(&config, config.WorkingDirectory)
	a.connectionSaved = &config
	a.connectionEffective = &config
	a.connectionForm.Error = strings.Repeat("A long form error that wraps without pushing controls out of the page. ", 8)
	a.windowWidth = 720
	a.openSettingsTab(settingsTabConnection, settingsSourceRail)
	tr := ui.NewTester(a.view, 720, 640)
	tr.Frame()
	tr.Scroll(500, 400, 0, 700)
	tr.Frame()
	if a.settingsScroll.Y <= 0 {
		t.Fatalf("long settings content did not scroll: %v", a.settingsScroll.Y)
	}
	r, ok := tr.Find("Save configuration")
	if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.X+r.W > 720 || r.Y < 0 || r.Y+r.H > 640 {
		t.Fatalf("primary action not reachable after scroll: %+v ok=%v", r, ok)
	}
}

func TestSettingsPageCmdCommaFromReaderKeepsLongScrollAndFocusedControl(t *testing.T) {
	a := issuePreviewApp("issue-detail")
	issue := a.selectedContact().selectedIssue()
	execution := issue.Executions[0]
	file := issueResultFile{ExecutionID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID, MessageID: execution.MessageID, CallID: "call-long", ToolName: "write", Path: "/tmp/long.txt", WorkingDirectory: "/tmp", RawResult: `{"path":"/tmp/long.txt","bytes_written":1}`}
	key := issueResultFileKey(file)
	longPreview := strings.Repeat("A long saved file line that must scroll independently in the reader.\n", 120)
	a.aux = "issue-result"
	a.issueResult = issueResultPage{
		Open: true, OwnerContactID: a.selectedContact().ID, IssueID: issue.ID, ExecutionID: execution.ID,
		Panel: issueResultPanelState{
			RunLabel: "Run 1", RunStatus: "Completed", Files: []issueResultFile{file},
			Inspection: &issueResultFileInspection{Exists: true, Regular: true, Preview: longPreview},
			InspectFor: key, ReaderKey: key,
		},
	}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	a.issueResult.Panel.ReaderCheckHandle.Focus()
	tr.Frame()
	if !tr.Focused("Check again") {
		t.Fatalf("reader check control did not receive focus: %v", tr.Texts())
	}
	tr.Scroll(520, 400, 0, 1200)
	tr.Frame()
	scrollBefore := a.issueResult.Panel.ReaderScroll.Y
	if scrollBefore <= 0 {
		t.Fatalf("reader preview did not scroll: %v", scrollBefore)
	}
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen {
		t.Fatal("Cmd+, did not open settings from reader")
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Check again") {
		t.Fatalf("reader returned focus to the wrong control: %v", tr.Texts())
	}
	if a.issueResult.Panel.ReaderScroll.Y != scrollBefore {
		t.Fatalf("reader scroll changed: %v -> %v", scrollBefore, a.issueResult.Panel.ReaderScroll.Y)
	}
}

func TestSettingsPageCmdCommaFromResultReturnsToReloadControl(t *testing.T) {
	a := issuePreviewApp("issue-detail")
	issue := a.selectedContact().selectedIssue()
	execution := issue.Executions[0]
	a.aux = "issue-result"
	a.issueResult = issueResultPage{Open: true, OwnerContactID: a.selectedContact().ID, IssueID: issue.ID, ExecutionID: execution.ID, Panel: issueResultPanelState{RunLabel: "Run 1", RunStatus: "Completed"}}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	a.issueResult.Panel.ReloadHandle.Focus()
	tr.Frame()
	if !tr.Focused("Reload result") {
		t.Fatalf("result reload control did not receive focus: %v", tr.Texts())
	}
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen {
		t.Fatal("Cmd+, did not open settings from result")
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Reload result") {
		t.Fatalf("result returned focus to the wrong control: %v", tr.Texts())
	}
}

func TestSettingsCmdCommaFromDynamicFileRowReturnsToSameRowAndReopens(t *testing.T) {
	a := issuePreviewApp("issue-detail")
	issue := a.selectedContact().selectedIssue()
	execution := issue.Executions[0]
	files := []issueResultFile{
		{ExecutionID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID, MessageID: execution.MessageID, CallID: "call-a", ToolName: "write", Path: "/tmp/a.txt", WorkingDirectory: "/tmp", RawResult: `{"path":"/tmp/a.txt"}`},
		{ExecutionID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID, MessageID: execution.MessageID, CallID: "call-b", ToolName: "write", Path: "/tmp/b.md", WorkingDirectory: "/tmp", RawResult: `{"path":"/tmp/b.md"}`},
		{ExecutionID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID, MessageID: execution.MessageID, CallID: "call-c", ToolName: "write", Path: "/tmp/c.txt", WorkingDirectory: "/tmp", RawResult: `{"path":"/tmp/c.txt"}`},
	}
	target := issueResultFileKey(files[1])
	inspection := issueResultFileInspection{Path: files[1].Path, WorkingDirectory: "/tmp", Exists: true, Regular: true, Preview: strings.Repeat("Long reader content for the second dynamic file.\n", 60)}
	a.aux = "issue-result"
	a.issueResult = issueResultPage{
		Open: true, OwnerContactID: a.selectedContact().ID, IssueID: issue.ID, ExecutionID: execution.ID,
		Panel: issueResultPanelState{
			RunLabel: "Run 1", RunStatus: "Completed",
			Response: "# Result\n\n" + strings.Repeat("Long result body before reported files.\n\n", 50),
			Files:    files, Inspection: &inspection, InspectFor: target,
		},
	}
	a.issueResultInspect = func(file issueResultFile) issueResultFileInspection { return inspection }
	a.selectedContact().Draft = "keep this draft"
	a.windowWidth = 1040
	pump := newUIPump(t, a)
	defer func() {
		pump.waitTimeout(func() bool { return a.resultInFlight == 0 }, 5*time.Second)
		pump.close()
	}()
	var tr *ui.Tester
	pump.do(func() {
		tr = ui.NewTester(a.view, 1040, 720)
		tr.Frame()
	})
	targetLabel := "Read file 2: b.md"
	for i := 0; i < 12; i++ {
		var visible bool
		pump.do(func() {
			r, ok := tr.Find(targetLabel)
			visible = ok && r.W > 0 && r.H > 0 && r.Y >= 0 && r.Y+r.H <= 720
		})
		if visible {
			break
		}
		pump.do(func() {
			tr.Scroll(520, 400, 0, 220)
			tr.Frame()
		})
	}
	var outerScrollBefore float32
	pump.do(func() { outerScrollBefore = a.issueResultScroll.Y })
	if outerScrollBefore <= 0 {
		t.Fatalf("outer result did not scroll to reported files: %v", outerScrollBefore)
	}
	var clickErr error
	pump.do(func() {
		clickErr = tr.Click(targetLabel)
		tr.Frame()
	})
	if clickErr != nil {
		t.Fatal(clickErr)
	}
	pump.wait(func() bool { return !a.issueResult.Panel.Checking })
	pump.do(func() { tr.Frame() })
	var readingOpen bool
	pump.do(func() { readingOpen = tr.HasText("File reading") })
	if !readingOpen {
		t.Fatal("reader did not open")
	}
	pump.do(func() {
		tr.Key(0, ui.KeyEscape)
		tr.Frame()
	})
	var rowFocused bool
	pump.do(func() { rowFocused = tr.Focused(targetLabel) })
	if !rowFocused {
		t.Fatalf("Escape did not return focus to dynamic file row: %v", tr.Texts())
	}
	pump.do(func() {
		tr.Key(ui.Cmd, ui.KeyComma)
		tr.Frame()
	})
	var settingsOpen bool
	pump.do(func() { settingsOpen = a.settingsOpen })
	if !settingsOpen {
		t.Fatal("Cmd+, from dynamic file row did not open settings")
	}
	pump.do(func() { clickErr = tr.Click("← Back to workbench"); tr.Frame() })
	if clickErr != nil {
		t.Fatal(clickErr)
	}
	var returnedFocus bool
	var scrollAfter float32
	pump.do(func() {
		returnedFocus = tr.Focused(targetLabel)
		scrollAfter = a.issueResultScroll.Y
	})
	// Do not stop at the focus mismatch: the actual regression must also prove
	// whether Return invokes the dynamic file row or closes the result page.
	pump.do(func() {
		tr.Key(0, ui.KeyEnter)
		tr.Frame()
	})
	pump.wait(func() bool { return !a.issueResult.Panel.Checking })
	var readerKey string
	var readerOpen, resultOpen bool
	var texts []string
	pump.do(func() {
		readerKey = a.issueResult.Panel.ReaderKey
		readerOpen = tr.HasText("File reading")
		resultOpen = a.issueResult.Open
		texts = append([]string(nil), tr.Texts()...)
	})
	if !returnedFocus || scrollAfter != outerScrollBefore || !resultOpen || readerKey != target || !readerOpen {
		t.Fatalf("Back/Return did not restore dynamic file row: focus=%v scroll=%v->%v resultOpen=%v reader=%q target=%q texts=%v",
			returnedFocus, outerScrollBefore, scrollAfter, resultOpen, readerKey, target, texts)
	}
	if a.selectedContact().Draft != "keep this draft" || a.issueResult.IssueID != issue.ID || a.issueResult.ExecutionID != execution.ID {
		t.Fatal("settings return changed draft or result context")
	}
}

func TestSettingsPageCmdCommaReturnsToReaderCopyPath(t *testing.T) {
	a := issuePreviewApp("issue-detail")
	issue := a.selectedContact().selectedIssue()
	execution := issue.Executions[0]
	file := issueResultFile{ExecutionID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID, MessageID: execution.MessageID, CallID: "copy-path", ToolName: "write", Path: "/tmp/copy-path.md", WorkingDirectory: "/tmp", RawResult: `{"path":"/tmp/copy-path.md"}`}
	key := issueResultFileKey(file)
	a.aux = "issue-result"
	a.issueResult = issueResultPage{Open: true, OwnerContactID: a.selectedContact().ID, IssueID: issue.ID, ExecutionID: execution.ID, Panel: issueResultPanelState{RunLabel: "Run 1", RunStatus: "Completed", Files: []issueResultFile{file}, ReaderKey: key}}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	a.issueResult.Panel.CopyPathHandle.Focus()
	tr.Frame()
	if !tr.Focused("Copy path") {
		t.Fatalf("Copy path did not receive focus: %v", tr.Texts())
	}
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen {
		t.Fatal("Cmd+, from Copy path did not open settings")
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Copy path") {
		t.Fatalf("Back did not return focus to Copy path: %v", tr.Texts())
	}
}

func TestSettingsReturnKeepsEachCodeCopyTarget(t *testing.T) {
	for _, reader := range []bool{false, true} {
		t.Run(fmt.Sprintf("reader=%v", reader), func(t *testing.T) {
			a := issuePreviewApp("issue-detail")
			issue := a.selectedContact().selectedIssue()
			execution := issue.Executions[0]
			file := issueResultPanelFixtureFile("code-copies", "/tmp/code-copies.md", "{}")
			key := issueResultFileKey(file)
			content := "```go\nfirst-copy-content\n```\n\nbetween\n\n```go\nsecond-copy-content\n```"
			a.aux = "issue-result"
			a.issueResult = issueResultPage{Open: true, OwnerContactID: a.selectedContact().ID, IssueID: issue.ID, ExecutionID: execution.ID,
				Panel: issueResultPanelState{Response: content, Files: []issueResultFile{file}}}
			state := &a.issueResult.Panel
			if reader {
				state.ReaderKey, state.InspectFor = key, key
				state.Inspection = &issueResultFileInspection{Exists: true, Regular: true, Preview: content}
			}
			a.windowWidth = 1040
			tr := ui.NewTester(a.view, 1040, 900)
			tr.Frame()
			handles := state.ResultCodeHandles
			if reader {
				handles = state.ReaderCodeHandles
			}
			keys := make([]int, 0, len(handles))
			for key := range handles {
				keys = append(keys, key)
			}
			sort.Ints(keys)
			if len(keys) != 2 {
				t.Fatalf("expected two independently focusable code copies, got %d", len(keys))
			}
			handles[keys[1]].Focus()
			tr.Frame()
			tr.Key(ui.Cmd, ui.KeyComma)
			tr.Frame()
			if !a.settingsOpen {
				t.Fatal("Cmd+, from code copy did not open settings")
			}
			if err := tr.Click("← Back to workbench"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			tr.Key(0, ui.KeyEnter)
			tr.Frame()
			if got := tr.Clipboard(); got != "second-copy-content\n" {
				t.Fatalf("Return after settings copied %q instead of the second block", got)
			}
			if !a.issueResult.Open || (reader && state.ReaderKey != key) {
				t.Fatal("Return closed the result or reading layer")
			}
		})
	}
}

func TestSettingsReturnKeepsReaderActions(t *testing.T) {
	file := issueResultPanelFixtureFile("handles", "/tmp/handles.txt", `{"path":"/tmp/handles.txt"}`)
	key := issueResultFileKey(file)
	a := testApp(t)
	a.aux = "issue-result"
	a.issueResult.Open = true
	a.issueResult.Panel = issueResultPanelState{
		RunLabel: "Run 1", RunStatus: "Completed", Files: []issueResultFile{file},
		Inspection: &issueResultFileInspection{Path: file.Path, Exists: true, Regular: true, Preview: "preview"},
		InspectFor: key, ReaderKey: key, SourceFor: key,
	}
	state := &a.issueResult.Panel
	opened, revealed, copied := 0, 0, ""
	actions := issueResultPanelActions{
		OpenSettings: func() { a.openSettingsTab(settingsTabAppearance, settingsSourceAux) },
		CheckFile:    func(issueResultFile) {},
		OpenFile:     func(issueResultFile) { opened++ },
		RevealFile:   func(issueResultFile) { revealed++ },
		CopyText:     func(text string) error { copied = text; return nil },
	}
	tr := ui.NewTester(func(c *ui.Context) {
		if a.settingsOpen {
			a.settingsPage(c, c.Theme())
			return
		}
		issueResultPanel(c, state, actions)
	}, 1040, 900)
	tr.Frame()
	for _, control := range []struct {
		label  string
		handle *ui.Handle
	}{
		{"Open file", &state.ReaderOpenHandle},
		{"Show in Finder", &state.ReaderRevealHandle},
		{"Copy tool record", &state.CopyRawHandle},
	} {
		control.handle.Focus()
		tr.Frame()
		tr.Key(ui.Cmd, ui.KeyComma)
		tr.Frame()
		if !a.settingsOpen {
			t.Fatalf("Cmd+, from %s did not open settings", control.label)
		}
		if err := tr.Click("← Back to workbench"); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if !tr.Focused(control.label) {
			t.Fatalf("Back did not restore %s", control.label)
		}
		tr.Key(0, ui.KeyEnter)
		tr.Frame()
	}
	if opened != 1 || revealed != 1 || copied != file.RawResult || state.ReaderKey != key {
		t.Fatalf("Return actions = open:%d reveal:%d copy:%q reader:%q", opened, revealed, copied, state.ReaderKey)
	}
}
