use super::*;
use crate::{
    agent::journal::{JournalRecord, TaskStatus},
    protocol::{ToolDefinition, ToolType},
    tools::tool_registry::{Tool, ToolRegistry},
};
use serde_json::json;
use std::{
    fs,
    io::Write,
    sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    },
    time::Duration,
};

use crate::test_support::{completed, scripted_http};

struct Workspace(PathBuf);
impl Workspace {
    fn new() -> Self {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        let path = std::env::temp_dir().join(format!(
            "saki-sessions-{}-{}",
            crate::agent::journal::new_id().unwrap(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir(&path).unwrap();
        Self(path)
    }
    fn store(&self) -> SessionStore {
        SessionStore::new(self.0.join("sessions")).unwrap()
    }
    fn create(&self, store: &SessionStore) -> String {
        store
            .create(
                &self.0,
                "test",
                SYSTEM_PROMPT,
                CompactionConfig::new(262_144),
            )
            .unwrap()
    }
}
impl Drop for Workspace {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

async fn event(rx: &mut UnboundedReceiver<SessionEvent>) -> SessionEvent {
    tokio::time::timeout(Duration::from_secs(5), rx.recv())
        .await
        .unwrap()
        .unwrap()
}
async fn finished(rx: &mut UnboundedReceiver<SessionEvent>, expected: &str) {
    loop {
        if let SessionEvent::Agent { session_id, event } = event(rx).await {
            assert_eq!(session_id, expected);
            match event {
                AgentEvent::TurnCompleted => return,
                AgentEvent::TurnFailed { message } => panic!("{message}"),
                _ => {}
            }
        }
    }
}

async fn titled(rx: &mut UnboundedReceiver<SessionEvent>, expected: &str) -> SessionMetadata {
    loop {
        if let SessionEvent::TitleUpdated(metadata) = event(rx).await {
            assert_eq!(metadata.id, expected);
            return metadata;
        }
    }
}

#[test]
fn metadata_and_history_are_separate_and_a_session_has_one_writer() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = workspace.create(&store);
    let metadata = store.rename(&id, "Compiler work").unwrap();
    assert_eq!(metadata.title, "Compiler work");
    let mut loaded = store.open(&id).unwrap();
    assert!(store.open(&id).is_err());
    assert!(store.list().unwrap()[0].in_use);
    loaded.conversation.add_user_message("remember red-71");
    loaded.conversation.save_history().unwrap();
    drop(loaded);
    let loaded = store.open(&id).unwrap();
    assert_eq!(loaded.metadata.title, "Compiler work");
    assert_eq!(loaded.metadata.model, "test");
    assert_eq!(loaded.replay.context.len(), 2);
    assert!(
        serde_json::to_string(&loaded.replay.context)
            .unwrap()
            .contains("red-71")
    );
    assert!(
        !fs::read_to_string(store.directory(&id).unwrap().join("metadata.json"))
            .unwrap()
            .contains("red-71")
    );
}

#[test]
fn torn_tail_is_removed_but_complete_corruption_and_unknown_versions_are_rejected() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = workspace.create(&store);
    let path = store.directory(&id).unwrap().join("history.jsonl");
    let original = fs::read(&path).unwrap();
    fs::OpenOptions::new()
        .append(true)
        .open(&path)
        .unwrap()
        .write_all(b"{\"schema_ver")
        .unwrap();
    let mut loaded = store.open(&id).unwrap();
    assert_eq!(fs::read(&path).unwrap(), original);
    loaded.conversation.add_user_message("after torn write");
    loaded.conversation.save_history().unwrap();
    drop(loaded);
    assert_eq!(store.open(&id).unwrap().replay.context.len(), 2);
    fs::OpenOptions::new()
        .append(true)
        .open(&path)
        .unwrap()
        .write_all(b"{broken}\n")
        .unwrap();
    let corrupted = fs::read(&path).unwrap();
    assert!(store.open(&id).err().unwrap().to_string().contains(":3:"));
    assert_eq!(fs::read(&path).unwrap(), corrupted);
    let mut unsupported = original.clone();
    unsupported.extend_from_slice(b"{\"schema_version\":99,\"type\":\"messages\",\"items\":[]}");
    fs::write(&path, &unsupported).unwrap();
    assert!(
        store
            .open(&id)
            .err()
            .unwrap()
            .to_string()
            .contains("version 99")
    );
    assert_eq!(fs::read(&path).unwrap(), unsupported);
}

#[test]
fn valid_final_record_without_newline_is_kept_and_can_be_appended() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = workspace.create(&store);
    let path = store.directory(&id).unwrap().join("history.jsonl");
    let mut original = fs::read(&path).unwrap();
    assert_eq!(original.pop(), Some(b'\n'));
    fs::write(&path, original).unwrap();
    let mut loaded = store.open(&id).unwrap();
    loaded.conversation.add_user_message("second record");
    loaded.conversation.save_history().unwrap();
    drop(loaded);
    assert_eq!(store.open(&id).unwrap().replay.context.len(), 2);
}

#[test]
fn unsupported_old_sessions_are_visible_without_breaking_the_session_list() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let good = workspace.create(&store);
    let old = store.directory("old-session").unwrap();
    fs::create_dir(&old).unwrap();
    fs::write(
        old.join("history.jsonl"),
        "{\"type\":\"messages\",\"items\":[]}\n",
    )
    .unwrap();
    let sessions = store.list().unwrap();
    assert_eq!(sessions.len(), 2);
    assert!(
        sessions
            .iter()
            .find(|item| item.metadata.id == "old-session")
            .unwrap()
            .unavailable
            .is_some()
    );
    assert!(
        sessions
            .iter()
            .find(|item| item.metadata.id == good)
            .unwrap()
            .unavailable
            .is_none()
    );
    assert!(store.directory("../other").is_err());
}

