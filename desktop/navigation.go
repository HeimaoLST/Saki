package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"saki/desktop/locales"
)

type detailKind string

const (
	detailWork    detailKind = "work"
	detailHistory detailKind = "history"
	detailProfile detailKind = "profile"
)

// historyViewMemory keeps the per-contact history surface together with its
// scroll position. The Runtime callback still owns the live result; this only
// remembers what the user was looking at when another contact is selected.
type historyViewMemory struct {
	Query       string
	Scroll      ui.ScrollState
	Result      *contactHistoryResult
	Error       string
	ErrorDetail string
}

var (
	navWorkIcon     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="7" width="18" height="13" rx="2"/><path d="M8 7V5a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M3 12h18M10 12v3h4v-3"/></svg>`))
	navHistoryIcon  = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5M12 7v5l3 2"/></svg>`))
	navProfileIcon  = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/></svg>`))
	navBackIcon     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="m15 18-6-6 6-6"/></svg>`))
	navContactsIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="8" r="3"/><path d="M3 20a6 6 0 0 1 12 0M17 8h4M19 6v4M17 20h4"/></svg>`))
	navCloseIcon    = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>`))
)

func (a *appModel) activeDetail() detailKind {
	if !a.workspaceOpen {
		return ""
	}
	if a.detail == "" {
		return detailWork
	}
	return a.detail
}

func (a *appModel) detailIs(kind detailKind) bool {
	return a.workspaceOpen && a.activeDetail() == kind
}

func (a *appModel) openDetail(kind detailKind) {
	wasOpen := a.workspaceOpen
	if wasOpen && a.activeDetail() == kind {
		a.focusDetailClose = true
		return
	}
	if a.detailIs(detailHistory) && kind != detailHistory {
		a.cancelHistoryFullDisplay()
	}
	a.workspaceOpen = true
	a.detail = kind
	a.focusDetailClose = true
	if !wasOpen {
		a.persist()
	}
}

func (a *appModel) toggleDetail(kind detailKind) {
	if a.workspaceOpen && a.activeDetail() == kind {
		a.closeDetail()
		return
	}
	a.openDetail(kind)
}

func (a *appModel) closeDetail() {
	kind := a.activeDetail()
	if kind == detailHistory {
		a.cancelHistoryFullDisplay()
	}
	a.workspaceOpen = false
	a.detail = ""
	a.focusDetailClose = false
	switch kind {
	case detailHistory:
		a.focusHistory = true
	case detailProfile:
		a.focusContactProfile = true
	default:
		a.focusWorkspace = true
	}
	a.persist()
}

func (a *appModel) setWorkspaceOpen(open bool) {
	if !open {
		if a.workspaceOpen {
			a.closeDetail()
		}
		return
	}
	wasOpen := a.workspaceOpen
	if a.detailIs(detailHistory) {
		a.cancelHistoryFullDisplay()
	}
	a.workspaceOpen = true
	a.detail = detailWork
	a.focusDetailClose = true
	if !wasOpen {
		a.persist()
	}
}

func (a *appModel) toggleWorkspace() { a.toggleDetail(detailWork) }

func (a *appModel) closeWorkspace() { a.closeDetail() }

// detailPageActive means the right surface is the whole application page
// because the three-pane layout cannot keep its 480px reading column.
func (a *appModel) detailPageActive() bool {
	return a.workspaceOpen && a.workOverlay()
}

// mainPageActive means a result, reader, source list or non-composer model
// surface has taken over the main area. The right detail remains available in
// state, but is not squeezed beside the reading surface.
func (a *appModel) mainPageActive() bool {
	if a.issueResult.Open || a.contactHistoryFull != nil {
		return true
	}
	switch a.aux {
	case "sources", "model", "task-context":
		return !a.composerInlineAux()
	default:
		return false
	}
}

func (a *appModel) focusDetailControl(c *ui.Context, button ui.Element) {
	if a.focusDetailClose {
		button.Focus()
		a.focusDetailClose = false
	}
}

func (a *appModel) navigationToolbar(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	ui.Toolbar(c, func() {
		if a.railCollapsed {
			contacts := appTooltip(c, ui.Button(c, "").Label(msgs.RailContactsTitle()), msgs.NavShowContactsTooltip()).
				MinHeight(32).Height(32).Padding(4, 8)
			contacts.Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
					ui.Icon(c, navContactsIcon).Width(18).Height(18).TextColor(t.TextMuted)
					ui.Text(c, msgs.RailContactsTitle()).FontSize(typeAction).LineHeight(1.3).TextColor(t.Text)
				})
			})
			if a.focusRail {
				contacts.Focus()
				a.focusRail = false
			}
			if contacts.Clicked() {
				a.toggleRail()
			}
		}
		a.navigationWorkEntry(c, t)
		a.navigationToggle(c, t, detailHistory, msgs.NavHistory(), msgs.NavConversationHistoryTooltip(), navHistoryIcon, &a.focusHistory)
		a.navigationToggle(c, t, detailProfile, msgs.NavContactProfile(), msgs.NavContactProfileTooltip(), navProfileIcon, &a.focusContactProfile)
	}).Label(msgs.NavWorkspaceDetails())
}

