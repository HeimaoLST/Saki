package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// Message typography is supplied at the view call site so the reader's
// parser, clipboard and file behavior stay untouched (#187 owns those files).
var messageMarkdownTheme = &markdown.Theme{FontSize: 15, LineHeight: 1.8}

func messageReaderOptions(plain bool) markdownReaderOptions {
	return markdownReaderOptions{PlainText: plain, CopyLabel: "Copy message", Theme: messageMarkdownTheme}
}

func (a *appModel) view(c *ui.Context) {
	// Context.begin resets this to the system theme on every pass, so this is
	// the live system appearance even while an explicit mode is shown.
	a.systemDark = c.Theme().Dark
	a.workTheme = resolveWorkbenchTheme(resolveAppearanceDark(a.appearanceMode, a.systemDark))
	t := a.workTheme.Theme
	c.SetTheme(t)
	ui.Box(c).Fill().Children(func() {
		ui.Row(c).Fill().Children(func() {
			a.layoutBody(c, t)
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
	ui.Column(c).FillWidth().FillHeight().Padding(10, 10).Gap(10).Background(a.workTheme.Rail).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Text(c, "Saki").FontSize(16).FontWeight(600).TextColor(t.Text)
			ui.Spacer(c)
			hide := secondaryButton(c, "Hide").OnClick(func() { a.toggleRail() })
			if a.focusRail {
				hide.Focus()
				a.focusRail = false
			}
		})
		ui.Text(c, "Contacts").FontSize(12).FontWeight(500).TextColor(t.TextMuted)
		secondaryButton(c, "Add contact").FillWidth().Radius(999).OnClick(func() { a.addDefaultContact() })
		ui.TextInput(c, &a.contactSearch).Label("Search contacts").FontSize(13).Placeholder("Name or purpose")
		ui.Text(c, "Contact scope").FontSize(12).TextColor(t.TextMuted)
		a.contactScopeSelect(c)
		visible := a.visibleContactIndices()
		if len(visible) == 0 {
			ui.Text(c, "No contacts match this scope and search.").FontSize(13).LineHeight(1.35).TextColor(t.TextMuted)
			ui.Spacer(c).Grow(1)
		} else {
			ui.Scroll(c).Grow(1).Children(func() {
				for _, i := range visible {
					a.railRow(c, t, i)
				}
			})
		}
		appearance := secondaryButton(c, "Appearance").FillWidth().OnClick(func() { a.openAppearance() })
		if a.focusAppearance {
			appearance.Focus()
			a.focusAppearance = false
		}
	})
}

func contactScopeChoiceLabel(scope string) string {
	switch scope {
	case "archived":
		return "Archived"
	case "all":
		return "All"
	default:
		return "Active"
	}
}

func contactScopeFromChoice(label string) string {
	switch label {
	case "Archived":
		return "archived"
	case "All":
		return "all"
	default:
		return "active"
	}
}

func (a *appModel) contactScopeSelect(c *ui.Context) {
	a.contactScopeChoice = contactScopeChoiceLabel(a.contactScope)
	choice := ui.Select(c, &a.contactScopeChoice, []string{"Active", "Archived", "All"}).
		Label("Contact scope").FontSize(13).FillWidth()
	if choice.Changed() {
		a.setContactScope(contactScopeFromChoice(a.contactScopeChoice))
	}
}

