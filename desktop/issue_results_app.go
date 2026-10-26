package main

// Application wiring for the read-only issue result page. Runtime history reads
// and filesystem inspection run off the render path and return through the
// existing UI hook with contact/issue/execution/session/generation checks.

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

type issueResultPage struct {
	Open           bool
	OwnerContactID string
	IssueID        string
	ExecutionID    string
	SessionID      string
	TaskID         string
	MessageID      string
	Generation     int
	checkSeq       int
	Loading        bool
	LoadError      string
	Notice         string
	Panel          issueResultPanelState
}

func (a *appModel) issueResultAsyncStarted() { a.resultInFlight++ }

func (a *appModel) issueResultAsyncFinished() {
	if a.resultInFlight > 0 {
		a.resultInFlight--
	}
	if a.shutdownStarted && !a.quitReady {
		a.finishShutdown()
	}
}

func (a *appModel) openIssueResult(issueID, executionID string) {
	ct := a.selectedContact()
	issue, execution := ct.executionByID(executionID)
	if issue == nil || execution == nil || issue.ID != issueID {
		return
	}
	a.issueResult.Generation++
	page := &a.issueResult
	runIndex := 0
	for index, candidate := range issue.Executions {
		if candidate == execution {
			runIndex = index
			break
		}
	}
	*page = issueResultPage{
		Open: true, OwnerContactID: ct.ID, IssueID: issue.ID, ExecutionID: execution.ID,
		SessionID: execution.SessionID, TaskID: execution.TaskID, MessageID: execution.MessageID,
		Generation: a.issueResult.Generation,
		Panel: issueResultPanelState{
			RunLabel:  fmt.Sprintf("Run %d · %s", runIndex+1, issue.Goal),
			RunStatus: issueExecutionLabel(execution),
		},
	}
	a.aux = "issue-result"
	a.startIssueResultLoad()
}

func (a *appModel) startIssueResultLoad() {
	page := &a.issueResult
	if page.SessionID == "" || page.TaskID == "" {
		page.Loading = false
		page.LoadError = "This execution has no recorded Runtime session/task; results cannot be inspected."
		return
	}
	if a.rt == nil || a.runtimeStatus != runtimeStateReady || a.shutdownStarted {
		page.Loading = false
		page.LoadError = "Runtime is not ready; reopen this result after history is available."
		return
	}
	page.Loading = true
	page.LoadError = ""
	page.Notice = ""
	rt, runtimeGeneration := a.rt, a.connectionGeneration
	a.issueResultAsyncStarted()
	go a.loadIssueResult(rt, runtimeGeneration, page.Generation,
		page.OwnerContactID, page.IssueID, page.ExecutionID, page.SessionID, page.TaskID, page.MessageID)
}

func (a *appModel) reloadIssueResult(generation int) {
	page := &a.issueResult
	if !page.Open || page.Generation != generation {
		return
	}
	ct := a.contactByID(page.OwnerContactID)
	if ct == nil {
		page.LoadError = "The issue owner is no longer available; reload cannot continue."
		return
	}
	issue, execution := ct.executionByID(page.ExecutionID)
	if issue == nil || execution == nil || issue.ID != page.IssueID {
		page.LoadError = "This execution is no longer attached to the issue; reload cannot continue."
		return
	}
	runIndex := 0
	for index, candidate := range issue.Executions {
		if candidate == execution {
			runIndex = index
			break
		}
	}
	page.SessionID = execution.SessionID
	page.TaskID = execution.TaskID
	page.MessageID = execution.MessageID
	page.Panel.RunLabel = fmt.Sprintf("Run %d · %s", runIndex+1, issue.Goal)
	page.Panel.RunStatus = issueExecutionLabel(execution)
	page.Panel.Checking = false
	page.Panel.ActionFor = ""
	page.Panel.Action = issueResultFileAction{}
	page.Generation++
	page.LoadError = ""
	page.Notice = ""
	a.startIssueResultLoad()
}

