package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"saki/desktop/locales"
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
		return "Stopped: interrupted"
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

func (a *appModel) toggleRail() {
	a.railCollapsed = !a.railCollapsed
	a.focusRail = true
}

// layoutBody draws the contact rail and the main conversation. A visible
// work panel nests a second Split so either edge can be resized independently.
func (a *appModel) layoutBody(c *ui.Context, t *ui.Theme) {
	if a.detailPageActive() {
		a.detailPage(c, t)
		return
	}
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
	if !a.workspaceOpen {
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

// workspaceFrame draws the current right detail in its own column.
func (a *appModel) workspaceFrame(c *ui.Context, t *ui.Theme) {
	ui.Column(c).FillWidth().FillHeight().Background(a.workTheme.Panel).BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() {
		a.detailContent(c, t, false)
	})
}

// detailPage is the non-modal narrow rendering of the same right detail. It
// replaces the whole workbench body, so the 480px reading column is not
// squeezed or covered by a backdrop/focus trap.
func (a *appModel) detailPage(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Fill().Background(a.workTheme.Panel).Children(func() {
		a.detailContent(c, t, true)
	})
}

func (a *appModel) detailContent(c *ui.Context, t *ui.Theme, narrow bool) {
	switch a.activeDetail() {
	case detailHistory:
		a.historyDetail(c, t, narrow)
	case detailProfile:
		a.profileDetail(c, t, narrow)
	case detailWork:
		a.workspaceContent(c, t, narrow)
	}
}

func (a *appModel) workspaceContent(c *ui.Context, t *ui.Theme, narrow bool) {
	ct := a.selectedContact()
	nav := ct.issueNavigation()
	issue := ct.issueByID(nav.DetailID)
	if nav.DetailID != "" && issue == nil {
		nav.DetailID = ""
	}
	msgs := locales.In(c.Locale())
	title := msgs.WorkTasksTab()
	if nav.ShowConversationWork {
		title = msgs.WorkChatRunsTab()
	} else if issue != nil {
		title = msgs.WorkTask()
	}
	newIssueAction := func() {
		if issue != nil || nav.ShowConversationWork || a.issueFormOpen {
			return
		}
		newIssue := secondaryButton(c, "New issue").FontWeight(500).OnClick(func() {
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
	ui.Column(c).FillWidth().Padding(12).Gap(8).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			if narrow {
				backAction := func() { a.closeDetail() }
				backLabel, backTip, backVisible := msgs.WorkBackToChat(), msgs.WorkBackToChat(), msgs.WorkBackToChat()
				if nav.ShowConversationWork {
					backAction = func() { a.backFromConversationWork() }
					backLabel, backTip, backVisible = msgs.WorkBackToIssues(), msgs.WorkBackToIssues(), msgs.WorkBackToIssues()
				} else if issue != nil {
					backAction = func() { a.backToIssueOverview() }
					backLabel, backTip, backVisible = msgs.WorkBackToIssues(), msgs.WorkBackToIssues(), msgs.WorkBackToIssues()
				}
				back := detailBackButton(c, t, backLabel, backTip, backVisible).OnClick(backAction)
				if nav.FocusBack {
					back.Focus()
					nav.FocusBack = false
				}
				a.focusDetailControl(c, back)
				ui.Text(c, title).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text).Grow(1).MaxLines(2)
				newIssueAction()
				return
			}
			ui.Text(c, title).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text).Grow(1)
			newIssueAction()
			close := ui.Button(c, "").Label(msgs.WorkClose()).MinWidth(32).Width(32).Height(32).Padding(0)
			close = appTooltip(c, close, msgs.WorkCloseTooltip()).OnClick(func() { a.closeDetail() })
			close.Children(func() { ui.Icon(c, navCloseIcon).Width(18).Height(18).TextColor(t.TextMuted) })
			a.focusDetailControl(c, close)
		})
		tab := 0
		if nav.ShowConversationWork {
			tab = 1
		}
		tabs := ui.Segmented(c, &tab, msgs.WorkTasksTab(), msgs.WorkChatRunsTab()).Label(msgs.WorkTabs()).
			FontSize(typeAction).Height(32).MaxWidth(360).FillWidth()
		if nav.FocusConversation {
			tabs.Focus()
			nav.FocusConversation = false
		}
		if tabs.Changed() {
			nav.ShowConversationWork = tab == 1
		}
	})
	switch {
	case nav.ShowConversationWork:
		a.conversationWorkView(c, t, ct, narrow)
	case issue != nil:
		a.issueDetailView(c, t, ct, issue, nav, narrow)
	default:
		a.issueOverviewView(c, t, ct, nav)
	}
}

