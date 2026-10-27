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

const (
	defaultLeftPaneWidth  float32 = 200
	minLeftPaneWidth      float32 = 180
	maxLeftPaneWidth      float32 = 280
	defaultRightPaneWidth float32 = 340
	minRightPaneWidth     float32 = 280
	maxRightPaneWidth     float32 = 440
	minCenterPaneWidth    float32 = 480
	paneSeparatorWidth    float32 = 1
)

func clampPaneWidth(value, fallback, minValue, maxValue float32) float32 {
	if value <= 0 {
		value = fallback
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func (a *appModel) layoutWidth() int {
	if a.windowWidth > 0 {
		return a.windowWidth
	}
	return 1040
}

func (a *appModel) railWidth() float32 {
	return clampPaneWidth(a.leftPaneWidth, defaultLeftPaneWidth, minLeftPaneWidth, maxLeftPaneWidth)
}

func (a *appModel) workWidth() float32 {
	return clampPaneWidth(a.rightPaneWidth, defaultRightPaneWidth, minRightPaneWidth, maxRightPaneWidth)
}

func (a *appModel) workOverlay() bool {
	return a.workOverlayFor(a.railWidth(), a.workWidth())
}

func (a *appModel) workOverlayFor(leftWidth, rightWidth float32) bool {
	total := float32(a.layoutWidth())
	left := float32(0)
	if !a.railCollapsed {
		left = leftWidth + paneSeparatorWidth
	}
	center := total - left - paneSeparatorWidth - rightWidth
	if center < minCenterPaneWidth || center <= rightWidth {
		return true
	}
	return !a.railCollapsed && center <= leftWidth
}

func (a *appModel) clampLeftForDrag(value float32, threePaneAtStart bool) float32 {
	limit := maxLeftPaneWidth
	if a.workspaceOpen && threePaneAtStart {
		byCenter := float32(a.layoutWidth()) - 2*paneSeparatorWidth - a.workWidth() - minCenterPaneWidth
		if byCenter < limit {
			limit = byCenter
		}
	}
	return clampPaneWidth(value, defaultLeftPaneWidth, minLeftPaneWidth, limit)
}

func (a *appModel) clampRightForDrag(value, left float32, threePaneAtStart bool) float32 {
	limit := maxRightPaneWidth
	if a.workspaceOpen && threePaneAtStart {
		byCenter := float32(a.layoutWidth()) - left - 2*paneSeparatorWidth - minCenterPaneWidth
		if byCenter < limit {
			limit = byCenter
		}
	}
	return clampPaneWidth(value, defaultRightPaneWidth, minRightPaneWidth, limit)
}

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
	default:
		return a.workTheme.WorkText
	}
}

func (a *appModel) setWorkspaceOpen(open bool) {
	if a.workspaceOpen == open {
		if open {
			a.focusWorkspaceOpen = true
		} else {
			a.focusWorkspace = true
		}
		return
	}
	a.workspaceOpen = open
	if open {
		a.focusWorkspaceOpen = true
	} else {
		a.focusWorkspace = true
	}
	a.persist()
}

func (a *appModel) toggleWorkspace() { a.setWorkspaceOpen(!a.workspaceOpen) }

func (a *appModel) closeWorkspace() { a.setWorkspaceOpen(false) }

func (a *appModel) toggleRail() {
	a.railCollapsed = !a.railCollapsed
	a.focusRail = true
}

// layoutBody draws the contact rail and the main conversation. A visible
// work panel nests a second Split so either edge can be resized independently.
func (a *appModel) layoutBody(c *ui.Context, t *ui.Theme) {
	if a.railCollapsed {
		a.layoutMainArea(c, t, 0)
		return
	}
	threePaneAtStart := a.workspaceOpen && !a.workOverlay()
	a.leftPaneWidth = a.railWidth()
	ui.Split(c, &a.leftPaneWidth, func() { a.rail(c, t) }, func() {
		a.layoutMainArea(c, t, a.leftPaneWidth)
	}).OnChange(func() {
		// Split updates its bound width inside the handle's afterInput, before
		// OnChange and the rebuild. Clamp here, not after a stale build.
		a.leftPaneWidth = a.clampLeftForDrag(a.leftPaneWidth, threePaneAtStart)
	}).Fill()
}

