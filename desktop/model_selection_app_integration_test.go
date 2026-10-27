package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func openModelGUI(t *testing.T, binary, api string, provider *modelProviderFixture) *guiHarness {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(provider.handler))
	t.Cleanup(server.Close)
	isolateConnectionApp(t, t.TempDir(), binary)
	h := openConnectionHarness(t)
	config := testConnectionConfig(t)
	config.API, config.Endpoint, config.Model = api, server.URL+"/v1", "model-A"
	saveAppConnection(t, h, config)
	applyAppConnection(t, h)
	h.pump.do(func() { h.app.closeAux() })
	return h
}

func openModelPanel(t *testing.T, h *guiHarness, entry string) {
	t.Helper()
	var clickErr error
	h.pump.do(func() {
		tr := ui.NewTester(h.app.view, 1280, 720)
		tr.Frame()
		clickErr = tr.Click(entry)
	})
	if clickErr != nil {
		t.Fatal(clickErr)
	}
	h.pump.wait(func() bool { return h.app.modelForm != nil && !h.app.modelForm.Busy })
	h.pump.do(func() {
		if h.app.modelForm.Unconfirmed || h.app.modelForm.Error != "" {
			t.Error("could not read the selected session model: " + h.app.modelForm.Error)
		}
	})
}

func clickModelAction(t *testing.T, h *guiHarness, action string) {
	t.Helper()
	var clickErr error
	h.pump.do(func() {
		tr := ui.NewTester(h.app.view, 1280, 720)
		tr.Frame()
		clickErr = clickConnectionAction(tr, action)
	})
	if clickErr != nil {
		t.Fatal(clickErr)
	}
}

func updateModelGUI(t *testing.T, h *guiHarness, model string) {
	t.Helper()
	h.pump.do(func() { h.app.modelForm.Draft.Model = model })
	clickModelAction(t, h, "Update model")
	h.pump.wait(func() bool { return !h.app.modelForm.Busy })
	h.pump.do(func() {
		form := h.app.modelForm
		if form.Error != "" || form.Unconfirmed || form.Effective.Config.EffectiveConfig.Model != model {
			t.Errorf("model update did not become effective: model=%s error=%s unknown=%v", form.Effective.Config.EffectiveConfig.Model, form.Error, form.Unconfirmed)
		}
	})
}

func previewModelGUI(t *testing.T, h *guiHarness, name string) {
	t.Helper()
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	h.pump.do(func() {
		originalWidth := h.app.windowWidth
		for _, width := range []int{960, 1280} {
			for _, dark := range []bool{false, true} {
				h.app.windowWidth = width
				tr := ui.NewTester(h.app.view, width, 720)
				tr.SetDark(dark)
				tr.Frame()
				writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("model-app-%s-%d-dark-%t.png", name, width, dark)), tr.Image())
			}
		}
		h.app.windowWidth = originalWidth
	})
}

