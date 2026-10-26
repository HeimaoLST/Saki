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
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const issueToolTestMarker = "CONVERSATION-TOOL"
const issueToolTestIssueGoal = "ISSUE-TOOL-GOAL"

type issueToolChainProvider struct {
	mu           sync.Mutex
	conversation int
	issueWork    int
	bodies       []string
	scope        string
	arrived      chan struct{}
	release      chan struct{}
	releaseOnce  sync.Once
	once         sync.Once
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
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go tool is required to build the issue tool helper: %v", err)
	}
	dir, err := os.MkdirTemp("", "saki-issue-tool-helper-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	helper := filepath.Join(dir, "saki-issue-tool-helper")
	cmd := exec.Command(goTool, "build", "-o", helper, ".")
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

func issueToolTestScope(body string) string {
	last := ""
	for _, pattern := range []string{
		`"ref":"(scope-[0-9a-f]+)"`,
		`\"ref\":\"(scope-[0-9a-f]+)\"`,
	} {
		for _, match := range regexp.MustCompile(pattern).FindAllStringSubmatch(body, -1) {
			if len(match) == 2 {
				last = match[1]
			}
		}
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
	if !strings.Contains(provider.body(0), issueToolScopeContextName) || !strings.Contains(provider.body(0), scopeRef) {
		t.Fatal("first conversation request did not carry the app-issued scope context")
	}
	if !strings.Contains(provider.body(0), `"name":"issue_action"`) || !strings.Contains(provider.body(0), `"name":"issue_get"`) {
		t.Fatal("first conversation request did not carry the externally selected issue tools")
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
		return len(h.app.issueToolScopes) == 2
	})
	var secondErr, secondScope string
	h.pump.do(func() {
		ct := h.app.contactByID(contactID)
		scopeRefs := make([]string, 0, len(h.app.issueToolScopes))
		for ref := range h.app.issueToolScopes {
			scopeRefs = append(scopeRefs, ref)
			if ref != scopeRef {
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
		if len(scopeRefs) != 2 || scopeRefs[0] == scopeRefs[1] {
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
}
