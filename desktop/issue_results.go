package main

// Independent issue result projection. A reported file is evidence only when a
// finished Runtime tool result from write/edit contains the resolved path and
// that call's success fields. Assistant prose, read/bash output, errors and
// interrupted results are never treated as generated files.

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"saki/desktop/locales"
)

// issueResultPreviewLimit bounds the current-file preview. A larger file is
// reported with its actual size instead of copying a silently truncated view.
const issueResultPreviewLimit = 256 * 1024

// issueResultSource is the execution identity supplied by the caller. It must
// come from issue.execution facts and its actual Runtime session, not from the
// selected contact's current session or the prompt text.
type issueResultSource struct {
	ExecutionID      string
	SessionID        string
	TaskID           string
	MessageID        string
	WorkingDirectory string
}

// issueResultFile is one successful write/edit result. Multiple calls and
// executions remain separate rows even when they report the same path.
type issueResultFile struct {
	ExecutionID      string
	SessionID        string
	TaskID           string
	MessageID        string
	CallID           string
	ToolName         string
	Path             string
	WorkingDirectory string
	RawResult        string
}

type issueResultDerivation struct {
	WorkingDirectory string
	Files            []issueResultFile
	UnavailableKind  string
}

// deriveIssueResultFiles reads only the supplied Runtime history response. The
// history header and the execution's recorded session must match; the function
// cannot discover a session by itself and never falls back to a prompt or
// latest work item.
func deriveIssueResultFiles(source issueResultSource, history runtimeHistory) issueResultDerivation {
	if source.SessionID == "" {
		return issueResultDerivation{UnavailableKind: "no-session"}
	}
	if source.SessionID != history.SessionID {
		return issueResultDerivation{UnavailableKind: "session-mismatch"}
	}
	if source.TaskID == "" {
		return issueResultDerivation{UnavailableKind: "no-task"}
	}

	projection := parseHistory(history.SessionID, history.Records, nil)
	var files []issueResultFile
	foundTask, messageMismatch := false, false
	for _, work := range projection.works {
		if work.TaskID != source.TaskID {
			continue
		}
		foundTask = true
		if source.MessageID != "" && work.MessageID != source.MessageID {
			messageMismatch = true
			continue
		}
		for _, tool := range work.Tools {
			if tool == nil || !tool.Finished {
				continue
			}
			path, ok := successfulWriteEditPath(tool.Name, tool.Output)
			if !ok {
				continue
			}
			files = append(files, issueResultFile{
				ExecutionID:      source.ExecutionID,
				SessionID:        source.SessionID,
				TaskID:           work.TaskID,
				MessageID:        work.MessageID,
				CallID:           tool.CallID,
				ToolName:         tool.Name,
				Path:             path,
				WorkingDirectory: source.WorkingDirectory,
				RawResult:        tool.Output,
			})
		}
	}
	if !foundTask {
		return issueResultDerivation{UnavailableKind: "task-missing"}
	}
	if messageMismatch {
		return issueResultDerivation{UnavailableKind: "message-missing"}
	}
	return issueResultDerivation{Files: files, WorkingDirectory: source.WorkingDirectory}
}

func successfulWriteEditPath(toolName, output string) (string, bool) {
	if toolName != "write" && toolName != "edit" {
		return "", false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &fields); err != nil {
		return "", false
	}
	if _, failed := fields["error"]; failed {
		return "", false
	}
	if raw, interrupted := fields["interrupted"]; interrupted {
		var value bool
		if json.Unmarshal(raw, &value) == nil && value {
			return "", false
		}
	}
	var path string
	rawPath, ok := fields["path"]
	if !ok || json.Unmarshal(rawPath, &path) != nil {
		return "", false
	}
	if path == "" || !filepath.IsAbs(path) {
		return "", false
	}
	switch toolName {
	case "write":
		var written int
		raw, ok := fields["bytes_written"]
		if !ok || json.Unmarshal(raw, &written) != nil || written < 0 {
			return "", false
		}
	case "edit":
		var applied int
		raw, ok := fields["applied"]
		if !ok || json.Unmarshal(raw, &applied) != nil || applied < 1 {
			return "", false
		}
	}
	return path, true
}

type issueResultFileInspection struct {
	Path             string
	WorkingDirectory string
	Exists           bool
	Regular          bool
	Size             int64
	Preview          string
	NoticeKind       string
	NoticeDetail     string
	ReadErrorKind    string
	ReadErrorDetail  string
}

