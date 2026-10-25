package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// A productIssue is the GUI-owned product object. Runtime tasks are linked as
// execution associations; an execution terminal is not an item completion.
type productIssue struct {
	ID           string
	ContactID    string
	Goal         string
	Requirements string
	Status       string
	Evidence     string
	CreatedAt    int64
	UpdatedAt    int64
	Executions   []*issueExecution

	// WorkSessionID is the issue-owned execution context. It is intentionally
	// separate from contact.SessionID (the ongoing conversation). Old
	// executions keep their actual SessionID and are never migrated in place.
	WorkSessionID string
	Work          issueWorkSession
}

// issueWorkSession is the in-memory runtime projection for one issue's
// execution context. Only WorkSessionID is persisted; messages, work items
// and task state are restored from the Runtime by session_id.
type issueWorkSession struct {
	SessionID          string
	Loaded             bool
	LoadErr            string
	ActiveTaskID       string
	LastTaskID         string
	SendState          sendState
	SendError          string
	SessionState       string
	Messages           []*message
	UnlinkedTools      []*workTool
	StopRequestedTasks map[string]bool
	pendingEvents      []runtimeEventEnvelope
	pendingExecutionID string
	historyGen         int
	HistoryDirty       bool
}

// issueExecution is the persisted association for one attempt on an issue.
// Prompt is the exact pre-dispatch snapshot; Runtime history remains the
// authority for task/message/tool results.
type issueExecution struct {
	ID        string
	SessionID string
	TaskID    string
	MessageID string
	Prompt    string
	Status    string
	Detail    string
}

const (
	issueStatusOpen = "open"

	issueExecutionPending       = "pending"
	issueExecutionNotDispatched = "not_dispatched"
	issueExecutionRejected      = "rejected"
	issueExecutionAccepted      = "accepted"
	issueExecutionRunning       = "running"
	issueExecutionCompleted     = "completed"
	issueExecutionFailed        = "failed"
	issueExecutionInterrupted   = "interrupted"
	issueExecutionUnknown       = "unknown"
)

func newIssueID() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "issue-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("issue-%d", time.Now().UnixNano())
}

func newIssueExecutionID() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "execution-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("execution-%d", time.Now().UnixNano())
}

func (ct *contact) issueByID(id string) *productIssue {
	for _, issue := range ct.Issues {
		if issue.ID == id {
			return issue
		}
	}
	return nil
}

func (ct *contact) selectedIssue() *productIssue {
	if ct.SelectedIssueID != "" {
		if issue := ct.issueByID(ct.SelectedIssueID); issue != nil {
			return issue
		}
	}
	for _, issue := range ct.Issues {
		if issue.Status == issueStatusOpen {
			return issue
		}
	}
	if len(ct.Issues) > 0 {
		return ct.Issues[0]
	}
	return nil
}

func (ct *contact) openIssues() []*productIssue {
	out := make([]*productIssue, 0, len(ct.Issues))
	for _, issue := range ct.Issues {
		if issue.Status == issueStatusOpen {
			out = append(out, issue)
		}
	}
	return out
}

func (ct *contact) executionByTask(taskID string) *issueExecution {
	if taskID == "" {
		return nil
	}
	for _, issue := range ct.Issues {
		for _, execution := range issue.Executions {
			if execution.TaskID == taskID {
				return execution
			}
		}
	}
	return nil
}

func issueHasTask(issue *productIssue, taskID string) bool {
	if issue == nil || taskID == "" {
		return false
	}
	for _, execution := range issue.Executions {
		if execution.TaskID == taskID {
			return true
		}
	}
	return false
}

func issueExecutionForTask(issue *productIssue, taskID string) (int, *issueExecution) {
	if issue == nil {
		return 0, nil
	}
	for index, execution := range issue.Executions {
		if execution.TaskID == taskID {
			return index, execution
		}
	}
	return 0, nil
}

func (ct *contact) executionByID(id string) (*productIssue, *issueExecution) {
	for _, issue := range ct.Issues {
		for _, execution := range issue.Executions {
			if execution.ID == id {
				return issue, execution
			}
		}
	}
	return nil, nil
}

func issuePrompt(issue *productIssue) string {
	goal := strings.TrimSpace(issue.Goal)
	requirements := strings.TrimSpace(issue.Requirements)
	if requirements == "" {
		return goal
	}
	return goal + "\n\nRequirements:\n" + requirements
}

