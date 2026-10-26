package main

// Real-process boundary test for #117 task context. It uses the candidate
// runtime binary and the existing controlled provider fixture:
//
//	SAKI_TEST_RUNTIME_BIN=/abs/path/to/Saki go test -run TestTaskContextRuntime -count=1 .
//
// The staging send helper uses runtimeClient.call directly because the typed
// GUI send signature is owned by the settings change. It is test-only and
// sends the same params.context shape the production adapter will use.

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type taskContextCallerContext struct {
	Type  string `json:"type"`
	Label string `json:"label"`
	Name  string `json:"name"`
	Text  string `json:"text"`
}

func TestTaskContextRuntimeSendRefreshAndRemoval(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{}
	server := httptest.NewServer(provider.handler(t, "task-context-117"))
	defer server.Close()

	root := t.TempDir()
	filePath := filepath.Join(root, "brief.txt")
	fileText := "File snapshot — 你好\nsecond line\n"
	if err := os.WriteFile(filePath, []byte(fileText), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(),
		"API_KEY=offline-fixture",
		"SAKI_API=openai-responses",
		"SAKI_BASE_URL="+server.URL+"/v1",
		"SAKI_MODEL=fixture",
		"SAKI_CONTEXT_TOKENS=131072",
		"SAKI_SESSION_DIR="+filepath.Join(root, "sessions"),
		"SAKI_SESSION=missing",
	)
	live := startIntegrationRuntime(t, binary, root, env)
	defer func() {
		if err := live.client.shutdown(); err != nil {
			t.Logf("runtime shutdown: %v", err)
		}
	}()
	session, err := live.client.createSession(root, "task context integration")
	if err != nil {
		t.Fatal(err)
	}

	tc := &taskContext{}
	named, err := tc.AddSnippet("Release notes", "First line\n第二行 — café")
	if err != nil {
		t.Fatal(err)
	}
	fileItem, err := tc.ImportFile(filePath, "")
	if err != nil {
		t.Fatal(err)
	}
	submitted := tc.snapshot()
	absolutePath, err := filepath.Abs(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if fileItem.Source != absolutePath || submitted[1].Source != absolutePath {
		t.Fatalf("file source = %q / %q, want %q", fileItem.Source, submitted[1].Source, absolutePath)
	}
	// Later disk changes must not affect the snapshot sent below.
	if err := os.WriteFile(filePath, []byte("different disk text"), 0o600); err != nil {
		t.Fatal(err)
	}

	first := sendTaskWithContext(t, live.client, session.SessionID, "Use the saved context.", submitted)
	live.finishTask(t, first.TaskID)
	if provider.count() != 1 {
		t.Fatalf("first task model requests = %d", provider.count())
	}
	firstContexts := taskContextCallerContexts(t, provider.body(0))
	if len(firstContexts) != 2 {
		t.Fatalf("first model request carried %d caller contexts: %+v", len(firstContexts), firstContexts)
	}
	if named.inputName() != named.Name {
		t.Fatalf("snippet input name changed: %q", named.inputName())
	}
	if !strings.Contains(fileItem.inputName(), fileItem.Name) || !strings.Contains(fileItem.inputName(), fileItem.Source) {
		t.Fatalf("file input name lacks display name or source: %q", fileItem.inputName())
	}
	byName := map[string]string{}
	for _, context := range firstContexts {
		byName[context.Name] = context.Text
	}
	if byName[named.inputName()] != named.Text {
		t.Fatalf("named context in model request = %q, want %q", byName[named.inputName()], named.Text)
	}
	if byName[fileItem.inputName()] != fileText {
		t.Fatalf("file context in model request = %q, want %q", byName[fileItem.inputName()], fileText)
	}
	if strings.Contains(strings.Join(firstContextsText(firstContexts), "\n"), "different disk text") {
		t.Fatal("model request used the later disk contents instead of the saved snapshot")
	}

	history, err := live.client.historyPage(session.SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	historyContexts := taskContextHistoryCallerContexts(t, history)
	if len(historyContexts) != 2 {
		t.Fatalf("history carried %d caller contexts: %+v", len(historyContexts), historyContexts)
	}
	for _, context := range historyContexts {
		if context.Name == named.inputName() && context.Text != named.Text {
			t.Fatalf("history named text = %q", context.Text)
		}
		if context.Name == fileItem.inputName() && context.Text != fileText {
			t.Fatalf("history file text = %q", context.Text)
		}
	}

	if removed := tc.clearSubmitted(submitted); removed != 2 {
		t.Fatalf("accepted send cleared %d snapshots", removed)
	}
	if len(tc.snapshot()) != 0 {
		t.Fatalf("accepted snapshots remained: %+v", tc.snapshot())
	}

	second := sendTaskWithContext(t, live.client, session.SessionID, "Follow up without new context.", tc.snapshot())
	live.finishTask(t, second.TaskID)
	if provider.count() != 2 {
		t.Fatalf("second task model requests = %d", provider.count())
	}
	secondBody := provider.body(1)
	if got := taskContextLastUserText(t, secondBody); strings.Contains(got, "caller_context") || !strings.Contains(got, "Follow up without new context.") {
		t.Fatalf("later user turn attached context again: %q", got)
	}
	// The earlier context remains only as conversation history: it is not sent
	// again as a fresh caller-context input for the later turn.
	if contexts := taskContextCallerContexts(t, secondBody); len(contexts) != 2 {
		t.Fatalf("later model request has %d caller contexts, want the two historical ones: %+v", len(contexts), contexts)
	}

	history, err = live.client.historyPage(session.SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if contexts := taskContextHistoryCallerContexts(t, history); len(contexts) != 2 {
		t.Fatalf("later history has %d caller contexts, want only the first turn's two: %+v", len(contexts), contexts)
	}
}

// sendTaskWithContext is staging evidence for the actual task.send protocol.
// Production keeps the typed runtimeContextInput in the settings-owned client.
func sendTaskWithContext(t *testing.T, client *runtimeClient, sessionID, text string, items []taskContextItem) runtimeSendResult {
	t.Helper()
	params := map[string]any{
		"session_id": sessionID,
		"message":    map[string]any{"text": text},
	}
	if len(items) > 0 {
		context := make([]map[string]string, 0, len(items))
		for _, item := range items {
			context = append(context, map[string]string{"name": item.inputName(), "text": item.Text})
		}
		params["context"] = context
	}
	var result runtimeSendResult
	if err := client.call("task.send", params, &result, runtimeSendTimeout); err != nil {
		t.Fatal(err)
	}
	if result.TaskID == "" || result.MessageID == "" {
		t.Fatal("runtime accepted task without task_id/message_id")
	}
	return result
}

func taskContextCallerContexts(t *testing.T, body string) []taskContextCallerContext {
	t.Helper()
	var root any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		t.Fatalf("model request is not JSON: %v", err)
	}
	var texts []string
	taskContextCollectStrings(root, &texts)
	var contexts []taskContextCallerContext
	for _, text := range texts {
		context, ok := taskContextParseCallerContext(text)
		if ok {
			contexts = append(contexts, context)
		}
	}
	return contexts
}

func taskContextCollectStrings(value any, out *[]string) {
	switch current := value.(type) {
	case string:
		*out = append(*out, current)
	case []any:
		for _, item := range current {
			taskContextCollectStrings(item, out)
		}
	case map[string]any:
		for _, item := range current {
			taskContextCollectStrings(item, out)
		}
	}
}

func taskContextParseCallerContext(text string) (taskContextCallerContext, bool) {
	if !strings.Contains(text, `"caller_context"`) {
		return taskContextCallerContext{}, false
	}
	var context taskContextCallerContext
	if err := json.Unmarshal([]byte(text), &context); err != nil || context.Type != "caller_context" {
		return taskContextCallerContext{}, false
	}
	return context, true
}

func taskContextHistoryCallerContexts(t *testing.T, page runtimeHistory) []taskContextCallerContext {
	t.Helper()
	var contexts []taskContextCallerContext
	for _, record := range page.Records {
		if record.Record.Type != "messages" {
			continue
		}
		for _, item := range record.Record.Items {
			if item.Role != "user" {
				continue
			}
			var parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(item.Content, &parts); err == nil {
				for _, part := range parts {
					if part.Type != "text" {
						continue
					}
					if context, ok := taskContextParseCallerContext(part.Text); ok {
						contexts = append(contexts, context)
					}
				}
				continue
			}
			var text string
			if err := json.Unmarshal(item.Content, &text); err == nil {
				if context, ok := taskContextParseCallerContext(text); ok {
					contexts = append(contexts, context)
				}
			}
		}
	}
	return contexts
}

func taskContextLastUserText(t *testing.T, body string) string {
	t.Helper()
	var request struct {
		Input []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatalf("model request is not JSON: %v", err)
	}
	for index := len(request.Input) - 1; index >= 0; index-- {
		if request.Input[index].Role != "user" {
			continue
		}
		var text string
		if err := json.Unmarshal(request.Input[index].Content, &text); err == nil {
			return text
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(request.Input[index].Content, &parts); err != nil {
			t.Fatalf("last user content is not text: %v", err)
		}
		var out []string
		for _, part := range parts {
			if part.Type == "input_text" || part.Type == "text" {
				out = append(out, part.Text)
			}
		}
		return strings.Join(out, "\n")
	}
	t.Fatal("model request had no user input")
	return ""
}

func firstContextsText(contexts []taskContextCallerContext) []string {
	out := make([]string, 0, len(contexts))
	for _, context := range contexts {
		out = append(out, context.Text)
	}
	return out
}
