package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestOpenSessionModelWithoutSessionDoesNotCreate(t *testing.T) {
	ct := &contact{ID: "contact-a", Name: "Writer", Status: statusReady}
	a := &appModel{
		contacts: []*contact{ct}, selected: 0, runtimeStatus: runtimeStateReady,
		workspaceOpen: true, scrollTo: -1,
	}
	a.openComposerModel()
	if !a.composerModelOpen || a.modelForm != nil || a.mainPageActive() {
		t.Fatal("no-session model entry opened an editable form")
	}
	if a.modelPanelMessage != modelPanelMessageNoSession {
		t.Fatalf("no-session message kind = %q", a.modelPanelMessage)
	}
	if ct.SessionID != "" {
		t.Fatal("opening the model panel created a session")
	}
	tr := ui.NewTester(a.view, 720, 720)
	tr.Frame()
	for _, text := range []string{"Chat model", "Connection settings", "Manage models"} {
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
	if reason := a.canSend(aContact); reason != blockModelUpdating {
		t.Fatalf("target send guard = %q", reason)
	}
	if reason := a.canSend(bContact); reason != "" {
		t.Fatalf("unrelated send was blocked: %q", reason)
	}
	if reason := a.canStartIssue(aContact, aIssue); reason != blockModelUpdating {
		t.Fatalf("target issue guard = %q", reason)
	}
	if reason := a.canStartIssue(bContact, bIssue); reason != "" {
		t.Fatalf("unrelated issue was blocked: %q", reason)
	}

	a.modelOpActive = false
	a.modelRecovery = map[string]modelRecoveryState{"session-a": modelRecoveryNeedsReload}
	if reason := a.canSend(aContact); reason != blockModelReload {
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

func TestModelRecoveryGateUsesTypedUnconfirmedCopy(t *testing.T) {
	a, _ := workspaceTestApp(t)
	form := newModelSelection(selectionTarget(), selectionCatalog())
	a.modelForm = form
	a.rt = &runtimeClient{}
	a.runtimeStatus = runtimeStateReady
	sessionID := form.Target.Config.SessionID
	a.modelRecovery = map[string]modelRecoveryState{sessionID: modelRecoveryNeedsReload}
	a.updateSessionModel(modelSelectionRequest{Target: form.Target})
	if !form.Unconfirmed {
		t.Fatal("recovery gate did not quarantine the update")
	}
	tr := modelSelectionTester(form, modelSelectionActions{}, 560, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("This session needs an explicit Reload before another model update.") || tr.HasText("need-reload-update") {
		t.Fatalf("English recovery gate copy missing: %v", tr.Texts())
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("此会话需要先显式重新加载，才能再次更新模型。") || tr.HasText("need-reload-update") {
		t.Fatalf("Chinese recovery gate copy missing: %v", tr.Texts())
	}
	form.Unconfirmed = false
	a.modelRecovery[sessionID] = modelRecoveryNeedsRefresh
	a.updateSessionModel(modelSelectionRequest{Target: form.Target})
	tr.SetLocale("en")
	tr.Frame()
	if !form.Unconfirmed || !tr.HasText("Refresh the Runtime configuration before another model update.") || tr.HasText("need-refresh-update") {
		t.Fatalf("English refresh gate copy missing: %v", tr.Texts())
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("请先刷新运行时配置，再更新模型。") {
		t.Fatalf("Chinese refresh gate copy missing: %v", tr.Texts())
	}
}
