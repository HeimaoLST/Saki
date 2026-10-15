use super::*;

fn ctrl(code: KeyCode) -> KeyEvent {
    key_with(code, KeyModifiers::CONTROL)
}

fn alt(code: KeyCode) -> KeyEvent {
    key_with(code, KeyModifiers::ALT)
}

#[test]
fn ctrl_b_f_and_alt_letters_move_by_grapheme_and_word() {
    let mut app = App::new();
    let mut editor = Editor::new();
    editor.insert_str("a👨\u{200d}👩b");
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('b')));
    assert_eq!(
        editor.cursor(),
        (0, 2),
        "Ctrl+B keeps the ZWJ grapheme intact"
    );
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('b')));
    assert_eq!(editor.cursor(), (0, 1));
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('f')));
    assert_eq!(editor.cursor(), (0, 2));

    let before = editor.text();
    handle_key(&mut app, &mut editor, alt(KeyCode::Char('b')));
    handle_key(&mut app, &mut editor, alt(KeyCode::Char('f')));
    handle_key(&mut app, &mut editor, alt(KeyCode::Char('x')));
    assert_eq!(
        editor.text(),
        before,
        "Alt letters are shortcuts, never text"
    );
}

#[test]
fn ctrl_a_e_and_home_end_have_distinct_line_boundaries() {
    let mut app = App::new();
    let mut editor = Editor::new();
    editor.insert_str("ab\ncd");
    editor.set_global_offset(1);
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('a')));
    assert_eq!(editor.cursor(), (0, 0));
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('a')));
    assert_eq!(editor.cursor(), (0, 0), "already at document start");
    handle_key(&mut app, &mut editor, key(KeyCode::End));
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('e')));
    assert_eq!(
        editor.cursor(),
        (1, 2),
        "Ctrl+E at line end moves to next line end"
    );
    handle_key(&mut app, &mut editor, key(KeyCode::Home));
    assert_eq!(editor.cursor(), (1, 0), "Home stays on the current line");
    handle_key(&mut app, &mut editor, key(KeyCode::End));
    assert_eq!(editor.cursor(), (1, 2));
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('a')));
    assert_eq!(
        editor.cursor(),
        (1, 0),
        "Ctrl+A moves to the current line start"
    );
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('a')));
    assert_eq!(
        editor.cursor(),
        (0, 0),
        "Ctrl+A at line start moves to previous line start"
    );
}

#[test]
fn word_motion_handles_unicode_words_and_punctuation() {
    let mut app = App::new();
    let mut editor = Editor::new();
    editor.insert_str("foo,bar 世界 baz");
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Left));
    assert_eq!(editor.cursor(), (0, 11), "previous word start");
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Left));
    assert_eq!(
        editor.cursor(),
        (0, 9),
        "Unicode word boundary between CJK ideographs"
    );
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Left));
    assert_eq!(editor.cursor(), (0, 8));
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Right));
    assert_eq!(editor.cursor(), (0, 9), "end of the next word");
    handle_key(&mut app, &mut editor, alt(KeyCode::Char('b')));
    assert_eq!(editor.cursor(), (0, 8), "Alt+B mirrors Ctrl+Left");

    editor.set_draft("foo,bar");
    editor.set_global_offset(7);
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Left));
    assert_eq!(
        editor.cursor(),
        (0, 4),
        "punctuation trailing a word is one stop"
    );
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Left));
    assert_eq!(editor.cursor(), (0, 3));
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Left));
    assert_eq!(editor.cursor(), (0, 0));
}

#[test]
fn word_delete_and_yank_round_trip_across_lines() {
    let mut app = App::new();
    let mut editor = Editor::new();
    editor.insert_str("foo bar baz");
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('w')));
    assert_eq!(editor.text(), "foo bar ");
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('y')));
    assert_eq!(editor.text(), "foo bar baz");

    editor.set_draft("foo\nbar");
    handle_key(&mut app, &mut editor, key(KeyCode::Home));
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('w')));
    assert_eq!(editor.text(), "bar", "word kill crosses the newline");
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('y')));
    assert_eq!(editor.text(), "foo\nbar");
}

