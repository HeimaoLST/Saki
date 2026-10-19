use crossterm::event::{
    KeyCode, KeyEvent, KeyEventKind, KeyModifiers, MouseButton, MouseEvent, MouseEventKind,
};
use ratatui::{
    Frame,
    layout::{Position, Rect},
    style::{Modifier, Style},
    text::{Line, Span},
    widgets::{Block, Borders, Clear, Paragraph},
};

use super::{
    editor::Editor,
    sessions::Sessions,
    state::{App, Focus},
};
use crate::{
    command::{self, Command, Definition},
    session::{SessionCommand, SessionEvent},
};

#[derive(Default)]
pub struct Commands {
    prefix: String,
    candidates: Vec<&'static Definition>,
    selected: usize,
    dismissed: bool,
    pending: Option<Command>,
    area: Rect,
    first_visible: usize,
}

impl Commands {
    pub fn sync(&mut self, app: &App, editor: &Editor) {
        let text = editor.text();
        if text != self.prefix {
            self.prefix = text;
            self.selected = 0;
            self.dismissed = false;
        }
        self.candidates = if !self.dismissed
            && !app.help_open
            && app.log_view.is_none()
            && app.focus == Focus::Input
            && editor.images().is_empty()
            && self.pending.is_none()
        {
            command::completions(&self.prefix)
        } else {
            Vec::new()
        };
        self.selected = self.selected.min(self.candidates.len().saturating_sub(1));
        self.area = Rect::default();
    }

    pub fn key(&mut self, editor: &mut Editor, key: KeyEvent) -> bool {
        if !matches!(key.kind, KeyEventKind::Press | KeyEventKind::Repeat)
            || self.candidates.is_empty()
            || key
                .modifiers
                .intersects(KeyModifiers::CONTROL | KeyModifiers::ALT | KeyModifiers::SHIFT)
        {
            return false;
        }
        match key.code {
            KeyCode::Up => self.selected = self.selected.saturating_sub(1),
            KeyCode::Down => self.selected = (self.selected + 1).min(self.candidates.len() - 1),
            KeyCode::Tab => self.complete(editor),
            KeyCode::Esc => {
                self.dismissed = true;
                self.candidates.clear();
            }
            _ => return false,
        }
        true
    }

    fn complete(&mut self, editor: &mut Editor) {
        let definition = self.candidates[self.selected];
        let whitespace = &self.prefix[..self.prefix.len() - self.prefix.trim_start().len()];
        editor.set_draft(format!("{whitespace}/{} ", definition.name));
        self.candidates.clear();
        self.area = Rect::default();
    }

    pub fn dispatch(
        &mut self,
        command: Command,
        app: &mut App,
        editor: &mut Editor,
        sessions: &mut Sessions,
    ) -> Option<SessionCommand> {
        if command.needs_idle() && app.running {
            app.notify(
                "Agent is busy. Draft kept; wait for the current task to finish.".to_owned(),
            );
            return None;
        }
        if command == Command::Help {
            editor.reset();
            app.help_open = true;
            app.help_scroll = 0;
            app.text_selection.clear();
            self.candidates.clear();
            return None;
        }
        let Some(metadata) = &app.session else {
            app.notify("Session is not ready. Draft kept.".to_owned());
            return None;
        };
        let request = command.request(&metadata.id);
        self.pending = Some(command);
        self.candidates.clear();
        sessions.pending = true;
        request
    }

    /// Clear a successful command before Session UI caches the outgoing draft.
    /// Errors retain it; unrelated Agent events are never acknowledgements.
    pub fn before_event(&mut self, editor: &mut Editor, event: &SessionEvent) {
        if self
            .pending
            .as_ref()
            .is_some_and(|command| command.acknowledged_by(event))
        {
            editor.reset();
            self.pending = None;
        } else if matches!(event, SessionEvent::Error { .. }) {
            self.pending = None;
        }
    }

    pub fn mouse(&mut self, editor: &mut Editor, mouse: MouseEvent) -> bool {
        if self.candidates.is_empty() || !self.area.contains(Position::new(mouse.column, mouse.row))
        {
            return false;
        }
        match mouse.kind {
            MouseEventKind::Down(MouseButton::Left) => {
                let row = mouse.row.saturating_sub(self.area.y + 1) as usize;
                let index = self.first_visible + row;
                if mouse.row > self.area.y
                    && mouse.row < self.area.bottom() - 1
                    && index < self.candidates.len()
                {
                    self.selected = index;
                    self.complete(editor);
                }
            }
            MouseEventKind::ScrollUp => self.selected = self.selected.saturating_sub(1),
            MouseEventKind::ScrollDown => {
                self.selected = (self.selected + 1).min(self.candidates.len() - 1)
            }
            _ => {}
        }
        true
    }

    pub fn draw(&mut self, frame: &mut Frame, app: &App) {
        if self.candidates.is_empty() {
            return;
        }
        let screen = frame.area();
        let height =
            (self.candidates.len() as u16 + 2).min(app.input_area.y.saturating_sub(screen.y));
        if screen.width < 4 || height < 3 {
            return;
        }
        self.area = Rect::new(
            screen.x,
            app.input_area.y - height,
            screen.width.min(76),
            height,
        );
        let block = Block::default()
            .borders(Borders::ALL)
            .title(" Commands ")
            .border_style(Style::default().fg(app.theme.accent));
        let inner = block.inner(self.area);
        self.first_visible = self
            .selected
            .saturating_sub(inner.height.saturating_sub(1) as usize);
        let lines: Vec<_> = self
            .candidates
            .iter()
            .enumerate()
            .skip(self.first_visible)
            .take(inner.height as usize)
            .map(|(index, definition)| {
                let style = if index == self.selected {
                    Style::default()
                        .fg(app.theme.accent)
                        .add_modifier(Modifier::BOLD)
                } else {
                    Style::default().fg(app.theme.secondary)
                };
                Line::from(vec![
                    Span::styled(format!("/{}", definition.name), style),
                    Span::styled(
                        format!("  {}", definition.description),
                        Style::default().fg(app.theme.secondary),
                    ),
                ])
            })
            .collect();
        frame.render_widget(Clear, self.area);
        frame.render_widget(block, self.area);
        frame.render_widget(Paragraph::new(lines), inner);
    }
}