#[tokio::test]
async fn switching_a_b_a_keeps_context_model_and_tool_working_directories_separate() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let a = workspace.create(&store);
    let other_cwd = workspace.0.join("other");
    fs::create_dir(&other_cwd).unwrap();
    fs::write(workspace.0.join("marker.txt"), "FILE-A").unwrap();
    fs::write(other_cwd.join("marker.txt"), "FILE-B").unwrap();
    let b = store
        .create(
            &other_cwd,
            "model-b",
            SYSTEM_PROMPT,
            CompactionConfig::new(131_072),
        )
        .unwrap();
    let tool = |id: &str| json!({"id":"tool-response", "status":"completed", "output":[{"type":"function_call", "status":"completed", "id":id,"call_id":id,"name":"read","arguments":"{\"path\":\"marker.txt\"}"}]});
    let (client, server) = scripted_http(vec![
        (200, tool("call-a")),
        (200, completed("answer-A")),
        (200, completed("Title A")),
        (200, tool("call-b")),
        (200, completed("answer-B")),
        (200, completed("Title B")),
        (200, completed("A still remembers")),
    ])
    .await;
    let loaded = store.open(&a).unwrap();
    let manager = SessionManager::new(
        store,
        client,
        loaded,
        workspace.0.clone(),
        "test".into(),
        CompactionConfig::new(262_144),
    )
    .unwrap();
    let (tx, rx) = mpsc::unbounded_channel();
    let (out, mut events) = mpsc::unbounded_channel();
    let worker = tokio::spawn(manager.run(rx, out));
    assert!(matches!(
        event(&mut events).await,
        SessionEvent::Opened { .. }
    ));
    tx.send(SessionCommand::Send {
        session_id: a.clone(),
        message: "secret-A".into(),
    })
    .unwrap();
    finished(&mut events, &a).await;
    let title_a = titled(&mut events, &a).await;
    assert_eq!(title_a.title, "Title A");
    assert_eq!(title_a.title_source.as_deref(), Some("auto"));
    tx.send(SessionCommand::Open {
        session_id: b.clone(),
    })
    .unwrap();
    match event(&mut events).await {
        SessionEvent::Opened {
            metadata,
            replay,
            context: AgentEvent::ContextUpdated { context_window, .. },
        } => {
            assert_eq!(metadata.id, b);
            assert_eq!(replay.context.len(), 1);
            assert_eq!(context_window, Some(131_072));
        }
        _ => panic!("expected B snapshot"),
    }
    tx.send(SessionCommand::Send {
        session_id: b.clone(),
        message: "secret-B".into(),
    })
    .unwrap();
    finished(&mut events, &b).await;
    let title_b = titled(&mut events, &b).await;
    assert_eq!(title_b.title, "Title B");
    tx.send(SessionCommand::Open {
        session_id: a.clone(),
    })
    .unwrap();
    match event(&mut events).await {
        SessionEvent::Opened {
            metadata, replay, ..
        } => {
            assert_eq!(metadata.id, a);
            assert_eq!(metadata.title, "Title A");
            assert_eq!(replay.status, TaskStatus::Completed);
            assert!(
                serde_json::to_string(&replay.context)
                    .unwrap()
                    .contains("secret-A")
            );
            assert!(
                !serde_json::to_string(&replay.context)
                    .unwrap()
                    .contains("secret-B")
            );
        }
        _ => panic!("expected A snapshot"),
    }
    tx.send(SessionCommand::Open {
        session_id: "missing".into(),
    })
    .unwrap();
    assert!(matches!(
        event(&mut events).await,
        SessionEvent::Error { .. }
    ));
    tx.send(SessionCommand::Send {
        session_id: a.clone(),
        message: "continue-A".into(),
    })
    .unwrap();
    finished(&mut events, &a).await;
    drop(tx);
    worker.await.unwrap();
    let requests = server.await.unwrap();
    assert_eq!(requests.len(), 7);
    // The title request is a tiny, tool-free call that never pollutes turns.
    assert_eq!(requests[2]["model"], "test");
    assert!(
        requests[2].get("tools").is_none(),
        "the title job must not send tools"
    );
    assert_eq!(requests[3]["model"], "model-b");
    assert_eq!(requests[5]["model"], "model-b");
    assert_eq!(requests[6]["model"], "test");
    assert!(requests[1].to_string().contains("FILE-A"));
    assert!(!requests[1].to_string().contains("FILE-B"));
    assert!(requests[4].to_string().contains("FILE-B"));
    assert!(!requests[4].to_string().contains("secret-A"));
    assert!(requests[6].to_string().contains("answer-A"));
    assert!(!requests[6].to_string().contains("secret-B"));
    let store = workspace.store();
    let a_again = store.open(&a).unwrap();
    assert_eq!(a_again.metadata.title, "Title A");
    assert_eq!(a_again.metadata.title_source.as_deref(), Some("auto"));
    assert_eq!(a_again.replay.status, TaskStatus::Completed);
    assert_eq!(
        a_again
            .replay
            .history
            .iter()
            .filter(|record| matches!(record, JournalRecord::TaskStarted { .. }))
            .count(),
        2
    );
}

