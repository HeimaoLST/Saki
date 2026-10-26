package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestIssueToolExistingSessionGetsRoleWithoutRebindingProfile(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected model request", http.StatusInternalServerError)
	})
	h := startIssueToolTestHarness(t, binary, provider)
	var sessionID, promptBefore string
	var rt *runtimeClient
	var setupErr error
	h.pump.do(func() {
		ct := h.app.selectedContact()
		session, err := h.app.rt.createConfiguredConversationSession(h.root, "OLD SESSION PROMPT", nil)
		if err != nil {
			setupErr = err
			return
		}
		sessionID, rt = session.SessionID, h.app.rt
		ct.SessionID, ct.Loaded = sessionID, true
		ct.Purpose, ct.Instructions = "CURRENT PROFILE PURPOSE", "CURRENT PROFILE INSTRUCTIONS"
		if err := h.app.persist(); err != nil {
			setupErr = err
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	configBefore, err := rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	promptBefore = configBefore.EffectiveConfig.SystemPrompt
	if promptBefore != "OLD SESSION PROMPT" || runtimeSelectionHasIssueTools(configBefore.EffectiveConfig.ToolSelection) {
		t.Fatalf("precondition = prompt=%q issueTools=%v", promptBefore, runtimeSelectionHasIssueTools(configBefore.EffectiveConfig.ToolSelection))
	}
	configured, err := h.app.issueToolEnsureConversationSession(rt, sessionID, issueToolConversationGuidance, false)
	if err != nil || !configured {
		t.Fatalf("ensure existing session = %v err=%v", configured, err)
	}
	configAfter, err := rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	wantPrompt := issueToolConversationGuidance + "\n\n" + promptBefore
	if configAfter.SessionID != configBefore.SessionID || configAfter.EffectiveConfig.SystemPrompt != wantPrompt ||
		!runtimeSelectionHasIssueTools(configAfter.EffectiveConfig.ToolSelection) {
		t.Fatalf("existing session update = session=%q prompt=%q tools=%v", configAfter.SessionID, configAfter.EffectiveConfig.SystemPrompt, configAfter.EffectiveConfig.ToolSelection)
	}
	if strings.Contains(configAfter.EffectiveConfig.SystemPrompt, "CURRENT PROFILE") {
		t.Fatal("existing session was rebound to the current contact profile")
	}
	var currentPurpose, currentInstructions string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		currentPurpose, currentInstructions = ct.Purpose, ct.Instructions
	})
	if currentPurpose != "CURRENT PROFILE PURPOSE" || currentInstructions != "CURRENT PROFILE INSTRUCTIONS" {
		t.Fatal("existing session update changed the contact profile")
	}
	// A second idle ensure is a no-op and does not create a replacement session.
	again, err := h.app.issueToolEnsureConversationSession(rt, sessionID, issueToolConversationGuidance, false)
	if err != nil || !again {
		t.Fatalf("second ensure = %v err=%v", again, err)
	}
	configAgain, err := rt.connectionConfig(sessionID)
	if err != nil || configAgain.SessionID != sessionID || configAgain.EffectiveConfig.SystemPrompt != wantPrompt {
		t.Fatalf("second ensure changed session: err=%v session=%q prompt=%q", err, configAgain.SessionID, configAgain.EffectiveConfig.SystemPrompt)
	}
}

func TestIssueToolNewSessionPromptCombinesRoleAndProfile(t *testing.T) {
	ct := &contact{ID: "profile-contact", Purpose: "Keep the launch notes.", Instructions: "Cite earlier decisions."}
	a := &appModel{}
	if got := a.issueToolConversationSystemPrompt(ct); got != "Purpose: Keep the launch notes.\nInstructions: Cite earlier decisions." {
		t.Fatalf("disabled issue tools changed the profile prompt: %q", got)
	}
	a.issueToolServer = &issueToolIPCServer{}
	a.issueToolEndpoint, a.issueToolHelper = "/tmp/issue.sock", "/tmp/helper"
	a.connectionManaged = true
	got := a.issueToolConversationSystemPrompt(ct)
	if !strings.HasPrefix(got, issueToolConversationGuidance+"\n\n") ||
		!strings.Contains(got, "Purpose: Keep the launch notes.") ||
		!strings.Contains(got, "Instructions: Cite earlier decisions.") {
		t.Fatalf("combined prompt = %q", got)
	}
	if ct.Purpose != "Keep the launch notes." || ct.Instructions != "Cite earlier decisions." {
		t.Fatal("prompt construction mutated the contact profile")
	}
	empty := &contact{ID: "empty-profile"}
	if got := a.issueToolConversationSystemPrompt(empty); got != issueToolConversationGuidance {
		t.Fatalf("empty-profile prompt = %q", got)
	}
	if got := a.issueToolWorkSystemPrompt(empty); got != issueToolWorkGuidance {
		t.Fatalf("empty-profile work prompt = %q", got)
	}
}

