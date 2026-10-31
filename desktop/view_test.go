package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestViewRendersBothThemes(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	for _, want := range []string{"Saki", "Mio", "Ren", "Send", "History", "Settings"} {
		if !tr.HasText(want) {
			t.Fatalf("light frame is missing %q", want)
		}
	}
	tr.SetDark(true)
	tr.Frame()
	if !tr.HasText("Saki") {
		t.Fatal("dark frame should render the selected contact")
	}
	tr.SetDark(false)
	tr.Frame()
}

// TestMinimumWindowRenders checks the 960x640 window minimum with the
// sidebar, chat header, and composer. The right workspace starts closed.
func TestMinimumWindowRenders(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 960, 640)
	tr.Frame()
	if !tr.HasText("Send") || !tr.HasText("Saki") {
		t.Fatal("chat should render at the 960x640 minimum")
	}
	a.openSettings()
	tr.Frame()
	if !tr.HasText("Save changes") {
		t.Fatal("settings should render at the 960x640 minimum")
	}
	a.closeSettings()
	tr.Frame()
	a.addDefaultContact()
	tr.Frame()
	if !tr.HasText("New contact 1") {
		t.Fatal("the default contact should render at the 960x640 minimum")
	}
}

// focusComposer clicks the labeled composer and verifies it holds the IME
// caret.
func focusComposer(t *testing.T, tr *ui.Tester, label string) {
	t.Helper()
	if err := tr.Click(label); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if _, active := tr.TextCaret(); !active {
		t.Fatalf("%s should hold the IME caret", label)
	}
}

// TestComposerCompositionDoesNotSendUntilCommit exercises the IME path: a
// composition must not send, and Enter after the committed text does.
func TestComposerCompositionDoesNotSendUntilCommit(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	focusComposer(t, tr, "Composer for Saki")
	before := len(a.selectedContact().Messages)
	tr.Compose("你好 Saki", 4)
	tr.Frame()
	if len(a.selectedContact().Messages) != before {
		t.Fatal("composing must not send a message")
	}
	tr.Compose("", 0)  // end the composition
	tr.Type("你好 Saki") // commit the text
	tr.Frame()
	if got := a.selectedContact().Draft; got != "你好 Saki" {
		t.Fatalf("committed draft = %q, want %q", got, "你好 Saki")
	}
	// Shift+Enter breaks the line instead of sending.
	tr.Key(ui.Shift, ui.KeyEnter)
	tr.Type("second line")
	tr.Frame()
	const payload = "你好 Saki\nsecond line"
	if got := a.selectedContact().Draft; got != payload {
		t.Fatalf("multiline draft = %q, want %q", got, payload)
	}
	// A bare Enter sends the exact multiline payload.
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	msgs := a.selectedContact().Messages
	if len(msgs) != before+2 {
		t.Fatalf("Enter should send once and append the demo reply: %d -> %d", before, len(msgs))
	}
	if got := msgs[len(msgs)-2].Text; got != payload {
		t.Fatalf("sent text = %q, want %q", got, payload)
	}
	if a.selectedContact().Draft != "" {
		t.Fatalf("accepted send should clear the draft, got %q", a.selectedContact().Draft)
	}
}

