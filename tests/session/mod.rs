use super::*;
use crate::client::config::RESPONSES_API;
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
use tokio::{sync::mpsc, task::JoinHandle};

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

struct Requests {
    tx: mpsc::Sender<SessionRequest>,
    next_id: u64,
}

impl Requests {
    fn new(tx: mpsc::Sender<SessionRequest>) -> Self {
        Self { tx, next_id: 0 }
    }

    async fn send(&mut self, command: SessionCommand) -> u64 {
        let request_id = self.next_id;
        self.next_id += 1;
        self.tx
            .send(SessionRequest {
                request_id,
                command,
            })
            .await
            .expect("send SessionRequest");
        request_id
    }
}

fn start_manager(
    manager: SessionManager,
) -> (
    Requests,
    SessionEventReceiver,
    JoinHandle<anyhow::Result<()>>,
) {
    let (request_tx, request_rx) = mpsc::channel(32);
    let (output, events) = manager.event_channel();
    let worker = tokio::spawn(manager.run(request_rx, output));
    (Requests::new(request_tx), events, worker)
}

async fn write_completion(socket: &mut tokio::net::TcpStream, text: &str) {
    use tokio::io::AsyncWriteExt;

    let body = serde_json::to_vec(&completed(text)).unwrap();
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
}

async fn packet(rx: &mut SessionEventReceiver) -> SessionEventPacket {
    tokio::time::timeout(Duration::from_secs(5), rx.recv())
        .await
        .expect("session event timed out")
        .expect("session event channel closed")
}

async fn event(rx: &mut SessionEventReceiver) -> SessionEvent {
    packet(rx).await.event
}

async fn request_event(rx: &mut SessionEventReceiver, request_id: u64) -> SessionEventPacket {
    loop {
        let packet = packet(rx).await;
        if packet.request_id == Some(request_id) {
            return packet;
        }
    }
}

async fn accept_task(
    requests: &mut Requests,
    events: &mut SessionEventReceiver,
    session_id: &str,
    text: &str,
) -> (u64, crate::session::runtime::Association) {
    let request_id = requests
        .send(SessionCommand::Send {
            session_id: session_id.to_owned(),
            message: text.into(),
        })
        .await;
    let packet = request_event(events, request_id).await;
    assert!(
        matches!(packet.event, SessionEvent::Accepted),
        "Accepted must be the first task reply"
    );
    (
        request_id,
        packet
            .association
            .expect("Accepted must carry a task association"),
    )
}

async fn finish_task(
    events: &mut SessionEventReceiver,
    association: &crate::session::runtime::Association,
) -> crate::session::runtime::TaskFinished {
    loop {
        let packet = packet(events).await;
        if packet
            .association
            .as_ref()
            .is_some_and(|value| value.task_id == association.task_id)
            && let SessionEvent::Finished(finished) = packet.event
        {
            return finished;
        }
    }
}

async fn send_and_finish(
    requests: &mut Requests,
    events: &mut SessionEventReceiver,
    session_id: &str,
    text: &str,
) -> (
    u64,
    crate::session::runtime::Association,
    crate::session::runtime::TaskFinished,
) {
    let (request_id, association) = accept_task(requests, events, session_id, text).await;
    let finished = finish_task(events, &association).await;
    (request_id, association, finished)
}

