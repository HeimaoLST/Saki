package main

// Application wiring for product-issue supplements. It deliberately uses the
// existing issue execution, Runtime send, history, context and UI state; there
// is no second dispatcher, queue, or history parser.

import (
	"errors"
	"strings"
)

const issueSupplementInputPrefix = "\n\nUser supplement:\n"

func issueSupplementLedgerFor(issue *productIssue) *issueSupplementLedger {
	if issue == nil {
		return nil
	}
	if issue.Supplements == nil {
		issue.Supplements = newIssueSupplementLedger()
	}
	return issue.Supplements
}

func issueSupplementTargetFor(ct *contact, issue *productIssue) issueSupplementTarget {
	if ct == nil || issue == nil {
		return issueSupplementTarget{}
	}
	return issueSupplementTarget{
		ContactID:            ct.ID,
		IssueID:              issue.ID,
		RequirementsRevision: issue.RequirementsRevision,
	}
}

func issueSupplementFullInput(issue *productIssue, ledger *issueSupplementLedger, target issueSupplementTarget) string {
	base := issuePrompt(issue)
	if ledger == nil {
		return base
	}
	text := strings.TrimSpace(ledger.pendingText(target))
	if text == "" {
		return base
	}
	return base + issueSupplementInputPrefix + text
}

func (a *appModel) addIssueSupplement(ct *contact, issueID, text string) error {
	issue := issueForContact(ct, issueID)
	if issue == nil {
		return errors.New("issue not found for this contact")
	}
	if issue.Archived || issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled {
		return errors.New("this issue cannot receive a supplement in its current state")
	}
	ledger := issueSupplementLedgerFor(issue)
	target := issueSupplementTargetFor(ct, issue)
	if _, err := ledger.Add(target, text, a.persist); err != nil {
		return err
	}
	a.issueActionError = ""
	// Receipt is durable. Only an issue with an existing execution is already
	// authorized to continue automatically; a never-run issue requires an
	// explicit Start/Run (which includes pending supplements) before dispatch.
	if len(issue.Executions) > 0 {
		a.maybeAutoContinueIssue(ct, issue)
	}
	return nil
}

func (a *appModel) setIssueSupplementAuto(ct *contact, issueID string, enabled bool) bool {
	issue := issueForContact(ct, issueID)
	if issue == nil {
		return false
	}
	ledger := issueSupplementLedgerFor(issue)
	if ledger.AutoContinue == enabled {
		return true
	}
	if enabled {
		if !ledger.ResumeAutoContinue(a.persist) {
			return false
		}
		a.issueActionError = ""
		a.maybeAutoContinueIssue(ct, issue)
		return true
	}
	// User Stop/Cancel intent: preserve all receipts and the current batch.
	var batch *issueSupplementBatch
	if active := ledger.activeBatch(issueSupplementTargetFor(ct, issue)); active != nil {
		batch = active
	}
	if !ledger.StopAutoContinue(batch, a.persist) {
		return false
	}
	a.issueActionError = ""
	return true
}

func (a *appModel) maybeAutoContinueAllIssues() {
	if a.supplementAutoPass {
		return
	}
	a.supplementAutoPass = true
	defer func() { a.supplementAutoPass = false }()
	for _, ct := range a.contacts {
		for _, issue := range ct.Issues {
			a.autoContinueIssueLocked(ct, issue)
		}
	}
}

func (a *appModel) maybeAutoContinueIssue(ct *contact, issue *productIssue) bool {
	if a.supplementAutoPass {
		return false
	}
	a.supplementAutoPass = true
	defer func() { a.supplementAutoPass = false }()
	return a.autoContinueIssueLocked(ct, issue)
}

func (a *appModel) autoContinueIssueLocked(ct *contact, issue *productIssue) bool {
	if a == nil || ct == nil || issue == nil || a.shutdownStarted {
		return false
	}
	ledger := issue.Supplements
	if ledger == nil || !ledger.AutoContinue || ledger.pendingCount() == 0 {
		return false
	}
	if len(issue.Executions) == 0 {
		return false
	}
	if issue.Archived || issue.Status != issueStatusNotStarted && issue.Status != issueStatusInProgress {
		return false
	}
	if reason := issueLifecycleStartBlocked(issue); reason != "" {
		return false
	}
	if reason := issueRunBlocked(issue); reason != "" {
		return false
	}
	target := issueSupplementTargetFor(ct, issue)
	if active := ledger.activeBatch(target); active != nil {
		// A local prepared batch never crossed the send boundary. It is safe to
		// release and rebuild at the current revision without revoking auto.
		if active.State != supplementBatchPrepared || active.ExecutionID != "" {
			return false
		}
		if !ledger.ReleasePrepared(active, a.persist) {
			issue.Work.SendError = "A locally prepared supplement batch could not be released for update."
			return false
		}
	}
	if reason := a.canStartIssue(ct, issue); reason != "" {
		return false
	}
	return a.issueRunCaptured(ct, issue)
}

