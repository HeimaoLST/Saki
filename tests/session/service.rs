use super::*;
#[cfg(unix)]
use crate::agent::journal::fault::{Guard, Point};
use crate::{
    agent::{AgentEvent, journal::JournalRecord},
    test_support::{completed, read_request, scripted_http},
};
use serde_json::{Value, json};
use std::{
    fs,
    path::PathBuf,
    sync::atomic::{AtomicUsize, Ordering},
    time::Duration,
};
use tokio::{io::AsyncWriteExt, net::TcpListener, sync::oneshot, task::JoinHandle};

#[path = "runtime_title.rs"]
mod title_tests;

struct Workspace(PathBuf);

impl Workspace {
    fn new() -> Self {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        let path = std::env::temp_dir().join(format!(
            "saki-service-{}-{}",
            journal::new_id().unwrap(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir(&path).unwrap();
        Self(path)
    }

    fn store(&self) -> SessionStore {
        SessionStore::new(self.0.join("sessions")).unwrap()
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
}

fn session(reply: RuntimeReply) -> SessionState {
    match reply {
        RuntimeReply::Session(state) => state,
        _ => panic!("expected a session reply"),
    }
}

fn accepted(reply: RuntimeReply) -> AcceptedTask {
    match reply {
        RuntimeReply::Accepted(task) => task,
        _ => panic!("expected an accepted task"),
    }
}

fn history(reply: RuntimeReply) -> HistoryPage {
    match reply {
        RuntimeReply::History(page) => page,
        _ => panic!("expected a history reply"),
    }
}

async fn finish(mut events: TaskEvents) -> TaskFinished {
    let mut terminal = None;
    let mut started = false;
    while let Some(event) = events.next().await {
        match event {
            TaskEvent::Event(packet) => {
                if matches!(packet.event, AgentEvent::TaskStarted { .. }) {
                    started = true;
                }
            }
            TaskEvent::Finished(finished) => {
                assert!(terminal.is_none(), "terminal was delivered twice");
                terminal = Some(finished);
            }
        }
    }
    assert!(started, "TaskStarted must be delivered before the terminal");
    assert!(events.next().await.is_none());
    terminal.expect("task did not deliver a terminal")
}

async fn collect_terminal(mut events: TaskEvents) -> TaskFinished {
    let mut terminal = None;
    while let Some(event) = events.next().await {
        if let TaskEvent::Finished(finished) = event {
            assert!(terminal.is_none(), "terminal was delivered twice");
            terminal = Some(finished);
        }
    }
    assert!(events.next().await.is_none());
    terminal.expect("task did not deliver a terminal")
}

async fn create_session(handle: &RuntimeHandle) -> String {
    session(
        handle
            .call(RuntimeCommand::Create {
                working_directory: None,
                system_prompt: None,
            })
            .await
            .unwrap(),
    )
    .metadata
    .expect("create reply must include metadata")
    .id
}

fn journal_file(workspace: &Workspace, session_id: &str) -> PathBuf {
    workspace
        .store()
        .directory(session_id)
        .unwrap()
        .join("history.jsonl")
}

#[cfg(unix)]
fn assert_failed_unconfirmed(finished: &TaskFinished) {
    assert_eq!(finished.status, "failed");
    let error = finished
        .error
        .as_ref()
        .expect("failed terminal needs an error");
    assert_eq!(error.code, "io_error");
    assert_eq!(finished.persistence, Some("unconfirmed"));
}

#[cfg(unix)]
fn assert_no_completed_terminal(page: &HistoryPage) {
    for record in &page.records {
        if let JournalRecord::TaskFinished { status, .. } = &record.record {
            assert!(
                !matches!(status, TaskStatus::Completed),
                "history contains a completed terminal after a fault"
            );
        }
    }
}

fn history_counts(page: &HistoryPage) -> (usize, usize) {
    (
        page.records
            .iter()
            .filter(|record| matches!(&record.record, JournalRecord::TaskStarted { .. }))
            .count(),
        page.records
            .iter()
            .filter(|record| matches!(&record.record, JournalRecord::TaskFinished { .. }))
            .count(),
    )
}

#[cfg(unix)]
async fn silent_server() -> (ModelClient, oneshot::Receiver<()>, JoinHandle<()>) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (seen_tx, seen) = oneshot::channel();
    let worker = tokio::spawn(async move {
        if listener.accept().await.is_ok() {
            let _ = seen_tx.send(());
        }
    });
    (
        ModelClient::for_test(format!("http://{address}/v1")),
        seen,
        worker,
    )
}

#[cfg(unix)]
fn unreachable_client() -> ModelClient {
    ModelClient::for_test("http://127.0.0.1:1/v1".to_owned())
}

#[cfg(unix)]
async fn release_session(handle: &RuntimeHandle, session_id: &str) {
    tokio::time::timeout(Duration::from_secs(5), async {
        loop {
            match handle
                .call(RuntimeCommand::Release {
                    session_id: session_id.to_owned(),
                })
                .await
            {
                Ok(RuntimeReply::Released { loaded, .. }) => {
                    assert!(!loaded, "release reports the resulting loaded state");
                    return;
                }
                Ok(_) => panic!("expected a released reply"),
                Err(error) if error.code == "busy" => tokio::task::yield_now().await,
                Err(error) => panic!("release failed: {error:?}"),
            }
        }
    })
    .await
    .expect("release did not observe the terminal slot release");
}

async fn shutdown_actor(handle: RuntimeHandle, actor: JoinHandle<anyhow::Result<()>>) {
    assert!(matches!(
        handle.call(RuntimeCommand::Shutdown).await.unwrap(),
        RuntimeReply::Shutdown { state: "draining" }
    ));
    drop(handle);
    actor.await.unwrap().unwrap();
}

async fn paused_model() -> (
    ModelClient,
    oneshot::Receiver<()>,
    oneshot::Sender<()>,
    JoinHandle<Value>,
) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (arrived_tx, arrived) = oneshot::channel();
    let (release, release_rx) = oneshot::channel();
    let worker = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
        let request = read_request(&mut socket).await;
        arrived_tx.send(()).unwrap();
        let _ = release_rx.await;
        let body = serde_json::to_vec(&completed("done")).unwrap();
        let head = format!(
            "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
            body.len()
        );
        let _ = socket.write_all(head.as_bytes()).await;
        let _ = socket.write_all(&body).await;
        request
    });
    (
        ModelClient::for_test(format!("http://{address}/v1")),
        arrived,
        release,
        worker,
    )
}

