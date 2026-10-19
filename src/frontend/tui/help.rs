use ratatui::{
    Frame,
    layout::Rect,
    style::{Modifier, Style},
    text::{Line, Span, Text},
    widgets::Paragraph,
};

use super::{state::App, textwrap};

fn lines(app: &App, width: usize) -> Vec<Line<'static>> {
    let mut entries = vec![(true, "Session".to_owned())];
    entries.push((
        false,
        "Ctrl+S or click the session title  Open session picker".to_owned(),
    ));
    entries.push((
        false,
        "In picker: Enter open; n new; r rename; / search; Esc close".to_owned(),
    ));
    entries.push((
        false,
        "In picker search: type to filter; Up/Down select; Enter open; Esc clears, Esc again closes"
            .to_owned(),
    ));
    if let Some(session) = &app.session {
        entries.push((false, format!("Title: {}", session.title)));
        entries.push((false, format!("Session ID: {}", session.id)));
    }
    if let Some(directory) = &app.working_directory {
        entries.push((false, format!("Directory: {}", directory.display())));
    }
    if let Some(context) = &app.context {
        entries.push((false, format!("Model: {}", context.model)));
        entries.push((
            false,
            format!(
                "Context: ~{} / {} tokens (estimate; excludes unsent draft)",
                context
                    .tokens
                    .map_or_else(|| "unknown".to_owned(), |n| n.to_string()),
                context
                    .window
                    .map_or_else(|| "unknown".to_owned(), |n| n.to_string())
            ),
        ));
        entries.push((
            false,
            format!(
                "Input limit: {} tokens",
                context
                    .input_limit
                    .map_or_else(|| "unknown".to_owned(), |n| n.to_string())
            ),
        ));
    }
    if let Some((input, output)) = app.usage {
        entries.push((
            false,
            format!(
                "Last response: input {} / output {} tokens",
                input.map_or_else(|| "unknown".to_owned(), |n| n.to_string()),
                output.map_or_else(|| "unknown".to_owned(), |n| n.to_string())
            ),
        ));
    }
    entries.push((true, "Local commands".to_owned()));
    for definition in crate::command::DEFINITIONS {
        entries.push((
            false,
            format!("{}  {}", definition.usage, definition.description),
        ));
    }
    entries.push((
        false,
        "Tab completes a command; Up/Down select; Esc closes completion".to_owned(),
    ));
    entries.push((
        false,
        "Use // to send literal slash text; multiline input stays a message".to_owned(),
    ));
    entries.extend([
        (
            false,
            "Tab  Switch input / records; Esc  Close search/help/log or clear selection first"
                .to_owned(),
        ),
        (
            false,
            "Esc while running  Interrupt the task after local views close; the draft is kept"
                .to_owned(),
        ),
        (
            false,
            "Ctrl+C  Copy selection, else close help/log, save and clear draft, or request exit; it never interrupts a task, and exit waits for it"
                .to_owned(),
        ),
        (true, "Input".to_owned()),
        (
            false,
            "Enter  Send; Alt/Shift+Enter, Ctrl+J  New line".to_owned(),
        ),
        (
            false,
            "Ctrl+V  Paste image or text from any view; paste an image path to attach it"
                .to_owned(),
        ),
        (
            false,
            "Backspace  Remove last image when text is empty".to_owned(),
        ),
        (
            false,
            "Up/Down, Ctrl+P/N  Move cursor; recall history from an empty draft".to_owned(),
        ),
        (
            false,
            "Ctrl+R  Search input history; Enter inserts without sending; Esc/Ctrl+C keeps the draft"
                .to_owned(),
        ),
        (
            false,
            "In history search: Ctrl+R/Up  Older match; Down  Newer match".to_owned(),
        ),
        (
            false,
            "Ctrl+B/F  Move by character; Ctrl/Alt+Left/Right, Alt+B/F  Move by word".to_owned(),
        ),
        (
            false,
            "Ctrl+A/E  Line start/end; Home/End  Current line start/end".to_owned(),
        ),
        (
            false,
            "Ctrl+W, Ctrl/Alt+Backspace  Delete previous word".to_owned(),
        ),
        (false, "Alt+D, Ctrl/Alt+Delete  Delete next word".to_owned()),
        (
            false,
            "Ctrl+U/K  Kill to line start/end; Ctrl+Y  Restore last word/line deletion".to_owned(),
        ),
        (
            false,
            "Alt+U / Alt+R  Undo/redo the draft; consecutive typing is one step".to_owned(),
        ),
        (
            false,
            "Ctrl+H/D  Delete previous/next character; Ctrl+D on empty input exits".to_owned(),
        ),
        (true, "Records".to_owned()),
        (false, "Click Thought  Show or hide reasoning".to_owned()),
        (
            false,
            "Drag  Select conversation text; hold at the top/bottom edge to keep scrolling; release copies"
                .to_owned(),
        ),
        (
            false,
            "Ctrl+C  Copy selected text; otherwise clear draft or exit".to_owned(),
        ),
        (
            false,
            "Wheel / PgUp/PgDn  Scroll; Alt+Up/Down  Scroll from any focus".to_owned(),
        ),
        (
            false,
            "Ctrl+Home/End  First/latest; Home/End also work in records".to_owned(),
        ),
        (
            false,
            "In records: Up/Down  Scroll; Shift+Up/Down  Select record".to_owned(),
        ),
        (
            false,
            "In records: Enter  Toggle tool/reasoning; y  Copy source/command".to_owned(),
        ),
        (
            false,
            "In records: Y  Copy code/tool output; l  Open tool log".to_owned(),
        ),
        (
            false,
            "In log: Up/Down  Scroll; PgUp/PgDn  Change page; Esc  Return".to_owned(),
        ),
        (true, "Help".to_owned()),
        (
            false,
            "F1, ? on empty input, or click Help  Open help".to_owned(),
        ),
        (
            false,
            "Up/Down, PgUp/PgDn  Scroll help; Home/End  First/last".to_owned(),
        ),
        (
            false,
            "Esc / F1 / Ctrl+C  Close help and keep draft and reading position".to_owned(),
        ),
    ]);
    let mut lines = Vec::new();
    for (heading, text) in entries {
        if heading && !lines.is_empty() {
            lines.push(Line::default());
        }
        let style = if heading {
            Style::default()
                .fg(app.theme.accent)
                .add_modifier(Modifier::BOLD)
        } else {
            Style::default().fg(app.theme.secondary)
        };
        for (part, _) in textwrap::wrap_bytes(&text, width.max(1)) {
            lines.push(Line::from(Span::styled(part, style)));
        }
    }
    lines
}

pub fn scroll(app: &mut App, delta: isize) {
    let height = app.help_area.height as usize;
    let body_height = height.saturating_sub(usize::from(height > 1));
    let max = lines(app, app.help_area.width as usize)
        .len()
        .saturating_sub(body_height);
    app.help_scroll = app.help_scroll.saturating_add_signed(delta).min(max);
}

pub fn draw(frame: &mut Frame, app: &mut App, area: Rect) {
    if area.is_empty() {
        return;
    }
    let header_height = u16::from(area.height > 1);
    if header_height > 0 {
        frame.render_widget(
            Paragraph::new("Help · Up/Down to scroll · Esc to close")
                .style(Style::default().fg(app.theme.accent)),
            Rect::new(area.x, area.y, area.width, 1),
        );
    }
    let body = Rect::new(
        area.x,
        area.y + header_height,
        area.width,
        area.height - header_height,
    );
    let lines = lines(app, body.width as usize);
    app.help_scroll = app
        .help_scroll
        .min(lines.len().saturating_sub(body.height as usize));
    frame.render_widget(
        Paragraph::new(Text::from(
            lines
                .into_iter()
                .skip(app.help_scroll)
                .take(body.height as usize)
                .collect::<Vec<_>>(),
        )),
        body,
    );
}