// createIssue persists an explicit product item and its original requirements.
// It does not dispatch any Runtime work; dispatch is a separate Run action.
func (a *appModel) createIssue(goal, requirements string) bool {
	goal = strings.TrimSpace(goal)
	requirements = strings.TrimSpace(requirements)
	if goal == "" {
		a.issueFormError = "Enter an issue goal."
		return false
	}
	ct := a.selectedContact()
	now := nowMillis()
	item := &productIssue{
		ID: newIssueID(), ContactID: ct.ID, Goal: goal, Requirements: requirements,
		Status: issueStatusOpen, CreatedAt: now, UpdatedAt: now,
	}
	previousSelected := ct.SelectedIssueID
	ct.Issues = append(ct.Issues, item)
	ct.SelectedIssueID = item.ID
	if err := a.persist(); err != nil {
		ct.Issues = ct.Issues[:len(ct.Issues)-1]
		ct.SelectedIssueID = previousSelected
		// Keep the user's goal/requirements and form visible so they can retry.
		a.issueFormOpen = true
		a.issueFormError = "Could not save this issue: " + err.Error()
		return false
	}
	a.issueFormOpen = false
	a.issueFormError = ""
	a.issueGoal = ""
	a.issueRequirements = ""
	a.focusIssueNew = true
	return true
}

// issueEvidenceText is a factual summary only. It does not turn a completed
// Runtime task into a completed product item or infer deliverable files.
func issueEvidenceText(issue *productIssue) string {
	if strings.TrimSpace(issue.Evidence) != "" {
		return issue.Evidence
	}
	return "not recorded; a completed Runtime task is not item completion."
}

func issueExecutionLabel(execution *issueExecution) string {
	switch execution.Status {
	case issueExecutionPending:
		return "Prepared, not dispatched"
	case issueExecutionNotDispatched:
		return "Not dispatched"
	case issueExecutionRejected:
		return "Rejected"
	case issueExecutionAccepted:
		return "Accepted"
	case issueExecutionRunning:
		return "Running"
	case issueExecutionCompleted:
		return "Runtime completed"
	case issueExecutionFailed:
		return "Runtime failed"
	case issueExecutionInterrupted:
		return "Runtime interrupted"
	default:
		return "Result unknown"
	}
}

func issuesForStoredContact(all []storedIssue, contactID string) []*productIssue {
	out := make([]*productIssue, 0)
	for _, stored := range all {
		if stored.ContactID != contactID || stored.ID == "" {
			continue
		}
		issue := &productIssue{
			ID: stored.ID, ContactID: contactID, Goal: stored.Goal,
			Requirements: stored.Requirements, Status: stored.Status,
			Evidence: stored.Evidence, CreatedAt: stored.CreatedAt, UpdatedAt: stored.UpdatedAt,
			WorkSessionID: stored.WorkSessionID,
		}
		issue.WorkSessionID = stored.WorkSessionID
		if issue.Status == "" {
			issue.Status = issueStatusOpen
		}
		for _, execution := range stored.Executions {
			if execution.ID == "" {
				continue
			}
			status := execution.Status
			if status == "" {
				status = issueExecutionUnknown
			}
			detail := execution.Detail
			switch {
			case status == issueExecutionPending:
				status = issueExecutionUnknown
				detail = "No trusted Runtime task association was recorded; reload before rerunning."
			case execution.TaskID == "" && (status == issueExecutionAccepted || status == issueExecutionRunning):
				status = issueExecutionUnknown
				detail = "No trusted Runtime task association was recorded; reload before rerunning."
			}
			issue.Executions = append(issue.Executions, &issueExecution{
				ID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID,
				MessageID: execution.MessageID, Prompt: execution.Prompt,
				Status: status, Detail: detail,
			})
		}
		out = append(out, issue)
	}
	return out
}

// runIssue persists a prepared execution snapshot before any Runtime dispatch.
// The issue remains open regardless of the eventual runtime task state.
func (a *appModel) runIssue(issueID string) bool {
	ct := a.selectedContact()
	issue := ct.issueByID(issueID)
	if issue == nil {
		a.issueFormError = "Issue not found for this contact."
		return false
	}
	if reason := issueRunBlocked(issue); reason != "" {
		a.issueFormError = reason
		return false
	}
	if reason := a.canStartIssue(ct, issue); reason != "" {
		a.issueFormError = reason
		issue.Work.SendError = reason
		return false
	}
	prompt := issuePrompt(issue)
	if strings.TrimSpace(prompt) == "" {
		a.issueFormError = "This issue has no goal to run."
		return false
	}
	ct.SelectedIssueID = issue.ID
	ct.SelectedWorkTaskID = ""
	execution := &issueExecution{
		ID: newIssueExecutionID(), SessionID: issue.WorkSessionID, Prompt: prompt, Status: issueExecutionPending,
	}
	issue.Executions = append(issue.Executions, execution)
	issue.UpdatedAt = nowMillis()
	previousSend, previousPending := issue.Work.SendState, issue.Work.pendingExecutionID
	issue.Work.SendState = sendSubmitting
	issue.Work.SendError = ""
	issue.Work.pendingExecutionID = execution.ID
	a.issueFormError = ""
	if err := a.persist(); err != nil {
		issue.Executions = issue.Executions[:len(issue.Executions)-1]
		issue.Work.SendState, issue.Work.pendingExecutionID = previousSend, previousPending
		a.issueFormError = "Could not save the issue execution: " + err.Error()
		return false
	}
	if !a.startIssueTask(ct, issue, execution, prompt) {
		issue.Work.SendState = sendIdle
		issue.Work.pendingExecutionID = ""
		execution.Status = issueExecutionNotDispatched
		execution.Detail = issue.Work.SendError
		if execution.Detail == "" {
			execution.Detail = "The task was not dispatched."
		}
		a.issueFormError = execution.Detail
		_ = a.persist()
		return false
	}
	return true
}

