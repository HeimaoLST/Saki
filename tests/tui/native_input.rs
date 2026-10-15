use super::*;
use crate::frontend::tui::terminal_input::map_event;
use termina::Parser;

fn map(chunks: &[&[u8]]) -> Vec<Event> {
    let mut parser = Parser::default();
    for (index, chunk) in chunks.iter().enumerate() {
        parser.parse(chunk, index + 1 < chunks.len());
    }
    let mut mapped = Vec::new();
    while let Some(event) = parser.pop() {
        if let Some(event) = map_event(event) {
            mapped.push(event);
        }
    }
    mapped
}

#[test]
fn real_termina_key_bytes_map_to_existing_editing_bindings() {
    let mut app = App::new();
    let mut editor = Editor::new();
    editor.insert_str("draft");

    // Ctrl+C writes control bytes; Termina maps them to Char('c') + CONTROL.
    for event in map(&[b"\x03"]) {
        let outcome = handle_key(&mut app, &mut editor, as_key(&event));
        assert!(!outcome.quit);
    }
    assert!(editor.is_empty(), "Ctrl+C clears the draft first");
    for event in map(&[b"\x03"]) {
        let outcome = handle_key(&mut app, &mut editor, as_key(&event));
        assert!(outcome.quit, "Ctrl+C on empty input exits");
    }

    // Alt+Left/Right keep their existing word movement.
    editor.insert_str("one two");
    for event in map(&[b"\x1b[1;3", b"D"]) {
        handle_key(&mut app, &mut editor, as_key(&event));
    }
    assert_eq!(editor.cursor().1, 4, "Alt+Left moves to the previous word");
    for event in map(&[b"\x1b[1;3", b"C"]) {
        handle_key(&mut app, &mut editor, as_key(&event));
    }
    assert_eq!(editor.cursor().1, 7, "Alt+Right moves to the next word");

    // Alt+Enter still inserts a newline instead of sending.
    editor.clear();
    editor.insert_str("line");
    for event in map(&[b"\x1b\r"]) {
        handle_key(&mut app, &mut editor, as_key(&event));
    }
    assert_eq!(editor.text(), "line\n", "Alt+Enter inserts a newline");
}

#[test]
fn real_termina_mouse_bytes_drive_scroll_and_drag() {
    let mut app = App::new();
    let editor = Editor::new();
    app.push(assistant(&code_fixture(30)));
    app.last_width = 40;
    app.last_height = 4;
    app.transcript_area = Rect::new(0, 0, 40, 4);
    app.focus = Focus::Input;
    app.follow = false;
    let layout = view::record_layout(&mut app, 0, 40);
    let start = 15;
    app.anchor = Some(Anchor {
        record: 0,
        offset: layout[start].source_start,
    });

    // SGR wheel-up (button 64) at column 10, row 2.
    for event in map(&[b"\x1b[<64;10;2M"]) {
        let _ = handle_mouse(&mut app, as_mouse(&event));
    }
    assert_eq!(
        line_index(&layout, app.anchor.unwrap().offset),
        start - 3,
        "wheel event path scrolls exactly three lines"
    );

    drop(app);
    let mut app = App::new();
    app.push(assistant("alpha 你好🙂e\u{301} omega"));
    render_app(&mut app, &editor, 48);
    let _ = handle_mouse(&mut app, as_mouse(&map(&[b"\x1b[<0;11;1M"])[0]));
    let _ = handle_mouse(&mut app, as_mouse(&map(&[b"\x1b[<32;17;1M"])[0]));
    let copied = handle_mouse(&mut app, as_mouse(&map(&[b"\x1b[<0;17;1m"])[0]));
    assert_eq!(copied.as_deref(), Some("你好🙂e\u{301}"));
    assert_eq!(editor.text(), "");
}

#[test]
fn real_termina_bracketed_paste_keeps_unicode_and_escape_looking_text_literal() {
    let mut app = App::new();
    let mut editor = Editor::new();
    let content = "第一行\nsecond 🙂 \x1b]11;rgb:0000/0000/0000\x1b\\ tail";
    let mut stream = Vec::new();
    stream.extend_from_slice(b"\x1b[200~");
    stream.extend_from_slice(content.as_bytes());
    stream.extend_from_slice(b"\x1b[201~");
    let split = stream.len() / 3;
    let events = map(&[
        &stream[..split],
        &stream[split..split * 2],
        &stream[split * 2..],
    ]);
    assert_eq!(events.len(), 1, "one paste event, no protocol leakage");
    let Event::Paste(text) = &events[0] else {
        panic!("expected paste, got {:?}", events[0]);
    };
    assert_eq!(text, content, "escape-looking text stays pasted content");
    handle_paste_text(&mut app, &mut editor, text);
    assert_eq!(editor.text(), content);
}

#[test]
fn protocol_replies_interleaved_with_keys_are_never_input() {
    let events = map(&[b"a\x1b]10;rgb:1111/2222/3333", b"\x1b\\b\x1b[?997;2n", b"c"]);
    let keys: Vec<char> = events
        .iter()
        .filter_map(|event| match event {
            Event::Key(key) => match key.code {
                KeyCode::Char(character) => Some(character),
                _ => None,
            },
            _ => None,
        })
        .collect();
    assert_eq!(keys, vec!['a', 'b', 'c'], "no reply bytes become input");
    assert_eq!(events.len(), 3, "OSC and scheme replies are consumed");
}

fn as_key(event: &Event) -> KeyEvent {
    match event {
        Event::Key(key) => *key,
        other => panic!("expected key, got {other:?}"),
    }
}

fn as_mouse(event: &Event) -> MouseEvent {
    match event {
        Event::Mouse(mouse) => *mouse,
        other => panic!("expected mouse, got {other:?}"),
    }
}

fn render_app(app: &mut App, editor: &Editor, width: u16) {
    let mut terminal = Terminal::new(TestBackend::new(width, 12)).unwrap();
    terminal
        .draw(|frame| view::draw(frame, app, editor))
        .unwrap();
}
