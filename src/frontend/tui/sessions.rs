use std::collections::HashMap;

use crossterm::event::{KeyCode, KeyEvent, KeyEventKind, KeyModifiers};
use ratatui::{
    Frame,
    layout::Rect,
    style::{Modifier, Style},
    text::{Line, Span},
    widgets::{Block, Borders, Clear, Paragraph},
};

use super::{
    editor::Editor,
    state::{App, Record},
    theme,
};
use crate::{
    agent::journal::TaskStatus,
    session::{SessionCommand, SessionEvent, store::SessionSummary},
};

pub struct Picker {
    pub sessions: Vec<SessionSummary>,
    pub selected: usize,
    active_id: Option<String>,
    rename: Option<String>,
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
                let selected = sessions
                    .iter()
                    .position(|session| {
                        app.session
                            .as_ref()
                            .is_some_and(|active| active.id == session.metadata.id)
                    })
                    .unwrap_or(0);
                self.picker = Some(Picker {
                    sessions,
                    selected,
                    active_id: app.session.as_ref().map(|session| session.id.clone()),
                    rename: None,
                });
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
        if let Some(title) = &mut picker.rename {
            match key.code {
                KeyCode::Esc => picker.rename = None,
                KeyCode::Backspace => {
                    title.pop();
                }
                KeyCode::Char('c') if key.modifiers.contains(KeyModifiers::CONTROL) => {
                    picker.rename = None
                }
                KeyCode::Char(c)
                    if !key
                        .modifiers
                        .intersects(KeyModifiers::CONTROL | KeyModifiers::ALT) =>
                {
                    title.push(c)
                }
                KeyCode::Enter if !title.trim().is_empty() => {
                    if let Some(session) = picker.sessions.get(picker.selected) {
                        self.pending = true;
                        return (
                            true,
                            Some(SessionCommand::Rename {
                                session_id: session.metadata.id.clone(),
                                title: title.clone(),
                            }),
                        );
                    }
                }
                _ => {}
            }
            return (true, None);
        }
        match key.code {
            KeyCode::Esc => self.picker = None,
            KeyCode::Char('c') if key.modifiers.contains(KeyModifiers::CONTROL) => {
                self.picker = None
            }
            KeyCode::Up => picker.selected = picker.selected.saturating_sub(1),
            KeyCode::Down => {
                picker.selected = (picker.selected + 1).min(picker.sessions.len().saturating_sub(1))
            }
            KeyCode::Char('r') => {
                if let Some(session) = picker
                    .sessions
                    .get(picker.selected)
                    .filter(|session| session.unavailable.is_none())
                {
                    picker.rename = Some(session.metadata.title.clone());
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
                    let Some(session) = picker.sessions.get(picker.selected) else {
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
        if !self.pending
            && let Some(title) = self
                .picker
                .as_mut()
                .and_then(|picker| picker.rename.as_mut())
        {
            title.push_str(&text.replace(['\r', '\n'], " "));
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
        let mut lines = Vec::new();
        let count = (inner.height.saturating_sub(2) / 2).max(1) as usize;
        let start = picker.selected.saturating_sub(count.saturating_sub(1));
        for (index, session) in picker.sessions.iter().enumerate().skip(start).take(count) {
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
        lines.push(Line::default());
        lines.push(Line::from(Span::styled(
            if self.pending {
                "Loading…".to_owned()
            } else if let Some(title) = &picker.rename {
                format!("Rename: {title}  · Enter save · Esc cancel")
            } else {
                "Enter open · n new · r rename · Esc close".to_owned()
            },
            Style::default().fg(theme.secondary),
        )));
        frame.render_widget(Paragraph::new(lines), inner);
    }
}
