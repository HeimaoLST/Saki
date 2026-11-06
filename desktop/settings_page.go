package main

import (
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"saki/desktop/locales"
)

const (
	settingsTabAppearance = "appearance"
	settingsTabConnection = "connection"
	settingsTabContact    = "contact"
	settingsTabAdvanced   = "advanced"

	settingsSourceRail          = "rail"
	settingsSourceProfile       = "profile"
	settingsSourceWork          = "work"
	settingsSourceConnection    = "connection"
	settingsSourceConfiguration = "configuration"
	settingsSourceCommand       = "command"
	settingsSourceComposer      = "composer"
	settingsSourceModel         = "composer-model"
	settingsSourceHistory       = "history"
	settingsSourceAux           = "aux"
	settingsSourceLink          = "link"
)

var applyAppLocale = mygo.App.SetLocale
var loadLanguageMode = readSavedLanguageTag

func normalizeLanguageMode(mode string) string {
	return strings.TrimSpace(mode)
}

func languageModeIndex(mode string) int {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return 0
	}
	if i18n.Match([]string{mode}, []string{"zh-Hans", "en"}) == "zh-Hans" {
		return 1
	}
	return 2
}

func languageSelfName(msgs locales.Messages, locale string) string {
	if locale == "zh-Hans" {
		return msgs.LanguageChinese()
	}
	return msgs.LanguageEnglish()
}

func (a *appModel) ensureLanguageMode() {
	if a.languageModeLoaded {
		return
	}
	a.languageMode = normalizeLanguageMode(loadLanguageMode())
	a.languageModeLoaded = true
}

func (a *appModel) setLanguageMode(mode string) {
	mode = normalizeLanguageMode(mode)
	applyAppLocale(mode)
	a.languageMode = mode
	a.languageModeLoaded = true
}

func (a *appModel) ensureSettingsProfileEditor(contactID string) *contactProfileEditor {
	ct := a.contactByID(contactID)
	if ct == nil {
		return nil
	}
	if a.settingsProfiles == nil {
		a.settingsProfiles = map[string]*contactProfileEditor{}
	}
	editor := a.settingsProfiles[contactID]
	if editor == nil {
		editor = newContactProfileEditor(contactProfileDraft(ct))
		a.settingsProfiles[contactID] = editor
	}
	a.contactProfile = editor
	a.contactProfileID = contactID
	return editor
}

func (a *appModel) profileEditorFor(contactID string) *contactProfileEditor {
	if a.settingsProfiles != nil {
		if editor := a.settingsProfiles[contactID]; editor != nil {
			return editor
		}
	}
	if a.contactProfile != nil && a.contactProfileID == contactID {
		return a.contactProfile
	}
	return nil
}

func (a *appModel) settingsContactEditor() *contactProfileEditor {
	if a.settingsContactID == "" {
		a.settingsContactID = a.selectedContact().ID
	}
	editor := a.ensureSettingsProfileEditor(a.settingsContactID)
	if editor == nil {
		a.settingsContactID = a.selectedContact().ID
		editor = a.ensureSettingsProfileEditor(a.settingsContactID)
	}
	if editor == nil {
		return nil
	}
	ct := a.contactByID(a.settingsContactID)
	editor.Archived = ct.Archived
	editor.HasConversation = ct.SessionID != ""
	editor.ActiveWork, editor.UnknownWork = a.contactActivity(ct)
	return editor
}

