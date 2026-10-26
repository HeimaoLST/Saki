package main

// Application-level product issue lifecycle actions. These operate on the same
// productIssue/issueExecution facts used by the workbench and future #154
// conversation actions. Execution routing and Runtime ownership stay in the
// existing runtime_model path.

import (
	"strings"
)

func (a *appModel) issueForSelected(issueID string) *productIssue {
	ct := a.selectedContact()
	if ct == nil {
		return nil
	}
	return ct.issueByID(issueID)
}

func issueLifecycleStartBlocked(issue *productIssue) string {
	if issue == nil {
		return "Issue not found."
	}
	if issue.Archived {
		return "Restore this archived issue before starting work."
	}
	switch issue.Status {
	case issueStatusCompleted:
		return "Reopen this completed issue before starting another execution."
	case issueStatusCanceled:
		return "Reopen this canceled issue before starting another execution."
	}
	return ""
}

// editIssue updates goal and requirement text. Unchanged requirement text keeps
// its stable identity/state; new or changed text starts unverified. Old
// execution Prompt snapshots are never touched.
func (a *appModel) editIssue(issueID, goal, requirementsText string) bool {
	issue := a.issueForSelected(issueID)
	goal = strings.TrimSpace(goal)
	if issue == nil {
		a.issueFormError = "Issue not found for this contact."
		return false
	}
	if goal == "" {
		a.issueFormError = "Enter an issue goal."
		return false
	}
	if issue.Archived {
		a.issueFormError = "Restore this archived issue before editing it."
		return false
	}
	if issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
		a.issueFormError = "Reopen this issue before editing a completed or canceled item."
		return false
	}
	oldGoal, oldRequirements, oldRevision, oldUpdated, oldEvidence := issue.Goal, issue.Requirements, issue.RequirementsRevision, issue.UpdatedAt, issue.Evidence
	byText := map[string][]*issueRequirement{}
	for _, requirement := range issue.Requirements {
		if requirement != nil {
			byText[strings.TrimSpace(requirement.Text)] = append(byText[strings.TrimSpace(requirement.Text)], requirement)
		}
	}
	var next []*issueRequirement
	for _, text := range strings.Split(strings.ReplaceAll(requirementsText, "\r\n", "\n"), "\n") {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if old := byText[text]; len(old) > 0 {
			next = append(next, old[0])
			byText[text] = old[1:]
			continue
		}
		next = append(next, &issueRequirement{ID: newRequirementID(), Text: text, State: requirementUnverified})
	}
	issue.Goal = goal
	issue.Requirements = next
	issue.RequirementsRevision++
	issue.Evidence = ""
	issue.UpdatedAt = nowMillis()
	if err := a.persist(); err != nil {
		issue.Goal, issue.Requirements, issue.RequirementsRevision, issue.UpdatedAt, issue.Evidence = oldGoal, oldRequirements, oldRevision, oldUpdated, oldEvidence
		a.issueFormError = "Could not save this issue: " + err.Error()
		return false
	}
	a.issueFormError = ""
	return true
}

func (a *appModel) setIssueRequirementState(issueID, requirementID, state, evidence string) bool {
	issue := a.issueForSelected(issueID)
	requirement := issueRequirementByID(issue, requirementID)
	if issue == nil || requirement == nil {
		return false
	}
	if issue.Archived || issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
		return false
	}
	if state != requirementUnverified && state != requirementSatisfied && state != requirementUnmet {
		return false
	}
	oldState, oldEvidence, oldUpdated := requirement.State, requirement.Evidence, requirement.UpdatedAt
	oldIssueUpdated := issue.UpdatedAt
	requirement.State = state
	requirement.Evidence = strings.TrimSpace(evidence)
	if state != requirementUnverified && requirement.Evidence == "" {
		requirement.Evidence = "User marked " + strings.ReplaceAll(state, "_", " ") + "."
	}
	requirement.UpdatedAt = nowMillis()
	issue.UpdatedAt = requirement.UpdatedAt
	if err := a.persist(); err != nil {
		requirement.State, requirement.Evidence, requirement.UpdatedAt = oldState, oldEvidence, oldUpdated
		issue.UpdatedAt = oldIssueUpdated
		return false
	}
	return true
}

func (a *appModel) markIssueWaiting(issueID string) bool {
	issue := a.issueForSelected(issueID)
	if issue == nil || issue.Archived {
		return false
	}
	if issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
		return false
	}
	if issue.Work.ActiveTaskID != "" || issue.Work.SendState != sendIdle || issueRunBlocked(issue) != "" {
		return false
	}
	return a.setIssueStatus(issue, issueStatusWaitingUser)
}

