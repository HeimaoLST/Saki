use super::*;

fn draw(app: &mut App, editor: &Editor, width: u16) -> Terminal<TestBackend> {
    let mut terminal = Terminal::new(TestBackend::new(width, 12)).unwrap();
    terminal
        .draw(|frame| view::draw(frame, app, editor))
        .unwrap();
    terminal
}

#[test]
fn drag_copies_and_highlights_whole_unicode_graphemes_without_touching_the_draft() {
    let mut app = App::new();
    app.theme = crate::frontend::tui::theme::Theme::dark();
    let mut editor = Editor::new();
    editor.insert_str("my draft");
    app.push(assistant("alpha 你好🙂e\u{301} omega"));
    draw(&mut app, &editor, 48);
    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Down(MouseButton::Left), 10, 0),
    );
    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Drag(MouseButton::Left), 16, 0),
    );
    assert_eq!(app.text_selection.text().as_deref(), Some("你好🙂e\u{301}"));
    let terminal = draw(&mut app, &editor, 48);
    assert_eq!(terminal.backend().buffer()[(9, 0)].bg, app.theme.accent);
    assert_eq!(terminal.backend().buffer()[(11, 0)].bg, app.theme.accent);
    assert_eq!(
        handle_mouse(
            &mut app,
            mouse(MouseEventKind::Up(MouseButton::Left), 16, 0)
        ),
        Some("你好🙂e\u{301}".to_owned())
    );
    assert_eq!(app.focus, Focus::Input);
    assert_eq!(editor.text(), "my draft");
    let outcome = handle_key(
        &mut app,
        &mut editor,
        KeyEvent::new(KeyCode::Char('c'), KeyModifiers::CONTROL),
    );
    assert_eq!(outcome.copy.as_deref(), Some("你好🙂e\u{301}"));
    assert!(!outcome.quit);
    assert_eq!(editor.text(), "my draft");
    assert!(app.text_selection.text().is_none());
}

#[test]
fn reverse_drag_copies_visible_lines_without_terminal_padding() {
    let mut app = App::new();
    let editor = Editor::new();
    app.push(assistant("```text\nfirst line\nsecond line\n```"));
    draw(&mut app, &editor, 48);
    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Down(MouseButton::Left), 14, 2),
    );
    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Drag(MouseButton::Left), 3, 1),
    );
    assert_eq!(
        handle_mouse(&mut app, mouse(MouseEventKind::Up(MouseButton::Left), 3, 1)),
        Some("first line\n   second line".to_owned())
    );
    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Down(MouseButton::Left), 3, 1),
    );
    assert_eq!(
        handle_mouse(&mut app, mouse(MouseEventKind::Up(MouseButton::Left), 3, 1)),
        None
    );
}

#[test]
fn selection_survives_metadata_updates_but_never_copies_changed_or_resized_content() {
    let mut app = App::new();
    let editor = Editor::new();
    app.push(assistant("first line"));
    draw(&mut app, &editor, 48);
    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Down(MouseButton::Left), 3, 0),
    );
    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Drag(MouseButton::Left), 8, 0),
    );
    app.apply_event(AgentEvent::ContextUpdated {
        model: "test".to_owned(),
        context_tokens: Some(1500),
        context_window: Some(65_536),
        input_limit: Some(57_344),
    });
    draw(&mut app, &editor, 48);
    assert_eq!(app.text_selection.text().as_deref(), Some("first"));
    draw(&mut app, &editor, 32);
    assert!(app.text_selection.text().is_none());
    assert_eq!(
        handle_mouse(&mut app, mouse(MouseEventKind::Up(MouseButton::Left), 8, 0)),
        None
    );
    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Down(MouseButton::Left), 3, 0),
    );
    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Drag(MouseButton::Left), 8, 0),
    );
    app.records[0] = assistant("other line");
    app.layouts[0] = None;
    draw(&mut app, &editor, 32);
    assert!(app.text_selection.text().is_none());
    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Down(MouseButton::Left), 3, 0),
    );
    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Drag(MouseButton::Left), 8, 0),
    );
    let _ = handle_mouse(&mut app, mouse(MouseEventKind::ScrollUp, 5, 0));
    assert_eq!(
        handle_mouse(&mut app, mouse(MouseEventKind::Up(MouseButton::Left), 8, 0)),
        None
    );
}

