use super::*;
use crate::{
    agent::{
        conversation::CompactionConfig,
        journal::{JournalRecord, Replay, TaskStatus},
        message::ImageAttachment,
    },
    frontend::tui::sessions::Sessions,
    protocol::{InputItem, InputMessageContent, MessageRole, ReasoningText},
    session::{
        SessionCommand, SessionEvent,
        store::{SessionMetadata, SessionStore, SessionSummary},
    },
};

pub(super) fn snapshot(title: &str) -> (SessionMetadata, Replay) {
    static NEXT: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
    let path = std::env::temp_dir().join(format!(
        "saki-session-ui-{}-{}",
        crate::agent::journal::new_id().unwrap(),
        NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
    ));
    std::fs::create_dir(&path).unwrap();
    let store = SessionStore::new(path.join("sessions")).unwrap();
    let id = store
        .create(
            &path,
            "test-model",
            "system",
            CompactionConfig::new(1_000_000),
        )
        .unwrap();
    let metadata = store.rename(&id, title).unwrap();
    let mut loaded = store.open(&id).unwrap();
    loaded.conversation.add_user_message("original input");
    loaded
        .conversation
        .add_reasoning(vec![ReasoningText::new("inspect first")], None);
    loaded
        .conversation
        .add_assistant_message("original answer".into());
    loaded.conversation.save_history().unwrap();
    drop(loaded);
    let replay = store.open(&id).unwrap().replay;
    std::fs::remove_dir_all(path).unwrap();
    (metadata, replay)
}

pub(super) fn task_snapshot(title: &str) -> (SessionMetadata, Replay) {
    static NEXT: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
    let path = std::env::temp_dir().join(format!(
        "saki-session-task-{}-{}",
        crate::agent::journal::new_id().unwrap(),
        NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
    ));
    std::fs::create_dir(&path).unwrap();
    let store = SessionStore::new(path.join("sessions")).unwrap();
    let id = store
        .create(
            &path,
            "test-model",
            "system",
            CompactionConfig::new(1_000_000),
        )
        .unwrap();
    let metadata = store.rename(&id, title).unwrap();
    let mut loaded = store.open(&id).unwrap();
    loaded.conversation.begin_task().unwrap();
    loaded.conversation.add_user_message("task question");
    loaded
        .conversation
        .add_reasoning(vec![ReasoningText::new("task thought")], None);
    loaded
        .conversation
        .add_assistant_message("task answer".into());
    loaded.conversation.finish_task(None).unwrap();
    drop(loaded);
    let replay = store.open(&id).unwrap().replay;
    std::fs::remove_dir_all(path).unwrap();
    (metadata, replay)
}

pub(super) fn opened(metadata: SessionMetadata, replay: Replay) -> SessionEvent {
    SessionEvent::Opened {
        metadata,
        replay,
        context: AgentEvent::ContextUpdated {
            model: "test-model".into(),
            context_tokens: Some(1000),
            context_window: Some(1_000_000),
            input_limit: Some(991_808),
        },
    }
}

