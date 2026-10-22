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
		scrollTo:      -1,
	}
	for _, stored := range store.state.Contacts {
		workspace := stored.Workspace
		if workspace == "" {
			workspace = workDir
		}
		a.contacts = append(a.contacts, &contact{
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
		})
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
		storePath:  storePath,
		sessionDir: guiSessionDir(storePath),
		workDir:    workDir,
		scrollTo:   -1,
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
			ct.SessionState = "unknown"
			ct.SendState = sendUnknown
			ct.Status = statusAttention
			ct.SendError = "Protocol failure; delivery not confirmed. Reload before sending again."
		}
	}
}

func (a *appModel) clearActiveTasksAfterExit() {
	for _, ct := range a.contacts {
		if ct.ActiveTaskID != "" {
			ct.ActiveTaskID = ""
			ct.SessionState = "unknown"
			ct.SendState = sendUnknown
			ct.Status = statusAttention
			ct.StatusDetail = "Connection lost before the task finished; reload to verify."
			ct.SendError = "Delivery/result not confirmed. Reload before sending again."
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
			ct.SendState = sendAccepted
			ct.Status = statusWorking
			ct.Work = "Resuming"
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

func (a *appModel) applyHistory(ct *contact, records []historyRecord, status string) {
	var messages []*message
	var entries []historyEntry
	taskID, messageID := "", ""
	detail := ""
	for _, rec := range records {
		switch rec.Record.Type {
		case "task_started":
			taskID, messageID = rec.Record.TaskID, rec.Record.MessageID
		case "task_finished":
			taskID, messageID = "", ""
			switch rec.Record.Status {
			case "failed":
				detail = "Task failed"
				if rec.Record.Error != nil && strings.TrimSpace(*rec.Record.Error) != "" {
					detail += ": " + strings.TrimSpace(*rec.Record.Error)
				}
			case "interrupted":
				detail = "Task interrupted"
			case "completed":
				detail = ""
			}
		case "messages":
			for index, item := range rec.Record.Items {
				key := fmt.Sprintf("hist:%s:%d:%d", ct.SessionID, rec.Offset, index)
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
					}
					messages = append(messages, &message{
						Key: key, Kind: kind, Text: text, Time: timeLabel,
						TaskID: taskID, MessageID: messageID, History: true,
					})
					entries = append(entries, historyEntry{ID: key, Date: timeLabel, From: from, Text: text})
				case "tool_call":
					messages = append(messages, &message{
						Key: key, Kind: msgActivity, Text: item.Name, Time: timeLabel,
						TaskID: taskID, MessageID: messageID, CallID: item.CallID,
						ToolName: item.Name, ToolArgs: item.Arguments, History: true,
					})
				case "tool_result":
					messages = append(messages, &message{
						Key: key, Kind: msgActivity, Text: "Result", Time: timeLabel,
						TaskID: taskID, MessageID: messageID, CallID: item.CallID,
						ToolOutput: item.Output, History: true,
					})
				case "reasoning", "provider_item":
					// Reasoning stays internal; desktop.md does not show it.
				}
			}
		}
	}
	ct.Messages = messages
	ct.History = entries
	ct.HistoryDirty = false
	ct.Loaded = true
	ct.Status = statusFromString(status)
	ct.StatusDetail = detail
	if ct.Status == statusAttention && ct.StatusDetail == "" {
		ct.StatusDetail = "Previous task did not complete."
	}
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
		return runtimeErr.Code + ": " + safeDiagnostic(runtimeErr.Message)
	}
	return safeDiagnostic(err.Error())
}

func confirmedReject(err error) bool {
	var runtimeErr *runtimeError
	return errors.As(err, &runtimeErr) && runtimeErr.Code != "protocol_error"
}

