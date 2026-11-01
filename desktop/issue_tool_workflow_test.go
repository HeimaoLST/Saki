package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
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
	userPrompt := "Keep this user prompt verbatim. It quotes an old rule: When the user explicitly delegates work, use the issue tools already available in this conversation. Do not rewrite this quote."
	selection := map[string]any{
		"builtin": []string{"read", "bash"},
		"external": []map[string]string{{
			"source_id": issueToolSourceID, "tool_name": "issue_get", "exposed_name": "alias_issue_get",
		}},
	}
	h.pump.do(func() {
		ct := h.app.selectedContact()
		session, err := h.app.rt.createConfiguredConversationSession(h.root, previousIssueToolConversationGuidance+"\n\n"+userPrompt, selection)
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
	if promptBefore != previousIssueToolConversationGuidance+"\n\n"+userPrompt || runtimeSelectionHasIssueTools(configBefore.EffectiveConfig.ToolSelection) {
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
	wantPrompt := issueToolConversationGuidance + "\n\n" + userPrompt
	if configAfter.SessionID != configBefore.SessionID || configAfter.EffectiveConfig.SystemPrompt != wantPrompt ||
		!runtimeSelectionHasIssueTools(configAfter.EffectiveConfig.ToolSelection) {
		t.Fatalf("existing session update = session=%q prompt=%q tools=%v", configAfter.SessionID, configAfter.EffectiveConfig.SystemPrompt, configAfter.EffectiveConfig.ToolSelection)
	}
	if !strings.Contains(configAfter.EffectiveConfig.SystemPrompt, userPrompt) {
		t.Fatal("existing session did not preserve the user prompt after the previous app guidance")
	}
	builtin, alias := configAfter.EffectiveConfig.ToolSelection.Builtin, false
	for _, binding := range configAfter.EffectiveConfig.ToolSelection.External {
		if binding.SourceID == issueToolSourceID && binding.ToolName == "issue_get" && binding.ExposedName == "alias_issue_get" {
			alias = true
		}
	}
	builtinSet := map[string]bool{}
	for _, name := range builtin {
		builtinSet[name] = true
	}
	if len(builtin) != 2 || !builtinSet["read"] || !builtinSet["bash"] || !alias {
		t.Fatalf("existing session did not preserve builtin/alias selection: builtin=%v alias=%v", builtin, alias)
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

func TestIssueToolEnsureSkipsNoOpConfigUpdate(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected model request", http.StatusInternalServerError)
	})
	h := startIssueToolTestHarness(t, binary, handler)
	var sessionID string
	var rt *runtimeClient
	var setupErr error
	h.pump.do(func() {
		ct := h.app.selectedContact()
		session, err := h.app.rt.createConfiguredConversationSession(h.root, issueToolConversationGuidance, map[string]any{
			"builtin": []string{"read", "bash", "edit", "write"}, "external": issueToolConversationExternal(),
		})
		if err != nil {
			setupErr = err
			return
		}
		sessionID, rt = session.SessionID, h.app.rt
		ct.SessionID, ct.Loaded = sessionID, true
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	before, err := rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !runtimeSelectionHasIssueTools(before.EffectiveConfig.ToolSelection) || before.EffectiveConfig.SystemPrompt != issueToolConversationGuidance {
		t.Fatalf("precondition = tools=%v prompt=%q", before.EffectiveConfig.ToolSelection, before.EffectiveConfig.SystemPrompt)
	}
	// A new Runtime/app instance has no in-memory ready mark. Because the
	// persisted selection/prompt are already exact, ensure must not issue a
	// no-op session.config.update (which native continuation rejects).
	if configured, err := h.app.issueToolEnsureConversationSession(rt, sessionID, issueToolConversationGuidance, false); err != nil || !configured {
		t.Fatalf("ensure = %v err=%v", configured, err)
	}
	after, err := rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ConfigRevision != before.ConfigRevision || after.SessionID != before.SessionID ||
		after.EffectiveConfig.SystemPrompt != before.EffectiveConfig.SystemPrompt ||
		!runtimeSelectionHasIssueTools(after.EffectiveConfig.ToolSelection) {
		t.Fatalf("no-op ensure changed config: revision=%d/%d session=%q prompt=%q tools=%v",
			before.ConfigRevision, after.ConfigRevision, after.SessionID, after.EffectiveConfig.SystemPrompt, after.EffectiveConfig.ToolSelection)
	}
	// A real typed schema-changed error must authorize exactly one refresh.
	h.app.noteIssueToolTaskError(rt, sessionID, &runtimeError{Code: "tool_schema_changed", Message: "controlled schema change"})
	if !h.app.issueToolRefreshRequired(rt, sessionID) {
		t.Fatal("typed tool_schema_changed did not mark refresh required")
	}
	if configured, err := h.app.issueToolEnsureConversationSession(rt, sessionID, issueToolConversationGuidance, false); err != nil || !configured {
		t.Fatalf("refresh ensure = %v err=%v", configured, err)
	}
	refreshed, err := rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.ConfigRevision == after.ConfigRevision || h.app.issueToolRefreshRequired(rt, sessionID) {
		t.Fatalf("typed refresh did not update/clear: revision=%d/%d", after.ConfigRevision, refreshed.ConfigRevision)
	}
}

func TestIssueToolTypedErrorOnlyMarksSchemaRefresh(t *testing.T) {
	a, _ := newIssueToolTestApp(t)
	rt := &runtimeClient{}
	a.noteIssueToolTaskError(rt, "session-typed", &runtimeError{Code: "tool_error", Message: "not schema"})
	if a.issueToolRefreshRequired(rt, "session-typed") {
		t.Fatal("non-schema error marked refresh")
	}
	a.noteIssueToolTaskError(rt, "session-typed", &runtimeError{Code: "tool_schema_changed", Message: "schema"})
	if !a.issueToolRefreshRequired(rt, "session-typed") {
		t.Fatal("typed schema error did not mark refresh")
	}
}

func TestIssueToolSchemaRefreshRefusalKeepsDraft(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := &modelProviderFixture{opaque: true}
	h := startIssueToolTestHarness(t, binary, http.HandlerFunc(provider.handler))
	var firstTask string
	var sendErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "native continuation seed"
		if !h.app.sendCurrent() {
			sendErr = ct.SendError
		}
	})
	if sendErr != "" {
		t.Fatal(sendErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct != nil && ct.SendState == sendIdle && ct.ActiveTaskID == "" && ct.LastTaskID != ""
	})
	var sessionID string
	var rt *runtimeClient
	h.pump.do(func() {
		ct := h.app.selectedContact()
		firstTask, sessionID, rt = ct.LastTaskID, ct.SessionID, h.app.rt
	})
	if sessionID == "" || rt == nil {
		t.Fatal("native continuation session was not established")
	}
	var sendOK bool
	h.pump.do(func() {
		ct := h.app.selectedContact()
		h.app.noteIssueToolTaskError(rt, sessionID, &runtimeError{Code: "tool_schema_changed", Message: "controlled typed refresh"})
		ct.Draft = "retry after typed schema change"
		sendOK = h.app.sendCurrent()
	})
	if !sendOK {
		t.Fatal("sendCurrent rejected the retry before the controlled refresh attempt")
	}
	h.pump.waitTimeout(func() bool {
		ct := h.app.selectedContact()
		return ct != nil && ct.SendError != "" && ct.SendState == sendIdle
	}, 15*time.Second)
	var draft, sendError, active string
	var pending bool
	var refresh bool
	h.pump.do(func() {
		ct := h.app.selectedContact()
		draft, sendError, active = ct.Draft, ct.SendError, ct.ActiveTaskID
		pending = ct.pendingSend != nil
		refresh = h.app.issueToolRefreshRequired(rt, sessionID)
	})
	if !strings.Contains(sendError, "continuation_not_supported") {
		t.Fatalf("refresh refusal error = %q", sendError)
	}
	if draft != "retry after typed schema change" || active != "" {
		t.Fatalf("draft/active after refused refresh = draft=%q active=%q pending=%v", draft, active, pending)
	}
	if !refresh {
		t.Fatal("typed refresh requirement was lost after the incompatible update")
	}
	if h.app.selectedContact().LastTaskID != firstTask {
		t.Fatal("refused refresh dispatched a replacement task")
	}
}

func TestIssueToolWorkRefreshUsesSameTypedMarker(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected model request", http.StatusInternalServerError)
	})
	h := startIssueToolTestHarness(t, binary, handler)
	var sessionID string
	var rt *runtimeClient
	var setupErr error
	h.pump.do(func() {
		session, err := h.app.rt.createConfiguredConversationSession(h.root, issueToolWorkGuidance, map[string]any{
			"builtin": []string{"read", "bash", "edit", "write"}, "external": issueToolWorkExternal(),
		})
		if err != nil {
			setupErr = err
			return
		}
		sessionID, rt = session.SessionID, h.app.rt
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	before, err := rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if configured, err := h.app.issueToolEnsureWorkSession(rt, sessionID, issueToolWorkGuidance, false); err != nil || !configured {
		t.Fatalf("work no-op ensure = %v err=%v", configured, err)
	}
	after, err := rt.connectionConfig(sessionID)
	if err != nil || after.ConfigRevision != before.ConfigRevision {
		t.Fatalf("work no-op changed revision: before=%d after=%d err=%v", before.ConfigRevision, after.ConfigRevision, err)
	}
	h.app.noteIssueToolTaskError(rt, sessionID, &runtimeError{Code: "tool_schema_changed", Message: "controlled work schema change"})
	if !h.app.issueToolRefreshRequired(rt, sessionID) {
		t.Fatal("work typed schema error did not mark refresh")
	}
	if configured, err := h.app.issueToolEnsureWorkSession(rt, sessionID, issueToolWorkGuidance, false); err != nil || !configured {
		t.Fatalf("work refresh ensure = %v err=%v", configured, err)
	}
	refreshed, err := rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if h.app.issueToolRefreshRequired(rt, sessionID) {
		t.Fatalf("work refresh did not clear marker: revision=%d/%d", after.ConfigRevision, refreshed.ConfigRevision)
	}
}
