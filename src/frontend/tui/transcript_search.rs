//! Rendered-transcript search (F3).
//!
//! Matching runs on the exact rendered display rows, not on raw record text and
//! not on `VisualLine::source_start` (which is only a reading anchor). Each hit
//! stores a `RowKey` plus a display-cell range, so highlighting never guesses
//! raw byte positions. Matches that would span a soft-wrap boundary are not
//! reported: joining wrapped rows would need a renderer-wide schema change.

use ratatui::{
    Frame,
    layout::Rect,
    style::{Modifier, Style},
    text::{Line, Span},
    widgets::Paragraph,
};
use unicode_segmentation::UnicodeSegmentation;
use unicode_width::UnicodeWidthStr;

use crate::frontend::tui::selection::{RowKey, VisualAnchor};
use crate::frontend::tui::state::{Anchor, App, Focus};
use crate::frontend::tui::view::record_layout;

/// Selection marker injected by the renderer on the selected record's first row.
const SELECTION_MARKER: &str = "▌";

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct SavedView {
    follow: bool,
    anchor: Option<Anchor>,
    visual_anchor: Option<VisualAnchor>,
    focus: Focus,
}

/// One rendered-text hit: logical row plus a display-cell range and the row's
/// source offset for the width-independent reading-anchor fallback.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct SearchMatch {
    pub key: RowKey,
    pub start: usize,
    pub end: usize,
    pub offset: usize,
}

/// One active transcript search. It never touches the composer or input history.
pub struct TranscriptSearch {
    pub query: String,
    pub matches: Vec<SearchMatch>,
    pub selected: usize,
    width: usize,
    built_query: String,
    keys: Vec<usize>,
    /// Monotonic record/projection revision the hits were built from.
    revision: u64,
    saved: SavedView,
}

pub fn active(app: &App) -> bool {
    app.transcript_search.is_some()
}

/// Opens the search from a normal input/transcript surface, preserving the
/// current reading view so Esc restores it exactly.
pub fn open(app: &mut App) {
    if app.transcript_search.is_some() {
        return;
    }
    app.text_selection.clear();
    let saved = SavedView {
        follow: app.follow,
        anchor: app.anchor,
        visual_anchor: app.visual_anchor,
        focus: app.focus,
    };
    app.transcript_search = Some(TranscriptSearch {
        query: String::new(),
        matches: Vec::new(),
        selected: 0,
        width: app.last_width.max(1),
        built_query: String::new(),
        keys: Vec::new(),
        revision: app.transcript_revision,
        saved,
    });
}

/// Closes the search and restores the exact follow/source/visual anchor/focus
/// captured by [`open`]. The draft, cursor, images and undo history are never
/// part of this state.
pub fn cancel(app: &mut App) {
    let Some(search) = app.transcript_search.take() else {
        return;
    };
    app.follow = search.saved.follow;
    app.anchor = search.saved.anchor;
    app.visual_anchor = search.saved.visual_anchor;
    app.focus = search.saved.focus;
}

pub fn push_char(app: &mut App, character: char) {
    if let Some(search) = &mut app.transcript_search {
        search.query.push(character);
    }
    rebuild(app);
}

/// Paste text into the query; the single-line field folds newlines.
pub fn push_str(app: &mut App, text: &str) {
    if let Some(search) = &mut app.transcript_search {
        search.query.push_str(&text.replace(['\r', '\n'], " "));
    }
    rebuild(app);
}

/// Grapheme-safe query Backspace.
pub fn backspace(app: &mut App) {
    if let Some(search) = &mut app.transcript_search
        && let Some((index, _)) = search.query.grapheme_indices(true).next_back()
    {
        search.query.truncate(index);
    }
    rebuild(app);
}