func (a *appModel) selectIssue(issueID string) {
	ct := a.selectedContact()
	if ct.issueByID(issueID) == nil {
		return
	}
	ct.SelectedIssueID = issueID
	ct.SelectedWorkTaskID = ""
}

func (a *appModel) showIssueWork(issueID, taskID string) {
	ct := a.selectedContact()
	issue := ct.issueByID(issueID)
	if issue == nil || workFor(ct, taskID) == nil || !issueHasTask(issue, taskID) {
		return
	}
	ct.SelectedIssueID = issueID
	ct.SelectedWorkTaskID = taskID
	a.workspaceOpen = true
}

func (a *appModel) updatePendingIssueExecution(ct *contact, status, message string) {
	if ct.pendingIssueExecution == "" {
		return
	}
	_, execution := ct.executionByID(ct.pendingIssueExecution)
	ct.pendingIssueExecution = ""
	if execution == nil {
		return
	}
	execution.Status = status
	execution.Detail = message
}

func issueExecutionStatusFromWork(state workState) string {
	switch state {
	case workAccepted:
		return issueExecutionAccepted
	case workRunning:
		return issueExecutionRunning
	case workCompleted:
		return issueExecutionCompleted
	case workFailed:
		return issueExecutionFailed
	case workInterrupted:
		return issueExecutionInterrupted
	case workStopping, workUnknown:
		return issueExecutionUnknown
	default:
		return issueExecutionUnknown
	}
}

func issueRequirementsSummary(issue *productIssue) string {
	if strings.TrimSpace(issue.Requirements) == "" {
		return "No requirements recorded."
	}
	return issue.Requirements
}

func unresolvedExecution(execution *issueExecution) bool {
	switch execution.Status {
	case issueExecutionPending, issueExecutionAccepted, issueExecutionRunning, issueExecutionUnknown:
		return true
	default:
		return false
	}
}

func issueRunBlocked(issue *productIssue) string {
	for _, execution := range issue.Executions {
		if unresolvedExecution(execution) {
			return "An execution is still active or has no trusted terminal state; reload before running this issue again."
		}
	}
	return ""
}

// reconcileIssueExecution is the explicit recovery action for an execution
// whose local terminal state is unknown. It only trusts the Runtime task
// association (session_id + task_id); it never guesses a task from prompt text
// and never dispatches a replacement task.
func (a *appModel) reconcileIssueExecution(issueID, executionID string) bool {
	ct := a.selectedContact()
	issue := ct.issueByID(issueID)
	owner, execution := ct.executionByID(executionID)
	if issue == nil || execution == nil || owner != issue {
		return false
	}
	if execution.SessionID == "" || execution.TaskID == "" {
		execution.Detail = "No trusted task association is recorded; inspect the issue work history or confirm that this execution was not dispatched. No automatic retry."
		return false
	}
	sessionID, taskID := execution.SessionID, execution.TaskID
	go a.checkIssueExecution(a.rt, ct.ID, issue.ID, execution.ID, sessionID, taskID)
	return true
}

