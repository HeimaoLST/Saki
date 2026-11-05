package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type connectionStartup struct {
	Path       string
	Config     *connectionConfig
	FormConfig *connectionConfig
	LoadErr    error
	Missing    bool
	External   bool
}

func connectionConfigPath(storePath string) string {
	return filepath.Join(filepath.Dir(storePath), "connection.json")
}

func externalConnectionSource(workDir string) bool {
	if strings.TrimSpace(os.Getenv("API_KEY")) != "" {
		return true
	}
	if strings.TrimSpace(os.Getenv("SAKI_RUNTIME_CONFIG")) != "" {
		return true
	}
	dir, err := filepath.Abs(workDir)
	if err != nil {
		return false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func readConnectionStartup(storePath string, workDir string) connectionStartup {
	startup := connectionStartup{Path: connectionConfigPath(storePath)}
	config, err := loadConnectionConfig(startup.Path)
	if err == nil {
		startup.Config = &config
		startup.FormConfig = &config
		return startup
	}
	if errors.Is(err, os.ErrNotExist) {
		startup.Missing = true
		startup.External = externalConnectionSource(workDir)
		startup.LoadErr = nil
		return startup
	}
	startup.LoadErr = err
	if config.WorkingDirectory != "" || config.Endpoint != "" || config.APIKey != "" || config.Model != "" {
		startup.FormConfig = &config
	}
	return startup
}

func contactsFromStore(store *contactStore) []*contact {
	contacts := make([]*contact, 0, len(store.state.Contacts))
	for _, stored := range store.state.Contacts {
		ct := &contact{
			ID: stored.ID, Name: stored.Name, Role: stored.Role,
			Purpose: stored.Purpose, Instructions: stored.Instructions,
			Workspace: stored.Workspace, BuiltinTools: cloneBuiltinTools(stored.BuiltinTools),
			SessionID: stored.SessionID, Draft: stored.Draft,
			Archived: stored.Archived, ArchivedAt: stored.ArchivedAt,
			PreviousSessions: append([]string(nil), stored.PreviousSessions...),
			Status:           statusReady, WorkLabel: "Preparing a reply",
		}
		ct.Issues = issuesForStoredContact(store.state.Issues, ct.ID)
		if stored.SelectedIssueID != "" {
			ct.SelectedIssueID = stored.SelectedIssueID
		}
		if issue := ct.selectedIssue(); issue != nil {
			ct.SelectedIssueID = issue.ID
		}
		contacts = append(contacts, ct)
	}
	return contacts
}

func placeholderContact(workDir string) *contact {
	return &contact{
		ID: newContactID(), Name: "Saki", Role: "Assistant",
		Workspace: workDir, Status: statusReady, WorkLabel: "Preparing a reply",
	}
}

func applyStoreSelection(a *appModel, store *contactStore) {
	a.store = store
	a.issueToolLedger = store.issueToolLedger
	a.issueToolScopes = store.issueToolScopes
	a.contacts = contactsFromStore(store)
	a.selected = 0
	for i, ct := range a.contacts {
		if ct.ID == store.state.Selected {
			a.selected = i
			break
		}
	}
	if len(a.contacts) == 0 {
		a.contacts = []*contact{placeholderContact(a.workDir)}
		a.selected = 0
	}
	if a.selectedContact().Archived {
		a.contactScope = "archived"
	}
	if layout := store.state.Layout; layout != nil {
		a.leftPaneWidth = clampPaneWidth(layout.LeftWidth, defaultLeftPaneWidth, minLeftPaneWidth, maxLeftPaneWidth)
		a.rightPaneWidth = clampPaneWidth(layout.RightWidth, defaultRightPaneWidth, minRightPaneWidth, maxRightPaneWidth)
		a.workspaceOpen = layout.WorkspaceOpen
	} else {
		a.leftPaneWidth = 0
		a.rightPaneWidth = 0
		a.workspaceOpen = false
	}
	a.appearanceMode = normalizeAppearanceMode(store.state.Appearance)
}

func (a *appModel) ensureConnectionForm() {
	if a.connectionForm != nil {
		return
	}
	config := a.connectionSaved
	a.connectionForm = newConnectionSettings(config, a.workDir)
	if config == nil {
		a.connectionForm.HasSaved = false
	}
}

func (a *appModel) connectionActions() connectionSettingsActions {
	return connectionSettingsActions{
		Save:         a.saveConnection,
		SaveAndApply: a.saveAndApplyConnection,
		Apply:        a.applyConnection,
		Test:         a.testConnection,
	}
}

func (a *appModel) saveConnection(config connectionConfig) {
	a.saveConnectionWithApply(config, false)
}

func (a *appModel) saveAndApplyConnection(config connectionConfig) {
	a.saveConnectionWithApply(config, true)
}

func (a *appModel) saveConnectionWithApply(config connectionConfig, applyAfterSave bool) {
	a.ensureConnectionForm()
	form := a.connectionForm
	if form.Busy || a.shutdownStarted {
		return
	}
	path := a.connectionPath
	if path == "" {
		path = connectionConfigPath(a.storePath)
		a.connectionPath = path
	}
	needStore := a.store == nil
	storePath := a.storePath
	form.Busy = true
	form.Error = ""
	form.ErrorDetail = ""
	form.ReloadBlocked = ""
	form.Status = connStatusSaving
	a.connectionSaveInFlight = true
	go func() {
		err := saveConnectionConfig(path, config)
		if err != nil {
			a.updateUI(func() {
				a.connectionSaveInFlight = false
				form.Busy = false
				form.Status = ""
				form.ErrorDetail = a.connectionSafeMessage(err)
				form.Error = form.ErrorDetail
				if a.shutdownStarted {
					a.finishShutdown()
				}
			})
			return
		}
		var store *contactStore
		if needStore {
			store, err = openContactStore(storePath, config.WorkingDirectory)
		}
		a.updateUI(func() {
			a.connectionSaveInFlight = false
			form.Busy = false
			a.connectionSaved = &config
			a.connectionForm.HasSaved = true
			a.connectionForm.initial = a.connectionForm.Draft
			a.connectionForm.Status = ""
			if a.connectionApplied() && *a.connectionEffective == config {
				a.connectionForm.Status = connStatusSavedApplied
			}
			a.connectionForm.Error = ""
			a.connectionForm.ErrorDetail = ""
			if err != nil {
				a.connectionForm.Error = connStatusSavedStoreFailed
				a.connectionForm.ErrorDetail = a.redactConnectionText(err.Error())
			} else if store != nil {
				a.workDir = config.WorkingDirectory
				a.startupDetailsOpen = false
				applyStoreSelection(a, store)
			}
			if applyAfterSave && !a.shutdownStarted {
				if reason := a.connectionReloadBlocked(); reason != "" {
					form.ReloadBlocked = reason
				} else {
					a.applyConnection()
				}
			}
			if a.shutdownStarted {
				a.finishShutdown()
			}
		})
	}()
}

func runtimeHasExited(rt *runtimeClient) bool {
	if rt == nil {
		return true
	}
	select {
	case <-rt.done:
		return true
	default:
		return false
	}
}

func (a *appModel) connectionReloadBlocked() string {
	if a.modelOpActive {
		return connBlockedModelOp
	}
	if a.connectionApplying {
		return connBlockedApplying
	}
	if a.shutdownStarted {
		return connBlockedShutdown
	}
	if a.connectionSaved == nil {
		return connBlockedNoSaved
	}
	if a.store == nil {
		return connBlockedStore
	}
	if runtimeHasExited(a.rt) {
		return ""
	}
	if a.activeTaskCount() > 0 {
		return connBlockedActiveWork
	}
	for _, ct := range a.contacts {
		for _, issue := range ct.Issues {
			if reason := issueRunBlocked(issue); reason != "" {
				return connBlockedUntrustedIssue
			}
		}
	}
	return ""
}

func (a *appModel) applyConnection() {
	a.ensureConnectionForm()
	form := a.connectionForm
	if form.Busy || a.shutdownStarted {
		return
	}
	if reason := a.connectionReloadBlocked(); reason != "" {
		form.ReloadBlocked = reason
		form.Error = ""
		form.ErrorDetail = ""
		form.Busy = false
		return
	}
	form.ReloadBlocked = ""
	form.Error = ""
	form.ErrorDetail = ""
	form.Busy = true
	form.Status = connStatusChecking
	a.connectionApplying = true
	oldRT := a.rt
	oldLaunch := a.connectionLaunch
	path := a.connectionPath
	sessionDir := a.sessionDir
	gen := a.connectionGeneration + 1
	go a.applyConnectionWorker(gen, path, sessionDir, oldRT, oldLaunch, a.issueToolLaunchSource())
}

func (a *appModel) applyConnectionWorker(gen int, path, sessionDir string, oldRT *runtimeClient, oldLaunch *connectionLaunch, toolSource *runtimeToolSource) {
	fail := func(kind, detail string) {
		a.updateUI(func() {
			a.connectionApplying = false
			a.connectionForm.Busy = false
			a.connectionForm.Status = ""
			a.connectionForm.Error = kind
			a.connectionForm.ErrorDetail = a.redactConnectionText(detail)
			if a.shutdownStarted {
				a.finishShutdown()
			}
		})
	}
	config, err := loadConnectionConfig(path)
	if err != nil {
		fail(connErrorApplyConfig, err.Error())
		return
	}
	binary := findRuntimeBinary()
	a.updateUI(func() {
		if a.rt == nil {
			a.runtimeHelperChecked = true
			a.runtimeBinary = binary
		}
	})
	if binary == "" {
		fail(connErrorHelperMissing, "")
		return
	}
	launch, err := prepareConnectionLaunchWithTools(config, os.Environ(), sessionDir, os.TempDir(), toolSource)
	if err != nil {
		fail(connErrorPrepare, err.Error())
		return
	}
	ready := make(chan runtimeReady, 1)
	rt, err := startRuntime(runtimeOptions{
		Binary: binary, Dir: launch.Dir, Env: launch.Env,
		Handlers: a.runtimeHandlers(gen, ready),
	})
	if err != nil {
		launch.Cleanup()
		fail(connErrorStart, err.Error())
		return
	}
	var readyValue runtimeReady
	select {
	case readyValue = <-ready:
	case <-rt.done:
		launch.Cleanup()
		fail(connErrorExitedBeforeReady, "")
		return
	case <-time.After(runtimeControlTimeout):
		_ = rt.shutdown()
		launch.Cleanup()
		fail(connErrorNotReady, "")
		return
	}
	connections, err := rt.connections()
	if err != nil || len(connections) != 1 || connections[0].ID != connectionID {
		_ = rt.shutdown()
		launch.Cleanup()
		fail(connErrorNotExposed, "")
		return
	}
	select {
	case <-rt.done:
		launch.Cleanup()
		fail(connErrorExitedWhileChecking, "")
		return
	default:
	}
	installed := false
	a.runOnUI(func() {
		if runtimeHasExited(rt) {
			return
		}
		installed = true
		a.connectionGeneration = gen
		a.modelRecovery = nil
		a.modelForm = nil
		a.conversationModelForms = nil
		a.modelPanelKind = ""
		a.modelPanelSessionID = ""
		a.modelPanelLabel = ""
		a.settingsReturnModel = nil
		a.modelOpenToken++
		a.rt = rt
		a.connectionLaunch = launch
		a.connectionEffective = &config
		a.connectionManaged = true
		a.connectionLegacy = false
		a.workDir = config.WorkingDirectory
		a.runtimeBinary = binary
		a.runtimeStatus = runtimeStateReady
		a.runtimeAPI = readyValue.API
		a.runtimeHandshake = true
		a.runtimeErr = ""
		a.runtimeErrKind = ""
		a.runtimeStage = ""
		a.runtimeHint = ""
		a.runtimeHintKind = ""
		a.runtimeHintArgs = nil
		a.connectionForm.Error = ""
		a.connectionForm.ReloadBlocked = ""
		a.runtimeCapabilitiesLoaded = false
		a.runtimeMaxActiveTasks = 0
		a.runtimeCapabilitiesErr = ""
		a.runtimeBuiltinTools = nil
		a.runtimeBuiltinToolsLoaded = false
		a.runtimeBuiltinToolsLoading = false
		a.runtimeBuiltinToolsErr = ""
	})
	if !installed {
		launch.Cleanup()
		fail(connErrorExitedDuringInstall, "")
		return
	}
	if oldRT != nil {
		_ = oldRT.shutdown()
	}
	if oldLaunch != nil {
		_ = oldLaunch.Cleanup()
	}
	a.runOnUI(func() {
		a.connectionApplying = false
		a.connectionForm.Busy = false
		if a.runtimeStatus != runtimeStateReady {
			a.connectionForm.Error = connErrorApplyConfig
			a.connectionForm.ErrorDetail = a.runtimeErr
			a.connectionForm.Status = ""
		} else {
			a.connectionForm.Status = connStatusApplied
		}
		if !a.shutdownStarted && a.runtimeStatus == runtimeStateReady {
			a.restoreContacts(rt, gen)
			a.restoreIssueWorks(rt, gen)
			a.startCapabilitiesLoad(rt, gen)
			if a.connectionManaged {
				a.startBuiltinToolsLoad(rt, gen)
			}
		}
		if a.shutdownStarted {
			a.finishShutdown()
		}
	})
}

func (a *appModel) runOnUI(fn func()) {
	done := make(chan struct{})
	a.updateUI(func() {
		fn()
		close(done)
	})
	<-done
}

func (a *appModel) testConnection(config connectionConfig) {
	a.ensureConnectionForm()
	form := a.connectionForm
	if form.Busy || a.shutdownStarted {
		return
	}
	binary := findRuntimeBinary()
	if a.rt == nil {
		a.runtimeHelperChecked = true
		a.runtimeBinary = binary
	}
	if binary == "" {
		form.Error = connErrorHelperMissing
		form.ErrorDetail = ""
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	a.connectionTesting = true
	a.connectionTestCancel = cancel
	form.Busy = true
	form.Error = ""
	form.ErrorDetail = ""
	form.Status = connStatusTesting
	go func() {
		result, err := probeConnection(ctx, binary, config, os.Environ(), os.TempDir())
		cancel()
		a.updateUI(func() {
			a.connectionTesting = false
			a.connectionTestCancel = nil
			form.Busy = false
			if a.shutdownStarted {
				a.finishShutdown()
				return
			}
			if err != nil {
				form.Status = ""
				form.Error = connErrorTestFailed
				form.ErrorDetail = a.connectionSafeMessage(err)
				return
			}
			form.Status = connStatusTestCompleted
			form.StatusArgs = []string{result.Config.EffectiveConfig.Model, result.Connection.Endpoint,
				fmt.Sprintf("%d", result.Config.EffectiveConfig.Compaction.ContextTokens),
				fmt.Sprintf("%d", result.Config.EffectiveConfig.Compaction.OutputTokens)}
			form.Error = ""
		})
	}()
}

func (a *appModel) runtimeHandlers(gen int, ready chan<- runtimeReady) runtimeHandlers {
	return runtimeHandlers{
		OnReady: func(value runtimeReady) {
			if ready != nil {
				select {
				case ready <- value:
				default:
				}
			}
			a.updateUI(func() { a.handleRuntimeReady(gen, value) })
		},
		OnEvent: func(event runtimeEventEnvelope) {
			a.updateUI(func() { a.handleRuntimeEvent(gen, event) })
		},
		OnExit: func(err error) {
			a.updateUI(func() { a.handleRuntimeExit(gen, err) })
		},
		OnProtocolError: func(message string) {
			a.updateUI(func() { a.handleRuntimeProtocolError(gen, message) })
		},
	}
}

func (a *appModel) handleRuntimeReady(gen int, ready runtimeReady) {
	if gen != a.connectionGeneration || a.rt == nil || a.shutdownStarted {
		return
	}
	a.runtimeStatus = runtimeStateReady
	a.runtimeErr = ""
	a.runtimeErrKind = ""
	a.runtimeStage = ""
	a.runtimeHint = ""
	a.runtimeHintKind = ""
	a.runtimeHintArgs = nil
	a.runtimeAPI = ready.API
	a.runtimeHandshake = true
	a.restoreContacts(a.rt, gen)
	a.restoreIssueWorks(a.rt, gen)
	a.startCapabilitiesLoad(a.rt, gen)
	if a.connectionManaged {
		a.startBuiltinToolsLoad(a.rt, gen)
	}
}

func (a *appModel) handleRuntimeEvent(gen int, event runtimeEventEnvelope) {
	if gen != a.connectionGeneration {
		return
	}
	a.applyRuntimeEvent(event)
}

func (a *appModel) handleRuntimeExit(gen int, err error) {
	if gen != a.connectionGeneration {
		return
	}
	a.applyRuntimeExit(err)
}

func (a *appModel) handleRuntimeProtocolError(gen int, message string) {
	if gen != a.connectionGeneration {
		return
	}
	a.protocolFailed(message)
}

func (a *appModel) connectionSafeMessage(err error) string {
	if err == nil {
		return ""
	}
	return a.redactConnectionText(err.Error())
}

func (a *appModel) redactConnectionText(text string) string {
	keys := make([]string, 0, 4)
	for _, config := range []*connectionConfig{a.connectionSaved, a.connectionEffective} {
		if config != nil && config.APIKey != "" {
			keys = append(keys, config.APIKey)
		}
	}
	if a.connectionForm != nil && a.connectionForm.Draft.APIKey != "" {
		keys = append(keys, a.connectionForm.Draft.APIKey)
	}
	for _, key := range keys {
		text = strings.ReplaceAll(text, key, "[redacted]")
	}
	return safeDiagnostic(text)
}

func (a *appModel) connectionRuntimeMessage(err error) string {
	return a.redactConnectionText(runtimeMessage(err))
}

func (a *appModel) connectionTaskFinishedDetail(payload *runtimeEvent) string {
	if payload == nil {
		return ""
	}
	copyPayload := *payload
	if payload.Error != nil {
		errorCopy := *payload.Error
		errorCopy.Message = a.redactConnectionText(payload.Error.Message)
		errorCopy.Reason = a.redactConnectionText(payload.Error.Reason)
		copyPayload.Error = &errorCopy
	}
	return taskFinishedDetail(&copyPayload)
}

func (a *appModel) configuredConversationCreate(rt *runtimeClient, configured bool, workspace, system string, builtin *[]string, issueTools bool) (runtimeSession, error) {
	if configured {
		selection, err := runtimeConversationSelection(rt, builtin, issueTools)
		if err != nil {
			return runtimeSession{}, err
		}
		return rt.createConfiguredConversationSession(workspace, system, selection)
	}
	if builtin != nil {
		return runtimeSession{}, errors.New("Built-in tool selection requires a managed Nagi connection; save and Apply a connection before creating a new conversation.")
	}
	return rt.createSession(workspace, system)
}

func (a *appModel) configuredWorkCreate(rt *runtimeClient, configured bool, workspace, system string, builtin *[]string, issueTools bool) (runtimeSession, error) {
	if configured {
		selection, err := runtimeWorkSelection(rt, builtin, issueTools)
		if err != nil {
			return runtimeSession{}, err
		}
		return rt.createConfiguredConversationSession(workspace, system, selection)
	}
	if builtin != nil {
		return runtimeSession{}, errors.New("Built-in tool selection requires a managed Nagi connection; save and Apply a connection before creating a new work session.")
	}
	return rt.createSession(workspace, system)
}

func (c *runtimeClient) createConfiguredSession(workingDir, systemPrompt string) (runtimeSession, error) {
	return c.createConfiguredSessionWithTools(workingDir, systemPrompt, nil)
}

func (c *runtimeClient) createConfiguredConversationSession(workingDir, systemPrompt string, toolSelection map[string]any) (runtimeSession, error) {
	return c.createConfiguredSessionWithTools(workingDir, systemPrompt, toolSelection)
}

func (c *runtimeClient) createConfiguredSessionWithTools(workingDir, systemPrompt string, toolSelection map[string]any) (runtimeSession, error) {
	config := map[string]any{
		"connection_id": connectionID,
	}
	if workingDir != "" {
		config["working_directory"] = workingDir
	}
	if systemPrompt != "" {
		config["system_prompt"] = systemPrompt
	}
	if toolSelection != nil {
		config["tool_selection"] = toolSelection
	}
	var session runtimeSession
	err := c.call("session.create", map[string]any{
		"storage_schema": 2,
		"config":         config,
	}, &session, runtimeControlTimeout)
	if err != nil {
		return runtimeSession{}, err
	}
	if session.SessionID == "" {
		return runtimeSession{}, errors.New("runtime returned a session without session_id")
	}
	return session, nil
}

// finishShutdown drains Runtime only after in-flight history, metadata,
// issue/conversation submission, and result reads have delivered their final
// UI callbacks; those callbacks re-enter this function.
func (a *appModel) finishShutdown() {
	if a.quitReady || a.shutdownDrainStarted || a.connectionSaveInFlight || a.connectionApplying || a.connectionTesting || a.modelOpActive || a.contactHistoryInFlight > 0 || a.resultInFlight > 0 || a.submissionInFlight > 0 || a.issueToolReleaseInFlight > 0 || a.metadataInFlight > 0 {
		return
	}
	if a.rt == nil {
		if a.issueToolServer == nil {
			a.quitReady = true
			if a.requestQuit != nil {
				a.requestQuit()
			}
			return
		}
		a.shutdownDrainStarted = true
		go func() {
			err := a.closeIssueToolService()
			a.updateUI(func() {
				a.quitReady = true
				if err != nil {
					a.runtimeErrKind = runtimeNoticeIssueToolShutdown
					a.runtimeErr = a.connectionSafeMessage(err)
				}
				if a.requestQuit != nil {
					a.requestQuit()
				}
			})
		}()
		return
	}
	a.shutdownDrainStarted = true
	a.runtimeStatus = runtimeStateDraining
	a.runtimeErr = ""
	a.runtimeErrKind = ""
	rt := a.rt
	launch := a.connectionLaunch
	go func() {
		err := rt.shutdown()
		if launch != nil {
			_ = launch.Cleanup()
		}
		toolErr := a.closeIssueToolService()
		a.updateUI(func() {
			a.quitReady = true
			if err != nil {
				a.runtimeErrKind = runtimeNoticeRuntimeShutdown
				a.runtimeErr = a.connectionSafeMessage(err)
			}
			if toolErr != nil {
				a.runtimeErrKind = runtimeNoticeIssueToolShutdown
				a.runtimeErr = a.connectionSafeMessage(toolErr)
			}
			if a.requestQuit != nil {
				a.requestQuit()
			}
		})
	}()
}
