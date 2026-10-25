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
	workDir := guiWorkDir()
	store, err := openContactStore(storePath, workDir)
	if err != nil {
		return nil, err
	}
	a := &appModel{
		store:         store,
		storePath:     storePath,
		sessionDir:    guiSessionDir(storePath),
		workDir:       workDir,
		runtimeStatus: runtimeStateStarting,
		workspaceOpen: true,
		scrollTo:      -1,
	}
	for _, stored := range store.state.Contacts {
		workspace := stored.Workspace
		if workspace == "" {
			workspace = workDir
		}
		ct := &contact{
			ID:           stored.ID,
			Name:         stored.Name,
			Role:         stored.Role,
			Purpose:      stored.Purpose,
			Instructions: stored.Instructions,
			Workspace:    workspace,
			SessionID:    stored.SessionID,
			Draft:        stored.Draft,
			Status:       statusReady,
			WorkLabel:    "Preparing a reply",
		}
		ct.Issues = issuesForStoredContact(store.state.Issues, ct.ID)
		if issue := ct.selectedIssue(); issue != nil {
			ct.SelectedIssueID = issue.ID
		}
		a.contacts = append(a.contacts, ct)
	}
	for i, ct := range a.contacts {
		if ct.ID == store.state.Selected {
			a.selected = i
		}
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
		workspaceOpen: true,
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
	if a.demo || a.store == nil || a.rt != nil {
		return
	}
	binary := findRuntimeBinary()
	if binary == "" {
		a.runtimeStatus = runtimeStateFailed
		a.runtimeStage = "Runtime helper"
		a.runtimeErr = "Runtime helper not found."
		a.runtimeHint = a.runtimeHelperHint()
		return
	}
	a.runtimeBinary = binary
	rt, err := startRuntime(runtimeOptions{
		Binary: binary,
		Dir:    a.workDir,
		Env:    a.childEnv(),
		Handlers: runtimeHandlers{
			OnReady: func(ready runtimeReady) {
				a.updateUI(func() {
					a.runtimeStatus = runtimeStateReady
					a.runtimeErr = ""
					a.runtimeStage = ""
					a.runtimeHint = ""
					a.runtimeAPI = ready.API
					a.runtimeHandshake = true
					a.restoreContacts()
					a.restoreIssueWorks()
					go a.loadCapabilities()
				})
			},
			OnEvent: func(event runtimeEventEnvelope) {
				a.updateUI(func() { a.applyRuntimeEvent(event) })
			},
			OnExit: func(err error) {
				a.updateUI(func() { a.applyRuntimeExit(err) })
			},
			OnProtocolError: func(message string) {
				a.updateUI(func() { a.protocolFailed(message) })
			},
			OnStderr: func(string) {},
		},
	})
	if err != nil {
		a.runtimeStatus = runtimeStateFailed
		a.runtimeStage = "Runtime start"
		a.runtimeErr = "Could not start runtime: " + safeDiagnostic(err.Error())
		a.runtimeHint = "Check the helper path and permissions at " + binary + ", then restart Saki."
		return
	}
	a.rt = rt
}

