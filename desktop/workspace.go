package main

import (
	"encoding/json"
	"strings"

	"github.com/egoist/mygo/ui"
)

// A workItem is the GUI projection of one runtime task. User/assistant text
// stays in chat; tool activity is attached here by task/call identity.
type workState string

const (
	workAccepted    workState = "accepted"
	workRunning     workState = "running"
	workStopping    workState = "stopping"
	workCompleted   workState = "completed"
	workFailed      workState = "failed"
	workInterrupted workState = "interrupted"
	workUnknown     workState = "unknown"
)

type workTool struct {
	CallID   string
	Name     string
	Args     string
	Output   string
	Started  bool
	Finished bool
	Expanded bool
}

type workItem struct {
	TaskID    string
	MessageID string
	Prompt    string
	State     workState
	Detail    string
	Tools     []*workTool
}

func (a *appModel) layoutWidth() int {
	if a.windowWidth > 0 {
		return a.windowWidth
	}
	return 1040
}

func (a *appModel) wideLayout() bool { return a.layoutWidth() >= 1120 }

func (a *appModel) railWidth() float32 {
	if a.wideLayout() {
		return 200
	}
	return 160
}

func (a *appModel) workWidth() float32 {
	if a.wideLayout() {
		return 360
	}
	return 320
}

func (a *appModel) workOverlay() bool { return a.layoutWidth() < 960 }

func workFor(ct *contact, taskID string) *workItem {
	if ct == nil || taskID == "" {
		return nil
	}
	for _, w := range ct.Works {
		if w.TaskID == taskID {
			return w
		}
	}
	return nil
}

func (a *appModel) ensureWork(ct *contact, taskID, messageID string) *workItem {
	if w := workFor(ct, taskID); w != nil {
		if w.MessageID == "" {
			w.MessageID = messageID
		}
		return w
	}
	w := &workItem{TaskID: taskID, MessageID: messageID, State: workAccepted}
	ct.Works = append(ct.Works, w)
	return w
}

func (ct *contact) activeWork() *workItem { return workFor(ct, ct.ActiveTaskID) }

func (ct *contact) recentWork() *workItem {
	if w := workFor(ct, ct.LastTaskID); w != nil {
		return w
	}
	for i := len(ct.Works) - 1; i >= 0; i-- {
		if ct.Works[i].TaskID != "" {
			return ct.Works[i]
		}
	}
	return nil
}

func workStateFromTask(status string) workState {
	switch status {
	case "completed":
		return workCompleted
	case "failed":
		return workFailed
	case "interrupted":
		return workInterrupted
	case "running":
		return workRunning
	case "unknown":
		return workUnknown
	default:
		return workUnknown
	}
}

func workStateLabel(state workState) string {
	switch state {
	case workAccepted:
		return "Accepted"
	case workRunning:
		return "Working"
	case workStopping:
		return "Stopping…"
	case workCompleted:
		return "Completed"
	case workFailed:
		return "Failed"
	case workInterrupted:
		return "Interrupted"
	default:
		return "Result unknown"
	}
}

func (a *appModel) workStateColor(t *ui.Theme, state workState) ui.Color {
	switch state {
	case workRunning, workAccepted:
		return t.Accent
	case workStopping, workFailed:
		return t.Warning
	case workCompleted:
		return t.Success
	case workInterrupted:
		return t.TextMuted
	default:
		return t.Warning
	}
}

func (a *appModel) toggleWorkspace() {
	a.workspaceOpen = !a.workspaceOpen
	if a.workspaceOpen {
		a.focusWorkspaceOpen = true
	} else {
		a.focusWorkspace = true
	}
}

func (a *appModel) closeWorkspace() {
	a.workspaceOpen = false
	a.focusWorkspace = true
}

func (a *appModel) toggleRail() {
	a.railCollapsed = !a.railCollapsed
	a.focusRail = true
}