func (a *appModel) resumeIssue(issueID string) bool {
	issue := a.issueForSelected(issueID)
	if issue == nil || issue.Archived {
		return false
	}
	if issue.Status != issueStatusWaitingUser && issue.Status != issueStatusNeedsAttention {
		return false
	}
	if issue.Work.ActiveTaskID != "" || issue.Work.SendState != sendIdle || issueRunBlocked(issue) != "" {
		return false
	}
	next := issueStatusInProgress
	if len(issue.Executions) == 0 {
		next = issueStatusNotStarted
	}
	return a.setIssueStatus(issue, next)
}

func (a *appModel) setIssueStatus(issue *productIssue, status string) bool {
	if issue == nil || issue.Archived {
		return false
	}
	oldStatus, oldUpdated := issue.Status, issue.UpdatedAt
	issue.Status = status
	issue.UpdatedAt = nowMillis()
	if err := a.persist(); err != nil {
		issue.Status, issue.UpdatedAt = oldStatus, oldUpdated
		return false
	}
	return true
}

func (a *appModel) completeIssue(issueID, evidence string) bool {
	issue := a.issueForSelected(issueID)
	if issue == nil || issue.Archived || issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
		return false
	}
	if reason := issueRunBlocked(issue); reason != "" {
		return false
	}
	if issue.Work.ActiveTaskID != "" || issue.Work.SendState != sendIdle {
		return false
	}
	oldStatus, oldEvidence, oldUpdated := issue.Status, issue.Evidence, issue.UpdatedAt
	issue.Status = issueStatusCompleted
	issue.Evidence = strings.TrimSpace(evidence)
	if issue.Evidence == "" {
		issue.Evidence = "User marked this issue complete."
	}
	issue.UpdatedAt = nowMillis()
	if err := a.persist(); err != nil {
		issue.Status, issue.Evidence, issue.UpdatedAt = oldStatus, oldEvidence, oldUpdated
		return false
	}
	return true
}

func (a *appModel) reopenIssue(issueID string) bool {
	issue := a.issueForSelected(issueID)
	if issue == nil || issue.Archived {
		return false
	}
	if issue.Status != issueStatusCompleted && issue.Status != issueStatusCanceled {
		return false
	}
	if issue.Work.ActiveTaskID != "" || issue.Work.SendState != sendIdle || issueRunBlocked(issue) != "" {
		return false
	}
	next := issueStatusInProgress
	if len(issue.Executions) == 0 {
		next = issueStatusNotStarted
	}
	return a.setIssueStatus(issue, next)
}

// cancelIssueProduct is separate from cancelIssue (stop execution): it records
// the product-level canceled state, then asks the existing execution path to
// stop any real active task. The task terminal remains whatever Runtime reports.
func (a *appModel) cancelIssueProduct(issueID string) bool {
	issue := a.issueForSelected(issueID)
	if issue == nil || issue.Archived {
		return false
	}
	if issue.Status == issueStatusCanceled || issue.Status == issueStatusCompleted {
		return false
	}
	activeTask := issue.Work.ActiveTaskID != ""
	pendingAccept := !activeTask && (issue.Work.SendState == sendSubmitting || issue.Work.SendState == sendUnknown)
	if activeTask && a.rt == nil {
		return false
	}
	oldStatus, oldUpdated, oldCancelAfter := issue.Status, issue.UpdatedAt, issue.Work.cancelAfterAccept
	issue.Status = issueStatusCanceled
	issue.Work.cancelAfterAccept = pendingAccept
	issue.UpdatedAt = nowMillis()
	if err := a.persist(); err != nil {
		issue.Status, issue.UpdatedAt, issue.Work.cancelAfterAccept = oldStatus, oldUpdated, oldCancelAfter
		return false
	}
	if activeTask {
		if !a.cancelIssue(issue.ID) {
			issue.Status, issue.UpdatedAt, issue.Work.cancelAfterAccept = oldStatus, oldUpdated, oldCancelAfter
			_ = a.persist()
			return false
		}
	}
	return true
}

func (a *appModel) archiveIssue(issueID string) bool {
	issue := a.issueForSelected(issueID)
	if issue == nil || issue.Archived {
		return false
	}
	old, oldUpdated := issue.Archived, issue.UpdatedAt
	issue.Archived = true
	issue.UpdatedAt = nowMillis()
	if err := a.persist(); err != nil {
		issue.Archived, issue.UpdatedAt = old, oldUpdated
		return false
	}
	return true
}

