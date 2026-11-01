package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func providerToolNames(t *testing.T, body string) map[string]bool {
	t.Helper()
	var request struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatalf("model request is not JSON: %v", err)
	}
	names := make(map[string]bool, len(request.Tools))
	for _, tool := range request.Tools {
		names[tool.Name] = true
	}
	return names
}

func assertBuiltinToolNames(t *testing.T, body string, want ...string) {
	t.Helper()
	got := providerToolNames(t, body)
	for _, name := range want {
		if !got[name] {
			t.Fatalf("model request missing built-in tool %q: %v", name, got)
		}
	}
}

func assertNoBuiltinToolNames(t *testing.T, body string) {
	t.Helper()
	got := providerToolNames(t, body)
	for _, name := range builtinToolNames(builtinToolsForTest()) {
		if got[name] {
			t.Fatalf("model request unexpectedly contains built-in tool %q: %v", name, got)
		}
	}
}

func assertExternalToolNames(t *testing.T, body string, want ...string) {
	t.Helper()
	got := providerToolNames(t, body)
	for _, name := range want {
		if !got[name] {
			t.Fatalf("model request missing external tool %q: %v", name, got)
		}
	}
}

func waitConversationIdle(t *testing.T, h *guiHarness) {
	t.Helper()
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.LastTaskID != "" && ct.ActiveTaskID == "" && ct.SendState == sendIdle
	})
}

func sendWithBuiltinSelection(t *testing.T, h *guiHarness, prompt string) string {
	t.Helper()
	var sendErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = prompt
		if !h.app.sendCurrent() {
			sendErr = ct.SendError
		}
	})
	if sendErr != "" {
		t.Fatalf("send %q failed: %s", prompt, sendErr)
	}
	waitConversationIdle(t, h)
	var sessionID string
	h.pump.do(func() { sessionID = h.app.selectedContact().SessionID })
	return sessionID
}

