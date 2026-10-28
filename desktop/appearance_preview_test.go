package main

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// appearancePreviewApp uses the existing workspace fixture state; it does not
// seed production work or call Runtime.
func appearancePreviewApp(t *testing.T, scene string) (*appModel, *contact) {
	t.Helper()
	a, ct := workspaceTestApp(t)
	a.workspaceOpen = false
	a.runtimeCapabilitiesLoaded = true
	a.runtimeMaxActiveTasks = 2
	ct.Role = "Assistant"
	ct.Draft = "把这周的发布说明收一下，保留待确认的问题。"
	switch scene {
	case "normal":
		ct.Messages = []*message{
			{ID: 1, Kind: msgUser, Text: "帮我把这周的发布说明收一下。", Time: "09:42"},
			{ID: 2, Kind: msgContact, Text: "可以。我会先核对已完成的事项，再把结果放到工作区。", Time: "09:43"},
		}
	case "working":
		ct.Status = statusWorking
		ct.ActiveTaskID, ct.LastTaskID = "preview-task", "preview-task"
		ct.Messages = []*message{
			{ID: 1, Kind: msgUser, Text: "先汇总已完成的事项。", Time: "10:02"},
			{ID: 2, Kind: msgContact, Text: "我正在核对结果与未满足的要求。", Time: "10:03"},
		}
		w := a.ensureWork(ct, "preview-task", "preview-message")
		w.State = workRunning
		w.Prompt = "Checking release notes"
		w.Tools = []*workTool{{CallID: "preview-tool", Name: "read", Args: `{"path":"notes.md"}`, Started: true, Finished: true, Output: "release notes"}}
		a.workspaceOpen = true
	case "attention":
		ct.Status = statusAttention
		ct.Messages = []*message{{
			ID: 1, Kind: msgContact, Text: "有一项要求还缺少依据，需要你确认后再继续。", Time: "10:12",
			AlertTitle: "Needs attention", AlertDetail: "One requirement is still unverified.",
		}}
	case "error":
		ct.SendError = "connection_mismatch: preview connection unavailable"
		ct.LoadErr = "connection_mismatch: preview connection unavailable"
		ct.Messages = []*message{{ID: 1, Kind: msgContact, Text: "连接不可用；这段草稿仍然保留。", Time: "10:20"}}
	case "empty":
		ct.Messages = nil
	}
	return a, ct
}

func writeV07AppearancePreview(t *testing.T, dir, name string, a *appModel, width, height int, dark bool) {
	t.Helper()
	tr := ui.NewTester(a.view, width, height)
	tr.SetDark(dark)
	tr.Frame()
	writePreviewPNG(t, filepath.Join(dir, name), tr.Image())
}

// writeV07AppearancePreviews is invoked by the existing TestPreviewImages
// entry; it adds no new opt-in skip and no second preview test.
func writeV07AppearancePreviews(t *testing.T, dir string) {
	t.Helper()
	if err := loadAvatar(); err != nil {
		t.Fatal(err)
	}
	// First review gate: main, Appearance panel, and the minimum composer.
	for _, mode := range []struct {
		name    string
		mode    string
		sysDark bool
	}{{"paper", appearanceLight, true}, {"night", appearanceDark, false}} {
		for _, tc := range []struct {
			name           string
			openAppearance bool
			width, height  int
		}{
			{"main", false, 1040, 720},
			{"appearance", true, 1040, 720},
			{"composer", false, 720, 640},
		} {
			a, _ := appearancePreviewApp(t, "normal")
			a.appearanceMode = mode.mode
			a.windowWidth = tc.width
			if tc.openAppearance {
				a.openAppearance()
			}
			writeV07AppearancePreview(t, dir,
				fmt.Sprintf("v07-review-%s-%s-%dx%d.png", mode.name, tc.name, tc.width, tc.height),
				a, tc.width, tc.height, mode.sysDark)
		}
	}

	// Wider state matrix: widths are normal shell, richer states at 1040.
	scenes := []string{"normal", "working", "attention", "error", "empty"}
	sizes := []struct{ width, height int }{{1040, 720}, {1380, 900}, {960, 640}, {720, 640}}
	for _, mode := range []struct {
		name    string
		mode    string
		sysDark bool
	}{{"paper", appearanceLight, true}, {"night", appearanceDark, false}} {
		for _, scene := range scenes {
			for _, size := range sizes {
				if scene != "normal" && (size.width != 1040 || size.height != 720) {
					continue
				}
				a, _ := appearancePreviewApp(t, scene)
				a.appearanceMode = mode.mode
				a.windowWidth = size.width
				writeV07AppearancePreview(t, dir,
					fmt.Sprintf("v07-%s-%s-%dx%d.png", mode.name, scene, size.width, size.height),
					a, size.width, size.height, mode.sysDark)
			}
		}
	}
}
