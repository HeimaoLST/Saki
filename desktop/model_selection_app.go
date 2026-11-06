package main

// App-level orchestration for the session model editor. The UI state lives in
// modelSelection; this file only snapshots a Runtime target, runs one request,
// and routes the result back to that same form/target.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"saki/desktop/locales"
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
	if a.composerModelOpen {
		a.closeComposerModel()
		return
	}
	ct := a.selectedContact()
	if ct == nil {
		return
	}
	if a.prepareConversationModel() {
		a.composerModelOpen = true
		a.composerModelFocus = true
	}
}

func (a *appModel) prepareConversationModel() bool {
	ct := a.selectedContact()
	if ct == nil || a.shutdownStarted || a.connectionApplying || a.modelOpActive {
		return false
	}
	if a.modelForm != nil && a.modelPanelKind == "conversation" {
		if a.conversationModelForms == nil {
			a.conversationModelForms = map[string]*modelSelection{}
		}
		a.conversationModelForms[a.modelForm.Target.Config.SessionID] = a.modelForm
	}
	if form := a.conversationModelForms[ct.SessionID]; form != nil && a.modelRecoveryFor(ct.SessionID) != modelRecoveryNeedsRefresh {
		a.modelForm = form
		a.modelPanelKind = "conversation"
		a.modelPanelSessionID = ct.SessionID
		form.Target.Label = ct.Name
		if a.rt != nil && a.runtimeStatus == runtimeStateReady {
			a.refreshSessionModel(form.Target)
		}
		return true
	}
	return a.openSessionModel(ct.SessionID, "conversation", ct.Name)
}

func (a *appModel) closeComposerModel() {
	a.composerModelOpen = false
}

func (a *appModel) chooseComposerModel(index int) {
	form := a.modelForm
	if form == nil || form.Busy || a.modelOpActive || index < 0 || index >= len(form.Catalog.ConfiguredModels) {
		return
	}
	if !form.Unconfirmed && a.modelRecoveryFor(form.Target.Config.SessionID) == 0 &&
		form.Catalog.ConfiguredModels[index].Model == form.Effective.Config.EffectiveConfig.Model {
		a.closeComposerModel()
		a.composerFocusModel = true
		return
	}
	form.SelectConfigured(index)
	if request, ok := form.Request(); ok {
		compaction := form.Catalog.ConfiguredModels[index].Compaction
		request.Patch.Compaction = &compaction
		a.updateSessionModel(request)
	}
}

