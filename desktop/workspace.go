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
	issueActive := false
	if issue != nil {
		work, _ = ct.issueWork(issue)
		title = "Issue result"
		if work != nil && issue.Work.ActiveTaskID == work.TaskID {
			issueActive = true
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
					a.workCard(c, t, work, issueActive, true)
					a.workActivity(c, t, work)
				} else {
					a.issueWorkEmptyState(c, t, ct, issue)
				}
			} else if work != nil {
				ui.Divider(c)
				a.workCard(c, t, work, current != nil, false)
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
	for _, taskID := range []string{issue.Work.ActiveTaskID, ct.SelectedWorkTaskID, ct.ActiveTaskID, ct.LastTaskID} {
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
	// New issue work messages live in the issue-owned session projection, not
	// in the conversation transcript.
	for _, issue := range ct.Issues {
		for index := len(issue.Work.Messages) - 1; index >= 0; index-- {
			m := issue.Work.Messages[index]
			if m.Kind != msgContact || strings.TrimSpace(m.Text) == "" {
				continue
			}
			if execution.TaskID != "" && m.TaskID == execution.TaskID {
				return strings.TrimSpace(m.Text)
			}
		}
	}
	// Legacy executions used the conversation session; keep their actual
	// history readable without migrating them.
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
		title := "New issue"
		if a.issueFormEditingID != "" {
			title = "Edit issue"
		}
		ui.Column(c).Padding(10).Radius(10).Background(t.SurfaceHover).Gap(6).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, title).FontSize(13).FontWeight(600).TextColor(t.Text)
				ui.Spacer(c)
				ui.Button(c, "Cancel").FontSize(12).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.cancelIssueForm() })
			})
			ui.Text(c, "Goal").FontSize(11).FontWeight(500).TextColor(t.Text)
			goal := ui.TextArea(c, &a.issueGoal).Label("Issue goal").FontSize(13).Placeholder("What should this issue accomplish?").Height(56)
			if a.focusIssueGoal {
				goal.Focus()
				a.focusIssueGoal = false
			}
			ui.Text(c, "Requirements").FontSize(11).FontWeight(500).TextColor(t.Text)
			ui.TextArea(c, &a.issueRequirements).Label("Issue requirements").FontSize(13).Placeholder("One requirement per line; edits start unverified").Height(72)
			if a.issueFormError != "" {
				ui.Text(c, a.issueFormError).FontSize(11).LineHeight(1.4).TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.PrimaryButton(c, "Save issue").FontSize(12).OnClick(func() { a.saveIssueForm() })
			})
		})
	}
	if issue := ct.selectedIssue(); issue != nil {
		a.issueCard(c, t, ct, issue)
	} else if !a.issueFormOpen {
		ui.Text(c, "No product issues yet. Use New issue to explicitly create one.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
	}
	a.issueList(c, t, ct)
}

