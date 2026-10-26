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

// issueResultPanelState is read-only presentation state owned by the caller.
// It contains no file I/O, task dispatch or persistence. Inspections and action
// results arrive through issueResultPanelActions and are rendered on the next
// frame for the matching stable file key.
type issueResultPanelState struct {
	RunLabel   string
	RunStatus  string
	Response   string
	Files      []issueResultFile
	Selected   string
	Inspection *issueResultFileInspection
	InspectFor string
	Checking   bool
	ActionFor  string
	Action     issueResultFileAction
	SourceFor  string
}

// issueResultPanelActions are explicit user requests. The component never
// inspects, opens, reveals or starts a task by itself.
type issueResultPanelActions struct {
	Close        func()
	SelectFile   func(key string)
	CheckFile    func(file issueResultFile)
	OpenFile     func(file issueResultFile)
	RevealFile   func(file issueResultFile)
	ToggleSource func(key string)
	CopyText     func(text string) error
}

func issueResultFileKey(file issueResultFile) string {
	return file.SessionID + "\x00" + file.TaskID + "\x00" + file.CallID
}

// issueResultPanel renders a selected execution result and its reported files.
// The caller supplies the actual run identity, exact response source and file
// rows; this function does not read history or the filesystem.
func issueResultPanel(c *ui.Context, state *issueResultPanelState, actions issueResultPanelActions) ui.Element {
	th := c.Theme()
	root := ui.Column(c).AlignSelf(ui.Stretch).MinWidth(0).Gap(12)
	root.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "Run results").FontSize(16).FontWeight(600).TextColor(th.Text).Grow(1)
			if actions.Close != nil {
				ui.Button(c, "Close results").FontSize(12).OnClick(actions.Close)
			}
		})
		if state.RunLabel != "" {
			ui.Text(c, state.RunLabel).FontSize(12).TextColor(th.TextMuted)
		}
		if state.RunStatus != "" {
			ui.Text(c, state.RunStatus).FontSize(11).FontWeight(500).TextColor(th.TextMuted)
		}

		result := ui.Column(c).AlignSelf(ui.Stretch).MinWidth(0).Padding(12).Radius(10).Background(th.Surface).Gap(8)
		result.Children(func() {
			ui.Text(c, "Result").FontSize(13).FontWeight(600).TextColor(th.Text)
			if state.Response == "" {
				ui.Text(c, "No assistant response was recorded for this run.").FontSize(12).LineHeight(1.45).TextColor(th.TextMuted)
				return
			}
			markdownReader(c, state.Response, markdownReaderOptions{
				CopyLabel: "Copy result",
				CopyText:  actions.CopyText,
			})
		})

		ui.Column(c).AlignSelf(ui.Stretch).MinWidth(0).Gap(8).Children(func() {
			ui.Text(c, "Reported files").FontSize(13).FontWeight(600).TextColor(th.Text)
			if len(state.Files) == 0 {
				ui.Text(c, "No files were reported for this run.").FontSize(12).LineHeight(1.45).TextColor(th.TextMuted)
				return
			}
			for index, file := range state.Files {
				issueResultFileRow(c, th, state, actions, file, index)
			}
		})
	})
	return root
}

func issueResultFileRow(c *ui.Context, th *ui.Theme, state *issueResultPanelState, actions issueResultPanelActions, file issueResultFile, index int) {
	key := issueResultFileKey(file)
	selected := key == state.Selected
	entry := ui.Column(c).AlignSelf(ui.Stretch).MinWidth(0).Padding(10).Radius(8).Border(1, th.Border).Gap(4)
	if selected {
		entry.Background(th.SurfaceHover)
	}
	entry.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, filepath.Base(file.Path)).FontSize(13).FontWeight(600).TextColor(th.Text).MaxLines(1).Grow(1)
			if actions.SelectFile != nil {
				ui.Button(c, fmt.Sprintf("View file %d", index+1)).FontSize(11).
					OnClick(func() { actions.SelectFile(key) })
			}
		})
		ui.Text(c, file.Path).FontSize(11).TextColor(th.TextMuted).MaxLines(2)
		ui.Text(c, "Working directory: "+file.WorkingDirectory).FontSize(11).TextColor(th.TextMuted).MaxLines(2)
		ui.Text(c, "Reported by "+file.ToolName).FontSize(11).TextColor(th.TextMuted).MaxLines(2)
		if state.ActionFor == key && state.Action.Err != "" {
			ui.Text(c, state.Action.Err).FontSize(11).LineHeight(1.4).TextColor(th.Danger)
		} else if state.ActionFor == key && state.Action.Notice != "" {
			ui.Text(c, state.Action.Notice).FontSize(11).LineHeight(1.4).TextColor(th.Warning)
		}
		if selected {
			issueResultFilePreview(c, th, state, actions, file, key)
		}
	})
}

