package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestDeriveIssueResultFilesOnlyFromSuccessfulWriteEditResults(t *testing.T) {
	root := t.TempDir()
	writePath := filepath.Join(root, "created.txt")
	editPath := filepath.Join(root, "edited.txt")
	writeResult := fmt.Sprintf(`{ "path" : %q, "bytes_written" : 7 }`, writePath)
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