func (a *appModel) layoutMainArea(c *ui.Context, t *ui.Theme, left float32) {
	if !a.workspaceOpen || a.aux == "settings" || a.workOverlay() {
		a.main(c, t)
		return
	}
	area := float32(a.layoutWidth()) - left
	if !a.railCollapsed {
		area -= paneSeparatorWidth
	}
	threePaneAtStart := !a.workOverlay()
	a.centerPaneWidth = area - paneSeparatorWidth - a.workWidth()
	ui.Split(c, &a.centerPaneWidth, func() {
		a.main(c, t)
	}, func() {
		a.workspaceFrame(c, t)
	}).OnChange(func() {
		candidate := area - paneSeparatorWidth - a.centerPaneWidth
		a.rightPaneWidth = a.clampRightForDrag(candidate, left, threePaneAtStart)
	}).Fill()
}

// workspaceFrame draws the current/recent task view in its own column.
func (a *appModel) workspaceFrame(c *ui.Context, t *ui.Theme) {
	ui.Column(c).FillWidth().FillHeight().Background(t.Surface).BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() {
		a.workspaceContent(c, t)
	})
}

func (a *appModel) workspaceModal(c *ui.Context, t *ui.Theme) {
	open := a.workspaceOpen
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.28))
		width := a.workWidth()
		if layout := float32(a.layoutWidth()); layout < width {
			width = layout
		}
		panel.Width(width).FillHeight().AlignSelf(ui.End).Background(t.Surface).Children(func() {
			a.workspaceContent(c, t)
		})
	})
	if a.workspaceOpen != open {
		a.setWorkspaceOpen(open)
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
		work = ct.issueWork(issue)
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
	ui.Column(c).Padding(12, 12).Gap(5).Children(func() {
		ui.Text(c, title).FontSize(16).FontWeight(600).TextColor(t.Text).MaxLines(2)
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Spacer(c)
			if !a.issueFormOpen {
				newIssue := secondaryButton(c, "New issue").OnClick(func() {
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
			close := secondaryButton(c, "Close work").OnClick(func() { a.closeWorkspace() })
			if a.focusWorkspaceOpen {
				close.Focus()
				a.focusWorkspaceOpen = false
			}
		})
	})
	ui.Scroll(c).Grow(1).Padding(12).Children(func() {
		ui.Column(c).Gap(10).Children(func() {
			explicitDetail := issue != nil && work != nil && (issueActive || ct.SelectedWorkTaskID == work.TaskID)
			if explicitDetail {
				if !issueActive {
					secondaryButton(c, "Back to issue").OnClick(func() {
						ct.SelectedWorkTaskID = ""
						a.focusWorkspaceOpen = true
					})
				}
				a.issueResultContext(c, t, issue, work)
				a.workCard(c, t, work, issueActive, true)
				a.workActivity(c, t, work)
				ui.Divider(c)
			}
			a.issueSection(c, t, ct)
			if issue != nil {
				if !explicitDetail && work == nil && latestIssueExecution(issue) == nil {
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

func (ct *contact) issueWork(issue *productIssue) *workItem {
	if issue == nil {
		return nil
	}
	for _, taskID := range []string{issue.Work.ActiveTaskID, ct.SelectedWorkTaskID, ct.ActiveTaskID, ct.LastTaskID} {
		if !issueHasTask(issue, taskID) {
			continue
		}
		if work := workFor(ct, taskID); work != nil {
			return work
		}
	}
	for index := len(issue.Executions) - 1; index >= 0; index-- {
		execution := issue.Executions[index]
		if work := workFor(ct, execution.TaskID); work != nil {
			return work
		}
	}
	return nil
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
	ui.Text(c, fmt.Sprintf("Run %d %s · %s", index+1, label, issue.Goal)).FontSize(14).FontWeight(500).TextColor(t.TextMuted).MaxLines(2)
}

func (a *appModel) issueWorkEmptyState(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue) {
	ui.Text(c, "No result recorded for this issue").FontSize(14).FontWeight(500).TextColor(t.Text)
	if current := ct.activeWork(); current != nil {
		if !issueHasTask(issue, current.TaskID) {
			ui.Text(c, "Another task is running. Its work is not shown as this issue's result.").FontSize(14).LineHeight(1.45).TextColor(t.Warning)
		}
	} else {
		ui.Text(c, "This issue has no execution with a trusted task result yet.").FontSize(14).LineHeight(1.45).TextColor(t.TextMuted)
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
				ui.Text(c, title).FontSize(14).FontWeight(600).TextColor(t.Text)
				ui.Spacer(c)
				secondaryButton(c, "Cancel").OnClick(func() { a.cancelIssueForm() })
			})
			ui.Text(c, "Goal").FontSize(14).FontWeight(500).TextColor(t.Text)
			goal := ui.TextArea(c, &a.issueGoal).Label("Issue goal").FontSize(14).Placeholder("What should this issue accomplish?").Height(56)
			if a.focusIssueGoal {
				goal.Focus()
				a.focusIssueGoal = false
			}
			ui.Text(c, "Requirements").FontSize(14).FontWeight(500).TextColor(t.Text)
			ui.TextArea(c, &a.issueRequirements).Label("Issue requirements").FontSize(14).Placeholder("One requirement per line; edits start unverified").Height(72)
			if a.issueFormError != "" {
				ui.Text(c, a.issueFormError).FontSize(14).LineHeight(1.4).TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.PrimaryButton(c, "Save issue").FontSize(13).OnClick(func() { a.saveIssueForm() })
			})
		})
	}
	if issue := ct.selectedIssue(); issue != nil {
		a.issueCard(c, t, ct, issue)
	} else if !a.issueFormOpen {
		ui.Text(c, "No product issues yet. Use New issue to explicitly create one.").FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
	}
	a.issueList(c, t, ct)
}

func latestIssueExecution(issue *productIssue) *issueExecution {
	if issue == nil {
		return nil
	}
	for index := len(issue.Executions) - 1; index >= 0; index-- {
		if issue.Executions[index] != nil {
			return issue.Executions[index]
		}
	}
	return nil
}

func (a *appModel) issueCard(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue) {
	editable := !issue.Archived && issue.Status != issueStatusCompleted && issue.Status != issueStatusCanceled
	expanded := a.issueDetailOpen[issue.ID]
	ui.Column(c).Padding(10).Radius(10).Background(t.SurfaceHover).Gap(5).Children(func() {
		ui.Column(c).Gap(2).Children(func() {
			ui.Text(c, issue.Goal).FontSize(14).FontWeight(600).TextColor(t.Text).MaxLines(3)
			ui.Text(c, issueStatusLabel(issue)).FontSize(14).TextColor(a.issueStatusColor(t, issue))
		})
		if a.issueActionError != "" {
			ui.Text(c, a.issueActionError).FontSize(14).LineHeight(1.4).TextColor(t.Warning)
		}
		if question := issue.openQuestion(); question != nil {
			ui.Column(c).Padding(8).Radius(8).Background(t.Surface).Gap(3).Children(func() {
				ui.Text(c, "Waiting for your answer").FontSize(14).FontWeight(500).TextColor(t.Warning)
				ui.Text(c, question.Text).FontSize(14).LineHeight(1.4).TextColor(t.Text)
				if editable {
					secondaryButton(c, "Answer question").TextColor(t.Text).OnClick(func() { a.openIssueSupplementForm(issue.ID) })
				}
			})
		}
		a.issueLatestResult(c, t, ct, issue)
		a.issueRequirementSummary(c, t, issue)
		if editable {
			a.issueStartAction(c, t, ct, issue)
		}
		label := "More issue actions"
		if expanded {
			label = "Hide issue actions"
		}
		secondaryButton(c, label).OnClick(func() {
			if a.issueDetailOpen == nil {
				a.issueDetailOpen = map[string]bool{}
			}
			a.issueDetailOpen[issue.ID] = !a.issueDetailOpen[issue.ID]
		})
		if expanded {
			ui.Divider(c)
			if editable {
				secondaryButton(c, "Edit issue").OnClick(func() { a.openEditIssueForm(issue) })
			}
			if issue.WorkSessionID != "" {
				secondaryButton(c, "Issue model").OnClick(func() {
					a.openSessionModel(issue.WorkSessionID, "work", issue.Goal)
				})
			}
			a.issueActions(c, t, ct, issue, editable)
			evidence := issueEvidenceText(issue)
			if issue.Evidence != "" && issue.Status != issueStatusCompleted {
				evidence = "Previous completion evidence: " + issue.Evidence
			}
			ui.Text(c, "Completion evidence").FontSize(14).TextColor(t.TextMuted)
			ui.Text(c, evidence).FontSize(14).LineHeight(1.4).TextColor(t.Text).MaxLines(4)
			a.issueRequirementEvidence(c, t, ct, issue, editable)
			a.issueSupplementSection(c, t, ct, issue, editable)
			a.issueExecutions(c, t, ct, issue)
		} else if a.issueSupplementFormID == issue.ID {
			a.issueSupplementSection(c, t, ct, issue, editable)
		}
		if executionHasUnknownRecovery(issue) {
			ui.Text(c, "Recovery is available for an execution without a trusted terminal state.").FontSize(14).LineHeight(1.35).TextColor(t.TextMuted)
		}
	})
}

func (a *appModel) issueLatestResult(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue) {
	execution := latestIssueExecution(issue)
	if execution == nil {
		ui.Text(c, "No execution yet").FontSize(14).FontWeight(500).TextColor(t.TextMuted)
		return
	}
	ui.Text(c, "Latest execution").FontSize(14).FontWeight(500).TextColor(t.TextMuted)
	ui.Text(c, issueExecutionLabel(execution)).FontSize(14).TextColor(t.TextMuted)
	text := strings.TrimSpace(ct.resultTextForExecution(execution))
	work := workFor(ct, execution.TaskID)
	switch {
	case execution.Status == issueExecutionAccepted || execution.Status == issueExecutionRunning:
		if text == "" {
			ui.Text(c, "No result yet; this execution is still running.").FontSize(14).LineHeight(1.4).TextColor(t.Text).MaxLines(3)
		} else {
			ui.Text(c, "Current response (execution still running): "+truncate(text, 300)).FontSize(14).LineHeight(1.4).TextColor(t.Text).MaxLines(3)
		}
	case execution.Status == issueExecutionCompleted:
		if text == "" {
			if work != nil && len(work.Tools) > 0 {
				ui.Text(c, "No assistant message was recorded; open the execution to inspect tool output.").FontSize(14).LineHeight(1.4).TextColor(t.Text).MaxLines(3)
			} else {
				ui.Text(c, "No assistant message was recorded for this execution.").FontSize(14).LineHeight(1.4).TextColor(t.Text).MaxLines(3)
			}
		} else {
			ui.Text(c, "Result preview: "+truncate(text, 300)).FontSize(14).LineHeight(1.4).TextColor(t.Text).MaxLines(3)
		}
	default:
		if text != "" {
			ui.Text(c, "Result preview: "+truncate(text, 300)).FontSize(14).LineHeight(1.4).TextColor(t.Text).MaxLines(3)
		}
		reason := strings.TrimSpace(execution.Detail)
		if reason == "" {
			switch execution.Status {
			case issueExecutionPending:
				reason = "Prepared but not dispatched yet."
			case issueExecutionNotDispatched:
				reason = "No task was dispatched."
			case issueExecutionRejected:
				reason = "The execution was rejected."
			case issueExecutionFailed:
				reason = "Execution failed; inspect the execution details for the recorded reason."
			case issueExecutionInterrupted:
				reason = "Execution interrupted; inspect the execution details for the recorded terminal state."
			default:
				reason = "The terminal state is unknown; inspect the execution details before continuing."
			}
		}
		ui.Text(c, truncate(reason, 180)).FontSize(14).LineHeight(1.4).TextColor(t.Warning).MaxLines(3)
	}
	ui.Row(c).Gap(10).Children(func() {
		secondaryButton(c, "Open result").OnClick(func() { a.openIssueResult(issue.ID, execution.ID) })
		if work != nil {
			secondaryButton(c, "View execution").OnClick(func() { a.showIssueWork(issue.ID, execution.TaskID) })
		}
	})
}

func (a *appModel) issueRequirementSummary(c *ui.Context, t *ui.Theme, issue *productIssue) {
	if len(issue.Requirements) == 0 {
		ui.Text(c, "Requirements").FontSize(14).FontWeight(500).TextColor(t.TextMuted)
		ui.Text(c, "No requirements recorded.").FontSize(14).TextColor(t.TextMuted)
		return
	}
	ui.Text(c, "Requirements").FontSize(14).FontWeight(500).TextColor(t.TextMuted)
	for _, requirement := range issue.Requirements {
		if requirement == nil {
			continue
		}
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Text(c, requirement.Text).FontSize(14).LineHeight(1.35).TextColor(t.Text).Grow(1)
			ui.Text(c, requirementStateLabel(requirement.State)).FontSize(14).TextColor(requirementStateColor(t, requirement.State))
		})
	}
}

func (a *appModel) issueStartAction(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue) {
	if issue.Archived || issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
		return
	}
	reason := issueLifecycleStartBlocked(issue)
	if reason == "" {
		reason = a.canStartIssue(ct, issue)
	}
	if reason == "" {
		reason = issueRunBlocked(issue)
	}
	if reason != "" {
		ui.Text(c, "Next action unavailable: "+reason).FontSize(14).LineHeight(1.35).TextColor(t.TextMuted).MaxLines(2)
		return
	}
	label := "Start issue"
	if len(issue.Executions) > 0 {
		label = "Continue issue"
	}
	if issue.Supplements != nil && issue.Supplements.pendingCount() > 0 {
		if len(issue.Executions) == 0 {
			label = "Start with supplement"
		} else {
			label = "Continue with supplement"
		}
	}
	ui.PrimaryButton(c, label).FontSize(13).OnClick(func() {
		if !a.runIssue(issue.ID) {
			a.issueActionError = a.issueFormError
		}
	})
}

