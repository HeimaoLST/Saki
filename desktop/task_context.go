package main

// Task context is the GUI-owned list of named text snippets and explicit
// UTF-8 text-file snapshots sent with a user turn. It stays independent of the
// app model and of the runtime wire client: the caller projects snapshot
// values onto the runtime protocol at send time.

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// The runtime bounds the sum of serialized caller-context wrappers carried by
// one user turn (src/agent/message.rs MAX_CONTEXT_BYTES). Validate the same
// budget here so a failed send cannot be the first place an oversize snapshot
// is reported.
const (
	taskContextMaxBytes  = 1024 * 1024
	taskContextReadLimit = taskContextMaxBytes + 1
)

var (
	errTaskContextName        = errors.New("context name is required")
	errTaskContextUTF8        = errors.New("context text is not valid UTF-8")
	errTaskContextNotText     = errors.New("file is not a UTF-8 text file")
	errTaskContextOversize    = errors.New("context exceeds the 1 MiB send limit")
	errTaskContextUnsupported = errors.New("unsupported file format; only UTF-8 text files are supported")
	errTaskContextRead        = errors.New("could not read context file")
	errTaskContextMissing     = errors.New("context item no longer exists")
	errTaskContextNoSource    = errors.New("this context item has no file source")
)

type taskContextItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Source   string `json:"source,omitempty"`
	Text     string `json:"text"`
	Revision uint64 `json:"revision"`
}

// taskContext keeps the saved snapshots together with the small, explicit UI
// editor state. Saved item text is never re-read from disk implicitly.
type taskContext struct {
	items []taskContextItem

	formOpen  bool
	formMode  string // "snippet", "file", or "edit"
	formID    string
	formName  string
	formText  string
	formPath  string
	formError string

	previewID   string
	actionError string
}

func newTaskContextID() string {
	return "context-" + rand.Text()
}

// inputName is the single label projection used for both the size budget and
// the caller-context wire value. Named snippets stay just their name; file
// snapshots make the explicit source path visible without changing Text.
func (item taskContextItem) inputName() string {
	if item.Source == "" {
		return item.Name
	}
	return item.Name + " · file " + item.Source
}

func (tc *taskContext) snapshot() []taskContextItem {
	out := make([]taskContextItem, len(tc.items))
	copy(out, tc.items)
	return out
}

func (tc *taskContext) itemByID(id string) (taskContextItem, bool) {
	for _, item := range tc.items {
		if item.ID == id {
			return item, true
		}
	}
	return taskContextItem{}, false
}

func (tc *taskContext) addItem(name, source, text string) (taskContextItem, error) {
	item := taskContextItem{
		ID: newTaskContextID(), Name: strings.TrimSpace(name), Source: source,
		Text: text, Revision: 1,
	}
	next := append(tc.snapshot(), item)
	if err := validateTaskContext(next); err != nil {
		return taskContextItem{}, err
	}
	tc.items = next
	return item, nil
}

// AddSnippet saves typed text. The name labels the source; Source stays empty
// because there is no file to refresh.
func (tc *taskContext) AddSnippet(name, text string) (taskContextItem, error) {
	return tc.addItem(name, "", text)
}

// ImportFile reads one explicit path into a snapshot. Later file changes do
// not affect the saved snapshot until Refresh is called.
func (tc *taskContext) ImportFile(path, name string) (taskContextItem, error) {
	text, source, err := readTaskContextFile(path)
	if err != nil {
		return taskContextItem{}, err
	}
	if strings.TrimSpace(name) == "" {
		name = filepath.Base(source)
	}
	return tc.addItem(name, source, text)
}

// Refresh re-reads the original source path into a new revision. On any
// failure the previous snapshot is left unchanged.
func (tc *taskContext) Refresh(id string) (taskContextItem, error) {
	for index, current := range tc.items {
		if current.ID != id {
			continue
		}
		if current.Source == "" {
			return taskContextItem{}, errTaskContextNoSource
		}
		text, source, err := readTaskContextFile(current.Source)
		if err != nil {
			return taskContextItem{}, err
		}
		next := current
		next.Source, next.Text, next.Revision = source, text, current.Revision+1
		items := tc.snapshot()
		items[index] = next
		if err := validateTaskContext(items); err != nil {
			return taskContextItem{}, err
		}
		tc.items = items
		return next, nil
	}
	return taskContextItem{}, errTaskContextMissing
}