func selectBuiltinSubsetThroughProfile(t *testing.T, h *guiHarness, keep []string) {
	t.Helper()
	var setupErr error
	h.pump.do(func() {
		a := h.app
		a.openSettingsTab(settingsTabContact, settingsSourceRail)
		tr := ui.NewTester(a.view, 1040, 720)
		tr.Frame()
		defaultLabel := "Use host defaults (" + strings.Join(builtinToolNames(a.runtimeBuiltinTools), ", ") + ")"
		if err := clickAfterBoundedScrollInPump(tr, defaultLabel, 1040, 720); err != nil {
			setupErr = err
			return
		}
		tr.Frame()
		for _, name := range builtinToolNames(a.runtimeBuiltinTools) {
			if containsString(keep, name) {
				continue
			}
			if err := clickAfterBoundedScrollInPump(tr, name, 1040, 720); err != nil {
				setupErr = err
				return
			}
			tr.Frame()
		}
		saveAction := "Save changes"
		if h.app.selectedContact().SessionID != "" {
			saveAction = "Save and start new conversation"
		}
		if err := clickAfterBoundedScrollInPump(tr, saveAction, 1040, 720); err != nil {
			setupErr = err
		}
		a.closeSettings()
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
}

func TestBuiltinToolSelectionCreatesRuntimeSessions(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	t.Setenv("SAKI_TEST_ONLINE", "0")
	provider := &providerFixture{}
	marker := "builtin-tool-selection-116"
	h := startIssueToolTestHarness(t, binary, provider.handler(t, marker))
	h.pump.wait(func() bool {
		return h.app.runtimeBuiltinToolsLoaded || h.app.runtimeBuiltinToolsErr != ""
	})
	var catalogErr string
	h.pump.do(func() { catalogErr = h.app.runtimeBuiltinToolsErr })
	if catalogErr != "" {
		t.Fatalf("Runtime tool.list failed: %s", catalogErr)
	}

	defaultSession := sendWithBuiltinSelection(t, h, "Default built-in tools.")
	if got := providerToolNames(t, provider.body(0)); len(got) == 0 {
		t.Fatal("default session request had no tools")
	}
	assertBuiltinToolNames(t, provider.body(0), "bash", "edit", "read", "write")
	assertExternalToolNames(t, provider.body(0), "issue_list", "issue_get", "issue_action")
	defaultConfig, err := h.app.rt.connectionConfig(defaultSession)
	if err != nil {
		t.Fatal(err)
	}
	if got := defaultConfig.EffectiveConfig.ToolSelection.Builtin; len(got) != 4 {
		t.Fatalf("default effective built-ins = %v", got)
	}

	selectBuiltinSubsetThroughProfile(t, h, []string{"read"})
	oldConfig, err := h.app.rt.connectionConfig(defaultSession)
	if err != nil {
		t.Fatal(err)
	}
	if got := oldConfig.EffectiveConfig.ToolSelection.Builtin; len(got) != 4 {
		t.Fatalf("profile save changed the existing session: %v", got)
	}
	subsetSession := sendWithBuiltinSelection(t, h, "Only read is enabled.")
	assertBuiltinToolNames(t, provider.body(1), "read")
	if got := providerToolNames(t, provider.body(1)); got["bash"] || got["edit"] || got["write"] {
		t.Fatalf("subset request retained excluded built-ins: %v", got)
	}
	assertExternalToolNames(t, provider.body(1), "issue_list", "issue_get", "issue_action")
	subsetConfig, err := h.app.rt.connectionConfig(subsetSession)
	if err != nil {
		t.Fatal(err)
	}
	if got := subsetConfig.EffectiveConfig.ToolSelection.Builtin; len(got) != 1 || got[0] != "read" {
		t.Fatalf("subset effective built-ins = %v", got)
	}

	selectBuiltinSubsetThroughProfile(t, h, nil)
	emptySession := sendWithBuiltinSelection(t, h, "No built-in tools.")
	assertNoBuiltinToolNames(t, provider.body(2))
	assertExternalToolNames(t, provider.body(2), "issue_list", "issue_get", "issue_action")
	emptyConfig, err := h.app.rt.connectionConfig(emptySession)
	if err != nil {
		t.Fatal(err)
	}
	if got := emptyConfig.EffectiveConfig.ToolSelection.Builtin; got == nil || len(got) != 0 {
		t.Fatalf("empty effective built-ins = %#v, want non-nil empty", got)
	}

	var issueID string
	var runErr string
	h.pump.do(func() {
		if !h.app.createIssue("Empty built-in work", "Keep the issue tools.") {
			runErr = h.app.issueFormError
			return
		}
		issueID = h.app.selectedContact().SelectedIssueID
		if !h.app.runIssue(issueID) {
			runErr = h.app.issueFormError
		}
	})
	if runErr != "" {
		t.Fatalf("issue run failed: %s", runErr)
	}
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		if issue == nil || len(issue.Executions) == 0 {
			return false
		}
		return issue.Work.ActiveTaskID == "" && issue.Executions[len(issue.Executions)-1].Status == issueExecutionCompleted
	})
	if provider.count() != 4 {
		t.Fatalf("provider requests = %d, want 4", provider.count())
	}
	assertNoBuiltinToolNames(t, provider.body(3))
	assertExternalToolNames(t, provider.body(3), issueToolWorkToolName)
	var workSession string
	h.pump.do(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		workSession = issue.WorkSessionID
	})
	workConfig, err := h.app.rt.connectionConfig(workSession)
	if err != nil {
		t.Fatal(err)
	}
	if got := workConfig.EffectiveConfig.ToolSelection.Builtin; got == nil || len(got) != 0 {
		t.Fatalf("work effective built-ins = %#v, want non-nil empty", got)
	}
	if !runtimeSelectionHasWorkWait(workConfig.EffectiveConfig.ToolSelection) {
		t.Fatalf("work effective selection lost issue_wait: %+v", workConfig.EffectiveConfig.ToolSelection)
	}

	// A later profile change must not rewrite the existing work session. A new
	// issue work session uses the read-only profile default.
	selectBuiltinSubsetThroughProfile(t, h, []string{"read"})
	unchangedWork, err := h.app.rt.connectionConfig(workSession)
	if err != nil {
		t.Fatal(err)
	}
	if got := unchangedWork.EffectiveConfig.ToolSelection.Builtin; got == nil || len(got) != 0 {
		t.Fatalf("profile save changed the existing work session: %#v", got)
	}
	var readIssueID string
	h.pump.do(func() {
		if !h.app.createIssue("Read-only built-in work", "Keep read available.") {
			runErr = h.app.issueFormError
			return
		}
		readIssueID = h.app.selectedContact().SelectedIssueID
		if !h.app.runIssue(readIssueID) {
			runErr = h.app.issueFormError
		}
	})
	if runErr != "" {
		t.Fatalf("read work run failed: %s", runErr)
	}
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(readIssueID)
		if issue == nil || len(issue.Executions) == 0 {
			return false
		}
		return issue.Work.ActiveTaskID == "" && issue.Executions[len(issue.Executions)-1].Status == issueExecutionCompleted
	})
	if provider.count() != 5 {
		t.Fatalf("provider requests = %d, want 5", provider.count())
	}
	assertBuiltinToolNames(t, provider.body(4), "read")
	if got := providerToolNames(t, provider.body(4)); got["bash"] || got["edit"] || got["write"] {
		t.Fatalf("read work retained excluded built-ins: %v", got)
	}
	assertExternalToolNames(t, provider.body(4), issueToolWorkToolName)
	var readWorkSession string
	h.pump.do(func() {
		issue := h.app.selectedContact().issueByID(readIssueID)
		readWorkSession = issue.WorkSessionID
	})
	readWorkConfig, err := h.app.rt.connectionConfig(readWorkSession)
	if err != nil {
		t.Fatal(err)
	}
	if got := readWorkConfig.EffectiveConfig.ToolSelection.Builtin; len(got) != 1 || got[0] != "read" {
		t.Fatalf("read work effective built-ins = %v", got)
	}
	if !runtimeSelectionHasWorkWait(readWorkConfig.EffectiveConfig.ToolSelection) {
		t.Fatalf("read work effective selection lost issue_wait: %+v", readWorkConfig.EffectiveConfig.ToolSelection)
	}
}

