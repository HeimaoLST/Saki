package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func composerTextArea(t *testing.T, tr *ui.Tester) ui.Rect {
	t.Helper()
	r, ok := tr.Find("Composer for Saki")
	if !ok || r.W <= 20 || r.H <= 20 {
		t.Fatalf("composer bounds = %+v ok=%v", r, ok)
	}
	return r
}

func TestComposerPointerAndKeyboardSelection(t *testing.T) {
	tests := []struct {
		name  string
		value string
		line  bool
	}{
		{"single", "中文内容 abcdefghijklmnopqrstuvwxyz", false},
		{"multiline", "第一行中文内容\n第二行 more text", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t)
			ct := a.selectedContact()
			ct.Draft = tc.value
			beforeMsgs := len(ct.Messages)
			tr := ui.NewTester(a.view, 1040, 720)
			tr.Frame()
			r := composerTextArea(t, tr)
			y := r.Y + 12
			if tc.line {
				y = r.Y + 16
			}
			start := r.X + 20
			end := r.X + 180
			for _, reverse := range []bool{false, true} {
				ct.Draft = tc.value
				tr.SetClipboard("")
				x1, x2 := start, end
				if reverse {
					x1, x2 = end, start
				}
				tr.Press(x1, y)
				tr.Move(x2, y)
				tr.Release(x2, y)
				tr.Command("copy")
				got := tr.Clipboard()
				if got == "" || !strings.Contains(tc.value, got) || got == tc.value {
					t.Fatalf("%s reverse=%v pointer selection copy=%q draft=%q", tc.name, reverse, got, ct.Draft)
				}
				if ct.Draft != tc.value || len(ct.Messages) != beforeMsgs {
					t.Fatalf("%s reverse=%v changed draft/messages: %q/%d", tc.name, reverse, ct.Draft, len(ct.Messages))
				}
			}

			tr.SetClipboard("")
			if err := tr.Click("Composer for Saki"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			tr.Key(ui.Cmd, ui.KeyA)
			tr.Key(ui.Cmd, ui.KeyC)
			if got := tr.Clipboard(); got != tc.value {
				t.Fatalf("%s CmdA/CmdC copy=%q want=%q", tc.name, got, tc.value)
			}
			tr.Type("替换")
			tr.Frame()
			if ct.Draft != "替换" || len(ct.Messages) != beforeMsgs {
				t.Fatalf("%s keyboard replacement draft=%q messages=%d", tc.name, ct.Draft, len(ct.Messages))
			}
		})
	}
}

func TestComposerShiftArrowSelectsWithoutSending(t *testing.T) {
	a := testApp(t)
	ct := a.selectedContact()
	ct.Draft = "abc中文"
	beforeMsgs := len(ct.Messages)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Composer for Saki"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	tr.Key(ui.Shift, ui.KeyLeft)
	tr.Frame()
	tr.SetClipboard("")
	tr.Command("copy")
	got := tr.Clipboard()
	if got == "" || !strings.Contains(ct.Draft, got) || len(ct.Messages) != beforeMsgs {
		t.Fatalf("Shift+Left copy=%q draft=%q messages=%d", got, ct.Draft, len(ct.Messages))
	}
}

func TestComposerMultilinePasteDoesNotSend(t *testing.T) {
	a := testApp(t)
	ct := a.selectedContact()
	beforeMsgs := len(ct.Messages)
	payload := "第一行\n第二行\nthird line"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if err := tr.Click("Composer for Saki"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	tr.SetClipboard(payload)
	tr.Command("paste")
	tr.Frame()
	if ct.Draft != payload || len(ct.Messages) != beforeMsgs {
		t.Fatalf("multiline paste draft=%q messages=%d", ct.Draft, len(ct.Messages))
	}
}

func TestComposerPointerSelectionReplacement(t *testing.T) {
	a := testApp(t)
	ct := a.selectedContact()
	const value = "中文内容 abcdefghijklmnopqrstuvwxyz"
	ct.Draft = value
	beforeMsgs := len(ct.Messages)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	r := composerTextArea(t, tr)
	x, y := r.X+20, r.Y+12
	tr.Press(x, y)
	tr.Move(x+180, y)
	tr.Release(x+180, y)
	tr.SetClipboard("")
	tr.Command("copy")
	selected := tr.Clipboard()
	if selected == "" || !strings.Contains(value, selected) {
		t.Fatalf("pointer selection copy=%q", selected)
	}
	tr.Type("替换")
	tr.Frame()
	want := strings.Replace(value, selected, "替换", 1)
	if ct.Draft != want || len(ct.Messages) != beforeMsgs {
		t.Fatalf("pointer replacement draft=%q want=%q messages=%d", ct.Draft, want, len(ct.Messages))
	}
}