func (a *appModel) issueRequirementEvidence(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue, editable bool) {
	if len(issue.Requirements) == 0 {
		return
	}
	ui.Text(c, "Requirement evidence").FontSize(14).FontWeight(500).TextColor(t.TextMuted)
	for _, requirement := range issue.Requirements {
		if requirement == nil {
			continue
		}
		ui.Column(c).Padding(6).Radius(6).Background(t.Surface).Gap(3).Children(func() {
			ui.Text(c, requirement.Text).FontSize(14).LineHeight(1.35).TextColor(t.Text).MaxLines(2)
			if editable {
				draft := a.requirementDraft(requirement)
				ui.TextInput(c, draft).Label("Evidence for " + requirement.Text).FontSize(14).Placeholder("Evidence or user judgment")
				ui.Row(c).Gap(4).Children(func() {
					for _, action := range []struct {
						label, state string
						color        ui.Color
					}{
						{"Satisfied", requirementSatisfied, t.Success},
						{"Unmet", requirementUnmet, t.Warning},
						{"Unverified", requirementUnverified, t.TextMuted},
					} {
						action := action
						secondaryButton(c, action.label).TextColor(action.color).OnClick(func() {
							if !a.setIssueRequirementState(issue.ID, requirement.ID, action.state, *draft) {
								a.issueActionError = "Could not save the requirement state."
							}
						})
					}
				})
			} else if requirement.Evidence != "" {
				ui.Text(c, "Evidence: "+requirement.Evidence).FontSize(14).LineHeight(1.3).TextColor(t.TextMuted).MaxLines(2)
			}
		})
	}
}

