package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func connectionSettingsTester(s *connectionSettings, actions connectionSettingsActions, width, height int) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		theme := resolveWorkbenchTheme(c.Theme().Dark).Theme
		c.SetTheme(theme)
		ui.Scroll(c).Fill().Padding(16).Background(theme.Surface).Children(func() { s.view(c, theme, actions) })
	}, width, height)
}

func TestConnectionSettingsRequireExplicitSaveAndTest(t *testing.T) {
	config := testConnectionConfig(t)
	s := newConnectionSettings(&config, "")
	s.Draft.Model = "changed-model"
	saved, tested, applied := 0, 0, 0
	tr := connectionSettingsTester(s, connectionSettingsActions{
		Save: func(candidate connectionConfig) {
			saved++
			if candidate.Model != "changed-model" {
				t.Fatal("save lost the edited model")
			}
			s.Error = "Cannot save configuration. Your changes are still here."
		},
		Test: func(connectionConfig) { tested++ }, Apply: func() { applied++ },
	}, 580, 1600)
	tr.Frame()
	if saved != 0 || tested != 0 || applied != 0 {
		t.Fatal("rendering performed an action")
	}
	if err := tr.Click("Save configuration"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if saved != 1 || tested != 0 || s.Draft.Model != "changed-model" || s.Error == "" {
		t.Fatal("save failure must retain editing and must not test or apply")
	}
	s.ReloadBlocked = "Wait for all active work to finish."
	tr.Frame()
	if err := tr.Click("Apply saved"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if applied != 0 {
		t.Fatal("blocked apply restarted the connection")
	}
	if err := tr.Click("Test connection and model"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if tested != 1 || saved != 1 || applied != 0 {
		t.Fatal("explicit connection test must not save or apply configuration")
	}
}

func TestConnectionSettingsValidationKeepsDraftAndFocus(t *testing.T) {
	config := testConnectionConfig(t)
	s := newConnectionSettings(&config, "")
	s.Draft.ContextTokens = "not-a-number"
	s.Draft.APIKey = ""
	calls := 0
	tr := connectionSettingsTester(s, connectionSettingsActions{Save: func(connectionConfig) { calls++ }}, 580, 1600)
	tr.SetFocused(true)
	tr.Frame()
	if err := tr.Click("Save configuration"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	tr.Frame()
	if calls != 0 || s.Draft.ContextTokens != "not-a-number" || s.Errors["APIKey"] == "" || s.Errors["ContextTokens"] == "" {
		t.Fatal("invalid form must retain every edit and report field errors without saving")
	}
	tr.Type("replacement-key")
	tr.Frame()
	if s.Draft.APIKey != "replacement-key" {
		t.Fatal("validation did not focus the first invalid field")
	}
}

func TestPreviewConnectionSettings(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR for connection form previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		for _, width := range []int{340, 600} {
			s := newConnectionSettings(nil, "/Users/you/Workspace")
			tr := connectionSettingsTester(s, connectionSettingsActions{}, width, 640)
			tr.SetDark(dark)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("connection-form-%d-dark-%t.png", width, dark)), tr.Image())
			tr.Scroll(100, 500, 0, 1000)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("connection-actions-%d-dark-%t.png", width, dark)), tr.Image())
		}
	}
	isolateConnectionApp(t, t.TempDir(), "")
	for _, dark := range []bool{false, true} {
		for _, size := range [][2]int{{1040, 720}, {720, 640}} {
			a, err := newRuntimeApp()
			if err != nil {
				t.Fatal(err)
			}
			a.windowWidth = size[0]
			tr := ui.NewTester(a.view, size[0], size[1])
			tr.SetDark(dark)
			tr.Frame()
			name := fmt.Sprintf("connection-app-%dx%d-dark-%t", size[0], size[1], dark)
			writePreviewPNG(t, filepath.Join(dir, name+"-form.png"), tr.Image())
			tr.Scroll(float32(size[0]/2), float32(size[1]/2), 0, 1200)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, name+"-actions.png"), tr.Image())
		}
	}
}
