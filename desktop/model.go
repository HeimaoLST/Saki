package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type contactStatus int

const (
	statusReady contactStatus = iota
	statusWorking
	statusAttention
)

type messageKind int

const (
	msgUser messageKind = iota
	msgContact
	msgActivity
)

type sendState int

const (
	sendIdle sendState = iota
	sendSubmitting
	sendAccepted
	sendUnknown
)

type historyEntry struct {
	ID   string
	Date string
	From string // "me" or "contact"
	Text string
}

type message struct {
	ID          int
	Kind        messageKind
	Text        string
	Time        string
	Steps       []string
	Sources     []string
	AlertTitle  string
	AlertDetail string
	Retry       bool
	RetryDone   bool
	Expanded    bool

	// Runtime-backed display identity. Key is stable for a journal record
	// item (offset+index) or a live (task,item/call) tuple.
	Key        string
	TaskID     string
	MessageID  string
	ItemID     string
	CallID     string
	ToolName   string
	ToolArgs   string
	ToolOutput string
	History    bool
	Streaming  bool
}

type contact struct {
	ID           string
	Name         string
	Role         string
	Purpose      string
	Instructions string
	Workspace    string
	Status       contactStatus
	Unread       int
	Work         string
	WorkLabel    string
	Messages     []*message
	History      []historyEntry
	Revealed     []string
	Draft        string
	replyIndex   int
	list         ui.ListState

	// Runtime-backed state. Empty SessionID means the contact has no runtime
	// session yet; it is created lazily before the first task.send.
	SessionID         string
	Loaded            bool
	LoadErr           string
	Unavailable       string
	ActiveTaskID      string
	LastTaskID        string
	SendState         sendState
	SendError         string
	SubmittedDraft    string
	SubmittedRev      int
	draftRev          int
	HistoryDirty      bool
	StatusDetail      string
	SessionWorkingDir string
	SessionModel      string
	SessionAPI        string
	SessionState      string
	historyGen        int
	pendingEvents     []runtimeEventEnvelope
}

type viewMode int

const (
	viewChat viewMode = iota
	viewHistory
)

type profileDraft struct {
	Name         string
	Purpose      string
	Instructions string
	Workspace    string
}

type appModel struct {
	contacts []*contact
	selected int
	screen   viewMode
	aux      string // "", "sources", "settings"

	auxMessage      int
	defaultContactN int
	focusComposer   bool
	settingsDraft   profileDraft
	settingsError   bool

	// Runtime mode. demo keeps the local fixture path for tests/screenshots;
	// the shipped app is runtime-backed.
	demo             bool
	store            *contactStore
	rt               *runtimeClient
	runtimeStatus    string
	runtimeErr       string
	runtimeStage     string
	runtimeHint      string
	runtimeBinary    string
	runtimeAPI       string
	runtimeHandshake bool
	workDir          string
	sessionDir       string
	storePath        string
	uiHook           func(func()) // test seam for deterministic UI-thread ordering
	requestQuit      func()       // set by main to mygo.App.Quit
	quitReady        bool
	shutdownStarted  bool
	historyQuery     string
	highlight        string

	seq int
	win *mygo.Window
	// scrollTo is a one-shot list scroll request consumed by the next frame:
	// -1 means no pending request.
	scrollTo int
}

var cannedReplies = map[string][]string{
	"saki": {
		"Noted — I kept the earlier MyGo scope in view while answering, and nothing earlier was rewritten.",
		"That fits the current scope. I will treat it as the next check-in point rather than a new decision.",
	},
	"mio": {
		"Got it. I will fold that into the draft and leave the earlier version untouched.",
		"Understood — I will adjust that section and keep the rest of the note as it is.",
	},
	"ren": {
		"Done. Focused tests pass: 64 passed, 0 failed. I will wait for the next change before touching the parser again.",
		"Understood. I will keep the last passing state until you ask for another run.",
	},
}

