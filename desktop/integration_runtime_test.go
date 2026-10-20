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
	requests atomic.Int32
	mu       sync.Mutex
	bodies   []string
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
		switch index {
		case 1:
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
	provider := &providerFixture{}
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