// inspectIssueResultFile reports the current file at the reported path. It does
// not search for moved files and does not claim the execution-time snapshot.
func inspectIssueResultFile(file issueResultFile) issueResultFileInspection {
	inspection := issueResultFileInspection{Path: file.Path, WorkingDirectory: file.WorkingDirectory}
	handle, err := os.OpenFile(file.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			inspection.NoticeKind, inspection.NoticeDetail = resultNoticeMissing, ""
		} else {
			inspection.NoticeKind, inspection.NoticeDetail = resultNoticeInspect, err.Error()
		}
		return inspection
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		inspection.NoticeKind, inspection.NoticeDetail = resultNoticeInspect, err.Error()
		return inspection
	}
	inspection.Exists = true
	inspection.Size = info.Size()
	if !info.Mode().IsRegular() {
		inspection.NoticeKind = resultNoticeRegular
		return inspection
	}
	inspection.Regular = true
	if info.Size() > issueResultPreviewLimit {
		inspection.NoticeKind = resultNoticeTooLarge
		return inspection
	}
	data, err := io.ReadAll(io.LimitReader(handle, issueResultPreviewLimit+1))
	if err != nil {
		inspection.ReadErrorKind, inspection.ReadErrorDetail = resultNoticeRead, err.Error()
		return inspection
	}
	if len(data) > issueResultPreviewLimit {
		inspection.NoticeKind = resultNoticeGrew
		return inspection
	}
	if !utf8.Valid(data) {
		inspection.NoticeKind = resultNoticeNotUTF8
		return inspection
	}
	inspection.Preview = string(data)
	return inspection
}

type issueResultFileAction struct {
	Path         string
	ErrKind      string
	ErrDetail    string
	NoticeKind   string
	NoticeDetail string
}

const (
	resultNoticeMissing  = "missing"
	resultNoticeInspect  = "inspect-failed"
	resultNoticeRegular  = "not-regular"
	resultNoticeTooLarge = "too-large"
	resultNoticeGrew     = "grew"
	resultNoticeNotUTF8  = "not-utf8"
	resultNoticeRead     = "read-failed"
	resultActionOpen     = "open-failed"
	resultActionReveal   = "reveal-failed"
)

func issueResultNoticeText(msgs locales.Messages, kind, detail string) string {
	switch kind {
	case resultNoticeMissing:
		return msgs.ResultInspectMissing()
	case resultNoticeInspect:
		return msgs.ResultInspectFailed(detail)
	case resultNoticeRegular:
		return msgs.ResultInspectNotRegular()
	case resultNoticeNotUTF8:
		return msgs.ResultInspectNotUTF8()
	case resultNoticeRead:
		return msgs.ResultReadFailed(detail)
	case resultActionOpen:
		return msgs.ResultOpenFailed(detail)
	case resultActionReveal:
		return msgs.ResultRevealFailed(detail)
	default:
		return kind
	}
}

func issueResultUnavailableRaw(kind string) string {
	switch kind {
	case "no-session":
		return "Execution has no recorded Runtime session; results cannot be inspected."
	case "session-mismatch":
		return "Runtime history belongs to a different session; results cannot be inspected."
	case "no-task":
		return "Execution has no recorded Runtime task; results cannot be inspected."
	case "task-missing":
		return "Runtime history does not contain the execution's task; the session may be unavailable or legacy."
	case "message-missing":
		return "Runtime history does not record the supplied execution message."
	default:
		return ""
	}
}

func issueResultPageText(msgs locales.Messages, kind, detail string) string {
	switch kind {
	case "load-no-session":
		return msgs.ResultLoadNoSession()
	case "load-runtime-not-ready":
		return msgs.ResultLoadRuntimeNotReady()
	case "load-owner-missing":
		return msgs.ResultLoadOwnerMissing()
	case "load-detached":
		return msgs.ResultLoadDetached()
	case "load-failed":
		return msgs.ResultLoadFailed(detail)
	case "notice-working-dir":
		return msgs.ResultNoticeWorkingDirMissing()
	case "no-session":
		return msgs.ResultDeriveNoSession()
	case "session-mismatch":
		return msgs.ResultDeriveSessionMismatch()
	case "no-task":
		return msgs.ResultDeriveNoTask()
	case "task-missing":
		return msgs.ResultDeriveTaskMissing()
	case "message-missing":
		return msgs.ResultDeriveMessageMissing()
	default:
		return kind
	}
}