func (a *appModel) openSettingsTab(tab, source string) {
	if source != settingsSourceLink {
		a.settingsReturnLink = markdownLinkTarget{}
		a.linkReturnTarget = markdownLinkTarget{}
		a.linkReturnSeen = false
		a.markdownLinkFocus = markdownLinkTarget{}
	}
	switch tab {
	case settingsTabAppearance, settingsTabConnection, settingsTabContact, settingsTabAdvanced:
	default:
		tab = settingsTabAppearance
	}
	wasOpen := a.settingsOpen
	if !wasOpen {
		if a.aux == "model" && a.modelPanelKind == "work" {
			target := modelSelectionTarget{Kind: "work", Label: a.modelPanelLabel, Config: runtimeConnectionConfig{SessionID: a.modelPanelSessionID}}
			if a.modelForm != nil {
				target = a.modelForm.Target
			}
			a.settingsReturnModelTarget = &target
			a.settingsReturnModel = a.modelForm
		}
		a.settingsReturnFocus = source
		a.settingsFocusBack = true
	}
	a.closeComposerModel()
	if a.modelForm != nil {
		a.modelForm.focusField = ""
	}
	a.settingsOpen = true
	a.settingsTab = tab
	if tab == settingsTabAppearance {
		a.languageModeLoaded = false
	}
	if tab == settingsTabAdvanced {
		a.startupDetailsOpen = true
	}
	a.settingsScroll.Y = 0
	if !wasOpen {
		a.settingsFocusTab = ""
	} else {
		a.settingsFocusTab = tab
	}
	if a.settingsContactID == "" {
		a.settingsContactID = a.selectedContact().ID
	}
	if tab == settingsTabContact {
		if editor := a.settingsContactEditor(); editor != nil {
			editor.focusField = ""
		}
	}
	a.ensureConnectionForm()
	if a.connectionForm != nil {
		a.connectionForm.focusField = ""
	}
}

func (a *appModel) selectSettingsTab(tab string) {
	if tab == a.settingsTab {
		a.settingsScroll.Y = 0
		a.settingsFocusTab = tab
		return
	}
	a.openSettingsTab(tab, a.settingsReturnFocus)
}

func (a *appModel) closeSettings() {
	if !a.settingsOpen || (a.modelOpActive && a.settingsReturnModelTarget != nil && a.settingsReturnModel == nil) {
		return
	}
	a.settingsOpen = false
	if target := a.settingsReturnModelTarget; target != nil && a.aux == "model" {
		if a.settingsReturnModel != nil {
			a.modelOpenToken++
			a.modelForm = a.settingsReturnModel
			a.modelPanelKind = target.Kind
			a.modelPanelSessionID = target.Config.SessionID
			a.modelPanelLabel = target.Label
		} else {
			a.openSessionModel(target.Config.SessionID, target.Kind, target.Label)
		}
	}
	a.settingsReturnModel = nil
	a.settingsReturnModelTarget = nil
	a.settingsFocusBack = false
	a.settingsFocusTab = ""
	switch a.settingsReturnFocus {
	case settingsSourceProfile:
		if a.detailIs(detailProfile) {
			a.focusProfileEdit = true
		} else {
			a.focusContactProfile = true
		}
	case settingsSourceWork:
		if a.detailPageActive() {
			a.focusDetailClose = true
		} else {
			a.focusWorkspace = true
		}
	case settingsSourceConnection:
		a.focusConnectionEntry = true
	case settingsSourceConfiguration:
		a.focusConfiguration = true
	case settingsSourceModel:
		a.composerFocusModel = true
	case settingsSourceComposer:
		a.focusComposer = true
	case settingsSourceHistory:
		switch {
		case a.contactHistoryFull != nil:
			a.focusHistoryFullBack = true
		case a.detailIs(detailHistory):
			a.focusDetailClose = true
		default:
			a.focusHistory = true
		}
	case settingsSourceLink:
		if a.settingsReturnLink != (markdownLinkTarget{}) {
			a.markdownLinkFocus = a.settingsReturnLink
			a.linkReturnTarget = a.settingsReturnLink
			a.linkReturnSeen = false
			a.armActiveSurfaceFallback()
		}
		a.settingsReturnLink = markdownLinkTarget{}
	case settingsSourceAux:
		switch {
		case a.issueResult.Open:
			if a.issueResult.Panel.FocusReturn == nil {
				if a.issueResult.Panel.ReaderKey != "" {
					a.issueResult.Panel.FocusReader = true
				} else {
					a.issueResult.Panel.FocusClose = true
				}
			}
		case a.aux == "sources":
			a.focusSourcesBack = true
		case a.aux == "model":
			if a.composerInlineAux() {
				a.composerFocusModel = true
			} else {
				a.focusAuxBack = true
			}
		case a.aux == "task-context":
			if a.composerInlineAux() {
				a.composerFocusContext = true
			} else {
				a.focusAuxBack = true
			}
		default:
			if a.detailPageActive() {
				a.focusDetailClose = true
			} else {
				a.focusWorkspace = true
			}
		}
	default:
		a.focusSettings = true
	}
	a.settingsReturnFocus = ""
}

