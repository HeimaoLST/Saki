package main

// Runtime-backed app wiring: configuration, startup/restore, the real send
// path, event application and history rendering. All UI state changes go
// through win.Update; I/O and JSON decoding stay off the UI thread.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	runtimeStateStarting = "starting"
	runtimeStateReady    = "ready"
	runtimeStateFailed   = "failed"
	runtimeStateExited   = "exited"
	runtimeStateDraining = "draining"

	startupStageLocalStore = "Local contact store"
)

func newRuntimeApp() (*appModel, error) {
	storePath := guiStorePath()
	defaultWorkDir := guiWorkDir()
	sessionDir := guiSessionDir(storePath)
	startup := readConnectionStartup(storePath, defaultWorkDir)
	workDir := defaultWorkDir
	formWorkDir := defaultWorkDir
	if startup.Config != nil {
		workDir = startup.Config.WorkingDirectory
		formWorkDir = workDir
	} else if startup.FormConfig != nil && startup.FormConfig.WorkingDirectory != "" {
		formWorkDir = startup.FormConfig.WorkingDirectory
	}

	_, statErr := os.Stat(storePath)
	storeExists := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	var store *contactStore
	var err error
	switch {
	case storeExists:
		store, err = openContactStore(storePath, workDir)
	case startup.Config != nil:
		store, err = openContactStore(storePath, startup.Config.WorkingDirectory)
	case startup.Missing && startup.External:
		store, err = openContactStore(storePath, workDir)
	}
	if err != nil {
		return nil, err
	}

	a := &appModel{
		store:               store,
		storePath:           storePath,
		sessionDir:          sessionDir,
		workDir:             workDir,
		runtimeStatus:       runtimeStateStarting,
		workspaceOpen:       false,
		scrollTo:            -1,
		connectionPath:      startup.Path,
		connectionSaved:     startup.Config,
		connectionEffective: startup.Config,
		connectionManaged:   startup.Config != nil,
		connectionLegacy:    startup.Missing && startup.External && startup.Config == nil,
	}
	if store != nil {
		applyStoreSelection(a, store)
		a.loadStoredTaskContexts(store)
	} else {
		a.contacts = []*contact{placeholderContact(workDir)}
		a.selected = 0
	}
	a.bindTaskContexts()
	a.normalizeRestoredSupplementBatches()
	if a.issueToolLedger == nil {
		a.issueToolLedger = &IssueToolOperationLedger{}
	}
	if a.issueToolScopes == nil {
		a.issueToolScopes = map[string]IssueToolScope{}
	}
	if a.issueToolWaiters == nil {
		a.issueToolWaiters = map[string]chan IssueToolReceipt{}
	}
	if a.issueToolShutdown == nil {
		a.issueToolShutdown = make(chan struct{})
	}
	if a.issueToolRefresh == nil {
		a.issueToolRefresh = map[string]issueToolRefreshMark{}
	}
	if err := a.startIssueToolService(); err != nil {
		a.issueToolErr = err.Error()
	}
	formConfig := startup.Config
	if formConfig == nil {
		formConfig = startup.FormConfig
	}
	a.connectionForm = newConnectionSettings(formConfig, formWorkDir)
	if startup.Config == nil {
		a.connectionForm.HasSaved = false
		switch {
		case startup.LoadErr != nil:
			a.connectionForm.Source = "Saved connection settings need repair"
			a.connectionForm.Error = "Saved connection settings need repair. Check the values and save again."
			a.runtimeStatus = runtimeStateFailed
			a.runtimeStage = "Connection configuration"
			a.runtimeErr = "Saved connection settings need repair."
			a.runtimeHint = "Fix the saved values in Connection settings, then save and apply."
		case startup.External:
			a.connectionForm.Source = "Environment configuration"
		default:
			a.connectionForm.Source = "Not configured in Nagi"
			a.runtimeStatus = runtimeStateFailed
			a.runtimeStage = "Connection configuration"
			a.runtimeErr = "Connection settings are required before the runtime can start."
			a.runtimeHint = "Enter a connection, model and working directory, then save and apply."
		}
	}
	if startup.Config != nil {
		a.connectionForm.Source = "Nagi connection settings"
		a.connectionForm.SavedSummary = connectionSummary(startup.Config)
	}
	if startup.LoadErr != nil || (startup.Config == nil && !startup.External) {
		a.openSettingsTab(settingsTabConnection, settingsSourceConnection)
	}
	// The first launch can legitimately have no store file yet. Appearance is
	// the first GUI-global preference, so allow a lazy save through persist;
	// read/stat failures still use newUnavailableApp and never overwrite data.
	if a.store == nil && a.storePath != "" {
		a.storeInitAllowed = true
	}
	return a, nil
}

// newUnavailableApp keeps the window usable when the local contact store cannot
// be read or created. It retains the actual paths, labels the local store stage,
// keeps store=nil, and never silently creates a replacement store.
func newUnavailableApp(err error) *appModel {
	storePath := guiStorePath()
	workDir := guiWorkDir()
	a := &appModel{
		runtimeStatus: runtimeStateFailed,
		runtimeStage:  startupStageLocalStore,
		runtimeErr:    err.Error(),
		runtimeHint: "Saki left the existing file untouched and did not create a replacement store. " +
			"Fix the file or its permissions at " + storePath + ", then restart Saki. " +
			"Drafts cannot be persisted while the local store is unavailable.",
		storePath:     storePath,
		sessionDir:    guiSessionDir(storePath),
		workDir:       workDir,
		workspaceOpen: false,
		scrollTo:      -1,
		contacts: []*contact{{
			ID:        newContactID(),
			Name:      "Saki",
			Role:      "Assistant",
			Status:    statusAttention,
			WorkLabel: "Preparing a reply",
		}},
	}
	a.contacts[0].Unavailable = err.Error()
	return a
}

func guiStorePath() string {
	if value := os.Getenv("SAKI_GUI_STORE"); value != "" {
		return value
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "Saki", "contacts.json")
}

func guiSessionDir(storePath string) string {
	if value := os.Getenv("SAKI_SESSION_DIR"); value != "" {
		return value
	}
	return filepath.Join(filepath.Dir(storePath), "sessions")
}

func guiWorkDir() string {
	if value := os.Getenv("SAKI_GUI_WORKDIR"); value != "" {
		return value
	}
	if wd, err := os.Getwd(); err == nil && wd != "" && wd != "/" {
		return wd
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "."
}

func runtimeHelperCandidates() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	dir := filepath.Dir(exe)
	return []string{
		filepath.Join(dir, "Saki-runtime"),
		filepath.Join(dir, "saki-runtime"),
		filepath.Join(dir, "..", "Resources", "Saki-runtime"),
		filepath.Join(dir, "..", "Resources", "Saki"),
	}
}