func (a *appModel) railRow(c *ui.Context, t *ui.Theme, i int) {
	ct := a.contacts[i]
	row := ui.ButtonBase(c).Label("Contact " + ct.Name).Gap(10).Padding(8).Radius(12).AlignItems(ui.Start).Background(ui.Transparent).BorderWidth(0)
	if i == a.selected {
		row.Background(t.SurfaceHover)
	}
	row.OnClick(func() { a.selectContact(i) })
	row.Children(func() {
		a.avatarTile(c, t, 34)
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
				ui.Text(c, ct.Name).FontSize(14).FontWeight(500).TextColor(t.Text)
				ui.Spacer(c)
				if workCount := issueWorkActiveCount(ct); workCount > 0 {
					ui.Text(c, fmt.Sprintf("%d work", workCount)).FontSize(12).TextColor(t.TextMuted)
				}
				if ct.Unread > 0 {
					ui.Text(c, fmt.Sprintf("%d", ct.Unread)).FontSize(12).TextColor(t.Warning)
				}
			})
			line := a.snippet(ct)
			color := t.TextMuted
			notice := a.issueWorkNotice(ct)
			if notice.Text != "" {
				line = "Issue running"
				if notice.Attention {
					line = "Attention"
					color = t.Warning
				} else {
					color = t.Success
				}
			}
			if ct.Status != statusReady && notice.Text == "" {
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
			ui.Text(c, line).FontSize(13).LineHeight(1.35).MaxLines(1).TextColor(color)
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
	ct := a.selectedContact()
	ui.Column(c).Grow(1).FillHeight().Background(a.workTheme.Reading).Children(func() {
		if !a.demo {
			switch {
			case a.runtimeStatus == runtimeStateDraining:
				ui.Text(c, "Draining the runtime…").FontSize(14).Padding(8, 16).TextColor(t.TextMuted)
			case a.runtimeErr != "":
				label := a.runtimeStage
				if label == "" {
					label = "Runtime"
				}
				ui.Column(c).Padding(8, 16).Gap(2).Children(func() {
					ui.Text(c, label+": "+a.runtimeErr).FontSize(14).LineHeight(1.4).TextColor(t.Danger)
					if a.runtimeHint != "" {
						ui.Text(c, a.runtimeHint).FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
					}
					ui.Row(c).Children(func() {
						if secondaryButton(c, "Configuration details").Clicked() {
							a.openConfiguration()
						}
					})
				})
			case a.runtimeStatus == runtimeStateStarting:
				ui.Text(c, "Connecting to runtime…").FontSize(14).Padding(8, 16).TextColor(t.TextMuted)
			}
		}
		a.connectionErrorBanner(c, t, ct)
		if ct.LoadErr != "" {
			if _, known := knownConnectionFailure(ct.LoadErr); !known {
				ui.Text(c, ct.LoadErr).FontSize(14).Padding(8, 16).TextColor(t.Warning)
			}
		}
		if ct.Archived {
			ui.Row(c).AlignItems(ui.Center).Gap(8).Padding(6, 16).Background(t.SurfaceHover).Children(func() {
				ui.Text(c, "Archived contact · sending and new issue runs are disabled. History, drafts and issue data are retained.").FontSize(14).TextColor(t.Warning)
				ui.Spacer(c)
				secondaryButton(c, "Restore contact").TextColor(t.Text).OnClick(func() { a.restoreContact(ct.ID) })
				secondaryButton(c, "Settings").OnClick(func() { a.openSettings() })
			})
		}
		a.header(c, t)
		a.chat(c, t)
	})
}

func (a *appModel) workEntryStatus(ct *contact) (string, bool) {
	if ct == nil {
		return "", false
	}
	if notice := a.issueWorkNotice(ct); notice.Text != "" {
		if notice.Attention {
			return "Work needs attention", true
		}
		return "Work running", false
	}
	if ct.activeWork() != nil || ct.Status == statusWorking {
		return "Work running", false
	}
	if ct.Status == statusAttention {
		return "Work needs attention", true
	}
	return "", false
}

func (a *appModel) workEntryHint(c *ui.Context, t *ui.Theme, ct *contact) {
	if a.workspaceOpen {
		return
	}
	label, attention := a.workEntryStatus(ct)
	if label == "" {
		return
	}
	color := t.Success
	if attention {
		color = t.Warning
	}
	ui.Text(c, label).FontSize(12).TextColor(color)
}

func (a *appModel) header(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	ui.Row(c).Justify(ui.Center).Children(func() {
		ui.Column(c).WidthPercent(86).MaxWidth(760).Padding(8, 0).Gap(6).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(9).Children(func() {
				a.avatarTile(c, t, 35)
				ui.Column(c).Gap(1).Children(func() {
					ui.Text(c, ct.Name).FontSize(17).FontWeight(600).TextColor(t.Text)
					ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
						ui.Text(c, a.statusLabel(ct)).FontSize(12).LineHeight(1.35).TextColor(a.statusColor(t, ct.Status))
						ui.Text(c, "· "+ct.Role).FontSize(12).LineHeight(1.35).MaxLines(1).TextColor(t.TextMuted)
					})
				})
			})
			nav := func(label string, active bool, focus *bool, onClick func()) {
				button := secondaryButton(c, label)
				if active {
					button.BorderWidth(1).BorderColor(t.Accent)
				}
				button.OnClick(onClick)
				if focus != nil && *focus {
					button.Focus()
					*focus = false
				}
			}
			if a.layoutWidth() < 820 {
				ui.Row(c).Gap(6).Children(func() {
					if a.railCollapsed {
						nav("Contacts", false, &a.focusRail, func() { a.toggleRail() })
					}
					nav("Work", a.workspaceOpen, &a.focusWorkspace, func() { a.toggleWorkspace() })
					a.workEntryHint(c, t, ct)
					nav("History", a.screen == viewHistory, &a.focusHistory, func() { a.openHistory() })
				})
				ui.Row(c).Gap(6).Children(func() {
					nav("Settings", a.aux == "settings", &a.focusSettings, func() { a.openSettings() })
				})
				return
			}
			ui.Row(c).Gap(6).Children(func() {
				if a.railCollapsed {
					nav("Contacts", false, &a.focusRail, func() { a.toggleRail() })
				}
				nav("Work", a.workspaceOpen, &a.focusWorkspace, func() { a.toggleWorkspace() })
				a.workEntryHint(c, t, ct)
				nav("History", a.screen == viewHistory, &a.focusHistory, func() { a.openHistory() })
				nav("Settings", a.aux == "settings", &a.focusSettings, func() { a.openSettings() })
			})
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
		if len(items) > 0 && !ct.list.AtEnd() {
			ui.Row(c).Key("back-to-latest").Justify(ui.Center).Children(func() {
				back := secondaryButton(c, "Back to latest")
				back.OnClick(func() {
					ct.list.ScrollToEnd()
					a.focusComposer = true
				})
			})
		}
		ui.Row(c).Key("composer").Justify(ui.Center).Children(func() {
			ui.Column(c).WidthPercent(86).MaxWidth(760).Children(func() {
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
					markdownReader(c, e.Text, messageReaderOptions(true)).
						FontSize(15).LineHeight(1.8).Padding(8, 12).Radius(12).Background(bg).TextColor(t.Text)
				}
				taskContextReadOnlyView(c, a.workTheme, e.Context)
			})
		})
		return
	}
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Start).Children(func() {
		a.avatarTile(c, t, 28)
		ui.Column(c).MaxWidth(680).MinWidth(0).Shrink(1).Padding(10, 14).Radius(14).Background(bg).Gap(4).Children(func() {
			ui.Text(c, a.selectedContact().Name+"  "+e.Date).FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
			markdownReader(c, e.Text, messageReaderOptions(false)).
				FontSize(15).LineHeight(1.8)
		})
	})
}