func seededApp() *appModel {
	return &appModel{
		contacts: []*contact{
			{
				ID: "saki", Name: "Saki", Role: "Research partner",
				Purpose:      "Long-running product and research questions, with earlier decisions kept in view.",
				Instructions: "Cite earlier decisions when they shape an answer. Keep replies short and concrete.",
				WorkLabel:    "Looking through this thread",
				Status:       statusReady,
				Messages: []*message{
					{ID: 1, Kind: msgUser, Text: "Where did we land on the MyGo decision before the desktop work starts?", Time: "9:42"},
					{ID: 2, Kind: msgActivity, Text: "Looked up earlier conversations", Time: "9:43", Sources: []string{"saki-h1", "saki-h2"},
						Steps: []string{
							"Searched this contact's history for “MyGo”",
							"Read 3 earlier messages from Sep 27 and Oct 3",
							"Cited 2 messages in the reply",
						}},
					{ID: 3, Kind: msgContact, Time: "9:43",
						Text:    "We agreed to try MyGo for the desktop shell while keeping Saki in Rust. Each contact keeps its own profile and history.",
						Sources: []string{"saki-h1", "saki-h2"}},
					{ID: 4, Kind: msgUser, Text: "Good. Then the desktop shell should show exactly that: contacts, drafts, and cited history.", Time: "9:44"},
					{ID: 5, Kind: msgContact, Time: "9:44",
						Text:    "Yes — and profile edits should only affect later replies, never rewrite old ones. I kept the earlier decision in view.",
						Sources: []string{"saki-h2", "saki-h5"}},
				},
				History: []historyEntry{
					{ID: "saki-h1", Date: "Sep 27", From: "me", Text: "Let's settle the first scope for the MyGo desktop shell."},
					{ID: "saki-h2", Date: "Sep 27", From: "contact", Text: "Keep Saki as the Rust agent core and let MyGo be the desktop shell. Each contact keeps its own profile and history."},
					{ID: "saki-h3", Date: "Oct 3", From: "me", Text: "So no shared memory between contacts yet."},
					{ID: "saki-h4", Date: "Oct 3", From: "contact", Text: "Right — scoped history only. Revisit collaboration after the single-contact loop is useful."},
					{ID: "saki-h5", Date: "Oct 5", From: "contact", Text: "Profile edits affect future replies; saved history stays intact."},
				},
			},
			{
				ID: "mio", Name: "Mio", Role: "Writing partner",
				Purpose:      "Tightening launch notes while keeping the voice consistent.",
				Instructions: "Prefer shorter sentences and concrete claims. Flag open questions at the end.",
				WorkLabel:    "Tightening the draft",
				Status:       statusReady,
				Unread:       1,
				Messages: []*message{
					{ID: 1, Kind: msgUser, Text: "Can you tighten the opening of the launch note? It still feels slow.", Time: "Yesterday"},
					{ID: 2, Kind: msgContact, Text: "Done. I cut the first paragraph in half and kept the two-sentence rhythm you liked. One open question: does the title still need the word “notes”?", Time: "Yesterday"},
					{ID: 3, Kind: msgUser, Text: "Keep the word for now. I will read the rest tonight.", Time: "Yesterday"},
					{ID: 4, Kind: msgContact, Text: "Sounds good. The draft stays where it is until you say otherwise.", Time: "8:15"},
				},
				History: []historyEntry{
					{ID: "mio-h1", Date: "Sep 30", From: "contact", Text: "I kept the closing short so the ask stands out."},
					{ID: "mio-h2", Date: "Oct 10", From: "me", Text: "The intro should promise less and show more."},
					{ID: "mio-h3", Date: "Oct 10", From: "contact", Text: "Agreed — I will lead with the concrete example."},
				},
			},
			{
				ID: "ren", Name: "Ren", Role: "Coding partner",
				Purpose:      "Small parser and runtime changes, reported as test results rather than guesses.",
				Instructions: "Report failing cases with the diff context. Do not claim a fix until the tests pass.",
				Workspace:    "~/Workspace/saki",
				WorkLabel:    "Running the parser tests",
				Status:       statusWorking,
				Work:         "Running the parser tests",
				Messages: []*message{
					{ID: 1, Kind: msgUser, Text: "Run the parser tests again after the trailing-newline change.", Time: "10:02"},
					{ID: 2, Kind: msgContact, Text: "Starting now. I will come back with the failing cases and the diff context.", Time: "10:02"},
				},
				History: []historyEntry{
					{ID: "ren-h1", Date: "Oct 8", From: "me", Text: "The empty-input case still crashes."},
					{ID: "ren-h2", Date: "Oct 8", From: "contact", Text: "Fixed — the parser now returns an empty list instead of unwrapping."},
					{ID: "ren-h3", Date: "Oct 11", From: "contact", Text: "I added a regression test for the roundtrip case."},
				},
			},
		},
		selected: 0,
		screen:   viewChat,
		seq:      100,
		scrollTo: -1,
	}
}

func (a *appModel) selectedContact() *contact { return a.contacts[a.selected] }