// Edit changes the label and text of a saved snapshot without touching disk.
func (tc *taskContext) Edit(id, name, text string) (taskContextItem, error) {
	for index, current := range tc.items {
		if current.ID != id {
			continue
		}
		next := current
		next.Name, next.Text, next.Revision = strings.TrimSpace(name), text, current.Revision+1
		items := tc.snapshot()
		items[index] = next
		if err := validateTaskContext(items); err != nil {
			return taskContextItem{}, err
		}
		tc.items = items
		return next, nil
	}
	return taskContextItem{}, errTaskContextMissing
}

func (tc *taskContext) Remove(id string) bool {
	for index, item := range tc.items {
		if item.ID != id {
			continue
		}
		tc.items = append(tc.items[:index], tc.items[index+1:]...)
		if tc.previewID == id {
			tc.previewID = ""
		}
		if tc.formID == id {
			tc.cancelForm()
		}
		return true
	}
	return false
}

// clearSubmitted removes only the exact versions handed to an accepted send.
// A snapshot edited while the send was pending survives for the next turn.
func (tc *taskContext) clearSubmitted(submitted []taskContextItem) int {
	kept := tc.items[:0]
	removed := 0
	for _, current := range tc.items {
		matched := false
		for _, sent := range submitted {
			if sent.ID == current.ID && sent.Revision == current.Revision {
				matched = true
				break
			}
		}
		if matched {
			removed++
			if tc.previewID == current.ID {
				tc.previewID = ""
			}
			continue
		}
		kept = append(kept, current)
	}
	tc.items = kept
	return removed
}

func (tc *taskContext) cancelForm() {
	tc.formOpen = false
	tc.formMode, tc.formID = "", ""
	tc.formName, tc.formText, tc.formPath, tc.formError = "", "", "", ""
}

func (tc *taskContext) openSnippetForm() {
	tc.actionError = ""
	tc.cancelForm()
	tc.formOpen, tc.formMode = true, "snippet"
}

func (tc *taskContext) openFileForm() {
	tc.actionError = ""
	tc.cancelForm()
	tc.formOpen, tc.formMode = true, "file"
}

func (tc *taskContext) openEditForm(id string) {
	item, ok := tc.itemByID(id)
	if !ok {
		tc.actionError = errTaskContextMissing.Error()
		return
	}
	tc.actionError = ""
	tc.cancelForm()
	tc.formOpen, tc.formMode, tc.formID = true, "edit", item.ID
	tc.formName, tc.formText, tc.formPath = item.Name, item.Text, item.Source
}

func (tc *taskContext) saveForm() {
	var (
		item taskContextItem
		err  error
	)
	switch tc.formMode {
	case "snippet":
		item, err = tc.AddSnippet(tc.formName, tc.formText)
	case "file":
		item, err = tc.ImportFile(tc.formPath, tc.formName)
	case "edit":
		item, err = tc.Edit(tc.formID, tc.formName, tc.formText)
	default:
		return
	}
	if err != nil {
		tc.formError = err.Error()
		return
	}
	tc.actionError = ""
	tc.cancelForm()
	tc.previewID = item.ID
}

func (tc *taskContext) refreshItem(id string) {
	if _, err := tc.Refresh(id); err != nil {
		tc.actionError = err.Error()
		return
	}
	tc.actionError = ""
}

func validateTaskContext(items []taskContextItem) error {
	total := 0
	for _, item := range items {
		if strings.TrimSpace(item.Name) == "" {
			return errTaskContextName
		}
		if !utf8.ValidString(item.Text) {
			return errTaskContextUTF8
		}
		total += taskContextWrapperBytes(item.inputName(), item.Text)
		if total > taskContextMaxBytes {
			return fmt.Errorf("%w: combined context needs %d bytes", errTaskContextOversize, total)
		}
	}
	return nil
}