func (a *appModel) issueExecutions(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue) {
	if len(issue.Executions) == 0 {
		return
	}
	ui.Divider(c)
	ui.Text(c, "Execution history").FontSize(14).FontWeight(500).TextColor(t.TextMuted)
	for index, execution := range issue.Executions {
		if execution == nil {
			continue
		}
		w := workFor(ct, execution.TaskID)
		ui.Column(c).Padding(7).Radius(7).Background(t.Surface).Gap(2).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, fmt.Sprintf("Execution %d · %s", index+1, issueExecutionLabel(execution))).FontSize(14).TextColor(t.TextMuted)
				ui.Spacer(c)
				if execution.RequirementsRevision > 0 {
					ui.Text(c, fmt.Sprintf("revision %d", execution.RequirementsRevision)).FontSize(14).TextColor(t.TextMuted)
				}
			})
			contextCount := len(executionTaskContextItems(ct, issue, execution))
			if execution.Prompt != "" || contextCount > 0 {
				label := "Show sent snapshot"
				if contextCount > 0 {
					label = fmt.Sprintf("Sent task context · %d", contextCount)
					if execution.ContextExpanded {
						label = "Hide sent task context"
					}
				} else if execution.ContextExpanded {
					label = "Hide sent snapshot"
				}
				secondaryButton(c, label).OnClick(func() {
					execution.ContextExpanded = !execution.ContextExpanded
				})
			}
			if execution.ContextExpanded {
				if execution.Prompt != "" {
					ui.Text(c, "Sent snapshot: "+truncate(execution.Prompt, 240)).FontSize(14).LineHeight(1.3).TextColor(t.TextMuted).MaxLines(3)
				}
				a.executionTaskContextView(c, t, ct, issue, execution)
			}
			if execution.Detail != "" {
				ui.Text(c, execution.Detail).FontSize(14).LineHeight(1.35).TextColor(t.Warning)
			}
			if execution.Status == issueExecutionUnknown || execution.Status == issueExecutionPending {
				ui.Row(c).Gap(6).Children(func() {
					secondaryButton(c, "Check execution").OnClick(func() {
						if !a.reconcileIssueExecution(issue.ID, execution.ID) {
							a.issueActionError = execution.Detail
						}
					})
					if execution.TaskID == "" && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle {
						secondaryButton(c, "Confirm not dispatched").OnClick(func() {
							if !a.confirmIssueExecutionNotDispatched(issue.ID, execution.ID) {
								a.issueActionError = "Could not confirm this execution."
							}
						})
					}
				})
			}
			ui.Row(c).Gap(8).Children(func() {
				secondaryButton(c, fmt.Sprintf("Open result · run %d", index+1)).OnClick(func() { a.openIssueResult(issue.ID, execution.ID) })
				if w != nil {
					secondaryButton(c, fmt.Sprintf("View execution · run %d", index+1)).OnClick(func() { a.showIssueWork(issue.ID, execution.TaskID) })
				}
			})
		})
	}
}

