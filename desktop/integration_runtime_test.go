package main

// Real-process integration for the #60 stdio contract, using the candidate
// runtime binary. It is opt-in so the default CI does not depend on a Rust
// build:
//
//	SAKI_TEST_RUNTIME_BIN=/abs/path/to/Saki go test -run TestRuntimeProcessIntegration -count=1 .
//
// The controlled provider is a local HTTP fixture; no credentials are read or
// printed.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type providerFixture struct {
	ToolFirst bool
	Auth401   bool
	Hold      chan struct{}
	Arrived   chan struct{}
	arrived   sync.Once
	requests  atomic.Int32
	mu        sync.Mutex
	bodies    []string
}

func (p *providerFixture) count() int { return int(p.requests.Load()) }

func (p *providerFixture) body(i int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < 0 || i >= len(p.bodies) {
		return ""
	}
	return p.bodies[i]
}

func (p *providerFixture) handler(t *testing.T, marker string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		index := int(p.requests.Add(1))
		p.mu.Lock()
		p.bodies = append(p.bodies, string(body))
		p.mu.Unlock()
		if p.Auth401 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-sentinel-112-auth"}}`))
			return
		}
		if index == 1 && p.Arrived != nil {
			p.arrived.Do(func() { close(p.Arrived) })
		}
		if p.Hold != nil && index == 1 {
			<-p.Hold
		}
		var response any
		switch {
		case p.ToolFirst && index == 1:
			response = map[string]any{"id": "read", "status": "completed", "output": []any{map[string]any{
				"type": "function_call", "status": "completed", "id": "f",
				"call_id": "read-file", "name": "read", "arguments": fmt.Sprintf(`{"path":%q}`, marker+".txt"),
			}}}
		default:
			response = map[string]any{"id": "response", "status": "completed", "output": []any{map[string]any{
				"type": "message", "id": "answer", "status": "completed", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": marker + " answer"}},
			}}}
		}
		data, _ := json.Marshal(response)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

type liveRuntime struct {
	client   *runtimeClient
	ready    chan runtimeReady
	events   chan runtimeEventEnvelope
	exit     chan error
	protocol chan string
}