func (a *appModel) loadIssueResult(rt *runtimeClient, runtimeGeneration, generation int,
	contactID, issueID, executionID, sessionID, taskID, messageID string) {
	history, historyErr := a.historyAllForSession(rt, sessionID)
	if historyErr != nil {
		message := "Execution history is unavailable: " + a.connectionRuntimeMessage(historyErr)
		a.updateUI(func() {
			defer a.issueResultAsyncFinished()
			a.applyIssueResultError(rt, runtimeGeneration, generation, contactID, issueID, executionID, sessionID, taskID, message)
		})
		return
	}

	// The history read is the authoritative path. Read the effective working
	// directory from the existing session config; only fall back to an explicit
	// session load when that config is unavailable.
	workingDirectory := ""
	if config, err := rt.connectionConfig(sessionID); err == nil {
		workingDirectory = config.EffectiveConfig.WorkingDirectory
	} else if session, loadErr := rt.loadSession(sessionID); loadErr == nil && session.Metadata != nil {
		workingDirectory = session.Metadata.WorkingDirectory
	}
	source := issueResultSource{
		ExecutionID: executionID, SessionID: sessionID, TaskID: taskID, MessageID: messageID,
		WorkingDirectory: workingDirectory,
	}
	derived := deriveIssueResultFiles(source, history)
	response := strings.Join(issueResultResponseSegments(history, taskID), "\n\n")
	notice := ""
	if workingDirectory == "" {
		notice = "Runtime session working directory is unavailable; file paths are shown as reported."
	}
	a.updateUI(func() {
		defer a.issueResultAsyncFinished()
		a.applyIssueResultLoaded(rt, runtimeGeneration, generation, contactID, issueID, executionID, sessionID, taskID,
			derived, response, notice)
	})
}

func issueResultResponseSegments(history runtimeHistory, taskID string) []string {
	projection := parseHistory(history.SessionID, history.Records, nil)
	var segments []string
	for _, message := range projection.messages {
		if message.Kind == msgContact && message.TaskID == taskID && strings.TrimSpace(message.Text) != "" {
			segments = append(segments, message.Text)
		}
	}
	return segments
}

func (a *appModel) applyIssueResultError(rt *runtimeClient, runtimeGeneration, generation int,
	contactID, issueID, executionID, sessionID, taskID, message string) {
	page := &a.issueResult
	if !a.issueResultStillOwned(rt, runtimeGeneration, generation, contactID, issueID, executionID, sessionID, taskID) {
		return
	}
	page.Loading = false
	page.LoadError = message
}

func (a *appModel) applyIssueResultLoaded(rt *runtimeClient, runtimeGeneration, generation int,
	contactID, issueID, executionID, sessionID, taskID string,
	derived issueResultDerivation, response, notice string) {
	page := &a.issueResult
	if !a.issueResultStillOwned(rt, runtimeGeneration, generation, contactID, issueID, executionID, sessionID, taskID) {
		return
	}
	page.Loading = false
	page.Notice = notice
	page.Panel.Response = response
	page.Panel.Files = derived.Files
	if derived.Unavailable != "" {
		page.LoadError = derived.Unavailable
	}
}

func (a *appModel) issueResultStillOwned(rt *runtimeClient, runtimeGeneration, generation int,
	contactID, issueID, executionID, sessionID, taskID string) bool {
	page := &a.issueResult
	return page.Open && a.aux == "issue-result" && page.Generation == generation &&
		page.OwnerContactID == contactID && page.IssueID == issueID && page.ExecutionID == executionID &&
		page.SessionID == sessionID && page.TaskID == taskID &&
		runtimeGeneration == a.connectionGeneration && a.rt == rt && a.contactByID(contactID) != nil
}

func (a *appModel) closeIssueResult() {
	a.cancelIssueResultView()
	a.aux = ""
}

func (a *appModel) cancelIssueResultView() {
	a.issueResult.Generation++
	a.issueResult.Open = false
	a.issueResult.Loading = false
	a.issueResult.LoadError = ""
	a.issueResult.Notice = ""
	a.issueResult.Panel = issueResultPanelState{}
}

func (a *appModel) issueResultPanelWidth() int {
	width := a.layoutWidth() - 40
	if width > 760 {
		width = 760
	}
	if width < 320 {
		width = 320
	}
	return width
}