func TestBuiltinToolSelectionSurvivesGUIModelABA(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	t.Setenv("SAKI_TEST_ONLINE", "0")
	provider := &modelProviderFixture{}
	h := startIssueToolTestHarness(t, binary, http.HandlerFunc(provider.handler))
	h.pump.wait(func() bool { return h.app.runtimeBuiltinToolsLoaded || h.app.runtimeBuiltinToolsErr != "" })
	selectBuiltinSubsetThroughProfile(t, h, []string{"read"})
	sessionID := sendWithBuiltinSelection(t, h, "Start with A.")
	config, err := h.app.rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := config.EffectiveConfig.ToolSelection.Builtin; len(got) != 1 || got[0] != "read" || !runtimeSelectionHasIssueTools(config.EffectiveConfig.ToolSelection) {
		t.Fatalf("initial model-A selection = %+v", config.EffectiveConfig.ToolSelection)
	}

	// Seed an existing binding with a custom exposed name and no canonical
	// Nagi bindings. Normal sending must supplement it without losing the alias.
	alias := runtimeExternalToolBinding{SourceID: issueToolSourceID, ToolName: "issue_get", ExposedName: "saved_issue_lookup"}
	_, err = h.app.rt.updateSessionConfiguration(sessionID, config.ConfigRevision, map[string]any{
		"builtin":  []string{"read"},
		"external": []runtimeExternalToolBinding{alias},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	openModelPanel(t, h, "Conversation model")
	updateModelGUI(t, h, "model-B")
	config, err = h.app.rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(config.EffectiveConfig.ToolSelection.External, []runtimeExternalToolBinding{alias}) {
		t.Fatalf("model update changed the existing alias: %+v", config.EffectiveConfig.ToolSelection.External)
	}
	h.pump.do(func() { h.app.closeAux() })
	sendWithBuiltinSelection(t, h, "Use B.")
	config, err = h.app.rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := config.EffectiveConfig.ToolSelection.Builtin; len(got) != 1 || got[0] != "read" || !runtimeSelectionHasIssueTools(config.EffectiveConfig.ToolSelection) {
		t.Fatalf("model-B selection = %+v", config.EffectiveConfig.ToolSelection)
	}

	openModelPanel(t, h, "Conversation model")
	updateModelGUI(t, h, "fixture")
	h.pump.do(func() { h.app.closeAux() })
	sendWithBuiltinSelection(t, h, "Return to A.")
	config, err = h.app.rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := config.EffectiveConfig.ToolSelection.Builtin; len(got) != 1 || got[0] != "read" || !runtimeSelectionHasIssueTools(config.EffectiveConfig.ToolSelection) {
		t.Fatalf("model-A return selection = %+v", config.EffectiveConfig.ToolSelection)
	}
	if !slices.Contains(config.EffectiveConfig.ToolSelection.External, alias) {
		t.Fatalf("model switching or tool supplementation lost the alias: %+v", config.EffectiveConfig.ToolSelection.External)
	}
	if got := provider.requestedModels(); len(got) != 3 || got[0] != "fixture" || got[1] != "model-B" || got[2] != "fixture" {
		t.Fatalf("requested models = %v", got)
	}
}

func TestLegacyRuntimeDoesNotOfferOrAcceptBuiltinSelection(t *testing.T) {
	binary := requireRuntimeBinary(t)
	h := startGUIHarness(t, binary, &providerFixture{}, "legacy-builtin-selection")
	h.pump.wait(func() bool { return h.app.runtimeStatus == runtimeStateReady })
	read := []string{"read"}
	var accepted bool
	h.pump.do(func() {
		h.app.openSettingsTab(settingsTabContact, settingsSourceRail)
		accepted = h.app.saveContactProfileDraftFor(h.app.selectedContact().ID, profileDraft{Name: h.app.selectedContact().Name, BuiltinTools: &read}, false)
	})
	if accepted {
		t.Fatal("legacy runtime accepted an explicit built-in selection")
	}
	var texts []string
	h.pump.do(func() {
		tr := ui.NewTester(h.app.view, 1040, 720)
		tr.Frame()
		texts = tr.Texts()
	})
	if !containsText(texts, "requires a managed Nagi connection") || containsText(texts, "Use host defaults") {
		t.Fatalf("legacy profile did not explain the limitation: %v", texts)
	}
}

func TestBuiltinToolSelectionCreateRejectionKeepsDraft(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	t.Setenv("SAKI_TEST_ONLINE", "0")
	provider := &providerFixture{}
	h := startIssueToolTestHarness(t, binary, provider.handler(t, "builtin-reject-116"))
	h.pump.wait(func() bool { return h.app.runtimeBuiltinToolsLoaded || h.app.runtimeBuiltinToolsErr != "" })
	var sendErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		invalid := []string{"not-a-runtime-tool"}
		ct.BuiltinTools = &invalid
		ct.Draft = "Keep this rejected draft."
		if !h.app.sendCurrent() {
			sendErr = ct.SendError
		}
	})
	if sendErr != "" {
		t.Fatalf("admission unexpectedly failed before Runtime create: %s", sendErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && ct.SendError != ""
	})
	var draft, detail string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		draft, detail = ct.Draft, ct.SendError
	})
	if draft != "Keep this rejected draft." || !strings.Contains(strings.ToLower(detail), "tool") || provider.count() != 0 {
		t.Fatalf("create rejection changed draft/request: draft=%q detail=%q provider=%d", draft, detail, provider.count())
	}
	if h.app.selectedContact().BuiltinTools == nil || len(*h.app.selectedContact().BuiltinTools) != 1 {
		t.Fatal("create rejection lost the user's explicit selection")
	}
}

