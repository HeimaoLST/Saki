package main

import (
	"encoding/json"
	"fmt"
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
	TaskID        string
	MessageID     string
	Prompt        string
	State         workState
	Detail        string
	Tools         []*workTool
	StopRequested bool
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

func workStateLabel(w *workItem) string {
	if w.State == workInterrupted && w.StopRequested {
		return "Stopped · interrupted"
	}
	return workBaseStateLabel(w.State)
}

func workBaseStateLabel(state workState) string {
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

func (a *appModel) workStateColor(state workState) ui.Color {
	switch state {
	case workStopping, workFailed, workInterrupted, workUnknown:
		return a.workTheme.WorkAttention
	case workCompleted:
		return a.workTheme.WorkText
	default:
		return a.workTheme.WorkText
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

// workspaceFrame draws the current/recent task view in its own column.
func (a *appModel) workspaceFrame(c *ui.Context, t *ui.Theme, width float32) {
	ui.Column(c).Width(width).FillHeight().Background(t.Surface).BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() {
		a.workspaceContent(c, t)
	})
}

func (a *appModel) workspaceModal(c *ui.Context, t *ui.Theme) {
	open := a.workspaceOpen
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.28))
		width := float32(360)
		if layout := float32(a.layoutWidth()); layout < width {
			width = layout
		}
		panel.Width(width).FillHeight().AlignSelf(ui.End).Background(t.Surface).Children(func() {
			a.workspaceContent(c, t)
		})
	})
	if a.workspaceOpen {
		a.workspaceOpen = open
	}
	if !a.workspaceOpen {
		a.focusWorkspace = true
	}
}

func (a *appModel) workspaceContent(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	current := ct.activeWork()
	issue := ct.selectedIssue()
	var work *workItem
	title := "Work"
	if issue != nil {
		work, _ = ct.issueWork(issue)
		title = "Issue result"
		if work != nil && current != nil && work.TaskID == current.TaskID {
			title = "Current issue run"
		}
	} else if current != nil {
		title = "Current work"
		work = current
	} else if recent := ct.recentWork(); recent != nil {
		title = "Recent work"
		work = recent
	}
	ui.Row(c).AlignItems(ui.Center).Padding(12, 12).Children(func() {
		ui.Text(c, title).FontSize(16).FontWeight(600).TextColor(t.Text)
		ui.Spacer(c)
		if !a.issueFormOpen {
			newIssue := ui.Button(c, "New issue").FontSize(12).Padding(3, 8).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() {
				a.issueFormOpen = true
				a.issueGoal = ""
				a.issueRequirements = ""
				a.issueFormError = ""
				a.focusIssueGoal = true
			})
			if a.focusIssueNew {
				newIssue.Focus()
				a.focusIssueNew = false
			}
		}
		close := ui.Button(c, "Close work").FontSize(12).OnClick(func() { a.closeWorkspace() })
		if a.focusWorkspaceOpen {
			close.Focus()
			a.focusWorkspaceOpen = false
		}
	})
	ui.Scroll(c).Grow(1).Padding(12).Children(func() {
		ui.Column(c).Gap(10).Children(func() {
			a.issueSection(c, t, ct)
			if issue != nil {
				ui.Divider(c)
				if work != nil {
					a.issueResultContext(c, t, issue, work)
					a.workCard(c, t, work, current != nil && work.TaskID == current.TaskID)
					a.workActivity(c, t, work)
				} else {
					a.issueWorkEmptyState(c, t, ct, issue)
				}
			} else if work != nil {
				ui.Divider(c)
				a.workCard(c, t, work, current != nil)
				a.workActivity(c, t, work)
			} else {
				a.workEmptyState(c, t, ct)
			}
			a.unlinkedActivity(c, t, ct.UnlinkedTools)
		})
	})
}