// TestDraftSwitchThroughTester drives the labeled view: type a Mio draft,
// switch to Saki and type a Saki draft, switch back to Mio, then send the
// restored Mio draft with Enter. That exercises the editor's e.Text(), not
// just the state fields.
func TestDraftSwitchThroughTester(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	if err := tr.Click("Contact Mio"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.selected != 1 {
		t.Fatalf("labeled click selected contact %d, want 1", a.selected)
	}
	focusComposer(t, tr, "Composer for Mio")
	tr.Type("draft for mio")
	tr.Frame()
	if a.contacts[1].Draft != "draft for mio" {
		t.Fatalf("Mio draft = %q", a.contacts[1].Draft)
	}
	if err := tr.Click("Contact Saki"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.selected != 0 {
		t.Fatal("did not switch to Saki")
	}
	focusComposer(t, tr, "Composer for Saki")
	tr.Type("saki draft")
	tr.Frame()
	if a.contacts[0].Draft != "saki draft" {
		t.Fatalf("Saki draft = %q", a.contacts[0].Draft)
	}
	if a.contacts[1].Draft != "draft for mio" {
		t.Fatal("typing for Saki changed Mio's draft")
	}
	if err := tr.Click("Contact Mio"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.selected != 1 {
		t.Fatal("did not return to Mio")
	}
	focusComposer(t, tr, "Composer for Mio")
	before := len(a.contacts[1].Messages)
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	msgs := a.contacts[1].Messages
	if len(msgs) != before+2 {
		t.Fatalf("Enter should send Mio's restored draft: %d -> %d", before, len(msgs))
	}
	if got := msgs[len(msgs)-2].Text; got != "draft for mio" {
		t.Fatalf("sent %q, want the restored Mio draft", got)
	}
	if a.contacts[1].Draft != "" {
		t.Fatalf("accepted send should clear Mio's draft, got %q", a.contacts[1].Draft)
	}
	if a.contacts[0].Draft != "saki draft" {
		t.Fatalf("Saki draft changed: %q", a.contacts[0].Draft)
	}
}

// TestAddContactFocusesComposerAndSends clicks Add contact twice through the
// labeled view. Each time it types and presses Enter without any extra click;
// each message must belong to the contact just created, and every existing
// contact's messages and draft must stay untouched. The second creation is
// the one the reviewer asked for: it proves the one-shot focus works again
// and that the two new contacts do not share drafts or messages.
func TestAddContactFocusesComposerAndSends(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	beforeMsgs := make([]int, len(a.contacts))
	beforeDrafts := make([]string, len(a.contacts))
	for i := range a.contacts {
		beforeMsgs[i] = len(a.contacts[i].Messages)
		beforeDrafts[i] = a.contacts[i].Draft
	}
	assertExistingUnchanged := func(when string) {
		t.Helper()
		for i := range beforeMsgs {
			if len(a.contacts[i].Messages) != beforeMsgs[i] || a.contacts[i].Draft != beforeDrafts[i] {
				t.Fatalf("%s: existing contact %d changed: msgs %d->%d draft %q->%q", when, i, beforeMsgs[i], len(a.contacts[i].Messages), beforeDrafts[i], a.contacts[i].Draft)
			}
		}
	}

	// First creation.
	if err := tr.Click("Add contact"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	first := a.selectedContact()
	if first.Name != "New contact 1" || first.Role != "Assistant" {
		t.Fatalf("created contact = %q role=%q", first.Name, first.Role)
	}
	if !tr.Focused("Composer for " + first.Name) {
		t.Fatal("first creation should focus its composer without a click")
	}
	tr.Type("hello default")
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	if len(first.Messages) != 2 {
		t.Fatalf("first contact messages = %d, want user + demo reply", len(first.Messages))
	}
	if got := first.Messages[0]; got.Kind != msgUser || got.Text != "hello default" {
		t.Fatalf("first message = %+v", got)
	}
	assertExistingUnchanged("after first send")

	// Second creation through the same UI path, again with no extra click.
	if err := tr.Click("Add contact"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	second := a.selectedContact()
	if second == first {
		t.Fatal("the second Add contact must create a new contact")
	}
	if second.Name != "New contact 2" || second.Role != "Assistant" {
		t.Fatalf("second contact = %q role=%q", second.Name, second.Role)
	}
	if !tr.Focused("Composer for " + second.Name) {
		t.Fatal("second creation should focus its composer without a click")
	}
	tr.Type("second hello")
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	if len(second.Messages) != 2 {
		t.Fatalf("second contact messages = %d, want user + demo reply", len(second.Messages))
	}
	if got := second.Messages[0]; got.Kind != msgUser || got.Text != "second hello" {
		t.Fatalf("second contact message = %+v, want the exact typed input", got)
	}
	// The first new contact still holds exactly its own message and no
	// draft; the second send must not have leaked into it.
	if len(first.Messages) != 2 || first.Messages[0].Text != "hello default" {
		t.Fatalf("first new contact was changed by the second send: %+v", first.Messages[0])
	}
	if first.Draft != "" || second.Draft != "" {
		t.Fatalf("sent send should clear both drafts: first=%q second=%q", first.Draft, second.Draft)
	}
	assertExistingUnchanged("after second send")
}

// TestSettingsFieldsReceiveInputAndDoNotStealComposerFocus verifies through
// the UI, not private flags, that a Settings field really takes typed input
// while the composer keeps its draft, and that Tab plus further frames do not
// pull focus back to the composer.
func TestSettingsFieldsReceiveInputAndDoNotStealComposerFocus(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	if err := tr.Click("Add contact"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	ct := a.selectedContact()
	tr.Type("keep me")
	tr.Frame()
	if ct.Draft != "keep me" {
		t.Fatalf("composer draft = %q", ct.Draft)
	}
	if err := tr.Click("Contact profile"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Edit full profile"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	tr.Frame() // a second frame must not re-focus the composer
	if tr.Focused("Composer for " + ct.Name) {
		t.Fatal("opening Settings must not keep composer focus")
	}

	// Explicitly focus the Settings name field through the UI and type.
	nameBefore := a.contactProfile.Draft.Name
	if err := tr.Click("Contact name"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Contact name") {
		t.Fatal("clicking the Settings name field should focus it")
	}
	tr.Type(" edited")
	tr.Frame()
	if a.contactProfile.Draft.Name != nameBefore+" edited" {
		t.Fatalf("Settings name field did not receive the typed input: %q", a.contactProfile.Draft.Name)
	}
	if ct.Draft != "keep me" {
		t.Fatalf("typing in Settings changed the composer draft: %q", ct.Draft)
	}

	// Tab moves focus to the next control. More frames must not steal it
	// back to the composer, and typing must not land in the composer.
	tr.Key(0, ui.KeyTab)
	tr.Frame()
	tr.Frame()
	if tr.Focused("Composer for " + ct.Name) {
		t.Fatal("frames after Tab must not return focus to the composer")
	}
	tr.Type("qq")
	tr.Frame()
	if ct.Draft != "keep me" {
		t.Fatalf("typing after Tab leaked into the composer: %q", ct.Draft)
	}
	if a.contactProfile.Draft.Name != nameBefore+" edited" {
		t.Fatalf("typing after Tab changed the name field: %q", a.contactProfile.Draft.Name)
	}
}

// TestSettingsSavePreservesMessagesHistoryAndDraft modifies and saves the
// profile of a contact that already has non-empty messages, history and a
// draft, and checks those are preserved; an empty name is still rejected.
func TestSettingsSavePreservesMessagesHistoryAndDraft(t *testing.T) {
	a := testApp(t)
	ct := a.selectedContact() // Saki is seeded with messages and history
	if len(ct.Messages) == 0 || len(ct.History) == 0 {
		t.Fatal("fixture must start with non-empty messages and history")
	}
	ct.Draft = "unsent draft"
	beforeMsgs := len(ct.Messages)
	beforeHist := len(ct.History)
	beforeLast := ct.Messages[beforeMsgs-1].Text

	a.openSettings()
	draft := a.contactProfile.Draft
	draft.Name = "Saki Renamed"
	draft.Purpose = "Keeps earlier decisions in view."
	draft.Workspace = t.TempDir()
	if !a.saveContactProfileDraft(draft, false) {
		t.Fatal("settings save should succeed with a name")
	}
	if ct.Name != "Saki Renamed" {
		t.Fatalf("name = %q", ct.Name)
	}
	if len(ct.Messages) != beforeMsgs || ct.Messages[beforeMsgs-1].Text != beforeLast {
		t.Fatalf("saving settings changed messages: %d -> %d", beforeMsgs, len(ct.Messages))
	}
	if len(ct.History) != beforeHist {
		t.Fatalf("saving settings changed history: %d -> %d", beforeHist, len(ct.History))
	}
	if ct.Draft != "unsent draft" {
		t.Fatalf("saving settings cleared the draft: %q", ct.Draft)
	}

	a.openSettings()
	a.contactProfile.Draft.Name = "  "
	if a.saveContactProfileDraft(a.contactProfile.Draft, false) || a.contactProfile.Errors["Name"] == "" {
		t.Fatal("empty settings name must be rejected")
	}
	if ct.Name != "Saki Renamed" {
		t.Fatalf("a rejected save must not rename the contact: %q", ct.Name)
	}
}

// TestSourceRevealScrollsOnceAndCanMoveAway covers the regression: with a
// long transcript the reveal requests one list scroll to the entry, keeps
// the highlight, and a later manual scroll can move the source out of view.
func TestSourceRevealScrollsOnceAndCanMoveAway(t *testing.T) {
	a := testApp(t)
	ct := a.selectedContact()
	for i := 0; i < 60; i++ {
		a.seq++
		ct.Messages = append(ct.Messages, &message{ID: a.seq, Kind: msgContact, Text: "filler line"})
	}
	a.selectContact(0)
	a.viewInConversation("saki-h1")
	a.viewInConversation("saki-h2") // the target is the second revealed entry
	if a.scrollTo != 1 || a.scrollTo != a.revealIndex() {
		t.Fatalf("reveal should request the entry row once: scrollTo=%d revealIndex=%d", a.scrollTo, a.revealIndex())
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if a.scrollTo != -1 {
		t.Fatalf("scroll request should be consumed by one frame, got %d", a.scrollTo)
	}
	if !tr.HasText("Keep Saki as the Rust agent core") {
		t.Fatal("the revealed source should be rendered in the list")
	}
	first, last := ct.list.Visible()
	tr.Scroll(400, 300, 0, 4000)
	tr.Frame()
	first2, last2 := ct.list.Visible()
	if first2 == first && last2 == last {
		t.Fatalf("manual scroll should move the visible window: %d..%d", first, last)
	}
	if a.scrollTo != -1 {
		t.Fatal("manual scrolling must not re-issue the reveal scroll")
	}
	if a.highlight != "saki-h2" {
		t.Fatal("the highlight must survive a manual scroll away")
	}
}

// TestPreviewImages writes headless preview PNGs for design review. It is
// inert unless SAKI_PREVIEW_DIR points at a scratch directory.
func TestPreviewImages(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR to write native preview PNGs")
	}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, scene := range []string{"chat", "new-contact", "settings", "sources", "history"} {
			a := testApp(t)
			switch scene {
			case "new-contact":
				a.addDefaultContact()
			case "settings":
				a.openSettings()
			case "sources":
				a.openSources(5)
			case "history":
				a.openHistory()
				a.historyQuery = "memory"
			}
			tr := ui.NewTester(a.view, 1040, 720)
			tr.SetDark(dark)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("native-%s-%s.png", theme, scene)), tr.Image())
		}
	}
	writeV07AppearancePreviews(t, dir)

	// Minimum window proof for the two densest panels.
	for _, scene := range []string{"chat", "settings"} {
		a := testApp(t)
		if scene == "settings" {
			a.openSettings()
		}
		tr := ui.NewTester(a.view, 960, 640)
		tr.Frame()
		writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("native-light-min-%s.png", scene)), tr.Image())
	}
}

func writePreviewPNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
}

// TestLiveTaskStatesRenderWithoutWaitingConflict drives the real event
// application and the view together: accepted-but-not-started, started,
// streaming, tool activity, and completed must not show contradictory text.
func TestLiveTaskStatesRenderWithoutWaitingConflict(t *testing.T) {
	ct := &contact{
		ID: "bound", Name: "Saki", Role: "Assistant", SessionID: "s1", Loaded: true,
		Status: statusReady, SendState: sendAccepted, ActiveTaskID: "t1", Draft: "hello",
	}
	a := &appModel{
		demo: false, runtimeStatus: runtimeStateReady, scrollTo: -1,
		workspaceOpen: true, windowWidth: 1040,
		contacts: []*contact{ct},
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	openConversationWorkPanel(t, tr)
	if !tr.HasText("Accepted; waiting for the runtime to start.") {
		t.Fatal("accepted task should show the pre-start waiting state")
	}

	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "task_started"},
	})
	tr.Frame()
	if tr.HasText("Accepted; waiting for the runtime to start.") {
		t.Fatal("started task must not keep the pre-start waiting text")
	}
	if !tr.HasText("Current work") || !tr.HasText("Working") {
		t.Fatal("started task should show the current work state")
	}

	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "response_text_delta", ItemID: "a1", Delta: "Hel"},
	})
	tr.Frame()
	if !tr.HasText("Hel") {
		t.Fatal("streaming delta should render in the conversation")
	}
	if !tr.HasText("Current work") || !tr.HasText("Working") {
		t.Fatal("streaming a message must not end the work state")
	}
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "response_text", ItemID: "a1", Text: "Hello"},
	})
	tr.Frame()
	if !tr.HasText("Hello") || tr.HasText("Hel\n") {
		t.Fatal("final text should replace the delta projection")
	}

	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "tool_started", CallID: "k1", Name: "read", Arguments: `{"path":"probe.txt"}`},
	})
	tr.Frame()
	if !tr.HasText("read") || !tr.HasText("Running") {
		t.Fatal("tool activity should render in the work panel with its running state")
	}
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "tool_finished", CallID: "k1", Name: "read", Output: []byte(`{"content":"marker"}`)},
	})
	tr.Frame()
	if !tr.HasText("marker") {
		t.Fatal("tool output should render in the work panel")
	}

	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "task_finished", Status: "completed"},
	})
	tr.Frame()
	if ct.SendState != sendIdle || ct.ActiveTaskID != "" {
		t.Fatalf("finished state = %+v", ct)
	}
	if !tr.HasText("Completed") || len(ct.Messages) != 1 {
		t.Fatalf("completed work/messages = %+v / %+v", ct.Works, ct.Messages)
	}
	if tr.HasText("Accepted; waiting for the runtime to start.") || tr.HasText("Working") {
		t.Fatal("completed task must not keep waiting/working text")
	}
}