func (a *appModel) issueCard(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue) {
	editable := !issue.Archived && issue.Status != issueStatusCompleted && issue.Status != issueStatusCanceled
	ui.Column(c).Padding(10).Radius(10).Background(t.SurfaceHover).Gap(5).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Text(c, "Issue · "+issue.Goal).FontSize(13).FontWeight(600).TextColor(t.Text).MaxLines(2)
			ui.Spacer(c)
			ui.Text(c, issueStatusLabel(issue)).FontSize(11).TextColor(a.issueStatusColor(t, issue))
			if issue.Archived {
				ui.Text(c, "Archived").FontSize(10).TextColor(t.TextMuted)
			}
		})
		if a.issueActionError != "" {
			ui.Text(c, a.issueActionError).FontSize(11).LineHeight(1.4).TextColor(t.Warning)
		}
		if editable {
			ui.Button(c, "Edit issue").FontSize(11).Padding(3, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.openEditIssueForm(issue) })
		}
		ui.Text(c, fmt.Sprintf("Requirements · revision %d", issue.RequirementsRevision)).FontSize(11).FontWeight(500).TextColor(t.TextMuted)
		if len(issue.Requirements) == 0 {
			ui.Text(c, "No requirements recorded.").FontSize(11).TextColor(t.TextMuted)
		}
		for _, requirement := range issue.Requirements {
			requirement := requirement
			if requirement == nil {
				continue
			}
			ui.Column(c).Padding(7).Radius(7).Background(t.Surface).Gap(3).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
					ui.Text(c, requirement.Text).FontSize(11).LineHeight(1.4).TextColor(t.Text).Grow(1)
					ui.Text(c, requirementStateLabel(requirement.State)).FontSize(10).TextColor(requirementStateColor(t, requirement.State))
				})
				if editable {
					draft := a.requirementDraft(requirement)
					ui.TextInput(c, draft).Label("Evidence for " + requirement.Text).FontSize(11).Placeholder("Evidence or user judgment")
					ui.Row(c).Gap(4).Children(func() {
						ui.Button(c, "Satisfied").FontSize(10).Padding(2, 6).Background(ui.Transparent).BorderWidth(0).TextColor(t.Success).OnClick(func() {
							if !a.setIssueRequirementState(issue.ID, requirement.ID, requirementSatisfied, *draft) {
								a.issueActionError = "Could not save the requirement state."
							}
						})
						ui.Button(c, "Unmet").FontSize(10).Padding(2, 6).Background(ui.Transparent).BorderWidth(0).TextColor(t.Warning).OnClick(func() {
							if !a.setIssueRequirementState(issue.ID, requirement.ID, requirementUnmet, *draft) {
								a.issueActionError = "Could not save the requirement state."
							}
						})
						ui.Button(c, "Unverified").FontSize(10).Padding(2, 6).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() {
							if !a.setIssueRequirementState(issue.ID, requirement.ID, requirementUnverified, *draft) {
								a.issueActionError = "Could not save the requirement state."
							}
						})
					})
				} else if requirement.Evidence != "" {
					ui.Text(c, "Evidence: "+requirement.Evidence).FontSize(10).LineHeight(1.35).TextColor(t.TextMuted).MaxLines(2)
				}
			})
		}
		completion := issueEvidenceText(issue)
		if issue.Evidence != "" && issue.Status != issueStatusCompleted {
			completion = "Previous completion evidence: " + issue.Evidence
		}
		ui.Text(c, "Completion: "+completion).FontSize(11).LineHeight(1.4).TextColor(t.Warning)
		a.issueActions(c, t, ct, issue, editable)

		ui.Divider(c)
		ui.Text(c, "Executions").FontSize(11).FontWeight(500).TextColor(t.TextMuted)
		for index, execution := range issue.Executions {
			execution := execution
			w := workFor(ct, execution.TaskID)
			resultText := ct.resultTextForExecution(execution)
			ui.Column(c).Padding(8).Radius(8).Background(t.Surface).Gap(2).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, fmt.Sprintf("Run %d · %s", index+1, issueExecutionLabel(execution))).FontSize(11).TextColor(t.TextMuted)
					ui.Spacer(c)
					if execution.RequirementsRevision > 0 {
						ui.Text(c, fmt.Sprintf("req r%d", execution.RequirementsRevision)).FontSize(10).TextColor(t.TextMuted)
					}
				})
				if execution.Prompt != "" {
					ui.Text(c, "Sent snapshot: "+truncate(execution.Prompt, 240)).FontSize(10).LineHeight(1.35).TextColor(t.TextMuted).MaxLines(2)
				}
				if execution.Detail != "" {
					ui.Text(c, execution.Detail).FontSize(11).LineHeight(1.4).TextColor(t.Warning)
				}
				if execution.Status == issueExecutionUnknown || execution.Status == issueExecutionPending {
					ui.Row(c).Gap(6).Children(func() {
						ui.Button(c, "Check execution").FontSize(10).Padding(2, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() {
							if !a.reconcileIssueExecution(issue.ID, execution.ID) {
								a.issueActionError = execution.Detail
							}
						})
						if execution.TaskID == "" && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle {
							ui.Button(c, "Confirm not dispatched").FontSize(10).Padding(2, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() {
								if !a.confirmIssueExecutionNotDispatched(issue.ID, execution.ID) {
									a.issueActionError = "Could not confirm this execution."
								}
							})
						}
					})
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
					ui.Button(c, label).FontSize(11).Padding(3, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.showIssueWork(issue.ID, execution.TaskID) })
				}
			})
		}
		if executionHasUnknownRecovery(issue) {
			ui.Text(c, "Recovery is available for an execution without a trusted terminal state.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
		}
	})
}

