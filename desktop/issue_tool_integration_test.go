package main

// Real Runtime -> same-binary MCP helper -> Unix IPC -> live appModel/store
// integration for #154. The provider is local and deterministic; no online
// credential or model is used.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const issueToolTestMarker = "CONVERSATION-TOOL"
const issueToolTestIssueGoal = "ISSUE-TOOL-GOAL"
const issueToolWaitGoal = "WAIT-A-GOAL"

type issueToolChainProvider struct {
	mu               sync.Mutex
	conversation     int
	issueWork        int
	bodies           []string
	scope            string
	scopeFromContext bool
	arrived          chan struct{}
	release          chan struct{}
	releaseOnce      sync.Once
	once             sync.Once
}

func newIssueToolChainProvider() *issueToolChainProvider {
	return &issueToolChainProvider{arrived: make(chan struct{}), release: make(chan struct{})}
}

func (p *issueToolChainProvider) releaseFixture() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func (p *issueToolChainProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

func (p *issueToolChainProvider) body(i int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < 0 || i >= len(p.bodies) {
		return ""
	}
	return p.bodies[i]
}

func (p *issueToolChainProvider) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.bodies = append(p.bodies, string(body))
		isConversation := strings.Contains(string(body), issueToolTestMarker)
		isIssueWork := strings.Contains(string(body), issueToolTestIssueGoal) && !isConversation
		step := p.conversation
		if isConversation {
			p.conversation++
		}
		workStep := p.issueWork
		if isIssueWork {
			p.issueWork++
		}
		scope := issueToolTestScope(string(body))
		if p.scopeFromContext {
			if named := issueToolTestNamedContext(string(body), issueToolScopeContextName); named != "" {
				scope = named
			}
		}
		if scope == "" {
			scope = p.scope
		}
		p.mu.Unlock()

		var response any
		switch {
		case isIssueWork && workStep == 0:
			response = issueResultFunctionCall("write", "issue-work-write", map[string]any{
				"path": "tool-result.txt", "content": "tool result\n",
			})
		case isIssueWork:
			response = issueResultPageFinal("ISSUE-WORK-FINAL")
		case isConversation && step == 0:
			p.once.Do(func() { close(p.arrived) })
			<-p.release
			scope = issueToolTestScope(string(body))
			if scope == "" {
				p.mu.Lock()
				scope = p.scope
				p.mu.Unlock()
			}
			response = issueResultFunctionCall("issue_action", "tool-create", map[string]any{
				"scope": scope, "action": "create", "goal": issueToolTestIssueGoal,
				"requirements": []string{"REQ-ONE"},
			})
		case isConversation && step == 1:
			issueID := issueToolTestIssueID(string(body))
			response = issueResultFunctionCall("issue_get", "tool-get", map[string]any{
				"scope": scope, "issue_id": issueID,
			})
		case isConversation && step == 2:
			issueID := issueToolTestIssueID(string(body))
			response = issueResultFunctionCall("issue_action", "tool-run", map[string]any{
				"scope": scope, "action": "run", "issue_id": issueID,
			})
		case isConversation && step == 4:
			issueID := issueToolTestIssueID(string(body))
			response = issueResultFunctionCall("issue_get", "second-get", map[string]any{
				"scope": scope, "issue_id": issueID,
			})
		default:
			response = issueResultPageFinal("CONVERSATION-FINAL")
		}
		data, _ := json.Marshal(response)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

func startIssueToolTestHarness(t *testing.T, binary string, handler http.Handler) *guiHarness {
	t.Helper()
	server := httptest.NewServer(handler)
	var h *guiHarness
	t.Cleanup(func() {
		if h != nil && !h.closed {
			h.shutdown(t)
		}
		server.Close()
	})
	root := t.TempDir()
	storePath := filepath.Join(root, "gui", "contacts.json")
	t.Setenv("SAKI_GUI_STORE", storePath)
	t.Setenv("SAKI_GUI_WORKDIR", root)
	t.Setenv("SAKI_SESSION_DIR", filepath.Join(root, "sessions"))
	t.Setenv("SAKI_RUNTIME_BIN", binary)
	if err := saveConnectionConfig(filepath.Join(root, "gui", "connection.json"), connectionConfig{
		Version: 1, API: connectionAPIResponses, Endpoint: server.URL + "/v1",
		APIKey: "offline-fixture", Model: "fixture", ContextTokens: 131072,
		OutputTokens: 8192, WorkingDirectory: root, MaxActiveTasks: 2,
	}); err != nil {
		t.Fatal(err)
	}
	app, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	pump := newUIPump(t, app)
	h = &guiHarness{app: app, pump: pump, root: root}
	app.startRuntime()
	pump.wait(func() bool {
		return (app.runtimeStatus == runtimeStateReady && (app.runtimeCapabilitiesLoaded || app.runtimeCapabilitiesErr != "")) ||
			app.runtimeStatus == runtimeStateFailed || app.runtimeStatus == runtimeStateExited
	})
	var startupErr string
	h.pump.do(func() {
		if h.app.runtimeStatus != runtimeStateReady && h.app.runtimeStatus != runtimeStateStarting {
			startupErr = fmt.Sprintf("runtime failed: status=%q stage=%q err=%q hint=%q toolErr=%q", h.app.runtimeStatus, h.app.runtimeStage, h.app.runtimeErr, h.app.runtimeHint, h.app.issueToolErr)
		} else if h.app.issueToolErr != "" {
			startupErr = "issue tool service failed: " + h.app.issueToolErr
		} else if !h.app.connectionManaged || h.app.connectionLaunch == nil {
			startupErr = "fixture did not use the saved managed connection path"
		}
	})
	if startupErr != "" {
		t.Fatal(startupErr)
	}
	return h
}

func ensureIssueToolTestHelper(t *testing.T) string {
	t.Helper()
	if helper := strings.TrimSpace(os.Getenv("SAKI_TEST_ISSUE_TOOL_HELPER")); helper != "" {
		if info, err := os.Stat(helper); err != nil || info.IsDir() {
			t.Fatalf("issue tool helper: %v", err)
		}
		return helper
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("python3 is required to build the issue tool helper: %v", err)
	}
	dir, err := os.MkdirTemp("", "saki-issue-tool-helper-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	helper := filepath.Join(dir, "saki-issue-tool-helper")
	cmd := exec.Command(python, "../scripts/gui-go.py", "build", "-o", helper, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build issue tool helper: %v\n%s", err, out)
	}
	if info, err := os.Stat(helper); err != nil || info.IsDir() {
		t.Fatalf("built issue tool helper: %v", err)
	}
	return helper
}

func issueToolTestIssueID(body string) string {
	var payload struct {
		Input []struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		} `json:"input"`
	}
	if json.Unmarshal([]byte(body), &payload) != nil {
		return ""
	}
	for _, input := range payload.Input {
		if input.Type != "function_call_output" || input.CallID != "tool-create" {
			continue
		}
		var output struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal([]byte(input.Output), &output) != nil {
			return ""
		}
		for _, content := range output.Content {
			var receipt struct {
				IssueID string `json:"issue_id"`
			}
			if json.Unmarshal([]byte(content.Text), &receipt) == nil && receipt.IssueID != "" {
				return receipt.IssueID
			}
		}
	}
	return ""
}

func issueToolTestNamedContext(body, name string) string {
	var root any
	if json.Unmarshal([]byte(body), &root) != nil {
		return ""
	}
	var found string
	var walk func(any)
	walk = func(value any) {
		switch current := value.(type) {
		case string:
			if strings.Contains(current, `"caller_context"`) {
				var context taskContextCallerContext
				if json.Unmarshal([]byte(current), &context) == nil && context.Name == name && found == "" {
					found = context.Text
				}
			}
		case []any:
			for _, item := range current {
				walk(item)
			}
		case map[string]any:
			for _, item := range current {
				walk(item)
			}
		}
	}
	walk(root)
	return found
}

func issueToolTestScope(body string) string {
	last := ""
	for offset := 0; ; {
		index := strings.Index(body[offset:], "scope-")
		if index < 0 {
			break
		}
		index += offset
		start := index + len("scope-")
		end := start
		for end < len(body) && (body[end] >= '0' && body[end] <= '9' || body[end] >= 'a' && body[end] <= 'f') {
			end++
		}
		if end-start == 16 {
			last = body[index:end]
		}
		offset = index + len("scope-")
	}
	return last
}
func TestIssueToolConversationRealChainQueryCreateRun(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := newIssueToolChainProvider()
	h := startIssueToolTestHarness(t, binary, provider.handler())
	t.Cleanup(provider.releaseFixture)
	var hostErr string
	h.pump.do(func() {
		if h.app.connectionLaunch == nil {
			hostErr = "managed connection launch is missing"
			return
		}
		data, err := os.ReadFile(h.app.connectionLaunch.HostConfigPath)
		if err != nil {
			hostErr = err.Error()
			return
		}
		var host struct {
			MaxToolProcesses *int                `json:"max_tool_processes"`
			ToolSources      []runtimeToolSource `json:"tool_sources"`
		}
		if err := json.Unmarshal(data, &host); err != nil {
			hostErr = err.Error()
			return
		}
		if host.MaxToolProcesses != nil || len(host.ToolSources) != 1 {
			hostErr = fmt.Sprintf("host source shape = max=%v sources=%d", host.MaxToolProcesses, len(host.ToolSources))
			return
		}
		source := host.ToolSources[0]
		if source.Command == "" || len(source.Args) != 1 || source.Args[0] != "--issue-tool-helper" ||
			source.Protocol != "legacy" || len(source.EnvFrom) != 1 || source.EnvFrom[0] != issueToolIPCSocketEnv {
			hostErr = fmt.Sprintf("host source = %+v", source)
			return
		}
		foundEnv := false
		for _, entry := range h.app.connectionLaunch.Env {
			if strings.HasPrefix(entry, issueToolIPCSocketEnv+"=") && strings.TrimPrefix(entry, issueToolIPCSocketEnv+"=") == h.app.issueToolEndpoint {
				foundEnv = true
			}
		}
		if !foundEnv {
			hostErr = "runtime environment does not carry the app IPC endpoint"
		}
		t.Logf("issue tool host source: command=%s args=%v env_from=%v protocol=%s", source.Command, source.Args, source.EnvFrom, source.Protocol)
	})
	if hostErr != "" {
		t.Fatal(hostErr)
	}
	var contactID, beforeSelected string
	var sendErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		contactID = ct.ID
		beforeSelected = ct.SelectedIssueID
		ct.Draft = issueToolTestMarker
		if !h.app.sendCurrent() {
			sendErr = ct.SendError
		}
	})
	if sendErr != "" {
		t.Fatalf("send: %s", sendErr)
	}
	select {
	case <-provider.arrived:
	case <-time.After(15 * time.Second):
		var state string
		h.pump.do(func() {
			ct := h.app.contactByID(contactID)
			state = fmt.Sprintf("send=%v active=%q error=%q session=%q runtime=%q/%q toolErr=%q scopes=%d ops=%d",
				ct.SendState, ct.ActiveTaskID, ct.SendError, ct.SessionID, h.app.runtimeStatus, h.app.runtimeErr, h.app.issueToolErr, len(h.app.issueToolScopes), len(h.app.issueToolLedger.Operations))
		})
		t.Fatalf("provider did not receive the first conversation request: %s", state)
	}
	h.pump.wait(func() bool { return len(h.app.issueToolScopes) == 1 })
	var scopeRef string
	h.pump.do(func() {
		for ref := range h.app.issueToolScopes {
			scopeRef = ref
		}
	})
	if scopeRef == "" {
		t.Fatal("no app-issued scope")
	}
	provider.mu.Lock()
	provider.scope = scopeRef
	provider.mu.Unlock()
	provider.releaseFixture()
	h.pump.wait(func() bool {
		ct := h.app.contactByID(contactID)
		if ct == nil {
			return false
		}
		var issue *productIssue
		if len(ct.Issues) > 0 {
			issue = ct.Issues[0]
		}
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" &&
			issue != nil && len(issue.Executions) == 1 &&
			issue.Executions[0].Status == issueExecutionCompleted &&
			issue.Work.ActiveTaskID == ""
	})
	var finalErr string
	h.pump.do(func() {
		ct := h.app.contactByID(contactID)
		if ct.SelectedIssueID != beforeSelected {
			finalErr = fmt.Sprintf("tool creation changed selected issue: %q -> %q", beforeSelected, ct.SelectedIssueID)
			return
		}
		if h.app.issueResult.Open {
			finalErr = "tool query opened the result page"
			return
		}
		if len(h.app.issueToolLedger.Operations) != 2 {
			finalErr = fmt.Sprintf("operation count = %d, want create+run", len(h.app.issueToolLedger.Operations))
			return
		}
		for _, operation := range h.app.issueToolLedger.Operations {
			if (operation.Receipt == nil || operation.Receipt.State != issueToolReceiptAccepted) && operation.Action == issueToolActionRun {
				finalErr = fmt.Sprintf("run operation receipt = %+v", operation)
				return
			}
		}
	})
	if finalErr != "" {
		t.Fatal(finalErr)
	}
	if provider.count() != 6 {
		t.Fatalf("provider requests = %d, want create/get/run/final + issue work write/final", provider.count())
	}
	var scopeLeaks int
	h.pump.do(func() {
		for _, message := range h.app.contactByID(contactID).Messages {
			for _, context := range message.Context {
				if context.Name == issueToolScopeContextName {
					scopeLeaks++
				}
			}
		}
	})
	if scopeLeaks != 0 {
		t.Fatalf("create-mutated scope leaked into GUI user history %d times", scopeLeaks)
	}
	if !strings.Contains(provider.body(0), issueToolScopeContextName) || !strings.Contains(provider.body(0), scopeRef) {
		t.Fatal("first conversation request did not carry the app-issued scope context")
	}
	if !strings.Contains(provider.body(0), `"name":"issue_action"`) || !strings.Contains(provider.body(0), `"name":"issue_get"`) {
		t.Fatal("first conversation request did not carry the externally selected issue tools")
	}
	if strings.Contains(provider.body(0), `"name":"issue_wait"`) {
		t.Fatal("conversation request incorrectly selected the work-only issue_wait tool")
	}
	for _, builtin := range []string{"read", "bash", "edit", "write"} {
		if !strings.Contains(provider.body(0), `"name":"`+builtin+`"`) {
			t.Fatalf("new conversation lost builtin tool %s", builtin)
		}
	}
	for i := 0; i < provider.count(); i++ {
		body := provider.body(i)
		if strings.Contains(body, issueToolTestIssueGoal) && !strings.Contains(body, issueToolTestMarker) && strings.Contains(body, `"name":"issue_action"`) {
			t.Fatal("issue work session recursively selected conversation issue tools")
		}
	}
	var getArgs []byte
	h.pump.do(func() {
		detail := h.app.contactByID(contactID).Issues[0]
		getArgs, _ = json.Marshal(map[string]any{"scope": scopeRef, "issue_id": detail.ID})
	})
	getResponse, getErr := h.app.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_get", Arguments: getArgs})
	if getErr != nil || getResponse.IsError {
		t.Fatalf("post-run typed history get = %+v err=%v", getResponse, getErr)
	}
	var detail IssueToolIssueDetail
	if err := json.Unmarshal(issueToolResponseJSON(t, getResponse), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ResultExecutionID == "" || !strings.Contains(detail.ResultExcerpt, "ISSUE-WORK-FINAL") || detail.ResultUnavailable != "" ||
		detail.ResultFileCount != 1 || len(detail.ResultFiles) != 1 || filepath.Base(detail.ResultFiles[0].Path) != "tool-result.txt" {
		t.Fatalf("post-run typed history detail = %+v", detail)
	}
	workdir, _ := filepath.EvalSymlinks(h.root)
	resultDir, _ := filepath.EvalSymlinks(filepath.Dir(detail.ResultFiles[0].Path))
	if workdir == "" || resultDir != workdir {
		t.Fatalf("result cwd = %q, want %q", resultDir, workdir)
	}
	var firstTask, firstSession string
	h.pump.do(func() {
		ct := h.app.contactByID(contactID)
		firstTask = ct.LastTaskID
		firstSession = ct.SessionID
	})
	var secondSendErr string
	h.pump.do(func() {
		ct := h.app.contactByID(contactID)
		ct.Draft = issueToolTestMarker + "-SECOND"
		if !h.app.sendCurrent() {
			secondSendErr = ct.SendError
		}
	})
	if secondSendErr != "" {
		t.Fatalf("second send: %s", secondSendErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.contactByID(contactID)
		if ct == nil || ct.SendState != sendIdle || ct.ActiveTaskID != "" || ct.LastTaskID == firstTask {
			return false
		}
		if len(ct.Issues) != 1 || len(ct.Issues[0].Executions) != 1 || ct.Issues[0].Work.ActiveTaskID != "" {
			return false
		}
		return len(h.app.issueToolScopes) == 3
	})
	var secondErr, secondScope string
	h.pump.do(func() {
		ct := h.app.contactByID(contactID)
		scopeRefs := make([]string, 0, len(h.app.issueToolScopes))
		for ref, scope := range h.app.issueToolScopes {
			scopeRefs = append(scopeRefs, ref)
			if scope.Mode != issueToolModeWork && ref != scopeRef {
				secondScope = ref
			}
		}
		if ct.SessionID != firstSession || ct.SelectedIssueID != beforeSelected {
			secondErr = fmt.Sprintf("second turn changed session/selection: session=%q/%q selected=%q/%q", firstSession, ct.SessionID, beforeSelected, ct.SelectedIssueID)
			return
		}
		if len(ct.Issues[0].Executions) != 1 || len(h.app.issueToolLedger.Operations) != 2 {
			secondErr = fmt.Sprintf("second query created work: executions=%d ops=%d", len(ct.Issues[0].Executions), len(h.app.issueToolLedger.Operations))
			return
		}
		if len(scopeRefs) != 3 || secondScope == "" {
			secondErr = fmt.Sprintf("second turn did not get a fresh scope: %v", scopeRefs)
		}
	})
	if secondErr != "" {
		t.Fatal(secondErr)
	}
	if provider.count() != 8 {
		t.Fatalf("provider requests after second turn = %d, want 8", provider.count())
	}
	lastBody := provider.body(provider.count() - 2)
	if !strings.Contains(lastBody, `"name":"issue_get"`) || secondScope == "" || !strings.Contains(lastBody, secondScope) {
		t.Fatalf("second turn query did not use the fresh managed-session scope: %q", truncateIssueToolText(lastBody, 800))
	}
	// The issue work response and conversation final are independent tasks.
	if !strings.Contains(provider.body(provider.count()-1), "CONVERSATION-FINAL") && provider.count() < 4 {
		t.Fatalf("last provider request was not the conversation final step: %q", provider.body(provider.count()-1))
	}
	if _, err := os.Stat(filepath.Join(h.root, "issue-tool-helper")); err == nil {
		t.Fatal("unexpected helper fixture created in workdir")
	}
	h.shutdown(t)
	restarted := restartGUIHarness(t, binary)
	t.Cleanup(func() {
		if !restarted.closed {
			restarted.shutdown(t)
		}
	})
	restarted.pump.wait(func() bool {
		ct := restarted.app.contactByID(contactID)
		return ct != nil && ct.Loaded
	})
	var restartScopeLeaks int
	restarted.pump.do(func() {
		for _, message := range restarted.app.contactByID(contactID).Messages {
			for _, context := range message.Context {
				if context.Name == issueToolScopeContextName {
					restartScopeLeaks++
				}
			}
		}
	})
	if restartScopeLeaks != 0 {
		t.Fatalf("create-mutated scope leaked after restart %d times", restartScopeLeaks)
	}
}

