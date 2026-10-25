package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func appReadingTestApp(t *testing.T) (*appModel, *contact) {
	t.Helper()
	a, ct := workspaceTestApp(t)
	a.workspaceOpen = false
	return a, ct
}

func TestAppAssistantMarkdownStreamsFinalRestoresAndCopies(t *testing.T) {
	const md = "# 标题\n\n列表：\n\n- 第一项\n- 第二项\n\n> 引用\n\n[链接](https://example.com/docs)\n\n" +
		"```go\n\tfmt.Println(\"x\")\n```\n\n| 名称 | 值 |\n| --- | --- |\n| 语言 | 中文 |\n"

	for _, size := range []struct {
		width, height int
	}{{1040, 720}, {720, 640}} {
		a, ct := appReadingTestApp(t)
		a.windowWidth = size.width
		ct.ActiveTaskID, ct.LastTaskID = "t1", "t1"
		ct.Messages = []*message{{
			Key: "live:t1:text:a1", Kind: msgContact, TaskID: "t1", ItemID: "a1",
			Text: "# 标题\n\n", Streaming: true,
		}}
		tr := ui.NewTester(a.view, size.width, size.height)
		tr.Frame()
		if !tr.HasText("标题") || !tr.HasText("Copy message") {
			t.Fatalf("%dx%d streaming markdown missing: %q", size.width, size.height, tr.Texts())
		}

		a.applyEvent(ct, runtimeEventEnvelope{
			Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
			Event: &runtimeEvent{Type: "response_text_delta", ItemID: "a1", Delta: "列表：\n\n- 第一项\n- 第二项\n\n"},
		})
		a.applyEvent(ct, runtimeEventEnvelope{
			Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
			Event: &runtimeEvent{Type: "response_text", ItemID: "a1", Text: md},
		})
		tr.Frame()
		for _, want := range []string{"标题", "第一项", "第二项", "引用", "链接", "fmt.Println", "名称", "中文"} {
			if !tr.HasText(want) {
				t.Fatalf("%dx%d final markdown missing %q: %q", size.width, size.height, want, tr.Texts())
			}
		}
		if err := tr.Click("Copy message"); err != nil {
			t.Fatal(err)
		}
		if got := tr.Clipboard(); got != md {
			t.Fatalf("%dx%d full message copy = %q, want exact source", size.width, size.height, got)
		}
		if err := tr.Click("Copy code"); err != nil {
			t.Fatal(err)
		}
		if got, want := tr.Clipboard(), "\tfmt.Println(\"x\")\n"; got != want {
			t.Fatalf("%dx%d code copy = %q, want %q", size.width, size.height, got, want)
		}

		content, err := json.Marshal(md)
		if err != nil {
			t.Fatal(err)
		}
		a.applyHistory(ct, []historyRecord{
			{Offset: 0, Record: journalRecord{Type: "task_started", TaskID: "t1", MessageID: "m1"}},
			{Offset: 1, Record: journalRecord{Type: "messages", Items: []inputItem{{
				Type: "message", Role: "assistant", Content: content,
			}}}},
			{Offset: 2, Record: journalRecord{Type: "task_finished", TaskID: "t1", Status: "completed"}},
		}, "completed")
		tr.Frame()
		if !tr.HasText("第一项") || !tr.HasText("引用") || !tr.HasText("fmt.Println") {
			t.Fatalf("%dx%d restored markdown missing: %q", size.width, size.height, tr.Texts())
		}
		if err := tr.Click("Copy message"); err != nil {
			t.Fatal(err)
		}
		if got := tr.Clipboard(); got != md {
			t.Fatalf("%dx%d restored copy = %q, want exact source", size.width, size.height, got)
		}
	}
}

