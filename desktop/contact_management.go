package main

import (
	"fmt"
	"strings"
)

type contactHistoryFull struct {
	SessionID string
	EntryID   string
	Date      string
	From      string
	Text      string
	Context   []taskContextItem
}

func contactProfileDraft(ct *contact) profileDraft {
	return profileDraft{Name: ct.Name, Purpose: ct.Purpose, Instructions: ct.Instructions, Workspace: ct.Workspace}
}

func (a *appModel) openContactProfile(contactID string) {
	ct := a.contactByID(contactID)
	if ct == nil {
		return
	}
	a.contactProfileID = ct.ID
	a.contactProfile = newContactProfileEditor(contactProfileDraft(ct))
	a.contactProfile.Archived = ct.Archived
	a.contactProfile.HasConversation = ct.SessionID != ""
	a.contactProfile.ActiveWork, a.contactProfile.UnknownWork = a.contactActivity(ct)
	a.aux = "settings"
}

func (a *appModel) contactActivity(ct *contact) (active, unknown bool) {
	if sessionBusy(ct.SendState, ct.ActiveTaskID) || ct.pendingSend != nil || ct.pendingIssueExecution != "" {
		active = true
	}
	if ct.SendState == sendUnknown || (ct.pendingSend != nil && ct.pendingSend.State == "unknown") {
		unknown = true
	}
	if a.modelOpActive && ct.SessionID != "" && a.modelOpSessionID == ct.SessionID {
		active = true
	}
	for _, issue := range ct.Issues {
		if sessionBusy(issue.Work.SendState, issue.Work.ActiveTaskID) || issue.Work.pendingExecutionID != "" {
			active = true
		}
		if issue.Work.SendState == sendUnknown {
			unknown = true
		}
		if issue.Supplements != nil && issue.Supplements.AutoContinue && issue.Supplements.pendingCount() > 0 {
			active = true
		}
		for _, execution := range issue.Executions {
			if !unresolvedExecution(execution) {
				continue
			}
			active = true
			if execution.Status == issueExecutionUnknown {
				unknown = true
			}
		}
		if a.modelOpActive && issue.WorkSessionID != "" && a.modelOpSessionID == issue.WorkSessionID {
			active = true
		}
	}
	return active, unknown
}

func (a *appModel) contactArchiveBlocked(ct *contact) string {
	if ct.Archived {
		return "This contact is already archived."
	}
	active, unknown := a.contactActivity(ct)
	if unknown {
		return "Work is still unresolved. Reload to confirm its status before archiving."
	}
	if active {
		return "Active work must finish or stop before archiving this contact."
	}
	return ""
}

func (a *appModel) saveContactProfileDraft(draft profileDraft, startNew bool) bool {
	ct := a.selectedContact()
	entered := draft
	if ct == nil || (a.contactProfile != nil && a.contactProfileID != "" && a.contactProfileID != ct.ID) {
		return false
	}
	if !a.demo && a.store == nil {
		if a.contactProfile != nil {
			a.contactProfile.Draft = entered
			a.contactProfile.SetError("Could not save this profile: the local contact store is unavailable.")
		}
		return false
	}
	if ct.Archived {
		if a.contactProfile != nil {
			a.contactProfile.SetError("Restore this contact before saving its profile.")
		}
		return false
	}
	if startNew {
		active, unknown := a.contactActivity(ct)
		if unknown || active {
			if a.contactProfile != nil {
				a.contactProfile.SetError(a.contactProfile.activityReason())
			}
			return false
		}
		if ct.SessionID == "" {
			if a.contactProfile != nil {
				a.contactProfile.SetError("There is no current conversation to leave.")
			}
			return false
		}
	}

	draft, problems := validateProfileDraft(entered)
	if len(problems) > 0 {
		if a.contactProfile != nil {
			a.contactProfile.Draft = entered
			a.contactProfile.Errors = problems
			a.contactProfile.Error = ""
			a.contactProfile.Status = ""
		}
		return false
	}
	previous := *ct
	previousSessions := append([]string(nil), ct.PreviousSessions...)
	var oldState storedState
	if a.store != nil {
		oldState = a.store.state
	}

	ct.Name = draft.Name
	ct.Purpose = draft.Purpose
	ct.Instructions = draft.Instructions
	ct.Workspace = draft.Workspace
	if startNew {
		if ct.SessionID != "" {
			ct.PreviousSessions = append(ct.PreviousSessions, ct.SessionID)
		}
		ct.SessionID = ""
		ct.Loaded = false
		ct.LoadErr = ""
		ct.Unavailable = ""
		ct.ActiveTaskID = ""
		ct.LastTaskID = ""
		ct.SendState = sendIdle
		ct.SendError = ""
		ct.SubmittedDraft = ""
		ct.SubmittedRev = 0
		ct.pendingSend = nil
		ct.Messages = nil
		ct.History = nil
		ct.Revealed = nil
		ct.Works = nil
		ct.SelectedWorkTaskID = ""
		ct.Status = statusReady
		ct.Work = ""
		ct.WorkLabel = "Preparing a reply"
		ct.UnlinkedTools = nil
		ct.StopRequestedTasks = nil
		ct.pendingEvents = nil
		ct.HistoryDirty = false
		ct.SessionWorkingDir = ""
		ct.SessionModel = ""
		ct.SessionAPI = ""
		ct.SessionState = ""
	}
	if err := a.persist(); err != nil {
		*ct = previous
		ct.PreviousSessions = previousSessions
		if a.store != nil {
			a.store.state = oldState
		}
		if a.contactProfile != nil {
			a.contactProfile.Draft = entered
			a.contactProfile.SetError("Could not save this profile. Your changes are still here.")
		}
		return false
	}
	if a.contactProfile != nil {
		a.contactProfile.SetProfile(draft)
		a.contactProfile.HasConversation = ct.SessionID != ""
		a.contactProfile.Status = "Profile saved. New conversations and new issue work sessions use these defaults; existing work is unchanged."
		if startNew {
			a.contactProfile.HasConversation = false
			a.contactProfile.Status = "Profile saved. The next message starts a new conversation with these defaults."
		}
	}
	return true
}

