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

func scrollIssueResultActionIntoView(tr *ui.Tester, label string, width, height int) error {
	for _, direction := range []float32{120, -120} {
		for attempt := 0; attempt < 12; attempt++ {
			if rect, ok := tr.Find(label); ok && rect.W > 0 && rect.H > 0 && rect.Y >= 0 && rect.Y+rect.H <= float32(height) {
				return nil
			}
			tr.Scroll(float32(width)*0.82, float32(height)/2, 0, direction)
			tr.Frame()
		}
	}
	return fmt.Errorf("issue result action %q was not reachable in %dx%d: %v", label, width, height, tr.Texts())
}

func issueResultTesterRunAt(h *guiHarness, width, height int, fn func(*ui.Tester) error) error {
	var err error
	h.pump.do(func() {
		tr := ui.NewTester(h.app.view, width, height)
		tr.Frame()
		err = fn(tr)
	})
	return err
}

func issueResultClickAt(h *guiHarness, width, height int, label string) error {
	return issueResultTesterRunAt(h, width, height, func(tr *ui.Tester) error {
		if err := scrollIssueResultActionIntoView(tr, label, width, height); err != nil {
			return err
		}
		return tr.Click(label)
	})
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

	if err := issueResultClick(h, "Issue Ship visible results"); err != nil {
		t.Fatal(err)
	}
	if err := issueResultClick(h, "More issue actions"); err != nil {
		t.Fatal(err)
	}
	if err := issueResultClick(h, "Open result · run 1"); err != nil {
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
	if err := issueResultClick(h, "Read file 1: result.txt"); err != nil {
		t.Fatal(err)
	}
	h.pump.wait(func() bool {
		return !h.app.issueResult.Panel.Checking && h.app.issueResult.Panel.Inspection != nil
	})
	readerClosed := false
	if err := issueResultTesterRun(h, func(tr *ui.Tester) error {
		tr.Key(0, ui.KeyEscape)
		tr.Frame()
		readerClosed = !tr.HasText("File reading")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.pump.do(func() {
		if !readerClosed || !h.app.issueResult.Open || h.app.aux != "issue-result" || !h.app.workspaceOpen {
			t.Errorf("Escape closed more than the reader: closed=%v pageOpen=%v aux=%q workOpen=%v", readerClosed, h.app.issueResult.Open, h.app.aux, h.app.workspaceOpen)
		}
	})
	if err := issueResultClick(h, "Read file 1: result.txt"); err != nil {
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

	openKey := issueResultFileKey(files[0])
	var openCalls, revealCalls atomic.Int32
	var openPath, revealPath string
	h.pump.do(func() {
		h.app.issueResultOpen = func(path string) error {
			openCalls.Add(1)
			openPath = path
			return fmt.Errorf("injected open failure")
		}
		h.app.issueResultReveal = func(path string) error {
			revealCalls.Add(1)
			revealPath = path
			return fmt.Errorf("injected reveal failure")
		}
	})
	if err := issueResultClick(h, "Open file"); err != nil {
		t.Fatal(err)
	}
	h.pump.wait(func() bool {
		action := h.app.issueResult.Panel.Action
		return h.app.issueResult.Panel.ActionFor == openKey && action.Err != ""
	})
	if openCalls.Load() != 1 || revealCalls.Load() != 0 || openPath != files[0].Path {
		t.Fatalf("Open called wrong OS action: open=%d reveal=%d path=%q want=%q", openCalls.Load(), revealCalls.Load(), openPath, files[0].Path)
	}
	issueResultRequireTexts(t, h, "injected open failure")
	if err := issueResultClick(h, "Check again"); err != nil {
		t.Fatal(err)
	}
	h.pump.wait(func() bool {
		page := h.app.issueResult
		return page.Panel.ActionFor == "" && !page.Panel.Checking && page.Panel.Inspection != nil
	})
	stem := issueResultTexts(h)
	if issueResultTextsHave(stem, "injected open failure") {
		t.Fatalf("successful check kept stale open error: %v", stem)
	}
	h.pump.do(func() {
		h.app.issueResultOpen = func(path string) error {
			openCalls.Add(1)
			openPath = path
			return nil
		}
	})
	if err := issueResultClick(h, "Open file"); err != nil {
		t.Fatal(err)
	}
	h.pump.wait(func() bool {
		page := h.app.issueResult
		return page.Panel.ActionFor == openKey && page.Panel.Action.Err == ""
	})
	if openCalls.Load() != 2 || revealCalls.Load() != 0 || openPath != files[0].Path {
		t.Fatalf("second Open called wrong OS action: open=%d reveal=%d path=%q", openCalls.Load(), revealCalls.Load(), openPath)
	}
	stem = issueResultTexts(h)
	if issueResultTextsHave(stem, "Opened") || issueResultTextsHave(stem, "injected open failure") {
		t.Fatalf("open request produced a false outcome or stale error: %v", stem)
	}
	if err := issueResultClick(h, "Show in Finder"); err != nil {
		t.Fatal(err)
	}
	h.pump.wait(func() bool {
		action := h.app.issueResult.Panel.Action
		return h.app.issueResult.Panel.ActionFor == openKey && strings.Contains(action.Err, "injected reveal failure")
	})
	if revealCalls.Load() != 1 || openCalls.Load() != 2 || revealPath != files[0].Path {
		t.Fatalf("Reveal called wrong OS action: open=%d reveal=%d path=%q want=%q", openCalls.Load(), revealCalls.Load(), revealPath, files[0].Path)
	}
	issueResultRequireTexts(t, h, "injected reveal failure")
	h.pump.do(func() {
		h.app.issueResultReveal = func(path string) error {
			revealCalls.Add(1)
			revealPath = path
			return nil
		}
	})
	if err := issueResultClick(h, "Show in Finder"); err != nil {
		t.Fatal(err)
	}
	h.pump.wait(func() bool {
		page := h.app.issueResult
		return page.Panel.ActionFor == openKey && page.Panel.Action.Err == ""
	})
	if revealCalls.Load() != 2 || openCalls.Load() != 2 || revealPath != files[0].Path {
		t.Fatalf("second Reveal called wrong OS action: open=%d reveal=%d path=%q", openCalls.Load(), revealCalls.Load(), revealPath)
	}
	stem = issueResultTexts(h)
	if issueResultTextsHave(stem, "Found in Finder") || issueResultTextsHave(stem, "injected reveal failure") {
		t.Fatalf("reveal request produced a false outcome or stale error: %v", stem)
	}
	if err := issueResultClick(h, "Source & raw record"); err != nil {
		t.Fatal(err)
	}
	if got, err := issueResultClickCopy(h, "Copy tool record"); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(got, `"bytes_written"`) {
		t.Fatalf("first run raw record = %q", got)
	}
	if err := issueResultClick(h, "Close reading"); err != nil {
		t.Fatal(err)
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
	if err := issueResultClick(h, "Read file 1: result.txt"); err != nil {
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
	if err := issueResultClick(h, "Close reading"); err != nil {
		t.Fatal(err)
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

const issueResultActualRunOneFinal = "  first result  \n\nsecond paragraph keeps spaces  \nI also wrote /tmp/assistant-claim-only.txt"
const issueResultActualRunTwoFinal = "\n second result\n\ncontinued paragraph  \n"

type issueResultRunProvider struct {
	keys   []string
	mu     sync.Mutex
	counts map[string]int
	bodies []string
}

func newIssueResultRunProvider() *issueResultRunProvider {
	return &issueResultRunProvider{
		keys:   []string{"PAGE-RUN-TWO", "PAGE-RUN-ONE"},
		counts: map[string]int{},
	}
}

func (p *issueResultRunProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

func (p *issueResultRunProvider) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.bodies = append(p.bodies, string(body))
		key := ""
		for _, candidate := range p.keys {
			if strings.Contains(string(body), candidate) {
				key = candidate
				break
			}
		}
		count := p.counts[key]
		p.counts[key]++
		p.mu.Unlock()
		var response any
		switch {
		case key == "PAGE-RUN-ONE" && count == 0:
			response = issueResultFunctionCall("write", "app-write", map[string]any{
				"path": "app-result.txt", "content": "app-first\n",
			})
		case key == "PAGE-RUN-TWO" && count == 0:
			response = issueResultFunctionCall("edit", "app-edit", map[string]any{
				"path": "app-result.txt",
				"edits": []any{
					map[string]any{"oldText": "app-first", "newText": "app-second"},
				},
			})
		case key == "PAGE-RUN-ONE":
			response = issueResultPageFinal(issueResultActualRunOneFinal)
		default:
			response = issueResultPageFinal(issueResultActualRunTwoFinal)
		}
		data, _ := json.Marshal(response)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

func TestIssueResultPageFromActualIssueRunsAndRestart(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := newIssueResultRunProvider()
	h := startRoutingHarnessWithHandler(t, binary, provider.handler(), 1)
	var contactID, issueID string
	var runOK bool
	var runErr string
	h.pump.do(func() {
		runOK = h.app.createIssue("PAGE-RUN-ONE", "Create and then modify the result file.")
		runErr = h.app.issueFormError
		if !runOK {
			return
		}
		contactID = h.app.selectedContact().ID
		issueID = h.app.selectedContact().SelectedIssueID
		runOK = h.app.runIssue(issueID)
		runErr = h.app.issueFormError
	})
	if !runOK {
		t.Fatalf("first actual issue run: %s", runErr)
	}
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted
	})
	if provider.count() != 2 {
		t.Fatalf("first run provider requests = %d, want write/final", provider.count())
	}
	var workErr error
	var workOpen bool
	h.pump.do(func() {
		tr := ui.NewTester(h.app.view, 1040, 720)
		tr.Frame()
		if err := tr.Click("Work"); err != nil {
			workErr = err
			return
		}
		tr.Frame()
		workOpen = h.app.workspaceOpen
		if r, ok := tr.Find("Close work"); !workOpen || !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.X+r.W > 1040 || r.Y < 0 || r.Y+r.H > 720 {
			workErr = fmt.Errorf("Work open geometry = open=%v rect=%+v ok=%v", workOpen, r, ok)
		}
	})
	if workErr != nil {
		t.Fatal(workErr)
	}
	if !workOpen {
		t.Fatal("clicking Work did not open the work panel")
	}
	issueResultRequireTexts(t, h, "Read latest result")
	if err := issueResultClick(h, "Read latest result"); err != nil {
		t.Fatal(err)
	}
	h.pump.wait(func() bool {
		page := h.app.issueResult
		return page.Open && !page.Loading && (page.LoadError != "" || len(page.Panel.Files) > 0)
	})
	open, loading, loadError, files, response := issueResultPageState(h)
	if !open || loading || loadError != "" || len(files) != 1 || response != issueResultActualRunOneFinal {
		t.Fatalf("actual first run page = open=%v loading=%v error=%q files=%+v response=%q", open, loading, loadError, files, response)
	}
	if files[0].ToolName != "write" || files[0].CallID != "app-write" || filepath.Base(files[0].Path) != "app-result.txt" {
		t.Fatalf("actual first run file = %+v", files[0])
	}
	if strings.Contains(files[0].Path, "assistant-claim-only") {
		t.Fatal("assistant text path became a reported file")
	}
	if got, err := issueResultClickCopy(h, "Copy result"); err != nil {
		t.Fatal(err)
	} else if got != issueResultActualRunOneFinal {
		t.Fatalf("actual first result copy = %q, want exact %q", got, issueResultActualRunOneFinal)
	}
	if err := issueResultClick(h, "Close results"); err != nil {
		t.Fatal(err)
	}

	var editOK, continueOK bool
	h.pump.do(func() {
		editOK = h.app.editIssue(issueID, "PAGE-RUN-TWO", "Modify the same result file.")
		runErr = h.app.issueFormError
		if editOK {
			continueOK = h.app.runIssue(issueID)
			runErr = h.app.issueFormError
		}
	})
	if !editOK || !continueOK {
		t.Fatalf("actual continue run: edit=%v run=%v error=%s", editOK, continueOK, runErr)
	}
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 2 && issue.Executions[1].Status == issueExecutionCompleted
	})
	if provider.count() != 4 {
		t.Fatalf("continue provider requests = %d, want edit/final", provider.count())
	}
	var secondExecutionID string
	h.pump.do(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		secondExecutionID = issue.Executions[1].ID
		h.app.openIssueResult(issueID, secondExecutionID)
	})
	h.pump.wait(func() bool {
		page := h.app.issueResult
		return page.Open && !page.Loading && (page.LoadError != "" || len(page.Panel.Files) > 0)
	})
	open, loading, loadError, files, response = issueResultPageState(h)
	if !open || loading || loadError != "" || len(files) != 1 || response != issueResultActualRunTwoFinal {
		t.Fatalf("actual second run page = open=%v loading=%v error=%q files=%+v response=%q", open, loading, loadError, files, response)
	}
	if files[0].ToolName != "edit" || files[0].CallID != "app-edit" {
		t.Fatalf("actual second run file = %+v", files[0])
	}
	var reloadGeneration int
	h.pump.do(func() { reloadGeneration = h.app.issueResult.Generation })
	beforeReload := provider.count()
	if err := issueResultClick(h, "Reload result"); err != nil {
		t.Fatal(err)
	}
	h.pump.wait(func() bool {
		page := h.app.issueResult
		return page.Generation > reloadGeneration && !page.Loading
	})
	open, loading, loadError, files, response = issueResultPageState(h)
	if !open || loading || loadError != "" || len(files) != 1 || response != issueResultActualRunTwoFinal {
		t.Fatalf("reloaded page = open=%v loading=%v error=%q files=%+v response=%q", open, loading, loadError, files, response)
	}
	if provider.count() != beforeReload {
		t.Fatalf("Reload result issued provider requests: %d -> %d", beforeReload, provider.count())
	}
	if err := issueResultClick(h, "Read file 1: app-result.txt"); err != nil {
		t.Fatal(err)
	}
	h.pump.wait(func() bool {
		return !h.app.issueResult.Panel.Checking && h.app.issueResult.Panel.Inspection != nil
	})
	issueResultRequireTexts(t, h, "Current file", "app-second")
	if err := issueResultClick(h, "Close reading"); err != nil {
		t.Fatal(err)
	}
	if err := issueResultClick(h, "Close results"); err != nil {
		t.Fatal(err)
	}

	beforeRestart := provider.count()
	h.shutdown(t)
	restarted := startIssueResultPageApp(t)
	var restoredOpen bool
	var reopenErr error
	restarted.pump.do(func() {
		restoredOpen = restarted.app.workspaceOpen
		ct := restarted.app.contactByID(contactID)
		if ct == nil {
			reopenErr = fmt.Errorf("restart did not restore the contact")
			return
		}
		for index, candidate := range restarted.app.contacts {
			if candidate.ID == contactID {
				restarted.app.selected = index
			}
		}
		issue := ct.issueByID(issueID)
		if issue == nil || len(issue.Executions) != 2 {
			reopenErr = fmt.Errorf("restart did not restore the dispatched issue executions")
			return
		}
		restarted.app.openIssueResult(issueID, issue.Executions[1].ID)
	})
	if !restoredOpen {
		t.Fatal("restart did not restore the persisted Work open preference")
	}
	if reopenErr != nil {
		t.Fatal(reopenErr)
	}
	restarted.pump.wait(func() bool {
		page := restarted.app.issueResult
		return page.Open && !page.Loading && (page.LoadError != "" || len(page.Panel.Files) > 0)
	})
	open, loading, loadError, files, response = issueResultPageState(restarted)
	if !open || loading || loadError != "" || len(files) != 1 || response != issueResultActualRunTwoFinal {
		t.Fatalf("restarted actual page = open=%v loading=%v error=%q files=%+v response=%q", open, loading, loadError, files, response)
	}
	if provider.count() != beforeRestart {
		t.Fatalf("restart result page issued provider requests: %d -> %d", beforeRestart, provider.count())
	}
}

func TestIssueResultNavigationEntryAndReturnWideNarrow(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &issueResultPageProvider{}
	h := startRoutingHarnessWithHandler(t, binary, provider.handler(t), 1)
	var issueID string
	h.pump.do(func() {
		if !h.app.createIssue("Navigation result", "Create a readable result.") {
			t.Fatalf("createIssue: %s", h.app.issueFormError)
		}
		issueID = h.app.selectedContact().SelectedIssueID
		if !h.app.runIssue(issueID) {
			t.Fatalf("runIssue: %s", h.app.issueFormError)
		}
	})
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted
	})
	if provider.count() != 2 {
		t.Fatalf("provider requests = %d, want write/final", provider.count())
	}
	// Enter through the visible Work button and issue row, then exercise both a
	// wide and the narrow work-drawer layout. Each iteration starts from overview.
	if err := issueResultClick(h, "Work"); err != nil {
		t.Fatal(err)
	}
	if err := issueResultClick(h, "Back to issues"); err != nil {
		t.Fatal(err)
	}
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "keep this draft"
		ct.SelectedWorkTaskID = ""
		ct.issueNavigation().Filter = issueFilterUnfinished
	})
	for _, width := range []int{1040, 720} {
		h.pump.do(func() { h.app.windowWidth = width })
		if err := issueResultClickAt(h, width, 720, "Issue Navigation result"); err != nil {
			t.Fatalf("width %d enter issue detail: %v", width, err)
		}
		var actionsOpen bool
		h.pump.do(func() { actionsOpen = h.app.issueDetailOpen[issueID] })
		if !actionsOpen {
			if err := issueResultClickAt(h, width, 720, "More issue actions"); err != nil {
				t.Fatalf("width %d expand run records: %v", width, err)
			}
		}
		if err := issueResultClickAt(h, width, 720, "View execution · run 1"); err != nil {
			t.Fatalf("width %d select run for observable work identity: %v", width, err)
		}
		if err := issueResultTesterRunAt(h, width, 720, func(tr *ui.Tester) error {
			return scrollIssueResultActionIntoView(tr, "Read latest result", width, 720)
		}); err != nil {
			t.Fatalf("width %d prepare latest row: %v", width, err)
		}
		var before struct {
			detailID, filter, selectedIssue, selectedTask, draft string
			detailScrollY                                        float32
			workspaceOpen                                        bool
		}
		h.pump.do(func() {
			ct := h.app.selectedContact()
			nav := ct.issueNavigation()
			before.detailID, before.filter = nav.DetailID, nav.Filter
			before.selectedIssue, before.selectedTask = ct.SelectedIssueID, ct.SelectedWorkTaskID
			before.draft, before.detailScrollY, before.workspaceOpen = ct.Draft, nav.DetailScroll.Y, h.app.workspaceOpen
		})
		if before.detailID != issueID || !before.workspaceOpen || before.selectedTask == "" {
			t.Fatalf("width %d did not enter issue detail with a selected work identity: %+v", width, before)
		}

		// Latest result: actual row entry, then actual file row entry.
		if err := issueResultClickAt(h, width, 720, "Read latest result"); err != nil {
			t.Fatalf("width %d latest entry: %v", width, err)
		}
		h.pump.wait(func() bool {
			page := h.app.issueResult
			return page.Open && !page.Loading && (page.LoadError != "" || len(page.Panel.Files) > 0)
		})
		if err := issueResultClickAt(h, width, 720, "Read file 1: result.txt"); err != nil {
			t.Fatalf("width %d file entry: %v", width, err)
		}
		h.pump.wait(func() bool {
			return !h.app.issueResult.Panel.Checking && h.app.issueResult.Panel.Inspection != nil
		})
		var readerClosed, resultStillOpen bool
		if err := issueResultTesterRunAt(h, width, 720, func(tr *ui.Tester) error {
			tr.Key(0, ui.KeyEscape)
			tr.Frame()
			readerClosed = !tr.HasText("File reading")
			resultStillOpen = tr.HasText("Run results")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var aux string
		var pageOpen bool
		h.pump.do(func() { aux, pageOpen = h.app.aux, h.app.issueResult.Open })
		if !readerClosed || !resultStillOpen || aux != "issue-result" || !pageOpen {
			t.Fatalf("width %d Escape closed more than reader: readerClosed=%v resultStillOpen=%v aux=%q pageOpen=%v", width, readerClosed, resultStillOpen, aux, pageOpen)
		}
		var latestClosed, latestFocused bool
		if err := issueResultTesterRunAt(h, width, 720, func(tr *ui.Tester) error {
			tr.Key(0, ui.KeyEscape)
			tr.Frame()
			latestClosed = !tr.HasText("Run results")
			latestFocused = tr.Focused("Read latest result")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var after struct {
			detailID, filter, selectedIssue, selectedTask, draft, aux string
			detailScrollY                                             float32
			workspaceOpen                                             bool
			pageOpen                                                  bool
		}
		h.pump.do(func() {
			ct := h.app.selectedContact()
			nav := ct.issueNavigation()
			after.detailID, after.filter = nav.DetailID, nav.Filter
			after.selectedIssue, after.selectedTask = ct.SelectedIssueID, ct.SelectedWorkTaskID
			after.draft, after.detailScrollY = ct.Draft, nav.DetailScroll.Y
			after.workspaceOpen, after.pageOpen, after.aux = h.app.workspaceOpen, h.app.issueResult.Open, h.app.aux
		})
		if !latestClosed || !latestFocused || after.pageOpen || after.aux != "" {
			t.Fatalf("width %d latest result return: closed=%v focused=%v pageOpen=%v aux=%q", width, latestClosed, latestFocused, after.pageOpen, after.aux)
		}
		t.Logf("width=%d viewport=%dx720 latest_focus=%v", width, width, latestFocused)
		if after.detailID != before.detailID || after.filter != before.filter ||
			after.selectedIssue != before.selectedIssue || after.selectedTask != before.selectedTask ||
			after.draft != before.draft || after.detailScrollY != before.detailScrollY || !after.workspaceOpen {
			t.Fatalf("width %d latest return changed detail context: before=%+v after=%+v", width, before, after)
		}

		// Specific run entry at a real nonzero detail scroll. The run record is
		// below the explicit work section, so this exercises actual scrolling and
		// then returns to that exact row without closing the work drawer.
		if err := issueResultTesterRunAt(h, width, 720, func(tr *ui.Tester) error {
			return scrollIssueResultActionIntoView(tr, "Open result · run 1", width, 720)
		}); err != nil {
			t.Fatalf("width %d prepare run row: %v", width, err)
		}
		var runVisible bool
		var runRect ui.Rect
		if err := issueResultTesterRunAt(h, width, 720, func(tr *ui.Tester) error {
			runRect, runVisible = tr.Find("Open result · run 1")
			runVisible = runVisible && runRect.W > 0 && runRect.H > 0 && runRect.Y >= 0 && runRect.Y+runRect.H <= 720
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var runBeforeScrollY float32
		h.pump.do(func() { runBeforeScrollY = h.app.selectedContact().issueNavigation().DetailScroll.Y })
		if !runVisible || runBeforeScrollY <= 0 {
			t.Fatalf("width %d run entry not reached with nonzero detail scroll: visible=%v rect=%+v scrollY=%v", width, runVisible, runRect, runBeforeScrollY)
		}
		if err := issueResultClickAt(h, width, 720, "Open result · run 1"); err != nil {
			t.Fatalf("width %d run entry: %v", width, err)
		}
		h.pump.wait(func() bool {
			page := h.app.issueResult
			return page.Open && !page.Loading
		})
		var runClosed, runFocused bool
		if err := issueResultTesterRunAt(h, width, 720, func(tr *ui.Tester) error {
			tr.Key(0, ui.KeyEscape)
			tr.Frame()
			runClosed = !tr.HasText("Run results")
			runFocused = tr.Focused("Open result · run 1")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var runAfter struct {
			scrollY                 float32
			selectedTask, draft     string
			workspaceOpen, pageOpen bool
		}
		h.pump.do(func() {
			ct := h.app.selectedContact()
			runAfter.scrollY = ct.issueNavigation().DetailScroll.Y
			runAfter.selectedTask, runAfter.draft = ct.SelectedWorkTaskID, ct.Draft
			runAfter.workspaceOpen, runAfter.pageOpen = h.app.workspaceOpen, h.app.issueResult.Open
		})
		if !runClosed || !runFocused || runAfter.pageOpen ||
			runAfter.scrollY != runBeforeScrollY || runAfter.scrollY <= 0 ||
			runAfter.selectedTask != before.selectedTask || runAfter.selectedTask == "" ||
			runAfter.draft != before.draft || !runAfter.workspaceOpen {
			t.Fatalf("width %d run return changed origin: closed=%v focused=%v beforeScroll=%v after=%+v before=%+v", width, runClosed, runFocused, runBeforeScrollY, runAfter, before)
		}
		t.Logf("width=%d viewport=%dx720 run_rect=%+v detail_scroll_before=%.1f detail_scroll_after=%.1f run_focus=%v selected_work_task=%q", width, width, runRect, runBeforeScrollY, runAfter.scrollY, runFocused, runAfter.selectedTask)
		if err := issueResultClickAt(h, width, 720, "Back to issues"); err != nil {
			t.Fatalf("width %d back to issues: %v", width, err)
		}
	}
}

func TestIssueResultPageReloadErrorKeepsReadableBody(t *testing.T) {
	a, ct := workspaceTestApp(t)
	file := issueResultPanelFixtureFile("reload", "/tmp/reload-body.txt", `{"path":"/tmp/reload-body.txt","bytes_written":1}`)
	a.aux = "issue-result"
	a.windowWidth = 720
	a.issueResult = issueResultPage{
		Open: true, OwnerContactID: ct.ID, IssueID: "issue-reload", ExecutionID: "exec-reload",
		SessionID: "session-reload", TaskID: "task-reload", MessageID: "msg-reload", Generation: 3,
		LoadError: "Execution history is unavailable: injected",
		Notice:    "Runtime session working directory is unavailable; file paths are shown as reported.",
		Panel: issueResultPanelState{
			RunLabel: "Run 2 · Reload error", RunStatus: "Completed",
			Response: "previous readable body\n", Files: []issueResultFile{file},
		},
	}
	tr := ui.NewTester(a.view, 720, 900)
	tr.Frame()
	texts := tr.Texts()
	for _, want := range []string{"Execution history is unavailable: injected", "previous readable body", "reload-body.txt", "Reload result"} {
		if !issueResultTextsHave(texts, want) {
			t.Fatalf("reload error hid readable body or retry control: missing %q in %v", want, texts)
		}
	}
}

func issueResultAppPreview(scene string, width int) *appModel {
	a := issuePreviewApp("issue-results")
	a.windowWidth = width
	a.aux = "issue-result"
	ct := a.selectedContact()
	file := issueResultPanelFixtureFile("preview", "/Users/you/Workspace/Saki/report.txt", `{"path":"/Users/you/Workspace/Saki/report.txt","bytes_written":12}`)
	page := issueResultPage{
		Open: true, OwnerContactID: ct.ID, IssueID: ct.SelectedIssueID, ExecutionID: "exec-preview",
		SessionID: "session-preview", TaskID: "task-preview", MessageID: "msg-preview", Generation: 1,
		Panel: issueResultPanelState{
			RunLabel: "Run 2 · Ship visible results", RunStatus: "Completed",
			Response: "# Recorded output\n\nResult body with `code`.\n\n```go\nfunc main() {}\n```\n",
			Files:    []issueResultFile{file},
		},
	}
	setReader := func(file issueResultFile, inspection issueResultFileInspection) {
		key := issueResultFileKey(file)
		page.Panel.Files = []issueResultFile{file}
		page.Panel.Selected = key
		page.Panel.InspectFor = key
		page.Panel.Inspection = &inspection
		page.Panel.ReaderKey = key
	}
	switch scene {
	case "navigation":
		a.aux = ""
		ct.issueNavigation().DetailID = ct.SelectedIssueID
		ct.issueNavigation().Filter = issueFilterUnfinished
		a.issueDetailOpen = map[string]bool{ct.SelectedIssueID: true}
	case "loading":
		page.Loading = true
		page.Panel.Response = ""
		page.Panel.Files = nil
	case "empty":
		page.Panel.Response = ""
		page.Panel.Files = nil
	case "markdown":
		mdFile := issueResultPanelFixtureFile("markdown", "/Users/you/Workspace/Saki/中文结果.md", `{"path":"/Users/you/Workspace/Saki/中文结果.md","bytes_written":12}`)
		setReader(mdFile, issueResultFileInspection{
			Path: mdFile.Path, WorkingDirectory: mdFile.WorkingDirectory, Exists: true, Regular: true,
			Preview: "# 中文结果\n\n这是中文段落，包含 **重点** 与 `inline`。\n\n- 第一项\n- 第二项\n\n| 名称 | 类型 | 标记 | 状态 |\n| --- | --- | --- | --- |\n| 中文内容 | Markdown | **重点** | 完成 |\n| 长内容 | text | `code` | 可滚动 |\n\n```go\nfunc main() {\n\tprintln(\"保留缩进\")\n}\n```\n",
		})
	case "plain":
		plainFile := issueResultPanelFixtureFile("plain", "/Users/you/Workspace/Saki/notes.txt", `{"path":"/Users/you/Workspace/Saki/notes.txt","bytes_written":12}`)
		setReader(plainFile, issueResultFileInspection{
			Path: plainFile.Path, WorkingDirectory: plainFile.WorkingDirectory, Exists: true, Regular: true,
			Preview: "# 这不是标题\n\n`这不是 inline code`\n\n| 不是 | 表格 |\n",
		})
	case "missing":
		missingFile := issueResultPanelFixtureFile("missing", "/Users/you/Workspace/Saki/missing.md", `{"path":"/Users/you/Workspace/Saki/missing.md","bytes_written":12}`)
		setReader(missingFile, issueResultFileInspection{
			Path: missingFile.Path, WorkingDirectory: missingFile.WorkingDirectory,
			Notice: "No file exists at the reported path; it may have been moved or deleted.",
		})
		page.Panel.ActionFor = issueResultFileKey(missingFile)
		page.Panel.Action = issueResultFileAction{Path: missingFile.Path, Err: "Could not open the current file: no handler"}
	case "readerror":
		readErrorFile := issueResultPanelFixtureFile("readerror", "/Users/you/Workspace/Saki/readerror.md", `{"path":"/Users/you/Workspace/Saki/readerror.md","bytes_written":12}`)
		setReader(readErrorFile, issueResultFileInspection{
			Path: readErrorFile.Path, WorkingDirectory: readErrorFile.WorkingDirectory, Exists: true, Regular: true,
			ReadError: "Could not read the current file: permission denied",
		})
	case "source":
		inspection := issueResultFileInspection{
			Path: file.Path, WorkingDirectory: file.WorkingDirectory, Exists: true, Regular: true,
			Preview: "current file content\n",
		}
		setReader(file, inspection)
		page.Panel.SourceFor = issueResultFileKey(file)
	case "long":
		longName := strings.Repeat("很长的中文结果文件名", 4) + ".md"
		longPath := "/Users/you/Workspace/Saki/" + longName
		longFile := issueResultPanelFixtureFile("long", longPath, fmt.Sprintf(`{"path":%q,"bytes_written":1}`, longPath))
		setReader(longFile, issueResultFileInspection{
			Path: longPath, WorkingDirectory: longFile.WorkingDirectory, Exists: true, Regular: true,
			Preview: "# 长文件名\n\n" + strings.Repeat("窄窗口中的中文长文本需要换行并保持可读。", 8) + "\n",
		})
	case "multiple":
		second := issueResultPanelFixtureFile("preview-two", file.Path, `{"path":"/Users/you/Workspace/Saki/report.txt","applied":1}`)
		page.Panel.RunLabel = "Run 3 · Ship visible results"
		page.Panel.Files = []issueResultFile{file, second}
	}
	a.issueResult = page
	return a
}

func TestIssueResultAppPreviewsAlwaysWriteHeadlessPNGs(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	type sceneCase struct {
		name  string
		width int
		dark  bool
	}
	var cases []sceneCase
	for _, scene := range []string{"markdown", "plain", "missing", "readerror", "source", "long"} {
		for _, width := range []int{720, 1040, 1280} {
			for _, dark := range []bool{false, true} {
				cases = append(cases, sceneCase{scene, width, dark})
			}
		}
	}
	for _, scene := range []sceneCase{
		{"loading", 720, false},
		{"loading", 1040, true},
		{"empty", 1040, false},
		{"empty", 720, true},
		{"multiple", 1280, true},
	} {
		cases = append(cases, sceneCase{"navigation", 720, false}, sceneCase{"navigation", 720, true}, sceneCase{"navigation", 1040, false}, sceneCase{"navigation", 1040, true}, sceneCase{"navigation", 1280, false}, sceneCase{"navigation", 1280, true})
		cases = append(cases, scene)
	}
	for _, scene := range cases {
		app := issueResultAppPreview(scene.name, scene.width)
		tr := ui.NewTester(app.view, scene.width, 900)
		tr.SetDark(scene.dark)
		tr.Frame()
		readerWant := map[string][]string{
			"markdown":   {"File reading", "Markdown", "中文结果", "名称", "完成"},
			"plain":      {"File reading", "Plain text", "# 这不是标题", "| 不是 | 表格 |"},
			"missing":    {"File reading", "No file exists at the reported path"},
			"readerror":  {"File reading", "permission denied"},
			"source":     {"File reading", "Source & raw record"},
			"long":       {"File reading", "Markdown", "很长的中文结果文件名"},
			"navigation": {"Issue", "Read latest result", "Open result · run 1"},
		}
		for _, want := range readerWant[scene.name] {
			if !tr.HasText(want) {
				t.Fatalf("preview scene %s missing %q: %v", scene.name, want, tr.Texts())
			}
		}
		theme := "light"
		if scene.dark {
			theme = "dark"
		}
		writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("issue-result-app-%s-%s-%d.png", scene.name, theme, scene.width)), tr.Image())
	}
}

func TestIssueResultPageReloadDuringCheckDrainsStaleCallback(t *testing.T) {
	execution := &issueExecution{
		ID: "exec-reload", SessionID: "new-session", TaskID: "new-task", MessageID: "new-message",
		Status: issueExecutionCompleted,
	}
	issue := &productIssue{
		ID: "issue-reload", ContactID: "contact-reload", Goal: "reload while checking",
		Status: issueStatusInProgress, Executions: []*issueExecution{execution},
	}
	ct := &contact{ID: "contact-reload", Issues: []*productIssue{issue}}
	app := &appModel{contacts: []*contact{ct}, selected: 0, scrollTo: -1}
	pump := newUIPump(t, app)
	t.Cleanup(func() { pump.close() })
	file := issueResultFile{
		ExecutionID: "exec-reload", SessionID: "old-session", TaskID: "old-task", MessageID: "old-message",
		CallID: "call-reload", ToolName: "write", Path: "/tmp/issue-result-reload.txt", WorkingDirectory: "/tmp",
		RawResult: `{"path":"/tmp/issue-result-reload.txt","bytes_written":1}`,
	}
	key := issueResultFileKey(file)
	app.aux = "issue-result"
	app.issueResult = issueResultPage{
		Open: true, OwnerContactID: ct.ID, IssueID: issue.ID, ExecutionID: execution.ID,
		SessionID: "old-session", TaskID: "old-task", MessageID: "old-message", Generation: 1,
		Panel: issueResultPanelState{
			RunLabel: "Run 1 · old label", RunStatus: "Running",
			Files: []issueResultFile{file}, Selected: key, InspectFor: key,
		},
	}
	checkStarted := make(chan struct{})
	releaseCheck := make(chan struct{})
	app.issueResultInspect = func(issueResultFile) issueResultFileInspection {
		close(checkStarted)
		<-releaseCheck
		return issueResultFileInspection{Path: file.Path, Exists: true, Regular: true, Preview: "old inspection"}
	}
	pump.do(func() { app.checkIssueResultFile(1, file) })
	select {
	case <-checkStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("check did not start")
	}
	pump.do(func() { app.reloadIssueResult(1) })
	var checking bool
	var sessionID, taskID, messageID, status, runLabel, loadError string
	pump.do(func() {
		page := app.issueResult
		checking = page.Panel.Checking
		sessionID, taskID, messageID = page.SessionID, page.TaskID, page.MessageID
		status, runLabel, loadError = page.Panel.RunStatus, page.Panel.RunLabel, page.LoadError
	})
	if checking {
		t.Fatal("reload left an in-flight Check marked as checking")
	}
	if sessionID != "new-session" || taskID != "new-task" || messageID != "new-message" {
		t.Fatalf("reload kept stale execution identity: %s/%s/%s", sessionID, taskID, messageID)
	}
	if status != issueExecutionLabel(execution) || runLabel != "Run 1 · reload while checking" {
		t.Fatalf("reload kept stale run label/status: %q / %q", runLabel, status)
	}
	if loadError == "" {
		t.Fatal("reload without a ready runtime should leave an explainable error")
	}
	close(releaseCheck)
	pump.wait(func() bool { return app.resultInFlight == 0 })
	var applied bool
	pump.do(func() {
		applied = app.issueResult.Panel.Inspection != nil && app.issueResult.Panel.Inspection.Preview == "old inspection"
	})
	if applied {
		t.Fatal("reload allowed a stale in-flight check callback to apply")
	}
}

func TestIssueResultAppEmptyStateShowsHonestPanelControls(t *testing.T) {
	app := issueResultAppPreview("empty", 1040)
	tr := ui.NewTester(app.view, 1040, 900)
	tr.Frame()
	texts := tr.Texts()
	for _, want := range []string{
		"No assistant response was recorded for this run.",
		"No files were reported for this run.",
		"Reload result",
		"Close results",
	} {
		if !issueResultTextsHave(texts, want) {
			t.Fatalf("loaded empty result page missing %q in %v", want, texts)
		}
	}
}
