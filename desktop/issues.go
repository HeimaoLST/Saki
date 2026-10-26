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
	ID                   string
	ContactID            string
	Goal                 string
	Requirements         []*issueRequirement
	Status               string
	Archived             bool
	Evidence             string
	CreatedAt            int64
	UpdatedAt            int64
	Executions           []*issueExecution
	RequirementsRevision int

	// Context is the pending context for the next Run/Continue. Each execution
	// stores its own immutable Context snapshot before dispatch.
	Context taskContext

	// Supplements owns user supplements and their dispatch receipts for this
	// issue. It is persisted with the issue; it is not a Runtime history copy.
	Supplements *issueSupplementLedger

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
	cancelAfterAccept  bool
	historyGen         int
	HistoryDirty       bool
}

// issueRequirement is the single live truth for a product requirement. The
// legacy Requirements string exists only at the store serialization boundary.
type issueRequirement struct {
	ID        string
	Text      string
	State     string
	Evidence  string
	UpdatedAt int64
}

// issueExecution is the persisted association for one attempt on an issue.
// Prompt is the exact pre-dispatch snapshot; Runtime history remains the
// authority for task/message/tool results.
type issueExecution struct {
	ID                   string
	SessionID            string
	TaskID               string
	MessageID            string
	Prompt               string
	Status               string
	Detail               string
	RequirementsRevision int
	Context              []taskContextItem
	ContextExpanded      bool
	SupplementBatchID    string
}

const (
	issueStatusNotStarted     = "not_started"
	issueStatusInProgress     = "in_progress"
	issueStatusWaitingUser    = "waiting_user"
	issueStatusNeedsAttention = "needs_attention"
	issueStatusCompleted      = "completed"
	issueStatusCanceled       = "canceled"

	// Legacy name kept for old tests/data; new code uses NotStarted.
	issueStatusOpen = issueStatusNotStarted

	requirementUnverified = "unverified"
	requirementSatisfied  = "satisfied"
	requirementUnmet      = "unmet"

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

func newRequirementID() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "requirement-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("requirement-%d", time.Now().UnixNano())
}

func parseIssueRequirements(text string) []*issueRequirement {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out []*issueRequirement
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, &issueRequirement{ID: newRequirementID(), Text: line, State: requirementUnverified})
	}
	return out
}

func issueRequirementsText(issue *productIssue) string {
	if issue == nil {
		return ""
	}
	parts := make([]string, 0, len(issue.Requirements))
	for _, requirement := range issue.Requirements {
		if requirement != nil && strings.TrimSpace(requirement.Text) != "" {
			parts = append(parts, requirement.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func issueRequirementByID(issue *productIssue, id string) *issueRequirement {
	if issue == nil {
		return nil
	}
	for _, requirement := range issue.Requirements {
		if requirement != nil && requirement.ID == id {
			return requirement
		}
	}
	return nil
}

func normalizeIssueStatus(status string, executions []*issueExecution) string {
	switch status {
	case issueStatusNotStarted, issueStatusInProgress, issueStatusWaitingUser,
		issueStatusNeedsAttention, issueStatusCompleted, issueStatusCanceled:
		return status
	case "", "open":
		if len(executions) > 0 {
			return issueStatusInProgress
		}
		return issueStatusNotStarted
	default:
		return issueStatusNeedsAttention
	}
}

func issueStatusLabel(issue *productIssue) string {
	if issue == nil {
		return "Unknown"
	}
	if issue.Archived {
		return "Archived"
	}
	switch issue.Status {
	case issueStatusNotStarted:
		return "Not started"
	case issueStatusInProgress:
		return "In progress"
	case issueStatusWaitingUser:
		return "Waiting for you"
	case issueStatusNeedsAttention:
		return "Needs attention"
	case issueStatusCompleted:
		return "Completed"
	case issueStatusCanceled:
		return "Canceled"
	default:
		return "Needs attention"
	}
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
		if !issue.Archived && issue.Status != issueStatusCompleted && issue.Status != issueStatusCanceled {
			return issue
		}
	}
	if len(ct.Issues) > 0 {
		return ct.Issues[0]
	}
	return nil
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
	requirements := strings.TrimSpace(issueRequirementsText(issue))
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
		ID: newIssueID(), ContactID: ct.ID, Goal: goal,
		Requirements: parseIssueRequirements(requirements), RequirementsRevision: 1,
		Status: issueStatusNotStarted, CreatedAt: now, UpdatedAt: now,
	}
	item.Context.onChange = a.persist
	if item.Supplements == nil {
		item.Supplements = newIssueSupplementLedger()
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
			Archived: stored.Archived, RequirementsRevision: stored.RequirementsRevision,
			Evidence: stored.Evidence, CreatedAt: stored.CreatedAt, UpdatedAt: stored.UpdatedAt,
			WorkSessionID: stored.WorkSessionID,
			Context:       taskContext{items: cloneTaskContextItems(stored.TaskContext)},
			Supplements:   cloneIssueSupplementLedger(stored.Supplements),
		}
		if len(stored.RequirementItems) > 0 {
			for _, item := range stored.RequirementItems {
				if strings.TrimSpace(item.Text) == "" {
					continue
				}
				state := item.State
				if state != requirementSatisfied && state != requirementUnmet {
					state = requirementUnverified
				}
				issue.Requirements = append(issue.Requirements, &issueRequirement{
					ID: item.ID, Text: item.Text, State: state, Evidence: item.Evidence, UpdatedAt: item.UpdatedAt,
				})
			}
		} else {
			issue.Requirements = parseIssueRequirements(stored.Requirements)
		}
		if issue.RequirementsRevision < 1 {
			issue.RequirementsRevision = 1
		}
		issue.WorkSessionID = stored.WorkSessionID
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
				Status: status, Detail: detail, RequirementsRevision: execution.RequirementsRevision,
				Context: cloneTaskContextItems(execution.Context), SupplementBatchID: execution.SupplementBatchID,
			})
		}
		issue.Status = normalizeIssueStatus(stored.Status, issue.Executions)
		out = append(out, issue)
	}
	return out
}

