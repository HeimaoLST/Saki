package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestDeriveIssueResultFilesOnlyFromSuccessfulWriteEditResults(t *testing.T) {
	root := t.TempDir()
	writePath := filepath.Join(root, "created.txt")
	editPath := filepath.Join(root, "edited.txt")
	writeResult := fmt.Sprintf(`{ "path": %q, "bytes_written": 7 }`, writePath)
	editResult := fmt.Sprintf(`{"path":%q,"applied":1}`, editPath)
	source := issueResultSource{
		ExecutionID: "exec-1", SessionID: "session-1", TaskID: "task-1", MessageID: "msg-1",
		WorkingDirectory: root,
	}
	history := runtimeHistory{SessionID: "session-1", Records: []historyRecord{
		{Offset: 0, TimestampMS: 100, Record: journalRecord{Type: "task_started", TaskID: "task-1", MessageID: "msg-1"}},
		{Offset: 1, TimestampMS: 110, Record: journalRecord{Type: "messages", Items: []inputItem{
			{Type: "tool_result", CallID: "c-orphan", Output: fmt.Sprintf(`{"path":%q,"bytes_written":1}`, writePath)},
			{Type: "tool_call", CallID: "c-write", Name: "write", Arguments: `{"path":"created.txt","content":"content"}`},
		}}},
		{Offset: 2, TimestampMS: 120, Record: journalRecord{Type: "messages", Items: []inputItem{
			{Type: "tool_result", CallID: "c-write", Output: writeResult},
			{Type: "tool_call", CallID: "c-edit", Name: "edit"},
			{Type: "tool_result", CallID: "c-edit", Output: editResult},
			{Type: "tool_call", CallID: "c-read", Name: "read"},
			{Type: "tool_result", CallID: "c-read", Output: "file body"},
			{Type: "tool_call", CallID: "c-bash", Name: "bash"},
			{Type: "tool_result", CallID: "c-bash", Output: writePath},
			{Type: "tool_call", CallID: "c-error", Name: "write"},
			{Type: "tool_result", CallID: "c-error", Output: `{"error":"permission denied"}`},
			{Type: "tool_call", CallID: "c-relative", Name: "write"},
			{Type: "tool_result", CallID: "c-relative", Output: `{"path":"relative.txt","bytes_written":1}`},
			{Type: "tool_call", CallID: "c-interrupted", Name: "edit"},
			{Type: "tool_result", CallID: "c-interrupted", Output: `{"error":"Execution interrupted before its result was saved.","interrupted":true}`},
			{Type: "tool_call", CallID: "c-zero", Name: "edit"},
			{Type: "tool_result", CallID: "c-zero", Output: fmt.Sprintf(`{"path":%q,"applied":0}`, editPath)},
			{Type: "message", Role: "assistant", Content: []byte(`"I wrote ` + writePath + `"`)},
		}}},
		{Offset: 3, TimestampMS: 130, Record: journalRecord{Type: "task_finished", TaskID: "task-1", Status: "completed"}},
	}}

	derived := deriveIssueResultFiles(source, history)
	if derived.Unavailable != "" {
		t.Fatalf("unavailable = %q", derived.Unavailable)
	}
	if len(derived.Files) != 2 {
		t.Fatalf("files = %+v, want write and edit only", derived.Files)
	}
	write := derived.Files[0]
	if write.CallID != "c-write" || write.ToolName != "write" || write.Path != writePath {
		t.Fatalf("write row = %+v", write)
	}
	if write.RawResult != writeResult {
		t.Fatalf("write raw result = %q, want exact %q", write.RawResult, writeResult)
	}
	if write.ExecutionID != "exec-1" || write.SessionID != "session-1" || write.TaskID != "task-1" ||
		write.MessageID != "msg-1" || write.WorkingDirectory != root {
		t.Fatalf("write identity = %+v", write)
	}
	edit := derived.Files[1]
	if edit.CallID != "c-edit" || edit.ToolName != "edit" || edit.Path != editPath || edit.RawResult != editResult {
		t.Fatalf("edit row = %+v", edit)
	}
}

