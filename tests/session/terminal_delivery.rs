use super::*;
use crate::{
    agent::{events::EventLimits, journal::JournalRecord},
    session::SessionEventPacket,
    test_support::{completed, read_request, sse},
};
use serde_json::{Value, json};
use std::{fs, time::Duration};
use tokio::{
    io::AsyncWriteExt,
    net::TcpListener,
    sync::{mpsc, oneshot},
    task::JoinHandle,
};

struct Workspace(PathBuf);

impl Workspace {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!(
            "saki-terminal-delivery-{}",
            journal::new_id().unwrap()
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
        "test".into(),
        CompactionConfig::new(262_144),
    )
}

async fn next(events: &mut SessionEventReceiver) -> SessionEventPacket {
    tokio::time::timeout(Duration::from_secs(5), events.recv())
        .await
        .unwrap()
        .unwrap()
}

async fn reply(events: &mut SessionEventReceiver, request_id: u64) -> SessionEventPacket {
    loop {
        let packet = next(events).await;
        if packet.request_id == Some(request_id) {
            return packet;
        }
    }
}

fn request(input: &mpsc::Sender<SessionRequest>, request_id: u64, command: SessionCommand) {
    assert!(
        input
            .try_send(SessionRequest {
                request_id,
                command
            })
            .is_ok()
    );
}

async fn streaming_client() -> (ModelClient, oneshot::Sender<()>, JoinHandle<Vec<Value>>) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let client = ModelClient::for_test(format!("http://{}/v1", listener.local_addr().unwrap()));
    let (release, wait) = oneshot::channel();
    let server = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
        let request = read_request(&mut socket).await;
        socket.write_all(b"HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n").await.unwrap();
        let delta = sse(
            json!({"type":"response.output_text.delta","item_id":"shared-item","delta":"live-prefix"}),
        );
        socket
            .write_all(format!("{:x}\r\n{delta}\r\n", delta.len()).as_bytes())
            .await
            .unwrap();
        wait.await.unwrap();
        let final_event =
            sse(json!({"type":"response.completed","response":completed("canonical-final")}));
        socket
            .write_all(format!("{:x}\r\n{final_event}\r\n0\r\n\r\n", final_event.len()).as_bytes())
            .await
            .unwrap();
        vec![request]
    });
    (client, release, server)
}

async fn start(
    workspace: &Workspace,
    client: ModelClient,
) -> (
    mpsc::Sender<SessionRequest>,
    SessionEventReceiver,
    JoinHandle<anyhow::Result<()>>,
    String,
) {
    let manager = SessionManager::from_runtime(runtime(client, workspace), None);
    let (output, mut events) = manager.event_channel();
    let (input, commands) = mpsc::channel(32);
    let worker = tokio::spawn(manager.run(commands, output));
    let id = match next(&mut events).await.event {
        SessionEvent::Opened { metadata, .. } => metadata.id,
        _ => panic!("startup must open a session"),
    };
    request(
        &input,
        1,
        SessionCommand::Rename {
            session_id: id.clone(),
            title: "Manual title".into(),
        },
    );
    assert!(matches!(
        reply(&mut events, 1).await.event,
        SessionEvent::Renamed(_)
    ));
    (input, events, worker, id)
}

#[tokio::test]
async fn accepted_stream_survives_view_switch_and_snapshots_do_not_overlap_live_history() {
    let workspace = Workspace::new();
    let (client, release, server) = streaming_client().await;
    let (input, mut events, worker, a) = start(&workspace, client).await;
    request(
        &input,
        2,
        SessionCommand::Send {
            session_id: a.clone(),
            message: UserMessage::text("active-input"),
        },
    );
    let accepted = reply(&mut events, 2).await;
    assert!(matches!(accepted.event, SessionEvent::Accepted));
    let association = accepted.association.unwrap();
    let mut saw_delta = false;
    while !saw_delta {
        let packet = next(&mut events).await;
        assert_eq!(
            packet.association.as_ref().unwrap().task_id,
            association.task_id
        );
        saw_delta = matches!(
            packet.event,
            SessionEvent::Agent {
                event: AgentEvent::ResponseTextDelta { .. },
                ..
            }
        );
    }
    request(&input, 3, SessionCommand::Create);
    let b = match reply(&mut events, 3).await.event {
        SessionEvent::Opened {
            metadata,
            state,
            stream,
            ..
        } => {
            assert_eq!(state.state, "idle");
            assert!(stream.is_none());
            metadata.id
        }
        _ => panic!("create B must succeed while A runs"),
    };
    request(
        &input,
        4,
        SessionCommand::Send {
            session_id: b.clone(),
            message: UserMessage::text("rejected-input"),
        },
    );
    match reply(&mut events, 4).await.event {
        SessionEvent::Error { operation, error } => {
            assert_eq!(operation, "send");
            assert_eq!(error.code, "busy");
            assert_eq!(error.session_id.as_deref(), Some(b.as_str()));
        }
        _ => panic!("capacity rejection must preserve request attribution"),
    }
    request(
        &input,
        5,
        SessionCommand::Open {
            session_id: a.clone(),
        },
    );
    match reply(&mut events, 5).await.event {
        SessionEvent::Opened {
            replay,
            state,
            stream,
            ..
        } => {
            assert_eq!(state.state, "running");
            assert_eq!(stream.unwrap().task_id, association.task_id);
            let stable = serde_json::to_string(&replay.history).unwrap();
            assert!(!stable.contains("active-input"));
            assert!(!stable.contains("live-prefix"));
            assert!(
                !replay
                    .history
                    .iter()
                    .any(|record| matches!(record, JournalRecord::TaskStarted { .. }))
            );
        }
        _ => panic!("A must reopen while its stream is live"),
    }
    release.send(()).unwrap();
    let mut finals = 0;
    loop {
        let packet = next(&mut events).await;
        assert_eq!(
            packet.association.as_ref().unwrap().task_id,
            association.task_id
        );
        match packet.event {
            SessionEvent::Agent {
                event: AgentEvent::ResponseText { text, .. },
                ..
            } => {
                assert_eq!(text, "canonical-final");
                finals += 1;
            }
            SessionEvent::Finished(finished) => {
                assert_eq!(finished.status, "completed");
                assert!(!finished.history_required);
                break;
            }
            SessionEvent::Agent {
                event:
                    AgentEvent::TurnCompleted
                    | AgentEvent::TurnInterrupted
                    | AgentEvent::TurnFailed { .. },
                ..
            } => panic!("no duplicate legacy terminal"),
            _ => {}
        }
    }
    assert_eq!(finals, 1);
    request(&input, 6, SessionCommand::Open { session_id: a });
    match reply(&mut events, 6).await.event {
        SessionEvent::Opened {
            replay,
            state,
            stream,
            ..
        } => {
            assert_eq!(state.state, "idle");
            assert!(stream.is_none());
            let history = serde_json::to_string(&replay.history).unwrap();
            assert!(history.contains("active-input"));
            assert_eq!(history.matches("canonical-final").count(), 1);
        }
        _ => panic!("completed snapshot must contain canonical history"),
    }
    drop(input);
    worker.await.unwrap().unwrap();
    assert_eq!(crate::test_support::requests(server).await.len(), 1);
    let history = workspace.store().open(&b).unwrap().replay.history;
    assert!(
        !serde_json::to_string(&history)
            .unwrap()
            .contains("rejected-input")
    );
}