func (a *appModel) archiveContact(contactID string) bool {
	ct := a.contactByID(contactID)
	if ct == nil || ct.Archived {
		return false
	}
	if !a.demo && a.store == nil {
		if a.contactProfile != nil && a.contactProfileID == contactID {
			a.contactProfile.SetError("Could not archive this contact: the local contact store is unavailable.")
		}
		return false
	}
	if reason := a.contactArchiveBlocked(ct); reason != "" {
		if a.contactProfile != nil && a.contactProfileID == contactID {
			a.contactProfile.SetError(reason)
		}
		return false
	}
	old := *ct
	var oldState storedState
	if a.store != nil {
		oldState = a.store.state
	}
	ct.Archived = true
	ct.ArchivedAt = nowMillis()
	if err := a.persist(); err != nil {
		*ct = old
		if a.store != nil {
			a.store.state = oldState
		}
		if a.contactProfile != nil && a.contactProfileID == contactID {
			a.contactProfile.SetError("Could not archive this contact. Its previous state is unchanged.")
		}
		return false
	}
	if a.contactProfile != nil && a.contactProfileID == contactID {
		a.contactProfile.Archived = true
		a.contactProfile.Status = "Contact archived. Sending and new issue runs are disabled; history and issue data are retained."
	}
	if a.selectedContact().ID == contactID {
		a.contactScope = "archived"
	}
	return true
}

func (a *appModel) restoreContact(contactID string) bool {
	ct := a.contactByID(contactID)
	if ct == nil || !ct.Archived {
		return false
	}
	if !a.demo && a.store == nil {
		if a.contactProfile != nil && a.contactProfileID == contactID {
			a.contactProfile.SetError("Could not restore this contact: the local contact store is unavailable.")
		}
		return false
	}
	old := *ct
	var oldState storedState
	if a.store != nil {
		oldState = a.store.state
	}
	ct.Archived = false
	ct.ArchivedAt = 0
	if err := a.persist(); err != nil {
		*ct = old
		if a.store != nil {
			a.store.state = oldState
		}
		if a.contactProfile != nil && a.contactProfileID == contactID {
			a.contactProfile.SetError("Could not restore this contact. Its archived state is unchanged.")
		}
		return false
	}
	if a.contactProfile != nil && a.contactProfileID == contactID {
		a.contactProfile.Archived = false
		a.contactProfile.Status = "Contact restored."
	}
	if a.selectedContact().ID == contactID {
		a.contactScope = "active"
	}
	return true
}

func (a *appModel) visibleContactIndices() []int {
	return a.visibleContactIndicesForScope(a.contactScope, a.contactSearch)
}

func (a *appModel) visibleContactIndicesForScope(scope, search string) []int {
	query := strings.ToLower(strings.TrimSpace(search))
	if scope == "" {
		scope = "active"
	}
	out := make([]int, 0, len(a.contacts))
	for i, ct := range a.contacts {
		switch scope {
		case "archived":
			if !ct.Archived {
				continue
			}
		case "all":
		default:
			if ct.Archived {
				continue
			}
		}
		if query != "" && !strings.Contains(strings.ToLower(ct.Name+" "+ct.Purpose), query) {
			continue
		}
		out = append(out, i)
	}
	return out
}

func (a *appModel) setContactScope(scope string) {
	if scope != "active" && scope != "archived" && scope != "all" {
		return
	}
	indices := a.visibleContactIndicesForScope(scope, a.contactSearch)
	if len(indices) == 0 {
		a.contactScope = scope
		return
	}
	target := indices[0]
	for _, i := range indices {
		if i == a.selected {
			target = a.selected
			break
		}
	}
	if target != a.selected {
		if !a.leaveIssueFormForSwitch() {
			return
		}
		a.contactScope = scope
		a.selectContact(target)
		return
	}
	a.contactScope = scope
}

