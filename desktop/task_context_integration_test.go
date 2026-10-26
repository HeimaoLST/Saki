package main

// Real-process boundary test for #117 task context. It uses the candidate
// runtime binary, the existing controlled provider fixture, and the typed
// runtimeClient.sendTask context hook:
//
//	SAKI_TEST_RUNTIME_BIN=/abs/path/to/Saki go test -run TestTaskContextRuntime -count=1 .
//
// The only adapter below projects saved snapshots onto runtimeContextInput.

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
	removed, err := tc.AddSnippet("Temporary note", "TEMP-117-REMOVED-BEFORE-SEND")
	if err != nil {
		t.Fatal(err)
	}
	if !tc.Remove(removed.ID) {
		t.Fatal("temporary context was not removed before send")
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

	first, err := live.client.sendTask(session.SessionID, "Use the saved context.", taskContextInputs(submitted)...)
	if err != nil {
		t.Fatal(err)
	}
	live.finishTask(t, first.TaskID)
	if provider.count() != 1 {
		t.Fatalf("first task model requests = %d", provider.count())
	}
	firstBody := provider.body(0)
	if strings.Contains(firstBody, removed.Text) {
		t.Fatal("removed context reached the model request")
	}
	firstContexts := taskContextCallerContexts(t, firstBody)
	if len(firstContexts) != 2 {
		t.Fatalf("first model request carried %d caller contexts: %+v", len(firstContexts), firstContexts)
	}
	firstUserContexts := taskContextUserCallerContexts(t, firstBody)
	if len(firstUserContexts) != 2 {
		t.Fatalf("caller context was not confined to the user turn: all=%d user=%d", len(firstContexts), len(firstUserContexts))
	}
	for _, context := range firstContexts {
		if context.Type != "caller_context" || context.Label != "Caller-provided context (user data)" {
			t.Fatalf("context was not marked as caller user data: %+v", context)
		}
	}
	mainTextCount := 0
	for _, segments := range taskContextUserTexts(t, firstBody) {
		for _, segment := range segments {
			mainTextCount += strings.Count(segment, "Use the saved context.")
		}
	}
	if mainTextCount != 1 {
		t.Fatalf("main user text appeared %d times in user input", mainTextCount)
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
	historyJSON, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(historyJSON), removed.Text) {
		t.Fatal("removed context reached runtime history")
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

	second, err := live.client.sendTask(session.SessionID, "Follow up without new context.", taskContextInputs(tc.snapshot())...)
	if err != nil {
		t.Fatal(err)
	}
	live.finishTask(t, second.TaskID)
	if provider.count() != 2 {
		t.Fatalf("second task model requests = %d", provider.count())
	}
	secondBody := provider.body(1)
	if strings.Contains(secondBody, removed.Text) {
		t.Fatal("removed context reappeared in a later model request")
	}
	if got := taskContextLastUserText(t, secondBody); strings.Contains(got, "caller_context") || !strings.Contains(got, "Follow up without new context.") {
		t.Fatalf("later user turn attached context again: %q", got)
	}
	// The earlier context remains only as conversation history: it is not sent
	// again as a fresh caller-context input for the later turn.
	if contexts := taskContextCallerContexts(t, secondBody); len(contexts) != 2 {
		t.Fatalf("later model request has %d caller contexts, want the two historical ones: %+v", len(contexts), contexts)
	}
	if contexts := taskContextUserCallerContexts(t, secondBody); len(contexts) != 2 {
		t.Fatalf("later user turn has %d historical caller contexts, want 2: %+v", len(contexts), contexts)
	}

	history, err = live.client.historyPage(session.SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if contexts := taskContextHistoryCallerContexts(t, history); len(contexts) != 2 {
		t.Fatalf("later history has %d caller contexts, want only the first turn's two: %+v", len(contexts), contexts)
	}
	historyJSON, err = json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(historyJSON), removed.Text) {
		t.Fatal("removed context reached later runtime history")
	}
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
	users := taskContextUserTexts(t, body)
	if len(users) == 0 {
		t.Fatal("model request had no user input")
	}
	return strings.Join(users[len(users)-1], "\n")
}

func taskContextUserCallerContexts(t *testing.T, body string) []taskContextCallerContext {
	t.Helper()
	var contexts []taskContextCallerContext
	for _, segments := range taskContextUserTexts(t, body) {
		for _, segment := range segments {
			if context, ok := taskContextParseCallerContext(segment); ok {
				contexts = append(contexts, context)
			}
		}
	}
	return contexts
}

func taskContextUserTexts(t *testing.T, body string) [][]string {
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
	var users [][]string
	for _, message := range request.Input {
		if message.Role == "user" {
			users = append(users, taskContextContentTexts(t, message.Content))
		}
	}
	return users
}

func taskContextContentTexts(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []string{text}
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("user content is not text: %v", err)
	}
	var texts []string
	for _, part := range parts {
		if part.Type == "input_text" || part.Type == "text" {
			texts = append(texts, part.Text)
		}
	}
	return texts
}

