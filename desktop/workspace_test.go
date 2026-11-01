package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func workspaceTestApp(t *testing.T) (*appModel, *contact) {
	t.Helper()
	ct := &contact{ID: "saki", Name: "Saki", Role: "Assistant", SessionID: "s1", Loaded: true}
	a := &appModel{
		demo: false, runtimeStatus: runtimeStateReady, workspaceOpen: true,
		windowWidth: 1040, scrollTo: -1, contacts: []*contact{ct},
	}
	return a, ct
}

func TestThreePaneBreakpointsAndToggles(t *testing.T) {
	a, _ := workspaceTestApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("No issues in Unfinished.") || !tr.HasText("Conversation work") {
		t.Fatal("new work panel should show the issue overview and local work entry")
	}
	if r, ok := tr.Find("No issues in Unfinished."); !ok || r.X < 620 {
		t.Fatalf("compact work overview x = %v, want >= 620", r.X)
	}

	if err := tr.Click("Hide"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if tr.HasText("Hide") || !tr.HasText("Contacts") {
		t.Fatal("hiding the contact rail should expose a Contacts entry")
	}
	if !tr.Focused("Contacts") {
		t.Fatal("collapsing the rail should return focus to Contacts")
	}
	if err := tr.Click("Contacts"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("Hide") {
		t.Fatal("Contacts did not reopen the rail")
	}

	if err := tr.Click("Close work"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if tr.HasText("No issues in Unfinished.") || !tr.Focused("Work") {
		t.Fatal("closing work should hide it and return focus to Work")
	}
	if err := tr.Click("Work"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("No issues in Unfinished.") || !tr.Focused("Close work") {
		t.Fatal("opening work should show it and focus Close work")
	}

	a.windowWidth = 960
	tr960 := ui.NewTester(a.view, 960, 640)
	tr960.Frame()
	if !a.workOverlay() || !tr960.HasText("Back") {
		t.Fatal("960 should use the non-modal detail page when the default columns do not fit")
	}
	a.windowWidth = 1380
	trWide := ui.NewTester(a.view, 1380, 900)
	trWide.Frame()
	if a.workOverlay() {
		t.Fatal("1380 should keep the work panel as a third column")
	}
	if r, ok := trWide.Find("No issues in Unfinished."); !ok || r.X < 1000 {
		t.Fatalf("wide work overview x = %v, want >= 1000", r.X)
	}

	a.windowWidth = 900
	trNarrow := ui.NewTester(a.view, 900, 640)
	trNarrow.Frame()
	if !trNarrow.HasText("Back") {
		t.Fatal("narrow work detail should be an application page")
	}
	if err := trNarrow.Click("Close work"); err != nil {
		t.Fatal(err)
	}
	trNarrow.Frame()
	if a.workspaceOpen || !trNarrow.Focused("Work") {
		t.Fatal("narrow detail page did not close to the Work entry")
	}
}

func TestWorkProjectionKeepsTextAndToolsSeparate(t *testing.T) {
	a, ct := workspaceTestApp(t)
	other := &contact{ID: "mio", Name: "Mio", Role: "Assistant"}
	a.contacts = append(a.contacts, other)

	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "task_started"},
	})
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "tool_started", CallID: "k1", Name: "read", Arguments: `{"path":"README.md"}`},
	})
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "tool_finished", CallID: "k1", Name: "read", Output: []byte(`{"content":"marker"}`)},
	})
	if len(ct.Messages) != 0 {
		t.Fatalf("tool-only work created chat messages: %+v", ct.Messages)
	}
	if len(ct.Works) != 1 || len(ct.Works[0].Tools) != 1 {
		t.Fatalf("work projection = %+v", ct.Works)
	}
	if ct.Works[0].Tools[0].Output != `{"content":"marker"}` || !ct.Works[0].Tools[0].Finished {
		t.Fatalf("tool = %+v", ct.Works[0].Tools[0])
	}

	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "response_text_delta", ItemID: "a1", Delta: "Hel"},
	})
	if ct.Works[0].State != workRunning || len(ct.Messages) != 1 || ct.Messages[0].Text != "Hel" {
		t.Fatalf("text during work changed state/projection: work=%+v messages=%+v", ct.Works[0], ct.Messages)
	}
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "response_text", ItemID: "a1", Text: "Hello"},
	})
	if len(ct.Messages) != 1 || ct.Messages[0].Text != "Hello" || ct.Works[0].State != workRunning {
		t.Fatalf("final text aggregated incorrectly: messages=%+v work=%+v", ct.Messages, ct.Works[0])
	}
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "task_finished", Status: "completed"},
	})
	if len(ct.Messages) != 1 || ct.Works[0].State != workCompleted {
		t.Fatalf("no-message finish added a bubble or lost work: messages=%d work=%+v", len(ct.Messages), ct.Works[0])
	}
}