// runIssue persists a prepared execution snapshot before any Runtime dispatch.
// The issue remains open regardless of the eventual runtime task state.
func (a *appModel) runIssue(issueID string) bool {
	return a.runIssueFor(a.selectedContact(), issueID)
}

func (a *appModel) runIssueFor(ct *contact, issueID string) bool {
	issue := issueForContact(ct, issueID)
	if issue == nil {
		if ct == nil {
			a.issueFormError = "Contact not found."
		} else {
			a.issueFormError = "Issue not found for this contact."
		}
		return false
	}
	if !a.issueRunCaptured(ct, issue) {
		return false
	}
	ct.SelectedIssueID = issue.ID
	ct.SelectedWorkTaskID = ""
	return true
}

// issueRunCaptured validates and dispatches using captured objects only. The
// automatic continuation path uses this directly so it never depends on, or
// steals, the UI's selected issue.
func (a *appModel) issueRunCaptured(ct *contact, issue *productIssue) bool {
	if ct == nil || issue == nil {
		a.issueFormError = "Issue not found for this contact."
		return false
	}
	if reason := issueLifecycleStartBlocked(issue); reason != "" {
		a.issueFormError = reason
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
	if strings.TrimSpace(issuePrompt(issue)) == "" {
		a.issueFormError = "This issue has no goal to run."
		return false
	}
	return a.startIssueRun(ct, issue)
}

func (a *appModel) selectIssue(issueID string) {
	ct := a.selectedContact()
	if ct.issueByID(issueID) == nil {
		return
	}
	ct.SelectedIssueID = issueID
	ct.SelectedWorkTaskID = ""
	a.issueActionError = ""
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
			a.maybeAutoContinueAllIssues()
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
			_ = a.persist()
		default:
			execution.Status = issueExecutionUnknown
			execution.Detail = "Runtime reports no trusted terminal state for this task; inspect the work record or confirm not dispatched. No automatic retry."
			_ = a.persist()
		}
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
	oldExecutionStatus, oldExecutionDetail := execution.Status, execution.Detail
	oldWorkState, oldWorkError, oldWorkPending := issue.Work.SendState, issue.Work.SendError, issue.Work.pendingExecutionID
	oldAuto := false
	if issue.Supplements != nil {
		oldAuto = issue.Supplements.AutoContinue
		issue.Supplements.AutoContinue = false
	}
	execution.Status = issueExecutionNotDispatched
	execution.Detail = "User confirmed that no task was dispatched; the execution remains available for an explicit retry."
	issue.Work.SendState = sendIdle
	issue.Work.SendError = ""
	issue.Work.pendingExecutionID = ""
	batch := issueSupplementBatchForExecution(issue, execution.ID)
	saved := true
	if batch != nil {
		if issue.Supplements == nil || !issue.Supplements.NotDispatched(batch, execution.Detail, a.persist) {
			saved = false
		}
	} else if err := a.persist(); err != nil {
		saved = false
	}
	if !saved {
		execution.Status, execution.Detail = oldExecutionStatus, oldExecutionDetail
		issue.Work.SendState, issue.Work.SendError, issue.Work.pendingExecutionID = oldWorkState, oldWorkError, oldWorkPending
		if issue.Supplements != nil {
			issue.Supplements.AutoContinue = oldAuto
		}
		return false
	}
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
