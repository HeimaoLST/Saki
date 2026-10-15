package main

import (
	"strings"
	"testing"
)

func testApp(t *testing.T) *appModel {
	t.Helper()
	a, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestDraftsAreScopedPerContact(t *testing.T) {
	a := testApp(t)
	a.selectContact(1)
	a.selectedContact().Draft = "draft for mio"
	a.selectContact(0)
	a.selectedContact().Draft = "草稿 for saki"
	if a.contacts[1].Draft != "draft for mio" {
		t.Fatalf("Mio draft changed: %q", a.contacts[1].Draft)
	}
	if a.contacts[0].Draft != "草稿 for saki" {
		t.Fatalf("Saki draft changed: %q", a.contacts[0].Draft)
	}
}

func TestRejectedSendKeepsDraft(t *testing.T) {
	a := testApp(t)
	a.selectContact(2) // Ren is working in the seed
	ct := a.selectedContact()
	ct.Draft = "while working"
	before := len(ct.Messages)
	if a.sendCurrent() {
		t.Fatal("send should be rejected while the contact is working")
	}
	if ct.Draft != "while working" {
		t.Fatalf("draft must be retained, got %q", ct.Draft)
	}
	if len(ct.Messages) != before {
		t.Fatal("no message should be appended for a rejected send")
	}
}

func TestRetryRejectedWhileWorking(t *testing.T) {
	a := testApp(t)
	ren := a.contactByID("ren")
	a.selectContact(2)
	m := &message{ID: 999, Kind: msgContact, Retry: true}
	if a.retry(m) {
		t.Fatal("retry should be rejected while the contact is working")
	}
	if m.RetryDone {
		t.Fatal("a rejected retry must not be marked done")
	}
	a.failRun(ren)
	if !a.retry(m) {
		t.Fatal("retry should be accepted after the run failed")
	}
}

func TestCreateRequiresName(t *testing.T) {
	a := testApp(t)
	a.openAdd()
	if a.createContact() {
		t.Fatal("create should fail without a name")
	}
	if !a.addNameError {
		t.Fatal("missing-name error should be set")
	}
	a.add.Name = "Kai"
	if !a.createContact() {
		t.Fatal("create should succeed with a name")
	}
	if a.selectedContact().Name != "Kai" || len(a.contacts) != 4 {
		t.Fatalf("created contact not selected: selected=%q contacts=%d", a.selectedContact().Name, len(a.contacts))
	}
	if a.selectedContact().Draft != "" {
		t.Fatal("new contact draft should be empty")
	}
}

func TestHistoryIsScopedToCurrentContact(t *testing.T) {
	a := testApp(t)
	a.selectContact(0)
	a.historyQuery = "shared memory"
	if len(a.historyMatches()) == 0 {
		t.Fatal("Saki history should match 'shared memory'")
	}
	a.historyQuery = "empty-input"
	if len(a.historyMatches()) != 0 {
		t.Fatal("Ren's history must not leak into Saki's scope")
	}
}

// TestReplyStaysInOriginatingContact covers the synchronous demo path (no
// window): the reply lands in the contact the send started from, and other
// contacts' messages are untouched. The real timed switch is exercised
// against the running app.
func TestReplyStaysInOriginatingContact(t *testing.T) {
	a := testApp(t)
	saki := a.contactByID("saki")
	mio := a.contactByID("mio")
	sakiCount := len(saki.Messages)
	sakiLastID := saki.Messages[sakiCount-1].ID
	a.selectContact(1)
	mio.Draft = "ping"
	if !a.sendCurrent() {
		t.Fatal("send should be accepted for a ready contact")
	}
	if mio.Draft != "" {
		t.Fatalf("accepted send should clear the draft, got %q", mio.Draft)
	}
	if mio.Messages[len(mio.Messages)-1].Kind != msgContact {
		t.Fatal("reply must land in the originating contact")
	}
	if len(saki.Messages) != sakiCount || saki.Messages[sakiCount-1].ID != sakiLastID {
		t.Fatal("another contact's messages must not change")
	}
	a.selectContact(0)
	if len(a.selectedContact().Messages) != sakiCount {
		t.Fatal("contacts must not share messages")
	}
}

func TestBackgroundWorkMarksUnread(t *testing.T) {
	a := testApp(t)
	ren := a.contactByID("ren")
	a.selectContact(0) // Saki visible, Ren in the background
	before := ren.Unread
	a.startWork(ren, "Re-running the parser tests", 0, func() { ren.Status = statusReady })
	if ren.Unread != before+1 {
		t.Fatalf("background completion should mark unread: %d -> %d", before, ren.Unread)
	}
}

func TestSourceScopeAndReveal(t *testing.T) {
	a := testApp(t)
	a.selectContact(0)
	entries := a.sourceEntries(5)
	if len(entries) != 2 {
		t.Fatalf("want 2 sources, got %d", len(entries))
	}
	for _, e := range entries {
		if e.ID != "saki-h2" && e.ID != "saki-h5" {
			t.Fatalf("unexpected source %s", e.ID)
		}
	}
	a.viewInConversation("saki-h2")
	if a.highlight != "saki-h2" || a.screen != viewChat {
		t.Fatal("reveal should highlight the entry in the conversation")
	}
	found := false
	for _, e := range a.revealedEntries() {
		if e.ID == "saki-h2" {
			found = true
		}
	}
	if !found {
		t.Fatal("revealed entry missing from the transcript set")
	}
	for _, e := range a.revealedEntries() {
		if strings.HasPrefix(e.ID, "ren-") {
			t.Fatal("revealed entries must stay in the current contact scope")
		}
	}
}

func TestSnippetTruncatesByRunes(t *testing.T) {
	c := &contact{Name: "X"}
	c.Messages = []*message{{Kind: msgUser, Text: strings.Repeat("你", 80)}}
	a := &appModel{contacts: []*contact{c}}
	got := a.snippet(c)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("long CJK snippet should be truncated: %q", got)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("snippet broke a rune: %q", got)
	}
}