func (a *appModel) connectionApplied() bool {
	return a.rt != nil && a.runtimeStatus == runtimeStateReady && a.connectionEffective != nil
}

func (a *appModel) settingsLandingTab() string {
	if a.connectionConfigurationBlocked() {
		return settingsTabConnection
	}
	return settingsTabAppearance
}

func (a *appModel) settingsPage(c *ui.Context, t *ui.Theme) {
	if a.settingsTab == "" {
		a.settingsTab = a.settingsLandingTab()
	}
	if a.settingsContactID == "" {
		a.settingsContactID = a.selectedContact().ID
	}
	a.ensureConnectionForm()
	if a.layoutWidth() < 800 {
		ui.Column(c).Fill().Background(a.workTheme.Reading).Children(func() {
			a.settingsSidebar(c, t, true)
			a.settingsMain(c, t, true)
		})
		return
	}
	ui.Row(c).Fill().Background(a.workTheme.Reading).Children(func() {
		a.settingsSidebar(c, t, false)
		a.settingsMain(c, t, false)
	})
}

func (a *appModel) settingsSidebar(c *ui.Context, t *ui.Theme, narrow bool) {
	msgs := locales.In(c.Locale())
	width := float32(200)
	if narrow {
		width = 0
	}
	column := ui.Column(c).Gap(12).Padding(16).Background(a.workTheme.Rail).AlignItems(ui.Start)
	if narrow {
		column.FillWidth()
	} else {
		column.FillHeight().Width(width)
	}
	column.Children(func() {
		ui.Toolbar(c, func() {
			back := ui.Button(c, "").Label(msgs.SettingsBackAccessible()).
				MinHeight(32).Height(32).Padding(4, 8).Gap(6)
			back.Children(func() {
				ui.Icon(c, navBackIcon).Width(16).Height(16).TextColor(t.TextMuted)
				ui.Text(c, msgs.SettingsBack()).FontSize(typeAction).TextColor(t.Text)
			})
			back.Disabled(a.modelOpActive && a.settingsReturnModelTarget != nil && a.settingsReturnModel == nil).OnClick(func() { a.closeSettings() })
			if a.settingsFocusBack {
				back.Focus()
				a.settingsFocusBack = false
			}
		}).Label(msgs.SettingsNavigationLabel()).AlignSelf(ui.Start).Padding(0)
		if a.modelOpActive && a.settingsReturnModelTarget != nil && a.settingsReturnModel == nil {
			ui.Text(c, msgs.ModelWaiting()).FontSize(typeAction).LineHeight(1.5).TextColor(t.TextMuted)
		}
		ui.Text(c, msgs.SettingsTitle()).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text)
		nav := func(label, tab string) {
			selected := a.settingsTab == tab
			button := ui.ButtonBase(c).Label(label).FontSize(typeAction).MinHeight(32).
				Padding(4, 8).Justify(ui.Start).Background(ui.Transparent).BorderWidth(0).
				TextColor(t.TextMuted).FontWeight(400)
			if !narrow {
				button.FillWidth()
			}
			if selected {
				button.Background(t.Selection).TextColor(t.Text).FontWeight(500)
			}
			if button.Pressed() {
				button.Background(t.SurfacePressed)
			} else if button.Hovered() {
				button.Background(t.SurfaceHover)
			}
			if button.Focused() {
				button.BorderWidth(1).BorderColor(t.Focus)
			}
			button.Children(func() { ui.Text(c, label).FontSize(typeAction) })
			button.OnClick(func() { a.selectSettingsTab(tab) })
			if a.settingsFocusTab == tab {
				button.Focus()
				a.settingsFocusTab = ""
			}
		}
		if narrow {
			ui.Row(c).Gap(4).Wrap().Children(func() {
				nav(msgs.SettingsGeneral(), settingsTabAppearance)
				nav(msgs.SettingsConnection(), settingsTabConnection)
				nav(msgs.SettingsContact(), settingsTabContact)
				nav(msgs.SettingsAdvanced(), settingsTabAdvanced)
			})
		} else {
			ui.Column(c).Gap(4).Children(func() {
				nav(msgs.SettingsGeneral(), settingsTabAppearance)
				nav(msgs.SettingsConnection(), settingsTabConnection)
				nav(msgs.SettingsContact(), settingsTabContact)
				nav(msgs.SettingsAdvanced(), settingsTabAdvanced)
			})
		}
	})
}

