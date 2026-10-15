use std::io::{self, Write};
use std::time::{Duration, Instant};

use crossterm::event as ce;
use futures_util::StreamExt;
use termina::escape::csi::{Csi, Mode, ThemeMode};
use termina::escape::osc::{ColorOrQuery, DynamicColorNumber, Osc};
use termina::event::{
    Event as TerminaEvent, KeyCode as TerminaKeyCode, KeyEvent as TerminaKeyEvent,
    KeyEventKind as TerminaKeyKind, KeyEventState as TerminaKeyState,
    Modifiers as TerminaModifiers, MouseButton as TerminaMouseButton,
    MouseEvent as TerminaMouseEvent, MouseEventKind as TerminaMouseKind,
};
use termina::{EventReader, EventStream};

use super::theme::{NativeColors, ThemeSetting};

/// One bounded window for startup/scheme color replies. The adapter never
/// blocks waiting: late replies are consumed, and a queued scheme change is
/// flushed when this window expires.
const QUERY_WINDOW: Duration = Duration::from_millis(250);

/// Enables mode2031 notifications, asks for the current scheme once, and
/// samples OSC 10/11 in one startup write.
const DETECT_SEQUENCE: &[u8] = b"\x1b[?2031h\x1b[?996n\x1b]10;?\x1b\\\x1b]11;?\x1b\\";

/// Refreshes only the sampled default colors. It deliberately does not ask for
/// a scheme report again, so a refresh cannot re-trigger itself.
const COLOR_QUERY: &[u8] = b"\x1b]10;?\x1b\\\x1b]11;?\x1b\\";

/// One frontend input item: a mapped user event or a coherent color sample.
pub(crate) enum Input {
    Event(ce::Event),
    Theme(NativeColors),
}

/// Protocol state for native color detection. Manual settings never query and
/// never emit a theme, but protocol events are still consumed safely.
///
/// OSC has no request IDs, so this does not attempt to prove which query a
/// reply belongs to. It guarantees fresh-pair handling: fg/bg replies count as
/// a pair until a new query begins, a deadline only stops waiting/coalescing,
/// and the latest complete pair becomes the rendered palette.
pub(crate) struct ProtocolState {
    detect: bool,
    window: Duration,
    foreground: Option<(u8, u8, u8)>,
    background: Option<(u8, u8, u8)>,
    scheme_dirty: bool,
    last_scheme: Option<ThemeMode>,
    pending: Option<Instant>,
}

impl ProtocolState {
    fn new(setting: ThemeSetting) -> Self {
        let detect = setting == ThemeSetting::Auto;
        Self {
            detect,
            window: QUERY_WINDOW,
            foreground: None,
            background: None,
            scheme_dirty: false,
            last_scheme: None,
            pending: detect.then(Instant::now),
        }
    }

    /// Consumes a protocol response. OSC 10/11 fill fresh slots; a changed 997
    /// scheme report queues one color refresh. Only the first startup scheme
    /// report is a baseline; later differing reports queue even while the
    /// initial probe is still pending.
    fn handle(&mut self, event: &TerminaEvent) {
        if !self.detect {
            return;
        }
        match event {
            TerminaEvent::Osc(Osc::ChangeDynamicColors(slot, colors)) => {
                let Some(ColorOrQuery::Color(rgb)) = colors.first() else {
                    return;
                };
                match slot {
                    DynamicColorNumber::TextForegroundColor => {
                        self.foreground = Some((rgb.red, rgb.green, rgb.blue));
                    }
                    DynamicColorNumber::TextBackgroundColor => {
                        self.background = Some((rgb.red, rgb.green, rgb.blue));
                    }
                    _ => {}
                }
            }
            TerminaEvent::Csi(Csi::Mode(Mode::ReportTheme(mode))) => {
                let first_report = self.last_scheme.is_none();
                if self.last_scheme == Some(*mode) {
                    return;
                }
                self.last_scheme = Some(*mode);
                if !first_report {
                    self.scheme_dirty = true;
                }
            }
            _ => {}
        }
    }

    fn on_focus(&mut self) {
        if self.detect {
            self.scheme_dirty = true;
        }
    }

    fn probe_deadline(&self) -> Option<Instant> {
        self.pending.map(|started| started + self.window)
    }

    fn take_theme(&mut self) -> Option<NativeColors> {
        if !self.detect || self.foreground.is_none() || self.background.is_none() {
            return None;
        }
        let foreground = self.foreground.take()?;
        let background = self.background.take()?;
        // The window stays open until its deadline so a queued scheme change
        // still gets its flush even though the pair already completed.
        Some(NativeColors {
            foreground,
            background,
        })
    }