#[test]
fn switching_restores_drafts_images_reading_position_and_thought_expansion() {
    let (a, mut a_history) = snapshot("Session A");
    let (b, b_history) = snapshot("Session B");
    let mut sessions = Sessions::default();
    let mut app = App::new();
    let mut editor = Editor::new();
    sessions.apply_event(&mut app, &mut editor, opened(a.clone(), a_history.clone()));
    assert!(!app.running);
    assert!(matches!(
        app.records[1],
        Record::Reasoning {
            expanded: false,
            ..
        }
    ));
    app.selected = 1;
    app.toggle_selected();
    app.focus = Focus::Transcript;
    app.follow = false;
    app.anchor = Some(Anchor {
        record: 1,
        offset: 0,
    });
    editor.set_draft(crate::agent::message::UserMessage {
        text: "unsent A draft".into(),
        images: vec![ImageAttachment::new("a.png", "data:image/png;base64,AA")],
    });
    sessions.apply_event(&mut app, &mut editor, opened(b.clone(), b_history));
    assert!(editor.is_empty());
    assert_eq!(app.session.as_ref().unwrap().id, b.id);
    assert!(
        !editor.undo(),
        "a switched-to session starts with no undo history"
    );
    editor.insert_str("unsent B draft");
    sessions.apply_event(
        &mut app,
        &mut editor,
        SessionEvent::Agent {
            session_id: a.id.clone(),
            event: AgentEvent::ResponseText {
                item_id: "late".into(),
                text: "must not appear in B".into(),
            },
        },
    );
    assert!(!format!("{:?}", app.records).contains("must not appear"));
    // Reopening reads disk again; preserve view state while accepting new history.
    a_history.history.push(JournalRecord::Messages {
        items: vec![InputItem::Message {
            role: MessageRole::Assistant,
            content: InputMessageContent::Text("external new answer".into()),
        }],
    });
    sessions.apply_event(&mut app, &mut editor, opened(a.clone(), a_history));
    assert_eq!(editor.text(), "unsent A draft");
    assert_eq!(editor.images().len(), 1);
    assert!(
        !editor.undo(),
        "session replacement resets undo history while keeping the cached draft"
    );
    assert_eq!(app.focus, Focus::Transcript);
    assert!(!app.follow);
    assert_eq!(
        app.anchor,
        Some(Anchor {
            record: 1,
            offset: 0
        })
    );
    assert!(matches!(
        app.records[1],
        Record::Reasoning { expanded: true, .. }
    ));
    assert!(format!("{:?}", app.records).contains("external new answer"));
    assert_eq!(app.session.as_ref().unwrap().id, a.id);
}

#[test]
fn reopening_a_session_preserves_manual_activity_expansion() {
    let (a, a_history) = task_snapshot("Task A");
    let (b, b_history) = task_snapshot("Task B");
    let mut sessions = Sessions::default();
    let mut app = App::new();
    let mut editor = Editor::new();
    sessions.apply_event(&mut app, &mut editor, opened(a.clone(), a_history.clone()));
    assert!(matches!(
        &app.records[1],
        Record::Activity {
            expanded: false,
            manual: false,
            ..
        }
    ));
    app.toggle_record(1);
    assert!(app.activity_expanded(1));
    sessions.apply_event(&mut app, &mut editor, opened(b, b_history));
    sessions.apply_event(&mut app, &mut editor, opened(a, a_history));
    assert!(matches!(
        &app.records[1],
        Record::Activity {
            expanded: true,
            manual: true,
            ..
        }
    ));
}

#[test]
fn picker_is_on_demand_and_busy_switching_preserves_the_draft() {
    let (metadata, replay) = snapshot("Compiler work");
    let mut sessions = Sessions::default();
    let mut app = App::new();
    let mut editor = Editor::new();
    sessions.apply_event(&mut app, &mut editor, opened(metadata.clone(), replay));
    editor.insert_str("unfinished draft");
    let (handled, request) = sessions.key(
        &mut app,
        key_with(KeyCode::Char('s'), KeyModifiers::CONTROL),
    );
    assert!(handled);
    assert!(matches!(request, Some(SessionCommand::List)));
    sessions.apply_event(&mut app, &mut editor, SessionEvent::Listed(vec![]));
    app.running = true;
    let (handled, request) = sessions.key(&mut app, key(KeyCode::Char('n')));
    assert!(handled);
    assert!(request.is_none());
    assert!(app.running);
    assert_eq!(editor.text(), "unfinished draft");
    assert!(app.feedback.as_ref().unwrap().0.contains("Wait"));
    app.running = false;
    let (_, request) = sessions.key(&mut app, key(KeyCode::Char('n')));
    assert!(matches!(request, Some(SessionCommand::Create)));
    sessions.apply_event(
        &mut app,
        &mut editor,
        SessionEvent::Error {
            message: "cannot open target".into(),
        },
    );
    assert!(!sessions.pending);
    assert_eq!(app.session.as_ref().unwrap().id, metadata.id);
    assert_eq!(editor.text(), "unfinished draft");
    sessions.key(&mut app, key(KeyCode::Esc));
    assert!(sessions.picker.is_none());
}