func (a *appModel) message(c *ui.Context, t *ui.Theme, ct *contact, m *message) {
	switch m.Kind {
	case msgActivity:
		if m.ToolName != "" || m.ToolOutput != "" {
			title := toolTitleFor(m.ToolName, m.ToolArgs)
			summary := toolOutputSummary(m.ToolOutput)
			if m.Expanded {
				summary = truncate(m.ToolOutput, 2000)
			}
			ui.Column(c).Gap(2).Padding(4, 0).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Text(c, title).FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
					if m.ToolOutput != "" {
						label := "Details"
						if m.Expanded {
							label = "Hide"
						}
						secondaryButton(c, label).Radius(999).OnClick(func() { m.Expanded = !m.Expanded })
					}
				})
				if summary != "" {
					lines := 2
					if m.Expanded {
						lines = 12
					}
					ui.Text(c, summary).FontSize(14).LineHeight(1.45).MaxLines(lines).TextColor(t.TextMuted)
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
					secondaryButton(c, label).OnClick(func() { m.Expanded = !m.Expanded })
				})
				if m.Expanded {
					for _, step := range m.Steps {
						ui.Text(c, "· "+step).FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
					}
					if len(m.Sources) > 0 {
						ui.Row(c).Children(func() {
							secondaryButton(c, "Open sources").OnClick(func() { a.openSources(m.ID) })
						})
					}
				}
			})
		})
	case msgUser:
		ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
			ui.Column(c).MaxWidthPercent(80).MinWidth(0).Shrink(1).Gap(2).Children(func() {
				if m.Key != "" && m.Key == a.highlight {
					ui.Text(c, "Located in history").FontSize(14).TextColor(t.TextMuted)
				}
				if m.Text != "" {
					markdownReader(c, m.Text, messageReaderOptions(true)).
						FontSize(15).LineHeight(1.8).Padding(10, 14).Radius(14).Background(a.workTheme.BubbleUser).TextColor(t.Text)
				}
				taskContextReadOnlyView(c, a.workTheme, m.Context)
			})
		})
	default:
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Start).Children(func() {
			a.avatarTile(c, t, 28)
			ui.Column(c).MaxWidthPercent(80).MinWidth(0).Shrink(1).Padding(10, 14).Radius(14).Background(a.workTheme.BubbleContact).Gap(4).Children(func() {
				if m.Key != "" && m.Key == a.highlight {
					ui.Text(c, "Located in history").FontSize(14).TextColor(t.TextMuted)
				}
				markdownReader(c, m.Text, markdownReaderOptions{Streaming: m.Streaming, CopyLabel: "Copy message", Theme: messageMarkdownTheme}).
					FontSize(15).LineHeight(1.8)
				if m.AlertTitle != "" {
					a.alert(c, t, m)
				}
				if len(m.Sources) > 0 {
					ui.Row(c).Children(func() {
						secondaryButton(c, fmt.Sprintf("%d earlier messages", len(m.Sources))).OnClick(func() { a.openSources(m.ID) })
					})
				}
			})
		})
	}
}

