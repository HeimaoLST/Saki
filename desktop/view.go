package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

func (a *appModel) view(c *ui.Context) {
	a.workTheme = resolveWorkbenchTheme(c.Theme().Dark)
	t := a.workTheme.Theme
	c.SetTheme(t)
	ui.Box(c).Fill().Children(func() {
		ui.Row(c).Fill().Children(func() {
			if !a.railCollapsed {
				a.rail(c, t)
			}
			a.main(c, t)
			if a.workspaceOpen && !a.workOverlay() {
				a.workspaceFrame(c, t, a.workWidth())
			}
		})
		if a.workspaceOpen && a.workOverlay() && a.aux == "" {
			a.workspaceModal(c, t)
		}
		if a.aux != "" {
			a.auxView(c, t)
		} else if a.screen == viewHistory {
			a.historyView(c, t)
		}
	})
}

// ---------- rail ----------

func (a *appModel) rail(c *ui.Context, t *ui.Theme) {
	width := a.railWidth()
	ui.Column(c).Width(width).FillHeight().Padding(10, 10).Gap(10).Background(a.workTheme.Rail).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Text(c, "Saki").FontSize(14).FontWeight(600).TextColor(t.Text)
			ui.Spacer(c)
			hide := ui.Button(c, "Hide").FontSize(12).Padding(3, 8).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.toggleRail() })
			if a.focusRail {
				hide.Focus()
				a.focusRail = false
			}
		})
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Text(c, "Contacts").FontSize(11).TextColor(t.TextMuted)
			ui.Spacer(c)
			ui.Button(c, "Add contact").FontSize(12).Padding(4, 8).Radius(999).Background(t.SurfaceHover).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.addDefaultContact() })
		})
		ui.Scroll(c).Grow(1).Children(func() {
			for i := range a.contacts {
				a.railRow(c, t, i)
			}
		})
	})
}

func (a *appModel) railRow(c *ui.Context, t *ui.Theme, i int) {
	ct := a.contacts[i]
	row := ui.ButtonBase(c).Label("Contact " + ct.Name).Gap(10).Padding(8).Radius(12).AlignItems(ui.Start).Background(ui.Transparent).BorderWidth(0)
	if i == a.selected {
		row.Background(t.SurfaceHover)
	}
	row.OnClick(func() { a.selectContact(i) })
	row.Children(func() {
		a.avatarTile(c, t, 32)
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
				ui.Text(c, ct.Name).FontSize(14).FontWeight(500).TextColor(t.Text)
				ui.Spacer(c)
				if ct.Unread > 0 {
					ui.Text(c, fmt.Sprintf("%d", ct.Unread)).FontSize(11).TextColor(t.Warning)
				}
			})
			line := a.snippet(ct)
			color := t.TextMuted
			if ct.Status != statusReady {
				status := a.statusLabel(ct)
				detail := strings.TrimSpace(line)
				if detail == status {
					detail = ""
				}
				if strings.HasPrefix(detail, "Running ") {
					detail = strings.TrimPrefix(detail, "Running ")
				}
				if detail == "" {
					line = status
				} else {
					line = status + " · " + detail
				}
				color = a.statusColor(t, ct.Status)
			}
			ui.Text(c, line).FontSize(12).LineHeight(1.35).MaxLines(1).TextColor(color)
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

func (a *appModel) main(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Grow(1).FillHeight().Background(t.Surface).Children(func() {
		if !a.demo {
			switch {
			case a.runtimeStatus == runtimeStateDraining:
				ui.Text(c, "Draining the runtime…").FontSize(12).Padding(8, 16).TextColor(t.TextMuted)
			case a.runtimeErr != "":
				label := a.runtimeStage
				if label == "" {
					label = "Runtime"
				}
				ui.Column(c).Padding(8, 16).Gap(2).Children(func() {
					ui.Text(c, label+": "+a.runtimeErr).FontSize(12).LineHeight(1.4).TextColor(t.Danger)
					if a.runtimeHint != "" {
						ui.Text(c, a.runtimeHint).FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
					}
					ui.Row(c).Children(func() {
						if ui.Button(c, "Configuration details").FontSize(12).Padding(3, 8).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).Clicked() {
							a.openConfiguration()
						}
					})
				})
			case a.runtimeStatus == runtimeStateStarting:
				ui.Text(c, "Connecting to runtime…").FontSize(12).Padding(8, 16).TextColor(t.TextMuted)
			}
			if ct := a.selectedContact(); ct.LoadErr != "" {
				ui.Text(c, "Contact load: "+ct.LoadErr).FontSize(12).Padding(8, 16).TextColor(t.Warning)
			}
		}
		a.header(c, t)
		a.chat(c, t)
	})
}

