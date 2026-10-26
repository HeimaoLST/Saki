package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestOpenSessionModelWithoutSessionDoesNotCreate(t *testing.T) {
	ct := &contact{ID: "contact-a", Name: "Writer", Status: statusReady}
	a := &appModel{
		contacts: []*contact{ct}, selected: 0, runtimeStatus: runtimeStateReady,
		workspaceOpen: true, scrollTo: -1,
	}
	a.openSessionModel("", "conversation", ct.Name)
	if a.aux != "model" || a.modelForm != nil {
		t.Fatal("no-session model entry opened an editable form")
	}
	if !strings.Contains(a.modelPanelMessage, "Connection settings") || !strings.Contains(a.modelPanelMessage, "after the first task") {
		t.Fatalf("no-session message = %q", a.modelPanelMessage)
	}
	if ct.SessionID != "" {
		t.Fatal("opening the model panel created a session")
	}
	tr := ui.NewTester(a.view, 720, 720)
	tr.Frame()
	for _, text := range []string{"Session model", "Connection settings", "Close"} {
		if !tr.HasText(text) {
			t.Fatalf("no-session panel missing %q", text)
		}
	}
}

func TestModelSessionGuardsOnlyBlockTheirTarget(t *testing.T) {
	aContact := &contact{ID: "a", SessionID: "session-a", Loaded: true, Status: statusReady}
	bContact := &contact{ID: "b", SessionID: "session-b", Loaded: true, Status: statusReady}
	aIssue := &productIssue{ID: "issue-a", WorkSessionID: "session-a", Work: issueWorkSession{Loaded: true}}
	bIssue := &productIssue{ID: "issue-b", WorkSessionID: "session-b", Work: issueWorkSession{Loaded: true}}
	aContact.Issues = []*productIssue{aIssue}
	bContact.Issues = []*productIssue{bIssue}
	a := &appModel{
		contacts: []*contact{aContact, bContact}, selected: 0,
		runtimeStatus: runtimeStateReady, runtimeMaxActiveTasks: 2,
		rt: &runtimeClient{},
	}

	a.modelOpActive = true
	a.modelOpSessionID = "session-a"
	if reason := a.canSend(aContact); !strings.Contains(reason, "being updated") {
		t.Fatalf("target send guard = %q", reason)
	}
	if reason := a.canSend(bContact); reason != "" {
		t.Fatalf("unrelated send was blocked: %q", reason)
	}
	if reason := a.canStartIssue(aContact, aIssue); !strings.Contains(reason, "being updated") {
		t.Fatalf("target issue guard = %q", reason)
	}
	if reason := a.canStartIssue(bContact, bIssue); reason != "" {
		t.Fatalf("unrelated issue was blocked: %q", reason)
	}

	a.modelOpActive = false
	a.modelRecovery = map[string]modelRecoveryState{"session-a": modelRecoveryNeedsReload}
	if reason := a.canSend(aContact); !strings.Contains(reason, "Reload") {
		t.Fatalf("quarantined send guard = %q", reason)
	}
	if reason := a.canSend(bContact); reason != "" {
		t.Fatalf("quarantine leaked to another session: %q", reason)
	}
	a.aux = "model"
	a.modelOpActive = true
	a.closeAux()
	if a.aux != "model" {
		t.Fatal("aux close bypassed an in-flight model operation")
	}
}