#[tokio::test]
async fn interrupted_tool_is_closed_on_next_input_and_is_never_reexecuted() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = workspace.create(&store);
    let mut loaded = store.open(&id).unwrap();
    loaded.conversation.begin_task().unwrap();
    loaded.conversation.add_user_message("change a file");
    loaded
        .conversation
        .add_tool_call("uncertain-call".into(), "count".into(), "{}".into());
    loaded.conversation.save_history().unwrap();
    fs::write(
        workspace.0.join("already-written.txt"),
        "existing side effect",
    )
    .unwrap();
    drop(loaded);
    let loaded = store.open(&id).unwrap();
    assert_eq!(loaded.replay.status, TaskStatus::Interrupted);
    let count = Arc::new(AtomicUsize::new(0));
    let executed = count.clone();
    let mut registry = ToolRegistry::new();
    registry.registry(Tool {
        definition: ToolDefinition {
            tool_type: ToolType::Function,
            name: "count".to_owned(),
            description: "count calls".to_owned(),
            parameters: json!({"type":"object"}),
        },
        handler: Arc::new(move |_| {
            let executed = executed.clone();
            Box::pin(async move {
                executed.fetch_add(1, Ordering::SeqCst);
                Ok(json!({"ok":true}))
            })
        }),
    });
    let (client, server) = scripted_http(vec![(200, completed("inspect current state"))]).await;
    let mut agent = Agent::new(client, registry, loaded.conversation).with_model("test".into());
    agent
        .handle_user_message("check what happened")
        .await
        .unwrap();
    let request = &server.await.unwrap()[0];
    assert!(request.to_string().contains("uncertain-call"));
    assert!(
        request
            .to_string()
            .contains("inspect current state before retrying")
    );
    assert_eq!(count.load(Ordering::SeqCst), 0);
    assert_eq!(
        fs::read_to_string(workspace.0.join("already-written.txt")).unwrap(),
        "existing side effect"
    );
    drop(agent);
    assert_eq!(
        store.open(&id).unwrap().replay.status,
        TaskStatus::Completed
    );
}

