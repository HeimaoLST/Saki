use std::io::{self, Stdout, Write};

use crossterm::{
    cursor::Show,
    event::{
        DisableBracketedPaste, DisableFocusChange, DisableMouseCapture, EnableBracketedPaste,
        EnableFocusChange, EnableMouseCapture,
    },
    execute,
    terminal::{EnterAlternateScreen, LeaveAlternateScreen},
};
use ratatui::{Terminal, backend::CrosstermBackend, layout::Rect};
use termina::{PlatformTerminal, Terminal as TerminaTerminal};

use super::terminal_input::{Input, TerminalInput};
use super::theme::ThemeSetting;

/// Terminal modes enabled for the TUI. Restores them once when dropped, so
/// partial startup failures and normal exits share one output cleanup path.
struct ScreenModes;

impl ScreenModes {
    fn enter() -> io::Result<Self> {
        // The local guard's Drop restores the output modes if any part of the
        // setup fails, so failure and normal exit share one cleanup path.
        let modes = Self;
        modes.apply()?;
        Ok(modes)
    }

    fn apply(&self) -> io::Result<()> {
        execute!(
            io::stdout(),
            EnterAlternateScreen,
            EnableBracketedPaste,
            EnableMouseCapture,
            EnableFocusChange
        )
    }

    fn restore(&self) {
        let _ = restore_sequences(&mut io::stdout());
    }
}

impl Drop for ScreenModes {
    fn drop(&mut self) {
        self.restore();
    }
}

/// Owns the terminal lifecycle. Field order is deliberate: `ScreenModes`
/// restores output first, then the input stream stops, and `PlatformTerminal`
/// drops last to restore the captured termios exactly once.
pub struct TerminalSession {
    _modes: ScreenModes,
    screen: Terminal<CrosstermBackend<Stdout>>,
    input: TerminalInput,
    _terminal: PlatformTerminal,
}

impl TerminalSession {
    pub fn enter(setting: ThemeSetting) -> io::Result<Self> {
        let mut terminal = PlatformTerminal::new()?;
        // The hook restores the screen while Termina restores the captured
        // termios before the previous hook runs.
        terminal.set_panic_hook(|write| {
            let _ = restore_sequences(write);
        });
        terminal.enter_raw_mode()?;
        let modes = ScreenModes::enter()?;
        let (input, detect_sequence) = TerminalInput::new(terminal.event_reader(), setting);
        if let Some(sequence) = detect_sequence {
            write_stdout(sequence)?;
        }
        let screen = Terminal::new(CrosstermBackend::new(io::stdout()))?;
        Ok(Self {
            _modes: modes,
            screen,
            input,
            _terminal: terminal,
        })
    }

    /// The single input path. Termina protocol replies are consumed inside the
    /// adapter and never surface as editor input.
    pub async fn next_input(&mut self) -> io::Result<Option<Input>> {
        self.input.next().await
    }

    /// Deterministic suspension for a cooked child process: join the input
    /// reader, restore the screen, then restore the same terminal's captured
    /// termios. The `PlatformTerminal` is kept so resume re-enters raw mode on
    /// the original handle instead of creating a second cooked snapshot.
    pub fn suspend_external(&mut self) -> io::Result<()> {
        self.input.suspend();
        self._modes.restore();
        self._terminal.enter_cooked_mode()
    }

    /// Re-enter raw mode, output modes and the reader, then invalidate the
    /// Ratatui buffers so the next draw is a full redraw.
    pub fn resume_external(&mut self) -> io::Result<()> {
        self._terminal.enter_raw_mode()?;
        self._modes.apply()?;
        self.input.resume();
        // Resize to the current PTY size: this clears the screen and resets the
        // back buffer without querying the cursor (which needs a terminal reply).
        let size = self._terminal.get_dimensions()?;
        self.screen.resize(Rect::new(0, 0, size.cols, size.rows))?;
        Ok(())
    }

    pub fn draw<F>(&mut self, draw: F) -> io::Result<()>
    where
        F: FnOnce(&mut ratatui::Frame),
    {
        self.screen.draw(draw)?;
        Ok(())
    }
}

fn restore_sequences<W: Write>(write: &mut W) -> io::Result<()> {
    execute!(
        write,
        DisableBracketedPaste,
        DisableMouseCapture,
        DisableFocusChange,
        LeaveAlternateScreen,
        Show
    )?;
    // Disable the color-scheme subscription even if setup only got halfway.
    write.write_all(b"\x1b[?2031l")?;
    write.flush()
}

fn write_stdout(bytes: &[u8]) -> io::Result<()> {
    let mut stdout = io::stdout();
    stdout.write_all(bytes)?;
    stdout.flush()
}
