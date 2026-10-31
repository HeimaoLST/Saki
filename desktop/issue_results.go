package main

// Independent issue result projection. A reported file is evidence only when a
// finished Runtime tool result from write/edit contains the resolved path and
// that call's success fields. Assistant prose, read/bash output, errors and
// interrupted results are never treated as generated files.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
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
	Files       []issueResultFile
	Unavailable string
}

// deriveIssueResultFiles reads only the supplied Runtime history response. The
// history header and the execution's recorded session must match; the function
// cannot discover a session by itself and never falls back to a prompt or
// latest work item.
func deriveIssueResultFiles(source issueResultSource, history runtimeHistory) issueResultDerivation {
	if source.SessionID == "" {
		return issueResultDerivation{Unavailable: "Execution has no recorded Runtime session; results cannot be inspected."}
	}
	if source.SessionID != history.SessionID {
		return issueResultDerivation{Unavailable: "Runtime history belongs to a different session; results cannot be inspected."}
	}
	if source.TaskID == "" {
		return issueResultDerivation{Unavailable: "Execution has no recorded Runtime task; results cannot be inspected."}
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
		return issueResultDerivation{Unavailable: "Runtime history does not contain the execution's task; the session may be unavailable or legacy."}
	}
	if messageMismatch {
		return issueResultDerivation{Unavailable: "Runtime history does not record the supplied execution message."}
	}
	return issueResultDerivation{Files: files}
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
	Notice           string
	ReadError        string
}

// inspectIssueResultFile reports the current file at the reported path. It does
// not search for moved files and does not claim the execution-time snapshot.
func inspectIssueResultFile(file issueResultFile) issueResultFileInspection {
	inspection := issueResultFileInspection{Path: file.Path, WorkingDirectory: file.WorkingDirectory}
	handle, err := os.OpenFile(file.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			inspection.Notice = "No file exists at the reported path; it may have been moved or deleted."
		} else {
			inspection.Notice = "Could not inspect the reported path: " + err.Error()
		}
		return inspection
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		inspection.Notice = "Could not inspect the reported path: " + err.Error()
		return inspection
	}
	inspection.Exists = true
	inspection.Size = info.Size()
	if !info.Mode().IsRegular() {
		inspection.Notice = "The reported path is no longer a regular file."
		return inspection
	}
	inspection.Regular = true
	if info.Size() > issueResultPreviewLimit {
		inspection.Notice = fmt.Sprintf("Current file is %d bytes; the preview limit is %d bytes. Open the file to inspect it in full.", info.Size(), issueResultPreviewLimit)
		return inspection
	}
	data, err := io.ReadAll(io.LimitReader(handle, issueResultPreviewLimit+1))
	if err != nil {
		inspection.ReadError = "Could not read the current file: " + err.Error()
		return inspection
	}
	if len(data) > issueResultPreviewLimit {
		inspection.Notice = fmt.Sprintf("Current file grew beyond the %d-byte preview limit. Open the file to inspect it in full.", issueResultPreviewLimit)
		return inspection
	}
	if !utf8.Valid(data) {
		inspection.Notice = "The current file is not UTF-8 text; open it with the system application."
		return inspection
	}
	inspection.Preview = string(data)
	return inspection
}

type issueResultFileAction struct {
	Path   string
	Err    string
	Notice string
}

func currentIssueResultPath(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "No file exists at the reported path; it may have been moved or deleted."
		}
		return "Could not inspect the reported path: " + err.Error()
	}
	if !info.Mode().IsRegular() {
		return "The reported path is not a regular file."
	}
	return ""
}

// requestOpenIssueResultFile checks the current path before invoking the injected
// opener. open is mygo.Shell.OpenPath in the application; tests inject a
// recorder so a missing or non-regular path proves the opener was not called.
func requestOpenIssueResultFile(file issueResultFile, open func(string) error) issueResultFileAction {
	if notice := currentIssueResultPath(file.Path); notice != "" {
		return issueResultFileAction{Path: file.Path, Notice: notice}
	}
	if err := open(file.Path); err != nil {
		return issueResultFileAction{Path: file.Path, Err: "Could not open the current file: " + err.Error()}
	}
	return issueResultFileAction{Path: file.Path}
}

// requestRevealIssueResultFile checks the current path before requesting a reveal.
// The non-macOS API reports no outcome, so callers must not toast success for
// it; only an explicit error is shown as failure.
func requestRevealIssueResultFile(file issueResultFile, reveal func(string) error) issueResultFileAction {
	if notice := currentIssueResultPath(file.Path); notice != "" {
		return issueResultFileAction{Path: file.Path, Notice: notice}
	}
	if err := reveal(file.Path); err != nil {
		return issueResultFileAction{Path: file.Path, Err: "Could not reveal the current file: " + err.Error()}
	}
	return issueResultFileAction{Path: file.Path}
}

