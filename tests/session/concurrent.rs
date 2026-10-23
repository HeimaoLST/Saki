use std::{fs, path::PathBuf, time::Duration};

use serde_json::{Value, json};
use tokio::{
    io::AsyncWriteExt,
    net::{TcpListener, TcpStream},
    sync::oneshot,
    task::JoinHandle,
};

use crate::{
    agent::{
        AgentEvent,
        conversation::CompactionConfig,
        events::EventLimits,
        journal::{JournalRecord, TaskStatus},
        message::UserMessage,
    },
    client::{ModelClient, config::RESPONSES_API},
    session::{
        SYSTEM_PROMPT,
        runtime::{
            AcceptedTask, HistoryPage, Runtime, RuntimeCommand, RuntimeError, RuntimeHandle,
            RuntimeReply, SessionState, TaskEvent, TaskEvents, TaskFinished, TaskState,
        },
        store::SessionStore,
    },
    test_support::{completed, read_request, scripted_http},
};

struct Workspace(PathBuf);

impl Workspace {
    fn new() -> Self {
        static NEXT: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
        let path = std::env::temp_dir().join(format!(
            "saki-concurrent-{}-{}",
            crate::agent::journal::new_id().unwrap(),
            NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        ));
        fs::create_dir(&path).unwrap();
        Self(path)
    }

    fn store(&self) -> SessionStore {
        SessionStore::new(self.0.join("sessions")).unwrap()
    }

    fn directory(&self, session_id: &str) -> PathBuf {
        self.store().directory(session_id).unwrap()
    }

    fn history_bytes(&self, session_id: &str) -> Vec<u8> {
        fs::read(self.directory(session_id).join("history.jsonl")).unwrap()
    }
}