func TestUnreadOnlyForNewAgentMessages(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.selected = 1
	a.contacts = append(a.contacts, &contact{ID: "mio", Name: "Mio", Role: "Assistant"})
	ct.ActiveTaskID = "t1"
	ct.LastTaskID = "t1"
	a.ensureWork(ct, "t1", "m1")
	ct.Messages = nil

	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "response_text_delta", ItemID: "a1", Delta: "hello"},
	})
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "response_text", ItemID: "a1", Text: "hello"},
	})
	if ct.Unread != 1 {
		t.Fatalf("new agent message unread = %d, want 1", ct.Unread)
	}
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "task_finished", Status: "completed"},
	})
	if ct.Unread != 1 {
		t.Fatalf("task finish changed chat unread: %d", ct.Unread)
	}
}

func TestLateAndStopEventsDoNotCrossTasks(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.ActiveTaskID = "t1"
	ct.LastTaskID = "t1"
	w := a.ensureWork(ct, "t1", "m1")
	w.State = workRunning
	a.applyCancelResult(ct.ID, "t2", "requested", nil)
	if w.State != workRunning {
		t.Fatal("cancel for another task changed current work")
	}
	a.applyCancelResult(ct.ID, "t1", "requested", nil)
	if w.State != workStopping {
		t.Fatalf("cancel state = %q, want stopping", w.State)
	}
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "task_finished", Status: "interrupted"},
	})
	if w.State != workInterrupted || ct.ActiveTaskID != "" {
		t.Fatalf("stop terminal = work %q active %q", w.State, ct.ActiveTaskID)
	}
	if got := workStateLabel(w); !strings.Contains(got, "Stopped") || !strings.Contains(got, "interrupted") {
		t.Fatalf("stop terminal label = %q", got)
	}

	ct.ActiveTaskID = "t2"
	ct.LastTaskID = "t2"
	w2 := a.ensureWork(ct, "t2", "m2")
	w2.State = workRunning
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "tool_started", CallID: "old", Name: "read"},
	})
	if len(w.Tools) != 0 || len(w2.Tools) != 0 {
		t.Fatal("late event from old task polluted another work item")
	}
	if !strings.Contains(ct.Work, "Running") && ct.Work != "" {
		t.Fatalf("unexpected work summary: %q", ct.Work)
	}
	beforeMessages := len(ct.Messages)
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "task_finished", Status: "completed"},
	})
	if ct.LastTaskID != "t2" || len(ct.Messages) != beforeMessages || w2.State != workRunning {
		t.Fatalf("late terminal polluted current work: last=%q work=%+v messages=%d", ct.LastTaskID, w2, len(ct.Messages))
	}
}

func TestDuplicateStartCannotReviveTerminalWork(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.ActiveTaskID = ""
	ct.LastTaskID = "t1"
	w := a.ensureWork(ct, "t1", "m1")
	w.State = workCompleted
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "task_started"},
	})
	if ct.ActiveTaskID != "" || w.State != workCompleted {
		t.Fatalf("duplicate start revived terminal task: active=%q work=%+v", ct.ActiveTaskID, w)
	}

	// A genuinely accepted next task may start, and the old task's terminal
	// cannot claim it.
	ct.ActiveTaskID = "t2"
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t2", MessageID: "m2",
		Event: &runtimeEvent{Type: "task_started"},
	})
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "task_finished", Status: "failed"},
	})
	if ct.ActiveTaskID != "t2" || ct.LastTaskID != "t2" || w.State != workCompleted {
		t.Fatalf("late old terminal polluted next task: active=%q last=%q old=%q", ct.ActiveTaskID, ct.LastTaskID, w.State)
	}
}

func TestWorkModalEscapeReturnsFocusToEntry(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.windowWidth = 900
	tr := ui.NewTester(a.view, 900, 640)
	tr.Frame()
	if !tr.HasText("Back") {
		t.Fatal("narrow work page missing")
	}
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if a.workspaceOpen || !tr.Focused("Work") {
		t.Fatal("Escape did not close/focus-return the work page")
	}
}