func firstContextsText(contexts []taskContextCallerContext) []string {
	out := make([]string, 0, len(contexts))
	for _, context := range contexts {
		out = append(out, context.Text)
	}
	return out
}

func TestTaskContextGUIAppConversationAndIssueRuntimeSend(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{}
	h := startGUIHarness(t, binary, provider, "task-context-gui-117")
	defer func() {
		if !h.closed {
			h.shutdown(t)
		}
	}()

	filePath := filepath.Join(h.root, "gui-brief.txt")
	fileText := "GUI file snapshot — 你好\nsecond line"
	if err := os.WriteFile(filePath, []byte(fileText), 0o600); err != nil {
		t.Fatal(err)
	}
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if _, err := ct.Context.AddSnippet("GUI release notes", "GUI snippet\n第一行"); err != nil {
			t.Fatal(err)
		}
		if _, err := ct.Context.ImportFile(filePath, ""); err != nil {
			t.Fatal(err)
		}
		ct.Draft = "Use the GUI context."
		if !h.app.sendCurrent() {
			t.Fatalf("conversation send was rejected: %s", ct.SendError)
		}
	})
	// The snapshot must stay at the bytes read at import even if the source
	// changes before the runtime request is made.
	if err := os.WriteFile(filePath, []byte("changed after snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.LastTaskID != "" && ct.ActiveTaskID == "" && ct.SendState == sendIdle &&
			strings.Contains(lastMessageText(ct.Messages), "task-context-gui-117 answer")
	})
	if provider.count() != 1 {
		t.Fatalf("conversation provider requests = %d", provider.count())
	}
	firstBody := provider.body(0)
	firstContexts := taskContextCallerContexts(t, firstBody)
	if len(firstContexts) != 2 {
		t.Fatalf("conversation contexts = %+v", firstContexts)
	}
	if !strings.Contains(firstBody, "Use the GUI context.") || strings.Contains(firstBody, "changed after snapshot") {
		t.Fatal("conversation body did not use the captured snapshot")
	}
	for _, context := range firstContexts {
		if context.Type != "caller_context" || !strings.Contains(context.Name, "GUI release notes") && !strings.Contains(context.Name, "gui-brief.txt") {
			t.Fatalf("conversation context shape = %+v", context)
		}
	}
	if !strings.Contains(firstContexts[1].Name, filePath) || firstContexts[1].Text != fileText {
		t.Fatalf("conversation file context = %+v", firstContexts[1])
	}

	var conversationSession string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		conversationSession = ct.SessionID
		if len(ct.Context.snapshot()) != 0 {
			t.Fatalf("accepted conversation context was not cleared: %+v", ct.Context.snapshot())
		}
	})
	history, err := h.app.rt.historyPage(conversationSession, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if contexts := taskContextHistoryCallerContexts(t, history); len(contexts) != 2 {
		t.Fatalf("conversation history contexts = %+v", contexts)
	}

	issueID := ""
	h.pump.do(func() {
		if !h.app.createIssue("Context issue", "Use the issue context.") {
			t.Fatalf("createIssue: %s", h.app.issueFormError)
		}
		ct := h.app.selectedContact()
		issueID = ct.SelectedIssueID
		issue := ct.issueByID(issueID)
		if issue == nil {
			t.Fatal("created issue missing")
		}
		if _, err := issue.Context.AddSnippet("Issue release notes", "issue snippet"); err != nil {
			t.Fatal(err)
		}
		ct.Draft = "ordinary conversation draft"
		if !h.app.runIssue(issueID) {
			t.Fatalf("runIssue: %s", h.app.issueFormError)
		}
	})
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		if issue == nil || len(issue.Executions) == 0 {
			return false
		}
		return issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle && issue.Work.Loaded &&
			issue.Executions[len(issue.Executions)-1].Status == issueExecutionCompleted
	})
	if provider.count() != 2 {
		t.Fatalf("issue provider requests = %d", provider.count())
	}
	secondBody := provider.body(1)
	secondContexts := taskContextCallerContexts(t, secondBody)
	if len(secondContexts) != 1 || secondContexts[0].Name != "Issue release notes" || secondContexts[0].Text != "issue snippet" {
		t.Fatalf("issue contexts = %+v", secondContexts)
	}
	h.pump.do(func() {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		if len(issue.Context.snapshot()) != 0 {
			t.Fatalf("accepted issue context was not cleared: %+v", issue.Context.snapshot())
		}
		execution := issue.Executions[len(issue.Executions)-1]
		if len(execution.Context) != 1 || execution.Context[0].Text != "issue snippet" {
			t.Fatalf("issue execution snapshot = %+v", execution.Context)
		}
		if ct.Draft != "ordinary conversation draft" {
			t.Fatalf("issue run changed conversation draft: %q", ct.Draft)
		}
	})
	issueHistory, err := h.app.rt.historyPage(h.app.selectedContact().issueByID(issueID).WorkSessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if contexts := taskContextHistoryCallerContexts(t, issueHistory); len(contexts) != 1 || contexts[0].Text != "issue snippet" {
		t.Fatalf("issue history contexts = %+v", contexts)
	}
}