func (a *appModel) openSessionModel(sessionID, kind, label string) bool {
	if a.shutdownStarted || a.connectionApplying || a.modelOpActive {
		return false
	}
	a.composerModelOpen = false
	if kind == "work" {
		a.cancelHistoryFullDisplay()
		a.composerPanel = ""
		a.composerFocusModel = false
		a.composerFocusContext = false
		a.aux = "model"
	}
	a.modelPanelKind = kind
	a.modelPanelSessionID = sessionID
	a.modelPanelLabel = label
	if kind == "work" {
		a.focusAuxBack = true
	}
	a.modelPanelMessage = ""
	a.modelPanelError = ""
	a.modelPanelErrorDetail = ""
	a.modelForm = nil
	a.modelOpenToken++
	openToken := a.modelOpenToken
	if strings.TrimSpace(sessionID) == "" {
		a.modelPanelMessage = modelPanelMessageNoSession
		return true
	}
	if a.rt == nil || a.runtimeStatus != runtimeStateReady {
		a.modelPanelError = modelPanelErrorRuntimeNotReady
		return true
	}
	rt, generation := a.rt, a.connectionGeneration
	a.modelPanelMessage = modelPanelMessageReading
	go func() {
		config, err := rt.connectionConfig(sessionID)
		if err != nil {
			a.updateUI(func() {
				if a.rt != rt || a.connectionGeneration != generation || a.modelPanelKind != kind || a.modelOpenToken != openToken || a.shutdownStarted {
					return
				}
				a.modelPanelMessage = ""
				a.modelPanelError = modelPanelErrorReadFailed
				a.modelPanelErrorDetail = a.connectionRuntimeMessage(err)
			})
			return
		}
		catalog, catalogErr := a.loadModelCatalog(rt, config)
		a.updateUI(func() {
			if a.rt != rt || a.connectionGeneration != generation || a.modelPanelKind != kind || a.modelOpActive || a.modelOpenToken != openToken || a.shutdownStarted {
				return
			}
			target := modelSelectionTarget{Kind: kind, Label: label, Config: config}
			form := newModelSelection(target, catalog)
			if kind == "conversation" {
				if a.conversationModelForms == nil {
					a.conversationModelForms = map[string]*modelSelection{}
				}
				if saved := a.conversationModelForms[sessionID]; saved != nil {
					form = saved
					form.SetEffective(config)
					form.SetCatalog(catalog)
				}
				a.conversationModelForms[sessionID] = form
			}
			if catalogErr != nil {
				form.SetStatus(modelStatusCatalogUnavailable)
			}
			if a.modelTargetNeedsReload(sessionID) {
				form.SetUnconfirmed(modelUnconfirmedQuarantined, "")
			} else {
				a.setModelRecovery(sessionID, 0)
				a.updateModelContactDisplay(sessionID, config.EffectiveConfig.Model)
			}
			a.modelForm = form
			a.modelPanelMessage = ""
			a.modelPanelError = ""
			a.modelPanelErrorDetail = ""
		})
	}()
	return true
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
	form.SetBusy(true, modelStatusReadingConfig)
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
				form.SetUnconfirmed(modelUnconfirmedNotConfirmed, "")
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
				form.SetUnconfirmed(modelUnconfirmedNeedsReload, "")
			} else {
				a.setModelRecovery(sessionID, 0)
				a.updateModelContactDisplay(sessionID, config.EffectiveConfig.Model)
				if catalogErr != nil {
					form.SetBusy(false, modelStatusRefreshedNoCatalog)
				} else {
					form.SetBusy(false, modelStatusRefreshed)
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
		form.SetErrorKind(modelErrorRuntimeNotReady, "")
		return
	}
	switch a.modelRecoveryFor(sessionID) {
	case modelRecoveryNeedsReload:
		form.SetUnconfirmed(modelUnconfirmedNeedReloadUpdate, "")
		return
	case modelRecoveryNeedsRefresh:
		form.SetUnconfirmed(modelUnconfirmedNeedRefreshUpdate, "")
		return
	}
	if form.Unconfirmed {
		form.SetErrorKind(modelErrorRefreshBefore, "")
		return
	}
	if a.modelTargetBusy(sessionID) {
		form.SetErrorKind(modelErrorBusy, "")
		return
	}
	contactID, issueID := a.modelOwner(sessionID)
	released := false
	if issueID != "" {
		released = a.contactByID(contactID).issueByID(issueID).Work.Released
	}
	rt, generation := a.rt, a.connectionGeneration
	revision := request.Target.Config.ConfigRevision
	form.SetBusy(true, modelStatusUpdating)
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
				form.SetErrorKind(modelErrorReleasedLoad, a.connectionRuntimeMessage(loadErr))
			} else if err == nil {
				form.SetResult(updated.runtimeConnectionConfig)
				form.SetStatus(modelStatusUpdated)
				a.updateModelContactDisplay(sessionID, updated.EffectiveConfig.Model)
				if a.modelForm == form && a.composerModelOpen {
					a.closeComposerModel()
					a.composerFocusModel = true
				}
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
		form.SetUnconfirmed(modelUnconfirmedCommit, message)
		return
	}
	if updated.ReadbackError != nil {
		a.setModelRecovery(sessionID, modelRecoveryNeedsRefresh)
		form.SetUnconfirmed(modelUnconfirmedReadback, message)
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
	form.SetBusy(true, modelStatusReloading)
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
				form.SetUnconfirmed(modelUnconfirmedReloadFailed, a.connectionRuntimeMessage(err))
			} else {
				a.setModelRecovery(sessionID, 0)
				form.SetEffective(config)
				form.SetBusy(false, modelStatusReloaded)
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
	msgs := locales.In(c.Locale())
	if a.modelForm != nil {
		if a.modelTargetNeedsReload(a.modelForm.Target.Config.SessionID) {
			secondaryButton(c, msgs.ModelReloadAction()).Disabled(a.modelOpActive).OnClick(func() { a.reloadModelSession() })
			ui.Text(c, msgs.ModelUnconfirmedBanner()).FontSize(14).LineHeight(1.5).TextColor(t.Warning)
		}
		a.modelForm.view(c, t, modelSelectionActions{
			Refresh: a.refreshSessionModel,
			Update:  a.updateSessionModel,
			Cancel:  a.closeAux,
		})
	} else if a.modelPanelError != "" {
		ui.Text(c, msgs.ModelSessionTitle()).FontSize(16).FontWeight(500).TextColor(t.Text)
		errorText := modelPanelErrorText(msgs, a.modelPanelError, a.modelPanelErrorDetail)
		ui.Text(c, errorText).FontSize(14).LineHeight(1.5).TextColor(t.Danger)
		secondaryButton(c, msgs.ModelClose()).OnClick(func() { a.closeAux() })
	} else {
		ui.Text(c, msgs.ModelSessionTitle()).FontSize(16).FontWeight(500).TextColor(t.Text)
		message := valueOr(modelPanelMessageText(msgs, a.modelPanelMessage), msgs.ModelDefaultReading())
		ui.Text(c, message).FontSize(14).LineHeight(1.5).TextColor(t.TextMuted)
		secondaryButton(c, msgs.ModelClose()).OnClick(func() { a.closeAux() })
	}
}

func (a *appModel) modelSessionStatus(sessionID string) string {
	switch a.modelRecoveryFor(sessionID) {
	case modelRecoveryNeedsReload:
		return blockModelReload
	case modelRecoveryNeedsRefresh:
		return blockModelRefresh
	}
	if a.modelOpActive && a.modelOpSessionID == sessionID {
		return blockModelUpdating
	}
	return ""
}