func (a *appModel) issueOverviewView(c *ui.Context, t *ui.Theme, ct *contact, nav *issueViewState) {
	if a.issueFormOpen {
		a.issueFormView(c, t)
	}
	ui.Column(c).FillWidth().Grow(1).Padding(12).Gap(8).Children(func() {
		a.issueFilterChoice = issueFilterChoiceLabel(nav.Filter)
		filter := selectWithoutChevron(c, &a.issueFilterChoice, []string{"Unfinished", "Completed", "All"}, "Issue filter")
		if filter.Changed() {
			a.setIssueFilter(ct, issueFilterFromChoice(a.issueFilterChoice))
			nav = ct.issueNavigation()
		}
		ui.Scroll(c).Grow(1).FillWidth().TrackScroll(&nav.OverviewScroll).Children(func() {
			ui.Column(c).Gap(3).Children(func() {
				a.issueList(c, t, ct)
			})
		})
	})
}

func issueRunLabel(msgs locales.Messages, status string) string {
	switch status {
	case issueExecutionFailed:
		return msgs.TaskRunFailed()
	case issueExecutionInterrupted:
		return msgs.TaskRunInterrupted()
	case issueExecutionUnknown:
		return msgs.TaskRunUnknown()
	case issueExecutionCompleted:
		return msgs.TaskRunCompleted()
	case issueExecutionRunning:
		return msgs.TaskRunRunning()
	case issueExecutionAccepted:
		return msgs.TaskRunAccepted()
	case issueExecutionPending:
		return msgs.TaskRunPending()
	case issueExecutionNotDispatched:
		return msgs.TaskRunNotDispatched()
	case issueExecutionRejected:
		return msgs.TaskRunRejected()
	default:
		return ""
	}
}

func issueDisplayLabel(msgs locales.Messages, display issueDisplayStatus) string {
	if display.Archived {
		return msgs.TaskStatusArchived()
	}
	switch display.Status {
	case issueStatusCompleted:
		return msgs.TaskStatusCompleted()
	case issueStatusCanceled:
		return msgs.TaskStatusCanceled()
	case issueStatusNeedsAttention:
		return msgs.TaskStatusNeedsAttention()
	case issueStatusWaitingUser:
		return msgs.TaskStatusWaiting()
	case issueStatusInProgress:
		return msgs.TaskStatusInProgress()
	case issueStatusNotStarted:
		return msgs.TaskStatusNotStarted()
	default:
		return msgs.TaskStatusNotStarted()
	}
}

func issueDisplayColor(t *ui.Theme, display issueDisplayStatus) ui.Color {
	if display.Archived || display.Status == issueStatusCanceled || display.Status == issueStatusNotStarted {
		return t.TextMuted
	}
	switch display.Status {
	case issueStatusNeedsAttention, issueStatusWaitingUser:
		return t.Warning
	default:
		return t.Success
	}
}

func (a *appModel) issueOverviewRow(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue) {
	if issue == nil {
		return
	}
	nav := ct.issueNavigation()
	row := ui.ButtonBase(c).Label("Issue " + issue.Goal).Gap(8).Padding(8).Radius(8).
		Background(ui.Transparent).BorderWidth(0).AlignItems(ui.Start)
	if row.Pressed() {
		row.Background(t.SurfacePressed)
	} else if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.OnClick(func() { a.selectIssue(issue.ID) })
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				goalWeight := 400
				if nav.DetailID == issue.ID {
					goalWeight = 500
				}
				ui.Text(c, issue.Goal).FontSize(typeAction).FontWeight(goalWeight).LineHeight(1.35).MaxLines(2).TextColor(t.Text).Grow(1)
				ui.Text(c, "›").FontSize(16).TextColor(t.TextMuted)
			})
			msgs := locales.In(c.Locale())
			display := issueDisplayStatusFor(issue)
			statusColor := issueDisplayColor(t, display)
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Text(c, "●").FontSize(12).TextColor(statusColor)
				ui.Text(c, issueDisplayLabel(msgs, display)).FontSize(12).LineHeight(1.4).MaxLines(1).TextColor(statusColor)
			})
			if latest := latestIssueExecution(issue); latest != nil {
				runLabel := issueRunLabel(msgs, latest.Status)
				sameCompleted := display.Status == issueStatusCompleted && latest.Status == issueExecutionCompleted
				sameActive := display.Status == issueStatusInProgress && display.Active &&
					(latest.Status == issueExecutionAccepted || latest.Status == issueExecutionRunning)
				if runLabel != "" && !sameCompleted && !sameActive {
					runColor := t.TextMuted
					if issueExecutionNeedsAttention(latest.Status) {
						runColor = t.Warning
					}
					ui.Text(c, runLabel).FontSize(12).LineHeight(1.4).MaxLines(1).TextColor(runColor)
				}
			}
		})
	})
	if nav.FocusRowID == issue.ID {
		row.Focus()
		row.ScrollIntoView()
		nav.FocusRowID = ""
	}
}

