package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"saki/desktop/locales"
)

// Message typography is supplied at the view call site so the reader's
// parser, clipboard and file behavior stay untouched (#187 owns those files).
var messageMarkdownTheme = &markdown.Theme{FontSize: typeBody, LineHeight: 1.5}

const (
	composerPanelContext = "task-context"
)

// Small, deliberately understated composer glyphs. MyGo's Icon inherits the
// button's text color, so these stay in the same semantic color path as UI text.
var (
	composerAttachIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M21 11.5l-8.6 8.6a6 6 0 0 1-8.5-8.5l9.3-9.3a4 4 0 0 1 5.7 5.7l-9.3 9.3a2 2 0 0 1-2.8-2.8l8.6-8.6"/></svg>`))
	composerSendIcon   = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M12 19V5M5 12l7-7 7 7"/></svg>`))
	composerStopIcon   = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect x="6" y="6" width="12" height="12" rx="2" fill="currentColor"/></svg>`))
	railHideIcon       = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="16" rx="2"/><path d="M9 4v16M16 9l-3 3 3 3"/></svg>`))
	railAddIcon        = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 5v14M5 12h14"/></svg>`))
	railSettingsIcon   = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h9M17 7h3M4 17h3M11 17h9"/><circle cx="15" cy="7" r="2"/><circle cx="9" cy="17" r="2"/></svg>`))
)

func messageReaderOptions(c *ui.Context, plain bool) markdownReaderOptions {
	return markdownReaderOptions{PlainText: plain, CopyLabel: locales.In(c.Locale()).CopyMessageLabel(), Theme: messageMarkdownTheme}
}

func (a *appModel) view(c *ui.Context) {
	a.uiLocale = c.Locale()
	a.syncDetailPageMode()
	if a.settingsOpen || a.mainPageActive() || a.detailPageActive() {
		a.closeComposerModel()
	}
	if !a.settingsOpen {
		a.linkReturnSeen = false
	}
	// Context.begin resets this to the system theme on every pass, so this is
	// the live system appearance even while an explicit mode is shown.
	a.systemDark = c.Theme().Dark
	a.workTheme = resolveWorkbenchTheme(resolveAppearanceDark(a.appearanceMode, a.systemDark))
	t := a.workTheme.Theme
	c.SetTheme(t)
	c.OnShortcut(ui.Cmd, ui.KeyComma, func() {
		if !a.settingsOpen {
			source := settingsSourceCommand
			if c.Focused(a.composerHandle) {
				source = settingsSourceComposer
			} else if !a.captureLinkSettingsSource(c, &source) {
				source = a.settingsSourceForVisiblePage()
				if source == settingsSourceAux && a.issueResult.Open {
					a.issueResult.Panel.FocusReturn = issueResultFocusedHandle(c, &a.issueResult.Panel, a.issueResult.Panel.ReaderKey != "")
				}
			}
			a.openSettingsTab(settingsTabAppearance, source)
		}
	})
	if a.settingsOpen {
		a.settingsPage(c, t)
		return
	}
	c.OnShortcut(0, ui.KeyEscape, func() { a.escapeTopLayer() })
	ui.Box(c).Fill().Children(func() {
		if a.mainPageActive() {
			a.mainPage(c, t)
			return
		}
		ui.Row(c).Fill().Children(func() {
			a.layoutBody(c, t)
		})
	})
	a.finishMarkdownLinkReturn()
}

func (a *appModel) escapeTopLayer() {
	if a.issueResult.Open {
		if a.issueResult.Panel.ReaderKey != "" {
			a.closeIssueResultReader()
			return
		}
		a.closeIssueResult()
		return
	}
	if a.contactHistoryFull != nil {
		a.closeContactHistoryFull()
		return
	}
	if a.aux != "" {
		a.closeAux()
		return
	}
	if a.workspaceOpen {
		if a.activeDetail() == detailWork {
			if a.escapeWorkspaceLayer() {
				return
			}
		}
		a.closeDetail()
	}
}

func (a *appModel) captureLinkSettingsSource(c *ui.Context, source *string) bool {
	link := ui.FocusedValue(c, &a.markdownLinkFocus)
	if link == (markdownLinkTarget{}) {
		return false
	}
	a.settingsReturnLink = link
	*source = settingsSourceLink
	return true
}

func (a *appModel) settingsSourceForVisiblePage() string {
	switch {
	case a.issueResult.Open, a.aux != "":
		return settingsSourceAux
	case a.contactHistoryFull != nil, a.detailIs(detailHistory):
		return settingsSourceHistory
	case a.detailIs(detailProfile):
		return settingsSourceProfile
	case a.workspaceOpen:
		return settingsSourceWork
	default:
		return settingsSourceCommand
	}
}

func (a *appModel) openSettingsFromContext(c *ui.Context, tab, source string) {
	a.captureLinkSettingsSource(c, &source)
	a.openSettingsTab(tab, source)
}

func (a *appModel) armActiveSurfaceFallback() {
	switch {
	case a.aux == "issue-result" && a.issueResult.Open:
		if a.issueResult.Panel.ReaderKey != "" {
			a.issueResult.Panel.FocusReader = true
		} else {
			a.issueResult.Panel.FocusClose = true
		}
	case a.contactHistoryFull != nil:
		a.focusHistoryFullBack = true
	case a.detailIs(detailHistory):
		a.focusDetailClose = true
	case a.aux == "sources":
		a.focusSourcesBack = true
	case a.detailIs(detailProfile):
		a.focusProfileEdit = true
	default:
		a.focusComposer = true
	}
}

func (a *appModel) readerOptions(options markdownReaderOptions, scope string) markdownReaderOptions {
	if !options.PlainText && scope != "" {
		options.LinkScope = scope
		options.LinkFocus = &a.markdownLinkFocus
		options.LinkReturn = &a.linkReturnTarget
		options.LinkReturnSeen = &a.linkReturnSeen
	}
	return options
}

func messageLinkScope(contactID string, m *message) string {
	if m == nil {
		return ""
	}
	var identity string
	switch {
	case m.Key != "":
		identity = m.Key
	case m.TaskID != "" || m.MessageID != "" || m.ItemID != "":
		identity = m.TaskID + "/" + m.MessageID + "/" + m.ItemID
	default:
		identity = fmt.Sprintf("%d", m.ID)
	}
	return "contact:" + contactID + "/message:" + identity
}

func historyLinkScope(contactID, document, entryID string) string {
	if entryID == "" {
		return ""
	}
	return "contact:" + contactID + "/history:" + document + "/entry:" + entryID
}

func (a *appModel) finishMarkdownLinkReturn() {
	if a.linkReturnTarget == (markdownLinkTarget{}) {
		return
	}
	if a.linkReturnSeen {
		a.linkReturnTarget = markdownLinkTarget{}
		return
	}
	a.markdownLinkFocus = markdownLinkTarget{}
	a.linkReturnTarget = markdownLinkTarget{}
}

// ---------- rail ----------

func (a *appModel) rail(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	ui.Column(c).FillWidth().FillHeight().Padding(8).Gap(8).Background(a.workTheme.Rail).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "Nagi").FontSize(typePageTitle).FontWeight(500).TextColor(t.Text)
			ui.Spacer(c)
			ui.Toolbar(c, func() {
				hide := appTooltip(c, ui.Button(c, "").Label(msgs.RailHide()), msgs.RailHideContactsTooltip()).
					MinWidth(32).MinHeight(32).Width(32).Height(32).Padding(0).Justify(ui.Center)
				hide.Children(func() {
					ui.Icon(c, railHideIcon).Width(18).Height(18).TextColor(t.TextMuted)
				})
				hide.OnClick(func() { a.toggleRail() })
				if a.focusRail {
					hide.Focus()
					a.focusRail = false
				}
			}).Label(msgs.RailWindowActions()).Padding(0)
		})
		ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
			ui.Text(c, msgs.RailContactsTitle()).FontSize(typeGroupTitle).FontWeight(500).TextColor(t.TextMuted)
			ui.Spacer(c)
			ui.Toolbar(c, func() {
				add := appTooltip(c, ui.Button(c, "").Label(msgs.AvatarAddContact()), msgs.AvatarAddContact()).
					MinWidth(32).MinHeight(32).Width(32).Height(32).Padding(0).Justify(ui.Center)
				add.Children(func() {
					ui.Icon(c, railAddIcon).Width(18).Height(18).TextColor(t.TextMuted)
				})
				add.OnClick(func() { a.addDefaultContact() })
			}).Label(msgs.RailContactListActions()).Padding(0)
		})
		ui.SearchField(c, &a.contactSearch).Label(msgs.SearchContacts()).FontSize(typeAction).Placeholder(msgs.RailContactSearchPlaceholder())
		a.contactScopeSelect(c)
		visible := a.visibleContactIndices()
		if len(visible) == 0 {
			ui.Text(c, msgs.RailNoContactsMatch()).FontSize(typeAction).LineHeight(1.35).TextColor(t.TextMuted)
			ui.Spacer(c).Grow(1)
		} else {
			ui.Scroll(c).Grow(1).Children(func() {
				for _, i := range visible {
					a.railRow(c, t, i)
				}
			})
		}
		ui.Toolbar(c, func() {
			settingsLabel := locales.In(c.Locale()).SettingsTitle()
			settings := appTooltip(c, ui.Button(c, "").Label(settingsLabel), locales.In(c.Locale()).SettingsOpenTooltip()).
				FillWidth().MinHeight(32).Height(32).Padding(4, 8).Gap(8).Justify(ui.Start)
			settings.Children(func() {
				ui.Icon(c, railSettingsIcon).Width(16).Height(16).TextColor(t.TextMuted)
				ui.Text(c, settingsLabel).FontSize(typeAction).TextColor(t.Text)
			})
			settings.OnClick(func() { a.openSettingsTab(a.settingsLandingTab(), settingsSourceRail) })
			if a.focusSettings {
				settings.Focus()
				a.focusSettings = false
			}
		}).Label(locales.In(c.Locale()).SettingsRailActions()).Padding(0).FillWidth()
	})
}