func (a *appModel) alert(c *ui.Context, t *ui.Theme, m *message) {
	ui.Column(c).Padding(8).Radius(10).Background(t.SurfaceHover).Gap(4).Children(func() {
		ui.Text(c, m.AlertTitle).FontSize(14).FontWeight(500).TextColor(t.Warning)
		if m.AlertDetail != "" {
			ui.Text(c, m.AlertDetail).FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
		}
		if m.Retry && !m.RetryDone {
			ui.Row(c).Children(func() {
				secondaryButton(c, "Try again").OnClick(func() { a.retry(m) })
			})
		} else if m.RetryDone {
			ui.Text(c, "Retried").FontSize(14).TextColor(t.TextMuted)
		}
	})
}

func (a *appModel) emptyState(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	ui.Column(c).Padding(8, 0).Gap(8).Children(func() {
		if !a.demo && ct.SessionID != "" && !ct.Loaded {
			ui.Text(c, "Conversation history is not loaded").FontSize(16).FontWeight(600).TextColor(t.Text)
			ui.Text(c, "This contact already has a session, but its history has not been read yet; it is not shown as empty.").FontSize(14).LineHeight(1.45).TextColor(t.TextMuted)
			if a.runtimeStatus != runtimeStateReady {
				ui.Text(c, "Runtime is not ready; history cannot be read yet.").FontSize(14).LineHeight(1.45).TextColor(t.Warning)
			}
			return
		}
		ui.Text(c, "No messages yet").FontSize(17).FontWeight(600).TextColor(t.Text)
		if a.demo {
			ui.Text(c, "Start typing below to talk with "+ct.Name+"; you can personalize name, purpose, instructions and workspace any time in Settings.").FontSize(14).LineHeight(1.45).TextColor(t.TextMuted)
		} else {
			ui.Text(c, "Start a conversation with "+ct.Name+". Edit this contact in Settings. Changes apply to new conversations.").FontSize(14).LineHeight(1.45).TextColor(t.TextMuted)
		}
		ui.Row(c).Children(func() {
			secondaryButton(c, "Settings").OnClick(func() { a.openSettings() })
		})
	})
}

func knownConnectionFailure(text string) (string, bool) {
	// runtimeMessage prefixes typed errors as "code: message". Match the code
	// exactly rather than treating an arbitrary diagnostic containing the word
	// as a known connection failure.
	if strings.HasPrefix(strings.TrimSpace(text), "connection_mismatch:") {
		return "Connection unavailable or changed. Review connection settings, or start a new conversation from the profile.", true
	}
	return "", false
}

func (a *appModel) connectionSendFailureKnown(ct *contact) bool {
	if ct == nil {
		return false
	}
	_, known := knownConnectionFailure(ct.SendError)
	return known
}