#[tokio::test]
async fn busy_session_rejects_switch_and_second_message_but_can_be_listed() {
    use tokio::{io::AsyncWriteExt, net::TcpListener, sync::oneshot};
    let workspace = Workspace::new();
    let store = workspace.store();
    let a = workspace.create(&store);
    let b = workspace.create(&store);
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (started_tx, started_rx) = oneshot::channel();
    let (release_tx, release_rx) = oneshot::channel();
    let server = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
        let request = crate::test_support::read_request(&mut socket).await;
        started_tx.send(()).unwrap();
        release_rx.await.unwrap();
        let body = serde_json::to_vec(&completed("done")).unwrap();
        socket.write_all(format!("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n", body.len()).as_bytes()).await.unwrap();
        socket.write_all(&body).await.unwrap();
        request
    });
    let manager = SessionManager::new(
        store,
        ResponsesClient::for_test(format!("http://{address}/v1")),
        workspace.store().open(&a).unwrap(),
        workspace.0.clone(),
        "test".into(),
        CompactionConfig::new(262_144),
    )
    .unwrap();
    let (tx, rx) = mpsc::unbounded_channel();
    let (out, mut events) = mpsc::unbounded_channel();
    let worker = tokio::spawn(manager.run(rx, out));
    let _ = event(&mut events).await;
    tx.send(SessionCommand::Send {
        session_id: a.clone(),
        message: "first".into(),
    })
    .unwrap();
    started_rx.await.unwrap();
    tx.send(SessionCommand::Open {
        session_id: b.clone(),
    })
    .unwrap();
    tx.send(SessionCommand::Send {
        session_id: a.clone(),
        message: "must-not-run".into(),
    })
    .unwrap();
    tx.send(SessionCommand::List).unwrap();
    let mut rejections = 0;
    loop {
        match event(&mut events).await {
            SessionEvent::Error { message } => {
                assert!(message.contains("busy"));
                rejections += 1;
            }
            SessionEvent::Listed(list) => {
                assert_eq!(list.len(), 2);
                break;
            }
            SessionEvent::Agent {
                event: AgentEvent::TurnCompleted,
                ..
            } => panic!("finished before release"),
            _ => {}
        }
    }
    assert_eq!(rejections, 2);
    release_tx.send(()).unwrap();
    finished(&mut events, &a).await;
    tx.send(SessionCommand::Open {
        session_id: b.clone(),
    })
    .unwrap();
    assert!(
        matches!(event(&mut events).await, SessionEvent::Opened { metadata, .. } if metadata.id == b)
    );
    drop(tx);
    worker.await.unwrap();
    assert!(!server.await.unwrap().to_string().contains("must-not-run"));
}

