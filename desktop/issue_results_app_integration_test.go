package main

// Real GUI result-page integration: two actual Runtime write/edit runs create
// and modify the same file, then the issue page opens each run from persisted
// execution identity and typed history. The local provider is offline.

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

const issueResultPageWriteFinal = "created result\n\n```text\nwrite\n```"
const issueResultPageEditFinal = "modified result"

type issueResultPageProvider struct {
	requests atomic.Int32
	mu       sync.Mutex
	bodies   []string
}

func (p *issueResultPageProvider) count() int { return int(p.requests.Load()) }

func (p *issueResultPageProvider) body(i int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < 0 || i >= len(p.bodies) {
		return ""
	}
	return p.bodies[i]
}

func (p *issueResultPageProvider) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
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
			response = issueResultPageFinal(issueResultPageWriteFinal)
		case 3:
			response = issueResultFunctionCall("edit", "call-edit", map[string]any{
				"path": "result.txt",
				"edits": []any{
					map[string]any{"oldText": "first", "newText": "second"},
				},
			})
		default:
			response = issueResultPageFinal(issueResultPageEditFinal)
		}
		data, err := json.Marshal(response)
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

func issueResultPageFinal(text string) any {
	return map[string]any{"id": "response", "status": "completed", "output": []any{map[string]any{
		"type": "message", "id": "answer", "status": "completed", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text}},
	}}}
}

func startIssueResultPageApp(t *testing.T) *guiHarness {
	t.Helper()
	app, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	pump := newUIPump(t, app)
	h := &guiHarness{app: app, pump: pump}
	app.startRuntime()
	pump.wait(func() bool {
		return app.runtimeStatus == runtimeStateReady && (app.runtimeCapabilitiesLoaded || app.runtimeCapabilitiesErr != "")
	})
	t.Cleanup(func() {
		if !h.closed {
			h.shutdown(t)
		}
	})
	return h
}

func clickIssueResultAction(tr *ui.Tester, label string) error {
	for attempt := 0; attempt < 12; attempt++ {
		if rect, ok := tr.Find(label); ok && rect.W > 0 && rect.H > 0 && rect.Y >= 0 && rect.Y+rect.H <= 1200 {
			return tr.Click(label)
		}
		tr.Scroll(900, 600, 0, 180)
		tr.Frame()
	}
	return fmt.Errorf("issue result action %q was not reachable: %v", label, tr.Texts())
}

func issueResultTesterRun(h *guiHarness, fn func(*ui.Tester) error) error {
	var err error
	h.pump.do(func() {
		tr := ui.NewTester(h.app.view, 1040, 1200)
		tr.Frame()
		err = fn(tr)
	})
	return err
}

func issueResultClick(h *guiHarness, label string) error {
	return issueResultTesterRun(h, func(tr *ui.Tester) error { return clickIssueResultAction(tr, label) })
}

func issueResultClickCopy(h *guiHarness, label string) (string, error) {
	var got string
	err := issueResultTesterRun(h, func(tr *ui.Tester) error {
		if err := clickIssueResultAction(tr, label); err != nil {
			return err
		}
		got = tr.Clipboard()
		return nil
	})
	return got, err
}

func issueResultTexts(h *guiHarness) []string {
	var texts []string
	h.pump.do(func() {
		tr := ui.NewTester(h.app.view, 1040, 1200)
		tr.Frame()
		texts = append(texts, tr.Texts()...)
	})
	return texts
}

func issueResultTextsHave(texts []string, want string) bool {
	for _, text := range texts {
		if strings.Contains(text, want) {
			return true
		}
	}
	return false
}

func issueResultRequireTexts(t *testing.T, h *guiHarness, want ...string) {
	t.Helper()
	texts := issueResultTexts(h)
	for _, text := range want {
		if !issueResultTextsHave(texts, text) {
			t.Fatalf("missing %q in %v", text, texts)
		}
	}
}

