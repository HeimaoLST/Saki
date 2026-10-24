package main

import (
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func readerTestView(source *string, streaming *bool, options *markdownReaderOptions, dark *bool) func(*ui.Context) {
	return func(c *ui.Context) {
		opts := *options
		opts.Streaming = *streaming
		c.SetTheme(resolveWorkbenchTheme(*dark).Theme)
		markdownReader(c, *source, opts)
	}
}

const readerRichSource = "# 阅读笔记\n\n正文和**重点**，以及[文档](https://example.com/docs)与[危险](javascript:alert(1))。\n\n" +
	"> 引用文字\n\n行内 `code` 结束。\n\n" +
	"- 第一项\n- 第二项\n\n" +
	"| 名称 | 值 |\n| --- | --- |\n| 语言 | 中文 |\n\n" +
	"![图](https://example.com/x.png)\n\n" +
	"```go\nfunc main() {\n  println(\"hi\")\n}\n```\n"

func TestMarkdownReaderRendersIntoRichLayoutInBothThemes(t *testing.T) {
	for _, dark := range []bool{false, true} {
		source, streaming := readerRichSource, false
		options := markdownReaderOptions{CopyLabel: "Copy message"}
		tr := ui.NewTester(readerTestView(&source, &streaming, &options, &dark), 760, 1000)
		for _, want := range []string{"阅读笔记", "正文和重点", "引用文字", "行内 code 结束。", "第一项", "第二项", "名称", "中文", "图", "func main() {", "go", "Copy message"} {
			if !tr.HasText(want) {
				t.Fatalf("dark=%v: no %q in %q", dark, want, tr.Texts())
			}
		}
		if err := tr.Click("文档"); err != nil {
			t.Fatal(err)
		}
		if err := tr.Click("危险"); err != nil {
			t.Fatal(err)
		}
		if got := tr.OpenedURLs(); len(got) != 1 || got[0] != "https://example.com/docs" {
			t.Fatalf("dark=%v opened %q, want only the safe https link", dark, got)
		}
	}
}

func TestMarkdownReaderCopiesExactSourceAndCodeWithoutFences(t *testing.T) {
	source := "# 标题\n\n```go\n  two\n    four\n```"
	streaming := false
	options := markdownReaderOptions{} // the default full-copy label is "Copy message"
	dark := false
	tr := ui.NewTester(readerTestView(&source, &streaming, &options, &dark), 600, 800)

	if err := tr.Click("Copy message"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != source {
		t.Fatalf("full copy = %q, want the raw source %q", got, source)
	}

	if err := tr.Click("Copy code"); err != nil {
		t.Fatal(err)
	}
	if got, want := tr.Clipboard(), "  two\n    four\n"; got != want {
		t.Fatalf("code copy = %q, want %q", got, want)
	}
}

func TestMarkdownReaderFullCopyPreservesRawLineEndings(t *testing.T) {
	source := "# 原始\r\n\r\n正文  \r\n"
	streaming := false
	options := markdownReaderOptions{CopyLabel: "Copy excerpt"}
	dark := false
	tr := ui.NewTester(readerTestView(&source, &streaming, &options, &dark), 500, 500)

	if err := tr.Click("Copy excerpt"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != source {
		t.Fatalf("full copy = %q, want raw %q", got, source)
	}
}

func TestMarkdownReaderAcknowledgesCopyFailureLocallyAndByCallback(t *testing.T) {
	source := "正文\n"
	streaming := false
	copyErr := errors.New("clipboard unavailable")
	fail := true
	calls, successes := 0, 0
	var result error
	options := markdownReaderOptions{
		CopyLabel: "Copy message",
		CopyText: func(text string) error {
			calls++
			if fail {
				return copyErr
			}
			return nil
		},
		OnCopyResult: func(err error) {
			result = err
			if err == nil {
				successes++
			}
		},
	}
	dark := false
	tr := ui.NewTester(readerTestView(&source, &streaming, &options, &dark), 500, 500)

	if err := tr.Click("Copy message"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !errors.Is(result, copyErr) {
		t.Fatalf("failure callback got %v after %d calls", result, calls)
	}
	if !tr.HasText("Copy failed") {
		t.Fatalf("failure is not visible in %q", tr.Texts())
	}

	fail = false
	if err := tr.Click("Copy message"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || result != nil || successes != 1 {
		t.Fatalf("success callback got %v, successes %d after %d calls", result, successes, calls)
	}
	if !tr.HasText("Copied") {
		t.Fatalf("success is not visible in %q", tr.Texts())
	}
}

func TestClipboardReadbackDetectsMismatch(t *testing.T) {
	stored := ""
	if err := writeClipboardText("exact", func(s string) { stored = s + "!" }, func() string { return stored }); !errors.Is(err, errClipboardReadback) {
		t.Fatalf("mismatched readback = %v, want %v", err, errClipboardReadback)
	}
	stored = ""
	if err := writeClipboardText("exact", func(s string) { stored = s }, func() string { return stored }); err != nil {
		t.Fatalf("matching readback = %v", err)
	}
}

func TestMarkdownReaderStreamsIncompleteFenceThenKeepsFocusIntoFinalAndHistory(t *testing.T) {
	source := "```go\nfunc main() {\n  println(\"hi\")\n"
	streaming := true
	options := markdownReaderOptions{CopyLabel: "Copy received"}
	dark := false
	tr := ui.NewTester(readerTestView(&source, &streaming, &options, &dark), 620, 800)
	tr.SetFocused(true)

	if !tr.HasText("func main() {") {
		t.Fatalf("received stream does not show its open fence in %q", tr.Texts())
	}
	if tr.Focused("Copy received") {
		t.Fatal("the reader stole focus on first render")
	}
	tr.Key(0, ui.KeyTab)
	if !tr.Focused("Copy received") {
		t.Fatal("the full-source copy control is not keyboard focusable")
	}
	if _, ok := tr.Find("Copy received code"); !ok {
		t.Fatalf("open received code has no copy control in %q", tr.Texts())
	}
	if err := tr.Click("Copy received code"); err != nil {
		t.Fatal(err)
	}
	if got, want := tr.Clipboard(), "func main() {\n  println(\"hi\")\n"; got != want {
		t.Fatalf("received code copy = %q, want %q", got, want)
	}
	if err := tr.Click("Copy received"); err != nil {
		t.Fatal(err)
	}
	if !tr.Focused("Copy received") {
		t.Fatal("the full-source copy control did not keep focus after clicking")
	}

	source += "```\n"
	streaming = false
	options.CopyLabel = "Copy message"
	tr.Frame()
	if !tr.HasText("println(\"hi\")") {
		t.Fatalf("final frame is missing the completed code in %q", tr.Texts())
	}
	if !tr.Focused("Copy message") {
		t.Fatal("changing from Stream to View and relabeling the control lost its focus")
	}
	tr.Frame() // history renders through the same finished View path
	if !tr.Focused("Copy message") {
		t.Fatal("a repeated finished/history frame lost the copy control's focus")
	}
	if err := tr.Click("Copy code"); err != nil {
		t.Fatal(err)
	}
	if got, want := tr.Clipboard(), "func main() {\n  println(\"hi\")\n"; got != want {
		t.Fatalf("final code copy = %q, want %q", got, want)
	}
}

func TestMarkdownReaderCopiesCodeFromAFinallyUnclosedFence(t *testing.T) {
	source := "```go\nreceived only\n"
	streaming := false
	options := markdownReaderOptions{CopyLabel: "Copy message"}
	dark := false
	tr := ui.NewTester(readerTestView(&source, &streaming, &options, &dark), 500, 400)
	if err := tr.Click("Copy code"); err != nil {
		t.Fatal(err)
	}
	if got, want := tr.Clipboard(), "received only\n"; got != want {
		t.Fatalf("unclosed final fence copy = %q, want %q", got, want)
	}
}

func TestMarkdownReaderSelectionCopiesPlainTextAndMarkdown(t *testing.T) {
	source := "# 标题\n\n正文和列表\n\n- 第一项\n- 第二项\n"
	streaming := false
	options := markdownReaderOptions{CopyLabel: "Copy message"}
	dark := false
	tr := ui.NewTester(readerTestView(&source, &streaming, &options, &dark), 600, 700)
	r, ok := tr.Find("正文和列表")
	if !ok {
		t.Fatalf("selected paragraph is not visible in %q", tr.Texts())
	}
	tr.ClickAt(r.X+2, r.Y+r.H/2)
	tr.Command("selectAll")
	tr.Command("copy")
	if got := tr.Clipboard(); !strings.Contains(got, "正文和列表") || !strings.Contains(got, "第一项") || strings.Contains(got, "#") {
		t.Fatalf("selection copied %q, want plain text across the document", got)
	}
	tr.Key(ui.Cmd|ui.Shift, ui.KeyC)
	if got := tr.Clipboard(); !strings.Contains(got, "# 标题") || !strings.Contains(got, "- 第一项") {
		t.Fatalf("Markdown selection copied %q, want source syntax", got)
	}
}

func TestMarkdownReaderWrapsLongCodeAtNarrowWidth(t *testing.T) {
	long := strings.Repeat("宽度", 45) + "tail"
	source := "```go\n" + long + "\n```\n"
	streaming := false
	options := markdownReaderOptions{CopyLabel: "Copy message"}
	dark := false
	narrow := ui.NewTester(readerTestView(&source, &streaming, &options, &dark), 340, 760)
	wide := ui.NewTester(readerTestView(&source, &streaming, &options, &dark), 1200, 760)
	within, ok := narrow.Find(long)
	if !ok {
		t.Fatalf("exact code label is not visible at 340 px: %q", narrow.Texts())
	}
	unwrapped, ok := wide.Find(long)
	if !ok {
		t.Fatalf("exact code label is not visible at 1200 px: %q", wide.Texts())
	}
	if within.X < 0 || within.X+within.W > 340 {
		t.Fatalf("wrapped code bounds %+v leave the 340 px window", within)
	}
	if within.H <= unwrapped.H {
		t.Fatalf("wrapped height %v is not greater than one-line height %v", within.H, unwrapped.H)
	}
	if within.W >= unwrapped.W {
		t.Fatalf("wrapped width %v is not narrower than unwrapped width %v", within.W, unwrapped.W)
	}
	if err := narrow.Click("Copy code"); err != nil {
		t.Fatal(err)
	}
	if got := narrow.Clipboard(); got != long+"\n" {
		t.Fatalf("wrapped code copy = %q, want %q", got, long+"\n")
	}
}

func TestMarkdownReaderCopiesEachCodeBlockOwnPayload(t *testing.T) {
	source := "```go\nfirst\n```\n\n```js\nsecond\n```\n"
	streaming := false
	options := markdownReaderOptions{CopyLabel: "Copy message"}
	dark := false
	tr := ui.NewTester(readerTestView(&source, &streaming, &options, &dark), 600, 600)
	first, ok := tr.Find("Copy code")
	if !ok {
		t.Fatalf("first code control is not visible: %q", tr.Texts())
	}
	if err := tr.Click("Copy code"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != "first\n" {
		t.Fatalf("first code copy = %q", got)
	}

	js, ok := tr.Find("js")
	if !ok {
		t.Fatalf("second code block is not visible: %q", tr.Texts())
	}
	tr.ClickAt(first.X+first.W/2, js.Y+js.H/2)
	if got := tr.Clipboard(); got != "second\n" {
		t.Fatalf("second code copy = %q, want its own payload", got)
	}
}

func TestMarkdownReaderSelectionSurvivesStreamAppendAndCopiesCurrentSource(t *testing.T) {
	source := "First paragraph here.\n\nSecond one is being"
	streaming := true
	options := markdownReaderOptions{CopyLabel: "Copy message"}
	dark := false
	tr := ui.NewTester(readerTestView(&source, &streaming, &options, &dark), 620, 500)
	r, ok := tr.Find("First paragraph here.")
	if !ok {
		t.Fatalf("first paragraph is not visible: %q", tr.Texts())
	}
	tr.ClickAt(r.X+2, r.Y+r.H/2)
	tr.Command("selectAll")
	tr.Command("copy")
	selected := tr.Clipboard()
	if !strings.Contains(selected, "First paragraph here.") {
		t.Fatalf("initial selection copied %q", selected)
	}
	if tr.Focused("Copy message") {
		t.Fatal("selecting the stream stole focus to the full-copy control")
	}

	source += " written, word by word."
	tr.Frame()
	if tr.Focused("Copy message") {
		t.Fatal("appending to the stream stole focus to the full-copy control")
	}
	tr.Command("copy")
	if got := tr.Clipboard(); got != selected {
		t.Fatalf("selection after append copied %q, want the original selection %q", got, selected)
	}

	if err := tr.Click("Copy message"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != source {
		t.Fatalf("full-source copy after append = %q, want current source %q", got, source)
	}
}

// TestMarkdownReaderScreenshot is opt-in because it writes images. Run it
// with SAKI_MARKDOWN_READER_SCREENSHOT_DIR, or the Go test cache may reuse
// an older result:
//
//	SAKI_MARKDOWN_READER_SCREENSHOT_DIR=/tmp/nagi-reader-113 \
//	  go test -count=1 -run TestMarkdownReaderScreenshot ./...
func TestMarkdownReaderScreenshot(t *testing.T) {
	dir := os.Getenv("SAKI_MARKDOWN_READER_SCREENSHOT_DIR")
	if dir == "" {
		t.Skip("set SAKI_MARKDOWN_READER_SCREENSHOT_DIR to write headless previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("abcdefghij", 24)
	source := readerRichSource + "\n```\n" + long + "\n```\n"
	streaming := false
	options := markdownReaderOptions{CopyLabel: "Copy message"}
	for _, size := range []struct {
		name string
		w, h int
	}{{"narrow", 340, 760}, {"normal", 720, 960}} {
		for _, dark := range []bool{false, true} {
			dark := dark
			tr := ui.NewTester(readerTestView(&source, &streaming, &options, &dark), size.w, size.h)
			theme := "light"
			if dark {
				theme = "dark"
			}
			path := filepath.Join(dir, fmt.Sprintf("markdown-reader-%s-%s.png", theme, size.name))
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, tr.Image()); err != nil {
				f.Close()
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}