#[tokio::test]
async fn typed_create_send_real_read_history_and_reload_keep_one_terminal() {
    let workspace = Workspace::new();
    fs::write(workspace.0.join("probe.txt"), "marker-service").unwrap();
    let tool = json!({"id":"tool-response","status":"completed","output":[{"type":"function_call","status":"completed","id":"f","call_id":"read-service","name":"read","arguments":"{\"path\":\"probe.txt\"}"}]});
    let (client, server) =
        scripted_http(vec![(200, tool), (200, completed("marker-service"))]).await;
    let (handle, actor) = runtime(client, &workspace).start();
    assert!(!handle.output_state().is_closed());

    let created = session(
        handle
            .call(RuntimeCommand::Create {
                working_directory: None,
                system_prompt: None,
            })
            .await
            .unwrap(),
    );
    let id = created.metadata.as_ref().unwrap().id.clone();
    assert!(created.loaded);
    assert_eq!(created.status, "unknown");
    assert_eq!(created.state, "idle");

    let task = accepted(
        handle
            .call(RuntimeCommand::Send {
                session_id: id.clone(),
                message: UserMessage::text("read probe.txt"),
            })
            .await
            .unwrap(),
    );
    let association = task.association.clone();
    let finished = finish(task.events).await;
    assert_eq!(finished.status, "completed");
    assert!(finished.error.is_none());

    let page = history(
        handle
            .call(RuntimeCommand::History {
                session_id: id.clone(),
                offset: 0,
                limit: 100,
            })
            .await
            .unwrap(),
    );
    assert_eq!(page.status, "completed");
    assert_eq!(
        page.records
            .iter()
            .filter(|record| matches!(&record.record, JournalRecord::TaskStarted { .. }))
            .count(),
        1
    );
    assert_eq!(
        page.records
            .iter()
            .filter(|record| matches!(&record.record, JournalRecord::TaskFinished { .. }))
            .count(),
        1
    );
    assert!(page.records.iter().any(|record| {
        matches!(
            &record.record,
            JournalRecord::TaskStarted { task_id, message_id, .. }
                if task_id == &association.task_id
                    && message_id.as_ref() == Some(&association.message_id)
        )
    }));

    let status = session(
        handle
            .call(RuntimeCommand::Status {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    assert_eq!(status.status, "completed");
    assert!(status.metadata.is_none());

    assert!(matches!(
        handle.call(RuntimeCommand::Shutdown).await.unwrap(),
        RuntimeReply::Shutdown { state: "draining" }
    ));
    drop(handle);
    actor.await.unwrap().unwrap();
    let requests = server.await.unwrap();
    assert_eq!(requests.len(), 2);
    assert!(requests[1]["input"].to_string().contains("marker-service"));

    // A fresh store/runtime loads the journal without replaying the task.
    let (client, server) = scripted_http(vec![(200, completed("continued"))]).await;
    let (handle, actor) = runtime(client, &workspace).start();
    let loaded = session(
        handle
            .call(RuntimeCommand::Load {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    assert!(loaded.metadata.is_some());
    assert_eq!(loaded.status, "completed");
    let page = history(
        handle
            .call(RuntimeCommand::History {
                session_id: id.clone(),
                offset: 0,
                limit: 100,
            })
            .await
            .unwrap(),
    );
    assert!(page.records.iter().any(|record| {
        serde_json::to_string(&record.record)
            .unwrap()
            .contains("marker-service")
    }));

    let task = accepted(
        handle
            .call(RuntimeCommand::Send {
                session_id: id.clone(),
                message: UserMessage::text("continue"),
            })
            .await
            .unwrap(),
    );
    let finished = finish(task.events).await;
    assert_eq!(finished.status, "completed");
    let requests = server.await.unwrap();
    assert!(requests[0]["input"].to_string().contains("marker-service"));
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    drop(handle);
    actor.await.unwrap().unwrap();
}

#[tokio::test]
async fn deferred_terminal_ack_holds_admission_until_stdio_enqueues_terminal() {
    let workspace = Workspace::new();
    let (client, arrived, release, server) = paused_model().await;
    let (handle, actor) = runtime(client, &workspace).start();
    let created = session(
        handle
            .call(RuntimeCommand::Create {
                working_directory: None,
                system_prompt: None,
            })
            .await
            .unwrap(),
    );
    let id = created.metadata.unwrap().id;
    let task = accepted(
        handle
            .call(RuntimeCommand::Send {
                session_id: id.clone(),
                message: UserMessage::text("defer terminal"),
            })
            .await
            .unwrap(),
    );
    let mut events = task.events;
    events.defer_terminal_ack();
    arrived.await.unwrap();
    release.send(()).unwrap();

    let mut terminal = None;
    while let Some(event) = events.next().await {
        if let TaskEvent::Finished(finished) = event {
            assert!(terminal.is_none());
            terminal = Some(finished);
            break;
        }
    }
    assert_eq!(terminal.unwrap().status, "completed");

    let error = match handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("must stay busy"),
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("the slot was released before terminal acknowledgement"),
    };
    assert_eq!(error.code, "busy");

    events.acknowledge_finished();
    drop(events);
    drop(handle);
    tokio::time::timeout(Duration::from_secs(5), actor)
        .await
        .expect("actor did not release the slot after acknowledgement")
        .unwrap()
        .unwrap();
    let _ = server.await;
}

#[tokio::test]
async fn queued_cancel_and_shutdown_are_processed_after_acceptance_stops() {
    let workspace = Workspace::new();
    let (client, arrived, release, server) = paused_model().await;
    let (handle, actor) = runtime(client, &workspace).start();
    let created = session(
        handle
            .call(RuntimeCommand::Create {
                working_directory: None,
                system_prompt: None,
            })
            .await
            .unwrap(),
    );
    let id = created.metadata.unwrap().id;
    let task = accepted(
        handle
            .call(RuntimeCommand::Send {
                session_id: id.clone(),
                message: UserMessage::text("pause before shutdown"),
            })
            .await
            .unwrap(),
    );
    arrived.await.unwrap();

    assert!(matches!(
        handle.call(RuntimeCommand::Shutdown).await.unwrap(),
        RuntimeReply::Shutdown { state: "draining" }
    ));
    let cancel = handle.submit(RuntimeCommand::Cancel {
        session_id: id.clone(),
        task_id: task.association.task_id.clone(),
    });
    let shutdown = handle.submit(RuntimeCommand::Shutdown);
    let cancel = match cancel.unwrap().await.unwrap().unwrap() {
        RuntimeReply::Cancelled { status } => status,
        _ => panic!("expected cancel reply"),
    };
    assert_eq!(cancel, "requested");
    assert!(matches!(
        shutdown.unwrap().await.unwrap().unwrap(),
        RuntimeReply::Shutdown { state: "draining" }
    ));
    release.send(()).unwrap();
    assert_eq!(finish(task.events).await.status, "interrupted");
    let _ = server.await;
    drop(handle);
    actor.await.unwrap().unwrap();
}

#[tokio::test]
async fn busy_send_is_rejected_without_queueing_and_cancel_has_one_terminal() {
    let workspace = Workspace::new();
    let (client, arrived, release, server) = paused_model().await;
    let (handle, actor) = runtime(client, &workspace).start();
    let created = session(
        handle
            .call(RuntimeCommand::Create {
                working_directory: None,
                system_prompt: None,
            })
            .await
            .unwrap(),
    );
    let id = created.metadata.unwrap().id;
    let task = accepted(
        handle
            .call(RuntimeCommand::Send {
                session_id: id.clone(),
                message: UserMessage::text("pause"),
            })
            .await
            .unwrap(),
    );
    arrived.await.unwrap();

    let loaded = session(
        handle
            .call(RuntimeCommand::Load {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    assert_eq!(loaded.state, "running");
    assert_eq!(loaded.status, "running");
    assert!(loaded.metadata.is_some());

    let error = match handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("must not queue"),
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("a second send was accepted while busy"),
    };
    assert_eq!(error.code, "busy");

    let cancelled = match handle
        .call(RuntimeCommand::Cancel {
            session_id: id.clone(),
            task_id: task.association.task_id.clone(),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Cancelled { status } => status,
        _ => panic!("expected cancel reply"),
    };
    assert_eq!(cancelled, "requested");
    release.send(()).unwrap();
    let finished = finish(task.events).await;
    assert_eq!(finished.status, "interrupted");
    let request = server.await.unwrap();
    assert!(request["input"].to_string().contains("pause"));
    assert!(!request["input"].to_string().contains("must not queue"));

    let page = history(
        handle
            .call(RuntimeCommand::History {
                session_id: id.clone(),
                offset: 0,
                limit: 100,
            })
            .await
            .unwrap(),
    );
    assert_eq!(page.status, "interrupted");
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    drop(handle);
    actor.await.unwrap().unwrap();
}

#[tokio::test]
async fn dropping_an_unread_task_stream_closes_output_but_not_the_business_task() {
    let workspace = Workspace::new();
    let (client, arrived, release, _server) = paused_model().await;
    let (handle, actor) = runtime(client, &workspace).start();
    let created = session(
        handle
            .call(RuntimeCommand::Create {
                working_directory: None,
                system_prompt: None,
            })
            .await
            .unwrap(),
    );
    let id = created.metadata.unwrap().id;
    let task = accepted(
        handle
            .call(RuntimeCommand::Send {
                session_id: id.clone(),
                message: UserMessage::text("finish despite lost reader"),
            })
            .await
            .unwrap(),
    );
    arrived.await.unwrap();
    drop(task.events);
    assert!(handle.output_state().is_closed());
    release.send(()).unwrap();

    drop(handle);
    actor.await.unwrap().unwrap();

    let (client, _server) = scripted_http(vec![]).await;
    let (handle, actor) = runtime(client, &workspace).start();
    let page = history(
        handle
            .call(RuntimeCommand::History {
                session_id: id.clone(),
                offset: 0,
                limit: 100,
            })
            .await
            .unwrap(),
    );
    assert_eq!(page.status, "completed");
    assert_eq!(
        page.records
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
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    drop(handle);
    actor.await.unwrap().unwrap();
}

#[tokio::test]
async fn full_event_queue_accepts_cancel_and_still_persists_a_terminal() {
    let workspace = Workspace::new();
    let (client, _arrived, release, server) = paused_model().await;
    let (handle, actor) = runtime(client, &workspace)
        .with_event_limits(EventLimits {
            messages: 1,
            bytes: 1024 * 1024,
            max_event_bytes: 1024 * 1024,
        })
        .start();
    let created = session(
        handle
            .call(RuntimeCommand::Create {
                working_directory: None,
                system_prompt: None,
            })
            .await
            .unwrap(),
    );
    let id = created.metadata.unwrap().id;
    let task = accepted(
        handle
            .call(RuntimeCommand::Send {
                session_id: id.clone(),
                message: UserMessage::text("fill the event queue"),
            })
            .await
            .unwrap(),
    );
    let events = task.events;
    // Do not read the queue. Wait until the bounded sink has filled it; the
    // worker then blocks on the next event until cancel releases the sink.
    tokio::time::timeout(Duration::from_secs(5), async {
        while events.events.is_empty() {
            tokio::task::yield_now().await;
        }
    })
    .await
    .expect("event queue did not fill");
    let stats = events.buffer_stats();
    assert!(stats.peak_messages >= 1);
    assert!(stats.peak_bytes <= 1024 * 1024);

    let cancelled = match handle
        .call(RuntimeCommand::Cancel {
            session_id: id.clone(),
            task_id: task.association.task_id.clone(),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Cancelled { status } => status,
        _ => panic!("expected cancel reply"),
    };
    assert_eq!(cancelled, "requested");
    release.send(()).unwrap();
    let finished = finish(events).await;
    assert_eq!(finished.status, "interrupted");
    server.abort();
    let page = history(
        handle
            .call(RuntimeCommand::History {
                session_id: id.clone(),
                offset: 0,
                limit: 100,
            })
            .await
            .unwrap(),
    );
    assert_eq!(page.status, "interrupted");
    assert_no_completed_terminal(&page);
    assert_eq!(
        page.records
            .iter()
            .filter(|record| matches!(&record.record, JournalRecord::TaskFinished { .. }))
            .count(),
        1
    );
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    drop(handle);
    actor.await.unwrap().unwrap();
}

#[cfg(unix)]
async fn assert_recovered_history(
    workspace: &Workspace,
    session_id: &str,
    started: usize,
    finished: usize,
) {
    let (handle, actor) = Runtime::new(
        workspace.store(),
        unreachable_client(),
        workspace.0.clone(),
        "test".to_owned(),
        CompactionConfig::new(262_144),
    )
    .start();
    let loaded = session(
        handle
            .call(RuntimeCommand::Load {
                session_id: session_id.to_owned(),
            })
            .await
            .unwrap(),
    );
    assert!(loaded.loaded);
    assert!(loaded.metadata.is_some());
    assert_ne!(loaded.status, "completed");
    let page = history(
        handle
            .call(RuntimeCommand::History {
                session_id: session_id.to_owned(),
                offset: 0,
                limit: 100,
            })
            .await
            .unwrap(),
    );
    assert_ne!(page.status, "completed");
    assert_eq!(history_counts(&page), (started, finished));
    assert_no_completed_terminal(&page);
    shutdown_actor(handle, actor).await;
}

#[cfg(unix)]
#[tokio::test]
async fn journal_fault_start_is_failed_unconfirmed_and_recoverable() {
    let workspace = Workspace::new();
    let (client, mut requested, server) = silent_server().await;
    let (handle, actor) = runtime(client, &workspace).start();
    let id = create_session(&handle).await;
    let _guard = Guard::new(&journal_file(&workspace, &id), Point::Start);
    let task = accepted(
        handle
            .call(RuntimeCommand::Send {
                session_id: id.clone(),
                message: UserMessage::text("fault start"),
            })
            .await
            .unwrap(),
    );
    let finished = tokio::time::timeout(Duration::from_secs(5), collect_terminal(task.events))
        .await
        .expect("start fault did not produce a terminal");
    assert_failed_unconfirmed(&finished);
    let rejected = match handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("must be rejected"),
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("send after start fault was accepted"),
    };
    assert!(matches!(rejected.code, "io_error" | "busy"));
    tokio::task::yield_now().await;
    assert!(
        requested.try_recv().is_err(),
        "start fault called the model"
    );
    let page = history(
        handle
            .call(RuntimeCommand::History {
                session_id: id.clone(),
                offset: 0,
                limit: 100,
            })
            .await
            .unwrap(),
    );
    assert_ne!(page.status, "completed");
    assert_eq!(history_counts(&page), (0, 0));
    release_session(&handle, &id).await;
    shutdown_actor(handle, actor).await;
    server.abort();
    assert_recovered_history(&workspace, &id, 0, 0).await;
}

#[cfg(unix)]
#[tokio::test]
async fn journal_fault_user_is_failed_unconfirmed_and_recoverable() {
    let workspace = Workspace::new();
    let (client, mut requested, server) = silent_server().await;
    let (handle, actor) = runtime(client, &workspace).start();
    let id = create_session(&handle).await;
    let _guard = Guard::new(&journal_file(&workspace, &id), Point::User);
    let task = accepted(
        handle
            .call(RuntimeCommand::Send {
                session_id: id.clone(),
                message: UserMessage::text("fault user"),
            })
            .await
            .unwrap(),
    );
    let finished = tokio::time::timeout(Duration::from_secs(5), collect_terminal(task.events))
        .await
        .expect("user fault did not produce a terminal");
    assert_failed_unconfirmed(&finished);
    let rejected = match handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("must be rejected"),
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("send after user fault was accepted"),
    };
    assert!(matches!(rejected.code, "io_error" | "busy"));
    tokio::task::yield_now().await;
    assert!(requested.try_recv().is_err(), "user fault called the model");
    let page = history(
        handle
            .call(RuntimeCommand::History {
                session_id: id.clone(),
                offset: 0,
                limit: 100,
            })
            .await
            .unwrap(),
    );
    assert_ne!(page.status, "completed");
    assert_eq!(history_counts(&page), (1, 0));
    release_session(&handle, &id).await;
    shutdown_actor(handle, actor).await;
    server.abort();
    assert_recovered_history(&workspace, &id, 1, 0).await;
}

#[cfg(unix)]
#[tokio::test]
async fn journal_fault_tool_result_is_failed_unconfirmed_without_second_model_request() {
    let workspace = Workspace::new();
    fs::write(workspace.0.join("probe.txt"), "marker-fault-tool").unwrap();
    let tool = json!({"id":"tool-response","status":"completed","output":[{"type":"function_call","status":"completed","id":"f","call_id":"fault-read","name":"read","arguments":"{\"path\":\"probe.txt\"}"}]});
    let (client, server) = scripted_http(vec![(200, tool)]).await;
    let (handle, actor) = runtime(client, &workspace).start();
    let id = create_session(&handle).await;
    let _guard = Guard::new(&journal_file(&workspace, &id), Point::ToolResult);
    let task = accepted(
        handle
            .call(RuntimeCommand::Send {
                session_id: id.clone(),
                message: UserMessage::text("read probe.txt"),
            })
            .await
            .unwrap(),
    );
    let finished = tokio::time::timeout(Duration::from_secs(5), collect_terminal(task.events))
        .await
        .expect("tool-result fault did not produce a terminal");
    assert_failed_unconfirmed(&finished);
    let rejected = match handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("must be rejected"),
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("send after tool-result fault was accepted"),
    };
    assert!(matches!(rejected.code, "io_error" | "busy"));
    let page = history(
        handle
            .call(RuntimeCommand::History {
                session_id: id.clone(),
                offset: 0,
                limit: 100,
            })
            .await
            .unwrap(),
    );
    assert_ne!(page.status, "completed");
    assert_eq!(history_counts(&page), (1, 1));
    assert_no_completed_terminal(&page);
    release_session(&handle, &id).await;
    shutdown_actor(handle, actor).await;
    let requests = tokio::time::timeout(Duration::from_secs(5), server)
        .await
        .expect("tool-result fault waited for a second model request")
        .unwrap();
    assert_eq!(requests.len(), 1);
    assert_recovered_history(&workspace, &id, 1, 1).await;
}

#[cfg(unix)]
#[tokio::test]
async fn journal_fault_finish_is_failed_unconfirmed_and_recoverable() {
    let workspace = Workspace::new();
    let (client, server) = scripted_http(vec![(200, completed("fault-finish"))]).await;
    let (handle, actor) = runtime(client, &workspace).start();
    let id = create_session(&handle).await;
    let _guard = Guard::new(&journal_file(&workspace, &id), Point::Finish);
    let task = accepted(
        handle
            .call(RuntimeCommand::Send {
                session_id: id.clone(),
                message: UserMessage::text("fault finish"),
            })
            .await
            .unwrap(),
    );
    let finished = tokio::time::timeout(Duration::from_secs(5), collect_terminal(task.events))
        .await
        .expect("finish fault did not produce a terminal");
    assert_failed_unconfirmed(&finished);
    let rejected = match handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("must be rejected"),
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("send after finish fault was accepted"),
    };
    assert!(matches!(rejected.code, "io_error" | "busy"));
    let page = history(
        handle
            .call(RuntimeCommand::History {
                session_id: id.clone(),
                offset: 0,
                limit: 100,
            })
            .await
            .unwrap(),
    );
    assert_ne!(page.status, "completed");
    assert_eq!(history_counts(&page), (1, 0));
    release_session(&handle, &id).await;
    shutdown_actor(handle, actor).await;
    let requests = tokio::time::timeout(Duration::from_secs(5), server)
        .await
        .expect("finish fault made an unexpected model request")
        .unwrap();
    assert_eq!(requests.len(), 1);
    assert_recovered_history(&workspace, &id, 1, 0).await;
}

#[cfg(unix)]
#[tokio::test]
async fn lost_accepted_reply_closes_output_and_drains_exactly_one_task() {
    let workspace = Workspace::new();
    let (client, arrived, release, server) = paused_model().await;
    let (handle, actor) = runtime(client, &workspace).start();
    let id = create_session(&handle).await;
    let reply = handle
        .submit(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("accepted before reply loss"),
        })
        .unwrap();
    arrived.await.unwrap();
    drop(reply);
    tokio::time::timeout(Duration::from_secs(5), handle.output_state().closed())
        .await
        .expect("lost acceptance reply did not close output");
    release.send(()).unwrap();
    drop(handle);
    tokio::time::timeout(Duration::from_secs(5), actor)
        .await
        .expect("actor did not drain the accepted task")
        .unwrap()
        .unwrap();
    let request = tokio::time::timeout(Duration::from_secs(5), server)
        .await
        .expect("lost reply task did not finish")
        .unwrap();
    assert!(
        request["input"]
            .to_string()
            .contains("accepted before reply loss")
    );

    let (handle, actor) = Runtime::new(
        workspace.store(),
        unreachable_client(),
        workspace.0.clone(),
        "test".to_owned(),
        CompactionConfig::new(262_144),
    )
    .start();
    let loaded = session(
        handle
            .call(RuntimeCommand::Load {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    assert!(loaded.loaded);
    assert_eq!(loaded.status, "completed");
    let page = history(
        handle
            .call(RuntimeCommand::History {
                session_id: id.clone(),
                offset: 0,
                limit: 100,
            })
            .await
            .unwrap(),
    );
    assert_eq!(history_counts(&page), (1, 1));
    assert_eq!(
        page.records
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
    shutdown_actor(handle, actor).await;
}

#[tokio::test]
async fn cancelling_next_while_waiting_for_terminal_does_not_lose_it() {
    use std::{future::Future, task::Poll};
    let (event_tx, event_rx) = mpsc::channel(1);
    drop(event_tx);
    let (terminal_tx, terminal_rx) = oneshot::channel();
    let (release_tx, mut release_rx) = mpsc::channel(1);
    let output = OutputState::default();
    let mut events = TaskEvents {
        metrics: BufferMetrics::default(),
        events: event_rx,
        finished: Some(terminal_rx),
        release: release_tx,
        output: output.clone(),
        task_id: "task".into(),
        delivered: false,
        defer_ack: false,
        released: false,
    };
    let mut next = Box::pin(events.next());
    std::future::poll_fn(|cx| {
        assert!(next.as_mut().poll(cx).is_pending());
        Poll::Ready(())
    })
    .await;
    drop(next);
    terminal_tx
        .send(TaskFinished {
            status: "completed",
            error: None,
            persistence: None,
            history_required: false,
        })
        .unwrap();
    assert!(matches!(
        events.next().await,
        Some(TaskEvent::Finished(TaskFinished {
            status: "completed",
            ..
        }))
    ));
    assert_eq!(release_rx.recv().await.as_deref(), Some("task"));
    assert!(events.next().await.is_none());
    drop(events);
    assert!(!output.is_closed());
}

#[tokio::test]
async fn oversized_typed_command_is_rejected_before_any_task_or_journal_change() {
    let workspace = Workspace::new();
    let client = ModelClient::for_test("http://127.0.0.1:1/v1".to_owned());
    let (handle, actor) = runtime(client, &workspace).start();
    let id = create_session(&handle).await;
    let before = fs::read(journal_file(&workspace, &id)).unwrap();
    let result = handle.submit(RuntimeCommand::Send {
        session_id: id.clone(),
        message: UserMessage::text("x".repeat(8 * 1024 * 1024)),
    });
    assert!(matches!(
        result,
        Err(RuntimeError {
            code: "invalid_request",
            ..
        })
    ));
    let status = session(
        handle
            .call(RuntimeCommand::Status {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    assert_eq!(status.state, "idle");
    assert_eq!(before, fs::read(journal_file(&workspace, &id)).unwrap());
    shutdown_actor(handle, actor).await;
}

#[tokio::test]
async fn output_loss_before_acceptance_rejects_send_without_changing_history() {
    let workspace = Workspace::new();
    let client = ModelClient::for_test("http://127.0.0.1:1/v1".to_owned());
    let (handle, actor) = runtime(client, &workspace).start();
    let id = create_session(&handle).await;
    let before = fs::read(journal_file(&workspace, &id)).unwrap();
    handle.output_state().close();
    let result = handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("must not start"),
        })
        .await;
    assert!(result.is_err());
    drop(handle);
    actor.await.unwrap().unwrap();
    assert_eq!(before, fs::read(journal_file(&workspace, &id)).unwrap());
}

#[tokio::test]
async fn drain_observation_waits_for_business_but_not_for_terminal_delivery() {
    let workspace = Workspace::new();
    let (client, arrived, release, server) = paused_model().await;
    let (handle, actor) = runtime(client, &workspace).start();
    let id = create_session(&handle).await;
    let task = accepted(
        handle
            .call(RuntimeCommand::Send {
                session_id: id,
                message: UserMessage::text("finish"),
            })
            .await
            .unwrap(),
    );
    arrived.await.unwrap();
    let mut drained = handle.work_drained();
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    assert!(!*drained.borrow());
    release.send(()).unwrap();
    tokio::time::timeout(Duration::from_secs(5), drained.wait_for(|drained| *drained))
        .await
        .unwrap()
        .unwrap();
    assert!(
        !actor.is_finished(),
        "terminal still occupies admission until consumed"
    );
    assert_eq!(finish(task.events).await.status, "completed");
    drop(handle);
    actor.await.unwrap().unwrap();
    server.await.unwrap();
}