impl Drop for Workspace {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn runtime(client: ModelClient, workspace: &Workspace) -> Runtime {
    Runtime::new(
        workspace.store(),
        client,
        workspace.0.clone(),
        "test".to_owned(),
        CompactionConfig::new(262_144),
    )
    .with_max_active_tasks(2)
    .unwrap()
}

fn session(reply: RuntimeReply) -> SessionState {
    match reply {
        RuntimeReply::Session(state) => state,
        _ => panic!("expected a session reply"),
    }
}

async fn create_in(handle: &RuntimeHandle, working_directory: Option<PathBuf>) -> String {
    session(
        handle
            .call(RuntimeCommand::Create {
                working_directory,
                system_prompt: None,
            })
            .await
            .unwrap(),
    )
    .metadata
    .expect("create reply must contain metadata")
    .id
}

async fn create_session(handle: &RuntimeHandle) -> String {
    create_in(handle, None).await
}

async fn load_session(handle: &RuntimeHandle, session_id: &str) -> SessionState {
    session(
        handle
            .call(RuntimeCommand::Load {
                session_id: session_id.to_owned(),
            })
            .await
            .unwrap(),
    )
}

async fn send_task(handle: &RuntimeHandle, session_id: &str, text: &str) -> AcceptedTask {
    match handle
        .call(RuntimeCommand::Send {
            session_id: session_id.to_owned(),
            message: UserMessage::text(text),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Accepted(task) => task,
        _ => panic!("expected an accepted task"),
    }
}

async fn send_error(handle: &RuntimeHandle, session_id: &str, text: &str) -> RuntimeError {
    match handle
        .call(RuntimeCommand::Send {
            session_id: session_id.to_owned(),
            message: UserMessage::text(text),
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("send was unexpectedly accepted"),
    }
}

async fn read_terminal(events: &mut TaskEvents) -> TaskFinished {
    let mut terminal = None;
    while let Some(event) = events.next().await {
        if let TaskEvent::Finished(finished) = event {
            assert!(terminal.is_none(), "terminal was delivered twice");
            terminal = Some(finished);
        }
    }
    terminal.expect("task did not deliver a terminal")
}

async fn finish(events: TaskEvents) -> TaskFinished {
    let mut events = events;
    read_terminal(&mut events).await
}

async fn read_terminal_with_marker(
    events: TaskEvents,
    marker: &str,
    other_marker: &str,
) -> TaskFinished {
    let mut events = events;
    let mut terminal = None;
    let mut saw_tool = false;
    while let Some(event) = events.next().await {
        match event {
            TaskEvent::Event(packet) => {
                if let AgentEvent::ToolFinished { output, .. } = packet.event {
                    let output = serde_json::to_string(&output).unwrap();
                    assert!(output.contains(marker), "tool output lost its cwd marker");
                    assert!(
                        !output.contains(other_marker),
                        "tool output crossed session cwd"
                    );
                    saw_tool = true;
                }
            }
            TaskEvent::Finished(finished) => {
                assert!(terminal.is_none(), "terminal was delivered twice");
                terminal = Some(finished);
            }
        }
    }
    assert!(saw_tool, "task did not emit its own ToolFinished event");
    terminal.expect("task did not deliver a terminal")
}

async fn wait_waiting(events: &TaskEvents) {
    tokio::time::timeout(Duration::from_secs(5), async {
        while events.buffer_stats().waiting_producers == 0 {
            tokio::task::yield_now().await;
        }
    })
    .await
    .expect("event producer did not become pending");
    assert_eq!(events.buffer_stats().waiting_producers, 1);
}

async fn task_status(handle: &RuntimeHandle, session_id: &str, task_id: &str) -> TaskState {
    match handle
        .call(RuntimeCommand::TaskStatus {
            session_id: session_id.to_owned(),
            task_id: task_id.to_owned(),
        })
        .await
        .unwrap()
    {
        RuntimeReply::TaskStatus(state) => state,
        _ => panic!("expected a task status reply"),
    }
}

async fn cancel(handle: &RuntimeHandle, session_id: &str, task_id: &str) -> &'static str {
    match handle
        .call(RuntimeCommand::Cancel {
            session_id: session_id.to_owned(),
            task_id: task_id.to_owned(),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Cancelled { status } => status,
        _ => panic!("expected a cancel reply"),
    }
}

async fn history_for(handle: &RuntimeHandle, session_id: &str) -> HistoryPage {
    match handle
        .call(RuntimeCommand::History {
            session_id: session_id.to_owned(),
            offset: 0,
            limit: usize::MAX,
        })
        .await
        .unwrap()
    {
        RuntimeReply::History(page) => page,
        _ => panic!("expected a history reply"),
    }
}

async fn request_bodies(server: JoinHandle<Vec<Value>>) -> Vec<Value> {
    tokio::time::timeout(Duration::from_secs(5), server)
        .await
        .expect("model fixture did not observe the expected requests")
        .unwrap()
}

async fn write_json(socket: &mut TcpStream, value: &Value) {
    write_json_status(socket, 200, value).await;
}

async fn write_json_status(socket: &mut TcpStream, status: u16, value: &Value) {
    let body = serde_json::to_vec(value).unwrap();
    let head = format!(
        "HTTP/1.1 {status} Test\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
        body.len()
    );
    let _ = socket.write_all(head.as_bytes()).await;
    let _ = socket.write_all(&body).await;
}

/// Serve `gated` requests concurrently: none is answered until all gated
/// requests have arrived and the caller releases the gate. Requests after the
/// gate are answered immediately.
async fn gated_model(
    gated: usize,
    total: usize,
) -> (
    ModelClient,
    oneshot::Receiver<()>,
    oneshot::Sender<()>,
    JoinHandle<Vec<Value>>,
) {
    assert!(gated > 0 && total >= gated);
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (arrived_tx, arrived) = oneshot::channel();
    let (release, release_rx) = oneshot::channel();
    let worker = tokio::spawn(async move {
        let mut first = Vec::new();
        for _ in 0..gated {
            let (mut socket, _) = listener.accept().await.unwrap();
            let request = read_request(&mut socket).await;
            first.push((socket, request));
        }
        let _ = arrived_tx.send(());
        let _ = release_rx.await;
        let mut requests = Vec::new();
        for (mut socket, request) in first {
            write_json(&mut socket, &completed("gated")).await;
            requests.push(request);
        }
        for _ in gated..total {
            let (mut socket, _) = listener.accept().await.unwrap();
            let request = read_request(&mut socket).await;
            write_json(&mut socket, &completed("ungated")).await;
            requests.push(request);
        }
        requests
    });
    (
        ModelClient::for_test(format!("http://{address}/v1")),
        arrived,
        release,
        worker,
    )
}

async fn gated_tool_model() -> (
    ModelClient,
    oneshot::Receiver<()>,
    oneshot::Sender<()>,
    JoinHandle<Vec<Value>>,
) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (arrived_tx, arrived) = oneshot::channel();
    let (release, release_rx) = oneshot::channel();
    let worker = tokio::spawn(async move {
        let mut first = Vec::new();
        for _ in 0..2 {
            let (mut socket, _) = listener.accept().await.unwrap();
            let request = read_request(&mut socket).await;
            first.push((socket, request));
        }
        let _ = arrived_tx.send(());
        let _ = release_rx.await;
        let mut requests = Vec::new();
        for (mut socket, request) in first {
            let response = json!({
                "id": "tool-response",
                "status": "completed",
                "output": [{
                    "type": "function_call",
                    "status": "completed",
                    "id": "shared-item",
                    "call_id": "shared-call",
                    "name": "read",
                    "arguments": "{\"path\":\"probe.txt\"}"
                }]
            });
            write_json(&mut socket, &response).await;
            requests.push(request);
        }
        for _ in 0..2 {
            let (mut socket, _) = listener.accept().await.unwrap();
            let request = read_request(&mut socket).await;
            write_json(&mut socket, &completed("tool follow-up")).await;
            requests.push(request);
        }
        requests
    });
    (
        ModelClient::for_test(format!("http://{address}/v1")),
        arrived,
        release,
        worker,
    )
}

async fn mixed_failure_model() -> (ModelClient, JoinHandle<Vec<Value>>) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let worker = tokio::spawn(async move {
        let mut requests = Vec::new();
        for _ in 0..2 {
            let (mut socket, _) = listener.accept().await.unwrap();
            let request = read_request(&mut socket).await;
            let input = request["input"].to_string();
            if input.contains("A-model-fail") {
                write_json_status(
                    &mut socket,
                    400,
                    &json!({"error": {
                        "message": "A model fixture failed",
                        "type": "invalid_request_error",
                        "code": "invalid_request"
                    }}),
                )
                .await;
            } else {
                assert!(input.contains("B-tool-fail"));
                write_json(
                    &mut socket,
                    &json!({
                        "id": "tool-response",
                        "status": "completed",
                        "output": [{
                            "type": "function_call",
                            "status": "completed",
                            "id": "tool-item",
                            "call_id": "tool-call",
                            "name": "read",
                            "arguments": "{\"path\":\"absent.txt\"}"
                        }]
                    }),
                )
                .await;
            }
            requests.push(request);
        }
        let (mut socket, _) = listener.accept().await.unwrap();
        let request = read_request(&mut socket).await;
        write_json(&mut socket, &completed("B after tool business failure")).await;
        requests.push(request);
        requests
    });
    (
        ModelClient::for_test(format!("http://{address}/v1")),
        worker,
    )
}

async fn shutdown_actor(handle: RuntimeHandle, actor: JoinHandle<anyhow::Result<()>>) {
    assert!(matches!(
        handle.call(RuntimeCommand::Shutdown).await.unwrap(),
        RuntimeReply::Shutdown { state: "draining" }
    ));
    drop(handle);
    actor.await.unwrap().unwrap();
}

#[tokio::test]
async fn capacity_two_overlaps_and_rejected_work_never_enters_the_journal_or_a_queue() {
    let workspace = Workspace::new();
    let (client, both_arrived, release, server) = gated_model(2, 3).await;
    let (handle, actor) = runtime(client, &workspace).start();
    let a = create_session(&handle).await;
    let b = create_session(&handle).await;
    let c = create_session(&handle).await;

    let task_a = send_task(&handle, &a, "A-marker").await;
    let task_b = send_task(&handle, &b, "B-marker").await;
    tokio::time::timeout(Duration::from_secs(5), both_arrived)
        .await
        .expect("A and B did not both reach the model gate")
        .expect("gate task dropped");

    let a_before_reject = workspace.history_bytes(&a);
    let c_before_reject = workspace.history_bytes(&c);
    let capacity = send_error(&handle, &c, "C-marker").await;
    assert_eq!(capacity.code, "busy");
    assert_eq!(capacity.reason, Some("capacity_exceeded"));
    let duplicate = send_error(&handle, &a, "A-duplicate").await;
    assert_eq!(duplicate.code, "busy");
    assert_eq!(duplicate.reason, Some("session_busy"));
    assert_eq!(workspace.history_bytes(&a), a_before_reject);
    assert_eq!(workspace.history_bytes(&c), c_before_reject);

    release.send(()).unwrap();
    let finished_a = finish(task_a.events).await;
    let finished_b = finish(task_b.events).await;
    assert_eq!(finished_a.status, "completed");
    assert_eq!(finished_b.status, "completed");

    // A rejected send must not be queued and replayed when capacity opens.
    handle.call(RuntimeCommand::List).await.unwrap();
    assert_eq!(workspace.history_bytes(&c), c_before_reject);
    let task_c = send_task(&handle, &c, "C-marker").await;
    assert_eq!(finish(task_c.events).await.status, "completed");

    let bodies = request_bodies(server).await;
    assert_eq!(bodies.len(), 3);
    let text = |body: &Value| body["input"].to_string();
    assert!(bodies.iter().any(|body| text(body).contains("A-marker")));
    assert!(bodies.iter().any(|body| text(body).contains("B-marker")));
    assert!(bodies.iter().any(|body| text(body).contains("C-marker")));
    assert!(!bodies.iter().any(|body| text(body).contains("A-duplicate")));
    shutdown_actor(handle, actor).await;
}

#[tokio::test]
async fn cancel_is_directed_and_task_status_reports_one_real_terminal_per_session() {
    let workspace = Workspace::new();
    let (client, both_arrived, release, server) = gated_model(2, 2).await;
    let (handle, actor) = runtime(client, &workspace).start();
    let a = create_session(&handle).await;
    let b = create_session(&handle).await;
    let task_a = send_task(&handle, &a, "A-cancel").await;
    let task_b = send_task(&handle, &b, "B-continue").await;
    tokio::time::timeout(Duration::from_secs(5), both_arrived)
        .await
        .expect("A and B did not both reach the model gate")
        .expect("gate task dropped");

    let a_running = task_status(&handle, &a, &task_a.association.task_id).await;
    let b_running = task_status(&handle, &b, &task_b.association.task_id).await;
    assert_eq!(a_running.status, "running");
    assert_eq!(a_running.phase, "running");
    assert_eq!(b_running.status, "running");
    assert_eq!(b_running.phase, "running");

    assert_eq!(cancel(&handle, &a, "wrong-task").await, "not_running");
    assert_eq!(
        cancel(&handle, &b, &task_a.association.task_id).await,
        "not_running"
    );
    assert_eq!(
        cancel(&handle, &a, &task_a.association.task_id).await,
        "requested"
    );
    assert!(matches!(
        cancel(&handle, &a, &task_a.association.task_id).await,
        "already_requested" | "not_running"
    ));

    let mut events_a = task_a.events;
    let mut events_b = task_b.events;
    events_a.defer_terminal_ack();
    events_b.defer_terminal_ack();
    release.send(()).unwrap();
    let finished_a = read_terminal(&mut events_a).await;
    let finished_b = read_terminal(&mut events_b).await;
    assert_eq!(finished_a.status, "interrupted");
    assert_eq!(finished_b.status, "completed");

    let a_finished = task_status(&handle, &a, &task_a.association.task_id).await;
    let b_finished = task_status(&handle, &b, &task_b.association.task_id).await;
    assert_eq!(a_finished.status, "interrupted");
    assert_eq!(a_finished.phase, "finished");
    assert!(a_finished.cancel_requested);
    assert_eq!(b_finished.status, "completed");
    assert_eq!(b_finished.phase, "finished");
    assert!(!b_finished.cancel_requested);
    assert_eq!(
        cancel(&handle, &a, &task_a.association.task_id).await,
        "not_running"
    );
    assert_eq!(
        cancel(&handle, &b, &task_b.association.task_id).await,
        "not_running"
    );
    events_a.acknowledge_finished();
    events_b.acknowledge_finished();

    let bodies = request_bodies(server).await;
    assert_eq!(bodies.len(), 2);
    assert!(
        bodies
            .iter()
            .any(|body| body["input"].to_string().contains("A-cancel"))
    );
    assert!(
        bodies
            .iter()
            .any(|body| body["input"].to_string().contains("B-continue"))
    );
    shutdown_actor(handle, actor).await;
}

#[tokio::test]
async fn panicking_worker_fails_only_its_session_and_the_other_task_keeps_its_history() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let config = CompactionConfig::new(262_144);
    let a = store
        .create(&workspace.0, "test", SYSTEM_PROMPT, config)
        .unwrap();
    let b = store
        .create(&workspace.0, "test", SYSTEM_PROMPT, config)
        .unwrap();
    let (client, server) = scripted_http(vec![(200, completed("B-ok"))]).await;
    let (handle, actor) = runtime(client, &workspace)
        .with_panicking_session(a.clone())
        .start();
    load_session(&handle, &a).await;
    load_session(&handle, &b).await;