func (a *appModel) avatarRail(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Width(avatarRailWidth).Shrink(0).FillHeight().Padding(4).Gap(4).Background(a.workTheme.Rail).Children(func() {
		addLabel := locales.In(c.Locale()).AvatarAddContact()
		add := appTooltip(c, ui.Button(c, "").Label(addLabel).
			MinWidth(32).MinHeight(32).Width(32).Height(32).Padding(0).Justify(ui.Center), addLabel)
		add.Children(func() { ui.Icon(c, railAddIcon).Width(18).Height(18).TextColor(t.TextMuted) })
		add.OnClick(func() { a.addDefaultContact() })
		visible := a.visibleContactIndices()
		if len(visible) == 0 {
			ui.Spacer(c).Grow(1)
		} else {
			ui.Scroll(c).Grow(1).Children(func() {
				for _, i := range visible {
					a.avatarRailRow(c, t, i)
				}
			})
		}
		ui.Toolbar(c, func() {
			settingsLabel := locales.In(c.Locale()).SettingsTitle()
			settings := appTooltip(c, ui.Button(c, "").Label(settingsLabel).
				MinWidth(32).MinHeight(32).Width(32).Height(32).Padding(0).Justify(ui.Center),
				locales.In(c.Locale()).SettingsOpenTooltip())
			settings.Children(func() { ui.Icon(c, railSettingsIcon).Width(16).Height(16).TextColor(t.TextMuted) })
			settings.OnClick(func() { a.openSettingsTab(a.settingsLandingTab(), settingsSourceRail) })
			if a.focusSettings {
				settings.Focus()
				a.focusSettings = false
			}
		}).Label(locales.In(c.Locale()).SettingsRailActions()).Padding(0)
	})
}

func (a *appModel) avatarRailStatus(msgs locales.Messages, ct *contact) string {
	notice := a.issueWorkNotice(ct)
	status := ""
	switch {
	case notice.Attention:
		status = msgs.TaskRailAttention(notice.IssueName)
	case notice.Waiting:
		status = msgs.TaskRailWaiting(notice.IssueName)
	case notice.Active:
		status = msgs.TaskRailInProgress(notice.IssueName)
	default:
		status = a.headerStatusLabel(ct, msgs)
	}
	if notice.ActiveCount > 0 {
		status += "\n" + msgs.TaskActiveCount(notice.ActiveCount)
	}
	return status
}

func (a *appModel) avatarRailRow(c *ui.Context, t *ui.Theme, i int) {
	ct := a.contacts[i]
	msgs := locales.In(c.Locale())
	status := a.avatarRailStatus(msgs, ct)
	label := msgs.AvatarContactLabel(ct.Name)
	tip := label
	if status != "" {
		tip += "\n" + status
	}
	row := ui.ButtonBase(c).Key(ct.ID).Label(label).Description(status).
		Selected(i == a.selected).Width(48).Height(44).Padding(4).Radius(8).
		Justify(ui.Center).Background(ui.Transparent).BorderWidth(0).TextColor(t.Text)
	if i == a.selected {
		row.Background(t.Selection).TextColor(t.Text)
	}
	row = appTooltip(c, row, tip)
	row.OnClick(func() { a.selectContact(i) })
	row.Children(func() {
		a.avatarTile(c, t, 28)
		if glyph, count, color := a.avatarRailMarker(ct); glyph != "" {
			text := glyph
			if count > 0 {
				text = fmt.Sprintf("%s%d", glyph, count)
			}
			ui.Text(c, text).
				FontSize(typeMeta).FontWeight(500).TextColor(color).
				Background(t.Surface).Padding(1, 3).Radius(4).SingleLine().
				Absolute().Right(0).Bottom(0).IgnoreInput(true)
		}
	})
}

func (a *appModel) avatarRailMarker(ct *contact) (string, int, ui.Color) {
	notice := a.issueWorkNotice(ct)
	switch {
	case notice.AttentionCount > 0:
		return "!", notice.AttentionCount, a.workTheme.Theme.Warning
	case notice.WaitingCount > 0:
		return "◷", notice.WaitingCount, a.workTheme.Theme.Warning
	case notice.ActiveCount > 0:
		return "▶", notice.ActiveCount, a.workTheme.Theme.Success
	case ct.Status == statusWorking:
		return "▶", 0, a.workTheme.Theme.Success
	case ct.Status == statusAttention:
		return "!", 0, a.workTheme.Theme.Warning
	default:
		return "", 0, a.workTheme.Theme.TextMuted
	}
}

func contactScopeIndex(scope string) int {
	switch scope {
	case "archived":
		return 1
	case "all":
		return 2
	default:
		return 0
	}
}

func contactScopeFromIndex(index int) string {
	switch index {
	case 1:
		return "archived"
	case 2:
		return "all"
	default:
		return "active"
	}
}

func (a *appModel) contactScopeSelect(c *ui.Context) {
	msgs := locales.In(c.Locale())
	index := contactScopeIndex(a.contactScope)
	choice := segmentedSelection(c, &index, msgs.ContactScopeActive(), msgs.ContactScopeArchived(), msgs.ContactScopeAll()).
		Label(msgs.ContactScopeLabel()).FontSize(typeAction).FillWidth()
	if choice.Changed() {
		a.setContactScope(contactScopeFromIndex(index))
	}
}

func (a *appModel) openIssueTarget(issueID string) {
	ct := a.selectedContact()
	if ct == nil || issueID == "" || ct.issueByID(issueID) == nil {
		return
	}
	a.selectIssue(issueID)
	a.setWorkspaceOpen(true)
}

func (a *appModel) openIssueFromRail(index int, issueID string) {
	if index < 0 || index >= len(a.contacts) || issueID == "" {
		return
	}
	ownerID := a.contacts[index].ID
	a.selectContact(index)
	ct := a.selectedContact()
	if ct == nil || ct.ID != ownerID {
		return
	}
	a.openIssueTarget(issueID)
}

func (a *appModel) railRow(c *ui.Context, t *ui.Theme, i int) {
	ct := a.contacts[i]
	currentMsgs := locales.In(c.Locale())
	row := ui.ButtonBase(c).Label(currentMsgs.AvatarContactLabel(ct.Name)).Gap(8).Padding(6).MinHeight(44).Radius(8).AlignItems(ui.Start).Background(ui.Transparent).BorderWidth(0)
	if i == a.selected {
		row.Background(t.SurfaceHover)
	}
	row.OnClick(func() { a.selectContact(i) })
	row.Children(func() {
		a.avatarTile(c, t, 28)
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
				nameWeight := 400
				if i == a.selected {
					nameWeight = 500
				}
				ui.Text(c, ct.Name).FontSize(typeRailName).FontWeight(nameWeight).TextColor(t.Text)
				ui.Spacer(c)
				msgs := locales.In(c.Locale())
				notice := a.issueWorkNotice(ct)
				if notice.ActiveCount > 0 {
					ui.Text(c, msgs.TaskActiveCount(notice.ActiveCount)).FontSize(12).TextColor(t.TextMuted)
				}
				if ct.Unread > 0 {
					ui.Text(c, fmt.Sprintf("%d", ct.Unread)).FontSize(12).TextColor(t.Warning)
				}
			})
			msgs := locales.In(c.Locale())
			notice := a.issueWorkNotice(ct)
			line := a.snippet(ct, msgs)
			color := t.TextMuted
			targetID := ""
			switch {
			case notice.Attention:
				line = msgs.TaskRailAttention(notice.IssueName)
				color = t.Warning
				targetID = notice.IssueID
			case notice.Waiting:
				line = msgs.TaskRailWaiting(notice.IssueName)
				color = t.Warning
				targetID = notice.IssueID
			case notice.Active:
				line = msgs.TaskRailInProgress(notice.IssueName)
				color = t.Success
				targetID = notice.IssueID
			case ct.Status != statusReady:
				status := a.statusLabel(ct, msgs)
				detail := strings.TrimSpace(line)
				if detail == status {
					detail = ""
				}
				if detail == "" {
					line = status
				} else {
					line = msgs.StatusDetail(status, detail)
				}
				color = a.statusColor(t, ct.Status)
			}
			if targetID != "" {
				summary := ui.Button(c, "").Label(line).FontSize(typeRailSummary).LineHeight(1.2).
					MinHeight(32).Padding(2, 6).FillWidth().Justify(ui.Start)
				summary = appTooltip(c, summary, line)
				summary.OnClick(func() { a.openIssueFromRail(i, targetID) })
				summary.Children(func() {
					ui.Text(c, line).FontSize(typeRailSummary).LineHeight(1.2).MaxLines(2).TextColor(color)
				})
			} else {
				ui.Text(c, line).FontSize(typeRailSummary).LineHeight(1.2).MaxLines(1).TextColor(color)
			}
		})
	})
}

