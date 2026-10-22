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
	if !tr.HasText("No current work") {
		t.Fatal("new work panel should show a real empty state")
	}
	if r, ok := tr.Find("No current work"); !ok || r.X < 620 {
		t.Fatalf("compact work panel x = %v, want >= 620", r.X)
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
	if tr.HasText("No current work") || !tr.Focused("Work") {
		t.Fatal("closing work should hide it and return focus to Work")
	}
	if err := tr.Click("Work"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("No current work") || !tr.Focused("Close work") {
		t.Fatal("opening work should show it and focus Close work")
	}

	a.windowWidth = 960
	tr960 := ui.NewTester(a.view, 960, 640)
	tr960.Frame()
	if r, ok := tr960.Find("No current work"); !ok || r.X < 580 {
		t.Fatalf("960 compact work panel x = %v, want >= 580", r.X)
	}
	a.windowWidth = 1200
	trWide := ui.NewTester(a.view, 1200, 800)
	trWide.Frame()
	if r, ok := trWide.Find("No current work"); !ok || r.X < 780 {
		t.Fatalf("wide work panel x = %v, want >= 780", r.X)
	}

	a.windowWidth = 900
	trNarrow := ui.NewTester(a.view, 900, 640)
	trNarrow.Frame()
	if !trNarrow.HasText("Close work") {
		t.Fatal("narrow work panel should be present as an overlay")
	}
	if err := trNarrow.Click("Close work"); err != nil {
		t.Fatal(err)
	}
	trNarrow.Frame()
	if trNarrow.HasText("No current work") {
		t.Fatal("narrow overlay did not close")
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
	if err := tr.Click("Details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("content") {
		t.Fatalf("raw tool output not visible after Details: %v", tr.Texts())
	}
}