    let task_a = send_task(&handle, &a, "A-panic").await;
    let task_b = send_task(&handle, &b, "B-marker").await;
    let mut events_a = task_a.events;
    let mut events_b = task_b.events;
    events_a.defer_terminal_ack();
    events_b.defer_terminal_ack();
    let finished_a = read_terminal(&mut events_a).await;
    let finished_b = read_terminal(&mut events_b).await;
    assert_eq!(finished_a.status, "failed");
    assert_eq!(finished_a.error.as_ref().unwrap().code, "runtime_error");
    assert_eq!(finished_a.persistence, Some("unconfirmed"));
    assert_eq!(finished_b.status, "completed");

    let a_status = task_status(&handle, &a, &task_a.association.task_id).await;
    let b_status = task_status(&handle, &b, &task_b.association.task_id).await;
    assert_eq!(a_status.status, "failed");
    assert_eq!(a_status.phase, "finished");
    assert_eq!(b_status.status, "completed");
    assert_eq!(b_status.phase, "finished");

    events_a.acknowledge_finished();
    events_b.acknowledge_finished();
    let b_page = history_for(&handle, &b).await;
    assert_eq!(
        b_page
            .records
            .iter()
            .filter(|record| matches!(&record.record, JournalRecord::TaskStarted { .. }))
            .count(),
        1
    );
    assert_eq!(
        b_page
            .records
            .iter()
            .filter(|record| matches!(
                &record.record,
                JournalRecord::TaskFinished {
                    status: TaskStatus::Completed,
                    ..
                }
            ))
            .count(),
        1
    );
    let rejected = send_error(&handle, &a, "must not resume damaged agent").await;
    assert!(matches!(rejected.code, "io_error" | "busy"));