func (a *appModel) settingsMain(c *ui.Context, t *ui.Theme, narrow bool) {
	paddingY, paddingX := float32(16), float32(16)
	if narrow {
		paddingY, paddingX = 16, 16
	}
	ui.Column(c).Grow(1).FillHeight().AlignItems(ui.Start).Children(func() {
		ui.Scroll(c).Grow(1).FillHeight().TrackScroll(&a.settingsScroll).Padding(paddingY, paddingX).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(720).AlignItems(ui.Start).Gap(12).Children(func() {
				switch a.settingsTab {
				case settingsTabAppearance:
					a.settingsAppearanceContent(c, t)
				case settingsTabConnection:
					a.settingsConnectionContent(c, t)
				case settingsTabContact:
					a.settingsContactContent(c, t)
				default:
					a.settingsAdvancedContent(c, t)
				}
			})
		})
	})
}

func (a *appModel) settingsAppearanceContent(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	a.ensureLanguageMode()
	ui.Text(c, msgs.SettingsGeneral()).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text)
	ui.Text(c, msgs.SettingsAppearance()).FontSize(typeGroupTitle).FontWeight(500).TextColor(t.TextMuted)
	selected := appearanceModeIndex(a.appearanceMode)
	appearance := segmentedSelection(c, &selected, msgs.AppearanceSystem(), msgs.AppearanceLight(), msgs.AppearanceDark()).Label(msgs.AppearanceMode()).FontSize(typeAction)
	if appearance.Changed() {
		a.setAppearanceMode(appearanceModeFromIndex(selected))
	}
	if a.appearanceStatus != appearanceStatusNone {
		ui.Text(c, appearanceStatusLabel(msgs, a.appearanceStatus)).FontSize(typeAction).TextColor(t.Success)
	}
	if a.appearanceError != "" {
		ui.Text(c, appearanceStatusLabel(msgs, a.appearanceError)).FontSize(typeBody).LineHeight(1.5).TextColor(t.Danger)
		secondaryButton(c, msgs.SettingsRetrySave()).OnClick(func() { a.saveAppearance() })
	}
	ui.Text(c, msgs.SettingsLanguage()).FontSize(typeGroupTitle).FontWeight(500).TextColor(t.TextMuted)
	languageModes := []string{"", "zh-Hans", "en"}
	languageSelected := languageModeIndex(a.languageMode)
	language := segmentedSelection(c, &languageSelected,
		msgs.LanguageSystem(languageSelfName(msgs, c.Locale())),
		msgs.LanguageChinese(),
		msgs.LanguageEnglish(),
	).Label(msgs.SettingsLanguage()).FontSize(typeAction)
	if language.Changed() {
		a.setLanguageMode(languageModes[languageSelected])
	}
	ui.Text(c, msgs.LanguageHint()).FontSize(typeMeta).LineHeight(1.5).TextColor(t.TextMuted)
}