func TestBuiltinToolSelectionSurvivesRestart(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	t.Setenv("SAKI_TEST_ONLINE", "0")
	provider := &providerFixture{}
	h := startIssueToolTestHarness(t, binary, provider.handler(t, "builtin-restart-116"))
	h.pump.wait(func() bool { return h.app.runtimeBuiltinToolsLoaded || h.app.runtimeBuiltinToolsErr != "" })
	selectBuiltinSubsetThroughProfile(t, h, []string{"read"})
	sessionID := sendWithBuiltinSelection(t, h, "Keep read after restart.")
	h.shutdown(t)

	restarted := restartGUIHarness(t, binary)
	defer func() {
		if !restarted.closed {
			restarted.shutdown(t)
		}
	}()
	restarted.pump.wait(func() bool { return restarted.app.selectedContact().Loaded })
	var restored []string
	restarted.pump.do(func() {
		ct := restarted.app.selectedContact()
		if ct.BuiltinTools == nil {
			restored = nil
		} else {
			restored = append([]string(nil), (*ct.BuiltinTools)...)
		}
	})
	if len(restored) != 1 || restored[0] != "read" {
		t.Fatalf("restart profile selection = %#v", restored)
	}
	config, err := restarted.app.rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := config.EffectiveConfig.ToolSelection.Builtin; len(got) != 1 || got[0] != "read" {
		t.Fatalf("restart session selection = %v", got)
	}
	if provider.count() != 1 {
		t.Fatalf("restart issued model requests: %d", provider.count())
	}
}