func issueResultInspectionNoticeText(msgs locales.Messages, inspection issueResultFileInspection) string {
	switch inspection.NoticeKind {
	case resultNoticeTooLarge:
		return msgs.ResultInspectTooLarge(strconv.FormatInt(inspection.Size, 10), strconv.Itoa(issueResultPreviewLimit))
	case resultNoticeGrew:
		return msgs.ResultInspectGrew(strconv.Itoa(issueResultPreviewLimit))
	default:
		return issueResultNoticeText(msgs, inspection.NoticeKind, inspection.NoticeDetail)
	}
}

func currentIssueResultPath(path string) (string, string) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return resultNoticeMissing, ""
		}
		return resultNoticeInspect, err.Error()
	}
	if !info.Mode().IsRegular() {
		return resultNoticeRegular, ""
	}
	return "", ""
}

// requestOpenIssueResultFile checks the current path before invoking the injected
// opener. open is mygo.Shell.OpenPath in the application; tests inject a
// recorder so a missing or non-regular path proves the opener was not called.
func requestOpenIssueResultFile(file issueResultFile, open func(string) error) issueResultFileAction {
	if kind, detail := currentIssueResultPath(file.Path); kind != "" {
		return issueResultFileAction{Path: file.Path, NoticeKind: kind, NoticeDetail: detail}
	}
	if err := open(file.Path); err != nil {
		return issueResultFileAction{Path: file.Path, ErrKind: resultActionOpen, ErrDetail: err.Error()}
	}
	return issueResultFileAction{Path: file.Path}
}

// requestRevealIssueResultFile checks the current path before requesting a reveal.
// The non-macOS API reports no outcome, so callers must not toast success for
// it; only an explicit error is shown as failure.
func requestRevealIssueResultFile(file issueResultFile, reveal func(string) error) issueResultFileAction {
	if kind, detail := currentIssueResultPath(file.Path); kind != "" {
		return issueResultFileAction{Path: file.Path, NoticeKind: kind, NoticeDetail: detail}
	}
	if err := reveal(file.Path); err != nil {
		return issueResultFileAction{Path: file.Path, ErrKind: resultActionReveal, ErrDetail: err.Error()}
	}
	return issueResultFileAction{Path: file.Path}
}

// OpenPath reports whether the request failed, not whether an application
// actually displayed the file; callers must not claim the OS opened it.
func openFileWithSystem(path string) error {
	return mygo.Shell.OpenPath(path)
}

func requestRevealIssueResultWithSystem(path string) error {
	if goruntime.GOOS == "darwin" {
		return exec.Command("open", "-R", path).Run()
	}
	mygo.Shell.ShowItemInFolder(path)
	return nil
}

// issueResultPanelState is presentation state for one result page. The caller
// supplies run/file/inspection/action facts; this component updates only the
// local reading mode (ReaderKey) and renders matching results on the next
// frame. It contains no file I/O, task dispatch or persistence.
type issueResultPanelState struct {
	WorkingDirectory   string
	RunNumber          int
	RunGoal            string
	RunStatus          string
	RunDetail          string
	Response           string
	Files              []issueResultFile
	Selected           string
	Inspection         *issueResultFileInspection
	InspectFor         string
	Checking           bool
	ActionFor          string
	Action             issueResultFileAction
	SourceFor          string
	ReaderKey          string
	ReaderReturnKey    string
	ReaderScroll       ui.ScrollState
	FocusClose         bool
	FocusReader        bool
	FocusReturn        *ui.Handle
	LinkFocus          *markdownLinkTarget
	LinkScope          string
	LinkReturn         *markdownLinkTarget
	LinkReturnSeen     *bool
	CloseHandle        ui.Handle
	ReloadHandle       ui.Handle
	ReaderCloseHandle  ui.Handle
	ReaderCheckHandle  ui.Handle
	ReaderSourceHandle ui.Handle
	FileHandles        map[string]*ui.Handle
	CopyResultHandle   ui.Handle
	CopyPathHandle     ui.Handle
	CopyCurrentHandle  ui.Handle
	CopyRawHandle      ui.Handle
	ReaderOpenHandle   ui.Handle
	ReaderRevealHandle ui.Handle
	ResultCodeHandles  map[int]*ui.Handle
	ReaderCodeHandles  map[int]*ui.Handle
}