func TestIssueResultFilesKeepExecutionsAndCallsDistinct(t *testing.T) {
	root := t.TempDir()
	sharedPath := filepath.Join(root, "shared.txt")
	result := fmt.Sprintf(`{"path":%q,"bytes_written":1}`, sharedPath)
	firstHistory := runtimeHistory{SessionID: "session-a", Records: []historyRecord{
		{Record: journalRecord{Type: "task_started", TaskID: "task-a", MessageID: "msg-a"}},
		{Record: journalRecord{Type: "messages", Items: []inputItem{
			{Type: "tool_call", CallID: "call-same", Name: "write"},
			{Type: "tool_result", CallID: "call-same", Output: result},
			{Type: "tool_call", CallID: "call-a2", Name: "write"},
			{Type: "tool_result", CallID: "call-a2", Output: result},
		}}},
		{Record: journalRecord{Type: "task_finished", TaskID: "task-a", Status: "completed"}},
	}}
	secondHistory := runtimeHistory{SessionID: "session-b", Records: []historyRecord{
		{Record: journalRecord{Type: "task_started", TaskID: "task-b", MessageID: "msg-b"}},
		{Record: journalRecord{Type: "messages", Items: []inputItem{
			{Type: "tool_call", CallID: "call-same", Name: "write"},
			{Type: "tool_result", CallID: "call-same", Output: result},
		}}},
		{Record: journalRecord{Type: "task_finished", TaskID: "task-b", Status: "completed"}},
	}}
	first := deriveIssueResultFiles(issueResultSource{
		ExecutionID: "exec-a", SessionID: "session-a", TaskID: "task-a", MessageID: "msg-a", WorkingDirectory: root,
	}, firstHistory)
	if first.Unavailable != "" {
		t.Fatalf("first unavailable = %q", first.Unavailable)
	}
	if len(first.Files) != 2 {
		t.Fatalf("first files = %+v", first.Files)
	}
	for _, file := range first.Files {
		if file.SessionID != "session-a" || file.TaskID != "task-a" || file.ExecutionID != "exec-a" || file.Path != sharedPath {
			t.Fatalf("first crossed identity: %+v", file)
		}
	}
	if first.Files[0].CallID != "call-same" || first.Files[1].CallID != "call-a2" {
		t.Fatalf("call rows = %+v", first.Files)
	}
	second := deriveIssueResultFiles(issueResultSource{
		ExecutionID: "exec-b", SessionID: "session-b", TaskID: "task-b", MessageID: "msg-b", WorkingDirectory: root,
	}, secondHistory)
	if second.Unavailable != "" || len(second.Files) != 1 {
		t.Fatalf("second = %+v", second)
	}
	if second.Files[0].SessionID != "session-b" || second.Files[0].TaskID != "task-b" || second.Files[0].CallID != "call-same" {
		t.Fatalf("second crossed identity: %+v", second.Files[0])
	}
}

func TestDeriveIssueResultFilesReportsUnavailableExecutionIdentity(t *testing.T) {
	history := runtimeHistory{SessionID: "session-1", Records: []historyRecord{
		{Record: journalRecord{Type: "task_started", TaskID: "task-1", MessageID: "msg-1"}},
	}}
	cases := []struct {
		name    string
		source  issueResultSource
		history runtimeHistory
		want    string
	}{
		{name: "no session", source: issueResultSource{TaskID: "task-1"}, history: history, want: "no recorded Runtime session"},
		{name: "history session mismatch", source: issueResultSource{SessionID: "session-other", TaskID: "task-1"}, history: history, want: "different session"},
		{name: "no task", source: issueResultSource{SessionID: "session-1"}, history: history, want: "no recorded Runtime task"},
		{name: "task absent", source: issueResultSource{SessionID: "session-1", TaskID: "task-2"}, history: history, want: "does not contain the execution's task"},
		{name: "message mismatch", source: issueResultSource{SessionID: "session-1", TaskID: "task-1", MessageID: "msg-2"}, history: history, want: "does not record the supplied execution message"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			derived := deriveIssueResultFiles(tc.source, tc.history)
			if !strings.Contains(derived.Unavailable, tc.want) {
				t.Fatalf("unavailable = %q, want %q", derived.Unavailable, tc.want)
			}
			if len(derived.Files) != 0 {
				t.Fatalf("files = %+v", derived.Files)
			}
		})
	}
}

func TestInspectIssueResultFileReadsCurrentFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "result.txt")
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := issueResultFile{Path: path, WorkingDirectory: root}
	first := inspectIssueResultFile(file)
	if !first.Exists || !first.Regular || first.Preview != "first\n" || first.Notice != "" || first.ReadError != "" {
		t.Fatalf("first inspection = %+v", first)
	}
	if first.WorkingDirectory != root {
		t.Fatalf("working directory = %q", first.WorkingDirectory)
	}
	if err := os.WriteFile(path, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := inspectIssueResultFile(file)
	if second.Preview != "second\n" {
		t.Fatalf("current file preview = %q, want current contents not execution snapshot", second.Preview)
	}
}