    let released_a = handle
        .call(RuntimeCommand::Release {
            session_id: a.clone(),
        })
        .await
        .unwrap();
    assert!(matches!(
        released_a,
        RuntimeReply::Released { loaded: false, .. }
    ));
    let reloaded_a = load_session(&handle, &a).await;
    assert!(reloaded_a.loaded);

    let bodies = request_bodies(server).await;
    assert_eq!(bodies.len(), 1);
    assert!(bodies[0]["input"].to_string().contains("B-marker"));
    assert!(
        !bodies[0]["input"]
            .to_string()
            .contains("must not resume damaged agent")
    );
    shutdown_actor(handle, actor).await;
}

#[tokio::test]
async fn idle_sessions_start_no_workers_and_release_their_locks_on_release() {
    let workspace = Workspace::new();
    let (client, server) = scripted_http(vec![(200, completed("only-explicit-send"))]).await;
    let (handle, actor) = runtime(client, &workspace).start();
    let ids: Vec<_> = {
        let mut ids = Vec::new();
        for _ in 0..6 {
            ids.push(create_session(&handle).await);
        }
        ids
    };
    let listed = match handle.call(RuntimeCommand::List).await.unwrap() {
        RuntimeReply::List { sessions } => sessions,
        _ => panic!("expected a list reply"),
    };
    assert_eq!(listed.len(), ids.len());
    assert!(listed.iter().all(|summary| summary.in_use));

    let outside = workspace.store();
    for id in &ids {
        assert!(
            outside.open_for_api(id, RESPONSES_API).is_err(),
            "loaded idle session must hold its journal lock"
        );
    }

    let target = ids[0].clone();
    let task = send_task(&handle, &target, "only explicit send").await;
    assert_eq!(finish(task.events).await.status, "completed");
    for id in &ids {
        let released = handle
            .call(RuntimeCommand::Release {
                session_id: id.clone(),
            })
            .await
            .unwrap();
        assert!(matches!(
            released,
            RuntimeReply::Released { loaded: false, .. }
        ));
    }
    let listed = match handle.call(RuntimeCommand::List).await.unwrap() {
        RuntimeReply::List { sessions } => sessions,
        _ => panic!("expected a list reply"),
    };
    assert!(listed.iter().all(|summary| !summary.in_use));
    for id in &ids {
        let loaded = outside.open_for_api(id, RESPONSES_API);
        assert!(loaded.is_ok(), "release must release the journal lock");
        drop(loaded);
    }

    let reloaded = load_session(&handle, &target).await;
    assert!(reloaded.loaded);
    assert!(
        outside.open_for_api(&target, RESPONSES_API).is_err(),
        "loaded session must reacquire its journal lock"
    );
    handle
        .call(RuntimeCommand::Release {
            session_id: target.clone(),
        })
        .await
        .unwrap();
    let reloaded = outside.open_for_api(&target, RESPONSES_API);
    assert!(reloaded.is_ok());
    drop(reloaded);

    let bodies = request_bodies(server).await;
    assert_eq!(bodies.len(), 1);
    assert!(
        bodies[0]["input"]
            .to_string()
            .contains("only explicit send")
    );
    shutdown_actor(handle, actor).await;
}