#[test]
fn title_attempt_is_claimed_once_and_failures_do_not_retry() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = workspace.create(&store);
    assert_eq!(store.metadata(&id).unwrap().title, store::DEFAULT_TITLE);
    assert_eq!(
        store.metadata(&id).unwrap().title_source.as_deref(),
        Some("pending")
    );
    assert!(store.claim_auto_title(&id).unwrap(), "first claim wins");
    assert!(!store.claim_auto_title(&id).unwrap(), "never claims twice");
    // A failed or empty job leaves provenance `auto` and the default title:
    // a later turn or restart must not retry.
    assert_eq!(
        store.metadata(&id).unwrap().title_source.as_deref(),
        Some("auto")
    );
    assert_eq!(store.metadata(&id).unwrap().title, store::DEFAULT_TITLE);
    let applied = store.apply_auto_title(&id, "Generated title").unwrap();
    assert_eq!(applied.unwrap().title, "Generated title");
    assert!(
        store
            .apply_auto_title(&id, "Second title")
            .unwrap()
            .is_none(),
        "an applied title is not replaced"
    );
    assert!(!store.claim_auto_title(&id).unwrap());
}

#[test]
fn manual_rename_always_wins_over_a_late_auto_title() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = workspace.create(&store);
    assert!(store.claim_auto_title(&id).unwrap());
    let manual = store.rename(&id, "My choice").unwrap();
    assert_eq!(manual.title_source.as_deref(), Some("manual"));
    assert!(
        store.apply_auto_title(&id, "Late title").unwrap().is_none(),
        "auto must not overwrite a manual rename"
    );
    assert_eq!(store.metadata(&id).unwrap().title, "My choice");
}

#[test]
fn metadata_without_provenance_keeps_its_explicit_title() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = workspace.create(&store);
    let path = store.directory(&id).unwrap().join("metadata.json");
    let mut metadata: serde_json::Value =
        serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
    metadata["title"] = json!("Existing explicit name");
    metadata.as_object_mut().unwrap().remove("title_source");
    fs::write(&path, serde_json::to_vec_pretty(&metadata).unwrap()).unwrap();
    let loaded = store.metadata(&id).unwrap();
    assert_eq!(loaded.title, "Existing explicit name");
    assert!(
        loaded.title_source.is_none(),
        "old metadata has no provenance"
    );
    assert!(!store.claim_auto_title(&id).unwrap());
    assert!(
        store
            .apply_auto_title(&id, "Never applied")
            .unwrap()
            .is_none()
    );
    assert_eq!(store.metadata(&id).unwrap().title, "Existing explicit name");
}

#[test]
fn title_from_output_sanitizes_lines_and_rejects_the_default() {
    let message = |text: &str| crate::protocol::OutputItem::Message {
        id: "m".to_owned(),
        status: "completed".to_owned(),
        role: crate::protocol::MessageRole::Assistant,
        content: vec![crate::protocol::OutputText {
            text: text.to_owned(),
        }],
    };
    assert_eq!(
        title_from_output(&[message("  \"Fix login bug\"  ")]).as_deref(),
        Some("Fix login bug")
    );
    assert_eq!(
        title_from_output(&[message("First line\nsecond line")]).as_deref(),
        Some("First line")
    );
    assert_eq!(title_from_output(&[message("   ")]), None);
    assert_eq!(title_from_output(&[message("New session")]), None);
    assert_eq!(title_from_output(&[]), None);
    let long = "word ".repeat(40);
    let title = title_from_output(&[message(&long)]).unwrap();
    assert!(title.chars().count() <= 60);
}

#[test]
fn a_title_result_for_another_session_updates_that_store_entry_only() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let a = workspace.create(&store);
    let b = workspace.create(&store);
    assert!(store.claim_auto_title(&a).unwrap());
    let loaded_b = store.open(&b).unwrap();
    let (client, _server) = block_on(scripted_http(vec![]));
    let mut manager = SessionManager::new(
        store,
        client,
        loaded_b,
        workspace.0.clone(),
        "test".into(),
        CompactionConfig::new(262_144),
    )
    .unwrap();
    let (out, mut events) = mpsc::unbounded_channel();
    manager.apply_title(
        super::TitleJobResult {
            session_id: a.clone(),
            title: Some("Title for A".to_owned()),
        },
        &out,
    );
    let updated = match events.try_recv().unwrap() {
        SessionEvent::TitleUpdated(metadata) => metadata,
        _ => panic!("expected TitleUpdated"),
    };
    assert_eq!(updated.id, a);
    assert_eq!(updated.title, "Title for A");
    // The active session metadata was not touched by another session's result.
    assert_eq!(manager.metadata.id, b);
    assert_eq!(manager.metadata.title, store::DEFAULT_TITLE);
    let store = workspace.store();
    assert_eq!(store.metadata(&a).unwrap().title, "Title for A");
    assert_eq!(store.metadata(&b).unwrap().title, store::DEFAULT_TITLE);
}