func (a *appModel) navigationWorkEntry(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	msgs := locales.In(c.Locale())
	notice := a.issueWorkNotice(ct)
	total := issueOpenCount(ct)
	targetID := ""
	if notice.Attention || notice.Waiting {
		targetID = notice.IssueID
	}
	on := a.detailIs(detailWork)
	summary := msgs.WorkEntrySummary(total, notice.AttentionCount, notice.WaitingCount, notice.ActiveCount)
	toggle := appTooltip(c, ui.Toggle(c, &on, "").Label(msgs.WorkEntryLabel()), summary).
		MinWidth(44).Height(32).Padding(2, 4)
	toggle.Children(func() {
		color := t.TextMuted
		if on {
			color = t.Text
		}
		if total > 0 {
			ui.Row(c).AlignItems(ui.Center).Gap(2).Children(func() {
				ui.Icon(c, navWorkIcon).Width(18).Height(18).TextColor(color)
				countColor := t.TextMuted
				if on {
					countColor = t.Text
				}
				if notice.AttentionCount > 0 {
					ui.Text(c, "!").FontSize(12).FontWeight(600).TextColor(t.Warning)
				}
				ui.Text(c, fmt.Sprintf("%d", total)).FontSize(12).TextColor(countColor)
			})
		} else {
			ui.Icon(c, navWorkIcon).Width(18).Height(18).TextColor(color)
		}
	})
	if on {
		toggle.Selected(true)
		selectionButtonState(c, toggle, true)
	}
	if a.focusWorkspace {
		toggle.Focus()
		a.focusWorkspace = false
	}
	if toggle.Changed() {
		if !on {
			a.closeDetail()
			return
		}
		if targetID != "" {
			a.openIssueTarget(targetID)
			return
		}
		a.toggleDetail(detailWork)
	}
}

func (a *appModel) navigationToggle(c *ui.Context, t *ui.Theme, kind detailKind, label, tooltip string, icon *ui.SVG, focus *bool) {
	active := a.detailIs(kind)
	on := active
	toggle := appTooltip(c, ui.Toggle(c, &on, "").Label(label), tooltip).
		MinWidth(32).Width(32).Height(32).Padding(0)
	toggle.Children(func() {
		color := t.TextMuted
		if active {
			color = t.Text
		}
		ui.Icon(c, icon).Width(18).Height(18).TextColor(color)
	})
	if active {
		toggle.Selected(true)
		selectionButtonState(c, toggle, true)
	}
	if focus != nil && *focus {
		toggle.Focus()
		*focus = false
	}
	if toggle.Changed() {
		a.toggleDetail(kind)
	}
}

func (a *appModel) rememberHistoryView(contactID string) {
	if contactID == "" {
		return
	}
	if a.historyByContact == nil {
		a.historyByContact = map[string]historyViewMemory{}
	}
	a.historyByContact[contactID] = historyViewMemory{
		Query: a.historyQuery, Scroll: a.historyScroll,
		Result: a.contactHistory, Error: a.contactHistoryError, ErrorDetail: a.contactHistoryErrorDetail,
	}
}

func (a *appModel) restoreHistoryView(contactID string) {
	var memory historyViewMemory
	if a.historyByContact != nil {
		memory = a.historyByContact[contactID]
	}
	a.historyQuery = memory.Query
	a.historyScroll = memory.Scroll
	a.contactHistory = memory.Result
	a.contactHistoryFull = nil
	a.contactHistoryError = memory.Error
	a.contactHistoryErrorDetail = memory.ErrorDetail
	a.contactHistoryBusy = false
	a.contactHistoryDetailBusy = false
	a.contactHistoryGen++
	a.contactHistoryDetailGen++
	a.focusHistoryFullID = ""
}

func (a *appModel) historyFullKey() string {
	if a.contactHistoryFull == nil {
		return ""
	}
	return historyOriginKey(a.contactHistoryFull.Origin, a.contactHistoryFull.SessionID, a.contactHistoryFull.EntryID)
}

func (a *appModel) closeContactHistoryFull() {
	if a.contactHistoryFull == nil {
		return
	}
	a.focusHistoryFullID = a.historyFullKey()
	a.contactHistoryFull = nil
}