func findRuntimeBinary() string {
	if value := os.Getenv("SAKI_RUNTIME_BIN"); value != "" {
		return value
	}
	for _, candidate := range runtimeHelperCandidates() {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	if path, err := exec.LookPath("Saki"); err == nil {
		return path
	}
	return ""
}

func (a *appModel) runtimeHelperHint() string {
	parts := []string{"Place Saki-runtime next to the Saki executable"}
	if candidates := runtimeHelperCandidates(); len(candidates) > 0 {
		parts = append(parts, "(expected: "+strings.Join(candidates, ", ")+")")
	}
	parts = append(parts, "or set SAKI_RUNTIME_BIN, then restart Saki.")
	return strings.Join(parts, " ")
}

var diagnosticSecret = regexp.MustCompile(`(?i)\b(api[_-]?key|token)\s*[:=]\s*[^\s,;]+|\bauthorization\s*:\s*(?:bearer\s+)?[^\s,;]+|\bbearer\s+[^\s,;]+`)
var diagnosticJSONSecret = regexp.MustCompile(`(?i)"(?:api[_-]?key|token|secret|password|authorization)"\s*:\s*"[^"]*"`)
var diagnosticSkKey = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`)
var diagnosticURLCred = regexp.MustCompile(`://[^/\s@]+@`)

func safeDiagnostic(text string) string {
	text = strings.TrimSpace(text)
	text = diagnosticSecret.ReplaceAllString(text, "[redacted]")
	text = diagnosticJSONSecret.ReplaceAllString(text, "[redacted]")
	text = diagnosticSkKey.ReplaceAllString(text, "[redacted]")
	text = diagnosticURLCred.ReplaceAllString(text, "://[redacted]@")
	return truncate(text, 500)
}

func (a *appModel) childEnv() []string {
	env := os.Environ()
	if os.Getenv("SAKI_SESSION_DIR") == "" {
		env = append(env, "SAKI_SESSION_DIR="+a.sessionDir)
	}
	return env
}

func (a *appModel) startRuntime() {
	if a.demo || a.store == nil || a.rt != nil || a.connectionApplying {
		return
	}
	if a.connectionEffective == nil && !a.connectionLegacy && a.connectionForm != nil && !a.connectionForm.HasSaved {
		a.runtimeStatus = runtimeStateFailed
		a.runtimeStage = "Connection configuration"
		a.runtimeErr = "Connection settings are required before the runtime can start."
		a.runtimeHint = "Enter a connection, model and working directory, then save and apply."
		return
	}
	binary := findRuntimeBinary()
	a.runtimeHelperChecked = true
	if binary == "" {
		a.runtimeStatus = runtimeStateFailed
		a.runtimeStage = "Runtime helper"
		a.runtimeErr = "Runtime helper not found."
		a.runtimeHint = a.runtimeHelperHint()
		if a.connectionForm != nil {
			a.connectionForm.Error = "Runtime helper not found."
			a.connectionForm.Busy = false
		}
		return
	}
	a.runtimeBinary = binary
	gen := a.connectionGeneration + 1
	a.connectionGeneration = gen

	var rt *runtimeClient
	var err error
	if a.connectionEffective != nil {
		launch, prepareErr := prepareConnectionLaunchWithTools(*a.connectionEffective, os.Environ(), a.sessionDir, os.TempDir(), a.issueToolLaunchSource())
		if prepareErr != nil {
			a.runtimeStatus = runtimeStateFailed
			a.runtimeStage = "Connection configuration"
			a.runtimeErr = "Could not prepare the saved connection: " + a.connectionSafeMessage(prepareErr)
			a.runtimeHint = "Fix Connection settings, then save and apply."
			if a.connectionForm != nil {
				a.connectionForm.Error = a.runtimeErr
				a.connectionForm.Busy = false
			}
			return
		}
		rt, err = startRuntime(runtimeOptions{
			Binary: binary, Dir: launch.Dir, Env: launch.Env,
			Handlers: a.runtimeHandlers(gen, nil),
		})
		if err != nil {
			launch.Cleanup()
		} else {
			a.connectionLaunch = launch
			a.connectionManaged = true
			a.connectionLegacy = false
			a.workDir = a.connectionEffective.WorkingDirectory
		}
	} else if a.connectionLegacy || a.connectionForm == nil {
		rt, err = startRuntime(runtimeOptions{
			Binary: binary, Dir: a.workDir, Env: a.childEnv(),
			Handlers: a.runtimeHandlers(gen, nil),
		})
	} else {
		a.connectionGeneration = gen - 1
		a.runtimeStatus = runtimeStateFailed
		a.runtimeStage = "Connection configuration"
		a.runtimeErr = "Connection settings are required before the runtime can start."
		a.runtimeHint = "Enter a connection, model and working directory, then save and apply."
		return
	}
	if err != nil {
		a.runtimeStatus = runtimeStateFailed
		a.runtimeStage = "Runtime start"
		a.runtimeErr = "Could not start runtime: " + a.connectionSafeMessage(err)
		a.runtimeHint = "Check the helper path and permissions at " + binary + ", then try Apply saved."
		if a.connectionForm != nil {
			a.connectionForm.Error = a.runtimeErr
			a.connectionForm.Busy = false
		}
		return
	}
	a.rt = rt
	a.runtimeStatus = runtimeStateStarting
	a.runtimeHandshake = false
	a.runtimeErr = ""
	a.runtimeStage = ""
	a.runtimeHint = ""
}

func (a *appModel) protocolFailed(message string) {
	a.runtimeStatus = runtimeStateFailed
	a.runtimeStage = "Runtime protocol"
	a.runtimeErr = "Runtime protocol error: " + a.redactConnectionText(message)
	a.runtimeHint = "Restart Saki; delivery or result may be unconfirmed. No automatic retry."
	for _, ct := range a.contacts {
		if ct.SendState == sendSubmitting || ct.ActiveTaskID != "" {
			if w := ct.activeWork(); w != nil {
				w.State = workUnknown
				w.Detail = "Protocol failure; result not confirmed."
			}
			ct.SessionState = "unknown"
			ct.SendState = sendUnknown
			ct.Status = statusAttention
			ct.SendError = "Protocol failure; delivery not confirmed. Reload before sending again."
		}
		for _, issue := range ct.Issues {
			if issue.Work.SendState == sendSubmitting || issue.Work.ActiveTaskID != "" || issue.Work.SendState == sendUnknown {
				if w := workFor(ct, issue.Work.ActiveTaskID); w != nil {
					w.State = workUnknown
					w.Detail = "Protocol failure; result not confirmed."
				}
				issue.Work.SessionState = "unknown"
				issue.Work.SendState = sendUnknown
				issue.Work.SendError = "Protocol failure; delivery not confirmed. Reload before sending again."
				if execution := ct.executionByTask(issue.Work.ActiveTaskID); execution != nil {
					execution.Status = issueExecutionUnknown
					execution.Detail = issue.Work.SendError
				}
				issue.Work.ActiveTaskID = ""
			}
		}
	}
}

func (a *appModel) clearActiveTasksAfterExit() {
	for _, ct := range a.contacts {
		if ct.ActiveTaskID != "" {
			if w := ct.activeWork(); w != nil {
				w.State = workUnknown
				w.Detail = "Connection lost before the task finished; reload to verify."
			}
			ct.ActiveTaskID = ""
			ct.SessionState = "unknown"
			ct.SendState = sendUnknown
			ct.Status = statusAttention
			ct.StatusDetail = "Connection lost before the task finished; reload to verify."
			ct.SendError = "Delivery/result not confirmed. Reload before sending again."
		}
		for _, issue := range ct.Issues {
			if issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle {
				continue
			}
			issue.Work.SessionState = "unknown"
			issue.Work.SendState = sendUnknown
			issue.Work.SendError = "Delivery/result not confirmed. Reload before sending again."
			if w := workFor(ct, issue.Work.ActiveTaskID); w != nil {
				w.State = workUnknown
				w.Detail = "Connection lost before the task finished; reload to verify."
			}
			execution := ct.executionByTask(issue.Work.ActiveTaskID)
			if execution != nil {
				execution.Status = issueExecutionUnknown
				execution.Detail = issue.Work.SendError
			}
			issue.Work.ActiveTaskID = ""
		}
	}
}

func (a *appModel) applyRuntimeExit(err error) {
	if a.runtimeStatus == runtimeStateDraining {
		a.runtimeStatus = runtimeStateExited
		return
	}
	if a.runtimeStage == "Runtime protocol" {
		a.runtimeStatus = runtimeStateExited
		a.clearActiveTasksAfterExit()
		return
	}
	diagnostic := ""
	if a.rt != nil {
		if lines := a.rt.diagnostics(); len(lines) > 0 {
			diagnostic = a.redactConnectionText(lines[len(lines)-1])
		}
	}
	wasReady := a.runtimeHandshake
	a.runtimeStatus = runtimeStateExited
	if wasReady {
		a.runtimeStage = "Runtime connection"
		a.runtimeErr = "Runtime connection lost"
		if err != nil {
			a.runtimeErr += ": " + a.redactConnectionText(err.Error())
		}
		a.runtimeHint = "Restart Saki and reload history. Accepted work may be unconfirmed; no automatic retry."
	} else {
		switch {
		case strings.Contains(diagnostic, "API_KEY is not set"):
			a.runtimeStage = "Runtime configuration"
			a.runtimeErr = "API_KEY is not set."
			a.runtimeHint = "Set API_KEY in the environment or in " + a.workDir + "/.env, then restart Saki."
		case strings.Contains(diagnostic, ".env exists but could not be parsed"):
			a.runtimeStage = "Runtime configuration"
			a.runtimeErr = ".env could not be parsed."
			a.runtimeHint = "Fix or replace the .env file in " + a.workDir + ", then restart Saki."
		default:
			a.runtimeStage = "Runtime startup"
			a.runtimeHint = "Check the helper path and the startup diagnostic, then restart Saki."
			if diagnostic != "" {
				a.runtimeErr = "Runtime exited before ready: " + diagnostic
			} else if err != nil {
				a.runtimeErr = "Runtime exited before ready: " + a.redactConnectionText(err.Error())
			} else {
				a.runtimeErr = "Runtime exited before ready."
			}
		}
	}
	a.clearActiveTasksAfterExit()
}

// beginShutdown is the single quit path for the window close button and
// Cmd+Q. It prevents the default close, drains the runtime, then asks the app
// to quit. The UI thread is never blocked on the process wait.
func (a *appModel) beginShutdown() {
	if a.shutdownStarted {
		return
	}
	a.shutdownStarted = true
	if a.issueToolShutdown != nil {
		close(a.issueToolShutdown)
	}
	a.persist()
	if a.connectionTestCancel != nil {
		a.connectionTestCancel()
		a.connectionTestCancel = nil
	}
	a.finishShutdown()
}

func (a *appModel) setUIHook(hook func(func())) {
	a.uiHookMu.Lock()
	a.uiHook = hook
	a.uiHookMu.Unlock()
}

func (a *appModel) getUIHook() func(func()) {
	a.uiHookMu.Lock()
	defer a.uiHookMu.Unlock()
	return a.uiHook
}

func (a *appModel) updateUI(fn func()) {
	if hook := a.getUIHook(); hook != nil {
		hook(fn)
		return
	}
	if a.win != nil {
		a.win.Update(fn)
		return
	}
	fn()
}

func (a *appModel) contactBySession(sessionID string) *contact {
	for _, ct := range a.contacts {
		if ct.SessionID == sessionID {
			return ct
		}
	}
	return nil
}

func (a *appModel) loadCapabilities(rt *runtimeClient, gen int) {
	if rt == nil {
		return
	}
	capabilities, err := rt.capabilities()
	a.updateUI(func() {
		if gen != a.connectionGeneration || a.rt != rt {
			return
		}
		if err != nil {
			a.runtimeCapabilitiesLoaded = false
			a.runtimeCapabilitiesErr = "Runtime capacity is not loaded: " + a.connectionRuntimeMessage(err) + ". The app uses the conservative single-slot behavior until it is retried."
			return
		}
		if capabilities.MaxActiveTasks < 1 {
			a.runtimeCapabilitiesLoaded = false
			a.runtimeCapabilitiesErr = "Runtime returned an invalid task capacity; the app uses the conservative single-slot behavior."
			return
		}
		a.runtimeMaxActiveTasks = capabilities.MaxActiveTasks
		a.runtimeCapabilitiesLoaded = true
		a.runtimeCapabilitiesErr = ""
		a.maybeAutoContinueAllIssues()
	})
}

func (a *appModel) capacityKnown() bool {
	return a.demo || a.runtimeCapabilitiesLoaded
}

func (a *appModel) retryCapabilities() {
	if a.rt == nil || a.runtimeStatus != runtimeStateReady {
		return
	}
	rt, gen := a.rt, a.connectionGeneration
	a.runtimeCapabilitiesErr = ""
	go a.loadCapabilities(rt, gen)
}

func (a *appModel) maxActiveTasks() int {
	if a.runtimeMaxActiveTasks > 0 {
		return a.runtimeMaxActiveTasks
	}
	return 1
}

func (a *appModel) workTaskLimit() int {
	if max := a.maxActiveTasks(); max > 1 {
		return max - 1
	}
	return 1
}

func sessionBusy(send sendState, activeTaskID string) bool {
	return activeTaskID != "" || send != sendIdle
}

func (a *appModel) activeTaskCount() int {
	count := 0
	for _, ct := range a.contacts {
		if sessionBusy(ct.SendState, ct.ActiveTaskID) {
			count++
		}
		for _, issue := range ct.Issues {
			if issue.WorkSessionID != "" || sessionBusy(issue.Work.SendState, issue.Work.ActiveTaskID) {
				if sessionBusy(issue.Work.SendState, issue.Work.ActiveTaskID) {
					count++
				}
			}
		}
	}
	return count
}

func (a *appModel) activeWorkTaskCount() int {
	count := 0
	for _, ct := range a.contacts {
		for _, issue := range ct.Issues {
			if sessionBusy(issue.Work.SendState, issue.Work.ActiveTaskID) {
				count++
			}
		}
	}
	return count
}

func (a *appModel) restoreIssueWorks(rt *runtimeClient, generation int) {
	for _, ct := range a.contacts {
		for _, issue := range ct.Issues {
			if issue.WorkSessionID == "" {
				continue
			}
			contactID, issueID, sessionID := ct.ID, issue.ID, issue.WorkSessionID
			issue.Work.Loaded = false
			a.historyAsyncStarted()
			go a.restoreIssueWork(rt, generation, contactID, issueID, sessionID)
		}
	}
}

func (a *appModel) restoreContacts(rt *runtimeClient, generation int) {
	for _, ct := range a.contacts {
		if ct.SessionID == "" {
			continue
		}
		contactID, sessionID := ct.ID, ct.SessionID
		ct.Loaded = false
		a.historyAsyncStarted()
		go a.restoreSession(rt, generation, contactID, sessionID)
	}
}

func (a *appModel) restoreSession(rt *runtimeClient, generation int, contactID, sessionID string) {
	session, err := rt.loadSession(sessionID)
	if err != nil {
		a.restoreContactReadOnly(rt, generation, contactID, sessionID, err)
		return
	}
	pages, status, err := a.historyAllFor(rt, sessionID)
	a.updateUI(func() {
		defer a.historyAsyncFinished()
		if generation != a.connectionGeneration || a.rt != rt {
			return
		}
		ct := a.contactByID(contactID)
		if ct == nil || ct.SessionID != sessionID {
			return
		}
		ct.Loaded = err == nil
		ct.LoadErr = ""
		ct.Unavailable = ""
		ct.SessionState = session.State
		if session.Metadata != nil {
			ct.SessionWorkingDir = session.Metadata.WorkingDirectory
			ct.SessionModel = session.Metadata.Model
			ct.SessionAPI = session.Metadata.API
		}
		if session.ActiveTask != nil {
			ct.ActiveTaskID = session.ActiveTask.TaskID
			ct.LastTaskID = session.ActiveTask.TaskID
			ct.SendState = sendAccepted
			ct.Status = statusWorking
			ct.Work = "Resuming"
			if w := a.ensureWork(ct, session.ActiveTask.TaskID, session.ActiveTask.MessageID); w != nil {
				w.State = workRunning
			}
		}
		if err != nil {
			ct.LoadErr = a.connectionRuntimeMessage(err)
			ct.Status = statusAttention
			ct.StatusDetail = ct.LoadErr
			return
		}
		if ct.ActiveTaskID != "" {
			ct.HistoryDirty = true
			return
		}
		a.applyHistory(ct, pages, status)
		if session.ActiveTask != nil {
			if w := a.ensureWork(ct, session.ActiveTask.TaskID, session.ActiveTask.MessageID); w != nil {
				w.State = workRunning
				ct.Work = a.workActivitySummary(ct)
			}
		}
	})
}

func (a *appModel) restoreContactReadOnly(rt *runtimeClient, generation int, contactID, sessionID string, loadErr error) {
	pages, status, historyErr := a.historyAllFor(rt, sessionID)
	config, configErr := rt.connectionConfig(sessionID)
	a.updateUI(func() {
		defer a.historyAsyncFinished()
		if generation != a.connectionGeneration || a.rt != rt {
			return
		}
		ct := a.contactByID(contactID)
		if ct == nil || ct.SessionID != sessionID {
			return
		}
		ct.Loaded = false
		ct.LoadErr = a.connectionRuntimeMessage(loadErr)
		ct.Unavailable = ct.LoadErr
		ct.Status = statusAttention
		ct.StatusDetail = ct.LoadErr
		if configErr == nil {
			ct.SessionModel = config.EffectiveConfig.Model
			ct.SessionAPI = config.EffectiveConfig.API
			ct.SessionWorkingDir = config.EffectiveConfig.WorkingDirectory
		}
		if historyErr == nil {
			a.applyHistory(ct, pages, status)
			ct.Loaded = false
			ct.Status = statusAttention
			ct.StatusDetail = ct.LoadErr
			ct.Unavailable = ct.LoadErr
		} else {
			ct.LoadErr += "; history unavailable: " + a.connectionRuntimeMessage(historyErr)
		}
	})
}

func (a *appModel) restoreIssueWork(rt *runtimeClient, generation int, contactID, issueID, sessionID string) {
	session, err := rt.loadSession(sessionID)
	if err != nil {
		a.restoreIssueReadOnly(rt, generation, contactID, issueID, sessionID, err)
		return
	}
	pages, status, err := a.historyAllFor(rt, sessionID)
	a.updateUI(func() {
		defer a.historyAsyncFinished()
		if generation != a.connectionGeneration || a.rt != rt {
			return
		}
		ct := a.contactByID(contactID)
		if ct == nil {
			return
		}
		issue := ct.issueByID(issueID)
		if issue == nil || issue.WorkSessionID != sessionID {
			return
		}
		issue.Work.Loaded = err == nil
		issue.Work.LoadErr = ""
		issue.Work.SessionState = session.State
		if err != nil {
			issue.Work.LoadErr = a.connectionRuntimeMessage(err)
			issue.Work.SendError = issue.Work.LoadErr
			return
		}
		a.applyIssueWorkHistory(ct, issue, pages, status)
		if session.ActiveTask != nil {
			issue.Work.ActiveTaskID = session.ActiveTask.TaskID
			issue.Work.LastTaskID = session.ActiveTask.TaskID
			issue.Work.SendState = sendAccepted
			if w := a.ensureWork(ct, session.ActiveTask.TaskID, session.ActiveTask.MessageID); w != nil {
				w.State = workRunning
			}
			if execution := ct.executionByTask(session.ActiveTask.TaskID); execution != nil {
				execution.Status = issueExecutionRunning
			}
			if issue.Status == issueStatusCanceled {
				_ = a.cancelIssueFor(ct, issue.ID)
			} else if issue.Status != issueStatusCompleted && issue.Status != issueStatusWaitingUser {
				issue.Status = issueStatusInProgress
			}
			return
		}
		issue.Work.ActiveTaskID = ""
		issue.Work.SendState = sendIdle
		issue.Work.SendError = ""
		a.maybeAutoContinueAllIssues()
	})
}

func (a *appModel) restoreIssueReadOnly(rt *runtimeClient, generation int, contactID, issueID, sessionID string, loadErr error) {
	pages, status, historyErr := a.historyAllFor(rt, sessionID)
	a.updateUI(func() {
		defer a.historyAsyncFinished()
		if generation != a.connectionGeneration || a.rt != rt {
			return
		}
		ct := a.contactByID(contactID)
		if ct == nil {
			return
		}
		issue := ct.issueByID(issueID)
		if issue == nil || issue.WorkSessionID != sessionID {
			return
		}
		issue.Work.Loaded = false
		issue.Work.LoadErr = a.connectionRuntimeMessage(loadErr)
		issue.Work.SendError = issue.Work.LoadErr
		if historyErr == nil {
			a.applyIssueWorkHistory(ct, issue, pages, status)
			issue.Work.Loaded = false
			issue.Work.SendError = issue.Work.LoadErr
		} else {
			issue.Work.LoadErr += "; history unavailable: " + a.connectionRuntimeMessage(historyErr)
		}
	})
}

func (a *appModel) reloadIssueWorkByID(contactID, issueID string) {
	if a.rt == nil || a.shutdownStarted || a.runtimeStatus == runtimeStateDraining || a.runtimeStatus == runtimeStateExited {
		return
	}
	ct := a.contactByID(contactID)
	if ct == nil {
		return
	}
	issue := ct.issueByID(issueID)
	if issue == nil || issue.WorkSessionID == "" {
		return
	}
	issue.Work.historyGen++
	gen := issue.Work.historyGen
	sessionID := issue.WorkSessionID
	rt, runtimeGeneration := a.rt, a.connectionGeneration
	a.historyAsyncStarted()
	go a.reloadIssueWork(rt, runtimeGeneration, contactID, issueID, sessionID, gen)
}

func (a *appModel) reloadIssueWork(rt *runtimeClient, runtimeGeneration int, contactID, issueID, sessionID string, gen int) {
	session, err := rt.loadSession(sessionID)
	if err != nil {
		a.restoreIssueReadOnly(rt, runtimeGeneration, contactID, issueID, sessionID, err)
		return
	}
	pages, status, err := a.historyAllFor(rt, sessionID)
	a.updateUI(func() {
		defer a.historyAsyncFinished()
		if runtimeGeneration != a.connectionGeneration || a.rt != rt {
			return
		}
		ct := a.contactByID(contactID)
		if ct == nil {
			return
		}
		issue := ct.issueByID(issueID)
		if issue == nil || issue.WorkSessionID != sessionID || issue.Work.historyGen != gen {
			return
		}
		if err != nil {
			issue.Work.LoadErr = a.connectionRuntimeMessage(err)
			issue.Work.SendError = issue.Work.LoadErr
			return
		}
		issue.Work.SessionState = session.State
		a.applyIssueWorkHistory(ct, issue, pages, status)
		if session.ActiveTask != nil {
			if issue.Work.SendState == sendSubmitting {
				return
			}
			issue.Work.ActiveTaskID = session.ActiveTask.TaskID
			issue.Work.LastTaskID = session.ActiveTask.TaskID
			issue.Work.SendState = sendAccepted
			issue.Work.SendError = ""
			if w := a.ensureWork(ct, session.ActiveTask.TaskID, session.ActiveTask.MessageID); w != nil {
				w.State = workRunning
			}
			if execution := ct.executionByTask(session.ActiveTask.TaskID); execution != nil {
				execution.Status = issueExecutionRunning
			}
			return
		}
		if issue.Work.SendState == sendSubmitting {
			return
		}
		issue.Work.ActiveTaskID = ""
		issue.Work.SendState = sendIdle
		issue.Work.SendError = ""
		issue.Work.LoadErr = ""
		a.maybeAutoContinueAllIssues()
	})
}

func (a *appModel) reloadContactByID(contactID string, explicit bool) {
	if a.rt == nil || a.shutdownStarted || a.runtimeStatus == runtimeStateDraining || a.runtimeStatus == runtimeStateExited {
		return
	}
	ct := a.contactByID(contactID)
	if ct == nil || ct.SessionID == "" {
		return
	}
	ct.historyGen++
	gen := ct.historyGen
	sessionID := ct.SessionID
	rt, runtimeGeneration := a.rt, a.connectionGeneration
	a.historyAsyncStarted()
	go a.reloadSession(rt, runtimeGeneration, contactID, sessionID, gen, explicit)
}

func (a *appModel) reloadSession(rt *runtimeClient, runtimeGeneration int, contactID, sessionID string, gen int, explicit bool) {
	session, err := rt.loadSession(sessionID)
	if err != nil {
		a.restoreContactReadOnly(rt, runtimeGeneration, contactID, sessionID, err)
		return
	}
	pages, status, err := a.historyAllFor(rt, sessionID)
	a.updateUI(func() {
		defer a.historyAsyncFinished()
		if runtimeGeneration != a.connectionGeneration || a.rt != rt {
			return
		}
		a.applyReload(contactID, sessionID, gen, session, pages, status, err, explicit)
	})
}

func (a *appModel) applyReload(contactID, sessionID string, gen int, session runtimeSession, pages []historyRecord, status string, err error, explicit bool) {
	ct := a.contactByID(contactID)
	if ct == nil || ct.SessionID != sessionID || ct.historyGen != gen {
		return // a newer submit/task superseded this reload
	}
	if err != nil {
		ct.LoadErr = a.connectionRuntimeMessage(err)
		ct.SendError = ct.LoadErr
		return
	}
	ct.SessionState = session.State
	if session.Metadata != nil {
		ct.SessionWorkingDir = session.Metadata.WorkingDirectory
		ct.SessionModel = session.Metadata.Model
		ct.SessionAPI = session.Metadata.API
	}
	if session.ActiveTask != nil {
		if ct.SendState == sendSubmitting {
			return
		}
		ct.ActiveTaskID = session.ActiveTask.TaskID
		ct.LastTaskID = session.ActiveTask.TaskID
		ct.SendState = sendAccepted
		ct.Status = statusWorking
		ct.Work = "Running"
		ct.SendError = ""
		if w := a.ensureWork(ct, session.ActiveTask.TaskID, session.ActiveTask.MessageID); w != nil {
			w.State = workRunning
		}
		return
	}
	if ct.SendState == sendSubmitting {
		return // a newer submit owns this contact; its generation changed too
	}
	// The runtime confirms this session is idle. For sendUnknown (or a missed
	// terminal event) this is where an explicit reload may recover, but only
	// after history was fetched successfully. Never re-send.
	a.applyHistory(ct, pages, status)
	unknownPending := ct.pendingSend != nil && ct.pendingSend.State == "unknown"
	if unknownPending && !explicit {
		ct.ActiveTaskID = ""
		ct.SendState = sendUnknown
		ct.Status = statusAttention
		ct.SendError = taskContextUncertainSendError
		ct.StatusDetail = ct.SendError
		ct.LoadErr = ""
		return
	}
	if unknownPending {
		previous := clonePendingSend(ct.pendingSend)
		ct.pendingSend = nil
		if err := a.persist(); err != nil {
			ct.pendingSend = previous
			ct.LoadErr = "Could not save the history-reload state: " + err.Error()
			return
		}
		ct.StatusDetail = "History reloaded; no retry was sent. Inspect the recorded messages before explicitly sending or removing retained task context."
	}
	ct.ActiveTaskID = ""
	ct.SendState = sendIdle
	ct.SendError = ""
	ct.LoadErr = ""
}

func (a *appModel) historyAll(sessionID string) ([]historyRecord, string, error) {
	return a.historyAllFor(a.rt, sessionID)
}

func (a *appModel) historyAllFor(rt *runtimeClient, sessionID string) ([]historyRecord, string, error) {
	history, err := a.historyAllForSession(rt, sessionID)
	if err != nil {
		return nil, "", err
	}
	return history.Records, history.Status, nil
}

// historyAllForSession keeps the Runtime session header so callers can verify
// typed provenance instead of assuming which session a record slice came from.
func (a *appModel) historyAllForSession(rt *runtimeClient, sessionID string) (runtimeHistory, error) {
	if rt == nil {
		return runtimeHistory{}, errRuntimeDisconnected
	}
	offset := 0
	var all []historyRecord
	status := ""
	for page := 0; page < 1000; page++ {
		result, err := rt.historyPage(sessionID, offset, 100)
		if err != nil {
			return runtimeHistory{}, err
		}
		if result.SessionID != sessionID {
			return runtimeHistory{}, errors.New("runtime history returned a different session")
		}
		all = append(all, result.Records...)
		status = result.Status
		if result.NextOffset == nil {
			return runtimeHistory{SessionID: result.SessionID, Records: all, Status: status}, nil
		}
		if *result.NextOffset <= offset {
			return runtimeHistory{}, errors.New("history pagination did not advance")
		}
		offset = *result.NextOffset
	}
	return runtimeHistory{}, errors.New("history pagination exceeded 1000 pages")
}

type historyProjection struct {
	messages []*message
	entries  []historyEntry
	works    []*workItem
	unlinked []*workTool
	detail   string
}

func parseHistory(sessionID string, records []historyRecord, stopRequested map[string]bool) historyProjection {
	return parseHistoryWithContextFilter(sessionID, records, stopRequested, nil)
}

func parseHistoryWithContextFilter(sessionID string, records []historyRecord, stopRequested map[string]bool, filter func(string, []taskContextItem) []taskContextItem) historyProjection {
	projection := historyProjection{}
	byTask := map[string]*workItem{}
	taskID, messageID := "", ""
	ensure := func(id, msgID string) *workItem {
		if id == "" {
			return nil
		}
		if w := byTask[id]; w != nil {
			if w.MessageID == "" {
				w.MessageID = msgID
			}
			return w
		}
		w := &workItem{TaskID: id, MessageID: msgID, State: workUnknown, StopRequested: stopRequested[id]}
		byTask[id] = w
		projection.works = append(projection.works, w)
		return w
	}
	unlinked := func(callID string) *workTool {
		if callID != "" {
			for _, call := range projection.unlinked {
				if call.CallID == callID {
					return call
				}
			}
		}
		call := &workTool{CallID: callID}
		projection.unlinked = append(projection.unlinked, call)
		return call
	}
	for _, rec := range records {
		switch rec.Record.Type {
		case "task_started":
			taskID, messageID = rec.Record.TaskID, rec.Record.MessageID
			if w := ensure(taskID, messageID); w != nil {
				w.State = workRunning
			}
		case "task_finished":
			finishedTask := rec.Record.TaskID
			if finishedTask == "" {
				finishedTask = taskID
			}
			finishedMessage := rec.Record.MessageID
			if finishedMessage == "" {
				finishedMessage = messageID
			}
			if w := ensure(finishedTask, finishedMessage); w != nil {
				w.State = workStateFromTask(rec.Record.Status)
				w.Detail = historyWorkDetail(rec.Record.Status, rec.Record.Error)
			}
			projection.detail = historyWorkDetail(rec.Record.Status, rec.Record.Error)
			taskID, messageID = "", ""
		case "messages":
			w := ensure(taskID, messageID)
			for index, item := range rec.Record.Items {
				key := fmt.Sprintf("hist:%s:%d:%d", sessionID, rec.Offset, index)
				timeLabel := formatMillis(rec.TimestampMS)
				switch item.Type {
				case "message":
					if item.Role == "system" || item.Role == "developer" {
						continue
					}
					text, contexts := inputText(item.Content), []taskContextItem(nil)
					if item.Role == "user" {
						text, contexts = taskContextUserContent(item.Content)
					}
					if filter != nil && len(contexts) > 0 {
						contexts = filter(sessionID, contexts)
					}
					if text == "" && len(contexts) == 0 {
						continue
					}
					kind := msgContact
					from := "contact"
					if item.Role == "user" {
						kind = msgUser
						from = "me"
						if w != nil && w.Prompt == "" {
							w.Prompt = text
						}
					}
					projection.messages = append(projection.messages, &message{
						Key: key, Kind: kind, Text: text, Time: timeLabel,
						TaskID: taskID, MessageID: messageID, History: true,
						Context: contexts,
					})
					projection.entries = append(projection.entries, historyEntry{ID: key, Date: timeLabel, From: from, Text: text, Context: contexts})
				case "tool_call":
					if w == nil {
						call := unlinked(item.CallID)
						call.Name = item.Name
						call.Args = item.Arguments
						call.Started = true
						continue
					}
					call := workToolFor(w, item.CallID)
					if call == nil {
						continue
					}
					call.Name = item.Name
					call.Args = item.Arguments
					call.Started = true
				case "tool_result":
					if w == nil {
						call := unlinked(item.CallID)
						call.Output = item.Output
						call.Finished = true
						continue
					}
					call := workToolFor(w, item.CallID)
					if call == nil {
						continue
					}
					call.Output = item.Output
					call.Finished = true
				case "reasoning", "provider_item":
					// Reasoning stays internal; desktop.md does not show it.
				}
			}
		}
	}
	return projection
}

// mergeWorks keeps work items from other runtime sessions (conversation vs an
// issue work context) while replacing the projection for the session being
// reloaded. A work item belongs to exactly one session.
// mergeWorks keeps work items owned by a different runtime session while
// replacing the projection for targetSession. Work items carry no session of
// their own; ownership comes from the persisted issue execution TaskID and its
// actual SessionID.
func mergeWorks(ct *contact, existing, incoming []*workItem, targetSession string) []*workItem {
	byTask := make(map[string]*workItem, len(incoming))
	for _, work := range incoming {
		byTask[work.TaskID] = work
	}
	owners := map[string]string{}
	for _, issue := range ct.Issues {
		for _, execution := range issue.Executions {
			if execution.TaskID != "" && execution.SessionID != "" {
				owners[execution.TaskID] = execution.SessionID
			}
		}
	}
	out := make([]*workItem, 0, len(existing)+len(incoming))
	for _, work := range existing {
		if _, ok := byTask[work.TaskID]; ok {
			continue
		}
		owner := owners[work.TaskID]
		switch {
		case owner != "" && owner != targetSession:
			out = append(out, work)
		case owner == "" && targetSession != ct.SessionID:
			out = append(out, work)
		}
	}
	out = append(out, incoming...)
	return out
}

func (a *appModel) applyHistory(ct *contact, records []historyRecord, status string) {
	projection := parseHistoryWithIssueToolScopeFilter(ct.SessionID, records, ct.StopRequestedTasks, a.issueToolScopeIndexSnapshot())
	ct.Messages = projection.messages
	ct.History = projection.entries
	ct.UnlinkedTools = projection.unlinked
	ct.Works = mergeWorks(ct, ct.Works, projection.works, ct.SessionID)
	for _, issue := range ct.Issues {
		for _, execution := range issue.Executions {
			// Only legacy executions that actually used the conversation
			// session are reconciled here. New issue work sessions are
			// reconciled by applyIssueWorkHistory.
			if execution.SessionID != ct.SessionID {
				continue
			}
			if w := workFor(ct, execution.TaskID); w != nil {
				execution.Status = issueExecutionStatusFromWork(w.State)
				execution.Detail = w.Detail
			} else if execution.Status == issueExecutionPending ||
				execution.Status == issueExecutionAccepted || execution.Status == issueExecutionRunning {
				execution.Status = issueExecutionUnknown
				execution.Detail = "No trusted Runtime task history was found for this execution; reload before rerunning."
			}
		}
	}
	ct.HistoryDirty = false
	ct.Loaded = true
	ct.Status = statusFromString(status)
	ct.StatusDetail = projection.detail
	if ct.Status == statusAttention && ct.StatusDetail == "" {
		ct.StatusDetail = "Previous task did not complete."
	}
	if len(projection.works) > 0 {
		ct.LastTaskID = projection.works[len(projection.works)-1].TaskID
	}
	if w := ct.activeWork(); w != nil {
		if w.State == workUnknown || w.State == workAccepted {
			w.State = workRunning
		}
		ct.Work = a.workActivitySummary(ct)
	} else {
		ct.Work = ""
	}
}

func (a *appModel) applyIssueWorkHistory(ct *contact, issue *productIssue, records []historyRecord, status string) {
	sessionID := issue.WorkSessionID
	projection := parseHistoryWithIssueToolScopeFilter(sessionID, records, issue.Work.StopRequestedTasks, a.issueToolScopeIndexSnapshot())
	issue.Work.Messages = projection.messages
	issue.Work.UnlinkedTools = projection.unlinked
	ct.Works = mergeWorks(ct, ct.Works, projection.works, sessionID)
	for _, execution := range issue.Executions {
		if execution.SessionID != sessionID {
			continue
		}
		if w := workFor(ct, execution.TaskID); w != nil {
			execution.Status = issueExecutionStatusFromWork(w.State)
			execution.Detail = w.Detail
		} else if execution.Status == issueExecutionPending ||
			execution.Status == issueExecutionAccepted || execution.Status == issueExecutionRunning {
			execution.Status = issueExecutionUnknown
			execution.Detail = "No trusted Runtime task history was found for this execution; reload before rerunning."
		}
	}
	issue.Work.Loaded = true
	issue.Work.SessionState = status
	if issue.Work.ReleasePending && issue.Work.ActiveTaskID == "" && issue.Work.SendState == sendIdle &&
		issue.Work.pendingExecutionID == "" && len(issue.Work.pendingEvents) == 0 && !issue.Work.Releasing {
		a.releaseIssueWorkSession(ct, issue)
	}
	issue.Work.HistoryDirty = false
	a.applyIssueRecoveredLifecycle(issue)
}

func (a *appModel) applyIssueRecoveredLifecycle(issue *productIssue) {
	if issue == nil || issue.Status == issueStatusCompleted || issue.Status == issueStatusCanceled ||
		issue.Status == issueStatusWaitingUser {
		return
	}
	if issue.Work.ActiveTaskID != "" {
		issue.Status = issueStatusInProgress
		issue.UpdatedAt = nowMillis()
		return
	}
	if len(issue.Executions) == 0 {
		return
	}
	latest := issue.Executions[len(issue.Executions)-1]
	switch latest.Status {
	case issueExecutionCompleted:
		issue.Status = issueStatusInProgress
	case issueExecutionFailed, issueExecutionUnknown, issueExecutionPending,
		issueExecutionNotDispatched, issueExecutionRejected:
		issue.Status = issueStatusNeedsAttention
	case issueExecutionInterrupted:
		if issue.Work.StopRequestedTasks[latest.TaskID] {
			issue.Status = issueStatusInProgress
		} else {
			issue.Status = issueStatusNeedsAttention
		}
	case issueExecutionAccepted, issueExecutionRunning:
		issue.Status = issueStatusInProgress
	}
	issue.UpdatedAt = nowMillis()
}

func historyWorkDetail(status string, errText *string) string {
	switch status {
	case "failed":
		return taskFailureDetail("", errText)
	case "interrupted":
		return "Task interrupted"
	default:
		return ""
	}
}

func unlinkedToolFor(ct *contact, callID string) *workTool {
	if callID != "" {
		for _, call := range ct.UnlinkedTools {
			if call.CallID == callID {
				return call
			}
		}
	}
	call := &workTool{CallID: callID}
	ct.UnlinkedTools = append(ct.UnlinkedTools, call)
	return call
}

func workToolFor(w *workItem, callID string) *workTool {
	if callID == "" {
		return nil
	}
	for _, call := range w.Tools {
		if call.CallID == callID {
			return call
		}
	}
	call := &workTool{CallID: callID}
	w.Tools = append(w.Tools, call)
	return call
}

func statusFromString(status string) contactStatus {
	switch status {
	case "failed", "interrupted":
		return statusAttention
	case "running":
		return statusWorking
	default:
		return statusReady
	}
}

func formatMillis(ms int64) string {
	if ms <= 0 {
		return ""
	}
	value := time.UnixMilli(ms)
	now := time.Now()
	if value.Year() == now.Year() && value.YearDay() == now.YearDay() {
		return value.Format("15:04")
	}
	return value.Format("Jan 2 15:04")
}

func runtimeMessage(err error) string {
	var runtimeErr *runtimeError
	if errors.As(err, &runtimeErr) {
		message := runtimeErr.Code + ": " + safeDiagnostic(runtimeErr.Message)
		if runtimeErr.Reason != "" {
			message += " [" + runtimeErr.Reason + "]"
		}
		return message
	}
	return safeDiagnostic(err.Error())
}

func confirmedReject(err error) bool {
	var runtimeErr *runtimeError
	return errors.As(err, &runtimeErr) && runtimeErr.Code != "protocol_error"
}

func taskAdmissionDeferred(err error) bool {
	var runtimeErr *runtimeError
	return errors.As(err, &runtimeErr) && runtimeErr.Code == "busy"
}

func (a *appModel) routeSession(sessionID string) (*contact, *productIssue, bool) {
	for _, ct := range a.contacts {
		if ct.SessionID == sessionID {
			return ct, nil, false
		}
		for _, issue := range ct.Issues {
			if issue.WorkSessionID == sessionID {
				return ct, issue, true
			}
		}
	}
	return nil, nil, false
}

func (a *appModel) applyRuntimeEvent(event runtimeEventEnvelope) {
	if event.Event == nil {
		return
	}
	if ct, issue, work := a.routeSession(event.SessionID); work {
		if issue == nil {
			return
		}
		if issue.Work.SendState == sendSubmitting && issue.Work.ActiveTaskID == "" {
			issue.Work.pendingEvents = append(issue.Work.pendingEvents, event)
			return
		}
		a.applyIssueEvent(ct, issue, event)
		return
	}
	ct := a.contactBySession(event.SessionID)
	if ct == nil {
		return
	}
	if ct.SendState == sendSubmitting && ct.ActiveTaskID == "" {
		ct.pendingEvents = append(ct.pendingEvents, event)
		return
	}
	a.applyEvent(ct, event)
}

func (a *appModel) applyIssueEvent(ct *contact, issue *productIssue, event runtimeEventEnvelope) {
	payload := event.Event
	if payload == nil {
		return
	}
	if issue.Work.ActiveTaskID == "" && payload.Type != "task_started" {
		return
	}
	if event.TaskID != "" && issue.Work.ActiveTaskID != "" && event.TaskID != issue.Work.ActiveTaskID {
		return
	}
	switch payload.Type {
	case "task_started":
		if w := workFor(ct, event.TaskID); w != nil {
			switch w.State {
			case workStopping, workCompleted, workFailed, workInterrupted, workUnknown:
				return
			}
		}
		if issue.Work.ActiveTaskID == "" && issue.Work.LastTaskID != "" && event.TaskID != issue.Work.LastTaskID {
			return
		}
		if issue.Work.ActiveTaskID == "" {
			issue.Work.ActiveTaskID = event.TaskID
		}
		if event.TaskID != issue.Work.ActiveTaskID {
			return
		}
		issue.Work.LastTaskID = event.TaskID
		issue.Work.SendState = sendAccepted
		issue.Work.SessionState = "running"
		issue.Work.SendError = ""
		if w := a.ensureWork(ct, event.TaskID, event.MessageID); w != nil {
			w.State = workRunning
		}
		if execution := ct.executionByTask(event.TaskID); execution != nil {
			execution.Status = issueExecutionRunning
			execution.Detail = ""
		}
	case "response_text_delta":
		item := a.liveIssueText(issue, event.TaskID, payload.ItemID)
		item.Text += payload.Delta
		item.Streaming = true
	case "response_text":
		item := a.liveIssueText(issue, event.TaskID, payload.ItemID)
		item.Text = payload.Text
		item.Streaming = false
	case "tool_started":
		w := a.ensureWork(ct, event.TaskID, event.MessageID)
		if w == nil {
			return
		}
		call := workToolFor(w, payload.CallID)
		if call == nil {
			return
		}
		call.Name = payload.Name
		call.Args = payload.Arguments
		call.Started = true
	case "tool_finished":
		w := a.ensureWork(ct, event.TaskID, event.MessageID)
		if w == nil {
			return
		}
		call := workToolFor(w, payload.CallID)
		if call == nil {
			return
		}
		call.Name = payload.Name
		call.Output = outputText(payload.Output)
		call.Finished = true
	case "task_finished":
		if issue.Work.ActiveTaskID != "" && event.TaskID != issue.Work.ActiveTaskID {
			return
		}
		if issue.Work.ActiveTaskID == "" && issue.Work.LastTaskID != "" && event.TaskID != issue.Work.LastTaskID {
			return
		}
		issue.Work.LastTaskID = event.TaskID
		issue.Work.ActiveTaskID = ""
		issue.Work.SendState = sendIdle
		issue.Work.SendError = ""
		issue.Work.SessionState = "unknown"
		issue.Work.HistoryDirty = true
		issue.Work.historyGen++
		detail := a.connectionTaskFinishedDetail(payload)
		if w := workFor(ct, event.TaskID); w != nil {
			w.State = workStateFromTask(payload.Status)
			w.Detail = detail
		} else if w := a.ensureWork(ct, event.TaskID, event.MessageID); w != nil {
			w.State = workStateFromTask(payload.Status)
			w.Detail = detail
		}
		if execution := ct.executionByTask(event.TaskID); execution != nil {
			execution.Status = issueExecutionStatusFromWork(workStateFromTask(payload.Status))
			execution.Detail = detail
		}
		a.applyIssueTerminalLifecycle(issue, event.TaskID, payload.Status)
		if a.issueToolEnabled() {
			issue.Work.ReleasePending = true
		}
		_ = a.persist()
		a.reloadIssueWorkByID(ct.ID, issue.ID)
		a.maybeAutoContinueAllIssues()
	}
}

func (a *appModel) liveIssueText(issue *productIssue, taskID, itemID string) *message {
	key := "work-live:" + taskID + ":text:" + itemID
	for _, m := range issue.Work.Messages {
		if m.Key == key {
			return m
		}
	}
	m := &message{Key: key, Kind: msgContact, TaskID: taskID, ItemID: itemID, Time: nowTime()}
	issue.Work.Messages = append(issue.Work.Messages, m)
	return m
}

func (a *appModel) applyEvent(ct *contact, event runtimeEventEnvelope) {
	payload := event.Event
	if ct.ActiveTaskID == "" && payload.Type != "task_started" {
		return
	}
	if event.TaskID != "" && ct.ActiveTaskID != "" && event.TaskID != ct.ActiveTaskID {
		return
	}
	switch payload.Type {
	case "task_started":
		if w := workFor(ct, event.TaskID); w != nil {
			switch w.State {
			case workStopping, workCompleted, workFailed, workInterrupted, workUnknown:
				return // completed/stopping/unknown work cannot be revived by a duplicate start
			}
		}
		if ct.ActiveTaskID == "" && ct.LastTaskID != "" && event.TaskID != ct.LastTaskID {
			return // a late start from an older task cannot own this contact
		}
		if ct.ActiveTaskID == "" {
			ct.ActiveTaskID = event.TaskID
		}
		if event.TaskID != ct.ActiveTaskID {
			return
		}
		ct.LastTaskID = event.TaskID
		ct.SendState = sendAccepted
		ct.SessionState = "running"
		ct.Status = statusWorking
		ct.Work = "Starting"
		ct.SendError = ""
		if w := a.ensureWork(ct, event.TaskID, event.MessageID); w != nil {
			w.State = workRunning
		}
		if execution := ct.executionByTask(event.TaskID); execution != nil {
			execution.Status = issueExecutionRunning
			execution.Detail = ""
		}
	case "model_request_started":
		if payload.Purpose == "compaction" {
			ct.Work = "Compacting context"
		} else {
			ct.Work = "Waiting for model"
		}
	case "reasoning_delta":
		ct.Work = "Thinking"
	case "response_text_delta":
		item := a.liveText(ct, event.TaskID, payload.ItemID)
		item.Text += payload.Delta
		item.Streaming = true
		ct.Work = "Writing"
	case "response_text":
		item := a.liveText(ct, event.TaskID, payload.ItemID)
		item.Text = payload.Text
		item.Streaming = false
		ct.Work = "Writing"
	case "tool_started":
		w := a.ensureWork(ct, event.TaskID, event.MessageID)
		if w == nil {
			return
		}
		call := workToolFor(w, payload.CallID)
		if call == nil {
			return
		}
		call.Name = payload.Name
		call.Args = payload.Arguments
		call.Started = true
		ct.Work = "Running " + payload.Name
	case "tool_finished":
		w := a.ensureWork(ct, event.TaskID, event.MessageID)
		if w == nil {
			return
		}
		call := workToolFor(w, payload.CallID)
		if call == nil {
			return
		}
		call.Name = payload.Name
		call.Output = outputText(payload.Output)
		call.Finished = true
		ct.Work = "Waiting for model"
	case "task_finished":
		if ct.ActiveTaskID != "" && event.TaskID != ct.ActiveTaskID {
			return
		}
		if ct.ActiveTaskID == "" && ct.LastTaskID != "" && event.TaskID != ct.LastTaskID {
			return // duplicate/late terminal from an older task
		}
		ct.LastTaskID = event.TaskID
		ct.ActiveTaskID = ""
		ct.SendState = sendIdle
		ct.SendError = ""
		ct.Work = ""
		ct.HistoryDirty = true
		ct.historyGen++
		ct.SessionState = "unknown"
		ct.Status = statusFromString(payload.Status)
		ct.StatusDetail = a.connectionTaskFinishedDetail(payload)
		if ct.Status == statusAttention && ct.StatusDetail == "" {
			ct.StatusDetail = "Previous task did not complete."
		}
		if w := workFor(ct, event.TaskID); w != nil {
			w.State = workStateFromTask(payload.Status)
			w.Detail = ct.StatusDetail
		} else if w := a.ensureWork(ct, event.TaskID, event.MessageID); w != nil {
			w.State = workStateFromTask(payload.Status)
			w.Detail = ct.StatusDetail
		}
		if execution := ct.executionByTask(event.TaskID); execution != nil {
			execution.Status = issueExecutionStatusFromWork(workStateFromTask(payload.Status))
			execution.Detail = ct.StatusDetail
		}
		_ = a.persist()
		a.reloadContactByID(ct.ID, false)
		a.maybeAutoContinueAllIssues()
	}
}

func httpAuthStatus(message string) string {
	message = strings.TrimSpace(message)
	for _, status := range []string{"HTTP 401", "HTTP 403"} {
		if !strings.HasPrefix(message, status) {
			continue
		}
		n := len(status)
		if len(message) == n || message[n] < '0' || message[n] > '9' {
			return status
		}
	}
	return ""
}

func providerAuthFailure(message string) bool { return httpAuthStatus(message) != "" }

func taskFailureDetail(code string, message *string) string {
	if message == nil {
		return "Task failed."
	}
	if status := httpAuthStatus(*message); status != "" {
		return "Provider authentication failed: " + status +
			". Check API_KEY and provider configuration; no automatic retry."
	}
	stage := "Task failed"
	if code == "model_error" {
		stage = "Model request failed"
	}
	prefix := stage
	if code != "" {
		prefix += ": " + code
	}
	return prefix + ": " + safeDiagnostic(*message) +
		". Check provider configuration (API_KEY / SAKI_BASE_URL / SAKI_MODEL); no automatic retry."
}

func taskFinishedDetail(payload *runtimeEvent) string {
	switch payload.Status {
	case "failed":
		if payload.Error != nil {
			return taskFailureDetail(payload.Error.Code, &payload.Error.Message)
		}
		return "Task failed."
	case "interrupted":
		return "Task interrupted; no automatic retry."
	case "completed":
		return ""
	default:
		return ""
	}
}

func (a *appModel) liveText(ct *contact, taskID, itemID string) *message {
	key := "live:" + taskID + ":text:" + itemID
	for _, m := range ct.Messages {
		if m.Key == key {
			return m
		}
	}
	m := &message{Key: key, Kind: msgContact, TaskID: taskID, ItemID: itemID, Time: nowTime()}
	ct.Messages = append(ct.Messages, m)
	if ct != a.selectedContact() {
		ct.Unread++
	}
	return m
}

func (a *appModel) canSend(ct *contact) string {
	if a.shutdownStarted {
		return "The app is shutting down."
	}
	if ct.Archived {
		return "This contact is archived. Restore it before sending a message."
	}
	if a.demo {
		return ""
	}
	if a.connectionApplying {
		return "Connection settings are being applied; wait for the new helper to become ready."
	}
	if a.rt == nil || a.runtimeStatus != runtimeStateReady {
		return "Runtime is not ready."
	}
	if status := a.modelSessionStatus(ct.SessionID); status != "" {
		return status
	}
	if ct.SessionID != "" && !ct.Loaded {
		return "This contact is still loading its history."
	}
	if sessionBusy(ct.SendState, ct.ActiveTaskID) {
		return "This contact is busy."
	}
	if a.activeTaskCount() >= a.maxActiveTasks() {
		if a.maxActiveTasks() <= 1 {
			return "Another contact is working; wait for its task to finish before sending here."
		}
		return "Runtime task capacity is full; retry after a task finishes."
	}
	return ""
}

func (a *appModel) canStartIssue(ct *contact, issue *productIssue) string {
	if a.shutdownStarted {
		return "The app is shutting down."
	}
	if ct.Archived {
		return "This contact is archived. Restore it before starting issue work."
	}
	if a.demo {
		return ""
	}
	if a.connectionApplying {
		return "Connection settings are being applied; wait for the new helper to become ready."
	}
	if a.rt == nil || a.runtimeStatus != runtimeStateReady {
		return "Runtime is not ready."
	}
	if status := a.modelSessionStatus(issue.WorkSessionID); status != "" {
		return status
	}
	if sessionBusy(issue.Work.SendState, issue.Work.ActiveTaskID) {
		return "This issue already has an active execution."
	}
	if issue.Work.Releasing || issue.Work.ReleasePending {
		return "This issue's work session is still being released; retry after it completes."
	}
	if issue.WorkSessionID != "" && !issue.Work.Loaded && !issue.Work.Released {
		return "This issue's work history is still loading."
	}
	if a.activeTaskCount() >= a.maxActiveTasks() {
		if a.maxActiveTasks() <= 1 {
			return "The single runtime slot is in use; retry after the conversation or work task finishes."
		}
		return "Runtime task capacity is full; retry after a task finishes."
	}
	if a.activeWorkTaskCount() >= a.workTaskLimit() {
		return "Work capacity is full; one runtime slot is reserved for conversation."
	}
	return ""
}

func (a *appModel) startIssueTask(ct *contact, issue *productIssue, execution *issueExecution, prompt string) bool {
	if a.shutdownStarted {
		issue.Work.SendError = "The app is shutting down."
		return false
	}
	if ct.Archived {
		issue.Work.SendError = "This contact is archived. Restore it before starting issue work."
		return false
	}
	if a.connectionApplying {
		issue.Work.SendError = "Connection settings are being applied; wait for the new helper to become ready."
		return false
	}
	if status := a.modelSessionStatus(issue.WorkSessionID); status != "" {
		issue.Work.SendError = status
		return false
	}
	if a.rt == nil || a.runtimeStatus != runtimeStateReady {
		issue.Work.SendError = "Runtime is not ready."
		return false
	}
	// Capture immutable session inputs on the UI thread. The worker must not
	// look up the contact/issue or read mutable profile/session fields later.
	workspace := a.effectiveContactWorkspace(ct)
	system := a.issueToolWorkSystemPrompt(ct)
	sessionID := issue.WorkSessionID
	context := cloneTaskContextItems(execution.Context)
	rt, configured := a.rt, a.connectionManaged
	released := issue.Work.Released
	go a.submitIssueTask(rt, configured, ct.ID, issue.ID, execution.ID, sessionID, workspace, system, prompt, context, released)
	return true
}

func (a *appModel) submitIssueTask(rt *runtimeClient, configured bool, contactID, issueID, executionID, sessionID, workspace, system, prompt string, context []taskContextItem, released bool) {
	workTools := a.issueToolEnabled() && a.connectionManaged
	createdSession := false
	if sessionID == "" {
		var session runtimeSession
		var err error
		if workTools {
			session, err = a.configuredWorkCreate(rt, configured, workspace, system)
			createdSession = err == nil
		} else {
			session, err = a.configuredCreate(rt, configured, workspace, system)
		}
		if err != nil {
			a.failIssueSubmit(contactID, issueID, err, false)
			return
		}
		sessionID = session.SessionID
		saved := make(chan error, 1)
		a.updateUI(func() {
			ct := a.contactByID(contactID)
			if ct == nil {
				saved <- errors.New("contact disappeared before saving its work session")
				return
			}
			issue := ct.issueByID(issueID)
			if issue == nil {
				saved <- errors.New("issue disappeared before saving its work session")
				return
			}
			if issue.WorkSessionID != "" && issue.WorkSessionID != sessionID {
				saved <- errors.New("issue work session changed before it was saved")
				return
			}
			issue.WorkSessionID = sessionID
			issue.Work.Loaded = true
			issue.Work.SessionState = session.State
			if _, execution := ct.executionByID(executionID); execution != nil {
				execution.SessionID = sessionID
			}
			saved <- a.persist()
		})
		if err := <-saved; err != nil {
			a.failIssueSubmit(contactID, issueID, fmt.Errorf("save issue work session mapping: %w", err), false)
			return
		}
	}
	if sessionID != "" && released {
		if _, err := rt.loadSession(sessionID); err != nil {
			a.failIssueSubmit(contactID, issueID, fmt.Errorf("reload released work session: %w", err), false)
			return
		}
		a.runOnUI(func() {
			if issue := a.contactByID(contactID).issueByID(issueID); issue != nil {
				issue.Work.Released = false
				issue.Work.Loaded = true
				_ = a.persist()
			}
		})
	}
	wireContext := taskContextInputs(context)
	if workTools {
		configuredTools, err := a.issueToolEnsureWorkSession(rt, sessionID, issueToolWorkGuidance, createdSession)
		if err != nil {
			a.failIssueSubmit(contactID, issueID, err, false)
			return
		}
		if configuredTools {
			var scope *IssueToolScope
			var scopeErr error
			a.runOnUI(func() {
				if a.shutdownStarted {
					scopeErr = errors.New("app is shutting down")
					return
				}
				scope, scopeErr = a.issueToolNewWorkScope(contactID, issueID, executionID, sessionID)
				if scopeErr != nil {
					return
				}
				ct := a.contactByID(contactID)
				if ct == nil {
					scopeErr = errors.New("contact disappeared before work scope")
					return
				}
				_, execution := ct.executionByID(executionID)
				if execution == nil {
					scopeErr = errors.New("execution disappeared before work scope")
					return
				}
				execution.ToolScopeRef = scope.Ref
				execution.ToolScopeInput = issueToolScopeToken(scope)
				if err := a.persist(); err != nil {
					scopeErr = err
				}
			})
			if scopeErr != nil {
				a.failIssueSubmit(contactID, issueID, scopeErr, false)
				return
			}
			if input, ok := issueToolScopeInput(scope); ok {
				wireContext = append(wireContext, input)
			}
		}
	}
	if err := validateRuntimeContextInputs(wireContext); err != nil {
		a.failIssueSubmit(contactID, issueID, fmt.Errorf("issue work caller context: %w", err), false)
		return
	}
	result, err := rt.sendTask(sessionID, prompt, wireContext...)
	if err != nil {
		a.noteIssueToolTaskError(rt, sessionID, err)
		a.failIssueSubmit(contactID, issueID, err, true)
		return
	}
	a.updateUI(func() {
		if a.rt != rt {
			return
		}
		ct := a.contactByID(contactID)
		if ct == nil {
			return
		}
		issue := ct.issueByID(issueID)
		if issue == nil || issue.WorkSessionID != sessionID {
			return
		}
		execution := ct.executionByTask(result.TaskID)
		if execution == nil {
			_, execution = ct.executionByID(executionID)
		}
		a.acceptIssueTask(ct, issue, execution, prompt, result)
	})
}

func (a *appModel) acceptIssueTask(ct *contact, issue *productIssue, execution *issueExecution, prompt string, result runtimeSendResult) {
	issue.Work.historyGen++
	issue.Work.ActiveTaskID = result.TaskID
	issue.Work.LastTaskID = result.TaskID
	issue.Work.SendState = sendAccepted
	issue.Work.SendError = ""
	if execution != nil {
		execution.SessionID = issue.WorkSessionID
		execution.TaskID = result.TaskID
		execution.MessageID = result.MessageID
		execution.Status = issueExecutionAccepted
		execution.Detail = ""
		if execution.ToolScopeRef != "" {
			a.issueToolBindTask(execution.ToolScopeRef, result.TaskID)
		}
	}
	issue.Work.pendingExecutionID = ""
	if w := a.ensureWork(ct, result.TaskID, result.MessageID); w != nil {
		w.State = workAccepted
		w.Prompt = prompt
	}
	batch := issueSupplementBatchForExecution(issue, executionIDOf(execution))
	acceptedSaved := false
	if batch != nil {
		// Clear the exact submitted context in the same save as the acceptance
		// receipt. If the save fails, restore the pending context and keep the
		// external acceptance receipt in memory.
		previousContext := issue.Context.snapshot()
		if execution != nil && len(execution.Context) > 0 {
			issue.Context.clearSubmitted(execution.Context)
		}
		association := issueSupplementAssociation{}
		if execution != nil {
			association = issueSupplementAssociation{
				SessionID: execution.SessionID, TaskID: execution.TaskID,
				MessageID: execution.MessageID, ExecutionID: execution.ID,
			}
		}
		accepted, err := issue.Supplements.Accept(batch, association, a.persist)
		switch {
		case accepted && err == nil:
			acceptedSaved = true
			issue.Work.SendError = ""
		case accepted && err != nil:
			issue.Context.items = previousContext
			issue.Work.SendError = "Runtime accepted this supplement, but its local receipt could not be saved; reload before sending again."
		case err != nil:
			issue.Context.items = previousContext
			issue.Work.SendError = "Could not save the supplement acceptance: " + err.Error()
		default:
			issue.Context.items = previousContext
			issue.Work.SendError = "The supplement dispatch association was not in a recordable state; reload before sending again."
			_ = a.persist()
		}
	} else {
		if err := a.persist(); err != nil {
			if issue.Supplements != nil {
				issue.Supplements.AutoContinue = false
			}
			if execution != nil {
				execution.Status = issueExecutionUnknown
				execution.Detail = "Task was accepted, but its work-session association could not be saved: " + err.Error()
			}
			issue.Work.SendError = "Task accepted, but its issue association could not be saved; reload before retrying."
		} else {
			acceptedSaved = true
		}
	}
	if acceptedSaved && batch == nil && execution != nil && len(execution.Context) > 0 {
		previous := issue.Context.snapshot()
		if issue.Context.clearSubmitted(execution.Context) > 0 {
			if err := a.persist(); err != nil {
				issue.Context.items = previous
				if issue.Supplements != nil {
					issue.Supplements.AutoContinue = false
				}
				issue.Work.SendError = "Task accepted, but its sent context could not be cleared from the next-run draft; reload before sending again."
			}
		}
	}
	pending := issue.Work.pendingEvents
	issue.Work.pendingEvents = nil
	for _, event := range pending {
		a.applyIssueEvent(ct, issue, event)
	}
	if (issue.Work.cancelAfterAccept || issue.Status == issueStatusCanceled) && issue.Work.ActiveTaskID != "" {
		if a.cancelIssueFor(ct, issue.ID) {
			issue.Work.cancelAfterAccept = false
		} else {
			issue.Work.SendError = "The accepted task could not be cancelled; do not retry it automatically."
		}
	}
	if execution != nil && execution.ToolOperationID != "" {
		detail := ""
		if !acceptedSaved {
			detail = issue.Work.SendError
			if detail == "" {
				detail = "Runtime accepted the task, but its local receipt could not be saved."
			}
		}
		a.issueToolCompleteExecution(execution, acceptedSaved, detail)
	}
	if execution != nil && execution.WaitRequested && issue.openQuestion() != nil &&
		a.requestIssueWaitStop(ct, issue, execution.ID, execution.SessionID, execution.TaskID) {
		// The actual Runtime task is now known; cancellation was intentionally
		// deferred until this accept callback.
	}
}

func executionIDOf(execution *issueExecution) string {
	if execution == nil {
		return ""
	}
	return execution.ID
}

func (a *appModel) failIssueSubmit(contactID, issueID string, err error, submitted bool) {
	a.updateUI(func() {
		ct := a.contactByID(contactID)
		if ct == nil {
			return
		}
		issue := ct.issueByID(issueID)
		if issue == nil {
			return
		}
		issue.Work.pendingEvents = nil
		_, execution := ct.executionByID(issue.Work.pendingExecutionID)
		issue.Work.pendingExecutionID = ""
		if execution != nil && execution.ToolOperationID != "" {
			defer func() {
				detail := execution.Detail
				if detail == "" {
					detail = issue.Work.SendError
				}
				a.issueToolCompleteExecution(execution, false, detail)
			}()
		}
		batch := issueSupplementBatchForExecution(issue, executionIDOf(execution))
		detail := a.connectionRuntimeMessage(err)
		switch {
		case taskAdmissionDeferred(err):
			issue.Work.SendState = sendIdle
			issue.Work.SendError = "Waiting for runtime availability: " + detail
			if execution != nil {
				execution.Status = issueExecutionNotDispatched
				execution.Detail = issue.Work.SendError
			}
			if batch != nil {
				if !issue.Supplements.DeferDispatch(batch, a.persist) {
					_ = a.persist()
				}
			} else {
				_ = a.persist()
			}
		case confirmedReject(err):
			if issue.Supplements != nil {
				issue.Supplements.AutoContinue = false
			}
			if issue.Status != issueStatusCanceled && issue.Status != issueStatusCompleted && !issue.Archived {
				issue.Status = issueStatusNeedsAttention
				issue.UpdatedAt = nowMillis()
			}
			issue.Work.SendState = sendIdle
			issue.Work.SendError = detail
			if execution != nil {
				execution.Status = issueExecutionRejected
				execution.Detail = detail
			}
			if batch != nil {
				if !issue.Supplements.Reject(batch, detail, a.persist) {
					_ = a.persist()
				}
			} else {
				_ = a.persist()
			}
		case submitted:
			if issue.Supplements != nil {
				issue.Supplements.AutoContinue = false
			}
			if issue.Status != issueStatusCanceled && issue.Status != issueStatusCompleted && !issue.Archived {
				issue.Status = issueStatusNeedsAttention
				issue.UpdatedAt = nowMillis()
			}
			issue.Work.SendState = sendUnknown
			issue.Work.SendError = "Supplement delivery not confirmed (" + detail + "). Reload to check before sending again."
			if execution != nil {
				execution.Status = issueExecutionUnknown
				execution.Detail = issue.Work.SendError
			}
			if batch != nil {
				if !issue.Supplements.Unknown(batch, issue.Work.SendError, a.persist) {
					_ = a.persist()
				}
			} else {
				_ = a.persist()
			}
		default:
			if issue.Supplements != nil {
				issue.Supplements.AutoContinue = false
			}
			if issue.Status != issueStatusCanceled && issue.Status != issueStatusCompleted && !issue.Archived {
				issue.Status = issueStatusNeedsAttention
				issue.UpdatedAt = nowMillis()
			}
			issue.Work.SendState = sendIdle
			issue.Work.SendError = detail
			if execution != nil {
				execution.Status = issueExecutionNotDispatched
				execution.Detail = detail
			}
			if batch != nil {
				if !issue.Supplements.NotDispatched(batch, detail, a.persist) {
					_ = a.persist()
				}
			} else {
				_ = a.persist()
			}
		}
	})
}

func (a *appModel) sendRuntime(ct *contact, text string) bool {
	return a.sendRuntimeForIssue(ct, text, "")
}

func (a *appModel) sendRuntimeForIssue(ct *contact, text, issueExecutionID string) bool {
	if reason := a.canSend(ct); reason != "" {
		ct.SendError = reason
		// A rejected send must not consume or replace an in-flight issue
		// association; only an admitted dispatch may change it.
		return false
	}
	previousExecution := ct.pendingIssueExecution
	ct.pendingIssueExecution = issueExecutionID
	contactID := ct.ID
	sessionID := ct.SessionID
	workspace := a.effectiveContactWorkspace(ct)
	system := a.issueToolConversationSystemPrompt(ct)
	submittedContext := ct.Context.snapshot()
	ct.historyGen++ // invalidate any in-flight reload
	ct.SubmittedDraft = text
	ct.SubmittedRev = ct.draftRev
	ct.pendingSend = &pendingSend{Draft: text, Context: cloneTaskContextItems(submittedContext), State: "submitting"}
	ct.SendState = sendSubmitting
	ct.SendError = ""
	if err := a.persist(); err != nil {
		ct.SendState = sendIdle
		ct.SendError = "Could not save this contact; the message was not sent. " + err.Error()
		ct.pendingIssueExecution = previousExecution
		ct.pendingSend = nil
		return false
	}
	rt, configured := a.rt, a.connectionManaged
	issueTools := a.issueToolEnabled() && a.connectionManaged
	go a.submitTask(rt, configured, contactID, sessionID, text, workspace, system, submittedContext, newIssueToolTurnID(), issueTools)
	return true
}

func (a *appModel) submitTask(rt *runtimeClient, configured bool, contactID, sessionID, text, workspace, system string, context []taskContextItem, turnID string, issueTools bool) {
	createdSession := false
	if sessionID == "" {
		session, err := a.configuredConversationCreate(rt, configured, workspace, system)
		if err != nil {
			a.failSubmit(contactID, err, false)
			return
		}
		sessionID = session.SessionID
		createdSession = true
		// The mapping must be durably saved before task.send; Window.Update is
		// asynchronous, so wait for the UI callback's save result.
		saved := make(chan error, 1)
		a.updateUI(func() {
			ct := a.contactByID(contactID)
			if ct == nil {
				saved <- errors.New("contact disappeared before saving its session")
				return
			}
			ct.SessionID = sessionID
			ct.Loaded = true
			ct.SessionState = session.State
			if _, execution := ct.executionByID(ct.pendingIssueExecution); execution != nil {
				execution.SessionID = sessionID
			}
			if session.Metadata != nil {
				ct.SessionWorkingDir = session.Metadata.WorkingDirectory
				ct.SessionModel = session.Metadata.Model
				ct.SessionAPI = session.Metadata.API
			}
			saved <- a.persist()
		})
		if err := <-saved; err != nil {
			a.failSubmit(contactID, fmt.Errorf("save session mapping: %w", err), false)
			return
		}
	}
	wireContext := taskContextInputs(context)
	if issueTools {
		configuredTools, err := a.issueToolEnsureConversationSession(rt, sessionID, issueToolConversationGuidance, createdSession)
		if err != nil {
			a.failSubmit(contactID, err, false)
			return
		}
		issueTools = configuredTools
	}
	if issueTools {
		var scope *IssueToolScope
		var scopeErr error
		a.runOnUI(func() {
			if a.shutdownStarted {
				scopeErr = errors.New("app is shutting down")
				return
			}
			scope, scopeErr = a.issueToolNewScope(contactID, sessionID, turnID)
		})
		if scopeErr != nil {
			a.failSubmit(contactID, fmt.Errorf("prepare issue tool scope: %w", scopeErr), false)
			return
		}
		a.runOnUI(func() {
			if err := a.issueToolAttachPendingScope(contactID, scope.Ref); err != nil {
				scopeErr = fmt.Errorf("attach issue tool scope: %w", err)
			}
		})
		if scopeErr != nil {
			a.failSubmit(contactID, scopeErr, false)
			return
		}
		if input, ok := issueToolScopeInput(scope); ok {
			wireContext = append(wireContext, input)
		}
	}
	if err := validateRuntimeContextInputs(wireContext); err != nil {
		a.failSubmit(contactID, fmt.Errorf("caller context plus issue tool scope: %w", err), false)
		return
	}
	result, err := rt.sendTask(sessionID, text, wireContext...)
	if err != nil {
		a.noteIssueToolTaskError(rt, sessionID, err)
		a.failSubmit(contactID, err, true)
		return
	}
	a.updateUI(func() {
		if a.rt != rt {
			return
		}
		ct := a.contactByID(contactID)
		if ct == nil || ct.SessionID != sessionID {
			return
		}
		a.acceptTask(ct, text, result)
	})
}

func (a *appModel) acceptTask(ct *contact, text string, result runtimeSendResult) {
	var submittedContext []taskContextItem
	scopeRef := ""
	if ct.pendingSend != nil {
		submittedContext = cloneTaskContextItems(ct.pendingSend.Context)
		scopeRef = ct.pendingSend.ToolScopeRef
	}
	ct.historyGen++
	ct.ActiveTaskID = result.TaskID
	ct.LastTaskID = result.TaskID
	ct.SendState = sendAccepted
	ct.SendError = ""
	a.issueToolBindTask(scopeRef, result.TaskID)
	issueSend := ct.pendingIssueExecution != ""
	if w := a.ensureWork(ct, result.TaskID, result.MessageID); w != nil {
		w.State = workAccepted
		w.Prompt = text
	}
	_, execution := ct.executionByID(ct.pendingIssueExecution)
	if execution != nil && execution.ToolScopeRef != "" {
		a.issueToolBindTask(execution.ToolScopeRef, result.TaskID)
	}
	if execution != nil {
		execution.SessionID = ct.SessionID
		execution.TaskID = result.TaskID
		execution.MessageID = result.MessageID
		execution.Status = issueExecutionAccepted
		execution.Detail = ""
		ct.pendingIssueExecution = ""
	}
	if !issueSend && ct.draftRev == ct.SubmittedRev && strings.TrimSpace(ct.Draft) == strings.TrimSpace(text) {
		ct.Draft = ""
	}
	ct.Messages = append(ct.Messages, &message{
		Key: "live:" + result.TaskID + ":user", Kind: msgUser, Text: text,
		Time: nowTime(), TaskID: result.TaskID, MessageID: result.MessageID,
		Context: submittedContext,
	})
	pending := ct.pendingEvents
	ct.pendingEvents = nil
	for _, event := range pending {
		a.applyEvent(ct, event)
	}
	if err := a.persist(); err != nil {
		if execution != nil {
			execution.Status = issueExecutionUnknown
			execution.Detail = "Task was accepted, but its issue association could not be saved: " + err.Error()
		}
		if ct.pendingSend != nil {
			ct.pendingSend.State = "unknown"
		}
		ct.SendError = "Task accepted, but its issue association could not be saved; reload before retrying."
	} else {
		needSave := ct.pendingSend != nil
		ct.pendingSend = nil
		previous := ct.Context.snapshot()
		if ct.Context.clearSubmitted(submittedContext) > 0 {
			needSave = true
		}
		if needSave {
			if err := a.persist(); err != nil {
				ct.Context.items = previous
				ct.SendError = "Task accepted, but its sent context could not be cleared from the next-message draft; reload before sending again."
			}
		}
	}
}

func (a *appModel) failSubmit(contactID string, err error, submitted bool) {
	a.updateUI(func() {
		ct := a.contactByID(contactID)
		if ct == nil {
			return
		}
		ct.pendingEvents = nil
		if confirmedReject(err) {
			ct.pendingSend = nil
			ct.SendState = sendIdle
			ct.SendError = a.connectionRuntimeMessage(err)
			a.updatePendingIssueExecution(ct, issueExecutionRejected, ct.SendError)
			_ = a.persist()
			return
		}
		ct.SendError = a.connectionRuntimeMessage(err)
		if submitted {
			if ct.pendingSend == nil {
				ct.pendingSend = &pendingSend{Draft: ct.SubmittedDraft, Context: nil, State: "unknown"}
			}
			ct.pendingSend.State = "unknown"
			ct.SendState = sendUnknown
			ct.SendError = "Delivery not confirmed (" + ct.SendError + "). Reload to check before sending again."
			a.updatePendingIssueExecution(ct, issueExecutionUnknown, ct.SendError)
			_ = a.persist()
			return
		}
		ct.pendingSend = nil
		ct.SendState = sendIdle
		a.updatePendingIssueExecution(ct, issueExecutionNotDispatched, ct.SendError)
		_ = a.persist()
	})
}

func (a *appModel) cancelSelected() {
	ct := a.selectedContact()
	if a.rt == nil {
		return
	}
	if ct.ActiveTaskID == "" {
		if issue := ct.selectedIssue(); issue != nil && issue.Work.ActiveTaskID != "" {
			a.cancelIssue(issue.ID)
		}
		return
	}
	contactID, sessionID, taskID := ct.ID, ct.SessionID, ct.ActiveTaskID
	if w := ct.activeWork(); w != nil {
		w.State = workStopping
		w.StopRequested = true
		if ct.StopRequestedTasks == nil {
			ct.StopRequestedTasks = map[string]bool{}
		}
		ct.StopRequestedTasks[taskID] = true
	}
	ct.SendError = "Cancellation requested"
	rt := a.rt
	go func() {
		status, err := rt.cancelTask(sessionID, taskID)
		a.updateUI(func() {
			if a.rt != rt {
				return
			}
			a.applyCancelResult(contactID, taskID, status, err)
		})
	}()
}

func (a *appModel) cancelIssue(issueID string) bool {
	return a.cancelIssueFor(a.selectedContact(), issueID)
}

func (a *appModel) cancelIssueFor(ct *contact, issueID string) bool {
	if a.rt == nil || ct == nil {
		return false
	}
	issue := ct.issueByID(issueID)
	if issue == nil || issue.Work.ActiveTaskID == "" || issue.WorkSessionID == "" {
		return false
	}
	if ledger := issue.Supplements; ledger != nil && ledger.AutoContinue {
		target := issueSupplementTargetFor(ct, issue)
		active := ledger.activeBatch(target)
		if !ledger.StopAutoContinue(active, a.persist) {
			// A local intent-save failure must not erase the real Stop request.
			ledger.AutoContinue = false
			issue.Work.SendError = "Supplement automation was stopped in memory; its intent could not be saved. Reload before relying on automatic continuation."
		}
	}
	taskID, sessionID := issue.Work.ActiveTaskID, issue.WorkSessionID
	if w := workFor(ct, taskID); w != nil {
		w.State = workStopping
		w.StopRequested = true
	}
	if issue.Work.StopRequestedTasks == nil {
		issue.Work.StopRequestedTasks = map[string]bool{}
	}
	issue.Work.StopRequestedTasks[taskID] = true
	issue.Work.SendError = "Cancellation requested"
	go func() {
		status, err := a.rt.cancelTask(sessionID, taskID)
		a.updateUI(func() { a.applyIssueCancelResult(ct.ID, issueID, taskID, status, err) })
	}()
	return true
}

func (a *appModel) applyIssueCancelResult(contactID, issueID, taskID, status string, err error) {
	ct := a.contactByID(contactID)
	if ct == nil {
		return
	}
	issue := ct.issueByID(issueID)
	if issue == nil {
		return
	}
	if err != nil {
		if issue.Work.ActiveTaskID == taskID {
			if w := workFor(ct, taskID); w != nil && w.State == workStopping {
				w.State = workRunning
			}
			issue.Work.SendError = "Cancel failed: " + a.connectionRuntimeMessage(err)
		}
		return
	}
	if issue.Work.ActiveTaskID != taskID {
		return
	}
	switch status {
	case "requested", "already_requested":
		if w := workFor(ct, taskID); w != nil {
			w.State = workStopping
			w.StopRequested = true
		}
		issue.Work.SendError = "Cancelling; waiting for the runtime to stop."
	case "not_running":
		issue.Work.SendError = "The task already finished; waiting for its final state."
	}
}

func (a *appModel) applyCancelResult(contactID, taskID, status string, err error) {
	ct := a.contactByID(contactID)
	if ct == nil {
		return
	}
	if err != nil {
		if ct.ActiveTaskID == taskID {
			if w := ct.activeWork(); w != nil && w.State == workStopping {
				w.State = workRunning
			}
			ct.SendError = "Cancel failed: " + a.connectionRuntimeMessage(err)
		}
		return
	}
	if ct.ActiveTaskID != taskID {
		// The task already finished, or a different task now owns the contact;
		// a late cancel reply must not rewrite that state.
		return
	}
	switch status {
	case "requested":
		if w := ct.activeWork(); w != nil {
			w.State = workStopping
			w.StopRequested = true
			if ct.StopRequestedTasks == nil {
				ct.StopRequestedTasks = map[string]bool{}
			}
			ct.StopRequestedTasks[taskID] = true
		}
		ct.SendError = "Cancelling; waiting for the runtime to stop."
	case "already_requested":
		if w := ct.activeWork(); w != nil {
			w.State = workStopping
			w.StopRequested = true
			if ct.StopRequestedTasks == nil {
				ct.StopRequestedTasks = map[string]bool{}
			}
			ct.StopRequestedTasks[taskID] = true
		}
		ct.SendError = "Cancellation already requested."
	case "not_running":
		ct.SendError = "The task already finished; waiting for its final state."
	}
}

func (a *appModel) reloadSelected() {
	ct := a.selectedContact()
	if a.rt == nil || a.runtimeStatus != runtimeStateReady {
		ct.SendError = "Runtime disconnected; restart Saki to reload history."
		return
	}
	if ct.SessionID != "" {
		a.reloadContactByID(ct.ID, true)
	}
	if issue := ct.selectedIssue(); issue != nil && issue.WorkSessionID != "" {
		a.reloadIssueWorkByID(ct.ID, issue.ID)
	}
}

func (a *appModel) addRuntimeContact() {
	if !a.leaveIssueFormForSwitch() {
		return
	}
	c := &contact{
		ID:        newContactID(),
		Name:      fmt.Sprintf("New contact %d", len(a.contacts)+1),
		Role:      "Assistant",
		Status:    statusReady,
		WorkLabel: "Preparing a reply",
		Workspace: a.workDir,
	}
	c.Context.onChange = a.persist
	a.contacts = append(a.contacts, c)
	a.selected = len(a.contacts) - 1
	a.contactScope = "active"
	a.contactSearch = ""
	a.screen = viewChat
	a.aux = ""
	a.focusComposer = true
	a.persist()
}

func profileSystemPrompt(ct *contact) string {
	var parts []string
	if strings.TrimSpace(ct.Purpose) != "" {
		parts = append(parts, "Purpose: "+strings.TrimSpace(ct.Purpose))
	}
	if strings.TrimSpace(ct.Instructions) != "" {
		parts = append(parts, "Instructions: "+ct.Instructions)
	}
	return strings.Join(parts, "\n")
}

func (a *appModel) effectiveContactWorkspace(ct *contact) string {
	if ct != nil && ct.Workspace != "" {
		return ct.Workspace
	}
	return a.workDir
}

func (a *appModel) selectedID() string {
	if a.selected < 0 || a.selected >= len(a.contacts) {
		return ""
	}
	return a.contacts[a.selected].ID
}

func (a *appModel) persist() error {
	if a.store == nil {
		return nil
	}
	a.store.setIssueToolState(a.issueToolLedger, a.issueToolScopes)
	a.store.state.Appearance = normalizeAppearanceMode(a.appearanceMode)
	a.store.state.Layout = &storedLayout{
		LeftWidth:     a.railWidth(),
		RightWidth:    a.workWidth(),
		WorkspaceOpen: a.workspaceOpen,
	}
	if err := a.store.save(a.contacts, a.selectedID()); err != nil {
		message := "Could not save contacts: " + err.Error()
		if a.localSaveErr == "" {
			a.localSavePrevErr = a.runtimeErr
		}
		a.localSaveErr = message
		a.runtimeErr = message
		return err
	}
	if a.localSaveErr != "" {
		if a.runtimeErr == a.localSaveErr {
			a.runtimeErr = a.localSavePrevErr
		}
		a.localSaveErr = ""
		a.localSavePrevErr = ""
	}
	if a.appearanceError != "" {
		a.appearanceError = ""
		a.appearanceStatus = "Applied and saved for the workbench."
	}
	return nil
}

func (a *appModel) draftChanged(ct *contact) {
	ct.draftRev++
	a.persist()
}