    /// Starts at most one query per window. Reaching the deadline stops
    /// waiting/coalescing but keeps partial fresh slots so a late reply can
    /// still pair; a queued scheme change is flushed at that point.
    fn refresh_query(&mut self) -> Option<&'static [u8]> {
        if !self.detect {
            return None;
        }
        if self
            .pending
            .is_some_and(|started| started.elapsed() < self.window)
        {
            return None;
        }
        self.pending = None;
        if !self.scheme_dirty {
            return None;
        }
        self.scheme_dirty = false;
        self.foreground = None;
        self.background = None;
        self.pending = Some(Instant::now());
        Some(COLOR_QUERY)
    }
}

/// The single Termina-backed input stream. Protocol replies are consumed here
/// and never become editor input.
pub(crate) struct TerminalInput {
    stream: EventStream,
    protocol: ProtocolState,
}

impl TerminalInput {
    /// Returns the adapter and the startup detection sequence to write once the
    /// output side is ready (empty for manual themes).
    pub(crate) fn new(reader: EventReader, setting: ThemeSetting) -> (Self, Option<&'static [u8]>) {
        let protocol = ProtocolState::new(setting);
        let detect = protocol.detect.then_some(DETECT_SEQUENCE);
        (
            Self {
                stream: EventStream::new(reader, |_| true),
                protocol,
            },
            detect,
        )
    }

    pub(crate) async fn next(&mut self) -> io::Result<Option<Input>> {
        loop {
            let maybe_event = if let Some(deadline) = self.protocol.probe_deadline() {
                let sleep = tokio::time::sleep_until(tokio::time::Instant::from_std(deadline));
                tokio::pin!(sleep);
                tokio::select! {
                    event = self.stream.next() => event,
                    _ = &mut sleep => {
                        // The window closed and a queued scheme change still
                        // flushes here, without needing another input.
                        self.flush_refresh()?;
                        continue;
                    }
                }
            } else {
                self.stream.next().await
            };
            let event = match maybe_event {
                Some(Ok(event)) => event,
                Some(Err(error)) => return Err(error),
                None => return Ok(None),
            };
            match event {
                TerminaEvent::Osc(_) | TerminaEvent::Csi(_) | TerminaEvent::Dcs(_) => {
                    self.protocol.handle(&event);
                    self.flush_refresh()?;
                    if let Some(colors) = self.protocol.take_theme() {
                        return Ok(Some(Input::Theme(colors)));
                    }
                }
                TerminaEvent::FocusIn => {
                    self.protocol.on_focus();
                    self.flush_refresh()?;
                    return Ok(Some(Input::Event(ce::Event::FocusGained)));
                }
                other => {
                    if let Some(mapped) = map_event(other) {
                        return Ok(Some(Input::Event(mapped)));
                    }
                }
            }
        }
    }

    fn flush_refresh(&mut self) -> io::Result<()> {
        if let Some(bytes) = self.protocol.refresh_query() {
            write_terminal(bytes)?;
        }
        Ok(())
    }
}

fn write_terminal(bytes: &[u8]) -> io::Result<()> {
    let mut stdout = std::io::stdout();
    stdout.write_all(bytes)?;
    stdout.flush()
}

/// Maps one Termina user event to the crossterm event shape the existing
/// handler consumes. Protocol responses return `None`.
pub(crate) fn map_event(event: TerminaEvent) -> Option<ce::Event> {
    match event {
        TerminaEvent::Key(key) => map_key(key).map(ce::Event::Key),
        TerminaEvent::Mouse(mouse) => Some(ce::Event::Mouse(map_mouse(mouse))),
        TerminaEvent::Paste(text) => Some(ce::Event::Paste(text)),
        TerminaEvent::WindowResized(size) => Some(ce::Event::Resize(size.cols, size.rows)),
        TerminaEvent::FocusIn => Some(ce::Event::FocusGained),
        TerminaEvent::FocusOut => Some(ce::Event::FocusLost),
        TerminaEvent::Csi(_) | TerminaEvent::Osc(_) | TerminaEvent::Dcs(_) => None,
    }
}