func issueResultPageState(h *guiHarness) (open, loading bool, loadError string, files []issueResultFile, response string) {
	h.pump.do(func() {
		page := h.app.issueResult
		open, loading, loadError, response = page.Open, page.Loading, page.LoadError, page.Panel.Response
		files = append(files, page.Panel.Files...)
	})
	return
}

func TestIssueResultPageRuntimeIntegration(t *testing.T) {
	binary := requireRuntimeBinary(t)
	root := t.TempDir()
	provider := &issueResultPageProvider{}
	server := httptest.NewServer(provider.handler(t))
	defer server.Close()
	sessionDir := filepath.Join(root, "sessions")
	runtimeEnv := append(os.Environ(),
		"API_KEY=offline-fixture",
		"SAKI_API=openai-responses",
		"SAKI_BASE_URL="+server.URL+"/v1",
		"SAKI_MODEL=fixture",
		"SAKI_CONTEXT_TOKENS=131072",
		"SAKI_SESSION_DIR="+sessionDir,
		"SAKI_SESSION=missing",
	)

	live := startIntegrationRuntime(t, binary, root, runtimeEnv)
	session, err := live.client.createSession(root, "issue result page")
	if err != nil {
		t.Fatal(err)
	}
	firstTask, err := live.client.sendTask(session.SessionID, "Create result.txt.")
	if err != nil {
		t.Fatal(err)
	}
	live.finishTask(t, firstTask.TaskID)
	secondTask, err := live.client.sendTask(session.SessionID, "Modify result.txt.")
	if err != nil {
		t.Fatal(err)
	}
	live.finishTask(t, secondTask.TaskID)
	if provider.count() != 4 {
		t.Fatalf("provider requests = %d, want write/edit/final/final", provider.count())
	}
	if err := live.client.shutdown(); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SAKI_GUI_STORE", filepath.Join(root, "gui", "contacts.json"))
	t.Setenv("SAKI_GUI_WORKDIR", root)
	t.Setenv("SAKI_SESSION_DIR", sessionDir)
	t.Setenv("SAKI_RUNTIME_BIN", binary)
	t.Setenv("API_KEY", "offline-fixture")
	t.Setenv("SAKI_API", "openai-responses")
	t.Setenv("SAKI_BASE_URL", server.URL+"/v1")
	t.Setenv("SAKI_MODEL", "fixture")
	t.Setenv("SAKI_CONTEXT_TOKENS", "131072")

	h := startIssueResultPageApp(t)
	issueID := "issue-result-page"
	var contactID string
	var persistErr error
	h.pump.do(func() {
		ct := h.app.selectedContact()
		contactID = ct.ID
		now := nowMillis()
		issue := &productIssue{
			ID: issueID, ContactID: ct.ID, Goal: "Ship visible results", Status: issueStatusInProgress,
			WorkSessionID: session.SessionID, RequirementsRevision: 1, CreatedAt: now, UpdatedAt: now,
			Supplements: newIssueSupplementLedger(),
			Executions: []*issueExecution{
				{ID: "exec-1", SessionID: session.SessionID, TaskID: firstTask.TaskID, MessageID: firstTask.MessageID, Prompt: "Create result.txt.", Status: issueExecutionCompleted},
				{ID: "exec-2", SessionID: session.SessionID, TaskID: secondTask.TaskID, MessageID: secondTask.MessageID, Prompt: "Modify result.txt.", Status: issueExecutionCompleted},
			},
		}
		ct.Issues = append(ct.Issues, issue)
		ct.SelectedIssueID = issueID
		h.app.workspaceOpen = true
		h.app.windowWidth = 1040
		persistErr = h.app.persist()
	})
	if persistErr != nil {
		t.Fatal(persistErr)
	}

	// Remove the file before opening so the page starts from an honest missing
	// state and can recover only through an explicit Check again.
	resultPath := filepath.Join(root, "result.txt")
	if err := os.Remove(resultPath); err != nil {
		t.Fatal(err)
	}

	issueResultRequireTexts(t, h, "Open result")
	if err := issueResultClick(h, "Open result"); err != nil {
		t.Fatal(err)
	}
	h.pump.wait(func() bool {
		page := h.app.issueResult
		return page.Open && !page.Loading && (page.LoadError != "" || len(page.Panel.Files) > 0)
	})
	open, loading, loadError, files, response := issueResultPageState(h)
	if !open || loading || loadError != "" || len(files) != 1 || response != issueResultPageWriteFinal {
		t.Fatalf("first run page = open=%v loading=%v error=%q files=%+v response=%q", open, loading, loadError, files, response)
	}
	if files[0].ToolName != "write" || files[0].CallID != "call-write" {
		t.Fatalf("first run source = %+v", files[0])
	}
	issueResultRequireTexts(t, h, "Run 1 · Ship visible results", "result.txt", "Reported by write")

	if got, err := issueResultClickCopy(h, "Copy result"); err != nil {
		t.Fatal(err)
	} else if got != issueResultPageWriteFinal {
		t.Fatalf("result copy = %q, want exact %q", got, issueResultPageWriteFinal)
	}
	if got, err := issueResultClickCopy(h, "Copy code"); err != nil {
		t.Fatal(err)
	} else if got != "write\n" {
		t.Fatalf("code copy = %q, want write\n", got)
	}
	if err := issueResultClick(h, "View file 1"); err != nil {
		t.Fatal(err)
	}
	h.pump.wait(func() bool {
		return !h.app.issueResult.Panel.Checking && h.app.issueResult.Panel.Inspection != nil
	})
	issueResultRequireTexts(t, h, "No file exists at the reported path", "Copy path")
	if err := os.WriteFile(files[0].Path, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := issueResultClick(h, "Check again"); err != nil {
		t.Fatal(err)
	}
	h.pump.wait(func() bool {
		inspection := h.app.issueResult.Panel.Inspection
		return !h.app.issueResult.Panel.Checking && inspection != nil && inspection.Notice == "" && inspection.Preview == "second\n"
	})
	issueResultRequireTexts(t, h, "Current file", "second")
	if got, err := issueResultClickCopy(h, "Copy current file"); err != nil {
		t.Fatal(err)
	} else if got != "second\n" {
		t.Fatalf("current file copy = %q", got)
	}
	if got, err := issueResultClickCopy(h, "Copy path"); err != nil {
		t.Fatal(err)
	} else if got != files[0].Path {
		t.Fatalf("path copy = %q, want %q", got, files[0].Path)
	}
	if err := issueResultClick(h, "Source & raw record"); err != nil {
		t.Fatal(err)
	}
	if got, err := issueResultClickCopy(h, "Copy tool record"); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(got, `"bytes_written"`) {
		t.Fatalf("first run raw record = %q", got)
	}
	if err := issueResultClick(h, "Close results"); err != nil {
		t.Fatal(err)
	}
	var aux string
	var pageOpen bool
	h.pump.do(func() {
		aux, pageOpen = h.app.aux, h.app.issueResult.Open
	})
	if aux != "" || pageOpen {
		t.Fatalf("close did not return to the work page: aux=%q open=%v", aux, pageOpen)
	}

	// Open the second run directly while the UI action is already covered above.
	h.pump.do(func() { h.app.openIssueResult(issueID, "exec-2") })
	h.pump.wait(func() bool {
		page := h.app.issueResult
		return page.Open && !page.Loading && (page.LoadError != "" || len(page.Panel.Files) > 0)
	})
	open, loading, loadError, files, response = issueResultPageState(h)
	if !open || loading || loadError != "" || len(files) != 1 || response != issueResultPageEditFinal {
		t.Fatalf("second run page = open=%v loading=%v error=%q files=%+v response=%q", open, loading, loadError, files, response)
	}
	if files[0].ToolName != "edit" || files[0].CallID != "call-edit" {
		t.Fatalf("second run source = %+v", files[0])
	}
	issueResultRequireTexts(t, h, "Run 2 · Ship visible results", "modified result", "Reported by edit")
	if err := issueResultClick(h, "View file 1"); err != nil {
		t.Fatal(err)
	}
	h.pump.wait(func() bool {
		return !h.app.issueResult.Panel.Checking && h.app.issueResult.Panel.Inspection != nil
	})
	if err := issueResultClick(h, "Source & raw record"); err != nil {
		t.Fatal(err)
	}
	if got, err := issueResultClickCopy(h, "Copy tool record"); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(got, `"applied"`) || strings.Contains(got, `"bytes_written"`) {
		t.Fatalf("second run raw record crossed runs: %q", got)
	}
	if err := issueResultClick(h, "Close results"); err != nil {
		t.Fatal(err)
	}
	beforeRestart := provider.count()
	h.shutdown(t)
	restarted := startIssueResultPageApp(t)
	var reopened bool
	restarted.pump.do(func() {
		ct := restarted.app.contactByID(contactID)
		if ct == nil {
			return
		}
		for index, candidate := range restarted.app.contacts {
			if candidate.ID == contactID {
				restarted.app.selected = index
			}
		}
		issue := ct.issueByID(issueID)
		if issue == nil {
			return
		}
		restarted.app.workspaceOpen = true
		restarted.app.openIssueResult(issueID, "exec-2")
		reopened = true
	})
	if !reopened {
		t.Fatal("restart did not restore the issue and execution")
	}
	restarted.pump.wait(func() bool {
		page := restarted.app.issueResult
		return page.Open && !page.Loading && (page.LoadError != "" || len(page.Panel.Files) > 0)
	})
	open, loading, loadError, files, response = issueResultPageState(restarted)
	if !open || loading || loadError != "" || len(files) != 1 || response != issueResultPageEditFinal {
		t.Fatalf("restarted page = open=%v loading=%v error=%q files=%+v response=%q", open, loading, loadError, files, response)
	}
	if provider.count() != beforeRestart {
		t.Fatalf("result page issued provider requests: %d -> %d", beforeRestart, provider.count())
	}
	issueResultRequireTexts(t, restarted, "Run 2 · Ship visible results", "Reported by edit")
	var selectedID string
	restarted.pump.do(func() { selectedID = restarted.app.selectedContact().ID })
	if selectedID != contactID {
		t.Fatalf("restart selected contact = %q", selectedID)
	}
	if provider.body(0) == "" || !strings.Contains(provider.body(0), "Create result.txt.") {
		t.Fatal("first provider request did not contain the original task")
	}
	if got := len(files); got != 1 {
		t.Fatalf("restart files = %d, want one reported file", got)
	}
}

func TestIssueResultPageOwnerSwitchRejectsLateCallback(t *testing.T) {
	binary := requireRuntimeBinary(t)
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected provider request while opening a result page")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("SAKI_GUI_STORE", filepath.Join(root, "gui", "contacts.json"))
	t.Setenv("SAKI_GUI_WORKDIR", root)
	t.Setenv("SAKI_SESSION_DIR", filepath.Join(root, "sessions"))
	t.Setenv("SAKI_RUNTIME_BIN", binary)
	t.Setenv("API_KEY", "offline-fixture")
	t.Setenv("SAKI_API", "openai-responses")
	t.Setenv("SAKI_BASE_URL", server.URL+"/v1")
	t.Setenv("SAKI_MODEL", "fixture")
	t.Setenv("SAKI_CONTEXT_TOKENS", "131072")

	h := startIssueResultPageApp(t)
	var contactA, contactB string
	h.pump.do(func() {
		h.app.addDefaultContact()
		contactA, contactB = h.app.contacts[0].ID, h.app.contacts[1].ID
		h.app.selectContact(0)
		issue := &productIssue{
			ID: "issue-switch", ContactID: contactA, Goal: "switch owner", Status: issueStatusInProgress,
			Executions: []*issueExecution{{ID: "exec-switch", SessionID: "missing-session", TaskID: "missing-task", Status: issueExecutionCompleted}},
		}
		h.app.contacts[0].Issues = append(h.app.contacts[0].Issues, issue)
		h.app.contacts[0].SelectedIssueID = issue.ID
		h.app.workspaceOpen = true
		// Both actions run in the same UI callback. Async history results can
		// only be delivered after this callback returns, so the switch is a
		// deterministic barrier rather than a timing race.
		h.app.openIssueResult(issue.ID, "exec-switch")
		h.app.selectContact(1)
	})
	h.pump.wait(func() bool { return h.app.resultInFlight == 0 })
	var open bool
	var aux string
	var selected string
	h.pump.do(func() {
		open, aux, selected = h.app.issueResult.Open, h.app.aux, h.app.selectedContact().ID
	})
	if open || aux == "issue-result" || selected != contactB {
		t.Fatalf("late result callback crossed owner: open=%v aux=%q selected=%q want=%q", open, aux, selected, contactB)
	}

	h.pump.do(func() {
		h.app.selectContact(0)
		h.app.openIssueResult("issue-switch", "exec-switch")
		h.app.closeIssueResult()
	})
	h.pump.wait(func() bool { return h.app.resultInFlight == 0 })
	h.pump.do(func() {
		open, aux = h.app.issueResult.Open, h.app.aux
	})
	if open || aux == "issue-result" {
		t.Fatalf("late callback reopened a closed result page: open=%v aux=%q", open, aux)
	}
}

func TestIssueResultPageCheckSequenceAppliesOnlyLatest(t *testing.T) {
	app := &appModel{scrollTo: -1}
	pump := newUIPump(t, app)
	t.Cleanup(func() { pump.close() })
	file := issueResultFile{
		ExecutionID: "exec-seq", SessionID: "session-seq", TaskID: "task-seq", MessageID: "msg-seq",
		CallID: "call-seq", ToolName: "write", Path: "/tmp/issue-result-seq.txt", WorkingDirectory: "/tmp",
		RawResult: `{"path":"/tmp/issue-result-seq.txt","bytes_written":1}`,
	}
	key := issueResultFileKey(file)
	app.aux = "issue-result"
	app.issueResult = issueResultPage{
		Open: true, OwnerContactID: "contact-seq", IssueID: "issue-seq", ExecutionID: "exec-seq",
		SessionID: "session-seq", TaskID: "task-seq", MessageID: "msg-seq", Generation: 1,
		Panel: issueResultPanelState{Files: []issueResultFile{file}, Selected: key, InspectFor: key},
	}

	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	var calls atomic.Int32
	app.issueResultInspect = func(issueResultFile) issueResultFileInspection {
		switch calls.Add(1) {
		case 1:
			close(firstStarted)
			<-releaseFirst
			return issueResultFileInspection{Path: file.Path, Exists: true, Regular: true, Preview: "older"}
		default:
			close(secondStarted)
			<-releaseSecond
			return issueResultFileInspection{Path: file.Path, Exists: true, Regular: true, Preview: "newer"}
		}
	}
	waitSignal := func(ch chan struct{}, label string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", label)
		}
	}

	pump.do(func() { app.checkIssueResultFile(1, file) })
	waitSignal(firstStarted, "first inspection")
	pump.do(func() { app.checkIssueResultFile(1, file) })
	waitSignal(secondStarted, "second inspection")
	close(releaseSecond)
	pump.wait(func() bool {
		inspection := app.issueResult.Panel.Inspection
		return inspection != nil && inspection.Preview == "newer"
	})
	close(releaseFirst)
	pump.wait(func() bool { return app.resultInFlight == 0 })
	var preview string
	pump.do(func() {
		if inspection := app.issueResult.Panel.Inspection; inspection != nil {
			preview = inspection.Preview
		}
	})
	if preview != "newer" {
		t.Fatalf("older same-key check overwrote latest inspection: %q", preview)
	}
}