func (a *appModel) contactHistorySessionIDs(ct *contact) []string {
	ids := make([]string, 0, 1+len(ct.PreviousSessions))
	seen := map[string]bool{}
	if ct.SessionID != "" {
		ids = append(ids, ct.SessionID)
		seen[ct.SessionID] = true
	}
	for _, id := range ct.PreviousSessions {
		if id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids
}

func (a *appModel) historyAsyncStarted() {
	a.contactHistoryInFlight++
}

func (a *appModel) historyAsyncFinished() {
	if a.contactHistoryInFlight > 0 {
		a.contactHistoryInFlight--
	}
	if a.shutdownStarted && !a.quitReady {
		a.finishShutdown()
	}
}

// Bind results to the contact, Runtime generation and query that started them.
func (a *appModel) searchContactHistoryAsync(query string) {
	if a.shutdownStarted {
		return
	}
	ct := a.selectedContact()
	query = strings.TrimSpace(query)
	if query == "" {
		a.contactHistoryError = "Enter a phrase to search saved contexts."
		return
	}
	if ct == nil || a.rt == nil || a.runtimeStatus != runtimeStateReady {
		a.contactHistoryError = "Saved-context search is unavailable until the runtime is ready."
		return
	}
	a.contactHistoryGen++
	a.contactHistoryDetailGen++
	generation := a.contactHistoryGen
	contactID := ct.ID
	rt, runtimeGeneration := a.rt, a.connectionGeneration
	sessionIDs := a.contactHistorySessionIDs(ct)
	scopeIndex := a.issueToolScopeIndexSnapshot()
	a.contactHistoryBusy = true
	a.contactHistoryDetailBusy = false
	a.contactHistoryFull = nil
	a.contactHistoryError = ""
	a.historyAsyncStarted()
	go func() {
		result := a.searchContactHistory(rt, sessionIDs, query, scopeIndex)
		a.updateUI(func() {
			a.historyAsyncFinished()
			if a.rt != rt || a.connectionGeneration != runtimeGeneration || generation != a.contactHistoryGen || a.selectedContact().ID != contactID {
				return
			}
			a.contactHistory = &result
			a.contactHistoryBusy = false
			if len(result.Failures) > 0 {
				a.contactHistoryError = "Some saved contexts could not be read."
			}
		})
	}()
}

func (a *appModel) openContactHistoryMatch(match contactHistoryMatch) bool {
	if a.shutdownStarted {
		return false
	}
	ct := a.selectedContact()
	if ct == nil || a.rt == nil || a.runtimeStatus != runtimeStateReady {
		a.contactHistoryError = "Saved-context search is unavailable until the runtime is ready."
		return false
	}
	inScope := false
	for _, sessionID := range a.contactHistorySessionIDs(ct) {
		if sessionID == match.SessionID {
			inScope = true
			break
		}
	}
	if !inScope {
		a.contactHistoryError = "That message is not part of this contact's saved contexts."
		return false
	}
	contactID := ct.ID
	rt, runtimeGeneration := a.rt, a.connectionGeneration
	scopeIndex := a.issueToolScopeIndexSnapshot()
	searchGen := a.contactHistoryGen
	a.contactHistoryDetailGen++
	detailGen := a.contactHistoryDetailGen
	a.contactHistoryDetailBusy = true
	a.contactHistoryFull = nil
	a.contactHistoryError = ""
	a.historyAsyncStarted()
	go func() {
		records, _, err := a.historyAllFor(rt, match.SessionID)
		var full *contactHistoryFull
		errorText := ""
		if err != nil {
			errorText = "Could not read this saved context."
		} else {
			for _, entry := range parseHistoryWithIssueToolScopeFilter(match.SessionID, records, nil, scopeIndex).entries {
				if entry.ID == match.EntryID {
					full = &contactHistoryFull{
						SessionID: match.SessionID, EntryID: match.EntryID, Date: entry.Date,
						From: entry.From, Text: entry.Text, Context: entry.Context,
					}
					break
				}
			}
			if full == nil {
				errorText = "The saved message moved or is no longer available."
			}
		}
		a.updateUI(func() {
			a.historyAsyncFinished()
			if a.rt != rt || a.connectionGeneration != runtimeGeneration ||
				a.contactHistoryGen != searchGen || a.contactHistoryDetailGen != detailGen ||
				a.selectedContact().ID != contactID {
				return
			}
			a.contactHistoryDetailBusy = false
			if errorText != "" {
				if err != nil {
					a.contactHistoryError = errorText + " " + a.connectionRuntimeMessage(err)
				} else {
					a.contactHistoryError = errorText
				}
				return
			}
			a.contactHistoryFull = full
		})
	}()
	return true
}

func (a *appModel) contactHistorySummary() string {
	if a.contactHistory == nil {
		return ""
	}
	summary := fmt.Sprintf("%d saved matches", len(a.contactHistory.Matches))
	if a.contactHistory.Limited {
		summary += " (limit reached)"
	}
	if len(a.contactHistory.Failures) > 0 {
		summary += fmt.Sprintf("; %d contexts unavailable", len(a.contactHistory.Failures))
	}
	return summary
}