fn block_on<T>(future: impl std::future::Future<Output = T>) -> T {
    tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .unwrap()
        .block_on(future)
}

#[tokio::test]
async fn cancel_emits_one_terminal_event_and_stale_cancels_are_harmless() {
    use tokio::{io::AsyncWriteExt, net::TcpListener, sync::oneshot};
    let workspace = Workspace::new();
    let store = workspace.store();
    let a = workspace.create(&store);
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (started_tx, started_rx) = oneshot::channel();
    let (release_tx, release_rx) = oneshot::channel();
    let server = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
        let _request = crate::test_support::read_request(&mut socket).await;
        started_tx.send(()).unwrap();
        let _ = release_rx.await;
        let body = serde_json::to_vec(&completed("too late")).unwrap();
        let _ = socket
            .write_all(
                format!(
                    "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                    body.len()
                )
                .as_bytes(),
            )
            .await;
        let _ = socket.write_all(&body).await;
    });
    let manager = SessionManager::new(
        store,
        ResponsesClient::for_test(format!("http://{address}/v1")),
        workspace.store().open(&a).unwrap(),
        workspace.0.clone(),
        "test".into(),
        CompactionConfig::new(262_144),
    )
    .unwrap();
    let (tx, rx) = mpsc::unbounded_channel();
    let (out, mut events) = mpsc::unbounded_channel();
    let worker = tokio::spawn(manager.run(rx, out));
    assert!(matches!(
        event(&mut events).await,
        SessionEvent::Opened { .. }
    ));
    tx.send(SessionCommand::Send {
        session_id: a.clone(),
        message: "start".into(),
    })
    .unwrap();
    started_rx.await.unwrap();
    tx.send(SessionCommand::Cancel {
        session_id: a.clone(),
    })
    .unwrap();
    tx.send(SessionCommand::Cancel {
        session_id: a.clone(),
    })
    .unwrap();
    tx.send(SessionCommand::Cancel {
        session_id: "another-session".into(),
    })
    .unwrap();
    tx.send(SessionCommand::List).unwrap();
    // Keep the server response held until the cancellation is observed, so the
    // worker cannot win the race by completing normally.
    let mut terminals = 0;
    let mut interrupted = 0;
    let mut errors = 0;
    let mut listed = false;
    while !listed || interrupted == 0 {
        match event(&mut events).await {
            SessionEvent::Agent {
                event: AgentEvent::TurnInterrupted,
                ..
            } => {
                interrupted += 1;
                terminals += 1;
            }
            SessionEvent::Agent {
                event: AgentEvent::TurnCompleted | AgentEvent::TurnFailed { .. },
                ..
            } => terminals += 1,
            SessionEvent::Listed(_) => listed = true,
            SessionEvent::Error { .. } => errors += 1,
            _ => {}
        }
    }
    // Release the held response only after the interrupted terminal was
    // observed; the worker must never win this race by completing normally.
    release_tx.send(()).unwrap();
    assert!(
        terminals == 1,
        "the held response must not complete the cancelled turn: {terminals}"
    );
    // One stale cancel was sent while idle, after the turn was terminal.
    tx.send(SessionCommand::Cancel {
        session_id: a.clone(),
    })
    .unwrap();
    tx.send(SessionCommand::List).unwrap();
    loop {
        match event(&mut events).await {
            SessionEvent::Listed(_) => break,
            SessionEvent::Error { .. } => errors += 1,
            _ => {}
        }
    }
    drop(tx);
    worker.await.unwrap();
    server.await.unwrap();
    assert_eq!(interrupted, 1, "exactly one interrupted terminal event");
    assert_eq!(terminals, 1, "exactly one terminal event for the turn");
    assert_eq!(errors, 0, "cancelling is never an error: {errors}");
}
