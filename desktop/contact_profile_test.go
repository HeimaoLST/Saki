package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func contactProfileTester(e *contactProfileEditor, actions contactProfileActions, width, height int) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		theme := resolveWorkbenchTheme(c.Theme().Dark).Theme
		c.SetTheme(theme)
		ui.Scroll(c).Fill().Padding(16).Background(theme.Surface).Children(func() {
			e.view(c, theme, actions)
		})
	}, width, height)
}

func profileDraftForTest(t *testing.T) profileDraft {
	return profileDraft{
		Name:         "Writer",
		Purpose:      "Keep product notes concise.",
		Instructions: "  Keep indentation\n\tand this line exactly.  ",
		Workspace:    t.TempDir(),
	}
}

func TestContactProfileDiscardDoesNotSaveNewOrArchive(t *testing.T) {
	editor := newContactProfileEditor(profileDraftForTest(t))
	editor.HasConversation = true
	editor.Draft.Name = "Changed"
	editor.Draft.Purpose = "Changed purpose"
	saved, newed, archived := 0, 0, 0
	actions := contactProfileActions{
		Save:       func(profileDraft) { saved++ },
		SaveAndNew: func(profileDraft) { newed++ },
		Archive:    func() { archived++ },
	}
	tr := contactProfileTester(editor, actions, 320, 720)
	tr.Frame()
	editor.ResetDraft()
	if saved != 0 || newed != 0 || archived != 0 {
		t.Fatalf("discard callbacks save=%d new=%d archive=%d", saved, newed, archived)
	}
	if editor.Draft.Name != "Writer" || editor.Draft.Purpose != "Keep product notes concise." {
		t.Fatal("discard did not restore the draft")
	}
}

func TestContactProfileValidationCorrectsWorkspaceAndPreservesInstructions(t *testing.T) {
	editor := newContactProfileEditor(profileDraftForTest(t))
	editor.Draft.Name = " "
	if _, ok := editor.Request(false); ok || editor.Errors["Name"] == "" {
		t.Fatal("blank name was accepted")
	}
	file := filepath.Join(t.TempDir(), "workspace.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, workspace := range []string{"relative/path", filepath.Join(t.TempDir(), "missing"), file} {
		editor.Draft.Name = "Writer"
		editor.Draft.Workspace = workspace
		if _, ok := editor.Request(false); ok || editor.Errors["Workspace"] == "" {
			t.Fatalf("invalid workspace %q was accepted", workspace)
		}
	}

	var saved profileDraft
	actions := contactProfileActions{Save: func(draft profileDraft) { saved = draft }}
	editor.Draft.Workspace = ""
	tr := contactProfileTester(editor, actions, 560, 1600)
	tr.Frame()
	if err := tr.Click("Save changes"); err != nil {
		t.Fatal(err)
	}
	if saved.Name != "Writer" || saved.Workspace != "" {
		t.Fatalf("saved draft = %+v", saved)
	}
	if saved.Instructions != "  Keep indentation\n\tand this line exactly.  " {
		t.Fatalf("instructions were altered: %q", saved.Instructions)
	}
}

