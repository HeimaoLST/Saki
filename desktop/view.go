package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

func (a *appModel) view(c *ui.Context) {
	t := roseTheme(c.Theme().Dark)
	c.SetTheme(t)
	ui.Row(c).Fill().Children(func() {
		a.rail(c, t)
		a.main(c, t)
		if a.aux != "" {
			a.auxView(c, t)
		}
	})
}

// ---------- rail ----------

func (a *appModel) rail(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Width(220).FillHeight().Padding(10).Gap(6).Background(t.Background).Children(func() {
		ui.Text(c, "Saki").FontSize(15).FontWeight(500).TextColor(t.Text)
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "CONTACTS").FontSize(12).TextColor(t.TextMuted)
			ui.Spacer(c)
			ui.Button(c, "Add contact").FontSize(12).OnClick(func() { a.addDefaultContact() })
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
	row := ui.ButtonBase(c).Label("Contact " + ct.Name).Gap(8).Padding(6).Radius(8).AlignItems(ui.Start).Background(ui.Transparent).BorderWidth(0)
	if i == a.selected {
		row.Background(ui.RGBA(t.Accent.R, t.Accent.G, t.Accent.B, 0.14))
		row.BorderWidth(0, 0, 0, 2).BorderColor(t.Accent)
	}
	row.OnClick(func() { a.selectContact(i) })
	row.Children(func() {
		a.avatarTile(c, t, 28)
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, ct.Name).FontSize(14).FontWeight(500).TextColor(t.Text)
				ui.Spacer(c)
				if ct.Unread > 0 {
					ui.Text(c, fmt.Sprintf("%d", ct.Unread)).FontSize(12).TextColor(t.Accent)
				}
			})
			ui.Text(c, a.snippet(ct)).FontSize(12).LineHeight(1.4).MaxLines(2).TextColor(t.TextMuted)
			if ct.Status != statusReady {
				ui.Text(c, a.statusLabel(ct)).FontSize(12).TextColor(a.statusColor(t, ct.Status))
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

func (a *appModel) main(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Grow(1).FillHeight().Background(t.Surface).Children(func() {
		if !a.demo {
			if a.runtimeStatus == runtimeStateDraining {
				ui.Text(c, "Draining the runtime…").FontSize(12).Padding(8, 16).TextColor(t.TextMuted)
			} else if a.runtimeErr != "" {
				ui.Text(c, a.runtimeErr).FontSize(12).LineHeight(1.4).Padding(8, 16).TextColor(t.Danger)
			} else if a.runtimeStatus == runtimeStateStarting {
				ui.Text(c, "Connecting to runtime…").FontSize(12).Padding(8, 16).TextColor(t.TextMuted)
			}
			if ct := a.selectedContact(); ct.LoadErr != "" {
				ui.Text(c, "Contact load: "+ct.LoadErr).FontSize(12).Padding(8, 16).TextColor(t.Warning)
			}
		}
		a.header(c, t)
		ui.Divider(c)
		switch a.screen {
		case viewHistory:
			a.historyView(c, t)
		default:
			a.chat(c, t)
		}
	})
}

func (a *appModel) header(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	ui.Row(c).Padding(12, 16).Gap(10).AlignItems(ui.Center).Children(func() {
		a.avatarTile(c, t, 36)
		ui.Column(c).Children(func() {
			ui.Text(c, ct.Name).FontSize(16).FontWeight(500).TextColor(t.Text)
			ui.Text(c, a.statusLabel(ct)+" · "+ct.Role).FontSize(12).LineHeight(1.4).TextColor(a.statusColor(t, ct.Status))
		})
		ui.Spacer(c)
		ui.Row(c).Gap(6).Children(func() {
			ui.Button(c, "History").FontSize(12).OnClick(func() { a.openHistory() })
			ui.Button(c, "Settings").FontSize(12).OnClick(func() { a.openSettings() })
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
	if ct.Status == statusWorking {
		items = append(items, chatItem{id: "working", working: true})
	}
	ct.list.Key = func(i int) any { return items[i].id }
	ct.list.FollowEnd = true
	if a.scrollTo >= 0 {
		ct.list.ScrollTo(a.scrollTo, ui.Start)
		a.scrollTo = -1
	}
	ui.Column(c).Grow(1).Children(func() {
		if len(items) == 0 {
			ui.Scroll(c).Grow(1).Padding(16).Children(func() {
				a.emptyState(c, t)
			})
		} else {
			ui.List(c, &ct.list, len(items), func(i int) {
				it := items[i]
				switch {
				case it.entry != nil:
					a.historyMessage(c, t, *it.entry, it.entry.ID == a.highlight)
				case it.working:
					a.workingRow(c, t, ct)
				default:
					a.message(c, t, ct, it.msg)
				}
			}).Grow(1).Padding(16, 16).Gap(16)
		}
		a.composer(c, t)
	})
}

func (a *appModel) historyMessage(c *ui.Context, t *ui.Theme, e historyEntry, highlight bool) {
	bg := t.Surface
	if highlight {
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
		ui.Column(c).MaxWidth(680).Padding(4).Radius(10).Background(bg).Gap(4).Children(func() {
			ui.Text(c, a.selectedContact().Name+"  "+e.Date).FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
			ui.Text(c, e.Text).FontSize(14).LineHeight(1.5).TextColor(t.Text)
		})
	})
}

func (a *appModel) message(c *ui.Context, t *ui.Theme, ct *contact, m *message) {
	switch m.Kind {
	case msgActivity:
		if m.ToolName != "" || m.ToolOutput != "" {
			label := m.ToolName
			if label == "" {
				label = "Tool"
			}
			if m.ToolArgs != "" {
				label += " " + truncate(m.ToolArgs, 120)
			}
			ui.Row(c).Gap(10).AlignItems(ui.Start).Children(func() {
				ui.Column(c).Gap(3).Children(func() {
					ui.Text(c, label).FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
					if m.ToolOutput != "" {
						ui.Text(c, truncate(m.ToolOutput, 300)).FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
					}
				})
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
			ui.Column(c).MaxWidth(460).Gap(4).Children(func() {
				if m.Key != "" && m.Key == a.highlight {
					ui.Text(c, "Located in history").FontSize(11).TextColor(t.Accent)
				}
				ui.Text(c, m.Text).FontSize(14).LineHeight(1.5).Padding(8, 12).Radius(12).Background(t.Selection).TextColor(t.Text)
				ui.Text(c, "You · "+m.Time).FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
			})
		})
	default:
		ui.Row(c).Gap(10).AlignItems(ui.Start).Children(func() {
			a.avatarTile(c, t, 28)
			ui.Column(c).MaxWidth(680).Gap(4).Children(func() {
				if m.Key != "" && m.Key == a.highlight {
					ui.Text(c, "Located in history").FontSize(11).TextColor(t.Accent)
				}
				ui.Text(c, ct.Name+"  "+m.Time).FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
				ui.Text(c, m.Text).FontSize(14).LineHeight(1.5).TextColor(t.Text)
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

func (a *appModel) workingRow(c *ui.Context, t *ui.Theme, ct *contact) {
	ui.Row(c).Gap(10).AlignItems(ui.Start).Children(func() {
		a.avatarTile(c, t, 28)
		ui.Text(c, ct.Name+" · "+ct.Work+"…").FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
	})
}

func (a *appModel) emptyState(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	ui.Column(c).Padding(16).Gap(6).Children(func() {
		ui.Text(c, "No messages yet").FontSize(16).FontWeight(500).TextColor(t.Text)
		if a.demo {
			ui.Text(c, "Start typing below to talk with "+ct.Name+"; you can personalize name, purpose, instructions and workspace any time in Settings.").FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
		} else {
			ui.Text(c, "Start typing below to talk with "+ct.Name+". Set purpose, instructions and workspace before the first message; after that only the display name can change.").FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
		}
		ui.Row(c).Children(func() {
			ui.Button(c, "Settings").FontSize(12).OnClick(func() { a.openSettings() })
		})
	})
}

// ---------- composer ----------

func (a *appModel) composer(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	ui.Column(c).Padding(8, 16).Gap(4).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.End).Padding(6, 8).Radius(12).Background(t.SurfaceHover).Children(func() {
			input := ui.TextArea(c, &ct.Draft).Label("Composer for " + ct.Name).FontSize(14).LineHeight(1.5).Background(ui.Transparent).BorderWidth(0).Padding(2).Placeholder("Message " + ct.Name).Grow(1).OnChange(func() {
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
			send := ui.PrimaryButton(c, "Send").FontSize(13).Disabled(sendDisabled)
			if send.Clicked() {
				a.sendCurrent()
			}
			if !a.demo && ct.ActiveTaskID != "" {
				if ui.Button(c, "Stop").FontSize(13).Clicked() {
					a.cancelSelected()
				}
			}
		})
		if !a.demo {
			switch {
			case ct.SendState == sendSubmitting:
				ui.Text(c, "Sending…").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
			case ct.SendState == sendAccepted:
				ui.Text(c, "Accepted; waiting for the runtime to start.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
			case ct.SendState == sendUnknown:
				ui.Text(c, "Delivery not confirmed. Reload to verify before sending again.").FontSize(11).LineHeight(1.4).TextColor(t.Danger)
			}
			if ct.SendError != "" {
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, ct.SendError).FontSize(11).LineHeight(1.4).TextColor(t.Warning)
					if ct.SendState == sendUnknown || ct.SendError != "" {
						if ui.Button(c, "Reload").FontSize(11).Clicked() {
							a.reloadSelected()
						}
					}
				})
			}
			if ct.StatusDetail != "" {
				ui.Text(c, ct.StatusDetail).FontSize(11).LineHeight(1.4).TextColor(t.Warning)
			}
		}
		ui.Text(c, "Enter to send · Shift + Enter for a new line").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
	})
}

// ---------- history ----------

func (a *appModel) historyView(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	ui.Scroll(c).Grow(1).Padding(16).Children(func() {
		ui.Column(c).MaxWidth(680).Gap(10).Children(func() {
			ui.Row(c).Children(func() {
				ui.Button(c, "Back to conversation").FontSize(12).OnClick(func() { a.backToChat() })
			})
			ui.Text(c, "History · "+ct.Name).FontSize(16).FontWeight(500).TextColor(t.Text)
			ui.Text(c, "Searching only "+ct.Name+"'s conversation history. Messages from other contacts are never included.").FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
			ui.TextInput(c, &a.historyQuery).FontSize(14).Placeholder("Search " + ct.Name + "'s history")
			matches := a.historyMatches()
			if len(matches) == 0 {
				ui.Text(c, "No messages in "+ct.Name+"'s history match “"+a.historyQuery+"”. Try a shorter phrase.").FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
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
}

func speakerLabel(c *contact, e historyEntry) string {
	if e.From == "me" {
		return "You"
	}
	return c.Name
}

// ---------- aux panel ----------

func (a *appModel) auxView(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Width(300).FillHeight().Background(t.Surface).BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() {
		ui.Scroll(c).Grow(1).Padding(12).Children(func() {
			ui.Column(c).Gap(10).Children(func() {
				if a.aux == "sources" {
					a.sourcesPanel(c, t)
					return
				}
				a.settingsPanel(c, t)
			})
		})
	})
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
	ui.TextInput(c, &a.settingsDraft.Workspace).Label("Contact workspace").FontSize(14).Placeholder("~/Workspace/my-project").Disabled(locked)
	ui.Row(c).Gap(8).Children(func() {
		ui.Button(c, "Cancel").FontSize(13).OnClick(func() { a.closeAux() })
		ui.PrimaryButton(c, "Save changes").FontSize(13).OnClick(func() { a.saveSettings() })
	})
}

func truncate(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}
