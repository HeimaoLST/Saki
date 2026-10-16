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
// sidebar, chat header, composer, and both panels.
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

// TestAddContactFocusesComposerAndSends clicks Add contact, then types and
// presses Enter without any extra click: the message must belong to the new
// default contact and existing contacts must not change.
func TestAddContactFocusesComposerAndSends(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	before := make([]int, len(a.contacts))
	for i := range a.contacts {
		before[i] = len(a.contacts[i].Messages)
	}
	if err := tr.Click("Add contact"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	created := a.selectedContact()
	if created.Name != "New contact 1" || created.Role != "Assistant" {
		t.Fatalf("created contact = %q role=%q", created.Name, created.Role)
	}
	tr.Type("hello default")
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	if len(created.Messages) != 2 {
		t.Fatalf("new contact messages = %d, want user + demo reply", len(created.Messages))
	}
	if got := created.Messages[0]; got.Kind != msgUser || got.Text != "hello default" {
		t.Fatalf("first message = %+v", got)
	}
	for i := range before {
		if len(a.contacts[i].Messages) != before[i] {
			t.Fatalf("existing contact %d changed: %d -> %d", i, before[i], len(a.contacts[i].Messages))
		}
	}
}

// TestSettingsDoesNotStealComposerFocus verifies the one-shot focus is
// consumed once, later focus changes stick, and Settings keeps the draft.
func TestSettingsDoesNotStealComposerFocus(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetFocused(true)
	tr.Frame()
	if err := tr.Click("Add contact"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	tr.Type("keep me")
	tr.Frame()
	if a.selectedContact().Draft != "keep me" {
		t.Fatalf("draft = %q", a.selectedContact().Draft)
	}
	if a.focusComposer {
		t.Fatal("the one-shot focus request should be consumed")
	}
	tr.ClickAt(400, 200) // leave the composer before clicking a button
	tr.Frame()
	if err := tr.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	tr.Frame()
	tr.Type("zz")
	tr.Frame()
	if a.selectedContact().Draft != "keep me" {
		t.Fatalf("composer focus was stolen after Settings: draft = %q", a.selectedContact().Draft)
	}
	a.settingsDraft.Name = "Renamed"
	if !a.saveSettings() {
		t.Fatal("settings save should succeed with a name")
	}
	if a.selectedContact().Name != "Renamed" {
		t.Fatalf("name = %q", a.selectedContact().Name)
	}
	if a.selectedContact().Draft != "keep me" || len(a.selectedContact().Messages) != 0 {
		t.Fatal("settings save must not clear the draft or history")
	}
	a.openSettings()
	a.settingsDraft.Name = "  "
	if a.saveSettings() || !a.settingsError {
		t.Fatal("empty settings name must be rejected")
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
