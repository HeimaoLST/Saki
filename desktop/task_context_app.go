package main

// App wiring for the task-context component: binding the single persistence
// callback, restoring owned snapshot lists, the bounded editor panel, and
// read-only material rendering for Runtime history.

import "github.com/egoist/mygo/ui"

const taskContextUncertainSendError = "A previous message may have been sent; delivery was not confirmed before restart. Reload history to verify. No automatic retry."

func clonePendingSend(send *pendingSend) *pendingSend {
	if send == nil {
		return nil
	}
	return &pendingSend{Draft: send.Draft, Context: cloneTaskContextItems(send.Context), State: send.State}
}

func (a *appModel) loadStoredTaskContexts(store *contactStore) {
	if store == nil {
		return
	}
	byID := make(map[string]*contact, len(a.contacts))
	for _, ct := range a.contacts {
		byID[ct.ID] = ct
	}
	for _, stored := range store.state.Contacts {
		ct := byID[stored.ID]
		if ct == nil {
			continue
		}
		ct.Context.items = cloneTaskContextItems(stored.TaskContext)
		ct.pendingSend = clonePendingSend(stored.PendingSend)
		if ct.pendingSend != nil && (ct.pendingSend.State == "submitting" || ct.pendingSend.State == "unknown") {
			if ct.SessionID == "" {
				// The GUI saves the runtime session mapping before task.send, so
				// a pending send without a session was never dispatched.
				ct.pendingSend = nil
			} else {
				// A restored in-flight intent has the same uncertainty as a
				// persisted unknown one; normalize it so automatic reload does
				// not silently retire it.
				ct.pendingSend.State = "unknown"
				ct.SendState = sendUnknown
				ct.Status = statusAttention
				ct.SendError = taskContextUncertainSendError
				ct.StatusDetail = ct.SendError
			}
		}
	}
}

func (a *appModel) bindTaskContexts() {
	for _, ct := range a.contacts {
		ct.Context.onChange = a.persist
		for _, issue := range ct.Issues {
			issue.Context.onChange = a.persist
		}
	}
}

func (a *appModel) openConversationTaskContext() {
	a.taskContextIssueID = ""
	a.aux = "task-context"
}

func (a *appModel) openIssueTaskContext(issueID string) {
	if a.selectedContact().issueByID(issueID) == nil {
		return
	}
	a.taskContextIssueID = issueID
	a.aux = "task-context"
}

func (a *appModel) taskContextPanel(c *ui.Context, t *ui.Theme) {
	ct := a.selectedContact()
	owner := "Conversation · " + ct.Name
	target := &ct.Context
	if a.taskContextIssueID != "" {
		issue := ct.issueByID(a.taskContextIssueID)
		if issue == nil {
			ui.Text(c, "This issue is no longer available.").FontSize(12).TextColor(t.Danger)
			ui.Button(c, "Close task context").FontSize(12).OnClick(func() { a.closeAux() })
			return
		}
		owner = "Issue · " + issue.Goal
		target = &issue.Context
	}
	ui.Row(c).AlignItems(ui.Center).Children(func() {
		ui.Text(c, owner).FontSize(16).FontWeight(600).TextColor(t.Text).MaxLines(2)
		ui.Spacer(c)
		ui.Button(c, "Close task context").FontSize(12).OnClick(func() { a.closeAux() })
	})
	target.view(c, a.workTheme)
}

func (a *appModel) executionTaskContextView(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue, execution *issueExecution) {
	taskContextReadOnlyView(c, a.workTheme, executionTaskContextItems(ct, issue, execution))
}

func executionTaskContextItems(ct *contact, issue *productIssue, execution *issueExecution) []taskContextItem {
	items := cloneTaskContextItems(execution.Context)
	if len(items) > 0 {
		return items
	}
	if execution.SessionID == "" {
		return nil
	}
	switch execution.SessionID {
	case issue.WorkSessionID:
		return taskContextMessageItems(issue.Work.Messages, execution)
	case ct.SessionID:
		return taskContextMessageItems(ct.Messages, execution)
	default:
		return nil
	}
}

func taskContextMessageItems(messages []*message, execution *issueExecution) []taskContextItem {
	for index := len(messages) - 1; index >= 0; index-- {
		m := messages[index]
		if len(m.Context) == 0 {
			continue
		}
		if execution.TaskID != "" && m.TaskID == execution.TaskID {
			return m.Context
		}
		if execution.TaskID == "" && execution.MessageID != "" && m.MessageID == execution.MessageID {
			return m.Context
		}
	}
	return nil
}