func TestLiveSendUnknownAfterDisconnectSaysRestart(t *testing.T) {
	ct := &contact{ID: "bound", Name: "Saki", SessionID: "s1", Loaded: true, SendState: sendUnknown}
	a := &appModel{demo: false, runtimeStatus: runtimeStateExited, scrollTo: -1, contacts: []*contact{ct}}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("restart Saki") {
		t.Fatal("disconnected sendUnknown should tell the user to restart")
	}
	if tr.HasText("Reload to verify") {
		t.Fatal("disconnected client must not offer an ineffective Reload instruction")
	}

	a.runtimeStatus = runtimeStateReady
	a.rt = &runtimeClient{}
	tr.Frame()
	if !tr.HasText("Reload to verify") {
		t.Fatal("a live connection should keep the Reload instruction")
	}
}

// runtimePreviewApp builds a runtime-backed model state without starting a
// process. It is a headless layout fixture, not an online model or native
// window capture.
func runtimePreviewApp(scene string) *appModel {
	bound := &contact{
		ID: "saki", Name: "Saki", Role: "Assistant", SessionID: "preview-session", Loaded: true,
		Status: statusReady, WorkLabel: "Preparing a reply",
		Purpose: "Keep earlier decisions in view.", Instructions: "Cite earlier decisions when they shape an answer.",
		Workspace: "/Users/you/Workspace/saki",
		Messages: []*message{
			{Key: "u1", Kind: msgUser, Text: "Use read to inspect README.md and report its first heading. Do not write files."},
			{Key: "a1", Kind: msgContact, Text: "The first heading is # Saki."},
		},
		Works: []*workItem{{
			TaskID: "task-preview", MessageID: "message-preview", Prompt: "Use read to inspect README.md and report its first heading.",
			State: workCompleted,
			Tools: []*workTool{{
				CallID: "read-1", Name: "read", Args: `{"path":"README.md"}`,
				Output:  `{"content":"# Saki\n\nA small agent runtime with a shared core.","start_line":1,"end_line":2}`,
				Started: true, Finished: true,
			}},
		}},
	}
	unbound := &contact{ID: "mio", Name: "Mio", Role: "Assistant", Draft: "draft for Mio"}
	a := &appModel{
		demo: false, runtimeStatus: runtimeStateReady, workspaceOpen: true, windowWidth: 1040,
		scrollTo: -1, contacts: []*contact{bound, unbound}, selected: 0,
	}
	switch scene {
	case "runtime-working":
		bound.Status = statusWorking
		bound.Work = "Running read"
		bound.ActiveTaskID = "task-preview"
		bound.SendState = sendAccepted
		bound.Messages = []*message{
			{Key: "u2", Kind: msgUser, Text: "Inspect README.md and summarize the project."},
		}
		bound.Works[0].State = workRunning
		bound.Works[0].Prompt = "Inspect README.md and summarize the project."
		bound.Works[0].Tools[0].Finished = false
		bound.Works[0].Tools[0].Output = ""
	case "runtime-progress":
		bound.Status = statusWorking
		bound.Work = "Waiting for model"
		bound.ActiveTaskID = "task-preview"
		bound.SendState = sendAccepted
		bound.Messages = append(bound.Messages, &message{
			Key: "live:task-preview:progress", Kind: msgContact,
			Text: "I found the first heading; checking the rest of the file before summarizing.",
		})
		bound.Works[0].State = workRunning
		bound.Works[0].Prompt = "Inspect README.md and summarize the project."
	case "runtime-empty":
		a.contacts = []*contact{{ID: "saki", Name: "Saki", Role: "Assistant", Status: statusReady}}
		a.selected = 0
	case "runtime-error":
		bound.Status = statusAttention
		bound.StatusDetail = "Task failed: model_error: upstream request failed"
		bound.Works[0].State = workFailed
		bound.Works[0].Detail = bound.StatusDetail
	case "runtime-settings":
		a.openSettings()
	case "runtime-history":
		a.openHistory()
	}
	return a
}