// workspaceFrame draws the current/recent task view. At narrow widths the
// same frame is absolutely positioned over the conversation by the caller.
func (a *appModel) workspaceFrame(c *ui.Context, t *ui.Theme, width float32, overlay bool) {
	col := ui.Column(c).Width(width).FillHeight().Background(t.Surface).BorderWidth(0, 0, 0, 1).BorderColor(t.Border)
	if overlay {
		col = col.Absolute().Right(0).Top(0)
	}
	col.Children(func() {
		ct := a.selectedContact()
		current := ct.activeWork()
		recent := ct.recentWork()
		title := "Recent work"
		work := recent
		if current != nil {
			title = "Current work"
			work = current
		}
		ui.Row(c).AlignItems(ui.Center).Padding(12, 12).Children(func() {
			ui.Text(c, "Work").FontSize(16).FontWeight(600).TextColor(t.Text)
			ui.Spacer(c)
			close := ui.Button(c, "Close work").FontSize(12).OnClick(func() { a.closeWorkspace() })
			if a.focusWorkspaceOpen {
				close.Focus()
				a.focusWorkspaceOpen = false
			}
		})
		if work == nil {
			ui.Scroll(c).Grow(1).Padding(12).Children(func() {
				ui.Column(c).Gap(8).Children(func() {
					ui.Text(c, "No current work").FontSize(14).FontWeight(500).TextColor(t.Text)
					ui.Text(c, "Work will appear here when "+ct.Name+" starts a task. Tool activity and results stay out of the conversation.").FontSize(12).LineHeight(1.45).TextColor(t.TextMuted)
				})
			})
			return
		}
		ui.Scroll(c).Grow(1).Padding(12).Children(func() {
			ui.Column(c).Gap(10).Children(func() {
				ui.Text(c, title).FontSize(11).FontWeight(500).TextColor(t.TextMuted)
				a.workCard(c, t, work, current != nil)
				a.workActivity(c, t, work)
			})
		})
	})
}

func (a *appModel) workCard(c *ui.Context, t *ui.Theme, w *workItem, isCurrent bool) {
	ui.Column(c).Padding(12).Radius(12).Background(t.SurfaceHover).BorderWidth(1).BorderColor(t.Border).Gap(6).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, workStateLabel(w.State)).FontSize(14).FontWeight(600).TextColor(a.workStateColor(t, w.State))
			ui.Spacer(c)
			if isCurrent && w.State != workStopping && w.State != workCompleted && w.State != workFailed && w.State != workInterrupted && w.State != workUnknown {
				label := "Stop task"
				if w.State == workStopping {
					label = "Stopping…"
				}
				stop := ui.Button(c, label).FontSize(12).OnClick(func() { a.cancelSelected() })
				if w.State == workStopping {
					stop.Disabled(true)
				}
			}
		})
		if w.Prompt != "" {
			ui.Text(c, w.Prompt).FontSize(13).LineHeight(1.5).MaxLines(4).TextColor(t.Text)
		}
		if w.Detail != "" {
			ui.Text(c, w.Detail).FontSize(12).LineHeight(1.45).TextColor(t.Warning)
		}
	})
}

func (a *appModel) workActivity(c *ui.Context, t *ui.Theme, w *workItem) {
	ui.Text(c, "Activity").FontSize(11).FontWeight(500).TextColor(t.TextMuted)
	if len(w.Tools) == 0 {
		ui.Text(c, "No tool activity yet.").FontSize(12).TextColor(t.TextMuted)
		return
	}
	for _, call := range w.Tools {
		call := call
		ui.Column(c).Padding(8).Radius(8).Background(t.SurfaceHover).Gap(3).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Text(c, toolTitleFor(call.Name, call.Args)).FontSize(12).LineHeight(1.4).TextColor(t.Text)
				ui.Spacer(c)
				state := "Running"
				if call.Finished {
					state = "Finished"
				}
				ui.Text(c, state).FontSize(11).TextColor(t.TextMuted)
			})
			if call.Output != "" {
				summary := toolOutputSummary(call.Output)
				label := "Details"
				if call.Expanded {
					label = "Hide"
				}
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, summary).FontSize(11).LineHeight(1.4).MaxLines(4).TextColor(t.TextMuted).Grow(1)
					ui.Button(c, label).FontSize(12).Padding(3, 8).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { call.Expanded = !call.Expanded })
				})
				if call.Expanded {
					ui.Text(c, truncate(call.Output, 6000)).FontSize(11).LineHeight(1.4).TextColor(t.TextMuted).Selectable()
				}
			} else if call.Started && !call.Finished {
				ui.Text(c, "Tool is running.").FontSize(11).TextColor(t.TextMuted)
			}
		})
	}
}

func toolTitleFor(name, args string) string {
	if name == "" {
		name = "Tool"
	}
	if args == "" {
		return name
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err == nil {
		for _, key := range []string{"path", "command"} {
			if value, ok := parsed[key].(string); ok && strings.TrimSpace(value) != "" {
				return name + " · " + truncate(value, 70)
			}
		}
	}
	return name + " · " + truncate(args, 70)
}

func (a *appModel) workActivitySummary(ct *contact) string {
	if w := ct.activeWork(); w != nil {
		if len(w.Tools) > 0 {
			last := w.Tools[len(w.Tools)-1]
			if !last.Finished {
				return "Running " + last.Name
			}
			return "Last action: " + last.Name
		}
		return workStateLabel(w.State)
	}
	if w := ct.recentWork(); w != nil {
		return workStateLabel(w.State)
	}
	return ""
}
