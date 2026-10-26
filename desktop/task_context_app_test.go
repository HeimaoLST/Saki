package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func taskContextApp(t *testing.T) (*appModel, *contact) {
	t.Helper()
	store, err := openContactStore(filepath.Join(t.TempDir(), "contacts.json"), "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	ct := &contact{ID: "saki-context", Name: "Saki", Role: "Assistant", SessionID: "s1", Loaded: true}
	a := &appModel{
		store: store, runtimeStatus: runtimeStateReady, workspaceOpen: true,
		windowWidth: 1040, scrollTo: -1, contacts: []*contact{ct}, selected: 0,
	}
	a.bindTaskContexts()
	return a, ct
}

func TestTaskContextStoreRoundTripAndOwnedLists(t *testing.T) {
	a, ct := taskContextApp(t)
	contactItem, err := ct.Context.AddSnippet("Contact notes", "contact text")
	if err != nil {
		t.Fatal(err)
	}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "Goal", Status: issueStatusNotStarted}
	issue.Context.onChange = a.persist
	issueItem, err := issue.Context.AddSnippet("Issue notes", "issue text")
	if err != nil {
		t.Fatal(err)
	}
	execution := &issueExecution{ID: "e1", Context: []taskContextItem{issueItem}}
	issue.Executions = []*issueExecution{execution}
	ct.Issues = []*productIssue{issue}
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}

	store, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if len(store.state.Contacts) != 1 || len(store.state.Contacts[0].TaskContext) != 1 || store.state.Contacts[0].TaskContext[0].ID != contactItem.ID {
		t.Fatalf("stored contact context = %+v", store.state.Contacts)
	}
	storedIssue := store.state.Issues[0]
	if len(storedIssue.TaskContext) != 1 || storedIssue.TaskContext[0].Text != "issue text" {
		t.Fatalf("stored issue context = %+v", storedIssue)
	}
	if len(storedIssue.Executions) != 1 || len(storedIssue.Executions[0].Context) != 1 || storedIssue.Executions[0].Context[0].Text != "issue text" {
		t.Fatalf("stored execution context = %+v", storedIssue.Executions)
	}

	reloaded := &appModel{contacts: contactsFromStore(store, "/tmp")}
	reloaded.loadStoredTaskContexts(store)
	reloaded.bindTaskContexts()
	if len(reloaded.contacts[0].Context.snapshot()) != 1 || reloaded.contacts[0].Context.snapshot()[0].Text != "contact text" {
		t.Fatalf("reloaded contact context = %+v", reloaded.contacts[0].Context.snapshot())
	}
	reloadedIssue := reloaded.contacts[0].issueByID("i1")
	if reloadedIssue == nil || len(reloadedIssue.Context.snapshot()) != 1 || reloadedIssue.Executions[0].Context[0].Text != "issue text" {
		t.Fatalf("reloaded issue = %+v", reloadedIssue)
	}
}

