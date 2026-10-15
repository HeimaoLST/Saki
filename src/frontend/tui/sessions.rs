use std::collections::HashMap;

use crossterm::event::{KeyCode, KeyEvent, KeyEventKind, KeyModifiers};
use ratatui::{
    Frame,
    layout::Rect,
    style::{Modifier, Style},
    text::{Line, Span},
    widgets::{Block, Borders, Clear, Paragraph},
};
use unicode_segmentation::UnicodeSegmentation;

use super::{
    editor::Editor,
    state::{App, Record},
    textwrap, theme,
};
use crate::{
    agent::journal::TaskStatus,
    session::{SessionCommand, SessionEvent, store::SessionSummary},
};

pub struct Picker {
    pub sessions: Vec<SessionSummary>,
    /// Indices into `sessions` matching the active query, in core order.
    /// `selected` indexes this list, so navigation only visits visible results.
    visible: Vec<usize>,
    /// Index into `visible`.
    pub selected: usize,
    active_id: Option<String>,
    rename: Option<Rename>,
    /// `Some` while the local search field is active; `None` in browse mode.
    query: Option<String>,
}

/// An in-progress rename. The target ID is pinned so an asynchronous title
/// update cannot redirect the rename to a different result.
struct Rename {
    id: String,
    title: String,
}

impl Picker {
    /// The session a confirm or rename would act on; `None` when the query has
    /// no matches.
    fn selected_session(&self) -> Option<&SessionSummary> {
        self.visible
            .get(self.selected)
            .and_then(|index| self.sessions.get(*index))
    }

    /// Recomputes matches. `preferred` stays selected when it still matches,
    /// otherwise the first result is selected.
    fn refilter(&mut self, preferred: Option<&str>) {
        self.visible = match &self.query {
            Some(query) => self
                .sessions
                .iter()
                .enumerate()
                .filter(|(_, session)| session_matches(session, query))
                .map(|(index, _)| index)
                .collect(),
            None => (0..self.sessions.len()).collect(),
        };
        self.selected = preferred
            .and_then(|id| {
                self.visible
                    .iter()
                    .position(|index| self.sessions[*index].metadata.id == id)
            })
            .unwrap_or(0);
    }

    /// Recomputes matches while keeping the selected session when it still
    /// matches.
    fn refresh(&mut self) {
        let preferred = self
            .selected_session()
            .map(|session| session.metadata.id.clone());
        self.refilter(preferred.as_deref());
    }
}

/// Case-insensitive, whitespace-separated AND match over the metadata the
/// picker already holds: title, ID, working directory and model route.
fn session_matches(session: &SessionSummary, query: &str) -> bool {
    let terms: Vec<String> = query.split_whitespace().map(str::to_lowercase).collect();
    if terms.is_empty() {
        return true;
    }
    let title = session.metadata.title.to_lowercase();
    let id = session.metadata.id.to_lowercase();
    let directory = session
        .metadata
        .working_directory
        .to_string_lossy()
        .to_lowercase();
    let model = session.metadata.model.to_lowercase();
    terms.iter().all(|term| {
        title.contains(term)
            || id.contains(term)
            || directory.contains(term)
            || model.contains(term)
    })
}

/// Removes the last grapheme so search Backspace is safe for combining marks
/// and ZWJ sequences.
fn pop_last_grapheme(text: &mut String) {
    if let Some((index, _)) = text.grapheme_indices(true).next_back() {
        text.truncate(index);
    }
}

#[derive(Default)]
pub struct Sessions {
    pub picker: Option<Picker>,
    pub pending: bool,
    cached: HashMap<String, (App, Editor)>,
}