/// Moves to the next (`forward`) or previous match, wrapping at the ends.
pub fn step(app: &mut App, forward: bool) {
    let Some(search) = &app.transcript_search else {
        return;
    };
    let total = search.matches.len();
    if total == 0 {
        return;
    }
    let selected = if forward {
        (search.selected + 1) % total
    } else {
        (search.selected + total - 1) % total
    };
    if let Some(search) = &mut app.transcript_search {
        search.selected = selected;
    }
    jump_to_selected(app);
}

/// Rebuilds cached hits only when the query, width, visible projection or the
/// owned layout caches actually changed. Called once per draw while active.
pub fn refresh(app: &mut App, width: usize) {
    let Some(search) = &app.transcript_search else {
        return;
    };
    let keys = app.visible_record_keys();
    if search.query.is_empty() {
        // Nothing is indexed for an empty query; just track the projection so
        // typing starts from the current records without laying anything out.
        let stale = search.width != width
            || !search.keys.is_empty()
            || search.revision != app.transcript_revision
            || search.built_query != search.query;
        if stale && let Some(search) = &mut app.transcript_search {
            search.width = width;
            search.keys = keys;
            search.revision = app.transcript_revision;
            search.built_query = search.query.clone();
            search.matches.clear();
            search.selected = 0;
        }
        return;
    }
    if search.width == width
        && search.query == search.built_query
        && search.keys == keys
        && search.revision == app.transcript_revision
    {
        return;
    }
    rebuild_with(app, width, keys);
}

fn rebuild(app: &mut App) {
    let width = app.last_width.max(1);
    let keys = app.visible_record_keys();
    rebuild_with(app, width, keys);
}

fn rebuild_with(app: &mut App, width: usize, keys: Vec<usize>) {
    let Some(search) = &app.transcript_search else {
        return;
    };
    let query = search.query.clone();
    let previous = search.matches.get(search.selected).copied();
    let mut matches = Vec::new();
    if !query.is_empty() {
        let needle = query.to_lowercase();
        for record in &keys {
            let layout = record_layout(app, *record, width);
            for (line, visual) in layout.iter().enumerate() {
                for (start, end) in line_matches(&visual.line, &needle) {
                    matches.push(SearchMatch {
                        key: RowKey {
                            record: *record,
                            line,
                        },
                        start,
                        end,
                        offset: visual.source_start,
                    });
                }
            }
        }
    }
    let selected = previous
        .and_then(|previous| matches.iter().position(|item| *item == previous))
        .unwrap_or(0);
    let revision = app.transcript_revision;
    if let Some(search) = &mut app.transcript_search {
        search.matches = matches;
        search.selected = if search.matches.is_empty() {
            0
        } else {
            selected.min(search.matches.len() - 1)
        };
        search.built_query = query;
        search.width = width;
        search.keys = keys;
        search.revision = revision;
    }
    jump_to_selected(app);
}

fn jump_to_selected(app: &mut App) {
    let Some(search) = &app.transcript_search else {
        return;
    };
    let Some(found) = search.matches.get(search.selected).copied() else {
        return;
    };
    app.follow = false;
    app.visual_anchor = Some(VisualAnchor {
        key: found.key,
        width: search.width,
    });
    app.anchor = Some(Anchor {
        record: found.key.record,
        offset: found.offset,
    });
}

/// Case-insensitive cell ranges for `needle` on one rendered row. The optional
/// selection marker is kept as a zero-searchable cell so reported ranges stay
/// aligned with the row actually drawn.
pub(crate) fn line_matches(line: &Line<'_>, needle: &str) -> Vec<(usize, usize)> {
    if needle.is_empty() {
        return Vec::new();
    }
    let text: String = line
        .spans
        .iter()
        .map(|span| span.content.as_ref())
        .collect();
    let mut haystack = String::new();
    // (cell, width, lowercase_start, lowercase_end, searchable)
    let mut pieces: Vec<(usize, usize, usize, usize, bool)> = Vec::new();
    let mut cell = 0usize;
    for grapheme in text.graphemes(true) {
        let width = UnicodeWidthStr::width(grapheme);
        let searchable = grapheme != SELECTION_MARKER;
        let lower = if searchable {
            grapheme.to_lowercase()
        } else {
            String::new()
        };
        let start = haystack.len();
        haystack.push_str(&lower);
        pieces.push((cell, width, start, haystack.len(), searchable));
        cell += width;
    }
    let mut ranges = Vec::new();
    for (start, _) in haystack.match_indices(needle) {
        let end = start + needle.len();
        let mut first = None;
        let mut last = None;
        for (cell, width, lower_start, lower_end, searchable) in &pieces {
            if *searchable && *lower_end > start && *lower_start < end {
                first.get_or_insert(*cell);
                last = Some(*cell + *width);
            }
        }
        if let (Some(first), Some(last)) = (first, last)
            && last > first
        {
            ranges.push((first, last));
        }
    }
    ranges
}