#[tokio::test]
async fn closing_terminal_output_does_not_cancel_accepted_business_work() {
    let workspace = Workspace::new();
    let (client, release, server) = streaming_client().await;
    let (input, mut events, worker, id) = start(&workspace, client).await;
    request(
        &input,
        2,
        SessionCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("keep-working"),
        },
    );
    assert!(matches!(
        reply(&mut events, 2).await.event,
        SessionEvent::Accepted
    ));
    loop {
        if matches!(
            next(&mut events).await.event,
            SessionEvent::Agent {
                event: AgentEvent::ResponseTextDelta { .. },
                ..
            }
        ) {
            break;
        }
    }
    drop(events);
    drop(input);
    release.send(()).unwrap();
    tokio::time::timeout(Duration::from_secs(5), worker)
        .await
        .unwrap()
        .unwrap()
        .unwrap();
    assert_eq!(crate::test_support::requests(server).await.len(), 1);
    let loaded = workspace.store().open(&id).unwrap();
    assert_eq!(loaded.replay.status, journal::TaskStatus::Completed);
    assert!(
        serde_json::to_string(&loaded.replay.history)
            .unwrap()
            .contains("canonical-final")
    );
}

#[tokio::test]
async fn cancelled_blocked_display_restores_canonical_history_before_one_finished() {
    let workspace = Workspace::new();
    let (handle, actor) = runtime(
        ModelClient::for_test("http://127.0.0.1:9/v1".into()),
        &workspace,
    )
    .with_event_limits(EventLimits {
        messages: 1,
        bytes: 1024 * 1024,
        max_event_bytes: 1024 * 1024,
    })
    .start();
    let id = match handle
        .call(RuntimeCommand::Create {
            working_directory: None,
            system_prompt: None,
        })
        .await
        .unwrap()
    {
        RuntimeReply::Session(state) => state.session_id,
        _ => panic!("expected session"),
    };
    let task = match handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("retained cancelled input"),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Accepted(task) => task,
        _ => panic!("expected task"),
    };
    let association = task.association.clone();
    let metrics = task.events.buffer_metrics();
    tokio::time::timeout(Duration::from_secs(5), async {
        while metrics.snapshot().waiting_producers == 0 {
            tokio::task::yield_now().await;
        }
    })
    .await
    .expect("bounded model event producer never reached Pending");
    assert!(matches!(
        handle
            .call(RuntimeCommand::Cancel {
                session_id: id.clone(),
                task_id: association.task_id.clone()
            })
            .await
            .unwrap(),
        RuntimeReply::Cancelled {
            status: "requested"
        }
    ));
    let (output, mut events) = channel::channel(handle.output_state());
    let delivery = Arc::new(Mutex::new(HashMap::from([(
        id.clone(),
        association.clone(),
    )])));
    let forwarding = tokio::spawn(forward_task(handle.clone(), output, task, delivery));
    let mut restored = false;
    loop {
        let packet = next(&mut events).await;
        assert_eq!(
            packet.association.as_ref().unwrap().task_id,
            association.task_id
        );
        match packet.event {
            SessionEvent::HistoryRestored { replay, state, .. } => {
                assert!(!restored);
                restored = true;
                assert_eq!(state.status, "interrupted");
                let history = serde_json::to_string(&replay.history).unwrap();
                assert!(history.contains("retained cancelled input"));
                assert_eq!(
                    replay
                        .history
                        .iter()
                        .filter(|record| matches!(
                            record,
                            journal::JournalRecord::TaskFinished { .. }
                        ))
                        .count(),
                    1
                );
            }
            SessionEvent::Finished(finished) => {
                assert!(restored, "canonical history must precede Finished");
                assert_eq!(finished.status, "interrupted");
                assert!(finished.history_required);
                break;
            }
            _ => {}
        }
    }
    forwarding.await.unwrap().unwrap();
    assert!(events.recv().await.is_none());
    assert_eq!(metrics.snapshot().bytes, 0);
    assert_eq!(metrics.snapshot().waiting_producers, 0);
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    actor.await.unwrap().unwrap();
}
