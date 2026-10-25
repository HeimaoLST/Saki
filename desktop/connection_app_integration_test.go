package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func openConnectionHarness(t *testing.T) *guiHarness {
	t.Helper()
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	h := &guiHarness{app: a, pump: newUIPump(t, a)}
	t.Cleanup(func() { h.shutdown(t) })
	h.pump.do(func() { a.startRuntime() })
	return h
}

func clickConnectionAction(tr *ui.Tester, action string) error {
	for i := 0; i < 12; i++ {
		if r, ok := tr.Find(action); ok && r.W > 0 && r.H > 0 && r.Y >= 0 && r.Y+r.H <= 720 {
			return tr.Click(action)
		}
		tr.Scroll(520, 360, 0, 180)
		tr.Frame()
	}
	return fmt.Errorf("connection action %q was not reachable by scrolling", action)
}

func saveAppConnection(t *testing.T, h *guiHarness, config connectionConfig) {
	t.Helper()
	h.pump.do(func() {
		h.app.openConfiguration()
		h.app.connectionForm.Draft = newConnectionSettings(&config, "").Draft
		h.app.saveConnection(config)
	})
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	h.pump.do(func() {
		if h.app.connectionForm.Error != "" {
			t.Error("configuration save failed: " + h.app.connectionForm.Error)
		}
	})
}

func applyAppConnection(t *testing.T, h *guiHarness) {
	t.Helper()
	h.pump.do(func() { h.app.applyConnection() })
	h.pump.wait(func() bool {
		return !h.app.connectionForm.Busy && h.app.runtimeStatus == runtimeStateReady && h.app.runtimeCapabilitiesLoaded
	})
	h.pump.do(func() {
		if h.app.connectionForm.Error != "" {
			t.Error("configuration apply failed: " + h.app.connectionForm.Error)
		}
	})
}

func sendConnectionConversation(t *testing.T, h *guiHarness, text, result string) string {
	t.Helper()
	accepted := false
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = text
		accepted = h.app.send(ct, text)
	})
	if !accepted {
		t.Fatal("conversation was not admitted")
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && !ct.HistoryDirty && containsText(contactMessageTexts(ct), result)
	})
	var id string
	h.pump.do(func() { id = h.app.selectedContact().SessionID })
	return id
}

