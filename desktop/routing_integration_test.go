package main

// Real-process routing regressions for #118. The controlled provider is local;
// no online model or credentials are used.

import (
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func startRoutingHarness(t *testing.T, binary string, provider *providerFixture, maxTasks int) *guiHarness {
	t.Helper()
	server := httptest.NewServer(provider.handler(t, "nagi-118"))
	root := t.TempDir()
	t.Setenv("SAKI_GUI_STORE", filepath.Join(root, "gui", "contacts.json"))
	t.Setenv("SAKI_GUI_WORKDIR", root)
	t.Setenv("SAKI_SESSION_DIR", filepath.Join(root, "sessions"))
	t.Setenv("SAKI_RUNTIME_BIN", binary)
	t.Setenv("SAKI_MAX_ACTIVE_TASKS", strconv.Itoa(maxTasks))
	t.Setenv("API_KEY", "offline-fixture")
	t.Setenv("SAKI_API", "openai-responses")
	t.Setenv("SAKI_BASE_URL", server.URL+"/v1")
	t.Setenv("SAKI_MODEL", "fixture")
	t.Setenv("SAKI_CONTEXT_TOKENS", "131072")
	app, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	pump := newUIPump(t, app)
	h := &guiHarness{app: app, pump: pump, provider: provider, root: root}
	app.startRuntime()
	pump.wait(func() bool {
		return app.runtimeStatus == runtimeStateReady && app.runtimeCapabilitiesLoaded &&
			app.runtimeMaxActiveTasks == maxTasks
	})
	t.Cleanup(func() {
		server.Close()
		if h.closed {
			return
		}
		if app.rt != nil {
			_ = app.rt.shutdown()
		}
		pump.close()
	})
	return h
}

func TestIndependentIssueWorkAndConversationCapacityTwo(t *testing.T) {
	binary := requireRuntimeBinary(t)
	hold := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hold) }) }
	defer release()
	arrived := make(chan struct{})
	provider := &providerFixture{ToolFirst: false, Hold: hold, Arrived: arrived}
	h := startRoutingHarness(t, binary, provider, 2)
	var issueID string
	h.pump.do(func() {
		if !h.app.createIssue("Issue A", "Work only in issue A context.") {
			t.Fatalf("createIssue: %s", h.app.issueFormError)
		}
		issueID = h.app.selectedContact().SelectedIssueID
		if !h.app.runIssue(issueID) {
			t.Fatalf("runIssue: %s", h.app.issueFormError)
		}
	})
	select {
	case <-arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("provider did not receive issue A")
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID != ""
	})
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "Conversation B while A works"
		if !h.app.sendCurrent() {
			t.Fatalf("conversation B was rejected at capacity 2: %s", ct.SendError)
		}
	})
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.ActiveTaskID == "" && ct.SendState == sendIdle && !ct.HistoryDirty &&
			len(ct.Messages) > 0 && strings.Contains(ct.Messages[len(ct.Messages)-1].Text, "nagi-118 answer")
	})
	var chatMessages []string
	var workMessages []string
	var conversationSession, workSession string
	h.snapshot(func() {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		conversationSession, workSession = ct.SessionID, issue.Work.SessionID
		for _, m := range ct.Messages {
			chatMessages = append(chatMessages, m.Text)
		}
		for _, m := range issue.Work.Messages {
			workMessages = append(workMessages, m.Text)
		}
	})
	if conversationSession == "" || workSession == "" || conversationSession == workSession {
		t.Fatalf("conversation/work sessions not independent: conversation=%q work=%q", conversationSession, workSession)
	}
	if containsText(chatMessages, "Issue A") {
		t.Fatalf("issue A prompt leaked into conversation: %v", chatMessages)
	}
	if containsText(workMessages, "Conversation B") {
		t.Fatalf("conversation B leaked into issue A history: %v", workMessages)
	}
	release()
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted
	})
	h.snapshot(func() {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		chatMessages = nil
		workMessages = nil
		for _, m := range ct.Messages {
			chatMessages = append(chatMessages, m.Text)
		}
		for _, m := range issue.Work.Messages {
			workMessages = append(workMessages, m.Text)
		}
		if issue.Executions[0].SessionID != workSession {
			t.Fatalf("execution session = %q, work = %q", issue.Executions[0].SessionID, workSession)
		}
	})
	if containsText(chatMessages, "Issue A") || containsText(workMessages, "Conversation B") {
		t.Fatalf("cross-session leak after A finishes: chat=%v work=%v", chatMessages, workMessages)
	}
	if !containsText(workMessages, "nagi-118 answer") {
		t.Fatalf("issue A did not receive its own result: %v", workMessages)
	}
	if provider.count() != 2 {
		t.Fatalf("provider requests = %d, want 2", provider.count())
	}
	for i := 0; i < provider.count(); i++ {
		body := provider.body(i)
		if strings.Contains(body, "Issue A") && strings.Contains(body, "Conversation B") {
			t.Fatalf("provider request %d mixed both session prompts: %s", i, body)
		}
	}
	workPages, _, err := h.app.historyAll(workSession)
	if err != nil {
		t.Fatalf("work history: %v", err)
	}
	chatPages, _, err := h.app.historyAll(conversationSession)
	if err != nil {
		t.Fatalf("conversation history: %v", err)
	}
	workHistory := historyTexts(workPages)
	chatHistory := historyTexts(chatPages)
	if containsText(workHistory, "Conversation B") || !containsText(workHistory, "nagi-118 answer") {
		t.Fatalf("actual work history leaked/corrupted: %v", workHistory)
	}
	if containsText(chatHistory, "Issue A") || !containsText(chatHistory, "Conversation B") {
		t.Fatalf("actual conversation history leaked/corrupted: %v", chatHistory)
	}
	h.shutdown(t)
	beforeRestart := provider.count()
	h2 := restartGUIHarness(t, binary)
	defer func() {
		if !h2.closed {
			h2.shutdown(t)
		}
	}()
	h2.pump.wait(func() bool {
		ct := h2.app.contacts[0]
		return ct.Loaded && len(ct.Issues) == 1 && ct.Issues[0].Work.Loaded
	})
	h2.snapshot(func() {
		ct := h2.app.contacts[0]
		issue := ct.Issues[0]
		if issue.Work.SessionID == "" || issue.Work.SessionID == ct.SessionID {
			t.Fatalf("restart lost independent sessions: conversation=%q work=%q", ct.SessionID, issue.Work.SessionID)
		}
		var chat, work []string
		for _, m := range ct.Messages {
			chat = append(chat, m.Text)
		}
		for _, m := range issue.Work.Messages {
			work = append(work, m.Text)
		}
		if containsText(chat, "Issue A") || containsText(work, "Conversation B") {
			t.Fatalf("restart cross-session leak: chat=%v work=%v", chat, work)
		}
	})
	if provider.count() != beforeRestart {
		t.Fatalf("restart issued provider requests: %d -> %d", beforeRestart, provider.count())
	}
}