func (a *appModel) header(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	ui.Row(c).Justify(ui.Center).Children(func() {
		ui.Row(c).WidthPercent(86).MaxWidth(760).Padding(8, 0).Gap(8).AlignItems(ui.Center).Children(func() {
			a.avatarTile(c, t, 28)
			ui.Column(c).Gap(1).Children(func() {
				ui.Text(c, ct.Name).FontSize(15).FontWeight(500).TextColor(t.Text)
				ui.Text(c, a.statusLabel(ct)+" · "+ct.Role).FontSize(11).LineHeight(1.35).TextColor(a.statusColor(t, ct.Status))
			})
			ui.Spacer(c)
			if a.railCollapsed {
				contacts := ui.Button(c, "Contacts").FontSize(12).Padding(4, 8).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.toggleRail() })
				if a.focusRail {
					contacts.Focus()
					a.focusRail = false
				}
			}
			work := ui.Button(c, "Work").FontSize(12).Padding(4, 8).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.toggleWorkspace() })
			if a.focusWorkspace {
				work.Focus()
				a.focusWorkspace = false
			}
			history := ui.Button(c, "History").FontSize(12).Padding(4, 8).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.openHistory() })
			if a.focusHistory {
				history.Focus()
				a.focusHistory = false
			}
			settings := ui.Button(c, "Settings").FontSize(12).Padding(4, 8).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.openSettings() })
			if a.focusSettings {
				settings.Focus()
				a.focusSettings = false
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
				}).FillHeight().WidthPercent(86).MaxWidth(760).Padding(16, 0).Gap(18)
			})
		}
		ui.Row(c).Justify(ui.Center).Children(func() {
			ui.Column(c).WidthPercent(86).MaxWidth(760).Children(func() {
				a.composer(c, t)
			})
		})
	})
}

func (a *appModel) historyMessage(c *ui.Context, t *ui.Theme, e historyEntry, highlight bool) {
	bg := t.Surface
	if e.From == "me" {
		bg = a.workTheme.BubbleUser
	} else if highlight {
		bg = t.Selection
	}
	if e.From == "me" {
		ui.Row(c).Justify(ui.End).Children(func() {
			ui.Text(c, e.Text).FontSize(14).LineHeight(1.5).Padding(8, 12).Radius(12).Background(bg).TextColor(t.Text).MaxWidth(460)
		})
		return
	}
	ui.Row(c).Gap(10).AlignItems(ui.Start).Children(func() {
		a.avatarTile(c, t, 28)
		ui.Column(c).MaxWidth(680).Padding(10, 14).Radius(14).Background(bg).Gap(4).Children(func() {
			ui.Text(c, a.selectedContact().Name+"  "+e.Date).FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
			ui.Text(c, e.Text).FontSize(14).LineHeight(1.5).TextColor(t.Text)
		})
	})
}