async fn titled(rx: &mut SessionEventReceiver, expected: &str) -> SessionMetadata {
    loop {
        let packet = packet(rx).await;
        if let SessionEvent::TitleUpdated(metadata) = packet.event {
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
    let mut loaded = store.open_for_api(&id, RESPONSES_API).unwrap();
    assert!(store.open_for_api(&id, RESPONSES_API).is_err());
    assert!(store.list().unwrap()[0].in_use);
    loaded.conversation.add_user_message("remember red-71");
    loaded.conversation.save_history().unwrap();
    drop(loaded);
    let loaded = store.open_for_api(&id, RESPONSES_API).unwrap();
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
    let mut loaded = store.open_for_api(&id, RESPONSES_API).unwrap();
    assert_eq!(fs::read(&path).unwrap(), original);
    loaded.conversation.add_user_message("after torn write");
    loaded.conversation.save_history().unwrap();
    drop(loaded);
    assert_eq!(
        store
            .open_for_api(&id, RESPONSES_API)
            .unwrap()
            .replay
            .context
            .len(),
        2
    );
    fs::OpenOptions::new()
        .append(true)
        .open(&path)
        .unwrap()
        .write_all(b"{broken}\n")
        .unwrap();
    let corrupted = fs::read(&path).unwrap();
    assert!(
        store
            .open_for_api(&id, RESPONSES_API)
            .err()
            .unwrap()
            .to_string()
            .contains(":3:")
    );
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
    let mut loaded = store.open_for_api(&id, RESPONSES_API).unwrap();
    loaded.conversation.add_user_message("second record");
    loaded.conversation.save_history().unwrap();
    drop(loaded);
    assert_eq!(
        store
            .open_for_api(&id, RESPONSES_API)
            .unwrap()
            .replay
            .context
            .len(),
        2
    );
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
    let tool = |id: &str| json!({"id":"tool-response", "status":"completed", "output":[{"type":"function_call", "status":"completed", "id":id, "call_id":id, "name":"read", "arguments":"{\"path\":\"marker.txt\"}"}]});
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
    let loaded = store.open_for_api(&a, RESPONSES_API).unwrap();
    let manager = SessionManager::new(
        store,
        client,
        loaded,
        workspace.0.clone(),
        "test".into(),
        CompactionConfig::new(262_144),
    )
    .unwrap();
    let (mut requests, mut events, worker) = start_manager(manager);
    match event(&mut events).await {
        SessionEvent::Opened { metadata, .. } => assert_eq!(metadata.id, a),
        _ => panic!("expected the initial A snapshot"),
    }

    let (_, _, finished) = send_and_finish(&mut requests, &mut events, &a, "secret-A").await;
    assert_eq!(finished.status, "completed");
    let title_a = titled(&mut events, &a).await;
    assert_eq!(title_a.title, "Title A");
    assert_eq!(title_a.title_source.as_deref(), Some("auto"));

    let request_id = requests
        .send(SessionCommand::Open {
            session_id: b.clone(),
        })
        .await;
    match request_event(&mut events, request_id).await.event {
        SessionEvent::Opened {
            metadata,
            replay,
            context: AgentEvent::ContextUpdated { context_window, .. },
            ..
        } => {
            assert_eq!(metadata.id, b);
            assert_eq!(replay.context.len(), 1);
            assert_eq!(context_window, Some(131_072));
        }
        _ => panic!("expected B snapshot"),
    }

    let (_, _, finished) = send_and_finish(&mut requests, &mut events, &b, "secret-B").await;
    assert_eq!(finished.status, "completed");
    let title_b = titled(&mut events, &b).await;
    assert_eq!(title_b.title, "Title B");

    let request_id = requests
        .send(SessionCommand::Open {
            session_id: a.clone(),
        })
        .await;
    match request_event(&mut events, request_id).await.event {
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

    let request_id = requests
        .send(SessionCommand::Open {
            session_id: "missing".into(),
        })
        .await;
    match request_event(&mut events, request_id).await.event {
        SessionEvent::Error { operation, .. } => {
            assert_eq!(operation, "open");
        }
        _ => panic!("expected a missing-session error"),
    }

    let (_, _, finished) = send_and_finish(&mut requests, &mut events, &a, "continue-A").await;
    assert_eq!(finished.status, "completed");
    drop(requests);
    worker.await.unwrap().unwrap();
    let requests = server.await.unwrap();
    assert_eq!(requests.len(), 7);
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
    let a_again = store.open_for_api(&a, RESPONSES_API).unwrap();
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
    let mut loaded = store.open_for_api(&id, RESPONSES_API).unwrap();
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
    let loaded = store.open_for_api(&id, RESPONSES_API).unwrap();
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
        store
            .open_for_api(&id, RESPONSES_API)
            .unwrap()
            .replay
            .status,
        TaskStatus::Completed
    );
}

#[tokio::test]
async fn running_a_allows_list_open_b_and_return_to_a_without_starting_b() {
    use tokio::{io::AsyncWriteExt, net::TcpListener, sync::oneshot};
    let workspace = Workspace::new();
    let store = workspace.store();
    let a = workspace.create(&store);
    let b = workspace.create(&store);
    // Keep this test focused on terminal navigation; automatic title work is
    // covered separately and would add a second model request.
    store.rename(&a, "Manual A").unwrap();
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (started_tx, started_rx) = oneshot::channel();
    let (release_tx, release_rx) = oneshot::channel();
    let server = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
        let request = crate::test_support::read_request(&mut socket).await;
        started_tx.send(()).unwrap();
        tokio::select! {
            biased;
            _ = listener.accept() => panic!("read-only navigation opened a second model connection"),
            result = release_rx => result.unwrap(),
        }
        let body = serde_json::to_vec(&completed("done")).unwrap();
        socket
            .write_all(
                format!(
                    "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                    body.len()
                )
                .as_bytes(),
            )
            .await
            .unwrap();
        socket.write_all(&body).await.unwrap();
        vec![request]
    });
    let manager = SessionManager::new(
        store,
        ModelClient::for_test(format!("http://{address}/v1")),
        workspace.store().open_for_api(&a, RESPONSES_API).unwrap(),
        workspace.0.clone(),
        "test".into(),
        CompactionConfig::new(262_144),
    )
    .unwrap();
    let (mut requests, mut events, worker) = start_manager(manager);
    match event(&mut events).await {
        SessionEvent::Opened { metadata, .. } => assert_eq!(metadata.id, a),
        _ => panic!("expected the initial A snapshot"),
    }

    let (_, association) = accept_task(&mut requests, &mut events, &a, "first").await;
    started_rx.await.unwrap();

    let request_id = requests.send(SessionCommand::List).await;
    match request_event(&mut events, request_id).await.event {
        SessionEvent::Listed(sessions) => {
            assert_eq!(sessions.len(), 2);
            assert!(
                sessions
                    .iter()
                    .find(|session| session.metadata.id == a)
                    .unwrap()
                    .in_use
            );
            assert!(
                !sessions
                    .iter()
                    .find(|session| session.metadata.id == b)
                    .unwrap()
                    .in_use
            );
        }
        _ => panic!("expected a session list while A is active"),
    }

    let request_id = requests
        .send(SessionCommand::Open {
            session_id: b.clone(),
        })
        .await;
    match request_event(&mut events, request_id).await.event {
        SessionEvent::Opened { metadata, .. } => assert_eq!(metadata.id, b),
        _ => panic!("expected to open B while A is active"),
    }

    let request_id = requests
        .send(SessionCommand::Open {
            session_id: a.clone(),
        })
        .await;
    match request_event(&mut events, request_id).await.event {
        SessionEvent::Opened { metadata, .. } => assert_eq!(metadata.id, a),
        _ => panic!("expected to return to A while its task is active"),
    }

    // Opening/listing B must not start a second model task. The controlled
    // server is still holding A's first request at this point.
    release_tx.send(()).unwrap();
    let finished = finish_task(&mut events, &association).await;
    assert_eq!(finished.status, "completed");
    drop(requests);
    worker.await.unwrap().unwrap();
    let requests = server.await.unwrap();
    assert_eq!(requests.len(), 1, "B must not start a model request");
    assert!(requests[0].to_string().contains("first"));
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

#[tokio::test]
async fn cancel_uses_the_accepted_task_id_and_old_ids_do_not_cancel_a_new_task() {
    use tokio::{net::TcpListener, sync::oneshot};
    let workspace = Workspace::new();
    let store = workspace.store();
    let a = workspace.create(&store);
    store.rename(&a, "Manual A").unwrap();
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (started_tx, started_rx) = oneshot::channel();
    let (release_tx, release_rx) = oneshot::channel();
    let (second_started_tx, second_started_rx) = oneshot::channel();
    let (second_release_tx, second_release_rx) = oneshot::channel();
    let server = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
        let first = crate::test_support::read_request(&mut socket).await;
        started_tx.send(()).unwrap();
        release_rx.await.unwrap();
        write_completion(&mut socket, "too late after cancel").await;

        let (mut socket, _) = listener.accept().await.unwrap();
        let second = crate::test_support::read_request(&mut socket).await;
        second_started_tx.send(()).unwrap();
        second_release_rx.await.unwrap();
        write_completion(&mut socket, "fresh task completed").await;
        vec![first, second]
    });
    let manager = SessionManager::new(
        store,
        ModelClient::for_test(format!("http://{address}/v1")),
        workspace.store().open_for_api(&a, RESPONSES_API).unwrap(),
        workspace.0.clone(),
        "test".into(),
        CompactionConfig::new(262_144),
    )
    .unwrap();
    let (mut requests, mut events, worker) = start_manager(manager);
    match event(&mut events).await {
        SessionEvent::Opened { metadata, .. } => assert_eq!(metadata.id, a),
        _ => panic!("expected the initial A snapshot"),
    }

    let (_, old_task) = accept_task(&mut requests, &mut events, &a, "first").await;
    started_rx.await.unwrap();

    let request_id = requests
        .send(SessionCommand::Cancel {
            session_id: a.clone(),
            task_id: old_task.task_id.clone(),
        })
        .await;
    match request_event(&mut events, request_id).await.event {
        SessionEvent::Cancelled { status } => assert_eq!(status, "requested"),
        _ => panic!("expected a requested cancellation"),
    }
    let interrupted = finish_task(&mut events, &old_task).await;
    assert_eq!(interrupted.status, "interrupted");
    release_tx.send(()).unwrap();

    let (_, new_task) = accept_task(&mut requests, &mut events, &a, "second").await;
    second_started_rx.await.unwrap();

    // The runtime must resolve this by the actual task id, not by session/latest.
    let request_id = requests
        .send(SessionCommand::Cancel {
            session_id: a.clone(),
            task_id: old_task.task_id.clone(),
        })
        .await;
    match request_event(&mut events, request_id).await.event {
        SessionEvent::Cancelled { status } => assert_eq!(status, "not_running"),
        _ => panic!("expected an old-id cancellation to be not_running"),
    }

    second_release_tx.send(()).unwrap();
    let completed = finish_task(&mut events, &new_task).await;
    assert_eq!(completed.status, "completed");
    drop(requests);
    worker.await.unwrap().unwrap();
    let requests = server.await.unwrap();
    assert_eq!(requests.len(), 2);
    assert!(requests[0].to_string().contains("first"));
    assert!(requests[1].to_string().contains("second"));
}

