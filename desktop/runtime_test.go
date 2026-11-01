package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
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
	var writeMu sync.Mutex
	write := func(value any) {
		bytes, _ := json.Marshal(value)
		writeMu.Lock()
		out.Write(bytes)
		out.WriteByte('\n')
		out.Flush()
		writeMu.Unlock()
	}
	write(map[string]any{"version": 1, "type": "ready", "api": "openai-responses"})
	writeStderr := func(message string) {
		writeMu.Lock()
		fmt.Fprintln(os.Stderr, message)
		writeMu.Unlock()
	}

	event := func(taskID string, payload map[string]any) {
		write(map[string]any{
			"version": 1, "type": "event", "session_id": "s1",
			"task_id": taskID, "message_id": "m1", "event": payload,
		})
	}

	gateMode := os.Getenv("RUNTIME_HELPER_GATE") == "1"
	capabilitiesFailFirst := os.Getenv("RUNTIME_CAPABILITIES_FAIL_FIRST") == "1"
	capabilityCalls := 0
	toolsFailFirst := os.Getenv("RUNTIME_TOOLS_FAIL_FIRST") == "1"
	toolListCalls := 0
	accepted := false
	completed := make(chan struct{})
	var completeOnce sync.Once
	if gateMode {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGUSR1)
		go func() {
			for range signals {
				completeOnce.Do(func() {
					event("t1", map[string]any{"type": "task_finished", "status": "completed"})
					close(completed)
				})
			}
		}()
	}

	var reorder []map[string]any
	slowID := ""
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
		if toolsFailFirst {
			writeStderr("tools-fixture:" + request.Method)
		}
		respond := func(result any) {
			write(map[string]any{"version": 1, "type": "response", "id": request.ID, "result": result})
		}
		switch request.Method {
		case "tool.list":
			toolListCalls++
			if toolsFailFirst && toolListCalls == 1 {
				write(map[string]any{
					"version": 1, "type": "response", "id": request.ID,
					"error": map[string]any{"code": "tool_source_unavailable", "message": "controlled tool catalog failure"},
				})
				continue
			}
			respond(map[string]any{"builtin": []map[string]string{{"name": "fixture-read", "description": "Fixture catalog tool"}}})
		case "runtime.capabilities":
			capabilityCalls++
			if capabilitiesFailFirst && capabilityCalls == 1 {
				writeStderr("runtime.capabilities:1:error")
				write(map[string]any{
					"version": 1, "type": "response", "id": request.ID,
					"error": map[string]any{"code": "fixture_failure", "message": "initial capabilities read failed"},
				})
				continue
			}
			writeStderr(fmt.Sprintf("runtime.capabilities:%d:ok", capabilityCalls))
			respond(map[string]any{
				"protocol_version": 1,
				"storage_schemas":  []int{1, 2},
				"methods":          []string{"session.create", "runtime.capabilities"},
				"max_active_tasks": 3,
				"automatic_titles": false,
			})
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
			if gateMode {
				accepted = true
			}
			event("t1", map[string]any{"type": "task_started"})
			if gateMode {
				continue
			}
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
			writeMu.Lock()
			out.WriteString("this is not json\n")
			out.Flush()
			writeMu.Unlock()
			respond(map[string]any{"ok": true})
		case "test.noshape":
			write(map[string]any{"version": 1, "type": "response", "id": request.ID})
		case "test.slow":
			slowID = request.ID
		case "test.release":
			if slowID != "" {
				write(map[string]any{"version": 1, "type": "response", "id": slowID, "result": map[string]any{"late": true}})
				slowID = ""
			}
			respond(map[string]any{"released": true})
		case "test.large":
			var params struct {
				Payload string `json:"payload"`
			}
			_ = json.Unmarshal(request.Params, &params)
			respond(map[string]any{"data": strings.Repeat("y", 200_000), "n": len(params.Payload)})
		case "test.exit":
			return 0
		case "runtime.shutdown":
			if capabilitiesFailFirst {
				writeStderr(fmt.Sprintf("runtime.capabilities:final:%d", capabilityCalls))
			}
			respond(map[string]any{"state": "draining"})
			writeStderr("draining")
			if !gateMode {
				return 0
			}
			// Keep reading until EOF; accepted gated work must drain.
		default:
			write(map[string]any{"version": 1, "type": "response", "id": request.ID, "error": map[string]any{"code": "unknown_method", "message": request.Method}})
		}
	}
	if accepted {
		<-completed
	}
	return 0
}

type testRuntime struct {
	client   *runtimeClient
	ready    chan runtimeReady
	events   chan runtimeEventEnvelope
	protocol chan string
	stderr   chan string
	exit     chan error
}