func TestPreviewRuntimeStates(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR to write runtime-backed headless previews")
	}
	if err := loadAvatar(); err != nil {
		t.Fatal(err)
	}
	write := func(scene string, width, height int, dark bool) {
		theme := "light"
		if dark {
			theme = "dark"
		}
		a := runtimePreviewApp(scene)
		a.windowWidth = width
		tr := ui.NewTester(a.view, width, height)
		tr.SetDark(dark)
		tr.Frame()
		writePreviewPNG(t, filepath.Join(dir,
			fmt.Sprintf("headless-%s-%s-%dx%d.png", theme, scene, width, height)), tr.Image())
	}
	for _, dark := range []bool{false, true} {
		for _, size := range []struct {
			width, height int
		}{{1040, 720}, {960, 640}, {1380, 900}} {
			for _, scene := range []string{"runtime-chat", "runtime-working", "runtime-progress", "runtime-empty"} {
				write(scene, size.width, size.height, dark)
			}
		}
		for _, scene := range []string{"runtime-error", "runtime-settings", "runtime-history"} {
			write(scene, 1040, 720, dark)
		}
		// Overlay proof at 900; the native minimum is now 720, so this
		// presentation is reachable in the real window.
		write("runtime-working", 900, 640, dark)
	}
}

func TestToolFailureIsVisibleBeforeDetails(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   string
		raw    string
	}{
		{"exit", `{"output":"normal text","exit_code":1,"timed_out":false}`, "Failed (exit 1)", "exit_code"},
		{"timeout", `{"output":"still working","timed_out":true}`, "Timed out", "timed_out"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ct := &contact{ID: "saki", Name: "Saki", Role: "Assistant", Loaded: true}
			ct.Works = []*workItem{{
				TaskID: "t1", State: workRunning,
				Tools: []*workTool{{CallID: "k1", Name: "bash", Args: `{"command":"false"}`, Output: tc.output, Started: true, Finished: true}},
			}}
			a := &appModel{demo: false, runtimeStatus: runtimeStateReady, scrollTo: -1, workspaceOpen: true, windowWidth: 1040, contacts: []*contact{ct}}
			tr := ui.NewTester(a.view, 1040, 720)
			tr.Frame()
			openConversationWorkPanel(t, tr)
			if !tr.HasText(tc.want) || !tr.HasText("normal text") && !tr.HasText("still working") {
				t.Fatalf("failure summary not visible: %v", tr.Texts())
			}
			if err := tr.Click("Details"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if !tr.HasText(tc.raw) {
				t.Fatalf("raw tool result not visible after Details: %v", tr.Texts())
			}
		})
	}
}

