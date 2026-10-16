pub mod commands;
pub mod editor;
pub mod help;
pub mod history;
pub mod log;
pub mod markdown;
pub mod markdown_layout;
pub mod selection;
pub mod sessions;
pub mod state;
pub mod terminal;
pub mod terminal_input;
pub mod textwrap;
pub mod theme;
pub mod view;

use std::path::PathBuf;

use crossterm::event::{
    Event, KeyCode, KeyEvent, KeyEventKind, KeyModifiers, MouseButton, MouseEvent, MouseEventKind,
};
use tokio::sync::mpsc::{UnboundedReceiver, UnboundedSender};

use crate::agent::message::{self, ImageAttachment, UserMessage};
use crate::command::{self, Command, ParsedInput};
use crate::session::{SessionCommand, SessionEvent};
use editor::Editor;
use state::{App, Focus};
use terminal_input::Input;

/// Terminal front end.
///
/// It consumes session snapshots and tagged Agent events through the channels
/// the worker provides. It never holds the agent, conversation or tools.
pub async fn run(
    input: UnboundedSender<SessionCommand>,
    mut events: UnboundedReceiver<SessionEvent>,
) -> anyhow::Result<()> {
    // Theme selection happens once; App.theme is the only rendered state.
    // Manual settings never send queries or subscribe to scheme notifications.
    let setting = theme::ThemeSetting::from_env();
    let mut app = App::new();
    app.theme = setting.initial_theme();
    let mut session = terminal::TerminalSession::enter(setting)?;
    let mut sessions = sessions::Sessions::default();
    sessions.pending = true;
    let mut editor = Editor::new();
    let mut commands = commands::Commands::default();

    loop {
        commands.sync(&app, &editor);
        session.draw(|frame| {
            view::draw(frame, &mut app, &editor);
            if sessions.picker.is_none() && !sessions.pending {
                commands.draw(frame, &app);
            }
            sessions.draw(frame, app.theme);
        })?;
        if app.should_quit() {
            break;
        }
        let feedback_deadline = app
            .feedback
            .as_ref()
            .map(|(_, until)| tokio::time::Instant::from_std(*until));
        let scroll_deadline = app
            .text_selection
            .edge_deadline()
            .map(tokio::time::Instant::from_std);
        tokio::select! {
            maybe = session.next_input() => {
                let (copy, notify_copy) = match maybe {
                    Ok(Some(Input::Event(Event::Key(key)))) => {
                        let (handled, command) = sessions.key(&mut app, key);
                        if let Some(command) = command
                            && input.send(command).is_err()
                        {
                            break;
                        }
                        if handled {
                            // A picker/cancel key must also stop any background
                            // edge-scroll timer.
                            app.text_selection.clear();
                            continue;
                        }
                        if commands.key(&mut editor, key) {
                            app.text_selection.clear();
                            continue;
                        }
                        let outcome = handle_key(&mut app, &mut editor, key);
                        if let Some(command) = outcome.command
                            && let Some(request) = commands.dispatch(command, &mut app, &mut editor, &mut sessions)
                            && input.send(request).is_err()
                        {
                            break;
                        }
                        if outcome.cancel
                            && let Some(metadata) = &app.session
                        {
                            app.cancelling = true;
                            app.status = None;
                            let command = SessionCommand::Cancel {
                                session_id: metadata.id.clone(),
                            };
                            if input.send(command).is_err() {
                                break;
                            }
                        }
                        if let Some(message) = outcome.send {
                            let Some(metadata) = &app.session else { continue; };
                            let command = SessionCommand::Send { session_id: metadata.id.clone(), message: message.clone() };
                            app.submit(message);
                            if input.send(command).is_err() {
                                break;
                            }
                        }
                        if outcome.paste_clipboard {
                            handle_clipboard(&mut app, &mut editor);
                        }
                        if outcome.quit {
                            break;
                        }
                        (outcome.copy, true)
                    }
                    Ok(Some(Input::Event(Event::Paste(text)))) => {
                        if sessions.picker.is_some() || sessions.pending {
                            app.text_selection.clear();
                            sessions.paste(&text);
                            continue;
                        }
                        handle_paste_text(&mut app, &mut editor, &text);
                        app.text_selection.clear();
                        (None, false)
                    }
                    Ok(Some(Input::Event(Event::Mouse(mouse)))) => {
                        if sessions.picker.is_some() || sessions.pending {
                            app.text_selection.clear();
                            continue;
                        }
                        if commands.mouse(&mut editor, mouse) {
                            app.text_selection.clear();
                            continue;
                        }
                        let position = ratatui::layout::Position::new(mouse.column, mouse.row);
                        if mouse.kind == MouseEventKind::Down(MouseButton::Left) && app.session_button_area.contains(position) {
                            app.text_selection.clear();
                            sessions.pending = true;
                            if input.send(SessionCommand::List).is_err() { break; }
                            continue;
                        }
                        (handle_mouse(&mut app, mouse), false)
                    },
                    Ok(Some(Input::Theme(colors))) => {
                        app.set_theme(theme::native_theme(colors));
                        (None, false)
                    }
                    Ok(Some(Input::Event(Event::FocusLost))) => {
                        // A held drag must not survive focus loss even if the
                        // matching mouse-up never arrives.
                        app.text_selection.clear();
                        (None, false)
                    }
                    Ok(Some(Input::Event(_))) => (None, false),
                    Ok(None) => break,
                    Err(error) => return Err(error.into()),
                };
                if let Some(text) = copy {
                    match copy_to_clipboard(&text) {
                        Ok(()) if notify_copy => app.notify(format!("Copied {} characters", text.chars().count())),
                        Ok(()) => {}
                        Err(error) => app.notify(format!("Copy failed: {error}")),
                    }
                }
            }
            maybe = events.recv() => {
                match maybe {
                    Some(event) => {
                        commands.before_event(&mut editor, &event);
                        sessions.apply_event(&mut app, &mut editor, event);
                        // Coalesce bursts into one redraw without starving terminal input.
                        for _ in 0..127 {
                            match events.try_recv() {
                                Ok(event) => {
                                    commands.before_event(&mut editor, &event);
                                    sessions.apply_event(&mut app, &mut editor, event);
                                }
                                Err(_) => break,
                            }
                        }
                    }
                    None => break,
                }
            }
            _ = tokio::time::sleep_until(feedback_deadline.unwrap_or_else(tokio::time::Instant::now)), if feedback_deadline.is_some() => {
                app.expire_feedback(std::time::Instant::now());
            }
            _ = tokio::time::sleep_until(scroll_deadline.unwrap_or_else(tokio::time::Instant::now)), if scroll_deadline.is_some() => {
                view::selection_scroll_tick(&mut app);
            }
        }
    }
    Ok(())
}