#[test]
fn session_title_is_visible_without_shortcuts_cluttering_the_status_line() {
    let (metadata, replay) = snapshot("Compiler work");
    let mut sessions = Sessions::default();
    let mut app = App::new();
    let mut editor = Editor::new();
    sessions.apply_event(&mut app, &mut editor, opened(metadata, replay));
    let mut terminal = Terminal::new(TestBackend::new(110, 25)).unwrap();
    terminal
        .draw(|frame| view::draw(frame, &mut app, &editor))
        .unwrap();
    let screen = buffer_text(terminal.backend().buffer());
    assert!(screen.contains("Compiler work"));
    assert!(screen.contains("test-model"));
    assert!(screen.contains("1M"));
    assert!(!screen.contains("Ctrl+S"));
    assert!(!app.session_button_area.is_empty());
    sessions.apply_event(&mut app, &mut editor, SessionEvent::Listed(vec![]));
    terminal
        .draw(|frame| {
            view::draw(frame, &mut app, &editor);
            sessions.draw(frame, app.theme);
        })
        .unwrap();
    let screen = buffer_text(terminal.backend().buffer());
    assert!(screen.contains("Sessions"));
    assert!(screen.contains("n new"));
}

fn picker_summary(id: &str, title: &str, model: &str, directory: &str) -> SessionSummary {
    let (mut metadata, _) = snapshot(title);
    metadata.id = id.to_owned();
    metadata.model = model.to_owned();
    metadata.working_directory = std::path::PathBuf::from(directory);
    SessionSummary {
        metadata,
        updated_at: 0,
        status: TaskStatus::Completed,
        in_use: false,
        unavailable: None,
    }
}

fn listed(items: Vec<SessionSummary>) -> (Sessions, App, Editor) {
    let mut sessions = Sessions::default();
    let mut app = App::new();
    let mut editor = Editor::new();
    sessions.apply_event(&mut app, &mut editor, SessionEvent::Listed(items));
    (sessions, app, editor)
}

fn search_and_open(
    sessions: &mut Sessions,
    app: &mut App,
    query: &str,
    downs: usize,
) -> Option<String> {
    sessions.key(app, key(KeyCode::Char('/')));
    for character in query.chars() {
        sessions.key(app, key(KeyCode::Char(character)));
    }
    for _ in 0..downs {
        sessions.key(app, key(KeyCode::Down));
    }
    match sessions.key(app, key(KeyCode::Enter)).1 {
        Some(SessionCommand::Open { session_id }) => Some(session_id),
        _ => None,
    }
}

fn rendered(sessions: &Sessions, app: &App, width: u16) -> String {
    let mut terminal = Terminal::new(TestBackend::new(width, 18)).unwrap();
    terminal
        .draw(|frame| sessions.draw(frame, app.theme))
        .unwrap();
    buffer_text(terminal.backend().buffer())
}

#[test]
fn picker_search_matches_title_id_directory_and_model() {
    let compiler = picker_summary("id-one", "Compiler work", "model-alpha", "/work/alpha");
    let release = picker_summary("id-two", "Release notes", "model-beta", "/work/beta");
    let ops = picker_summary("id-three", "Deploy checklist", "model-gamma", "/work/gamma");
    let items = vec![compiler, release, ops];

    let (mut sessions, mut app, _) = listed(items.clone());
    assert_eq!(
        search_and_open(&mut sessions, &mut app, "COMPILER", 0),
        Some("id-one".to_owned())
    );

    let (mut sessions, mut app, _) = listed(items.clone());
    assert_eq!(
        search_and_open(&mut sessions, &mut app, "id-three", 0),
        Some("id-three".to_owned())
    );

    let (mut sessions, mut app, _) = listed(items.clone());
    assert_eq!(
        search_and_open(&mut sessions, &mut app, "work/beta", 0),
        Some("id-two".to_owned())
    );

    let (mut sessions, mut app, _) = listed(items.clone());
    assert_eq!(
        search_and_open(&mut sessions, &mut app, "model-gamma", 0),
        Some("id-three".to_owned())
    );

    // Two whitespace-separated terms must both match the same session.
    let (mut sessions, mut app, _) = listed(items.clone());
    assert_eq!(
        search_and_open(&mut sessions, &mut app, "compiler work", 0),
        Some("id-one".to_owned())
    );
    let (mut sessions, mut app, _) = listed(items);
    assert!(
        search_and_open(&mut sessions, &mut app, "compiler release", 0).is_none(),
        "terms do not combine across sessions"
    );
}