type issueToolSelectionProvider struct {
	mu      sync.Mutex
	bodies  []string
	issueID string
}

func (p *issueToolSelectionProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

func (p *issueToolSelectionProvider) body(i int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < 0 || i >= len(p.bodies) {
		return ""
	}
	return p.bodies[i]
}

func (p *issueToolSelectionProvider) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.bodies = append(p.bodies, string(body))
		step := len(p.bodies) - 1
		issueID := p.issueID
		p.mu.Unlock()
		var response any
		if step%2 == 0 {
			response = issueResultFunctionCall("issue_get", "selection-get", map[string]any{
				"scope": issueToolTestScope(string(body)), "issue_id": issueID,
			})
		} else {
			response = issueResultPageFinal("SELECTION-RESULT")
		}
		data, _ := json.Marshal(response)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

func issueToolTestOutput(body, callID string) string {
	var payload struct {
		Input []struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		} `json:"input"`
	}
	if json.Unmarshal([]byte(body), &payload) != nil {
		return ""
	}
	for _, input := range payload.Input {
		if input.Type != "function_call_output" || input.CallID != callID {
			continue
		}
		var output struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal([]byte(input.Output), &output) != nil {
			return ""
		}
		for _, content := range output.Content {
			return content.Text
		}
	}
	return ""
}