func (a *appModel) protocolFailed(message string) {
	a.runtimeStatus = runtimeStateFailed
	a.runtimeStage = "Runtime protocol"
	a.runtimeErr = "Runtime protocol error: " + safeDiagnostic(message)
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
			diagnostic = safeDiagnostic(lines[len(lines)-1])
		}
	}
	wasReady := a.runtimeHandshake
	a.runtimeStatus = runtimeStateExited
	if wasReady {
		a.runtimeStage = "Runtime connection"
		a.runtimeErr = "Runtime connection lost"
		if err != nil {
			a.runtimeErr += ": " + safeDiagnostic(err.Error())
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
				a.runtimeErr = "Runtime exited before ready: " + safeDiagnostic(err.Error())
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
	a.persist()
	if a.rt == nil {
		a.quitReady = true
		if a.requestQuit != nil {
			a.requestQuit()
		}
		return
	}
	a.runtimeStatus = runtimeStateDraining
	a.runtimeErr = ""
	go func() {
		err := a.rt.shutdown()
		a.updateUI(func() {
			a.quitReady = true
			if err != nil {
				a.runtimeErr = "Runtime shutdown: " + err.Error()
			}
			if a.requestQuit != nil {
				a.requestQuit()
			}
		})
	}()
}

func (a *appModel) updateUI(fn func()) {
	if a.uiHook != nil {
		a.uiHook(fn)
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

func (a *appModel) loadCapabilities() {
	if a.rt == nil {
		return
	}
	capabilities, err := a.rt.capabilities()
	a.updateUI(func() {
		if err != nil {
			a.runtimeCapabilitiesLoaded = false
			a.runtimeCapabilitiesErr = "Runtime capacity is not loaded: " + runtimeMessage(err) + ". The app uses the conservative single-slot behavior until it is retried."
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
	})
}

func (a *appModel) capacityKnown() bool {
	return a.demo || a.runtimeCapabilitiesLoaded
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
			if issue.WorkSessionID != "" || issue.Work.SessionID != "" || sessionBusy(issue.Work.SendState, issue.Work.ActiveTaskID) {
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

func (a *appModel) restoreIssueWorks() {
	for _, ct := range a.contacts {
		for _, issue := range ct.Issues {
			if issue.Work.SessionID == "" {
				continue
			}
			contactID, issueID, sessionID := ct.ID, issue.ID, issue.Work.SessionID
			issue.Work.Loaded = false
			go a.restoreIssueWork(contactID, issueID, sessionID)
		}
	}
}

func (a *appModel) restoreContacts() {
	for _, ct := range a.contacts {
		if ct.SessionID == "" {
			continue
		}
		contactID, sessionID := ct.ID, ct.SessionID
		ct.Loaded = false
		go a.restoreSession(contactID, sessionID)
	}
}

func (a *appModel) restoreSession(contactID, sessionID string) {
	session, err := a.rt.loadSession(sessionID)
	if err != nil {
		a.updateUI(func() {
			ct := a.contactByID(contactID)
			if ct == nil || ct.SessionID != sessionID {
				return
			}
			ct.Loaded = false
			ct.LoadErr = runtimeMessage(err)
			ct.Unavailable = runtimeMessage(err)
			ct.Status = statusAttention
			ct.StatusDetail = runtimeMessage(err)
		})
		return
	}
	pages, status, err := a.historyAll(sessionID)
	a.updateUI(func() {
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
			ct.LoadErr = runtimeMessage(err)
			ct.Status = statusAttention
			ct.StatusDetail = runtimeMessage(err)
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

func (a *appModel) restoreIssueWork(contactID, issueID, sessionID string) {
	session, err := a.rt.loadSession(sessionID)
	if err != nil {
		a.updateUI(func() {
			ct := a.contactByID(contactID)
			if ct == nil {
				return
			}
			issue := ct.issueByID(issueID)
			if issue == nil || issue.Work.SessionID != sessionID {
				return
			}
			issue.Work.Loaded = false
			issue.Work.LoadErr = runtimeMessage(err)
			issue.Work.SendError = runtimeMessage(err)
		})
		return
	}
	pages, status, err := a.historyAll(sessionID)
	a.updateUI(func() {
		ct := a.contactByID(contactID)
		if ct == nil {
			return
		}
		issue := ct.issueByID(issueID)
		if issue == nil || issue.Work.SessionID != sessionID {
			return
		}
		issue.Work.Loaded = err == nil
		issue.Work.LoadErr = ""
		issue.Work.SessionState = session.State
		if err != nil {
			issue.Work.LoadErr = runtimeMessage(err)
			issue.Work.SendError = runtimeMessage(err)
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
			return
		}
		issue.Work.ActiveTaskID = ""
		issue.Work.SendState = sendIdle
		issue.Work.SendError = ""
	})
}

func (a *appModel) reloadIssueWorkByID(contactID, issueID string) {
	if a.rt == nil {
		return
	}
	ct := a.contactByID(contactID)
	if ct == nil {
		return
	}
	issue := ct.issueByID(issueID)
	if issue == nil || issue.Work.SessionID == "" {
		return
	}
	issue.Work.historyGen++
	gen := issue.Work.historyGen
	sessionID := issue.Work.SessionID
	go a.reloadIssueWork(contactID, issueID, sessionID, gen)
}

func (a *appModel) reloadIssueWork(contactID, issueID, sessionID string, gen int) {
	session, err := a.rt.loadSession(sessionID)
	if err != nil {
		a.updateUI(func() {
			ct := a.contactByID(contactID)
			if ct == nil {
				return
			}
			issue := ct.issueByID(issueID)
			if issue == nil || issue.Work.SessionID != sessionID || issue.Work.historyGen != gen {
				return
			}
			issue.Work.LoadErr = runtimeMessage(err)
			issue.Work.SendError = runtimeMessage(err)
		})
		return
	}
	pages, status, err := a.historyAll(sessionID)
	a.updateUI(func() {
		ct := a.contactByID(contactID)
		if ct == nil {
			return
		}
		issue := ct.issueByID(issueID)
		if issue == nil || issue.Work.SessionID != sessionID || issue.Work.historyGen != gen {
			return
		}
		if err != nil {
			issue.Work.LoadErr = runtimeMessage(err)
			issue.Work.SendError = runtimeMessage(err)
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
	})
}

func (a *appModel) reloadContactByID(contactID string) {
	if a.rt == nil {
		return
	}
	ct := a.contactByID(contactID)
	if ct == nil || ct.SessionID == "" {
		return
	}
	ct.historyGen++
	gen := ct.historyGen
	sessionID := ct.SessionID
	go a.reloadSession(contactID, sessionID, gen)
}

func (a *appModel) reloadSession(contactID, sessionID string, gen int) {
	session, err := a.rt.loadSession(sessionID)
	if err != nil {
		a.updateUI(func() {
			ct := a.contactByID(contactID)
			if ct == nil || ct.SessionID != sessionID || ct.historyGen != gen {
				return
			}
			ct.LoadErr = runtimeMessage(err)
			ct.SendError = runtimeMessage(err)
		})
		return
	}
	pages, status, err := a.historyAll(sessionID)
	a.updateUI(func() { a.applyReload(contactID, sessionID, gen, session, pages, status, err) })
}

func (a *appModel) applyReload(contactID, sessionID string, gen int, session runtimeSession, pages []historyRecord, status string, err error) {
	ct := a.contactByID(contactID)
	if ct == nil || ct.SessionID != sessionID || ct.historyGen != gen {
		return // a newer submit/task superseded this reload
	}
	if err != nil {
		ct.LoadErr = runtimeMessage(err)
		ct.SendError = runtimeMessage(err)
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
	// terminal event) this is where reload may recover, but only after history
	// was fetched successfully. Never re-send.
	a.applyHistory(ct, pages, status)
	ct.ActiveTaskID = ""
	ct.SendState = sendIdle
	ct.SendError = ""
	ct.LoadErr = ""
}

func (a *appModel) historyAll(sessionID string) ([]historyRecord, string, error) {
	offset := 0
	var all []historyRecord
	status := ""
	for page := 0; page < 1000; page++ {
		result, err := a.rt.historyPage(sessionID, offset, 100)
		if err != nil {
			return nil, "", err
		}
		all = append(all, result.Records...)
		status = result.Status
		if result.NextOffset == nil {
			return all, status, nil
		}
		if *result.NextOffset <= offset {
			return nil, "", errors.New("history pagination did not advance")
		}
		offset = *result.NextOffset
	}
	return nil, "", errors.New("history pagination exceeded 1000 pages")
}

type historyProjection struct {
	messages []*message
	entries  []historyEntry
	works    []*workItem
	unlinked []*workTool
	detail   string
}

func parseHistory(sessionID string, records []historyRecord, stopRequested map[string]bool) historyProjection {
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
					text := inputText(item.Content)
					if text == "" {
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
					})
					projection.entries = append(projection.entries, historyEntry{ID: key, Date: timeLabel, From: from, Text: text})
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
	projection := parseHistory(ct.SessionID, records, ct.StopRequestedTasks)
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
	sessionID := issue.Work.SessionID
	projection := parseHistory(sessionID, records, issue.Work.StopRequestedTasks)
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
	issue.Work.HistoryDirty = false
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

func (a *appModel) routeSession(sessionID string) (*contact, *productIssue, bool) {
	for _, ct := range a.contacts {
		if ct.SessionID == sessionID {
			return ct, nil, false
		}
		for _, issue := range ct.Issues {
			if issue.Work.SessionID == sessionID || issue.WorkSessionID == sessionID {
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
		detail := taskFinishedDetail(payload)
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
		_ = a.persist()
		a.reloadIssueWorkByID(ct.ID, issue.ID)
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
		ct.StatusDetail = taskFinishedDetail(payload)
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
		a.reloadContactByID(ct.ID)
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
	if a.demo {
		return ""
	}
	if a.rt == nil || a.runtimeStatus != runtimeStateReady {
		return "Runtime is not ready."
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
	if a.demo {
		return ""
	}
	if a.rt == nil || a.runtimeStatus != runtimeStateReady {
		return "Runtime is not ready."
	}
	if sessionBusy(issue.Work.SendState, issue.Work.ActiveTaskID) {
		return "This issue already has an active execution."
	}
	if issue.WorkSessionID != "" && !issue.Work.Loaded {
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
	if a.rt == nil || a.runtimeStatus != runtimeStateReady {
		issue.Work.SendError = "Runtime is not ready."
		return false
	}
	// Capture immutable session inputs on the UI thread. The worker must not
	// look up the contact/issue or read mutable profile/session fields later.
	workspace := ct.Workspace
	system := profileSystemPrompt(ct)
	sessionID := issue.Work.SessionID
	go a.submitIssueTask(ct.ID, issue.ID, execution.ID, sessionID, workspace, system, prompt)
	return true
}

func (a *appModel) submitIssueTask(contactID, issueID, executionID, sessionID, workspace, system, prompt string) {
	if sessionID == "" {
		session, err := a.rt.createSession(workspace, system)
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
			if issue.Work.SessionID != "" && issue.Work.SessionID != sessionID {
				saved <- errors.New("issue work session changed before it was saved")
				return
			}
			issue.Work.SessionID = sessionID
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
	result, err := a.rt.sendTask(sessionID, prompt)
	if err != nil {
		a.failIssueSubmit(contactID, issueID, err, true)
		return
	}
	a.updateUI(func() {
		ct := a.contactByID(contactID)
		if ct == nil {
			return
		}
		issue := ct.issueByID(issueID)
		if issue == nil || issue.Work.SessionID != sessionID {
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
		execution.SessionID = issue.Work.SessionID
		execution.TaskID = result.TaskID
		execution.MessageID = result.MessageID
		execution.Status = issueExecutionAccepted
		execution.Detail = ""
	}
	issue.Work.pendingExecutionID = ""
	if w := a.ensureWork(ct, result.TaskID, result.MessageID); w != nil {
		w.State = workAccepted
		w.Prompt = prompt
	}
	pending := issue.Work.pendingEvents
	issue.Work.pendingEvents = nil
	for _, event := range pending {
		a.applyIssueEvent(ct, issue, event)
	}
	if err := a.persist(); err != nil {
		if execution != nil {
			execution.Status = issueExecutionUnknown
			execution.Detail = "Task was accepted, but its work-session association could not be saved: " + err.Error()
		}
		issue.Work.SendError = "Task accepted, but its issue association could not be saved; reload before retrying."
	}
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
		switch {
		case confirmedReject(err):
			issue.Work.SendState = sendIdle
			issue.Work.SendError = runtimeMessage(err)
			if execution != nil {
				execution.Status = issueExecutionRejected
				execution.Detail = issue.Work.SendError
			}
		case submitted:
			issue.Work.SendState = sendUnknown
			issue.Work.SendError = "Delivery not confirmed (" + runtimeMessage(err) + "). Reload to check before sending again."
			if execution != nil {
				execution.Status = issueExecutionUnknown
				execution.Detail = issue.Work.SendError
			}
		default:
			issue.Work.SendState = sendIdle
			issue.Work.SendError = runtimeMessage(err)
			if execution != nil {
				execution.Status = issueExecutionNotDispatched
				execution.Detail = issue.Work.SendError
			}
		}
		_ = a.persist()
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
	workspace := ct.Workspace
	system := profileSystemPrompt(ct)
	ct.historyGen++ // invalidate any in-flight reload
	ct.SubmittedDraft = text
	ct.SubmittedRev = ct.draftRev
	ct.SendState = sendSubmitting
	ct.SendError = ""
	if err := a.persist(); err != nil {
		ct.SendState = sendIdle
		ct.SendError = "Could not save this contact; the message was not sent. " + err.Error()
		ct.pendingIssueExecution = previousExecution
		return false
	}
	go a.submitTask(contactID, sessionID, text, workspace, system)
	return true
}

func (a *appModel) submitTask(contactID, sessionID, text, workspace, system string) {
	if sessionID == "" {
		session, err := a.rt.createSession(workspace, system)
		if err != nil {
			a.failSubmit(contactID, err, false)
			return
		}
		sessionID = session.SessionID
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
	result, err := a.rt.sendTask(sessionID, text)
	if err != nil {
		a.failSubmit(contactID, err, true)
		return
	}
	a.updateUI(func() {
		ct := a.contactByID(contactID)
		if ct == nil || ct.SessionID != sessionID {
			return
		}
		a.acceptTask(ct, text, result)
	})
}

func (a *appModel) acceptTask(ct *contact, text string, result runtimeSendResult) {
	ct.historyGen++
	ct.ActiveTaskID = result.TaskID
	ct.LastTaskID = result.TaskID
	ct.SendState = sendAccepted
	ct.SendError = ""
	issueSend := ct.pendingIssueExecution != ""
	if w := a.ensureWork(ct, result.TaskID, result.MessageID); w != nil {
		w.State = workAccepted
		w.Prompt = text
	}
	_, execution := ct.executionByID(ct.pendingIssueExecution)
	if execution != nil {
		execution.SessionID = ct.SessionID
		execution.TaskID = result.TaskID
		execution.MessageID = result.MessageID
		execution.Status = issueExecutionAccepted
		execution.Detail = ""
		ct.SelectedWorkTaskID = result.TaskID
		ct.pendingIssueExecution = ""
	}
	if !issueSend && ct.draftRev == ct.SubmittedRev && strings.TrimSpace(ct.Draft) == strings.TrimSpace(text) {
		ct.Draft = ""
	}
	ct.Messages = append(ct.Messages, &message{
		Key: "live:" + result.TaskID + ":user", Kind: msgUser, Text: text,
		Time: nowTime(), TaskID: result.TaskID, MessageID: result.MessageID,
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
		ct.SendError = "Task accepted, but its issue association could not be saved; reload before retrying."
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
			ct.SendState = sendIdle
			ct.SendError = runtimeMessage(err)
			a.updatePendingIssueExecution(ct, issueExecutionRejected, ct.SendError)
			_ = a.persist()
			return
		}
		ct.SendError = runtimeMessage(err)
		if submitted {
			ct.SendState = sendUnknown
			ct.SendError = "Delivery not confirmed (" + ct.SendError + "). Reload to check before sending again."
			a.updatePendingIssueExecution(ct, issueExecutionUnknown, ct.SendError)
			_ = a.persist()
			return
		}
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
	go func() {
		status, err := a.rt.cancelTask(sessionID, taskID)
		a.updateUI(func() { a.applyCancelResult(contactID, taskID, status, err) })
	}()
}

func (a *appModel) cancelIssue(issueID string) bool {
	ct := a.selectedContact()
	if a.rt == nil {
		return false
	}
	issue := ct.issueByID(issueID)
	if issue == nil || issue.Work.ActiveTaskID == "" || issue.Work.SessionID == "" {
		return false
	}
	taskID, sessionID := issue.Work.ActiveTaskID, issue.Work.SessionID
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
			issue.Work.SendError = "Cancel failed: " + runtimeMessage(err)
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
			ct.SendError = "Cancel failed: " + runtimeMessage(err)
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
		a.reloadContactByID(ct.ID)
	}
	if issue := ct.selectedIssue(); issue != nil && issue.Work.SessionID != "" {
		a.reloadIssueWorkByID(ct.ID, issue.ID)
	}
}

func (a *appModel) profileLocked(ct *contact) bool {
	if a.demo {
		return false
	}
	if ct.SessionID != "" || ct.SendState == sendSubmitting {
		return true
	}
	for _, issue := range ct.Issues {
		if issue.Work.SessionID != "" || issue.Work.SendState == sendSubmitting || issue.Work.ActiveTaskID != "" {
			return true
		}
	}
	return false
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
	a.contacts = append(a.contacts, c)
	a.selected = len(a.contacts) - 1
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
		parts = append(parts, "Instructions: "+strings.TrimSpace(ct.Instructions))
	}
	return strings.Join(parts, "\n")
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
	if err := a.store.save(a.contacts, a.selectedID()); err != nil {
		a.runtimeErr = "Could not save contacts: " + err.Error()
		return err
	}
	return nil
}

func (a *appModel) draftChanged(ct *contact) {
	ct.draftRev++
	a.persist()
}