func TestAppUserMessageStaysLiteralAndCopiesRaw(t *testing.T) {
	const raw = "# 不是标题\n\n- 不是列表\n\n```\nnot code\n```"
	a, ct := appReadingTestApp(t)
	ct.Messages = []*message{{Key: "live:t1:user", Kind: msgUser, Text: raw}}
	tr := ui.NewTester(a.view, 720, 640)
	tr.Frame()
	if !tr.HasText("# 不是标题") || !tr.HasText("not code") {
		t.Fatalf("literal user text missing: %q", tr.Texts())
	}
	if tr.HasText("Copy code") || tr.HasText("Table · plain text") {
		t.Fatalf("user text was parsed as Markdown: %q", tr.Texts())
	}
	if err := tr.Click("Copy message"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != raw {
		t.Fatalf("user copy = %q, want raw %q", got, raw)
	}
}

func TestAppHistoryAndSourcesRenderAndCopyMarkdown(t *testing.T) {
	a, ct := appReadingTestApp(t)
	ct.History = []historyEntry{
		{ID: "h1", Date: "Oct 1", From: "contact", Text: "## 历史标题\n\n- 条目\n\n```go\n\tx := 1\n```"},
		{ID: "h2", Date: "Oct 1", From: "me", Text: "# 用户原文"},
	}
	ct.Messages = []*message{{ID: 1, Kind: msgContact, Text: "answer", Sources: []string{"h1", "h2"}}}
	a.openHistory()
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("历史标题") || !tr.HasText("条目") || !tr.HasText("x := 1") {
		t.Fatalf("history markdown missing: %q", tr.Texts())
	}
	if !tr.HasText("# 用户原文") {
		t.Fatalf("history user text was not literal: %q", tr.Texts())
	}
	if !tr.HasText("Copy history message") {
		t.Fatalf("history copy control missing: %q", tr.Texts())
	}

	a.backToChat()
	a.openSources(1)
	tr.Frame()
	if !tr.HasText("历史标题") || !tr.HasText("条目") || !tr.HasText("Copy source message") {
		t.Fatalf("source markdown/copy missing: %q", tr.Texts())
	}
	if !tr.HasText("# 用户原文") {
		t.Fatalf("source user text was not literal: %q", tr.Texts())
	}
	if err := tr.Click("Copy source message"); err != nil {
		t.Fatal(err)
	}
	if got, want := tr.Clipboard(), ct.History[0].Text; got != want {
		t.Fatalf("source copy = %q, want %q", got, want)
	}
}

func TestAppEmptyChatHasNoBackToLatest(t *testing.T) {
	a, _ := appReadingTestApp(t)
	tr := ui.NewTester(a.view, 720, 640)
	tr.Frame()
	if tr.HasText("Back to latest") {
		t.Fatalf("empty chat showed Back to latest: %q", tr.Texts())
	}
}

func TestAppToolDetailsShowAndCopyCompleteReceivedOutput(t *testing.T) {
	a, ct := workspaceTestApp(t)
	output := strings.Repeat("received tool line\n", 500) + "# plain tail marker"
	if len(output) <= 6000 {
		t.Fatalf("fixture is not long enough: %d", len(output))
	}
	ct.Works = []*workItem{{
		TaskID: "t1", State: workRunning,
		Tools: []*workTool{{
			CallID: "k1", Name: "read", Args: `{"path":"README.md"}`,
			Output: output, Started: true, Finished: true,
		}},
	}}
	ct.ActiveTaskID, ct.LastTaskID = "t1", "t1"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Preview ·") {
		t.Fatalf("collapsed tool summary is not labeled as a preview: %q", tr.Texts())
	}
	if err := tr.Click("Copy full output"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != output {
		t.Fatalf("collapsed tool copy = %d bytes, want %d exact bytes", len(got), len(output))
	}
	if err := tr.Click("Details"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.HasText("# plain tail marker") {
		t.Fatalf("expanded tool detail lost the received tail: %q", tr.Texts())
	}
	if tr.HasText("Copy full output") || tr.HasText("Copy code") {
		t.Fatalf("expanded tool detail claims a non-plain or duplicate copy surface: %q", tr.Texts())
	}
	if err := tr.Click("Copy received output"); err != nil {
		t.Fatal(err)
	}
	if got := tr.Clipboard(); got != output {
		t.Fatalf("expanded tool copy = %d bytes, want %d exact bytes", len(got), len(output))
	}
	if len(ct.Messages) != 0 {
		t.Fatalf("tool details added chat messages: %+v", ct.Messages)
	}
}

func TestAppFourColumnTableAndCodeFitReadingBubble(t *testing.T) {
	md := "# 宽表\n\n| 名称 | 说明 | 标记 | 状态 |\n| --- | --- | --- | --- |\n| 中文长内容需要换行显示 | " +
		strings.Repeat("abcdefghij", 18) + " | **重点** | `code` |\n\n```go\n" +
		strings.Repeat("long-code-token-", 20) + "\n```\n"
	cells := []string{"名称", "说明", "标记", "状态", "中文长内容需要换行显示", strings.Repeat("abcdefghij", 18), "**重点**", "`code`"}
	for _, size := range []struct {
		width, height int
	}{{1040, 720}, {720, 640}} {
		a, ct := appReadingTestApp(t)
		a.windowWidth = size.width
		ct.Messages = []*message{{ID: 1, Kind: msgContact, Text: md}}
		tr := ui.NewTester(a.view, size.width, size.height)
		tr.Frame()
		for _, want := range cells {
			bounds, ok := tr.Find(want)
			if !ok {
				t.Fatalf("%dx%d table cell %q missing: %q", size.width, size.height, want, tr.Texts())
			}
			if bounds.X < 0 || bounds.X+bounds.W > float32(size.width) {
				t.Fatalf("%dx%d table cell %q overflows: %+v", size.width, size.height, want, bounds)
			}
		}
		codeLine := strings.Repeat("long-code-token-", 20)
		if bounds, ok := tr.Find(codeLine); !ok || bounds.X < 0 || bounds.X+bounds.W > float32(size.width) {
			t.Fatalf("%dx%d code line missing or overflowing: %+v ok=%v", size.width, size.height, bounds, ok)
		}
		for _, label := range []string{"Copy message", "Copy code"} {
			bounds, ok := tr.Find(label)
			if !ok {
				t.Fatalf("%dx%d %q missing: %q", size.width, size.height, label, tr.Texts())
			}
			if bounds.X < 0 || bounds.X+bounds.W > float32(size.width) {
				t.Fatalf("%dx%d %q overflows: %+v", size.width, size.height, label, bounds)
			}
		}
	}
}

func TestAppChatScrollUpSurvivesStreamAppendAndBackToLatest(t *testing.T) {
	a, ct := appReadingTestApp(t)
	for i := 0; i < 50; i++ {
		ct.Messages = append(ct.Messages, &message{
			Key: fmt.Sprintf("m-%02d", i), Kind: msgContact,
			Text: fmt.Sprintf("message %02d\n\nwith a second line", i),
		})
	}
	ct.Messages = append(ct.Messages, &message{
		Key: "live:t1:text:a1", Kind: msgContact, TaskID: "t1", ItemID: "a1", Text: "live start", Streaming: true,
	})
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !ct.list.AtEnd() {
		t.Fatal("chat did not start following its end")
	}
	for i := 0; i < 3 && ct.list.AtEnd(); i++ {
		tr.Scroll(500, 300, 0, -4000)
		tr.Frame()
	}
	if ct.list.AtEnd() || !tr.HasText("Back to latest") {
		t.Fatalf("scrolling up did not pause follow/back control: atEnd=%v", ct.list.AtEnd())
	}
	first, _ := ct.list.Visible()
	row, ok := tr.Find(fmt.Sprintf("message %02d", first))
	if !ok {
		t.Fatalf("first visible row is not readable: %d %q", first, tr.Texts())
	}
	tr.ClickAt(row.X+2, row.Y+row.H/2)
	tr.Command("selectAll")
	tr.Command("copy")
	selected := tr.Clipboard()
	if selected == "" {
		t.Fatal("selection copy is empty")
	}
	firstBeforeAppend, _ := ct.list.Visible()

	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "response_text_delta", ItemID: "a1", Delta: " appended"},
	})
	tr.Frame()
	if ct.list.AtEnd() {
		t.Fatal("appending while scrolled up jumped back to the end")
	}
	afterFirst, _ := ct.list.Visible()
	if afterFirst != firstBeforeAppend {
		t.Fatalf("appending moved the visible window: %d -> %d", firstBeforeAppend, afterFirst)
	}
	tr.SetClipboard("")
	tr.Command("copy")
	if got := tr.Clipboard(); got != selected {
		t.Fatalf("append changed the earlier selection: %q -> %q", selected, got)
	}

	if err := tr.Click("Composer for Saki"); err != nil {
		t.Fatal(err)
	}
	if !tr.Focused("Composer for Saki") {
		t.Fatal("composer did not take focus")
	}
	a.applyEvent(ct, runtimeEventEnvelope{
		Version: 1, Type: "event", SessionID: "s1", TaskID: "t1", MessageID: "m1",
		Event: &runtimeEvent{Type: "response_text_delta", ItemID: "a1", Delta: " more"},
	})
	tr.Frame()
	if !tr.Focused("Composer for Saki") {
		t.Fatal("streaming text stole composer focus")
	}

	if err := tr.Click("Back to latest"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !ct.list.AtEnd() || tr.HasText("Back to latest") {
		t.Fatal("Back to latest did not resume following")
	}
}