func TestConnectionGUIFirstSaveApplyTestSendAndRestart(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{}
	server := httptest.NewServer(provider.handler(t, "first-use"))
	t.Cleanup(server.Close)
	root := t.TempDir()
	isolateConnectionApp(t, root, binary)
	h := openConnectionHarness(t)
	config := testConnectionConfig(t)
	config.Endpoint, config.Model = server.URL, "first-model"
	var clickErr error
	h.pump.do(func() {
		h.app.connectionForm.Draft = newConnectionSettings(&config, "").Draft
		tr := ui.NewTester(h.app.view, 1040, 720)
		tr.Frame()
		clickErr = clickConnectionAction(tr, "Save configuration")
		tr.Frame()
	})
	if clickErr != nil {
		t.Fatal(clickErr)
	}
	h.pump.wait(func() bool { return h.app.connectionForm.HasSaved && !h.app.connectionForm.Busy })
	if provider.count() != 0 {
		t.Fatal("the Save button called the model")
	}
	h.pump.do(func() {
		if h.app.selectedContact().Workspace != config.WorkingDirectory {
			t.Error("the first real contact did not use the chosen working directory")
		}
		tr := ui.NewTester(h.app.view, 1040, 720)
		tr.Frame()
		clickErr = clickConnectionAction(tr, "Apply saved")
		tr.Frame()
	})
	if clickErr != nil {
		t.Fatal(clickErr)
	}
	h.pump.wait(func() bool {
		return h.app.runtimeStatus == runtimeStateReady && h.app.runtimeCapabilitiesLoaded && !h.app.connectionForm.Busy
	})
	if provider.count() != 0 {
		t.Fatal("Apply performed a model test without an explicit request")
	}
	beforeTest, err := os.ReadFile(guiStorePath())
	if err != nil {
		t.Fatal(err)
	}
	h.pump.do(func() {
		tr := ui.NewTester(h.app.view, 1040, 720)
		tr.Frame()
		clickErr = clickConnectionAction(tr, "Test connection and model")
		tr.Frame()
	})
	if clickErr != nil {
		t.Fatal(clickErr)
	}
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	if provider.count() != 1 {
		t.Fatal("the actual Test button did not run exactly one fixture request")
	}
	afterTest, err := os.ReadFile(guiStorePath())
	if err != nil || string(beforeTest) != string(afterTest) {
		t.Fatal("the connection test changed contacts or their work history")
	}
	oldID := sendConnectionConversation(t, h, "first real conversation", "first-use answer")
	var actual runtimeConnectionConfig
	var client *runtimeClient
	h.pump.do(func() { client = h.app.rt })
	actual, err = client.connectionConfig(oldID)
	wantDir, pathErr := filepath.EvalSymlinks(config.WorkingDirectory)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if err != nil || actual.EffectiveConfig.ConnectionBinding.State != "bound" || actual.EffectiveConfig.WorkingDirectory != wantDir || actual.EffectiveConfig.Model != config.Model {
		t.Fatal("first GUI request did not use the saved connection/model/cwd with a bound identity")
	}
	h.pump.do(func() {
		h.app.selectedContact().Draft = "a draft across restart"
		h.app.persist()
	})
	h.shutdown(t)
	t.Setenv("API_KEY", "irrelevant-fixture-key")
	t.Setenv("SAKI_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("SAKI_MODEL", "wrong-model")
	t.Setenv("SAKI_GUI_WORKDIR", root)
	restarted := openConnectionHarness(t)
	restarted.pump.wait(func() bool {
		ct := restarted.app.selectedContact()
		return restarted.app.runtimeStatus == runtimeStateReady && ct.Loaded && len(ct.Messages) > 0
	})
	restarted.pump.do(func() {
		ct := restarted.app.selectedContact()
		if ct.SessionID != oldID || ct.Draft != "a draft across restart" || ct.SessionModel != config.Model {
			t.Error("restart did not retain the conversation, draft and effective model")
		}
	})
	if provider.count() != 2 {
		t.Fatal("restart silently submitted a model request")
	}
}

func TestConnectionGUISaveDuringIssueAndReloadFailurePreservesHelper(t *testing.T) {
	binary := requireRuntimeBinary(t)
	hold := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	provider := &providerFixture{Hold: hold, Arrived: make(chan struct{})}
	server := httptest.NewServer(provider.handler(t, "working"))
	t.Cleanup(server.Close)
	isolateConnectionApp(t, t.TempDir(), binary)
	h := openConnectionHarness(t)
	t.Cleanup(release)
	config := testConnectionConfig(t)
	config.Endpoint = server.URL
	saveAppConnection(t, h, config)
	applyAppConnection(t, h)
	var original *runtimeClient
	var issueID string
	h.pump.do(func() {
		original = h.app.rt
		h.app.createIssue("Keep the original work", "Preserve these requirements")
		issueID = h.app.selectedContact().SelectedIssueID
		if !h.app.runIssue(issueID) {
			t.Error("issue was not admitted")
		}
	})
	select {
	case <-provider.Arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("issue did not reach the provider")
	}
	var workSession string
	h.pump.do(func() { workSession = h.app.selectedContact().issueByID(issueID).WorkSessionID })
	actual, err := original.connectionConfig(workSession)
	if err != nil || actual.EffectiveConfig.ConnectionBinding.State != "bound" || actual.EffectiveConfig.Model != config.Model {
		t.Fatal("issue work did not use the configured connection")
	}
	h.pump.do(func() { h.app.addDefaultContact() })
	edited := config
	edited.Model, edited.MaxActiveTasks = "next-model", 3
	edited.WorkingDirectory = t.TempDir()
	saveAppConnection(t, h, edited)
	h.pump.do(func() {
		if h.app.workDir != config.WorkingDirectory {
			t.Error("Save changed the effective working directory before Apply")
		}
		if h.app.connectionReloadBlocked() == "" {
			t.Error("a non-selected contact's active issue did not block reload")
		}
		h.app.applyConnection()
		if h.app.rt != original {
			t.Error("Apply replaced the helper while issue work was running")
		}
	})
	select {
	case <-original.done:
		t.Fatal("saving/applying new defaults interrupted active work")
	default:
	}
	release()
	h.pump.wait(func() bool {
		issue := h.app.contacts[0].issueByID(issueID)
		return issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle && len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted
	})
	t.Setenv("SAKI_RUNTIME_BIN", filepath.Join(t.TempDir(), "missing-helper"))
	h.pump.do(func() { h.app.applyConnection() })
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	h.pump.do(func() {
		if h.app.rt != original || h.app.runtimeStatus != runtimeStateReady || h.app.connectionForm.Error == "" {
			t.Error("failed candidate start did not retain the old helper and show its failure")
		}
	})
	falseReady := filepath.Join(t.TempDir(), "ready-then-exit")
	if err := os.WriteFile(falseReady, []byte("#!/bin/sh\nprintf '%s\\n' '{\"version\":1,\"type\":\"ready\",\"api\":\"openai-responses\"}'\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAKI_RUNTIME_BIN", falseReady)
	h.pump.do(func() { h.app.applyConnection() })
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	h.pump.do(func() {
		if h.app.rt != original || h.app.runtimeStatus != runtimeStateReady || h.app.connectionForm.Error == "" {
			t.Error("ready without a validated connection replaced the working helper")
		}
	})
	sendConnectionConversation(t, h, "old helper remains usable", "working answer")
	if !strings.Contains(provider.body(1), `"model":"`+config.Model+`"`) {
		t.Fatal("failed reload silently applied the pending default model")
	}
	t.Setenv("SAKI_RUNTIME_BIN", binary)
	applyAppConnection(t, h)
	h.pump.do(func() {
		if h.app.rt == original || h.app.runtimeMaxActiveTasks != 3 {
			t.Error("successful reload did not install the candidate and reread real capacity")
		}
		if h.app.contacts[0].issueByID(issueID).Requirements != "Preserve these requirements" {
			t.Error("reload changed issue requirements")
		}
	})
}

func TestConnectionGUIQuitWaitsForTestCleanup(t *testing.T) {
	binary := requireRuntimeBinary(t)
	hold := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	provider := &providerFixture{Hold: hold, Arrived: make(chan struct{})}
	server := httptest.NewServer(provider.handler(t, "probe-cancel"))
	t.Cleanup(server.Close)
	isolateConnectionApp(t, t.TempDir(), binary)
	h := openConnectionHarness(t)
	t.Cleanup(release)
	config := testConnectionConfig(t)
	config.Endpoint = server.URL
	saveAppConnection(t, h, config)
	applyAppConnection(t, h)
	h.pump.do(func() {
		h.app.createIssue("Do not start during quit", "Keep this pending")
		h.app.testConnection(config)
	})
	select {
	case <-provider.Arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("connection test did not reach the provider")
	}
	quit := make(chan struct{}, 1)
	h.pump.do(func() {
		h.app.requestQuit = func() {
			if h.app.connectionTesting {
				t.Error("quit was requested before the test helper finished cleanup")
			}
			quit <- struct{}{}
		}
		h.app.beginShutdown()
		if h.app.send(h.app.selectedContact(), "too late") || h.app.runIssue(h.app.selectedContact().SelectedIssueID) {
			t.Error("new conversation or issue work was admitted after quit began")
		}
		if h.app.quitReady {
			t.Error("quit did not wait for the in-flight test")
		}
	})
	select {
	case <-quit:
	case <-time.After(10 * time.Second):
		t.Fatal("connection test cancellation did not allow quit")
	}
	h.pump.close()
	h.closed = true
	release()
}

func TestConnectionGUIFailedTestCanBeCorrectedWithoutSaving(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{}
	valid := provider.handler(t, "corrected")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer rejected-fixture-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("rejected-fixture-key is invalid"))
			return
		}
		valid.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	isolateConnectionApp(t, t.TempDir(), binary)
	h := openConnectionHarness(t)
	config := testConnectionConfig(t)
	config.Endpoint, config.APIKey = server.URL, "rejected-fixture-key"
	h.pump.do(func() {
		h.app.connectionForm.Draft = newConnectionSettings(&config, "").Draft
		h.app.testConnection(config)
	})
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	h.pump.do(func() {
		if h.app.connectionForm.Error == "" || strings.Contains(h.app.connectionForm.Error, config.APIKey) {
			t.Error("failed Test did not show a key-free, correctable error")
		}
		if h.app.connectionForm.Draft.APIKey != config.APIKey {
			t.Error("failed Test discarded the editable draft")
		}
	})
	config.APIKey = "corrected-fixture-key"
	h.pump.do(func() {
		h.app.connectionForm.Draft = newConnectionSettings(&config, "").Draft
		h.app.testConnection(config)
	})
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	h.pump.do(func() {
		if h.app.connectionForm.Error != "" || !strings.Contains(h.app.connectionForm.Status, "test completed") {
			t.Error("corrected Test did not succeed")
		}
		if h.app.connectionSaved != nil || h.app.connectionEffective != nil || h.app.rt != nil {
			t.Error("Test saved or applied the draft without an explicit action")
		}
	})
	if _, err := os.Stat(guiStorePath()); !os.IsNotExist(err) {
		t.Fatal("Test created a contact store")
	}
}

