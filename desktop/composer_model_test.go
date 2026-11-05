package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func composerModelTestApp(t *testing.T) (*appModel, *contact) {
	t.Helper()
	a := testApp(t)
	ct := a.selectedContact()
	target := selectionTarget()
	ct.SessionID = target.Config.SessionID
	ct.SessionModel = target.Config.EffectiveConfig.Model
	ct.Loaded = true
	ct.Draft = "Keep this draft"
	ct.Context.items = []taskContextItem{{ID: "material", Name: "Source material", Text: "Keep this context"}}
	catalog := selectionCatalog()
	catalog.ConfiguredModels = []runtimeConfiguredModel{
		{Model: target.Config.EffectiveConfig.Model, Compaction: target.Config.EffectiveConfig.Compaction},
		{Model: "provider-model-b", Compaction: runtimeCompaction{ContextTokens: 65536, OutputTokens: 4096}},
	}
	a.modelForm = newModelSelection(target, catalog)
	a.modelPanelKind = "conversation"
	a.modelPanelSessionID = ct.SessionID
	return a, ct
}

func TestComposerModelPopoverGeometryAndKeyboard(t *testing.T) {
	for _, size := range []struct{ w, h int }{{1040, 720}, {960, 640}, {720, 640}} {
		t.Run(fmt.Sprint(size.w), func(t *testing.T) {
			a, ct := composerModelTestApp(t)
			a.windowWidth = size.w
			tr := ui.NewTester(a.view, size.w, size.h)
			tr.SetFocused(true)
			tr.Frame()
			input, inputFound := tr.Find("Composer for Saki")
			chat, chatFound := tr.Find("Chat messages")
			model, modelFound := tr.Find("Chat model")
			if !inputFound || !chatFound || !modelFound {
				t.Fatal("composer/chat/trigger bounds missing before opening")
			}
			messages := len(ct.Messages)
			if err := tr.Click("Chat model"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			after, inputFound := tr.Find("Composer for Saki")
			afterChat, chatFound := tr.Find("Chat messages")
			if !inputFound || !chatFound || chat != afterChat || input != after || a.mainPageActive() || a.workspaceOpen {
				t.Fatalf("opening popover moved chat: before=%+v after=%+v", input, after)
			}
			if !a.composerModelOpen || !tr.Focused("provider-model-a") {
				t.Fatalf("popover did not focus first candidate: %v", tr.Texts())
			}
			for _, name := range []string{"provider-model-a", "provider-model-b", "Manage models"} {
				r, ok := tr.Find(name)
				if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size.w) || r.Y+r.H > float32(size.h) {
					t.Fatalf("%s clipped: %+v", name, r)
				}
			}
			if r, _ := tr.Find("Manage models"); r.Y >= model.Y {
				t.Fatalf("popover did not flip above bottom anchor: popup=%+v anchor=%+v", r, model)
			}
			tr.Key(0, ui.KeyDown)
			tr.Frame()
			if !tr.Focused("provider-model-b") {
				t.Fatal("Down did not move between candidates")
			}
			tr.Key(0, ui.KeyUp)
			tr.Frame()
			if !tr.Focused("provider-model-a") {
				t.Fatal("Up did not return to current model")
			}
			tr.Key(0, ui.KeyDown)
			tr.Frame()
			if !tr.Focused("provider-model-b") {
				t.Fatal("busy Enter did not target the other model")
			}
			// A legitimate busy target exercises the existing write rejection through Enter.
			a.rt = &runtimeClient{}
			a.runtimeStatus = runtimeStateReady
			ct.ActiveTaskID = "active-task"
			tr.Key(0, ui.KeyEnter)
			tr.Frame()
			if !a.composerModelOpen || a.modelForm.Error != modelErrorBusy || len(ct.Messages) != messages || ct.Draft != "Keep this draft" {
				t.Fatal("menu Enter sent draft or hid failed selection")
			}
			a.rt = nil
			tr.Key(0, ui.KeyEscape)
			tr.Frame()
			if a.composerModelOpen || !tr.Focused("Chat model") {
				t.Fatalf("Escape did not restore trigger focus: %v", tr.Focused("Chat model"))
			}
			tr.Key(0, ui.KeyDown)
			tr.Frame()
			if !a.composerModelOpen {
				t.Fatal("Down did not open focused model trigger")
			}
			tr.Key(0, ui.KeyTab)
			tr.Frame()
			if a.composerModelOpen || len(ct.Messages) != messages {
				t.Fatal("Tab did not leave menu without sending")
			}
			a.openComposerModel()
			tr.Frame()
			// Outside click must also reach the clicked input.
			if err := tr.Click("Search contacts"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if a.composerModelOpen || !tr.Focused("Search contacts") {
				t.Fatal("outside click was swallowed")
			}
			focusComposer(t, tr, "Composer for Saki")
			tr.Compose("你好", 2)
			tr.Frame()
			if a.composerModelOpen || len(ct.Messages) != messages {
				t.Fatal("IME composition opened model menu or sent draft")
			}
			tr.Compose("", 0)
		})
	}
}

func TestComposerModelPopoverDeclaredModelsAndSettingsReturn(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			a, ct := composerModelTestApp(t)
			a.modelForm.Catalog.ConfiguredModels = a.modelForm.Catalog.ConfiguredModels[:count]
			// The actual model remains visible even when not in the declared list.
			ct.SessionModel = "actual-unlisted-model"
			a.modelForm.Effective.Config.EffectiveConfig.Model = ct.SessionModel
			tr := ui.NewTester(a.view, 960, 640)
			tr.Frame()
			a.openComposerModel()
			tr.Frame()
			if !tr.HasText("Effective model: actual-unlisted-model") {
				t.Fatalf("actual model hidden: %v", tr.Texts())
			}
			if tr.HasText("Model B") || tr.HasText("Model C") {
				t.Fatal("capacity catalog was used as configured choices")
			}
			if count == 0 && !tr.HasText("No models declared for this connection.") {
				t.Fatal("empty declarations were not reported")
			}
			form := a.modelForm
			form.Draft.Model = "manual-unsaved"
			before := ct.SessionID
			if err := tr.Click("Manage models"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if !a.settingsOpen || a.settingsTab != settingsTabConnection || a.composerModelOpen || a.modelForm != form {
				t.Fatal("Manage did not reuse connection settings and form")
			}
			if !tr.HasText("Default connection") || !tr.HasText("Current conversation override") {
				t.Fatal("configuration scopes are not visible")
			}
			if err := clickSettingsAction(tr, "Provider model ID"); err != nil {
				t.Fatal(err)
			}
			if form.Draft.Model != "manual-unsaved" {
				t.Fatal("settings replaced unsaved conversation model draft")
			}
			a.closeSettings()
			tr.Frame()
			if ct.Draft != "Keep this draft" || ct.Context.count() != 1 || ct.SessionID != before || !tr.Focused("Chat model") {
				t.Fatal("settings return lost input/context/session/focus")
			}
			a.openSettingsTab(settingsTabConnection, settingsSourceRail)
			tr.Frame()
			if a.modelForm != form || form.Draft.Model != "manual-unsaved" {
				t.Fatal("global Settings uses a different conversation form")
			}
		})
	}
}

