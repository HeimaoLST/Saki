package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
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
			Workspace: stored.Workspace, SessionID: stored.SessionID, Draft: stored.Draft,
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

func connectionSummary(config *connectionConfig) string {
	if config == nil {
		return ""
	}
	return fmt.Sprintf("%s · %s · cwd %s · context %d / output %d · %d slots",
		config.Model, config.API, config.WorkingDirectory, config.ContextTokens, config.OutputTokens, config.MaxActiveTasks)
}

func (a *appModel) ensureConnectionForm() {
	if a.connectionForm != nil {
		return
	}
	config := a.connectionSaved
	a.connectionForm = newConnectionSettings(config, a.workDir)
	if config == nil {
		a.connectionForm.HasSaved = false
		if a.connectionLegacy {
			a.connectionForm.Source = "Environment configuration"
		}
	}
}

func (a *appModel) connectionActions() connectionSettingsActions {
	return connectionSettingsActions{
		Save:  a.saveConnection,
		Apply: a.applyConnection,
		Test:  a.testConnection,
		Close: a.closeAux,
	}
}

func (a *appModel) saveConnection(config connectionConfig) {
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
	form.Status = "Saving configuration…"
	a.connectionSaveInFlight = true
	go func() {
		err := saveConnectionConfig(path, config)
		if err != nil {
			a.updateUI(func() {
				a.connectionSaveInFlight = false
				form.Busy = false
				form.Status = ""
				form.Error = "Could not save configuration. Your changes are still here."
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
			a.connectionForm.Source = "Nagi connection settings"
			a.connectionForm.SavedSummary = connectionSummary(&config)
			a.connectionForm.Status = "Saved. Apply saved to use this connection."
			a.connectionForm.Error = ""
			if err != nil {
				a.connectionForm.Error = "Configuration was saved, but the local contact store could not be created: " + a.redactConnectionText(err.Error())
			} else if store != nil {
				a.workDir = config.WorkingDirectory
				a.startupDetailsOpen = false
				applyStoreSelection(a, store)
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
		return "Wait for the in-flight session model operation to finish before applying a new connection."
	}
	if a.connectionApplying {
		return "A new connection is already being applied."
	}
	if a.shutdownStarted {
		return "The app is shutting down."
	}
	if a.connectionSaved == nil {
		return "Save a valid connection configuration first."
	}
	if a.store == nil {
		return "The local contact store is unavailable. Fix its file or permissions, then save again."
	}
	if runtimeHasExited(a.rt) {
		return ""
	}
	if a.activeTaskCount() > 0 {
		return "Wait for all conversation and issue work to finish before applying a new connection."
	}
	for _, ct := range a.contacts {
		for _, issue := range ct.Issues {
			if reason := issueRunBlocked(issue); reason != "" {
				return "An issue execution has no trusted terminal state; reload or reconcile it before applying a new connection."
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
		form.Busy = false
		return
	}
	form.ReloadBlocked = ""
	form.Error = ""
	form.Busy = true
	form.Status = "Checking the saved connection…"
	a.connectionApplying = true
	oldRT := a.rt
	oldLaunch := a.connectionLaunch
	path := a.connectionPath
	sessionDir := a.sessionDir
	helperHint := a.runtimeHelperHint()
	gen := a.connectionGeneration + 1
	go a.applyConnectionWorker(gen, path, sessionDir, helperHint, oldRT, oldLaunch, a.issueToolLaunchSource())
}

func (a *appModel) applyConnectionWorker(gen int, path, sessionDir, helperHint string, oldRT *runtimeClient, oldLaunch *connectionLaunch, toolSource *runtimeToolSource) {
	fail := func(message string) {
		a.updateUI(func() {
			a.connectionApplying = false
			a.connectionForm.Busy = false
			a.connectionForm.Status = ""
			a.connectionForm.Error = a.redactConnectionText(message)
			if a.shutdownStarted {
				a.finishShutdown()
			}
		})
	}
	config, err := loadConnectionConfig(path)
	if err != nil {
		fail("Could not apply saved configuration: " + err.Error())
		return
	}
	binary := findRuntimeBinary()
	if binary == "" {
		fail("Runtime helper not found. " + helperHint)
		return
	}
	launch, err := prepareConnectionLaunchWithTools(config, os.Environ(), sessionDir, os.TempDir(), toolSource)
	if err != nil {
		fail("Could not prepare the saved connection: " + err.Error())
		return
	}
	ready := make(chan runtimeReady, 1)
	rt, err := startRuntime(runtimeOptions{
		Binary: binary, Dir: launch.Dir, Env: launch.Env,
		Handlers: a.runtimeHandlers(gen, ready),
	})
	if err != nil {
		launch.Cleanup()
		fail("Could not start the new runtime: " + err.Error())
		return
	}
	var readyValue runtimeReady
	select {
	case readyValue = <-ready:
	case <-rt.done:
		launch.Cleanup()
		fail("The new runtime exited before it was ready; the previous connection was kept.")
		return
	case <-time.After(runtimeControlTimeout):
		_ = rt.shutdown()
		launch.Cleanup()
		fail("The new runtime did not become ready; the previous connection was kept.")
		return
	}
	connections, err := rt.connections()
	if err != nil || len(connections) != 1 || connections[0].ID != connectionID {
		_ = rt.shutdown()
		launch.Cleanup()
		fail("The new runtime did not expose the saved connection; the previous connection was kept.")
		return
	}
	select {
	case <-rt.done:
		launch.Cleanup()
		fail("The new runtime exited while validating the saved connection; the previous connection was kept.")
		return
	default:
	}
	capabilities, _ := rt.capabilities()
	observed, observedErr := probeRuntimeConfig(binary, config, os.Environ(), os.TempDir())
	effectiveSummary := observedConnectionSummary(connections[0], observed, capabilities.MaxActiveTasks)
	if observedErr != nil {
		effectiveSummary = connections[0].DefaultModel + " · " + connections[0].Endpoint
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
		a.runtimeStage = ""
		a.runtimeHint = ""
		a.connectionForm.Error = ""
		a.connectionForm.ReloadBlocked = ""
		a.connectionForm.SavedSummary = connectionSummary(&config)
		a.connectionForm.EffectiveSummary = effectiveSummary
		a.connectionForm.Source = "Nagi connection settings"
		a.runtimeCapabilitiesLoaded = false
		a.runtimeMaxActiveTasks = 0
		a.runtimeCapabilitiesErr = ""
	})
	if !installed {
		launch.Cleanup()
		fail("The new runtime exited during validation; the previous connection was kept.")
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
			a.connectionForm.Error = a.runtimeErr
			a.connectionForm.Status = ""
		} else {
			a.connectionForm.Status = "Applied. New work uses this connection."
			if observedErr != nil {
				a.connectionForm.Status += " Effective details could not be fully read back."
			}
		}
		if !a.shutdownStarted && a.runtimeStatus == runtimeStateReady {
			a.restoreContacts(rt, gen)
			a.restoreIssueWorks(rt, gen)
			go a.loadCapabilities(rt, gen)
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
	if binary == "" {
		form.Error = "Runtime helper not found. " + a.runtimeHelperHint()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	a.connectionTesting = true
	a.connectionTestCancel = cancel
	form.Busy = true
	form.Error = ""
	form.Status = "Testing connection and model…"
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
				form.Error = "Connection test failed: " + a.connectionSafeMessage(err)
				return
			}
			form.Status = fmt.Sprintf("Connection test completed: %s · %s · context %d / output %d",
				result.Config.EffectiveConfig.Model, result.Connection.Endpoint,
				result.Config.EffectiveConfig.Compaction.ContextTokens,
				result.Config.EffectiveConfig.Compaction.OutputTokens)
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
	a.runtimeStage = ""
	a.runtimeHint = ""
	a.runtimeAPI = ready.API
	a.runtimeHandshake = true
	if a.connectionForm != nil && a.connectionEffective != nil {
		a.connectionForm.Source = "Nagi connection settings"
	}
	a.restoreContacts(a.rt, gen)
	a.restoreIssueWorks(a.rt, gen)
	go a.loadCapabilities(a.rt, gen)
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

func (a *appModel) configuredCreate(rt *runtimeClient, configured bool, workspace, system string) (runtimeSession, error) {
	if configured {
		return rt.createConfiguredSession(workspace, system)
	}
	return rt.createSession(workspace, system)
}

func (a *appModel) configuredConversationCreate(rt *runtimeClient, configured bool, workspace, system string) (runtimeSession, error) {
	if configured {
		return rt.createConfiguredConversationSession(workspace, system, a.issueToolConversationSelection())
	}
	return rt.createSession(workspace, system)
}

func (a *appModel) configuredWorkCreate(rt *runtimeClient, configured bool, workspace, system string) (runtimeSession, error) {
	if configured {
		return rt.createConfiguredConversationSession(workspace, system, a.issueToolWorkSelection())
	}
	return rt.createSession(workspace, system)
}

func observedConnectionSummary(connection runtimeConnection, config runtimeConnectionConfig, maxActiveTasks int) string {
	effective := config.EffectiveConfig
	summary := fmt.Sprintf("%s · %s · cwd %s · context %d / output %d",
		effective.Model, connection.Endpoint, effective.WorkingDirectory,
		effective.Compaction.ContextTokens, effective.Compaction.OutputTokens)
	if maxActiveTasks > 0 {
		summary += fmt.Sprintf(" · %d slots", maxActiveTasks)
	}
	return summary
}

func probeRuntimeConfig(binary string, config connectionConfig, parentEnv []string, tempRoot string) (runtimeConnectionConfig, error) {
	root, err := os.MkdirTemp(tempRoot, "nagi-effective-")
	if err != nil {
		return runtimeConnectionConfig{}, err
	}
	defer os.RemoveAll(root)
	launch, err := prepareConnectionLaunch(config, parentEnv, filepath.Join(root, "sessions"), root)
	if err != nil {
		return runtimeConnectionConfig{}, err
	}
	defer launch.Cleanup()
	ready := make(chan struct{}, 1)
	rt, err := startRuntime(runtimeOptions{
		Binary: binary, Dir: launch.Dir, Env: launch.Env,
		Handlers: runtimeHandlers{OnReady: func(runtimeReady) {
			select {
			case ready <- struct{}{}:
			default:
			}
		}},
	})
	if err != nil {
		return runtimeConnectionConfig{}, err
	}
	defer rt.shutdown()
	select {
	case <-ready:
	case <-rt.done:
		return runtimeConnectionConfig{}, errors.New("effective configuration helper exited before ready")
	case <-time.After(runtimeControlTimeout):
		return runtimeConnectionConfig{}, errors.New("effective configuration helper did not become ready")
	}
	session, err := rt.createConfiguredSession(config.WorkingDirectory, "")
	if err != nil {
		return runtimeConnectionConfig{}, err
	}
	return rt.connectionConfig(session.SessionID)
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

func (a *appModel) connectionPanel(c *ui.Context, t *ui.Theme) {
	if a.connectionForm == nil {
		return
	}
	a.connectionForm.view(c, t, a.connectionActions())
}

// finishShutdown drains Runtime only after in-flight history reads have
// delivered their final UI callback; those callbacks re-enter this function.
func (a *appModel) finishShutdown() {
	if a.quitReady || a.shutdownDrainStarted || a.connectionSaveInFlight || a.connectionApplying || a.connectionTesting || a.modelOpActive || a.contactHistoryInFlight > 0 || a.resultInFlight > 0 || a.issueToolReleaseInFlight > 0 {
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
					a.runtimeErr = "Issue tool service shutdown: " + a.connectionSafeMessage(err)
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
				a.runtimeErr = "Runtime shutdown: " + a.connectionSafeMessage(err)
			}
			if toolErr != nil {
				a.runtimeErr = "Issue tool service shutdown: " + a.connectionSafeMessage(toolErr)
			}
			if a.requestQuit != nil {
				a.requestQuit()
			}
		})
	}()
}