pub struct KeyOutcome {
    pub send: Option<UserMessage>,
    pub command: Option<Command>,
    pub copy: Option<String>,
    /// Ctrl+V intent; the run loop performs the actual clipboard IO.
    pub paste_clipboard: bool,
    /// Esc intent while the agent is running; the run loop sends Cancel.
    pub cancel: bool,
    pub quit: bool,
}

/// Maps one key event to state changes. Kept free of terminal I/O so it can be
/// driven directly by tests. Held keys arrive as `Repeat` and are accepted;
/// `Release` is ignored.
pub fn handle_key(app: &mut App, editor: &mut Editor, key: KeyEvent) -> KeyOutcome {
    let mut outcome = KeyOutcome {
        send: None,
        command: None,
        copy: None,
        paste_clipboard: false,
        cancel: false,
        quit: false,
    };
    if !matches!(key.kind, KeyEventKind::Press | KeyEventKind::Repeat) {
        return outcome;
    }
    let ctrl = key.modifiers.contains(KeyModifiers::CONTROL);
    let alt = key.modifiers.contains(KeyModifiers::ALT);
    let shift = key.modifiers.contains(KeyModifiers::SHIFT);
    let had_selection = app.text_selection.active();

    if ctrl
        && matches!(key.code, KeyCode::Char('c' | 'C'))
        && let Some(text) = app.text_selection.text()
    {
        outcome.copy = Some(text);
        app.text_selection.clear();
        return outcome;
    }
    app.text_selection.clear();

    if ctrl && matches!(key.code, KeyCode::Char('v' | 'V')) {
        app.help_open = false;
        if app.log_view.is_some() {
            log::close(app);
        }
        app.focus = Focus::Input;
        outcome.paste_clipboard = true;
        return outcome;
    }

    if key.code == KeyCode::F(1) && !ctrl && !alt {
        app.toggle_help();
        return outcome;
    }
    if app.help_open {
        match key.code {
            KeyCode::Esc => app.help_open = false,
            KeyCode::Char('c') if ctrl => app.help_open = false,
            KeyCode::Up => help::scroll(app, -1),
            KeyCode::Down => help::scroll(app, 1),
            KeyCode::PageUp => help::scroll(app, -(app.help_area.height.max(1) as isize)),
            KeyCode::PageDown => help::scroll(app, app.help_area.height.max(1) as isize),
            KeyCode::Home => app.help_scroll = 0,
            KeyCode::End => help::scroll(app, isize::MAX),
            _ => {}
        }
        return outcome;
    }
    if key.code == KeyCode::Char('?')
        && !ctrl
        && !alt
        && app.focus == Focus::Input
        && app.log_view.is_none()
        && editor.is_empty()
    {
        app.toggle_help();
        return outcome;
    }

    if ctrl && key.code == KeyCode::Char('c') {
        outcome.quit = handle_ctrl_c(app, editor);
        return outcome;
    }

    // Conversation start/end from any focus; the log sub-view gives way first.
    if ctrl && key.code == KeyCode::Home {
        if app.log_view.is_some() {
            log::close(app);
        }
        app.home();
        return outcome;
    }
    if ctrl && key.code == KeyCode::End {
        if app.log_view.is_some() {
            log::close(app);
        }
        app.end();
        return outcome;
    }

    // Alt+Up/Down scrolls one line in any focus, including the log sub-view.
    if alt {
        match key.code {
            KeyCode::Up => {
                if app.log_view.is_some() {
                    view::log_scroll(app, -1);
                } else {
                    view::page_up(app, 1);
                }
                return outcome;
            }
            KeyCode::Down => {
                if app.log_view.is_some() {
                    view::log_scroll(app, 1);
                } else {
                    view::page_down(app, 1);
                }
                return outcome;
            }
            _ => {}
        }
    }

    if app.log_view.is_some() {
        match key.code {
            KeyCode::Esc => log::close(app),
            KeyCode::Down => view::log_scroll(app, 1),
            KeyCode::Up => view::log_scroll(app, -1),
            KeyCode::PageDown => view::log_page_nav(app, true),
            KeyCode::PageUp => view::log_page_nav(app, false),
            _ => {}
        }
        return outcome;
    }

    if key.code == KeyCode::Esc && !ctrl && !alt {
        // Local UI first: Esc clears an active selection before agent work.
        if had_selection {
            app.text_selection.clear();
            return outcome;
        }
        if app.running && !app.cancelling {
            outcome.cancel = true;
            return outcome;
        }
        if app.focus == Focus::Transcript {
            app.focus = Focus::Input;
        }
        return outcome;
    }

    let page = app.last_height.saturating_sub(1).max(1);
    match key.code {
        KeyCode::PageUp => {
            view::page_up(app, page);
            return outcome;
        }
        KeyCode::PageDown => {
            view::page_down(app, page);
            return outcome;
        }
        KeyCode::Tab => {
            match app.focus {
                Focus::Input => view::focus_transcript(app),
                Focus::Transcript => app.focus = Focus::Input,
            }
            return outcome;
        }
        _ => {}
    }

    match app.focus {
        Focus::Transcript => {
            match key.code {
                KeyCode::Up if shift => view::select(app, -1),
                KeyCode::Down if shift => view::select(app, 1),
                KeyCode::Up => view::page_up(app, 1),
                KeyCode::Down => view::page_down(app, 1),
                KeyCode::Home => app.home(),
                KeyCode::End => app.end(),
                KeyCode::Enter => {
                    app.toggle_selected();
                }
                KeyCode::Char('y') => outcome.copy = view::copy_source(app),
                KeyCode::Char('Y') => outcome.copy = view::copy_code_source(app),
                KeyCode::Char('l') => {
                    log::open(app);
                }
                _ => {}
            }
            outcome
        }
        // Editing and history keys only apply to the input focus.
        Focus::Input => input_key(app, editor, key),
    }
}