func TestPreviewComposerModelPopover(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR for headless composer model popover matrix")
	}
	if err := loadAvatar(); err != nil {
		t.Fatal(err)
	}
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, dark := range []bool{false, true} {
			for _, width := range []int{1040, 960} {
				a, _ := composerModelTestApp(t)
				a.windowWidth = width
				a.openComposerModel()
				tr := ui.NewTester(a.view, width, 640)
				tr.SetLocale(locale)
				tr.SetDark(dark)
				tr.Frame()
				writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("headless-model-popover-%s-dark%t-%d.png", locale, dark, width)), tr.Image())
			}
		}
	}
	for _, scene := range []struct {
		locale string
		dark   bool
	}{{"zh-Hans", false}, {"en", true}} {
		a, ct := composerModelTestApp(t)
		a.windowWidth = 960
		long := "provider/organization/region/very-long-shared-model-family-and-release-2026-conversation-model-omega"
		ct.SessionModel = long
		a.modelForm.Effective.Config.EffectiveConfig.Model = long
		a.modelForm.SetErrorKind(modelErrorBusy, "")
		a.openComposerModel()
		tr := ui.NewTester(a.view, 960, 640)
		tr.SetLocale(scene.locale)
		tr.SetDark(scene.dark)
		tr.Frame()
		writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("headless-model-popover-error-long-%s.png", scene.locale)), tr.Image())
	}

}

