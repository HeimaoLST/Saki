//! Path text completion for the composer (Tab).
//!
//! A small shell-like token lexer finds the path component under the cursor,
//! replacing only that component (plus a directory's existing separator). It
//! supports unquoted backslash escapes, double/single quotes and mid-component
//! cursors with a suffix. This is not shell expansion: no variables, globs or
//! command substitution. Directory listing is metadata-only and runs only on an
//! explicit Tab, never per frame.

use std::fs;
use std::path::{Path, PathBuf};

use ratatui::{
    Frame,
    layout::Rect,
    style::{Modifier, Style},
    text::{Line, Span},
    widgets::{Block, Borders, Clear, Paragraph},
};
use unicode_width::UnicodeWidthStr;

use crate::frontend::tui::editor::Editor;
use crate::frontend::tui::state::App;

/// Retained candidates; more matches are reported as "type to narrow".
const MAX_CANDIDATES: usize = 50;
/// Popup rows shown at once.
const VISIBLE_ROWS: usize = 8;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Quote {
    None,
    Double,
    Single,
}

struct Candidate {
    name: String,
    is_dir: bool,
}

/// One open completion popup. Spans are exact document byte ranges for the
/// component under the cursor from the last Tab.
pub struct PathCompletion {
    candidates: Vec<Candidate>,
    selected: usize,
    truncated: usize,
    start: usize,
    end: usize,
    quote: Quote,
    /// A separator already follows the component; the insertion must keep it.
    separator: bool,
}

struct Token {
    content_start: usize,
    empty_quote: Quote,
    /// (char, document start, document end, active quote)
    chars: Vec<(char, usize, usize, Quote)>,
}

fn token_at(text: &str, cursor: usize) -> Option<Token> {
    let line_start = text[..cursor].rfind('\n').map_or(0, |index| index + 1);
    let line_end = text[cursor..]
        .find('\n')
        .map_or(text.len(), |index| cursor + index);
    let mut index = line_start;
    while index < line_end {
        let mut character = text[index..].chars().next()?;
        while character == ' ' || character == '\t' {
            index += character.len_utf8();
            if index >= line_end {
                return None;
            }
            character = text[index..].chars().next()?;
        }
        let start = index;
        let mut quote = Quote::None;
        let mut empty_quote = Quote::None;
        let mut content_start = start;
        let mut chars = Vec::new();
        while index < line_end {
            let character = text[index..].chars().next()?;
            let width = character.len_utf8();
            match quote {
                Quote::Single => {
                    if character == '\'' {
                        quote = Quote::None;
                        index += width;
                    } else {
                        chars.push((character, index, index + width, Quote::Single));
                        index += width;
                    }
                }
                Quote::Double => {
                    if character == '\\' && index + width < line_end {
                        let next = text[index + width..].chars().next()?;
                        let next_width = next.len_utf8();
                        chars.push((next, index, index + width + next_width, Quote::Double));
                        index += width + next_width;
                    } else if character == '"' {
                        quote = Quote::None;
                        index += width;
                    } else {
                        chars.push((character, index, index + width, Quote::Double));
                        index += width;
                    }
                }
                Quote::None => {
                    if character == ' ' || character == '\t' {
                        break;
                    }
                    if character == '\\' && index + width < line_end {
                        let next = text[index + width..].chars().next()?;
                        let next_width = next.len_utf8();
                        chars.push((next, index, index + width + next_width, Quote::None));
                        index += width + next_width;
                    } else if character == '\'' {
                        empty_quote = Quote::Single;
                        content_start = index + width;
                        quote = Quote::Single;
                        index += width;
                    } else if character == '"' {
                        empty_quote = Quote::Double;
                        content_start = index + width;
                        quote = Quote::Double;
                        index += width;
                    } else {
                        if chars.is_empty() {
                            content_start = index;
                        }
                        chars.push((character, index, index + width, Quote::None));
                        index += width;
                    }
                }
            }
        }
        let token = Token {
            content_start: if chars.is_empty() {
                content_start
            } else {
                chars[0].1
            },
            empty_quote,
            chars,
        };
        if start <= cursor && cursor <= index {
            return Some(token);
        }
        if index == start {
            return None;
        }
    }
    None
}

struct Decoded {
    text: String,
    /// (decoded byte offset, document byte offset) at character boundaries.
    bounds: Vec<(usize, usize)>,
    modes: Vec<Quote>,
}

impl Decoded {
    fn doc_at(&self, decoded: usize) -> Option<usize> {
        self.bounds
            .iter()
            .find_map(|(position, doc)| (*position == decoded).then_some(*doc))
    }

    fn decoded_at(&self, doc: usize) -> usize {
        self.bounds
            .iter()
            .rev()
            .find_map(|(decoded, position)| (*position <= doc).then_some(*decoded))
            .unwrap_or(0)
    }
}