// startIssueRun is the common preparation/send path for manual Run and an
// automatic supplement boundary. It snapshots the current issue requirement
// revision, all eligible pending supplements, and task context before the one
// save that records the execution and dispatching batch.
func (a *appModel) startIssueRun(ct *contact, issue *productIssue) bool {
	prompt := issuePrompt(issue)
	if strings.TrimSpace(prompt) == "" {
		a.issueFormError = "This issue has no goal to run."
		return false
	}
	ledger := issueSupplementLedgerFor(issue)
	target := issueSupplementTargetFor(ct, issue)
	if active := ledger.activeBatch(target); active != nil {
		if active.State != supplementBatchPrepared || active.ExecutionID != "" {
			a.issueFormError = "A supplement batch is already " + active.State + " for this issue."
			return false
		}
		if !ledger.ReleasePrepared(active, a.persist) {
			a.issueFormError = "Could not release the locally prepared supplement batch."
			return false
		}
	}
	pendingText := strings.TrimSpace(ledger.pendingText(target))
	if pendingText != "" {
		prompt += issueSupplementInputPrefix + pendingText
	}
	execution := &issueExecution{
		ID: newIssueExecutionID(), SessionID: issue.WorkSessionID, Prompt: prompt,
		Status: issueExecutionPending, RequirementsRevision: issue.RequirementsRevision,
		Context: issue.Context.snapshot(),
	}
	previousExecutions := issue.Executions
	previousSend, previousPending := issue.Work.SendState, issue.Work.pendingExecutionID
	previousStatus, previousUpdated := issue.Status, issue.UpdatedAt
	previousError := issue.Work.SendError
	issue.Executions = append(issue.Executions, execution)
	issue.Status = issueStatusInProgress
	issue.UpdatedAt = nowMillis()
	issue.Work.SendState = sendSubmitting
	issue.Work.SendError = ""
	issue.Work.pendingExecutionID = execution.ID

	var batch *issueSupplementBatch
	var err error
	if pendingText != "" {
		batch, err = ledger.PrepareDispatch(target, execution, a.persist)
	} else {
		err = a.persist()
	}
	if err != nil {
		issue.Executions = previousExecutions
		issue.Work.SendState, issue.Work.pendingExecutionID = previousSend, previousPending
		issue.Status, issue.UpdatedAt, issue.Work.SendError = previousStatus, previousUpdated, previousError
		a.issueFormError = "Could not save the issue execution: " + err.Error()
		return false
	}
	a.issueFormError = ""
	if !a.startIssueTask(ct, issue, execution, prompt) {
		detail := issue.Work.SendError
		if detail == "" {
			detail = "The task was not dispatched."
		}
		issue.Work.SendState = sendIdle
		issue.Work.pendingExecutionID = ""
		execution.Status = issueExecutionNotDispatched
		execution.Detail = detail
		if issue.Status != issueStatusCanceled && issue.Status != issueStatusCompleted {
			issue.Status = issueStatusNeedsAttention
		}
		ledger.AutoContinue = false
		if batch != nil {
			if !ledger.NotDispatched(batch, detail, a.persist) {
				_ = a.persist()
			}
		} else {
			_ = a.persist()
		}
		a.issueFormError = detail
		return false
	}
	return true
}

func issueSupplementBatchForExecution(issue *productIssue, executionID string) *issueSupplementBatch {
	if issue == nil || issue.Supplements == nil || executionID == "" {
		return nil
	}
	for _, batch := range issue.Supplements.Batches {
		if batch != nil && batch.ExecutionID == executionID {
			return batch
		}
	}
	return nil
}

func issueSupplementBatchForID(issue *productIssue, batchID string) *issueSupplementBatch {
	if issue == nil || issue.Supplements == nil || batchID == "" {
		return nil
	}
	return issue.Supplements.batchByID(batchID)
}

func (a *appModel) openIssueSupplementForm(issueID string) {
	a.issueSupplementFormID = issueID
}

func (a *appModel) closeIssueSupplementForm() {
	a.issueSupplementFormID = ""
}

func (a *appModel) issueSupplementDraft(issueID string) *string {
	if a.issueSupplementDrafts == nil {
		a.issueSupplementDrafts = map[string]*string{}
	}
	draft, ok := a.issueSupplementDrafts[issueID]
	if !ok {
		value := ""
		draft = &value
		a.issueSupplementDrafts[issueID] = draft
	}
	return draft
}

func (a *appModel) saveIssueSupplement(ct *contact, issueID, text string) bool {
	issue := issueForContact(ct, issueID)
	if issue == nil {
		a.issueActionError = "Issue not found."
		return false
	}
	if err := a.addIssueSupplement(ct, issueID, text); err != nil {
		a.issueActionError = err.Error()
		return false
	}
	a.issueActionError = ""
	return true
}

func (a *appModel) retryIssueSupplementBatch(ct *contact, issue *productIssue, batch *issueSupplementBatch) bool {
	if issue == nil || batch == nil || issue.Supplements == nil {
		return false
	}
	if !issue.Supplements.RetryUnaccepted(batch, a.persist) {
		a.issueActionError = "Could not save the retry intent; the old supplement receipt was left unchanged."
		return false
	}
	if !a.issueRunCaptured(ct, issue) {
		a.issueActionError = a.issueFormError
		return false
	}
	return true
}

// normalizeRestoredSupplementBatches prevents restart from silently treating
// an in-flight or unknown dispatch as a fresh automatic candidate. It does not
// resend. If an execution association is missing, the batch becomes unknown.
func (a *appModel) normalizeRestoredSupplementBatches() {
	changed := false
	for _, ct := range a.contacts {
		for _, issue := range ct.Issues {
			ledger := issue.Supplements
			if ledger == nil {
				continue
			}
			for _, batch := range ledger.Batches {
				if batch == nil {
					continue
				}
				switch batch.State {
				case supplementBatchDispatching, supplementBatchUnknown:
					if ledger.AutoContinue {
						ledger.AutoContinue = false
						changed = true
					}
					if batch.ExecutionID == "" && batch.State == supplementBatchDispatching {
						batch.State = supplementBatchUnknown
						for _, snapshot := range batch.Snapshot {
							if item := ledger.itemByID(snapshot.ID); item != nil && item.State == supplementStateDispatching {
								item.State = supplementStateUnknown
							}
						}
						changed = true
					}
				}
			}
		}
	}
	if changed {
		if err := a.persist(); err != nil {
			a.issueActionError = "Could not save restored supplement integrity state: " + err.Error()
		}
	}
}