#[tokio::test]
async fn a_session_created_on_another_api_is_not_opened_or_changed() {
    use crate::client::config::CHAT_COMPLETIONS_API;
    let workspace = Workspace::new();
    let store = workspace.store();
    let responses_session = workspace.create(&store);
    let chat_session = store
        .create_with_api(
            &workspace.0,
            "test",
            SYSTEM_PROMPT,
            CHAT_COMPLETIONS_API,
            CompactionConfig::new(262_144),
        )
        .unwrap();
    let loaded = store
        .open_for_api(&responses_session, RESPONSES_API)
        .unwrap();
    let (client, _server) = scripted_http(vec![]).await;
    let manager = SessionManager::new(
        store,
        client,
        loaded,
        workspace.0.clone(),
        "test".into(),
        CompactionConfig::new(262_144),
    )
    .unwrap();
    let (mut requests, mut events, worker) = start_manager(manager);
    match event(&mut events).await {
        SessionEvent::Opened { metadata, .. } => assert_eq!(metadata.id, responses_session),
        _ => panic!("expected initial session"),
    }

    let request_id = requests
        .send(SessionCommand::Open {
            session_id: chat_session.clone(),
        })
        .await;
    match request_event(&mut events, request_id).await.event {
        SessionEvent::Error { operation, error } => {
            assert_eq!(operation, "open");
            assert_eq!(error.code, "api_mismatch");
            assert_eq!(error.session_id.as_deref(), Some(chat_session.as_str()));
        }
        _ => panic!("incompatible session was opened"),
    }

    let request_id = requests.send(SessionCommand::List).await;
    match request_event(&mut events, request_id).await.event {
        SessionEvent::Listed(sessions) => {
            assert!(
                sessions
                    .iter()
                    .find(|session| session.metadata.id == responses_session)
                    .unwrap()
                    .in_use
            );
            assert!(
                !sessions
                    .iter()
                    .find(|session| session.metadata.id == chat_session)
                    .unwrap()
                    .in_use
            );
        }
        _ => panic!("expected unchanged session listing"),
    }
    drop(requests);
    worker.await.unwrap().unwrap();
    assert_eq!(
        workspace.store().metadata(&chat_session).unwrap().api,
        CHAT_COMPLETIONS_API
    );
}