fn map_key(key: TerminaKeyEvent) -> Option<ce::KeyEvent> {
    let code = match key.code {
        TerminaKeyCode::Char(character) => ce::KeyCode::Char(character),
        TerminaKeyCode::Enter => ce::KeyCode::Enter,
        TerminaKeyCode::Backspace => ce::KeyCode::Backspace,
        TerminaKeyCode::Tab => ce::KeyCode::Tab,
        TerminaKeyCode::BackTab => ce::KeyCode::BackTab,
        TerminaKeyCode::Escape => ce::KeyCode::Esc,
        TerminaKeyCode::Left => ce::KeyCode::Left,
        TerminaKeyCode::Right => ce::KeyCode::Right,
        TerminaKeyCode::Up => ce::KeyCode::Up,
        TerminaKeyCode::Down => ce::KeyCode::Down,
        TerminaKeyCode::Home => ce::KeyCode::Home,
        TerminaKeyCode::End => ce::KeyCode::End,
        TerminaKeyCode::PageUp => ce::KeyCode::PageUp,
        TerminaKeyCode::PageDown => ce::KeyCode::PageDown,
        TerminaKeyCode::Insert => ce::KeyCode::Insert,
        TerminaKeyCode::Delete => ce::KeyCode::Delete,
        TerminaKeyCode::Null => ce::KeyCode::Null,
        TerminaKeyCode::CapsLock => ce::KeyCode::CapsLock,
        TerminaKeyCode::ScrollLock => ce::KeyCode::ScrollLock,
        TerminaKeyCode::NumLock => ce::KeyCode::NumLock,
        TerminaKeyCode::PrintScreen => ce::KeyCode::PrintScreen,
        TerminaKeyCode::Pause => ce::KeyCode::Pause,
        TerminaKeyCode::Menu => ce::KeyCode::Menu,
        TerminaKeyCode::KeypadBegin => ce::KeyCode::KeypadBegin,
        TerminaKeyCode::Function(number) => ce::KeyCode::F(number),
        // Media/modifier-only key events have no existing handler binding.
        TerminaKeyCode::Media(_) | TerminaKeyCode::Modifier(_) => return None,
    };
    Some(ce::KeyEvent::new_with_kind_and_state(
        code,
        map_modifiers(key.modifiers),
        match key.kind {
            TerminaKeyKind::Press => ce::KeyEventKind::Press,
            TerminaKeyKind::Repeat => ce::KeyEventKind::Repeat,
            TerminaKeyKind::Release => ce::KeyEventKind::Release,
        },
        map_state(key.state),
    ))
}

fn map_modifiers(modifiers: TerminaModifiers) -> ce::KeyModifiers {
    let mut mapped = ce::KeyModifiers::NONE;
    if modifiers.contains(TerminaModifiers::SHIFT) {
        mapped |= ce::KeyModifiers::SHIFT;
    }
    if modifiers.contains(TerminaModifiers::CONTROL) {
        mapped |= ce::KeyModifiers::CONTROL;
    }
    if modifiers.contains(TerminaModifiers::ALT) {
        mapped |= ce::KeyModifiers::ALT;
    }
    if modifiers.contains(TerminaModifiers::SUPER) {
        mapped |= ce::KeyModifiers::SUPER;
    }
    if modifiers.contains(TerminaModifiers::HYPER) {
        mapped |= ce::KeyModifiers::HYPER;
    }
    if modifiers.contains(TerminaModifiers::META) {
        mapped |= ce::KeyModifiers::META;
    }
    mapped
}

fn map_state(state: TerminaKeyState) -> ce::KeyEventState {
    let mut mapped = ce::KeyEventState::NONE;
    if state.contains(TerminaKeyState::KEYPAD) {
        mapped |= ce::KeyEventState::KEYPAD;
    }
    if state.contains(TerminaKeyState::CAPS_LOCK) {
        mapped |= ce::KeyEventState::CAPS_LOCK;
    }
    if state.contains(TerminaKeyState::NUM_LOCK) {
        mapped |= ce::KeyEventState::NUM_LOCK;
    }
    mapped
}

fn map_mouse(mouse: TerminaMouseEvent) -> ce::MouseEvent {
    ce::MouseEvent {
        kind: match mouse.kind {
            TerminaMouseKind::Down(button) => ce::MouseEventKind::Down(map_button(button)),
            TerminaMouseKind::Up(button) => ce::MouseEventKind::Up(map_button(button)),
            TerminaMouseKind::Drag(button) => ce::MouseEventKind::Drag(map_button(button)),
            TerminaMouseKind::Moved => ce::MouseEventKind::Moved,
            TerminaMouseKind::ScrollDown => ce::MouseEventKind::ScrollDown,
            TerminaMouseKind::ScrollUp => ce::MouseEventKind::ScrollUp,
            TerminaMouseKind::ScrollLeft => ce::MouseEventKind::ScrollLeft,
            TerminaMouseKind::ScrollRight => ce::MouseEventKind::ScrollRight,
        },
        column: mouse.column,
        row: mouse.row,
        modifiers: map_modifiers(mouse.modifiers),
    }
}

