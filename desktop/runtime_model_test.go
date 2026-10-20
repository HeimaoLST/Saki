package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRuntimeModel(t *testing.T) (*appModel, *contact, *contact) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "contacts.json")
	store, err := openContactStore(path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	bound := &contact{
		ID: "bound", Name: "Saki", Role: "Assistant",
		Purpose: "old purpose", Instructions: "old instructions", Workspace: "/old",
		SessionID: "s1", Loaded: true, Draft: "keep me", Status: statusReady,
		Messages: []*message{{ID: 1, Key: "hist:s1:0:0", Kind: msgUser, Text: "old message"}},
		History:  []historyEntry{{ID: "hist:s1:0:0", Text: "old message"}},
	}
	unbound := &contact{ID: "new", Name: "New contact 1", Role: "Assistant", Workspace: "/tmp", Status: statusReady}
	a := &appModel{
		store: store, runtimeStatus: runtimeStateStarting, scrollTo: -1,
		contacts: []*contact{bound, unbound}, selected: 0,
	}
	if err := store.save(a.contacts, a.selectedID()); err != nil {
		t.Fatal(err)
	}
	return a, bound, unbound
}

func TestSettingsBoundOnlyRenamesAndKeepsIdentity(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	a.openSettings()
	a.settingsDraft.Name = "Renamed"
	a.settingsDraft.Purpose = "new purpose"
	a.settingsDraft.Instructions = "new instructions"
	a.settingsDraft.Workspace = "/new"
	if !a.saveSettings() {
		t.Fatal("save should succeed")
	}
	if bound.Name != "Renamed" {
		t.Fatalf("name = %q", bound.Name)
	}
	if bound.Purpose != "old purpose" || bound.Instructions != "old instructions" || bound.Workspace != "/old" {
		t.Fatalf("bound profile changed: %+v", bound)
	}
	if bound.SessionID != "s1" || bound.Draft != "keep me" || len(bound.Messages) != 1 || len(bound.History) != 1 {
		t.Fatalf("identity/history/draft changed: %+v", bound)
	}
	reloaded, err := openContactStore(a.store.path, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	stored := reloaded.state.Contacts[0]
	if stored.Name != "Renamed" || stored.Purpose != "old purpose" || stored.SessionID != "s1" {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestSettingsUnboundProfileIsUsedBeforeFirstSession(t *testing.T) {
	a, _, unbound := testRuntimeModel(t)
	a.selected = 1
	a.openSettings()
	a.settingsDraft.Purpose = "p"
	a.settingsDraft.Instructions = "i"
	a.settingsDraft.Workspace = "/work"
	if !a.saveSettings() {
		t.Fatal("save should succeed")
	}
	if unbound.Purpose != "p" || unbound.Instructions != "i" || unbound.Workspace != "/work" {
		t.Fatalf("profile = %+v", unbound)
	}
	if unbound.SessionID != "" {
		t.Fatal("saving settings must not create a session")
	}
	if got := profileSystemPrompt(unbound); !strings.Contains(got, "Purpose: p") || !strings.Contains(got, "Instructions: i") {
		t.Fatalf("system prompt = %q", got)
	}
}

func TestSettingsDuringFirstCreateKeepsSubmittedSnapshot(t *testing.T) {
	a, _, unbound := testRuntimeModel(t)
	a.selected = 1
	unbound.SendState = sendSubmitting
	unbound.Purpose = "snapshot"
	a.openSettings()
	a.settingsDraft.Name = "Still Renamable"
	a.settingsDraft.Purpose = "later edit"
	if !a.saveSettings() {
		t.Fatal("save should succeed")
	}
	if unbound.Name != "Still Renamable" || unbound.Purpose != "snapshot" {
		t.Fatalf("contact = %+v", unbound)
	}
}

func TestFailSubmitKeepsDraftForUnconfirmedProtocolError(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	bound.Draft = "keep"
	bound.SendState = sendSubmitting
	a.failSubmit(bound.ID, &runtimeError{Code: "protocol_error", Message: "bad wire"}, true)
	if bound.SendState != sendUnknown || bound.Draft != "keep" {
		t.Fatalf("state=%v draft=%q", bound.SendState, bound.Draft)
	}
	bound.SendState = sendSubmitting
	a.failSubmit(bound.ID, &runtimeError{Code: "busy", Message: "slot occupied"}, true)
	if bound.SendState != sendIdle || !strings.Contains(bound.SendError, "busy") {
		t.Fatalf("state=%v error=%q", bound.SendState, bound.SendError)
	}
}

func TestAcceptTaskKeepsLaterDraftEdits(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	bound.Draft = "edited later"
	bound.SubmittedDraft = "sent"
	bound.SubmittedRev = 1
	bound.draftRev = 2
	bound.SendState = sendSubmitting
	bound.pendingEvents = []runtimeEventEnvelope{{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "task_started"},
	}}
	a.acceptTask(bound, "sent", runtimeSendResult{TaskID: "t1", MessageID: "m1"})
	if bound.Draft != "edited later" {
		t.Fatalf("late acceptance cleared the later draft: %q", bound.Draft)
	}
	if bound.ActiveTaskID != "t1" || bound.SendState != sendAccepted || bound.LastTaskID != "t1" {
		t.Fatalf("task state = %+v", bound)
	}
	if !strings.HasPrefix(bound.Messages[len(bound.Messages)-1].Key, "live:t1:user") {
		t.Fatalf("user message not appended: %+v", bound.Messages[len(bound.Messages)-1])
	}
}

func TestStaleReloadCannotClobberNewSubmit(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	bound.historyGen = 5
	bound.SendState = sendSubmitting
	before := len(bound.Messages)
	a.applyReload(bound.ID, "s1", 4, runtimeSession{SessionID: "s1"}, nil, "completed", nil)
	if len(bound.Messages) != before || bound.SendState != sendSubmitting {
		t.Fatal("stale reload changed a newer submit")
	}
	a.applyReload(bound.ID, "s1", 5, runtimeSession{SessionID: "s1"}, nil, "completed", nil)
	if len(bound.Messages) != before || bound.SendState != sendSubmitting {
		t.Fatal("reload applied while a submit was in flight")
	}
}

func TestReloadRecoversUnknownOnlyAfterIdleConfirmation(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	bound.historyGen = 7
	bound.SendState = sendUnknown
	bound.SendError = "Delivery not confirmed"
	active := runtimeSession{SessionID: "s1", ActiveTask: &runtimeActiveTask{TaskID: "t9", MessageID: "m9"}}
	a.applyReload(bound.ID, "s1", 7, active, nil, "running", nil)
	if bound.SendState != sendAccepted || bound.ActiveTaskID != "t9" {
		t.Fatalf("running task not restored: %+v", bound)
	}

	bound.ActiveTaskID = ""
	bound.SendState = sendUnknown
	records := []historyRecord{
		{Offset: 0, Record: journalRecord{Type: "task_started", TaskID: "t9", MessageID: "m9"}},
		{Offset: 1, Record: journalRecord{Type: "messages", Items: []inputItem{{
			Type: "message", Role: "assistant", Content: []byte(`"finished text"`),
		}}}},
		{Offset: 2, Record: journalRecord{Type: "task_finished", TaskID: "t9", Status: "completed"}},
	}
	a.applyReload(bound.ID, "s1", 7, runtimeSession{SessionID: "s1"}, records, "completed", nil)
	if bound.SendState != sendIdle || bound.ActiveTaskID != "" || bound.SendError != "" {
		t.Fatalf("idle reload did not recover: %+v", bound)
	}
	if len(bound.Messages) != 1 || bound.Messages[0].Text != "finished text" {
		t.Fatalf("history not applied: %+v", bound.Messages)
	}
}

func TestHistoryFailureDetailSurvivesReload(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	bound.historyGen = 1
	reason := "upstream request failed"
	records := []historyRecord{
		{Offset: 0, Record: journalRecord{Type: "task_started", TaskID: "t1", MessageID: "m1"}},
		{Offset: 1, Record: journalRecord{Type: "task_finished", TaskID: "t1", Status: "failed", Error: &reason}},
	}
	a.applyHistory(bound, records, "failed")
	if bound.Status != statusAttention || !strings.Contains(bound.StatusDetail, reason) {
		t.Fatalf("status=%v detail=%q", bound.Status, bound.StatusDetail)
	}
	a.applyReload(bound.ID, "s1", 1, runtimeSession{SessionID: "s1"}, records, "failed", nil)
	if !strings.Contains(bound.StatusDetail, reason) {
		t.Fatalf("reload wiped the failure reason: %q", bound.StatusDetail)
	}
}

func TestLateCancelDoesNotRewriteFinishedTask(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	bound.ActiveTaskID = ""
	bound.LastTaskID = "t1"
	a.applyCancelResult(bound.ID, "t1", "requested", nil)
	if bound.SendError != "" {
		t.Fatalf("late cancel wrote %q", bound.SendError)
	}
	bound.ActiveTaskID = "t2"
	a.applyCancelResult(bound.ID, "t1", "requested", nil)
	if bound.SendError != "" {
		t.Fatalf("cancel for another task wrote %q", bound.SendError)
	}
	bound.ActiveTaskID = "t1"
	a.applyCancelResult(bound.ID, "t1", "requested", nil)
	if !strings.Contains(bound.SendError, "Cancelling") {
		t.Fatalf("active cancel = %q", bound.SendError)
	}
}

func TestHistoryUsesStableOffsetKeysAndTaskContext(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	records := []historyRecord{
		{Offset: 0, Record: journalRecord{Type: "task_started", TaskID: "t1", MessageID: "m1"}},
		{Offset: 1, Record: journalRecord{Type: "messages", Items: []inputItem{
			{Type: "message", Role: "system", Content: []byte(`"hidden prompt"`)},
			{Type: "reasoning", Content: []byte(`[{"text":"secret"}]`)},
			{Type: "message", Role: "user", Content: []byte(`"hello"`)},
			{Type: "tool_call", CallID: "k1", Name: "read", Arguments: `{"path":"x"}`},
			{Type: "tool_result", CallID: "k1", Output: `{"content":"marker"}`},
			{Type: "message", Role: "assistant", Content: []byte(`[{"type":"text","text":"done"}]`)},
		}}},
	}
	a.applyHistory(bound, records, "completed")
	if len(bound.Messages) != 4 {
		t.Fatalf("messages = %d: %+v", len(bound.Messages), bound.Messages)
	}
	if bound.Messages[0].Key != "hist:s1:1:2" || bound.Messages[0].TaskID != "t1" || bound.Messages[0].MessageID != "m1" {
		t.Fatalf("first = %+v", bound.Messages[0])
	}
	if bound.Messages[0].Time != "" {
		t.Fatalf("zero timestamp must not fabricate a date: %q", bound.Messages[0].Time)
	}
	if bound.Messages[3].Text != "done" || bound.Messages[1].ToolName != "read" {
		t.Fatalf("converted = %+v", bound.Messages)
	}
}

func TestBeginShutdownIsIdempotentWithoutRuntime(t *testing.T) {
	a, _, _ := testRuntimeModel(t)
	quit := make(chan struct{}, 2)
	a.requestQuit = func() { quit <- struct{}{} }
	a.beginShutdown()
	if !a.shutdownStarted || !a.quitReady {
		t.Fatalf("state started=%v ready=%v", a.shutdownStarted, a.quitReady)
	}
	select {
	case <-quit:
	case <-time.After(time.Second):
		t.Fatal("quit was not requested")
	}
	a.beginShutdown()
	select {
	case <-quit:
		t.Fatal("shutdown ran twice")
	default:
	}
}

func TestReloadSelectedRequiresLiveRuntime(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	a.runtimeStatus = runtimeStateExited
	a.reloadSelected()
	if !strings.Contains(bound.SendError, "restart Saki") {
		t.Fatalf("disconnected reload message = %q", bound.SendError)
	}
}

func TestCanSendRejectsDisconnectedOrDrainingRuntime(t *testing.T) {
	a, bound, _ := testRuntimeModel(t)
	a.rt = &runtimeClient{}
	a.runtimeStatus = runtimeStateDraining
	if reason := a.canSend(bound); reason == "" {
		t.Fatal("draining runtime must not accept a send")
	}
	a.runtimeStatus = runtimeStateReady
	bound.SessionID = "s1"
	bound.Loaded = false
	if reason := a.canSend(bound); !strings.Contains(reason, "loading") {
		t.Fatalf("unloaded session reason = %q", reason)
	}
}
