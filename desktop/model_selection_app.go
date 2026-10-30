package main

// App-level orchestration for the session model editor. The UI state lives in
// modelSelection; this file only snapshots a Runtime target, runs one request,
// and routes the result back to that same form/target.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

type modelRecoveryState int

const (
	modelRecoveryNeedsRefresh modelRecoveryState = iota + 1
	modelRecoveryNeedsReload
)

func modelConnectionID(config runtimeConnectionConfig) (string, bool) {
	effective := config.EffectiveConfig
	if effective.ConnectionID != nil && *effective.ConnectionID != "" {
		return *effective.ConnectionID, true
	}
	if effective.ExecutionConnectionID != nil && *effective.ExecutionConnectionID != "" {
		return *effective.ExecutionConnectionID, true
	}
	return "", false
}

func (a *appModel) modelRecoveryFor(sessionID string) modelRecoveryState {
	return a.modelRecovery[sessionID]
}

func (a *appModel) setModelRecovery(sessionID string, state modelRecoveryState) {
	if sessionID == "" {
		return
	}
	if state == 0 {
		delete(a.modelRecovery, sessionID)
		return
	}
	if a.modelRecovery == nil {
		a.modelRecovery = map[string]modelRecoveryState{}
	}
	a.modelRecovery[sessionID] = state
	a.updateModelContactDisplay(sessionID, "")
}

func (a *appModel) loadModelCatalog(rt *runtimeClient, config runtimeConnectionConfig) (runtimeModelCatalog, error) {
	connectionID, ok := modelConnectionID(config)
	if !ok {
		return runtimeModelCatalog{}, errors.New("Runtime did not report a persistent connection id")
	}
	return rt.models(connectionID)
}

func (a *appModel) modelTargetBusy(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	for _, ct := range a.contacts {
		if ct.SessionID == sessionID && sessionBusy(ct.SendState, ct.ActiveTaskID) {
			return true
		}
		for _, issue := range ct.Issues {
			if issue.WorkSessionID == sessionID &&
				(sessionBusy(issue.Work.SendState, issue.Work.ActiveTaskID) || issue.Work.Releasing || issue.Work.ReleasePending) {
				return true
			}
		}
	}
	return false
}

func (a *appModel) modelTargetNeedsReload(sessionID string) bool {
	return a.modelRecoveryFor(sessionID) == modelRecoveryNeedsReload
}

func (a *appModel) modelOwner(sessionID string) (contactID, issueID string) {
	for _, ct := range a.contacts {
		if ct.SessionID == sessionID {
			return ct.ID, ""
		}
		for _, issue := range ct.Issues {
			if issue.WorkSessionID == sessionID {
				return ct.ID, issue.ID
			}
		}
	}
	return "", ""
}

func (a *appModel) updateModelContactDisplay(sessionID, model string) {
	for _, ct := range a.contacts {
		if ct.SessionID == sessionID {
			ct.SessionModel = model
			return
		}
	}
}

func (a *appModel) openComposerModel() {
	ct := a.selectedContact()
	if ct == nil {
		return
	}
	a.composerPanel = composerPanelModel
	a.openSessionModel(ct.SessionID, "conversation", ct.Name)
}