impl Sessions {
    pub fn apply_event(&mut self, app: &mut App, editor: &mut Editor, event: SessionEvent) {
        let renamed = matches!(&event, SessionEvent::Renamed(_));
        match event {
            SessionEvent::Opened {
                metadata,
                replay,
                context,
            } => {
                let mut next = App::restore(&replay);
                let next_editor;
                if let Some((previous, draft)) = self.cached.remove(&metadata.id) {
                    next.follow = previous.follow;
                    next.anchor = previous
                        .anchor
                        .filter(|anchor| anchor.record < next.records.len());
                    next.selected = previous.selected.min(next.records.len().saturating_sub(1));
                    next.focus = previous.focus;
                    next.history = previous.history;
                    for (record, old) in next.records.iter_mut().zip(&previous.records) {
                        match (record, old) {
                            (
                                Record::Reasoning { text, expanded, .. },
                                Record::Reasoning {
                                    text: old_text,
                                    expanded: old_expanded,
                                    ..
                                },
                            ) if text == old_text => *expanded = *old_expanded,
                            (
                                Record::Tool {
                                    call_id, expanded, ..
                                },
                                Record::Tool {
                                    call_id: old_id,
                                    expanded: old_expanded,
                                    ..
                                },
                            ) if call_id == old_id => *expanded = *old_expanded,
                            // Manual Activity expansion survives a reopen even
                            // though the restored history starts collapsed.
                            (
                                Record::Activity {
                                    expanded, manual, ..
                                },
                                Record::Activity {
                                    expanded: old_expanded,
                                    manual: old_manual,
                                    ..
                                },
                            ) => {
                                *expanded = *old_expanded;
                                *manual = *old_manual;
                            }
                            _ => {}
                        }
                    }
                    next_editor = draft;
                } else {
                    next_editor = Editor::new();
                }
                next.working_directory = Some(metadata.working_directory.clone());
                next.session = Some(metadata);
                next.apply_event(context);
                // The live palette (including an auto-detected one) follows the
                // reader across sessions; a cached session must never resurrect
                // the palette that was current when it was cached.
                next.theme = app.theme;
                next.normalize_selection();
                next.normalize_anchor();
                let previous = std::mem::replace(app, next);
                let draft = std::mem::replace(editor, next_editor);
                if let Some(metadata) = &previous.session {
                    self.cached.insert(metadata.id.clone(), (previous, draft));
                }
                self.picker = None;
                self.pending = false;
            }
            SessionEvent::Listed(sessions) => {
                let active_id = app.session.as_ref().map(|session| session.id.clone());
                let mut picker = Picker {
                    sessions,
                    visible: Vec::new(),
                    selected: 0,
                    active_id: active_id.clone(),
                    rename: None,
                    query: None,
                };
                picker.refilter(active_id.as_deref());
                self.picker = Some(picker);
                self.pending = false;
                app.help_open = false;
                app.text_selection.clear();
            }
            SessionEvent::Renamed(metadata) | SessionEvent::TitleUpdated(metadata) => {
                if app
                    .session
                    .as_ref()
                    .is_some_and(|active| active.id == metadata.id)
                {
                    app.session = Some(metadata.clone());
                }
                if let Some((cached, _)) = self.cached.get_mut(&metadata.id) {
                    cached.session = Some(metadata.clone());
                }
                if let Some(picker) = &mut self.picker {
                    // During rename the pinned target stays selected across
                    // asynchronous title updates.
                    let preferred = picker
                        .rename
                        .as_ref()
                        .map(|rename| rename.id.clone())
                        .or_else(|| {
                            picker
                                .selected_session()
                                .map(|session| session.metadata.id.clone())
                        });
                    if let Some(session) = picker
                        .sessions
                        .iter_mut()
                        .find(|session| session.metadata.id == metadata.id)
                    {
                        session.metadata = metadata;
                    }
                    if renamed {
                        picker.rename = None;
                    }
                    picker.refilter(preferred.as_deref());
                }
                if renamed {
                    self.pending = false;
                }
            }
            SessionEvent::Agent { session_id, event } => {
                if app
                    .session
                    .as_ref()
                    .is_some_and(|active| active.id == session_id)
                {
                    app.apply_event(event);
                }
            }
            SessionEvent::Error { message } => {
                self.pending = false;
                app.notify(message);
            }
        }
    }

