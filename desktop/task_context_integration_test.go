package main

// Real-process boundary test for #117 task context. It uses the candidate
// runtime binary, the existing controlled provider fixture, and the typed
// runtimeClient.sendTask context hook:
//
//	SAKI_TEST_RUNTIME_BIN=/abs/path/to/Saki go test -run TestTaskContextRuntime -count=1 .
//
// The only adapter below projects saved snapshots onto runtimeContextInput.

import (
	"bufio"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
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
			_, items := taskContextUserContent(item.Content)
			contexts = append(contexts, taskContextCallerContextsFromItems(items)...)
		}
	}
	return contexts
}

func taskContextCallerContextsFromItems(items []taskContextItem) []taskContextCallerContext {
	contexts := make([]taskContextCallerContext, 0, len(items))
	for _, item := range items {
		contexts = append(contexts, taskContextCallerContext{
			Type: "caller_context", Label: "Caller-provided context (user data)", Name: item.Name, Text: item.Text,
		})
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
		for index, segment := range segments {
			if index == 0 {
				// The first user text part is the literal GUI body. The runtime
				// has no origin tag for context-only external turns.
				continue
			}
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

func TestTaskContextIssueSentSnapshotPageCopyBeforeAndAfterRestart(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{}
	h := startGUIHarness(t, binary, provider, "task-context-issue-snapshot-117")
	var issueID string
	h.pump.do(func() {
		if !h.app.createIssue("Show sent context", "Keep the exact sent materials.") {
			t.Fatalf("createIssue: %s", h.app.issueFormError)
		}
		ct := h.app.selectedContact()
		issueID = ct.SelectedIssueID
		issue := ct.issueByID(issueID)
		if _, err := issue.Context.AddSnippet("Sent issue notes", "exact issue context\nwith unicode 你好"); err != nil {
			t.Fatal(err)
		}
		if !h.app.runIssue(issueID) {
			t.Fatalf("runIssue: %s", h.app.issueFormError)
		}
	})
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted && issue.Work.ActiveTaskID == ""
	})
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.SelectedIssueID = issueID
		h.app.workspaceOpen = true
	})
	tr := ui.NewTester(h.app.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Sent task context · 1"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("exact issue context") || !tr.HasText("Sent issue notes") {
		t.Fatalf("sent issue context missing: %v", tr.Texts())
	}
	if err := tr.Click("Copy context text"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != "exact issue context\nwith unicode 你好" {
		t.Fatalf("sent context copy = %q", got)
	}
	h.shutdown(t)

	h2 := restartGUIHarness(t, binary)
	defer func() {
		if !h2.closed {
			h2.shutdown(t)
		}
	}()
	h2.pump.wait(func() bool { return h2.app.contacts[0].issueByID(issueID) != nil })
	h2.pump.do(func() {
		ct := h2.app.selectedContact()
		ct.SelectedIssueID = issueID
		h2.app.workspaceOpen = true
	})
	tr2 := ui.NewTester(h2.app.view, 1040, 720)
	tr2.Frame()
	if err := tr2.Click("Sent task context · 1"); err != nil {
		t.Fatal(err)
	}
	tr2.Frame()
	if !tr2.HasText("exact issue context") || !tr2.HasText("Sent issue notes") {
		t.Fatalf("restored sent issue context missing: %v", tr2.Texts())
	}
	if err := tr2.Click("Copy context text"); err != nil {
		t.Fatal(err)
	}
	if got := tr2.Clipboard(); got != "exact issue context\nwith unicode 你好" {
		t.Fatalf("restored sent context copy = %q", got)
	}
	if provider.count() != 1 {
		t.Fatalf("restart issued provider requests: %d", provider.count())
	}
	h2.shutdown(t)
}