func TestIndependentExecutionCapacityOneSerializesConversation(t *testing.T) {
	binary := requireRuntimeBinary(t)
	hold := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hold) }) }
	defer release()
	arrived := make(chan struct{})
	provider := &providerFixture{ToolFirst: false, Hold: hold, Arrived: arrived}
	h := startRoutingHarness(t, binary, provider, 1)
	var issueID string
	h.pump.do(func() {
		if !h.app.createIssue("Issue serial", "Run in its own work session.") {
			t.Fatalf("createIssue: %s", h.app.issueFormError)
		}
		issueID = h.app.selectedContact().SelectedIssueID
		if !h.app.runIssue(issueID) {
			t.Fatalf("runIssue: %s", h.app.issueFormError)
		}
	})
	select {
	case <-arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("provider did not receive issue")
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID != ""
	})
	var draft string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "queued B"
		if h.app.sendCurrent() {
			t.Fatal("conversation B was accepted while capacity 1 was occupied")
		}
		draft = ct.Draft
	})
	if draft != "queued B" {
		t.Fatalf("rejected B lost its draft: %q", draft)
	}
	release()
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted
	})
	h.pump.do(func() {
		if !h.app.sendCurrent() {
			t.Fatalf("conversation B did not run after the work slot released: %s", h.app.selectedContact().SendError)
		}
	})
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.ActiveTaskID == "" && ct.SendState == sendIdle && !ct.HistoryDirty &&
			len(ct.Messages) > 0 && strings.Contains(ct.Messages[len(ct.Messages)-1].Text, "nagi-118 answer")
	})
	if provider.count() != 2 {
		t.Fatalf("provider requests = %d, want 2", provider.count())
	}
	h.shutdown(t)
}

func historyTexts(records []historyRecord) []string {
	var out []string
	for _, record := range records {
		if record.Record.Type != "messages" {
			continue
		}
		for _, item := range record.Record.Items {
			if item.Type == "message" {
				if text := inputText(item.Content); text != "" {
					out = append(out, text)
				}
			}
		}
	}
	return out
}