#[tokio::test]
async fn tool_results_and_events_stay_with_their_own_cwd_and_task() {
    let workspace = Workspace::new();
    let dir_a = workspace.0.join("a");
    let dir_b = workspace.0.join("b");
    fs::create_dir(&dir_a).unwrap();
    fs::create_dir(&dir_b).unwrap();
    fs::write(dir_a.join("probe.txt"), "marker-cwd-a").unwrap();
    fs::write(dir_b.join("probe.txt"), "marker-cwd-b").unwrap();
    let (client, both_arrived, release, server) = gated_tool_model().await;
    let (handle, actor) = runtime(client, &workspace).start();
    let a = create_in(&handle, Some(dir_a)).await;
    let b = create_in(&handle, Some(dir_b)).await;
    let task_a = send_task(&handle, &a, "A-tool-marker").await;
    let task_b = send_task(&handle, &b, "B-tool-marker").await;
    tokio::time::timeout(Duration::from_secs(5), both_arrived)
        .await
        .expect("A and B first model requests did not both arrive")
        .expect("tool gate task dropped");

    release.send(()).unwrap();
    let (finished_a, finished_b) = tokio::join!(
        read_terminal_with_marker(task_a.events, "marker-cwd-a", "marker-cwd-b"),
        read_terminal_with_marker(task_b.events, "marker-cwd-b", "marker-cwd-a"),
    );
    assert_eq!(finished_a.status, "completed");
    assert_eq!(finished_b.status, "completed");

    let page_a = history_for(&handle, &a).await;
    let page_b = history_for(&handle, &b).await;
    let history_a = serde_json::to_string(&page_a.records).unwrap();
    let history_b = serde_json::to_string(&page_b.records).unwrap();
    assert!(history_a.contains("marker-cwd-a"));
    assert!(!history_a.contains("marker-cwd-b"));
    assert!(history_b.contains("marker-cwd-b"));
    assert!(!history_b.contains("marker-cwd-a"));

    let bodies = request_bodies(server).await;
    assert_eq!(bodies.len(), 4);
    for body in &bodies[..2] {
        let input = body["input"].to_string();
        assert!(input.contains("A-tool-marker") || input.contains("B-tool-marker"));
        assert!(!(input.contains("A-tool-marker") && input.contains("B-tool-marker")));
    }
    for body in &bodies[2..] {
        let input = body["input"].to_string();
        assert!(input.contains("shared-call"));
        assert!(input.contains("marker-cwd-a") || input.contains("marker-cwd-b"));
        assert!(!(input.contains("marker-cwd-a") && input.contains("marker-cwd-b")));
    }
    shutdown_actor(handle, actor).await;
}