// taskContextWrapperBytes mirrors the runtime's serialized caller_context
// wrapper for budget purposes. Go's JSON escaping is a little more eager than
// serde's, so the estimate is conservative; it is not a wire DTO.
func taskContextWrapperBytes(name, text string) int {
	data, _ := json.Marshal(struct {
		Type  string `json:"type"`
		Label string `json:"label"`
		Name  string `json:"name"`
		Text  string `json:"text"`
	}{
		Type:  "caller_context",
		Label: "Caller-provided context (user data)",
		Name:  name,
		Text:  text,
	})
	return len(data)
}

var taskContextBinaryExtensions = map[string]bool{
	".pdf": true, ".doc": true, ".docx": true, ".odt": true, ".rtf": true,
	".xls": true, ".xlsx": true, ".ods": true, ".ppt": true, ".pptx": true, ".odp": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".bmp": true, ".tif": true, ".tiff": true,
	".zip": true, ".gz": true, ".tar": true, ".7z": true, ".rar": true,
	".bin": true, ".exe": true, ".dll": true, ".so": true, ".dylib": true,
	".o": true, ".a": true, ".wasm": true, ".mp3": true, ".mp4": true,
	".mov": true, ".avi": true, ".sqlite": true, ".db": true,
}

func readTaskContextFile(path string) (text, source string, err error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", "", fmt.Errorf("%w: enter a file path", errTaskContextRead)
	}
	ext := strings.ToLower(filepath.Ext(path))
	if taskContextBinaryExtensions[ext] {
		return "", "", fmt.Errorf("%w: %s", errTaskContextUnsupported, ext)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", errTaskContextRead, err)
	}
	file, err := os.Open(absolute)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", errTaskContextRead, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, taskContextReadLimit))
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", errTaskContextRead, err)
	}
	if len(data) > taskContextMaxBytes {
		return "", "", fmt.Errorf("%w: %s is larger than 1 MiB", errTaskContextOversize, filepath.Base(absolute))
	}
	if !utf8.Valid(data) {
		return "", "", fmt.Errorf("%w: %s", errTaskContextUTF8, filepath.Base(absolute))
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", "", fmt.Errorf("%w: %s", errTaskContextNotText, filepath.Base(absolute))
	}
	return string(data), absolute, nil
}

// view renders the compact list and its explicit editor. It depends only on
// the workbench theme, not on the app model.
func (tc *taskContext) view(c *ui.Context, theme workbenchTheme) {
	t := theme.Theme
	ui.Column(c).Gap(8).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Task context").FontSize(14).FontWeight(600).TextColor(t.Text)
			ui.Spacer(c)
			ui.Text(c, fmt.Sprintf("%d %s · %s / 1 MiB", len(tc.items), taskContextItemWord(len(tc.items)), taskContextSizeLabel(tc))).FontSize(11).TextColor(t.TextMuted)
		})
		ui.Text(c, "Saved text is sent as caller-provided context with the next message. A file snapshot changes only when you refresh it.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)

		if tc.actionError != "" {
			ui.Text(c, tc.actionError).FontSize(11).LineHeight(1.4).TextColor(t.Danger)
		}
		if !tc.formOpen {
			ui.Row(c).Gap(8).Children(func() {
				ui.Button(c, "Add snippet").FontSize(12).OnClick(func() { tc.openSnippetForm() })
				ui.Button(c, "Add file").FontSize(12).OnClick(func() { tc.openFileForm() })
			})
		} else {
			tc.formView(c, theme)
		}

		if len(tc.items) == 0 {
			ui.Text(c, "No saved context. The next message will not include caller context.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
		}
		for _, item := range tc.items {
			item := item
			ui.Column(c).Key(item.ID).Padding(9).Radius(8).Background(t.SurfaceHover).Gap(4).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, item.Name).FontSize(12).FontWeight(600).TextColor(t.Text)
					ui.Spacer(c)
					ui.Text(c, taskContextSourceLabel(item)).FontSize(11).TextColor(t.TextMuted)
				})
				if item.Source != "" {
					ui.Text(c, "Source · "+item.Source).FontSize(11).LineHeight(1.35).TextColor(t.TextMuted)
				}
				ui.Row(c).Gap(8).Children(func() {
					ui.Button(c, "Preview").FontSize(11).Padding(2, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { tc.previewID = item.ID })
					ui.Button(c, "Edit").FontSize(11).Padding(2, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { tc.openEditForm(item.ID) })
					if item.Source != "" {
						ui.Button(c, "Refresh from file").FontSize(11).Padding(2, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { tc.refreshItem(item.ID) })
					}
					ui.Button(c, "Remove").FontSize(11).Padding(2, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { tc.Remove(item.ID) })
				})
				if tc.previewID == item.ID {
					tc.previewView(c, theme, item)
				}
			})
		}
	})
}

