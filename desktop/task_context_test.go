package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestTaskContextFileSnapshotRefreshAndDiskIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brief.txt")
	original := "First line\n第二行 — café\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	tc := &taskContext{}
	item, err := tc.ImportFile(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if item.Name != "brief.txt" || item.Source != path || item.Text != original || item.Revision != 1 {
		t.Fatalf("imported snapshot = %+v", item)
	}

	changed := "changed on disk\r\nwith a new line\n"
	if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := tc.snapshot()[0]; got.Text != original || got.Revision != 1 {
		t.Fatalf("file change mutated the saved snapshot: %+v", got)
	}

	refreshed, err := tc.Refresh(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Text != changed || refreshed.Revision != 2 || refreshed.Source != path {
		t.Fatalf("refreshed snapshot = %+v", refreshed)
	}

	edited, err := tc.Edit(item.ID, "Edited brief", "edited only in the snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if edited.Text != "edited only in the snapshot" || edited.Revision != 3 || edited.Source != path {
		t.Fatalf("edited snapshot = %+v", edited)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != changed {
		t.Fatalf("editing the snapshot wrote the file: %q", onDisk)
	}
}

func TestTaskContextNamedTextAndSubmittedRevision(t *testing.T) {
	tc := &taskContext{}
	original := "First line\n第二行 — café 🍵"
	item, err := tc.AddSnippet("Release notes", original)
	if err != nil {
		t.Fatal(err)
	}
	if item.Text != original || item.Source != "" || item.Name != "Release notes" || item.Revision != 1 {
		t.Fatalf("named snippet = %+v", item)
	}

	submitted := tc.snapshot()
	if _, err := tc.Edit(item.ID, "Release notes v2", "new text"); err != nil {
		t.Fatal(err)
	}
	if submitted[0].Text != original || submitted[0].Name != "Release notes" || submitted[0].Revision != 1 {
		t.Fatalf("submitted snapshot changed after a later edit: %+v", submitted[0])
	}
	if removed := tc.clearSubmitted(submitted); removed != 0 {
		t.Fatalf("cleared a newer revision: %d", removed)
	}
	current := tc.snapshot()
	if len(current) != 1 || current[0].Text != "new text" || current[0].Revision != 2 {
		t.Fatalf("newer revision was not preserved: %+v", current)
	}
	if removed := tc.clearSubmitted(current); removed != 1 {
		t.Fatalf("exact accepted revision was not cleared: %d", removed)
	}
	if len(tc.snapshot()) != 0 {
		t.Fatal("cleared accepted snapshot remained in the list")
	}
}

func TestTaskContextFailuresPreservePreviousValues(t *testing.T) {
	tc := &taskContext{}
	item, err := tc.AddSnippet("Keep me", "unchanged")
	if err != nil {
		t.Fatal(err)
	}
	before := tc.snapshot()

	if _, err := tc.AddSnippet("", "text"); !errors.Is(err, errTaskContextName) {
		t.Fatalf("blank name error = %v", err)
	}
	if _, err := tc.AddSnippet("large", strings.Repeat("a", taskContextMaxBytes+10)); !errors.Is(err, errTaskContextOversize) {
		t.Fatalf("oversize error = %v", err)
	}
	// The raw text is below 1 MiB, but its serialized caller-context wrapper is
	// not; size checks must account for the name and wrapper bytes.
	rawBelowLimit := strings.Repeat("a", taskContextMaxBytes-100)
	if _, err := tc.AddSnippet(strings.Repeat("n", 400), rawBelowLimit); !errors.Is(err, errTaskContextOversize) {
		t.Fatalf("serialized-size error = %v", err)
	}
	chunk := strings.Repeat("a", 600*1024)
	if _, err := tc.AddSnippet("half", chunk); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.AddSnippet("overflow", chunk); !errors.Is(err, errTaskContextOversize) {
		t.Fatalf("aggregate error = %v", err)
	}
	if got := len(tc.snapshot()); got != 2 {
		t.Fatalf("failed additions changed the list: %d items", got)
	}
	if _, err := tc.Edit(item.ID, "", "text"); !errors.Is(err, errTaskContextName) {
		t.Fatalf("failed edit error = %v", err)
	}
	after, ok := tc.itemByID(item.ID)
	if !ok || after != before[0] {
		t.Fatalf("failed edit changed the snapshot: %+v", after)
	}
	if _, err := tc.Edit("missing", "name", "text"); !errors.Is(err, errTaskContextMissing) {
		t.Fatalf("missing edit error = %v", err)
	}
	if _, err := tc.Refresh(item.ID); !errors.Is(err, errTaskContextNoSource) {
		t.Fatalf("named refresh error = %v", err)
	}
}

func TestTaskContextEmptyFileAndInputNameProjection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tc := &taskContext{}
	fileItem, err := tc.ImportFile(path, "Empty brief")
	if err != nil {
		t.Fatalf("empty UTF-8 file was rejected: %v", err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if fileItem.Text != "" || fileItem.Source != absolute {
		t.Fatalf("empty file snapshot = %+v", fileItem)
	}
	if want := "Empty brief · file " + absolute; fileItem.inputName() != want {
		t.Fatalf("file input name = %q, want %q", fileItem.inputName(), want)
	}
	snippet, err := tc.AddSnippet("Empty snippet", "")
	if err != nil {
		t.Fatalf("empty named snippet was rejected: %v", err)
	}
	if snippet.inputName() != "Empty snippet" {
		t.Fatalf("snippet input name = %q", snippet.inputName())
	}
	if err := os.WriteFile(path, []byte("now has text"), 0o600); err != nil {
		t.Fatal(err)
	}
	refreshed, err := tc.Refresh(fileItem.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Text != "now has text" || refreshed.Revision != 2 {
		t.Fatalf("refreshed empty file = %+v", refreshed)
	}

	// The same name projection is used for the aggregate budget: a snippet
	// that fits as typed text does not fit once the explicit file source path
	// is part of the caller-context label.
	base := taskContextWrapperBytes("x", "")
	text := strings.Repeat("a", taskContextMaxBytes-base-10)
	budget := &taskContext{}
	if _, err := budget.AddSnippet("x", text); err != nil {
		t.Fatalf("projection-free snippet did not fit: %v", err)
	}
	fileText := filepath.Join(dir, "projection-budget.txt")
	if err := os.WriteFile(fileText, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	fileBudget := &taskContext{}
	if _, err := fileBudget.ImportFile(fileText, "x"); !errors.Is(err, errTaskContextOversize) {
		t.Fatalf("file source was not included in the size budget: %v", err)
	}
}

func TestTaskContextFileValidationErrors(t *testing.T) {
	dir := t.TempDir()
	tc := &taskContext{}
	item, err := tc.AddSnippet("Keep me", "unchanged")
	if err != nil {
		t.Fatal(err)
	}
	before := tc.snapshot()

	pdf := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalid := filepath.Join(dir, "invalid.txt")
	if err := os.WriteFile(invalid, []byte{0xff, 0xfe, 0xfd}, 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "binary.txt")
	if err := os.WriteFile(binary, []byte("text\x00more"), 0o600); err != nil {
		t.Fatal(err)
	}
	oversize := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(oversize, []byte(strings.Repeat("a", taskContextMaxBytes+10)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		path string
		want error
	}{
		{"unsupported", pdf, errTaskContextUnsupported},
		{"invalid utf8", invalid, errTaskContextUTF8},
		{"not text", binary, errTaskContextNotText},
		{"missing", filepath.Join(dir, "missing.txt"), errTaskContextRead},
		{"oversize", oversize, errTaskContextOversize},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := tc.ImportFile(test.path, ""); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if got := tc.snapshot(); len(got) != 1 || got[0] != before[0] {
				t.Fatalf("failed import changed the list: %+v", got)
			}
		})
	}

	svg := filepath.Join(dir, "diagram.svg")
	if err := os.WriteFile(svg, []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"><text>still text</text></svg>"), 0o600); err != nil {
		t.Fatal(err)
	}
	svgContext := &taskContext{}
	if _, err := svgContext.ImportFile(svg, ""); err != nil {
		t.Fatalf("UTF-8 SVG text was rejected: %v", err)
	}

	path := filepath.Join(dir, "brief.txt")
	if err := os.WriteFile(path, []byte("old snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileItem, err := tc.ImportFile(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.Refresh(fileItem.ID); !errors.Is(err, errTaskContextRead) {
		t.Fatalf("missing refresh error = %v", err)
	}
	kept, ok := tc.itemByID(fileItem.ID)
	if !ok || kept.Text != "old snapshot" || kept.Revision != 1 {
		t.Fatalf("failed refresh lost the old snapshot: %+v", kept)
	}
	if item, ok := tc.itemByID(item.ID); !ok || item.Text != "unchanged" {
		t.Fatalf("failed refresh changed another item: %+v", item)
	}
	if !tc.Remove(fileItem.ID) || tc.Remove(fileItem.ID) {
		t.Fatal("remove did not report exact removal")
	}
	if _, ok := tc.itemByID(fileItem.ID); ok {
		t.Fatal("removed item remained")
	}
}

func TestTaskContextViewEditsPreviewsRefreshesAndReportsErrors(t *testing.T) {
	for _, dark := range []bool{false, true} {
		dark := dark
		name := "light"
		if dark {
			name = "dark"
		}
		t.Run(name, func(t *testing.T) {
			exerciseTaskContextView(t, dark)
		})
	}
}

func exerciseTaskContextView(t *testing.T, dark bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("old file text"), 0o600); err != nil {
		t.Fatal(err)
	}
	tc := &taskContext{}
	tr := taskContextTester(tc, dark)

	tr.Frame()
	if !tr.HasText("Task context") || !tr.HasText("No saved context") {
		t.Fatalf("empty component state missing: %v", tr.Texts())
	}
	if err := tr.Click("Add snippet"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Context name"); err != nil {
		t.Fatal(err)
	}
	tr.Type("Release notes")
	if err := tr.Click("Context text"); err != nil {
		t.Fatal(err)
	}
	tr.Type("Line one · 你好")
	if err := tr.Click("Save snippet"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if len(tc.snapshot()) != 1 || tc.snapshot()[0].Text != "Line one · 你好" {
		t.Fatalf("UI add snippet = %+v", tc.snapshot())
	}
	if !tr.HasText("Release notes") || !tr.HasText("Named snippet") {
		t.Fatalf("snippet card missing: %v", tr.Texts())
	}
	if err := tr.Click("Preview"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("Line one · 你好") || !tr.HasText("Close preview") {
		t.Fatalf("preview did not show saved text: %v", tr.Texts())
	}

	if err := tr.Click("Edit"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Context name"); err != nil {
		t.Fatal(err)
	}
	tr.Command("selectAll")
	tr.Type("Renamed snippet")
	if err := tr.Click("Save changes"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if got := tc.snapshot()[0]; got.Name != "Renamed snippet" || got.Text != "Line one · 你好" || got.Revision != 2 {
		t.Fatalf("UI edit = %+v", got)
	}
	if !tr.HasText("Renamed snippet") {
		t.Fatalf("edited name missing: %v", tr.Texts())
	}

	if err := tr.Click("Add file"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Context file path"); err != nil {
		t.Fatal(err)
	}
	tr.Type(path)
	if err := tr.Click("Save file"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if len(tc.snapshot()) != 2 || tc.snapshot()[1].Source != path || tc.snapshot()[1].Name != "notes.txt" {
		t.Fatalf("UI file import = %+v", tc.snapshot())
	}
	if !tr.HasText("Source · "+path) || !tr.HasText("old file text") {
		t.Fatalf("file card or preview missing: %v", tr.Texts())
	}
	if err := os.WriteFile(path, []byte("new file text"), 0o600); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("old file text") || tr.HasText("new file text") {
		t.Fatalf("file changed without an explicit refresh: %v", tr.Texts())
	}
	if err := tr.Click("Refresh from file"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("new file text") || tc.snapshot()[1].Text != "new file text" || tc.snapshot()[1].Revision != 2 {
		t.Fatalf("UI refresh = %+v", tc.snapshot())
	}

	if err := tr.Click("Add snippet"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Save snippet"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("context name is required") || !tr.HasText("Save snippet") {
		t.Fatalf("validation error did not preserve the form: %v", tr.Texts())
	}
	if len(tc.snapshot()) != 2 {
		t.Fatalf("failed UI form changed the list: %+v", tc.snapshot())
	}
	if err := tr.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()

	if err := tr.Click("Remove"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if len(tc.snapshot()) != 1 {
		t.Fatalf("UI remove changed the wrong item: %+v", tc.snapshot())
	}
	if err := tr.Click("Remove"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if len(tc.snapshot()) != 0 || !tr.HasText("No saved context") {
		t.Fatalf("final remove = %+v / %v", tc.snapshot(), tr.Texts())
	}
}

func taskContextTester(tc *taskContext, dark bool) *ui.Tester {
	return taskContextTesterSize(tc, dark, 720, 1200)
}

func taskContextTesterSize(tc *taskContext, dark bool, width, height int) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		theme := resolveWorkbenchTheme(dark)
		c.SetTheme(theme.Theme)
		ui.Scroll(c).Fill().Padding(16).Background(theme.Theme.Background).Children(func() {
			tc.view(c, theme)
		})
	}, width, height)
}

func TestTaskContextEmptyFilePreviewCopyAndRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tc := &taskContext{}
	item, err := tc.ImportFile(path, "Empty brief")
	if err != nil {
		t.Fatal(err)
	}
	tc.previewID = item.ID
	tr := taskContextTester(tc, false)
	tr.Frame()
	if !tr.HasText("Preview · Empty brief") || !tr.HasText("Source · "+path) {
		t.Fatalf("empty snapshot preview missing: %v", tr.Texts())
	}
	if err := tr.Click("Copy context text"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != "" {
		t.Fatalf("empty snapshot copy = %q", got)
	}
	if err := os.WriteFile(path, []byte("filled later"), 0o600); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if tr.HasText("filled later") {
		t.Fatal("empty snapshot reread the file without refresh")
	}
	if err := tr.Click("Refresh from file"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("filled later") {
		t.Fatalf("refreshed empty file preview missing: %v", tr.Texts())
	}
	if err := tr.Click("Copy context text"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != "filled later" {
		t.Fatalf("refreshed snapshot copy = %q", got)
	}
}

func TestTaskContextHeadlessPreviews(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	path := filepath.Join(t.TempDir(), "interface-notes.txt")
	if err := os.WriteFile(path, []byte("Compact cards keep the exact saved text.\nRefresh is explicit; later file changes stay out of the snapshot.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tc := &taskContext{}
	fileItem, err := tc.ImportFile(path, "Interface notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tc.AddSnippet("Review checklist", "Confirm Unicode: 你好 — café\nPreserve both lines."); err != nil {
		t.Fatal(err)
	}
	tc.previewID = fileItem.ID
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, width := range []int{340, 600} {
			tr := taskContextTesterSize(tc, dark, width, 900)
			tr.Frame()
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("task-context-%s-%d.png", theme, width)), tr.Image())
		}
	}
}

func TestTaskContextSaveFailureKeepsEditorAndValues(t *testing.T) {
	tc := &taskContext{onChange: func() error { return errors.New("store unavailable") }}
	tr := taskContextTester(tc, false)
	tr.Frame()
	if err := tr.Click("Add snippet"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if err := tr.Click("Context name"); err != nil {
		t.Fatal(err)
	}
	tr.Type("Unsaved name")
	if err := tr.Click("Context text"); err != nil {
		t.Fatal(err)
	}
	tr.Type("Unsaved body")
	if err := tr.Click("Save snippet"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tc.formOpen || tc.formName != "Unsaved name" || tc.formText != "Unsaved body" {
		t.Fatalf("save failure lost the editor: %+v", tc)
	}
	if len(tc.snapshot()) != 0 {
		t.Fatalf("save failure committed an item: %+v", tc.snapshot())
	}
	if !tr.HasText("store unavailable") || !tr.HasText("Save snippet") {
		t.Fatalf("save failure was not visible inline: %v", tr.Texts())
	}
}
