package main

// Headless previews for the compact issue detail and explicit secondary
// actions. Inert unless SAKI_PREVIEW_DIR is set; not a native window run.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func issueDetailPreviewApp(t *testing.T, expanded bool) (*appModel, *productIssue) {
	a, ct := workspaceTestApp(t)
	a.rt = &runtimeClient{}
	a.runtimeCapabilitiesLoaded = true
	a.runtimeMaxActiveTasks = 2
	a.windowWidth = 1040
	issue := &productIssue{
		ID: "issue-preview-detail", ContactID: ct.ID, Goal: "整理本周发布资料的临时结果",
		Status: issueStatusInProgress, RequirementsRevision: 1, WorkSessionID: "work-detail",
		Requirements: parseIssueRequirements("保留资料中的项目代号、负责人和下一步。\n将整理结果写入临时 result.txt。\n不要自动把 Runtime 完成当作事项完成。"),
		Executions: []*issueExecution{{
			ID: "exec-detail", SessionID: "work-detail", TaskID: "task-detail", MessageID: "message-detail",
			Prompt: "整理本周发布资料并输出 result.txt", Status: issueExecutionCompleted,
			RequirementsRevision: 1,
		}},
	}
	issue.Requirements[0].State = requirementSatisfied
	issue.Requirements[0].Evidence = "资料中的三项字段已经保留。"
	issue.Requirements[1].State = requirementUnverified
	issue.Work.Loaded = true
	issue.Work.Messages = []*message{{
		Key: "preview-result", Kind: msgContact, TaskID: "task-detail",
		Text: "已整理完成。项目代号：青岚；负责人：林；下一步：整理成 result.txt。结果写在临时文件中，未改动其他文件。",
	}}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	a.issueDetailOpen = map[string]bool{issue.ID: expanded}
	if expanded {
		issue.Executions[0].ContextExpanded = true
	}
	return a, issue
}

func TestPreviewIssueReadableDetails(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR to write issue-detail previews")
	}
	if err := loadAvatar(); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, expanded := range []bool{false, true} {
			for _, size := range []struct{ width, height int }{{1040, 720}, {960, 640}, {720, 640}, {1380, 900}} {
				a, _ := issueDetailPreviewApp(t, expanded)
				a.windowWidth = size.width
				tr := ui.NewTester(a.view, size.width, size.height)
				tr.SetDark(dark)
				tr.Frame()
				state := "compact"
				if expanded {
					state = "expanded"
				}
				writePreviewPNG(t, filepath.Join(dir,
					fmt.Sprintf("headless-%s-issue-detail-%s-%dx%d.png", theme, state, size.width, size.height)), tr.Image())
			}
		}
	}
}