func (tc *taskContext) formView(c *ui.Context, theme workbenchTheme) {
	t := theme.Theme
	ui.Column(c).Padding(10).Radius(8).Background(t.SurfaceHover).Gap(6).Children(func() {
		title := "Add snippet"
		if tc.formMode == "file" {
			title = "Add file snapshot"
		} else if tc.formMode == "edit" {
			title = "Edit context"
		}
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(12).FontWeight(600).TextColor(t.Text)
			ui.Spacer(c)
			ui.Button(c, "Cancel").FontSize(11).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { tc.cancelForm() })
		})
		switch tc.formMode {
		case "file":
			ui.Text(c, "Name (optional)").FontSize(11).FontWeight(500).TextColor(t.Text)
			ui.TextInput(c, &tc.formName).Label("Context file name").FontSize(13).Placeholder("Defaults to the file name")
			ui.Text(c, "Path").FontSize(11).FontWeight(500).TextColor(t.Text)
			ui.TextInput(c, &tc.formPath).Label("Context file path").FontSize(13).Placeholder("/absolute/path/to/file.txt")
			ui.Text(c, "Only UTF-8 text files are supported; PDF, Office, and other binary files are rejected. The file is read once now; later changes need Refresh.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
		default:
			ui.Text(c, "Name").FontSize(11).FontWeight(500).TextColor(t.Text)
			ui.TextInput(c, &tc.formName).Label("Context name").FontSize(13).Placeholder("Release notes")
			ui.Text(c, "Text").FontSize(11).FontWeight(500).TextColor(t.Text)
			ui.TextArea(c, &tc.formText).Label("Context text").FontSize(13).Height(84).Placeholder("Text to include exactly as written")
			if tc.formMode == "edit" && tc.formPath != "" {
				ui.Text(c, "Editing the snapshot does not change the source file. Refresh re-reads it.").FontSize(11).LineHeight(1.4).TextColor(t.TextMuted)
			}
		}
		if tc.formError != "" {
			ui.Text(c, tc.formError).FontSize(11).LineHeight(1.4).TextColor(t.Danger)
		}
		label := "Save snippet"
		if tc.formMode == "file" {
			label = "Save file"
		} else if tc.formMode == "edit" {
			label = "Save changes"
		}
		ui.PrimaryButton(c, label).FontSize(12).OnClick(func() { tc.saveForm() })
	})
}

func (tc *taskContext) previewView(c *ui.Context, theme workbenchTheme, item taskContextItem) {
	t := theme.Theme
	ui.Column(c).Gap(4).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Preview · "+item.Name).FontSize(11).FontWeight(600).TextColor(t.Text)
			ui.Spacer(c)
			ui.Button(c, "Close preview").FontSize(11).Padding(2, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { tc.previewID = "" })
		})
		if item.Source != "" {
			ui.Text(c, item.Source).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Scroll(c).Height(220).Padding(8).Radius(6).Background(t.Surface).Children(func() {
			markdownReader(c, item.Text, markdownReaderOptions{PlainText: true, CopyLabel: "Copy context text"}).
				FontSize(12).LineHeight(1.45).TextColor(t.Text)
		})
	})
}

func taskContextItemWord(count int) string {
	if count == 1 {
		return "item"
	}
	return "items"
}

func taskContextSourceLabel(item taskContextItem) string {
	if item.Source == "" {
		return "Named snippet"
	}
	return "File snapshot · revision " + fmt.Sprint(item.Revision)
}

func taskContextSizeLabel(tc *taskContext) string {
	total := 0
	for _, item := range tc.items {
		total += taskContextWrapperBytes(item.inputName(), item.Text)
	}
	if total < 1024 {
		return fmt.Sprintf("%d B", total)
	}
	return fmt.Sprintf("%.1f KiB", float64(total)/1024)
}
