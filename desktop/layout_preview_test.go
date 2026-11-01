package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestPreviewAdjustableLayout is an opt-in headless matrix for the global
// workbench layout. It is not native drag or native window evidence.
func TestPreviewAdjustableLayout(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR to write adjustable-layout previews")
	}
	if err := loadAvatar(); err != nil {
		t.Fatal(err)
	}
	scenes := []string{"closed", "open", "dragged"}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, scene := range scenes {
			for _, size := range []struct{ width, height int }{{1040, 720}, {1380, 900}, {960, 640}, {720, 640}} {
				a, ct := workspaceTestApp(t)
				a.windowWidth = size.width
				a.workspaceOpen = scene != "closed"
				if scene == "dragged" && size.width >= 1040 {
					a.leftPaneWidth = defaultLeftPaneWidth
					a.rightPaneWidth = maxRightPaneWidth - 82
				}
				ct.Status = statusWorking
				ct.ActiveTaskID, ct.LastTaskID = "task-layout", "task-layout"
				w := a.ensureWork(ct, "task-layout", "message-layout")
				w.State = workRunning
				w.Prompt = "Layout preview"
				tr := ui.NewTester(a.view, size.width, size.height)
				tr.SetDark(dark)
				tr.Frame()
				writePreviewPNG(t, filepath.Join(dir,
					fmt.Sprintf("headless-%s-layout-%s-%dx%d.png", theme, scene, size.width, size.height)), tr.Image())
			}
		}
	}
}