func (a *appModel) checkIssueExecution(rt *runtimeClient, contactID, issueID, executionID, sessionID, taskID string) {
	state, err := rt.taskStatus(sessionID, taskID)
	a.updateUI(func() {
		if a.rt != rt {
			return
		}
		ct := a.contactByID(contactID)
		if ct == nil {
			return
		}
		issue := ct.issueByID(issueID)
		if issue == nil {
			return
		}
		owner, execution := ct.executionByID(executionID)
		if owner != issue || execution == nil || execution.SessionID != sessionID || execution.TaskID != taskID {
			return
		}
		if err != nil {
			execution.Detail = "Could not verify this execution with Runtime: " + a.connectionRuntimeMessage(err) + ". No automatic retry."
			return
		}
		switch state.Status {
		case "completed", "failed", "interrupted":
			execution.Status = state.Status
			execution.Detail = ""
			if issue.Work.ActiveTaskID == taskID {
				issue.Work.ActiveTaskID = ""
				issue.Work.SendState = sendIdle
				issue.Work.SendError = ""
			}
			if w := workFor(ct, taskID); w != nil {
				w.State = workStateFromTask(state.Status)
				w.Detail = ""
			}
			_ = a.persist()
			a.reloadIssueWorkByID(contactID, issueID)
		case "accepted", "running":
			execution.Status = state.Status
			execution.Detail = ""
			if issue.Work.ActiveTaskID == "" || issue.Work.ActiveTaskID == taskID {
				issue.Work.ActiveTaskID = taskID
				issue.Work.LastTaskID = taskID
				issue.Work.SendState = sendAccepted
				issue.Work.SendError = ""
			}
			if w := a.ensureWork(ct, taskID, execution.MessageID); w != nil {
				if state.Status == "running" {
					w.State = workRunning
				} else {
					w.State = workAccepted
				}
			}
		default:
			execution.Status = issueExecutionUnknown
			execution.Detail = "Runtime reports no trusted terminal state for this task; inspect the work record or confirm not dispatched. No automatic retry."
		}
		_ = a.persist()
	})
}

// confirmIssueExecutionNotDispatched is the explicit user decision for a
// pending/unknown execution with no trusted task ID. It records "no task was
// dispatched" and never claims a result or retries the side effect.
func (a *appModel) confirmIssueExecutionNotDispatched(issueID, executionID string) bool {
	ct := a.selectedContact()
	issue := ct.issueByID(issueID)
	owner, execution := ct.executionByID(executionID)
	if issue == nil || execution == nil || owner != issue || execution.TaskID != "" {
		return false
	}
	if issue.Work.ActiveTaskID != "" || issue.Work.SendState == sendSubmitting {
		return false
	}
	switch execution.Status {
	case issueExecutionPending, issueExecutionUnknown:
	default:
		return false
	}
	execution.Status = issueExecutionNotDispatched
	execution.Detail = "User confirmed that no task was dispatched; the execution remains available for an explicit retry."
	issue.Work.SendState = sendIdle
	issue.Work.SendError = ""
	issue.Work.pendingExecutionID = ""
	_ = a.persist()
	return true
}

// issueWorkNotice is a model-derived, non-unread summary for issue work. It
// keeps work visible when the workspace is hidden and keeps attention states
// discoverable even when the work produced no chat message. It never changes
// contact.ActiveTaskID or conversation status.
type issueWorkNotice struct {
	Text      string
	Attention bool
	IssueID   string
}

func (a *appModel) issueWorkNotice(ct *contact) issueWorkNotice {
	attention := issueWorkNotice{}
	for _, issue := range ct.Issues {
		if issue.Work.ActiveTaskID != "" || issue.Work.SendState == sendSubmitting ||
			issue.Work.SendState == sendAccepted || issue.Work.SendState == sendUnknown {
			text := "Issue work active: " + issue.Goal
			attention := issue.Work.SendError != "" || issue.Work.SendState == sendUnknown
			if attention {
				text = "Issue work needs attention: " + issue.Goal
			}
			if issue.Work.SendError != "" {
				text += " — " + truncate(issue.Work.SendError, 180)
			}
			return issueWorkNotice{Text: text, Attention: attention, IssueID: issue.ID}
		}
		for index := len(issue.Executions) - 1; index >= 0; index-- {
			execution := issue.Executions[index]
			switch execution.Status {
			case issueExecutionFailed, issueExecutionUnknown, issueExecutionInterrupted:
				text := "Issue work needs attention: " + issue.Goal
				if execution.Detail != "" {
					text += " — " + truncate(execution.Detail, 180)
				}
				if attention.Text == "" {
					attention = issueWorkNotice{Text: text, Attention: true, IssueID: issue.ID}
				}
			}
		}
	}
	return attention
}

func (ct *contact) issueForTask(taskID string) (*productIssue, *issueExecution) {
	for _, issue := range ct.Issues {
		for _, execution := range issue.Executions {
			if execution.TaskID == taskID {
				return issue, execution
			}
		}
	}
	return nil, nil
}

func issueWorkActiveCount(ct *contact) int {
	count := 0
	for _, issue := range ct.Issues {
		if issue.Work.ActiveTaskID != "" || issue.Work.SendState == sendSubmitting || issue.Work.SendState == sendAccepted {
			count++
		}
	}
	return count
}