#[test]
fn clipboard_paste_returns_to_input_from_records_and_help_without_sending() {
    let mut app = App::new();
    let mut editor = Editor::new();
    editor.insert_str("draft ");
    app.focus = Focus::Transcript;
    app.help_open = true;
    let outcome = handle_key(
        &mut app,
        &mut editor,
        KeyEvent::new(KeyCode::Char('v'), KeyModifiers::CONTROL),
    );
    assert!(outcome.paste_clipboard);
    assert!(outcome.send.is_none());
    assert_eq!(app.focus, Focus::Input);
    assert!(!app.help_open);
    app.focus = Focus::Transcript;
    crate::frontend::tui::handle_paste_text(&mut app, &mut editor, "中文🙂\r\nnext");
    assert_eq!(app.focus, Focus::Input);
    assert_eq!(editor.text(), "draft 中文🙂\nnext");
    app.log_view = Some(crate::frontend::tui::state::LogView {
        path: "fixture.log".to_owned(),
        offset: 0,
        next: 0,
        eof: true,
        text: String::new(),
        scroll: 0,
        history: Vec::new(),
        saved_follow: false,
        saved_anchor: None,
        saved_visual_anchor: None,
    });
    crate::frontend::tui::handle_paste_text(&mut app, &mut editor, " pasted from log");
    assert!(app.log_view.is_none());
    assert_eq!(app.focus, Focus::Input);
    assert!(
        !app.follow,
        "paste preserves the reading position restored from the log"
    );
    let outcome = handle_key(&mut app, &mut editor, key(KeyCode::Enter));
    assert_eq!(
        outcome.send.unwrap().text,
        "draft 中文🙂\nnext pasted from log"
    );
}

fn draw_with_height(
    app: &mut App,
    editor: &Editor,
    width: u16,
    height: u16,
) -> Terminal<TestBackend> {
    let mut terminal = Terminal::new(TestBackend::new(width, height)).unwrap();
    terminal
        .draw(|frame| view::draw(frame, app, editor))
        .unwrap();
    terminal
}

fn code_block(prefix: &str, count: usize) -> String {
    let mut text = String::from("```\n");
    for index in 0..count {
        text.push_str(&format!("{prefix}{index:02}\n"));
    }
    text.push_str("```");
    text
}

fn markers(text: &str) -> Vec<String> {
    text.lines()
        .map(|line| line.trim_start().to_owned())
        .filter(|line| !line.is_empty())
        .collect()
}

fn select_to(app: &mut App, column: u16, row: u16) {
    let _ = handle_mouse(
        app,
        mouse(MouseEventKind::Drag(MouseButton::Left), column, row),
    );
}

#[test]
fn drag_down_across_many_viewports_copies_every_row_once_in_order() {
    let mut app = App::new();
    let editor = Editor::new();
    app.push(assistant(&code_block("line ", 60)));
    app.home();
    draw_with_height(&mut app, &editor, 40, 12);
    let area = app.transcript_area;
    assert!(area.height >= 3, "need a multi-row viewport");

    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Down(MouseButton::Left), area.x, area.y),
    );
    select_to(&mut app, area.right() - 1, area.bottom() - 1);
    let mut ticks = 0;
    while view::selection_scroll_tick(&mut app) {
        ticks += 1;
        assert!(ticks < 300, "auto-scroll must reach the document limit");
    }
    let copied = handle_mouse(
        &mut app,
        mouse(
            MouseEventKind::Up(MouseButton::Left),
            area.right() - 1,
            area.bottom() - 1,
        ),
    )
    .expect("release copies the selection");
    let copied_markers = markers(&copied);
    let expected: Vec<String> = (0..60).map(|index| format!("line {index:02}")).collect();
    assert_eq!(copied_markers, expected);
}

#[test]
fn reverse_drag_up_returns_the_same_ordered_text_without_duplicates() {
    let mut app = App::new();
    let editor = Editor::new();
    app.push(assistant(&code_block("line ", 60)));
    app.home();
    draw_with_height(&mut app, &editor, 40, 12);
    // Scroll to the document end and anchor the drag on the last row.
    view::page_down(&mut app, 1000);
    draw_with_height(&mut app, &editor, 40, 12);
    let area = app.transcript_area;

    let _ = handle_mouse(
        &mut app,
        mouse(
            MouseEventKind::Down(MouseButton::Left),
            area.x,
            area.bottom() - 1,
        ),
    );
    select_to(&mut app, area.x, area.y);
    let mut ticks = 0;
    while view::selection_scroll_tick(&mut app) {
        ticks += 1;
        assert!(ticks < 300);
    }
    let copied = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Up(MouseButton::Left), area.x, area.y),
    )
    .expect("reverse release copies");
    let copied_markers = markers(&copied);
    let expected: Vec<String> = (0..60).map(|index| format!("line {index:02}")).collect();
    assert_eq!(
        copied_markers, expected,
        "reversing must not duplicate or reorder"
    );
}