func TestInspectIssueResultFileReportsMissingNonRegularBinaryAndOversize(t *testing.T) {
	root := t.TempDir()
	missing := inspectIssueResultFile(issueResultFile{Path: filepath.Join(root, "missing.txt")})
	if missing.Exists || !strings.Contains(missing.Notice, "moved or deleted") {
		t.Fatalf("missing = %+v", missing)
	}

	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := inspectIssueResultFile(issueResultFile{Path: directory})
	if !dir.Exists || dir.Regular || !strings.Contains(dir.Notice, "regular file") {
		t.Fatalf("directory = %+v", dir)
	}

	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	pipe := inspectIssueResultFile(issueResultFile{Path: fifo})
	if !pipe.Exists || pipe.Regular || !strings.Contains(pipe.Notice, "regular file") {
		t.Fatalf("fifo = %+v", pipe)
	}

	binary := filepath.Join(root, "binary.bin")
	if err := os.WriteFile(binary, []byte{0xff, 0xfe, 0x00}, 0o600); err != nil {
		t.Fatal(err)
	}
	notText := inspectIssueResultFile(issueResultFile{Path: binary})
	if !notText.Regular || notText.Preview != "" || !strings.Contains(notText.Notice, "not UTF-8") {
		t.Fatalf("binary = %+v", notText)
	}

	large := filepath.Join(root, "large.txt")
	if err := os.WriteFile(large, []byte(strings.Repeat("a", issueResultPreviewLimit+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	oversize := inspectIssueResultFile(issueResultFile{Path: large})
	if oversize.Preview != "" || oversize.Size != issueResultPreviewLimit+1 ||
		!strings.Contains(oversize.Notice, "preview limit") || !strings.Contains(oversize.Notice, "Open the file") {
		t.Fatalf("oversize = %+v", oversize)
	}
}

func TestIssueResultActionsCheckCurrentPathBeforeOpenAndReveal(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(existing, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "missing.txt")
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	var opened, revealed []string
	record := func(list *[]string) func(string) error {
		return func(path string) error {
			*list = append(*list, path)
			return nil
		}
	}
	if action := requestOpenIssueResultFile(issueResultFile{Path: missing}, record(&opened)); action.Notice == "" {
		t.Fatalf("missing open = %+v", action)
	}
	if action := requestRevealIssueResultFile(issueResultFile{Path: missing}, record(&revealed)); action.Notice == "" {
		t.Fatalf("missing reveal = %+v", action)
	}
	if action := requestOpenIssueResultFile(issueResultFile{Path: directory}, record(&opened)); action.Notice == "" {
		t.Fatalf("directory open = %+v", action)
	}
	if action := requestRevealIssueResultFile(issueResultFile{Path: directory}, record(&revealed)); action.Notice == "" {
		t.Fatalf("directory reveal = %+v", action)
	}
	if len(opened) != 0 || len(revealed) != 0 {
		t.Fatalf("actions called on unavailable paths: opened=%v revealed=%v", opened, revealed)
	}

	if action := requestOpenIssueResultFile(issueResultFile{Path: existing}, record(&opened)); action.Err != "" || action.Notice != "" {
		t.Fatalf("existing open = %+v", action)
	}
	if action := requestRevealIssueResultFile(issueResultFile{Path: existing}, record(&revealed)); action.Err != "" || action.Notice != "" {
		t.Fatalf("existing reveal = %+v", action)
	}
	if len(opened) != 1 || opened[0] != existing || len(revealed) != 1 || revealed[0] != existing {
		t.Fatalf("opened=%v revealed=%v", opened, revealed)
	}

	openErr := errors.New("no handler registered")
	if action := requestOpenIssueResultFile(issueResultFile{Path: existing}, func(string) error { return openErr }); !strings.Contains(action.Err, openErr.Error()) {
		t.Fatalf("open error = %+v", action)
	}
	revealErr := errors.New("file manager refused")
	if action := requestRevealIssueResultFile(issueResultFile{Path: existing}, func(string) error { return revealErr }); !strings.Contains(action.Err, revealErr.Error()) {
		t.Fatalf("reveal error = %+v", action)
	}
}

func TestIssueResultMissingFileRecoversOnlyByExplicitReinspection(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "restored.txt")
	file := issueResultFile{Path: path, WorkingDirectory: root}

	missing := inspectIssueResultFile(file)
	if missing.Exists || !strings.Contains(missing.Notice, "moved or deleted") {
		t.Fatalf("missing inspection = %+v", missing)
	}
	if err := os.WriteFile(path, []byte("restored content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored := inspectIssueResultFile(file)
	if !restored.Exists || restored.Preview != "restored content\n" {
		t.Fatalf("reinspection = %+v", restored)
	}
	opened := ""
	action := requestOpenIssueResultFile(file, func(got string) error {
		opened = got
		return nil
	})
	if action.Err != "" || action.Notice != "" || opened != path {
		t.Fatalf("open after explicit reinspection = %+v opened=%q", action, opened)
	}
}

func TestIssueResultPathRetainsTrailingSpaceFilename(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "report ")
	if err := os.WriteFile(path, []byte("trailing space file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := issueResultSource{
		ExecutionID: "exec-space", SessionID: "session-space", TaskID: "task-space", MessageID: "msg-space",
		WorkingDirectory: root,
	}
	history := runtimeHistory{SessionID: "session-space", Records: []historyRecord{
		{Record: journalRecord{Type: "task_started", TaskID: "task-space", MessageID: "msg-space"}},
		{Record: journalRecord{Type: "messages", Items: []inputItem{
			{Type: "tool_call", CallID: "call-space", Name: "write"},
			{Type: "tool_result", CallID: "call-space", Output: fmt.Sprintf(`{"path":%q,"bytes_written":20}`, path)},
		}}},
	}}
	derived := deriveIssueResultFiles(source, history)
	if derived.Unavailable != "" || len(derived.Files) != 1 {
		t.Fatalf("derived = %+v", derived)
	}
	file := derived.Files[0]
	if file.Path != path || !strings.HasSuffix(file.Path, " ") {
		t.Fatalf("reported path = %q, want exact %q", file.Path, path)
	}
	inspection := inspectIssueResultFile(file)
	if !inspection.Exists || inspection.Preview != "trailing space file\n" {
		t.Fatalf("inspection = %+v", inspection)
	}
	opened := ""
	action := requestOpenIssueResultFile(file, func(got string) error {
		opened = got
		return nil
	})
	if action.Err != "" || action.Notice != "" || opened != path {
		t.Fatalf("open action = %+v opened=%q, want %q", action, opened, path)
	}
}

func issueResultPanelTester(state *issueResultPanelState, actions issueResultPanelActions, width, height int, dark bool) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		c.SetTheme(resolveWorkbenchTheme(dark).Theme)
		closeReader := func() {
			if state.ReaderKey != "" {
				state.ReaderReturnKey = state.ReaderKey
			}
			state.ReaderKey = ""
		}
		c.OnShortcut(0, ui.KeyEscape, closeReader)
		if state.ReaderKey != "" {
			if file, ok := issueResultFileByKey(state.Files, state.ReaderKey); ok {
				issueResultReaderPage(c, c.Theme(), state, actions, file)
				return
			}
			state.ReaderKey = ""
		}
		ui.Column(c).FillHeight().Children(func() {
			issueResultPageHeader(c, c.Theme(), state, actions, true)
			ui.Scroll(c).Grow(1).Padding(0, 16).Children(func() {
				issueResultPanel(c, state, actions)
			})
		})
	}, width, height)
}

func issueResultPanelFixtureFile(key, path, raw string) issueResultFile {
	return issueResultFile{
		ExecutionID: "exec-" + key, SessionID: "session-" + key, TaskID: "task-" + key, MessageID: "msg-" + key,
		CallID: "call-" + key, ToolName: "write", Path: path, WorkingDirectory: "/workspace/" + key, RawResult: raw,
	}
}

func TestIssueResultPanelCopiesExactResultMarkdownAndCode(t *testing.T) {
	source := "# Result\n\nExact **raw** source.\n\n```go\nfunc main() {\n  println(\"hi\")\n}\n```\n"
	state := &issueResultPanelState{
		RunLabel:  "Run 2: result: Ship the issue result panel",
		RunStatus: "Completed",
		Response:  source,
		Files: []issueResultFile{
			issueResultPanelFixtureFile("one", "/tmp/workspace/report.md", `{"path":"/tmp/workspace/report.md","bytes_written":12}`),
		},
	}
	tr := issueResultPanelTester(state, issueResultPanelActions{SelectFile: func(string) {}}, 600, 900, false)
	tr.Frame()
	for _, want := range []string{"Run results", "Run 2: result: Ship the issue result panel", "Completed", "Result", "Reported files", "report.md", "Reported by write", "Read file 1: report.md", "Read file →"} {
		if !tr.HasText(want) {
			t.Fatalf("missing %q in %q", want, tr.Texts())
		}
	}
	if err := tr.Click("Copy result"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != source {
		t.Fatalf("result copy = %q, want exact %q", got, source)
	}
	if err := tr.Click("Copy code"); err != nil {
		t.Fatal(err)
	}
	if got, want := tr.Clipboard(), "func main() {\n  println(\"hi\")\n}\n"; got != want {
		t.Fatalf("code copy = %q, want %q", got, want)
	}
	if tr.HasText("Session:") || tr.HasText("Task:") || tr.HasText("call-one") {
		t.Fatalf("debug identity was visible by default: %q", tr.Texts())
	}
}

func TestIssueResultPanelNoResponseStillShowsReportedFilesAndClose(t *testing.T) {
	file := issueResultPanelFixtureFile("one", "/tmp/workspace/created.txt", `{"path":"/tmp/workspace/created.txt","bytes_written":1}`)
	state := &issueResultPanelState{
		RunLabel:  "Run 1: result: No response case",
		RunStatus: "Completed",
		Files:     []issueResultFile{file},
	}
	closed := 0
	tr := issueResultPanelTester(state, issueResultPanelActions{Close: func() { closed++ }}, 600, 700, false)
	tr.Frame()
	for _, want := range []string{"No assistant response was recorded for this run.", "Reported files", "created.txt", file.Path, "Working directory: /workspace/one", "Reported by write"} {
		if !tr.HasText(want) {
			t.Fatalf("missing %q in %q", want, tr.Texts())
		}
	}
	if err := tr.Click("Close results"); err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Fatalf("close calls = %d", closed)
	}
}

func TestIssueResultPanelPreviewReadsCurrentFileAndCopiesExactPath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "current.txt")
	if err := os.WriteFile(path, []byte("alpha-current\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := issueResultPanelFixtureFile("one", path, fmt.Sprintf(`{"path":%q,"bytes_written":14}`, path))
	state := &issueResultPanelState{
		RunLabel: "Run 1: result: Current file", RunStatus: "Completed", Response: "Result body.",
		Files: []issueResultFile{file}, Selected: issueResultFileKey(file),
	}
	inspection := inspectIssueResultFile(file)
	state.Inspection, state.InspectFor = &inspection, issueResultFileKey(file)
	tr := issueResultPanelTester(state, issueResultPanelActions{CheckFile: func(issueResultFile) {}}, 600, 900, false)
	tr.Frame()
	if err := tr.Click("Read file 1: current.txt"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	for _, want := range []string{"Current file", "alpha-current", "Copy current file", "Copy path", "Check again"} {
		if !tr.HasText(want) {
			t.Fatalf("missing %q in %q", want, tr.Texts())
		}
	}
	if err := os.WriteFile(path, []byte("beta-current\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	updated := inspectIssueResultFile(file)
	state.Inspection = &updated
	tr.Frame()
	if !tr.HasText("beta-current") || tr.HasText("alpha-current") {
		t.Fatalf("preview did not follow current disk content: %q", tr.Texts())
	}
	if err := tr.Click("Copy current file"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != "beta-current\n" {
		t.Fatalf("current file copy = %q", got)
	}
	if err := tr.Click("Copy path"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != path {
		t.Fatalf("path copy = %q, want exact %q", got, path)
	}
}

func TestIssueResultPanelMissingFileKeepsPathAndRecordCopyAndRetries(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "missing.txt")
	raw := `{"path":"` + path + `","bytes_written":7}`
	file := issueResultPanelFixtureFile("one", path, raw)
	key := issueResultFileKey(file)
	inspection := inspectIssueResultFile(file)
	state := &issueResultPanelState{
		RunLabel: "Run 1: result: Missing file", RunStatus: "Completed", Response: "Created a file.",
		Files: []issueResultFile{file}, Selected: key, Inspection: &inspection, InspectFor: key, Checking: true,
		ActionFor: key, Action: issueResultFileAction{Path: path, Err: "Could not open the current file: no handler"},
	}
	checks := 0
	actions := issueResultPanelActions{
		ToggleSource: func(got string) { state.SourceFor = got },
		CheckFile: func(got issueResultFile) {
			checks++
			if err := os.WriteFile(path, []byte("restored\n"), 0o600); err != nil {
				return
			}
			fresh := inspectIssueResultFile(got)
			state.Checking, state.Inspection, state.InspectFor = false, &fresh, issueResultFileKey(got)
			state.ActionFor, state.Action = "", issueResultFileAction{}
		},
	}
	tr := issueResultPanelTester(state, actions, 600, 900, false)
	tr.Frame()
	if err := tr.Click("Read file 1: missing.txt"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	for _, want := range []string{"Checking the current file…", "Current file", "No file exists at the reported path", "Could not open the current file: no handler", "Copy path", "Check again", "Source & raw record"} {
		if !tr.HasText(want) {
			t.Fatalf("missing %q in %q", want, tr.Texts())
		}
	}
	if tr.HasText("Open file") || tr.HasText("Show in Finder") {
		t.Fatalf("missing file exposed OS actions: %q", tr.Texts())
	}
	if err := tr.Click("Copy path"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != path {
		t.Fatalf("missing path copy = %q, want %q", got, path)
	}
	if err := tr.Click("Source & raw record"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("Session: session-one") || !tr.HasText("Call: call-one") {
		t.Fatalf("expanded source identity missing: %q", tr.Texts())
	}
	if err := tr.Click("Copy tool record"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != raw {
		t.Fatalf("raw record copy = %q, want exact %q", got, raw)
	}
	if err := tr.Click("Check again"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if checks != 1 || !tr.HasText("restored") {
		t.Fatalf("retry checks=%d texts=%q", checks, tr.Texts())
	}
}

func TestIssueResultPanelUnsupportedAndOversizeOfferExternalOpen(t *testing.T) {
	root := t.TempDir()
	binaryPath := filepath.Join(root, "binary.bin")
	if err := os.WriteFile(binaryPath, []byte{0xff, 0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}
	largePath := filepath.Join(root, "large.txt")
	if err := os.WriteFile(largePath, []byte(strings.Repeat("a", issueResultPreviewLimit+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"binary", binaryPath, "not UTF-8"},
		{"oversize", largePath, "preview limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := issueResultPanelFixtureFile(tc.name, tc.path, fmt.Sprintf(`{"path":%q,"bytes_written":1}`, tc.path))
			inspection := inspectIssueResultFile(file)
			state := &issueResultPanelState{
				RunLabel: "Run 1: result: " + tc.name, RunStatus: "Completed", Files: []issueResultFile{file},
				Selected: issueResultFileKey(file), Inspection: &inspection, InspectFor: issueResultFileKey(file),
			}
			opened, revealed := 0, 0
			tr := issueResultPanelTester(state, issueResultPanelActions{
				OpenFile:   func(issueResultFile) { opened++ },
				RevealFile: func(issueResultFile) { revealed++ },
			}, 600, 800, false)
			tr.Frame()
			if err := tr.Click("Read file 1: " + filepath.Base(tc.path)); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if !tr.HasText(tc.want) || !tr.HasText("Open file") || !tr.HasText("Show in Finder") {
				t.Fatalf("unsupported state missing: %q", tr.Texts())
			}
			if err := tr.Click("Open file"); err != nil {
				t.Fatal(err)
			}
			if err := tr.Click("Show in Finder"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			if opened != 1 || revealed != 1 {
				t.Fatalf("opened=%d revealed=%d", opened, revealed)
			}
			if tr.HasText("Opened") || tr.HasText("Found in Finder") {
				t.Fatalf("component claimed an unverified OS outcome: %q", tr.Texts())
			}
		})
	}
}

func TestIssueResultPanelEmptyFilePreviewAndCopyAreValid(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "empty.txt")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	file := issueResultPanelFixtureFile("empty", path, fmt.Sprintf(`{"path":%q,"bytes_written":0}`, path))
	inspection := inspectIssueResultFile(file)
	if !inspection.Exists || !inspection.Regular || inspection.Preview != "" || inspection.Notice != "" || inspection.ReadError != "" {
		t.Fatalf("empty inspection = %+v", inspection)
	}
	state := &issueResultPanelState{
		RunLabel: "Run 1: result: Empty file", RunStatus: "Completed", Files: []issueResultFile{file},
		Selected: issueResultFileKey(file), Inspection: &inspection, InspectFor: issueResultFileKey(file),
	}
	tr := issueResultPanelTester(state, issueResultPanelActions{}, 600, 700, false)
	tr.Frame()
	if err := tr.Click("Read file 1: empty.txt"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("Empty file (0 bytes).") || !tr.HasText("Copy current file") {
		t.Fatalf("empty file preview missing: %q", tr.Texts())
	}
	tr.SetClipboard("sentinel")
	if err := tr.Click("Copy current file"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != "" {
		t.Fatalf("empty file copy = %q, want empty", got)
	}
}

func TestIssueResultPanelRepeatedPathKeepsCallIdentity(t *testing.T) {
	path := "/tmp/workspace/shared.txt"
	first := issueResultPanelFixtureFile("one", path, `{"path":"/tmp/workspace/shared.txt","bytes_written":1}`)
	second := issueResultPanelFixtureFile("two", path, `{"path":"/tmp/workspace/shared.txt","bytes_written":2}`)
	state := &issueResultPanelState{
		RunLabel: "Run 1: result: Repeated path", RunStatus: "Completed",
		Files: []issueResultFile{first, second},
	}
	tr := issueResultPanelTester(state, issueResultPanelActions{
		SelectFile:   func(key string) { state.Selected = key },
		ToggleSource: func(key string) { state.SourceFor = key },
	}, 600, 900, false)
	tr.Frame()
	for _, want := range []string{"Read file 1: shared.txt", "Read file 2: shared.txt", "Reported by write", path} {
		if !tr.HasText(want) {
			t.Fatalf("missing %q in %q", want, tr.Texts())
		}
	}
	if tr.HasText("call-one") || tr.HasText("call-two") {
		t.Fatalf("call IDs were visible before source expansion: %q", tr.Texts())
	}
	if err := tr.Click("Read file 2: shared.txt"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if state.Selected != issueResultFileKey(second) {
		t.Fatalf("selected key = %q, want second call", state.Selected)
	}
	if err := tr.Click("Source & raw record"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Copy tool record"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != second.RawResult {
		t.Fatalf("raw record = %q, want second call %q", got, second.RawResult)
	}
}

func TestIssueResultPanelLongContentWrapsAtNarrowWidth(t *testing.T) {
	longToken := strings.Repeat("narrow-token-", 18)
	path := "/tmp/workspace/" + strings.Repeat("very-long-directory-", 6) + "result.txt"
	file := issueResultPanelFixtureFile("long", path, fmt.Sprintf(`{"path":%q,"bytes_written":1}`, path))
	inspection := issueResultFileInspection{
		Path: path, WorkingDirectory: file.WorkingDirectory, Exists: true, Regular: true,
		Preview: longToken + "\n",
	}
	state := &issueResultPanelState{
		RunLabel:  "Run 1: result: " + strings.Repeat("A long run label that must wrap. ", 6),
		RunStatus: "Completed with a long status line that still wraps quietly", Response: longToken,
		Files: []issueResultFile{file}, Selected: issueResultFileKey(file),
		Inspection: &inspection, InspectFor: issueResultFileKey(file), SourceFor: issueResultFileKey(file),
	}
	tr := issueResultPanelTester(state, issueResultPanelActions{Close: func() {}, CheckFile: func(issueResultFile) {}, ToggleSource: func(string) {}}, 340, 900, false)
	tr.Frame()
	for _, want := range []string{"Run results", "Close results", state.RunLabel, state.RunStatus, path} {
		bounds, ok := tr.Find(want)
		if !ok {
			t.Fatalf("missing result %q in %q", want, tr.Texts())
		}
		if bounds.X < 0 || bounds.X+bounds.W > 340 {
			t.Fatalf("result %q overflows 340px: %+v", want, bounds)
		}
	}
	if err := tr.Click("Read file 1: " + filepath.Base(path)); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	for _, want := range []string{"Close reading", "← Back to results", path, "Copy path", "Check again", "Source & raw record", "Hide source & raw record"} {
		bounds, ok := tr.Find(want)
		if !ok {
			t.Fatalf("missing reader %q in %q", want, tr.Texts())
		}
		if bounds.X < 0 || bounds.X+bounds.W > 340 {
			t.Fatalf("reader %q overflows 340px: %+v", want, bounds)
		}
	}
}

func TestIssueResultPanelPreviewsAlwaysWriteHeadlessPNGs(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := "/tmp/workspace/report with a long name that still wraps.txt"
	file := issueResultPanelFixtureFile("preview", path, fmt.Sprintf(`{"path":%q,"bytes_written":1}`, path))
	inspection := issueResultFileInspection{Path: path, WorkingDirectory: file.WorkingDirectory, Exists: true, Regular: true, Preview: "current preview\n"}
	state := &issueResultPanelState{
		RunLabel: "Run 1: result: Preview", RunStatus: "Completed",
		Response: "# Result\n\nPreview body with `code`.\n\n```go\nfunc main() {}\n```\n",
		Files:    []issueResultFile{file}, Selected: issueResultFileKey(file),
		Inspection: &inspection, InspectFor: issueResultFileKey(file),
	}
	for _, width := range []int{340, 600} {
		for _, dark := range []bool{false, true} {
			dark := dark
			tr := issueResultPanelTester(state, issueResultPanelActions{}, width, 900, dark)
			tr.SetDark(dark)
			tr.Frame()
			theme := "light"
			if dark {
				theme = "dark"
			}
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("issue-result-panel-%s-%d.png", theme, width)), tr.Image())
		}
	}
}

func TestIssueResultPanelCopyPathShowsReaderFailureFeedback(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "copy-source.txt")
	if err := os.WriteFile(path, []byte("content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := issueResultPanelFixtureFile("copy", path, fmt.Sprintf(`{"path":%q,"bytes_written":8}`, path))
	inspection := inspectIssueResultFile(file)
	state := &issueResultPanelState{
		RunLabel: "Run 1: result: Copy failure", RunStatus: "Completed", Files: []issueResultFile{file},
		Selected: issueResultFileKey(file), Inspection: &inspection, InspectFor: issueResultFileKey(file),
	}
	copyErr := errors.New("clipboard unavailable")
	fail := true
	calls := 0
	actions := issueResultPanelActions{CopyText: func(string) error {
		calls++
		if fail {
			return copyErr
		}
		return nil
	}}
	tr := issueResultPanelTester(state, actions, 600, 800, false)
	tr.Frame()
	if err := tr.Click("Read file 1: copy-source.txt"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	button, ok := tr.Find("Copy path")
	if !ok {
		t.Fatalf("copy path control is missing: %q", tr.Texts())
	}
	before := tr.Image()
	if err := tr.Click("Copy path"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("copy failure was swallowed: calls=%d", calls)
	}
	if got, ok := tr.Find("Copy path"); !ok || got != button {
		t.Fatalf("copy failure moved the control: before=%+v after=%+v", button, got)
	}
	failedImage := tr.Image()
	if readerImageRegionDiff(before, failedImage, button) == 0 {
		t.Fatal("copy failure was not shown")
	}
	fail = false
	if err := tr.Click("Copy path"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("copy success callback missing: calls=%d", calls)
	}
	if got, ok := tr.Find("Copy path"); !ok || got != button {
		t.Fatalf("copy success moved the control: before=%+v after=%+v", button, got)
	}
	if readerImageRegionDiff(failedImage, tr.Image(), button) == 0 {
		t.Fatal("copy success feedback does not differ from failure feedback")
	}
}

func TestIssueResultReaderRendersMarkdownAndCopiesOriginalBytes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "中文结果.md")
	source := "# 中文标题\n\n这是中文段落，包含 **重点** 与 `inline`。\n\n- 第一项\n- 第二项\n\n| 名称 | 值 |\n| --- | --- |\n| 语言 | 中文 |\n| 状态 | 完成 |\n\n```go\nfunc main() {\n\tprintln(\"保留缩进\")\n}\n```\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	file := issueResultPanelFixtureFile("md", path, fmt.Sprintf(`{"path":%q,"bytes_written":%d}`, path, len(source)))
	inspection := inspectIssueResultFile(file)
	state := &issueResultPanelState{
		RunLabel: "Run 1: result: Markdown", RunStatus: "Completed", Files: []issueResultFile{file},
		Selected: issueResultFileKey(file), Inspection: &inspection, InspectFor: issueResultFileKey(file),
	}
	tr := issueResultPanelTester(state, issueResultPanelActions{SelectFile: func(string) {}}, 600, 900, false)
	tr.Frame()
	if err := tr.Click("Read file 1: 中文结果.md"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	for _, want := range []string{"中文标题", "这是中文段落", "第一项", "名称", "语言", "完成", "func main() {", "保留缩进"} {
		if !tr.HasText(want) {
			t.Fatalf("Markdown reader missing %q in %q", want, tr.Texts())
		}
	}
	if err := tr.Click("Copy current file"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != source {
		t.Fatalf("current file copy = %q, want exact original bytes %q", got, source)
	}
}

func TestIssueResultReaderKeepsOrdinaryTextPlain(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	source := "# 这不是标题\n\n`这不是 inline code`\n\n| 不是 | 表格 |\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	file := issueResultPanelFixtureFile("txt", path, fmt.Sprintf(`{"path":%q,"bytes_written":%d}`, path, len(source)))
	inspection := inspectIssueResultFile(file)
	state := &issueResultPanelState{
		RunLabel: "Run 1: result: Plain text", RunStatus: "Completed", Files: []issueResultFile{file},
		Selected: issueResultFileKey(file), Inspection: &inspection, InspectFor: issueResultFileKey(file),
	}
	tr := issueResultPanelTester(state, issueResultPanelActions{}, 600, 800, false)
	tr.Frame()
	if err := tr.Click("Read file 1: notes.txt"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	for _, want := range []string{"# 这不是标题", "`这不是 inline code`", "| 不是 | 表格 |"} {
		if !tr.HasText(want) {
			t.Fatalf("ordinary text was parsed as Markdown, missing %q in %q", want, tr.Texts())
		}
	}
	if _, ok := tr.Find("Table, wrapped plain text"); ok {
		t.Fatalf("ordinary .txt was rendered as a Markdown table: %q", tr.Texts())
	}
}

func TestIssueResultReaderEscapeAndCloseReturnToFileRow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "return.md")
	source := "# Return\n\nreader return check\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	file := issueResultPanelFixtureFile("return", path, fmt.Sprintf(`{"path":%q,"bytes_written":%d}`, path, len(source)))
	inspection := inspectIssueResultFile(file)
	state := &issueResultPanelState{
		RunLabel: "Run 1: result: Return", RunStatus: "Completed", Files: []issueResultFile{file},
		Selected: issueResultFileKey(file), Inspection: &inspection, InspectFor: issueResultFileKey(file),
	}
	tr := issueResultPanelTester(state, issueResultPanelActions{SelectFile: func(string) {}}, 600, 900, false)
	tr.Frame()
	rowLabel := "Read file 1: return.md"
	if err := tr.Click(rowLabel); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("reader return check") || !tr.Focused("Close reading") {
		t.Fatalf("reader did not open: %q", tr.Texts())
	}
	tr.Key(0, ui.KeyEscape)
	tr.Frame()
	if tr.HasText("reader return check") || !tr.HasText("Run results") || !tr.HasText(rowLabel) {
		t.Fatalf("Escape did not close only the reader: %q", tr.Texts())
	}
	if !tr.Focused(rowLabel) {
		t.Fatalf("Escape did not return focus to file row: %q", tr.Texts())
	}
	if err := tr.Click(rowLabel); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Close reading"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if tr.HasText("File reading") || !tr.Focused(rowLabel) {
		t.Fatalf("Close reading did not return to row: %q", tr.Texts())
	}
}

func TestIssueResultReaderShowsReadErrorWithCurrentPath(t *testing.T) {
	path := "/tmp/workspace/read-error.md"
	file := issueResultPanelFixtureFile("error", path, fmt.Sprintf(`{"path":%q,"bytes_written":1}`, path))
	inspection := issueResultFileInspection{
		Path: path, WorkingDirectory: file.WorkingDirectory, Exists: true, Regular: true,
		ReadError: "Could not read the current file: permission denied",
	}
	state := &issueResultPanelState{
		RunLabel: "Run 1: result: Read error", RunStatus: "Completed", Files: []issueResultFile{file},
		Selected: issueResultFileKey(file), Inspection: &inspection, InspectFor: issueResultFileKey(file),
	}
	tr := issueResultPanelTester(state, issueResultPanelActions{CheckFile: func(issueResultFile) {}}, 600, 800, false)
	tr.Frame()
	if err := tr.Click("Read file 1: read-error.md"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	for _, want := range []string{"Current file", "Could not read the current file: permission denied", path, "Copy path", "Check again"} {
		if !tr.HasText(want) {
			t.Fatalf("read error missing %q in %q", want, tr.Texts())
		}
	}
}

func TestIssueResultReaderLongChineseNameWrapsAtNarrowWidth(t *testing.T) {
	longName := strings.Repeat("很长的中文结果文件名", 5) + ".md"
	path := "/tmp/workspace/" + longName
	file := issueResultPanelFixtureFile("zh", path, fmt.Sprintf(`{"path":%q,"bytes_written":1}`, path))
	inspection := issueResultFileInspection{
		Path: path, WorkingDirectory: file.WorkingDirectory, Exists: true, Regular: true, Preview: "# 中文\n",
	}
	state := &issueResultPanelState{
		RunLabel: "Run 1: result: 中文长名称", RunStatus: "Completed", Files: []issueResultFile{file},
		Selected: issueResultFileKey(file), Inspection: &inspection, InspectFor: issueResultFileKey(file),
	}
	tr := issueResultPanelTester(state, issueResultPanelActions{}, 340, 900, false)
	tr.Frame()
	rowLabel := "Read file 1: " + longName
	for _, want := range []string{rowLabel, "Read file →"} {
		bounds, ok := tr.Find(want)
		if !ok {
			t.Fatalf("missing %q in %q", want, tr.Texts())
		}
		if bounds.X < 0 || bounds.X+bounds.W > 340 {
			t.Fatalf("%q overflowed 340px: %+v", want, bounds)
		}
	}
}