// OpenPath reports whether the request failed, not whether an application
// actually displayed the file; callers must not claim the OS opened it.
func requestOpenIssueResultWithSystem(path string) error {
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
	RunLabel           string
	RunStatus          string
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
	OpenSettings func(c *ui.Context)
	Close        func()
	Reload       func()
	SelectFile   func(key string)
	CheckFile    func(file issueResultFile)
	OpenFile     func(file issueResultFile)
	RevealFile   func(file issueResultFile)
	ToggleSource func(key string)
	CopyText     func(text string) error
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
		if state.RunLabel != "" {
			ui.Text(c, state.RunLabel).FontSize(14).TextColor(th.TextMuted)
		}
		if state.RunStatus != "" {
			ui.Text(c, state.RunStatus).FontSize(14).FontWeight(500).TextColor(th.TextMuted)
		}

		result := ui.Column(c).AlignSelf(ui.Stretch).MinWidth(0).Padding(12).Radius(th.Radius).Background(th.Surface).Gap(8)
		result.Children(func() {
			ui.Text(c, "Result").FontSize(14).FontWeight(600).TextColor(th.Text)
			if state.Response == "" {
				ui.Text(c, "No assistant response was recorded for this run.").FontSize(14).LineHeight(1.45).TextColor(th.TextMuted)
				return
			}
			markdownReader(c, state.Response, markdownReaderOptions{
				CopyLabel:       "Copy result",
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
			ui.Text(c, "Reported files").FontSize(14).FontWeight(600).TextColor(th.Text)
			if len(state.Files) == 0 {
				ui.Text(c, "No files were reported for this run.").FontSize(14).LineHeight(1.45).TextColor(th.TextMuted)
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
		Label(fmt.Sprintf("Read file %d: %s", index+1, filepath.Base(file.Path))).
		AlignSelf(ui.Stretch).MinWidth(0).Padding(10).Radius(th.Radius).Border(0.5, th.Border).Gap(4).Bind(handle)
	if state.Selected == key {
		row.Background(th.SurfaceHover)
	}
	row.Children(func() {
		ui.Row(c).Wrap().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, filepath.Base(file.Path)).FontSize(14).FontWeight(600).TextColor(th.Text).MaxLines(2).Grow(1)
			ui.Text(c, "Read file →").FontSize(typeAction).FontWeight(500).TextColor(th.TextMuted)
		})
		ui.Text(c, file.Path).FontSize(14).TextColor(th.TextMuted).MaxLines(2)
		ui.Text(c, "Working directory: "+file.WorkingDirectory).FontSize(typeAction).TextColor(th.TextMuted).MaxLines(2)
		ui.Text(c, "Reported by "+file.ToolName).FontSize(typeAction).TextColor(th.TextMuted).MaxLines(2)
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
			back := appTooltip(c, secondaryButton(c, "← Back to results").Label("Close reading"), "Back to results").Bind(&state.ReaderCloseHandle).OnClick(closeReader)
			if state.FocusReader {
				back.Focus()
				state.FocusReader = false
			}
			ui.Column(c).MinWidth(0).Shrink(1).Gap(2).Children(func() {
				ui.Text(c, filepath.Base(file.Path)).FontSize(16).FontWeight(600).TextColor(th.Text).MaxLines(2)
			})
		})
		ui.Scroll(c).Grow(1).TrackScroll(&state.ReaderScroll).Padding(0, 16).Children(func() {
			ui.Column(c).AlignSelf(ui.Stretch).MaxWidth(760).MinWidth(0).Gap(10).Children(func() {
				markdownReader(c, file.Path, markdownReaderOptions{
					PlainText: true, CopyLabel: "Copy path", CopyText: actions.CopyText,
					CopyHandle: &state.CopyPathHandle,
				})
				issueResultReaderActions(c, th, state, actions, file)
				ui.Text(c, "Current file: content may differ from this run").FontSize(typeAction).TextColor(th.TextMuted)
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
	button := ui.Button(c, label).FontSize(typeAction).MinHeight(32).Padding(4, 8).
		Background(ui.Transparent).BorderWidth(0).TextColor(th.TextMuted)
	if button.Pressed() {
		button.Background(th.SurfacePressed)
	} else if button.Hovered() {
		button.Background(th.SurfaceHover)
	}
	return button
}

func issueResultReaderActions(c *ui.Context, th *ui.Theme, state *issueResultPanelState, actions issueResultPanelActions, file issueResultFile) {
	key := issueResultFileKey(file)
	var inspection *issueResultFileInspection
	if state.InspectFor == key {
		inspection = state.Inspection
	}
	ui.Row(c).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
		if actions.CheckFile != nil {
			issueResultReaderAction(c, th, "Check again").Bind(&state.ReaderCheckHandle).OnClick(func() { actions.CheckFile(file) })
		}
		if inspection != nil && inspection.Exists && inspection.Regular {
			if actions.OpenFile != nil {
				issueResultReaderAction(c, th, "Open file").Bind(&state.ReaderOpenHandle).OnClick(func() { actions.OpenFile(file) })
			}
			if actions.RevealFile != nil {
				issueResultReaderAction(c, th, "Show in Finder").Bind(&state.ReaderRevealHandle).OnClick(func() { actions.RevealFile(file) })
			}
		}
		if actions.ToggleSource != nil {
			label := "Source & raw record"
			if state.SourceFor == key {
				label = "Hide source & raw record"
			}
			issueResultReaderAction(c, th, label).Bind(&state.ReaderSourceHandle).OnClick(func() { actions.ToggleSource(key) })
		}
	})
	if state.ActionFor == key && state.Action.Err != "" {
		ui.Text(c, state.Action.Err).FontSize(typeAction).LineHeight(1.4).TextColor(th.Danger)
	} else if state.ActionFor == key && state.Action.Notice != "" {
		ui.Text(c, state.Action.Notice).FontSize(typeAction).LineHeight(1.4).TextColor(th.Warning)
	}
}

func issueResultReaderPreview(c *ui.Context, th *ui.Theme, state *issueResultPanelState, actions issueResultPanelActions, file issueResultFile, key string) {
	var inspection *issueResultFileInspection
	if state.InspectFor == key {
		inspection = state.Inspection
	}
	if state.Checking && state.InspectFor == key {
		ui.Text(c, "Checking the current file…").FontSize(14).TextColor(th.TextMuted)
	}
	if inspection == nil {
		if !state.Checking {
			ui.Text(c, "Check the current file at this path to preview it.").FontSize(14).TextColor(th.TextMuted)
		}
		return
	}
	if inspection.Notice != "" {
		ui.Text(c, inspection.Notice).FontSize(14).LineHeight(1.45).TextColor(th.Warning)
	}
	if inspection.ReadError != "" {
		ui.Text(c, inspection.ReadError).FontSize(14).LineHeight(1.45).TextColor(th.Danger)
	}
	if !inspection.Exists || !inspection.Regular || inspection.Notice != "" || inspection.ReadError != "" {
		return
	}
	if inspection.Preview == "" {
		ui.Text(c, "Empty file (0 bytes). Copy the current file to verify its empty content.").FontSize(14).LineHeight(1.45).TextColor(th.TextMuted)
	}
	markdownReader(c, inspection.Preview, markdownReaderOptions{
		PlainText:       !issueResultMarkdownFile(file.Path),
		CopyLabel:       "Copy current file",
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
		ui.Text(c, "Source & raw record").FontSize(14).FontWeight(600).TextColor(th.Text)
		ui.Text(c, "Working directory: "+file.WorkingDirectory).FontSize(typeAction).TextColor(th.TextMuted).MaxLines(2)
		ui.Text(c, "The reader shows the current file at the reported path. Its content may have changed since the execution; it is not an execution-time snapshot.").FontSize(typeAction).LineHeight(1.45).TextColor(th.TextMuted)
		ui.Text(c, "Session: "+file.SessionID).FontSize(typeAction).TextColor(th.TextMuted).MaxLines(2)
		ui.Text(c, "Task: "+file.TaskID).FontSize(typeAction).TextColor(th.TextMuted).MaxLines(2)
		ui.Text(c, "Message: "+file.MessageID).FontSize(typeAction).TextColor(th.TextMuted).MaxLines(2)
		ui.Text(c, "Call: "+file.CallID).FontSize(typeAction).TextColor(th.TextMuted).MaxLines(2)
		ui.Scroll(c).MaxHeight(160).AlignSelf(ui.Stretch).Children(func() {
			markdownReader(c, file.RawResult, markdownReaderOptions{
				PlainText: true, CopyLabel: "Copy tool record", CopyText: actions.CopyText,
				CopyHandle: &state.CopyRawHandle,
			})
		})
	})
}