func (a *appModel) applyRuntimeEvent(event runtimeEventEnvelope) {
	ct := a.contactBySession(event.SessionID)
	if ct == nil || event.Event == nil {
		return
	}
	if ct.SendState == sendSubmitting && ct.ActiveTaskID == "" {
		ct.pendingEvents = append(ct.pendingEvents, event)
		return
	}
	a.applyEvent(ct, event)
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
		item := a.liveTool(ct, event.TaskID, payload.CallID)
		item.ToolName = payload.Name
		item.ToolArgs = payload.Arguments
		item.Text = payload.Name
		ct.Work = "Running " + payload.Name
	case "tool_finished":
		item := a.liveTool(ct, event.TaskID, payload.CallID)
		item.ToolName = payload.Name
		item.ToolOutput = outputText(payload.Output)
		ct.Work = "Waiting for model"
	case "task_finished":
		if ct.ActiveTaskID != "" && event.TaskID != ct.ActiveTaskID {
			return
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
		if ct != a.selectedContact() {
			ct.Unread++
		}
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

func taskFinishedDetail(payload *runtimeEvent) string {
	switch payload.Status {
	case "failed":
		if payload.Error != nil {
			stage := "Task failed"
			if payload.Error.Code == "model_error" {
				if status := httpAuthStatus(payload.Error.Message); status != "" {
					return "Provider authentication failed: " + status +
						". Check API_KEY and provider configuration; no automatic retry."
				}
				stage = "Model request failed"
			}
			return stage + ": " + payload.Error.Code + ": " + safeDiagnostic(payload.Error.Message) +
				". Check provider configuration (API_KEY / SAKI_BASE_URL / SAKI_MODEL); no automatic retry."
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
	return m
}

func (a *appModel) liveTool(ct *contact, taskID, callID string) *message {
	key := "live:" + taskID + ":tool:" + callID
	for _, m := range ct.Messages {
		if m.Key == key {
			return m
		}
	}
	m := &message{Key: key, Kind: msgActivity, TaskID: taskID, CallID: callID, Time: nowTime()}
	ct.Messages = append(ct.Messages, m)
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
	if ct.ActiveTaskID != "" || ct.SendState != sendIdle {
		return "This contact is busy."
	}
	return ""
}

func (a *appModel) sendRuntime(ct *contact, text string) bool {
	if reason := a.canSend(ct); reason != "" {
		ct.SendError = reason
		return false
	}
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
	if ct.draftRev == ct.SubmittedRev && strings.TrimSpace(ct.Draft) == strings.TrimSpace(text) {
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
	a.persist()
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
			return
		}
		ct.SendError = runtimeMessage(err)
		if submitted {
			ct.SendState = sendUnknown
			ct.SendError = "Delivery not confirmed (" + ct.SendError + "). Reload to check before sending again."
			return
		}
		ct.SendState = sendIdle
	})
}

func (a *appModel) cancelSelected() {
	ct := a.selectedContact()
	if a.rt == nil || ct.ActiveTaskID == "" {
		return
	}
	contactID, sessionID, taskID := ct.ID, ct.SessionID, ct.ActiveTaskID
	ct.SendError = "Cancellation requested"
	go func() {
		status, err := a.rt.cancelTask(sessionID, taskID)
		a.updateUI(func() { a.applyCancelResult(contactID, taskID, status, err) })
	}()
}

func (a *appModel) applyCancelResult(contactID, taskID, status string, err error) {
	ct := a.contactByID(contactID)
	if ct == nil {
		return
	}
	if err != nil {
		if ct.ActiveTaskID == taskID {
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
		ct.SendError = "Cancelling; waiting for the runtime to stop."
	case "already_requested":
		ct.SendError = "Cancellation already requested."
	case "not_running":
		ct.SendError = "The task already finished; waiting for its final state."
	}
}

func (a *appModel) reloadSelected() {
	ct := a.selectedContact()
	if ct.SessionID == "" {
		return
	}
	if a.rt == nil || a.runtimeStatus != runtimeStateReady {
		ct.SendError = "Runtime disconnected; restart Saki to reload history."
		return
	}
	a.reloadContactByID(ct.ID)
}

func (a *appModel) profileLocked(ct *contact) bool {
	if a.demo {
		return false
	}
	return ct.SessionID != "" || ct.SendState == sendSubmitting
}

func (a *appModel) addRuntimeContact() {
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