func (a *appModel) issueDetailView(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue, nav *issueViewState, narrow bool) {
	work := ct.issueWork(issue)
	active := work != nil && issue.Work.ActiveTaskID == work.TaskID
	explicit := work != nil && ct.SelectedWorkTaskID != "" && ct.SelectedWorkTaskID == work.TaskID
	ui.Scroll(c).Grow(1).FillWidth().TrackScroll(&nav.DetailScroll).Padding(0, 12).Children(func() {
		ui.Column(c).Gap(8).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				if !narrow {
					back := detailBackButton(c, t, locales.In(c.Locale()).WorkBackToIssues(), locales.In(c.Locale()).WorkBackToIssues(), locales.In(c.Locale()).WorkBackToIssues()).OnClick(func() { a.backToIssueOverview() })
					if nav.FocusBack {
						back.Focus()
						nav.FocusBack = false
					}
				}
				ui.Spacer(c)
				if active {
					label := "Stop issue task"
					if work.State == workStopping {
						label = "Stopping issue…"
					}
					stop := secondaryButton(c, label).TextColor(t.Warning).OnClick(func() {
						a.cancelWorkTask(ct, work.TaskID)
					})
					if work.State == workStopping {
						stop.Disabled(true)
					}
				}
			})
			if explicit {
				ui.Row(c).Children(func() {
					secondaryButton(c, "Back to issue").OnClick(func() {
						ct.SelectedWorkTaskID = ""
						nav.FocusBack = true
					})
				})
			}
			if active {
				ui.Text(c, "Current run: "+workStateLabel(work)).FontSize(12).LineHeight(1.4).MaxLines(2).TextColor(a.workStateColor(work.State))
			}
			display := issueDisplayStatusFor(issue)
			if issue.openQuestion() == nil {
				ui.Text(c, issueDisplayLabel(locales.In(c.Locale()), display)).FontSize(12).FontWeight(500).TextColor(issueDisplayColor(t, display))
			}
			if issue.Status == issueStatusCompleted {
				ui.Text(c, "Marked by user: "+issueEvidenceText(issue)).FontSize(12).LineHeight(1.4).TextColor(t.TextMuted).MaxLines(3)
			}
			if a.issueFormOpen && a.issueFormEditingID != "" {
				a.issueFormView(c, t)
			}
			if explicit {
				a.issueResultContext(c, t, issue, work)
				a.workCard(c, t, work, active, true)
				a.workActivity(c, t, work)
				ui.Divider(c)
			}
			a.issueCard(c, t, ct, issue)
		})
	})
}

func (a *appModel) conversationWorkView(c *ui.Context, t *ui.Theme, ct *contact, narrow bool) {
	nav := ct.issueNavigation()
	ui.Scroll(c).Grow(1).FillWidth().Padding(0, 12).Children(func() {
		ui.Column(c).Gap(8).Children(func() {
			ui.Row(c).Children(func() {
				if !narrow {
					back := detailBackButton(c, t, locales.In(c.Locale()).WorkBackToIssues(), locales.In(c.Locale()).WorkBackToIssues(), locales.In(c.Locale()).WorkBackToIssues()).OnClick(func() { a.backFromConversationWork() })
					if nav.FocusBack {
						back.Focus()
						nav.FocusBack = false
					}
				}
			})
			work := conversationWork(ct)
			if work == nil {
				a.workEmptyState(c, t, ct)
			} else {
				title := "Recent work"
				if ct.ActiveTaskID == work.TaskID {
					title = "Current work"
				}
				ui.Text(c, title).FontSize(typePageTitle).FontWeight(500).TextColor(t.Text)
				a.workCard(c, t, work, ct.ActiveTaskID == work.TaskID, false)
				a.workActivity(c, t, work)
			}
			a.unlinkedActivity(c, t, ct.UnlinkedTools)
		})
	})
}