/// Applies highlight styles to one rendered row in place. Content is untouched,
/// so copying source never sees injected text.
pub fn highlight_line(app: &App, key: RowKey, line: &mut Line<'static>) {
    let Some(search) = &app.transcript_search else {
        return;
    };
    if search.matches.is_empty() {
        return;
    }
    let theme = app.theme;
    let mut spans = Vec::new();
    let mut cell = 0usize;
    for span in &line.spans {
        let base = span.style;
        let mut buffer = String::new();
        let mut class: Option<bool> = None;
        for grapheme in span.content.graphemes(true) {
            let width = UnicodeWidthStr::width(grapheme);
            let mut hit = None;
            for (index, item) in search.matches.iter().enumerate() {
                if item.key != key {
                    continue;
                }
                let intersects = if width == 0 {
                    cell >= item.start && cell < item.end
                } else {
                    cell < item.end && cell + width > item.start
                };
                if intersects {
                    hit = Some(index == search.selected);
                    break;
                }
            }
            if hit != class {
                flush(&mut spans, &buffer, base, class, theme);
                buffer.clear();
                class = hit;
            }
            buffer.push_str(grapheme);
            cell += width;
        }
        flush(&mut spans, &buffer, base, class, theme);
    }
    line.spans = spans;
}

fn flush(
    spans: &mut Vec<Span<'static>>,
    content: &str,
    base: Style,
    class: Option<bool>,
    theme: crate::frontend::tui::theme::Theme,
) {
    if content.is_empty() {
        return;
    }
    let style = match class {
        None => base,
        Some(current) => {
            let mut style = base.fg(theme.accent).add_modifier(Modifier::UNDERLINED);
            if current {
                style = style.add_modifier(Modifier::REVERSED | Modifier::BOLD);
            }
            style
        }
    };
    spans.push(Span::styled(content.to_owned(), style));
}

/// Compact strip shown only while the search is active.
pub fn draw(frame: &mut Frame, app: &App, area: Rect) {
    let Some(search) = &app.transcript_search else {
        return;
    };
    let theme = app.theme;
    let total = search.matches.len();
    let status = if search.query.is_empty() {
        "type to search".to_owned()
    } else if total == 0 {
        "No matches".to_owned()
    } else {
        format!("{}/{}", search.selected + 1, total)
    };
    let mut spans = vec![
        Span::styled(
            "Search: ",
            Style::default()
                .fg(theme.accent)
                .add_modifier(Modifier::BOLD),
        ),
        Span::styled(search.query.clone(), Style::default().fg(theme.text)),
        Span::styled(SELECTION_MARKER, Style::default().fg(theme.accent)),
        Span::raw("  "),
        Span::styled(status, Style::default().fg(theme.secondary)),
    ];
    let hint = "  F3/Enter next · Shift+F3/Up prev · Esc close";
    let used: usize = spans
        .iter()
        .map(|span| UnicodeWidthStr::width(span.content.as_ref()))
        .sum();
    if (area.width as usize) > used + hint.len() {
        spans.push(Span::styled(hint, Style::default().fg(theme.secondary)));
    }
    frame.render_widget(Paragraph::new(Line::from(spans)), area);
}