func TestConnectionGUISaveFailureAndUnknownExecutionRecovery(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{}
	server := httptest.NewServer(provider.handler(t, "recovery"))
	t.Cleanup(server.Close)
	isolateConnectionApp(t, t.TempDir(), binary)
	h := openConnectionHarness(t)
	config := testConnectionConfig(t)
	config.Endpoint = server.URL
	saveAppConnection(t, h, config)
	applyAppConnection(t, h)
	path := filepath.Join(filepath.Dir(guiStorePath()), "connection.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	edited := config
	edited.Model = "unsaved-new-model"
	h.pump.do(func() {
		h.app.connectionForm.Draft = newConnectionSettings(&edited, "").Draft
		h.app.saveConnection(edited)
	})
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	h.pump.do(func() {
		if h.app.connectionForm.Error == "" || h.app.connectionForm.Draft.Model != edited.Model {
			t.Error("save failure did not retain editable values and an error")
		}
	})
	after, err := os.ReadFile(path)
	if err != nil || string(original) != string(after) {
		t.Fatal("failed save replaced the existing source")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var client *runtimeClient
	var issueID string
	h.pump.do(func() {
		client = h.app.rt
		h.app.createIssue("Unconfirmed dispatch", "Preserve this before retrying")
		issue := h.app.selectedContact().selectedIssue()
		issueID = issue.ID
		issue.Executions = []*issueExecution{{ID: "unconfirmed-local-execution", Status: issueExecutionUnknown}}
		h.app.persist()
	})
	saveAppConnection(t, h, edited)
	h.pump.do(func() {
		if h.app.connectionReloadBlocked() == "" {
			t.Error("an unresolved persisted execution was ignored")
		}
		h.app.applyConnection()
		if h.app.rt != client {
			t.Error("unknown execution did not protect the current helper")
		}
		if !h.app.confirmIssueExecutionNotDispatched(issueID, "unconfirmed-local-execution") {
			t.Error("the existing explicit recovery action did not resolve the unknown dispatch")
		}
	})
	applyAppConnection(t, h)
	if provider.count() != 0 {
		t.Fatal("configuration recovery replayed an unconfirmed task")
	}
}

func TestConnectionGUIEndpointChangePreservesOldContextAndNewDefaults(t *testing.T) {
	binary := requireRuntimeBinary(t)
	providerA, providerB := &providerFixture{}, &providerFixture{}
	serverA := httptest.NewServer(providerA.handler(t, "original-context"))
	serverB := httptest.NewServer(providerB.handler(t, "new-context"))
	t.Cleanup(serverA.Close)
	t.Cleanup(serverB.Close)
	isolateConnectionApp(t, t.TempDir(), binary)
	h := openConnectionHarness(t)
	configA := testConnectionConfig(t)
	configA.Endpoint, configA.Model = serverA.URL, "model-A"
	saveAppConnection(t, h, configA)
	applyAppConnection(t, h)
	oldSession := sendConnectionConversation(t, h, "keep this original conversation", "original-context answer")
	h.pump.do(func() {
		h.app.selectedContact().Draft = "keep this old-context draft"
		h.app.persist()
	})
	configB := configA
	configB.Endpoint, configB.Model, configB.WorkingDirectory = serverB.URL, "model-B", t.TempDir()
	saveAppConnection(t, h, configB)
	applyAppConnection(t, h)
	h.pump.wait(func() bool { return h.app.contacts[0].Unavailable != "" && len(h.app.contacts[0].Messages) > 0 })
	h.pump.do(func() {
		ct := h.app.contacts[0]
		if ct.SessionID != oldSession || ct.Workspace != configA.WorkingDirectory || ct.Draft != "keep this old-context draft" || ct.Loaded {
			t.Error("endpoint change rebound or overwrote the old conversation")
		}
		if !containsText(contactMessageTexts(ct), "original-context answer") {
			t.Error("incompatible context lost its readable history")
		}
		h.app.addDefaultContact()
		if h.app.selectedContact().Workspace != configB.WorkingDirectory {
			t.Error("new contact did not use the applied default directory")
		}
	})
	newSession := sendConnectionConversation(t, h, "use new connection explicitly", "new-context answer")
	if oldSession == newSession || providerA.count() != 1 || providerB.count() != 1 || !strings.Contains(providerB.body(0), `"model":"model-B"`) {
		t.Fatal("new work did not use a new context on B without replaying A")
	}
}