func (a *appModel) issueFormView(c *ui.Context, t *ui.Theme) {
	title := "New issue"
	if a.issueFormEditingID != "" {
		title = "Edit issue"
	}
	ui.Column(c).Padding(8).Radius(8).Background(a.workTheme.Well).Gap(4).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(typeBody).FontWeight(500).TextColor(t.Text)
			ui.Spacer(c)
			secondaryButton(c, "Cancel").OnClick(func() { a.cancelIssueForm() })
		})
		ui.Text(c, "Goal").FontSize(typeAction).FontWeight(500).TextColor(t.Text)
		goal := ui.TextArea(c, &a.issueGoal).Label("Issue goal").FontSize(14).Placeholder("What should this issue accomplish?").Height(56)
		if a.focusIssueGoal {
			goal.Focus()
			a.focusIssueGoal = false
		}
		ui.Text(c, "Requirements").FontSize(typeAction).FontWeight(500).TextColor(t.Text)
		ui.TextArea(c, &a.issueRequirements).Label("Issue requirements").FontSize(14).Placeholder("One requirement per line; edits start unverified").Height(72)
		if a.issueFormError != "" {
			ui.Text(c, a.issueFormError).FontSize(14).LineHeight(1.5).TextColor(t.Danger)
		}
		ui.Row(c).Gap(8).Children(func() {
			ui.PrimaryButton(c, "Save issue").FontSize(typeAction).MinHeight(32).OnClick(func() { a.saveIssueForm() })
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
	ui.Text(c, fmt.Sprintf("Run %d %s: %s", index+1, label, issue.Goal)).FontSize(typeAction).FontWeight(500).TextColor(t.TextMuted).MaxLines(2)
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

func issueHasReadableResult(ct *contact, execution *issueExecution) bool {
	if execution == nil || execution.Status != issueExecutionCompleted {
		return false
	}
	if strings.TrimSpace(ct.resultTextForExecution(execution)) != "" {
		return true
	}
	work := workFor(ct, execution.TaskID)
	if work == nil {
		return false
	}
	for _, tool := range work.Tools {
		if tool != nil && tool.Finished && strings.TrimSpace(tool.Output) != "" {
			return true
		}
	}
	return false
}

func (a *appModel) issuePrimaryAction(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue) string {
	msgs := locales.In(c.Locale())
	editable := !issue.Archived && issue.Status != issueStatusCompleted && issue.Status != issueStatusCanceled
	if issue.openQuestion() != nil && editable {
		ui.Row(c).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			secondaryButton(c, msgs.WorkAnswer()).FontWeight(500).OnClick(func() { a.openIssueSupplementForm(issue.ID) })
		})
		return "answer"
	}
	startReason := ""
	if !issueManagementTerminal(issue) && !issue.Archived {
		startReason = issueLifecycleStartBlocked(issue)
		if startReason == "" {
			startReason = a.canStartIssue(ct, issue)
		}
		if startReason == "" {
			startReason = issueRunBlocked(issue)
		}
		if startReason == "" {
			label := msgs.WorkStart()
			if len(issue.Executions) > 0 {
				label = msgs.WorkContinue()
			}
			if issue.Supplements != nil && issue.Supplements.pendingCount() > 0 {
				if len(issue.Executions) == 0 {
					label = msgs.WorkStartWithSupplement()
				} else {
					label = msgs.WorkContinueWithSupplement()
				}
			}
			ui.Row(c).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
				secondaryButton(c, label).FontWeight(500).OnClick(func() {
					if !a.runIssue(issue.ID) {
						a.issueActionError = a.issueFormError
					}
				})
			})
			return "continue"
		}
	}
	if latest := latestIssueExecution(issue); issueHasReadableResult(ct, latest) {
		ui.Row(c).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			read := secondaryButton(c, msgs.WorkReadResult()).FontWeight(500).OnClick(func() {
				a.openIssueResultFromLatest(issue.ID, latest.ID)
			})
			if a.issueResultShouldFocus(issue.ID, latest.ID, true) {
				read.Focus()
				a.consumeIssueResultReturnFocus()
			}
		})
		return "read"
	}
	if startReason != "" {
		ui.Text(c, msgs.WorkNextActionUnavailable(startReason)).FontSize(12).LineHeight(1.4).TextColor(t.TextMuted).MaxLines(2)
	}
	return ""
}