func (a *appModel) avatarTile(c *ui.Context, t *ui.Theme, size float32) {
	tile := ui.Column(c).Width(size).Height(size).Radius(size / 3).Background(t.SurfaceHover)
	tile.Children(func() {
		if avatarBMP != nil {
			ui.Image(c, avatarBMP).Width(size).Height(size).Radius(size / 3)
		}
	})
}

func (a *appModel) statusColor(t *ui.Theme, s contactStatus) ui.Color {
	switch s {
	case statusWorking:
		return t.Success
	case statusAttention:
		return t.Warning
	default:
		return t.TextMuted
	}
}

// ---------- main column ----------

func (a *appModel) headerStatusLabel(ct *contact, msgs locales.Messages) string {
	if ct != nil && (ct.Status == statusWorking || ct.Status == statusAttention) {
		return a.statusLabel(ct, msgs)
	}
	switch a.connectionViewState() {
	case connectionViewApplying:
		return msgs.ConnectionStatusApplying()
	case connectionViewConnecting:
		return msgs.ConnectionStatusConnecting()
	case connectionViewSaved:
		return msgs.ConnectionStatusSavedAwaitingApply()
	case connectionViewSetup:
		return msgs.ConnectionStatusSetupNeeded()
	case connectionViewNotConnected:
		return msgs.ConnectionStatusNotConnected()
	default:
		return a.statusLabel(ct, msgs)
	}
}

func (a *appModel) headerStatusColor(t *ui.Theme, ct *contact) ui.Color {
	if ct != nil && (ct.Status == statusWorking || ct.Status == statusAttention) {
		return a.statusColor(t, ct.Status)
	}
	switch a.connectionViewState() {
	case connectionViewSaved, connectionViewSetup, connectionViewNotConnected:
		return t.Warning
	default:
		return a.statusColor(t, ct.Status)
	}
}

func (a *appModel) connectionRecoveryNotice(c *ui.Context, t *ui.Theme, state connectionViewState) {
	msgs := locales.In(c.Locale())
	title, hint := msgs.ConnectionStatusSetupNeeded(), msgs.ConnectionRecoverySetupHint()
	switch state {
	case connectionViewSaved:
		title, hint = msgs.ConnectionStatusSavedAwaitingApply(), msgs.ConnectionRecoverySavedHint()
	case connectionViewNotConnected:
		title, hint = msgs.ConnectionStatusNotConnected(), msgs.ConnectionRecoveryNotConnectedHint()
		if a.runtimeStage == "Connection configuration" && (a.connectionEffective != nil || a.connectionSaved != nil || a.connectionManaged) {
			title, hint = msgs.ConnectionRecoveryManagedTitle(), msgs.ConnectionRecoveryManagedHint()
		}
	}
	ui.Column(c).Padding(8, 16).Gap(3).Children(func() {
		ui.Text(c, title).FontSize(14).LineHeight(1.5).TextColor(t.Warning)
		if hint != "" {
			ui.Text(c, hint).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
		}
		if state == connectionViewNotConnected && a.runtimeHintDisplay(msgs) != "" {
			ui.Text(c, a.runtimeHintDisplay(msgs)).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
		}
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			secondaryButton(c, msgs.ConnectionRecoveryOpenSettings()).Label(msgs.ConnectionRecoveryOpenSettings()).MinHeight(32).OnClick(func() { a.openConfiguration() })
			secondaryButton(c, msgs.ConfigurationDetails()).OnClick(func() { a.openAdvancedSettings() })
		})
	})
}

func (a *appModel) main(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	msgs := locales.In(c.Locale())
	ui.Column(c).Grow(1).FillHeight().Background(a.workTheme.Reading).Children(func() {
		if !a.demo {
			state := a.connectionViewState()
			switch {
			case a.runtimeStatus == runtimeStateDraining:
				ui.Text(c, msgs.RuntimeDraining()).FontSize(14).Padding(8, 16).TextColor(t.TextMuted)
			case state == connectionViewApplying:
				ui.Column(c).Padding(8, 16).Gap(2).Children(func() {
					ui.Text(c, msgs.ConnectionStatusApplying()).FontSize(14).LineHeight(1.5).TextColor(t.Warning)
					ui.Text(c, msgs.ConnectionRecoverySavedHint()).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
				})
			case a.connectionConfigurationBlocked():
				a.connectionRecoveryNotice(c, t, state)
			case a.hasRuntimeError():
				label := a.runtimeStageDisplay(msgs)
				ui.Column(c).Padding(8, 16).Gap(2).Children(func() {
					ui.Text(c, msgs.RuntimeStageLabel(label, a.runtimeErrorDisplay(msgs))).FontSize(14).LineHeight(1.5).TextColor(t.Danger)
					if a.runtimeHintDisplay(msgs) != "" {
						ui.Text(c, a.runtimeHintDisplay(msgs)).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
					}
					ui.Row(c).Children(func() {
						details := secondaryButton(c, msgs.ConfigurationDetails())
						if a.focusConfiguration {
							details.Focus()
							a.focusConfiguration = false
						}
						if details.Clicked() {
							a.openAdvancedSettings()
						}
					})
				})
			case a.runtimeStatus == runtimeStateStarting:
				ui.Text(c, msgs.RuntimeConnecting()).FontSize(14).Padding(8, 16).TextColor(t.TextMuted)
			}
		}
		a.connectionErrorBanner(c, t, ct)
		if ct.LoadErr != "" {
			if !knownConnectionFailure(ct.LoadErr) {
				ui.Text(c, ct.LoadErr).FontSize(14).Padding(8, 16).TextColor(t.Warning)
			}
		}
		if ct.Archived {
			ui.Row(c).AlignItems(ui.Center).Gap(8).Padding(6, 16).Background(t.SurfaceHover).Children(func() {
				ui.Text(c, msgs.ArchiveNotice()).FontSize(14).TextColor(t.Warning)
				ui.Spacer(c)
				secondaryButton(c, msgs.RestoreContactAction()).TextColor(t.Text).OnClick(func() { a.restoreContact(ct.ID) })
				secondaryButton(c, msgs.ContactProfileAction()).OnClick(func() { a.openContactProfile(ct.ID) })
			})
		}
		a.header(c, t)
		a.chat(c, t)
	})
}

func (a *appModel) workEntryStatus(ct *contact, msgs locales.Messages) (string, bool) {
	if ct == nil {
		return "", false
	}
	notice := a.issueWorkNotice(ct)
	switch {
	case notice.Attention:
		return msgs.TaskStatusNeedsAttention(), true
	case notice.Waiting:
		return msgs.TaskStatusWaiting(), true
	case notice.Active:
		return msgs.TaskStatusInProgress(), false
	default:
		return "", false
	}
}

func (a *appModel) header(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	msgs := locales.In(c.Locale())
	ui.Row(c).Justify(ui.Center).Children(func() {
		ui.Column(c).WidthPercent(86).MaxWidth(760).Padding(6, 0).Gap(4).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
				a.avatarTile(c, t, 28)
				ui.Column(c).MinWidth(0).Shrink(1).Gap(1).Children(func() {
					ui.Text(c, ct.Name).FontSize(typeContactTitle).FontWeight(500).TextColor(t.Text).MaxLines(2)
					ui.Row(c).AlignItems(ui.Center).Gap(12).Wrap().Children(func() {
						ui.Text(c, a.headerStatusLabel(ct, msgs)).FontSize(12).LineHeight(1.35).FontWeight(500).TextColor(a.headerStatusColor(t, ct))
						ui.Text(c, ct.Role).FontSize(12).FontWeight(400).LineHeight(1.35).MinWidth(0).Grow(1).TextColor(t.TextMuted)
					})
				})
				ui.Spacer(c)
				ui.Row(c).Shrink(0).Children(func() { a.navigationToolbar(c, t) })
			})
			if status, attention := a.workEntryStatus(ct, msgs); status != "" && !a.workspaceOpen {
				color := t.Success
				if attention {
					color = t.Warning
				}
				ui.Text(c, status).FontSize(typeMeta).LineHeight(1.35).TextColor(color)
			}
		})
	})
}

