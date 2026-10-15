use super::*;
use crate::{
    agent::{
        conversation::CompactionConfig,
        journal::{JournalRecord, Replay},
        message::ImageAttachment,
    },
    frontend::tui::sessions::Sessions,
    protocol::{InputItem, InputMessageContent, MessageRole, ReasoningText},
    session::{
        SessionCommand, SessionEvent,
        store::{SessionMetadata, SessionStore},
    },
};

pub(super) fn snapshot(title: &str) -> (SessionMetadata, Replay) {
    let path = std::env::temp_dir().join(format!(
        "saki-session-ui-{}",
        crate::agent::journal::new_id().unwrap()
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
    let path = std::env::temp_dir().join(format!(
        "saki-session-task-{}",
        crate::agent::journal::new_id().unwrap()
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