func containsText(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func TestStoppingIssueWorkDoesNotStopConversation(t *testing.T) {
	binary := requireRuntimeBinary(t)
	hold := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hold) }) }
	defer release()
	arrived := make(chan struct{})
	provider := &providerFixture{ToolFirst: false, Hold: hold, Arrived: arrived}
	h := startRoutingHarness(t, binary, provider, 2)
	var issueID string
	h.pump.do(func() {
		if !h.app.createIssue("Issue stop", "Stop only this work.") {
			t.Fatalf("createIssue: %s", h.app.issueFormError)
		}
		issueID = h.app.selectedContact().SelectedIssueID
		if !h.app.runIssue(issueID) {
			t.Fatalf("runIssue: %s", h.app.issueFormError)
		}
	})
	select {
	case <-arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("provider did not receive issue")
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID != ""
	})
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "B before stop"
		if !h.app.sendCurrent() {
			t.Fatalf("conversation B rejected: %s", ct.SendError)
		}
	})
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.ActiveTaskID == "" && ct.SendState == sendIdle && !ct.HistoryDirty
	})
	h.pump.do(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		if issue.Work.ActiveTaskID == "" {
			t.Fatal("issue work finished before cancel")
		}
		if !h.app.cancelIssue(issueID) {
			t.Fatal("cancelIssue did not accept the active issue")
		}
	})
	release()
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionInterrupted
	})
	ct := h.app.selectedContact()
	issue := ct.issueByID(issueID)
	if issue.Status != issueStatusOpen {
		t.Fatalf("stop closed the issue: %+v", issue)
	}
	if !containsText(contactMessageTexts(ct), "nagi-118 answer") {
		t.Fatal("conversation B answer disappeared after stopping A")
	}
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "B after stop"
		if !h.app.sendCurrent() {
			t.Fatalf("conversation B did not run after A stopped: %s", ct.SendError)
		}
	})
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.ActiveTaskID == "" && ct.SendState == sendIdle && !ct.HistoryDirty &&
			containsText(contactMessageTexts(ct), "B after stop")
	})
	h.shutdown(t)
}

func contactMessageTexts(ct *contact) []string {
	out := make([]string, 0, len(ct.Messages))
	for _, m := range ct.Messages {
		out = append(out, m.Text)
	}
	return out
}

func TestCapacityTwoReservesOneSlotForConversation(t *testing.T) {
	binary := requireRuntimeBinary(t)
	hold := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hold) }) }
	defer release()
	arrived := make(chan struct{})
	provider := &providerFixture{ToolFirst: false, Hold: hold, Arrived: arrived}
	h := startRoutingHarness(t, binary, provider, 2)
	var issueA, issueB string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		if !h.app.createIssue("Issue A", "First work.") {
			t.Fatalf("create A: %s", h.app.issueFormError)
		}
		issueA = ct.SelectedIssueID
		if !h.app.createIssue("Issue B", "Second work.") {
			t.Fatalf("create B: %s", h.app.issueFormError)
		}
		issueB = ct.SelectedIssueID
		if !h.app.runIssue(issueA) {
			t.Fatalf("run A: %s", h.app.issueFormError)
		}
	})
	select {
	case <-arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("provider did not receive A")
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueA)
		return issue != nil && issue.Work.ActiveTaskID != ""
	})
	h.pump.do(func() {
		if h.app.runIssue(issueB) {
			t.Fatal("second work execution was admitted at capacity 2")
		}
		issue := h.app.selectedContact().issueByID(issueB)
		if len(issue.Executions) != 0 || !strings.Contains(issue.Work.SendError, "Work capacity") {
			t.Fatalf("second work admission = exec=%+v error=%q", issue.Executions, issue.Work.SendError)
		}
	})
	release()
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueA)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted
	})
	h.pump.do(func() {
		if !h.app.runIssue(issueB) {
			t.Fatalf("second work did not run after A released: %s", h.app.issueFormError)
		}
	})
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueB)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted
	})
	if provider.count() != 2 {
		t.Fatalf("provider requests = %d, want 2", provider.count())
	}
	h.shutdown(t)
}

