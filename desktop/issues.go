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

	issueExecutionPending     = "pending"
	issueExecutionAccepted    = "accepted"
	issueExecutionRunning     = "running"
	issueExecutionCompleted   = "completed"
	issueExecutionFailed      = "failed"
	issueExecutionInterrupted = "interrupted"
	issueExecutionUnknown     = "unknown"
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
	ct.Issues = append(ct.Issues, item)
	ct.SelectedIssueID = item.ID
	a.issueFormOpen = false
	a.issueFormError = ""
	a.issueGoal = ""
	a.issueRequirements = ""
	if err := a.persist(); err != nil {
		ct.Issues = ct.Issues[:len(ct.Issues)-1]
		if len(ct.Issues) > 0 {
			ct.SelectedIssueID = ct.Issues[len(ct.Issues)-1].ID
		} else {
			ct.SelectedIssueID = ""
		}
		a.issueFormError = "Could not save this issue: " + err.Error()
		return false
	}
	return true
}

// issueEvidenceText is a factual summary only. It does not turn a completed
// Runtime task into a completed product item or infer deliverable files.
func issueEvidenceText(issue *productIssue) string {
	if strings.TrimSpace(issue.Evidence) != "" {
		return issue.Evidence
	}
	return "Completion evidence is not recorded; a completed Runtime task is not item completion."
}

func issueExecutionLabel(execution *issueExecution) string {
	switch execution.Status {
	case issueExecutionPending:
		return "Prepared, not dispatched"
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
		if stored.ContactID != "" && stored.ContactID != contactID {
			continue
		}
		if stored.ID == "" {
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
			issue.Executions = append(issue.Executions, &issueExecution{
				ID: execution.ID, SessionID: execution.SessionID, TaskID: execution.TaskID,
				MessageID: execution.MessageID, Prompt: execution.Prompt,
				Status: status, Detail: execution.Detail,
			})
		}
		out = append(out, issue)
	}
	return out
}
