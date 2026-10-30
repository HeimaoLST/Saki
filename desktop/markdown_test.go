package main

import (
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

func readerTestView(source *string, streaming *bool, options *markdownReaderOptions, dark *bool) func(*ui.Context) {
	return func(c *ui.Context) {
		opts := *options
		opts.Streaming = *streaming
		c.SetTheme(resolveWorkbenchTheme(*dark).Theme)
		markdownReader(c, *source, opts)
	}
}

var readerFourColumnSource = "# 宽表\n\n" +
	"| 名称 | 说明 | 标记 | 状态 |\n" +
	"| --- | --- | --- | --- |\n" +
	"| 中文长内容需要换行显示 | " + strings.Repeat("abcdefghij", 12) + " | **重点** | [链接](https://example.com/table) |\n" +
	"| 第二行 | 短内容 | `code` | 完成 |\n"

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

	button, ok := tr.Find("Copy message")
	if !ok {
		t.Fatalf("copy control is missing: %q", tr.Texts())
	}
	text, ok := tr.Find("正文")
	if !ok {
		t.Fatalf("message text is missing: %q", tr.Texts())
	}
	before := tr.Image()
	if err := tr.Click("Copy message"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !errors.Is(result, copyErr) {
		t.Fatalf("failure callback got %v after %d calls", result, calls)
	}
	if got, ok := tr.Find("Copy message"); !ok || got != button {
		t.Fatalf("failure feedback moved the copy control: before=%+v after=%+v", button, got)
	}
	if got, ok := tr.Find("正文"); !ok || got != text {
		t.Fatalf("failure feedback moved the prose: before=%+v after=%+v", text, got)
	}
	failedImage := tr.Image()
	if readerImageRegionDiff(before, failedImage, button) == 0 {
		t.Fatal("failure feedback is not visible")
	}

	fail = false
	if err := tr.Click("Copy message"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || result != nil || successes != 1 {
		t.Fatalf("success callback got %v, successes %d after %d calls", result, successes, calls)
	}
	if got, ok := tr.Find("Copy message"); !ok || got != button {
		t.Fatalf("success feedback moved the copy control: before=%+v after=%+v", button, got)
	}
	if got, ok := tr.Find("正文"); !ok || got != text {
		t.Fatalf("success feedback moved the prose: before=%+v after=%+v", text, got)
	}
	if readerImageRegionDiff(failedImage, tr.Image(), button) == 0 {
		t.Fatal("success feedback does not differ from failure feedback")
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
	for i := 0; i < 12 && !tr.Focused("Copy received"); i++ {
		tr.Key(0, ui.KeyTab)
	}
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

func TestMarkdownReaderFourColumnTableWrapsWithoutHorizontalScroll(t *testing.T) {
	source := readerFourColumnSource
	headers := []string{"名称", "说明", "标记", "状态"}
	body := []string{
		"中文长内容需要换行显示",
		strings.Repeat("abcdefghij", 12),
		"**重点**",
		"[链接](https://example.com/table)",
		"第二行",
		"短内容",
		"`code`",
		"完成",
	}
	cells := append(append([]string{}, headers...), body...)
	narrowHeights, wideHeights := map[string]float32{}, map[string]float32{}
	for _, width := range []int{340, 720} {
		streaming := true
		options := markdownReaderOptions{CopyLabel: "Copy message"}
		dark := false
		tr := ui.NewTester(readerTestView(&source, &streaming, &options, &dark), width, 760)

		checkCells := func(when string) {
			t.Helper()
			if !tr.HasText("Table · plain text") {
				t.Fatalf("%dx%d %s: plain-text table label is missing: %q", width, 760, when, tr.Texts())
			}
			for _, want := range cells {
				bounds, ok := tr.Find(want)
				if !ok {
					t.Fatalf("%dx%d %s: cell %q is missing: %q", width, 760, when, want, tr.Texts())
				}
				if bounds.X < 0 || bounds.X+bounds.W > float32(width) {
					t.Fatalf("%dx%d %s: cell %q bounds %+v leave the window", width, 760, when, want, bounds)
				}
				if when == "final" {
					if width == 340 {
						narrowHeights[want] = bounds.H
					} else {
						wideHeights[want] = bounds.H
					}
				}
			}
		}
		checkCells("streamed")
		streaming = false
		tr.Frame()
		checkCells("final")

		if err := tr.Click("Copy message"); err != nil {
			t.Fatal(err)
		}
		if got := tr.Clipboard(); got != source {
			t.Fatalf("%dx%d full copy = %q, want exact source", width, 760, got)
		}

		first, ok := tr.Find(cells[0])
		if !ok {
			t.Fatal("first cell is not visible")
		}
		tr.ClickAt(first.X+2, first.Y+first.H/2)
		tr.Command("selectAll")
		tr.Command("copy")
		copied := tr.Clipboard()
		for _, want := range cells {
			if !strings.Contains(copied, want) {
				t.Fatalf("%dx%d selection dropped %q: %q", width, 760, want, copied)
			}
		}
	}
	for _, want := range []string{body[0], body[1]} {
		if narrowHeights[want] <= wideHeights[want] {
			t.Fatalf("cell %q wrapped height %v at 340 px is not greater than %v at 720 px", want, narrowHeights[want], wideHeights[want])
		}
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

func readerImageRegionDiff(a, b *image.RGBA, r ui.Rect) int {
	x0, y0 := max(0, int(r.X)), max(0, int(r.Y))
	x1, y1 := min(a.Bounds().Dx(), int(r.X+r.W)), min(a.Bounds().Dy(), int(r.Y+r.H))
	count := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if a.RGBAAt(x, y) != b.RGBAAt(x, y) {
				count++
			}
		}
	}
	return count
}

func TestMarkdownReaderCompactSourceCopyVisibilityAndGeometry(t *testing.T) {
	source := "一句话中文消息，复制按钮应贴在消息右侧。"
	streaming := false
	options := markdownReaderOptions{CopyLabel: "Copy message"}
	dark := false
	tr := ui.NewTester(readerTestView(&source, &streaming, &options, &dark), 340, 300)
	tr.SetFocused(true)
	tr.Frame()
	button, ok := tr.Find("Copy message")
	if !ok {
		t.Fatalf("copy control is missing: %q", tr.Texts())
	}
	if button.W != 32 || button.H != 32 {
		t.Fatalf("copy hit area = %+v, want 32x32", button)
	}
	text, ok := tr.Find(source)
	if !ok {
		t.Fatalf("one-line message is missing: %q", tr.Texts())
	}
	if button.X+button.W > 340 || text.X < 0 || text.X+text.W > button.X+0.5 {
		t.Fatalf("content/control overflow or overlap: text=%+v button=%+v", text, button)
	}
	if text.Y < button.Y || text.Y > button.Y+button.H {
		t.Fatalf("content is not first/top-aligned with the control: text=%+v button=%+v", text, button)
	}
	hidden := tr.Image()
	tr.Move(text.X+text.W/2, text.Y+text.H/2)
	tr.Frame()
	hoverButton, _ := tr.Find("Copy message")
	hoverText, _ := tr.Find(source)
	if hoverButton != button || hoverText != text {
		t.Fatalf("hover changed geometry: button %+v -> %+v text %+v -> %+v", button, hoverButton, text, hoverText)
	}
	hovered := tr.Image()
	if readerImageRegionDiff(hidden, hovered, button) == 0 {
		t.Fatal("copy control is not visible on message hover")
	}
	tr.Move(1, 1)
	tr.Frame()
	beforeFocus := tr.Image()
	tr.Key(0, ui.KeyTab)
	tr.Frame()
	if !tr.Focused("Copy message") {
		t.Fatalf("copy control is not first in keyboard order: %q", tr.Texts())
	}
	focusButton, _ := tr.Find("Copy message")
	focusText, _ := tr.Find(source)
	if focusButton != button || focusText != text {
		t.Fatalf("focus changed geometry: button %+v -> %+v text %+v -> %+v", button, focusButton, text, focusText)
	}
	if readerImageRegionDiff(beforeFocus, tr.Image(), button) == 0 {
		t.Fatal("copy control is not visible on keyboard focus")
	}
}

func TestMarkdownReaderCompactSourceCopyAddsNoShortMessageHeight(t *testing.T) {
	source := "一句话中文消息。"
	options := markdownReaderOptions{CopyLabel: "Copy message"}
	dark := false
	var root ui.Handle
	var rootBounds ui.Rect
	tr := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(resolveWorkbenchTheme(dark).Theme)
		element := markdownReader(c, source, options)
		element.Bind(&root)
		rootBounds = root.Bounds(c)
	}, 340, 300)
	tr.Frame()
	tr.Frame()
	text, ok := tr.Find(source)
	if !ok {
		t.Fatalf("one-line message is missing: %q", tr.Texts())
	}
	if rootBounds.H <= 0 || rootBounds.H > text.H+1 {
		t.Fatalf("short message height = %v for text height %v; compact copy added a vertical row", rootBounds.H, text.H)
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
	scenes := []struct {
		name, hover, source string
	}{
		{"one-line", "一句话中文消息，复制按钮应贴在消息右侧。", "一句话中文消息，复制按钮应贴在消息右侧。"},
		{"rich", "宽表", readerFourColumnSource + "\n```\n" + long + "\n```\n"},
	}
	streaming := false
	options := markdownReaderOptions{CopyLabel: "Copy message"}
	for _, scene := range scenes {
		source := scene.source
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
				write := func(state string) {
					path := filepath.Join(dir, fmt.Sprintf("markdown-reader-%s-%s-%s%s.png", scene.name, theme, size.name, state))
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
				write("")
				if r, ok := tr.Find(scene.hover); ok {
					tr.Move(r.X+r.W/2, r.Y+r.H/2)
					tr.Frame()
					write("-hover")
				}
				tr.Move(1, 1)
				tr.Frame()
				for i := 0; i < 20 && !tr.Focused("Copy message"); i++ {
					tr.Key(0, ui.KeyTab)
				}
				tr.Frame()
				write("-focus")
			}
		}
	}
}

func TestMarkdownLinkElementNilKeepsTabAndEnterBehavior(t *testing.T) {
	source := "请阅读[文档](https://example.com/docs)。"
	tr := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(resolveWorkbenchTheme(false).Theme)
		markdown.View(c, source, nil)
	}, 600, 300)
	for i := 0; i < 20 && !tr.Focused("文档"); i++ {
		tr.Key(0, ui.KeyTab)
		tr.Frame()
	}
	if !tr.Focused("文档") {
		t.Fatalf("Tab never reached the link: %v", tr.Texts())
	}
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	if got := tr.OpenedURLs(); len(got) != 1 || got[0] != "https://example.com/docs" {
		t.Fatalf("nil hook opened %v, want only the original URL", got)
	}
}