func (ct *contact) issueWork(issue *productIssue) (*workItem, int) {
	if issue == nil {
		return nil, 0
	}
	for _, taskID := range []string{ct.SelectedWorkTaskID, ct.ActiveTaskID, ct.LastTaskID} {
		if !issueHasTask(issue, taskID) {
			continue
		}
		if work := workFor(ct, taskID); work != nil {
			index, _ := issueExecutionForTask(issue, taskID)
			return work, index
		}
	}
	for index := len(issue.Executions) - 1; index >= 0; index-- {
		execution := issue.Executions[index]
		if work := workFor(ct, execution.TaskID); work != nil {
			return work, index
		}
	}
	return nil, 0
}

func (ct *contact) resultTextForExecution(execution *issueExecution) string {
	if execution == nil {
		return ""
	}
	for index := len(ct.Messages) - 1; index >= 0; index-- {
		m := ct.Messages[index]
		if m.Kind != msgContact || strings.TrimSpace(m.Text) == "" {
			continue
		}
		if execution.TaskID != "" && m.TaskID == execution.TaskID {
			return strings.TrimSpace(m.Text)
		}
		if execution.TaskID == "" && execution.MessageID != "" && m.MessageID == execution.MessageID {
			return strings.TrimSpace(m.Text)
		}
	}
	return ""
}

func (a *appModel) issueResultContext(c *ui.Context, t *ui.Theme, issue *productIssue, work *workItem) {
	index, _ := issueExecutionForTask(issue, work.TaskID)
	label := "result"
	if work.State == workAccepted || work.State == workRunning || work.State == workStopping {
		label = "in progress"
	}
	ui.Text(c, fmt.Sprintf("Run %d %s · %s", index+1, label, issue.Goal)).FontSize(11).FontWeight(500).TextColor(t.TextMuted).MaxLines(2)
}

func (a *appModel) issueWorkEmptyState(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue) {
	ui.Text(c, "No result recorded for this issue").FontSize(14).FontWeight(500).TextColor(t.Text)
	if current := ct.activeWork(); current != nil {
		if !issueHasTask(issue, current.TaskID) {
			ui.Text(c, "Another task is running. Its work is not shown as this issue's result.").FontSize(12).LineHeight(1.45).TextColor(t.Warning)
		}
	} else {
		ui.Text(c, "This issue has no execution with a trusted task result yet.").FontSize(12).LineHeight(1.45).TextColor(t.TextMuted)
	}
}

