use super::*;

mod startup;

#[test]
fn deltas_flush_immediately_and_final_text_does_not_repeat() {
    let mut output = ConsoleOutput::new(Vec::new());
    output
        .apply_event(AgentEvent::ResponseTextDelta {
            item_id: "m".into(),
            delta: "Hello".into(),
        })
        .unwrap();
    assert_eq!(
        String::from_utf8(output.writer.clone()).unwrap(),
        "Agent:\n\nHello"
    );
    output
        .apply_event(AgentEvent::ResponseTextDelta {
            item_id: "m".into(),
            delta: " world".into(),
        })
        .unwrap();
    output
        .apply_event(AgentEvent::ResponseText {
            item_id: "m".into(),
            text: "Hello world!".into(),
        })
        .unwrap();
    output
        .apply_event(AgentEvent::Usage {
            input_tokens: Some(10),
            output_tokens: Some(3),
        })
        .unwrap();
    output
        .apply_event(AgentEvent::ResponseText {
            item_id: "m".into(),
            text: "Next response".into(),
        })
        .unwrap();
    assert_eq!(
        String::from_utf8(output.writer).unwrap(),
        "Agent:\n\nHello world!\nAgent:\n\nNext response\n"
    );
}

#[test]
fn reasoning_end_closes_the_block_and_final_response_does_not_repeat_it() {
    let mut output = ConsoleOutput::new(Vec::new());
    for event in [
        AgentEvent::ReasoningDelta {
            item_id: "r".into(),
            delta: "Check first".into(),
        },
        AgentEvent::ReasoningCompleted {
            item_id: "r".into(),
        },
        AgentEvent::ResponseTextDelta {
            item_id: "m".into(),
            delta: "Answer".into(),
        },
        AgentEvent::Reasoning {
            item_id: "r".into(),
            text: "Check first".into(),
        },
        AgentEvent::ResponseText {
            item_id: "m".into(),
            text: "Answer".into(),
        },
    ] {
        output.apply_event(event).unwrap();
    }
    assert_eq!(
        String::from_utf8(output.writer).unwrap(),
        "Thinking...:\n\nCheck first\nAgent:\n\nAnswer\n"
    );
}

fn session_fixture() -> (
    std::path::PathBuf,
    crate::session::store::SessionStore,
    String,
) {
    static NEXT: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
    let root = std::env::temp_dir().join(format!(
        "saki-command-console-{}-{}",
        crate::agent::journal::new_id().unwrap(),
        NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
    ));
    std::fs::create_dir(&root).unwrap();
    let store = crate::session::store::SessionStore::new(root.join("sessions")).unwrap();
    let id = store
        .create(
            &root,
            "test",
            "system",
            crate::agent::conversation::CompactionConfig::new(262_144),
        )
        .unwrap();
    (root, store, id)
}
fn snapshot_event(store: &crate::session::store::SessionStore, id: &str) -> SessionEvent {
    let loaded = store.open(id).unwrap();
    SessionEvent::Opened {
        metadata: loaded.metadata,
        replay: loaded.replay,
        context: AgentEvent::ContextUpdated {
            model: "test".into(),
            context_tokens: None,
            context_window: Some(262_144),
            input_limit: Some(253_952),
        },
    }
}