func (a *appModel) issueActions(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue, editable bool) {
	if issue.Archived {
		ui.Button(c, "Restore issue").FontSize(12).OnClick(func() {
			if !a.restoreIssue(issue.ID) {
				a.issueActionError = "Could not restore this issue."
			} else {
				a.issueFilter = "active"
			}
		})
		return
	}
	if editable {
		ui.Row(c).Gap(6).Children(func() {
			reason := issueLifecycleStartBlocked(issue)
			if reason == "" {
				reason = a.canStartIssue(ct, issue)
			}
			if reason == "" {
				reason = issueRunBlocked(issue)
			}
			if reason == "" {
				label := "Start issue"
				if len(issue.Executions) > 0 {
					label = "Continue issue"
				}
				ui.Button(c, label).FontSize(12).OnClick(func() {
					if !a.runIssue(issue.ID) {
						a.issueActionError = a.issueFormError
					}
				})
			} else {
				ui.Text(c, "Start unavailable: "+reason).FontSize(10).LineHeight(1.35).TextColor(t.TextMuted).MaxLines(2)
			}
			if issue.Status == issueStatusWaitingUser || issue.Status == issueStatusNeedsAttention {
				ui.Button(c, "Resume").FontSize(11).Padding(2, 7).OnClick(func() {
					if !a.resumeIssue(issue.ID) {
						a.issueActionError = "Could not resume this issue."
					}
				})
			} else if issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle {
				ui.Button(c, "Mark waiting").FontSize(11).Padding(2, 7).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() {
					if !a.markIssueWaiting(issue.ID) {
						a.issueActionError = "Could not mark this issue waiting."
					}
				})
			}
		})
	}
	ui.Row(c).Gap(6).Children(func() {
		if editable {
			ui.Button(c, "Cancel issue").FontSize(11).Padding(2, 7).Background(ui.Transparent).BorderWidth(0).TextColor(t.Warning).OnClick(func() {
				if !a.cancelIssueProduct(issue.ID) {
					a.issueActionError = "Could not cancel this issue; its state was not changed."
				}
			})
		}
		ui.Button(c, "Archive").FontSize(11).Padding(2, 7).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() {
			if !a.archiveIssue(issue.ID) {
				a.issueActionError = "Could not archive this issue."
			} else {
				a.issueFilter = "archived"
			}
		})
	})
	if issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
		ui.Button(c, "Reopen issue").FontSize(12).OnClick(func() {
			if !a.reopenIssue(issue.ID) {
				a.issueActionError = "Could not reopen this issue."
			} else {
				a.issueFilter = "active"
			}
		})
		return
	}
	if editable && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle && issueRunBlocked(issue) == "" {
		ui.Text(c, "Completion evidence").FontSize(10).TextColor(t.TextMuted)
		draft := a.completionDraft(issue)
		ui.TextInput(c, draft).Label("Completion evidence").FontSize(11).Placeholder("What proves this issue is complete?")
		ui.Button(c, "Mark complete").FontSize(11).OnClick(func() {
			if !a.completeIssue(issue.ID, *draft) {
				a.issueActionError = "Could not complete this issue."
			}
		})
	}
}