func issueResultFilePreview(c *ui.Context, th *ui.Theme, state *issueResultPanelState, actions issueResultPanelActions, file issueResultFile, key string) {
	var inspection *issueResultFileInspection
	if state.InspectFor == key {
		inspection = state.Inspection
	}
	box := ui.Column(c).AlignSelf(ui.Stretch).MinWidth(0).Padding(10).Radius(8).Background(th.Background).Border(1, th.Border).Gap(6)
	box.Children(func() {
		ui.Text(c, "Current file").FontSize(11).FontWeight(600).TextColor(th.Text)
		ui.Text(c, "Current content may have changed since the run. This is the file at the reported path, not an execution-time snapshot.").
			FontSize(11).LineHeight(1.4).TextColor(th.TextMuted)
		markdownReader(c, file.Path, markdownReaderOptions{
			PlainText: true, CopyLabel: "Copy path", CopyText: actions.CopyText,
		})
		if state.Checking && state.InspectFor == key {
			ui.Text(c, "Checking the current file…").FontSize(11).TextColor(th.TextMuted)
		}
		if inspection != nil {
			if inspection.Notice != "" {
				ui.Text(c, inspection.Notice).FontSize(11).LineHeight(1.4).TextColor(th.Warning)
			}
			if inspection.ReadError != "" {
				ui.Text(c, inspection.ReadError).FontSize(11).LineHeight(1.4).TextColor(th.Danger)
			}
			if inspection.Exists && inspection.Regular && inspection.Notice == "" && inspection.ReadError == "" {
				ui.Scroll(c).MaxHeight(180).AlignSelf(ui.Stretch).Children(func() {
					markdownReader(c, inspection.Preview, markdownReaderOptions{
						PlainText: true, CopyLabel: "Copy current file", CopyText: actions.CopyText,
					})
				})
			}
		} else if !state.Checking {
			ui.Text(c, "Check the current file at this path to preview it.").FontSize(11).TextColor(th.TextMuted)
		}

		ui.Row(c).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			if actions.CheckFile != nil {
				ui.Button(c, "Check again").FontSize(11).OnClick(func() { actions.CheckFile(file) })
			}
			if inspection != nil && inspection.Exists && inspection.Regular {
				if actions.OpenFile != nil {
					ui.Button(c, "Open file").FontSize(11).OnClick(func() { actions.OpenFile(file) })
				}
				if actions.RevealFile != nil {
					ui.Button(c, "Show in Finder").FontSize(11).OnClick(func() { actions.RevealFile(file) })
				}
			}
			if actions.ToggleSource != nil {
				label := "Source & raw record"
				if state.SourceFor == key {
					label = "Hide source & raw record"
				}
				ui.Button(c, label).FontSize(11).OnClick(func() { actions.ToggleSource(key) })
			}
		})

		if state.SourceFor == key {
			issueResultSourceView(c, th, actions, file)
		}
	})
}

func issueResultSourceView(c *ui.Context, th *ui.Theme, actions issueResultPanelActions, file issueResultFile) {
	source := ui.Column(c).AlignSelf(ui.Stretch).MinWidth(0).Gap(4)
	source.Children(func() {
		ui.Text(c, "Source & raw record").FontSize(11).FontWeight(600).TextColor(th.Text)
		ui.Text(c, "Session: "+file.SessionID).FontSize(11).TextColor(th.TextMuted).MaxLines(2)
		ui.Text(c, "Task: "+file.TaskID).FontSize(11).TextColor(th.TextMuted).MaxLines(2)
		ui.Text(c, "Message: "+file.MessageID).FontSize(11).TextColor(th.TextMuted).MaxLines(2)
		ui.Text(c, "Call: "+file.CallID).FontSize(11).TextColor(th.TextMuted).MaxLines(2)
		ui.Scroll(c).MaxHeight(160).AlignSelf(ui.Stretch).Children(func() {
			markdownReader(c, file.RawResult, markdownReaderOptions{
				PlainText: true, CopyLabel: "Copy tool record", CopyText: actions.CopyText,
			})
		})
	})
}