func (a *appModel) settingsConnectionContent(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	form := a.connectionForm
	ui.Column(c).FillWidth().MaxWidth(600).AlignItems(ui.Start).Gap(12).Children(func() {
		ui.Text(c, msgs.SettingsConnection()).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text)
		ui.Text(c, msgs.ModelDefaultScope()).FontSize(typeGroupTitle).FontWeight(500).TextColor(t.Text)
		if a.connectionApplied() && a.connectionEffective != nil {
			ui.Column(c).FillWidth().Gap(4).Children(func() {
				ui.Text(c, msgs.ConnectionCurrent(connectionCurrentSummary(a.connectionEffective))).FontSize(typeBody).LineHeight(1.5).TextColor(t.Text)
				if form.Error != "" {
					ui.Text(c, msgs.ConnectionKeptOnFailure()).FontSize(typeAction).LineHeight(1.5).TextColor(t.Warning)
				}
			})
		} else {
			ui.Text(c, msgs.ConnectionNoneApplied()).FontSize(typeBody).LineHeight(1.5).TextColor(t.TextMuted)
		}
		if form.Dirty() {
			ui.Text(c, msgs.ConnectionUnsavedForm()).FontSize(typeAction).LineHeight(1.5).TextColor(t.Warning)
		}
		if a.connectionSavedPending() {
			ui.Text(c, msgs.ConnectionSavedAwaitingApply()).FontSize(typeAction).LineHeight(1.5).TextColor(t.Warning)
		}
		form.ReloadBlocked = a.connectionReloadBlocked()
		form.SavedPending = a.connectionSavedPending()
		actions := a.connectionActions()
		actions.Discard = func() { form.ResetDraft() }
		form.view(c, t, actions)
		ui.Divider(c)
		ui.Text(c, msgs.ModelConversationScope()).FontSize(typeGroupTitle).FontWeight(500).TextColor(t.Text)
		if a.modelPanelKind == "conversation" && a.modelPanelSessionID == a.selectedContact().SessionID {
			a.modelPanel(c, t)
		} else {
			secondaryButton(c, msgs.ModelSessionTitle()).Disabled(a.modelOpActive).
				OnClick(func() {
					a.prepareConversationModel()
				})
		}
	})
}