func TestIssueWorkSessionUsesProfileSnapshotAtAdmission(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{ToolFirst: false}
	h := startRoutingHarness(t, binary, provider, 1)
	manual := make(chan func(), 8)
	var issueID, otherIssueID string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Purpose = "Old purpose for snapshot"
		ct.Instructions = "Old instructions for snapshot"
		if !h.app.createIssue("Snapshot issue", "Use the profile at admission.") {
			t.Fatalf("createIssue: %s", h.app.issueFormError)
		}
		issueID = ct.SelectedIssueID
		h.app.uiHook = func(fn func()) { manual <- fn }
		if !h.app.runIssue(issueID) {
			t.Fatalf("runIssue: %s", h.app.issueFormError)
		}
	})
	var mapping func()
	select {
	case mapping = <-manual:
	case <-time.After(15 * time.Second):
		t.Fatal("session mapping callback did not arrive")
	}
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Purpose = "New purpose changed during pause"
		ct.Instructions = "New instructions changed during pause"
		if !h.app.createIssue("Other issue", "Must not disturb the in-flight issue.") {
			t.Fatalf("create other issue: %s", h.app.issueFormError)
		}
		otherIssueID = ct.SelectedIssueID
	})
	h.pump.do(func() {
		h.app.uiHook = func(fn func()) { h.pump.q <- fn }
		mapping()
	})
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			issue.Work.Loaded && len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted
	})
	body := provider.body(0)
	if !strings.Contains(body, "Old purpose for snapshot") || !strings.Contains(body, "Old instructions for snapshot") {
		t.Fatalf("work request did not use the admission profile snapshot: %s", body)
	}
	if strings.Contains(body, "New purpose changed during pause") || strings.Contains(body, "New instructions changed during pause") {
		t.Fatalf("work request used a profile changed after admission: %s", body)
	}
	ct := h.app.selectedContact()
	issue := ct.issueByID(issueID)
	other := ct.issueByID(otherIssueID)
	if issue == nil || other == nil {
		t.Fatalf("issues after snapshot test: first=%+v other=%+v", issue, other)
	}
	if other.Work.SessionID != "" || len(other.Executions) != 0 {
		t.Fatalf("adding another issue changed its work target: %+v", other)
	}
	h.shutdown(t)
}

func TestIssueWorkOnContactXDoesNotBlockConversationOnContactY(t *testing.T) {
	binary := requireRuntimeBinary(t)
	hold := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hold) }) }
	defer release()
	arrived := make(chan struct{})
	provider := &providerFixture{ToolFirst: false, Hold: hold, Arrived: arrived}
	h := startRoutingHarness(t, binary, provider, 2)
	var issueID string
	h.pump.do(func() {
		if !h.app.createIssue("Contact X work", "Do not leak into contact Y.") {
			t.Fatalf("createIssue: %s", h.app.issueFormError)
		}
		issueID = h.app.selectedContact().SelectedIssueID
		if !h.app.runIssue(issueID) {
			t.Fatalf("runIssue: %s", h.app.issueFormError)
		}
	})
	select {
	case <-arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("provider did not receive X issue")
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID != ""
	})
	h.pump.do(func() {
		h.app.addDefaultContact()
		ct := h.app.selectedContact()
		ct.Draft = "Conversation on contact Y"
		if !h.app.sendCurrent() {
			t.Fatalf("contact Y conversation rejected: %s", ct.SendError)
		}
	})
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct.ActiveTaskID == "" && ct.SendState == sendIdle && !ct.HistoryDirty &&
			containsText(contactMessageTexts(ct), "Conversation on contact Y") &&
			containsText(contactMessageTexts(ct), "nagi-118 answer")
	})
	var yMessages []string
	h.snapshot(func() {
		yMessages = contactMessageTexts(h.app.selectedContact())
	})
	if containsText(yMessages, "Contact X work") {
		t.Fatalf("contact Y received contact X issue prompt: %v", yMessages)
	}
	h.pump.do(func() { h.app.selectContact(0) })
	release()
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		issue := ct.issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted
	})
	x := h.app.selectedContact()
	if containsText(contactMessageTexts(x), "Conversation on contact Y") {
		t.Fatalf("contact X chat received contact Y message: %v", contactMessageTexts(x))
	}
	issue := x.issueByID(issueID)
	if !containsText(contactMessageTextsForWork(issue), "nagi-118 answer") {
		t.Fatalf("contact X issue lost its result: %+v", issue.Work.Messages)
	}
	h.shutdown(t)
}

func contactMessageTextsForWork(issue *productIssue) []string {
	out := make([]string, 0, len(issue.Work.Messages))
	for _, m := range issue.Work.Messages {
		out = append(out, m.Text)
	}
	return out
}