func TestIssueToolWorkflowGuidanceDoesNotCreateDiscussionSideEffects(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := issueResultPageFinal("DISCUSSION-ONLY")
		data, _ := json.Marshal(response)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})
	h := startIssueToolTestHarness(t, binary, handler)
	var sendErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "DISCUSSION-ONLY"
		if !h.app.sendCurrent() {
			sendErr = ct.SendError
		}
	})
	if sendErr != "" {
		t.Fatal(sendErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct != nil && ct.SendState == sendIdle && ct.ActiveTaskID == ""
	})
	var issues int
	h.pump.do(func() { issues = len(h.app.selectedContact().Issues) })
	if issues != 0 {
		t.Fatalf("ordinary discussion created %d issues", issues)
	}
}

func TestIssueToolWorkflowGuidanceRequiresManagedConnection(t *testing.T) {
	ct := &contact{Purpose: "Keep profile", Instructions: "Keep instructions"}
	a := &appModel{
		issueToolServer: &issueToolIPCServer{}, issueToolEndpoint: "/tmp/issue.sock",
		issueToolHelper: "/tmp/helper", connectionManaged: false,
	}
	profile := "Purpose: Keep profile\nInstructions: Keep instructions"
	if got := a.issueToolConversationSystemPrompt(ct); got != profile {
		t.Fatalf("legacy conversation prompt = %q", got)
	}
	if got := a.issueToolWorkSystemPrompt(ct); got != profile {
		t.Fatalf("legacy work prompt = %q", got)
	}
	a.connectionManaged = true
	if got := a.issueToolConversationSystemPrompt(ct); !strings.Contains(got, issueToolConversationGuidance) {
		t.Fatalf("managed conversation prompt missing guidance: %q", got)
	}
	if got := a.issueToolWorkSystemPrompt(ct); !strings.Contains(got, issueToolWorkGuidance) {
		t.Fatalf("managed work prompt missing guidance: %q", got)
	}
	a.connectionManaged = false
	if got := a.issueToolConversationSystemPrompt(ct); got != profile {
		t.Fatalf("legacy conversation prompt changed after managed toggle: %q", got)
	}
}

func TestIssueToolExistingWorkSessionGetsRoleWithoutRebindingProfile(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected model request", http.StatusInternalServerError)
	})
	h := startIssueToolTestHarness(t, binary, provider)
	var sessionID string
	var rt *runtimeClient
	var setupErr error
	h.pump.do(func() {
		ct := h.app.selectedContact()
		session, err := h.app.rt.createConfiguredConversationSession(h.root, "OLD WORK PROMPT", nil)
		if err != nil {
			setupErr = err
			return
		}
		sessionID, rt = session.SessionID, h.app.rt
		ct.SessionID, ct.Loaded = sessionID, true
		ct.Purpose, ct.Instructions = "CURRENT WORK PROFILE", "CURRENT WORK INSTRUCTIONS"
		if err := h.app.persist(); err != nil {
			setupErr = err
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	if configured, err := h.app.issueToolEnsureWorkSession(rt, sessionID, issueToolWorkGuidance, false); err != nil || !configured {
		t.Fatalf("ensure work = %v err=%v", configured, err)
	}
	config, err := rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	want := issueToolWorkGuidance + "\n\nOLD WORK PROMPT"
	if config.EffectiveConfig.SystemPrompt != want || !runtimeSelectionHasWorkWait(config.EffectiveConfig.ToolSelection) {
		t.Fatalf("work session role/prompt = %q tools=%v", config.EffectiveConfig.SystemPrompt, config.EffectiveConfig.ToolSelection)
	}
	if strings.Contains(config.EffectiveConfig.SystemPrompt, "CURRENT WORK PROFILE") {
		t.Fatal("work session was rebound to the current contact profile")
	}
}