func (a *appModel) settingsContactContent(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	editor := a.settingsContactEditor()
	if editor == nil {
		ui.Text(c, msgs.SettingsContact()).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text)
		ui.Text(c, msgs.SettingsNoContactToEdit()).FontSize(14).TextColor(t.Warning)
		return
	}
	options := make([]selectOption, 0, len(a.contacts))
	for _, contact := range a.contacts {
		if contact != nil {
			options = append(options, selectOption{Value: contact.ID, Label: a.settingsContactChoice(msgs, contact)})
		}
	}
	picker := selectWithoutChevron(c, &a.settingsContactID, options, msgs.EditContactLabel())
	if picker.Changed() {
		a.ensureSettingsProfileEditor(a.settingsContactID)
		a.settingsScroll.Y = 0
	}
	var ct *contact
	ct = a.contactByID(a.settingsContactID)
	editor = a.settingsContactEditor()
	ui.Text(c, msgs.SettingsContactTitle(a.settingsContactChoice(msgs, ct))).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text)
	if !profileDraftEqual(editor.Draft, editor.initial) {
		ui.Text(c, msgs.SettingsUnsavedChanges()).FontSize(typeMeta).LineHeight(1.5).TextColor(t.Warning)
	}
	editor.BuiltinTools = append([]runtimeBuiltinTool(nil), a.runtimeBuiltinTools...)
	editor.BuiltinToolsLoaded = a.runtimeBuiltinToolsLoaded
	editor.BuiltinToolsLoading = a.runtimeBuiltinToolsLoading
	editor.BuiltinToolsError = a.runtimeBuiltinToolsErr
	editor.BuiltinToolsSupported = a.connectionManaged
	editor.CanRetryBuiltinTools = editor.BuiltinToolsSupported && a.rt != nil && a.runtimeStatus == runtimeStateReady
	if !editor.BuiltinToolsSupported {
		if a.connectionLegacy {
			editor.BuiltinToolsError = msgs.SettingsBuiltinLegacyError()
		} else {
			editor.BuiltinToolsError = msgs.SettingsBuiltinManagedError()
		}
	} else if !editor.BuiltinToolsLoaded && !editor.BuiltinToolsLoading && editor.BuiltinToolsError == "" && !editor.CanRetryBuiltinTools {
		editor.BuiltinToolsError = msgs.SettingsBuiltinNotReady()
	}
	editor.view(c, t, contactProfileActions{
		Save:              func(draft profileDraft) { a.saveContactProfileDraftFor(ct.ID, draft, false) },
		SaveAndNew:        func(draft profileDraft) { a.saveContactProfileDraftFor(ct.ID, draft, true) },
		Archive:           func() { a.archiveContact(ct.ID) },
		Restore:           func() { a.restoreContact(ct.ID) },
		Discard:           func() { editor.ResetDraft() },
		RetryBuiltinTools: func() { a.retryBuiltinTools() },
	})
}

func (a *appModel) settingsContactChoice(msgs locales.Messages, contact *contact) string {
	if contact == nil {
		return ""
	}
	sameName := 0
	for _, other := range a.contacts {
		if other != nil && other.Name == contact.Name {
			sameName++
		}
	}
	if sameName <= 1 {
		return contact.Name
	}
	for index, other := range a.contacts {
		if other == contact {
			return msgs.ContactChoiceDuplicate(contact.Name, index+1)
		}
	}
	return contact.Name
}

func connectionCurrentSummary(config *connectionConfig) string {
	if config == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if strings.TrimSpace(config.Model) != "" {
		parts = append(parts, config.Model)
	}
	if strings.TrimSpace(config.Endpoint) != "" {
		parts = append(parts, config.Endpoint)
	}
	if strings.TrimSpace(config.API) != "" {
		parts = append(parts, config.API)
	}
	return strings.Join(parts, ", ")
}

func (a *appModel) connectionSavedPending() bool {
	if a.connectionSaved == nil {
		return false
	}
	if !a.connectionApplied() || a.connectionEffective == nil {
		return true
	}
	return *a.connectionSaved != *a.connectionEffective
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (a *appModel) settingsAdvancedContent(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	ui.Text(c, msgs.SettingsAdvanced()).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text)
	ui.Text(c, msgs.SettingsAdvancedIntro()).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
	ui.Text(c, msgs.SettingsStartupSection()).FontSize(typeGroupTitle).FontWeight(500).TextColor(t.Text)
	label := msgs.SettingsShowRuntimeDetails()
	if a.startupDetailsOpen {
		label = msgs.SettingsHideRuntimeDetails()
	}
	secondaryButton(c, label).OnClick(func() { a.startupDetailsOpen = !a.startupDetailsOpen })
	if a.startupDetailsOpen {
		a.runtimePanel(c, t, a.selectedContact())
	}
	secondaryButton(c, msgs.SettingsOpenConnection()).OnClick(func() { a.selectSettingsTab(settingsTabConnection) })
}

func (a *appModel) auxScrollFor(kind string) *ui.ScrollState {
	switch kind {
	case "sources":
		return &a.sourcesScroll
	case "model":
		return &a.modelScroll
	case "task-context":
		return &a.taskContextScroll
	case "issue-result":
		return &a.issueResultScroll
	default:
		return &a.sourcesScroll
	}
}