func TestIssueToolExistingSchemaTwoSessionGetsSelectionAndRestart(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := &issueToolSelectionProvider{}
	h := startIssueToolTestHarness(t, binary, provider.handler())
	var sessionID, contactID, issueID string
	var setupErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if !h.app.createIssue("RESTART-GOAL", "RESTART-REQ") {
			setupErr = h.app.issueFormError
			return
		}
		issueID = ct.SelectedIssueID
		session, err := h.app.rt.createConfiguredConversationSession(h.root, "", map[string]any{
			"builtin": []string{}, "external": []map[string]string{},
		})
		if err != nil {
			setupErr = err.Error()
			return
		}
		ct.SessionID = session.SessionID
		ct.Loaded = true
		ct.Context.items = []taskContextItem{{
			ID: "user-material", Name: "user-note", Text: "literal user material containing {\"type\":\"caller_context\"}", Revision: 1,
		}}
		sessionID, contactID = session.SessionID, ct.ID
		if err := h.app.persist(); err != nil {
			setupErr = err.Error()
		}
	})
	if setupErr != "" {
		t.Fatal(setupErr)
	}
	beforeConfig, err := h.app.rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	builtinBefore := append([]string(nil), beforeConfig.EffectiveConfig.ToolSelection.Builtin...)
	modelBefore, cwdBefore := beforeConfig.EffectiveConfig.Model, beforeConfig.EffectiveConfig.WorkingDirectory
	provider.mu.Lock()
	provider.issueID = issueID
	provider.mu.Unlock()
	var sendErr string
	h.pump.do(func() {
		ct := h.app.contactByID(contactID)
		ct.Draft = issueToolTestMarker + "-EXISTING"
		if !h.app.sendCurrent() {
			sendErr = ct.SendError
		}
	})
	if sendErr != "" {
		t.Fatal(sendErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.contactByID(contactID)
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && provider.count() == 2
	})
	var userHistoryContexts []taskContextItem
	var userHistoryText, userContextClearErr string
	h.pump.do(func() {
		ct := h.app.contactByID(contactID)
		if len(ct.Context.items) != 0 {
			userContextClearErr = "accepted send did not clear user context"
			return
		}
		for _, message := range ct.Messages {
			if message.Kind == msgUser && len(message.Context) > 0 {
				userHistoryContexts = append([]taskContextItem(nil), message.Context...)
				userHistoryText = message.Text
			}
		}
	})
	if userContextClearErr != "" {
		t.Fatal(userContextClearErr)
	}
	if len(userHistoryContexts) != 1 || userHistoryContexts[0].Name != "user-note" ||
		!strings.Contains(userHistoryContexts[0].Text, "literal user material") || userHistoryText != issueToolTestMarker+"-EXISTING" {
		t.Fatalf("GUI user history misclassified internal scope: text=%q contexts=%+v", userHistoryText, userHistoryContexts)
	}
	if !strings.Contains(provider.body(0), "user-note") || !strings.Contains(provider.body(0), issueToolScopeContextName) {
		t.Fatal("wire did not contain both user material and app scope")
	}
	var firstQuery IssueToolIssueDetail
	if err := json.Unmarshal([]byte(issueToolTestOutput(provider.body(1), "selection-get")), &firstQuery); err != nil {
		t.Fatalf("first tool output: %v", err)
	}
	if firstQuery.Issue.ID != issueID || firstQuery.Issue.Goal != "RESTART-GOAL" || len(firstQuery.Requirements) != 1 ||
		firstQuery.Requirements[0].Text != "RESTART-REQ" || firstQuery.ExecutionCount != 0 {
		t.Fatalf("first real issue_get output = %+v issueID=%q body1=%s", firstQuery, issueID, truncateIssueToolText(provider.body(1), 1800))
	}
	var configured bool
	var readErr error
	var schema int
	var selection runtimeToolSelection
	h.pump.do(func() {
		config, err := h.app.rt.connectionConfig(sessionID)
		readErr = err
		schema = config.StorageSchema
		selection = config.EffectiveConfig.ToolSelection
		configured = runtimeSelectionHasIssueTools(selection)
	})
	if readErr != nil || !configured {
		var debug string
		h.pump.do(func() { debug = h.app.contactByID(contactID).SendError })
		t.Fatalf("existing schema2 tool selection = configured=%v err=%v schema=%d selection=%+v count=%d sendErr=%q body0tools=%v", configured, readErr, schema, selection, provider.count(), debug, strings.Contains(provider.body(0), `"name":"issue_action"`))
	}
	if len(builtinBefore) != 0 || len(selection.Builtin) != 0 {
		t.Fatalf("explicit empty builtin changed: before=%v after=%v", builtinBefore, selection.Builtin)
	}
	config, err := h.app.rt.connectionConfig(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.rt.updateSessionModel(sessionID, config.ConfigRevision, runtimeModelPatch{Model: config.EffectiveConfig.Model}); err != nil {
		t.Fatalf("model apply config update: %v", err)
	}
	config, err = h.app.rt.connectionConfig(sessionID)
	if err != nil || !runtimeSelectionHasIssueTools(config.EffectiveConfig.ToolSelection) {
		t.Fatalf("model apply dropped issue tools: err=%v selection=%+v", err, config.EffectiveConfig.ToolSelection)
	}
	if config.EffectiveConfig.Model != modelBefore || config.EffectiveConfig.WorkingDirectory != cwdBefore {
		t.Fatalf("tool selection changed model/cwd: model=%q/%q cwd=%q/%q", modelBefore, config.EffectiveConfig.Model, cwdBefore, config.EffectiveConfig.WorkingDirectory)
	}
	if strings.Contains(provider.body(0), `"name":"issue_action"`) == false {
		t.Fatal("existing schema2 conversation did not expose issue tools")
	}
	var oldEndpoint string
	h.pump.do(func() { oldEndpoint = h.app.issueToolEndpoint })
	h.shutdown(t)
	restarted := restartGUIHarness(t, binary)
	t.Cleanup(func() {
		if !restarted.closed {
			restarted.shutdown(t)
		}
	})
	restarted.pump.wait(func() bool {
		ct := restarted.app.contactByID(contactID)
		return ct != nil && ct.Loaded
	})
	var restartHistoryContexts []taskContextItem
	restarted.pump.do(func() {
		ct := restarted.app.contactByID(contactID)
		for _, message := range ct.Messages {
			if message.Kind == msgUser && len(message.Context) > 0 {
				restartHistoryContexts = append([]taskContextItem(nil), message.Context...)
			}
		}
	})
	if len(restartHistoryContexts) != 1 || restartHistoryContexts[0].Name != "user-note" {
		t.Fatalf("restart history misclassified internal scope: %+v", restartHistoryContexts)
	}
	if restarted.app.issueToolEndpoint == oldEndpoint || restarted.app.issueToolEndpoint == "" {
		t.Fatalf("restart did not establish a new IPC socket: old=%q new=%q", oldEndpoint, restarted.app.issueToolEndpoint)
	}
	restartConfig, err := restarted.app.rt.connectionConfig(sessionID)
	if err != nil || !runtimeSelectionHasIssueTools(restartConfig.EffectiveConfig.ToolSelection) ||
		len(restartConfig.EffectiveConfig.ToolSelection.Builtin) != 0 ||
		restartConfig.EffectiveConfig.Model != modelBefore || restartConfig.EffectiveConfig.WorkingDirectory != cwdBefore {
		t.Fatalf("restart config changed: err=%v builtin=%v model=%q cwd=%q", err, restartConfig.EffectiveConfig.ToolSelection.Builtin, restartConfig.EffectiveConfig.Model, restartConfig.EffectiveConfig.WorkingDirectory)
	}
	var restartSendErr string
	restarted.pump.do(func() {
		ct := restarted.app.contactByID(contactID)
		if ct == nil {
			restartSendErr = "contact missing after restart"
			return
		}
		ct.Draft = issueToolTestMarker + "-RESTART"
		if !restarted.app.sendCurrent() {
			restartSendErr = ct.SendError
		}
	})
	if restartSendErr != "" {
		t.Fatal(restartSendErr)
	}
	restarted.pump.wait(func() bool {
		ct := restarted.app.contactByID(contactID)
		return ct != nil && ct.SendState == sendIdle && ct.ActiveTaskID == "" && provider.count() == 4
	})
	if !strings.Contains(provider.body(2), `"name":"issue_get"`) {
		t.Fatal("restart turn did not query through the new MCP helper socket")
	}
	var secondQuery IssueToolIssueDetail
	if err := json.Unmarshal([]byte(issueToolTestOutput(provider.body(3), "selection-get")), &secondQuery); err != nil {
		t.Fatalf("restart tool output: %v", err)
	}
	if secondQuery.Issue.ID != issueID || secondQuery.Issue.Goal != "RESTART-GOAL" ||
		len(secondQuery.Requirements) != 1 || secondQuery.Requirements[0].Text != "RESTART-REQ" || secondQuery.ExecutionCount != 0 {
		t.Fatalf("restart real issue_get output = %+v", secondQuery)
	}
	var restartStateErr string
	restarted.pump.do(func() {
		ct := restarted.app.contactByID(contactID)
		if ct == nil || len(ct.Issues) != 1 || len(ct.Issues[0].Executions) != 0 || len(restarted.app.issueToolLedger.Operations) != 0 {
			restartStateErr = fmt.Sprintf("restart query created execution/operation: %+v", ct)
		}
	})
	if restartStateErr != "" {
		t.Fatal(restartStateErr)
	}
}