func (a *appModel) issueSection(c *ui.Context, t *ui.Theme, ct *contact) {
	if a.issueFormOpen {
		ui.Column(c).Padding(10).Radius(10).Background(t.SurfaceHover).Gap(6).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "New issue").FontSize(13).FontWeight(600).TextColor(t.Text)
				ui.Spacer(c)
				ui.Button(c, "Cancel").FontSize(12).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() {
					a.issueFormOpen = false
					a.issueFormError = ""
					a.focusIssueNew = true
				})
			})
			ui.Text(c, "Goal").FontSize(11).FontWeight(500).TextColor(t.Text)
			goal := ui.TextArea(c, &a.issueGoal).Label("Issue goal").FontSize(13).Placeholder("What should this issue accomplish?").Height(56)
			if a.focusIssueGoal {
				goal.Focus()
				a.focusIssueGoal = false
			}
			ui.Text(c, "Requirements").FontSize(11).FontWeight(500).TextColor(t.Text)
			ui.TextArea(c, &a.issueRequirements).Label("Issue requirements").FontSize(13).Placeholder("Original requirements; one per line or a short note").Height(72)
			if a.issueFormError != "" {
				ui.Text(c, a.issueFormError).FontSize(11).LineHeight(1.4).TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.PrimaryButton(c, "Save issue").FontSize(12).OnClick(func() { a.createIssue(a.issueGoal, a.issueRequirements) })
			})
		})
	}
	if issue := ct.selectedIssue(); issue != nil {
		ui.Column(c).Padding(10).Radius(10).Background(t.SurfaceHover).Gap(5).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Text(c, "Issue · "+issue.Goal).FontSize(13).FontWeight(600).TextColor(t.Text).MaxLines(2)
				ui.Spacer(c)
				ui.Text(c, "Open").FontSize(11).TextColor(t.Warning)
			})
			ui.Text(c, "Original requirements · unverified").FontSize(11).FontWeight(500).TextColor(t.TextMuted)
			ui.Text(c, issueRequirementsSummary(issue)).FontSize(11).LineHeight(1.45).TextColor(t.TextMuted)
			ui.Text(c, "Completion: "+issueEvidenceText(issue)).FontSize(11).LineHeight(1.4).TextColor(t.Warning)
			for index, execution := range issue.Executions {
				execution := execution
				w := workFor(ct, execution.TaskID)
				resultText := ct.resultTextForExecution(execution)
				ui.Column(c).Padding(8).Radius(8).Background(t.Surface).Gap(2).Children(func() {
					ui.Row(c).AlignItems(ui.Center).Children(func() {
						ui.Text(c, fmt.Sprintf("Run %d · %s", index+1, issueExecutionLabel(execution))).FontSize(11).TextColor(t.TextMuted)
					})
					if execution.Detail != "" {
						ui.Text(c, execution.Detail).FontSize(11).LineHeight(1.4).TextColor(t.Warning)
					}
					if resultText != "" {
						label := "Result preview"
						if execution.Status == issueExecutionAccepted || execution.Status == issueExecutionRunning {
							label = "Partial result preview"
						}
						ui.Text(c, label+": "+truncate(resultText, 300)).FontSize(11).LineHeight(1.4).TextColor(t.Text).MaxLines(4)
					} else if w != nil {
						switch {
						case len(w.Tools) > 0:
							last := w.Tools[len(w.Tools)-1]
							lead := "No assistant message recorded for this run; tool output only: "
							if execution.Status == issueExecutionAccepted || execution.Status == issueExecutionRunning {
								lead = "No assistant message yet; tool output only: "
							}
							ui.Text(c, lead+toolTitleFor(last.Name, last.Args)+" · "+toolOutputSummary(last.Output)).FontSize(11).LineHeight(1.4).TextColor(t.TextMuted).MaxLines(3)
						case execution.Status == issueExecutionCompleted:
							ui.Text(c, "No assistant message was recorded for this run. Use View execution to inspect its tool activity.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
						}
					}
					if w != nil {
						label := "View result"
						if resultText == "" {
							label = "View execution"
						}
						ui.Button(c, label).FontSize(11).Padding(3, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() {
							a.showIssueWork(issue.ID, execution.TaskID)
						})
					}
				})
			}
			reason := a.canSend(ct)
			if reason == "" {
				reason = issueRunBlocked(issue)
			}
			if reason == "" {
				ui.Button(c, "Run issue").FontSize(12).OnClick(func() { a.runIssue(issue.ID) })
			} else {
				ui.Text(c, "Run unavailable: "+reason).FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
			}
		})
	} else if !a.issueFormOpen {
		ui.Text(c, "No product issues yet. Use New issue to explicitly create one.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
	}
	open := ct.openIssues()
	if len(open) > 1 {
		ui.Text(c, "Open issues").FontSize(11).FontWeight(500).TextColor(t.TextMuted)
		ui.Column(c).Gap(4).Children(func() {
			for _, candidate := range open {
				candidate := candidate
				label := truncate(candidate.Goal, 40)
				if selected := ct.selectedIssue(); selected != nil && selected.ID == candidate.ID {
					label = "• " + label
				}
				ui.Button(c, label).FontSize(11).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.selectIssue(candidate.ID) })
			}
		})
	}
}