func (a *appModel) issueActions(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue, editable bool) {
	if issue.Archived {
		secondaryButton(c, "Restore issue").OnClick(func() {
			if !a.restoreIssue(issue.ID) {
				a.issueActionError = "Could not restore this issue."
			} else {
				a.issueFilter = "active"
			}
		})
		return
	}
	if editable {
		taskContextButton := func() {
			label := "Task context"
			if count := issue.Context.count(); count > 0 {
				label = fmt.Sprintf("Task context · %d", count)
			}
			secondaryButton(c, label).OnClick(func() { a.openIssueTaskContext(issue.ID) })
		}
		addSupplementButton := func() {
			label := "Add supplement"
			if issue.Supplements != nil && (issue.Supplements.pendingCount() > 0 || len(issue.Supplements.Batches) > 0) {
				label = fmt.Sprintf("Supplements · %d", issue.Supplements.pendingCount()+len(issue.Supplements.Batches))
			}
			secondaryButton(c, label).OnClick(func() { a.openIssueSupplementForm(issue.ID) })
		}
		waitButton := func() {
			if issue.openQuestion() != nil {
				return
			}
			if issue.Status == issueStatusWaitingUser || issue.Status == issueStatusNeedsAttention {
				secondaryButton(c, "Resume").OnClick(func() {
					if !a.resumeIssue(issue.ID) {
						a.issueActionError = "Could not resume this issue."
					}
				})
			} else if issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle {
				secondaryButton(c, "Mark waiting").OnClick(func() {
					if !a.markIssueWaiting(issue.ID) {
						a.issueActionError = "Could not mark this issue waiting."
					}
				})
			}
		}

		ui.Row(c).Gap(6).Children(func() {
			taskContextButton()
			addSupplementButton()
		})
		ui.Row(c).Gap(6).Children(waitButton)
	}

	ui.Row(c).Gap(6).Children(func() {
		if editable {
			secondaryButton(c, "Cancel issue").TextColor(t.Warning).OnClick(func() {
				if !a.cancelIssueProduct(issue.ID) {
					a.issueActionError = "Could not cancel this issue; its state was not changed."
				}
			})
		}
		secondaryButton(c, "Archive").OnClick(func() {
			if !a.archiveIssue(issue.ID) {
				a.issueActionError = "Could not archive this issue."
			} else {
				a.issueFilter = "archived"
			}
		})
	})
	if issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
		secondaryButton(c, "Reopen issue").OnClick(func() {
			if !a.reopenIssue(issue.ID) {
				a.issueActionError = "Could not reopen this issue."
			} else {
				a.issueFilter = "active"
			}
		})
		return
	}
	if editable && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle && issueRunBlocked(issue) == "" {
		ui.Text(c, "Completion evidence").FontSize(14).TextColor(t.TextMuted)
		draft := a.completionDraft(issue)
		ui.TextInput(c, draft).Label("Completion evidence").FontSize(14).Placeholder("What proves this issue is complete?")
		secondaryButton(c, "Mark complete").OnClick(func() {
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
				label := issueFilterActionLabel(candidate)
				if candidate == filter {
					label = "• " + label
				}
				button := secondaryButton(c, label)
				if candidate == filter {
					button.BorderWidth(2).BorderColor(t.Accent)
				}
				button.OnClick(func() { a.issueFilter = candidate })
			}
		})
	}
	issues := ct.issuesForFilter(filter)
	if len(issues) == 0 {
		ui.Text(c, "No issues in "+issueFilterLabel(filter)+".").FontSize(14).TextColor(t.TextMuted)
		return
	}
	ui.Text(c, "Issues · "+issueFilterLabel(filter)).FontSize(14).FontWeight(500).TextColor(t.TextMuted)
	ui.Column(c).Gap(3).Children(func() {
		for _, candidate := range issues {
			candidate := candidate
			button := secondaryButton(c, "").FillWidth()
			if selected := ct.selectedIssue(); selected != nil && selected.ID == candidate.ID {
				button.BorderWidth(2).BorderColor(t.Accent)
			}
			button.Children(func() {
				ui.Column(c).Gap(1).Children(func() {
					ui.Text(c, issueStatusLabel(candidate)).FontSize(12).TextColor(t.TextMuted)
					ui.Text(c, candidate.Goal).FontSize(14).LineHeight(1.3).MaxLines(2).TextColor(t.Text)
				})
			})
			button.OnClick(func() { a.selectIssue(candidate.ID) })
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
		ui.Text(c, "This contact already has a session, but its history has not been loaded yet; existing work is not shown as absent.").FontSize(14).LineHeight(1.45).TextColor(t.TextMuted)
		if a.runtimeStatus != runtimeStateReady {
			ui.Text(c, "Runtime is not ready; work history cannot be read yet.").FontSize(14).LineHeight(1.45).TextColor(t.Warning)
		}
		return
	}
	if !a.demo && a.runtimeStatus != runtimeStateReady {
		ui.Text(c, "Work is unavailable while Runtime is not ready").FontSize(14).FontWeight(500).TextColor(t.Text)
		ui.Text(c, "No work is shown until the Runtime connection is ready; this is not a claim that no work exists.").FontSize(14).LineHeight(1.45).TextColor(t.TextMuted)
		return
	}
	ui.Text(c, "No current work").FontSize(14).FontWeight(500).TextColor(t.Text)
	ui.Text(c, "Work will appear here when "+ct.Name+" starts a task. Tool activity and results stay out of the conversation.").FontSize(14).LineHeight(1.45).TextColor(t.TextMuted)
}

func (a *appModel) workCard(c *ui.Context, t *ui.Theme, w *workItem, isCurrent, issueWork bool) {
	ui.Column(c).Padding(14).Radius(13).Background(a.workTheme.WorkSurface).Gap(6).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, workStateLabel(w)).FontSize(14).FontWeight(600).TextColor(a.workStateColor(w.State))
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
				stop := secondaryButton(c, label).OnClick(func() {
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
			ui.Text(c, w.Prompt).FontSize(14).LineHeight(1.5).MaxLines(4).TextColor(a.workTheme.WorkText)
		}
		if len(w.Tools) > 0 {
			last := w.Tools[len(w.Tools)-1]
			action := last.Name
			if last.Finished {
				action = "Last action: " + action
			} else {
				action = "Running " + action
			}
			ui.Text(c, action).FontSize(14).LineHeight(1.4).TextColor(a.workTheme.WorkMuted)
		}
		if w.Detail != "" {
			ui.Text(c, w.Detail).FontSize(14).LineHeight(1.45).TextColor(a.workTheme.WorkAttention)
		}
	})
}