func (a *appModel) connectionErrorBanner(c *ui.Context, t *ui.Theme, ct *contact) {
	if ct == nil {
		return
	}
	summary, known := knownConnectionFailure(ct.LoadErr)
	if !known {
		summary, known = knownConnectionFailure(ct.SendError)
	}
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
		ui.Text(c, summary).FontSize(14).LineHeight(1.4).TextColor(t.Warning).MaxLines(3)
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			secondaryButton(c, "Connection settings").TextColor(t.Text).OnClick(func() { a.openConfiguration() })
			label := "Details"
			if expanded {
				label = "Hide details"
			}
			secondaryButton(c, label).OnClick(func() {
				if a.connectionDetailsOpen == nil {
					a.connectionDetailsOpen = map[string]bool{}
				}
				a.connectionDetailsOpen[ct.ID] = !a.connectionDetailsOpen[ct.ID]
			})
		})
		if expanded && details != "" {
			markdownReader(c, details, markdownReaderOptions{PlainText: true, CopyLabel: "Copy error details"}).
				FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
		}
	})
}

// ---------- composer ----------

func (a *appModel) composer(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	ui.Column(c).Padding(10, 0).Gap(4).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.End).Padding(6, 10).Radius(14).Background(a.workTheme.Well).Children(func() {
			input := ui.TextArea(c, &ct.Draft).Label("Composer for " + ct.Name).FontSize(15).LineHeight(1.6).Height(44).Background(ui.Transparent).BorderWidth(0).Padding(2).Placeholder("Message " + ct.Name).Grow(1).OnChange(func() {
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
			label := "Task context"
			if count := ct.Context.count(); count > 0 {
				label = fmt.Sprintf("Task context · %d", count)
			}
			secondaryButton(c, label).OnClick(func() { a.openConversationTaskContext() })
			send := ui.PrimaryButton(c, "Send").FontSize(13).MinHeight(32).Padding(3, 10).Disabled(sendDisabled)
			if send.Clicked() {
				a.sendCurrent()
			}
		})
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			secondaryButton(c, "Conversation model").OnClick(func() {
				a.openSessionModel(ct.SessionID, "conversation", ct.Name)
			})
			ui.Text(c, a.conversationModelSummary(ct)).FontSize(12).LineHeight(1.35).MaxLines(1).TextColor(t.TextMuted).Grow(1)
		})
		if !a.demo && ct.ActiveTaskID != "" {
			ui.Row(c).Justify(ui.End).Children(func() {
				secondaryButton(c, "Stop conversation").TextColor(t.Warning).OnClick(func() { a.cancelSelected() })
			})
		}
		if !a.demo {
			if !a.capacityKnown() {
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					message := a.runtimeCapabilitiesErr
					if message == "" {
						message = "Runtime task capacity is not loaded; using the conservative single-slot behavior."
					}
					ui.Text(c, message).FontSize(14).LineHeight(1.4).TextColor(t.Warning).Grow(1)
					secondaryButton(c, "Retry capacity").OnClick(func() { a.retryCapabilities() })
				})
			}
			if reason := a.canSend(ct); strings.Contains(reason, "Another contact is working") {
				ui.Text(c, reason).FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
			}
			switch {
			case ct.SendState == sendSubmitting:
				ui.Text(c, "Sending…").FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
			case ct.SendState == sendAccepted && ct.Status != statusWorking:
				ui.Text(c, "Accepted; waiting for the runtime to start.").FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
			case ct.SendState == sendUnknown:
				if a.runtimeStatus == runtimeStateReady && a.rt != nil {
					ui.Text(c, "Delivery not confirmed. Reload to verify before sending again.").FontSize(14).LineHeight(1.4).TextColor(t.Danger)
				} else {
					ui.Text(c, "Runtime disconnected; restart Saki to check the task and history.").FontSize(14).LineHeight(1.4).TextColor(t.Danger)
				}
			}
			if ct.SendError != "" && !a.connectionSendFailureKnown(ct) {
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, ct.SendError).FontSize(14).LineHeight(1.4).TextColor(t.Warning)
					if (ct.SendState == sendUnknown || ct.SendError != "") && a.runtimeStatus == runtimeStateReady && a.rt != nil {
						if secondaryButton(c, "Reload").Clicked() {
							a.reloadSelected()
						}
					}
				})
			}
			if ct.StatusDetail != "" {
				ui.Text(c, ct.StatusDetail).FontSize(14).LineHeight(1.4).TextColor(t.Warning)
			}
		}
		ui.Text(c, "Enter to send · Shift + Enter for a new line").FontSize(12).LineHeight(1.4).TextColor(t.TextMuted)
	})
}