#[tokio::test]
async fn console_local_commands_use_session_operations_and_never_send_model_messages() {
    let (root, store, id) = session_fixture();
    let script = format!(
        "/help\n/rename Compiler work\n/sessions\n/new\n/resume {id}\n/resume missing\n/resuem\n"
    );
    let (tx, mut requests) = tokio::sync::mpsc::unbounded_channel();
    let (events, rx) = tokio::sync::mpsc::unbounded_channel();
    events.send(snapshot_event(&store, &id)).unwrap();
    let backend = async {
        let mut operations = Vec::new();
        while let Some(request) = requests.recv().await {
            let event = match request {
                SessionCommand::Send { .. } => panic!("local command reached model message path"),
                SessionCommand::Rename { session_id, title } => {
                    operations.push("rename");
                    SessionEvent::Renamed(store.rename(&session_id, &title).unwrap())
                }
                SessionCommand::List => {
                    operations.push("list");
                    SessionEvent::Listed(store.list().unwrap())
                }
                SessionCommand::Create => {
                    operations.push("create");
                    let new = store
                        .create(
                            &root,
                            "test",
                            "system",
                            crate::agent::conversation::CompactionConfig::new(262_144),
                        )
                        .unwrap();
                    snapshot_event(&store, &new)
                }
                SessionCommand::Open { session_id } => {
                    operations.push("open");
                    if session_id == "missing" {
                        SessionEvent::Error {
                            message: "session missing".into(),
                        }
                    } else {
                        snapshot_event(&store, &session_id)
                    }
                }
                SessionCommand::Cancel { .. } => SessionEvent::Error {
                    message: "session missing".into(),
                },
            };
            events.send(event).unwrap();
        }
        drop(events);
        operations
    };
    let mut output = Vec::new();
    let frontend = run_with_io(
        tx,
        rx,
        BufReader::new(script.as_bytes()),
        &mut output,
        false,
    );
    let (result, operations) = tokio::time::timeout(std::time::Duration::from_secs(5), async {
        tokio::join!(frontend, backend)
    })
    .await
    .unwrap();
    result.unwrap();
    assert_eq!(operations, vec!["rename", "list", "create", "open", "open"]);
    let text = String::from_utf8(output).unwrap();
    assert!(text.contains("Local commands:"));
    assert!(text.contains("Compiler work"));
    assert!(text.contains("Input kept: /resume missing"));
    assert!(text.contains("Input kept: /resuem"));
    for entry in std::fs::read_dir(root.join("sessions")).unwrap() {
        let journal = std::fs::read_to_string(entry.unwrap().path().join("history.jsonl")).unwrap();
        assert!(!journal.contains("task_started"));
        assert!(!journal.contains("/help"));
    }
    std::fs::remove_dir_all(root).unwrap();
}

#[tokio::test]
async fn console_can_handle_commands_during_a_job_and_drain_piped_messages_after_eof() {
    let (root, store, id) = session_fixture();
    let script = "first job\n/help\n/sessions\n/rename Active job\nsecond job\n";
    let (tx, mut requests) = tokio::sync::mpsc::unbounded_channel();
    let (events, rx) = tokio::sync::mpsc::unbounded_channel();
    events.send(snapshot_event(&store, &id)).unwrap();
    let backend = async {
        let mut sent = Vec::new();
        while let Some(request) = requests.recv().await {
            match request {
                SessionCommand::Send {
                    session_id,
                    message,
                } => {
                    assert_eq!(session_id, id);
                    sent.push(message.text);
                    if sent.len() == 1 {
                        events
                            .send(SessionEvent::Agent {
                                session_id: id.clone(),
                                event: AgentEvent::ResponseTextDelta {
                                    item_id: "m".into(),
                                    delta: "Working".into(),
                                },
                            })
                            .unwrap();
                    } else {
                        events
                            .send(SessionEvent::Agent {
                                session_id: id.clone(),
                                event: AgentEvent::TurnCompleted,
                            })
                            .unwrap();
                    }
                }
                SessionCommand::List => {
                    assert_eq!(sent.len(), 1);
                    events
                        .send(SessionEvent::Listed(store.list().unwrap()))
                        .unwrap();
                }
                SessionCommand::Rename { session_id, title } => {
                    assert_eq!(sent.len(), 1);
                    let metadata = store.rename(&session_id, &title).unwrap();
                    events
                        .send(SessionEvent::TitleUpdated(metadata.clone()))
                        .unwrap();
                    events.send(SessionEvent::Renamed(metadata)).unwrap();
                    events
                        .send(SessionEvent::Agent {
                            session_id: id.clone(),
                            event: AgentEvent::TurnCompleted,
                        })
                        .unwrap();
                }
                _ => panic!("unexpected request"),
            }
        }
        drop(events);
        sent
    };
    let mut output = Vec::new();
    let frontend = run_with_io(
        tx,
        rx,
        BufReader::new(script.as_bytes()),
        &mut output,
        false,
    );
    let (result, sent) = tokio::time::timeout(std::time::Duration::from_secs(5), async {
        tokio::join!(frontend, backend)
    })
    .await
    .unwrap();
    result.unwrap();
    assert_eq!(sent, vec!["first job", "second job"]);
    let text = String::from_utf8(output).unwrap();
    assert!(text.contains("Local commands:"));
    assert!(text.contains("Active job"));
    std::fs::remove_dir_all(root).unwrap();
}
