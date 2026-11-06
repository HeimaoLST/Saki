package main

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"

	"saki/desktop/locales"
)

// markdownReaderOptions configures markdownReader. The source is the only
// content it reads: no app, contact or runtime state is involved.
type markdownReaderOptions struct {
	// OpenLocalFile receives only explicit path-link activations; the caller owns the source directory and errors.
	OpenLocalFile func(destination string) error
	// PlainText shows source literally and never parses it as Markdown.
	PlainText bool
	// Streaming shows source as a reply still being written.
	Streaming bool
	// CopyLabel is the accessible name and idle tooltip of the icon-only
	// full-source copy control, as "Copy message" or "Copy received".
	CopyLabel string
	// CopyText writes text and reports whether it stuck. When nil,
	// markdownReader writes the native clipboard on the UI thread and
	// reads it back.
	CopyText func(text string) error
	// OnCopyResult receives every full-source and code-copy result.
	OnCopyResult func(err error)
	// CopyHandle binds the full-source copy control, not code-block copies.
	CopyHandle *ui.Handle
	// CodeCopyHandles binds code-block copy controls by block index.
	CodeCopyHandles map[int]*ui.Handle
	// Theme overrides the Markdown foreground roles, as the workspace
	// surface requires.
	Theme *markdown.Theme
	// MessageBubble places the source copy in an external gutter and paints
	// the reader on a bubble surface. Nil keeps the compact reader layout.
	MessageBubble *markdownMessageBubble
	// LinkScope is stable identity for this Markdown document; LinkFocus is
	// the app-owned FocusBind field for its inline links. Both are optional.
	LinkScope string
	LinkFocus *markdownLinkTarget
	// LinkReturn and LinkReturnSeen report whether this document contains the
	// exact link a Settings return is waiting for; the caller decides once,
	// after the workbench build, whether to consume or cancel that request.
	LinkReturn     *markdownLinkTarget
	LinkReturnSeen *bool
}

var errClipboardReadback = errors.New("clipboard readback did not match")