#[test]
fn stationary_pointer_keeps_ticking_and_stops_cleanly_at_the_limit() {
    let mut app = App::new();
    let editor = Editor::new();
    app.push(assistant(&code_block("line ", 60)));
    app.home();
    draw_with_height(&mut app, &editor, 40, 12);
    let area = app.transcript_area;

    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Down(MouseButton::Left), area.x, area.y),
    );
    select_to(&mut app, area.right() - 1, area.bottom() - 1);
    let pointer = ratatui::layout::Position::new(area.right() - 1, area.bottom() - 1);
    assert_eq!(app.text_selection.pointer(), pointer);
    let before = app.text_selection.cached_rows();
    assert!(
        view::selection_scroll_tick(&mut app),
        "stationary drag advances"
    );
    assert!(app.text_selection.cached_rows() > before);
    assert_eq!(
        app.text_selection.pointer(),
        pointer,
        "pointer does not move"
    );

    while view::selection_scroll_tick(&mut app) {}
    assert!(
        app.text_selection.edge_deadline().is_none(),
        "boundary stops the timer until fresh mouse intent"
    );
    // A new drag event at the edge restarts the timer.
    select_to(&mut app, area.right() - 1, area.bottom() - 1);
    assert!(app.text_selection.edge_deadline().is_some());
}

#[test]
fn interior_and_cancel_stop_the_edge_timer() {
    let mut app = App::new();
    let editor = Editor::new();
    app.push(assistant(&code_block("line ", 30)));
    draw_with_height(&mut app, &editor, 40, 12);
    let area = app.transcript_area;

    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Down(MouseButton::Left), area.x, area.y),
    );
    select_to(&mut app, area.right() - 1, area.bottom() - 1);
    assert!(app.text_selection.edge_deadline().is_some());
    select_to(&mut app, area.x + 2, area.y + 1);
    assert!(
        app.text_selection.edge_deadline().is_none(),
        "interior pointer stops edge scrolling"
    );

    select_to(&mut app, area.right() - 1, area.bottom() - 1);
    let _ = handle_key(&mut app, &mut Editor::new(), key(KeyCode::Esc));
    assert!(!app.text_selection.active());
    assert!(app.text_selection.edge_deadline().is_none());
}

#[test]
fn selection_across_record_boundaries_and_shrinking_stay_ordered() {
    let mut app = App::new();
    let editor = Editor::new();
    app.push(assistant(&code_block("aaa", 12)));
    app.push(assistant(&code_block("bbb", 12)));
    app.home();
    draw_with_height(&mut app, &editor, 40, 12);
    let area = app.transcript_area;

    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Down(MouseButton::Left), area.x, area.y),
    );
    select_to(&mut app, area.right() - 1, area.bottom() - 1);
    while view::selection_scroll_tick(&mut app) {}
    let copied = handle_mouse(
        &mut app,
        mouse(
            MouseEventKind::Up(MouseButton::Left),
            area.right() - 1,
            area.bottom() - 1,
        ),
    )
    .expect("copied across records");
    let copied_markers = markers(&copied);
    let mut expected: Vec<String> = (0..12).map(|index| format!("aaa{index:02}")).collect();
    expected.extend((0..12).map(|index| format!("bbb{index:02}")));
    assert_eq!(copied_markers, expected);

    // A fresh drag that shrinks before release keeps the smaller ordered range.
    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Down(MouseButton::Left), area.x, area.y),
    );
    select_to(&mut app, area.right() - 1, area.bottom() - 1);
    while view::selection_scroll_tick(&mut app) {}
    select_to(&mut app, area.x + 2, area.y + 1);
    let shrunk = handle_mouse(
        &mut app,
        mouse(
            MouseEventKind::Up(MouseButton::Left),
            area.x + 2,
            area.y + 1,
        ),
    )
    .unwrap_or_default();
    let shrunk_markers = markers(&shrunk);
    assert!(!shrunk_markers.is_empty());
    assert!(
        shrunk_markers.len() < copied_markers.len(),
        "shrinking the drag must shorten the copied range"
    );
    assert!(
        shrunk_markers.windows(2).all(|pair| pair[0] < pair[1]),
        "shrunk range stays in display order: {shrunk_markers:?}"
    );
}