// issueResultPanelActions are explicit user requests. The component never
// inspects, opens, reveals or starts a task by itself.
type issueResultPanelActions struct {
	OpenLocalFile func(base, destination string) error
	OpenSettings  func(c *ui.Context)
	Close         func()
	Reload        func()
	SelectFile    func(key string)
	CheckFile     func(file issueResultFile)
	OpenFile      func(file issueResultFile)
	RevealFile    func(file issueResultFile)
	ToggleSource  func(key string)
	CopyText      func(text string) error
}

// issueResultMarkdownFile is the explicit Markdown rendering allowlist. Other
// UTF-8 files stay plain text so arbitrary code or text is never treated as
// Markdown.
func issueResultMarkdownFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return true
	default:
		return false
	}
}

func issueResultFileKey(file issueResultFile) string {
	return file.SessionID + "\x00" + file.TaskID + "\x00" + file.CallID
}

// issueResultPanel renders a selected execution result and its reported files.
// The caller supplies the actual run identity, exact response source and file
// rows; this function does not read history or the filesystem.
func issueResultPanel(c *ui.Context, state *issueResultPanelState, actions issueResultPanelActions) ui.Element {
	th := c.Theme()
	if state.ResultCodeHandles == nil {
		state.ResultCodeHandles = map[int]*ui.Handle{}
	}
	root := ui.Column(c).AlignSelf(ui.Stretch).MinWidth(0).Gap(12)
	if actions.OpenSettings != nil {
		root.OnShortcut(ui.Cmd, ui.KeyComma, func() {
			state.FocusReturn = issueResultFocusedHandle(c, state, false)
			actions.OpenSettings(c)
		})
	}
	root.Children(func() {
		if state.RunGoal != "" {
			ui.Text(c, locales.In(c.Locale()).ResultRunLabel(strconv.Itoa(state.RunNumber), state.RunGoal)).FontSize(14).TextColor(th.TextMuted)
		}
		if state.RunStatus != "" {
			ui.Text(c, issueExecutionText(locales.In(c.Locale()), state.RunStatus)).FontSize(typeAction).FontWeight(500).TextColor(th.TextMuted)
		}
		if state.RunDetail != "" {
			ui.Text(c, state.RunDetail).FontSize(14).LineHeight(1.5).TextColor(th.Warning)
		}

		result := ui.Column(c).AlignSelf(ui.Stretch).MinWidth(0).Padding(12).Radius(th.Radius).Background(th.Surface).Gap(8)
		result.Children(func() {
			ui.Text(c, locales.In(c.Locale()).ResultHeading()).FontSize(typeAction).FontWeight(500).TextColor(th.Text)
			if state.Response == "" {
				ui.Text(c, locales.In(c.Locale()).ResultNoAssistant()).FontSize(14).LineHeight(1.5).TextColor(th.TextMuted)
				return
			}
			var openLocalFile func(string) error
			if actions.OpenLocalFile != nil {
				openLocalFile = func(dest string) error { return actions.OpenLocalFile(state.WorkingDirectory, dest) }
			}
			markdownReader(c, state.Response, markdownReaderOptions{
				OpenLocalFile:   openLocalFile,
				CopyLabel:       locales.In(c.Locale()).CopyResultLabel(),
				CopyText:        actions.CopyText,
				CopyHandle:      &state.CopyResultHandle,
				CodeCopyHandles: state.ResultCodeHandles,
				LinkScope:       state.LinkScope + ":response",
				LinkFocus:       state.LinkFocus,
				LinkReturn:      state.LinkReturn,
				LinkReturnSeen:  state.LinkReturnSeen,
			})
		})

		ui.Column(c).AlignSelf(ui.Stretch).MinWidth(0).Gap(8).Children(func() {
			ui.Text(c, locales.In(c.Locale()).ResultReportedFiles()).FontSize(typeAction).FontWeight(500).TextColor(th.Text)
			if len(state.Files) == 0 {
				ui.Text(c, locales.In(c.Locale()).ResultNoFiles()).FontSize(14).LineHeight(1.5).TextColor(th.TextMuted)
				return
			}
			for index, file := range state.Files {
				issueResultFileRow(c, th, state, actions, file, index)
			}
		})
	})
	if state.ReaderReturnKey != "" {
		if _, ok := issueResultFileByKey(state.Files, state.ReaderReturnKey); !ok {
			state.ReaderReturnKey = ""
		}
	}
	if state.FocusReturn != nil {
		state.FocusReturn.Focus(c)
		state.FocusReturn = nil
	}
	return root
}