#[tokio::test]
async fn full_event_queues_keep_control_responsive_and_output_close_only_stops_display() {
    let workspace = Workspace::new();
    let (client, server) = scripted_http(vec![(200, completed("B business completed"))]).await;
    let (handle, actor) = runtime(client, &workspace)
        .with_event_limits(EventLimits {
            messages: 1,
            bytes: 1024 * 1024,
            max_event_bytes: 1024 * 1024,
        })
        .start();
    let a = create_session(&handle).await;
    let b = create_session(&handle).await;
    let task_a = send_task(&handle, &a, "A-full-queue").await;
    let task_b = send_task(&handle, &b, "B-full-queue").await;
    let mut events_a = task_a.events;
    let mut events_b = task_b.events;

    wait_waiting(&events_a).await;
    wait_waiting(&events_b).await;
    assert_eq!(
        task_status(&handle, &a, &task_a.association.task_id)
            .await
            .status,
        "running"
    );
    assert_eq!(
        task_status(&handle, &b, &task_b.association.task_id)
            .await
            .status,
        "running"
    );

    assert_eq!(
        cancel(&handle, &a, &task_a.association.task_id).await,
        "requested"
    );
    let finished_a = read_terminal(&mut events_a).await;
    assert_eq!(finished_a.status, "interrupted");
    assert!(finished_a.history_required);
    assert_eq!(events_b.buffer_stats().waiting_producers, 1);
    assert_eq!(
        task_status(&handle, &b, &task_b.association.task_id)
            .await
            .status,
        "running"
    );

    handle.output_state().close();
    assert!(handle.output_state().is_closed());
    let finished_b = read_terminal(&mut events_b).await;
    assert_eq!(finished_b.status, "completed");
    assert_eq!(events_b.buffer_stats().waiting_producers, 0);

    let bodies = request_bodies(server).await;
    assert_eq!(bodies.len(), 1);
    let input = bodies[0]["input"].to_string();
    assert!(input.contains("B-full-queue"));
    assert!(!input.contains("A-full-queue"));

    drop(events_a);
    drop(events_b);
    drop(handle);
    tokio::time::timeout(Duration::from_secs(5), actor)
        .await
        .expect("actor did not drain after output close")
        .unwrap()
        .unwrap();

    let outside = workspace.store();
    let loaded_a = outside.open_for_api(&a, RESPONSES_API).unwrap();
    let loaded_b = outside.open_for_api(&b, RESPONSES_API).unwrap();
    assert_eq!(loaded_a.replay.status, TaskStatus::Interrupted);
    assert_eq!(loaded_b.replay.status, TaskStatus::Completed);
}