/// Input-focus key map. Kept separate so record/log bindings cannot regress.
fn input_key(app: &mut App, editor: &mut Editor, key: KeyEvent) -> KeyOutcome {
    let mut outcome = KeyOutcome {
        send: None,
        command: None,
        copy: None,
        paste_clipboard: false,
        cancel: false,
        quit: false,
    };
    let ctrl = key.modifiers.contains(KeyModifiers::CONTROL);
    let alt = key.modifiers.contains(KeyModifiers::ALT);
    let shift = key.modifiers.contains(KeyModifiers::SHIFT);
    match key.code {
        KeyCode::Enter if alt || shift => editor.insert_newline(),
        KeyCode::Enter => {
            if !editor.is_empty() {
                match command::parse(&editor.text(), !editor.images().is_empty()) {
                    Ok(ParsedInput::Command(command)) => outcome.command = Some(command),
                    Ok(ParsedInput::Message(text)) if !app.running => {
                        let mut message = editor.take();
                        message.text = text;
                        outcome.send = Some(message);
                    }
                    Ok(ParsedInput::Message(_)) => {
                        app.status =
                            Some("Draft kept. Send again when the task finishes.".to_owned())
                    }
                    Err(error) => app.notify(error.to_string()),
                }
            }
        }
        KeyCode::Char('j') if ctrl => editor.insert_newline(),
        KeyCode::Char('b') if ctrl => editor.move_left(),
        KeyCode::Char('f') if ctrl => editor.move_right(),
        KeyCode::Char('b') if alt => editor.move_word_left(),
        KeyCode::Char('f') if alt => editor.move_word_right(),
        KeyCode::Char('a') if ctrl => editor.move_line_start(),
        KeyCode::Char('e') if ctrl => editor.move_line_end(),
        KeyCode::Char('w') if ctrl => editor.delete_word_backward(),
        KeyCode::Char('d') if alt => editor.delete_word_forward(),
        KeyCode::Char('d') if ctrl => {
            if editor.is_empty() {
                outcome.quit = exit_or_wait(app);
            } else {
                editor.delete();
            }
        }
        KeyCode::Char('u') if ctrl => editor.kill_to_line_start(),
        KeyCode::Char('k') if ctrl => editor.kill_to_line_end(),
        KeyCode::Char('y') if ctrl => editor.yank(),
        KeyCode::Char('h') if ctrl => editor.backspace(),
        KeyCode::Char('p') if ctrl => navigate_or_move(app, editor, false),
        KeyCode::Char('n') if ctrl => navigate_or_move(app, editor, true),
        KeyCode::Left if ctrl || alt => editor.move_word_left(),
        KeyCode::Right if ctrl || alt => editor.move_word_right(),
        KeyCode::Left => editor.move_left(),
        KeyCode::Right => editor.move_right(),
        KeyCode::Backspace if ctrl || alt => editor.delete_word_backward(),
        KeyCode::Backspace => editor.backspace(),
        KeyCode::Delete if ctrl || alt => editor.delete_word_forward(),
        KeyCode::Delete => editor.delete(),
        KeyCode::Up => navigate_or_move(app, editor, false),
        KeyCode::Down => navigate_or_move(app, editor, true),
        KeyCode::Home => editor.home(),
        KeyCode::End => editor.end(),
        // Alt and Ctrl letters are shortcuts; they must not be inserted.
        KeyCode::Char(character) if !ctrl && !alt => editor.insert_str(&character.to_string()),
        _ => {}
    }
    outcome
}