func (state *issueResultPanelState) fileHandle(key string) *ui.Handle {
	if state.FileHandles == nil {
		state.FileHandles = map[string]*ui.Handle{}
	}
	if state.FileHandles[key] == nil {
		state.FileHandles[key] = &ui.Handle{}
	}
	return state.FileHandles[key]
}

func issueResultFileByKey(files []issueResultFile, key string) (issueResultFile, bool) {
	for _, file := range files {
		if issueResultFileKey(file) == key {
			return file, true
		}
	}
	return issueResultFile{}, false
}

func issueResultFileRow(c *ui.Context, th *ui.Theme, state *issueResultPanelState, actions issueResultPanelActions, file issueResultFile, index int) {
	key := issueResultFileKey(file)
	handle := state.fileHandle(key)
	row := ui.ButtonBase(c).
		Label(locales.In(c.Locale()).ResultFileRowLabel(strconv.Itoa(index+1), filepath.Base(file.Path))).
		AlignSelf(ui.Stretch).MinWidth(0).Padding(10).Radius(th.Radius).Border(0.5, th.Border).Gap(4).Bind(handle)
	if state.Selected == key {
		row.Background(th.SurfaceHover)
	}
	row.Children(func() {
		ui.Row(c).Wrap().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, filepath.Base(file.Path)).FontSize(typeAction).FontWeight(500).TextColor(th.Text).MaxLines(2).Grow(1)
			ui.Text(c, locales.In(c.Locale()).ResultReadFile()).FontSize(typeAction).FontWeight(500).TextColor(th.TextMuted)
		})
		ui.Text(c, file.Path).FontSize(14).TextColor(th.TextMuted).MaxLines(2)
		ui.Text(c, locales.In(c.Locale()).ResultWorkingDirectory(file.WorkingDirectory)).FontSize(typeAction).TextColor(th.TextMuted).MaxLines(2)
		ui.Text(c, locales.In(c.Locale()).ResultReportedBy(file.ToolName)).FontSize(typeAction).TextColor(th.TextMuted).MaxLines(2)
	})
	if state.ReaderReturnKey == key {
		row.Focus()
		state.ReaderReturnKey = ""
	}
	if row.Clicked() {
		state.ReaderReturnKey = ""
		state.ReaderKey = key
		state.FocusReader = true
		state.ReaderScroll = ui.ScrollState{}
		if actions.SelectFile != nil {
			actions.SelectFile(key)
		}
	}
}

func issueResultFocusedHandle(c *ui.Context, state *issueResultPanelState, reader bool) *ui.Handle {
	var controls []*ui.Handle
	if reader {
		controls = []*ui.Handle{
			&state.ReaderCloseHandle, &state.ReaderCheckHandle, &state.ReaderSourceHandle,
			&state.ReaderOpenHandle, &state.ReaderRevealHandle,
			&state.CopyPathHandle, &state.CopyCurrentHandle, &state.CopyRawHandle,
		}
		for _, handle := range state.ReaderCodeHandles {
			controls = append(controls, handle)
		}
	} else {
		controls = []*ui.Handle{&state.CloseHandle, &state.ReloadHandle, &state.CopyResultHandle}
		for _, file := range state.Files {
			controls = append(controls, state.FileHandles[issueResultFileKey(file)])
		}
		for _, handle := range state.ResultCodeHandles {
			controls = append(controls, handle)
		}
	}
	for _, handle := range controls {
		if handle != nil && c.Focused(*handle) {
			return handle
		}
	}
	return controls[0]
}

