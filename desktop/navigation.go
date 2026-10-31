package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
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
	Query  string
	Scroll ui.ScrollState
	Result *contactHistoryResult
	Error  string
}

var (
	navWorkIcon     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="7" width="18" height="13" rx="2"/><path d="M8 7V5a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M3 12h18M10 12v3h4v-3"/></svg>`))
	navHistoryIcon  = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5M12 7v5l3 2"/></svg>`))
	navProfileIcon  = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/></svg>`))
	navBackIcon     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="m15 18-6-6 6-6"/></svg>`))
	navContactsIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="8" r="3"/><path d="M3 20a6 6 0 0 1 12 0M17 8h4M19 6v4M17 20h4"/></svg>`))
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
	ui.Toolbar(c, func() {
		if a.railCollapsed {
			contacts := appTooltip(c, ui.Button(c, "").Label("Contacts"), "Show contacts").
				MinHeight(32).Height(32).Padding(4, 8)
			contacts.Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
					ui.Icon(c, navContactsIcon).Width(18).Height(18).TextColor(t.TextMuted)
					ui.Text(c, "Contacts").FontSize(typeAction).LineHeight(1.3).TextColor(t.Text)
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
		a.navigationToggle(c, t, detailWork, "Work", "Work details", navWorkIcon, &a.focusWorkspace)
		a.navigationToggle(c, t, detailHistory, "History", "Conversation history", navHistoryIcon, &a.focusHistory)
		a.navigationToggle(c, t, detailProfile, "Contact profile", "Contact profile summary", navProfileIcon, &a.focusContactProfile)
	}).Label("Workspace details")
}

func (a *appModel) navigationToggle(c *ui.Context, t *ui.Theme, kind detailKind, label, tooltip string, icon *ui.SVG, focus *bool) {
	active := a.detailIs(kind)
	on := active
	toggle := appTooltip(c, ui.Toggle(c, &on, "").Label(label), tooltip).
		MinWidth(32).Width(32).Height(32).Padding(0)
	toggle.Children(func() {
		color := t.TextMuted
		if active {
			color = t.AccentText
		}
		ui.Icon(c, icon).Width(18).Height(18).TextColor(color)
	})
	if active {
		toggle.Selected(true)
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
		Result: a.contactHistory, Error: a.contactHistoryError,
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
	ui.Column(c).FillHeight().Padding(12).Gap(8).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			if narrow {
				back := detailBackButton(c, t, "Close profile", "Back from Contact profile").OnClick(func() { a.closeDetail() })
				a.focusDetailControl(c, back)
				ui.Text(c, "Contact profile").FontSize(typePageTitle).FontWeight(600).TextColor(t.Text).Grow(1)
				return
			}
			ui.Text(c, "Contact profile").FontSize(typePageTitle).FontWeight(600).TextColor(t.Text).Grow(1)
			close := appTooltip(c, secondaryButton(c, "Close profile"), "Back from Contact profile").OnClick(func() { a.closeDetail() })
			a.focusDetailControl(c, close)
		})
		ui.Scroll(c).Grow(1).TrackScroll(a.profileScrollFor(ct.ID)).Children(func() {
			ui.Column(c).Gap(10).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(9).Children(func() {
					a.avatarTile(c, t, 42)
					ui.Column(c).MinWidth(0).Shrink(1).Gap(1).Children(func() {
						ui.Text(c, ct.Name).FontSize(typePageTitle).FontWeight(600).TextColor(t.Text).MaxLines(2)
						ui.Text(c, a.statusLabel(ct)+": "+ct.Role).FontSize(typeMeta).LineHeight(1.4).TextColor(a.statusColor(t, ct.Status))
					})
				})
				if ct.Archived {
					ui.Text(c, "Archived contact. Existing history, drafts and issue data remain available.").FontSize(typeAction).LineHeight(1.4).TextColor(t.Warning)
				}
				a.profileSummaryField(c, t, "Purpose", valueOr(ct.Purpose, "No purpose recorded."))
				instructions := strings.TrimSpace(ct.Instructions)
				if instructions == "" {
					instructions = "No instructions recorded."
				}
				a.profileSummaryField(c, t, "Instructions", instructions)
				workspace := strings.TrimSpace(ct.Workspace)
				if workspace == "" {
					workspace = "Inherited from the connection default"
				}
				a.profileSummaryField(c, t, "Workspace", workspace)
				session := "No conversation session yet."
				if ct.SessionID != "" {
					session = "Conversation session " + ct.SessionID
					if !ct.Loaded {
						session += ": history not loaded"
					}
				}
				a.profileSummaryField(c, t, "Conversation", session)
				active, unknown := a.contactActivity(ct)
				activity := "No active work."
				switch {
				case unknown:
					activity = "Work status is unresolved; reload before changing or archiving."
				case active:
					activity = "Work is active for this contact."
				}
				a.profileSummaryField(c, t, "Activity", activity)
				edit := secondaryButton(c, "Edit full profile").TextColor(t.Text).OnClick(func() { a.editContactProfile() })
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
		ui.Text(c, label).FontSize(typeMeta).FontWeight(600).TextColor(t.TextMuted)
		ui.Text(c, value).FontSize(14).LineHeight(1.45).TextColor(t.Text)
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
	ct := a.selectedContact()
	from := "Contact"
	if full.From == "me" {
		from = "You"
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(16, 16).AlignItems(ui.Center).Gap(8).Children(func() {
			back := appTooltip(c, secondaryButton(c, "← Back to history").Label("Close full message"), "Back to History").OnClick(func() { a.closeContactHistoryFull() })
			if a.focusHistoryFullBack {
				back.Focus()
				a.focusHistoryFullBack = false
			}
			ui.Text(c, "Full saved message").FontSize(typePageTitle).FontWeight(600).TextColor(t.Text)
		})
		ui.Scroll(c).Grow(1).TrackScroll(&a.historyFullScroll).Padding(0, 16).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(760).AlignItems(ui.Start).Gap(10).Children(func() {
				ui.Text(c, from+". "+full.Date+". "+full.SessionID).FontSize(typeMeta).LineHeight(1.4).TextColor(t.TextMuted)
				markdownReader(c, full.Text, a.readerOptions(markdownReaderOptions{CopyLabel: "Copy saved message", Theme: messageMarkdownTheme}, historyLinkScope(ct.ID, "full:"+full.SessionID, full.EntryID))).FontSize(typeBody).LineHeight(1.8).TextColor(t.Text)
				taskContextReadOnlyView(c, a.workTheme, full.Context)
			})
		})
	})
}

func (a *appModel) sourcesPage(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(16, 16).AlignItems(ui.Center).Gap(8).Children(func() {
			back := appTooltip(c, secondaryButton(c, "← Back").Label("Close sources"), "Back from sources").OnClick(func() { a.closeAux() })
			if a.focusSourcesBack {
				back.Focus()
				a.focusSourcesBack = false
			}
			ui.Text(c, "Sources").FontSize(typePageTitle).FontWeight(600).TextColor(t.Text)
		})
		ui.Scroll(c).Grow(1).TrackScroll(&a.sourcesScroll).Padding(0, 16).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(760).AlignItems(ui.Start).Gap(10).Children(func() {
				a.sourcesPanel(c, t)
			})
		})
	})
}

func (a *appModel) auxPage(c *ui.Context, t *ui.Theme) {
	title := "Session model"
	closeLabel := "Close session model"
	if a.aux == "task-context" {
		title = "Task context"
		closeLabel = "Close task context"
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(16, 16).AlignItems(ui.Center).Gap(8).Children(func() {
			back := appTooltip(c, secondaryButton(c, "← Back").Label(closeLabel), "Back from "+title).OnClick(func() { a.closeAux() })
			if a.focusAuxBack {
				back.Focus()
				a.focusAuxBack = false
			}
			ui.Text(c, title).FontSize(typePageTitle).FontWeight(600).TextColor(t.Text)
		})
		ui.Scroll(c).Grow(1).TrackScroll(a.auxScrollFor(a.aux)).Padding(0, 16).Children(func() {
			ui.Column(c).FillWidth().MaxWidth(760).AlignItems(ui.Start).Gap(10).Children(func() {
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
	now := a.detailPageActive()
	if now == a.detailPageMode {
		return
	}
	a.detailPageMode = now
	if a.workspaceOpen {
		a.focusDetailClose = true
	}
}

func detailBackButton(c *ui.Context, t *ui.Theme, accessible, tooltip string) ui.Element {
	button := appTooltip(c, secondaryButton(c, "").Label(accessible), tooltip).Padding(4, 8)
	button.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
			ui.Icon(c, navBackIcon).Width(16).Height(16).TextColor(t.TextMuted)
			ui.Text(c, "Back").FontSize(typeAction).LineHeight(1.3).TextColor(t.Text)
		})
	})
	return button
}