var (
	markdownCopyIcon       = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>`))
	markdownCopiedIcon     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>`))
	markdownCopyFailedIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>`))
)

const (
	readerCopiedFor     = 2 * time.Second
	readerCopyFailedFor = 4 * time.Second
)

type markdownMessageBubble struct {
	Background     ui.Color
	PaddingX       float32
	PaddingY       float32
	Radius         float32
	ContextualCopy bool
	Header         func(*ui.Context)
	Footer         func(*ui.Context)
	BodyHandle     *ui.Handle
}

type markdownReaderState struct {
	opts markdown.Options

	copyText        func(string) error
	onCopyResult    func(error)
	streaming       bool
	codeCopyHandles map[int]*ui.Handle

	sourceFeedback readerCopyFeedback
	codeFeedback   map[int]*readerCopyFeedback

	linkError  error
	linkSource string
	linkScope  string
	ready      bool
}

type readerCopyFeedback struct {
	at  time.Time
	err error
}

func (f *readerCopyFeedback) show(now time.Time) (visible, failed bool) {
	if f.at.IsZero() {
		return false, false
	}
	d := readerCopiedFor
	if f.err != nil {
		d = readerCopyFailedFor
	}
	if now.Sub(f.at) >= d {
		return false, false
	}
	return true, f.err != nil
}

// markdownReader shows source as finished or streaming Markdown with one
// copy control for the exact source and one per code block. It reuses the
// pinned Markdown view, so selection, links, parsing and streaming keep
// their established behavior.
//
//	markdownReader(c, msg.text, markdownReaderOptions{
//		Streaming: !msg.done,
//		CopyLabel: "Copy message",
//	})
func markdownReader(c *ui.Context, source string, options markdownReaderOptions) ui.Element {
	var root ui.Element
	if options.MessageBubble == nil {
		root = ui.Column(c).AlignSelf(ui.Stretch).LineHeight(1.5)
	} else {
		root = ui.Row(c).AlignItems(ui.Start).Gap(4).Shrink(1).LineHeight(1.5)
	}
	st := ui.Local(root, markdownReaderStateKey{}, func() markdownReaderState {
		return markdownReaderState{codeFeedback: map[int]*readerCopyFeedback{}}
	})
	st.copyText = options.CopyText
	st.onCopyResult = options.OnCopyResult
	st.streaming = options.Streaming
	st.codeCopyHandles = options.CodeCopyHandles
	if !st.ready {
		st.ready = true
		st.opts.WrapCode = true
		st.opts.Reveal = &markdown.Reveal{Unit: markdown.AtOnce, Fade: -1}
		st.opts.CodeBlock = func(c *ui.Context, b *markdown.CodeBlockView) {
			markdownReaderCodeBlock(c, b, st)
		}
		st.opts.Block = func(c *ui.Context, d *parser.Document, i int) bool {
			if d.Block(i).Kind != parser.Table {
				return false
			}
			markdownReaderTable(c, d, i, markdownReaderTheme(c, st.opts.Theme))
			return true
		}
	}
	if st.linkSource != source || st.linkScope != options.LinkScope {
		st.linkError = nil
		st.opts.Anchor = ""
		st.linkSource, st.linkScope = source, options.LinkScope
	}
	st.opts.OnLink = nil
	if options.OpenLocalFile != nil {
		st.opts.OnLink = func(dest string) {
			st.linkError = nil
			switch {
			case strings.HasPrefix(dest, "#"):
				st.opts.Anchor = strings.TrimPrefix(dest, "#")
				st.opts.AnchorVersion++
			case ui.SafeToOpen(dest):
				c.OpenURL(dest)
			case !strings.ContainsAny(dest, "\\\x00") && !strings.HasPrefix(dest, "//"):
				if i := strings.IndexAny(dest, ":/?#"); i < 0 || dest[i] != ':' {
					st.linkError = options.OpenLocalFile(dest)
				}
			}
		}
	}
	st.opts.Theme = normalizedMarkdownTheme(options.Theme)
	st.opts.LinkElement = nil
	if !options.PlainText && options.LinkScope != "" && options.LinkFocus != nil {
		scope, focus, returnTarget, returnSeen := options.LinkScope, options.LinkFocus, options.LinkReturn, options.LinkReturnSeen
		st.opts.LinkElement = func(c *ui.Context, block, inline int, dest string, element ui.Element) {
			value := markdownLinkTarget{Scope: scope, Block: block, Inline: inline, Dest: dest}
			element.FocusBind(focus, value)
			if returnTarget != nil && *returnTarget == value {
				element.Focus()
				if returnSeen != nil {
					*returnSeen = true
				}
			}
		}
	}

	label := options.CopyLabel
	if label == "" {
		label = locales.In(c.Locale()).CopyMessageLabel()
	}

	drawBody := func(c *ui.Context) {
		switch {
		case options.PlainText:
			text := ui.Text(c, source).Selectable().LineHeight(1.5)
			if options.MessageBubble != nil && options.MessageBubble.ContextualCopy {
				text.ContextMenu(func(m *ui.Menu) {
					if m.Item(label).Chosen() {
						st.copySource(c, c.Now(), source, &st.sourceFeedback)
					}
					m.Separator()
					m.EditItems()
				})
			}
		case options.Streaming:
			markdown.Stream(c, source, &st.opts)
		default:
			markdown.View(c, source, &st.opts)
		}
		if st.linkError != nil {
			ui.Text(c, st.linkError.Error()).FontSize(typeAction).LineHeight(1.4).TextColor(c.Theme().Danger)
		}
	}
	if options.MessageBubble == nil {
		root.Children(func() {
			st.sourceCopyButton(c, c.Now(), label, func() string { return source }, &st.sourceFeedback, options.CopyHandle, false, nil)
			ui.Column(c).Padding(0, 32, 0, 0).Children(func() { drawBody(c) })
		})
		return root
	}
	bubble := options.MessageBubble
	root.Children(func() {
		body := ui.Column(c).Grow(1).MinWidth(0).Padding(bubble.PaddingY, bubble.PaddingX).
			Radius(bubble.Radius).Background(bubble.Background).Gap(4)
		if bubble.BodyHandle != nil {
			body.Bind(bubble.BodyHandle)
		}
		body.Children(func() {
			if bubble.Header != nil {
				bubble.Header(c)
			}
			drawBody(c)
			if bubble.Footer != nil {
				bubble.Footer(c)
			}
		})
		reveal := func() bool {
			if !bubble.ContextualCopy {
				return true
			}
			visible, _ := st.sourceFeedback.show(c.Now())
			return root.Hovered() || root.FocusWithin() || visible
		}
		st.sourceCopyButton(c, c.Now(), label, func() string { return source }, &st.sourceFeedback, options.CopyHandle, true, reveal)
	})
	return root
}

type markdownReaderStateKey struct{}

func markdownReaderCodeBlock(c *ui.Context, b *markdown.CodeBlockView, st *markdownReaderState) {
	th := markdownReaderTheme(c, st.opts.Theme)
	em := func(v float32) float32 { return v * th.FontSize }

	box := ui.Column(c).
		Direction(ui.LeftToRight).
		Background(th.CodeBlockBackground).
		Border(1, th.CodeBlockBorder).
		Radius(th.CodeBlockRadius).
		AlignSelf(ui.Stretch).
		Clip().
		Key("code-" + strconv.Itoa(b.Index))
	box.Role(ui.RoleGroup).Label(locales.In(c.Locale()).MarkdownCodeGroup())
	box.Children(func() {
		head := ui.Row(c).AlignItems(ui.Center).Padding(em(0.35), em(0.4), 0, em(th.CodeBlockPadding)).Key("code-head")
		head.Children(func() {
			if b.Language != "" {
				ui.Text(c, b.Language).FontSize(typeMeta).TextColor(th.Muted).Unselectable()
			}
			ui.Spacer(c)
			accessible := locales.In(c.Locale()).CopyCodeLabel()
			if st.streaming && b.Open {
				accessible = locales.In(c.Locale()).CopyReceivedCodeLabel()
			}
			// b.Code returns the fenced block's content without its fences,
			// with each line ending in LF as CommonMark normalizes it. The
			// full-source control still copies the original source bytes.
			var handle *ui.Handle
			if st.codeCopyHandles != nil {
				handle = st.codeCopyHandles[b.Index]
				if handle == nil {
					handle = &ui.Handle{}
					st.codeCopyHandles[b.Index] = handle
				}
			}
			st.copyButton(c, c.Now(), accessible, locales.In(c.Locale()).CopyActionLabel(), b.Code, st.codeFeedbackFor(b.Index), th, handle)
		})
		b.Body(c)
	})
}

// markdownReaderTable wraps a parsed table into the available width instead of
// the pinned renderer's horizontal scroll. Rows, headers, alignment and every
// cell stay, with raw cell source so Markdown syntax and link destinations are
// still inspectable. The table role carries the plain-text description; the
// pinned inline renderer is unexported, and reimplementing it here would be a
// second renderer.
func markdownReaderTable(c *ui.Context, d *parser.Document, i int, th markdown.Theme) {
	b := d.Block(i)
	padX, padY := th.TableCellPadding[0]*th.FontSize, th.TableCellPadding[1]*th.FontSize
	wrap := ui.Column(c).AlignSelf(ui.Stretch)
	wrap.Children(func() {
		table := ui.Column(c).AlignSelf(ui.Stretch).Border(1, th.TableBorder).Radius(th.FontSize * 0.3).Clip().Role(ui.RoleTable).Label(locales.In(c.Locale()).MarkdownTableGroup())
		table.Children(func() {
			n := 0
			for r := range d.Children(i) {
				row := d.Block(r)
				rowEl := ui.Row(c).AlignSelf(ui.Stretch).AlignItems(ui.Stretch).Role(ui.RoleRow)
				if row.Header {
					rowEl.Background(th.TableHeader)
				} else if n%2 == 1 && th.TableStripe.A > 0 {
					rowEl.Background(th.TableStripe)
				}
				if n > 0 {
					rowEl.BorderWidth(0.5, 0, 0, 0).BorderColor(th.TableBorder)
				}
				header := row.Header
				rowEl.Children(func() {
					k := 0
					cellAt := func(cell, index int) {
						role := ui.RoleCell
						weight := 0
						if header {
							role = ui.RoleColumnHeader
							weight = th.StrongWeight
						}
						box := ui.Column(c).Grow(1).Basis(0).Shrink(1).MinWidth(0).Padding(padY, padX).Role(role)
						if index > 0 {
							box.BorderWidth(0, 0, 0, 0.5).BorderColor(th.TableBorder)
						}
						cellBlock := d.Block(cell)
						text := d.Source()[cellBlock.Start:cellBlock.End]
						align := ui.Start
						switch b.Align[index] {
						case parser.AlignCenter:
							align = ui.Center
						case parser.AlignRight:
							align = ui.End
						}
						box.Children(func() {
							ui.Text(c, text).FontSize(th.FontSize).LineHeight(th.LineHeight).TextColor(th.Text).FontWeight(weight).TextAlign(align)
						})
					}
					for cell := range d.Children(r) {
						cellAt(cell, k)
						k++
					}
				})
				n++
			}
		})
	})
}

func (st *markdownReaderState) codeFeedbackFor(index int) *readerCopyFeedback {
	fb := st.codeFeedback[index]
	if fb == nil {
		fb = &readerCopyFeedback{}
		st.codeFeedback[index] = fb
	}
	return fb
}

func (st *markdownReaderState) sourceCopyButton(c *ui.Context, now time.Time, accessible string, text func() string, fb *readerCopyFeedback, handle *ui.Handle, outside bool, reveal func() bool) ui.Element {
	feedbackVisible, failed := fb.show(now)
	btn := ui.Button(c, "").Label(accessible)
	if handle != nil {
		btn.Bind(handle)
	}
	theme := c.Theme()
	btn.Key("source-copy").Size(32, 32).Padding(0).Gap(0).Radius(6).
		Background(ui.Transparent).BorderWidth(0)
	if !outside {
		btn.Absolute().Top(0).Right(0)
	}
	if btn.Pressed() {
		btn.Background(theme.SurfacePressed)
	} else if btn.Hovered() {
		btn.Background(theme.SurfaceHover)
	}
	color := markdownReaderTheme(c, st.opts.Theme).Muted
	icon := markdownCopyIcon
	tooltip := accessible
	if feedbackVisible {
		if failed {
			color, icon = theme.Danger, markdownCopyFailedIcon
			tooltip = locales.In(c.Locale()).CopyFailed(fb.err.Error())
		} else {
			color, icon = theme.Success, markdownCopiedIcon
			tooltip = locales.In(c.Locale()).Copied()
		}
	}
	appTooltip(c, btn, tooltip)
	showIcon := reveal == nil || reveal() || btn.Hovered() || btn.FocusVisible()
	if showIcon {
		btn.Children(func() {
			ui.Icon(c, icon).Size(16, 16).TextColor(color)
		})
	}
	if btn.Clicked() {
		st.copySource(c, now, text(), fb)
	}
	return btn
}

func (st *markdownReaderState) copySource(c *ui.Context, now time.Time, text string, fb *readerCopyFeedback) {
	fb.at = now
	fb.err = st.performCopy(c, text)
	if st.onCopyResult != nil {
		st.onCopyResult(fb.err)
	}
	if fb.err != nil {
		c.After(readerCopyFailedFor)
	} else {
		c.After(readerCopiedFor)
	}
}

func (st *markdownReaderState) copyButton(c *ui.Context, now time.Time, accessible, idle string, text func() string, fb *readerCopyFeedback, th markdown.Theme, handle *ui.Handle) ui.Element {
	row := ui.Row(c).Shrink(0).Key("copy-button")
	row.Children(func() {
		color, icon := th.Muted, markdownCopyIcon
		if visibleNow, failed := fb.show(now); visibleNow {
			if failed {
				color, icon = c.Theme().Danger, markdownCopyFailedIcon
			} else {
				color, icon = c.Theme().Success, markdownCopiedIcon
			}
		}
		btn := ui.ButtonBase(c).Label(accessible)
		if handle != nil {
			btn.Bind(handle)
		}
		theme := c.Theme()
		btn.Size(32, 32).Padding(0).Radius(6).BorderWidth(0).Background(ui.Transparent)
		if btn.Pressed() {
			btn.Background(theme.SurfacePressed)
		} else if btn.Hovered() {
			btn.Background(theme.SurfaceHover)
		}
		tooltip := idle
		if fb.err != nil {
			if visibleNow, failed := fb.show(now); visibleNow && failed {
				tooltip = locales.In(c.Locale()).CopyFailed(fb.err.Error())
			}
		}
		appTooltip(c, btn, tooltip)
		btn.Children(func() {
			ui.Icon(c, icon).Size(16, 16).TextColor(color)
		})
		if btn.Clicked() {
			st.copySource(c, now, text(), fb)
		}
	})
	return row
}

func (st *markdownReaderState) performCopy(c *ui.Context, text string) error {
	if st.copyText != nil {
		return st.copyText(text)
	}
	return writeClipboardText(text, c.Services().WriteClipboard, c.Services().ReadClipboard)
}

func writeClipboardText(text string, write func(string), read func() string) error {
	write(text)
	if read() != text {
		return errClipboardReadback
	}
	return nil
}

// normalizedMarkdownTheme keeps rendered Markdown inside the app's four-size
// scale. H1 maps to 16; H2-H6 map to 14 and use weight/spacing; code uses 13.
// Source text
// and user-provided Markdown are never rewritten.
func normalizedMarkdownTheme(override *markdown.Theme) *markdown.Theme {
	// Start with the complete caller override and let the framework resolve all
	// fields it left zero. Only constrain the app-controlled size scale here.
	th := markdown.Theme{}
	if override != nil {
		th = *override
	}
	th.FontSize = typeBody
	th.LineHeight = 1.5
	th.CodeLineHeight = 1.5
	headingSizes := [6]float32{16, 14, 14, 14, 14, 14}
	for i := range th.Headings {
		th.Headings[i].Size = headingSizes[i] / th.FontSize
		if th.Headings[i].Weight == 0 {
			// Preserve an explicit caller weight; otherwise keep semantic hierarchy
			// with the app's restrained 16/14 scale.
			th.Headings[i].Weight = 500
			if i == 0 {
				th.Headings[i].Weight = 600
			}
		}
	}
	th.CodeSize = typeAction / th.FontSize
	return &th
}

// markdownReaderTheme resolves only the roles this reader draws itself:
// the code frame and its header. The Markdown view resolves its own full
// theme from the same override.
func markdownReaderTheme(c *ui.Context, override *markdown.Theme) markdown.Theme {
	th := *markdown.DefaultTheme(c.Theme())
	if override == nil {
		return th
	}
	if override.FontSize != 0 {
		th.FontSize = override.FontSize
	}
	if override.LineHeight != 0 {
		th.LineHeight = override.LineHeight
	}
	if override.Text != (markdown.Color{}) {
		th.Text = override.Text
	}
	if override.Muted != (markdown.Color{}) {
		th.Muted = override.Muted
	}
	if override.CodeBlockBackground != (markdown.Color{}) {
		th.CodeBlockBackground = override.CodeBlockBackground
	}
	if override.CodeBlockBorder != (markdown.Color{}) {
		th.CodeBlockBorder = override.CodeBlockBorder
	}
	if override.CodeBlockRadius != 0 {
		th.CodeBlockRadius = override.CodeBlockRadius
	}
	if override.CodeBlockPadding != 0 {
		th.CodeBlockPadding = override.CodeBlockPadding
	}
	return th
}