// ---------- chat ----------

type chatItem struct {
	id      string
	entry   *historyEntry
	msg     *message
	working bool
}

func (a *appModel) chat(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	items := make([]chatItem, 0, len(ct.Messages)+len(ct.Revealed)+1)
	for _, e := range a.revealedEntries() {
		e := e
		items = append(items, chatItem{id: "h-" + e.ID, entry: &e})
	}
	for _, m := range ct.Messages {
		m := m
		key := fmt.Sprintf("m-%d", m.ID)
		if m.Key != "" {
			key = m.Key
		}
		items = append(items, chatItem{id: key, msg: m})
	}
	ct.list.Key = func(i int) any { return items[i].id }
	ct.list.FollowEnd = true
	if a.scrollTo >= 0 {
		ct.list.ScrollTo(a.scrollTo, ui.Start)
		a.scrollTo = -1
	}
	ui.Column(c).Grow(1).Children(func() {
		if len(items) == 0 {
			ui.Row(c).Grow(1).FillHeight().Justify(ui.Center).Children(func() {
				ui.Scroll(c).FillHeight().WidthPercent(86).MaxWidth(760).Padding(16, 0).Children(func() {
					a.emptyState(c, t)
				})
			})
		} else {
			ui.Row(c).Grow(1).FillHeight().Justify(ui.Center).Children(func() {
				ui.List(c, &ct.list, len(items), func(i int) {
					it := items[i]
					switch {
					case it.entry != nil:
						a.historyMessage(c, t, *it.entry, it.entry.ID == a.highlight)
					default:
						a.message(c, t, ct, it.msg)
					}
				}).Label(locales.In(c.Locale()).ConversationMessagesLabel()).FillHeight().WidthPercent(86).MaxWidth(760).Padding(16, 0).Gap(12)
			})
		}
		if len(items) > 0 && !ct.list.AtEnd() {
			ui.Row(c).Key("back-to-latest").Justify(ui.Center).Children(func() {
				back := secondaryButton(c, locales.In(c.Locale()).BackToLatest())
				back.OnClick(func() {
					ct.list.ScrollToEnd()
					a.focusComposer = true
				})
			})
		}
		ui.Row(c).Key("composer").Justify(ui.Center).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(760).Children(func() {
				a.composer(c, t)
			})
		})
	})
}

func (a *appModel) historyMessage(c *ui.Context, t *ui.Theme, e historyEntry, highlight bool) {
	bg := a.workTheme.BubbleContact
	if e.From == "me" {
		bg = a.workTheme.BubbleUser
	} else if highlight {
		bg = t.Selection
	}
	if e.From == "me" {
		ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
			ui.Column(c).MaxWidth(460).MinWidth(0).Gap(4).Children(func() {
				if e.Text != "" {
					opts := messageReaderOptions(c, true)
					opts.MessageBubble = &markdownMessageBubble{Background: bg, PaddingX: 12, PaddingY: 8, Radius: 12, ContextualCopy: true}
					markdownReader(c, e.Text, opts).FontSize(typeBody).LineHeight(1.5).TextColor(t.Text)
				}
				taskContextReadOnlyView(c, a.workTheme, e.Context)
			})
		})
		return
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Start).Children(func() {
		a.avatarTile(c, t, 28)
		ui.Column(c).MaxWidth(680).MinWidth(0).Shrink(1).Children(func() {
			opts := a.readerOptions(messageReaderOptions(c, false), historyLinkScope(a.selectedContact().ID, "conversation", e.ID))
			opts.MessageBubble = &markdownMessageBubble{
				Background: bg, PaddingX: 14, PaddingY: 10, Radius: 14,
				Header: func(hc *ui.Context) {
					ui.Text(hc, a.selectedContact().Name+"  "+e.Date).FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
				},
			}
			markdownReader(c, e.Text, opts).FontSize(typeBody).LineHeight(1.5)
		})
	})
}

func (a *appModel) message(c *ui.Context, t *ui.Theme, ct *contact, m *message) {
	msgs := locales.In(c.Locale())
	switch m.Kind {
	case msgActivity:
		if m.ToolName != "" || m.ToolOutput != "" {
			title := toolTitleFor(m.ToolName, m.ToolArgs)
			summary := toolOutputSummary(msgs, m.ToolOutput)
			if m.Expanded {
				summary = truncate(m.ToolOutput, 2000)
			}
			ui.Column(c).Gap(2).Padding(4, 0).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Text(c, title).FontSize(typeAction).LineHeight(1.4).TextColor(t.TextMuted)
					if m.ToolOutput != "" {
						label := msgs.DetailsAction()
						if m.Expanded {
							label = msgs.HideAction()
						}
						secondaryButton(c, label).Radius(999).OnClick(func() { m.Expanded = !m.Expanded })
					}
				})
				if summary != "" {
					lines := 2
					if m.Expanded {
						lines = 12
					}
					ui.Text(c, summary).FontSize(typeAction).LineHeight(1.5).MaxLines(lines).TextColor(t.TextMuted)
				}
			})
			return
		}
		ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
			ui.Column(c).Gap(4).Children(func() {
				label := msgs.LookedUpEarlier()
				if m.Expanded {
					label = "– " + label
				} else {
					label = "+ " + label
				}
				ui.Row(c).Children(func() {
					secondaryButton(c, label).OnClick(func() { m.Expanded = !m.Expanded })
				})
				if m.Expanded {
					for _, step := range m.Steps {
						ui.Text(c, step).FontSize(typeAction).LineHeight(1.4).TextColor(t.TextMuted)
					}
					if len(m.Sources) > 0 {
						ui.Row(c).Children(func() {
							button := secondaryButton(c, msgs.OpenSources()).OnClick(func() { a.openSources(m.ID) })
							if a.focusSources && a.auxMessage == m.ID {
								button.Focus()
								a.focusSources = false
							}
						})
					}
				}
			})
		})
	case msgUser:
		ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
			ui.Column(c).MaxWidthPercent(80).MinWidth(0).Shrink(1).Gap(2).Children(func() {
				if m.Key != "" && m.Key == a.highlight {
					ui.Text(c, msgs.LocatedInHistory()).FontSize(14).TextColor(t.TextMuted)
				}
				if m.Text != "" {
					opts := messageReaderOptions(c, true)
					opts.MessageBubble = &markdownMessageBubble{Background: a.workTheme.BubbleUser, PaddingX: 12, PaddingY: 8, Radius: 12, ContextualCopy: true}
					markdownReader(c, m.Text, opts).FontSize(typeBody).LineHeight(1.5).TextColor(t.Text)
				}
				taskContextReadOnlyView(c, a.workTheme, m.Context)
			})
		})
	default:
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Start).Children(func() {
			a.avatarTile(c, t, 28)
			ui.Column(c).MaxWidthPercent(80).MinWidth(0).Shrink(1).Children(func() {
				opts := a.readerOptions(markdownReaderOptions{Streaming: m.Streaming, CopyLabel: locales.In(c.Locale()).CopyMessageLabel(), Theme: messageMarkdownTheme}, messageLinkScope(ct.ID, m))
				opts.MessageBubble = &markdownMessageBubble{
					Background: a.workTheme.BubbleContact, PaddingX: 12, PaddingY: 8, Radius: 12,
					Header: func(hc *ui.Context) {
						if m.Key != "" && m.Key == a.highlight {
							ui.Text(hc, msgs.LocatedInHistory()).FontSize(14).TextColor(t.TextMuted)
						}
					},
					Footer: func(fc *ui.Context) {
						if m.AlertTitle != "" {
							a.alert(fc, t, m)
						}
						if len(m.Sources) > 0 {
							ui.Row(fc).Children(func() {
								button := secondaryButton(fc, msgs.EarlierMessagesCount(len(m.Sources))).OnClick(func() { a.openSources(m.ID) })
								if a.focusSources && a.auxMessage == m.ID {
									button.Focus()
									a.focusSources = false
								}
							})
						}
					},
				}
				markdownReader(c, m.Text, opts).FontSize(typeBody).LineHeight(1.5)
			})
		})
	}
}

func (a *appModel) alert(c *ui.Context, t *ui.Theme, m *message) {
	msgs := locales.In(c.Locale())
	ui.Column(c).Padding(8).Radius(10).Background(t.SurfaceHover).Gap(4).Children(func() {
		ui.Text(c, m.AlertTitle).FontSize(typeAction).FontWeight(500).TextColor(t.Warning)
		if m.AlertDetail != "" {
			ui.Text(c, m.AlertDetail).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
		}
		if m.Retry && !m.RetryDone {
			ui.Row(c).Children(func() {
				secondaryButton(c, msgs.TryAgain()).OnClick(func() { a.retry(m) })
			})
		} else if m.RetryDone {
			ui.Text(c, msgs.Retried()).FontSize(14).TextColor(t.TextMuted)
		}
	})
}