// ---------- history ----------

func (a *appModel) historyView(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	open := a.screen == viewHistory
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.28))
		panel.Width(560).FillHeight().Background(a.workTheme.Panel).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Padding(12, 12).Children(func() {
				ui.Text(c, "History · "+ct.Name).FontSize(17).FontWeight(600).TextColor(t.Text)
				ui.Spacer(c)
				close := secondaryButton(c, "Close history").OnClick(func() {
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
					ui.Text(c, "Searching only "+ct.Name+"'s conversation history. Messages from other contacts are never included.").FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
					if !a.demo && ct.SessionID != "" && !ct.Loaded {
						ui.Text(c, "History is not loaded yet; existing records are not shown as absent.").FontSize(14).LineHeight(1.4).TextColor(t.Warning)
					}
					ui.TextInput(c, &a.historyQuery).FontSize(14).Placeholder("Search " + ct.Name + "'s history")
					if !a.demo {
						secondaryButton(c, "Search saved contexts").TextColor(t.Text).OnClick(func() { a.searchContactHistoryAsync(a.historyQuery) })
						if a.contactHistoryBusy {
							ui.Text(c, "Searching saved contexts…").FontSize(14).TextColor(t.TextMuted)
						}
						if a.contactHistoryError != "" {
							ui.Text(c, a.contactHistoryError).FontSize(14).LineHeight(1.4).TextColor(t.Warning)
						}
						if a.contactHistory != nil {
							ui.Text(c, a.contactHistorySummary()).FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
							if len(a.contactHistory.Failures) > 0 {
								for _, failure := range a.contactHistory.Failures {
									ui.Text(c, "Saved context unavailable: "+failure.SessionID).FontSize(14).LineHeight(1.4).TextColor(t.Warning)
								}
							}
							for _, match := range a.contactHistory.Matches {
								match := match
								ui.Column(c).FillWidth().MinWidth(0).Padding(10).Radius(10).Background(t.SurfaceHover).Gap(4).Children(func() {
									ui.Text(c, match.Date+" · saved context "+match.SessionID).FontSize(14).LineHeight(1.4).TextColor(t.Success)
									ui.Text(c, match.Preview).FontSize(14).LineHeight(1.5).TextColor(t.Text)
									secondaryButton(c, "View full message").OnClick(func() { a.openContactHistoryMatch(match) })
								})
							}
						}
						if a.contactHistoryDetailBusy {
							ui.Text(c, "Opening the full saved message…").FontSize(14).TextColor(t.TextMuted)
						}
						if a.contactHistoryFull != nil {
							full := a.contactHistoryFull
							from := "Contact"
							if full.From == "me" {
								from = "You"
							}
							ui.Column(c).FillWidth().MinWidth(0).Padding(10).Radius(10).Background(t.SurfaceHover).Gap(4).Children(func() {
								ui.Text(c, "Full saved message · "+from+" · "+full.Date+" · "+full.SessionID).FontSize(14).LineHeight(1.4).TextColor(t.Success)
								markdownReader(c, full.Text, markdownReaderOptions{CopyLabel: "Copy saved message", Theme: messageMarkdownTheme}).FontSize(15).LineHeight(1.8).TextColor(t.Text)
								taskContextReadOnlyView(c, a.workTheme, full.Context)
								secondaryButton(c, "Close full message").OnClick(func() { a.contactHistoryFull = nil })
							})
						}
					}
					matches := a.historyMatches()
					if len(matches) == 0 {
						if strings.TrimSpace(a.historyQuery) == "" {
							ui.Text(c, "No saved messages in "+ct.Name+"'s loaded history.").FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
						} else {
							ui.Text(c, "No messages in "+ct.Name+"'s loaded history match “"+a.historyQuery+"”.").FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
						}
					}
					for _, e := range matches {
						e := e
						ui.Column(c).FillWidth().MinWidth(0).Padding(10).Radius(10).Background(t.SurfaceHover).Gap(4).Children(func() {
							ui.Text(c, e.Date+" · "+speakerLabel(ct, e)).FontSize(14).LineHeight(1.4).TextColor(t.Success)
							if e.From == "me" {
								markdownReader(c, e.Text, markdownReaderOptions{PlainText: true, CopyLabel: "Copy history message", Theme: messageMarkdownTheme}).FontSize(15).LineHeight(1.8).TextColor(t.Text)
							} else {
								markdownReader(c, e.Text, markdownReaderOptions{CopyLabel: "Copy history message", Theme: messageMarkdownTheme}).FontSize(15).LineHeight(1.8)
							}
							taskContextReadOnlyView(c, a.workTheme, e.Context)
							ui.Row(c).Children(func() {
								secondaryButton(c, "View in conversation").OnClick(func() { a.viewInConversation(e.ID) })
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
	width := float32(340)
	if returnAux == "configuration" || returnAux == "model" || returnAux == "task-context" {
		width = 560
	}
	if returnAux == "issue-result" {
		width = float32(a.issueResultPanelWidth())
	}
	open := true
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.28))
		panel.Width(width).FillHeight().Background(a.workTheme.Panel).Children(func() {
			ui.Scroll(c).Grow(1).Padding(12).Children(func() {
				ui.Column(c).Gap(10).Children(func() {
					switch returnAux {
					case "sources":
						a.sourcesPanel(c, t)
					case "configuration":
						a.configurationPanel(c, t)
					case "model":
						a.modelPanel(c, t)
					case "task-context":
						a.taskContextPanel(c, t)
					case "appearance":
						a.appearancePanel(c, t)
					case "issue-result":
						a.issueResultPanelView(c, t)
					default:
						a.settingsPanel(c, t)
					}
				})
			})
		})
	})
	if !open {
		if returnAux == "model" && a.modelOpActive {
			return
		}
		if returnAux == "issue-result" {
			a.cancelIssueResultView()
		}
		a.aux = ""
		if returnAux == "task-context" {
			a.taskContextIssueID = ""
		}
		switch returnAux {
		case "settings":
			a.focusSettings = true
		case "sources":
			a.focusHistory = false
		case "appearance":
			a.focusAppearance = true
		}
	}
}

func (a *appModel) sourcesPanel(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	entries := a.sourceEntries(a.auxMessage)
	ui.Row(c).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "Sources").FontSize(17).FontWeight(600).TextColor(t.Text)
		ui.Spacer(c)
		secondaryButton(c, "Close").OnClick(func() { a.closeAux() })
	})
	ui.Text(c, fmt.Sprintf("%d earlier messages from %s's history, cited in this reply.", len(entries), ct.Name)).FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
	for _, e := range entries {
		e := e
		ui.Column(c).FillWidth().MinWidth(0).Padding(10).Radius(10).Background(t.SurfaceHover).Gap(4).Children(func() {
			ui.Text(c, e.Date+" · "+speakerLabel(ct, e)).FontSize(14).LineHeight(1.4).TextColor(t.Success)
			if e.From == "me" {
				markdownReader(c, e.Text, markdownReaderOptions{PlainText: true, CopyLabel: "Copy source message", Theme: messageMarkdownTheme}).FontSize(15).LineHeight(1.8).TextColor(t.Text)
			} else {
				markdownReader(c, e.Text, markdownReaderOptions{CopyLabel: "Copy source message", Theme: messageMarkdownTheme}).FontSize(15).LineHeight(1.8)
			}
			ui.Row(c).Children(func() {
				secondaryButton(c, "View in conversation").OnClick(func() { a.viewInConversation(e.ID) })
			})
		})
	}
}

func (a *appModel) settingsPanel(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	if a.contactProfile == nil || a.contactProfileID != ct.ID {
		a.openContactProfile(ct.ID)
	}
	editor := a.contactProfile
	editor.Archived = ct.Archived
	editor.HasConversation = ct.SessionID != ""
	editor.ActiveWork, editor.UnknownWork = a.contactActivity(ct)
	ui.Row(c).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "Contact settings").FontSize(17).FontWeight(600).TextColor(t.Text)
		ui.Spacer(c)
		if !a.demo {
			secondaryButton(c, "Startup status").OnClick(func() { a.openConfiguration() })
		}
		secondaryButton(c, "Close").OnClick(func() { a.closeAux() })
	})
	editor.view(c, t, contactProfileActions{
		Save: func(draft profileDraft) {
			a.saveContactProfileDraft(draft, false)
		},
		SaveAndNew: func(draft profileDraft) {
			a.saveContactProfileDraft(draft, true)
		},
		Archive: func() {
			a.archiveContact(ct.ID)
		},
		Restore: func() {
			a.restoreContact(ct.ID)
		},
		Cancel: func() { a.closeAux() },
	})
}