func TestModelSelectionGUIActualTargetsAndRestart(t *testing.T) {
	binary := requireRuntimeBinary(t)
	for _, api := range []string{connectionAPIChatCompletions, connectionAPIResponses} {
		t.Run(api, func(t *testing.T) {
			provider := &modelProviderFixture{}
			h := openModelGUI(t, binary, api, provider)
			conversation := sendConnectionConversation(t, h, "Remember this original conversation", "model fixture answer")
			var contactID string
			h.pump.do(func() { contactID = h.app.selectedContact().ID })
			openModelPanel(t, h, "Conversation model")
			h.pump.do(func() {
				if h.app.modelForm.Target.Config.SessionID != conversation || h.app.modelForm.Effective.Config.EffectiveConfig.Model != "model-A" {
					t.Error("conversation entry did not display its Runtime configuration")
				}
			})
			updateModelGUI(t, h, "model-B")
			previewModelGUI(t, h, api+"-conversation")
			clickModelAction(t, h, "Cancel")
			sendConnectionConversation(t, h, "Continue the original conversation", "model fixture answer")
			var issueID, workSession string
			h.pump.do(func() {
				if !h.app.createIssue("Independent work", "Keep this work separate") {
					t.Error(h.app.issueFormError)
					return
				}
				issueID = h.app.selectedContact().SelectedIssueID
				if !h.app.runIssue(issueID) {
					t.Error(h.app.issueFormError)
				}
			})
			h.pump.wait(func() bool {
				issue := h.app.selectedContact().issueByID(issueID)
				return issue != nil && len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted && !issue.Work.HistoryDirty
			})
			h.pump.do(func() {
				issue := h.app.selectedContact().issueByID(issueID)
				workSession = issue.WorkSessionID
				h.app.workspaceOpen = true
				if workSession == conversation || workSession == "" {
					t.Error("work and conversation shared an execution context")
				}
			})
			if err := issueResultClick(h, "More issue actions"); err != nil {
				t.Error(err)
				return
			}
			openModelPanel(t, h, "Issue model")
			h.pump.do(func() {
				if h.app.modelForm.Target.Config.SessionID != workSession || h.app.modelForm.Effective.Config.EffectiveConfig.Model != "model-A" {
					t.Error("issue model inherited the conversation selection")
				}
			})
			updateModelGUI(t, h, "work-model-B")
			previewModelGUI(t, h, api+"-issue")
			clickModelAction(t, h, "Cancel")
			h.pump.do(func() {
				if !h.app.runIssue(issueID) {
					t.Error(h.app.issueFormError)
				}
			})
			h.pump.wait(func() bool {
				issue := h.app.selectedContact().issueByID(issueID)
				return len(issue.Executions) == 2 && issue.Executions[1].Status == issueExecutionCompleted && !issue.Work.HistoryDirty
			})
			h.shutdown(t)
			restarted := openConnectionHarness(t)
			restarted.pump.wait(func() bool {
				ct := restarted.app.selectedContact()
				issue := ct.issueByID(issueID)
				return ct.Loaded && issue != nil && issue.Work.Loaded
			})
			restarted.pump.do(func() {
				ct := restarted.app.selectedContact()
				issue := ct.issueByID(issueID)
				if ct.ID != contactID || ct.SessionID != conversation || ct.SessionModel != "model-B" || issue.WorkSessionID != workSession || len(issue.Executions) != 2 {
					t.Error("restart lost the target identity, effective model or execution association")
				}
				if !containsText(contactMessageTexts(ct), "Remember this original conversation") {
					t.Error("model selection discarded previous conversation history")
				}
			})
			sendConnectionConversation(t, restarted, "Use the restored conversation model", "model fixture answer")
			openModelPanel(t, restarted, "Conversation model")
			updateModelGUI(t, restarted, "model-A")
			clickModelAction(t, restarted, "Cancel")
			sendConnectionConversation(t, restarted, "Return to A", "model fixture answer")
			previewModelGUI(t, restarted, api+"-workspace")
			want := []string{"model-A", "model-B", "model-A", "work-model-B", "model-B", "model-A"}
			if !reflect.DeepEqual(provider.requestedModels(), want) {
				t.Fatalf("actual requests did not follow each target: %v", provider.requestedModels())
			}
			var rt *runtimeClient
			restarted.pump.do(func() { rt = restarted.app.rt })
			workConfig, err := rt.connectionConfig(workSession)
			if err != nil || workConfig.EffectiveConfig.Model != "work-model-B" {
				t.Fatalf("conversation update rewrote the work model: %v", err)
			}
		})
	}
}