func TestTaskContextAcceptedSendClearsOnlyExactSubmittedVersions(t *testing.T) {
	a, ct := taskContextApp(t)
	first, err := ct.Context.AddSnippet("First", "old first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ct.Context.AddSnippet("Second", "remove me")
	if err != nil {
		t.Fatal(err)
	}
	submitted := ct.Context.snapshot()
	if _, err := ct.Context.Edit(first.ID, first.Name, "newer first"); err != nil {
		t.Fatal(err)
	}
	ct.Draft = "prompt"
	ct.SubmittedRev = ct.draftRev
	ct.submittedContext = submitted
	a.acceptTask(ct, "prompt", runtimeSendResult{TaskID: "t1", MessageID: "m1"})

	left := ct.Context.snapshot()
	if len(left) != 1 || left[0].ID != first.ID || left[0].Text != "newer first" || left[0].Revision != 2 {
		t.Fatalf("accepted clear changed newer context: %+v", left)
	}
	if _, ok := ct.Context.itemByID(second.ID); ok {
		t.Fatal("exact submitted sibling was not removed")
	}
	if len(ct.Messages) != 1 || len(ct.Messages[0].Context) != 2 || ct.Messages[0].Context[0].Text != "old first" {
		t.Fatalf("live message context = %+v", ct.Messages)
	}
	if ct.Messages[0].Context[0].Revision != submitted[0].Revision || ct.Messages[0].Context[1].Text != "remove me" {
		t.Fatalf("message did not retain the exact submitted values: %+v", ct.Messages[0].Context)
	}
	if ct.pendingSend != nil {
		t.Fatalf("accepted send left pending intent: %+v", ct.pendingSend)
	}
}

func TestTaskContextAcceptedSaveFailureKeepsNextDraftVisible(t *testing.T) {
	a, ct := taskContextApp(t)
	item, err := ct.Context.AddSnippet("Keep", "keep after acceptance")
	if err != nil {
		t.Fatal(err)
	}
	ct.submittedContext = ct.Context.snapshot()
	a.store = &contactStore{path: t.TempDir()}
	a.acceptTask(ct, "prompt", runtimeSendResult{TaskID: "t1", MessageID: "m1"})
	if got := ct.Context.snapshot(); len(got) != 1 || got[0].ID != item.ID {
		t.Fatalf("accepted+savefailure cleared user data: %+v", got)
	}
	if !strings.Contains(ct.SendError, "accepted") {
		t.Fatalf("accepted save failure was not explicit: %q", ct.SendError)
	}
	if ct.SendState != sendAccepted || ct.ActiveTaskID != "t1" {
		t.Fatalf("accepted state was not retained: state=%v task=%q", ct.SendState, ct.ActiveTaskID)
	}
}

func TestTaskContextRejectedSendKeepsMaterials(t *testing.T) {
	a, ct := taskContextApp(t)
	if _, err := ct.Context.AddSnippet("Keep", "rejected context"); err != nil {
		t.Fatal(err)
	}
	ct.submittedContext = ct.Context.snapshot()
	a.failSubmit(ct.ID, &runtimeError{Code: "capacity", Message: "full"}, false)
	if len(ct.Context.snapshot()) != 1 || len(ct.submittedContext) != 0 {
		t.Fatalf("rejected send changed context: %+v", ct.Context.snapshot())
	}
	if ct.SendState != sendIdle {
		t.Fatalf("confirmed rejection state = %v", ct.SendState)
	}
}

func TestTaskContextUnknownFailurePersistsPendingIntent(t *testing.T) {
	a, ct := taskContextApp(t)
	if _, err := ct.Context.AddSnippet("Maybe sent", "possibly delivered"); err != nil {
		t.Fatal(err)
	}
	ct.pendingSend = &pendingSend{Draft: "uncertain prompt", Context: ct.Context.snapshot(), State: "submitting"}
	ct.submittedContext = ct.Context.snapshot()
	ct.SubmittedDraft = "uncertain prompt"
	a.failSubmit(ct.ID, &runtimeError{Code: "protocol_error", Message: "disconnected"}, true)
	if ct.SendState != sendUnknown || ct.pendingSend == nil || ct.pendingSend.State != "unknown" {
		t.Fatalf("unknown failure = state %v pending %+v", ct.SendState, ct.pendingSend)
	}
	if len(ct.Context.snapshot()) != 1 {
		t.Fatalf("unknown failure lost context: %+v", ct.Context.snapshot())
	}
	store, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if store.state.Contacts[0].PendingSend == nil || store.state.Contacts[0].PendingSend.State != "unknown" {
		t.Fatalf("unknown intent was not persisted: %+v", store.state.Contacts[0].PendingSend)
	}
}

func TestTaskContextIssueAcceptedClearsPendingOnly(t *testing.T) {
	a, ct := taskContextApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "Goal", Status: issueStatusInProgress}
	issue.Context.onChange = a.persist
	item, err := issue.Context.AddSnippet("Issue notes", "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	issue.Context.openSnippetForm()
	issue.Context.formName = "next run draft"
	execution := &issueExecution{ID: "e1", Context: []taskContextItem{item}}
	issue.Executions = []*issueExecution{execution}
	ct.Issues = []*productIssue{issue}
	a.acceptIssueTask(ct, issue, execution, "prompt", runtimeSendResult{TaskID: "t1", MessageID: "m1"})
	if got := issue.Context.snapshot(); len(got) != 0 {
		t.Fatalf("accepted issue context was not cleared: %+v", got)
	}
	if len(execution.Context) != 1 || execution.Context[0].Text != "snapshot" {
		t.Fatalf("execution context was not immutable: %+v", execution.Context)
	}
	if !issue.Context.formOpen || issue.Context.formName != "next run draft" {
		t.Fatal("accepted clear destroyed an unsaved next-run editor")
	}
}

func TestTaskContextUserContentSeparatesOnlyActualEnvelope(t *testing.T) {
	envelope, err := json.Marshal(map[string]string{
		"type": "caller_context", "label": "Caller-provided context (user data)",
		"name": "notes · file /tmp/notes.txt", "text": "exact\nbody",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`[{"type":"text","text":"main text"},{"type":"text","text":` + string(mustJSON(t, string(envelope))) + `}]`)
	text, contexts := taskContextUserContent(raw)
	if text != "main text" || len(contexts) != 1 || contexts[0].Name != "notes · file /tmp/notes.txt" || contexts[0].Text != "exact\nbody" {
		t.Fatalf("parsed = %q / %+v", text, contexts)
	}
	ordinary := []byte(`[{"type":"text","text":"{\"type\":\"caller_context\",\"name\":\"ordinary\"}"}]`)
	text, contexts = taskContextUserContent(ordinary)
	if text != `{"type":"caller_context","name":"ordinary"}` || len(contexts) != 0 {
		t.Fatalf("ordinary JSON became context: %q / %+v", text, contexts)
	}
	text, contexts = taskContextUserContent([]byte(`"main only"`))
	if text != "main only" || len(contexts) != 0 {
		t.Fatalf("plain content = %q / %+v", text, contexts)
	}
}

func mustJSON(t *testing.T, text string) []byte {
	t.Helper()
	data, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestTaskContextAppModalAndReadOnlyHistoryCopy(t *testing.T) {
	a, ct := taskContextApp(t)
	if _, err := ct.Context.AddSnippet("Conversation notes", "conversation body"); err != nil {
		t.Fatal(err)
	}
	a.openConversationTaskContext()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	for _, want := range []string{"Task context", "Conversation · Saki", "Conversation notes", "Close task context"} {
		if !tr.HasText(want) {
			t.Fatalf("conversation modal missing %q: %v", want, tr.Texts())
		}
	}

	ct.Messages = nil
	ct.History = []historyEntry{{
		ID: "hist:readonly", From: "me", Text: "history prompt",
		Context: []taskContextItem{{Name: "Doc · file /tmp/doc.txt", Text: "history exact"}},
	}}
	a.aux = ""
	a.openHistory()
	tr.Frame()
	if !tr.HasText("history prompt") || !tr.HasText("history exact") || !tr.HasText("Doc · file /tmp/doc.txt") {
		t.Fatalf("history context missing: %v", tr.Texts())
	}
	if err := tr.Click("Copy context text"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != "history exact" {
		t.Fatalf("history context copy = %q", got)
	}
	if err := tr.Click("Close history"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	a.screen = viewChat
	ct.Messages = []*message{{
		Key: "live:readonly", Kind: msgUser, Text: "main prompt",
		Context: []taskContextItem{{Name: "Doc · file /tmp/doc.txt", Text: "read only text"}},
	}}
	a.aux = ""
	tr = ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("main prompt") || !tr.HasText("read only text") {
		t.Fatalf("live context missing: %v", tr.Texts())
	}
}

func TestTaskContextRefreshStaysInOwningContactAndIssue(t *testing.T) {
	a, ct := taskContextApp(t)
	second := &contact{ID: "second", Name: "Mio", Role: "Assistant"}
	a.contacts = append(a.contacts, second)
	a.bindTaskContexts()
	path := filepath.Join(t.TempDir(), "shared.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	contactItem, err := ct.Context.ImportFile(path, "Contact file")
	if err != nil {
		t.Fatal(err)
	}
	secondItem, err := second.Context.ImportFile(path, "Second file")
	if err != nil {
		t.Fatal(err)
	}
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "Goal"}
	issue.Context.onChange = a.persist
	issueItem, err := issue.Context.ImportFile(path, "Issue file")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ct.Context.Refresh(contactItem.ID); err != nil {
		t.Fatal(err)
	}
	if got := ct.Context.snapshot()[0]; got.Text != "new" || got.Revision != 2 {
		t.Fatalf("contact refresh = %+v", got)
	}
	if got := second.Context.snapshot()[0]; got.Text != "old" || got.Revision != 1 || got.ID != secondItem.ID {
		t.Fatalf("refresh leaked into second contact: %+v", got)
	}
	if got := issue.Context.snapshot()[0]; got.Text != "old" || got.Revision != 1 || got.ID != issueItem.ID {
		t.Fatalf("refresh leaked into issue: %+v", got)
	}
}

func TestTaskContextHistoryProjectionUsesOnlyRealEnvelope(t *testing.T) {
	contextJSON, err := json.Marshal(map[string]string{
		"type": "caller_context", "label": "Caller-provided context (user data)",
		"name": "Doc · file /tmp/doc.txt", "text": "history body",
	})
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal([]any{
		map[string]string{"type": "text", "text": "main prompt"},
		map[string]string{"type": "text", "text": string(contextJSON)},
	})
	if err != nil {
		t.Fatal(err)
	}
	records := []historyRecord{
		{Offset: 0, Record: journalRecord{Type: "task_started", TaskID: "t1", MessageID: "m1"}},
		{Offset: 1, Record: journalRecord{Type: "messages", Items: []inputItem{{
			Type: "message", Role: "user", Content: content,
		}}}},
	}
	projection := parseHistory("s1", records, nil)
	if len(projection.messages) != 1 || len(projection.messages[0].Context) != 1 {
		t.Fatalf("projection = %+v", projection)
	}
	message := projection.messages[0]
	if message.Text != "main prompt" || message.Context[0].Text != "history body" || message.Context[0].Name != "Doc · file /tmp/doc.txt" {
		t.Fatalf("message projection = %+v", message)
	}
	if len(projection.entries) != 1 || len(projection.entries[0].Context) != 1 || projection.entries[0].Text != "main prompt" {
		t.Fatalf("entry projection = %+v", projection.entries)
	}
}

func TestTaskContextUnsavedEditorSurvivesContactSwitch(t *testing.T) {
	a, ct := taskContextApp(t)
	second := &contact{ID: "second", Name: "Mio", Role: "Assistant"}
	second.Context.onChange = a.persist
	a.contacts = append(a.contacts, second)
	ct.Context.openSnippetForm()
	ct.Context.formName = "unsaved editor"
	a.selectContact(1)
	a.selectContact(0)
	if !ct.Context.formOpen || ct.Context.formName != "unsaved editor" {
		t.Fatalf("contact switch lost unsaved editor: %+v", ct.Context)
	}
	if second.Context.count() != 0 || second.Context.formOpen {
		t.Fatalf("editor leaked to another contact: %+v", second.Context)
	}
}

func TestTaskContextAppPreviewScreenshots(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR to write headless app previews")
	}
	path := filepath.Join(t.TempDir(), "release-notes.txt")
	if err := os.WriteFile(path, []byte("Exact saved file text.\nSecond line with Unicode: 你好 — café.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, size := range []struct {
			name          string
			width, height int
		}{{"narrow", 720, 640}, {"wide", 1040, 720}} {
			a, ct := taskContextApp(t)
			if _, err := ct.Context.AddSnippet("Review checklist", "Confirm the exact snippet survives copy and send."); err != nil {
				t.Fatal(err)
			}
			if _, err := ct.Context.ImportFile(path, "Release notes"); err != nil {
				t.Fatal(err)
			}
			a.windowWidth = size.width
			a.openConversationTaskContext()
			tr := ui.NewTester(a.view, size.width, size.height)
			tr.SetDark(dark)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("task-context-app-%s-%s.png", theme, size.name)), tr.Image())
		}
	}
}

func TestTaskContextIssueDetailsOpenOwnEditor(t *testing.T) {
	a, ct := taskContextApp(t)
	issue := &productIssue{ID: "i1", ContactID: ct.ID, Goal: "Ship the issue context", Status: issueStatusNotStarted}
	issue.Context.onChange = a.persist
	if _, err := issue.Context.AddSnippet("Issue notes", "issue-only body"); err != nil {
		t.Fatal(err)
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	a.workspaceOpen = true
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Task context · 1"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	for _, want := range []string{"Issue · Ship the issue context", "Issue notes", "Close task context"} {
		if !tr.HasText(want) {
			t.Fatalf("issue context modal missing %q: %v", want, tr.Texts())
		}
	}
	if ct.Context.count() != 0 {
		t.Fatalf("issue modal edited conversation context: %+v", ct.Context.snapshot())
	}
}

func TestTaskContextExecutionFallbackUsesOnlyItsOwnedSession(t *testing.T) {
	ct := &contact{ID: "c", SessionID: "conversation-session"}
	issue := &productIssue{ID: "i", WorkSessionID: "issue-session"}
	execution := &issueExecution{ID: "e", TaskID: "t1", SessionID: "other-session"}
	ct.Messages = []*message{{TaskID: "t1", Context: []taskContextItem{{Name: "conversation", Text: "wrong"}}}}
	issue.Work.Messages = []*message{{TaskID: "t1", Context: []taskContextItem{{Name: "issue", Text: "wrong"}}}}
	if got := executionTaskContextItems(ct, issue, execution); len(got) != 0 {
		t.Fatalf("arbitrary session leaked context: %+v", got)
	}
	execution.SessionID = "issue-session"
	if got := executionTaskContextItems(ct, issue, execution); len(got) != 1 || got[0].Name != "issue" {
		t.Fatalf("issue session fallback = %+v", got)
	}
	execution.SessionID = "conversation-session"
	if got := executionTaskContextItems(ct, issue, execution); len(got) != 1 || got[0].Name != "conversation" {
		t.Fatalf("conversation session fallback = %+v", got)
	}
}

func TestTaskContextUnknownSendSurvivesStoreRestartWithoutAutoReplay(t *testing.T) {
	a, ct := taskContextApp(t)
	if _, err := ct.Context.AddSnippet("Maybe sent", "possibly delivered"); err != nil {
		t.Fatal(err)
	}
	ct.pendingSend = &pendingSend{
		Draft: "uncertain prompt", Context: ct.Context.snapshot(), State: "unknown",
	}
	ct.SendState = sendUnknown
	ct.SendError = "Delivery not confirmed."
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	store, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if store.state.Version != contactStoreVersion {
		t.Fatalf("store version = %d", store.state.Version)
	}
	reloaded := &appModel{store: store, contacts: contactsFromStore(store, "/tmp")}
	reloaded.loadStoredTaskContexts(store)
	reloaded.bindTaskContexts()
	got := reloaded.contacts[0]
	if got.SendState != sendUnknown || got.pendingSend == nil || got.pendingSend.State != "unknown" {
		t.Fatalf("restart unknown state = state %v pending %+v", got.SendState, got.pendingSend)
	}
	if len(got.Context.snapshot()) != 1 || !strings.Contains(got.SendError, "may have been sent") {
		t.Fatalf("restart context/error = %+v / %q", got.Context.snapshot(), got.SendError)
	}
	if reloaded.rt != nil {
		t.Fatal("restart unknown state created a runtime or replayed a send")
	}
}

func TestTaskContextStoreV5ReadsOldV4WithoutContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	legacy := storedState{
		Version: 4, Selected: "c1",
		Contacts: []storedContact{{ID: "c1", Name: "Saki", Draft: "old draft"}},
	}
	if err := writeStoredState(path, legacy); err != nil {
		t.Fatal(err)
	}
	store, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if len(store.state.Contacts) != 1 || store.state.Contacts[0].Draft != "old draft" || len(store.state.Contacts[0].TaskContext) != 0 {
		t.Fatalf("v4 read = %+v", store.state)
	}
	if err := store.save([]*contact{{ID: "c1", Name: "Saki"}}, "c1"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Version != 5 {
		t.Fatalf("old v4 data was not saved as v5: %d", reloaded.state.Version)
	}
}
