package main

// Real Rust write/edit integration for issue result derivation. It is opt-in
// like the other runtime process tests:
//
//	SAKI_TEST_RUNTIME_BIN=/abs/path/to/Saki go test -run TestIssueResultFilesRuntimeIntegration -count=1 .
//
// The controlled provider is a local HTTP fixture; no credentials are read or printed.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type issueResultProvider struct {
	root     string
	requests atomic.Int32
	mu       sync.Mutex
	bodies   []string
}

func (p *issueResultProvider) count() int { return int(p.requests.Load()) }

func (p *issueResultProvider) body(i int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < 0 || i >= len(p.bodies) {
		return ""
	}
	return p.bodies[i]
}

func (p *issueResultProvider) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		index := int(p.requests.Add(1))
		p.mu.Lock()
		p.bodies = append(p.bodies, string(body))
		p.mu.Unlock()
		var response any
		switch index {
		case 1:
			response = issueResultFunctionCall("write", "call-write", map[string]any{
				"path": "result.txt", "content": "first\n",
			})
		case 2:
			response = issueResultFunctionCall("edit", "call-edit", map[string]any{
				"path": "result.txt",
				"edits": []any{
					map[string]any{"oldText": "first", "newText": "second"},
				},
			})
		default:
			response = map[string]any{"id": "response", "status": "completed", "output": []any{map[string]any{
				"type": "message", "id": "answer", "status": "completed", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "created and modified result.txt"}},
			}}}
		}
		data, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

func issueResultFunctionCall(name, callID string, arguments map[string]any) any {
	raw, err := json.Marshal(arguments)
	if err != nil {
		panic(err)
	}
	return map[string]any{"id": "response-" + callID, "status": "completed", "output": []any{map[string]any{
		"type": "function_call", "status": "completed", "id": "item-" + callID,
		"call_id": callID, "name": name, "arguments": string(raw),
	}}}
}

func TestIssueResultFilesRuntimeIntegration(t *testing.T) {
	binary := requireRuntimeBinary(t)
	root := t.TempDir()
	provider := &issueResultProvider{root: root}
	server := httptest.NewServer(provider.handler(t))
	defer server.Close()
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
	session, err := first.client.createSession(root, "issue result integration")
	if err != nil {
		t.Fatal(err)
	}
	task, err := first.client.sendTask(session.SessionID, "Create result.txt and then modify it.")
	if err != nil {
		t.Fatal(err)
	}
	first.finishTask(t, task.TaskID)
	if provider.count() != 3 {
		t.Fatalf("model requests = %d, want write + edit + final", provider.count())
	}
	history, err := first.client.historyPage(session.SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if session.Metadata == nil || session.Metadata.WorkingDirectory == "" {
		t.Fatalf("session create did not return an actual working directory: %+v", session.Metadata)
	}
	if session.Metadata.WorkingDirectory != resolvedRoot {
		t.Fatalf("session working directory = %q, want %q", session.Metadata.WorkingDirectory, resolvedRoot)
	}
	source := issueResultSource{
		ExecutionID: "exec-runtime", SessionID: session.SessionID, TaskID: task.TaskID,
		MessageID: task.MessageID, WorkingDirectory: session.Metadata.WorkingDirectory,
	}
	derived := deriveIssueResultFiles(source, history)
	if derived.Unavailable != "" {
		t.Fatalf("unavailable = %q", derived.Unavailable)
	}
	if len(derived.Files) != 2 {
		t.Fatalf("files = %+v", derived.Files)
	}
	wantPath := filepath.Join(resolvedRoot, "result.txt")
	if derived.Files[0].ToolName != "write" || derived.Files[0].CallID != "call-write" || derived.Files[0].Path != wantPath {
		t.Fatalf("write row = %+v", derived.Files[0])
	}
	if derived.Files[1].ToolName != "edit" || derived.Files[1].CallID != "call-edit" || derived.Files[1].Path != wantPath {
		t.Fatalf("edit row = %+v", derived.Files[1])
	}
	inspection := inspectIssueResultFile(derived.Files[1])
	if !inspection.Exists || !inspection.Regular || inspection.Preview != "second\n" {
		t.Fatalf("current file after real write/edit = %+v", inspection)
	}
	opened := ""
	action := requestOpenIssueResultFile(derived.Files[1], func(path string) error {
		opened = path
		return nil
	})
	if action.Err != "" || action.Notice != "" || opened != wantPath {
		t.Fatalf("open action = %+v opened=%q", action, opened)
	}
	if err := first.client.shutdown(); err != nil {
		t.Fatal(err)
	}

	// A new process restores the same execution identity from persisted history
	// without asking the provider to run the tools again.
	before := provider.count()
	second := startIntegrationRuntime(t, binary, root, env)
	loaded, err := second.client.loadSession(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Metadata == nil || loaded.Metadata.WorkingDirectory != resolvedRoot {
		t.Fatalf("loaded working directory = %+v", loaded.Metadata)
	}
	restored, err := second.client.historyPage(session.SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if provider.count() != before {
		t.Fatalf("restart issued model requests: %d -> %d", before, provider.count())
	}
	source.WorkingDirectory = loaded.Metadata.WorkingDirectory
	again := deriveIssueResultFiles(source, restored)
	if again.Unavailable != "" || len(again.Files) != 2 {
		t.Fatalf("restored derivation = %+v", again)
	}
	if again.Files[0].RawResult != derived.Files[0].RawResult || again.Files[1].RawResult != derived.Files[1].RawResult {
		t.Fatal("restored raw results differ from the original execution results")
	}
	if inspection := inspectIssueResultFile(again.Files[1]); inspection.Preview != "second\n" {
		t.Fatalf("restored current file = %+v", inspection)
	}
	if !strings.Contains(provider.body(0), "Create result.txt") {
		t.Fatal("first provider request did not contain the original task")
	}
	if err := second.client.shutdown(); err != nil {
		t.Fatal(err)
	}
}