func (a *appModel) issueLifecycleActions(c *ui.Context, t *ui.Theme, issue *productIssue) {
	msgs := locales.In(c.Locale())
	if issue.Archived {
		ui.Row(c).Wrap().Gap(6).Children(func() {
			secondaryButton(c, msgs.WorkRestore()).OnClick(func() {
				if !a.restoreIssue(issue.ID) {
					a.issueActionError = "Could not restore this issue."
				}
			})
		})
		return
	}
	editable := issue.Status != issueStatusCompleted && issue.Status != issueStatusCanceled
	if !editable {
		if reason := issueRunBlocked(issue); reason == "" {
			ui.Row(c).Wrap().Gap(6).Children(func() {
				secondaryButton(c, msgs.WorkReopen()).OnClick(func() {
					if !a.reopenIssue(issue.ID) {
						a.issueActionError = "Could not reopen this issue."
					}
				})
			})
		} else {
			ui.Text(c, msgs.WorkNextActionUnavailable(reason)).FontSize(12).LineHeight(1.4).TextColor(t.TextMuted).MaxLines(2)
		}
		return
	}
	ui.Row(c).Wrap().AlignItems(ui.Center).Gap(6).Children(func() {
		secondaryButton(c, msgs.WorkCancel()).TextColor(t.Warning).OnClick(func() {
			if !a.cancelIssueProduct(issue.ID) {
				a.issueActionError = "Could not cancel this issue; its state was not changed."
			}
		})
	})
	if issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle && issueRunBlocked(issue) == "" {
		ui.Text(c, msgs.WorkCompletionEvidence()).FontSize(12).TextColor(t.TextMuted)
		draft := a.completionDraft(issue)
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.TextInput(c, draft).Label(msgs.WorkCompletionEvidence()).FontSize(14).Placeholder(msgs.WorkCompletionEvidencePlaceholder()).Grow(1)
			secondaryButton(c, msgs.WorkComplete()).OnClick(func() {
				if !a.completeIssue(issue.ID, *draft) {
					a.issueActionError = "Could not complete this issue."
				}
			})
		})
	}
}

func (a *appModel) issueCard(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue) {
	editable := !issue.Archived && issue.Status != issueStatusCompleted && issue.Status != issueStatusCanceled
	expanded := a.issueDetailOpen[issue.ID]
	ui.Column(c).Padding(8).Radius(8).Background(a.workTheme.Well).Gap(4).Children(func() {
		ui.Column(c).Gap(2).Children(func() {
			ui.Text(c, issue.Goal).FontSize(typePageTitle).FontWeight(500).LineHeight(1.5).TextColor(t.Text)
		})
		if a.issueActionError != "" {
			ui.Text(c, a.issueActionError).FontSize(14).LineHeight(1.5).TextColor(t.Warning)
		}
		if question := issue.openQuestion(); question != nil {
			display := issueDisplayStatusFor(issue)
			ui.Column(c).Padding(8).Radius(8).Background(t.Surface).Gap(3).Children(func() {
				ui.Text(c, issueDisplayLabel(locales.In(c.Locale()), display)).FontSize(typeAction).FontWeight(500).TextColor(issueDisplayColor(t, display))
				ui.Text(c, question.Text).FontSize(14).LineHeight(1.5).TextColor(t.Text)
			})
		}
		primaryKind := a.issuePrimaryAction(c, t, ct, issue)
		a.issueLatestResult(c, t, ct, issue, primaryKind != "read")
		if latest := latestIssueExecution(issue); latest != nil && latest.Status == issueExecutionCompleted &&
			issue.Status != issueStatusCompleted && issue.Status != issueStatusCanceled && issueHasRemainingRequirements(issue) {
			ui.Text(c, "This execution ended; the issue remains open.").FontSize(typeAction).LineHeight(1.4).TextColor(t.TextMuted)
		}
		a.issueRequirementSummary(c, t, issue)
		a.issueLifecycleActions(c, t, issue)
		label := "More issue actions"
		if expanded {
			label = "Hide issue actions"
		}
		ui.Row(c).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			secondaryButton(c, label).OnClick(func() {
				if a.issueDetailOpen == nil {
					a.issueDetailOpen = map[string]bool{}
				}
				a.issueDetailOpen[issue.ID] = !a.issueDetailOpen[issue.ID]
			})
		})
		if expanded {
			ui.Row(c).Wrap().Gap(6).Children(func() {
				if editable {
					secondaryButton(c, "Edit issue").OnClick(func() { a.openEditIssueForm(issue) })
				}
				if issue.WorkSessionID != "" {
					modelButton := secondaryButton(c, "Issue model").OnClick(func() {
						a.openSessionModel(issue.WorkSessionID, "work", issue.Goal)
					})
					if a.focusIssueModel {
						modelButton.Focus()
						a.focusIssueModel = false
					}
				}
			})
			a.issueActions(c, t, ct, issue, editable)
			evidence := issueEvidenceText(issue)
			if issue.Evidence != "" && issue.Status != issueStatusCompleted {
				evidence = "Previous completion evidence: " + issue.Evidence
			}
			ui.Text(c, "Completion evidence").FontSize(14).TextColor(t.TextMuted)
			ui.Text(c, evidence).FontSize(14).LineHeight(1.5).TextColor(t.Text).MaxLines(4)
			a.issueRequirementEvidence(c, t, ct, issue, editable)
			a.issueSupplementSection(c, t, ct, issue, editable)
			a.issueExecutions(c, t, ct, issue)
		} else if a.issueSupplementFormID == issue.ID {
			a.issueSupplementSection(c, t, ct, issue, editable)
		}
		if executionHasUnknownRecovery(issue) {
			ui.Text(c, "Recovery is available for an execution without a trusted terminal state.").FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
		}
	})
}

