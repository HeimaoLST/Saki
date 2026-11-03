package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"saki/desktop/locales"
)

// TestPreviewI18nSelectedSlice is the I1 headless preview for the selected
// Settings and copy slice. It is not native-window evidence and does not
// expand into the final AC3 preview matrix.
func TestPreviewI18nSelectedSlice(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR to write the I1 i18n previews")
	}
	for _, locale := range []string{"en", "zh-Hans", "en-XA"} {
		for _, dark := range []bool{false, true} {
			theme := "light"
			if dark {
				theme = "dark"
			}
			a, _ := workspaceTestApp(t)
			a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
			a.windowWidth = 1040
			tr := ui.NewTester(a.view, 1040, 720)
			tr.SetLocale(locale)
			tr.SetDark(dark)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("settings-%s-%s-1040x720.png", locale, theme)), tr.Image())

			chat := testApp(t)
			chat.windowWidth = 1040
			chatTr := ui.NewTester(chat.view, 1040, 720)
			chatTr.SetLocale(locale)
			chatTr.SetDark(dark)
			chatTr.Frame()
			showPreviewTooltip(t, chatTr, locales.In(locale).CopyMessageLabel())
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("copy-%s-%s-1040x720.png", locale, theme)), chatTr.Image())
		}
	}

	a, _ := workspaceTestApp(t)
	a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
	a.windowWidth = 960
	tr := ui.NewTester(a.view, 960, 640)
	tr.SetLocale("zh-Hans")
	tr.Frame()
	writePreviewPNG(t, filepath.Join(dir, "settings-zh-Hans-light-960x640.png"), tr.Image())
}

func showPreviewTooltip(t *testing.T, tr *ui.Tester, label string) {
	t.Helper()
	r, ok := tr.Find(label)
	if !ok {
		t.Fatalf("preview control %q not found: %v", label, tr.Texts())
	}
	tr.Move(1, 1)
	tr.Move(r.X+r.W/2, r.Y+r.H/2)
	tr.Advance(700 * time.Millisecond)
	tr.Frame()
}