    /// Returns whether the picker consumed the key and an optional core request.
    pub fn key(&mut self, app: &mut App, key: KeyEvent) -> (bool, Option<SessionCommand>) {
        if !matches!(key.kind, KeyEventKind::Press | KeyEventKind::Repeat) {
            return (self.picker.is_some() || self.pending, None);
        }
        if self.pending {
            return (true, None);
        }
        if key.modifiers.contains(KeyModifiers::CONTROL)
            && matches!(key.code, KeyCode::Char('s' | 'S'))
        {
            if self.picker.take().is_some() {
                return (true, None);
            }
            self.pending = true;
            return (true, Some(SessionCommand::List));
        }
        let Some(picker) = &mut self.picker else {
            return (false, None);
        };
        let ctrl = key.modifiers.contains(KeyModifiers::CONTROL);
        let alt = key.modifiers.contains(KeyModifiers::ALT);

        // Rename editing owns printable keys; n/r are title text here.
        if let Some(rename) = &mut picker.rename {
            match key.code {
                KeyCode::Esc => picker.rename = None,
                KeyCode::Backspace => {
                    rename.title.pop();
                }
                KeyCode::Char('c') if ctrl => picker.rename = None,
                KeyCode::Char(c) if !ctrl && !alt => rename.title.push(c),
                KeyCode::Enter if !rename.title.trim().is_empty() => {
                    self.pending = true;
                    return (
                        true,
                        Some(SessionCommand::Rename {
                            session_id: rename.id.clone(),
                            title: rename.title.clone(),
                        }),
                    );
                }
                _ => {}
            }
            return (true, None);
        }

        // Search text editing; Up/Down and Enter continue to the shared guards.
        if picker.query.is_some() {
            match key.code {
                KeyCode::Esc => {
                    picker.query = None;
                    picker.refresh();
                }
                KeyCode::Char('c') if ctrl => {
                    picker.query = None;
                    picker.refresh();
                }
                KeyCode::Backspace => {
                    if let Some(query) = picker.query.as_mut() {
                        pop_last_grapheme(query);
                    }
                    picker.refresh();
                }
                KeyCode::Char(c) if !ctrl && !alt => {
                    if let Some(query) = picker.query.as_mut() {
                        query.push(c);
                    }
                    picker.refresh();
                }
                _ => {}
            }
            if !matches!(key.code, KeyCode::Up | KeyCode::Down | KeyCode::Enter) {
                return (true, None);
            }
        }

        match key.code {
            KeyCode::Esc => self.picker = None,
            KeyCode::Char('c') if ctrl => self.picker = None,
            KeyCode::Up => picker.selected = picker.selected.saturating_sub(1),
            KeyCode::Down => {
                picker.selected = (picker.selected + 1).min(picker.visible.len().saturating_sub(1))
            }
            KeyCode::Char('/') => {
                picker.query = Some(String::new());
                picker.refresh();
            }
            KeyCode::Char('r') => {
                if let Some(session) = picker
                    .selected_session()
                    .filter(|session| session.unavailable.is_none())
                {
                    picker.rename = Some(Rename {
                        id: session.metadata.id.clone(),
                        title: session.metadata.title.clone(),
                    });
                }
            }
            KeyCode::Enter | KeyCode::Char('n') => {
                if app.running {
                    app.notify(
                        "Wait for the current task to finish before switching sessions.".to_owned(),
                    );
                    return (true, None);
                }
                let command = if key.code == KeyCode::Char('n') {
                    SessionCommand::Create
                } else {
                    let Some(session) = picker.selected_session() else {
                        return (true, None);
                    };
                    if app
                        .session
                        .as_ref()
                        .is_some_and(|active| active.id == session.metadata.id)
                    {
                        self.picker = None;
                        return (true, None);
                    }
                    if let Some(error) = &session.unavailable {
                        app.notify(error.clone());
                        return (true, None);
                    }
                    if session.in_use {
                        app.notify("This session is in use by another process.".to_owned());
                        return (true, None);
                    }
                    SessionCommand::Open {
                        session_id: session.metadata.id.clone(),
                    }
                };
                self.pending = true;
                return (true, Some(command));
            }
            _ => {}
        }
        (true, None)
    }

    pub fn paste(&mut self, text: &str) {
        if self.pending {
            return;
        }
        let Some(picker) = self.picker.as_mut() else {
            return;
        };
        if let Some(rename) = picker.rename.as_mut() {
            rename.title.push_str(&text.replace(['\r', '\n'], " "));
            return;
        }
        if picker.query.is_some() {
            let text = text.replace(['\r', '\n'], " ");
            if let Some(query) = picker.query.as_mut() {
                query.push_str(&text);
            }
            picker.refresh();
        }
    }