func (a *appModel) startSeeded() {
	ren := a.contactByID("ren")
	if ren == nil || a.win == nil {
		return
	}
	a.startWork(ren, "Running the parser tests", 12*time.Second, func() { a.failRun(ren) })
}

func (a *appModel) contactByID(id string) *contact {
	for _, c := range a.contacts {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (a *appModel) selectContact(i int) {
	if i < 0 || i >= len(a.contacts) || i == a.selected {
		return
	}
	if !a.demo {
		a.persist()
	}
	a.contacts[i].Unread = 0
	a.selected = i
	a.screen = viewChat
	a.aux = ""
	a.highlight = ""
	if !a.demo {
		ct := a.contacts[i]
		if ct.SessionID != "" && (!ct.Loaded || ct.HistoryDirty) && ct.ActiveTaskID == "" {
			a.reloadContactByID(ct.ID)
		}
	}
}

func (a *appModel) snippet(c *contact) string {
	if c.Status == statusWorking && c.Work != "" {
		return c.Work
	}
	for i := len(c.Messages) - 1; i >= 0; i-- {
		m := c.Messages[i]
		if m.Kind == msgActivity {
			continue
		}
		text := strings.Join(strings.Fields(m.Text), " ")
		if text == "" {
			continue
		}
		prefix := ""
		if m.Kind == msgUser {
			prefix = "You: "
		}
		runes := []rune(text)
		if len(runes) > 36 {
			text = strings.TrimSpace(string(runes[:35])) + "…"
		}
		return prefix + text
	}
	return "No messages yet"
}

func (a *appModel) statusLabel(c *contact) string {
	switch c.Status {
	case statusWorking:
		return "Working"
	case statusAttention:
		return "Needs attention"
	default:
		return "Ready"
	}
}

// addDefaultContact creates and selects a ready contact with a usable
// default name, then asks the composer to take focus once.
func (a *appModel) addDefaultContact() {
	if !a.demo {
		a.addRuntimeContact()
		return
	}
	a.seq++
	a.defaultContactN++
	c := &contact{
		ID:        fmt.Sprintf("contact-%d", a.seq),
		Name:      fmt.Sprintf("New contact %d", a.defaultContactN),
		Role:      "Assistant",
		Status:    statusReady,
		WorkLabel: "Preparing a reply",
	}
	a.contacts = append(a.contacts, c)
	a.selected = len(a.contacts) - 1
	a.screen = viewChat
	a.aux = ""
	a.focusComposer = true
}

func (a *appModel) openSettings() {
	c := a.selectedContact()
	a.settingsDraft = profileDraft{Name: c.Name, Purpose: c.Purpose, Instructions: c.Instructions, Workspace: c.Workspace}
	a.settingsError = false
	a.aux = "settings"
}

func (a *appModel) saveSettings() bool {
	name := strings.TrimSpace(a.settingsDraft.Name)
	if name == "" {
		a.settingsError = true
		return false
	}
	c := a.selectedContact()
	c.Name = name
	// Once a runtime session exists (or its first create is in flight), the
	// profile was already used for session.create and cannot be hot-applied.
	// Keep the stored values unchanged and only update the product name.
	if !a.profileLocked(c) {
		c.Purpose = strings.TrimSpace(a.settingsDraft.Purpose)
		c.Instructions = strings.TrimSpace(a.settingsDraft.Instructions)
		c.Workspace = strings.TrimSpace(a.settingsDraft.Workspace)
	}
	a.aux = ""
	a.settingsError = false
	a.persist()
	return true
}

func (a *appModel) closeAux() { a.aux = "" }

func (a *appModel) openSources(messageID int) {
	a.aux = "sources"
	a.auxMessage = messageID
}

func (a *appModel) openHistory() {
	a.screen = viewHistory
	a.aux = ""
	a.historyQuery = ""
}

func (a *appModel) backToChat() {
	a.screen = viewChat
	a.highlight = ""
}

func (a *appModel) historyMatches() []historyEntry {
	c := a.selectedContact()
	query := strings.ToLower(strings.TrimSpace(a.historyQuery))
	out := make([]historyEntry, 0, len(c.History))
	for i := len(c.History) - 1; i >= 0; i-- {
		e := c.History[i]
		if query == "" {
			out = append(out, e)
			continue
		}
		speaker := c.Name
		if e.From == "me" {
			speaker = "You"
		}
		haystack := strings.ToLower(e.Text + " " + e.Date + " " + speaker)
		if strings.Contains(haystack, query) {
			out = append(out, e)
		}
	}
	return out
}

func (a *appModel) viewInConversation(entryID string) {
	c := a.selectedContact()
	// Real history entries are already in the transcript under stable
	// offset+item keys; do not duplicate them through the demo reveal list.
	for i, m := range c.Messages {
		if m.Key != entryID {
			continue
		}
		a.highlight = entryID
		a.screen = viewChat
		a.scrollTo = len(a.revealedEntries()) + i
		return
	}
	found := false
	for _, id := range c.Revealed {
		if id == entryID {
			found = true
			break
		}
	}
	if !found {
		c.Revealed = append(c.Revealed, entryID)
	}
	a.highlight = entryID
	a.screen = viewChat
	a.scrollTo = a.revealIndex()
}

// revealIndex is the row of the highlighted entry among the revealed
// entries that open the chat list; -1 when there is none.
func (a *appModel) revealIndex() int {
	entries := a.revealedEntries()
	for i, e := range entries {
		if e.ID == a.highlight {
			return i
		}
	}
	return -1
}

func (a *appModel) revealedEntries() []historyEntry {
	c := a.selectedContact()
	out := make([]historyEntry, 0, len(c.Revealed))
	for _, e := range c.History {
		for _, id := range c.Revealed {
			if e.ID == id {
				out = append(out, e)
			}
		}
	}
	return out
}

func (a *appModel) sourceEntries(messageID int) []historyEntry {
	c := a.selectedContact()
	for _, m := range c.Messages {
		if m.ID != messageID {
			continue
		}
		out := make([]historyEntry, 0, len(m.Sources))
		for _, id := range m.Sources {
			for _, e := range c.History {
				if e.ID == id {
					out = append(out, e)
				}
			}
		}
		return out
	}
	return nil
}

func (a *appModel) send(ct *contact, text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if !a.demo {
		return a.sendRuntime(ct, text)
	}
	if ct.Status == statusWorking {
		return false
	}
	a.seq++
	ct.Messages = append(ct.Messages, &message{ID: a.seq, Kind: msgUser, Text: text, Time: nowTime()})
	label := ct.WorkLabel
	if label == "" {
		label = "Preparing a reply"
	}
	a.startWork(ct, label, 1500*time.Millisecond, func() {
		ct.Status = statusReady
		a.seq++
		ct.Messages = append(ct.Messages, &message{ID: a.seq, Kind: msgContact, Text: a.canned(ct), Time: nowTime()})
	})
	return true
}

func (a *appModel) sendCurrent() bool {
	ct := a.selectedContact()
	text := ct.Draft
	if !a.send(ct, text) {
		return false
	}
	if a.demo {
		ct.Draft = ""
	}
	return true
}

func (a *appModel) canned(ct *contact) string {
	replies := cannedReplies[ct.ID]
	if len(replies) == 0 {
		return "Noted. I will keep this thread in view and follow up here."
	}
	reply := replies[ct.replyIndex%len(replies)]
	ct.replyIndex++
	return reply
}

func (a *appModel) startWork(ct *contact, label string, d time.Duration, finish func()) {
	ct.Status = statusWorking
	ct.Work = label
	if a.win == nil {
		ct.Work = ""
		finish()
		if a.contacts[a.selected] != ct {
			ct.Unread++
		}
		return
	}
	win := a.win
	time.AfterFunc(d, func() {
		win.Update(func() {
			ct.Work = ""
			finish()
			if a.contacts[a.selected] != ct {
				ct.Unread++
			}
		})
	})
}

func (a *appModel) failRun(ct *contact) {
	ct.Status = statusAttention
	a.seq++
	ct.Messages = append(ct.Messages, &message{
		ID: a.seq, Kind: msgContact, Time: nowTime(),
		Text:        "Two failures in parser_roundtrip: expected eof, got newline. The trailing-newline change is the likely cause.",
		AlertTitle:  "Run failed · 2 failing tests",
		AlertDetail: "cargo test — parser_roundtrip",
		Retry:       true,
	})
}

func (a *appModel) retry(m *message) bool {
	ct := a.selectedContact()
	if !m.Retry || m.RetryDone || ct.Status == statusWorking {
		return false
	}
	m.RetryDone = true
	a.startWork(ct, "Re-running the parser tests", 2*time.Second, func() {
		ct.Status = statusReady
		a.seq++
		ct.Messages = append(ct.Messages, &message{ID: a.seq, Kind: msgContact, Text: "Fixed the trailing-newline branch. Parser tests pass now: 64 passed, 0 failed.", Time: nowTime()})
	})
	return true
}

func nowTime() string {
	return time.Now().Format("15:04")
}