func lastMessageText(messages []*message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Kind == msgContact && messages[index].Text != "" {
			return messages[index].Text
		}
	}
	return ""
}

func TestTaskContextGUIAppPendingContextSurvivesRestartAndAcceptedClearPersists(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{}
	h := startGUIHarness(t, binary, provider, "task-context-restart-117")
	filePath := filepath.Join(h.root, "restart-brief.txt")
	fileText := "restart snapshot\nsecond line"
	if err := os.WriteFile(filePath, []byte(fileText), 0o600); err != nil {
		t.Fatal(err)
	}
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if _, err := ct.Context.AddSnippet("Restart notes", "restart snippet"); err != nil {
			t.Fatal(err)
		}
		if _, err := ct.Context.ImportFile(filePath, ""); err != nil {
			t.Fatal(err)
		}
	})
	var source string
	h.pump.do(func() { source = h.app.selectedContact().Context.snapshot()[1].Source })
	h.shutdown(t)

	h2 := restartGUIHarness(t, binary)
	defer func() {
		if !h2.closed {
			h2.shutdown(t)
		}
	}()
	h2.pump.do(func() {
		ct := h2.app.selectedContact()
		items := ct.Context.snapshot()
		if len(items) != 2 || items[0].Name != "Restart notes" || items[1].Text != fileText || items[1].Source != source {
			t.Fatalf("restart pending context = %+v", items)
		}
		ct.Draft = "Send restored context."
		if !h2.app.sendCurrent() {
			t.Fatalf("restart send: %s", ct.SendError)
		}
	})
	h2.pump.wait(func() bool {
		ct := h2.app.selectedContact()
		return ct.LastTaskID != "" && ct.ActiveTaskID == "" && ct.SendState == sendIdle
	})
	if provider.count() != 1 {
		t.Fatalf("restart send provider requests = %d", provider.count())
	}
	body := provider.body(0)
	if contexts := taskContextCallerContexts(t, body); len(contexts) != 2 {
		t.Fatalf("restart contexts = %+v", contexts)
	}
	h2.pump.do(func() {
		ct := h2.app.selectedContact()
		if len(ct.Context.snapshot()) != 0 {
			t.Fatalf("accepted restart context was not cleared: %+v", ct.Context.snapshot())
		}
	})
	h2.shutdown(t)

	h3 := restartGUIHarness(t, binary)
	defer func() {
		if !h3.closed {
			h3.shutdown(t)
		}
	}()
	h3.pump.wait(func() bool {
		ct := h3.app.contacts[0]
		return ct.Loaded && len(ct.Messages) > 0
	})
	h3.pump.do(func() {
		ct := h3.app.selectedContact()
		if len(ct.Context.snapshot()) != 0 {
			t.Fatalf("accepted clear did not persist: %+v", ct.Context.snapshot())
		}
		found := false
		for _, m := range ct.Messages {
			if len(m.Context) == 2 && m.Context[0].Text == "restart snippet" {
				found = true
			}
		}
		if !found {
			t.Fatalf("runtime history did not restore conversation context: %+v", ct.Messages)
		}
	})
	h3.shutdown(t)
}

func TestTaskContextGUIAppUnknownSendRestartKeepsWarningWithoutReplay(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{}
	h := startGUIHarness(t, binary, provider, "task-context-unknown-117")
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "Establish the session."
		if !h.app.sendCurrent() {
			t.Fatalf("initial send: %s", ct.SendError)
		}
	})
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.LastTaskID != "" && ct.ActiveTaskID == "" && ct.SendState == sendIdle
	})
	if provider.count() != 1 {
		t.Fatalf("initial provider requests = %d", provider.count())
	}
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if _, err := ct.Context.AddSnippet("Maybe sent", "possibly delivered"); err != nil {
			t.Fatal(err)
		}
		ct.pendingSend = &pendingSend{Draft: "uncertain prompt", Context: ct.Context.snapshot(), State: "unknown"}
		ct.SendState = sendUnknown
		ct.SendError = "Delivery not confirmed."
		if err := h.app.persist(); err != nil {
			t.Fatal(err)
		}
	})
	h.shutdown(t)

	h2 := restartGUIHarness(t, binary)
	defer func() {
		if !h2.closed {
			h2.shutdown(t)
		}
	}()
	h2.pump.wait(func() bool { return h2.app.contacts[0].Loaded })
	h2.pump.do(func() {
		ct := h2.app.selectedContact()
		if ct.SendState != sendUnknown || ct.pendingSend == nil || ct.pendingSend.State != "unknown" {
			t.Fatalf("restart lost unknown send intent: state=%v pending=%+v", ct.SendState, ct.pendingSend)
		}
		if len(ct.Context.snapshot()) != 1 || !strings.Contains(ct.SendError, "may have been sent") {
			t.Fatalf("restart context/warning = %+v / %q", ct.Context.snapshot(), ct.SendError)
		}
	})
	if provider.count() != 1 {
		t.Fatalf("restart replayed a send: %d provider requests", provider.count())
	}
	h2.shutdown(t)
}