func (a *appModel) issueList(c *ui.Context, t *ui.Theme, ct *contact) {
	filter := a.issueFilter
	if filter == "" {
		filter = "active"
	}
	filters := []string{"active", "waiting", "attention", "completed", "canceled", "archived"}
	for start := 0; start < len(filters); start += 3 {
		end := start + 3
		if end > len(filters) {
			end = len(filters)
		}
		ui.Row(c).Gap(4).Children(func() {
			for _, candidate := range filters[start:end] {
				candidate := candidate
				label := issueFilterLabel(candidate)
				if candidate == filter {
					label = "• " + label
				}
				ui.Button(c, label).FontSize(10).Padding(2, 6).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.issueFilter = candidate })
			}
		})
	}
	issues := ct.issuesForFilter(filter)
	if len(issues) == 0 {
		ui.Text(c, "No issues in "+issueFilterLabel(filter)+".").FontSize(11).TextColor(t.TextMuted)
		return
	}
	ui.Text(c, "Issues · "+issueFilterLabel(filter)).FontSize(11).FontWeight(500).TextColor(t.TextMuted)
	ui.Column(c).Gap(3).Children(func() {
		for _, candidate := range issues {
			candidate := candidate
			label := issueStatusLabel(candidate) + " · " + truncate(candidate.Goal, 34)
			if selected := ct.selectedIssue(); selected != nil && selected.ID == candidate.ID {
				label = "• " + label
			}
			ui.Button(c, label).FontSize(10).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.selectIssue(candidate.ID) })
		}
	})
}

func (a *appModel) issueStatusColor(t *ui.Theme, issue *productIssue) ui.Color {
	if issue == nil {
		return t.TextMuted
	}
	if issue.Archived {
		return t.TextMuted
	}
	switch issue.Status {
	case issueStatusNeedsAttention, issueStatusWaitingUser:
		return t.Warning
	case issueStatusCompleted, issueStatusInProgress:
		return t.Success
	default:
		return t.TextMuted
	}
}

func requirementStateLabel(state string) string {
	switch state {
	case requirementSatisfied:
		return "Satisfied"
	case requirementUnmet:
		return "Unmet"
	default:
		return "Unverified"
	}
}

func requirementStateColor(t *ui.Theme, state string) ui.Color {
	switch state {
	case requirementSatisfied:
		return t.Success
	case requirementUnmet:
		return t.Warning
	default:
		return t.TextMuted
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

func (a *appModel) workCard(c *ui.Context, t *ui.Theme, w *workItem, isCurrent, issueWork bool) {
	ui.Column(c).Padding(14).Radius(13).Background(a.workTheme.WorkSurface).Gap(6).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, workStateLabel(w)).FontSize(12).FontWeight(600).TextColor(a.workStateColor(w.State))
			ui.Spacer(c)
			if isCurrent && (w.State == workAccepted || w.State == workRunning || w.State == workStopping) {
				label := "Stop task"
				if issueWork {
					label = "Stop issue task"
				}
				if w.State == workStopping {
					label = "Stopping…"
					if issueWork {
						label = "Stopping issue…"
					}
				}
				stop := ui.Button(c, label).FontSize(12).OnClick(func() {
					if issueWork {
						a.cancelWorkTask(a.selectedContact(), w.TaskID)
					} else {
						a.cancelSelected()
					}
				})
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

func executionHasUnknownRecovery(issue *productIssue) bool {
	for _, execution := range issue.Executions {
		if execution.Status == issueExecutionUnknown || execution.Status == issueExecutionPending {
			return true
		}
	}
	return false
}

func (a *appModel) cancelWorkTask(ct *contact, taskID string) {
	if issue, _ := ct.issueForTask(taskID); issue != nil && issue.Work.ActiveTaskID == taskID {
		a.cancelIssue(issue.ID)
		return
	}
	a.cancelSelected()
}