func TestExplicitBuiltinSelectionRequiresManagedConnectionOnNewCreate(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	t.Setenv("SAKI_TEST_ONLINE", "0")
	provider := &providerFixture{}
	h := startIssueToolTestHarness(t, binary, provider.handler(t, "builtin-transition-116"))
	h.pump.wait(func() bool { return h.app.runtimeBuiltinToolsLoaded || h.app.runtimeBuiltinToolsErr != "" })
	selectBuiltinSubsetThroughProfile(t, h, []string{"read"})
	var connectionPath, storePath, workDir, sessionDir string
	var config connectionConfig
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if ct.SessionID != "" {
			t.Error("transition fixture unexpectedly created a session before testing it")
		}
		if ct.BuiltinTools == nil || len(*ct.BuiltinTools) != 1 || (*ct.BuiltinTools)[0] != "read" {
			t.Errorf("explicit saved selection = %v", ct.BuiltinTools)
		}
		if h.app.connectionEffective == nil {
			t.Error("managed connection was not captured")
			return
		}
		config = *h.app.connectionEffective
		connectionPath, storePath, workDir, sessionDir = h.app.connectionPath, h.app.storePath, h.app.workDir, h.app.sessionDir
	})
	if connectionPath == "" || config.Endpoint == "" {
		t.Fatal("transition fixture has no connection to remove")
	}
	h.shutdown(t)
	if err := os.Remove(connectionPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAKI_GUI_STORE", storePath)
	t.Setenv("SAKI_GUI_WORKDIR", workDir)
	t.Setenv("SAKI_SESSION_DIR", sessionDir)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", "")
	t.Setenv("API_KEY", config.APIKey)
	t.Setenv("SAKI_API", config.API)
	t.Setenv("SAKI_BASE_URL", config.Endpoint)
	t.Setenv("SAKI_MODEL", config.Model)
	app, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	pump := newUIPump(t, app)
	restartedLegacy := &guiHarness{app: app, pump: pump, root: workDir}
	t.Cleanup(func() {
		if !restartedLegacy.closed {
			restartedLegacy.shutdown(t)
		}
	})
	app.startRuntime()
	pump.wait(func() bool { return app.runtimeStatus == runtimeStateReady })
	var legacy, managed bool
	appStorePath := ""
	pump.do(func() { legacy, managed, appStorePath = app.connectionLegacy, app.connectionManaged, app.storePath })
	if !legacy || managed || appStorePath != storePath {
		t.Fatalf("transition did not load legacy app: legacy=%v managed=%v store=%q/%q", legacy, managed, appStorePath, storePath)
	}
	var sendErr string
	pump.do(func() {
		ct := app.selectedContact()
		ct.Draft = "Keep this selection and draft."
		if !app.sendCurrent() {
			sendErr = ct.SendError
		}
	})
	if sendErr != "" {
		t.Fatalf("send was rejected before create: %s", sendErr)
	}
	pump.wait(func() bool {
		ct := app.selectedContact()
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && ct.SendError != ""
	})
	pump.do(func() {
		ct := app.selectedContact()
		if ct.SessionID != "" || ct.Draft != "Keep this selection and draft." {
			t.Errorf("legacy create failure changed session/draft: session=%q draft=%q", ct.SessionID, ct.Draft)
		}
		if ct.BuiltinTools == nil || len(*ct.BuiltinTools) != 1 || (*ct.BuiltinTools)[0] != "read" {
			t.Errorf("legacy create failure lost selection: %v", ct.BuiltinTools)
		}
		if !strings.Contains(ct.SendError, "managed Nagi connection") {
			t.Errorf("legacy create error = %q", ct.SendError)
		}
	})
	if provider.count() != 0 {
		t.Fatalf("legacy explicit create reached provider: %d", provider.count())
	}
}

func builtinUI(t *testing.T, h *guiHarness, fn func() error) {
	t.Helper()
	var err error
	h.pump.do(func() { err = fn() })
	if err != nil {
		t.Fatal(err)
	}
}