func (a *appModel) message(c *ui.Context, t *ui.Theme, ct *contact, m *message) {
	switch m.Kind {
	case msgActivity:
		if m.ToolName != "" || m.ToolOutput != "" {
			title := toolTitle(m)
			summary := toolOutputSummary(m.ToolOutput)
			if m.Expanded {
				summary = truncate(m.ToolOutput, 2000)
			}
			ui.Column(c).Gap(2).Padding(4, 0).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Text(c, title).FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
					if m.ToolOutput != "" {
						label := "Details"
						if m.Expanded {
							label = "Hide"
						}
						ui.Button(c, label).FontSize(12).Padding(4, 8).Radius(999).Background(t.SurfaceHover).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { m.Expanded = !m.Expanded })
					}
				})
				if summary != "" {
					lines := 2
					if m.Expanded {
						lines = 12
					}
					ui.Text(c, summary).FontSize(12).LineHeight(1.45).MaxLines(lines).TextColor(t.TextMuted)
				}
			})
			return
		}
		ui.Row(c).Gap(10).AlignItems(ui.Start).Children(func() {
			ui.Column(c).Gap(4).Children(func() {
				label := "Looked up earlier conversations"
				if m.Expanded {
					label = "– " + label
				} else {
					label = "+ " + label
				}
				ui.Row(c).Children(func() {
					ui.Button(c, label).FontSize(12).OnClick(func() { m.Expanded = !m.Expanded })
				})
				if m.Expanded {
					for _, step := range m.Steps {
						ui.Text(c, "· "+step).FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
					}
					if len(m.Sources) > 0 {
						ui.Row(c).Children(func() {
							ui.Button(c, "Open sources").FontSize(12).OnClick(func() { a.openSources(m.ID) })
						})
					}
				}
			})
		})
	case msgUser:
		ui.Row(c).Justify(ui.End).Children(func() {
			ui.Column(c).MaxWidthPercent(80).Gap(2).Children(func() {
				if m.Key != "" && m.Key == a.highlight {
					ui.Text(c, "Located in history").FontSize(11).TextColor(t.TextMuted)
				}
				ui.Text(c, m.Text).FontSize(14).LineHeight(1.55).Padding(10, 14).Radius(14).Background(a.workTheme.BubbleUser).TextColor(t.Text)
			})
		})
	default:
		ui.Row(c).Gap(10).AlignItems(ui.Start).Children(func() {
			a.avatarTile(c, t, 28)
			ui.Column(c).MaxWidthPercent(80).Padding(10, 14).Radius(14).Background(t.SurfaceHover).Gap(4).Children(func() {
				if m.Key != "" && m.Key == a.highlight {
					ui.Text(c, "Located in history").FontSize(11).TextColor(t.TextMuted)
				}
				ui.Text(c, m.Text).FontSize(14).LineHeight(1.55).TextColor(t.Text)
				if m.AlertTitle != "" {
					a.alert(c, t, m)
				}
				if len(m.Sources) > 0 {
					ui.Row(c).Children(func() {
						ui.Button(c, fmt.Sprintf("%d earlier messages", len(m.Sources))).FontSize(12).OnClick(func() { a.openSources(m.ID) })
					})
				}
			})
		})
	}
}

func (a *appModel) alert(c *ui.Context, t *ui.Theme, m *message) {
	ui.Column(c).Padding(8).Radius(10).Background(t.SurfaceHover).Gap(4).Children(func() {
		ui.Text(c, m.AlertTitle).FontSize(12).FontWeight(500).TextColor(t.Warning)
		if m.AlertDetail != "" {
			ui.Text(c, m.AlertDetail).FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
		}
		if m.Retry && !m.RetryDone {
			ui.Row(c).Children(func() {
				ui.Button(c, "Try again").FontSize(12).OnClick(func() { a.retry(m) })
			})
		} else if m.RetryDone {
			ui.Text(c, "Retried").FontSize(12).TextColor(t.TextMuted)
		}
	})
}

func (a *appModel) emptyState(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	ui.Column(c).Padding(8, 0).Gap(8).Children(func() {
		if !a.demo && ct.SessionID != "" && !ct.Loaded {
			ui.Text(c, "Conversation history is not loaded").FontSize(15).FontWeight(500).TextColor(t.Text)
			ui.Text(c, "This contact already has a session, but its history has not been read yet; it is not shown as empty.").FontSize(12).LineHeight(1.45).TextColor(t.TextMuted)
			if a.runtimeStatus != runtimeStateReady {
				ui.Text(c, "Runtime is not ready; history cannot be read yet.").FontSize(12).LineHeight(1.45).TextColor(t.Warning)
			}
			return
		}
		ui.Text(c, "No messages yet").FontSize(15).FontWeight(500).TextColor(t.Text)
		if a.demo {
			ui.Text(c, "Start typing below to talk with "+ct.Name+"; you can personalize name, purpose, instructions and workspace any time in Settings.").FontSize(12).LineHeight(1.45).TextColor(t.TextMuted)
		} else {
			ui.Text(c, "Start typing below to talk with "+ct.Name+". Set purpose, instructions and workspace before the first message; after that only the display name can change.").FontSize(12).LineHeight(1.45).TextColor(t.TextMuted)
		}
		ui.Row(c).Children(func() {
			ui.Button(c, "Settings").FontSize(12).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.openSettings() })
		})
	})
}

// ---------- composer ----------