func (a *appModel) restoreIssue(issueID string) bool {
	issue := a.issueForSelected(issueID)
	if issue == nil || !issue.Archived {
		return false
	}
	old, oldUpdated := issue.Archived, issue.UpdatedAt
	issue.Archived = false
	issue.UpdatedAt = nowMillis()
	if err := a.persist(); err != nil {
		issue.Archived, issue.UpdatedAt = old, oldUpdated
		return false
	}
	return true
}

// applyIssueTerminalLifecycle maps an execution terminal to a visible product
// status without ever making a completed execution equal to issue completion.
func (a *appModel) applyIssueTerminalLifecycle(issue *productIssue, taskID, status string) {
	if issue == nil || issue.Archived || issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
		return
	}
	switch status {
	case "completed":
		issue.Status = issueStatusInProgress
	case "failed", "unknown":
		issue.Status = issueStatusNeedsAttention
	case "interrupted":
		if issue.Work.StopRequestedTasks[taskID] {
			issue.Status = issueStatusInProgress
		} else {
			issue.Status = issueStatusNeedsAttention
		}
	}
	issue.UpdatedAt = nowMillis()
}

func (a *appModel) openNewIssueForm() {
	a.issueFormOpen = true
	a.issueFormEditingID = ""
	a.issueGoal = ""
	a.issueRequirements = ""
	a.issueFormError = ""
	a.focusIssueGoal = true
}

func (a *appModel) openEditIssueForm(issue *productIssue) {
	if issue == nil || issue.Archived || issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
		return
	}
	a.issueFormOpen = true
	a.issueFormEditingID = issue.ID
	a.issueGoal = issue.Goal
	a.issueRequirements = issueRequirementsText(issue)
	a.issueFormError = ""
	a.focusIssueGoal = true
}

func (a *appModel) saveIssueForm() {
	if a.issueFormEditingID != "" {
		if a.editIssue(a.issueFormEditingID, a.issueGoal, a.issueRequirements) {
			a.issueFormOpen = false
			a.issueFormEditingID = ""
			a.issueFormError = ""
			a.focusIssueNew = true
		}
		return
	}
	if a.createIssue(a.issueGoal, a.issueRequirements) {
		a.issueFormEditingID = ""
	}
}

func (a *appModel) cancelIssueForm() {
	a.issueFormOpen = false
	a.issueFormEditingID = ""
	a.issueFormError = ""
	a.focusIssueNew = true
}

func (a *appModel) requirementDraft(requirement *issueRequirement) *string {
	if a.issueRequirementDrafts == nil {
		a.issueRequirementDrafts = map[string]*string{}
	}
	draft, ok := a.issueRequirementDrafts[requirement.ID]
	if !ok {
		value := requirement.Evidence
		draft = &value
		a.issueRequirementDrafts[requirement.ID] = draft
	}
	return draft
}

func (a *appModel) completionDraft(issue *productIssue) *string {
	if a.issueCompletionDrafts == nil {
		a.issueCompletionDrafts = map[string]*string{}
	}
	draft, ok := a.issueCompletionDrafts[issue.ID]
	if !ok {
		value := issue.Evidence
		draft = &value
		a.issueCompletionDrafts[issue.ID] = draft
	}
	return draft
}

func (ct *contact) issuesForFilter(filter string) []*productIssue {
	if filter == "" {
		filter = "active"
	}
	out := make([]*productIssue, 0)
	for _, issue := range ct.Issues {
		match := false
		switch filter {
		case "archived":
			match = issue.Archived
		case "completed":
			match = !issue.Archived && issue.Status == issueStatusCompleted
		case "canceled":
			match = !issue.Archived && issue.Status == issueStatusCanceled
		case "waiting":
			match = !issue.Archived && issue.Status == issueStatusWaitingUser
		case "attention":
			match = !issue.Archived && issue.Status == issueStatusNeedsAttention
		default: // active
			match = !issue.Archived && issue.Status != issueStatusCompleted && issue.Status != issueStatusCanceled &&
				issue.Status != issueStatusWaitingUser && issue.Status != issueStatusNeedsAttention
		}
		if match {
			out = append(out, issue)
		}
	}
	return out
}

func issueFilterLabel(filter string) string {
	switch filter {
	case "waiting":
		return "Waiting for you"
	case "attention":
		return "Needs attention"
	case "completed":
		return "Completed"
	case "canceled":
		return "Canceled"
	case "archived":
		return "Archived"
	default:
		return "Active"
	}
}