func (a *appModel) openSessionModel(sessionID, kind, label string) {
	if a.shutdownStarted || a.connectionApplying || a.modelOpActive {
		return
	}
	a.aux = "model"
	a.modelPanelMessage = ""
	a.modelPanelError = ""
	a.modelForm = nil
	a.modelOpenToken++
	openToken := a.modelOpenToken
	if strings.TrimSpace(sessionID) == "" {
		a.modelPanelMessage = "This context has no Runtime session yet. New work uses the model from Connection settings; after the first task creates a session you can switch it independently."
		return
	}
	if a.rt == nil || a.runtimeStatus != runtimeStateReady {
		a.modelPanelError = "Runtime is not ready; the session model cannot be read."
		return
	}
	rt, generation := a.rt, a.connectionGeneration
	a.modelPanelMessage = "Reading the session model from Runtime…"
	go func() {
		config, err := rt.connectionConfig(sessionID)
		if err != nil {
			a.updateUI(func() {
				if a.rt != rt || a.connectionGeneration != generation || a.aux != "model" || a.modelOpenToken != openToken || a.shutdownStarted {
					return
				}
				a.modelPanelMessage = ""
				a.modelPanelError = "Could not read this session's Runtime configuration: " + a.connectionRuntimeMessage(err)
			})
			return
		}
		catalog, catalogErr := a.loadModelCatalog(rt, config)
		a.updateUI(func() {
			if a.rt != rt || a.connectionGeneration != generation || a.aux != "model" || a.modelOpActive || a.modelOpenToken != openToken || a.shutdownStarted {
				return
			}
			target := modelSelectionTarget{Kind: kind, Label: label, Config: config}
			form := newModelSelection(target, catalog)
			if catalogErr != nil {
				form.SetStatus("Runtime catalog could not be loaded; explicit limits are available.")
			}
			if a.modelTargetNeedsReload(sessionID) {
				form.SetUnconfirmed("This session's configuration is quarantined. Reload the session before changing the model.")
			} else {
				a.setModelRecovery(sessionID, 0)
				a.updateModelContactDisplay(sessionID, config.EffectiveConfig.Model)
			}
			a.modelForm = form
			a.modelPanelMessage = ""
			a.modelPanelError = ""
		})
	}()
}

func (a *appModel) refreshSessionModel(target modelSelectionTarget) {
	form := a.modelForm
	if form == nil || form.Busy || a.modelOpActive || a.shutdownStarted || a.connectionApplying {
		return
	}
	sessionID := form.Target.Config.SessionID
	if sessionID == "" || target.Config.SessionID != sessionID || a.rt == nil {
		return
	}
	rt, generation := a.rt, a.connectionGeneration
	form.SetBusy(true, "Reading Runtime configuration…")
	go func() {
		config, err := rt.connectionConfig(sessionID)
		if err != nil {
			a.updateUI(func() {
				if a.rt != rt || a.connectionGeneration != generation || a.modelForm != form || a.modelOpActive || a.shutdownStarted {
					return
				}
				if !a.modelTargetNeedsReload(sessionID) {
					a.setModelRecovery(sessionID, modelRecoveryNeedsRefresh)
				}
				form.SetUnconfirmed("Runtime did not confirm this session's configuration. Refresh to read it again.")
			})
			return
		}
		catalog, catalogErr := a.loadModelCatalog(rt, config)
		a.updateUI(func() {
			if a.rt != rt || a.connectionGeneration != generation || a.modelForm != form || a.modelOpActive || a.shutdownStarted {
				return
			}
			form.SetEffective(config)
			if catalogErr == nil {
				form.SetCatalog(catalog)
			}
			if a.modelTargetNeedsReload(sessionID) {
				form.SetUnconfirmed("Configuration was read back, but this session still needs an explicit Reload before new tasks.")
			} else {
				a.setModelRecovery(sessionID, 0)
				a.updateModelContactDisplay(sessionID, config.EffectiveConfig.Model)
				if catalogErr != nil {
					form.SetBusy(false, "Configuration refreshed; Runtime catalog could not be loaded.")
				} else {
					form.SetBusy(false, "Runtime configuration refreshed.")
				}
			}
		})
	}()
}