#[tokio::test]
async fn model_failure_and_tool_business_failure_are_local_to_their_own_session() {
    let workspace = Workspace::new();
    let (client, server) = mixed_failure_model().await;
    let (handle, actor) = runtime(client, &workspace).start();
    let a = create_session(&handle).await;
    let b = create_session(&handle).await;
    let task_a = send_task(&handle, &a, "A-model-fail").await;
    let task_b = send_task(&handle, &b, "B-tool-fail").await;
    let (finished_a, finished_b) = tokio::join!(finish(task_a.events), finish(task_b.events));
    assert_eq!(finished_a.status, "failed");
    assert_eq!(finished_a.error.as_ref().unwrap().code, "model_error");
    assert_eq!(finished_b.status, "completed");

    let page_a = history_for(&handle, &a).await;
    let page_b = history_for(&handle, &b).await;
    assert_eq!(page_a.status, "failed");
    assert_eq!(page_b.status, "completed");
    let history_a = serde_json::to_string(&page_a.records).unwrap();
    let history_b = serde_json::to_string(&page_b.records).unwrap();
    assert!(history_a.contains("A-model-fail"));
    assert!(!history_a.contains("B-tool-fail"));
    assert!(history_b.contains("B-tool-fail"));
    assert!(history_b.contains("absent.txt"));
    assert!(history_b.contains("error"));
    assert!(!history_b.contains("A-model-fail"));

    let bodies = request_bodies(server).await;
    assert_eq!(bodies.len(), 3);
    assert!(bodies.iter().any(|body| {
        let input = body["input"].to_string();
        input.contains("A-model-fail") && !input.contains("B-tool-fail")
    }));
    assert!(bodies.iter().any(|body| {
        let input = body["input"].to_string();
        input.contains("B-tool-fail") && !input.contains("A-model-fail")
    }));
    assert!(bodies.iter().any(|body| {
        let input = body["input"].to_string();
        input.contains("absent.txt") && !input.contains("A-model-fail")
    }));
    shutdown_actor(handle, actor).await;
}

fn seed_compacting_session(workspace: &Workspace, marker: &str) -> String {
    let store = workspace.store();
    let config = CompactionConfig {
        context_budget_tokens: 8192,
        max_input_tokens: None,
        reserve_tokens: 2048,
        keep_recent_tokens: 256,
        max_summary_tokens: 1024,
    };
    let id = store
        .create(&workspace.0, "test", SYSTEM_PROMPT, config)
        .unwrap();
    let mut loaded = store.open(&id).unwrap();
    loaded
        .conversation
        .add_user_message(format!("{marker}: {}", "verified context ".repeat(1100)));
    loaded
        .conversation
        .add_assistant_message("Completed initial work.".into());
    loaded.conversation.add_user_message("Continue the goal.");
    loaded.conversation.save_history().unwrap();
    id
}

#[tokio::test]
async fn compact_requests_overlap_and_each_task_keeps_its_compaction_events() {
    use crate::agent::{AgentEvent, ModelRequestPurpose};
    let workspace = Workspace::new();
    let a = seed_compacting_session(&workspace, "source-A");
    let b = seed_compacting_session(&workspace, "source-B");
    let (client, arrived, release, server) = gated_model(2, 4).await;
    let (handle, actor) = runtime(client, &workspace).start();
    load_session(&handle, &a).await;
    load_session(&handle, &b).await;
    let task_a = send_task(&handle, &a, "A follow up").await;
    let task_b = send_task(&handle, &b, "B follow up").await;
    let collect = |mut events: TaskEvents| {
        tokio::spawn(async move {
            let mut compact = 0;
            let mut requests = 0;
            let mut terminal = 0;
            while let Some(event) = events.next().await {
                match event {
                    TaskEvent::Event(packet) => match packet.event {
                        AgentEvent::Compaction { .. } => compact += 1,
                        AgentEvent::ModelRequestStarted {
                            purpose: ModelRequestPurpose::Compaction,
                        } => requests += 1,
                        _ => {}
                    },
                    TaskEvent::Finished(value) => {
                        assert_eq!(value.status, "completed");
                        terminal += 1;
                    }
                }
            }
            assert_eq!((compact, requests, terminal), (1, 1, 1));
        })
    };
    let a_events = collect(task_a.events);
    let b_events = collect(task_b.events);
    tokio::time::timeout(Duration::from_secs(5), arrived)
        .await
        .unwrap()
        .unwrap();
    release.send(()).unwrap();
    a_events.await.unwrap();
    b_events.await.unwrap();
    let bodies = request_bodies(server).await;
    assert_eq!(bodies.len(), 4);
    assert!(
        bodies[..2]
            .iter()
            .any(|body| body["input"].to_string().contains("source-A"))
    );
    assert!(
        bodies[..2]
            .iter()
            .any(|body| body["input"].to_string().contains("source-B"))
    );
    for id in [&a, &b] {
        assert_eq!(
            history_for(&handle, id)
                .await
                .records
                .iter()
                .filter(|record| matches!(record.record, JournalRecord::Compaction { .. }))
                .count(),
            1
        );
    }
    shutdown_actor(handle, actor).await;
}

