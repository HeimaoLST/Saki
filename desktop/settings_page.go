package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
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
	settingsSourceHistory       = "history"
	settingsSourceAux           = "aux"
	settingsSourceLink          = "link"
)

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
		a.settingsReturnFocus = source
		a.settingsFocusBack = true
	}
	a.settingsOpen = true
	a.settingsTab = tab
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
	if !a.settingsOpen {
		return
	}
	a.settingsOpen = false
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

func (a *appModel) settingsPage(c *ui.Context, t *ui.Theme) {
	if a.settingsTab == "" {
		a.settingsTab = settingsTabAppearance
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
	width := float32(200)
	if narrow {
		width = 0
	}
	column := ui.Column(c).Gap(14).Padding(18, 14).Background(a.workTheme.Rail)
	if narrow {
		column.FillWidth()
	} else {
		column.FillHeight().Width(width)
	}
	column.Children(func() {
		back := secondaryButton(c, "← Back to workbench").OnClick(func() { a.closeSettings() })
		if a.settingsFocusBack {
			back.Focus()
			a.settingsFocusBack = false
		}
		ui.Text(c, "Settings").FontSize(typePageTitle).FontWeight(600).TextColor(t.Text)
		nav := func(label, tab string) {
			selected := a.settingsTab == tab
			button := ui.ButtonBase(c).Label(label).FontSize(typeAction).MinHeight(32).
				Padding(4, 8).Background(ui.Transparent).BorderWidth(0).
				TextColor(t.TextMuted).FontWeight(400)
			if !narrow {
				button.FillWidth()
			}
			if selected {
				button.Background(t.Selection).TextColor(t.Text).FontWeight(600)
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
				nav("Appearance", settingsTabAppearance)
				nav("Connection & model", settingsTabConnection)
				nav("Contact profile", settingsTabContact)
				nav("Advanced", settingsTabAdvanced)
			})
		} else {
			ui.Column(c).Gap(3).Children(func() {
				nav("Appearance", settingsTabAppearance)
				nav("Connection & model", settingsTabConnection)
				nav("Contact profile", settingsTabContact)
				nav("Advanced", settingsTabAdvanced)
			})
		}
	})
}

func (a *appModel) settingsMain(c *ui.Context, t *ui.Theme, narrow bool) {
	paddingY, paddingX := float32(32), float32(40)
	if narrow {
		paddingY, paddingX = 20, 16
	}
	ui.Column(c).Grow(1).FillHeight().AlignItems(ui.Start).Children(func() {
		ui.Scroll(c).Grow(1).FillHeight().TrackScroll(&a.settingsScroll).Padding(paddingY, paddingX).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(720).AlignItems(ui.Start).Gap(10).Children(func() {
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
	ui.Text(c, "Appearance").FontSize(typePageTitle).FontWeight(600).TextColor(t.Text)
	ui.Text(c, "Changes apply immediately.").FontSize(typeAction).LineHeight(1.45).TextColor(t.TextMuted)
	a.appearanceChoice = appearanceModeLabel(a.appearanceMode)
	ui.Row(c).Gap(10).AlignItems(ui.Stretch).Children(func() {
		for _, mode := range []struct{ label, value string }{
			{"System", appearanceSystem}, {"Light", appearanceLight}, {"Dark", appearanceDark},
		} {
			mode := mode
			button := ui.ButtonBase(c).Label(mode.label).Grow(1).MinWidth(120).Padding(10).Radius(t.Radius).AlignSelf(ui.Stretch).
				Background(t.SurfaceHover).BorderWidth(1).BorderColor(t.Border)
			if normalizeAppearanceMode(a.appearanceMode) == mode.value {
				button.BorderColor(t.Accent).BorderWidth(2)
			}
			if button.Pressed() {
				button.Background(t.SurfacePressed)
			} else if button.Hovered() {
				button.Background(t.SurfaceHover)
			}
			button.OnClick(func() { a.setAppearanceMode(mode.value) })
			button.Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
					ui.Text(c, mode.label).FontSize(typeAction).FontWeight(600).TextColor(t.Text)
					if mode.value == appearanceSystem {
						system := "Light"
						if a.systemDark {
							system = "Dark"
						}
						ui.Text(c, system).FontSize(typeMeta).TextColor(t.TextMuted)
					}
				})
			})
		}
	})
	if a.appearanceStatus != "" {
		ui.Text(c, a.appearanceStatus).FontSize(typeAction).TextColor(t.Success)
	}
	if a.appearanceError != "" {
		ui.Text(c, a.appearanceError).FontSize(typeAction).LineHeight(1.45).TextColor(t.Danger)
		secondaryButton(c, "Retry save").OnClick(func() { a.saveAppearance() })
	}
}