#[test]
fn ctrl_u_k_kill_line_edges_and_newlines() {
    let mut app = App::new();
    let mut editor = Editor::new();
    editor.insert_str("hello world");
    editor.set_global_offset(5);
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('k')));
    assert_eq!(editor.text(), "hello");
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('u')));
    assert_eq!(editor.text(), "");
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('y')));
    assert_eq!(editor.text(), "hello");

    editor.set_draft("ab\ncd");
    handle_key(&mut app, &mut editor, key(KeyCode::Home));
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('u')));
    assert_eq!(
        editor.text(),
        "abcd",
        "Ctrl+U at line start kills the newline"
    );
}

#[test]
fn kill_buffer_survives_send_and_history_recall() {
    let mut app = App::new();
    let mut editor = Editor::new();
    editor.insert_str("keep me");
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('w')));
    assert_eq!(
        editor.take().text,
        "keep ",
        "the draft is submitted without the word"
    );
    editor.set_draft("recalled");
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('y')));
    assert_eq!(
        editor.text(),
        "recalledme",
        "kill buffer survives take and recall"
    );

    // Ordinary character deletion must not overwrite the kill buffer.
    editor.clear();
    editor.insert_str("xy");
    handle_key(&mut app, &mut editor, key(KeyCode::Backspace));
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('y')));
    assert_eq!(editor.text(), "xme");

    // Empty-input kills with nothing to delete must not overwrite it either.
    let mut editor = Editor::new();
    editor.insert_str("ab");
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('w')));
    assert!(editor.is_empty());
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('w')));
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('u')));
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('k')));
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('y')));
    assert_eq!(editor.text(), "ab", "no-op kills keep the previous buffer");
}

#[test]
fn ctrl_h_and_ctrl_d_edit_and_ctrl_d_empty_uses_exit_path() {
    let mut app = App::new();
    let mut editor = Editor::new();
    editor.insert_str("abc");
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('h')));
    assert_eq!(editor.text(), "ab");
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('a')));
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('d')));
    assert_eq!(editor.text(), "b");

    editor.clear();
    let outcome = handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('d')));
    assert!(outcome.quit, "idle empty Ctrl+D quits");
    app.running = true;
    let outcome = handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('d')));
    assert!(!outcome.quit);
    assert!(app.quit_after_turn);
    assert!(app.status.is_some());
}

#[test]
fn ctrl_j_inserts_a_newline() {
    let mut app = App::new();
    let mut editor = Editor::new();
    editor.insert_str("a");
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('j')));
    assert_eq!(editor.text(), "a\n");
    assert_eq!(editor.cursor(), (1, 0));
}

#[test]
fn display_line_soft_wrap_end_does_not_claim_the_next_fragment() {
    let editor = {
        let mut editor = Editor::new();
        editor.insert_str("abcd界abcd");
        editor
    };
    let layout = editor.layout(5);
    let wrapped = &layout.lines[1];
    assert!(!wrapped.is_logical_end);
    assert_eq!(wrapped.text, "界abc");
    assert_eq!(
        wrapped.position_for_cell(5),
        (0, 7),
        "a soft-wrap fragment clamps into its last grapheme"
    );
    let last = layout.lines.last().unwrap();
    assert!(last.is_logical_end);
    assert_eq!(
        last.position_for_cell(5),
        (0, 9),
        "the logical end may use EOL"
    );
}

#[test]
fn visual_navigation_wraps_and_restores_the_preferred_column() {
    let mut editor = {
        let mut editor = Editor::new();
        editor.insert_str("abcd界abcd");
        editor
    };
    assert_eq!(editor.layout(5).cursor_line, 2);
    editor.move_visual_up(5);
    assert_eq!(
        editor.cursor(),
        (0, 4),
        "up lands inside the previous fragment"
    );
    editor.move_visual_up(5);
    assert_eq!(editor.cursor(), (0, 1));

    let mut editor = {
        let mut editor = Editor::new();
        editor.insert_str("abcdef\nxy\nabcdef");
        editor
    };
    editor.move_visual_up(10);
    assert_eq!(editor.cursor(), (1, 2));
    editor.move_visual_up(10);
    assert_eq!(
        editor.cursor(),
        (0, 6),
        "preferred column restored on the longer line"
    );
    editor.move_visual_down(10);
    assert_eq!(editor.cursor(), (1, 2));
    editor.move_visual_down(10);
    assert_eq!(editor.cursor(), (2, 6));
}