func (a *appModel) issueLatestResult(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue, showRead bool) {
	execution := latestIssueExecution(issue)
	if execution == nil {
		ui.Text(c, "No execution yet").FontSize(typeAction).FontWeight(500).TextColor(t.TextMuted)
		return
	}
	ui.Text(c, "Latest execution").FontSize(typeGroupTitle).FontWeight(500).TextColor(t.TextMuted)
	ui.Text(c, issueExecutionLabel(execution)).FontSize(14).TextColor(t.TextMuted)
	text := strings.TrimSpace(ct.resultTextForExecution(execution))
	work := workFor(ct, execution.TaskID)
	switch {
	case execution.Status == issueExecutionAccepted || execution.Status == issueExecutionRunning:
		if text == "" {
			ui.Text(c, "No result yet; this execution is still running.").FontSize(14).LineHeight(1.5).TextColor(t.Text).MaxLines(3)
		} else {
			ui.Text(c, "Current response (execution still running): "+truncate(text, 300)).FontSize(14).LineHeight(1.5).TextColor(t.Text).MaxLines(3)
		}
	case execution.Status == issueExecutionCompleted:
		if text == "" {
			if work != nil && len(work.Tools) > 0 {
				ui.Text(c, "No assistant message was recorded; open the execution to inspect tool output.").FontSize(14).LineHeight(1.5).TextColor(t.Text).MaxLines(3)
			} else {
				ui.Text(c, "No assistant message was recorded for this execution.").FontSize(14).LineHeight(1.5).TextColor(t.Text).MaxLines(3)
			}
		} else {
			ui.Text(c, "Result preview: "+truncate(text, 300)).FontSize(14).LineHeight(1.5).TextColor(t.Text).MaxLines(3)
		}
	default:
		if text != "" {
			ui.Text(c, "Result preview: "+truncate(text, 300)).FontSize(14).LineHeight(1.5).TextColor(t.Text).MaxLines(3)
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
		ui.Text(c, truncate(reason, 180)).FontSize(14).LineHeight(1.5).TextColor(t.Warning).MaxLines(3)
	}
	if showRead {
		ui.Row(c).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			read := secondaryButton(c, locales.In(c.Locale()).WorkReadResult()).OnClick(func() { a.openIssueResultFromLatest(issue.ID, execution.ID) })
			if a.issueResultShouldFocus(issue.ID, execution.ID, true) {
				read.Focus()
				a.consumeIssueResultReturnFocus()
			}
		})
	}
	if work != nil {
		ui.Row(c).Wrap().Gap(6).Children(func() {
			secondaryButton(c, "View execution").OnClick(func() { a.showIssueWork(issue.ID, execution.TaskID) })
		})
	}
}

func (a *appModel) issueRequirementSummary(c *ui.Context, t *ui.Theme, issue *productIssue) {
	if len(issue.Requirements) == 0 {
		ui.Text(c, "Requirements").FontSize(typeGroupTitle).FontWeight(500).TextColor(t.TextMuted)
		ui.Text(c, "No requirements recorded.").FontSize(14).TextColor(t.TextMuted)
		return
	}
	ui.Text(c, "Requirements").FontSize(typeGroupTitle).FontWeight(500).TextColor(t.TextMuted)
	for _, requirement := range issue.Requirements {
		if requirement == nil {
			continue
		}
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Text(c, requirement.Text).FontSize(14).LineHeight(1.5).TextColor(t.Text).Grow(1)
			ui.Text(c, requirementStateLabel(requirement.State)).FontSize(14).TextColor(requirementStateColor(t, requirement.State))
		})
	}
}