func (a *appModel) composer(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	ui.Column(c).Padding(10, 0).Gap(4).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.End).Padding(6, 10).Radius(14).Background(t.SurfaceHover).Children(func() {
			input := ui.TextArea(c, &ct.Draft).Label("Composer for " + ct.Name).FontSize(14).LineHeight(1.5).Height(44).Background(ui.Transparent).BorderWidth(0).Padding(2).Placeholder("Message " + ct.Name).Grow(1).OnChange(func() {
				a.draftChanged(ct)
			}).OnEnter(func(e ui.Enter) bool {
				if e.Mods&ui.Shift != 0 {
					return false
				}
				text := strings.TrimSpace(e.Text())
				if text == "" {
					return true
				}
				if a.send(ct, text) && a.demo {
					e.SetText("")
				}
				return true
			})
			if a.focusComposer {
				input.Focus()
				a.focusComposer = false
			}
			sendDisabled := strings.TrimSpace(ct.Draft) == "" || ct.Status == statusWorking
			if !a.demo {
				sendDisabled = strings.TrimSpace(ct.Draft) == "" || a.canSend(ct) != ""
			}
			send := ui.PrimaryButton(c, "Send").FontSize(12).Padding(3, 10).Disabled(sendDisabled)
			if send.Clicked() {
				a.sendCurrent()
			}
		})
		if !a.demo {
			if reason := a.canSend(ct); strings.Contains(reason, "Another contact is working") {
				ui.Text(c, reason).FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
			}
			switch {
			case ct.SendState == sendSubmitting:
				ui.Text(c, "Sending…").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
			case ct.SendState == sendAccepted && ct.Status != statusWorking:
				ui.Text(c, "Accepted; waiting for the runtime to start.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
			case ct.SendState == sendUnknown:
				if a.runtimeStatus == runtimeStateReady && a.rt != nil {
					ui.Text(c, "Delivery not confirmed. Reload to verify before sending again.").FontSize(11).LineHeight(1.4).TextColor(t.Danger)
				} else {
					ui.Text(c, "Runtime disconnected; restart Saki to check the task and history.").FontSize(11).LineHeight(1.4).TextColor(t.Danger)
				}
			}
			if ct.SendError != "" {
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, ct.SendError).FontSize(11).LineHeight(1.4).TextColor(t.Warning)
					if (ct.SendState == sendUnknown || ct.SendError != "") && a.runtimeStatus == runtimeStateReady && a.rt != nil {
						if ui.Button(c, "Reload").FontSize(12).Padding(3, 8).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).Clicked() {
							a.reloadSelected()
						}
					}
				})
			}
			if ct.StatusDetail != "" {
				ui.Text(c, ct.StatusDetail).FontSize(11).LineHeight(1.4).TextColor(t.Warning)
			}
		}
		ui.Text(c, "Enter to send · Shift + Enter for a new line").FontSize(10).LineHeight(1.4).TextColor(t.TextMuted)
	})
}

// ---------- history ----------

func (a *appModel) historyView(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	open := a.screen == viewHistory
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.28))
		panel.Width(560).FillHeight().Background(t.Surface).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Padding(12, 12).Children(func() {
				ui.Text(c, "History · "+ct.Name).FontSize(16).FontWeight(600).TextColor(t.Text)
				ui.Spacer(c)
				close := ui.Button(c, "Close history").FontSize(12).OnClick(func() {
					open = false
					a.backToChat()
				})
				if a.focusHistoryOpen {
					close.Focus()
					a.focusHistoryOpen = false
				}
			})
			ui.Scroll(c).Grow(1).Padding(12).Children(func() {
				ui.Column(c).Gap(10).Children(func() {
					ui.Text(c, "Searching only "+ct.Name+"'s conversation history. Messages from other contacts are never included.").FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
					if !a.demo && ct.SessionID != "" && !ct.Loaded {
						ui.Text(c, "History is not loaded yet; existing records are not shown as absent.").FontSize(12).LineHeight(1.4).TextColor(t.Warning)
						return
					}
					ui.TextInput(c, &a.historyQuery).FontSize(14).Placeholder("Search " + ct.Name + "'s history")
					matches := a.historyMatches()
					if len(matches) == 0 {
						if strings.TrimSpace(a.historyQuery) == "" {
							ui.Text(c, "No saved messages in "+ct.Name+"'s loaded history.").FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
						} else {
							ui.Text(c, "No messages in "+ct.Name+"'s history match “"+a.historyQuery+"”. Try a shorter phrase.").FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
						}
						return
					}
					for _, e := range matches {
						e := e
						ui.Column(c).Padding(10).Radius(10).Background(t.SurfaceHover).Gap(4).Children(func() {
							ui.Text(c, e.Date+" · "+speakerLabel(ct, e)).FontSize(12).LineHeight(1.4).TextColor(t.Success)
							ui.Text(c, e.Text).FontSize(14).LineHeight(1.5).TextColor(t.Text)
							ui.Row(c).Children(func() {
								ui.Button(c, "View in conversation").FontSize(12).OnClick(func() { a.viewInConversation(e.ID) })
							})
						})
					}
				})
			})
		})
	})
	if !open && a.screen == viewHistory {
		a.backToChat()
	}
}