#[test]
fn tiny_viewport_has_no_edge_scroll_and_single_clicks_do_not_move() {
    let mut app = App::new();
    let editor = Editor::new();
    app.push(assistant("short"));
    draw_with_height(&mut app, &editor, 24, 5);
    let area = app.transcript_area;
    if area.height <= 1 {
        let _ = handle_mouse(
            &mut app,
            mouse(MouseEventKind::Down(MouseButton::Left), area.x, area.y),
        );
        select_to(&mut app, area.x + 1, area.y);
        assert!(app.text_selection.edge_deadline().is_none());
        let _ = handle_mouse(
            &mut app,
            mouse(MouseEventKind::Up(MouseButton::Left), area.x + 1, area.y),
        );
    }
}

#[test]
fn appending_a_record_while_reading_preserves_the_selected_text() {
    let mut app = App::new();
    let editor = Editor::new();
    app.push(assistant(&code_block("line ", 60)));
    app.home();
    draw_with_height(&mut app, &editor, 40, 12);
    let area = app.transcript_area;

    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Down(MouseButton::Left), area.x, area.y),
    );
    select_to(&mut app, area.right() - 1, area.bottom() - 1);
    while view::selection_scroll_tick(&mut app) {
        assert!(!app.follow, "every moving tick keeps the viewport frozen");
    }
    assert!(!app.follow, "the tail stop keeps the viewport frozen");
    let before_text = app.text_selection.text();
    let before_top = app.transcript_rows.first().unwrap().key;
    assert!(before_text.is_some());

    // Append immediately after the tick that landed on the tail.
    app.push(assistant("appended while reading"));
    draw_with_height(&mut app, &editor, 40, 12);
    assert_eq!(app.text_selection.text(), before_text);
    assert_eq!(app.transcript_rows.first().unwrap().key, before_top);
    assert!(!app.follow, "selection keeps the reader's position");
    assert!(
        !app.text_selection
            .text()
            .is_some_and(|text| text.contains("appended")),
        "new output does not pollute the selection"
    );
}

#[test]
fn long_history_only_caches_visited_rows() {
    let mut app = App::new();
    let editor = Editor::new();
    app.push(assistant(&code_block("row ", 400)));
    draw_with_height(&mut app, &editor, 40, 12);
    let area = app.transcript_area;
    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Down(MouseButton::Left), area.x, area.y),
    );
    select_to(&mut app, area.x + 5, area.y + 1);
    let cached = app.text_selection.cached_rows();
    assert!(
        cached <= area.height as usize + 1,
        "only the visited viewport is cached, got {cached}"
    );
    assert!(cached < 400);
}

#[test]
fn scrolling_short_tail_records_stops_at_the_true_tail_without_jumping_back() {
    let mut app = App::new();
    let editor = Editor::new();
    for index in 0..20 {
        app.push(assistant(&format!("short {index:02}")));
    }
    app.home();
    draw_with_height(&mut app, &editor, 40, 12);
    let area = app.transcript_area;

    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Down(MouseButton::Left), area.x, area.y),
    );
    select_to(&mut app, area.right() - 1, area.bottom() - 1);
    let mut tops = vec![app.transcript_rows.first().unwrap().key];
    let mut ticks = 0;
    while view::selection_scroll_tick(&mut app) {
        ticks += 1;
        tops.push(app.transcript_rows.first().unwrap().key);
        assert!(ticks < 200);
    }
    assert!(ticks > 0, "the fixture must actually scroll");
    assert!(
        tops.windows(2).all(|pair| pair[0] < pair[1]),
        "viewport top rows must advance monotonically: {tops:?}"
    );
    let copied = handle_mouse(
        &mut app,
        mouse(
            MouseEventKind::Up(MouseButton::Left),
            area.right() - 1,
            area.bottom() - 1,
        ),
    )
    .expect("copied tail records");
    let copied_markers = markers(&copied);
    let expected: Vec<String> = (0..20).map(|index| format!("short {index:02}")).collect();
    assert_eq!(
        copied_markers, expected,
        "each tail row appears exactly once"
    );
}

#[test]
fn same_row_reverse_horizontal_drag_copies_the_same_text() {
    let mut forward = App::new();
    let editor = Editor::new();
    forward.push(assistant("alpha beta gamma"));
    draw_with_height(&mut forward, &editor, 48, 12);
    let _ = handle_mouse(
        &mut forward,
        mouse(MouseEventKind::Down(MouseButton::Left), 3, 0),
    );
    let _ = handle_mouse(
        &mut forward,
        mouse(MouseEventKind::Drag(MouseButton::Left), 9, 0),
    );
    let forward_text = handle_mouse(
        &mut forward,
        mouse(MouseEventKind::Up(MouseButton::Left), 9, 0),
    );

    let mut reverse = App::new();
    reverse.push(assistant("alpha beta gamma"));
    draw_with_height(&mut reverse, &editor, 48, 12);
    let _ = handle_mouse(
        &mut reverse,
        mouse(MouseEventKind::Down(MouseButton::Left), 9, 0),
    );
    let _ = handle_mouse(
        &mut reverse,
        mouse(MouseEventKind::Drag(MouseButton::Left), 3, 0),
    );
    let reverse_text = handle_mouse(
        &mut reverse,
        mouse(MouseEventKind::Up(MouseButton::Left), 3, 0),
    );
    assert_eq!(forward_text, reverse_text);
    assert!(forward_text.is_some_and(|text| !text.is_empty()));
}