type issueToolWaitAnswerProvider struct {
	mu           sync.Mutex
	bodies       []string
	conversation int
	issueWork    int
	issueID      string
	revision     int
	questionRef  string
}

func (p *issueToolWaitAnswerProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

func (p *issueToolWaitAnswerProvider) body(i int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < 0 || i >= len(p.bodies) {
		return ""
	}
	return p.bodies[i]
}

func (p *issueToolWaitAnswerProvider) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.bodies = append(p.bodies, string(body))
		isConversation := strings.Contains(string(body), "WAIT-CONV-")
		step := p.conversation
		if isConversation {
			p.conversation++
		}
		workStep := p.issueWork
		if strings.Contains(string(body), issueToolWaitGoal) && !isConversation {
			p.issueWork++
		}
		issueID, revision, questionRef := p.issueID, p.revision, p.questionRef
		scope := issueToolTestScope(string(body))
		p.mu.Unlock()
		var response any
		switch {
		case !isConversation && workStep == 0:
			response = issueResultPageFinal("A-FIRST")
		case !isConversation:
			response = issueResultPageFinal("A-SECOND")
		case step == 0:
			response = issueResultFunctionCall("issue_action", "wait-call", map[string]any{
				"scope": scope, "action": issueToolActionWait, "issue_id": issueID,
				"expected_revision": revision, "text": "Which verification method?",
			})
		case step == 1:
			p.mu.Lock()
			p.questionRef = issueToolTestQuestionRef(string(body), "wait-call")
			p.mu.Unlock()
			response = issueResultPageFinal("WAIT-CONV-FINAL")
		case step == 2:
			response = issueResultPageFinal("B-CONV-FINAL")
		case step == 3:
			response = issueResultFunctionCall("issue_action", "answer-call", map[string]any{
				"scope": scope, "action": issueToolActionAddSupplement, "issue_id": issueID,
				"text": "Use SMS.", "question_ref": questionRef,
			})
		case step == 4:
			response = issueResultPageFinal("ANSWER-CONV-FINAL")
		case step == 5:
			response = issueResultFunctionCall("issue_action", "duplicate-answer", map[string]any{
				"scope": scope, "action": issueToolActionAddSupplement, "issue_id": issueID,
				"text": "Use SMS.", "question_ref": questionRef,
			})
		default:
			response = issueResultPageFinal("DUP-CONV-FINAL")
		}
		data, _ := json.Marshal(response)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

func issueToolTestQuestionRef(body, callID string) string {
	text := issueToolTestOutput(body, callID)
	var receipt IssueToolReceipt
	if json.Unmarshal([]byte(text), &receipt) != nil {
		return ""
	}
	return receipt.QuestionRef
}

func TestIssueToolWaitAnswerResumeRealChain(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := &issueToolWaitAnswerProvider{}
	h := startIssueToolTestHarness(t, binary, provider.handler())
	var issueID, scopeRef, questionRef string
	var revision int
	var setupErr string
	h.pump.do(func() {
		if !h.app.createIssue("WAIT-A-GOAL", "WAIT-A-REQ") {
			setupErr = h.app.issueFormError
			return
		}
		ct := h.app.selectedContact()
		issueID = ct.SelectedIssueID
		revision = ct.issueByID(issueID).RequirementsRevision
		if !h.app.runIssue(issueID) {
			setupErr = h.app.issueFormError
			return
		}
	})
	if setupErr != "" {
		t.Fatal(setupErr)
	}
	provider.mu.Lock()
	provider.issueID = issueID
	provider.revision = revision
	provider.mu.Unlock()
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" &&
			issue != nil && len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted
	})
	var sendErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "WAIT-CONV-WAIT"
		if !h.app.sendCurrent() {
			sendErr = ct.SendError
		}
	})
	if sendErr != "" {
		t.Fatal(sendErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" &&
			issue != nil && issue.Status == issueStatusWaitingUser && issue.openQuestion() != nil
	})
	h.pump.do(func() {
		var latest int64
		for ref, scope := range h.app.issueToolScopes {
			if scope.CreatedAt >= latest {
				latest, scopeRef = scope.CreatedAt, ref
			}
		}
		if question := h.app.selectedContact().issueByID(issueID).openQuestion(); question != nil {
			questionRef = question.Ref
		}
	})
	// The scope used by the wait turn is the latest persisted one at this point.
	var waitStateErr string
	h.pump.do(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		if issue == nil || issue.Status != issueStatusWaitingUser {
			waitStateErr = fmt.Sprintf("wait did not mark A waiting: %+v", issue)
			return
		}
		question := issue.openQuestion()
		if question == nil || question.Text == "" || question.RequirementsRevision != revision ||
			question.SourceScopeRef == "" || question.SourceTurnID == "" {
			waitStateErr = fmt.Sprintf("persisted question source = %+v", question)
			return
		}
		sourceScope, ok := h.app.issueToolScopes[question.SourceScopeRef]
		if !ok || question.SourceScopeRef != scopeRef {
			waitStateErr = fmt.Sprintf("question source scope is not the wait turn: question=%+v scopeRef=%q", question, scopeRef)
			return
		}
		if question.SourceTaskID != "" && question.SourceTaskID != sourceScope.RuntimeTaskID {
			waitStateErr = fmt.Sprintf("question task/scope mismatch: question=%+v scope=%+v", question, sourceScope)
		}
	})
	if waitStateErr != "" {
		t.Fatal(waitStateErr)
	}
	var bErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "WAIT-CONV-B"
		if !h.app.sendCurrent() {
			bErr = ct.SendError
		}
	})
	if bErr != "" {
		t.Fatal(bErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && issue != nil &&
			issue.Status == issueStatusWaitingUser && issue.Work.ActiveTaskID == ""
	})
	var answerErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "WAIT-CONV-ANSWER"
		if !h.app.sendCurrent() {
			answerErr = ct.SendError
		}
	})
	if answerErr != "" {
		t.Fatal(answerErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && issue != nil &&
			len(issue.Executions) == 2 && issue.Work.ActiveTaskID == "" &&
			issue.Executions[1].Status == issueExecutionCompleted
	})
	var secondWorkErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		if issue == nil || issue.openQuestion() != nil || len(issue.Questions) != 1 || issue.Questions[0].State != issueQuestionAnswered {
			secondWorkErr = fmt.Sprintf("answer/question state = %+v", issue)
		}
		if containsText(contactMessageTextsForWork(issue), "B-CONV-FINAL") {
			secondWorkErr = "B leaked into A work"
		}
	})
	if secondWorkErr != "" {
		t.Fatal(secondWorkErr)
	}
	var duplicateErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "WAIT-CONV-DUP"
		if !h.app.sendCurrent() {
			duplicateErr = ct.SendError
		}
	})
	if duplicateErr != "" {
		t.Fatal(duplicateErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && issue != nil && len(issue.Executions) == 2
	})
	var duplicateStateErr string
	h.pump.do(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		if issue == nil || len(issue.Executions) != 2 || issue.Supplements.pendingCount() != 0 {
			duplicateStateErr = fmt.Sprintf("duplicate answer created work/supplement: %+v", issue)
		}
	})
	if duplicateStateErr != "" {
		t.Fatal(duplicateStateErr)
	}
	// Wait for the terminal-triggered history reload callbacks to drain before
	// the test closes the UI pump.
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && !ct.HistoryDirty &&
			issue != nil && issue.Work.ActiveTaskID == "" && !issue.Work.HistoryDirty
	})
	// Restart and query the old issue through the retained scope.
	h.shutdown(t)
	restarted := restartGUIHarness(t, binary)
	t.Cleanup(func() {
		if !restarted.closed {
			restarted.shutdown(t)
		}
	})
	restarted.pump.wait(func() bool {
		ct := restarted.app.contactByID(h.app.selectedContact().ID)
		return ct != nil && ct.Loaded
	})
	var queryArgs []byte
	restarted.pump.do(func() {
		queryArgs, _ = json.Marshal(map[string]any{"scope": scopeRef, "issue_id": issueID})
	})
	query, err := restarted.app.handleIssueToolForward(context.Background(), issueToolForwardRequest{Tool: "issue_get", Arguments: queryArgs})
	if err != nil || query.IsError {
		t.Fatalf("restart query = %+v err=%v", query, err)
	}
	var detail IssueToolIssueDetail
	if err := json.Unmarshal(issueToolResponseJSON(t, query), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ExecutionCount != 2 || detail.Issue.Goal != "WAIT-A-GOAL" {
		t.Fatalf("restart old issue = %+v", detail)
	}
	var persistedQuestion *issueQuestion
	restarted.pump.do(func() {
		issue := restarted.app.contacts[0].Issues[0]
		if question := issue.questionByRef(questionRef); question != nil {
			copy := *question
			persistedQuestion = &copy
		}
	})
	if persistedQuestion == nil || persistedQuestion.State != issueQuestionAnswered || persistedQuestion.AnswerSupplementID == "" {
		t.Fatalf("restart question/answer association = %+v", persistedQuestion)
	}
}