/// Up/Down (and Ctrl+P/N) share visual movement and history recall. History is
/// used only when the draft is empty, or still equals the recalled entry with
/// the cursor at the document boundary.
fn navigate_or_move(app: &mut App, editor: &mut Editor, forward: bool) {
    // Compare fields instead of building a full draft so large base64 snapshots
    // are never cloned on every Up/Down keypress.
    let empty = editor.text().is_empty() && editor.images().is_empty();
    let navigating = if empty {
        !app.history.is_empty()
    } else if let Some(recalled) = app.history.recalled() {
        editor.text() == recalled.text
            && editor.images() == recalled.images.as_slice()
            && editor.at_document_boundary()
    } else {
        false
    };
    if !navigating {
        if forward {
            editor.move_visual_down(app.input_width);
        } else {
            editor.move_visual_up(app.input_width);
        }
        return;
    }
    if forward {
        let was = app.history.navigating();
        match app.history.newer() {
            Some(entry) => editor.set_draft(entry),
            None if was => editor.clear(),
            None => {}
        }
    } else if let Some(entry) = app.history.older() {
        editor.set_draft(entry);
    }
}

/// Ctrl+C: close the log sub-view first, else save and clear a non-empty draft
/// (even while busy, without requesting exit), else use the shared exit path.
fn handle_ctrl_c(app: &mut App, editor: &mut Editor) -> bool {
    if app.log_view.is_some() {
        log::close(app);
        return false;
    }
    if !editor.is_empty() {
        app.history.push(editor.take());
        app.focus = Focus::Input;
        app.status = None;
        return false;
    }
    exit_or_wait(app)
}