func startIntegrationRuntime(t *testing.T, binary, dir string, env []string) *liveRuntime {
	t.Helper()
	live := &liveRuntime{
		ready:    make(chan runtimeReady, 1),
		events:   make(chan runtimeEventEnvelope, 64),
		exit:     make(chan error, 1),
		protocol: make(chan string, 4),
	}
	client, err := startRuntime(runtimeOptions{
		Binary: binary, Dir: dir, Env: env,
		Handlers: runtimeHandlers{
			OnReady:         func(ready runtimeReady) { live.ready <- ready },
			OnEvent:         func(event runtimeEventEnvelope) { live.events <- event },
			OnExit:          func(err error) { live.exit <- err },
			OnProtocolError: func(message string) { live.protocol <- message },
			OnStderr:        func(string) {},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	live.client = client
	select {
	case ready := <-live.ready:
		if ready.Version != 1 || ready.API != "openai-responses" {
			t.Fatalf("ready = %+v", ready)
		}
	case err := <-live.protocol:
		t.Fatalf("protocol error before ready: %s", err)
	case <-time.After(10 * time.Second):
		t.Fatal("runtime did not become ready")
	}
	return live
}

func (l *liveRuntime) finishTask(t *testing.T, taskID string) []runtimeEventEnvelope {
	t.Helper()
	var events []runtimeEventEnvelope
	deadline := time.After(10 * time.Second)
	for {
		select {
		case event := <-l.events:
			events = append(events, event)
			if event.TaskID == taskID && event.Event.Type == "task_finished" {
				if event.Event.Status != "completed" {
					t.Fatalf("task %s finished as %q: %+v", taskID, event.Event.Status, event.Event.Error)
				}
				return events
			}
		case message := <-l.protocol:
			t.Fatalf("protocol error: %s", message)
		case <-deadline:
			t.Fatalf("task %s did not finish; events=%d", taskID, len(events))
		}
	}
}

func TestRuntimeProcessIntegration(t *testing.T) {
	binary := os.Getenv("SAKI_TEST_RUNTIME_BIN")
	if binary == "" {
		t.Skip("set SAKI_TEST_RUNTIME_BIN to run the real-runtime integration")
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("runtime binary: %v", err)
	}
	marker := "saki-go-integration-63"
	provider := &providerFixture{ToolFirst: true}
	server := httptest.NewServer(provider.handler(t, marker))
	defer server.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, marker+".txt"), []byte(marker+" marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(root, "sessions")
	env := append(os.Environ(),
		"API_KEY=offline-fixture",
		"SAKI_API=openai-responses",
		"SAKI_BASE_URL="+server.URL+"/v1",
		"SAKI_MODEL=fixture",
		"SAKI_CONTEXT_TOKENS=131072",
		"SAKI_SESSION_DIR="+sessionDir,
		"SAKI_SESSION=missing",
	)

	first := startIntegrationRuntime(t, binary, root, env)
	session, err := first.client.createSession(root, "integration")
	if err != nil {
		t.Fatal(err)
	}
	task, err := first.client.sendTask(session.SessionID, "Use read to inspect "+marker+".txt and report its marker.")
	if err != nil {
		t.Fatal(err)
	}
	events := first.finishTask(t, task.TaskID)
	var sawTool, sawFinal bool
	for _, event := range events {
		if event.Event.Type == "tool_finished" && strings.Contains(string(event.Event.Output), marker) {
			sawTool = true
		}
		if event.Event.Type == "response_text" && strings.Contains(event.Event.Text, marker+" answer") {
			sawFinal = true
		}
	}
	if !sawTool || !sawFinal {
		t.Fatalf("tool=%v final=%v events=%+v", sawTool, sawFinal, events)
	}
	if provider.count() != 2 {
		t.Fatalf("model requests after first task = %d", provider.count())
	}
	firstTaskID := task.TaskID
	firstMessageID := task.MessageID
	if err := first.client.shutdown(); err != nil {
		t.Fatal(err)
	}

	// New process: load/history must not replay model or tool calls.
	before := provider.count()
	second := startIntegrationRuntime(t, binary, root, env)
	loaded, err := second.client.loadSession(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Loaded || loaded.Status != "completed" {
		t.Fatalf("loaded = %+v", loaded)
	}
	history, err := second.client.historyPage(session.SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if provider.count() != before {
		t.Fatalf("load/history issued model requests: %d -> %d", before, provider.count())
	}
	var startedTask, startedMessage string
	for _, record := range history.Records {
		if record.Record.Type == "task_started" {
			startedTask, startedMessage = record.Record.TaskID, record.Record.MessageID
		}
	}
	if startedTask != firstTaskID || startedMessage != firstMessageID {
		t.Fatalf("history task/message = %q/%q", startedTask, startedMessage)
	}
	if !strings.Contains(provider.body(1), marker) {
		t.Fatal("second model request did not contain the tool result")
	}

	continuation, err := second.client.sendTask(session.SessionID, "Continue from the previous answer.")
	if err != nil {
		t.Fatal(err)
	}
	second.finishTask(t, continuation.TaskID)
	if provider.count() != before+1 {
		t.Fatalf("continuation requests = %d, want %d", provider.count(), before+1)
	}
	if !strings.Contains(provider.body(2), marker) || !strings.Contains(provider.body(2), "Continue from the previous answer") {
		t.Fatal("continuation request did not include restored context")
	}
	if err := second.client.shutdown(); err != nil {
		t.Fatal(err)
	}
}

// ---------- application-layer integration (GUI model + real runtime) ----------

type uiWaiter struct {
	cond func() bool
	ch   chan struct{}
	once sync.Once
}

type uiPump struct {
	t       *testing.T
	q       chan func()
	mu      sync.Mutex
	waiters []*uiWaiter
	done    chan struct{}
}

func newUIPump(t *testing.T, app *appModel) *uiPump {
	p := &uiPump{t: t, q: make(chan func(), 256), done: make(chan struct{})}
	app.setUIHook(func(fn func()) { p.q <- fn })
	go p.run()
	return p
}

func (p *uiPump) run() {
	for fn := range p.q {
		fn()
		p.check()
	}
	close(p.done)
}

func (p *uiPump) check() {
	p.mu.Lock()
	waiters := p.waiters
	keep := waiters[:0]
	for _, waiter := range waiters {
		if waiter.cond() {
			waiter.once.Do(func() { close(waiter.ch) })
		} else {
			keep = append(keep, waiter)
		}
	}
	p.waiters = keep
	p.mu.Unlock()
}

func (p *uiPump) do(fn func()) {
	done := make(chan struct{})
	p.q <- func() {
		fn()
		close(done)
	}
	<-done
}

func (p *uiPump) wait(cond func() bool) {
	p.waitTimeout(cond, 15*time.Second)
}

func (p *uiPump) waitTimeout(cond func() bool, timeout time.Duration) {
	waiter := &uiWaiter{cond: cond, ch: make(chan struct{})}
	p.mu.Lock()
	p.waiters = append(p.waiters, waiter)
	p.mu.Unlock()
	p.q <- func() {} // trigger a check for an already-true condition
	select {
	case <-waiter.ch:
	case <-time.After(timeout):
		p.t.Fatal("timed out waiting for GUI runtime state")
	}
}

func (p *uiPump) close() {
	close(p.q)
	<-p.done
}

type guiHarness struct {
	app      *appModel
	pump     *uiPump
	provider *providerFixture
	root     string
	closed   bool
}

func requireRuntimeBinary(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("SAKI_TEST_RUNTIME_BIN")
	if binary == "" {
		t.Skip("set SAKI_TEST_RUNTIME_BIN to run the real-runtime integration")
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("runtime binary: %v", err)
	}
	return binary
}

func startGUIHarness(t *testing.T, binary string, provider *providerFixture, marker string) *guiHarness {
	t.Helper()
	server := httptest.NewServer(provider.handler(t, marker))
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, marker+".txt"), []byte(marker+"-file-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAKI_GUI_STORE", filepath.Join(root, "gui", "contacts.json"))
	t.Setenv("SAKI_GUI_WORKDIR", root)
	t.Setenv("SAKI_SESSION_DIR", filepath.Join(root, "sessions"))
	t.Setenv("SAKI_RUNTIME_BIN", binary)
	t.Setenv("API_KEY", "offline-fixture")
	t.Setenv("SAKI_API", "openai-responses")
	t.Setenv("SAKI_BASE_URL", server.URL+"/v1")
	t.Setenv("SAKI_MODEL", "fixture")
	t.Setenv("SAKI_CONTEXT_TOKENS", "131072")
	app, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	pump := newUIPump(t, app)
	h := &guiHarness{app: app, pump: pump, provider: provider, root: root}
	app.startRuntime()
	pump.wait(func() bool { return app.runtimeStatus == runtimeStateReady })
	t.Cleanup(func() {
		server.Close()
		if h.closed {
			return
		}
		h.shutdown(t)
	})
	return h
}

func restartGUIHarness(t *testing.T, binary string) *guiHarness {
	t.Helper()
	app, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	pump := newUIPump(t, app)
	h := &guiHarness{app: app, pump: pump}
	app.startRuntime()
	pump.wait(func() bool { return app.runtimeStatus == runtimeStateReady })
	return h
}

func (h *guiHarness) shutdown(t *testing.T) {
	t.Helper()
	if h.closed {
		return
	}
	h.pump.do(func() { h.app.beginShutdown() })
	h.pump.waitTimeout(func() bool {
		if !h.app.quitReady {
			return false
		}
		return h.app.rt == nil || h.app.runtimeStatus == runtimeStateExited
	}, 15*time.Second)
	h.pump.close()
	h.closed = true
}

func (h *guiHarness) snapshot(fn func()) {
	h.pump.do(fn)
}

func TestGUIShutdownAfterManualBeginIsIdempotent(t *testing.T) {
	app := &appModel{}
	pump := newUIPump(t, app)
	h := &guiHarness{app: app, pump: pump}
	h.pump.do(func() { h.app.beginShutdown() })
	h.shutdown(t)
	h.shutdown(t)
	if !h.closed {
		t.Fatal("idempotent harness shutdown did not finish")
	}
}

func TestGUIAppRuntimeFlow(t *testing.T) {
	binary := requireRuntimeBinary(t)
	marker := "saki-app-flow-63"
	provider := &providerFixture{ToolFirst: true}
	h := startGUIHarness(t, binary, provider, marker)

	// Add B with a draft, then switch back to A and send from the GUI path.
	h.pump.do(func() {
		h.app.addDefaultContact()
		b := h.app.selectedContact()
		b.Draft = "draft for B"
		h.app.draftChanged(b)
	})
	h.pump.do(func() { h.app.selectContact(0) })
	var sessionBefore string
	var purposeBefore string
	h.pump.do(func() {
		ct := h.app.contacts[0]
		sessionBefore = ct.SessionID
		purposeBefore = ct.Purpose
		ct.Draft = "hello A"
		h.app.sendCurrent()
	})
	if sessionBefore != "" || purposeBefore != "" {
		t.Fatalf("zero-profile contact should start unbound/empty: session=%q purpose=%q", sessionBefore, purposeBefore)
	}
	// Switch to B while A is running; A's events must not leak into B.
	h.pump.do(func() { h.app.selectContact(1) })
	h.pump.wait(func() bool {
		ct := h.app.contacts[0]
		return ct.LastTaskID != "" && ct.ActiveTaskID == "" && ct.SendState == sendIdle && !ct.HistoryDirty &&
			len(ct.Messages) > 0 && strings.Contains(ct.Messages[len(ct.Messages)-1].Text, marker+" answer")
	})
	var aSession, aDraft, bDraft string
	var aMessages, bMessages int
	var aProfile string
	h.pump.do(func() {
		a := h.app.contacts[0]
		b := h.app.contacts[1]
		aSession = a.SessionID
		aDraft = a.Draft
		bDraft = b.Draft
		aMessages = len(a.Messages)
		bMessages = len(b.Messages)
		aProfile = a.Purpose
	})
	if aSession == "" || aDraft != "" || bDraft != "draft for B" {
		t.Fatalf("session/drafts: session=%q a=%q b=%q", aSession, aDraft, bDraft)
	}
	if bMessages != 0 || aMessages == 0 {
		t.Fatalf("event routing: A=%d B=%d", aMessages, bMessages)
	}
	var sawToolResult bool
	h.pump.do(func() {
		for _, w := range h.app.contacts[0].Works {
			for _, call := range w.Tools {
				if strings.Contains(call.Output, marker+"-file-content") {
					sawToolResult = true
				}
			}
		}
	})
	if !sawToolResult {
		t.Fatal("GUI tool result did not contain the actual probe file content")
	}
	if aProfile != "" {
		t.Fatalf("zero-profile send changed purpose: %q", aProfile)
	}

	// Bound profile edits update future defaults; they must not create/send or
	// replace the current conversation, draft or history.
	before := provider.count()
	futureWorkspace := h.root
	h.pump.do(func() {
		h.app.selectContact(0)
		ct := h.app.contacts[0]
		ct.Draft = "keep after restart"
		h.app.draftChanged(ct)
		h.app.openSettings()
		draft := h.app.contactProfile.Draft
		draft.Name = "Renamed"
		draft.Purpose = "future purpose"
		draft.Instructions = "future instructions"
		draft.Workspace = futureWorkspace
		if !h.app.saveContactProfileDraft(draft, false) {
			t.Errorf("profile save failed: %s", h.app.contactProfile.Error)
		}
	})
	var name, sessionAfter, draftAfter, purposeAfter, workspaceAfter string
	var messagesAfter int
	h.pump.do(func() {
		ct := h.app.contacts[0]
		name, sessionAfter, draftAfter, purposeAfter, workspaceAfter, messagesAfter = ct.Name, ct.SessionID, ct.Draft, ct.Purpose, ct.Workspace, len(ct.Messages)
	})
	if name != "Renamed" || sessionAfter != aSession || purposeAfter != "future purpose" || workspaceAfter != futureWorkspace || draftAfter != "keep after restart" || messagesAfter != aMessages {
		t.Fatalf("profile edit changed the current session: name=%q session=%q purpose=%q workspace=%q draft=%q messages=%d", name, sessionAfter, purposeAfter, workspaceAfter, draftAfter, messagesAfter)
	}
	if provider.count() != before {
		t.Fatalf("settings save issued runtime work: %d -> %d", before, provider.count())
	}

	h.shutdown(t)
	// New app process: restore same store/session without model calls.
	beforeRestart := provider.count()
	h2 := restartGUIHarness(t, binary)
	defer func() {
		if !h2.closed {
			h2.shutdown(t)
		}
	}()
	h2.pump.wait(func() bool {
		ct := h2.app.contacts[0]
		return ct.Loaded && len(ct.Messages) > 0 && len(ct.Works) > 0 && ct.SessionModel != ""
	})
	// Keep the #112 restore contract: compare the loaded state/metadata with
	// a fresh Runtime response, not only the work projection.
	runtimeTruth, err := h2.app.rt.loadSession(aSession)
	if err != nil {
		t.Fatal(err)
	}
	expectedRoot, err := filepath.EvalSymlinks(h.root)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeTruth.Metadata == nil || runtimeTruth.State != "idle" ||
		runtimeTruth.Metadata.Model != "fixture" || runtimeTruth.Metadata.API != "openai-responses" ||
		runtimeTruth.Metadata.WorkingDirectory != expectedRoot {
		t.Fatalf("unexpected runtime session truth: state=%q metadata=%+v root=%q", runtimeTruth.State, runtimeTruth.Metadata, expectedRoot)
	}
	var restoredSession, restoredName, restoredPurpose, restoredDraft, restoredBDraft string
	var restoredState, restoredModel, restoredAPI, restoredWorkingDir string
	h2.snapshot(func() {
		ct := h2.app.contacts[0]
		restoredSession, restoredName, restoredPurpose, restoredDraft = ct.SessionID, ct.Name, ct.Purpose, ct.Draft
		restoredState, restoredModel, restoredAPI, restoredWorkingDir = ct.SessionState, ct.SessionModel, ct.SessionAPI, ct.SessionWorkingDir
		restoredBDraft = h2.app.contacts[1].Draft
	})
	if restoredState != runtimeTruth.State || restoredModel != runtimeTruth.Metadata.Model ||
		restoredAPI != runtimeTruth.Metadata.API || restoredWorkingDir != runtimeTruth.Metadata.WorkingDirectory {
		t.Fatalf("restored metadata/state = state %q model %q api %q cwd %q; runtime = %+v",
			restoredState, restoredModel, restoredAPI, restoredWorkingDir, runtimeTruth)
	}
	if restoredSession != aSession || restoredName != "Renamed" || restoredPurpose != "future purpose" || restoredDraft != "keep after restart" {
		t.Fatalf("restored contact: session=%q name=%q purpose=%q draft=%q", restoredSession, restoredName, restoredPurpose, restoredDraft)
	}
	if restoredBDraft != "draft for B" {
		t.Fatalf("B draft = %q", restoredBDraft)
	}
	var restoredWorkTask, restoredTool string
	h2.snapshot(func() {
		if w := h2.app.contacts[0].recentWork(); w != nil {
			restoredWorkTask = w.TaskID
			for _, call := range w.Tools {
				if strings.Contains(call.Output, marker+"-file-content") {
					restoredTool = call.Output
				}
			}
		}
	})
	if restoredWorkTask == "" || restoredTool == "" {
		t.Fatalf("restored work/tool = task %q output %q", restoredWorkTask, restoredTool)
	}
	if h2.app.contacts[1].recentWork() != nil {
		t.Fatal("work from contact A leaked into contact B on restore")
	}
	if provider.count() != beforeRestart {
		t.Fatalf("restore issued model requests: %d -> %d", beforeRestart, provider.count())
	}

	h2.pump.do(func() {
		ct := h2.app.contacts[0]
		ct.Draft = "continue after restart"
		h2.app.sendCurrent()
	})
	h2.pump.wait(func() bool {
		ct := h2.app.contacts[0]
		return ct.LastTaskID != "" && ct.ActiveTaskID == "" && ct.SendState == sendIdle
	})
	if provider.count() != beforeRestart+1 {
		t.Fatalf("continuation requests = %d, want %d", provider.count(), beforeRestart+1)
	}
	lastBody := provider.body(provider.count() - 1)
	if !strings.Contains(lastBody, "continue after restart") || !strings.Contains(lastBody, marker+" answer") {
		t.Fatal("continuation request did not include restored context")
	}
	h2.shutdown(t)
}

func TestGUIAppProviderAuthHistoryRedaction(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{Auth401: true}
	h := startGUIHarness(t, binary, provider, "saki-auth-boundary-112")

	h.pump.do(func() {
		ct := h.app.contacts[0]
		ct.Draft = "accepted-auth-boundary-check"
		h.app.sendCurrent()
		ct.Draft = "later-unsent-after-auth"
	})
	h.pump.wait(func() bool {
		ct := h.app.contacts[0]
		return ct.ActiveTaskID == "" && ct.SendState == sendIdle && ct.Loaded &&
			!ct.HistoryDirty && strings.Contains(ct.StatusDetail, "Provider authentication failed")
	})
	var detail, draft string
	h.pump.do(func() {
		ct := h.app.contacts[0]
		detail, draft = ct.StatusDetail, ct.Draft
	})
	if !strings.Contains(detail, "HTTP 401") || strings.Contains(detail, "sk-sentinel-112-auth") ||
		strings.Contains(detail, "Incorrect API key") {
		t.Fatalf("history status detail echoed provider body: %q", detail)
	}
	var workDetail string
	h.pump.do(func() {
		if w := h.app.contacts[0].recentWork(); w != nil {
			workDetail = w.Detail
		}
	})
	if strings.Contains(workDetail, "sk-sentinel-112-auth") || strings.Contains(workDetail, "Incorrect API key") {
		t.Fatalf("Work.Detail echoed provider body: %q", workDetail)
	}
	if draft != "later-unsent-after-auth" {
		t.Fatalf("later draft = %q", draft)
	}
	h.pump.do(func() {
		found := false
		for _, m := range h.app.contacts[0].Messages {
			if m.Kind == msgUser && m.Text == "accepted-auth-boundary-check" {
				found = true
			}
		}
		if !found {
			t.Fatalf("accepted auth task history was lost: %+v", h.app.contacts[0].Messages)
		}
	})
	tr := ui.NewTester(h.app.view, 1040, 720)
	tr.Frame()
	if strings.Contains(strings.Join(tr.Texts(), "\n"), "sk-sentinel-112-auth") {
		t.Fatal("rendered UI leaked the provider body")
	}
	if provider.count() != 1 {
		t.Fatalf("initial auth requests = %d, want 1", provider.count())
	}

	h.shutdown(t)
	beforeRestart := provider.count()
	h2 := restartGUIHarness(t, binary)
	defer func() {
		if !h2.closed {
			h2.shutdown(t)
		}
	}()
	h2.pump.wait(func() bool {
		ct := h2.app.contacts[0]
		return ct.Loaded && strings.Contains(ct.StatusDetail, "Provider authentication failed")
	})
	var restoredDetail, restoredDraft string
	h2.snapshot(func() {
		ct := h2.app.contacts[0]
		restoredDetail, restoredDraft = ct.StatusDetail, ct.Draft
	})
	if strings.Contains(restoredDetail, "sk-sentinel-112-auth") || strings.Contains(restoredDetail, "Incorrect API key") {
		t.Fatalf("restart history leaked provider body: %q", restoredDetail)
	}
	var restoredWorkDetail string
	h2.snapshot(func() {
		if w := h2.app.contacts[0].recentWork(); w != nil {
			restoredWorkDetail = w.Detail
		}
	})
	if strings.Contains(restoredWorkDetail, "sk-sentinel-112-auth") || strings.Contains(restoredWorkDetail, "Incorrect API key") {
		t.Fatalf("restart Work.Detail leaked provider body: %q", restoredWorkDetail)
	}
	if restoredDraft != "later-unsent-after-auth" {
		t.Fatalf("restored later draft = %q", restoredDraft)
	}
	h2.snapshot(func() {
		found := false
		for _, m := range h2.app.contacts[0].Messages {
			if m.Kind == msgUser && m.Text == "accepted-auth-boundary-check" {
				found = true
			}
		}
		if !found {
			t.Fatalf("restored auth history lost accepted message: %+v", h2.app.contacts[0].Messages)
		}
	})
	if provider.count() != beforeRestart {
		t.Fatalf("restart issued model requests: %d -> %d", beforeRestart, provider.count())
	}
}

func TestGUIAppStopSurvivesHistoryReload(t *testing.T) {
	binary := requireRuntimeBinary(t)
	hold := make(chan struct{})
	defer close(hold)
	arrived := make(chan struct{})
	provider := &providerFixture{ToolFirst: true, Hold: hold, Arrived: arrived}
	h := startGUIHarness(t, binary, provider, "saki-130-stop")
	h.pump.do(func() {
		ct := h.app.contacts[0]
		ct.Draft = "stop-input"
		h.app.sendCurrent()
	})
	select {
	case <-arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("controlled provider did not receive the first request")
	}
	// The socket count is external to the UI queue; wait on the UI event that
	// establishes the accepted task, then cancel in the same UI callback.
	h.pump.wait(func() bool {
		return h.app.contacts[0].ActiveTaskID != ""
	})
	stoppingNow := false
	h.pump.do(func() {
		ct := h.app.contacts[0]
		ct.Draft = "later-stop-draft"
		h.app.cancelSelected()
		w := ct.activeWork()
		stoppingNow = w != nil && w.State == workStopping
	})
	if !stoppingNow {
		t.Fatal("cancelSelected did not expose the immediate stopping state on the UI queue")
	}
	h.pump.wait(func() bool {
		ct := h.app.contacts[0]
		return ct.ActiveTaskID == "" && ct.SendState == sendIdle && !ct.HistoryDirty &&
			ct.recentWork() != nil && ct.Draft == "later-stop-draft"
	})
	var label string
	h.pump.do(func() {
		if w := h.app.contacts[0].recentWork(); w != nil {
			label = workStateLabel(w)
		}
	})
	if !strings.Contains(label, "Stopped") {
		t.Fatalf("stopped label after auto reload = %q", label)
	}
	h.shutdown(t)
	h2 := restartGUIHarness(t, binary)
	defer func() {
		if !h2.closed {
			h2.shutdown(t)
		}
	}()
	h2.pump.wait(func() bool {
		ct := h2.app.contacts[0]
		return ct.Loaded && ct.recentWork() != nil
	})
	h2.snapshot(func() {
		ct := h2.app.contacts[0]
		w := ct.recentWork()
		if w == nil || workStateLabel(w) != "Interrupted" {
			t.Fatalf("restart stop reason was invented: work=%+v label=%q", w, func() string {
				if w == nil {
					return ""
				}
				return workStateLabel(w)
			}())
		}
		if ct.Draft != "later-stop-draft" {
			t.Fatalf("restart stop draft = %q", ct.Draft)
		}
	})
}

func TestGUIAppIssueExecutionsPersistAndRestart(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{ToolFirst: false}
	h := startGUIHarness(t, binary, provider, "saki-issue-141")
	issueID := ""
	h.pump.do(func() {
		if !h.app.createIssue("Implement login", "Must support SMS verification.") {
			t.Fatalf("createIssue failed: %s", h.app.issueFormError)
		}
		ct := h.app.selectedContact()
		issueID = ct.SelectedIssueID
		if issueID == "" || len(ct.Issues) != 1 || ct.Issues[0].Goal != "Implement login" {
			t.Fatalf("issue not created/selected: %+v", ct)
		}
		ct.Draft = "ordinary composer draft"
	})
	run := func() {
		h.pump.do(func() {
			if !h.app.runIssue(issueID) {
				t.Fatalf("runIssue failed: %s", h.app.issueFormError)
			}
		})
		h.pump.wait(func() bool {
			ct := h.app.selectedContact()
			if len(ct.Issues) != 1 {
				return false
			}
			issue := ct.Issues[0]
			exec := issue.Executions
			return issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
				issue.Work.Loaded && ct.ActiveTaskID == "" && ct.SendState == sendIdle &&
				len(exec) > 0 && exec[len(exec)-1].Status == issueExecutionCompleted
		})
	}
	run()
	var prompt string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		prompt = issuePrompt(ct.Issues[0])
		ct.Draft = prompt
	})
	run()
	var issue *productIssue
	var draftAfterRun, workSession, conversationSession string
	var executionSessions []string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		issue = ct.Issues[0]
		draftAfterRun = ct.Draft
		workSession, conversationSession = issue.WorkSessionID, ct.SessionID
		for _, execution := range issue.Executions {
			executionSessions = append(executionSessions, execution.SessionID)
		}
	})
	if len(issue.Executions) != 2 {
		t.Fatalf("executions = %+v", issue.Executions)
	}
	if issue.Executions[0].TaskID == issue.Executions[1].TaskID ||
		issue.Executions[0].SessionID == "" || issue.Executions[1].MessageID == "" {
		t.Fatalf("execution associations = %+v", issue.Executions)
	}
	if issue.Status != issueStatusInProgress {
		t.Fatalf("runtime completion changed issue status: %q", issue.Status)
	}
	if !strings.Contains(issueEvidenceText(issue), "Completion not verified") {
		t.Fatalf("completion evidence = %q", issueEvidenceText(issue))
	}
	if draftAfterRun != prompt {
		t.Fatalf("issue run consumed a composer draft equal to its prompt: %q", draftAfterRun)
	}
	if workSession == "" || workSession == conversationSession {
		t.Fatalf("issue work session was not independent: work=%q conversation=%q", workSession, conversationSession)
	}
	for _, sessionID := range executionSessions {
		if sessionID != workSession {
			t.Fatalf("execution session mapping = %v / work=%q", executionSessions, workSession)
		}
	}

	if provider.count() != 2 {
		t.Fatalf("issue runs made %d model requests, want 2", provider.count())
	}
	h.shutdown(t)
	beforeRestart := provider.count()
	h2 := restartGUIHarness(t, binary)
	defer func() {
		if !h2.closed {
			h2.shutdown(t)
		}
	}()
	h2.pump.wait(func() bool {
		ct := h2.app.contacts[0]
		return len(ct.Issues) == 1 && ct.Issues[0].Work.Loaded && len(ct.Issues[0].Executions) == 2
	})
	var restoredIssue *productIssue
	var restoredDraft string
	h2.snapshot(func() {
		ct := h2.app.contacts[0]
		restoredIssue = ct.Issues[0]
		restoredDraft = ct.Draft
	})
	if restoredIssue.ID != issueID || restoredIssue.Status != issueStatusInProgress || len(restoredIssue.Executions) != 2 {
		t.Fatalf("restored issue = %+v", restoredIssue)
	}
	if restoredIssue.Executions[0].TaskID == "" || restoredIssue.Executions[1].TaskID == "" {
		t.Fatalf("restored execution IDs = %+v", restoredIssue.Executions)
	}
	if restoredDraft != prompt {
		t.Fatalf("restart issue draft = %q", restoredDraft)
	}

	if provider.count() != beforeRestart {
		t.Fatalf("restart issued model requests: %d -> %d", beforeRestart, provider.count())
	}
}

