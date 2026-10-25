package main

// Headless issue/workbench previews. They are inert unless SAKI_PREVIEW_DIR
// points at a scratch directory; they exercise the layout, not a native window
// or a real Runtime.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func issuePreviewApp(scene string) *appModel {
	ct := &contact{
		ID: "saki", Name: "Saki", Role: "Assistant", SessionID: "issue-preview-session", Loaded: true,
		Workspace: "/Users/you/Workspace/Saki", Status: statusReady,
		Issues: []*productIssue{
			{
				ID: "issue-1", ContactID: "saki", Goal: "Ship explicit product issues", Status: issueStatusOpen,
				Requirements:  "Persist the original goal and requirements.\nKeep every execution associated with this issue.",
				WorkSessionID: "issue-preview-work-1",
				Executions: []*issueExecution{
					{ID: "exec-1", SessionID: "issue-preview-work-1", TaskID: "task-1", MessageID: "msg-1", Prompt: "Ship explicit product issues", Status: issueExecutionCompleted},
					{ID: "exec-2", SessionID: "issue-preview-work-1", TaskID: "task-2", MessageID: "msg-2", Prompt: "Ship explicit product issues", Status: issueExecutionCompleted},
				},
			},
			{
				ID: "issue-2", ContactID: "saki", Goal: "Verify restart continuation", Status: issueStatusOpen,
				Requirements:  "Do not turn an accepted draft into a new task after restart.",
				WorkSessionID: "issue-preview-work-2",
				Executions: []*issueExecution{
					{ID: "exec-3", SessionID: "issue-preview-work-2", TaskID: "task-3", MessageID: "msg-3", Prompt: "Verify restart continuation", Status: issueExecutionInterrupted},
				},
			},
		},
		SelectedIssueID: "issue-1",
		Works: []*workItem{
			{
				TaskID: "task-1", MessageID: "msg-1", Prompt: "Ship explicit product issues", State: workCompleted,
				Tools: []*workTool{{CallID: "read-1", Name: "read", Args: `{"path":"desktop/issues.go"}`, Output: `{"content":"productIssue persisted before dispatch","start_line":1,"end_line":2}`, Started: true, Finished: true}},
			},
			{TaskID: "task-2", MessageID: "msg-2", Prompt: "Ship explicit product issues", State: workCompleted},
			{TaskID: "task-3", MessageID: "msg-3", Prompt: "Verify restart continuation", State: workInterrupted},
		},
		Messages: []*message{
			{ID: 1, Kind: msgUser, TaskID: "task-1", MessageID: "msg-1", Text: "Ship explicit product issues"},
			{ID: 2, Kind: msgContact, TaskID: "task-1", MessageID: "msg-1", Text: "Issue persistence and task associations are now visible; requirements remain unverified."},
			{ID: 3, Kind: msgUser, TaskID: "task-2", MessageID: "msg-2", Text: "Ship explicit product issues"},
			{ID: 4, Kind: msgUser, TaskID: "task-3", MessageID: "msg-3", Text: "Verify restart continuation"},
			{ID: 5, Kind: msgContact, TaskID: "task-3", MessageID: "msg-3", Text: "Interrupted before a terminal result was recorded."},
		},
	}
	ct.Issues[0].Work.Loaded = true
	ct.Issues[1].Work.Loaded = true
	ct.Issues[0].Work.Messages = []*message{
		{Key: "work-1-user", Kind: msgUser, TaskID: "task-1", Text: "Ship explicit product issues"},
		{Key: "work-1-result", Kind: msgContact, TaskID: "task-1", Text: "Issue persistence and task associations are now visible; requirements remain unverified."},
	}
	ct.Issues[1].Work.Messages = []*message{
		{Key: "work-2-user", Kind: msgUser, TaskID: "task-3", Text: "Verify restart continuation"},
		{Key: "work-2-result", Kind: msgContact, TaskID: "task-3", Text: "Interrupted before a terminal result was recorded."},
	}
	a := &appModel{
		demo: false, runtimeStatus: runtimeStateReady, rt: &runtimeClient{}, workspaceOpen: true,
		windowWidth: 1040, scrollTo: -1, contacts: []*contact{ct}, selected: 0,
		runtimeCapabilitiesLoaded: true, runtimeMaxActiveTasks: 2,
	}
	switch scene {
	case "issue-working":
		ct.Issues[0].Work.ActiveTaskID = "task-2"
		ct.Issues[0].Work.LastTaskID = "task-2"
		ct.Issues[0].Work.SendState = sendAccepted
		ct.Issues[0].Executions[1].Status = issueExecutionRunning
		ct.Works[1].State = workRunning
		ct.Works[1].Tools = []*workTool{{CallID: "read-2", Name: "read", Args: `{"path":"desktop/workspace.go"}`, Started: true, Finished: false}}
	case "issue-form":
		a.issueFormOpen = true
		a.issueGoal = "Keep requirements attached to each run"
		a.issueRequirements = "Show the original requirement text and an explicit unverified completion state."
	case "issue-unknown":
		ct.Issues[1].Executions[0] = &issueExecution{ID: "exec-3", SessionID: "issue-preview-work-2", Prompt: "Verify restart continuation", Status: issueExecutionUnknown, Detail: "No trusted Runtime task history was found for this execution; manually inspect or confirm not dispatched."}
		ct.SelectedIssueID = "issue-2"
		ct.Works = ct.Works[:1]
	}
	return a
}

func TestPreviewIssueStates(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR to write issue/workbench previews")
	}
	if err := loadAvatar(); err != nil {
		t.Fatal(err)
	}
	write := func(scene string, width, height int, dark bool) {
		theme := "light"
		if dark {
			theme = "dark"
		}
		a := issuePreviewApp(scene)
		a.windowWidth = width
		tr := ui.NewTester(a.view, width, height)
		tr.SetDark(dark)
		tr.Frame()
		writePreviewPNG(t, filepath.Join(dir,
			fmt.Sprintf("headless-%s-%s-%dx%d.png", theme, scene, width, height)), tr.Image())
	}
	for _, dark := range []bool{false, true} {
		write("issue-results", 960, 640, dark)
		write("issue-results", 1040, 720, dark)
		write("issue-results", 1380, 900, dark)
		write("issue-results", 900, 640, dark)
		write("issue-results", 320, 640, dark)
		write("issue-results", 320, 900, dark)
		write("issue-results", 360, 800, dark)
		write("issue-form", 960, 640, dark)
		write("issue-working", 1040, 720, dark)
		write("issue-unknown", 320, 640, dark)
	}
}