func (a *appModel) closeIssueResultReader() {
	state := &a.issueResult.Panel
	if state.ReaderKey != "" {
		state.ReaderReturnKey = state.ReaderKey
	}
	state.ReaderKey = ""
}

func (a *appModel) profileScrollFor(contactID string) *ui.ScrollState {
	if a.profileScrolls == nil {
		a.profileScrolls = map[string]*ui.ScrollState{}
	}
	if a.profileScrolls[contactID] == nil {
		a.profileScrolls[contactID] = &ui.ScrollState{}
	}
	return a.profileScrolls[contactID]
}

func (a *appModel) profileDetail(c *ui.Context, t *ui.Theme, narrow bool) {
	ct := a.selectedContact()
	if ct == nil {
		return
	}
	msgs := locales.In(c.Locale())
	ui.Column(c).FillHeight().Padding(16).Gap(8).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			if narrow {
				back := detailBackButton(c, t, msgs.ProfileClose(), msgs.ProfileBackTooltip()).OnClick(func() { a.closeDetail() })
				a.focusDetailControl(c, back)
				ui.Text(c, msgs.NavContactProfile()).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text).Grow(1)
				return
			}
			ui.Text(c, msgs.NavContactProfile()).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text).Grow(1)
			close := appTooltip(c, secondaryButton(c, msgs.ProfileClose()), msgs.ProfileBackTooltip()).OnClick(func() { a.closeDetail() })
			a.focusDetailControl(c, close)
		})
		ui.Scroll(c).Grow(1).TrackScroll(a.profileScrollFor(ct.ID)).Children(func() {
			ui.Column(c).Gap(8).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					a.avatarTile(c, t, 32)
					ui.Column(c).MinWidth(0).Shrink(1).Gap(1).Children(func() {
						ui.Text(c, ct.Name).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text).MaxLines(2)
						ui.Text(c, msgs.StatusDetail(a.statusLabel(ct, msgs), ct.Role)).FontSize(typeMeta).LineHeight(1.4).TextColor(a.statusColor(t, ct.Status))
					})
				})
				if ct.Archived {
					ui.Text(c, msgs.ProfileArchivedNotice()).FontSize(typeAction).LineHeight(1.4).TextColor(t.Warning)
				}
				a.profileSummaryField(c, t, msgs.ProfilePurpose(), valueOr(ct.Purpose, msgs.ProfileNoPurpose()))
				instructions := strings.TrimSpace(ct.Instructions)
				if instructions == "" {
					instructions = msgs.ProfileNoInstructions()
				}
				a.profileSummaryField(c, t, msgs.ProfileInstructions(), instructions)
				workspace := strings.TrimSpace(ct.Workspace)
				if workspace == "" {
					workspace = msgs.ProfileWorkspaceInherited()
				}
				a.profileSummaryField(c, t, msgs.ProfileWorkspace(), workspace)
				session := msgs.ProfileNoSession()
				if ct.SessionID != "" {
					session = msgs.ProfileSession(ct.SessionID)
					if !ct.Loaded {
						session = msgs.ProfileSessionNotLoaded(ct.SessionID)
					}
				}
				a.profileSummaryField(c, t, msgs.ProfileConversation(), session)
				active, unknown := a.contactActivity(ct)
				activity := msgs.ProfileNoActivity()
				switch {
				case unknown:
					activity = msgs.ProfileActivityUnknown()
				case active:
					activity = msgs.ProfileActivityActive()
				}
				a.profileSummaryField(c, t, msgs.ProfileActivity(), activity)
				edit := secondaryButton(c, msgs.ProfileEditFull()).TextColor(t.Text).OnClick(func() { a.editContactProfile() })
				if a.focusProfileEdit {
					edit.Focus()
					a.focusProfileEdit = false
				}
			})
		})
	})
}

func (a *appModel) profileSummaryField(c *ui.Context, t *ui.Theme, label, value string) {
	ui.Column(c).FillWidth().MinWidth(0).Gap(2).Children(func() {
		ui.Text(c, label).FontSize(typeMeta).FontWeight(500).TextColor(t.TextMuted)
		ui.Text(c, value).FontSize(14).LineHeight(1.5).TextColor(t.Text)
	})
}

func (a *appModel) mainPage(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Fill().Background(a.workTheme.Reading).Children(func() {
		switch {
		case a.issueResult.Open:
			a.issueResultMainPage(c, t)
		case a.contactHistoryFull != nil:
			a.historyFullPage(c, t)
		case a.aux == "sources":
			a.sourcesPage(c, t)
		case a.aux == "model":
			a.auxPage(c, t)
		case a.aux == "task-context":
			a.auxPage(c, t)
		default:
			a.main(c, t)
		}
	})
}