func TestGUIAppIssueSaveFailureStopsBeforeDispatch(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{ToolFirst: false}
	h := startGUIHarness(t, binary, provider, "saki-issue-save-failure-141")
	defer func() {
		if !h.closed {
			h.shutdown(t)
		}
	}()
	var issueID string
	h.pump.do(func() {
		if !h.app.createIssue("Implement login", "Must use SMS.") {
			t.Fatalf("createIssue failed: %s", h.app.issueFormError)
		}
		issueID = h.app.selectedContact().SelectedIssueID
		ct := h.app.selectedContact()
		ct.Draft = "ordinary draft"
	})
	badParent := filepath.Join(h.root, "not-a-directory")
	if err := os.WriteFile(badParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.app.store.path = filepath.Join(badParent, "contacts.json")
	h.pump.do(func() {
		if h.app.runIssue(issueID) {
			t.Fatal("runIssue unexpectedly dispatched with failing store")
		}
	})
	ct := h.app.selectedContact()
	if len(ct.Issues[0].Executions) != 0 || ct.Issues[0].Status != issueStatusNotStarted {
		t.Fatalf("failed pre-save issue state = %+v", ct.Issues[0])
	}
	if ct.Draft != "ordinary draft" || provider.count() != 0 {
		t.Fatalf("pre-save failure draft/requests = %q/%d", ct.Draft, provider.count())
	}
	if !strings.Contains(h.app.issueFormError, "save") {
		t.Fatalf("issue save error = %q", h.app.issueFormError)
	}
}

func TestGUIAppIssueStopKeepsItemAndExecution(t *testing.T) {
	binary := requireRuntimeBinary(t)
	hold := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hold) }) }
	defer release()
	arrived := make(chan struct{})
	provider := &providerFixture{ToolFirst: true, Hold: hold, Arrived: arrived}
	h := startGUIHarness(t, binary, provider, "saki-issue-stop-141")
	var issueID string
	h.pump.do(func() {
		if !h.app.createIssue("Stop test", "Keep item after stop.") {
			t.Fatalf("createIssue failed: %s", h.app.issueFormError)
		}
		issueID = h.app.selectedContact().SelectedIssueID
		if !h.app.runIssue(issueID) {
			t.Fatalf("runIssue failed: %s", h.app.issueFormError)
		}
	})
	select {
	case <-arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("issue provider did not receive first request")
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID != ""
	})
	stopping := false
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "later issue draft"
		h.app.cancelSelected()
		issue := ct.issueByID(issueID)
		w := workFor(ct, issue.Work.ActiveTaskID)
		stopping = w != nil && w.State == workStopping
	})
	if !stopping {
		release()
		t.Fatal("cancel did not expose stopping")
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		if ct.ActiveTaskID != "" || ct.SendState != sendIdle {
			return false
		}
		issue := ct.issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionInterrupted
	})
	ct := h.app.selectedContact()
	issue := ct.issueByID(issueID)
	if issue == nil || issue.Status != issueStatusInProgress || ct.Draft != "later issue draft" {
		release()
		t.Fatalf("stop issue/draft = %+v / %q", issue, ct.Draft)
	}
	release()
	h.shutdown(t)
}

