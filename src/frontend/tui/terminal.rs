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
use ratatui::{Terminal, backend::CrosstermBackend};
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
        execute!(
            io::stdout(),
            EnterAlternateScreen,
            EnableBracketedPaste,
            EnableMouseCapture,
            EnableFocusChange
        )?;
        Ok(modes)
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