func (a *appModel) emptyState(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	msgs := locales.In(c.Locale())
	ui.Column(c).Padding(8, 0).Gap(8).Children(func() {
		if !a.demo && ct.SessionID != "" && !ct.Loaded {
			ui.Text(c, msgs.HistoryNotLoadedTitle()).FontSize(16).FontWeight(500).TextColor(t.Text)
			ui.Text(c, msgs.HistoryNotLoadedHint()).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
			if a.runtimeStatus != runtimeStateReady {
				ui.Text(c, msgs.HistoryRuntimeNotReady()).FontSize(14).LineHeight(1.5).TextColor(t.Warning)
			}
			return
		}
		notice := a.issueWorkNotice(ct)
		fact := ""
		switch {
		case notice.Attention:
			fact = msgs.EmptyTaskAttention(notice.IssueName)
		case notice.Waiting:
			fact = msgs.EmptyTaskWaiting(notice.IssueName)
		case notice.Active:
			fact = msgs.EmptyTaskInProgress(notice.IssueName)
		}
		if fact != "" {
			ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Text(c, fact).FontSize(typeBody).LineHeight(1.5).TextColor(t.Text).Grow(1)
				secondaryButton(c, msgs.TaskView()).OnClick(func() { a.openIssueTarget(notice.IssueID) })
			})
			return
		}
		ui.Text(c, msgs.EmptyNoMessages()).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text)
		hint := msgs.EmptyConversationStart(ct.Name)
		if a.demo {
			hint = msgs.EmptyConversationDraft(ct.Name)
		}
		ui.Text(c, hint).FontSize(typeBody).LineHeight(1.5).TextColor(t.TextMuted)
	})
}

func knownConnectionFailure(text string) bool {
	// runtimeMessage prefixes typed errors as "code: message". Match the code
	// exactly rather than treating an arbitrary diagnostic containing the word
	// as a known connection failure.
	return strings.HasPrefix(strings.TrimSpace(text), "connection_mismatch:")
}

func (a *appModel) connectionSendFailureKnown(ct *contact) bool {
	if ct == nil {
		return false
	}
	return knownConnectionFailure(ct.SendError)
}

func (a *appModel) connectionErrorBanner(c *ui.Context, t *ui.Theme, ct *contact) {
	if ct == nil {
		return
	}
	msgs := locales.In(c.Locale())
	known := knownConnectionFailure(ct.LoadErr) || knownConnectionFailure(ct.SendError)
	if !known {
		return
	}
	details := strings.TrimSpace(ct.LoadErr)
	if details == "" || details == ct.SendError {
		details = strings.TrimSpace(ct.SendError)
	} else if ct.SendError != "" {
		details = details + "\n\n" + strings.TrimSpace(ct.SendError)
	}
	expanded := a.connectionDetailsOpen != nil && a.connectionDetailsOpen[ct.ID]
	ui.Column(c).Padding(8, 16).Gap(3).Background(t.SurfaceHover).Children(func() {
		ui.Text(c, msgs.ConnectionMismatchSummary()).FontSize(14).LineHeight(1.5).TextColor(t.Warning).MaxLines(3)
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			entry := secondaryButton(c, msgs.ConnectionSettingsAction()).TextColor(t.Text)
			entry.OnClick(func() { a.openSettingsTab(settingsTabConnection, settingsSourceConnection) })
			if a.focusConnectionEntry {
				entry.Focus()
				a.focusConnectionEntry = false
			}
			label := msgs.DetailsAction()
			if expanded {
				label = msgs.HideDetailsAction()
			}
			secondaryButton(c, label).OnClick(func() {
				if a.connectionDetailsOpen == nil {
					a.connectionDetailsOpen = map[string]bool{}
				}
				a.connectionDetailsOpen[ct.ID] = !a.connectionDetailsOpen[ct.ID]
			})
		})
		if expanded && details != "" {
			markdownReader(c, details, markdownReaderOptions{PlainText: true, CopyLabel: locales.In(c.Locale()).CopyErrorDetailsLabel()}).
				FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
		}
	})
}

// ---------- composer ----------

func (a *appModel) composerInlineAux() bool {
	return a.aux == composerPanelContext && a.composerPanel == composerPanelContext
}

func (a *appModel) composerModelButton(c *ui.Context, t *ui.Theme, ct *contact) ui.Element {
	msgs := locales.In(c.Locale())
	summary := a.conversationModelSummary(msgs, ct)
	button := appTooltip(c, ui.Button(c, "").Label(msgs.ConversationModelLabel()).
		MinHeight(32).Height(32).MinWidth(0).MaxWidth(230).Shrink(1).Padding(4, 8),
		msgs.ConversationModelTooltip(summary))
	head, tail := a.conversationModelDisplayParts(msgs, ct)
	button.Children(func() {
		ui.Row(c).Grow(1).MinWidth(0).AlignItems(ui.Center).Gap(3).Children(func() {
			ui.Text(c, msgs.ConversationBadge()).FontSize(typeMeta).LineHeight(1.3).
				MaxLines(1).Shrink(0).TextColor(t.TextMuted)
			ui.Row(c).Grow(1).MinWidth(0).AlignItems(ui.Center).Gap(0).Children(func() {
				if head != "" {
					ui.Text(c, head).FontSize(typeAction).FontWeight(500).
						Grow(1).Shrink(1).MinWidth(0).MaxLines(1).Ellipsis("…").TextColor(t.Text)
				}
				if tail != "" {
					ui.Text(c, tail).FontSize(typeAction).FontWeight(500).
						MaxLines(1).Shrink(0).TextColor(t.Text)
				}
			})
		})
	})
	return button
}

var modelSelectedIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="m5 12 4 4L19 6"/></svg>`))

func (a *appModel) composerModelPopover(c *ui.Context, t *ui.Theme, anchor ui.Element) {
	msgs := locales.In(c.Locale())
	panel := ui.PopoverBase(c, anchor, &a.composerModelOpen, func(panel ui.Element) {
		panel.Label(msgs.ConversationModelLabel()).Role(ui.RoleMenu).FocusGroup(ui.Vertical).
			Width(320).MaxHeight(360).Padding(6).Gap(4).Margin(4, 0, 0, 0).
			Radius(t.Radius+2).Background(t.Background).Border(1, t.Border).
			Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, 0.18))
		if panel.OverlayShortcut(0, ui.KeyEscape) {
			a.closeComposerModel()
			a.composerFocusModel = true
		}
		leaveOnTab := func(ev ui.InputEvent) bool {
			if ev.Kind == ui.InputKeyDown && ev.Key == ui.KeyTab {
				a.closeComposerModel()
			}
			return false
		}
		ui.Scroll(c).FillWidth().MaxHeight(280).Children(func() {
			ui.Column(c).FillWidth().Gap(4).Children(func() {
				form := a.modelForm
				if form == nil {
					text := modelPanelMessageText(msgs, a.modelPanelMessage)
					color := t.TextMuted
					if a.modelPanelError != "" {
						text = modelPanelErrorText(msgs, a.modelPanelError, a.modelPanelErrorDetail)
						color = t.Danger
					}
					ui.Text(c, text).FontSize(typeAction).LineHeight(1.5).TextColor(color)
				} else {
					current := form.Effective.Config.EffectiveConfig.Model
					declared := false
					for _, entry := range form.Catalog.ConfiguredModels {
						declared = declared || entry.Model == current
					}
					if !declared || form.Unconfirmed {
						label := msgs.ModelEffectiveModel(current)
						if form.Unconfirmed {
							label = msgs.ModelLastReadModel(current)
						}
						ui.Text(c, label).FontSize(typeAction).LineHeight(1.5).TextColor(t.TextMuted)
					}
					if form.Status == modelStatusCatalogUnavailable || form.Status == modelStatusRefreshedNoCatalog {
						ui.Text(c, msgs.ModelCatalogNotLoaded()).FontSize(typeAction).LineHeight(1.5).TextColor(t.Warning)
					} else if len(form.Catalog.ConfiguredModels) == 0 {
						ui.Text(c, msgs.ModelNoConfigured()).FontSize(typeAction).LineHeight(1.5).TextColor(t.TextMuted)
					}
					ui.Column(c).FillWidth().Children(func() {
						ui.Column(c).FillWidth().Gap(2).Children(func() {
							for index, entry := range form.Catalog.ConfiguredModels {
								selected := !form.Unconfirmed && entry.Model == current
								row := appTooltip(c, ui.ButtonBase(c).Radius(4).TextColor(t.Text).Label(entry.Model).Role(ui.RoleMenuItemRadio).Checked(selected).
									FillWidth().MinHeight(32).Padding(6, 8).HandleInput(leaveOnTab), entry.Model)
								if selected || row.Hovered() || row.FocusVisible() {
									row.Background(t.SurfaceHover)
								}
								if row.Pressed() {
									row.Background(t.SurfacePressed)
								}
								row.Children(func() {
									ui.Row(c).FillWidth().MinWidth(0).Gap(6).AlignItems(ui.Center).Children(func() {
										ui.Text(c, entry.Model).FontSize(typeAction).Grow(1).MinWidth(0).MaxLines(1).Ellipsis("…")
										if selected {
											ui.Icon(c, modelSelectedIcon).Size(16, 16).Shrink(0)
										}
									})
								})
								row.Disabled(form.Busy || form.Unconfirmed || a.modelOpActive).
									OnClick(func() { a.chooseComposerModel(index) })
								if a.composerModelFocus && !form.Busy && !form.Unconfirmed {
									row.Focus()
									a.composerModelFocus = false
								}
							}
						})
					})
					if form.Error != "" {
						ui.Text(c, modelErrorText(msgs, form.Error, form.ErrorDetail)).FontSize(typeAction).LineHeight(1.5).TextColor(t.Danger)
					}
					if form.UnconfirmedKind != "" {
						ui.Text(c, modelUnconfirmedText(msgs, form.UnconfirmedKind, form.UnconfirmedDetail)).FontSize(typeAction).LineHeight(1.5).TextColor(t.Warning)
					}
					if form.Busy {
						ui.Text(c, modelStatusText(msgs, form.Status)).FontSize(typeMeta).TextColor(t.TextMuted)
					}
				}
			})
		})
		ui.Divider(c)
		manage := ui.ButtonBase(c).MinHeight(32).Padding(6, 8).Radius(4).TextColor(t.Text).Label(msgs.ModelManage()).FillWidth().Justify(ui.Start).HandleInput(leaveOnTab).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Icon(c, railSettingsIcon).Size(16, 16)
				ui.Text(c, msgs.ModelManage()).FontSize(typeAction)
			})
		}).OnClick(func() {
			a.openSettingsTab(settingsTabConnection, settingsSourceModel)
		})
		if manage.Hovered() || manage.FocusVisible() {
			manage.Background(t.SurfaceHover)
		}
		if manage.Pressed() {
			manage.Background(t.SurfacePressed)
		}
		if a.composerModelFocus {
			manage.Focus()
			a.composerModelFocus = false
		}
	})
	if panel.Dismissed() {
		a.closeComposerModel()
	}
}

func (a *appModel) composerInlinePanel(c *ui.Context, t *ui.Theme) {
	if !a.composerInlineAux() {
		return
	}
	msgs := locales.In(c.Locale())
	c.OnShortcut(0, ui.KeyEscape, func() { a.closeAux() })
	ui.Column(c).FillWidth().Children(func() {
		ui.Scroll(c).Label(msgs.TaskContextDetailsScroll()).MaxHeight(300).TrackScroll(a.auxScrollFor(a.aux)).Children(func() {
			ui.Column(c).Padding(8, 10).Radius(10).Background(t.Surface).Gap(8).Children(func() {
				a.taskContextPanel(c, t)
			})
		})
	})
}

func contactNoticeText(msgs locales.Messages, kind, detail string) string {
	switch kind {
	case "send-protocol":
		return msgs.SendNoticeProtocol()
	case "send-delivery":
		return msgs.SendNoticeDelivery()
	case "send-cancelled":
		return msgs.SendNoticeCancelled()
	case "send-previous-incomplete", "status-previous-incomplete":
		return msgs.SendNoticePreviousIncomplete()
	case "status-history-reloaded":
		return msgs.SendNoticeHistoryReloaded()
	case "send-uncertain":
		return msgs.SendNoticeUncertain()
	case "send-issue-association":
		return msgs.SendNoticeIssueAssociation()
	case "send-draft-not-cleared":
		return msgs.SendNoticeDraftNotCleared()
	case "send-delivery-unknown":
		return msgs.SendNoticeDeliveryUnknown(detail)
	case "send-contact-save":
		return msgs.SendNoticeContactSave(detail)
	case "send-cancel-failed":
		return msgs.IssueSendCancelFailed(detail)
	case "send-cancelling":
		return msgs.IssueSendCancelling()
	case "send-cancel-already":
		return msgs.IssueSendCancelAlready()
	case "send-already-finished":
		return msgs.IssueSendAlreadyFinished()
	case "send-reload-disconnected":
		return msgs.IssueSendReloadDisconnected()
	case "supplement-receipt-failed":
		return msgs.IssueSendReceiptFailed()
	case "supplement-receipt-save-failed":
		return msgs.IssueSendReceiptSaveFailed(detail)
	case "supplement-association":
		return msgs.IssueSendAssociation()
	case "waiting-runtime":
		return msgs.IssueSendWaitingRuntime(detail)
	case "supplement-delivery":
		return msgs.IssueSendSupplementDelivery(detail)
	case "auto-stop-memory":
		return msgs.IssueSendAutoStopMemory()
	case "release-tool-failed":
		return msgs.IssueSendReleaseToolFailed(detail)
	case "supplement-history-receipt-failed":
		return msgs.IssueSendHistoryReceiptFailed(detail)
	case "answer-stopping":
		return msgs.IssueSendAnswerStopping()
	case "send-accepted-cancel":
		return msgs.IssueSendAcceptedCancel()
	case "status-connection-lost":
		return msgs.SendNoticeConnectionLost()
	default:
		if text := sendBlockText(msgs, kind); text != kind {
			return text
		}
		return kind
	}
}

func (a *appModel) composer(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	msgs := locales.In(c.Locale())
	ui.Column(c).FillWidth().Padding(8, 12).Gap(4).Children(func() {
		ui.Column(c).FillWidth().Padding(8, 12).Radius(12).Background(a.workTheme.Well).Gap(4).Children(func() {
			input := ui.TextArea(c, &ct.Draft).Bind(&a.composerHandle).Label(msgs.ComposerLabel(ct.Name)).
				FontSize(typeBody).LineHeight(1.5).Lines(2, 6).MinHeight(0).
				Background(ui.Transparent).BorderWidth(0).Padding(2).
				Placeholder(msgs.ComposerPlaceholder(ct.Name)).FillWidth().OnChange(func() {
				a.draftChanged(ct)
			}).OnTextKey(func(k ui.TextKey) bool {
				if k.Key != ui.KeyEnter || k.Mods&ui.Shift != 0 {
					return false
				}
				text := strings.TrimSpace(k.Text())
				if text == "" {
					return true
				}
				if a.send(ct, text) && a.demo {
					k.SetText("")
				}
				return true
			})
			if a.focusComposer {
				input.Focus()
				a.focusComposer = false
			}
			contextCount := ct.Context.count()
			contextLabel := msgs.TaskContextCount(contextCount)
			sendDisabled := strings.TrimSpace(ct.Draft) == "" || ct.Status == statusWorking
			sendReason := ""
			if !a.demo {
				sendReason = a.canSend(ct)
				sendDisabled = strings.TrimSpace(ct.Draft) == "" || sendReason != ""
			}
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(4).Children(func() {
				ui.Toolbar(c, func() {
					contextButton := appTooltip(c, ui.Button(c, "").Label(contextLabel), contextLabel).
						MinWidth(32).Height(32).Padding(2, 6)
					contextButton.Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(2).Children(func() {
							ui.Icon(c, composerAttachIcon).Width(16).Height(16).TextColor(t.TextMuted)
							if contextCount > 0 {
								ui.Text(c, fmt.Sprint(contextCount)).FontSize(typeMeta).LineHeight(1.3).TextColor(t.TextMuted)
							}
						})
					})
					contextButton.OnClick(func() { a.openComposerTaskContext() })
					if a.composerFocusContext {
						contextButton.Focus()
						a.composerFocusContext = false
					}
				}).Label(msgs.ComposerTools()).Shrink(0).Padding(0)
				ui.Spacer(c)
				modelButton := a.composerModelButton(c, t, ct)
				modelButton.OnClick(func() { a.openComposerModel() })
				if modelButton.Shortcut(0, ui.KeyDown) {
					a.openComposerModel()
				}
				a.composerModelPopover(c, t, modelButton)
				if a.composerFocusModel {
					modelButton.Focus()
					a.composerFocusModel = false
				}
				// Send/Stop are outside the Toolbar: the main action must never
				// disappear into a toolbar overflow menu at narrow widths.
				if !a.demo && ct.ActiveTaskID != "" {
					stop := appTooltip(c, secondaryButton(c, "").Label(msgs.StopConversation()).FontWeight(500), msgs.StopConversationTooltip()).
						Shrink(0).Height(32).Padding(4, 8).TextColor(t.Warning)
					stop.Children(func() {
						ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
							ui.Icon(c, composerStopIcon).Width(16).Height(16)
							ui.Text(c, msgs.StopConversation()).FontSize(typeAction).FontWeight(500).LineHeight(1.3).TextColor(t.Warning)
						})
					})
					stop.OnClick(func() { a.cancelSelected() })
				} else {
					send := appTooltip(c, ui.PrimaryButton(c, "").Label(msgs.SendAction()), msgs.SendTooltip()).
						Width(32).Height(32).Padding(0).Disabled(sendDisabled)
					send.Children(func() {
						ui.Icon(c, composerSendIcon).Width(16).Height(16)
					})
					if send.Clicked() {
						a.sendCurrent()
					}
				}
			})
			if !a.demo {
				if a.rt != nil && a.runtimeStatus == runtimeStateReady && !a.capacityKnown() {
					ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
						message := a.runtimeCapabilitiesErr
						if message == "" {
							message = msgs.CapacityUnknown()
						}
						ui.Text(c, message).FontSize(14).LineHeight(1.5).TextColor(t.Warning).Grow(1)
						secondaryButton(c, msgs.RetryCapacity()).OnClick(func() { a.retryCapabilities() })
					})
				}
				if sendReason != "" && strings.TrimSpace(ct.Draft) != "" {
					ui.Text(c, sendBlockText(msgs, sendReason)).FontSize(typeBody).LineHeight(1.5).TextColor(t.TextMuted)
				}
				if sendReason != "" && strings.TrimSpace(ct.Draft) == "" && (!a.runtimeAvailableForView() || a.connectionViewState() == connectionViewApplying) {
					msgs := locales.In(c.Locale())
					if a.shutdownStarted || ct.Archived {
						ui.Text(c, sendBlockText(msgs, sendReason)).FontSize(typeBody).LineHeight(1.5).TextColor(t.TextMuted)
					} else {
						hint := msgs.ConnectionRecoverySetupHint()
						switch a.connectionViewState() {
						case connectionViewSaved:
							hint = msgs.ConnectionRecoverySavedHint()
						case connectionViewNotConnected:
							hint = msgs.ConnectionRecoveryNotConnectedHint()
							if a.runtimeStage == "Connection configuration" && (a.connectionEffective != nil || a.connectionSaved != nil || a.connectionManaged) {
								hint = msgs.ConnectionRecoveryManagedHint()
							}
						case connectionViewApplying:
							hint = msgs.ConnectionRecoveryApplyingHint()
						case connectionViewConnecting:
							hint = msgs.ConnectionRecoveryConnectingHint()
						}
						ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
							ui.Text(c, hint).FontSize(typeBody).LineHeight(1.5).TextColor(t.TextMuted).Grow(1)
							if a.connectionConfigurationBlocked() {
								secondaryButton(c, msgs.ConnectionRecoveryOpenSettings()).Label(msgs.ConnectionRecoveryOpenSettings()).OnClick(func() { a.openConfiguration() })
							}
						})
					}
				}
				switch {
				case ct.SendState == sendSubmitting:
					ui.Text(c, msgs.SendingState()).FontSize(typeMeta).LineHeight(1.4).TextColor(t.TextMuted)
				case ct.SendState == sendAccepted && ct.Status != statusWorking:
					ui.Text(c, msgs.SendAcceptedState()).FontSize(typeMeta).LineHeight(1.4).TextColor(t.TextMuted)
				case ct.SendState == sendUnknown:
					if a.runtimeStatus == runtimeStateReady && a.rt != nil {
						ui.Text(c, msgs.SendUnknownState()).FontSize(typeMeta).LineHeight(1.4).TextColor(t.Danger)
					} else {
						ui.Text(c, msgs.SendDisconnectedState()).FontSize(typeMeta).LineHeight(1.4).TextColor(t.Danger)
					}
				}
				if ct.SendError != "" && !a.connectionSendFailureKnown(ct) {
					ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
						sendText := ct.SendError
						if ct.SendErrorKind != "" {
							sendText = contactNoticeText(locales.In(c.Locale()), ct.SendErrorKind, ct.SendErrorDetail)
						}
						ui.Text(c, sendText).FontSize(typeBody).LineHeight(1.5).TextColor(t.Warning)
						if (ct.SendState == sendUnknown || ct.SendError != "") && a.runtimeStatus == runtimeStateReady && a.rt != nil {
							if secondaryButton(c, msgs.ReloadAction()).Clicked() {
								a.reloadSelected()
							}
						}
					})
				}
				if ct.StatusDetail != "" {
					statusText := ct.StatusDetail
					if ct.StatusDetailKind != "" {
						statusText = contactNoticeText(locales.In(c.Locale()), ct.StatusDetailKind, "")
					}
					ui.Text(c, statusText).FontSize(typeBody).LineHeight(1.5).TextColor(t.Warning)
				}
			}
		})
		if a.composerInlineAux() {
			a.composerInlinePanel(c, t)
		}
	})
}

// ---------- history ----------

func (a *appModel) historyDetail(c *ui.Context, t *ui.Theme, narrow bool) {
	ct := a.selectedContact()
	if ct == nil {
		return
	}
	msgs := locales.In(c.Locale())
	ui.Column(c).FillHeight().Padding(16).Gap(8).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			if narrow {
				back := detailBackButton(c, t, msgs.CloseHistory(), msgs.BackFromHistory()).OnClick(func() { a.closeDetail() })
				a.focusDetailControl(c, back)
				ui.Text(c, msgs.HistoryTitle(ct.Name)).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text).MaxLines(2).Grow(1)
				return
			}
			ui.Text(c, msgs.HistoryTitle(ct.Name)).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text).MaxLines(2).Grow(1)
			close := appTooltip(c, secondaryButton(c, msgs.CloseHistory()), msgs.BackFromHistory()).OnClick(func() { a.closeDetail() })
			a.focusDetailControl(c, close)
		})
		ui.Scroll(c).Grow(1).TrackScroll(&a.historyScroll).Children(func() {
			ui.Column(c).Gap(8).Children(func() {
				ui.Text(c, msgs.HistorySearchScopeHint(ct.Name)).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
				if !a.demo && ct.SessionID != "" && !ct.Loaded {
					ui.Text(c, msgs.HistoryNotLoadedShort()).FontSize(14).LineHeight(1.5).TextColor(t.Warning)
				}
				ui.TextInput(c, &a.historyQuery).FontSize(14).Placeholder(msgs.HistorySearchPlaceholder(ct.Name))
				if !a.demo {
					secondaryButton(c, msgs.SearchSavedContexts()).TextColor(t.Text).OnClick(func() { a.searchContactHistoryAsync(a.historyQuery) })
					if a.contactHistoryBusy {
						ui.Text(c, msgs.SearchingSavedContexts()).FontSize(14).TextColor(t.TextMuted)
					}
					if a.contactHistoryError != "" {
						ui.Text(c, contactHistoryNoticeText(msgs, a.contactHistoryError, a.contactHistoryErrorDetail)).FontSize(14).LineHeight(1.5).TextColor(t.Warning)
					}
					if a.contactHistory != nil {
						ui.Text(c, msgs.HistorySummary(len(a.contactHistory.Matches), boolIndex(a.contactHistory.Limited), len(a.contactHistory.Failures))).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
						if len(a.contactHistory.Failures) > 0 {
							for _, failure := range a.contactHistory.Failures {
								ui.Text(c, msgs.SavedContextUnavailable(failure.SessionID)).FontSize(14).LineHeight(1.5).TextColor(t.Warning)
							}
						}
						for _, match := range a.contactHistory.Matches {
							match := match
							key := historyOriginKey(historyOriginSaved, match.SessionID, match.EntryID)
							ui.Column(c).FillWidth().MinWidth(0).Padding(8).Radius(8).Background(t.SurfaceHover).Gap(4).Children(func() {
								ui.Text(c, msgs.SavedContextMatch(match.Date, match.SessionID)).FontSize(typeMeta).LineHeight(1.4).TextColor(t.Success)
								ui.Text(c, match.Preview).FontSize(14).LineHeight(1.5).TextColor(t.Text)
								button := secondaryButton(c, msgs.ViewFullMessage()).OnClick(func() { a.openContactHistoryMatch(match) })
								if a.focusHistoryFullID == key {
									button.Focus()
									a.focusHistoryFullID = ""
								}
							})
						}
					}
					if a.contactHistoryDetailBusy {
						ui.Text(c, msgs.OpeningFullSavedMessage()).FontSize(14).TextColor(t.TextMuted)
					}
				}
				matches := a.historyMatches()
				if len(matches) == 0 {
					if strings.TrimSpace(a.historyQuery) == "" {
						ui.Text(c, msgs.NoSavedMessages(ct.Name)).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
					} else {
						ui.Text(c, msgs.NoMatchingMessages(ct.Name, a.historyQuery)).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
					}
				}
				for _, e := range matches {
					e := e
					ui.Column(c).FillWidth().MinWidth(0).Padding(8).Radius(8).Background(t.SurfaceHover).Gap(4).Children(func() {
						ui.Text(c, e.Date+". "+speakerLabel(msgs, ct, e)).FontSize(typeMeta).LineHeight(1.4).TextColor(t.Success)
						if len([]rune(e.Text)) <= 180 {
							if e.From == "me" {
								markdownReader(c, e.Text, markdownReaderOptions{PlainText: true, CopyLabel: msgs.CopyHistoryMessageLabel(), Theme: messageMarkdownTheme}).FontSize(typeBody).LineHeight(1.5).TextColor(t.Text)
							} else {
								markdownReader(c, e.Text, a.readerOptions(markdownReaderOptions{CopyLabel: msgs.CopyHistoryMessageLabel(), Theme: messageMarkdownTheme}, historyLinkScope(ct.ID, "history", e.ID))).FontSize(typeBody).LineHeight(1.5)
							}
						} else {
							ui.Text(c, truncate(e.Text, 150)).FontSize(14).LineHeight(1.5).MaxLines(3).TextColor(t.Text)
						}
						if len(e.Context) > 0 {
							ui.Text(c, msgs.ContextCount(len(e.Context))).FontSize(typeAction).TextColor(t.TextMuted)
						}
						ui.Row(c).Gap(4).Wrap().Children(func() {
							read := secondaryButton(c, msgs.ReadFullMessage()).OnClick(func() { a.openHistoryEntryFull(e) })
							if a.focusHistoryFullID == historyOriginKey(historyOriginLoaded, ct.SessionID, e.ID) {
								read.Focus()
								a.focusHistoryFullID = ""
							}
							secondaryButton(c, msgs.ViewInConversation()).OnClick(func() { a.viewInConversation(e.ID) })
						})
					})
				}
			})
		})
	})
}

func boolIndex(value bool) int {
	if value {
		return 1
	}
	return 0
}

func speakerLabel(msgs locales.Messages, c *contact, e historyEntry) string {
	if e.From == "me" {
		return msgs.SpeakerYou()
	}
	return c.Name
}

// ---------- main pages ----------

func (a *appModel) sourcesPanel(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	msgs := locales.In(c.Locale())
	entries := a.sourceEntries(a.auxMessage)
	ui.Text(c, msgs.SourcesSummary(len(entries), ct.Name)).FontSize(typeMeta).LineHeight(1.4).TextColor(t.TextMuted)
	for _, e := range entries {
		e := e
		ui.Column(c).FillWidth().MinWidth(0).Padding(8).Radius(8).Background(t.SurfaceHover).Gap(4).Children(func() {
			ui.Text(c, e.Date+". "+speakerLabel(msgs, ct, e)).FontSize(typeMeta).LineHeight(1.4).TextColor(t.Success)
			if e.From == "me" {
				markdownReader(c, e.Text, markdownReaderOptions{PlainText: true, CopyLabel: msgs.CopySourceMessageLabel(), Theme: messageMarkdownTheme}).FontSize(typeBody).LineHeight(1.5).TextColor(t.Text)
			} else {
				markdownReader(c, e.Text, a.readerOptions(markdownReaderOptions{CopyLabel: msgs.CopySourceMessageLabel(), Theme: messageMarkdownTheme}, historyLinkScope(ct.ID, "sources", e.ID))).FontSize(typeBody).LineHeight(1.5)
			}
			ui.Row(c).Children(func() {
				secondaryButton(c, msgs.ViewInConversation()).OnClick(func() { a.viewInConversation(e.ID) })
			})
		})
	}
}

func (a *appModel) openConfiguration() {
	a.ensureConnectionForm()
	a.openSettingsTab(settingsTabConnection, settingsSourceConnection)
}

func (a *appModel) runtimePanel(c *ui.Context, t *ui.Theme, ct *contact) {
	msgs := locales.In(c.Locale())
	localStore := a.runtimeStage == startupStageLocalStore
	if localStore {
		ui.Text(c, msgs.RuntimeLocalStoreUnavailable()).FontSize(14).LineHeight(1.5).TextColor(t.Danger)
		ui.Text(c, msgs.RuntimeNotStartedLocalStore()).FontSize(14).LineHeight(1.5).TextColor(t.Text)
	} else {
		status := msgs.RuntimeStatusLine(a.runtimeStatusText(msgs))
		ui.Text(c, status).FontSize(14).LineHeight(1.5).TextColor(t.Text)
	}
	if a.hasRuntimeError() {
		ui.Text(c, msgs.RuntimeStageLabel(a.runtimeStageDisplay(msgs), a.runtimeErrorDisplay(msgs))).FontSize(14).LineHeight(1.5).TextColor(t.Danger)
	}
	if a.runtimeHintDisplay(msgs) != "" {
		ui.Text(c, a.runtimeHintDisplay(msgs)).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
	}
	helper := a.runtimeBinary
	if helper == "" {
		switch {
		case localStore:
			helper = msgs.RuntimeHelperNotStartedLocalStore()
		case a.runtimeHelperChecked || a.runtimeStage == "Runtime helper":
			helper = msgs.RuntimeHelperNotFound()
		case a.connectionForm != nil && a.connectionForm.HasSaved:
			helper = msgs.RuntimeHelperNotApplied()
		default:
			helper = msgs.RuntimeHelperNotChecked()
		}
	}
	ui.Text(c, msgs.RuntimeHelperLine(helper)).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
	ui.Text(c, msgs.RuntimeStoreLine(valueOr(a.storePath, msgs.RuntimeValueNotConfigured()))).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
	ui.Text(c, msgs.RuntimeSessionDirLine(valueOr(a.sessionDir, msgs.RuntimeValueNotConfigured()))).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
	ui.Text(c, msgs.RuntimeWorkDirLine(valueOr(a.workDir, msgs.RuntimeValueNotConfigured()))).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
	workspace := ct.SessionWorkingDir
	if workspace == "" {
		workspace = a.effectiveContactWorkspace(ct)
	}
	ui.Text(c, msgs.RuntimeContactWorkspaceLine(valueOr(workspace, msgs.RuntimeValueNotConfigured()))).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
	session := ct.SessionID
	if session == "" {
		session = msgs.RuntimeSessionNotCreated()
	}
	state := ct.SessionState
	if state == "" {
		state = msgs.RuntimeValueUnknown()
	}
	ui.Text(c, msgs.RuntimeSessionLine(session, state)).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
	switch {
	case ct.SessionAPI != "":
		ui.Text(c, msgs.RuntimeSessionAPILine(ct.SessionAPI)).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
	case a.runtimeAPI != "":
		ui.Text(c, msgs.RuntimeAPIConnectedLine(a.runtimeAPI)).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
	default:
		ui.Text(c, msgs.RuntimeSessionAPIUnknown()).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
	}
	ui.Text(c, msgs.RuntimeSessionModelLine(valueOr(ct.SessionModel, msgs.RuntimeSessionModelUnknown()))).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
	if a.connectionManaged || (a.connectionForm != nil && a.connectionForm.HasSaved) {
		ui.Text(c, msgs.RuntimeConnectionManagedNote()).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
	} else {
		ui.Text(c, msgs.RuntimeHostConfigNote()).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
	}
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func toolOutputSummary(msgs locales.Messages, output string) string {
	if strings.TrimSpace(output) == "" {
		return ""
	}
	var value map[string]any
	if json.Unmarshal([]byte(output), &value) != nil {
		return truncate(output, 300)
	}
	body := ""
	for _, key := range []string{"content", "stdout", "stderr", "output"} {
		if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
			body = truncate(text, 240)
			break
		}
	}
	prefix := ""
	if text, ok := value["error"].(string); ok && strings.TrimSpace(text) != "" {
		prefix = msgs.ToolOutputFailed(truncate(text, 120))
	}
	code, hasCode := value["exit_code"].(float64)
	timedOut, _ := value["timed_out"].(bool)
	switch {
	case timedOut && hasCode && code != 0:
		prefix = msgs.ToolOutputTimedOutExit(fmt.Sprintf("%g", code))
	case timedOut:
		prefix = msgs.ToolOutputTimedOut()
	case hasCode && code != 0:
		if prefix == "" {
			prefix = msgs.ToolOutputFailedExit(fmt.Sprintf("%g", code))
		} else {
			prefix += " (" + fmt.Sprintf("%g", code) + ")"
		}
	}
	if prefix != "" {
		if body != "" {
			return prefix + ": " + body
		}
		return prefix
	}
	if body != "" {
		return body
	}
	// Structured tool results such as write/edit receipts have no readable
	// assistant body. Keep the exact record in Details/Copy, but do not present
	// raw JSON as a result preview.
	return msgs.ToolOutputStructured()
}

func truncate(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}
