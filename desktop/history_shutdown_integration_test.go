package main

import (
	"testing"
	"time"
)

func TestHistoryReloadCallbackFinishesBeforeShutdownQuit(t *testing.T) {
	binary := requireRuntimeBinary(t)
	h := startGUIHarness(t, binary, &providerFixture{}, "history-shutdown-reload")
	var sendErr string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		ct.Draft = "history-shutdown-reload"
		if !h.app.sendCurrent() {
			sendErr = ct.SendError
		}
	})
	if sendErr != "" {
		t.Fatal(sendErr)
	}
	h.pump.wait(func() bool {
		ct := h.app.selectedContact()
		return ct != nil && ct.SendState == sendIdle && ct.ActiveTaskID == "" && len(ct.Messages) > 0 &&
			ct.Loaded && h.app.contactHistoryInFlight == 0
	})
	callbacks := make(chan func(), 8)
	h.app.setUIHook(func(fn func()) { callbacks <- fn })
	quit := false
	h.app.requestQuit = func() { quit = true }
	var contactID string
	h.pump.do(func() {
		ct := h.app.selectedContact()
		contactID = ct.ID
		ct.HistoryDirty = true
		h.app.reloadContactByID(contactID, false)
	})
	var historyCallback func()
	select {
	case historyCallback = <-callbacks:
	case <-time.After(10 * time.Second):
		t.Fatal("history reload callback was not delivered")
	}
	var inFlight int
	h.pump.do(func() { inFlight = h.app.contactHistoryInFlight })
	if inFlight != 1 {
		t.Fatalf("history reload was not accounted before callback: inFlight=%d", inFlight)
	}
	h.app.beginShutdown()
	var drainStarted bool
	h.pump.do(func() { drainStarted = h.app.shutdownDrainStarted })
	if drainStarted || h.app.quitReady {
		t.Fatal("shutdown started before the held history callback")
	}
	historyCallback()
	var dirty bool
	var remaining int
	h.pump.do(func() {
		dirty = h.app.contactByID(contactID).HistoryDirty
		remaining = h.app.contactHistoryInFlight
	})
	if dirty || remaining != 0 {
		t.Fatalf("history callback did not apply/finish: dirty=%v remaining=%d", dirty, remaining)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var quitReady bool
		h.pump.do(func() { quitReady = h.app.quitReady })
		if quitReady && quit {
			break
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("shutdown did not request quit after history: quitReady=%v quit=%v", quitReady, quit)
		}
		select {
		case callback := <-callbacks:
			callback()
		case <-time.After(remaining):
			t.Fatalf("shutdown callback was not delivered after history: quitReady=%v quit=%v", quitReady, quit)
		}
	}
	h.closed = true
	h.pump.close()
}
