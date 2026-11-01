package main

// Headless previews for the readable workspace tool card. These are inert
// unless SAKI_PREVIEW_DIR is set and do not exercise a native window.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestPreviewWorkspaceToolCardWrapping(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR to write workspace tool-card previews")
	}
	if err := loadAvatar(); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, size := range []struct{ width, height int }{{1040, 720}, {960, 640}, {720, 640}, {320, 900}, {360, 800}, {1380, 900}} {
			a, ct := workspaceTestApp(t)
			ct.ActiveTaskID = "t1"
			ct.LastTaskID = "t1"
			w := a.ensureWork(ct, "t1", "m1")
			w.State = workRunning
			w.Prompt = "整理测试资料并报告结果"
			longPath := "/Users/someone/" + strings.Repeat("非常长的目录/", 20) + "file.json"
			w.Tools = []*workTool{{
				CallID: "c1", Name: "read", Finished: true,
				Args:   `{"path":"` + longPath + `"}`,
				Output: `{"content":"` + strings.Repeat("非常长的未换行工具输出/very/long/unbroken/path/", 12) + `","path":"` + longPath + `"}`,
			}}
			a.windowWidth = size.width
			tr := ui.NewTester(a.view, size.width, size.height)
			tr.SetDark(dark)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir,
				fmt.Sprintf("headless-%s-workspace-tool-card-%dx%d.png", theme, size.width, size.height)), tr.Image())
		}
	}
}

// TestPreviewToolFailureDetails captures the bounded #113 evidence gap: a
// failed conversation task with its diagnostic and expanded received output,
// opened through the real Conversation work and Details controls. It writes
// only the four requested light/dark normal/minimum previews.
func TestPreviewToolFailureDetails(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR to write tool-failure details previews")
	}
	if err := loadAvatar(); err != nil {
		t.Fatal(err)
	}
	const diagnostic = "tool_error: permission denied while reading customer-report.md"
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, size := range []struct{ width, height int }{{1040, 720}, {720, 640}} {
			a, ct := workspaceTestApp(t)
			ct.LastTaskID = "task-failure"
			w := a.ensureWork(ct, "task-failure", "message-failure")
			w.State = workFailed
			w.Prompt = "Read the customer report and extract its delivery risks."
			w.Detail = diagnostic
			w.Tools = []*workTool{{
				CallID: "call-failure", Name: "read",
				Args:    `{"path":"customer-report.md"}`,
				Output:  "received-output-start-marker\n" + strings.Repeat("received diagnostic line with enough content to remain readable in the expanded details\n", 160),
				Started: true, Finished: true,
			}}
			a.windowWidth = size.width
			tr := ui.NewTester(a.view, size.width, size.height)
			tr.SetDark(dark)
			tr.Frame()
			openConversationWorkPanel(t, tr)
			if !tr.HasText("Failed") || !tr.HasText(diagnostic) {
				t.Fatalf("%s %dx%d failed work/diagnostic missing: %q", theme, size.width, size.height, tr.Texts())
			}
			if err := tr.Click("Details"); err != nil {
				t.Fatalf("%s %dx%d Details: %v", theme, size.width, size.height, err)
			}
			tr.Frame()
			if !tr.HasText("Hide") {
				t.Fatalf("%s %dx%d details did not expand: %q", theme, size.width, size.height, tr.Texts())
			}
			if !tr.HasText("received-output-start-marker") {
				t.Fatalf("%s %dx%d received output did not render: %q", theme, size.width, size.height, tr.Texts())
			}
			writePreviewPNG(t, filepath.Join(dir,
				fmt.Sprintf("tool-failure-details-%s-%dx%d.png", theme, size.width, size.height)), tr.Image())
		}
	}
}