func (a *appModel) workActivity(c *ui.Context, t *ui.Theme, w *workItem) {
	ui.Text(c, "Activity").FontSize(14).FontWeight(500).TextColor(t.TextMuted)
	if len(w.Tools) == 0 {
		ui.Text(c, "No tool activity yet.").FontSize(14).TextColor(t.TextMuted)
		return
	}
	a.toolCalls(c, t, w.Tools)
}

func (a *appModel) unlinkedActivity(c *ui.Context, t *ui.Theme, tools []*workTool) {
	if len(tools) == 0 {
		return
	}
	ui.Text(c, "Earlier activity").FontSize(14).FontWeight(500).TextColor(t.TextMuted)
	ui.Text(c, "No task ID was recorded; these tool results are shown as readable history, without a claimed task.").FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
	a.toolCalls(c, t, tools)
}

func (a *appModel) toolCalls(c *ui.Context, t *ui.Theme, tools []*workTool) {
	for _, call := range tools {
		call := call
		ui.Column(c).Padding(8).Radius(8).Background(t.SurfaceHover).Gap(3).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Text(c, toolTitleFor(call.Name, call.Args)).FontSize(14).LineHeight(1.4).TextColor(t.Text)
				ui.Spacer(c)
				state := "Running"
				if call.Finished {
					state = "Finished"
				}
				ui.Text(c, state).FontSize(14).TextColor(t.TextMuted)
			})
			if call.Output != "" {
				summary := toolOutputSummary(call.Output)
				if summary != "" {
					// Keep long output (including unbroken JSON/paths) in its
					// own full-width line so it cannot push the actions out of
					// the card.
					ui.Text(c, "Preview · "+summary).FontSize(14).LineHeight(1.4).MaxLines(4).TextColor(t.TextMuted)
				}
				label := "Details"
				if call.Expanded {
					label = "Hide"
				}
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					if !call.Expanded {
						secondaryButton(c, "Copy received output").OnClick(func() {
							if err := writeClipboardText(call.Output, c.Services().WriteClipboard, c.Services().ReadClipboard); err != nil {
								c.Toast("Copy failed: " + err.Error())
							} else {
								c.Toast("Copied received output")
							}
						})
					}
					secondaryButton(c, label).OnClick(func() { call.Expanded = !call.Expanded })
				})
				if call.Expanded {
					markdownReader(c, call.Output, markdownReaderOptions{PlainText: true, CopyLabel: "Copy received output"}).
						FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
				}
			} else if call.Started && !call.Finished {
				ui.Text(c, "Tool is running.").FontSize(14).TextColor(t.TextMuted)
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
