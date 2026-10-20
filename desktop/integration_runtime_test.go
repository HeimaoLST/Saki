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
)

type providerFixture struct {
	ToolFirst bool
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
	app.uiHook = func(fn func()) { p.q <- fn }
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
	waiter := &uiWaiter{cond: cond, ch: make(chan struct{})}
	p.mu.Lock()
	p.waiters = append(p.waiters, waiter)
	p.mu.Unlock()
	p.q <- func() {} // trigger a check for an already-true condition
	select {
	case <-waiter.ch:
	case <-time.After(15 * time.Second):
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
		if app.rt != nil {
			_ = app.rt.shutdown()
		}
		pump.close()
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
	quit := make(chan struct{}, 1)
	h.pump.do(func() {
		h.app.requestQuit = func() { quit <- struct{}{} }
		h.app.beginShutdown()
	})
	select {
	case <-quit:
	case <-time.After(15 * time.Second):
		t.Fatal("runtime did not drain before quit")
	}
	h.pump.close()
	h.closed = true
}

func (h *guiHarness) snapshot(fn func()) {
	h.pump.do(fn)
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
	if aProfile != "" {
		t.Fatalf("zero-profile send changed purpose: %q", aProfile)
	}

	// Bound settings: only the name changes; no create/send, history/draft kept.
	before := provider.count()
	h.pump.do(func() {
		h.app.selectContact(0)
		ct := h.app.contacts[0]
		ct.Draft = "keep after restart"
		h.app.draftChanged(ct)
		h.app.openSettings()
		h.app.settingsDraft.Name = "Renamed"
		h.app.settingsDraft.Purpose = "should not apply"
		h.app.settingsDraft.Instructions = "should not apply"
		h.app.settingsDraft.Workspace = "/should-not-apply"
		h.app.saveSettings()
	})
	var name, sessionAfter, draftAfter string
	var purposeAfter string
	var messagesAfter int
	h.pump.do(func() {
		ct := h.app.contacts[0]
		name, sessionAfter, draftAfter, purposeAfter, messagesAfter = ct.Name, ct.SessionID, ct.Draft, ct.Purpose, len(ct.Messages)
	})
	if name != "Renamed" || sessionAfter != aSession || purposeAfter != "" || draftAfter != "keep after restart" || messagesAfter != aMessages {
		t.Fatalf("bound settings changed identity: name=%q session=%q purpose=%q draft=%q messages=%d", name, sessionAfter, purposeAfter, draftAfter, messagesAfter)
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
		return ct.Loaded && len(ct.Messages) > 0
	})
	h2.snapshot(func() {
		ct := h2.app.contacts[0]
		if ct.SessionID != aSession || ct.Name != "Renamed" || ct.Purpose != "" || ct.Draft != "keep after restart" {
			t.Fatalf("restored contact = %+v", ct)
		}
		if h2.app.contacts[1].Draft != "draft for B" {
			t.Fatalf("B draft = %q", h2.app.contacts[1].Draft)
		}
	})
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
		h.app.settingsDraft.Name = "Writer"
		h.app.settingsDraft.Purpose = "B purpose"
		h.app.settingsDraft.Instructions = "B instructions"
		h.app.settingsDraft.Workspace = h.root
		h.app.saveSettings()
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