func issueResultReaderPage(c *ui.Context, th *ui.Theme, state *issueResultPanelState, actions issueResultPanelActions, file issueResultFile) ui.Element {
	if state.ReaderCodeHandles == nil {
		state.ReaderCodeHandles = map[int]*ui.Handle{}
	}
	closeReader := func() {
		if state.ReaderKey != "" {
			state.ReaderReturnKey = state.ReaderKey
		}
		state.ReaderKey = ""
	}
	root := ui.Column(c).Fill().Background(th.Background)
	if actions.OpenSettings != nil {
		root.OnShortcut(ui.Cmd, ui.KeyComma, func() {
			state.FocusReturn = issueResultFocusedHandle(c, state, true)
			actions.OpenSettings(c)
		})
	}
	root.Children(func() {
		ui.Row(c).Padding(16, 16).AlignItems(ui.Center).Gap(8).Children(func() {
			back := appTooltip(c, secondaryButton(c, locales.In(c.Locale()).ResultBackResults()).Label(locales.In(c.Locale()).ResultCloseReading()), locales.In(c.Locale()).ResultBackToResultsTooltip()).Bind(&state.ReaderCloseHandle).OnClick(closeReader)
			if state.FocusReader {
				back.Focus()
				state.FocusReader = false
			}
			ui.Column(c).MinWidth(0).Shrink(1).Gap(2).Children(func() {
				ui.Text(c, filepath.Base(file.Path)).FontSize(16).FontWeight(500).TextColor(th.Text).MaxLines(2)
			})
		})
		ui.Scroll(c).Grow(1).TrackScroll(&state.ReaderScroll).Padding(0, 16).Children(func() {
			ui.Column(c).AlignSelf(ui.Stretch).MaxWidth(760).MinWidth(0).Gap(10).Children(func() {
				markdownReader(c, file.Path, markdownReaderOptions{
					PlainText: true, CopyLabel: locales.In(c.Locale()).CopyPathLabel(), CopyText: actions.CopyText,
					CopyHandle: &state.CopyPathHandle,
				})
				issueResultReaderActions(c, th, state, actions, file)
				ui.Text(c, locales.In(c.Locale()).ResultCurrentFileDiff()).FontSize(typeAction).TextColor(th.TextMuted)
				issueResultReaderPreview(c, th, state, actions, file, issueResultFileKey(file))
				if state.SourceFor == issueResultFileKey(file) {
					issueResultSourceView(c, th, state, actions, file)
				}
			})
		})
	})
	if state.FocusReturn != nil {
		state.FocusReturn.Focus(c)
		state.FocusReturn = nil
	}
	return root
}

func issueResultReaderAction(c *ui.Context, th *ui.Theme, label string) ui.Element {
	button := ui.Button(c, label).Label(label).FontSize(typeAction).MinHeight(32).Padding(4, 8).
		Background(th.Surface).BorderWidth(0).TextColor(th.Text)
	if button.Pressed() {
		button.Background(th.SurfacePressed)
	} else if button.Hovered() {
		button.Background(th.SurfaceHover)
	}
	if button.Focused() {
		button.BorderWidth(1).BorderColor(th.Focus)
	}
	return button
}

func issueResultReaderActions(c *ui.Context, th *ui.Theme, state *issueResultPanelState, actions issueResultPanelActions, file issueResultFile) {
	key := issueResultFileKey(file)
	msgs := locales.In(c.Locale())
	var inspection *issueResultFileInspection
	if state.InspectFor == key {
		inspection = state.Inspection
	}
	ui.Row(c).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
		if actions.CheckFile != nil {
			issueResultReaderAction(c, th, msgs.ReaderCheckAgainLabel()).Bind(&state.ReaderCheckHandle).OnClick(func() { actions.CheckFile(file) })
		}
		if inspection != nil && inspection.Exists && inspection.Regular {
			if actions.OpenFile != nil {
				issueResultReaderAction(c, th, msgs.ReaderOpenFileLabel()).Bind(&state.ReaderOpenHandle).OnClick(func() { actions.OpenFile(file) })
			}
			if actions.RevealFile != nil {
				issueResultReaderAction(c, th, msgs.ReaderShowInFinderLabel()).Bind(&state.ReaderRevealHandle).OnClick(func() { actions.RevealFile(file) })
			}
		}
		if actions.ToggleSource != nil {
			label := msgs.ReaderSourceRawLabel()
			if state.SourceFor == key {
				label = msgs.ReaderHideSourceRawLabel()
			}
			issueResultReaderAction(c, th, label).Bind(&state.ReaderSourceHandle).OnClick(func() { actions.ToggleSource(key) })
		}
	})
	if state.ActionFor == key && state.Action.ErrKind != "" {
		ui.Text(c, issueResultNoticeText(locales.In(c.Locale()), state.Action.ErrKind, state.Action.ErrDetail)).FontSize(typeAction).LineHeight(1.4).TextColor(th.Danger)
	} else if state.ActionFor == key && state.Action.NoticeKind != "" {
		ui.Text(c, issueResultNoticeText(locales.In(c.Locale()), state.Action.NoticeKind, state.Action.NoticeDetail)).FontSize(typeAction).LineHeight(1.4).TextColor(th.Warning)
	}
}

