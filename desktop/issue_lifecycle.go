package main

// Application-level product issue lifecycle actions. These operate on the same
// productIssue/issueExecution facts used by the workbench and future #154
// conversation actions. Execution routing and Runtime ownership stay in the
// existing runtime_model path.

import (
	"strings"
)

func issueForContact(ct *contact, issueID string) *productIssue {
	if ct == nil {
		return nil
	}
	return ct.issueByID(issueID)
}

func (a *appModel) issueForSelected(issueID string) *productIssue {
	return issueForContact(a.selectedContact(), issueID)
}

func issueLifecycleStartBlocked(issue *productIssue) string {
	if issue == nil {
		return "Issue not found."
	}
	if issue.Archived {
		return "Restore this archived issue before starting work."
	}
	if issue.openQuestion() != nil {
		return "Answer the waiting question before continuing this issue."
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
func (a *appModel) editIssueFor(ct *contact, issueID, goal, requirementsText string) bool {
	issue := issueForContact(ct, issueID)
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
	type requirementSnapshot struct {
		state, evidence string
		updated         int64
	}
	oldRequirementFields := map[string]requirementSnapshot{}
	for _, requirement := range issue.Requirements {
		if requirement != nil {
			oldRequirementFields[requirement.ID] = requirementSnapshot{requirement.State, requirement.Evidence, requirement.UpdatedAt}
		}
	}
	goalChanged := strings.TrimSpace(oldGoal) != goal
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
	if goalChanged {
		for _, requirement := range next {
			requirement.State = requirementUnverified
			if requirement.Evidence != "" && !strings.HasPrefix(requirement.Evidence, "Previous evidence (goal changed): ") {
				requirement.Evidence = "Previous evidence (goal changed): " + requirement.Evidence
			}
		}
	}
	issue.Goal = goal
	issue.Requirements = next
	issue.RequirementsRevision++
	issue.Evidence = ""
	issue.UpdatedAt = nowMillis()
	if err := a.persist(); err != nil {
		for _, requirement := range oldRequirements {
			if snapshot, ok := oldRequirementFields[requirement.ID]; ok {
				requirement.State, requirement.Evidence, requirement.UpdatedAt = snapshot.state, snapshot.evidence, snapshot.updated
			}
		}
		issue.Goal, issue.Requirements, issue.RequirementsRevision, issue.UpdatedAt, issue.Evidence = oldGoal, oldRequirements, oldRevision, oldUpdated, oldEvidence
		a.issueFormError = "Could not save this issue: " + err.Error()
		return false
	}
	a.issueFormError = ""
	return true
}

func (a *appModel) setIssueRequirementStateFor(ct *contact, issueID, requirementID, state, evidence string) bool {
	issue := issueForContact(ct, issueID)
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

func (a *appModel) markIssueWaitingFor(ct *contact, issueID string) bool {
	return a.markIssueWaitingForQuestion(ct, issueID, "", nil, nil, nil)
}

// markIssueWaitingForQuestion is the one waiting application save. It validates
// the active/unknown boundary before touching an existing question, then writes
// status, AutoContinue, the optional supersede/new question and its operation
// receipt in one persist. UI mark-waiting and tool wait share this path.
func (a *appModel) markIssueWaitingForQuestion(ct *contact, issueID, supersedeRef string, question *issueQuestion, operation *IssueToolOperation, receipt *IssueToolReceipt) bool {
	return a.markIssueWaitingForQuestionActive(ct, issueID, supersedeRef, question, operation, receipt, false)
}

func (a *appModel) markIssueWaitingForQuestionActive(ct *contact, issueID, supersedeRef string, question *issueQuestion, operation *IssueToolOperation, receipt *IssueToolReceipt, allowActive bool) bool {
	issue := issueForContact(ct, issueID)
	if issue == nil || issue.Archived {
		return false
	}
	if issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
		return false
	}
	if !allowActive && (issue.Work.ActiveTaskID != "" || issue.Work.SendState != sendIdle || issueRunBlocked(issue) != "") {
		return false
	}
	if allowActive && issue.Work.ActiveTaskID == "" && issue.Work.SendState != sendSubmitting {
		return false
	}
	ledger := issueSupplementLedgerFor(issue)
	previousQuestions := append([]*issueQuestion(nil), issue.Questions...)
	questionStates := make(map[*issueQuestion]issueQuestion, len(issue.Questions))
	for _, existing := range issue.Questions {
		if existing != nil {
			questionStates[existing] = *existing
		}
	}
	previousStatus, previousUpdated, previousAuto := issue.Status, issue.UpdatedAt, ledger.AutoContinue
	var previousReceipt *IssueToolReceipt
	if operation != nil {
		previousReceipt = operation.Receipt
	}
	if supersedeRef != "" {
		for _, existing := range issue.Questions {
			if existing != nil && existing.Ref == supersedeRef {
				existing.State = issueQuestionSuperseded
				break
			}
		}
	}
	if question != nil {
		issue.Questions = append(issue.Questions, question)
	}
	issue.Status, issue.UpdatedAt = issueStatusWaitingUser, nowMillis()
	ledger.AutoContinue = false
	if operation != nil {
		operation.Receipt = receipt
	}
	if err := a.persist(); err != nil {
		issue.Questions = previousQuestions
		for existing, snapshot := range questionStates {
			*existing = snapshot
		}
		issue.Status, issue.UpdatedAt, ledger.AutoContinue = previousStatus, previousUpdated, previousAuto
		if operation != nil {
			operation.Receipt = previousReceipt
		}
		return false
	}
	return true
}

func (a *appModel) resumeIssueFor(ct *contact, issueID string) bool {
	issue := issueForContact(ct, issueID)
	if issue == nil || issue.Archived {
		return false
	}
	if issue.openQuestion() != nil {
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
	if !a.setIssueStatus(issue, next) {
		return false
	}
	if ledger := issue.Supplements; ledger != nil && ledger.pendingCount() > 0 {
		if !a.setIssueSupplementAuto(ct, issue.ID, true) {
			return false
		}
	}
	return true
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

func (a *appModel) completeIssueFor(ct *contact, issueID, evidence string) bool {
	issue := issueForContact(ct, issueID)
	if issue == nil || issue.Archived || issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
		return false
	}
	if reason := issueRunBlocked(issue); reason != "" {
		return false
	}
	if issue.Work.ActiveTaskID != "" || issue.Work.SendState != sendIdle {
		return false
	}
	if !a.setIssueSupplementAuto(ct, issue.ID, false) {
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

func (a *appModel) reopenIssueFor(ct *contact, issueID string) bool {
	issue := issueForContact(ct, issueID)
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

func (a *appModel) editIssue(issueID, goal, requirementsText string) bool {
	return a.editIssueFor(a.selectedContact(), issueID, goal, requirementsText)
}

func (a *appModel) setIssueRequirementState(issueID, requirementID, state, evidence string) bool {
	return a.setIssueRequirementStateFor(a.selectedContact(), issueID, requirementID, state, evidence)
}

func (a *appModel) markIssueWaiting(issueID string) bool {
	return a.markIssueWaitingFor(a.selectedContact(), issueID)
}

func (a *appModel) resumeIssue(issueID string) bool {
	return a.resumeIssueFor(a.selectedContact(), issueID)
}

func (a *appModel) completeIssue(issueID, evidence string) bool {
	return a.completeIssueFor(a.selectedContact(), issueID, evidence)
}

func (a *appModel) reopenIssue(issueID string) bool {
	return a.reopenIssueFor(a.selectedContact(), issueID)
}

func (a *appModel) archiveIssue(issueID string) bool {
	return a.archiveIssueFor(a.selectedContact(), issueID)
}

func (a *appModel) restoreIssue(issueID string) bool {
	return a.restoreIssueFor(a.selectedContact(), issueID)
}

// cancelIssueProduct is separate from cancelIssue (stop execution): it records
// the product-level canceled state, then asks the existing execution path to
// stop any real active task. The task terminal remains whatever Runtime reports.
func (a *appModel) cancelIssueProduct(issueID string) bool {
	return a.cancelIssueProductFor(a.selectedContact(), issueID)
}

func (a *appModel) cancelIssueProductFor(ct *contact, issueID string) bool {
	if ct == nil {
		return false
	}
	issue := ct.issueByID(issueID)
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
	ledger := issueSupplementLedgerFor(issue)
	target := issueSupplementTargetFor(ct, issue)
	var active *issueSupplementBatch
	if ledger != nil {
		active = ledger.activeBatch(target)
	}
	if ledger == nil || !ledger.StopAutoContinue(active, a.persist) {
		issue.Status, issue.UpdatedAt, issue.Work.cancelAfterAccept = oldStatus, oldUpdated, oldCancelAfter
		return false
	}
	if activeTask {
		if !a.cancelIssueFor(ct, issue.ID) {
			issue.Status, issue.UpdatedAt, issue.Work.cancelAfterAccept = oldStatus, oldUpdated, oldCancelAfter
			_ = a.persist()
			return false
		}
	}
	return true
}

func (a *appModel) archiveIssueFor(ct *contact, issueID string) bool {
	issue := issueForContact(ct, issueID)
	if issue == nil || issue.Archived {
		return false
	}
	if !a.setIssueSupplementAuto(ct, issue.ID, false) {
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

func (a *appModel) restoreIssueFor(ct *contact, issueID string) bool {
	issue := issueForContact(ct, issueID)
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
	if issue == nil || issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
		return
	}
	switch status {
	case "completed":
		issue.Status = issueStatusInProgress
	case "failed", "unknown":
		issue.Status = issueStatusNeedsAttention
		if issue.Supplements != nil {
			issue.Supplements.AutoContinue = false
		}
	case "interrupted":
		if issue.Work.StopRequestedTasks[taskID] {
			issue.Status = issueStatusInProgress
		} else if execution := executionForTask(issue, taskID); execution != nil && execution.WaitRequested {
			if issue.openQuestion() != nil {
				issue.Status = issueStatusWaitingUser
				if issue.Supplements != nil {
					issue.Supplements.AutoContinue = false
				}
			} else {
				issue.Status = issueStatusInProgress
			}
		} else {
			issue.Status = issueStatusNeedsAttention
			if issue.Supplements != nil {
				issue.Supplements.AutoContinue = false
			}
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
	filter = normalizeIssueFilter(filter)
	out := make([]*productIssue, 0)
	for _, issue := range ct.Issues {
		if issue == nil {
			continue
		}
		match := false
		switch filter {
		case issueFilterAll:
			match = true
		case issueFilterCompleted:
			match = !issue.Archived && issue.Status == issueStatusCompleted
		default: // unfinished includes waiting/attention
			match = !issue.Archived && issue.Status != issueStatusCompleted && issue.Status != issueStatusCanceled
		}
		if match {
			out = append(out, issue)
		}
	}
	return out
}
