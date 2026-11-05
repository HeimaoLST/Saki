package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAddWorkflowGuidanceUpgradesReleasedPrefixesWithoutProfileLoss(t *testing.T) {
	profile := "Purpose: Keep the custom brief.\nInstructions: Preserve this exact text."
	cases := []struct {
		name     string
		current  string
		guidance string
	}{
		{"conversation-initial", previousIssueToolConversationGuidance, issueToolConversationGuidance},
		{"conversation-207", previousIssueToolConversationGuidanceV2, issueToolConversationGuidance},
		{"conversation-2026-11-03", previousIssueToolConversationGuidanceV3, issueToolConversationGuidance},
		{"work-released", previousIssueToolWorkGuidance, issueToolWorkGuidance},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.guidance + "\n\n" + profile
			got := addWorkflowGuidance(tc.current+"\n\n"+profile, tc.guidance)
			if got != want {
				t.Fatalf("upgrade lost profile or stacked the old role:\n%s", got)
			}
			if again := addWorkflowGuidance(got, tc.guidance); again != got {
				t.Fatal("repeated ensure changed an already-current prompt")
			}
		})
	}
}

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
	ensured, err := h.app.issueToolEnsureConversationSession(rt, sessionID, issueToolConversationGuidance, false)
	if err != nil || !ensured.Ready {
		t.Fatalf("ensure existing session = %+v err=%v", ensured, err)
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
	if err != nil || !again.Ready {
		t.Fatalf("second ensure = %+v err=%v", again, err)
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
		session, err := h.app.rt.createConfiguredConversationSession(h.root, previousIssueToolWorkGuidance+"\n\nOLD WORK PROMPT", nil)
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
	if ensured, err := h.app.issueToolEnsureWorkSession(rt, sessionID, issueToolWorkGuidance, false); err != nil || !ensured.Ready {
		t.Fatalf("ensure work = %+v err=%v", ensured, err)
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
	if ensured, err := h.app.issueToolEnsureConversationSession(rt, sessionID, issueToolConversationGuidance, false); err != nil || !ensured.Ready {
		t.Fatalf("ensure = %+v err=%v", ensured, err)
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
	if ensured, err := h.app.issueToolEnsureConversationSession(rt, sessionID, issueToolConversationGuidance, false); err != nil || !ensured.Ready {
		t.Fatalf("refresh ensure = %+v err=%v", ensured, err)
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
	if ensured, err := h.app.issueToolEnsureWorkSession(rt, sessionID, issueToolWorkGuidance, false); err != nil || !ensured.Ready {
		t.Fatalf("work no-op ensure = %+v err=%v", ensured, err)
	}
	after, err := rt.connectionConfig(sessionID)
	if err != nil || after.ConfigRevision != before.ConfigRevision {
		t.Fatalf("work no-op changed revision: before=%d after=%d err=%v", before.ConfigRevision, after.ConfigRevision, err)
	}
	h.app.noteIssueToolTaskError(rt, sessionID, &runtimeError{Code: "tool_schema_changed", Message: "controlled work schema change"})
	if !h.app.issueToolRefreshRequired(rt, sessionID) {
		t.Fatal("work typed schema error did not mark refresh")
	}
	if ensured, err := h.app.issueToolEnsureWorkSession(rt, sessionID, issueToolWorkGuidance, false); err != nil || !ensured.Ready {
		t.Fatalf("work refresh ensure = %+v err=%v", ensured, err)
	}
	refreshed, err := rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if h.app.issueToolRefreshRequired(rt, sessionID) {
		t.Fatalf("work refresh did not clear marker: revision=%d/%d", after.ConfigRevision, refreshed.ConfigRevision)
	}
}

type issueToolNativeBodyProvider struct {
	mu     sync.Mutex
	bodies []string
	plain  bool
}

func (p *issueToolNativeBodyProvider) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.bodies = append(p.bodies, string(body))
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if p.plain {
		_, _ = w.Write([]byte(`{"id":"response","status":"completed","output":[{"type":"message","id":"answer","status":"completed","role":"assistant","content":[{"type":"output_text","text":"plain fixture answer"}]}]}`))
		return
	}
	_, _ = w.Write([]byte(`{"id":"response","status":"completed","output":[{"type":"reasoning","id":"reasoning","status":"completed","content":[{"type":"reasoning_text","text":"opaque"}],"encrypted_content":"opaque-native"},{"type":"message","id":"answer","status":"completed","role":"assistant","content":[{"type":"output_text","text":"native fixture answer"}]}]}`))
}

func (p *issueToolNativeBodyProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

func (p *issueToolNativeBodyProvider) body(index int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if index < 0 || index >= len(p.bodies) {
		return ""
	}
	return p.bodies[index]
}

func issueToolNativeRequestSystem(t *testing.T, body string) string {
	t.Helper()
	var request struct {
		Input []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatalf("native request is not JSON: %v", err)
	}
	for _, item := range request.Input {
		if item.Role != "system" {
			continue
		}
		var text string
		if json.Unmarshal(item.Content, &text) == nil {
			return text
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(item.Content, &parts) == nil {
			var joined []string
			for _, part := range parts {
				joined = append(joined, part.Text)
			}
			return strings.Join(joined, "")
		}
	}
	t.Fatal("native request has no system prompt")
	return ""
}

func issueToolCurrentUserCallerContexts(t *testing.T, body string) []taskContextCallerContext {
	t.Helper()
	users := taskContextUserTexts(t, body)
	if len(users) == 0 {
		t.Fatal("model request had no user input")
	}
	var contexts []taskContextCallerContext
	for index, segment := range users[len(users)-1] {
		if index == 0 {
			continue
		}
		if context, ok := taskContextParseCallerContext(segment); ok {
			contexts = append(contexts, context)
		}
	}
	return contexts
}

func TestIssueToolNativeTaskGuidanceRequiresExactReadback(t *testing.T) {
	connectionID, executionID := "connection-native", "connection-native"
	base := runtimeConnectionConfig{SessionID: "native-session", ConfigRevision: 7, StorageSchema: 2}
	base.EffectiveConfig.SystemPrompt = previousIssueToolConversationGuidanceV2 + "\n\nPROFILE BYTES"
	base.EffectiveConfig.ConnectionID = &connectionID
	base.EffectiveConfig.ExecutionConnectionID = &executionID
	base.EffectiveConfig.ConnectionBinding.State = "bound"
	base.EffectiveConfig.ToolSelection = runtimeToolSelection{
		Builtin: []string{"read"},
		External: []runtimeExternalToolBinding{{
			SourceID: issueToolSourceID, ToolName: "issue_get", ExposedName: "issue_get",
		}},
	}
	updated := runtimeConfigUpdated{runtimeConnectionConfig: base}
	nativeErr := &runtimeError{Code: "continuation_not_supported", Message: "native"}
	if got := issueToolNativeTaskGuidance(base, updated, nativeErr, issueToolConversationGuidance, true, false); got != issueToolConversationGuidance {
		t.Fatalf("valid native guidance = %q", got)
	}
	withReadbackError := updated
	withReadbackError.ReadbackError = errors.New("readback failed")
	if got := issueToolNativeTaskGuidance(base, withReadbackError, nativeErr, issueToolConversationGuidance, true, false); got != "" {
		t.Fatalf("readback error accepted: %q", got)
	}
	withRevision := updated
	withRevision.ConfigRevision++
	if got := issueToolNativeTaskGuidance(base, withRevision, nativeErr, issueToolConversationGuidance, true, false); got != "" {
		t.Fatalf("revision change accepted: %q", got)
	}
	withPrompt := updated
	withPrompt.EffectiveConfig.SystemPrompt += " changed"
	if got := issueToolNativeTaskGuidance(base, withPrompt, nativeErr, issueToolConversationGuidance, true, false); got != "" {
		t.Fatalf("readback prompt change accepted: %q", got)
	}
	withTools := updated
	withTools.EffectiveConfig.ToolSelection.Builtin = append([]string{}, withTools.EffectiveConfig.ToolSelection.Builtin...)
	withTools.EffectiveConfig.ToolSelection.Builtin = append(withTools.EffectiveConfig.ToolSelection.Builtin, "edit")
	if got := issueToolNativeTaskGuidance(base, withTools, nativeErr, issueToolConversationGuidance, true, false); got != "" {
		t.Fatalf("readback tool change accepted: %q", got)
	}
	if got := issueToolNativeTaskGuidance(base, updated, &runtimeError{Code: "tool_schema_changed", Message: "schema"}, issueToolConversationGuidance, true, false); got != "" {
		t.Fatalf("non-native error accepted: %q", got)
	}
	if got := issueToolNativeTaskGuidance(base, updated, nativeErr, issueToolConversationGuidance, false, false); got != "" {
		t.Fatalf("missing tools accepted: %q", got)
	}
	if got := issueToolNativeTaskGuidance(base, updated, nativeErr, issueToolConversationGuidance, true, true); got != "" {
		t.Fatalf("refresh accepted: %q", got)
	}
	mixed := base
	mixed.EffectiveConfig.SystemPrompt = previousIssueToolConversationGuidanceV2 + " trailing mixed text"
	if got := issueToolNativeTaskGuidance(mixed, runtimeConfigUpdated{runtimeConnectionConfig: mixed}, nativeErr, issueToolConversationGuidance, true, false); got != "" {
		t.Fatalf("mixed prompt accepted: %q", got)
	}
	quoted := base
	quoted.EffectiveConfig.SystemPrompt = previousIssueToolConversationGuidanceV2 + "\n\nPROFILE: quote " + issueToolConversationGuidance
	if got := issueToolNativeTaskGuidance(quoted, runtimeConfigUpdated{runtimeConnectionConfig: quoted}, nativeErr, issueToolConversationGuidance, true, false); got != issueToolConversationGuidance {
		t.Fatalf("profile containing current role rejected: %q", got)
	}
}

func TestAddWorkflowGuidancePreservesProfileContainingCurrentRole(t *testing.T) {
	profile := "Purpose: quote the current rule without replacing it: " + issueToolConversationGuidance
	want := issueToolConversationGuidance + "\n\n" + profile
	if got := addWorkflowGuidance(previousIssueToolConversationGuidanceV2+"\n\n"+profile, issueToolConversationGuidance); got != want {
		t.Fatalf("profile containing current role was not preserved: %q", got)
	}
}

func TestIssueToolUserRoleGuidanceMaterialSurvivesNonNativeHistory(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := &issueToolNativeBodyProvider{plain: true}
	h := startIssueToolTestHarness(t, binary, http.HandlerFunc(provider.handler))
	var material taskContextItem
	var snapshotBefore []taskContextItem
	var draft string
	var sendErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		item, err := ct.Context.AddSnippet(issueToolRoleGuidanceContextName, issueToolConversationGuidance)
		if err != nil {
			sendErr = err.Error()
			return
		}
		material = item
		snapshotBefore = ct.Context.snapshot()
		draft = "non-native same-form user material"
		ct.Draft = draft
		if !h.app.sendCurrent() {
			sendErr = ct.SendError
		}
	})
	if sendErr != "" {
		t.Fatal(sendErr)
	}
	h.pump.waitTimeout(func() bool {
		ct := h.app.selectedContact()
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && ct.LastTaskID != ""
	}, 15*time.Second)
	var sessionID string
	var index map[string]issueToolScopeIdentity
	h.pump.do(func() {
		ct := h.app.selectedContact()
		sessionID = ct.SessionID
		index = h.app.issueToolScopeIndexSnapshot()
	})
	if sessionID == "" || len(snapshotBefore) != 1 || snapshotBefore[0].ID != material.ID || snapshotBefore[0].Name != issueToolRoleGuidanceContextName || snapshotBefore[0].Text != issueToolConversationGuidance {
		t.Fatalf("saved user material changed before send: session=%q snapshot=%+v", sessionID, snapshotBefore)
	}
	body := provider.body(0)
	var bodyGuidance int
	for _, context := range issueToolCurrentUserCallerContexts(t, body) {
		if context.Name == issueToolRoleGuidanceContextName && context.Text == issueToolConversationGuidance {
			bodyGuidance++
		}
	}
	if bodyGuidance != 1 {
		t.Fatalf("actual non-native task user material count=%d body=%s", bodyGuidance, body)
	}
	raw, err := h.app.rt.historyPage(sessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var rawGuidance int
	for _, context := range taskContextHistoryCallerContexts(t, raw) {
		if context.Name == issueToolRoleGuidanceContextName && context.Text == issueToolConversationGuidance {
			rawGuidance++
		}
	}
	if rawGuidance != 1 {
		t.Fatalf("raw history user material count=%d", rawGuidance)
	}
	projection := parseHistoryWithIssueToolScopeFilter(sessionID, raw.Records, nil, index)
	var projected *message
	for _, message := range projection.messages {
		if message.Text == draft {
			projected = message
			break
		}
	}
	if projected == nil {
		t.Fatalf("projected user message missing: %+v", projection.messages)
	}
	var projectedGuidance int
	for _, context := range projected.Context {
		if context.Name == issueToolRoleGuidanceContextName && context.Text == issueToolConversationGuidance {
			projectedGuidance++
		}
	}
	if projectedGuidance != 1 {
		t.Fatalf("user material hidden by projection: contexts=%+v", projected.Context)
	}
}

func TestIssueToolNativeConversationUsesOneCurrentRoleGuidanceContext(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := &issueToolNativeBodyProvider{}
	h := startIssueToolTestHarness(t, binary, http.HandlerFunc(provider.handler))
	oldPrompt := previousIssueToolConversationGuidanceV3 + "\n\nCUSTOM PROFILE: keep these exact bytes"
	seedSelection := runtimeToolSelection{
		Builtin: []string{"read", "bash"},
		External: []runtimeExternalToolBinding{{
			SourceID: issueToolSourceID, ToolName: "issue_get", ExposedName: "alias_issue_get",
		}},
	}
	var sessionID string
	var rt *runtimeClient
	var setupErr error
	h.pump.do(func() {
		ct := h.app.selectedContact()
		selection, err := h.app.issueToolMergedSelection(seedSelection)
		if err != nil {
			setupErr = err
			return
		}
		session, err := h.app.rt.createConfiguredConversationSession(h.root, oldPrompt, selection)
		if err != nil {
			setupErr = err
			return
		}
		sessionID, rt = session.SessionID, h.app.rt
		ct.SessionID, ct.Loaded = sessionID, true
		ct.Purpose, ct.Instructions = "CURRENT CONTACT PROFILE", "CURRENT CONTACT INSTRUCTIONS"
		if err := h.app.persist(); err != nil {
			setupErr = err
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	var seedTask string
	h.pump.do(func() {
		result, err := rt.sendTask(sessionID, "native seed")
		if err != nil {
			setupErr = err
			return
		}
		seedTask = result.TaskID
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	h.pump.waitTimeout(func() bool {
		ct := h.app.selectedContact()
		return ct.LastTaskID == seedTask && ct.ActiveTaskID == ""
	}, 15*time.Second)
	configBefore, err := rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	historyBefore, err := rt.historyPage(sessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	builtin := map[string]bool{}
	for _, name := range configBefore.EffectiveConfig.ToolSelection.Builtin {
		builtin[name] = true
	}
	alias := false
	for _, binding := range configBefore.EffectiveConfig.ToolSelection.External {
		if binding.SourceID == issueToolSourceID && binding.ToolName == "issue_get" && binding.ExposedName == "alias_issue_get" {
			alias = true
		}
	}
	if provider.count() != 1 || configBefore.EffectiveConfig.SystemPrompt != oldPrompt ||
		len(builtin) != 2 || !builtin["read"] || !builtin["bash"] || !alias ||
		!runtimeSelectionHasIssueTools(configBefore.EffectiveConfig.ToolSelection) {
		t.Fatalf("native seed precondition requests=%d prompt=%q builtin=%v alias=%v", provider.count(), configBefore.EffectiveConfig.SystemPrompt, builtin, alias)
	}
	ensured, err := h.app.issueToolEnsureConversationSession(rt, sessionID, issueToolConversationGuidance, false)
	if err != nil || !ensured.Ready || ensured.TaskGuidance != issueToolConversationGuidance {
		t.Fatalf("native ensure = %+v err=%v", ensured, err)
	}
	again, err := h.app.issueToolEnsureConversationSession(rt, sessionID, issueToolConversationGuidance, false)
	if err != nil || !again.Ready || again.TaskGuidance != issueToolConversationGuidance || provider.count() != 1 {
		t.Fatalf("repeated native ensure = %+v err=%v requests=%d", again, err, provider.count())
	}
	configAfterEnsure, err := rt.connectionConfig(sessionID)
	if err != nil || !reflect.DeepEqual(configBefore, configAfterEnsure) {
		t.Fatalf("native ensure changed config: err=%v before=%+v after=%+v", err, configBefore, configAfterEnsure)
	}
	checkCurrentGuidance := func(body string, want int) {
		t.Helper()
		if got := issueToolNativeRequestSystem(t, body); got != oldPrompt {
			t.Fatalf("native task system prompt = %q", got)
		}
		var guidanceCount int
		for _, context := range issueToolCurrentUserCallerContexts(t, body) {
			if context.Name != issueToolRoleGuidanceContextName || context.Text != issueToolConversationGuidance {
				continue
			}
			guidanceCount++
		}
		if guidanceCount != want {
			t.Fatalf("native current task guidance contexts=%d, want %d", guidanceCount, want)
		}
	}
	var firstMaterial taskContextItem
	var firstSnapshot []taskContextItem
	firstDraft := "native first task"
	var sendOK bool
	var sendErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		item, err := ct.Context.AddSnippet(issueToolRoleGuidanceContextName, issueToolConversationGuidance)
		if err != nil {
			setupErr = err
			return
		}
		firstMaterial = item
		firstSnapshot = ct.Context.snapshot()
		ct.Draft = firstDraft
		sendOK = h.app.sendCurrent()
		sendErr = ct.SendError
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	if len(firstSnapshot) != 1 || firstSnapshot[0].ID != firstMaterial.ID ||
		firstSnapshot[0].Name != issueToolRoleGuidanceContextName ||
		firstSnapshot[0].Text != issueToolConversationGuidance {
		t.Fatalf("native user material snapshot changed: %+v", firstSnapshot)
	}
	if !sendOK {
		t.Fatalf("native first task rejected: %q", sendErr)
	}
	h.pump.waitTimeout(func() bool {
		ct := h.app.selectedContact()
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && ct.LastTaskID != seedTask
	}, 15*time.Second)
	var firstTask string
	var firstIndex map[string]issueToolScopeIdentity
	h.pump.do(func() {
		firstTask = h.app.selectedContact().LastTaskID
		firstIndex = h.app.issueToolScopeIndexSnapshot()
	})
	configAfterFirst, err := rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	historyAfterFirst, err := rt.historyPage(sessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(configBefore, configAfterFirst) {
		t.Fatalf("native first task changed config: before=%+v after=%+v", configBefore, configAfterFirst)
	}
	if len(historyAfterFirst.Records) <= len(historyBefore.Records) || !reflect.DeepEqual(historyAfterFirst.Records[:len(historyBefore.Records)], historyBefore.Records) {
		t.Fatalf("native first task changed journal prefix: before=%d after=%d", len(historyBefore.Records), len(historyAfterFirst.Records))
	}
	if provider.count() != 2 {
		t.Fatalf("native first task requests=%d, want one seed + one task", provider.count())
	}
	checkCurrentGuidance(provider.body(1), 2)
	firstProjection := parseHistoryWithIssueToolScopeFilter(sessionID, historyAfterFirst.Records, nil, firstIndex)
	var firstProjected *message
	for _, message := range firstProjection.messages {
		if message.Text == firstDraft {
			firstProjected = message
			break
		}
	}
	if firstProjected == nil || len(firstProjected.Context) != 1 ||
		firstProjected.Context[0].Name != issueToolRoleGuidanceContextName ||
		firstProjected.Context[0].Text != issueToolConversationGuidance {
		t.Fatalf("native user material projection = %+v", firstProjected)
	}
	beforeSecond, err := h.app.issueToolEnsureConversationSession(rt, sessionID, issueToolConversationGuidance, false)
	if err != nil || !beforeSecond.Ready || beforeSecond.TaskGuidance != issueToolConversationGuidance || provider.count() != 2 {
		t.Fatalf("ensure before second task = %+v err=%v requests=%d", beforeSecond, err, provider.count())
	}
	configBeforeSecond, err := rt.connectionConfig(sessionID)
	if err != nil || !reflect.DeepEqual(configBefore, configBeforeSecond) {
		t.Fatalf("ensure before second task changed config: err=%v before=%+v after=%+v", err, configBefore, configBeforeSecond)
	}
	secondDraft := "native second task"
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = secondDraft
		sendOK = h.app.sendCurrent()
		sendErr = ct.SendError
	})
	if !sendOK {
		t.Fatalf("native second task rejected: %q", sendErr)
	}
	h.pump.waitTimeout(func() bool {
		ct := h.app.selectedContact()
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && ct.LastTaskID != firstTask
	}, 15*time.Second)
	configAfterSecond, err := rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	historyAfterSecond, err := rt.historyPage(sessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(configBefore, configAfterSecond) {
		t.Fatalf("native second task changed config: before=%+v after=%+v", configBefore, configAfterSecond)
	}
	if len(historyAfterSecond.Records) <= len(historyAfterFirst.Records) || !reflect.DeepEqual(historyAfterSecond.Records[:len(historyAfterFirst.Records)], historyAfterFirst.Records) {
		t.Fatalf("native second task changed journal prefix: before=%d after=%d", len(historyAfterFirst.Records), len(historyAfterSecond.Records))
	}
	if provider.count() != 3 {
		t.Fatalf("native repeat requests=%d, want seed + two sends", provider.count())
	}
	checkCurrentGuidance(provider.body(2), 1)
	allGuidance := 0
	for _, context := range taskContextUserCallerContexts(t, provider.body(2)) {
		if context.Name == issueToolRoleGuidanceContextName && context.Text == issueToolConversationGuidance {
			allGuidance++
		}
	}
	if allGuidance != 3 {
		t.Fatalf("historical guidance retained=%d, want user + previous app + current app", allGuidance)
	}
	reloadedStore, err := openContactStore(h.app.store.path, h.root)
	if err != nil {
		t.Fatal(err)
	}
	reloadedApp := &appModel{issueToolScopes: reloadedStore.issueToolScopes}
	secondProjection := parseHistoryWithIssueToolScopeFilter(sessionID, historyAfterSecond.Records, nil, reloadedApp.issueToolScopeIndexSnapshot())
	var firstAfterSecond *message
	for _, message := range secondProjection.messages {
		if message.Text == firstDraft {
			firstAfterSecond = message
			break
		}
	}
	if firstAfterSecond == nil || len(firstAfterSecond.Context) != 1 ||
		firstAfterSecond.Context[0].Name != issueToolRoleGuidanceContextName ||
		firstAfterSecond.Context[0].Text != issueToolConversationGuidance {
		t.Fatalf("native second projection lost user material: %+v", firstAfterSecond)
	}
}

func TestIssueToolNativeWorkUsesOneCurrentRoleGuidanceContext(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := &issueToolNativeBodyProvider{}
	h := startIssueToolTestHarness(t, binary, http.HandlerFunc(provider.handler))
	oldPrompt := previousIssueToolWorkGuidance + "\n\nWORK PROFILE: keep these exact bytes"
	selection := map[string]any{
		"builtin":  []string{"read", "bash", "edit", "write"},
		"external": issueToolWorkExternal(),
	}
	var sessionID, seedTask string
	var rt *runtimeClient
	var setupErr error
	h.pump.do(func() {
		ct := h.app.selectedContact()
		session, err := h.app.rt.createConfiguredConversationSession(h.root, oldPrompt, selection)
		if err != nil {
			setupErr = err
			return
		}
		sessionID, rt = session.SessionID, h.app.rt
		ct.SessionID, ct.Loaded = sessionID, true
		result, err := rt.sendTask(sessionID, "native work seed")
		if err != nil {
			setupErr = err
			return
		}
		seedTask = result.TaskID
		if err := h.app.persist(); err != nil {
			setupErr = err
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	h.pump.waitTimeout(func() bool {
		ct := h.app.selectedContact()
		return ct.LastTaskID == seedTask && ct.ActiveTaskID == ""
	}, 15*time.Second)
	configBefore, err := rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	historyBefore, err := rt.historyPage(sessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if provider.count() != 1 || configBefore.EffectiveConfig.SystemPrompt != oldPrompt {
		t.Fatalf("native work seed precondition requests=%d prompt=%q", provider.count(), configBefore.EffectiveConfig.SystemPrompt)
	}
	var issueID string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		// The seed used the contact slot only to observe events. Give the work
		// session its single issue owner before creating/dispatching the issue.
		ct.SessionID, ct.Loaded, ct.ActiveTaskID, ct.SendState = "", false, "", sendIdle
		if !h.app.createIssue("NATIVE-WORK-GOAL", "") {
			setupErr = errors.New(h.app.issueFormError)
			return
		}
		issueID = ct.SelectedIssueID
		issue := ct.issueByID(issueID)
		issue.WorkSessionID = sessionID
		issue.Work.Loaded = true
		issue.Work.SendState = sendIdle
		if err := h.app.persist(); err != nil {
			setupErr = err
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	ensured, err := h.app.issueToolEnsureWorkSession(rt, sessionID, issueToolWorkGuidance, false)
	if err != nil || !ensured.Ready || ensured.TaskGuidance != issueToolWorkGuidance {
		t.Fatalf("native work ensure = %+v err=%v", ensured, err)
	}
	again, err := h.app.issueToolEnsureWorkSession(rt, sessionID, issueToolWorkGuidance, false)
	if err != nil || !again.Ready || again.TaskGuidance != issueToolWorkGuidance || provider.count() != 1 {
		t.Fatalf("repeated native work ensure = %+v err=%v requests=%d", again, err, provider.count())
	}
	configAfterEnsure, err := rt.connectionConfig(sessionID)
	if err != nil || !reflect.DeepEqual(configBefore, configAfterEnsure) {
		t.Fatalf("native work ensure changed config: err=%v before=%+v after=%+v", err, configBefore, configAfterEnsure)
	}
	var runOK bool
	var runErr string
	h.pump.do(func() {
		runOK = h.app.runIssue(issueID)
		if !runOK {
			runErr = h.app.issueFormError
		}
	})
	if !runOK {
		t.Fatalf("native work task rejected: %q", runErr)
	}
	h.pump.waitTimeout(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return issue != nil && issue.Work.SendState == sendIdle && issue.Work.ActiveTaskID == "" &&
			(issue.Work.SendError != "" || issue.Work.LastTaskID != seedTask)
	}, 15*time.Second)
	var workState string
	var workExecutions int
	var workStatus string
	h.pump.do(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		workState = issue.Work.SendError
		workExecutions = len(issue.Executions)
		if len(issue.Executions) > 0 {
			workStatus = issue.Executions[0].Status
		}
	})
	if workState != "" || workExecutions != 1 || workStatus != issueExecutionCompleted {
		t.Fatalf("native work result: error=%q executions=%d status=%q", workState, workExecutions, workStatus)
	}
	configAfter, err := rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	historyAfter, err := rt.historyPage(sessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(configBefore, configAfter) {
		t.Fatalf("native work task changed config: before=%+v after=%+v", configBefore, configAfter)
	}
	if len(historyAfter.Records) <= len(historyBefore.Records) || !reflect.DeepEqual(historyAfter.Records[:len(historyBefore.Records)], historyBefore.Records) {
		t.Fatalf("native work task changed journal prefix: before=%d after=%d", len(historyBefore.Records), len(historyAfter.Records))
	}
	if provider.count() != 2 {
		t.Fatalf("native work requests=%d, want one seed + one task", provider.count())
	}
	body := provider.body(1)
	if got := issueToolNativeRequestSystem(t, body); got != oldPrompt {
		t.Fatalf("native work system prompt = %q", got)
	}
	var guidanceCount int
	var guidance string
	for _, context := range issueToolCurrentUserCallerContexts(t, body) {
		if context.Name != issueToolRoleGuidanceContextName {
			continue
		}
		guidanceCount++
		guidance = context.Text
	}
	if guidanceCount != 1 || guidance != issueToolWorkGuidance {
		t.Fatalf("native work guidance contexts=%d text=%q", guidanceCount, guidance)
	}
}

func TestIssueToolNativeToolChangeStillRefused(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := &issueToolNativeBodyProvider{}
	h := startIssueToolTestHarness(t, binary, http.HandlerFunc(provider.handler))
	oldPrompt := previousIssueToolConversationGuidanceV2 + "\n\nCUSTOM PROFILE"
	var sessionID string
	var rt *runtimeClient
	var setupErr error
	h.pump.do(func() {
		ct := h.app.selectedContact()
		session, err := h.app.rt.createConfiguredConversationSession(h.root, oldPrompt, map[string]any{
			"builtin":  []string{"read"},
			"external": []map[string]string{},
		})
		if err != nil {
			setupErr = err
			return
		}
		sessionID, rt = session.SessionID, h.app.rt
		ct.SessionID, ct.Loaded = sessionID, true
		result, err := rt.sendTask(sessionID, "native seed")
		if err != nil {
			setupErr = err
			return
		}
		ct.Draft = result.TaskID
		if err := h.app.persist(); err != nil {
			setupErr = err
		}
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	var seedTask string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		seedTask = ct.Draft
		ct.Draft = ""
	})
	h.pump.waitTimeout(func() bool {
		ct := h.app.selectedContact()
		return ct.LastTaskID == seedTask && ct.ActiveTaskID == ""
	}, 15*time.Second)
	configBefore, err := rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	ensured, err := h.app.issueToolEnsureConversationSession(rt, sessionID, issueToolConversationGuidance, false)
	if err == nil || ensured.TaskGuidance != "" || !strings.Contains(err.Error(), "continuation_not_supported") {
		t.Fatalf("native tool change result = %+v err=%v", ensured, err)
	}
	configAfter, err := rt.connectionConfig(sessionID)
	if err != nil || !reflect.DeepEqual(configBefore, configAfter) || provider.count() != 1 {
		t.Fatalf("refused native tool change modified state: err=%v before=%+v after=%+v requests=%d", err, configBefore, configAfter, provider.count())
	}
}