func (a *appModel) updateSessionModel(request modelSelectionRequest) {
	form := a.modelForm
	if form == nil || form.Busy {
		return
	}
	sessionID := request.Target.Config.SessionID
	if sessionID == "" || form.Target.Config.SessionID != sessionID || a.modelOpActive || a.shutdownStarted || a.connectionApplying {
		return
	}
	if a.rt == nil || a.runtimeStatus != runtimeStateReady {
		form.SetError("Runtime is not ready; the model was not updated.")
		return
	}
	switch a.modelRecoveryFor(sessionID) {
	case modelRecoveryNeedsReload:
		form.SetUnconfirmed("This session needs an explicit Reload before another model update.")
		return
	case modelRecoveryNeedsRefresh:
		form.SetUnconfirmed("Refresh the Runtime configuration before another model update.")
		return
	}
	if form.Unconfirmed {
		form.SetError("Refresh the Runtime configuration before updating the model.")
		return
	}
	if a.modelTargetBusy(sessionID) {
		form.SetError("This session is busy. Wait for its current task to finish before changing the model.")
		return
	}
	contactID, issueID := a.modelOwner(sessionID)
	released := false
	if issueID != "" {
		released = a.contactByID(contactID).issueByID(issueID).Work.Released
	}
	rt, generation := a.rt, a.connectionGeneration
	revision := request.Target.Config.ConfigRevision
	form.SetBusy(true, "Updating the session model…")
	a.modelOpActive = true
	a.modelOpSessionID = sessionID
	go func() {
		var loaded runtimeSession
		var loadErr error
		if released {
			loaded, loadErr = rt.loadSession(sessionID)
			if loadErr != nil {
				loadErr = fmt.Errorf("load released work session: %w", loadErr)
			}
		}
		var updated runtimeConfigUpdated
		var err error
		if loadErr == nil {
			updated, err = rt.updateSessionModel(sessionID, revision, request.Patch)
		}
		a.updateUI(func() {
			if !a.modelOpActive || a.modelOpSessionID != sessionID {
				return
			}
			a.modelOpActive = false
			a.modelOpSessionID = ""
			sameRuntime := a.rt == rt && a.connectionGeneration == generation
			if !sameRuntime || !a.modelOriginStillCurrent(contactID, issueID, sessionID) {
				if a.shutdownStarted {
					a.finishShutdown()
				}
				return
			}
			if released && loadErr == nil && issueID != "" {
				issue := a.contactByID(contactID).issueByID(issueID)
				issue.Work.Released = false
				issue.Work.Loaded = true
				if loaded.State != "" {
					issue.Work.SessionState = loaded.State
				}
			}
			if loadErr != nil {
				form.SetBusy(false, "")
				form.SetError("Could not load this released work session before updating the model: " + a.connectionRuntimeMessage(loadErr))
			} else if err == nil {
				form.SetResult(updated.runtimeConnectionConfig)
				form.SetStatus("Model updated. The next task on this session uses it; history is unchanged.")
				a.updateModelContactDisplay(sessionID, updated.EffectiveConfig.Model)
			} else {
				a.applyModelUpdateFailure(form, sessionID, updated, err)
			}
			if a.shutdownStarted {
				a.finishShutdown()
			}
		})
	}()
}

func (a *appModel) modelOriginStillCurrent(contactID, issueID, sessionID string) bool {
	ct := a.contactByID(contactID)
	if ct == nil {
		return false
	}
	if issueID != "" {
		issue := ct.issueByID(issueID)
		return issue != nil && issue.WorkSessionID == sessionID
	}
	return ct.SessionID == sessionID
}

func (a *appModel) applyModelUpdateFailure(form *modelSelection, sessionID string, updated runtimeConfigUpdated, err error) {
	message := a.connectionRuntimeMessage(err)
	if isCommitUnknown(err) {
		a.setModelRecovery(sessionID, modelRecoveryNeedsReload)
		if updated.ReadbackError == nil {
			form.SetEffective(updated.runtimeConnectionConfig)
		}
		form.SetUnconfirmed("Configuration commit is unconfirmed. Reload the session before sending another task. The original error was: " + message)
		return
	}
	if updated.ReadbackError != nil {
		a.setModelRecovery(sessionID, modelRecoveryNeedsRefresh)
		form.SetUnconfirmed("Runtime did not confirm the current configuration. Refresh to read it again. The update error was: " + message)
		return
	}
	a.setModelRecovery(sessionID, 0)
	form.SetEffective(updated.runtimeConnectionConfig)
	a.updateModelContactDisplay(sessionID, updated.EffectiveConfig.Model)
	form.SetError(message)
}

func isCommitUnknown(err error) bool {
	var runtimeErr *runtimeError
	return errors.As(err, &runtimeErr) && runtimeErr.Code == "commit_unknown"
}

