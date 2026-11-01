package main

import "github.com/egoist/mygo/ui"

const (
	issueFilterUnfinished = "unfinished"
	issueFilterCompleted  = "completed"
	issueFilterAll        = "all"
)

// issueViewState is short-lived per-contact display state. It never replaces
// the persistent contact.SelectedIssueID or any runtime/issue lifecycle fact.
type issueViewState struct {
	Filter               string
	DetailID             string
	FocusRowID           string
	FocusBack            bool
	FocusConversation    bool
	ShowConversationWork bool
	OverviewScroll       ui.ScrollState
	DetailScroll         ui.ScrollState
}

func (ct *contact) issueNavigation() *issueViewState {
	if ct == nil {
		return nil
	}
	if ct.issueView.Filter == "" {
		ct.issueView.Filter = issueFilterUnfinished
	}
	return &ct.issueView
}

func normalizeIssueFilter(filter string) string {
	switch filter {
	case issueFilterCompleted, issueFilterAll:
		return filter
	default:
		return issueFilterUnfinished
	}
}

func issueFilterChoiceLabel(filter string) string {
	switch normalizeIssueFilter(filter) {
	case issueFilterCompleted:
		return "Completed"
	case issueFilterAll:
		return "All"
	default:
		return "Unfinished"
	}
}

func issueFilterFromChoice(label string) string {
	switch label {
	case "Completed":
		return issueFilterCompleted
	case "All":
		return issueFilterAll
	default:
		return issueFilterUnfinished
	}
}

func issueFilterDescription(filter string) string {
	switch normalizeIssueFilter(filter) {
	case issueFilterCompleted:
		return "Completed"
	case issueFilterAll:
		return "All issues"
	default:
		return "Unfinished"
	}
}

func (a *appModel) setIssueFilter(ct *contact, filter string) {
	nav := ct.issueNavigation()
	if nav == nil {
		return
	}
	nav.Filter = normalizeIssueFilter(filter)
	nav.OverviewScroll.Y = 0
	nav.FocusRowID = ""
}

func (a *appModel) escapeWorkspaceLayer() bool {
	ct := a.selectedContact()
	if ct == nil {
		return false
	}
	nav := ct.issueNavigation()
	if ct.SelectedWorkTaskID != "" {
		ct.SelectedWorkTaskID = ""
		nav.FocusBack = true
		return true
	}
	if nav.DetailID != "" {
		a.backToIssueOverview()
		return true
	}
	if nav.ShowConversationWork {
		a.backFromConversationWork()
		return true
	}
	if a.workspaceOpen {
		a.setWorkspaceOpen(false)
		a.focusWorkspace = true
		return true
	}
	return false
}

func (a *appModel) backToIssueOverview() {
	ct := a.selectedContact()
	nav := ct.issueNavigation()
	if nav == nil || nav.DetailID == "" {
		return
	}
	detailID := nav.DetailID
	nav.DetailID = ""
	ct.SelectedWorkTaskID = ""
	nav.FocusRowID = detailID
}

func (a *appModel) backFromConversationWork() {
	ct := a.selectedContact()
	nav := ct.issueNavigation()
	if nav == nil {
		return
	}
	nav.ShowConversationWork = false
	nav.FocusConversation = true
}

func conversationWork(ct *contact) *workItem {
	if ct == nil {
		return nil
	}
	if current := ct.activeWork(); current != nil {
		if issue, _ := ct.issueForTask(current.TaskID); issue == nil {
			return current
		}
	}
	for i := len(ct.Works) - 1; i >= 0; i-- {
		w := ct.Works[i]
		if w != nil {
			if issue, _ := ct.issueForTask(w.TaskID); issue == nil {
				return w
			}
		}
	}
	return nil
}