fn map_button(button: TerminaMouseButton) -> ce::MouseButton {
    match button {
        TerminaMouseButton::Left => ce::MouseButton::Left,
        TerminaMouseButton::Right => ce::MouseButton::Right,
        TerminaMouseButton::Middle => ce::MouseButton::Middle,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use termina::style::RgbColor;

    fn osc(slot: DynamicColorNumber, rgb: (u8, u8, u8)) -> TerminaEvent {
        TerminaEvent::Osc(Osc::ChangeDynamicColors(
            slot,
            vec![ColorOrQuery::Color(RgbColor {
                red: rgb.0,
                green: rgb.1,
                blue: rgb.2,
            })],
        ))
    }

    fn scheme(mode: ThemeMode) -> TerminaEvent {
        TerminaEvent::Csi(Csi::Mode(Mode::ReportTheme(mode)))
    }

    fn expire(state: &mut ProtocolState) {
        state.pending = Some(Instant::now() - state.window);
    }

    #[test]
    fn first_scheme_is_baseline_but_later_difference_queues_before_the_window_ends() {
        let mut state = ProtocolState::new(ThemeSetting::Auto);
        // First startup report is baseline, not a change.
        state.handle(&scheme(ThemeMode::Light));
        // A differing report during the initial pending window still queues.
        state.handle(&scheme(ThemeMode::Dark));
        assert!(
            state.refresh_query().is_none(),
            "a refresh is coalesced while the initial window is open"
        );
        expire(&mut state);
        assert!(
            state.refresh_query().is_some(),
            "the queued change flushes once the window closes"
        );

        let mut identical = ProtocolState::new(ThemeSetting::Auto);
        identical.handle(&scheme(ThemeMode::Light));
        expire(&mut identical);
        assert!(identical.refresh_query().is_none());
        identical.handle(&scheme(ThemeMode::Light));
        assert!(
            identical.refresh_query().is_none(),
            "identical scheme reports never requery"
        );
    }

    #[test]
    fn queued_scheme_change_survives_a_complete_pair_and_flushes_at_the_deadline() {
        let mut state = ProtocolState::new(ThemeSetting::Auto);
        state.handle(&scheme(ThemeMode::Light)); // startup baseline
        state.handle(&scheme(ThemeMode::Dark)); // queued while the probe is open
        state.handle(&osc(DynamicColorNumber::TextForegroundColor, (1, 2, 3)));
        state.handle(&osc(DynamicColorNumber::TextBackgroundColor, (4, 5, 6)));
        assert!(state.take_theme().is_some(), "the pair still applies");
        assert!(
            state.refresh_query().is_none(),
            "the queued change waits for the window deadline"
        );
        expire(&mut state);
        assert!(
            state.refresh_query().is_some(),
            "the queued change flushes after the deadline"
        );
    }

    #[test]
    fn late_second_slot_pairs_after_the_deadline_until_a_new_query_starts() {
        let mut state = ProtocolState::new(ThemeSetting::Auto);
        state.handle(&osc(DynamicColorNumber::TextForegroundColor, (1, 2, 3)));
        expire(&mut state);
        // Deadline passed; the partial slot is retained and pairs with the bg.
        state.handle(&osc(DynamicColorNumber::TextBackgroundColor, (4, 5, 6)));
        assert_eq!(
            state.take_theme(),
            Some(NativeColors {
                foreground: (1, 2, 3),
                background: (4, 5, 6),
            })
        );

        // A new query clears fresh slots without dropping the last sample.
        state.handle(&scheme(ThemeMode::Light));
        state.handle(&scheme(ThemeMode::Dark));
        expire(&mut state);
        assert!(state.refresh_query().is_some());
        state.handle(&osc(DynamicColorNumber::TextBackgroundColor, (7, 8, 9)));
        assert!(
            state.take_theme().is_none(),
            "a superseded foreground cannot pair with the new query's background"
        );
    }

    #[test]
    fn manual_settings_never_query_or_emit_a_theme() {
        let mut state = ProtocolState::new(ThemeSetting::Dark);
        state.handle(&osc(DynamicColorNumber::TextForegroundColor, (1, 2, 3)));
        state.handle(&osc(DynamicColorNumber::TextBackgroundColor, (4, 5, 6)));
        state.handle(&scheme(ThemeMode::Light));
        state.on_focus();
        assert!(state.take_theme().is_none());
        assert!(state.refresh_query().is_none());
    }
}
