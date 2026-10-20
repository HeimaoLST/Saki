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
use crate::frontend::tui::textwrap;
use crate::frontend::tui::view::record_layout;

/// Cursor glyph in the compact search strip (not stripped from record text).
const CURSOR: &str = "▌";

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

/// Case-insensitive cell ranges for `needle` on one rendered row.
///
/// The haystack is the whole rendered text lowercased, matching the query's
/// whole-string lowercase (Greek final sigma included). Per-grapheme lowercase
/// pieces only supply byte boundaries for mapping a hit back to original cells;
/// expansion such as U+0130 keeps its correct original width.
pub(crate) fn line_matches(line: &Line<'_>, needle: &str) -> Vec<(usize, usize)> {
    if needle.is_empty() {
        return Vec::new();
    }
    let text: String = line
        .spans
        .iter()
        .map(|span| span.content.as_ref())
        .collect();
    let whole = text.to_lowercase();
    let mut joined = String::new();
    // (cell_start, width, lowercase_byte_start, lowercase_byte_end)
    let mut pieces: Vec<(usize, usize, usize, usize)> = Vec::new();
    let mut cell = 0usize;
    for grapheme in text.graphemes(true) {
        let width = UnicodeWidthStr::width(grapheme);
        let start = joined.len();
        joined.push_str(&grapheme.to_lowercase());
        pieces.push((cell, width, start, joined.len()));
        cell += width;
    }
    // Context-sensitive casing can differ inside a piece but keeps its byte
    // length; a length mismatch is the only case where boundaries are unsafe.
    let haystack = if joined.len() == whole.len() {
        whole.as_str()
    } else {
        joined.as_str()
    };
    let mut ranges = Vec::new();
    for (start, _) in haystack.match_indices(needle) {
        let end = start + needle.len();
        let mut first = None;
        let mut last = None;
        for (cell, width, lower_start, lower_end) in &pieces {
            if *lower_end > start && *lower_start < end {
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

/// Applies highlight styles to one rendered row in place. Hits are ordered by
/// `RowKey`, so only this row's slice is visited: navigation cost is row cells
/// plus row hits, never the whole history per grapheme.
pub fn highlight_line(app: &App, key: RowKey, line: &mut Line<'static>) {
    let Some(search) = &app.transcript_search else {
        return;
    };
    if search.matches.is_empty() {
        return;
    }
    let lo = search.matches.partition_point(|item| item.key < key);
    let hi = lo + search.matches[lo..].partition_point(|item| item.key == key);
    if lo == hi {
        return;
    }
    let theme = app.theme;
    let mut spans = Vec::new();
    let mut cell = 0usize;
    let mut cursor = lo;
    for span in &line.spans {
        let base = span.style;
        let mut buffer = String::new();
        let mut class: Option<bool> = None;
        for grapheme in span.content.graphemes(true) {
            let width = UnicodeWidthStr::width(grapheme);
            let start = cell;
            let end = cell + width;
            // Ranges are sorted and non-overlapping; advance past finished ones.
            while cursor < hi && search.matches[cursor].end <= start {
                cursor += 1;
            }
            let hit = if cursor < hi {
                let item = &search.matches[cursor];
                let intersects = if width == 0 {
                    start >= item.start && start < item.end
                } else {
                    start < item.end && end > item.start
                };
                intersects.then_some(cursor == search.selected)
            } else {
                None
            };
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

/// Compact strip shown only while the search is active. The query cursor stays
/// visible at narrow widths by showing the query tail before the status.
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
    let label = if area.width as usize >= 10 {
        "Search: "
    } else {
        "S:"
    };
    let label_width = UnicodeWidthStr::width(label);
    let status_width = UnicodeWidthStr::width(status.as_str()) + 2;
    let room = (area.width as usize)
        .saturating_sub(label_width + status_width + 1)
        .max(1);
    let query_width = UnicodeWidthStr::width(search.query.as_str());
    let shown = if query_width <= room {
        search.query.clone()
    } else {
        textwrap::visible_suffix(&search.query, room)
    };
    let shown_width = UnicodeWidthStr::width(shown.as_str());
    let mut spans = vec![
        Span::styled(
            label,
            Style::default()
                .fg(theme.accent)
                .add_modifier(Modifier::BOLD),
        ),
        Span::styled(shown, Style::default().fg(theme.text)),
        Span::styled(CURSOR, Style::default().fg(theme.accent)),
    ];
    if (area.width as usize) > label_width + shown_width + 1 + status_width {
        spans.push(Span::raw("  "));
        spans.push(Span::styled(status, Style::default().fg(theme.secondary)));
        let hint = "  F3/Enter next · Shift+F3/Up prev · Esc close";
        let used: usize = spans
            .iter()
            .map(|span| UnicodeWidthStr::width(span.content.as_ref()))
            .sum();
        if (area.width as usize) > used + hint.len() {
            spans.push(Span::styled(hint, Style::default().fg(theme.secondary)));
        }
    }
    frame.render_widget(Paragraph::new(Line::from(spans)), area);
}