    pub fn draw(&self, frame: &mut Frame, theme: theme::Theme) {
        let Some(picker) = &self.picker else {
            return;
        };
        let screen = frame.area();
        let width = screen.width.saturating_sub(4).min(90);
        let height = screen.height.saturating_sub(4).min(18);
        if width < 4 || height < 4 {
            return;
        }
        let area = Rect::new(
            screen.x + (screen.width - width) / 2,
            screen.y + (screen.height - height) / 2,
            width,
            height,
        );
        let block = Block::default()
            .borders(Borders::ALL)
            .title(" Sessions ")
            .border_style(Style::default().fg(theme.accent));
        let inner = block.inner(area);
        frame.render_widget(Clear, area);
        frame.render_widget(block, area);
        let mut lines: Vec<Line> = Vec::new();
        if let Some(query) = &picker.query {
            // Show the query tail so the cursor stays visible when the query is
            // wider than the picker; wrap_bytes is grapheme/cell aware.
            let available = (inner.width as usize).saturating_sub("Search: ".len() + 1);
            let shown = textwrap::wrap_bytes(query, available.max(1))
                .pop()
                .map(|(line, _)| line)
                .unwrap_or_default();
            lines.push(Line::from(vec![
                Span::styled("Search: ", Style::default().fg(theme.accent)),
                Span::styled(shown, Style::default().fg(theme.secondary)),
                Span::styled("▌", Style::default().fg(theme.accent)),
            ]));
        }
        // Reserve the blank separator and footer for the hint line.
        let available = (inner.height as usize)
            .saturating_sub(lines.len())
            .saturating_sub(2);
        if picker.visible.is_empty() {
            lines.push(Line::from(Span::styled(
                if picker.query.is_some() {
                    "No sessions match the search.".to_owned()
                } else {
                    "No saved sessions.".to_owned()
                },
                Style::default().fg(theme.secondary),
            )));
        } else {
            let count = (available / 2).max(1);
            let start = picker.selected.saturating_sub(count.saturating_sub(1));
            for (index, session_index) in picker.visible.iter().enumerate().skip(start).take(count)
            {
                let session = &picker.sessions[*session_index];
                let marker = if index == picker.selected {
                    "› "
                } else {
                    "  "
                };
                let state = if session.unavailable.is_some() {
                    " · unavailable"
                } else if picker.active_id.as_ref() == Some(&session.metadata.id) {
                    " · current"
                } else if session.in_use {
                    " · in use"
                } else if session.status == TaskStatus::Interrupted {
                    " · interrupted"
                } else {
                    ""
                };
                let seconds = crate::agent::journal::now_ms()
                    .unwrap_or(session.updated_at)
                    .saturating_sub(session.updated_at)
                    / 1000;
                let age = if seconds < 60 {
                    "now".to_owned()
                } else if seconds < 3600 {
                    format!("{}m ago", seconds / 60)
                } else if seconds < 86400 {
                    format!("{}h ago", seconds / 3600)
                } else {
                    format!("{}d ago", seconds / 86400)
                };
                let text = format!("{marker}{}{state} · {age}", session.metadata.title);
                let style = if index == picker.selected {
                    Style::default()
                        .fg(theme.accent)
                        .add_modifier(Modifier::BOLD)
                } else {
                    Style::default().fg(theme.secondary)
                };
                lines.push(Line::from(Span::styled(text, style)));
                lines.push(Line::from(Span::styled(
                    format!(
                        "  {} · {}",
                        session.metadata.model,
                        session.metadata.working_directory.display()
                    ),
                    Style::default().fg(theme.secondary),
                )));
            }
        }
        lines.push(Line::default());
        lines.push(Line::from(Span::styled(
            if self.pending {
                "Loading…".to_owned()
            } else if let Some(rename) = &picker.rename {
                format!("Rename: {}  · Enter save · Esc cancel", rename.title)
            } else if picker.query.is_some() {
                if inner.width >= 50 {
                    "Type to filter · Up/Down select · Enter open · Esc clear".to_owned()
                } else {
                    "Type · Up/Down · Enter · Esc".to_owned()
                }
            } else if inner.width >= 54 {
                "Enter open · n new · r rename · / search · Esc close".to_owned()
            } else {
                "Enter open · / search".to_owned()
            },
            Style::default().fg(theme.secondary),
        )));
        frame.render_widget(Paragraph::new(lines), inner);
    }
}