func TestIssueToolScopeWireBudgetRejectsBeforeSend(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := &issueToolSelectionProvider{}
	h := startIssueToolTestHarness(t, binary, provider.handler())
	large := taskContextItem{
		ID: "large-user-context", Name: "large user material",
		Text: strings.Repeat("x", taskContextMaxBytes-64), Revision: 1,
	}
	var sendErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Context.items = []taskContextItem{large}
		ct.Draft = "budget request"
		if !h.app.sendCurrent() {
			sendErr = ct.SendError
		}
	})
	if sendErr != "" {
		t.Fatal(sendErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.SendState == sendIdle && strings.Contains(ct.SendError, "1 MiB")
	})
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if ct.Draft != "budget request" || len(ct.Context.items) != 1 || ct.Context.items[0].Text != large.Text {
			t.Fatalf("budget failure lost draft/context: draft=%q contexts=%d", ct.Draft, len(ct.Context.items))
		}
	})
	if provider.count() != 0 {
		t.Fatalf("budget failure reached provider %d times", provider.count())
	}
}

type issueToolWorkWaitProvider struct {
	mu           sync.Mutex
	bodies       []string
	work         int
	bWork        int
	conversation int
	issueID      string
	questionRef  string
}

func (p *issueToolWorkWaitProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

func (p *issueToolWorkWaitProvider) body(i int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < 0 || i >= len(p.bodies) {
		return ""
	}
	return p.bodies[i]
}

func (p *issueToolWorkWaitProvider) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.bodies = append(p.bodies, string(body))
		isConversation := strings.Contains(string(body), "WORK-CONV")
		isAWork := strings.Contains(string(body), "WORK-WAIT-GOAL") && !isConversation
		isBWork := strings.Contains(string(body), "WORK-B-GOAL") && !isConversation
		workStep := p.work
		if isAWork {
			p.work++
		}
		if isBWork {
			p.bWork++
		}
		convStep := p.conversation
		if isConversation {
			p.conversation++
		}
		issueID, questionRef := p.issueID, p.questionRef
		scope := issueToolTestScope(string(body))
		p.mu.Unlock()
		var response any
		switch {
		case isAWork && workStep == 0:
			response = issueResultFunctionCall("issue_wait", "work-wait", map[string]any{
				"scope": scope, "text": "Which environment should I use?",
			})
		case isAWork:
			response = issueResultPageFinal("A-WORK-SECOND")
		case isBWork:
			response = issueResultPageFinal("B-WORK-OUTPUT")
		case isConversation && convStep == 0:
			response = issueResultPageFinal("B-WORK-FINAL")
		case isConversation && convStep == 1:
			response = issueResultFunctionCall("issue_list", "work-list", map[string]any{"scope": scope})
		case isConversation && convStep == 2:
			var listed IssueToolIssueListResult
			if json.Unmarshal([]byte(issueToolTestOutput(string(body), "work-list")), &listed) == nil {
				p.mu.Lock()
				for _, summary := range listed.Issues {
					if summary.Goal == "WORK-WAIT-GOAL" {
						p.issueID, p.questionRef = summary.ID, summary.QuestionRef
					}
				}
				p.mu.Unlock()
			}
			response = issueResultPageFinal("A-LIST-FINAL")
		case isConversation && convStep == 3:
			response = issueResultFunctionCall("issue_action", "answer-work", map[string]any{
				"scope": scope, "action": issueToolActionAddSupplement, "issue_id": issueID,
				"text": "Use staging.", "question_ref": questionRef,
			})
		default:
			response = issueResultPageFinal("A-ANSWER-FINAL")
		}
		data, _ := json.Marshal(response)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

func TestIssueToolWorkWaitPausesAndAnswerContinues(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := &issueToolWorkWaitProvider{}
	h := startIssueToolTestHarness(t, binary, provider.handler())
	var issueID, bIssueID, contactID string
	var revision int
	var setupErr string
	h.pump.do(func() {
		if !h.app.createIssue("WORK-WAIT-GOAL", "WORK-WAIT-REQ") {
			setupErr = h.app.issueFormError
			return
		}
		ct := h.app.selectedContact()
		issueID = ct.SelectedIssueID
		issue := ct.issueByID(issueID)
		if issue == nil || !h.app.editIssueFor(ct, issueID, issue.Goal, issueRequirementsText(issue)+"\nWORK-WAIT-REQ-2") {
			setupErr = "could not raise requirements revision: " + h.app.issueFormError
			return
		}
		revision = issue.RequirementsRevision
		contactID = ct.ID
		if !h.app.runIssue(issueID) {
			setupErr = h.app.issueFormError
			return
		}
		if !h.app.createIssue("WORK-B-GOAL", "WORK-B-REQ") {
			setupErr = h.app.issueFormError
			return
		}
		bIssueID = ct.SelectedIssueID
		if bIssueID == issueID {
			setupErr = "B issue was not created"
		}
	})
	if setupErr != "" {
		t.Fatal(setupErr)
	}
	t.Logf("stage: A work dispatched; waiting for work wait/cancel")
	h.pump.wait(func() bool {
		ct := h.app.contactByID(contactID)
		issue := ct.issueByID(issueID)
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && issue != nil &&
			issue.Status == issueStatusWaitingUser && issue.Work.ActiveTaskID == "" &&
			len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionInterrupted &&
			issue.Executions[0].WaitRequested
	})
	if !strings.Contains(provider.body(0), `"name":"issue_wait"`) ||
		strings.Contains(provider.body(0), `"name":"issue_action"`) || strings.Contains(provider.body(0), `"name":"issue_get"`) {
		t.Fatal("work request tool selection is not the narrow issue_wait set")
	}
	var questionRef, sourceTask string
	h.pump.do(func() {
		issue := h.app.contactByID(contactID).issueByID(issueID)
		question := issue.openQuestion()
		if question == nil || question.Text == "" || question.RequirementsRevision != revision {
			t.Fatalf("work question = %+v", question)
		}
		questionRef, sourceTask = question.Ref, question.SourceTaskID
		if sourceTask != "" && sourceTask != issue.Executions[0].TaskID {
			t.Fatalf("work question source task = %q execution=%q", sourceTask, issue.Executions[0].TaskID)
		}
		if issue.Work.StopRequestedTasks[issue.Executions[0].TaskID] {
			t.Fatal("work wait was recorded as a user Stop")
		}
	})
	t.Logf("stage: A waiting; sending B")
	var bErr string
	h.pump.do(func() {
		ct := h.app.contactByID(contactID)
		ct.Draft = "WORK-CONV-B"
		if !h.app.sendCurrent() {
			bErr = ct.SendError
		}
	})
	if bErr != "" {
		t.Fatal(bErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.contactByID(contactID)
		issue := ct.issueByID(issueID)
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && issue != nil &&
			issue.Status == issueStatusWaitingUser && issue.Work.ActiveTaskID == ""
	})
	var bWorkErr string
	h.pump.do(func() {
		if !h.app.runIssue(bIssueID) {
			bWorkErr = h.app.issueFormError
		}
	})
	if bWorkErr != "" {
		t.Fatalf("B work run: %s", bWorkErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.contactByID(contactID)
		b := ct.issueByID(bIssueID)
		return b != nil && len(b.Executions) == 1 && b.Executions[0].Status == issueExecutionCompleted && b.Work.ActiveTaskID == ""
	})
	var listErr string
	h.pump.do(func() {
		ct := h.app.contactByID(contactID)
		ct.Draft = "WORK-CONV-LIST"
		if !h.app.sendCurrent() {
			listErr = ct.SendError
		}
	})
	if listErr != "" {
		t.Fatal(listErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.contactByID(contactID)
		a := ct.issueByID(issueID)
		b := ct.issueByID(bIssueID)
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && a != nil && b != nil &&
			len(a.Executions) == 1 && len(b.Executions) == 1 && a.Status == issueStatusWaitingUser
	})
	t.Logf("stage: B work done and list clarification done; answering A")
	var answerErr string
	h.pump.do(func() {
		ct := h.app.contactByID(contactID)
		ct.Draft = "WORK-CONV-ANSWER"
		if !h.app.sendCurrent() {
			answerErr = ct.SendError
		}
	})
	if answerErr != "" {
		t.Fatal(answerErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.contactByID(contactID)
		issue := ct.issueByID(issueID)
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && issue != nil &&
			len(issue.Executions) == 2 && issue.Work.ActiveTaskID == "" &&
			issue.Executions[1].Status == issueExecutionCompleted
	})
	var finalErr string
	h.pump.do(func() {
		issue := h.app.contactByID(contactID).issueByID(issueID)
		if issue.openQuestion() != nil || len(issue.Questions) != 1 || issue.Questions[0].State != issueQuestionAnswered {
			finalErr = fmt.Sprintf("work answer state = %+v", issue)
		}
		if containsText(contactMessageTextsForWork(issue), "B-WORK-FINAL") {
			finalErr = "B conversation leaked into A work messages"
		}
	})
	if finalErr != "" {
		t.Fatal(finalErr)
	}
	var bIsolationErr string
	h.pump.do(func() {
		b := h.app.contactByID(contactID).issueByID(bIssueID)
		if b == nil || len(b.Executions) != 1 || b.Executions[0].Status != issueExecutionCompleted ||
			!containsText(contactMessageTextsForWork(b), "B-WORK-OUTPUT") {
			bIsolationErr = fmt.Sprintf("B work changed/duplicated: %+v", b)
		}
	})
	if bIsolationErr != "" {
		t.Fatal(bIsolationErr)
	}
	h.shutdown(t)
	restarted := restartGUIHarness(t, binary)
	t.Cleanup(func() {
		if !restarted.closed {
			restarted.shutdown(t)
		}
	})
	restarted.pump.wait(func() bool {
		ct := restarted.app.contactByID(contactID)
		return ct != nil && ct.Loaded && ct.issueByID(issueID) != nil
	})
	var restartQuestion *issueQuestion
	restarted.pump.do(func() {
		issue := restarted.app.contactByID(contactID).issueByID(issueID)
		if question := issue.questionByRef(questionRef); question != nil {
			copy := *question
			restartQuestion = &copy
		}
		if len(issue.Executions) != 2 {
			t.Fatalf("restart work executions = %d", len(issue.Executions))
		}
		b := restarted.app.contactByID(contactID).issueByID(bIssueID)
		if b == nil || len(b.Executions) != 1 || b.Executions[0].Status != issueExecutionCompleted {
			t.Fatalf("restart B work = %+v", b)
		}
	})
	if restartQuestion == nil || restartQuestion.State != issueQuestionAnswered || restartQuestion.AnswerSupplementID == "" {
		t.Fatalf("restart work question = %+v", restartQuestion)
	}
}

func TestIssueToolReleaseCallbackRunsBeforeQuit(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := &issueToolSelectionProvider{}
	h := startIssueToolTestHarness(t, binary, provider.handler())
	var contactID, issueID string
	var setupErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if !h.app.createIssue("RELEASE-GOAL", "") {
			setupErr = h.app.issueFormError
			return
		}
		issueID, contactID = ct.SelectedIssueID, ct.ID
		session, err := h.app.rt.createConfiguredSession(h.root, "")
		if err != nil {
			setupErr = err.Error()
			return
		}
		issue := ct.issueByID(issueID)
		issue.WorkSessionID = session.SessionID
		issue.Work.Loaded = true
		issue.Work.SendState = sendIdle
		if err := h.app.persist(); err != nil {
			setupErr = err.Error()
		}
	})
	if setupErr != "" {
		t.Fatal(setupErr)
	}
	originalHook := h.app.getUIHook()
	held := make(chan func(), 1)
	h.pump.do(func() { h.app.setUIHook(func(fn func()) { held <- fn }) })
	h.pump.do(func() {
		ct := h.app.contactByID(contactID)
		h.app.releaseIssueWorkSession(ct, ct.issueByID(issueID))
	})
	var releaseCallback func()
	select {
	case releaseCallback = <-held:
	case <-time.After(10 * time.Second):
		h.pump.do(func() { h.app.setUIHook(originalHook) })
		t.Fatal("release callback did not reach the UI queue")
	}
	var releaseStateErr string
	h.pump.do(func() {
		if h.app.issueToolReleaseInFlight != 1 {
			releaseStateErr = fmt.Sprintf("release in-flight count before callback = %d", h.app.issueToolReleaseInFlight)
			return
		}
		if h.app.contactByID(contactID).issueByID(issueID).Work.Released {
			releaseStateErr = "released flag set before final release callback"
		}
	})
	if releaseStateErr != "" {
		t.Fatal(releaseStateErr)
	}
	var order []string
	var shutdownStateErr string
	h.pump.do(func() {
		h.app.requestQuit = func() { order = append(order, "quit") }
		h.app.beginShutdown()
		if h.app.quitReady || len(order) != 0 {
			shutdownStateErr = "shutdown completed before held release callback"
		}
		h.app.setUIHook(originalHook)
	})
	if shutdownStateErr != "" {
		t.Fatal(shutdownStateErr)
	}
	h.pump.do(func() {
		releaseCallback()
		order = append(order, "release-callback")
	})
	h.pump.wait(func() bool { return h.app.quitReady })
	if h.app.issueToolReleaseInFlight != 0 || !h.app.contactByID(contactID).issueByID(issueID).Work.Released {
		t.Fatalf("release state after callback = inFlight=%d released=%v", h.app.issueToolReleaseInFlight, h.app.contactByID(contactID).issueByID(issueID).Work.Released)
	}
	if len(order) < 2 || order[len(order)-1] != "quit" || order[len(order)-2] != "release-callback" {
		t.Fatalf("release/quit callback order = %v", order)
	}
}