func TestComposerModelSettingsKeepsEachSessionDraft(t *testing.T) {
	a, ct := composerModelTestApp(t)
	a.openComposerModel()
	first := a.modelForm
	first.Draft.Model = "manual-A"
	a.openSettingsTab(settingsTabConnection, settingsSourceModel)
	a.closeSettings()
	other := a.contacts[1]
	other.SessionID = "session-B"
	target := selectionTarget()
	target.Config.SessionID = other.SessionID
	target.Label = other.Name
	second := newModelSelection(target, selectionCatalog())
	a.conversationModelForms[other.SessionID] = second
	a.selectContact(1)
	a.openComposerModel()
	if a.modelForm != second {
		t.Fatal("B did not read its own session model form")
	}
	second.Draft.Model = "manual-B"
	a.openSettingsTab(settingsTabConnection, settingsSourceModel)
	a.selectSettingsTab(settingsTabAppearance)
	a.selectSettingsTab(settingsTabConnection)
	a.closeSettings()
	a.selectContact(0)
	a.openComposerModel()
	if a.modelForm != first || first.Draft.Model != "manual-A" || second.Draft.Model != "manual-B" || ct.Draft != "Keep this draft" {
		t.Fatal("switching sessions replaced an unsaved model or chat draft")
	}
}

func TestComposerModelSettingsReturnsToOriginalWorkForm(t *testing.T) {
	a, ct := composerModelTestApp(t)
	a.openComposerModel()
	conversation := a.modelForm
	conversation.Draft.Model = "conversation-unsaved"
	a.closeComposerModel()
	target := selectionTarget()
	target.Kind = "work"
	target.Config.SessionID = "work-session"
	work := newModelSelection(target, selectionCatalog())
	work.Draft.Model = "work-unsaved"
	a.modelForm = work
	a.modelPanelKind = "work"
	a.modelPanelSessionID = target.Config.SessionID
	a.aux = "model"
	a.openSettingsTab(settingsTabConnection, settingsSourceAux)
	a.prepareConversationModel()
	if a.modelForm != conversation || a.modelForm.Target.Config.SessionID != ct.SessionID {
		t.Fatal("settings edited work instead of conversation")
	}
	a.modelOpActive = true
	a.closeSettings()
	if a.settingsOpen {
		t.Fatal("an already-read work form could not be restored during a conversation update")
	}
	a.modelOpActive = false
	if a.modelForm != work || a.modelPanelKind != "work" || work.Draft.Model != "work-unsaved" || conversation.Draft.Model != "conversation-unsaved" {
		t.Fatal("settings return replaced another model form or draft")
	}
}

func TestComposerModelSettingsWaitsForUnreadWorkTarget(t *testing.T) {
	a, _ := composerModelTestApp(t)
	a.aux = "model"
	a.modelForm = nil
	a.modelPanelKind = "work"
	a.modelPanelSessionID = "work-session"
	a.modelPanelLabel = "Original work"
	a.openSettingsTab(settingsTabAppearance, settingsSourceAux)
	a.modelOpActive = true
	a.closeSettings()
	tr := ui.NewTester(a.view, 960, 640)
	tr.Frame()
	if !a.settingsOpen || !tr.HasText("Waiting for Runtime…") {
		t.Fatal("pending return lost its target or visible reason")
	}
	a.modelOpActive = false
	a.closeSettings()
	if a.settingsOpen || a.modelPanelKind != "work" || a.modelPanelSessionID != "work-session" || a.modelPanelLabel != "Original work" {
		t.Fatal("unread work return loaded a different target")
	}
}

func TestComposerModelCurrentItemDoesNotConfirmUnknownState(t *testing.T) {
	for _, recovery := range []modelRecoveryState{0, modelRecoveryNeedsRefresh, modelRecoveryNeedsReload} {
		t.Run(fmt.Sprint(recovery), func(t *testing.T) {
			a, ct := composerModelTestApp(t)
			a.openComposerModel()
			if recovery == 0 {
				a.modelForm.SetUnconfirmed(modelUnconfirmedReadback, "controlled unknown")
			} else {
				a.setModelRecovery(ct.SessionID, recovery)
			}
			a.rt = &runtimeClient{}
			a.runtimeStatus = runtimeStateReady
			a.chooseComposerModel(0)
			if !a.composerModelOpen || !a.modelForm.Unconfirmed || a.modelOpActive {
				t.Fatal("current item bypassed unknown/recovery state")
			}
		})
	}
}