func TestGUIFirstCreateUsesProfileSnapshot(t *testing.T) {
	binary := requireRuntimeBinary(t)
	marker := "saki-profile-snapshot-63"
	provider := &providerFixture{ToolFirst: false}
	h := startGUIHarness(t, binary, provider, marker)
	defer func() {
		if !h.closed {
			h.shutdown(t)
		}
	}()

	h.pump.do(func() {
		h.app.addDefaultContact()
		ct := h.app.selectedContact()
		h.app.openSettings()
		draft := h.app.contactProfile.Draft
		draft.Name = "Writer"
		draft.Purpose = "B purpose"
		draft.Instructions = "B instructions"
		draft.Workspace = h.root
		if !h.app.saveContactProfileDraft(draft, false) {
			t.Errorf("profile save failed: %s", h.app.contactProfile.Error)
		}
		ct.Draft = "hello profile"
		h.app.sendCurrent()
	})
	h.pump.wait(func() bool {
		ct := h.app.contacts[1]
		return ct.LastTaskID != "" && ct.ActiveTaskID == "" && ct.SendState == sendIdle
	})
	var session string
	h.pump.do(func() { session = h.app.contacts[1].SessionID })
	if session == "" {
		t.Fatal("first create did not bind a session")
	}
	firstBody := provider.body(0)
	if !strings.Contains(firstBody, "Purpose: B purpose") || !strings.Contains(firstBody, "Instructions: B instructions") {
		t.Fatalf("first create did not use the profile snapshot: %s", firstBody)
	}
	h.shutdown(t)
}