type markdownLinkReport struct {
	block, inline int
	dest          string
}

func TestMarkdownLinkElementReportsASTPositionsForRepeatedURLs(t *testing.T) {
	source := "[链接一](https://example.com/same) 和 [链接二](https://example.com/same)"
	focus := ""
	var reports []markdownLinkReport
	tr := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(resolveWorkbenchTheme(false).Theme)
		reports = reports[:0]
		options := markdown.Options{LinkElement: func(c *ui.Context, block, inline int, dest string, element ui.Element) {
			reports = append(reports, markdownLinkReport{block: block, inline: inline, dest: dest})
			element.FocusBind(&focus, fmt.Sprintf("%d:%d:%s", block, inline, dest))
		}}
		markdown.View(c, source, &options)
	}, 600, 300)
	for i := 0; i < 20 && !tr.Focused("链接一"); i++ {
		tr.Key(0, ui.KeyTab)
		tr.Frame()
	}
	if !tr.Focused("链接一") {
		t.Fatalf("Tab never reached the first link: %v", tr.Texts())
	}
	firstFocus := focus
	for i := 0; i < 20 && !tr.Focused("链接二"); i++ {
		tr.Key(0, ui.KeyTab)
		tr.Frame()
	}
	if !tr.Focused("链接二") {
		t.Fatalf("Tab never reached the second link: %v", tr.Texts())
	}
	if focus == firstFocus {
		t.Fatalf("repeated URL positions shared focus identity %q", focus)
	}
	if len(reports) != 2 || reports[0].dest != "https://example.com/same" || reports[1].dest != reports[0].dest {
		t.Fatalf("hook reports = %+v, want both links with the same destination", reports)
	}
	if reports[0].block != reports[1].block || reports[0].inline == reports[1].inline {
		t.Fatalf("hook positions = %+v, want distinct inline indices", reports)
	}
	tr.Key(0, ui.KeyEnter)
	tr.Frame()
	if got := tr.OpenedURLs(); len(got) != 1 || got[0] != "https://example.com/same" {
		t.Fatalf("second link opened %v, want only its URL", got)
	}
}