func TestContactProfileActiveAndUnknownWorkLimitOnlyNewAndArchive(t *testing.T) {
	editor := newContactProfileEditor(profileDraftForTest(t))
	editor.HasConversation = true
	editor.ActiveWork = true
	saved, newed, archived := 0, 0, 0
	actions := contactProfileActions{
		Save:       func(profileDraft) { saved++ },
		SaveAndNew: func(profileDraft) { newed++ },
		Archive:    func() { archived++ },
	}
	tr := contactProfileTester(editor, actions, 560, 1600)
	tr.Frame()
	if !tr.HasText("Active work must finish or stop") {
		t.Fatal("active work reason missing")
	}
	if err := tr.Click("Save changes"); err != nil {
		t.Fatal(err)
	}
	if saved != 1 {
		t.Fatal("profile save was blocked by active work")
	}
	if err := tr.Click("Save and start new conversation"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Click("Archive"); err != nil {
		t.Fatal(err)
	}
	if newed != 0 || archived != 0 {
		t.Fatalf("active work allowed new=%d archive=%d", newed, archived)
	}

	editor.HasConversation = true
	editor.ActiveWork = false
	editor.UnknownWork = true
	tr.Frame()
	if !tr.HasText("Work is still unresolved") {
		t.Fatal("unknown work reason missing")
	}
	if _, ok := editor.Request(true); ok || editor.Error == "" {
		t.Fatal("Request accepted save-and-new while work was unknown")
	}
	if err := tr.Click("Save and start new conversation"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Click("Archive"); err != nil {
		t.Fatal(err)
	}
	if newed != 0 || archived != 0 {
		t.Fatal("unknown work allowed new or archive")
	}
}

func TestContactProfileArchiveIsHostConfirmed(t *testing.T) {
	editor := newContactProfileEditor(profileDraftForTest(t))
	archived, restored, saved := 0, 0, 0
	actions := contactProfileActions{
		Archive: func() { archived++ },
		Restore: func() { restored++ },
		Save:    func(profileDraft) { saved++ },
	}
	tr := contactProfileTester(editor, actions, 560, 1600)
	tr.Frame()
	if err := tr.Click("Archive"); err != nil {
		t.Fatal(err)
	}
	if archived != 1 || editor.Archived {
		t.Fatal("component assumed archive success before host confirmation")
	}
	editor.Archived = true
	tr.Frame()
	if _, ok := editor.Request(false); ok || editor.Error == "" {
		t.Fatal("Request accepted a save for an archived contact")
	}
	if !tr.HasText("Archived") {
		t.Fatal("archived state is not visible")
	}
	if err := tr.Click("Save changes"); err != nil {
		t.Fatal(err)
	}
	if saved != 0 {
		t.Fatal("archived state allowed profile save")
	}
	if err := tr.Click("Restore"); err != nil {
		t.Fatal(err)
	}
	if restored != 1 || !editor.Archived {
		t.Fatal("component assumed restore success before host confirmation")
	}
}

func TestContactProfileSaveFailureKeepsDraftAndArchiveState(t *testing.T) {
	editor := newContactProfileEditor(profileDraftForTest(t))
	editor.Draft.Name = "Draft after failure"
	editor.Draft.Instructions = "keep\n  this"
	editor.SetBusy(true, "Saving…")
	if _, ok := editor.Request(false); ok || editor.Error == "" {
		t.Fatal("Request accepted a save while saving was in progress")
	}
	editor.SetError("Could not save this profile. Your changes are still here.")
	if editor.Busy || !strings.Contains(editor.Error, "still here") {
		t.Fatal("save failure did not restore an editable state with an error")
	}
	if editor.Draft.Name != "Draft after failure" || editor.Draft.Instructions != "keep\n  this" || editor.Archived {
		t.Fatal("save failure changed the draft or archive state")
	}
	tr := contactProfileTester(editor, contactProfileActions{}, 320, 720)
	tr.Frame()
	if !tr.HasText("Could not save this profile") {
		t.Fatal("save error is not visible near the actions")
	}
	if tr.HasText("Saving…") {
		t.Fatal("save failure still displays an in-progress status")
	}
	editor.SetBusy(true, "Saving…")
	editor.SetProfile(editor.Draft)
	editor.Draft.Name = "Next edit"
	editor.ResetDraft()
	if editor.Draft.Name != "Draft after failure" {
		t.Fatal("confirmed save did not re-enable editing and become the new discard target")
	}
}

func TestPreviewContactProfile(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, size := range []struct {
			width, height int
			label         string
		}{{560, 720, "560x720"}, {320, 720, "320x720"}} {
			editor := newContactProfileEditor(profileDraftForTest(t))
			editor.HasConversation = true
			editor.Draft.Purpose = "A long purpose line that should wrap clearly at narrow width without hiding the primary action."
			editor.SetError("Saving failed. The previous profile is still intact and this draft is editable.")
			tr := contactProfileTester(editor, contactProfileActions{}, size.width, size.height)
			tr.SetDark(dark)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, "contact-profile-"+size.label+"-"+theme+"-top.png"), tr.Image())
			tr.Scroll(100, 500, 0, 1000)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, "contact-profile-"+size.label+"-"+theme+"-scrolled.png"), tr.Image())
		}
	}
}