#[test]
fn held_edge_without_drag_never_scrolls_and_click_restores_follow() {
    let mut app = App::new();
    let editor = Editor::new();
    app.push(assistant(&code_block("line ", 30)));
    draw_with_height(&mut app, &editor, 40, 12);
    let area = app.transcript_area;
    assert!(app.follow);

    // Long press exactly on the bottom edge without a Drag event.
    let _ = handle_mouse(
        &mut app,
        mouse(
            MouseEventKind::Down(MouseButton::Left),
            area.x + 2,
            area.bottom() - 1,
        ),
    );
    assert!(app.text_selection.edge_deadline().is_none());
    assert!(!view::selection_scroll_tick(&mut app));
    let copied = handle_mouse(
        &mut app,
        mouse(
            MouseEventKind::Up(MouseButton::Left),
            area.x + 2,
            area.bottom() - 1,
        ),
    );
    assert!(copied.is_none(), "a same-cell click does not copy");
    assert!(app.follow, "a click without a drag restores follow");
}

#[test]
fn drag_to_the_right_edge_includes_the_last_visible_cell() {
    let mut app = App::new();
    let editor = Editor::new();
    // Width 20 with the 3-cell body margin fits exactly 17 characters.
    app.push(assistant("abcdefghijklmnopq"));
    draw_with_height(&mut app, &editor, 20, 12);
    let area = app.transcript_area;
    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Down(MouseButton::Left), 3, 0),
    );
    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Drag(MouseButton::Left), area.right(), 0),
    );
    let copied = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Up(MouseButton::Left), area.right(), 0),
    )
    .expect("copied to the edge");
    assert_eq!(
        copied.trim_end(),
        "abcdefghijklmnopq",
        "the final cell at the right edge is included"
    );
}

#[test]
fn tool_mutation_at_the_frontend_boundary_clears_cached_selection() {
    let mut app = App::new();
    let editor = Editor::new();
    app.push(Record::Tool {
        call_id: "c1".to_owned(),
        name: "bash".to_owned(),
        arguments: json!({"command": "echo hi"}).to_string(),
        output: None,
        expanded: false,
    });
    draw_with_height(&mut app, &editor, 40, 12);
    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Down(MouseButton::Left), 3, 0),
    );
    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Drag(MouseButton::Left), 9, 0),
    );
    assert!(app.text_selection.active());
    app.apply_event(AgentEvent::ToolFinished {
        call_id: "c1".to_owned(),
        name: "bash".to_owned(),
        output: json!({"output": "hi", "exit_code": 0}),
    });
    assert!(
        !app.text_selection.active(),
        "a mutated selected record clears the cached text"
    );
}

#[test]
fn offscreen_streamed_selection_is_cleared_by_the_agent_mutation_boundary() {
    let mut app = App::new();
    let editor = Editor::new();
    app.push(assistant("first static record"));
    app.apply_event(AgentEvent::ResponseTextDelta {
        item_id: "m".to_owned(),
        delta: "streamed record".to_owned(),
    });
    app.push(assistant(&code_block("tail ", 40)));
    app.home();
    draw_with_height(&mut app, &editor, 40, 12);
    let area = app.transcript_area;

    let _ = handle_mouse(
        &mut app,
        mouse(MouseEventKind::Down(MouseButton::Left), area.x, area.y),
    );
    select_to(&mut app, area.right() - 1, area.bottom() - 1);
    while view::selection_scroll_tick(&mut app) {}
    // The streamed record has scrolled far above the viewport but is cached.
    assert!(app.text_selection.cached_rows() > area.height as usize);
    assert!(!app.transcript_rows.iter().any(|row| row.key.record == 1));

    app.apply_event(AgentEvent::ResponseTextDelta {
        item_id: "m".to_owned(),
        delta: " changed".to_owned(),
    });
    assert!(
        !app.text_selection.active(),
        "a mutation of an offscreen cached record clears the selection"
    );
    assert!(app.text_selection.text().is_none());
}