fn decode(token: &Token) -> Decoded {
    let mut text = String::new();
    let mut bounds = Vec::new();
    let mut modes = Vec::new();
    for (index, (character, doc_start, doc_end, mode)) in token.chars.iter().enumerate() {
        // Boundaries are character ends (and the first character start): the end
        // of a component maps before a closing quote, while a separator's start
        // maps after that quote.
        if index == 0 {
            bounds.push((0, *doc_start));
        }
        text.push(*character);
        modes.push(*mode);
        bounds.push((text.len(), *doc_end));
    }
    if token.chars.is_empty() {
        bounds.push((0, token.content_start));
    }
    Decoded {
        text,
        bounds,
        modes,
    }
}

/// Quote mode for one component, indexed by character (not decoded byte).
/// Mixed quoting inside the component is unsupported and returns `None`.
fn component_quote(decoded: &Decoded, token: &Token, start: usize, end: usize) -> Option<Quote> {
    let first = decoded.text[..start].chars().count();
    let last = decoded.text[..end].chars().count();
    if first < last {
        let mode = *decoded.modes.get(first)?;
        if decoded.modes[first..last].iter().all(|item| *item == mode) {
            Some(mode)
        } else {
            None
        }
    } else if decoded.modes.is_empty() {
        Some(token.empty_quote)
    } else {
        decoded
            .modes
            .get(first)
            .copied()
            .or_else(|| decoded.modes.last().copied())
            .or(Some(token.empty_quote))
    }
}

fn escape_name(name: &str, quote: Quote) -> String {
    let mut out = String::with_capacity(name.len() + 2);
    match quote {
        Quote::None => {
            for character in name.chars() {
                if character.is_whitespace() || matches!(character, '\\' | '\'' | '"' | '`' | '$') {
                    out.push('\\');
                }
                out.push(character);
            }
        }
        Quote::Double => {
            for character in name.chars() {
                if matches!(character, '\\' | '"' | '`' | '$') {
                    out.push('\\');
                }
                out.push(character);
            }
        }
        Quote::Single => {
            for character in name.chars() {
                if character == '\'' {
                    out.push_str("'\\''");
                } else {
                    out.push(character);
                }
            }
        }
    }
    out
}

fn base_for(prefix: &str, working_directory: &Path) -> Option<PathBuf> {
    if let Some(rest) = prefix.strip_prefix("~/") {
        let home = std::env::var_os("HOME")?;
        return Some(PathBuf::from(home).join(rest));
    }
    if prefix == "~" || prefix.starts_with('~') {
        return None;
    }
    if prefix.starts_with('/') {
        return Some(PathBuf::from(prefix));
    }
    Some(working_directory.join(prefix))
}

fn list_candidates(base: &Path, partial: &str, only_dirs: bool) -> (Vec<Candidate>, usize) {
    let dotfile = partial.starts_with('.');
    let needle = partial.to_lowercase();
    let mut matches = Vec::new();
    let Ok(entries) = fs::read_dir(base) else {
        return (matches, 0);
    };
    for entry in entries.flatten() {
        let Some(name) = entry.file_name().to_str().map(str::to_owned) else {
            continue;
        };
        if !dotfile && name.starts_with('.') {
            continue;
        }
        if !name.to_lowercase().starts_with(&needle) {
            continue;
        }
        let is_dir = entry.metadata().map(|meta| meta.is_dir()).unwrap_or(false);
        if only_dirs && !is_dir {
            continue;
        }
        matches.push(Candidate { name, is_dir });
    }
    matches.sort_by(|left, right| {
        right
            .is_dir
            .cmp(&left.is_dir)
            .then_with(|| left.name.to_lowercase().cmp(&right.name.to_lowercase()))
            .then_with(|| left.name.cmp(&right.name))
    });
    let extra = matches.len().saturating_sub(MAX_CANDIDATES);
    matches.truncate(MAX_CANDIDATES);
    (matches, extra)
}

pub fn active(app: &App) -> bool {
    app.path_completion.is_some()
}

pub fn cancel(app: &mut App) {
    app.path_completion = None;
}

fn apply(
    app: &mut App,
    editor: &mut Editor,
    start: usize,
    end: usize,
    quote: Quote,
    separator: bool,
    candidate: &Candidate,
) {
    let mut insert = escape_name(&candidate.name, quote);
    if candidate.is_dir && !separator {
        insert.push('/');
    }
    editor.complete_global_range(start, end, &insert);
    app.path_completion = None;
}