#[test]
fn cross_api_open_with_torn_tail_fails_before_repairing_the_history() {
    use crate::client::config::CHAT_COMPLETIONS_API;
    let workspace = Workspace::new();
    let store = workspace.store();
    let chat_session = store
        .create_with_api(
            &workspace.0,
            "test",
            SYSTEM_PROMPT,
            CHAT_COMPLETIONS_API,
            CompactionConfig::new(262_144),
        )
        .unwrap();
    let path = store
        .directory(&chat_session)
        .unwrap()
        .join("history.jsonl");
    fs::OpenOptions::new()
        .append(true)
        .open(&path)
        .unwrap()
        .write_all(b"{\"schema_ver")
        .unwrap();
    let torn = fs::read(&path).unwrap();

    // Wrong API: fails before `Conversation::restore` can trim the torn tail.
    let error = store
        .open_for_api(&chat_session, RESPONSES_API)
        .map(|_| ())
        .unwrap_err();
    assert!(
        error
            .to_string()
            .contains("was created for API openai-chat-completions"),
        "{error:?}"
    );
    assert_eq!(fs::read(&path).unwrap(), torn);

    // The right API still restores and repairs the torn tail as before.
    let loaded = store
        .open_for_api(&chat_session, CHAT_COMPLETIONS_API)
        .unwrap();
    drop(loaded);
    assert!(fs::read(&path).unwrap().len() < torn.len());
}