// TestGUIOnlineSmoke is the real-provider application-layer smoke. It is
// opt-in and never substitutes the controlled fixture:
//
//	SAKI_TEST_ONLINE=1 SAKI_TEST_ONLINE_WORKDIR=/path/to/repo \
//	SAKI_TEST_RUNTIME_BIN=/abs/path/to/Saki \
//	go test -run TestGUIOnlineSmoke -count=1 .
//
// The directory must contain the host's .env; no value is read or printed by
// the test. The verified host combination uses Chat Completions.
func TestGUIOnlineSmoke(t *testing.T) {
	if os.Getenv("SAKI_TEST_ONLINE") != "1" {
		t.Skip("set SAKI_TEST_ONLINE=1 for the online provider smoke")
	}
	binary := requireRuntimeBinary(t)
	workdir := os.Getenv("SAKI_TEST_ONLINE_WORKDIR")
	if workdir == "" {
		wd, _ := os.Getwd()
		workdir = filepath.Dir(wd)
	}
	if _, err := os.Stat(filepath.Join(workdir, ".env")); err != nil {
		t.Fatalf("online workdir %s has no .env: %v", workdir, err)
	}
	root := t.TempDir()
	marker := fmt.Sprintf("saki-online-%d", time.Now().UnixNano())
	if err := os.Symlink(filepath.Join(workdir, ".env"), filepath.Join(root, ".env")); err != nil {
		t.Fatalf("link host .env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "probe.txt"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAKI_GUI_STORE", filepath.Join(root, "gui", "contacts.json"))
	t.Setenv("SAKI_SESSION_DIR", filepath.Join(root, "sessions"))
	t.Setenv("SAKI_GUI_WORKDIR", root)
	t.Setenv("SAKI_RUNTIME_BIN", binary)
	api := os.Getenv("SAKI_TEST_ONLINE_API")
	if api == "" {
		api = "openai-chat-completions"
	}
	t.Setenv("SAKI_API", api)

	app, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	pump := newUIPump(t, app)
	h := &guiHarness{app: app, pump: pump, root: root}
	defer func() {
		if !h.closed {
			h.shutdown(t)
		}
	}()
	app.startRuntime()
	pump.waitTimeout(func() bool { return app.runtimeStatus == runtimeStateReady }, 30*time.Second)
	pump.do(func() {
		ct := app.contacts[0]
		ct.Draft = "Use read to inspect probe.txt and report its exact contents. Do not write files."
		app.sendCurrent()
	})
	pump.waitTimeout(func() bool {
		ct := app.contacts[0]
		return ct.LastTaskID != "" && ct.ActiveTaskID == "" && ct.SendState == sendIdle && !ct.HistoryDirty
	}, 180*time.Second)

	var status contactStatus
	var detail, taskID string
	var sawTool, sawFinal bool
	pump.do(func() {
		ct := app.contacts[0]
		status, detail, taskID = ct.Status, ct.StatusDetail, ct.LastTaskID
		for _, work := range ct.Works {
			if work.TaskID != taskID {
				continue
			}
			for _, tool := range work.Tools {
				if tool.Finished && strings.Contains(tool.Output, marker) {
					sawTool = true
				}
			}
		}
		for _, m := range ct.Messages {
			if m.Kind == msgContact && strings.Contains(m.Text, marker) && m.ToolName == "" {
				sawFinal = true
			}
		}
	})
	if status != statusReady || detail != "" {
		t.Fatalf("online task status=%v detail=%q", status, detail)
	}
	if !sawTool || !sawFinal {
		t.Fatalf("online tool=%v final=%v", sawTool, sawFinal)
	}
	h.shutdown(t)
}

// TestGUIAppPersistsMappingBeforeSend pauses the asynchronous mapping callback,
// proves no task was sent, then saves the mapping and lets exactly one task run.
func TestGUIAppPersistsMappingBeforeSend(t *testing.T) {
	binary := requireRuntimeBinary(t)
	marker := "saki-mapping-before-send-63"
	provider := &providerFixture{ToolFirst: true}
	h := startGUIHarness(t, binary, provider, marker)
	defer func() {
		if !h.closed {
			h.shutdown(t)
		}
	}()

	manual := make(chan func(), 8)
	var pumpHook func(func())
	h.pump.do(func() {
		pumpHook = h.app.getUIHook()
		h.app.setUIHook(func(fn func()) { manual <- fn })
		ct := h.app.contacts[0]
		ct.Draft = "mapping ordering"
		h.app.sendCurrent()
	})
	var mapping func()
	select {
	case mapping = <-manual:
	case <-time.After(15 * time.Second):
		t.Fatal("session mapping callback did not arrive")
	}
	if got := provider.count(); got != 0 {
		t.Fatalf("task reached provider before mapping persisted: %d", got)
	}
	h.pump.do(func() { h.app.setUIHook(pumpHook) })
	h.pump.do(mapping) // set SessionID + persist, then unblock the worker
	h.pump.wait(func() bool {
		ct := h.app.contacts[0]
		return ct.LastTaskID != "" && ct.ActiveTaskID == "" && ct.SendState == sendIdle && !ct.HistoryDirty
	})
	if got := provider.count(); got != 2 {
		t.Fatalf("one task should make read+final requests, got %d", got)
	}
	var session string
	h.pump.do(func() { session = h.app.contacts[0].SessionID })
	if session == "" {
		t.Fatal("session mapping was not set")
	}
	store, err := openContactStore(h.app.store.path, h.root)
	if err != nil {
		t.Fatal(err)
	}
	if store.state.Contacts[0].SessionID != session {
		t.Fatalf("persisted session = %q, want %q", store.state.Contacts[0].SessionID, session)
	}
	h.shutdown(t)
}

// TestGUIAppSaveFailureStopsBeforeSend makes the store unwritable while the
// mapping callback is paused. The mapping save must fail, no task may reach
// the provider, and the draft/error must remain visible.
func TestGUIAppSaveFailureStopsBeforeSend(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{ToolFirst: true}
	h := startGUIHarness(t, binary, provider, "saki-save-failure-63")
	defer func() {
		if !h.closed {
			h.shutdown(t)
		}
	}()

	manual := make(chan func(), 8)
	var pumpHook func(func())
	h.pump.do(func() {
		pumpHook = h.app.getUIHook()
		h.app.setUIHook(func(fn func()) { manual <- fn })
		ct := h.app.contacts[0]
		ct.Draft = "keep draft"
		h.app.sendCurrent()
	})
	var mapping func()
	select {
	case mapping = <-manual:
	case <-time.After(15 * time.Second):
		t.Fatal("session mapping callback did not arrive")
	}
	if got := provider.count(); got != 0 {
		t.Fatalf("task reached provider before mapping persisted: %d", got)
	}
	badParent := filepath.Join(h.root, "not-a-directory")
	if err := os.WriteFile(badParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.app.store.path = filepath.Join(badParent, "contacts.json")
	h.pump.do(func() { h.app.setUIHook(pumpHook) })
	h.pump.do(mapping)
	h.pump.wait(func() bool {
		ct := h.app.contacts[0]
		return ct.SendState == sendIdle && strings.Contains(ct.SendError, "save")
	})
	var draft, errorText, active string
	var messages int
	h.pump.do(func() {
		ct := h.app.contacts[0]
		draft, errorText, active, messages = ct.Draft, ct.SendError, ct.ActiveTaskID, len(ct.Messages)
	})
	if provider.count() != 0 {
		t.Fatalf("model/tool calls after save failure: %d", provider.count())
	}
	if draft != "keep draft" || active != "" || messages != 0 || errorText == "" {
		t.Fatalf("state after save failure: draft=%q active=%q messages=%d error=%q", draft, active, messages, errorText)
	}

	// A send on the now-bound contact must also stop before task.send when the
	// contact/draft save fails.
	h.pump.do(func() {
		ct := h.app.contacts[0]
		ct.Draft = "second draft"
		h.app.sendCurrent()
	})
	h.pump.do(func() { errorText = h.app.contacts[0].SendError })
	if provider.count() != 0 || !strings.Contains(errorText, "save") {
		t.Fatalf("bound send after save failure: requests=%d error=%q", provider.count(), errorText)
	}
	h.shutdown(t)
}

func TestGUIAppRejectedComposerSendKeepsInFlightIssueAssociation(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{ToolFirst: false}
	h := startGUIHarness(t, binary, provider, "saki-issue-inflight-141")
	defer func() {
		if !h.closed {
			h.shutdown(t)
		}
	}()
	manual := make(chan func(), 8)
	var issueID, executionID string
	h.pump.do(func() {
		if !h.app.createIssue("In-flight issue", "Keep the association while mapping is paused.") {
			t.Fatalf("createIssue failed: %s", h.app.issueFormError)
		}
		ct := h.app.selectedContact()
		issueID = ct.SelectedIssueID
		ct.Draft = "ordinary composer attempt"
		h.app.setUIHook(func(fn func()) { manual <- fn })
		if !h.app.runIssue(issueID) {
			t.Fatalf("runIssue failed: %s", h.app.issueFormError)
		}
	})
	var mapping func()
	select {
	case mapping = <-manual:
	case <-time.After(15 * time.Second):
		t.Fatal("session mapping callback did not arrive")
	}
	if provider.count() != 0 {
		t.Fatalf("provider received a task before mapping persisted: %d", provider.count())
	}
	var pending string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if len(ct.Issues) != 1 || len(ct.Issues[0].Executions) != 1 {
			t.Fatalf("in-flight issue state = %+v", ct.Issues)
		}
		executionID = ct.Issues[0].Executions[0].ID
		if !h.app.sendCurrent() {
			// expected: the in-flight task rejects a normal composer send
		}
		pending = ct.Issues[0].Work.pendingExecutionID
	})
	if pending != executionID {
		t.Fatalf("rejected normal send consumed in-flight association: pending=%q want=%q", pending, executionID)
	}
	h.pump.do(func() {
		h.app.setUIHook(func(fn func()) { h.pump.q <- fn })
		mapping()
	})
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		if len(ct.Issues) != 1 || len(ct.Issues[0].Executions) != 1 {
			return false
		}
		issue := ct.Issues[0]
		return issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle && issue.Work.Loaded &&
			ct.ActiveTaskID == "" && ct.SendState == sendIdle &&
			issue.Executions[0].Status == issueExecutionCompleted
	})
	ct := h.app.selectedContact()
	execution := ct.Issues[0].Executions[0]
	if execution.TaskID == "" || ct.Issues[0].Work.pendingExecutionID != "" || execution.ID != executionID {
		t.Fatalf("accepted task lost its issue attribution: %+v pending=%q", execution, ct.Issues[0].Work.pendingExecutionID)
	}
	if len(ct.Issues[0].Executions) != 1 || ct.Issues[0].Status != issueStatusInProgress || ct.Draft != "ordinary composer attempt" {
		t.Fatalf("issue/draft after in-flight rejection = %+v / %q", ct.Issues[0], ct.Draft)
	}
	if provider.count() != 1 {
		t.Fatalf("in-flight issue produced %d provider requests, want 1", provider.count())
	}
	h.shutdown(t)
}

