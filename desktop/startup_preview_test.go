package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestPreviewStartupDiagnostics writes a small, opt-in set of headless images
// for the G1 startup/configuration UI. It is not a pixel assertion and does not
// run unless SAKI_PREVIEW_DIR points at a scratch directory; native Finder/IME
// evidence remains separate.
func TestPreviewStartupDiagnostics(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SAKI_PREVIEW_DIR to write G1 startup/configuration previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := loadAvatar(); err != nil {
		t.Fatal(err)
	}

	preview := func(scene string) *appModel {
		a := &appModel{
			demo:          false,
			runtimeStatus: runtimeStateFailed,
			runtimeStage:  "Runtime configuration",
			runtimeErr:    "API_KEY is not set.",
			runtimeHint:   "Set API_KEY in the environment or in /Users/you/Workspace/saki-run/.env, then restart Saki.",
			runtimeBinary: "/Users/you/Applications/Saki.app/Contents/MacOS/Saki-runtime",
			storePath:     "/Users/you/Library/Application Support/Saki/contacts.json",
			sessionDir:    "/Users/you/Library/Application Support/Saki/sessions",
			workDir:       "/Users/you/Workspace/saki-run",
			scrollTo:      -1,
			contacts: []*contact{{
				ID: "saki", Name: "Saki", Role: "Assistant",
				Draft: "Keep this draft while I fix configuration.",
			}},
		}
		if scene == "configuration-details" {
			a.openConfiguration()
		}
		return a
	}

	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, scene := range []string{"config-failure", "configuration-details"} {
			for _, size := range []struct {
				width, height int
				label         string
			}{{1040, 720, "1040x720"}, {960, 640, "960x640"}} {
				a := preview(scene)
				tr := ui.NewTester(a.view, size.width, size.height)
				tr.SetDark(dark)
				tr.Frame()
				writePreviewPNG(t, filepath.Join(dir,
					fmt.Sprintf("headless-%s-g1-%s-%s.png", theme, scene, size.label)), tr.Image())
			}
		}
	}

	a := preview("configuration-details")
	tr := ui.NewTester(a.view, 1600, 900)
	tr.Frame()
	writePreviewPNG(t, filepath.Join(dir, "headless-light-g1-configuration-details-1600x900.png"), tr.Image())
}