func (a *appModel) issueResultPanelView(c *ui.Context, t *ui.Theme) {
	page := &a.issueResult
	if !page.Open {
		return
	}
	hasBody := page.Panel.Response != "" || len(page.Panel.Files) > 0
	if !hasBody {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "Run results").FontSize(16).FontWeight(600).TextColor(t.Text).Grow(1)
			if !page.Loading {
				ui.Button(c, "Reload result").FontSize(12).OnClick(func() { a.reloadIssueResult(page.Generation) })
			}
			ui.Button(c, "Close results").FontSize(12).OnClick(func() { a.closeIssueResult() })
		})
		if page.Panel.RunLabel != "" {
			ui.Text(c, page.Panel.RunLabel).FontSize(12).TextColor(t.TextMuted)
		}
		if page.Panel.RunStatus != "" {
			ui.Text(c, page.Panel.RunStatus).FontSize(11).FontWeight(500).TextColor(t.TextMuted)
		}
		if page.Loading {
			ui.Text(c, "Loading execution history from Runtime…").FontSize(12).TextColor(t.TextMuted)
		}
		if page.LoadError != "" {
			ui.Text(c, page.LoadError).FontSize(12).LineHeight(1.45).TextColor(t.Warning)
		}
		return
	}
	if page.Loading {
		ui.Text(c, "Reloading result from Runtime… The recorded output below is the previous snapshot.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
	}
	if page.LoadError != "" {
		ui.Text(c, page.LoadError).FontSize(11).LineHeight(1.4).TextColor(t.Warning)
	}
	if page.Notice != "" {
		ui.Text(c, page.Notice).FontSize(11).LineHeight(1.4).TextColor(t.Warning)
	}
	issueResultPanel(c, &page.Panel, a.issueResultPanelActions(page.Generation))
}

func (a *appModel) issueResultPanelActions(generation int) issueResultPanelActions {
	return issueResultPanelActions{
		Close:      func() { a.closeIssueResult() },
		Reload:     func() { a.reloadIssueResult(generation) },
		SelectFile: func(key string) { a.checkIssueResultFileByKey(generation, key) },
		CheckFile:  func(file issueResultFile) { a.checkIssueResultFile(generation, file) },
		OpenFile:   func(file issueResultFile) { a.requestIssueResultFileAction(generation, file, true) },
		RevealFile: func(file issueResultFile) { a.requestIssueResultFileAction(generation, file, false) },
		ToggleSource: func(key string) {
			page := &a.issueResult
			if page.Generation != generation {
				return
			}
			if page.Panel.SourceFor == key {
				page.Panel.SourceFor = ""
			} else {
				page.Panel.SourceFor = key
			}
		},
	}
}

func (a *appModel) checkIssueResultFileByKey(generation int, key string) {
	page := &a.issueResult
	if page.Generation != generation {
		return
	}
	for _, file := range page.Panel.Files {
		if issueResultFileKey(file) == key {
			a.checkIssueResultFile(generation, file)
			return
		}
	}
}

func (a *appModel) checkIssueResultFile(generation int, file issueResultFile) {
	page := &a.issueResult
	if page.Generation != generation {
		return
	}
	key := issueResultFileKey(file)
	page.checkSeq++
	seq := page.checkSeq
	page.Panel.Selected = key
	page.Panel.InspectFor = key
	page.Panel.Checking = true
	if page.Panel.ActionFor == key {
		page.Panel.ActionFor = ""
		page.Panel.Action = issueResultFileAction{}
	}
	rt, runtimeGeneration := a.rt, a.connectionGeneration
	inspect := a.issueResultInspect
	if inspect == nil {
		inspect = inspectIssueResultFile
	}
	a.issueResultAsyncStarted()
	go func() {
		inspection := inspect(file)
		a.updateUI(func() {
			defer a.issueResultAsyncFinished()
			page := &a.issueResult
			if page.Generation != generation || page.checkSeq != seq || !page.Open || a.aux != "issue-result" ||
				page.Panel.InspectFor != key || runtimeGeneration != a.connectionGeneration || a.rt != rt {
				return
			}
			page.Panel.Checking = false
			page.Panel.Inspection = &inspection
		})
	}()
}

func (a *appModel) requestIssueResultFileAction(generation int, file issueResultFile, open bool) {
	page := &a.issueResult
	if page.Generation != generation {
		return
	}
	key := issueResultFileKey(file)
	if page.Panel.ActionFor == key {
		page.Panel.ActionFor = ""
		page.Panel.Action = issueResultFileAction{}
	}
	openWith, revealWith := a.issueResultOpen, a.issueResultReveal
	if openWith == nil {
		openWith = requestOpenIssueResultWithSystem
	}
	if revealWith == nil {
		revealWith = requestRevealIssueResultWithSystem
	}
	rt, runtimeGeneration := a.rt, a.connectionGeneration
	a.issueResultAsyncStarted()
	go func() {
		var action issueResultFileAction
		if open {
			action = requestOpenIssueResultFile(file, openWith)
		} else {
			action = requestRevealIssueResultFile(file, revealWith)
		}
		a.updateUI(func() {
			defer a.issueResultAsyncFinished()
			page := &a.issueResult
			if page.Generation != generation || !page.Open || a.aux != "issue-result" ||
				runtimeGeneration != a.connectionGeneration || a.rt != rt {
				return
			}
			page.Panel.ActionFor = key
			page.Panel.Action = action
		})
	}()
}