func TestModelSelectionGUIRefusalConflictAndCancelPreserveHistory(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &modelProviderFixture{}
	h := openModelGUI(t, binary, connectionAPIResponses, provider)
	sessionID := sendConnectionConversation(t, h, "A history that must survive", "model fixture answer")
	var rt *runtimeClient
	var historyPath string
	h.pump.do(func() {
		rt = h.app.rt
		historyPath = filepath.Join(h.app.sessionDir, sessionID, "history.jsonl")
	})
	before, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	openModelPanel(t, h, "Conversation model")
	h.pump.do(func() { h.app.modelForm.Draft.Model = "canceled-model" })
	clickModelAction(t, h, "Cancel")
	actual, err := rt.connectionConfig(sessionID)
	if err != nil || actual.ConfigRevision != initial.ConfigRevision || actual.EffectiveConfig.Model != "model-A" {
		t.Fatal("Cancel submitted a model update")
	}
	openModelPanel(t, h, "Conversation model")
	h.pump.do(func() {
		h.app.modelForm.Draft.Model = "invalid-model"
		h.app.modelForm.Draft.ContextTokens = "2000"
		h.app.modelForm.Draft.OutputTokens = "1000"
	})
	clickModelAction(t, h, "Update model")
	h.pump.wait(func() bool { return !h.app.modelForm.Busy })
	h.pump.do(func() {
		form := h.app.modelForm
		if !strings.Contains(form.Error, "invalid_model_limits") || form.Effective.Config.EffectiveConfig.Model != "model-A" || form.Draft.Model != "invalid-model" {
			t.Errorf("invalid config did not retain actual value and repairable draft: %s", form.Error)
		}
		form.ResetDraft()
	})
	capacity := initial.EffectiveConfig.Compaction
	changed, err := rt.updateSessionModel(sessionID, initial.ConfigRevision, runtimeModelPatch{Model: "external-model", Compaction: &capacity})
	if err != nil {
		t.Fatal(err)
	}
	h.pump.do(func() { h.app.modelForm.Draft.Model = "stale-model" })
	clickModelAction(t, h, "Update model")
	h.pump.wait(func() bool { return !h.app.modelForm.Busy })
	h.pump.do(func() {
		form := h.app.modelForm
		if !strings.Contains(form.Error, "config_conflict") || form.Effective.Config.ConfigRevision != changed.ConfigRevision || form.Effective.Config.EffectiveConfig.Model != "external-model" || form.Draft.Model != "stale-model" {
			t.Errorf("conflict did not read actual value without retry: %s", form.Error)
		}
	})
	if _, err := rt.updateSessionModel(sessionID, changed.ConfigRevision, runtimeModelPatch{Model: "refreshed-model", Compaction: &capacity}); err != nil {
		t.Fatal(err)
	}
	clickModelAction(t, h, "Refresh")
	h.pump.wait(func() bool { return !h.app.modelForm.Busy })
	h.pump.do(func() {
		if h.app.selectedContact().SessionModel != "refreshed-model" || h.app.modelForm.Draft.Model != "stale-model" {
			t.Error("Refresh did not update the main model display while preserving the correction")
		}
	})
	after, err := os.ReadFile(historyPath)
	if err != nil || string(after) != string(before) || len(provider.requestedModels()) != 1 {
		t.Fatal("configuration actions rewrote history or submitted inference")
	}
}

func modelGUIProxy(t *testing.T, binary, mode string) (string, string) {
	t.Helper()
	proxy := filepath.Join(t.TempDir(), "model-proxy")
	executable := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
	if err := os.WriteFile(proxy, []byte("#!/bin/sh\nexec "+executable+" -test.run=^TestModelSelectionProxyHelper$\n"), 0700); err != nil {
		t.Fatal(err)
	}
	countPath := filepath.Join(t.TempDir(), "updates")
	t.Setenv("SAKI_MODEL_PROXY", "1")
	t.Setenv("SAKI_MODEL_PROXY_BINARY", binary)
	t.Setenv("SAKI_MODEL_PROXY_COUNT", countPath)
	t.Setenv("SAKI_MODEL_PROXY_MODE", mode)
	return proxy, countPath
}

