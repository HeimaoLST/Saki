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
		for _, size := range []struct{ width, height int }{{1040, 720}, {960, 640}, {720, 640}, {1380, 900}} {
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