func TestHistoryModalPreservesWorkAndReturnsFocus(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.ActiveTaskID = "t1"
	ct.LastTaskID = "t1"
	ct.Draft = "later draft"
	w := a.ensureWork(ct, "t1", "m1")
	w.State = workRunning
	ct.Messages = []*message{{Key: "hist:1", Kind: msgContact, Text: "earlier answer"}}
	ct.History = []historyEntry{{ID: "hist:1", Date: "Yesterday", From: "contact", Text: "earlier answer"}}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("History"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.detailIs(detailHistory) || !tr.HasText("Close history") || !tr.Focused("Close history") {
		t.Fatal("history modal did not open/focus")
	}
	if err := tr.Click("Close history"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.workspaceOpen || !tr.Focused("History") || tr.HasText("History: Saki") {
		t.Fatal("closing history did not return to chat with the History entry focused")
	}
	if ct.ActiveTaskID != "t1" || w.State != workRunning || ct.Draft != "later draft" {
		t.Fatal("history modal changed work or draft")
	}

	if err := tr.Click("History"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if a.workspaceOpen || !tr.Focused("History") {
		t.Fatal("Escape did not close history/focus its entry")
	}
	if err := tr.Click("History"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("View in conversation"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.workspaceOpen || a.highlight != "hist:1" {
		t.Fatal("view-in-conversation did not close/return to chat")
	}
}

func TestHistoryModalIncludesLiveTextBeforeReload(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Messages = []*message{
		{Key: "live:t1:user", Kind: msgUser, Text: "live question"},
		{Key: "live:t1:text:a1", Kind: msgContact, Text: "live answer"},
	}
	a.openHistory()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("live question") || !tr.HasText("live answer") {
		t.Fatalf("live text missing from History: %v", tr.Texts())
	}
	if tr.HasText("No saved messages") {
		t.Fatal("live text was reported as no saved messages")
	}
}

func TestHistoryEmptyQueryDoesNotClaimNoMatch(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.History = nil
	a.openHistory()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if tr.HasText("Try a shorter phrase") {
		t.Fatal("empty history was reported as a failed search")
	}
	if !tr.HasText("No saved messages") {
		t.Fatal("empty history did not use the saved-message empty state")
	}
}

func TestSettingsPageEscapeDoesNotLeavePageAndBackReturnsToThreePane(t *testing.T) {
	a, _ := workspaceTestApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || !tr.HasText("Settings") {
		t.Fatal("settings page did not open")
	}
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if !a.settingsOpen {
		t.Fatal("Escape incorrectly closed the settings page")
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.settingsOpen || !tr.Focused("Settings") || !tr.HasText("Issues") {
		t.Fatal("Back did not return to the three-pane work view with entry focus")
	}
}

func TestSingleExecutionSlotBlocksOtherContact(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.ActiveTaskID = "t1"
	ct.Loaded = true
	other := &contact{ID: "mio", Name: "Mio", Role: "Assistant", SessionID: "s2", Loaded: true}
	a.contacts = append(a.contacts, other)
	a.selected = 1
	a.rt = &runtimeClient{}
	a.runtimeStatus = runtimeStateReady
	reason := a.canSend(other)
	if !strings.Contains(reason, "Another contact is working") {
		t.Fatalf("second contact reason = %q", reason)
	}
	if other.Draft != "" || ct.ActiveTaskID != "t1" {
		t.Fatal("busy guard changed drafts or active task")
	}
}

func TestUnloadedSessionDoesNotClaimEmptyHistory(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.SessionID = "s1"
	ct.Loaded = false
	a.runtimeStatus = runtimeStateFailed
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	openConversationWorkPanel(t, tr)
	if tr.HasText("No messages yet") || tr.HasText("No current work") {
		t.Fatalf("unloaded session was presented as truly empty: %v", tr.Texts())
	}
	for _, want := range []string{"Conversation history is not loaded", "Session history is not loaded", "Runtime is not ready"} {
		if !tr.HasText(want) {
			t.Errorf("unloaded state missing %q", want)
		}
	}
}

func TestWorkSurvivesNavigationSwitchAndDetails(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.ActiveTaskID = "t1"
	ct.LastTaskID = "t1"
	ct.Draft = "later draft"
	w := a.ensureWork(ct, "t1", "m1")
	w.State = workRunning
	other := &contact{ID: "mio", Name: "Mio", Role: "Assistant", Draft: "mio draft"}
	a.contacts = append(a.contacts, other)

	a.selectContact(1)
	if ct.ActiveTaskID != "t1" || w.State != workRunning || ct.Draft != "later draft" {
		t.Fatal("switching contacts stopped or cleared work")
	}
	a.toggleWorkspace()
	if a.workspaceOpen {
		t.Fatal("workspace did not close")
	}
	a.toggleWorkspace()
	if !a.workspaceOpen {
		t.Fatal("workspace did not reopen")
	}
	a.openHistory()
	a.backToChat()
	a.openSettings()
	a.closeAux()
	if ct.ActiveTaskID != "t1" || w.State != workRunning || ct.Draft != "later draft" {
		t.Fatal("history/settings round trip changed work")
	}
	a.selectContact(0)
	if ct.ActiveTaskID != "t1" || w.State != workRunning || ct.Draft != "later draft" {
		t.Fatal("returning to the contact lost work")
	}
}

func TestFailedWorkAddsAttentionButNotUnread(t *testing.T) {
	a, ct := workspaceTestApp(t)
	other := &contact{ID: "mio", Name: "Mio", Role: "Assistant"}
	a.contacts = append(a.contacts, other)
	a.selected = 1
	ct.ActiveTaskID = "t1"
	ct.LastTaskID = "t1"
	w := a.ensureWork(ct, "t1", "m1")
	w.State = workRunning
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "task_finished", Status: "failed", Error: &runtimeError{Code: "tool_error", Message: "tool failed"}},
	})
	if w.State != workFailed || ct.Status != statusAttention || ct.Unread != 0 {
		t.Fatalf("failed work state=%q contact=%v unread=%d", w.State, ct.Status, ct.Unread)
	}
}

func TestUnlinkedHistoryToolsRemainReadable(t *testing.T) {
	a, ct := workspaceTestApp(t)
	records := []historyRecord{
		{Offset: 0, Record: journalRecord{Type: "messages", Items: []inputItem{
			{Type: "tool_call", CallID: "k1", Name: "read", Arguments: `{"path":"old.txt"}`},
		}}},
		{Offset: 1, Record: journalRecord{Type: "messages", Items: []inputItem{
			{Type: "tool_result", CallID: "k1", Output: `{"content":"old marker"}`},
		}}},
		{Offset: 2, Record: journalRecord{Type: "messages", Items: []inputItem{
			{Type: "message", Role: "assistant", Content: []byte(`"old answer"`)},
		}}},
	}
	a.applyHistory(ct, records, "completed")
	if len(ct.Works) != 0 || len(ct.UnlinkedTools) != 1 {
		t.Fatalf("unlinked projection = works %+v tools %+v", ct.Works, ct.UnlinkedTools)
	}
	if len(ct.Messages) != 1 || ct.Messages[0].Text != "old answer" {
		t.Fatalf("text history changed: %+v", ct.Messages)
	}
	if ct.UnlinkedTools[0].Output != `{"content":"old marker"}` {
		t.Fatalf("unlinked output = %+v", ct.UnlinkedTools[0])
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	openConversationWorkPanel(t, tr)
	if !tr.HasText("Earlier activity") || !tr.HasText("old marker") {
		t.Fatalf("unlinked tool history not readable: %v", tr.Texts())
	}
}

func TestWorkPanelRendersToolDetailsWithoutInference(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Works = []*workItem{{
		TaskID: "t1", State: workRunning, Prompt: "Inspect the file.",
		Tools: []*workTool{{
			CallID: "k1", Name: "read", Args: `{"path":"README.md"}`,
			Output: `{"content":"first heading"}`, Started: true, Finished: true,
		}},
	}}
	ct.ActiveTaskID = "t1"
	ct.LastTaskID = "t1"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	openConversationWorkPanel(t, tr)
	for _, want := range []string{"Current work", "read", "first heading", "No tool activity yet."} {
		if want == "No tool activity yet." {
			if tr.HasText(want) {
				t.Fatal("work panel should not add an empty-activity placeholder when tools exist")
			}
			continue
		}
		if !tr.HasText(want) {
			t.Errorf("work panel missing %q", want)
		}
	}
	if tr.HasText("Delivered file") || tr.HasText("artifact") {
		t.Fatal("tool output must not be inferred as a delivered file")
	}
	if err := tr.Click("Copy received output"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tr.Clipboard(), "first heading") {
		t.Fatalf("collapsed copy did not preserve raw tool output: %q", tr.Clipboard())
	}
	if err := tr.Click("Details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("content") {
		t.Fatalf("raw tool output not visible after Details: %v", tr.Texts())
	}
	if tr.HasText("Copy full output") {
		t.Fatal("expanded tool details duplicated the full-output copy control")
	}
	if err := tr.Click("Copy received output"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tr.Clipboard(), "first heading") {
		t.Fatalf("copy did not preserve raw tool output: %q", tr.Clipboard())
	}
}

func TestWorkspaceToolCardActionsStayVisibleAtAllWidths(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.ActiveTaskID = "t1"
	ct.LastTaskID = "t1"
	w := a.ensureWork(ct, "t1", "m1")
	w.State = workRunning
	w.Prompt = "long work"
	longPath := "/Users/someone/" + strings.Repeat("非常长的目录/", 20) + "file.json"
	output := `{"content":"` + strings.Repeat("x", 240) + `","path":"` + longPath + `"}`
	w.Tools = []*workTool{{CallID: "c1", Name: "read", Finished: true, Args: `{"path":"` + longPath + `"}`, Output: output}}

	for _, size := range []struct{ width, height int }{{1040, 720}, {960, 640}, {720, 640}, {1380, 900}} {
		a.windowWidth = size.width
		w.Tools[0].Expanded = false
		tr := ui.NewTester(a.view, size.width, size.height)
		tr.Frame()
		openConversationWorkPanel(t, tr)
		panel, ok := tr.Find("Activity")
		if !ok || panel.W <= 0 {
			t.Fatalf("%dx%d activity panel = %+v ok=%v", size.width, size.height, panel, ok)
		}
		for _, label := range []string{"Finished", "Copy received output", "Details"} {
			r, ok := tr.Find(label)
			if !ok || r.W <= 0 || r.H <= 0 || r.X < panel.X || r.X+r.W > panel.X+panel.W ||
				r.Y < panel.Y || r.X < 0 || r.X+r.W > float32(size.width) || r.Y+r.H > float32(size.height) {
				t.Fatalf("%dx%d %q bounds = %+v panel=%+v ok=%v", size.width, size.height, label, r, panel, ok)
			}
		}
		tr.SetClipboard("sentinel")
		if err := tr.Click("Copy received output"); err != nil {
			t.Fatalf("%dx%d copy click: %v", size.width, size.height, err)
		}
		if got := tr.Clipboard(); got != output {
			t.Fatalf("%dx%d copied %q", size.width, size.height, got)
		}
		if err := tr.Click("Details"); err != nil {
			t.Fatalf("%dx%d details click: %v", size.width, size.height, err)
		}
		tr.Frame()
		if !w.Tools[0].Expanded || !tr.HasText("Hide") {
			t.Fatalf("%dx%d details did not expand", size.width, size.height)
		}
	}
}

func TestWorkspaceStructuredToolOutputDoesNotLeakRawJSON(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.ActiveTaskID = "t-write"
	ct.LastTaskID = "t-write"
	w := a.ensureWork(ct, "t-write", "m-write")
	w.State = workRunning
	raw := `{"path":"/Users/someone/result.txt","bytes_written":42}`
	w.Tools = []*workTool{{CallID: "write-call", Name: "write", Args: `{"path":"/Users/someone/result.txt"}`, Finished: true, Output: raw}}
	a.windowWidth = 1040
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	openConversationWorkPanel(t, tr)
	if tr.HasText(`"bytes_written"`) || tr.HasText(`{"path"`) {
		t.Fatalf("raw structured result leaked into preview: %v", tr.Texts())
	}
	if !tr.HasText("Structured output received; open Details for the exact record.") {
		t.Fatalf("neutral structured summary missing: %v", tr.Texts())
	}
	tr.SetClipboard("")
	if err := tr.Click("Copy received output"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != raw {
		t.Fatalf("copy lost exact structured output: %q", got)
	}
	if err := tr.Click("Details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText(`"bytes_written"`) {
		t.Fatalf("details did not expose exact structured output: %v", tr.Texts())
	}
}