func TestModelSelectionGUILostReplyAndQuitWaitForReadback(t *testing.T) {
	proxy, countPath := modelGUIProxy(t, requireRuntimeBinary(t), "")
	provider := &modelProviderFixture{}
	h := openModelGUI(t, proxy, connectionAPIResponses, provider)
	sessionID := sendConnectionConversation(t, h, "Keep the original history", "model fixture answer")
	openModelPanel(t, h, "Conversation model")
	h.pump.do(func() { h.app.modelForm.Draft.Model = "model-B" })
	clickModelAction(t, h, "Update model")
	h.pump.do(func() {
		if h.app.connectionReloadBlocked() == "" || h.app.canSend(h.app.selectedContact()) == "" {
			t.Error("in-flight model update allowed helper replacement or another send on the same session")
		}
	})
	h.pump.waitTimeout(func() bool { return !h.app.modelForm.Busy }, 30*time.Second)
	h.pump.do(func() {
		form := h.app.modelForm
		if form.Unconfirmed || form.Effective.Config.EffectiveConfig.Model != "model-B" || !strings.Contains(form.Error, "timed out") || h.app.selectedContact().SessionModel != "model-B" {
			t.Errorf("lost reply did not display the confirmed readback and original error: %s", form.Error)
		}
	})
	updates, err := os.ReadFile(countPath)
	if err != nil || string(updates) != "update\n" {
		t.Fatal("uncertain GUI update was dispatched again")
	}
	clickModelAction(t, h, "Cancel")
	sendConnectionConversation(t, h, "Use the confirmed model", "model fixture answer")
	openModelPanel(t, h, "Conversation model")
	manual := make(chan func(), 16)
	quit := make(chan struct{}, 1)
	h.pump.do(func() {
		h.app.setUIHook(func(fn func()) { manual <- fn })
		h.app.modelForm.Draft.Model = "model-A"
		request, ok := h.app.modelForm.Request()
		if !ok {
			t.Error(h.app.modelForm.Error)
			return
		}
		h.app.updateSessionModel(request)
		h.app.requestQuit = func() { quit <- struct{}{} }
		h.app.beginShutdown()
		if h.app.shutdownDrainStarted || h.app.quitReady {
			t.Error("Quit closed the helper before model reconciliation finished")
		}
	})
	var completion func()
	select {
	case completion = <-manual:
	case <-time.After(10 * time.Second):
		t.Fatal("model update callback did not arrive")
	}
	h.pump.do(func() {
		h.app.setUIHook(func(fn func()) { h.pump.q <- fn })
		completion()
	})
	select {
	case <-quit:
	case <-time.After(10 * time.Second):
		t.Fatal("Quit did not resume after the model operation finished")
	}
	h.pump.wait(func() bool { return h.app.runtimeStatus == runtimeStateExited })
	h.pump.close()
	h.closed = true
	restarted := openConnectionHarness(t)
	restarted.pump.wait(func() bool { return restarted.app.selectedContact().Loaded })
	restarted.pump.do(func() {
		ct := restarted.app.selectedContact()
		if ct.SessionID != sessionID || ct.SessionModel != "model-A" || !containsText(contactMessageTexts(ct), "Keep the original history") {
			t.Error("quit/restart lost the confirmed configuration or original context")
		}
	})
}

func TestModelSelectionGUIReadbackFailureStaysUnknownAfterClosingPanel(t *testing.T) {
	proxy, countPath := modelGUIProxy(t, requireRuntimeBinary(t), "fail_readback")
	h := openModelGUI(t, proxy, connectionAPIResponses, &modelProviderFixture{})
	sendConnectionConversation(t, h, "Retain this context", "model fixture answer")
	openModelPanel(t, h, "Conversation model")
	h.pump.do(func() { h.app.modelForm.Draft.Model = "model-B" })
	clickModelAction(t, h, "Update model")
	h.pump.waitTimeout(func() bool { return !h.app.modelForm.Busy }, 30*time.Second)
	h.pump.do(func() {
		if !h.app.modelForm.Unconfirmed || h.app.canSend(h.app.selectedContact()) == "" || h.app.selectedContact().SessionModel != "" {
			t.Error("failed readback did not preserve an explicit unknown state")
		}
	})
	clickModelAction(t, h, "Cancel")
	h.pump.do(func() {
		if h.app.canSend(h.app.selectedContact()) == "" {
			t.Error("closing the panel silently cleared an unknown model update")
		}
	})
	openModelPanel(t, h, "Conversation model")
	h.pump.do(func() {
		if h.app.modelForm.Effective.Config.EffectiveConfig.Model != "model-B" || h.app.selectedContact().SessionModel != "model-B" || h.app.canSend(h.app.selectedContact()) != "" {
			t.Error("successful re-read did not restore the actual model and task admission")
		}
	})
	updates, err := os.ReadFile(countPath)
	if err != nil || string(updates) != "update\n" {
		t.Fatal("failed readback caused another model mutation")
	}
}

func TestModelSelectionGUIHelperApplyClearsOldRecoveryState(t *testing.T) {
	proxy, _ := modelGUIProxy(t, requireRuntimeBinary(t), "commit_unknown")
	h := openModelGUI(t, proxy, connectionAPIResponses, &modelProviderFixture{})
	sessionID := sendConnectionConversation(t, h, "Keep this history when replacing the helper", "model fixture answer")
	openModelPanel(t, h, "Conversation model")
	h.pump.do(func() { h.app.modelForm.Draft.Model = "model-B" })
	clickModelAction(t, h, "Update model")
	h.pump.wait(func() bool { return !h.app.modelForm.Busy })
	var previous *runtimeClient
	h.pump.do(func() {
		previous = h.app.rt
		if h.app.canSend(h.app.selectedContact()) == "" {
			t.Error("commit_unknown was not protected before replacement")
		}
		h.app.closeAux()
		h.app.openConfiguration()
	})
	applyAppConnection(t, h)
	h.pump.wait(func() bool { return h.app.selectedContact().Loaded })
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if h.app.rt == previous || ct.SessionID != sessionID || ct.SessionModel != "model-B" || h.app.canSend(ct) != "" || !containsText(contactMessageTexts(ct), "Keep this history when replacing the helper") {
			t.Error("new helper retained the old quarantine or discarded the real session")
		}
	})
}