func TestGUIShutdownWaitsForFinalOwnerCallback(t *testing.T) {
	for _, firstIndex := range []int{0, 1} {
		firstIndex := firstIndex
		t.Run(fmt.Sprintf("execute_collected_%d_first", firstIndex+1), func(t *testing.T) {
			binary := requireRuntimeBinary(t)
			h := startGUIHarness(t, binary, &providerFixture{ToolFirst: false}, fmt.Sprintf("shutdown-barrier-%d", firstIndex))
			h.pump.wait(func() bool {
				return h.app.runtimeStatus == runtimeStateReady && h.app.runtimeCapabilitiesLoaded
			})
			callbacks := make(chan func(), 8)
			h.pump.do(func() {
				h.app.setUIHook(func(fn func()) { callbacks <- fn })
			})
			var quitCount int
			h.pump.do(func() {
				h.app.requestQuit = func() { quitCount++ }
				h.app.beginShutdown()
			})

			// finish() closes done before OnExit, but finishShutdown can enqueue the
			// final callback before OnExit. Collect both owner callbacks without
			// assuming their order; execute only one first.
			var callbacksSeen [2]func()
			for i := 0; i < len(callbacksSeen); i++ {
				select {
				case callbacksSeen[i] = <-callbacks:
				case <-time.After(15 * time.Second):
					t.Fatalf("shutdown owner callback %d did not arrive", i+1)
				}
			}
			h.pump.do(callbacksSeen[firstIndex])
			var exited, ready bool
			h.pump.do(func() {
				exited = h.app.runtimeStatus == runtimeStateExited
				ready = h.app.quitReady
			})
			if exited == ready {
				t.Fatalf("after first owner callback, expected exactly one completion fact: exited=%v quitReady=%v", exited, ready)
			}
			t.Logf("first executed owner point: exited=%v quitReady=%v", exited, ready)

			cleanupDone := make(chan struct{})
			go func() {
				h.shutdown(t)
				close(cleanupDone)
			}()
			select {
			case <-cleanupDone:
				t.Fatal("cleanup completed before both owner callbacks were applied")
			default:
			}
			h.pump.do(func() {
				callbacksSeen[1-firstIndex]()
				exited = h.app.runtimeStatus == runtimeStateExited
				ready = h.app.quitReady
			})
			select {
			case <-cleanupDone:
			case <-time.After(5 * time.Second):
				t.Fatal("cleanup did not complete after both owner callbacks")
			}
			if !exited || !ready || quitCount != 1 || !h.closed {
				t.Fatalf("final owner state: exited=%v quitReady=%v quitCount=%d closed=%v", exited, ready, quitCount, h.closed)
			}
			h.shutdown(t)
			if quitCount != 1 {
				t.Fatalf("idempotent cleanup changed quit callback count: %d", quitCount)
			}
			select {
			case extra := <-callbacks:
				t.Fatalf("unexpected callback after owner completion: %p", extra)
			default:
			}
		})
	}
}