func startTestRuntime(t *testing.T, extraEnv ...string) *testRuntime {
	t.Helper()
	tr := &testRuntime{
		ready:    make(chan runtimeReady, 1),
		events:   make(chan runtimeEventEnvelope, 64),
		protocol: make(chan string, 8),
		stderr:   make(chan string, 8),
		exit:     make(chan error, 1),
	}
	env := append(os.Environ(), "GO_WANT_RUNTIME_HELPER=1")
	env = append(env, extraEnv...)
	client, err := startRuntime(runtimeOptions{
		Binary: os.Args[0],
		Args:   []string{"-test.run=^TestRuntimeHelperProcess$"},
		Dir:    t.TempDir(),
		Env:    env,
		Handlers: runtimeHandlers{
			OnReady:         func(ready runtimeReady) { tr.ready <- ready },
			OnEvent:         func(event runtimeEventEnvelope) { tr.events <- event },
			OnExit:          func(err error) { tr.exit <- err },
			OnProtocolError: func(message string) { tr.protocol <- message },
			OnStderr:        func(line string) { tr.stderr <- line },
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

func TestRuntimeClientProtocolFailureIsTerminal(t *testing.T) {
	tr := startTestRuntime(t)
	err := tr.client.call("test.malformed", map[string]any{}, nil, runtimeControlTimeout)
	if err == nil {
		t.Fatal("malformed output must fail the pending call")
	}
	select {
	case message := <-tr.protocol:
		if !strings.Contains(message, "malformed JSON") {
			t.Fatalf("protocol error = %q", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("malformed output was not reported")
	}
	// A later response from the same connection must not become a success.
	if err := tr.client.call("session.status", map[string]any{"session_id": "s1"}, nil, runtimeControlTimeout); err == nil {
		t.Fatal("calls after a protocol failure must not succeed")
	}
}

func TestRuntimeClientRejectsResponseWithoutResultOrError(t *testing.T) {
	tr := startTestRuntime(t)
	if err := tr.client.call("test.noshape", map[string]any{}, nil, runtimeControlTimeout); err == nil {
		t.Fatal("a response with neither result nor error must fail")
	}
	select {
	case message := <-tr.protocol:
		if !strings.Contains(message, "exactly one") {
			t.Fatalf("protocol error = %q", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("empty response shape was not reported")
	}
}

func TestRuntimeClientDiscardsLateResponseAfterTimeout(t *testing.T) {
	tr := startTestRuntime(t)
	if err := tr.client.call("test.slow", map[string]any{}, nil, 100*time.Millisecond); err == nil {
		t.Fatal("expected a timeout")
	}
	if err := tr.client.call("test.release", map[string]any{}, nil, runtimeControlTimeout); err != nil {
		t.Fatal(err)
	}
	var session runtimeSession
	if err := tr.client.call("session.status", map[string]any{"session_id": "s1"}, &session, runtimeControlTimeout); err != nil {
		t.Fatalf("late response broke the connection: %v", err)
	}
	select {
	case message := <-tr.protocol:
		t.Fatalf("late response was treated as a protocol failure: %q", message)
	default:
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

func TestRuntimeShutdownDrainsAcceptedTask(t *testing.T) {
	tr := startTestRuntime(t, "RUNTIME_HELPER_GATE=1")
	if _, err := tr.client.createSession("/tmp", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.client.sendTask("s1", "gated"); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-tr.events:
		if event.Event.Type != "task_started" {
			t.Fatalf("first event = %+v", event.Event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("task did not start")
	}

	shutdownErr := make(chan error, 1)
	go func() { shutdownErr <- tr.client.shutdown() }()
	// Wait until the core has observed shutdown and started draining before
	// releasing the accepted task. No sleeps: the helper reports this barrier.
	select {
	case line := <-tr.stderr:
		if line != "draining" {
			t.Fatalf("stderr = %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not start draining")
	}
	select {
	case <-shutdownErr:
		t.Fatal("shutdown returned before the accepted task was released")
	default:
	}
	if err := tr.client.cmd.Process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-shutdownErr:
		if err != nil {
			t.Fatalf("shutdown = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish after the task was released")
	}
	select {
	case event := <-tr.events:
		if event.Event.Type != "task_finished" || event.Event.Status != "completed" {
			t.Fatalf("release event = %+v", event.Event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("drained task did not finish")
	}
	if state := tr.client.cmd.ProcessState; state == nil || state.ExitCode() != 0 {
		t.Fatalf("process state = %+v", state)
	}
}

func TestRuntimeClientParallelLargeIO(t *testing.T) {
	tr := startTestRuntime(t)
	payload := strings.Repeat("z", 1_000_000)
	type result struct {
		n   int
		len int
		err error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out struct {
				Data string `json:"data"`
				N    int    `json:"n"`
			}
			err := tr.client.call("test.large", map[string]any{"payload": payload}, &out, 10*time.Second)
			results <- result{n: out.N, len: len(out.Data), err: err}
		}()
	}
	wg.Wait()
	close(results)
	for got := range results {
		if got.err != nil || got.n != len(payload) || got.len != 200_000 {
			t.Fatalf("result = %+v", got)
		}
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