/// Tab in the input focus: complete a single candidate immediately, or open the
/// list for several. Returns false when the token has no candidates so the
/// caller keeps the original Tab focus behavior.
pub fn tab(app: &mut App, editor: &mut Editor) -> bool {
    let Some(working_directory) = app.working_directory.clone() else {
        return false;
    };
    let text = editor.text();
    let cursor = editor.global_offset();
    let Some(token) = token_at(&text, cursor) else {
        return false;
    };
    let decoded = decode(&token);
    let cursor = decoded.decoded_at(cursor);
    let component_start = decoded.text[..cursor]
        .rfind('/')
        .map_or(0, |index| index + 1);
    let component_end = decoded.text[cursor..]
        .find('/')
        .map_or(decoded.text.len(), |index| cursor + index);
    // Match only the text typed through the cursor, but replace the whole
    // component so a mid-component typo cannot leave a duplicated suffix.
    let partial = &decoded.text[component_start..cursor];
    let prefix = &decoded.text[..component_start];
    let separator = decoded.text[component_end..].starts_with('/');
    let Some(base) = base_for(prefix, &working_directory) else {
        return false;
    };
    let (candidates, extra) = list_candidates(&base, partial, separator);
    if candidates.is_empty() {
        return false;
    }
    let Some(quote) = component_quote(&decoded, &token, component_start, component_end) else {
        // Mixed quoting inside one component is not supported: consume the Tab
        // without editing the draft rather than generating malformed quotes.
        return true;
    };
    let Some(start) = decoded.doc_at(component_start) else {
        return false;
    };
    // Replace only the component; an existing separator and closing quote stay
    // untouched. A directory adds '/' only when no separator exists yet.
    let end = decoded.doc_at(component_end).unwrap_or(start);
    if candidates.len() == 1 {
        apply(app, editor, start, end, quote, separator, &candidates[0]);
        return true;
    }
    app.path_completion = Some(PathCompletion {
        candidates,
        selected: 0,
        truncated: extra,
        start,
        end,
        quote,
        separator,
    });
    true
}

pub fn move_selection(app: &mut App, forward: bool) {
    let Some(state) = &mut app.path_completion else {
        return;
    };
    if state.candidates.is_empty() {
        return;
    }
    state.selected = if forward {
        (state.selected + 1).min(state.candidates.len() - 1)
    } else {
        state.selected.saturating_sub(1)
    };
}

/// Confirms the selected candidate. Never submits; directories close after one
/// separator so the next Tab lists their children.
pub fn confirm(app: &mut App, editor: &mut Editor) {
    let Some(state) = app.path_completion.take() else {
        return;
    };
    let Some(candidate) = state.candidates.get(state.selected) else {
        return;
    };
    apply(
        app,
        editor,
        state.start,
        state.end,
        state.quote,
        state.separator,
        candidate,
    );
}

/// Compact list above the composer, only while the completion is active.
pub fn draw(frame: &mut Frame, app: &App) {
    let Some(state) = &app.path_completion else {
        return;
    };
    if app.help_open
        || app.log_view.is_some()
        || app.transcript_search.is_some()
        || app.focus != crate::frontend::tui::state::Focus::Input
    {
        return;
    }
    let theme = app.theme;
    let name_width = state
        .candidates
        .iter()
        .map(|candidate| candidate.name.width() + usize::from(candidate.is_dir))
        .max()
        .unwrap_or(4);
    let hint = "Up/Down choose · Enter/Tab insert · Esc dismiss";
    let width = (name_width + 4)
        .max(if state.truncated > 0 { 38 } else { 22 })
        .min(app.input_area.width.max(1) as usize) as u16;
    if width < 4 {
        return;
    }
    let rows = state.candidates.len().min(VISIBLE_ROWS);
    let footer = usize::from(state.truncated > 0);
    let height = (rows + footer) as u16 + 2;
    if app.input_area.y < height {
        return;
    }
    let area = Rect::new(app.input_area.x, app.input_area.y - height, width, height);
    let mut lines: Vec<Line> = Vec::new();
    let start = state.selected.saturating_sub(VISIBLE_ROWS - 1);
    for (index, candidate) in state.candidates.iter().enumerate().skip(start).take(rows) {
        let selected = index == state.selected;
        let marker = if selected { "› " } else { "  " };
        let suffix = if candidate.is_dir { "/" } else { "" };
        let style = if selected {
            Style::default()
                .fg(theme.accent)
                .add_modifier(Modifier::BOLD | Modifier::REVERSED)
        } else {
            Style::default().fg(theme.text)
        };
        lines.push(Line::from(Span::styled(
            format!("{marker}{}{}", candidate.name, suffix),
            style,
        )));
    }
    if state.truncated > 0 {
        lines.push(Line::from(Span::styled(
            format!("… {} more — type to narrow", state.truncated),
            Style::default().fg(theme.secondary),
        )));
    }
    let hint_fits = (width as usize) > hint.len() + 2;
    let block = Block::default()
        .borders(Borders::ALL)
        .title(if hint_fits {
            " Paths · Up/Down · Enter/Tab · Esc "
        } else {
            " Paths "
        })
        .border_style(Style::default().fg(theme.accent));
    let inner = block.inner(area);
    frame.render_widget(Clear, area);
    frame.render_widget(block, area);
    frame.render_widget(Paragraph::new(lines), inner);
}