#[test]
fn visual_navigation_reaches_document_head_and_tail() {
    let mut editor = Editor::new();
    editor.insert_str("hello");
    editor.set_global_offset(3);
    editor.move_visual_up(80);
    assert_eq!(
        editor.cursor(),
        (0, 0),
        "Up on the first visual line goes to the head"
    );
    editor.move_visual_down(80);
    assert_eq!(
        editor.cursor(),
        (0, 5),
        "Down on the last visual line goes to the tail"
    );
}

#[test]
fn history_navigation_replaces_and_restores_drafts() {
    let mut app = App::new();
    app.history.push("first".to_owned());
    app.history.push("second".to_owned());
    let mut editor = Editor::new();
    handle_key(&mut app, &mut editor, key(KeyCode::Up));
    assert_eq!(editor.text(), "second");
    assert_eq!(editor.cursor(), (0, 6), "recall puts the cursor at the end");
    handle_key(&mut app, &mut editor, key(KeyCode::Up));
    assert_eq!(editor.text(), "first");
    handle_key(&mut app, &mut editor, key(KeyCode::Down));
    assert_eq!(editor.text(), "second");
    handle_key(&mut app, &mut editor, key(KeyCode::Down));
    assert_eq!(
        editor.text(),
        "",
        "down past the newest restores an empty draft"
    );
}

#[test]
fn history_navigation_yields_to_cursor_movement_after_edits() {
    let mut app = App::new();
    app.history.push("first".to_owned());
    let mut editor = Editor::new();
    handle_key(&mut app, &mut editor, key(KeyCode::Up));
    assert_eq!(editor.text(), "first");
    handle_key(&mut app, &mut editor, key(KeyCode::Char('x')));
    assert_eq!(editor.text(), "firstx");
    handle_key(&mut app, &mut editor, key(KeyCode::Up));
    assert_eq!(
        editor.text(),
        "firstx",
        "edited recall does not navigate history"
    );
}

#[test]
fn history_dedupes_adjacent_entries() {
    let mut app = App::new();
    app.history.push("a".to_owned());
    app.history.push("a".to_owned());
    app.history.push("b".to_owned());
    let mut editor = Editor::new();
    handle_key(&mut app, &mut editor, key(KeyCode::Up));
    assert_eq!(editor.text(), "b");
    handle_key(&mut app, &mut editor, key(KeyCode::Up));
    assert_eq!(editor.text(), "a");
    handle_key(&mut app, &mut editor, key(KeyCode::Up));
    assert_eq!(
        editor.text(),
        "a",
        "oldest entry does not advance past itself"
    );
}

#[test]
fn ctrl_c_saves_and_clears_a_draft_even_while_busy() {
    let mut app = App::new();
    app.running = true;
    let mut editor = Editor::new();
    editor.insert_str("draft");
    let outcome = handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('c')));
    assert!(!outcome.quit);
    assert!(
        !app.quit_after_turn,
        "clearing a busy draft does not request exit"
    );
    assert!(editor.is_empty());
    assert_eq!(app.focus, Focus::Input);
    handle_key(&mut app, &mut editor, key(KeyCode::Up));
    assert_eq!(
        editor.text(),
        "draft",
        "cleared draft is recalled from history"
    );
}

#[test]
fn word_delete_keeps_combining_clusters_intact() {
    let mut app = App::new();
    let mut editor = Editor::new();
    editor.insert_str("a:\u{301}");
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('w')));
    assert_eq!(editor.text(), "a", "backward word kill keeps the cluster");

    editor.set_draft("");
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('y')));
    assert_eq!(
        editor.text(),
        ":\u{301}",
        "kill buffer holds the whole cluster, not a stray mark"
    );

    let mut editor = Editor::new();
    editor.insert_str(":\u{301}b");
    handle_key(&mut app, &mut editor, key(KeyCode::Home));
    handle_key(&mut app, &mut editor, alt(KeyCode::Char('d')));
    assert_eq!(editor.text(), "b", "forward word kill keeps the cluster");
}