func TestContactRailControlsStayVisibleAtNarrowWidths(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.workspaceOpen = false
	a.detail = ""
	for _, width := range []int{1040, 960} {
		a.windowWidth = width
		tr := ui.NewTester(a.view, width, 720)
		tr.Frame()
		limit := a.railWidth()
		if limit != defaultLeftPaneWidth {
			t.Fatalf("%d rail width = %v", width, limit)
		}
		for _, label := range []string{"Add contact", "Contact scope", "Active"} {
			r, ok := tr.Find(label)
			if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.X+r.W > limit || r.Y < 0 || r.Y+r.H > 720 {
				t.Fatalf("%dx720 contact rail %q out of rail: ok=%v rect=%+v", width, label, ok, r)
			}
		}
	}
}

func TestComposerModelAndTaskContextStayInlineAndReturnFocus(t *testing.T) {
	a := testApp(t)
	ct := a.selectedContact()
	ct.Context.items = []taskContextItem{
		{ID: "p1", Name: "资料一", Text: "第一条"},
		{ID: "p2", Name: "资料二", Text: "第二条"},
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Conversation model"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.aux != "model" || !a.composerInlineAux() || a.mainPageActive() {
		t.Fatalf("conversation model aux=%q panel=%q inline=%v", a.aux, a.composerPanel, a.composerInlineAux())
	}
	if !tr.HasText("This context has no Runtime session yet") {
		t.Fatalf("inline model panel missing actual target state: %v", tr.Texts())
	}
	if err := tr.Click("Close"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.aux != "" || a.composerPanel != "" || !tr.Focused("Conversation model") {
		t.Fatalf("model close = aux=%q panel=%q focused=%v", a.aux, a.composerPanel, tr.Focused("Conversation model"))
	}

	if err := tr.Click("Task context · 2"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.aux != "task-context" || !a.composerInlineAux() || !tr.HasText("Conversation · Saki") || !tr.HasText("资料一") {
		t.Fatalf("inline context = aux=%q panel=%q texts=%v", a.aux, a.composerPanel, tr.Texts())
	}
	if err := tr.Click("Close task context"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.aux != "" || a.composerPanel != "" || !tr.Focused("Task context · 2") {
		t.Fatalf("context close = aux=%q panel=%q focused=%v", a.aux, a.composerPanel, tr.Focused("Task context · 2"))
	}

	// A model open request refused by an active model transaction must not
	// expose a model panel flag while the context panel remains the owner.
	for _, reject := range []struct {
		name  string
		set   func(*appModel)
		clear func(*appModel)
	}{
		{name: "model-busy", set: func(a *appModel) { a.modelOpActive = true }, clear: func(a *appModel) { a.modelOpActive = false }},
		{name: "connection-applying", set: func(a *appModel) { a.connectionApplying = true }, clear: func(a *appModel) { a.connectionApplying = false }},
	} {
		t.Run(reject.name, func(t *testing.T) {
			a := testApp(t)
			ct := a.selectedContact()
			ct.Context.items = []taskContextItem{{ID: "reject-1", Name: "资料", Text: "保留"}}
			tr := ui.NewTester(a.view, 1040, 720)
			tr.Frame()
			if err := tr.Click("Task context · 1"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			reject.set(a)
			if err := tr.Click("Conversation model"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if a.aux != "task-context" || a.composerPanel != composerPanelContext || !a.composerInlineAux() || tr.HasText("Session model") {
				t.Fatalf("rejected model open changed owner: aux=%q panel=%q inline=%v texts=%v", a.aux, a.composerPanel, a.composerInlineAux(), tr.Texts())
			}
			reject.clear(a)
			if err := tr.Click("Conversation model"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if a.aux != "model" || a.composerPanel != composerPanelModel || !a.composerInlineAux() {
				t.Fatalf("model could not open after rejection cleared: aux=%q panel=%q inline=%v", a.aux, a.composerPanel, a.composerInlineAux())
			}
		})
	}

	// Switching contacts while the inline model entry is open must drop the
	// owner-specific panel rather than leaving it attached to the new target.
	if err := tr.Click("Conversation model"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	a.selectContact(1)
	tr.Frame()
	if a.aux != "" || a.composerPanel != "" || a.composerInlineAux() || tr.HasText("Session model") {
		t.Fatalf("contact switch kept stale inline model panel: aux=%q panel=%q texts=%v", a.aux, a.composerPanel, tr.Texts())
	}
	a.selectContact(0)
	tr.Frame()

	// The visible model label is the selected contact's value, not a stale
	// value left by another contact.
	a.contacts[1].SessionID = "session-mio"
	a.contacts[1].SessionModel = "model-mio"
	a.selectContact(1)
	tr.Frame()
	if !tr.HasText("model-mio") || tr.HasText("model-default") {
		t.Fatalf("selected contact model label crossed contacts: %v", tr.Texts())
	}
}

func TestComposerKeepsModelScopeAndPrimaryActionAtNarrowCenter(t *testing.T) {
	a := testApp(t)
	a.windowWidth = 1040
	a.leftPaneWidth = defaultLeftPaneWidth
	a.rightPaneWidth = 358 // leaves the real minimum 480px center beside the work pane
	a.workspaceOpen = true
	ct := a.selectedContact()
	ct.SessionID = "long-model-session"
	ct.SessionModel = "provider/very-long-model-name-that-must-not-push-the-action-out-of-the-composer"
	ct.Draft = "保留这段草稿"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if a.workOverlay() {
		t.Fatal("test setup must keep the three-pane center")
	}
	composer, ok := tr.Find("Composer for Saki")
	if !ok || composer.W <= 0 {
		t.Fatalf("composer bounds missing: %+v ok=%v", composer, ok)
	}
	left, right := composer.X, composer.X+composer.W
	withinComposer := func(label string) ui.Rect {
		t.Helper()
		r, ok := tr.Find(label)
		if !ok || r.W <= 0 || r.H <= 0 || r.X < left || r.X+r.W > right {
			t.Fatalf("%s is outside the composer bounds: rect=%+v ok=%v composer=%+v", label, r, ok, composer)
		}
		return r
	}
	model := withinComposer("Conversation model")
	scope := withinComposer("Conversation")
	if scope.X < model.X || scope.X+scope.W > model.X+model.W {
		t.Fatalf("model scope is outside its chip: scope=%+v model=%+v", scope, model)
	}
	short := a.conversationModelShortName(ct)
	if short == "" || !tr.HasText(short) {
		t.Fatalf("short model name %q missing: %v", short, tr.Texts())
	}
	send := withinComposer("Send")
	if send.X < model.X+model.W {
		t.Fatalf("send action overlaps the model chip: send=%+v model=%+v", send, model)
	}

	// A running conversation replaces Send with the same fixed Stop target.
	a.demo = false
	a.runtimeStatus = runtimeStateReady
	a.runtimeCapabilitiesLoaded = true
	a.rt = &runtimeClient{}
	ct.ActiveTaskID = "conversation-task"
	ct.Status = statusWorking
	tr.Frame()
	composer, ok = tr.Find("Composer for Saki")
	if !ok || composer.W <= 0 {
		t.Fatalf("composer bounds missing after stop switch: %+v ok=%v", composer, ok)
	}
	left, right = composer.X, composer.X+composer.W
	model = withinComposer("Conversation model")
	stop := withinComposer("Stop conversation")
	if stop.X < model.X+model.W {
		t.Fatalf("stop action overlaps the model chip: stop=%+v model=%+v", stop, model)
	}
	if tr.HasText("Send") {
		t.Fatal("running conversation kept the Send action instead of Stop conversation")
	}

	// The same view shows the real send guard reason when the model recovery
	// state blocks a non-empty draft; it is not hidden only in the button tip.
	a = testApp(t)
	a.demo = false
	a.runtimeStatus = runtimeStateReady
	a.runtimeCapabilitiesLoaded = true
	a.rt = &runtimeClient{}
	ct = a.selectedContact()
	ct.SessionID = "recovery-session"
	ct.Loaded = true
	ct.Draft = "保留并提示原因"
	a.modelRecovery = map[string]modelRecoveryState{ct.SessionID: modelRecoveryNeedsReload}
	tr = ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Reload the session before sending") {
		t.Fatalf("send guard reason missing: %v", tr.Texts())
	}
}

// TestPreviewComposerTypography always exercises the composer geometry matrix;
// it writes the review PNGs only when SAKI_PREVIEW_DIR is set.
func TestPreviewComposerTypography(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	type scene struct {
		name, draft string
		context     int
		active      bool
		width       int
		height      int
	}
	scenes := []scene{
		{name: "empty", width: 1040, height: 720},
		{name: "single", draft: "帮我整理这周的发布说明。", context: 2, width: 1040, height: 720},
		{name: "multiline", draft: "第一行：保留项目代号。\n第二行：确认负责人。\n第三行：列出下一步。", context: 2, width: 720, height: 640},
		{name: "long", draft: "把这周的资料整理成一份可以执行的计划：先核对项目代号和负责人，再把风险、依赖和下一步写清楚，最后给出可以直接继续处理的最小行动。", context: 3, width: 720, height: 640},
		{name: "stop", draft: "当前对话正在运行；这个位置应显示停止动作。", context: 1, active: true, width: 720, height: 640},
	}
	for _, sc := range scenes {
		for _, dark := range []bool{false, true} {
			a := testApp(t)
			a.windowWidth = sc.width
			a.workspaceOpen = false
			a.runtimeStatus = runtimeStateReady
			a.runtimeCapabilitiesLoaded = true
			a.runtimeMaxActiveTasks = 2
			ct := a.selectedContact()
			ct.SessionID = "composer-preview-session"
			ct.SessionModel = "deepseek/deepseek-chat"
			ct.Draft = sc.draft
			ct.Context.items = nil
			for i := 0; i < sc.context; i++ {
				ct.Context.items = append(ct.Context.items, taskContextItem{Name: "资料 " + fmt.Sprint(i+1), Text: "预览资料"})
			}
			if sc.active {
				a.demo = false
				a.rt = &runtimeClient{}
				ct.ActiveTaskID = "composer-preview-task"
				ct.Status = statusWorking
			}
			tr := ui.NewTester(a.view, sc.width, sc.height)
			tr.SetDark(dark)
			tr.Frame()
			visible := func(label string) ui.Rect {
				t.Helper()
				r, ok := tr.Find(label)
				if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > float32(sc.width) || r.Y+r.H > float32(sc.height) {
					t.Fatalf("%s: %s bounds=%+v ok=%v", sc.name, label, r, ok)
				}
				return r
			}
			visible("Composer for Saki")
			visible(contextLabelForTest(sc.context))
			visible("Conversation model")
			if sc.active {
				visible("Stop conversation")
			} else {
				visible("Send")
			}
			if !tr.HasText("deepseek/deepseek-chat") {
				t.Fatalf("%s: actual model label missing: %v", sc.name, tr.Texts())
			}
			if sc.active && sc.width == 720 {
				composer := visible("Composer for Saki")
				context := visible(contextLabelForTest(sc.context))
				model := visible("Conversation model")
				stop := visible("Stop conversation")
				if !tr.HasText("Stop conversation") {
					t.Fatalf("%s: Stop text was hidden: %v", sc.name, tr.Texts())
				}
				if context.X+context.W > model.X || model.X+model.W > stop.X ||
					stop.X+stop.W > composer.X+composer.W ||
					context.X < composer.X || model.X < composer.X || stop.X < composer.X {
					t.Fatalf("%s: composer controls overlap/overflow composer=%+v context=%+v model=%+v stop=%+v",
						sc.name, composer, context, model, stop)
				}
				scope, ok := tr.Find("Conversation")
				if !ok || scope.X < model.X || scope.X+scope.W > model.X+model.W {
					t.Fatalf("%s: model scope outside model button: scope=%+v ok=%v model=%+v", sc.name, scope, ok, model)
				}
				actual, ok := tr.Find(a.conversationModelShortName(ct))
				if !ok || actual.X < model.X || actual.X+actual.W > model.X+model.W {
					t.Fatalf("%s: model text outside model button: actual=%+v ok=%v model=%+v", sc.name, actual, ok, model)
				}
			}
			if dir != "" {
				theme := "light"
				if dark {
					theme = "dark"
				}
				writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("headless-composer-%s-%dx%d-%s.png", sc.name, sc.width, sc.height, theme)), tr.Image())
			}
		}
	}
}

func contextLabelForTest(count int) string {
	if count > 0 {
		return fmt.Sprintf("Task context · %d", count)
	}
	return "Task context"
}