#[tokio::test]
async fn blocked_compaction_request_hooks_are_cancel_safe_and_output_loss_releases_the_other_task()
{
    use crate::agent::{AgentEvent, events::EventLimits};
    let workspace = Workspace::new();
    let a = seed_compacting_session(&workspace, "source-A");
    let b = seed_compacting_session(&workspace, "source-B");
    let (client, server) =
        scripted_http(vec![(200, completed("summary")), (200, completed("done"))]).await;
    let (handle, actor) = runtime(client, &workspace)
        .with_event_limits(EventLimits {
            messages: 1,
            bytes: 4096,
            max_event_bytes: 2048,
        })
        .start();
    load_session(&handle, &a).await;
    load_session(&handle, &b).await;
    let mut task_a = send_task(&handle, &a, "A follow up").await;
    let mut task_b = send_task(&handle, &b, "B follow up").await;
    for task in [&mut task_a, &mut task_b] {
        assert!(
            matches!(task.events.next().await,Some(TaskEvent::Event(packet)) if matches!(packet.event,AgentEvent::TaskStarted{..}))
        );
    }
    tokio::time::timeout(Duration::from_secs(5), async {
        while task_a.events.buffer_stats().waiting_producers != 1
            || task_b.events.buffer_stats().waiting_producers != 1
        {
            tokio::task::yield_now().await;
        }
    })
    .await
    .unwrap();
    assert_eq!(
        cancel(&handle, &a, &task_a.association.task_id).await,
        "requested"
    );
    let terminal = finish(task_a.events).await;
    assert_eq!(terminal.status, "interrupted");
    assert!(terminal.history_required);
    assert_eq!(task_b.events.buffer_stats().waiting_producers, 1);
    handle.output_state().close();
    assert_eq!(finish(task_b.events).await.status, "completed");
    let bodies = request_bodies(server).await;
    assert_eq!(bodies.len(), 2);
    assert!(bodies[0]["input"].to_string().contains("source-B"));
    assert!(!bodies[0]["input"].to_string().contains("source-A"));
    drop(handle);
    actor.await.unwrap().unwrap();
    assert_eq!(
        workspace.store().open(&a).unwrap().replay.status,
        TaskStatus::Interrupted
    );
    assert_eq!(
        workspace.store().open(&b).unwrap().replay.status,
        TaskStatus::Completed
    );
}

#[tokio::test]
async fn task_status_after_restart_preserves_missing_terminal_and_configuration_as_unknown() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = store
        .create(
            &workspace.0,
            "test",
            SYSTEM_PROMPT,
            CompactionConfig::new(262144),
        )
        .unwrap();
    let mut loaded = store.open(&id).unwrap();
    loaded
        .conversation
        .begin_task_with_id("unfinished".into(), None)
        .unwrap();
    loaded.conversation.add_user_message("no automatic replay");
    loaded.conversation.save_history().unwrap();
    drop(loaded);
    let before = workspace.history_bytes(&id);
    let (client, server) = scripted_http(vec![]).await;
    let (handle, actor) = runtime(client, &workspace).start();
    let state = task_status(&handle, &id, "unfinished").await;
    assert_eq!(state.status, "unknown");
    assert_eq!(state.phase, "recovery");
    assert_eq!(state.persistence, Some("unconfirmed"));
    assert!(state.config_revision.is_none());
    let missing = task_status(&handle, &id, "not-present").await;
    assert_eq!(missing.phase, "unknown");
    assert!(missing.persistence.is_none());
    assert_eq!(workspace.history_bytes(&id), before);
    shutdown_actor(handle, actor).await;
    assert!(request_bodies(server).await.is_empty());
}
