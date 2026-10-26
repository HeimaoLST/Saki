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
	quit := false
	var contactID string
	var historyGenAfterReload int
	h.pump.do(func() {
		ct := h.app.selectedContact()
		contactID = ct.ID
		h.app.setUIHook(func(fn func()) { callbacks <- fn })
		h.app.requestQuit = func() { quit = true }
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
	h.pump.do(func() {
		inFlight = h.app.contactHistoryInFlight
		historyGenAfterReload = h.app.contactByID(contactID).historyGen
	})
	if inFlight != 1 {
		t.Fatalf("history reload was not accounted before callback: inFlight=%d", inFlight)
	}
	h.pump.do(func() { h.app.beginShutdown() })
	var drainStarted, quitReadyBefore bool
	h.pump.do(func() {
		drainStarted, quitReadyBefore = h.app.shutdownDrainStarted, h.app.quitReady
	})
	if drainStarted || quitReadyBefore {
		t.Fatal("shutdown started before the held history callback")
	}
	h.pump.do(func() { historyCallback() })
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
		h.pump.do(func() {
			quitReady, _ = h.app.quitReady, quit
		})
		if quitReady && quit {
			break
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("shutdown did not request quit after history: quitReady=%v quit=%v", quitReady, quit)
		}
		select {
		case callback := <-callbacks:
			h.pump.do(func() { callback() })
		case <-time.After(remaining):
			t.Fatalf("shutdown callback was not delivered after history: quitReady=%v quit=%v", quitReady, quit)
		}
	}
	// The existing shutdown guard must prevent a terminal-triggered reload from
	// starting another history callback after quit is requested.
	var terminalInFlight, terminalGen int
	h.pump.do(func() {
		h.app.reloadContactByID(contactID, false)
		terminalInFlight = h.app.contactHistoryInFlight
		terminalGen = h.app.contactByID(contactID).historyGen
	})
	if terminalInFlight != 0 || terminalGen != historyGenAfterReload {
		t.Fatalf("shutdown started a new history reload: inFlight=%d gen=%d/%d", terminalInFlight, terminalGen, historyGenAfterReload)
	}
	select {
	case callback := <-callbacks:
		h.pump.do(func() { callback() })
		t.Fatal("shutdown produced a new history callback")
	default:
	}
	h.closed = true
	h.pump.close()
}