// reloadModelSession is the explicit Release→Load recovery for a quarantined
// session. Refreshing config alone is not enough to make the session sendable.
func (a *appModel) reloadModelSession() {
	form := a.modelForm
	if form == nil || form.Busy || a.modelOpActive || a.shutdownStarted || a.connectionApplying {
		return
	}
	sessionID := form.Target.Config.SessionID
	if sessionID == "" || !a.modelTargetNeedsReload(sessionID) || a.rt == nil {
		return
	}
	rt, generation := a.rt, a.connectionGeneration
	contactID, issueID := a.modelOwner(sessionID)
	form.SetBusy(true, "Reloading the session…")
	a.modelOpActive = true
	a.modelOpSessionID = sessionID
	go func() {
		err := rt.releaseSession(sessionID)
		if err == nil {
			_, err = rt.loadSession(sessionID)
		}
		var config runtimeConnectionConfig
		if err == nil {
			config, err = rt.connectionConfig(sessionID)
		}
		a.updateUI(func() {
			if !a.modelOpActive || a.modelOpSessionID != sessionID {
				return
			}
			a.modelOpActive = false
			a.modelOpSessionID = ""
			if a.rt != rt || a.connectionGeneration != generation || a.modelForm != form {
				if a.shutdownStarted {
					a.finishShutdown()
				}
				return
			}
			if err != nil {
				form.SetUnconfirmed("Session reload failed; the session remains quarantined. " + a.connectionRuntimeMessage(err))
			} else {
				a.setModelRecovery(sessionID, 0)
				form.SetEffective(config)
				form.SetBusy(false, "Session reloaded. The next task uses the read-back model.")
				a.updateModelContactDisplay(sessionID, config.EffectiveConfig.Model)
				if issueID != "" {
					if ct := a.contactByID(contactID); ct != nil {
						if issue := ct.issueByID(issueID); issue != nil {
							issue.Work.Loaded = false
						}
					}
					a.reloadIssueWorkByID(contactID, issueID)
				} else if contactID != "" {
					if ct := a.contactByID(contactID); ct != nil {
						ct.Loaded = false
					}
					a.reloadContactByID(contactID, false)
				}
			}
			if a.shutdownStarted {
				a.finishShutdown()
			}
		})
	}()
}

func (a *appModel) modelPanel(c *ui.Context, t *ui.Theme) {
	if a.modelForm != nil {
		if a.modelTargetNeedsReload(a.modelForm.Target.Config.SessionID) {
			secondaryButton(c, "Reload session").Disabled(a.modelOpActive).OnClick(func() { a.reloadModelSession() })
			ui.Text(c, "Configuration commit is unconfirmed. Reload is required before this session can accept another task; Refresh only re-reads config.").FontSize(14).LineHeight(1.4).TextColor(t.Warning)
		}
		a.modelForm.view(c, t, modelSelectionActions{
			Refresh: a.refreshSessionModel,
			Update:  a.updateSessionModel,
			Cancel:  a.closeAux,
		})
	} else if a.modelPanelError != "" {
		ui.Text(c, "Session model").FontSize(17).FontWeight(600).TextColor(t.Text)
		ui.Text(c, a.modelPanelError).FontSize(14).LineHeight(1.4).TextColor(t.Danger)
		secondaryButton(c, "Close").OnClick(func() { a.closeAux() })
	} else {
		ui.Text(c, "Session model").FontSize(17).FontWeight(600).TextColor(t.Text)
		ui.Text(c, valueOr(a.modelPanelMessage, "Reading session model…")).FontSize(14).LineHeight(1.4).TextColor(t.TextMuted)
		secondaryButton(c, "Close").OnClick(func() { a.closeAux() })
	}
}

func (a *appModel) modelSessionStatus(sessionID string) string {
	switch a.modelRecoveryFor(sessionID) {
	case modelRecoveryNeedsReload:
		return "Session configuration is unconfirmed; Reload the session before sending."
	case modelRecoveryNeedsRefresh:
		return "Session configuration is unconfirmed; Refresh before sending."
	}
	if a.modelOpActive && a.modelOpSessionID == sessionID {
		return "The session model is being updated; this session cannot accept a new task yet."
	}
	return ""
}