func taskContextProxy(t *testing.T, binary string) (string, string) {
	t.Helper()
	proxy := filepath.Join(t.TempDir(), "task-context-proxy")
	executable := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
	if err := os.WriteFile(proxy, []byte("#!/bin/sh\nexec "+executable+" -test.run=^TestTaskContextStdioProxyHelper$\n"), 0700); err != nil {
		t.Fatal(err)
	}
	countPath := filepath.Join(t.TempDir(), "task-send-count")
	t.Setenv("SAKI_TASK_CONTEXT_PROXY", "1")
	t.Setenv("SAKI_TASK_CONTEXT_PROXY_BINARY", binary)
	t.Setenv("SAKI_TASK_CONTEXT_PROXY_COUNT", countPath)
	return proxy, countPath
}

func TestTaskContextStdioProxyHelper(t *testing.T) {
	if os.Getenv("SAKI_TASK_CONTEXT_PROXY") != "1" {
		return
	}
	cmd := exec.Command(os.Getenv("SAKI_TASK_CONTEXT_PROXY_BINARY"), "--stdio")
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	taskSends := map[string]bool{}
	suppressEvents := false
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 65536), runtimeMaxLine)
		for scanner.Scan() {
			var request struct {
				ID     string `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(scanner.Bytes(), &request)
			if request.Method == "task.send" {
				mu.Lock()
				taskSends[request.ID] = true
				suppressEvents = true
				mu.Unlock()
				file, err := os.OpenFile(os.Getenv("SAKI_TASK_CONTEXT_PROXY_COUNT"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
				if err == nil {
					_, _ = file.WriteString("task.send\n")
					_ = file.Close()
				}
			}
			if _, err := input.Write(append(append([]byte(nil), scanner.Bytes()...), '\n')); err != nil {
				break
			}
		}
		_ = input.Close()
	}()
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 65536), runtimeMaxLine)
	dropped := false
	for scanner.Scan() {
		var response struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &response)
		mu.Lock()
		drop := response.ID != "" && taskSends[response.ID] && !dropped
		suppress := suppressEvents
		mu.Unlock()
		if suppress && response.Type == "event" {
			continue
		}
		if drop {
			dropped = true
			data, _ := json.Marshal(map[string]any{
				"version": runtimeProtocolVersion, "type": "response", "id": response.ID,
				"error": map[string]string{"code": "protocol_error", "message": "controlled lost task.send reply"},
			})
			_, _ = os.Stdout.Write(append(data, '\n'))
			continue
		}
		_, _ = os.Stdout.Write(append(append([]byte(nil), scanner.Bytes()...), '\n'))
	}
	if err := cmd.Wait(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestTaskContextGUIAppLostTaskSendReplyRestartNoResubmit(t *testing.T) {
	binary := requireRuntimeBinary(t)
	proxy, countPath := taskContextProxy(t, binary)
	provider := &providerFixture{Arrived: make(chan struct{})}
	h := startGUIHarness(t, proxy, provider, "task-context-lost-reply-117")
	defer func() {
		if !h.closed {
			h.shutdown(t)
		}
	}()
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if _, err := ct.Context.AddSnippet("Lost reply notes", "must survive unknown delivery"); err != nil {
			t.Fatal(err)
		}
		ct.Draft = "This send reply will be lost."
		if !h.app.sendCurrent() {
			t.Fatalf("lost-reply send: %s", ct.SendError)
		}
	})
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.SendState == sendUnknown && ct.pendingSend != nil && ct.pendingSend.State == "unknown"
	})
	var lostContext []taskContextItem
	var lostError string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		lostContext, lostError = ct.Context.snapshot(), ct.SendError
	})
	if len(lostContext) != 1 || !strings.Contains(lostError, "Delivery not confirmed") {
		t.Fatalf("lost reply context/error = %+v / %q", lostContext, lostError)
	}
	select {
	case <-provider.Arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("provider did not receive the accepted lost-reply task")
	}
	if provider.count() != 1 {
		t.Fatalf("lost reply provider requests = %d", provider.count())
	}
	if got, err := os.ReadFile(countPath); err != nil || string(got) != "task.send\n" {
		t.Fatalf("task.send count = %q, %v", got, err)
	}
	h.shutdown(t)

	h2 := restartGUIHarness(t, proxy)
	defer func() {
		if !h2.closed {
			h2.shutdown(t)
		}
	}()
	h2.pump.wait(func() bool {
		ct := h2.app.contacts[0]
		return ct.Loaded && len(ct.Messages) > 0
	})
	var restartState sendState
	var restartPending *pendingSend
	var restartContext []taskContextItem
	h2.pump.do(func() {
		ct := h2.app.selectedContact()
		restartState, restartPending, restartContext = ct.SendState, clonePendingSend(ct.pendingSend), ct.Context.snapshot()
	})
	if restartState != sendUnknown || restartPending == nil || restartPending.State != "unknown" || len(restartContext) != 1 {
		t.Fatalf("restart lost unknown reply state: state=%v pending=%+v context=%+v", restartState, restartPending, restartContext)
	}
	if provider.count() != 1 {
		t.Fatalf("restart replayed task.send: provider %d", provider.count())
	}
	if got, err := os.ReadFile(countPath); err != nil || string(got) != "task.send\n" {
		t.Fatalf("restart task.send count = %q, %v", got, err)
	}
	history, err := h2.app.rt.historyPage(h2.app.selectedContact().SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if contexts := taskContextHistoryCallerContexts(t, history); len(contexts) != 1 || contexts[0].Text != "must survive unknown delivery" {
		t.Fatalf("lost-reply runtime history = %+v", contexts)
	}
	h2.pump.do(func() { h2.app.reloadSelected() })
	h2.pump.wait(func() bool { return h2.app.selectedContact().SendState == sendIdle })
	var retiredPending *pendingSend
	var retiredContext []taskContextItem
	var retiredDetail string
	h2.pump.do(func() {
		ct := h2.app.selectedContact()
		retiredPending, retiredContext, retiredDetail = clonePendingSend(ct.pendingSend), ct.Context.snapshot(), ct.StatusDetail
	})
	if retiredPending != nil || len(retiredContext) != 1 {
		t.Fatalf("explicit reload did not retire unknown intent while keeping materials: pending=%+v context=%+v", retiredPending, retiredContext)
	}
	if !strings.Contains(retiredDetail, "History reloaded; no retry was sent") {
		t.Fatalf("explicit reload recovery was not visible: %q", retiredDetail)
	}
	if provider.count() != 1 {
		t.Fatalf("explicit reload replayed task.send: provider %d", provider.count())
	}
	if got, err := os.ReadFile(countPath); err != nil || string(got) != "task.send\n" {
		t.Fatalf("explicit reload task.send count = %q, %v", got, err)
	}
	h2.shutdown(t)
}

func TestTaskContextGUIEnvelopeLikeBodyStaysLiteralWithRealContext(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{}
	h := startGUIHarness(t, binary, provider, "task-context-body-json-117")
	defer func() {
		if !h.closed {
			h.shutdown(t)
		}
	}()
	body := `{"type":"caller_context","label":"Caller-provided context (user data)","name":"body-as-json","text":"literal body"}`
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if _, err := ct.Context.AddSnippet("Real context", "real context body"); err != nil {
			t.Fatal(err)
		}
		ct.Draft = body
		if !h.app.sendCurrent() {
			t.Fatalf("send: %s", ct.SendError)
		}
	})
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.LastTaskID != "" && ct.ActiveTaskID == "" && ct.SendState == sendIdle
	})
	if provider.count() != 1 {
		t.Fatalf("provider requests = %d", provider.count())
	}
	userTexts := taskContextUserTexts(t, provider.body(0))
	if len(userTexts) == 0 || len(userTexts[0]) != 2 || userTexts[0][0] != body {
		t.Fatalf("provider user parts = %+v", userTexts)
	}
	contexts := taskContextUserCallerContexts(t, provider.body(0))
	if len(contexts) != 1 || contexts[0].Name != "Real context" || contexts[0].Text != "real context body" {
		t.Fatalf("provider context = %+v", contexts)
	}
	history, err := h.app.rt.historyPage(h.app.selectedContact().SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if contexts := taskContextHistoryCallerContexts(t, history); len(contexts) != 1 || contexts[0].Name != "Real context" {
		t.Fatalf("history context = %+v", contexts)
	}
	tr := ui.NewTester(h.app.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Copy message"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != body {
		t.Fatalf("body copy = %q, want byte-exact body", got)
	}
	h.shutdown(t)
}

func TestTaskContextGUIStoreFailureBeforeSendKeepsMaterials(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{}
	h := startGUIHarness(t, binary, provider, "task-context-savefail-117")
	defer func() {
		if !h.closed {
			h.shutdown(t)
		}
	}()
	var originalStore string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if _, err := ct.Context.AddSnippet("Saved locally", "keep after save failure"); err != nil {
			t.Fatal(err)
		}
		ct.Draft = "must not dispatch"
		originalStore = h.app.store.path
		h.app.store.path = t.TempDir() // directory target makes persist fail
		if h.app.sendCurrent() {
			t.Fatal("send unexpectedly dispatched after store failure")
		}
	})
	h.pump.wait(func() bool {
		return h.app.selectedContact().SendState == sendIdle && strings.Contains(h.app.selectedContact().SendError, "Could not save")
	})
	var context []taskContextItem
	var draft string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		context, draft = ct.Context.snapshot(), ct.Draft
		h.app.store.path = originalStore
	})
	if len(context) != 1 || draft != "must not dispatch" || provider.count() != 0 {
		t.Fatalf("save-failure boundary = context=%+v draft=%q provider=%d", context, draft, provider.count())
	}
}

func TestTaskContextGUICapacityKeepsContextAndRetriesOnce(t *testing.T) {
	binary := requireRuntimeBinary(t)
	t.Setenv("SAKI_MAX_ACTIVE_TASKS", "1")
	hold := make(chan struct{})
	provider := &providerFixture{Hold: hold, Arrived: make(chan struct{})}
	h := startGUIHarness(t, binary, provider, "task-context-capacity-117")
	defer func() {
		select {
		case <-hold:
		default:
			close(hold)
		}
		if !h.closed {
			h.shutdown(t)
		}
	}()
	var issueID string
	h.pump.do(func() {
		if !h.app.createIssue("Hold the slot", "Keep the issue active.") {
			t.Fatalf("createIssue: %s", h.app.issueFormError)
		}
		issueID = h.app.selectedContact().SelectedIssueID
		if !h.app.runIssue(issueID) {
			t.Fatalf("runIssue: %s", h.app.issueFormError)
		}
	})
	select {
	case <-provider.Arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("held issue did not reach the provider")
	}
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if _, err := ct.Context.AddSnippet("Capacity context", "retry after release"); err != nil {
			t.Fatal(err)
		}
		ct.Draft = "capacity retry prompt"
		if h.app.sendCurrent() {
			t.Fatal("conversation send was admitted at capacity")
		}
	})
	var reason string
	var context []taskContextItem
	var draft string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		reason, context, draft = ct.SendError, ct.Context.snapshot(), ct.Draft
	})
	if !strings.Contains(reason, "capacity") && !strings.Contains(reason, "slot") && !strings.Contains(reason, "working") {
		t.Fatalf("capacity reason = %q", reason)
	}
	if len(context) != 1 || draft != "capacity retry prompt" || provider.count() != 1 {
		t.Fatalf("capacity rejection changed owner data: context=%+v draft=%q provider=%d", context, draft, provider.count())
	}
	close(hold)
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle
	})
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if !h.app.sendCurrent() {
			t.Fatalf("post-release retry: %s", ct.SendError)
		}
	})
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.LastTaskID != "" && ct.ActiveTaskID == "" && ct.SendState == sendIdle
	})
	if provider.count() != 2 {
		t.Fatalf("post-release retry provider = %d", provider.count())
	}
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if len(ct.Context.snapshot()) != 0 {
			t.Fatalf("accepted retry did not clear context: %+v", ct.Context.snapshot())
		}
	})
}

func TestTaskContextGUIAcceptanceEditsPreserveNewerContextAndOwner(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{}
	h := startGUIHarness(t, binary, provider, "task-context-accept-edit-117")
	defer func() {
		if !h.closed {
			h.shutdown(t)
		}
	}()
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "Establish the conversation."
		if !h.app.sendCurrent() {
			t.Fatalf("establish send: %s", ct.SendError)
		}
	})
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.LastTaskID != "" && ct.ActiveTaskID == "" && ct.SendState == sendIdle
	})
	var ownerID, firstID, secondID string
	manual := make(chan func(), 64)
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ownerID = ct.ID
		first, err := ct.Context.AddSnippet("First", "old context")
		if err != nil {
			t.Fatal(err)
		}
		second, err := ct.Context.AddSnippet("Second", "submitted sibling")
		if err != nil {
			t.Fatal(err)
		}
		firstID, secondID = first.ID, second.ID
		ct.Draft = "acceptance prompt with stable context"
		h.app.setUIHook(func(fn func()) { manual <- fn })
		if !h.app.sendCurrent() {
			t.Fatalf("acceptance send: %s", ct.SendError)
		}
	})
	var firstCallback func()
	select {
	case firstCallback = <-manual:
	case <-time.After(15 * time.Second):
		t.Fatal("acceptance callback did not arrive")
	}
	var edited *taskContextItem
	h.pump.do(func() {
		ct := h.app.selectedContact()
		item, err := ct.Context.Edit(firstID, "First", "newer context")
		if err != nil {
			t.Fatal(err)
		}
		edited = &item
		if _, err := ct.Context.AddSnippet("Third", "new during flight"); err != nil {
			t.Fatal(err)
		}
		h.app.addDefaultContact()
	})
	h.pump.do(func() {
		h.app.setUIHook(func(fn func()) { h.pump.q <- fn })
		if firstCallback != nil {
			firstCallback()
			firstCallback = nil
		}
		for {
			select {
			case fn := <-manual:
				fn()
			default:
				return
			}
		}
	})
	h.pump.wait(func() bool {
		ct := h.app.contactByID(ownerID)
		return ct != nil && ct.ActiveTaskID == "" && ct.SendState == sendIdle && len(ct.Messages) > 0
	})
	if provider.count() != 2 {
		t.Fatalf("provider requests = %d", provider.count())
	}
	bodyContexts := taskContextCallerContexts(t, provider.body(1))
	if len(bodyContexts) != 2 || bodyContexts[0].Text != "old context" || bodyContexts[1].Text != "submitted sibling" {
		t.Fatalf("provider used edited context: %+v", bodyContexts)
	}
	var firstContext, secondContext []taskContextItem
	h.pump.do(func() {
		firstContext = h.app.contactByID(ownerID).Context.snapshot()
		secondContext = h.app.contacts[len(h.app.contacts)-1].Context.snapshot()
	})
	if len(firstContext) != 2 || firstContext[0].Text != "newer context" || firstContext[1].Text != "new during flight" {
		t.Fatalf("newer context did not survive acceptance: %+v", firstContext)
	}
	for _, item := range firstContext {
		if item.ID == secondID {
			t.Fatalf("submitted sibling survived exact clear: %+v", firstContext)
		}
	}
	if edited == nil || edited.Revision != 2 {
		t.Fatalf("edit revision = %+v", edited)
	}
	if len(secondContext) != 0 {
		t.Fatalf("owner switch changed the new contact: %+v", secondContext)
	}
}