func TestIssueExecutionCanBeVerifiedFromRuntimeTaskStatus(t *testing.T) {
	binary := requireRuntimeBinary(t)
	provider := &providerFixture{ToolFirst: false}
	h := startRoutingHarness(t, binary, provider, 1)
	var issueID, executionID string
	h.pump.do(func() {
		if !h.app.createIssue("Recovery issue", "Verify without retrying.") {
			t.Fatalf("createIssue: %s", h.app.issueFormError)
		}
		ct := h.app.selectedContact()
		issueID = ct.SelectedIssueID
		if !h.app.runIssue(issueID) {
			t.Fatalf("runIssue: %s", h.app.issueFormError)
		}
	})
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
			len(issue.Executions) == 1 && issue.Executions[0].Status == issueExecutionCompleted
	})
	before := provider.count()
	h.pump.do(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		executionID = issue.Executions[0].ID
		issue.Executions[0].Status = issueExecutionUnknown
		if !h.app.reconcileIssueExecution(issueID, executionID) {
			t.Fatal("reconcile did not start for a task with trusted IDs")
		}
	})
	h.pump.wait(func() bool {
		issue := h.app.selectedContact().issueByID(issueID)
		return issue != nil && issue.Executions[0].Status == issueExecutionCompleted
	})
	if provider.count() != before {
		t.Fatalf("verification retried the task: %d -> %d", before, provider.count())
	}
	h.pump.do(func() {
		issue := h.app.selectedContact().issueByID(issueID)
		issue.Executions[0].TaskID = ""
		issue.Executions[0].Status = issueExecutionUnknown
		if h.app.reconcileIssueExecution(issueID, executionID) {
			t.Fatal("reconcile accepted an execution without a task ID")
		}
	})
	if !strings.Contains(h.app.selectedContact().issueByID(issueID).Executions[0].Detail, "No trusted task association") {
		t.Fatal("missing-task recovery did not remain explicitly unknown")
	}
	h.shutdown(t)
}

func TestIssueWorkMessageDoesNotBecomeConversationUnread(t *testing.T) {
	ct := &contact{ID: "x", Name: "X", SessionID: "conversation-x", Loaded: true}
	issue := &productIssue{ID: "i1", ContactID: "x", Goal: "work", WorkSessionID: "work-i1"}
	issue.Work.SessionID = "work-i1"
	issue.Work.ActiveTaskID = "t1"
	ct.Issues = []*productIssue{issue}
	other := &contact{ID: "y", Name: "Y"}
	a := &appModel{contacts: []*contact{ct, other}, selected: 1, scrollTo: -1}
	a.applyIssueEvent(ct, issue, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "work-i1", TaskID: "t1",
		Event: &runtimeEvent{Type: "response_text", ItemID: "text-1", Text: "issue result"},
	})
	if len(ct.Messages) != 0 || ct.Unread != 0 {
		t.Fatalf("issue message entered conversation/unread: messages=%d unread=%d", len(ct.Messages), ct.Unread)
	}
	if len(issue.Work.Messages) != 1 || issue.Work.Messages[0].Text != "issue result" {
		t.Fatalf("issue work message missing: %+v", issue.Work.Messages)
	}
}

func TestLateIssueTaskTerminalDoesNotReplaceCurrentWork(t *testing.T) {
	ct := &contact{ID: "x", Name: "X", Loaded: true}
	issue := &productIssue{ID: "i1", ContactID: "x", Goal: "work"}
	issue.Work.SessionID = "work-i1"
	issue.Work.ActiveTaskID = "t2"
	issue.Work.LastTaskID = "t2"
	issue.Work.SendState = sendAccepted
	issue.Executions = []*issueExecution{
		{ID: "e1", SessionID: "work-i1", TaskID: "t1", Status: issueExecutionCompleted},
		{ID: "e2", SessionID: "work-i1", TaskID: "t2", Status: issueExecutionRunning},
	}
	ct.Issues = []*productIssue{issue}
	ct.Works = []*workItem{{TaskID: "t1", State: workCompleted}, {TaskID: "t2", State: workRunning}}
	a := &appModel{contacts: []*contact{ct}, selected: 0, scrollTo: -1}
	a.applyIssueEvent(ct, issue, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "work-i1", TaskID: "t1",
		Event: &runtimeEvent{Type: "task_finished", Status: "failed"},
	})
	if issue.Work.ActiveTaskID != "t2" || issue.Work.SendState == sendIdle {
		t.Fatalf("late terminal replaced current issue work: %+v", issue.Work)
	}
	if issue.Executions[0].Status != issueExecutionCompleted || issue.Executions[1].Status != issueExecutionRunning {
		t.Fatalf("late terminal rewrote executions: %+v", issue.Executions)
	}
}