func (a *appModel) settingsConnectionContent(c *ui.Context, t *ui.Theme) {
	form := a.connectionForm
	ui.Text(c, "Connection & model").FontSize(typePageTitle).FontWeight(600).TextColor(t.Text)
	if a.connectionApplied() && a.connectionEffective != nil {
		ui.Column(c).Padding(10, 12).Radius(t.Radius).Background(t.SurfaceHover).Gap(3).Children(func() {
			ui.Text(c, "Current: "+connectionCurrentSummary(a.connectionEffective)).FontSize(typeAction).LineHeight(1.45).TextColor(t.Text)
			if form.Error != "" {
				ui.Text(c, "The current connection remains in use; this attempt did not replace it.").FontSize(typeAction).LineHeight(1.45).TextColor(t.Warning)
			}
		})
	} else {
		ui.Text(c, "No connection is currently applied.").FontSize(typeAction).LineHeight(1.45).TextColor(t.TextMuted)
	}
	if form.Dirty() {
		ui.Text(c, "Unsaved form changes.").FontSize(typeAction).LineHeight(1.45).TextColor(t.Warning)
	}
	if a.connectionSavedPending() {
		ui.Text(c, "Saved configuration is awaiting Apply.").FontSize(typeAction).LineHeight(1.45).TextColor(t.Warning)
	}
	form.ReloadBlocked = a.connectionReloadBlocked()
	actions := a.connectionActions()
	actions.Discard = func() { form.ResetDraft() }
	form.view(c, t, actions)
}

func (a *appModel) settingsContactContent(c *ui.Context, t *ui.Theme) {
	editor := a.settingsContactEditor()
	if editor == nil {
		ui.Text(c, "Contact profile").FontSize(typePageTitle).FontWeight(600).TextColor(t.Text)
		ui.Text(c, "No contact is available to edit.").FontSize(14).TextColor(t.Warning)
		return
	}
	choices := make([]string, 0, len(a.contacts))
	for _, contact := range a.contacts {
		if contact != nil {
			choices = append(choices, a.settingsContactChoice(contact))
		}
	}
	want := a.settingsContactChoice(a.contactByID(a.settingsContactID))
	if a.settingsPickerChoice == "" || !containsString(choices, a.settingsPickerChoice) {
		a.settingsPickerChoice = want
	}
	picker := selectWithoutChevron(c, &a.settingsPickerChoice, choices, "Edit contact")
	if picker.Changed() {
		for _, contact := range a.contacts {
			if contact != nil && a.settingsContactChoice(contact) == a.settingsPickerChoice {
				a.settingsContactID = contact.ID
				a.ensureSettingsProfileEditor(contact.ID)
				a.settingsScroll.Y = 0
				break
			}
		}
	}
	var ct *contact
	ct = a.contactByID(a.settingsContactID)
	editor = a.settingsContactEditor()
	ui.Text(c, "Contact profile: "+a.settingsContactChoice(ct)).FontSize(typePageTitle).FontWeight(600).TextColor(t.Text)
	if editor.Draft != editor.initial {
		ui.Text(c, "Unsaved changes.").FontSize(typeAction).LineHeight(1.45).TextColor(t.Warning)
	}
	editor.view(c, t, contactProfileActions{
		Save:       func(draft profileDraft) { a.saveContactProfileDraftFor(ct.ID, draft, false) },
		SaveAndNew: func(draft profileDraft) { a.saveContactProfileDraftFor(ct.ID, draft, true) },
		Archive:    func() { a.archiveContact(ct.ID) },
		Restore:    func() { a.restoreContact(ct.ID) },
		Discard:    func() { editor.ResetDraft() },
	})
}

func (a *appModel) settingsContactChoice(contact *contact) string {
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
			return fmt.Sprintf("%s (%d)", contact.Name, index+1)
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
	ui.Text(c, "Advanced").FontSize(typePageTitle).FontWeight(600).TextColor(t.Text)
	ui.Text(c, "Read-only Runtime and startup diagnostics. Open connection settings to repair configuration.").FontSize(14).LineHeight(1.45).TextColor(t.TextMuted)
	ui.Text(c, "Startup & configuration").FontSize(typeGroupTitle).FontWeight(600).TextColor(t.Text)
	label := "Show runtime, paths and session"
	if a.startupDetailsOpen {
		label = "Hide runtime, paths and session"
	}
	secondaryButton(c, label).OnClick(func() { a.startupDetailsOpen = !a.startupDetailsOpen })
	if a.startupDetailsOpen {
		a.runtimePanel(c, t, a.selectedContact())
	}
	secondaryButton(c, "Open connection & model").OnClick(func() { a.selectSettingsTab(settingsTabConnection) })
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