func speakerLabel(c *contact, e historyEntry) string {
	if e.From == "me" {
		return "You"
	}
	return c.Name
}

// ---------- aux panel ----------

func (a *appModel) auxView(c *ui.Context, t *ui.Theme) {
	returnAux := a.aux
	open := true
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.28))
		panel.Width(340).FillHeight().Background(t.Surface).Children(func() {
			ui.Scroll(c).Grow(1).Padding(12).Children(func() {
				ui.Column(c).Gap(10).Children(func() {
					switch returnAux {
					case "sources":
						a.sourcesPanel(c, t)
					case "configuration":
						a.configurationPanel(c, t)
					default:
						a.settingsPanel(c, t)
					}
				})
			})
		})
	})
	if !open {
		a.aux = ""
		switch returnAux {
		case "settings":
			a.focusSettings = true
		case "sources":
			a.focusHistory = false
		}
	}
}

func (a *appModel) sourcesPanel(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	entries := a.sourceEntries(a.auxMessage)
	ui.Row(c).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "Sources").FontSize(16).FontWeight(500).TextColor(t.Text)
		ui.Spacer(c)
		ui.Button(c, "Close").FontSize(12).OnClick(func() { a.closeAux() })
	})
	ui.Text(c, fmt.Sprintf("%d earlier messages from %s's history, cited in this reply.", len(entries), ct.Name)).FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
	for _, e := range entries {
		e := e
		ui.Column(c).Padding(10).Radius(10).Background(t.SurfaceHover).Gap(4).Children(func() {
			ui.Text(c, e.Date+" · "+speakerLabel(ct, e)).FontSize(12).LineHeight(1.4).TextColor(t.Success)
			ui.Text(c, e.Text).FontSize(14).LineHeight(1.5).TextColor(t.Text)
			ui.Row(c).Children(func() {
				ui.Button(c, "View in conversation").FontSize(12).OnClick(func() { a.viewInConversation(e.ID) })
			})
		})
	}
}

func (a *appModel) settingsPanel(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	locked := a.profileLocked(ct)
	ui.Row(c).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "Contact settings").FontSize(16).FontWeight(500).TextColor(t.Text)
		ui.Spacer(c)
		if !a.demo {
			ui.Button(c, "Startup status").FontSize(12).Padding(3, 8).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.openConfiguration() })
		}
		ui.Button(c, "Close").FontSize(12).OnClick(func() { a.closeAux() })
	})
	switch {
	case a.demo:
		ui.Text(c, "Changes apply to the demo only. Saved history stays intact.").FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
	case locked:
		ui.Text(c, "Purpose, instructions and workspace are fixed once this contact's session starts. They can be set before the first message; the name can still change.").FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
	default:
		ui.Text(c, "Optional. These are used when the first message creates this contact's session.").FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
	}
	ui.Text(c, "Name").FontSize(12).FontWeight(500).TextColor(t.Text)
	ui.TextInput(c, &a.settingsDraft.Name).Label("Contact name").FontSize(14).Placeholder("Name")
	if a.settingsError {
		ui.Text(c, "Enter a name for this contact.").FontSize(12).TextColor(t.Danger)
	}
	ui.Text(c, "Purpose").FontSize(12).FontWeight(500).TextColor(t.Text)
	ui.TextArea(c, &a.settingsDraft.Purpose).Label("Contact purpose").FontSize(14).Placeholder("Purpose").Disabled(locked)
	ui.Text(c, "Instructions").FontSize(12).FontWeight(500).TextColor(t.Text)
	ui.TextArea(c, &a.settingsDraft.Instructions).Label("Contact instructions").FontSize(14).Placeholder("Instructions").Disabled(locked)
	ui.Text(c, "Workspace").FontSize(12).FontWeight(500).TextColor(t.Text)
	ui.TextInput(c, &a.settingsDraft.Workspace).Label("Contact workspace").FontSize(14).Placeholder("/absolute/path/to/workspace").Disabled(locked)
	ui.Row(c).Gap(8).Children(func() {
		ui.Button(c, "Cancel").FontSize(13).OnClick(func() { a.closeAux() })
		ui.PrimaryButton(c, "Save changes").FontSize(13).OnClick(func() { a.saveSettings() })
	})
}