func (a *appModel) issueRequirementEvidence(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue, editable bool) {
	if len(issue.Requirements) == 0 {
		return
	}
	ui.Text(c, "Requirement evidence").FontSize(typeGroupTitle).FontWeight(500).TextColor(t.TextMuted)
	for _, requirement := range issue.Requirements {
		if requirement == nil {
			continue
		}
		ui.Column(c).Padding(6).Radius(6).Background(t.Surface).Gap(3).Children(func() {
			ui.Text(c, requirement.Text).FontSize(14).LineHeight(1.5).TextColor(t.Text).MaxLines(2)
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
				ui.Text(c, "Evidence: "+requirement.Evidence).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted).MaxLines(2)
			}
		})
	}
}

func (a *appModel) issueExecutions(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue) {
	if len(issue.Executions) == 0 {
		return
	}
	ui.Text(c, "Execution history").FontSize(typeGroupTitle).FontWeight(500).TextColor(t.TextMuted)
	for index, execution := range issue.Executions {
		if execution == nil {
			continue
		}
		w := workFor(ct, execution.TaskID)
		ui.Column(c).Padding(8).Radius(8).Background(t.Surface).Gap(4).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, fmt.Sprintf("Execution %d: %s", index+1, issueExecutionLabel(execution))).FontSize(14).TextColor(t.TextMuted)
				if execution.RequirementsRevision > 0 {
					ui.Text(c, fmt.Sprintf("revision %d", execution.RequirementsRevision)).FontSize(14).TextColor(t.TextMuted)
				}
			})
			contextCount := len(executionTaskContextItems(ct, issue, execution))
			if execution.Prompt != "" || contextCount > 0 {
				label := "Show sent snapshot"
				if contextCount > 0 {
					label = fmt.Sprintf("Sent task context: %d", contextCount)
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
					ui.Text(c, "Sent snapshot: "+truncate(execution.Prompt, 240)).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted).MaxLines(3)
				}
				a.executionTaskContextView(c, t, ct, issue, execution)
			}
			if execution.Detail != "" {
				ui.Text(c, execution.Detail).FontSize(14).LineHeight(1.5).TextColor(t.Warning)
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
			ui.Column(c).AlignItems(ui.Start).Gap(6).Children(func() {
				resultButton := secondaryButton(c, fmt.Sprintf("Open result: run %d", index+1)).OnClick(func() { a.openIssueResult(issue.ID, execution.ID) })
				if a.issueResultShouldFocus(issue.ID, execution.ID, false) {
					resultButton.Focus()
					a.consumeIssueResultReturnFocus()
				}
				if w != nil {
					secondaryButton(c, fmt.Sprintf("View execution: run %d", index+1)).OnClick(func() { a.showIssueWork(issue.ID, execution.TaskID) })
				}
			})
		})
	}
}