func TestIssueToolScopeTokenRealChain(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := newIssueToolChainProvider()
	provider.scopeFromContext = true
	h := startIssueToolTestHarness(t, binary, provider.handler())
	t.Cleanup(provider.releaseFixture)
	material := "TOKEN-WIRE-MATERIAL\n青岚\n"
	var sendErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if _, err := ct.Context.AddSnippet("token-wire-material", material); err != nil {
			sendErr = err.Error()
			return
		}
		ct.Draft = issueToolTestMarker
		if !h.app.sendCurrent() {
			sendErr = ct.SendError
		}
	})
	if sendErr != "" {
		t.Fatal(sendErr)
	}
	select {
	case <-provider.arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("provider did not receive the conversation request")
	}
	h.pump.wait(func() bool { return len(h.app.issueToolScopes) == 1 })
	var scopeRef string
	h.pump.do(func() {
		for ref := range h.app.issueToolScopes {
			scopeRef = ref
		}
	})
	if scopeRef == "" {
		t.Fatal("no app-issued scope")
	}
	provider.mu.Lock()
	provider.scope = scopeRef
	provider.mu.Unlock()
	provider.releaseFixture()
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		if ct == nil || ct.SendState != sendIdle || ct.ActiveTaskID != "" || len(ct.Issues) != 1 || len(ct.Issues[0].Executions) != 1 {
			return false
		}
		return ct.Issues[0].Executions[0].Status == issueExecutionCompleted && ct.Issues[0].Work.ActiveTaskID == ""
	})
	var issue *productIssue
	h.pump.do(func() { issue = h.app.selectedContact().Issues[0] })
	if issue == nil || len(issue.Executions) != 1 {
		t.Fatal("token-wire issue did not complete")
	}
	if got := issueToolTestNamedContext(provider.body(0), issueToolScopeContextName); got != scopeRef || strings.Contains(got, "{") {
		t.Fatalf("named scope context = %q, want opaque ref %q", got, scopeRef)
	}
	execution := issue.Executions[0]
	if len(execution.Context) != 1 || execution.Context[0].Name != "token-wire-material" || execution.Context[0].Text != material {
		t.Fatalf("token-wire execution context = %+v", execution.Context)
	}
}

// issueToolConversationStopProvider is a deterministic local provider for the
// conversation -> issue_action stop path. It holds only the first A issue-work
// request; B and the second A run complete immediately. Each conversation turn
// first requests issue_action, then repeats the same operation once through the
// same-binary helper and Unix IPC before finishing.
type issueToolConversationStopProvider struct {
	mu            sync.Mutex
	bodies        []string
	counts        map[string]int
	aID           string
	gate          chan struct{}
	gateOnce      sync.Once
	arrived       chan struct{}
	arriveOnce    sync.Once
	firstDone     chan struct{}
	firstDoneOnce sync.Once
}

func newIssueToolConversationStopProvider() *issueToolConversationStopProvider {
	return &issueToolConversationStopProvider{
		counts: map[string]int{}, gate: make(chan struct{}),
		arrived: make(chan struct{}), firstDone: make(chan struct{}),
	}
}

func (p *issueToolConversationStopProvider) setAID(issueID string) {
	p.mu.Lock()
	p.aID = issueID
	p.mu.Unlock()
}

func (p *issueToolConversationStopProvider) releaseA() {
	p.gateOnce.Do(func() { close(p.gate) })
}

func (p *issueToolConversationStopProvider) count(group string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.counts[group]
}