func (a *appModel) workEmptyState(c *ui.Context, t *ui.Theme, ct *contact) {
	if !a.demo && ct.SessionID != "" && !ct.Loaded {
		ui.Text(c, "Session history is not loaded").FontSize(14).FontWeight(500).TextColor(t.Text)
		ui.Text(c, "This contact already has a session, but its history has not been loaded yet; existing work is not shown as absent.").FontSize(12).LineHeight(1.45).TextColor(t.TextMuted)
		if a.runtimeStatus != runtimeStateReady {
			ui.Text(c, "Runtime is not ready; work history cannot be read yet.").FontSize(12).LineHeight(1.45).TextColor(t.Warning)
		}
		return
	}
	if !a.demo && a.runtimeStatus != runtimeStateReady {
		ui.Text(c, "Work is unavailable while Runtime is not ready").FontSize(14).FontWeight(500).TextColor(t.Text)
		ui.Text(c, "No work is shown until the Runtime connection is ready; this is not a claim that no work exists.").FontSize(12).LineHeight(1.45).TextColor(t.TextMuted)
		return
	}
	ui.Text(c, "No current work").FontSize(14).FontWeight(500).TextColor(t.Text)
	ui.Text(c, "Work will appear here when "+ct.Name+" starts a task. Tool activity and results stay out of the conversation.").FontSize(12).LineHeight(1.45).TextColor(t.TextMuted)
}

func (a *appModel) workCard(c *ui.Context, t *ui.Theme, w *workItem, isCurrent bool) {
	ui.Column(c).Padding(14).Radius(13).Background(a.workTheme.WorkSurface).Gap(6).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, workStateLabel(w)).FontSize(12).FontWeight(600).TextColor(a.workStateColor(w.State))
			ui.Spacer(c)
			if isCurrent && (w.State == workAccepted || w.State == workRunning || w.State == workStopping) {
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
			ui.Text(c, w.Prompt).FontSize(13).LineHeight(1.5).MaxLines(4).TextColor(a.workTheme.WorkText)
		}
		if len(w.Tools) > 0 {
			last := w.Tools[len(w.Tools)-1]
			action := last.Name
			if last.Finished {
				action = "Last action: " + action
			} else {
				action = "Running " + action
			}
			ui.Text(c, action).FontSize(11).LineHeight(1.4).TextColor(a.workTheme.WorkMuted)
		}
		if w.Detail != "" {
			ui.Text(c, w.Detail).FontSize(12).LineHeight(1.45).TextColor(a.workTheme.WorkAttention)
		}
	})
}

func (a *appModel) workActivity(c *ui.Context, t *ui.Theme, w *workItem) {
	ui.Text(c, "Activity").FontSize(11).FontWeight(500).TextColor(t.TextMuted)
	if len(w.Tools) == 0 {
		ui.Text(c, "No tool activity yet.").FontSize(12).TextColor(t.TextMuted)
		return
	}
	a.toolCalls(c, t, w.Tools)
}

func (a *appModel) unlinkedActivity(c *ui.Context, t *ui.Theme, tools []*workTool) {
	if len(tools) == 0 {
		return
	}
	ui.Text(c, "Earlier activity").FontSize(11).FontWeight(500).TextColor(t.TextMuted)
	ui.Text(c, "No task ID was recorded; these tool results are shown as readable history, without a claimed task.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
	a.toolCalls(c, t, tools)
}

func (a *appModel) toolCalls(c *ui.Context, t *ui.Theme, tools []*workTool) {
	for _, call := range tools {
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
					if summary != "" {
						summary = "Preview · " + summary
					}
					ui.Text(c, summary).FontSize(11).LineHeight(1.4).MaxLines(4).TextColor(t.TextMuted).Grow(1)
					if !call.Expanded {
						ui.Button(c, "Copy received output").FontSize(12).Padding(3, 8).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() {
							if err := writeClipboardText(call.Output, c.Services().WriteClipboard, c.Services().ReadClipboard); err != nil {
								c.Toast("Copy failed: " + err.Error())
							} else {
								c.Toast("Copied received output")
							}
						})
					}
					ui.Button(c, label).FontSize(12).Padding(3, 8).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { call.Expanded = !call.Expanded })
				})
				if call.Expanded {
					markdownReader(c, call.Output, markdownReaderOptions{PlainText: true, CopyLabel: "Copy received output"}).
						FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
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
		return workStateLabel(w)
	}
	if w := ct.recentWork(); w != nil {
		return workStateLabel(w)
	}
	return ""
}