func (a *appModel) openConfiguration() {
	a.ensureConnectionForm()
	a.aux = "configuration"
	if a.store == nil {
		a.startupDetailsOpen = true
	}
}

func (a *appModel) configurationPanel(c *ui.Context, t *ui.Theme) {
	a.connectionPanel(c, t)
	label := "Startup details"
	if a.startupDetailsOpen {
		label = "Hide startup details"
	}
	secondaryButton(c, label).OnClick(func() { a.startupDetailsOpen = !a.startupDetailsOpen })
	if !a.startupDetailsOpen {
		return
	}
	ui.Text(c, "Startup & configuration").FontSize(16).FontWeight(500).TextColor(t.Text)
	a.runtimePanel(c, t, a.selectedContact())
}

func (a *appModel) runtimePanel(c *ui.Context, t *ui.Theme, ct *contact) {
	localStore := a.runtimeStage == startupStageLocalStore
	if localStore {
		ui.Text(c, "Runtime: not started (local contact store unavailable)").FontSize(14).LineHeight(1.4).TextColor(t.Text)
	} else {
		status := a.runtimeStatus
		if status == "" {
			status = "unknown"
		}
		if status == runtimeStateReady {
			status += " (connection only)"
		}
		ui.Text(c, "Runtime: "+status).FontSize(14).LineHeight(1.4).TextColor(t.Text)
	}
	if a.runtimeErr != "" {
		label := a.runtimeStage
		if label == "" {
			label = "Runtime"
		}
		ui.Text(c, label+": "+a.runtimeErr).FontSize(14).LineHeight(1.4).TextColor(t.Danger)
	}
	if a.runtimeHint != "" {
		ui.Text(c, a.runtimeHint).FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
	}
	helper := a.runtimeBinary
	if helper == "" {
		if localStore {
			helper = "not started; fix the local contact store first"
		} else {
			helper = "not found (expected next to Saki as Saki-runtime, or set SAKI_RUNTIME_BIN)"
		}
	}
	ui.Text(c, "Helper: "+helper).FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
	ui.Text(c, "Store: "+valueOr(a.storePath, "not configured")).FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
	ui.Text(c, "Session directory: "+valueOr(a.sessionDir, "not configured")).FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
	ui.Text(c, "GUI work directory: "+valueOr(a.workDir, "not configured")).FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
	workspace := ct.SessionWorkingDir
	if workspace == "" {
		workspace = a.effectiveContactWorkspace(ct)
	}
	ui.Text(c, "Contact workspace: "+valueOr(workspace, "not configured")).FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
	session := ct.SessionID
	if session == "" {
		session = "not created until the first message"
	}
	state := ct.SessionState
	if state == "" {
		state = "unknown"
	}
	ui.Text(c, "Session: "+session+" · state: "+state).FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
	switch {
	case ct.SessionAPI != "":
		ui.Text(c, "Session API: "+ct.SessionAPI).FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
	case a.runtimeAPI != "":
		ui.Text(c, "Runtime API (connected): "+a.runtimeAPI+"; no session API loaded").FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
	default:
		ui.Text(c, "Session API: unknown until the runtime session is loaded").FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
	}
	ui.Text(c, "Session model: "+valueOr(ct.SessionModel, "unknown until the session is loaded")).FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
	if a.connectionManaged || (a.connectionForm != nil && a.connectionForm.HasSaved) {
		ui.Text(c, "Connection settings are managed by Nagi; the runtime reads its endpoint, model and key from that saved connection.").FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
	} else {
		ui.Text(c, "Host configuration: API_KEY is required; SAKI_API, SAKI_BASE_URL and SAKI_MODEL have runtime defaults. Set them in the environment or in the GUI work directory's .env; values stay hidden.").FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
	}
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
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
	// Structured tool results such as write/edit receipts have no readable
	// assistant body. Keep the exact record in Details/Copy, but do not present
	// raw JSON as a result preview.
	return "Structured output received; open Details for the exact record."
}

func truncate(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}