#[test]
fn picker_search_navigates_only_visible_results() {
    let alpha = picker_summary("id-a", "ticket alpha", "model", "/dir-a");
    let beta = picker_summary("id-b", "ticket beta", "model", "/dir-b");
    let gamma = picker_summary("id-c", "unrelated gamma", "model", "/dir-c");
    let items = vec![alpha, beta, gamma];

    let (mut sessions, mut app, _) = listed(items.clone());
    assert_eq!(
        search_and_open(&mut sessions, &mut app, "ticket", 1),
        Some("id-b".to_owned())
    );

    // Down past the last visible result stays on it.
    let (mut sessions, mut app, _) = listed(items.clone());
    assert_eq!(
        search_and_open(&mut sessions, &mut app, "ticket", 9),
        Some("id-b".to_owned())
    );

    let (mut sessions, mut app, _) = listed(items);
    assert_eq!(
        search_and_open(&mut sessions, &mut app, "gamma", 5),
        Some("id-c".to_owned())
    );
}

#[test]
fn picker_search_treats_n_and_r_as_text_and_esc_exits_then_closes() {
    let (mut sessions, mut app, _) = listed(vec![picker_summary("id-a", "alpha", "model", "/a")]);
    sessions.key(&mut app, key(KeyCode::Char('/')));
    // n and r edit the query; they never create or rename.
    for character in "nr".chars() {
        let (_, request) = sessions.key(&mut app, key(KeyCode::Char(character)));
        assert!(
            request.is_none() && !sessions.pending,
            "'{character}' is query text"
        );
    }
    let (_, request) = sessions.key(&mut app, key(KeyCode::Enter));
    assert!(
        request.is_none() && !sessions.pending,
        "no match does not confirm"
    );
    // First Esc leaves search and keeps the picker open.
    sessions.key(&mut app, key(KeyCode::Esc));
    assert!(sessions.picker.is_some());
    let (_, request) = sessions.key(&mut app, key(KeyCode::Enter));
    assert!(matches!(request, Some(SessionCommand::Open { .. })));
    // Second Esc closes the picker.
    sessions.pending = false;
    sessions.key(&mut app, key(KeyCode::Esc));
    assert!(sessions.picker.is_none());
}

#[test]
fn picker_search_ctrl_c_exits_search_before_closing() {
    let (mut sessions, mut app, _) = listed(vec![picker_summary("id-a", "alpha", "model", "/a")]);
    sessions.key(&mut app, key(KeyCode::Char('/')));
    sessions.key(&mut app, key(KeyCode::Char('x')));
    sessions.key(
        &mut app,
        key_with(KeyCode::Char('c'), KeyModifiers::CONTROL),
    );
    assert!(sessions.picker.is_some());
    sessions.key(
        &mut app,
        key_with(KeyCode::Char('c'), KeyModifiers::CONTROL),
    );
    assert!(sessions.picker.is_none());
}

#[test]
fn picker_search_no_match_shows_empty_state_and_enter_does_nothing() {
    let (mut sessions, mut app, _) = listed(vec![picker_summary("id-a", "alpha", "model", "/a")]);
    sessions.key(&mut app, key(KeyCode::Char('/')));
    for character in "zzz-no-such".chars() {
        sessions.key(&mut app, key(KeyCode::Char(character)));
    }
    let (_, request) = sessions.key(&mut app, key(KeyCode::Enter));
    assert!(request.is_none() && !sessions.pending);
    // 'r' stays literal in search, so Enter cannot start a rename.
    sessions.key(&mut app, key(KeyCode::Char('r')));
    let (_, request) = sessions.key(&mut app, key(KeyCode::Enter));
    assert!(request.is_none() && !sessions.pending);
    assert!(sessions.picker.is_some());
    let screen = rendered(&sessions, &app, 60);
    assert!(screen.contains("Search:"), "{screen}");
    assert!(screen.contains("No sessions match"), "{screen}");
}