func issueResultReaderPreview(c *ui.Context, th *ui.Theme, state *issueResultPanelState, actions issueResultPanelActions, file issueResultFile, key string) {
	var inspection *issueResultFileInspection
	if state.InspectFor == key {
		inspection = state.Inspection
	}
	if state.Checking && state.InspectFor == key {
		ui.Text(c, locales.In(c.Locale()).ResultCheckingFile()).FontSize(14).TextColor(th.TextMuted)
	}
	if inspection == nil {
		if !state.Checking {
			ui.Text(c, locales.In(c.Locale()).ResultCheckCurrentFile()).FontSize(14).TextColor(th.TextMuted)
		}
		return
	}
	if inspection.NoticeKind != "" {
		ui.Text(c, issueResultInspectionNoticeText(locales.In(c.Locale()), *inspection)).FontSize(14).LineHeight(1.5).TextColor(th.Warning)
	}
	if inspection.ReadErrorKind != "" {
		ui.Text(c, issueResultNoticeText(locales.In(c.Locale()), inspection.ReadErrorKind, inspection.ReadErrorDetail)).FontSize(14).LineHeight(1.5).TextColor(th.Danger)
	}
	if !inspection.Exists || !inspection.Regular || inspection.NoticeKind != "" || inspection.ReadErrorKind != "" {
		return
	}
	if inspection.Preview == "" {
		ui.Text(c, locales.In(c.Locale()).ResultEmptyFile()).FontSize(14).LineHeight(1.5).TextColor(th.TextMuted)
	}
	var openLocalFile func(string) error
	if actions.OpenLocalFile != nil {
		openLocalFile = func(dest string) error { return actions.OpenLocalFile(filepath.Dir(file.Path), dest) }
	}
	markdownReader(c, inspection.Preview, markdownReaderOptions{
		OpenLocalFile:   openLocalFile,
		PlainText:       !issueResultMarkdownFile(file.Path),
		CopyLabel:       locales.In(c.Locale()).CopyCurrentFileLabel(),
		CopyText:        actions.CopyText,
		CopyHandle:      &state.CopyCurrentHandle,
		CodeCopyHandles: state.ReaderCodeHandles,
		LinkScope:       state.LinkScope + ":file:" + issueResultFileKey(file),
		LinkFocus:       state.LinkFocus,
		LinkReturn:      state.LinkReturn,
		LinkReturnSeen:  state.LinkReturnSeen,
	})
}

func issueResultSourceView(c *ui.Context, th *ui.Theme, state *issueResultPanelState, actions issueResultPanelActions, file issueResultFile) {
	source := ui.Column(c).AlignSelf(ui.Stretch).MinWidth(0).Gap(4)
	source.Children(func() {
		ui.Text(c, locales.In(c.Locale()).ResultSourceRaw()).FontSize(typeAction).FontWeight(500).TextColor(th.Text)
		ui.Text(c, locales.In(c.Locale()).ResultWorkingDirectory(file.WorkingDirectory)).FontSize(typeAction).TextColor(th.TextMuted).MaxLines(2)
		ui.Text(c, locales.In(c.Locale()).ResultSessionLine(file.SessionID)).FontSize(typeAction).TextColor(th.TextMuted).MaxLines(2)
		ui.Text(c, locales.In(c.Locale()).ResultTaskLine(file.TaskID)).FontSize(typeAction).TextColor(th.TextMuted).MaxLines(2)
		ui.Text(c, locales.In(c.Locale()).ResultMessageLine(file.MessageID)).FontSize(typeAction).TextColor(th.TextMuted).MaxLines(2)
		ui.Text(c, locales.In(c.Locale()).ResultCallLine(file.CallID)).FontSize(typeAction).TextColor(th.TextMuted).MaxLines(2)
		ui.Scroll(c).MaxHeight(160).AlignSelf(ui.Stretch).Children(func() {
			markdownReader(c, file.RawResult, markdownReaderOptions{
				PlainText: true, CopyLabel: locales.In(c.Locale()).CopyToolRecordLabel(), CopyText: actions.CopyText,
				CopyHandle: &state.CopyRawHandle,
			})
		})
	})
}