func (p *issueToolConversationStopProvider) groupBody(group string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	for index := len(p.bodies) - 1; index >= 0; index-- {
		// Bodies are grouped by the same marker classification as handler.
		body := p.bodies[index]
		if issueToolConversationStopGroup(body) == group {
			return body
		}
	}
	return ""
}

// issueToolCurrentUserText reads the latest user message from the same
// Responses input array the Runtime sends. Classifying the whole body would
// mistake a second conversation turn for the previous turn because its tool
// history still contains the old user text.
func issueToolCurrentUserText(body string) string {
	var request struct {
		Input []struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"input"`
	}
	if json.Unmarshal([]byte(body), &request) != nil {
		return ""
	}
	for index := len(request.Input) - 1; index >= 0; index-- {
		item := request.Input[index]
		if item.Type != "message" || item.Role != "user" {
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
			for _, part := range parts {
				if part.Text != "" {
					return part.Text
				}
			}
		}
	}
	return ""
}

func issueToolConversationStopGroup(body string) string {
	current := issueToolCurrentUserText(body)
	switch {
	case strings.Contains(current, "STOP-CONV-"):
		return "stop-conversation"
	case strings.Contains(current, "RUN-CONV-"):
		return "run-conversation"
	case strings.Contains(current, "A-GOAL-200"):
		return "a-work"
	case strings.Contains(current, "B-GOAL-200"):
		return "b-work"
	default:
		return "other"
	}
}

func (p *issueToolConversationStopProvider) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		group := issueToolConversationStopGroup(string(body))
		p.mu.Lock()
		p.bodies = append(p.bodies, string(body))
		p.counts[group]++
		step := p.counts[group]
		aID := p.aID
		p.mu.Unlock()

		var response any
		switch group {
		case "b-work":
			response = issueResultPageFinal("B-COMPLETED-200")
		case "a-work":
			if step == 1 {
				p.arriveOnce.Do(func() { close(p.arrived) })
				<-p.gate
				response = issueResultPageFinal("A-INTERRUPTED-200")
			} else {
				response = issueResultPageFinal("A-SECOND-COMPLETED-200")
			}
		case "stop-conversation":
			switch {
			case issueToolTestOutput(string(body), "stop-call") == "":
				response = issueResultFunctionCall("issue_action", "stop-call", map[string]any{
					"scope": issueToolTestScope(string(body)), "action": issueToolActionStop, "issue_id": aID,
				})
			case issueToolTestOutput(string(body), "stop-duplicate") == "":
				response = issueResultFunctionCall("issue_action", "stop-duplicate", map[string]any{
					"scope": issueToolTestScope(string(body)), "action": issueToolActionStop, "issue_id": aID,
				})
			default:
				response = issueResultPageFinal("STOP-CONVERSATION-FINAL-200")
			}
		case "run-conversation":
			switch {
			case issueToolTestOutput(string(body), "run-call") == "":
				response = issueResultFunctionCall("issue_action", "run-call", map[string]any{
					"scope": issueToolTestScope(string(body)), "action": issueToolActionRun, "issue_id": aID,
				})
			case issueToolTestOutput(string(body), "run-duplicate") == "":
				response = issueResultFunctionCall("issue_action", "run-duplicate", map[string]any{
					"scope": issueToolTestScope(string(body)), "action": issueToolActionRun, "issue_id": aID,
				})
			default:
				response = issueResultPageFinal("RUN-CONVERSATION-FINAL-200")
			}
		default:
			response = issueResultPageFinal("OTHER-FINAL-200")
		}
		data, _ := json.Marshal(response)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		if group == "a-work" && step == 1 {
			p.firstDoneOnce.Do(func() { close(p.firstDone) })
		}
	}
}

type issueToolJournalTaskSnapshot map[string]string

func issueToolJournalTasks(t *testing.T, h *guiHarness, sessionID string) issueToolJournalTaskSnapshot {
	t.Helper()
	var rt *runtimeClient
	h.pump.do(func() { rt = h.app.rt })
	records, _, err := h.app.historyAllFor(rt, sessionID)
	if err != nil {
		t.Fatalf("read Runtime journal for %s: %v", sessionID, err)
	}
	tasks := issueToolJournalTaskSnapshot{}
	for _, record := range records {
		switch record.Record.Type {
		case "task_started":
			if record.Record.TaskID != "" {
				if _, ok := tasks[record.Record.TaskID]; !ok {
					tasks[record.Record.TaskID] = ""
				}
			}
		case "task_finished":
			if record.Record.TaskID != "" {
				tasks[record.Record.TaskID] = record.Record.Status
			}
		}
	}
	return tasks
}

func TestIssueToolConversationStopAndRunKeepsBAndJournal(t *testing.T) {
	binary := requireRuntimeBinary(t)
	helper := ensureIssueToolTestHelper(t)
	t.Setenv("SAKI_TEST_ISSUE_TOOL_HELPER", helper)
	provider := newIssueToolConversationStopProvider()
	h := startIssueToolTestHarness(t, binary, provider.handler())
	t.Cleanup(provider.releaseA)

	var aID, bID string
	var bGoal string
	var bRequirements []string
	var bRevision int
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if !h.app.createIssue("A-GOAL-200", "A-REQ-200") {
			t.Fatalf("create A: %s", h.app.issueFormError)
		}
		aID = ct.SelectedIssueID
		if !h.app.createIssue("B-GOAL-200", "B-REQ-200") {
			t.Fatalf("create B: %s", h.app.issueFormError)
		}
		bID = ct.SelectedIssueID
		b := ct.issueByID(bID)
		bGoal, bRevision = b.Goal, b.RequirementsRevision
		for _, requirement := range b.Requirements {
			bRequirements = append(bRequirements, requirement.Text)
		}
		if !h.app.runIssue(bID) {
			t.Fatalf("run B: %s", h.app.issueFormError)
		}
	})
	t.Logf("stage: waiting for B completed")
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		b := ct.issueByID(bID)
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && b != nil &&
			b.Work.ActiveTaskID == "" && b.Work.SendState == sendIdle &&
			len(b.Executions) == 1 && b.Executions[0].Status == issueExecutionCompleted &&
			b.WorkSessionID != "" && b.Executions[0].TaskID != ""
	})
	var bExecution issueExecution
	h.pump.do(func() {
		b := h.app.selectedContact().issueByID(bID)
		bExecution = *b.Executions[0]
	})
	provider.setAID(aID)

	h.pump.do(func() {
		if !h.app.runIssue(aID) {
			t.Fatalf("run A: %s", h.app.issueFormError)
		}
		h.app.selectIssue(bID)
	})
	select {
	case <-provider.arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("provider did not receive held A issue work")
	}
	t.Logf("stage: waiting for A accepted")
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		a := ct.issueByID(aID)
		return a != nil && a.Work.ActiveTaskID != "" &&
			len(a.Executions) == 1 &&
			(a.Executions[0].Status == issueExecutionAccepted || a.Executions[0].Status == issueExecutionRunning)
	})
	var aOldExecution, aOldRequirements, aOldSession, aOldTask string
	var aOldPrompt string
	var aOldRevision int
	h.pump.do(func() {
		ct := h.app.selectedContact()
		a := ct.issueByID(aID)
		aOldExecution = a.Executions[0].ID
		aOldPrompt = a.Executions[0].Prompt
		aOldSession, aOldTask = a.Executions[0].SessionID, a.Executions[0].TaskID
		aOldRevision = a.RequirementsRevision
		for _, requirement := range a.Requirements {
			aOldRequirements += requirement.Text + "\n"
		}
	})
	if aOldSession == "" || aOldTask == "" || aOldSession == bExecution.SessionID {
		t.Fatalf("A/B work sessions not distinct and non-empty: A=%q B=%q", aOldSession, bExecution.SessionID)
	}

	var sendErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "STOP-CONV-200"
		if !h.app.sendCurrent() {
			sendErr = ct.SendError
		}
	})
	if sendErr != "" {
		t.Fatalf("send stop conversation: %s", sendErr)
	}
	t.Logf("stage: waiting for A interrupt and conversation final")
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		a := ct.issueByID(aID)
		return a != nil && a.Work.ActiveTaskID == "" && a.Work.SendState == sendIdle &&
			len(a.Executions) == 1 && a.Executions[0].ID == aOldExecution &&
			a.Executions[0].Status == issueExecutionInterrupted &&
			a.Executions[0].TaskID == aOldTask && a.Executions[0].SessionID == aOldSession &&
			ct.SendState == sendIdle && ct.ActiveTaskID == "" && !ct.HistoryDirty
	})
	stopBody := provider.groupBody("stop-conversation")
	if stopBody == "" {
		t.Fatal("stop conversation body was not recorded")
	}
	stopReceiptText := issueToolTestOutput(stopBody, "stop-call")
	if stopReceiptText == "" {
		t.Fatal("stop helper receipt was not returned to the conversation")
	}
	var stopReceipt IssueToolReceipt
	if err := json.Unmarshal([]byte(stopReceiptText), &stopReceipt); err != nil {
		t.Fatalf("decode stop receipt %q: %v", stopReceiptText, err)
	}
	if stopReceipt.State != issueToolReceiptStopRequested || stopReceipt.IssueID != aID ||
		stopReceipt.ExecutionID != aOldExecution || stopReceipt.SessionID != aOldSession ||
		stopReceipt.TaskID != aOldTask {
		t.Fatalf("stop receipt identity = %+v, want A exec/session/task", stopReceipt)
	}
	// 本用例 Stop 后事项仍 in_progress；resume 用于 waiting/needs_attention，
	// 不能替代这里创建下一执行的 run。
	var aStatusAfterStop string
	h.pump.do(func() { aStatusAfterStop = h.app.selectedContact().issueByID(aID).Status })
	if aStatusAfterStop != issueStatusInProgress {
		t.Fatalf("A status after stop = %q, want in_progress before an explicit run", aStatusAfterStop)
	}
	stopDuplicateText := issueToolTestOutput(stopBody, "stop-duplicate")
	if stopDuplicateText == "" {
		t.Fatal("duplicate stop receipt was not returned in the same conversation turn")
	}
	var stopDuplicate IssueToolReceipt
	if err := json.Unmarshal([]byte(stopDuplicateText), &stopDuplicate); err != nil {
		t.Fatalf("decode duplicate stop receipt %q: %v", stopDuplicateText, err)
	}
	if stopDuplicate.State != stopReceipt.State || stopDuplicate.IssueID != stopReceipt.IssueID ||
		stopDuplicate.ExecutionID != stopReceipt.ExecutionID || stopDuplicate.SessionID != stopReceipt.SessionID ||
		stopDuplicate.TaskID != stopReceipt.TaskID {
		t.Fatalf("duplicate stop receipt changed: got=%+v want=%+v", stopDuplicate, stopReceipt)
	}

	var conversationSession string
	h.pump.do(func() { conversationSession = h.app.selectedContact().SessionID })
	if conversationSession == "" || conversationSession == aOldSession || conversationSession == bExecution.SessionID {
		t.Fatalf("conversation session is not independent: %q A=%q B=%q", conversationSession, aOldSession, bExecution.SessionID)
	}

	provider.releaseA()
	select {
	case <-provider.firstDone:
	case <-time.After(15 * time.Second):
		t.Fatal("held A provider request did not finish after release")
	}
	t.Logf("stage: waiting for conversation stop to drain")
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && !ct.HistoryDirty
	})

	var runSendErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "RUN-CONV-200"
		if !h.app.sendCurrent() {
			runSendErr = ct.SendError
		}
	})
	if runSendErr != "" {
		t.Fatalf("send run conversation: %s", runSendErr)
	}
	t.Logf("stage: waiting for second A execution completed")
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		a := ct.issueByID(aID)
		return ct.SendState == sendIdle && ct.ActiveTaskID == "" && !ct.HistoryDirty &&
			a != nil && a.Work.ActiveTaskID == "" && a.Work.SendState == sendIdle &&
			len(a.Executions) == 2 && a.Executions[0].ID == aOldExecution &&
			a.Executions[0].Status == issueExecutionInterrupted &&
			a.Executions[1].Status == issueExecutionCompleted &&
			a.Executions[1].TaskID != aOldTask
	})
	var newExecution issueExecution
	var runReceipt IssueToolReceipt
	var runBody string
	h.pump.do(func() {
		a := h.app.selectedContact().issueByID(aID)
		newExecution = *a.Executions[1]
		runBody = provider.groupBody("run-conversation")
	})
	if runBody == "" {
		t.Fatal("run conversation body was not recorded")
	}
	runReceiptText := issueToolTestOutput(runBody, "run-call")
	if runReceiptText == "" {
		t.Fatal("run helper receipt was not returned to the conversation")
	}
	if err := json.Unmarshal([]byte(runReceiptText), &runReceipt); err != nil {
		t.Fatalf("decode run receipt %q: %v", runReceiptText, err)
	}
	if runReceipt.State != issueToolReceiptAccepted || runReceipt.IssueID != aID ||
		runReceipt.ExecutionID != newExecution.ID || runReceipt.SessionID != newExecution.SessionID ||
		runReceipt.TaskID != newExecution.TaskID || runReceipt.MessageID == "" {
		t.Fatalf("run receipt identity = %+v, want new A execution/task", runReceipt)
	}
	runDuplicateText := issueToolTestOutput(runBody, "run-duplicate")
	if runDuplicateText == "" {
		t.Fatal("duplicate run receipt was not returned in the same conversation turn")
	}
	var runDuplicate IssueToolReceipt
	if err := json.Unmarshal([]byte(runDuplicateText), &runDuplicate); err != nil {
		t.Fatalf("decode duplicate run receipt %q: %v", runDuplicateText, err)
	}
	if runDuplicate.State != runReceipt.State || runDuplicate.IssueID != runReceipt.IssueID ||
		runDuplicate.ExecutionID != runReceipt.ExecutionID || runDuplicate.SessionID != runReceipt.SessionID ||
		runDuplicate.TaskID != runReceipt.TaskID || runDuplicate.MessageID != runReceipt.MessageID {
		t.Fatalf("duplicate run receipt changed: got=%+v want=%+v", runDuplicate, runReceipt)
	}
	if newExecution.ID == aOldExecution || newExecution.TaskID == aOldTask ||
		newExecution.SessionID != aOldSession || newExecution.RequirementsRevision != aOldRevision {
		t.Fatalf("new/old execution identity = new=%+v old=%s/%s/%d", newExecution, aOldExecution, aOldTask, aOldRevision)
	}
	var oldPromptPreserved bool
	h.pump.do(func() {
		oldPromptPreserved = h.app.selectedContact().issueByID(aID).Executions[0].Prompt == aOldPrompt
	})
	if !oldPromptPreserved {
		t.Fatal("old A execution prompt snapshot was not preserved")
	}
	var aRequirementsText []string
	h.pump.do(func() {
		for _, requirement := range h.app.selectedContact().issueByID(aID).Requirements {
			aRequirementsText = append(aRequirementsText, requirement.Text)
		}
	})
	if strings.Join(aRequirementsText, "\n") != strings.TrimSuffix(aOldRequirements, "\n") {
		t.Fatalf("A requirements snapshot changed: %q -> %v", aOldRequirements, aRequirementsText)
	}
	aRecords := issueToolJournalTasks(t, h, aOldSession)
	if len(aRecords) != 2 || aRecords[aOldTask] != string(issueExecutionInterrupted) ||
		aRecords[newExecution.TaskID] != string(issueExecutionCompleted) {
		t.Fatalf("A journal tasks = %v, want exactly old interrupted/new completed", aRecords)
	}
	conversationRecords := issueToolJournalTasks(t, h, conversationSession)
	if len(conversationRecords) != 2 {
		t.Fatalf("conversation journal tasks = %v, want stop+run turns", conversationRecords)
	}
	for taskID, status := range conversationRecords {
		if status != string(issueExecutionCompleted) {
			t.Fatalf("conversation task %s terminal = %q", taskID, status)
		}
	}

	// The duplicate calls above used the same scope/turn and returned the
	// retained receipts. They must not create another operation, execution or
	// Runtime task.
	var operationCount, executionCount int
	h.pump.do(func() {
		operationCount = len(h.app.issueToolLedger.Operations)
		executionCount = len(h.app.selectedContact().issueByID(aID).Executions)
	})
	if operationCount != 2 || executionCount != 2 {
		t.Fatalf("duplicate calls changed operations/executions: ops=%d execs=%d", operationCount, executionCount)
	}
	if provider.count("a-work") != 2 || provider.count("b-work") != 1 ||
		provider.count("stop-conversation") != 3 || provider.count("run-conversation") != 3 ||
		provider.count("other") != 0 {
		t.Fatalf("provider groups = a=%d b=%d stop=%d run=%d other=%d", provider.count("a-work"), provider.count("b-work"), provider.count("stop-conversation"), provider.count("run-conversation"), provider.count("other"))
	}
	var selectedB, bIntact bool
	var bRequirementsText []string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		b := ct.issueByID(bID)
		selectedB = ct.SelectedIssueID == bID
		bIntact = b != nil && b.Goal == bGoal && b.RequirementsRevision == bRevision &&
			len(b.Executions) == 1 && b.Executions[0].ID == bExecution.ID &&
			b.Executions[0].Status == issueExecutionCompleted &&
			b.Executions[0].SessionID == bExecution.SessionID && b.Executions[0].TaskID == bExecution.TaskID &&
			b.Executions[0].MessageID == bExecution.MessageID &&
			b.Executions[0].Prompt == bExecution.Prompt && b.WorkSessionID == bExecution.SessionID
		for _, requirement := range b.Requirements {
			bRequirementsText = append(bRequirementsText, requirement.Text)
		}
	})
	if !selectedB || !bIntact {
		t.Fatalf("selected/B state changed after final A continuation: selected=%v intact=%v", selectedB, bIntact)
	}
	if strings.Join(bRequirementsText, "\n") != strings.Join(bRequirements, "\n") {
		t.Fatalf("B requirements changed: %v -> %v", bRequirements, bRequirementsText)
	}
	bRecords := issueToolJournalTasks(t, h, bExecution.SessionID)
	if len(bRecords) != 1 || bRecords[bExecution.TaskID] != string(issueExecutionCompleted) {
		t.Fatalf("B journal tasks = %v", bRecords)
	}
	t.Logf("provider groups: a=%d b=%d stop=%d run=%d other=%d", provider.count("a-work"), provider.count("b-work"), provider.count("stop-conversation"), provider.count("run-conversation"), provider.count("other"))
	t.Logf("journal A=%v B=%v conversation=%v", aRecords, bRecords, conversationRecords)
	t.Logf("receipts stop=%+v run=%+v", stopReceipt, runReceipt)
	t.Logf("final facts: operations=%d A_executions=%d selectedB=%v conversation_session=%s A_session=%s B_session=%s", operationCount, executionCount, selectedB, conversationSession, aOldSession, bExecution.SessionID)
	h.shutdown(t)
}