func (a *appModel) openConfiguration() {
	a.aux = "configuration"
}

func (a *appModel) configurationPanel(c *ui.Context, t *ui.Theme) {
	ui.Row(c).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "Startup & configuration").FontSize(16).FontWeight(500).TextColor(t.Text)
		ui.Spacer(c)
		ui.Button(c, "Close").FontSize(12).OnClick(func() { a.closeAux() })
	})
	a.runtimePanel(c, t, a.selectedContact())
}

func (a *appModel) runtimePanel(c *ui.Context, t *ui.Theme, ct *contact) {
	localStore := a.runtimeStage == startupStageLocalStore
	if localStore {
		ui.Text(c, "Runtime: not started (local contact store unavailable)").FontSize(12).LineHeight(1.4).TextColor(t.Text)
	} else {
		status := a.runtimeStatus
		if status == "" {
			status = "unknown"
		}
		if status == runtimeStateReady {
			status += " (connection only)"
		}
		ui.Text(c, "Runtime: "+status).FontSize(12).LineHeight(1.4).TextColor(t.Text)
	}
	if a.runtimeErr != "" {
		label := a.runtimeStage
		if label == "" {
			label = "Runtime"
		}
		ui.Text(c, label+": "+a.runtimeErr).FontSize(12).LineHeight(1.4).TextColor(t.Danger)
	}
	if a.runtimeHint != "" {
		ui.Text(c, a.runtimeHint).FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
	}
	helper := a.runtimeBinary
	if helper == "" {
		if localStore {
			helper = "not started; fix the local contact store first"
		} else {
			helper = "not found (expected next to Saki as Saki-runtime, or set SAKI_RUNTIME_BIN)"
		}
	}
	ui.Text(c, "Helper: "+helper).FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
	ui.Text(c, "Store: "+valueOr(a.storePath, "not configured")).FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
	ui.Text(c, "Session directory: "+valueOr(a.sessionDir, "not configured")).FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
	ui.Text(c, "GUI work directory: "+valueOr(a.workDir, "not configured")).FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
	workspace := ct.SessionWorkingDir
	if workspace == "" {
		workspace = ct.Workspace
	}
	ui.Text(c, "Contact workspace: "+valueOr(workspace, "not configured")).FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
	session := ct.SessionID
	if session == "" {
		session = "not created until the first message"
	}
	state := ct.SessionState
	if state == "" {
		state = "unknown"
	}
	ui.Text(c, "Session: "+session+" · state: "+state).FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
	switch {
	case ct.SessionAPI != "":
		ui.Text(c, "Session API: "+ct.SessionAPI).FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
	case a.runtimeAPI != "":
		ui.Text(c, "Runtime API (connected): "+a.runtimeAPI+"; no session API loaded").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
	default:
		ui.Text(c, "Session API: unknown until the runtime session is loaded").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
	}
	ui.Text(c, "Session model: "+valueOr(ct.SessionModel, "unknown until the session is loaded")).FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
	ui.Text(c, "Host configuration: API_KEY is required; SAKI_API, SAKI_BASE_URL and SAKI_MODEL have runtime defaults. Set them in the environment or in the GUI work directory's .env; values stay hidden.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func toolTitle(m *message) string {
	name := m.ToolName
	if name == "" {
		name = "Tool"
	}
	if m.ToolArgs == "" {
		return name
	}
	var args map[string]any
	if json.Unmarshal([]byte(m.ToolArgs), &args) == nil {
		for _, key := range []string{"path", "command"} {
			if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
				return name + " · " + truncate(value, 70)
			}
		}
	}
	return name + " · " + truncate(m.ToolArgs, 70)
}

func toolOutputSummary(output string) string {
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
		prefix = "Failed: " + truncate(text, 120)
	}
	code, hasCode := value["exit_code"].(float64)
	timedOut, _ := value["timed_out"].(bool)
	switch {
	case timedOut && hasCode && code != 0:
		prefix = fmt.Sprintf("Timed out (exit %g)", code)
	case timedOut:
		prefix = "Timed out"
	case hasCode && code != 0:
		if prefix == "" {
			prefix = fmt.Sprintf("Failed (exit %g)", code)
		} else {
			prefix += fmt.Sprintf(" (exit %g)", code)
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
	return truncate(output, 300)
}

func truncate(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}