func TestModelSelectionGUICommitUnknownRequiresExplicitRecovery(t *testing.T) {
	// The proxy reports a controlled commit_unknown after a real successful
	// update. Runtime's filesystem failure injection is covered by its own tests.
	proxy, countPath := modelGUIProxy(t, requireRuntimeBinary(t), "commit_unknown")
	provider := &modelProviderFixture{}
	h := openModelGUI(t, proxy, connectionAPIResponses, provider)
	sessionID := sendConnectionConversation(t, h, "Retain history during recovery", "model fixture answer")
	openModelPanel(t, h, "Conversation model")
	h.pump.do(func() { h.app.modelForm.Draft.Model = "model-B" })
	clickModelAction(t, h, "Update model")
	h.pump.wait(func() bool { return !h.app.modelForm.Busy })
	h.pump.do(func() {
		if !h.app.modelForm.Unconfirmed || h.app.canSend(h.app.selectedContact()) == "" {
			t.Error("commit_unknown did not require explicit recovery")
		}
		h.app.modelForm.Draft.Model = "unsent-correction"
	})
	clickModelAction(t, h, "Refresh")
	h.pump.wait(func() bool { return !h.app.modelForm.Busy })
	h.pump.do(func() {
		if !h.app.modelForm.Unconfirmed || h.app.canSend(h.app.selectedContact()) == "" {
			t.Error("config.get alone released a quarantined context")
		}
	})
	clickModelAction(t, h, "Reload session")
	h.pump.wait(func() bool { return !h.app.modelForm.Busy && h.app.canSend(h.app.selectedContact()) == "" })
	h.pump.do(func() {
		form := h.app.modelForm
		ct := h.app.selectedContact()
		if form.Unconfirmed || form.Effective.Config.EffectiveConfig.Model != "model-B" || form.Draft.Model != "unsent-correction" || ct.SessionID != sessionID || !containsText(contactMessageTexts(ct), "Retain history during recovery") {
			t.Error("explicit recovery lost the actual value, repairable draft or original history")
		}
	})
	clickModelAction(t, h, "Cancel")
	sendConnectionConversation(t, h, "Continue with the recovered model", "model fixture answer")
	if !reflect.DeepEqual(provider.requestedModels(), []string{"model-A", "model-B"}) {
		t.Fatal("recovery submitted the unsent correction")
	}
	updates, err := os.ReadFile(countPath)
	if err != nil || string(updates) != "update\n" {
		t.Fatal("recovery resubmitted the uncertain mutation")
	}
}

func TestModelSelectionGUIOlderReadCannotReplaceNewTarget(t *testing.T) {
	h := openModelGUI(t, requireRuntimeBinary(t), connectionAPIResponses, &modelProviderFixture{})
	first := sendConnectionConversation(t, h, "First context", "model fixture answer")
	h.pump.do(func() { h.app.addDefaultContact() })
	second := sendConnectionConversation(t, h, "Second context", "model fixture answer")
	manual := make(chan func(), 16)
	h.pump.do(func() {
		h.app.setUIHook(func(fn func()) { manual <- fn })
		h.app.openSessionModel(first, "conversation", "First")
	})
	readCallback := func() func() {
		t.Helper()
		select {
		case fn := <-manual:
			return fn
		case <-time.After(10 * time.Second):
			t.Fatal("model read callback did not arrive")
			return nil
		}
	}
	older := readCallback()
	h.pump.do(func() {
		h.app.closeAux()
		h.app.openSessionModel(second, "conversation", "Second")
	})
	newer := readCallback()
	h.pump.do(func() {
		h.app.setUIHook(func(fn func()) { h.pump.q <- fn })
		newer()
		older()
		if h.app.modelForm == nil || h.app.modelForm.Target.Config.SessionID != second || h.app.selectedContact().SessionID != second {
			t.Error("older model read overwrote the newly selected target")
		}
	})
}