#[test]
fn picker_search_backspace_is_grapheme_safe() {
    let (mut sessions, mut app, _) = listed(vec![picker_summary("id-a", "alpha", "model", "/a")]);
    sessions.key(&mut app, key(KeyCode::Char('/')));
    for character in "cafe\u{301}".chars() {
        sessions.key(&mut app, key(KeyCode::Char(character)));
    }
    let screen = rendered(&sessions, &app, 60);
    assert!(screen.contains("Search: cafe\u{301}"), "{screen}");
    sessions.key(&mut app, key(KeyCode::Backspace));
    let screen = rendered(&sessions, &app, 60);
    assert!(screen.contains("Search: caf"), "{screen}");
    assert!(
        !screen.contains("cafe"),
        "the combining grapheme is gone: {screen}"
    );
}

#[test]
fn picker_search_recomputes_and_preserves_selection_on_title_change() {
    let alpha = picker_summary("id-a", "alpha one", "model", "/a");
    let beta = picker_summary("id-b", "alpha two", "model", "/b");
    let (mut sessions, mut app, mut editor) = listed(vec![alpha, beta.clone()]);
    sessions.key(&mut app, key(KeyCode::Char('/')));
    for character in "alpha".chars() {
        sessions.key(&mut app, key(KeyCode::Char(character)));
    }
    sessions.key(&mut app, key(KeyCode::Down));
    // A title update that keeps the second result matching preserves it.
    let mut updated = beta.metadata.clone();
    updated.title = "alpha two revised".into();
    sessions.apply_event(&mut app, &mut editor, SessionEvent::TitleUpdated(updated));
    let (_, request) = sessions.key(&mut app, key(KeyCode::Enter));
    assert!(matches!(request, Some(SessionCommand::Open { session_id }) if session_id == "id-b"));
    // A rename that removes it from the result set falls back to the first.
    sessions.pending = false;
    let mut renamed = beta.metadata.clone();
    renamed.title = "beta two".into();
    sessions.apply_event(&mut app, &mut editor, SessionEvent::Renamed(renamed));
    let (_, request) = sessions.key(&mut app, key(KeyCode::Enter));
    assert!(matches!(request, Some(SessionCommand::Open { session_id }) if session_id == "id-a"));
}

#[test]
fn picker_rename_target_stays_stable_across_title_updates() {
    let alpha = picker_summary("id-a", "alpha one", "model", "/a");
    let beta = picker_summary("id-b", "alpha two", "model", "/b");
    let (mut sessions, mut app, mut editor) = listed(vec![alpha.clone(), beta]);
    sessions.key(&mut app, key(KeyCode::Char('/')));
    for character in "alpha".chars() {
        sessions.key(&mut app, key(KeyCode::Char(character)));
    }
    sessions.key(&mut app, key(KeyCode::Down));
    sessions.key(&mut app, key(KeyCode::Esc)); // leave search, selection preserved
    sessions.key(&mut app, key(KeyCode::Char('r'))); // rename id-b
    // A late automatic title for a different session must not redirect it.
    let mut updated = alpha.metadata.clone();
    updated.title = "alpha one revised".into();
    sessions.apply_event(&mut app, &mut editor, SessionEvent::TitleUpdated(updated));
    let (_, request) = sessions.key(&mut app, key(KeyCode::Enter));
    assert!(
        matches!(request, Some(SessionCommand::Rename { session_id, .. }) if session_id == "id-b")
    );
}