func TestBuiltinSelectionCapturedBeforeAdmissionAndRetainedOnContinue(t *testing.T) {
	binary := requireRuntimeBinary(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", ensureIssueToolTestHelper(t))
	for _, kind := range []string{"conversation", "work"} {
		t.Run(kind, func(t *testing.T) {
			hold, arrived := make(chan struct{}), make(chan struct{})
			var released sync.Once
			release := func() { released.Do(func() { close(hold) }) }
			provider := &providerFixture{Hold: hold, Arrived: arrived}
			h := startIssueToolTestHarness(t, binary, provider.handler(t, "builtin-snapshot"))
			defer release()
			h.pump.wait(func() bool { return h.app.runtimeBuiltinToolsLoaded })
			selectBuiltinSubsetThroughProfile(t, h, []string{"read"})
			manual := make(chan func(), 32)
			var issueID string
			builtinUI(t, h, func() error {
				if kind == "work" {
					if !h.app.createIssue("Captured tools", "Keep the admitted selection.") {
						return errors.New(h.app.issueFormError)
					}
					issueID = h.app.selectedContact().SelectedIssueID
				}
				h.app.setUIHook(func(fn func()) { manual <- fn })
				if kind == "work" {
					if !h.app.runIssue(issueID) {
						return errors.New(h.app.issueFormError)
					}
				} else {
					h.app.selectedContact().Draft = "Keep the admitted tools."
					if !h.app.sendCurrent() {
						return errors.New(h.app.selectedContact().SendError)
					}
				}
				return nil
			})
			var mapping func()
			defer func() {
				h.pump.do(func() {
					h.app.setUIHook(func(fn func()) { h.pump.q <- fn })
					if mapping != nil {
						mapping()
						mapping = nil
					}
				})
			}()
			select {
			case mapping = <-manual:
			case <-time.After(10 * time.Second):
				t.Fatal("session creation did not reach the mapping boundary")
			}
			builtinUI(t, h, func() error {
				h.app.openSettingsTab(settingsTabContact, settingsSourceRail)
				tr := ui.NewTester(h.app.view, 1040, 720)
				tr.Frame()
				if err := clickAfterBoundedScrollInPump(tr, "read", 1040, 720); err != nil {
					return err
				}
				tr.Frame()
				if err := clickAfterBoundedScrollInPump(tr, "Save changes", 1040, 720); err != nil {
					return err
				}
				ct := h.app.selectedContact()
				if ct.BuiltinTools == nil || len(*ct.BuiltinTools) != 0 {
					return errors.New("profile change was not saved during admission")
				}
				h.app.closeSettings()
				h.app.setUIHook(func(fn func()) { h.pump.q <- fn })
				mapping()
				mapping = nil
				return nil
			})
			select {
			case <-arrived:
			case <-time.After(10 * time.Second):
				t.Fatal("admitted request did not reach provider")
			}
			var sessionID string
			h.pump.wait(func() bool {
				ct := h.app.selectedContact()
				if kind == "work" {
					return ct.issueByID(issueID).Work.ActiveTaskID != ""
				}
				return ct.ActiveTaskID != ""
			})
			builtinUI(t, h, func() error {
				ct := h.app.selectedContact()
				sessionID = ct.SessionID
				if kind == "work" {
					sessionID = ct.issueByID(issueID).WorkSessionID
				}
				return nil
			})
			config, err := h.app.rt.connectionConfig(sessionID)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(config.EffectiveConfig.ToolSelection.Builtin, []string{"read"}) {
				t.Fatalf("running selection = %v", config.EffectiveConfig.ToolSelection.Builtin)
			}
			builtinUI(t, h, func() error {
				h.app.openSettingsTab(settingsTabContact, settingsSourceRail)
				tr := ui.NewTester(h.app.view, 1040, 720)
				tr.Frame()
				label := "Use host defaults (" + strings.Join(builtinToolNames(h.app.runtimeBuiltinTools), ", ") + ")"
				if err := clickAfterBoundedScrollInPump(tr, label, 1040, 720); err != nil {
					return err
				}
				tr.Frame()
				if err := clickAfterBoundedScrollInPump(tr, "Save changes", 1040, 720); err != nil {
					return err
				}
				if h.app.selectedContact().BuiltinTools != nil {
					return errors.New("running profile save did not restore the future host default")
				}
				h.app.closeSettings()
				return nil
			})
			release()
			waitIdle := func() {
				h.pump.wait(func() bool {
					ct := h.app.selectedContact()
					if kind == "work" {
						work := ct.issueByID(issueID).Work
						return work.ActiveTaskID == "" && work.SendState == sendIdle && !work.HistoryDirty && !work.Releasing && !work.ReleasePending
					}
					return ct.ActiveTaskID == "" && ct.SendState == sendIdle && !ct.HistoryDirty
				})
			}
			waitIdle()
			if provider.count() != 1 {
				t.Fatalf("profile save sent extra requests: %d", provider.count())
			}
			builtinUI(t, h, func() error {
				if kind == "work" {
					if !h.app.runIssue(issueID) {
						return errors.New(h.app.issueFormError)
					}
				} else {
					h.app.selectedContact().Draft = "Continue the same session."
					if !h.app.sendCurrent() {
						return errors.New(h.app.selectedContact().SendError)
					}
				}
				return nil
			})
			waitIdle()
			if provider.count() != 2 {
				t.Fatalf("continue requests = %d", provider.count())
			}
			for i := 0; i < 2; i++ {
				tools := providerToolNames(t, provider.body(i))
				if !tools["read"] || tools["write"] || tools["edit"] || tools["bash"] {
					t.Fatalf("request %d changed the captured selection: %v", i, tools)
				}
			}
			builtinUI(t, h, func() error {
				ct := h.app.selectedContact()
				current := ct.SessionID
				if kind == "work" {
					current = ct.issueByID(issueID).WorkSessionID
				}
				if current != sessionID {
					return errors.New("continue silently replaced the session")
				}
				return nil
			})
			history, err := h.app.rt.historyPage(sessionID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			starts := 0
			for _, record := range history.Records {
				if record.Record.Type == "task_started" {
					starts++
				}
			}
			if starts != 2 {
				t.Fatalf("retained session history has %d tasks, want 2", starts)
			}
		})
	}
}

func TestBuiltinSelectionUnknownRecoveryKeepsSessionAndNewProfile(t *testing.T) {
	binary := requireRuntimeBinary(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", ensureIssueToolTestHelper(t))
	proxy, countPath := taskContextProxy(t, binary)
	provider := &providerFixture{Arrived: make(chan struct{})}
	h := startIssueToolTestHarness(t, proxy, provider.handler(t, "builtin-unknown"))
	h.pump.wait(func() bool { return h.app.runtimeBuiltinToolsLoaded })
	selectBuiltinSubsetThroughProfile(t, h, []string{"read"})
	builtinUI(t, h, func() error {
		h.app.selectedContact().Draft = "Keep this uncertain execution."
		if !h.app.sendCurrent() {
			return errors.New(h.app.selectedContact().SendError)
		}
		return nil
	})
	h.pump.wait(func() bool { return h.app.selectedContact().SendState == sendUnknown })
	select {
	case <-provider.Arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("accepted task never reached provider")
	}
	var sessionID string
	builtinUI(t, h, func() error {
		ct := h.app.selectedContact()
		sessionID = ct.SessionID
		draft := contactProfileDraft(ct)
		empty := []string{}
		draft.BuiltinTools = &empty
		if !h.app.saveContactProfileDraftFor(ct.ID, draft, false) {
			return errors.New("could not save future default during unknown")
		}
		return nil
	})
	h.shutdown(t)
	restarted := restartGUIHarness(t, proxy)
	defer restarted.shutdown(t)
	restarted.pump.wait(func() bool { return restarted.app.selectedContact().Loaded })
	builtinUI(t, restarted, func() error {
		ct := restarted.app.selectedContact()
		if ct.SessionID != sessionID || ct.SendState != sendUnknown || ct.BuiltinTools == nil || len(*ct.BuiltinTools) != 0 {
			return errors.New("restart lost unknown protection, session or future empty default")
		}
		restarted.app.reloadSelected()
		return nil
	})
	restarted.pump.wait(func() bool { return restarted.app.selectedContact().SendState == sendIdle })
	config, err := restarted.app.rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(config.EffectiveConfig.ToolSelection.Builtin, []string{"read"}) {
		t.Fatalf("recovery rebound session to profile: %v", config.EffectiveConfig.ToolSelection.Builtin)
	}
	if provider.count() != 1 {
		t.Fatalf("unknown recovery replayed %d requests", provider.count())
	}
	calls, err := os.ReadFile(countPath)
	if err != nil || string(calls) != "task.send\n" {
		t.Fatalf("task send count = %q, %v", calls, err)
	}
}