func TestAppWorkspaceResultSummaryIsPreview(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Issues = []*productIssue{{ID: "i1", ContactID: "saki", Goal: "First issue", Status: issueStatusOpen, Executions: []*issueExecution{{ID: "e1", TaskID: "t1", Status: issueExecutionCompleted}}}}
	ct.SelectedIssueID = "i1"
	ct.Works = []*workItem{{TaskID: "t1", MessageID: "m1", State: workCompleted, Prompt: "prompt"}}
	ct.Messages = []*message{{ID: 1, Kind: msgContact, TaskID: "t1", MessageID: "m1", Text: "# Full result body"}}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Result preview: # Full result body") {
		t.Fatalf("workspace result summary is not an honest preview: %q", tr.Texts())
	}
	if tr.HasText("Result: # Full result body") {
		t.Fatalf("workspace result summary claims to be the full result: %q", tr.Texts())
	}
}

func TestIntegratedMarkdownPreview(t *testing.T) {
	dir := os.Getenv("SAKI_READER_INTEGRATED_DIR")
	if dir == "" {
		t.Skip("set SAKI_READER_INTEGRATED_DIR to write integrated headless previews")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "# 阅读\n\n段落、**重点**、[链接](https://example.com)。\n\n> 引用\n\n- 第一项\n- 第二项\n\n" +
		"```go\n" + "func long() {\n\tfmt.Println(\"wrapped at the reading width\")\n}\n" + "```\n\n" +
		"| 名称 | 说明 | 标记 | 状态 |\n| --- | --- | --- | --- |\n| 中文长内容需要换行 | " +
		strings.Repeat("abcdefghij", 18) + " | `code` | 完成 |\n"
	for _, scene := range []string{"chat", "details"} {
		for _, dark := range []bool{false, true} {
			theme := "light"
			if dark {
				theme = "dark"
			}
			for _, size := range []struct {
				name          string
				width, height int
			}{{"1040x720", 1040, 720}, {"720x640", 720, 640}} {
				a, ct := workspaceTestApp(t)
				a.windowWidth = size.width
				a.workspaceOpen = scene == "details"
				ct.Draft = "Draft kept while reading"
				ct.Messages = []*message{
					{ID: 1, Kind: msgUser, Text: "# literal user text"},
					{ID: 2, Kind: msgContact, Text: md},
				}
				if scene == "details" {
					ct.Works = []*workItem{{
						TaskID: "t1", State: workRunning,
						Tools: []*workTool{{
							CallID: "k1", Name: "read", Args: `{"path":"README.md"}`,
							Output: strings.Repeat("received line\n", 80) + "# tail", Started: true, Finished: true, Expanded: true,
						}},
					}}
					ct.ActiveTaskID, ct.LastTaskID = "t1", "t1"
				}
				tr := ui.NewTester(a.view, size.width, size.height)
				if dark {
					tr.SetDark(true)
				}
				tr.Frame()
				if scene == "chat" {
					ct.list.ScrollTo(0, ui.Start)
					tr.Frame()
				}
				writePreviewPNG(t, filepath.Join(dir,
					fmt.Sprintf("integrated-%s-%s-%s.png", scene, theme, size.name)), tr.Image())
			}
		}
	}
}
