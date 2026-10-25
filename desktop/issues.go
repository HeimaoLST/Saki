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
		}
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
	if reason := a.canSend(ct); reason != "" {
		a.issueFormError = reason
		ct.SendError = reason
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
		ID: newIssueExecutionID(), SessionID: ct.SessionID, Prompt: prompt, Status: issueExecutionPending,
	}
	issue.Executions = append(issue.Executions, execution)
	issue.UpdatedAt = nowMillis()
	ct.pendingIssueExecution = execution.ID
	a.issueFormError = ""
	if err := a.persist(); err != nil {
		issue.Executions = issue.Executions[:len(issue.Executions)-1]
		ct.pendingIssueExecution = ""
		a.issueFormError = "Could not save the issue execution: " + err.Error()
		return false
	}
	if !a.sendRuntimeForIssue(ct, prompt, execution.ID) {
		ct.pendingIssueExecution = ""
		execution.Status = issueExecutionNotDispatched
		execution.Detail = ct.SendError
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