func (a *appModel) historyFullPage(c *ui.Context, t *ui.Theme) {
	full := a.contactHistoryFull
	if full == nil {
		return
	}
	msgs := locales.In(c.Locale())
	ct := a.selectedContact()
	from := msgs.SpeakerContact()
	if full.From == "me" {
		from = msgs.SpeakerYou()
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(16, 16).AlignItems(ui.Center).Gap(8).Children(func() {
			back := appTooltip(c, secondaryButton(c, msgs.BackToHistoryVisible()).Label(msgs.CloseFullMessage()), msgs.BackToHistoryTooltip()).OnClick(func() { a.closeContactHistoryFull() })
			if a.focusHistoryFullBack {
				back.Focus()
				a.focusHistoryFullBack = false
			}
			ui.Text(c, msgs.FullSavedMessage()).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text)
		})
		ui.Scroll(c).Grow(1).TrackScroll(&a.historyFullScroll).Padding(0, 16).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(760).AlignItems(ui.Start).Gap(8).Children(func() {
				ui.Text(c, msgs.HistoryMessageMeta(from, full.Date, full.SessionID)).FontSize(typeMeta).LineHeight(1.4).TextColor(t.TextMuted)
				markdownReader(c, full.Text, a.readerOptions(markdownReaderOptions{CopyLabel: msgs.CopySavedMessageLabel(), Theme: messageMarkdownTheme}, historyLinkScope(ct.ID, "full:"+full.SessionID, full.EntryID), full.WorkingDirectory)).FontSize(typeBody).LineHeight(1.6).TextColor(t.Text)
				taskContextReadOnlyView(c, a.workTheme, full.Context)
			})
		})
	})
}

func (a *appModel) sourcesPage(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(16, 16).AlignItems(ui.Center).Gap(8).Children(func() {
			back := appTooltip(c, secondaryButton(c, msgs.BackAction()).Label(msgs.CloseSources()), msgs.BackFromSources()).OnClick(func() { a.closeAux() })
			if a.focusSourcesBack {
				back.Focus()
				a.focusSourcesBack = false
			}
			ui.Text(c, msgs.SourcesTitle()).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text)
		})
		ui.Scroll(c).Grow(1).TrackScroll(&a.sourcesScroll).Padding(0, 16).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(760).AlignItems(ui.Start).Gap(8).Children(func() {
				a.sourcesPanel(c, t)
			})
		})
	})
}

func (a *appModel) auxPage(c *ui.Context, t *ui.Theme) {
	msgs := locales.In(c.Locale())
	title := msgs.SessionModelTitle()
	closeLabel := msgs.CloseSessionModel()
	if a.aux == "task-context" {
		title = msgs.TaskContextTitle()
		closeLabel = msgs.CloseTaskContext()
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(16, 16).AlignItems(ui.Center).Gap(8).Children(func() {
			back := appTooltip(c, secondaryButton(c, msgs.BackAction()).Label(closeLabel), msgs.BackFromAux(title)).OnClick(func() { a.closeAux() })
			if a.focusAuxBack {
				back.Focus()
				a.focusAuxBack = false
			}
			ui.Text(c, title).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text)
		})
		ui.Scroll(c).Grow(1).TrackScroll(a.auxScrollFor(a.aux)).Padding(0, 16).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(760).AlignItems(ui.Start).Gap(8).Children(func() {
				switch a.aux {
				case "model":
					a.modelPanel(c, t)
				case "task-context":
					a.taskContextPanel(c, t)
				}
			})
		})
	})
}

func (a *appModel) cancelHistoryFullDisplay() {
	a.contactHistoryDetailGen++
	a.contactHistoryDetailBusy = false
}

func (a *appModel) syncDetailPageMode() {
	// A Settings or main-reader page is not showing the workbench. Do not
	// advance the detail page mode or arm a hidden workbench close while that
	// page owns focus; the next visible workbench frame evaluates it.
	if a.settingsOpen || a.mainPageActive() {
		return
	}
	now := a.detailPageActive()
	if now == a.detailPageMode {
		return
	}
	a.detailPageMode = now
	if a.workspaceOpen {
		a.focusDetailClose = true
	}
}

func detailBackButton(c *ui.Context, t *ui.Theme, accessible, tooltip string, visible ...string) ui.Element {
	label := locales.In(c.Locale()).BackLabel()
	if len(visible) > 0 && visible[0] != "" {
		label = visible[0]
	}
	button := appTooltip(c, secondaryButton(c, "").Label(accessible), tooltip).Padding(4, 8)
	button.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
			ui.Icon(c, navBackIcon).Width(16).Height(16).TextColor(t.TextMuted)
			ui.Text(c, label).FontSize(typeAction).LineHeight(1.3).TextColor(t.Text)
		})
	})
	return button
}