/// The one exit path shared by Ctrl+C and Ctrl+D on an empty input: quit when
/// idle, otherwise ask the worker to finish and wait truthfully.
fn exit_or_wait(app: &mut App) -> bool {
    if app.running {
        app.quit_after_turn = true;
        app.status = Some("Waiting for the current task before exiting.".to_owned());
        false
    } else {
        true
    }
}

/// Footer actions and help scrolling are handled before transcript routing.
/// Dragging copies visible text; navigation never edits the draft or its focus.
/// Returns text for the event loop to write to the clipboard.
pub fn handle_mouse(app: &mut App, mouse: MouseEvent) -> Option<String> {
    let position = ratatui::layout::Position::new(mouse.column, mouse.row);
    if mouse.kind == MouseEventKind::Down(MouseButton::Left) {
        app.text_selection.clear();
        app.disclosure_press = None;
        if app.help_button_area.contains(position) {
            app.toggle_help();
        } else if !app.help_open && app.latest_button_area.contains(position) {
            if app.log_view.is_some() {
                log::close(app);
            }
            app.end();
        } else {
            if !app.help_open && app.log_view.is_none() {
                app.disclosure_press = app
                    .disclosure_buttons
                    .iter()
                    .find(|(area, _)| area.contains(position))
                    .map(|(_, index)| *index);
            }
            let transcript =
                !app.help_open && app.log_view.is_none() && app.transcript_area.contains(position);
            // Pin the mapped viewport at mouse-down so a potential drag cannot
            // be disturbed by new output; a click without a drag restores
            // follow on release.
            let was_following = app.follow;
            if transcript && was_following {
                view::page_up(app, 0);
            }
            let started = transcript && {
                let viewport = app.transcript_rows.clone();
                app.text_selection.start_transcript(
                    app.transcript_area,
                    position,
                    &viewport,
                    was_following,
                )
            };
            if !started {
                if was_following {
                    app.end();
                }
                app.text_selection.start(position);
            }
        }
        return None;
    }
    match mouse.kind {
        MouseEventKind::Drag(MouseButton::Left) => {
            app.disclosure_press = None;
            if app.text_selection.is_transcript() {
                if app.follow {
                    view::page_up(app, 0);
                }
                view::selection_drag(app, position);
            } else {
                app.text_selection.drag(position);
            }
            return None;
        }
        MouseEventKind::Up(MouseButton::Left) => {
            let pressed = app.disclosure_press.take();
            let restore_follow = app.text_selection.take_follow_restore();
            let viewport = app.transcript_rows.clone();
            let area = app.transcript_area;
            let copied = app.text_selection.finish(position, area, &viewport);
            if copied.is_none() && !app.help_open && app.log_view.is_none() {
                // Freeze the viewport before reflow so a long thought, tool or
                // activity detail opens from its beginning.
                let toggled = pressed.is_some_and(|index| {
                    app.disclosure_buttons
                        .iter()
                        .any(|(area, record)| *record == index && area.contains(position))
                });
                if let Some(index) = pressed.filter(|_| toggled) {
                    view::page_up(app, 0);
                    app.toggle_record(index);
                    app.text_selection.clear();
                } else if restore_follow {
                    // A click without a drag never permanently leaves follow.
                    app.end();
                }
                // A same-cell click is not a selection; never let an invisible
                // zero-size selection shadow Esc.
                app.text_selection.clear();
            }
            return copied;
        }
        MouseEventKind::ScrollUp | MouseEventKind::ScrollDown => {
            app.disclosure_press = None;
            app.text_selection.clear();
        }
        _ => {}
    }
    if app.help_open {
        if app.help_area.contains(position) {
            match mouse.kind {
                MouseEventKind::ScrollUp => help::scroll(app, -3),
                MouseEventKind::ScrollDown => help::scroll(app, 3),
                _ => {}
            }
        }
        return None;
    }
    let over_pane = app.transcript_area.contains(position);
    if !over_pane {
        return None;
    }
    match mouse.kind {
        MouseEventKind::ScrollUp => {
            if app.log_view.is_some() {
                view::log_scroll(app, -3);
            } else {
                view::page_up(app, 3);
            }
        }
        MouseEventKind::ScrollDown => {
            if app.log_view.is_some() {
                view::log_scroll(app, 3);
            } else {
                view::page_down(app, 3);
            }
        }
        _ => {}
    }
    None
}