#[test]
fn picker_search_preserves_draft_images_and_reading_position() {
    let (mut sessions, mut app, mut editor) =
        listed(vec![picker_summary("id-a", "alpha", "model", "/a")]);
    editor.set_draft(crate::agent::message::UserMessage {
        text: "keep this draft".into(),
        images: vec![ImageAttachment::new("a.png", "data:image/png;base64,AA")],
    });
    app.follow = false;
    app.anchor = Some(Anchor {
        record: 0,
        offset: 0,
    });
    sessions.key(&mut app, key(KeyCode::Char('/')));
    for character in "alp".chars() {
        sessions.key(&mut app, key(KeyCode::Char(character)));
    }
    sessions.key(&mut app, key(KeyCode::Esc));
    sessions.key(&mut app, key(KeyCode::Esc));
    assert!(sessions.picker.is_none());
    assert_eq!(editor.text(), "keep this draft");
    assert_eq!(editor.images().len(), 1);
    assert!(!app.follow);
    assert_eq!(
        app.anchor,
        Some(Anchor {
            record: 0,
            offset: 0
        })
    );
}

#[test]
fn picker_search_renders_field_at_narrow_and_wide_widths() {
    let alpha = picker_summary("id-a", "alpha project", "model-x", "/long/dir/alpha");
    let beta = picker_summary("id-b", "beta project", "model-y", "/long/dir/beta");
    let (mut sessions, mut app, _) = listed(vec![alpha, beta]);

    for width in [40u16, 110] {
        let screen = rendered(&sessions, &app, width);
        assert!(screen.contains("Sessions"), "width {width}: {screen}");
        assert!(screen.contains("/ search"), "width {width}: {screen}");
    }

    sessions.key(&mut app, key(KeyCode::Char('/')));
    for character in "alpha".chars() {
        sessions.key(&mut app, key(KeyCode::Char(character)));
    }
    for width in [40u16, 110] {
        let screen = rendered(&sessions, &app, width);
        assert!(screen.contains("Search:"), "width {width}: {screen}");
        assert!(screen.contains("alpha project"), "width {width}: {screen}");
        assert!(!screen.contains("beta project"), "width {width}: {screen}");
    }
}

#[test]
fn picker_search_keeps_the_query_tail_and_cursor_visible_when_narrow() {
    let (mut sessions, mut app, _) = listed(vec![picker_summary("id-a", "alpha", "model", "/a")]);
    sessions.key(&mut app, key(KeyCode::Char('/')));
    let prefix = "汉".repeat(10);
    let suffix = "zq7x9 rocket e\u{301}";
    for character in format!("{prefix}{suffix}").chars() {
        sessions.key(&mut app, key(KeyCode::Char(character)));
    }
    let screen = rendered(&sessions, &app, 40);
    assert!(screen.contains("Search:"), "{screen}");
    assert!(
        screen.contains("zq7x9 rocket e\u{301}▌"),
        "the entire fitting suffix and caret are visible: {screen}"
    );
    // Editing keeps working at the visible tail.
    sessions.key(&mut app, key(KeyCode::Backspace));
    let screen = rendered(&sessions, &app, 40);
    assert!(
        screen.contains("zq7x9 rocket ▌"),
        "suffix and caret stay visible after grapheme Backspace: {screen}"
    );
    assert!(
        !screen.contains("e\u{301}"),
        "backspace removes the combining grapheme: {screen}"
    );
}

#[test]
fn opening_a_session_closes_an_active_transcript_search() {
    let (a, a_history) = snapshot("Session A");
    let (b, b_history) = snapshot("Session B");
    let mut sessions = Sessions::default();
    let mut app = App::new();
    let mut editor = Editor::new();
    sessions.apply_event(&mut app, &mut editor, opened(a, a_history));
    app.last_width = 40;
    app.last_height = 6;
    crate::frontend::tui::transcript_search::open(&mut app);
    assert!(crate::frontend::tui::transcript_search::active(&app));
    sessions.apply_event(&mut app, &mut editor, opened(b, b_history));
    assert!(
        !crate::frontend::tui::transcript_search::active(&app),
        "session replacement never caches an active search"
    );
}