#[test]
fn word_motion_does_not_stick_inside_a_combining_cluster() {
    let mut app = App::new();
    let mut editor = Editor::new();
    editor.insert_str(":\u{301}b");
    handle_key(&mut app, &mut editor, key(KeyCode::Home));
    assert_eq!(editor.cursor(), (0, 0));
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Right));
    assert_ne!(editor.cursor(), (0, 0), "word-right must progress");
    let after_right = editor.cursor();
    handle_key(&mut app, &mut editor, ctrl(KeyCode::Left));
    assert_ne!(editor.cursor(), after_right, "word-left must progress");
    assert_eq!(editor.cursor(), (0, 0));
}

#[test]
fn preferred_column_survives_resize_and_reaches_the_previous_display_line() {
    let mut editor = Editor::new();
    editor.insert_str("abcdefghijklmnopqrstuvwxyz\nsecond");
    editor.set_global_offset(20);
    // Wide layout: one display line per logical line; a vertical move records
    // the preferred column.
    editor.move_visual_down(40);
    assert_eq!(editor.cursor(), (1, 6));
    editor.move_visual_up(40);
    assert_eq!(editor.cursor(), (0, 20));

    // Narrow layout: the first logical line wraps into fragments. Continuing to
    // move up must step through the previous display lines, not skip them.
    assert_eq!(editor.layout(6).cursor_line, 3);
    editor.move_visual_up(6);
    assert_eq!(editor.cursor(), (0, 17));
    assert_eq!(editor.layout(6).cursor_line, 2);
    editor.move_visual_up(6);
    assert_eq!(editor.layout(6).cursor_line, 1);
    editor.move_visual_up(6);
    assert_eq!(editor.layout(6).cursor_line, 0);
    editor.move_visual_up(6);
    assert_eq!(editor.cursor(), (0, 0), "reaches the document head");
}

#[test]
fn narrowing_then_widening_keeps_the_draft_and_allows_renavigation() {
    let mut editor = Editor::new();
    let text = "你好世界你好世界\nsecond";
    editor.insert_str(text);
    editor.set_global_offset(0);

    editor.move_visual_down(6);
    let narrow_line = editor.layout(6).cursor_line;
    assert!(narrow_line > 0, "narrow layout wraps the first line");
    assert_eq!(editor.text(), text, "navigation never edits the draft");

    let wide_line = editor.layout(40).cursor_line;
    editor.move_visual_up(40);
    assert!(editor.layout(40).cursor_line <= wide_line);
    assert_eq!(editor.text(), text, "draft preserved across resize");
}

#[test]
fn ctrl_c_closes_the_log_before_touching_the_draft() {
    let path = std::env::temp_dir().join(format!("saki-input-log-{}.txt", std::process::id()));
    std::fs::write(&path, "line one\nline two\n").unwrap();
    let mut app = App::new();
    app.apply_event(AgentEvent::ToolStarted {
        call_id: "c1".to_owned(),
        name: "bash".to_owned(),
        arguments: "{}".to_owned(),
    });
    app.apply_event(AgentEvent::ToolFinished {
        call_id: "c1".to_owned(),
        name: "bash".to_owned(),
        output: json!({
            "output": "line two\n",
            "exit_code": 0,
            "output_path": path.to_string_lossy(),
        }),
    });
    app.selected = 0;
    assert!(log::open(&mut app));
    app.running = true;
    let mut editor = Editor::new();
    editor.insert_str("draft");

    let outcome = handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('c')));
    assert!(app.log_view.is_none(), "Ctrl+C closes the log first");
    assert_eq!(editor.text(), "draft", "the draft is preserved");
    assert!(!app.quit_after_turn, "no exit flag yet");
    assert!(!outcome.quit);

    // Only the next Ctrl+C saves and clears the draft.
    let outcome = handle_key(&mut app, &mut editor, ctrl(KeyCode::Char('c')));
    assert!(editor.is_empty());
    assert!(!outcome.quit);
    assert!(!app.quit_after_turn, "busy clear does not request exit");
    handle_key(&mut app, &mut editor, key(KeyCode::Up));
    assert_eq!(editor.text(), "draft", "the cleared draft is in history");
    let _ = std::fs::remove_file(&path);
}