func TestMarkdownLinkElementPreservesOnLinkAndIgnoresUnopenableDestinations(t *testing.T) {
	for _, withHook := range []bool{false, true} {
		t.Run(fmt.Sprintf("hook=%v", withHook), func(t *testing.T) {
			const url = "https://example.com/docs"
			var onLink []string
			var reports []markdownLinkReport
			options := markdown.Options{OnLink: func(url string) { onLink = append(onLink, url) }}
			if withHook {
				options.LinkElement = func(c *ui.Context, block, inline int, dest string, element ui.Element) {
					reports = append(reports, markdownLinkReport{block: block, inline: inline, dest: dest})
				}
			}
			source := "请阅读[文档](" + url + ")。"
			tr := ui.NewTester(func(c *ui.Context) {
				c.SetTheme(resolveWorkbenchTheme(false).Theme)
				markdown.View(c, source, &options)
			}, 600, 300)
			for i := 0; i < 20 && !tr.Focused("文档"); i++ {
				tr.Key(0, ui.KeyTab)
				tr.Frame()
			}
			if !tr.Focused("文档") {
				t.Fatalf("Tab never reached the OnLink link: %v", tr.Texts())
			}
			tr.Key(0, ui.KeyEnter)
			tr.Frame()
			if len(onLink) != 1 || onLink[0] != url {
				t.Fatalf("OnLink calls = %v, want the original URL once", onLink)
			}
			if got := tr.OpenedURLs(); len(got) != 0 {
				t.Fatalf("OnLink link also opened natively: %v", got)
			}
			if withHook && (len(reports) == 0 || reports[len(reports)-1].dest != url) {
				t.Fatalf("hook reports = %+v, want the OnLink destination", reports)
			}
		})
	}

	for _, withHook := range []bool{false, true} {
		t.Run(fmt.Sprintf("disallowed-hook=%v", withHook), func(t *testing.T) {
			options := markdown.Options{}
			if withHook {
				options.LinkElement = func(c *ui.Context, block, inline int, dest string, element ui.Element) {}
			}
			source := "危险[链接](javascript:alert(1))。"
			tr := ui.NewTester(func(c *ui.Context) {
				c.SetTheme(resolveWorkbenchTheme(false).Theme)
				markdown.View(c, source, &options)
			}, 600, 300)
			for i := 0; i < 20 && !tr.Focused("链接"); i++ {
				tr.Key(0, ui.KeyTab)
				tr.Frame()
			}
			if !tr.Focused("链接") {
				t.Fatalf("Tab never reached the disallowed link: %v", tr.Texts())
			}
			tr.Key(0, ui.KeyEnter)
			tr.Frame()
			if got := tr.OpenedURLs(); len(got) != 0 {
				t.Fatalf("disallowed destination opened %v", got)
			}
		})
	}

	for _, withHook := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending-hook=%v", withHook), func(t *testing.T) {
			options := markdown.Options{}
			if withHook {
				options.LinkElement = func(c *ui.Context, block, inline int, dest string, element ui.Element) {}
			}
			source := "请阅读[待定](https://example.com"
			tr := ui.NewTester(func(c *ui.Context) {
				c.SetTheme(resolveWorkbenchTheme(false).Theme)
				markdown.Stream(c, source, &options)
			}, 600, 300)
			for i := 0; i < 20 && !tr.Focused("待定"); i++ {
				tr.Key(0, ui.KeyTab)
				tr.Frame()
			}
			if !tr.Focused("待定") {
				t.Fatalf("Tab never reached the pending link: %v", tr.Texts())
			}
			tr.Key(0, ui.KeyEnter)
			tr.Frame()
			if got := tr.OpenedURLs(); len(got) != 0 {
				t.Fatalf("pending destination opened %v", got)
			}
		})
	}
}
