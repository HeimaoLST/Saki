package main

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRuntimeHelperProcess is the subprocess used by the client tests. It only
// runs when the parent sets GO_WANT_RUNTIME_HELPER; it speaks newline JSON on
// stdout and reads requests on stdin, like the real Saki --stdio entry.
func TestRuntimeHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_RUNTIME_HELPER") != "1" {
		return
	}
	os.Exit(helperMain())
}

func helperMain() int {
	out := bufio.NewWriter(os.Stdout)
	write := func(value any) {
		bytes, _ := json.Marshal(value)
		out.Write(bytes)
		out.WriteByte('\n')
		out.Flush()
	}
	write(map[string]any{"version": 1, "type": "ready", "api": "openai-responses"})

	var reorder []map[string]any
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), runtimeMaxLine)
	for scanner.Scan() {
		var request struct {
			Version int             `json:"version"`
			ID      string          `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			continue
		}
		respond := func(result any) {
			write(map[string]any{"version": 1, "type": "response", "id": request.ID, "result": result})
		}
		event := func(taskID string, payload map[string]any) {
			write(map[string]any{
				"version": 1, "type": "event", "session_id": "s1",
				"task_id": taskID, "message_id": "m1", "event": payload,
			})
		}
		switch request.Method {
		case "session.create", "session.load", "session.status":
			respond(map[string]any{
				"session_id": "s1", "loaded": true, "state": "idle", "status": "unknown",
				"active_task": nil,
				"metadata": map[string]any{
					"schema_version": 1, "id": "s1", "title": "New session",
					"created_at": 1700000000000, "working_directory": "/tmp",
					"model": "fixture", "api": "openai-responses",
				},
			})
		case "session.history":
			respond(map[string]any{
				"session_id": "s1", "records": []any{}, "next_offset": nil,
				"status": "completed", "state": "idle", "active_task_id": nil,
			})
		case "task.send":
			respond(map[string]any{"task_id": "t1", "message_id": "m1"})
			event("t1", map[string]any{"type": "task_started"})
			event("t1", map[string]any{"type": "model_request_started", "purpose": "generation"})
			event("t1", map[string]any{"type": "response_text_delta", "item_id": "a1", "delta": "hel"})
			event("t1", map[string]any{"type": "response_text", "item_id": "a1", "text": strings.Repeat("x", 200_000)})
			event("t1", map[string]any{"type": "tool_started", "call_id": "k1", "name": "read", "arguments": `{"path":"probe.txt"}`})
			event("t1", map[string]any{"type": "tool_finished", "call_id": "k1", "name": "read", "output": map[string]any{"content": "marker"}})
			event("t1", map[string]any{"type": "task_finished", "status": "completed"})
		case "test.reorder":
			var params map[string]int
			_ = json.Unmarshal(request.Params, &params)
			reorder = append(reorder, map[string]any{"id": request.ID, "n": params["n"]})
			if len(reorder) == 2 {
				for i := len(reorder) - 1; i >= 0; i-- {
					write(map[string]any{"version": 1, "type": "response", "id": reorder[i]["id"], "result": map[string]any{"n": reorder[i]["n"]}})
				}
				reorder = nil
			}
		case "test.malformed":
			out.WriteString("this is not json\n")
			out.Flush()
			respond(map[string]any{"ok": true})
		case "test.exit":
			return 0
		case "runtime.shutdown":
			respond(map[string]any{"state": "draining"})
			return 0
		default:
			write(map[string]any{"version": 1, "type": "response", "id": request.ID, "error": map[string]any{"code": "unknown_method", "message": request.Method}})
		}
	}
	return 0
}

type testRuntime struct {
	client   *runtimeClient
	ready    chan runtimeReady
	events   chan runtimeEventEnvelope
	protocol chan string
	exit     chan error
}

func startTestRuntime(t *testing.T) *testRuntime {
	t.Helper()
	tr := &testRuntime{
		ready:    make(chan runtimeReady, 1),
		events:   make(chan runtimeEventEnvelope, 64),
		protocol: make(chan string, 8),
		exit:     make(chan error, 1),
	}
	client, err := startRuntime(runtimeOptions{
		Binary: os.Args[0],
		Args:   []string{"-test.run=^TestRuntimeHelperProcess$"},
		Dir:    t.TempDir(),
		Env:    append(os.Environ(), "GO_WANT_RUNTIME_HELPER=1"),
		Handlers: runtimeHandlers{
			OnReady:         func(ready runtimeReady) { tr.ready <- ready },
			OnEvent:         func(event runtimeEventEnvelope) { tr.events <- event },
			OnExit:          func(err error) { tr.exit <- err },
			OnProtocolError: func(message string) { tr.protocol <- message },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr.client = client
	t.Cleanup(func() { _ = client.shutdown() })
	select {
	case ready := <-tr.ready:
		if ready.Version != 1 || ready.Type != "ready" {
			t.Fatalf("ready = %+v", ready)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ready")
	}
	return tr
}

func TestRuntimeClientRoutesEventsAndLongLines(t *testing.T) {
	tr := startTestRuntime(t)
	session, err := tr.client.createSession("/tmp", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionID != "s1" || session.Metadata == nil || session.Metadata.Model != "fixture" {
		t.Fatalf("session = %+v", session)
	}
	task, err := tr.client.sendTask("s1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if task.TaskID != "t1" || task.MessageID != "m1" {
		t.Fatalf("task = %+v", task)
	}
	var eventTypes []string
	var bigText string
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-tr.events:
			eventTypes = append(eventTypes, event.Event.Type)
			if event.Event.Type == "response_text" {
				bigText = event.Event.Text
			}
			if event.Event.Type == "task_finished" {
				if event.Event.Status != "completed" {
					t.Fatalf("task_finished = %+v", event.Event)
				}
				goto done
			}
		case <-deadline:
			t.Fatalf("events = %v", eventTypes)
		}
	}
done:
	for _, want := range []string{"task_started", "model_request_started", "response_text_delta", "response_text", "tool_started", "tool_finished", "task_finished"} {
		found := false
		for _, got := range eventTypes {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing event %q in %v", want, eventTypes)
		}
	}
	if len(bigText) != 200_000 {
		t.Fatalf("long event text length = %d", len(bigText))
	}
}

func TestRuntimeClientRoutesConcurrentResponsesByID(t *testing.T) {
	tr := startTestRuntime(t)
	type result struct {
		expected int
		got      int
		err      error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, n := range []int{1, 2} {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			var out struct {
				N int `json:"n"`
			}
			err := tr.client.call("test.reorder", map[string]any{"n": n}, &out, runtimeControlTimeout)
			results <- result{expected: n, got: out.N, err: err}
		}(n)
	}
	wg.Wait()
	close(results)
	for got := range results {
		if got.err != nil || got.got != got.expected {
			t.Fatalf("result = %+v", got)
		}
	}
}

func TestRuntimeClientReportsMalformedOutput(t *testing.T) {
	tr := startTestRuntime(t)
	var out struct {
		OK bool `json:"ok"`
	}
	if err := tr.client.call("test.malformed", map[string]any{}, &out, runtimeControlTimeout); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-tr.protocol:
		if !strings.Contains(message, "malformed JSON") {
			t.Fatalf("protocol error = %q", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("malformed output was not reported")
	}
}

func TestRuntimeClientDisconnectFailsPendingCall(t *testing.T) {
	tr := startTestRuntime(t)
	err := tr.client.call("test.exit", map[string]any{}, nil, runtimeControlTimeout)
	if err == nil {
		t.Fatal("expected a disconnect error")
	}
	select {
	case <-tr.exit:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime exit was not reported")
	}
}

func TestInputTextShapes(t *testing.T) {
	cases := map[string]string{
		`"hello"`: "hello",
		`[{"type":"text","text":"a"},{"type":"image"}]`: "a[image]",
		`[{"type":"text","text":"no"}]`:                 "no",
	}
	for raw, want := range cases {
		if got := inputText(json.RawMessage(raw)); got != want {
			t.Errorf("inputText(%s) = %q, want %q", raw, got, want)
		}
	}
}