func (a *appModel) issueActions(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue, editable bool) {
	if issue.Archived {
		return
	}
	if editable {
		taskContextButton := func() {
			label := "Task context"
			if count := issue.Context.count(); count > 0 {
				label = fmt.Sprintf("Task context: %d", count)
			}
			button := secondaryButton(c, label).OnClick(func() { a.openIssueTaskContext(issue.ID) })
			if a.focusIssueContext {
				button.Focus()
				a.focusIssueContext = false
			}
		}
		addSupplementButton := func() {
			label := "Add supplement"
			if issue.Supplements != nil && (issue.Supplements.pendingCount() > 0 || len(issue.Supplements.Batches) > 0) {
				label = fmt.Sprintf("Supplements: %d", issue.Supplements.pendingCount()+len(issue.Supplements.Batches))
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

		ui.Row(c).Wrap().Gap(6).Children(func() {
			taskContextButton()
			addSupplementButton()
		})
		ui.Row(c).Wrap().Gap(6).Children(waitButton)
	}

	ui.Row(c).Wrap().AlignItems(ui.Center).Gap(6).Children(func() {
		secondaryButton(c, "Archive").OnClick(func() {
			if !a.archiveIssue(issue.ID) {
				a.issueActionError = "Could not archive this issue."
			}
		})
	})
}

func (a *appModel) issueList(c *ui.Context, t *ui.Theme, ct *contact) {
	nav := ct.issueNavigation()
	issues := ct.issuesForFilter(nav.Filter)
	if len(issues) == 0 {
		ui.Text(c, "No issues in "+issueFilterDescription(nav.Filter)+".").FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
		return
	}
	for _, issue := range issues {
		a.issueOverviewRow(c, t, ct, issue)
	}
}

func (a *appModel) issueStatusColor(t *ui.Theme, issue *productIssue) ui.Color {
	return issueDisplayColor(t, issueDisplayStatusFor(issue))
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
		ui.Text(c, "Session history is not loaded").FontSize(typeAction).FontWeight(500).TextColor(t.Text)
		ui.Text(c, "This contact already has a session, but its history has not been loaded yet; existing work is not shown as absent.").FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
		if a.runtimeStatus != runtimeStateReady {
			ui.Text(c, "Runtime is not ready; work history cannot be read yet.").FontSize(14).LineHeight(1.5).TextColor(t.Warning)
		}
		return
	}
	if !a.demo && a.runtimeStatus != runtimeStateReady {
		ui.Text(c, "Work is unavailable while Runtime is not ready").FontSize(typeAction).FontWeight(500).TextColor(t.Text)
		ui.Text(c, "No work is shown until the Runtime connection is ready; this is not a claim that no work exists.").FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
		return
	}
	ui.Text(c, "No current work").FontSize(typePageTitle).FontWeight(500).TextColor(t.Text)
	ui.Text(c, "Work will appear here when "+ct.Name+" starts a task. Tool activity and results stay out of the conversation.").FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
}

func (a *appModel) workCard(c *ui.Context, t *ui.Theme, w *workItem, isCurrent, issueWork bool) {
	ui.Column(c).Padding(8).Radius(8).Background(a.workTheme.WorkSurface).Border(0.5, t.Border).Gap(4).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, workStateLabel(w)).FontSize(typeAction).FontWeight(500).TextColor(a.workStateColor(w.State))
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
				stop := secondaryButton(c, label).FontWeight(500).OnClick(func() {
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
			ui.Text(c, w.Prompt).FontSize(typeBody).LineHeight(1.5).MaxLines(4).TextColor(a.workTheme.WorkText)
		}
		if len(w.Tools) > 0 {
			last := w.Tools[len(w.Tools)-1]
			action := last.Name
			if last.Finished {
				action = "Last action: " + action
			} else {
				action = "Running " + action
			}
			ui.Text(c, action).FontSize(14).LineHeight(1.5).TextColor(a.workTheme.WorkMuted)
		}
		if w.Detail != "" {
			ui.Text(c, w.Detail).FontSize(14).LineHeight(1.5).TextColor(a.workTheme.WorkAttention)
		}
	})
}

func (a *appModel) workActivity(c *ui.Context, t *ui.Theme, w *workItem) {
	ui.Text(c, "Activity").FontSize(12).FontWeight(500).TextColor(t.TextMuted)
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
	ui.Text(c, "Earlier activity").FontSize(typeAction).FontWeight(500).TextColor(t.TextMuted)
	ui.Text(c, "No task ID was recorded; these tool results are shown as readable history, without a claimed task.").FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
	a.toolCalls(c, t, tools)
}

func (a *appModel) toolCalls(c *ui.Context, t *ui.Theme, tools []*workTool) {
	for _, call := range tools {
		call := call
		ui.Column(c).Padding(8).Radius(8).Background(a.workTheme.Well).Gap(3).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Text(c, toolTitleFor(call.Name, call.Args)).FontSize(14).LineHeight(1.5).TextColor(t.Text)
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
					ui.Text(c, "Preview: "+summary).FontSize(14).LineHeight(1.5).MaxLines(4).TextColor(t.TextMuted)
				}
				label := "Details"
				if call.Expanded {
					label = "Hide"
				}
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					if !call.Expanded {
						secondaryButton(c, locales.In(c.Locale()).CopyReceivedOutputLabel()).OnClick(func() {
							if err := writeClipboardText(call.Output, c.Services().WriteClipboard, c.Services().ReadClipboard); err != nil {
								c.Toast(locales.In(c.Locale()).CopyFailed(err.Error()))
							} else {
								c.Toast(locales.In(c.Locale()).CopiedReceivedOutput())
							}
						})
					}
					secondaryButton(c, label).OnClick(func() { call.Expanded = !call.Expanded })
				})
				if call.Expanded {
					markdownReader(c, call.Output, markdownReaderOptions{PlainText: true, CopyLabel: locales.In(c.Locale()).CopyReceivedOutputLabel()}).
						FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
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
				return name + ": " + truncate(value, 70)
			}
		}
	}
	return name + ": " + truncate(args, 70)
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