#[cfg(test)]
#[path = "../../../tests/tui/mod.rs"]
mod tests;

fn copy_to_clipboard(text: &str) -> Result<(), String> {
    let mut clipboard = arboard::Clipboard::new().map_err(|error| error.to_string())?;
    clipboard.set_text(text).map_err(|error| error.to_string())
}

/// Terminal paste or drag: load a single image path, otherwise insert text.
/// A failed explicit image path keeps the original paste as editable text and
/// never clears existing attachments.
fn handle_paste_text(app: &mut App, editor: &mut Editor, text: &str) {
    app.help_open = false;
    if app.log_view.is_some() {
        log::close(app);
    }
    app.focus = Focus::Input;
    match message::classify_image_paste(text, |path| path.is_file()) {
        Some(paste) => {
            if let Err(error) = attach_image(app, editor, paste) {
                app.status = Some(error);
                editor.insert_str(text);
            }
        }
        None => editor.insert_str(text),
    }
}

/// Ctrl+V: clipboard image first, else clipboard text (which may itself be an
/// image path).
fn handle_clipboard(app: &mut App, editor: &mut Editor) {
    let mut clipboard = match arboard::Clipboard::new() {
        Ok(clipboard) => clipboard,
        Err(error) => {
            app.status = Some(format!("Clipboard unavailable: {error}"));
            return;
        }
    };
    match clipboard.get_image() {
        Ok(image) => match encode_clipboard_png(&image) {
            Ok(bytes) => {
                editor.insert_image(ImageAttachment::from_bytes(
                    "clipboard.png",
                    "image/png",
                    &bytes,
                ));
                app.status = None;
            }
            Err(error) => app.status = Some(error),
        },
        Err(_) => match clipboard.get_text() {
            Ok(text) => handle_paste_text(app, editor, &text),
            Err(error) => app.status = Some(format!("Clipboard read failed: {error}")),
        },
    }
}

fn attach_image(app: &mut App, editor: &mut Editor, path: PathBuf) -> Result<(), String> {
    let path = expand_home(&path);
    let bytes = std::fs::read(&path).map_err(|error| format!("Could not read image: {error}"))?;
    let mime = message::image_mime_from_bytes(&bytes)
        .ok_or_else(|| "Unsupported image format".to_owned())?;
    // Decode once so truncated or corrupt files are rejected here instead of
    // being sent as a broken data URL.
    image::load_from_memory(&bytes).map_err(|error| format!("Could not decode image: {error}"))?;
    let name = path
        .file_name()
        .and_then(|name| name.to_str())
        .unwrap_or("image")
        .to_owned();
    editor.insert_image(ImageAttachment::from_bytes(name, mime, &bytes));
    app.status = None;
    Ok(())
}

/// Expands a leading `~/` (the only shell form we accept) so drag paths can be
/// opened relative to the user's home directory.
fn expand_home(path: &std::path::Path) -> PathBuf {
    let text = path.to_string_lossy();
    if let Some(rest) = text.strip_prefix("~/")
        && let Some(home) = std::env::var_os("HOME")
    {
        return PathBuf::from(home).join(rest);
    }
    path.to_path_buf()
}

fn encode_clipboard_png(image: &arboard::ImageData<'_>) -> Result<Vec<u8>, String> {
    let buffer = image::RgbaImage::from_raw(
        image.width as u32,
        image.height as u32,
        image.bytes.to_vec(),
    )
    .ok_or_else(|| "Invalid clipboard image dimensions".to_owned())?;
    let mut cursor = std::io::Cursor::new(Vec::new());
    image::DynamicImage::ImageRgba8(buffer)
        .write_to(&mut cursor, image::ImageFormat::Png)
        .map_err(|error| format!("Could not encode image: {error}"))?;
    Ok(cursor.into_inner())
}
