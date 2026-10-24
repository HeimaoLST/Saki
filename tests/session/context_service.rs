use super::*;
use crate::{
    agent::{
        conversation::CompactionConfig,
        journal::{self, JournalRecord},
    },
    client::{
        ClientConfig, ModelApi,
        connections::{Connection, ConnectionRegistry},
    },
    protocol::{InputItem, InputMessageContent, MessageRole},
    test_support::{completed, read_request, requests},
};
use serde_json::{Value, json};
use std::{
    fs::{self, OpenOptions},
    io,
    path::Path,
};
use tokio::{
    io::AsyncWriteExt,
    net::{TcpListener, TcpStream},
};

fn message(role: MessageRole, text: &str) -> JournalRecord {
    JournalRecord::Messages {
        items: vec![InputItem::Message {
            role,
            content: InputMessageContent::Text(text.to_owned()),
        }],
    }
}

fn append(store: &SessionStore, id: &str, record: JournalRecord) {
    let path = store.directory(id).unwrap().join("history.jsonl");
    journal::write(
        &mut OpenOptions::new().append(true).open(path).unwrap(),
        &record,
    )
    .unwrap();
}

fn seed_source(directory: &Directory, marker: &str, compact: bool) -> (SessionStore, String) {
    let store = SessionStore::new(directory.0.join("sessions")).unwrap();
    let id = store
        .create(
            &directory.0,
            "test",
            SYSTEM_PROMPT,
            CompactionConfig::new(262_144),
        )
        .unwrap();
    append(
        &store,
        &id,
        message(
            MessageRole::User,
            &format!("{marker} {}", "source material ".repeat(800)),
        ),
    );
    append(
        &store,
        &id,
        message(MessageRole::Assistant, "source turn completed"),
    );
    if compact {
        append(
            &store,
            &id,
            JournalRecord::Compaction {
                summary: "first source summary".to_owned(),
                keep_from: 2,
                tokens_before: 900,
                tokens_after: 100,
                usage: None,
            },
        );
        append(&store, &id, message(MessageRole::User, "later source turn"));
        append(
            &store,
            &id,
            JournalRecord::Compaction {
                summary: "second source summary".to_owned(),
                keep_from: 3,
                tokens_before: 400,
                tokens_after: 80,
                usage: None,
            },
        );
    }
    (store, id)
}

fn connected_runtime(directory: &Directory, base: String) -> Runtime {
    let connection = Connection::new(
        "test".into(),
        ClientConfig::new(ModelApi::Responses, "offline-key", base),
        "test".into(),
    )
    .unwrap();
    Runtime::new(
        SessionStore::new(directory.0.join("sessions")).unwrap(),
        connection.client().clone(),
        directory.0.clone(),
        "test".into(),
        CompactionConfig::new(262_144),
    )
    .with_connections(ConnectionRegistry::new("test".into(), vec![connection]).unwrap())
}

async fn create_context_b(driver: &mut Driver) -> String {
    let created = driver
        .call(
            "session.create",
            json!({"storage_schema":2,"config":{"connection_id":"test"}}),
        )
        .await;
    assert!(created.get("error").is_none(), "{created}");
    let id = created["result"]["session_id"].as_str().unwrap().to_owned();
    let updated = driver
        .call(
            "session.config.update",
            json!({
                "session_id":id,
                "expected_revision":1,
                "patch":{"tool_selection":{"builtin":["open_context","read_context","search_context"],"external":[]}}
            }),
        )
        .await;
    assert!(updated.get("error").is_none(), "{updated}");
    assert_eq!(
        updated["result"]["effective_config"]["tool_selection"]["builtin"],
        json!(["open_context", "read_context", "search_context"])
    );
    id
}

fn function_output(request: &Value) -> Value {
    let output = request["input"]
        .as_array()
        .unwrap()
        .iter()
        .rev()
        .find(|item| item["type"] == "function_call_output")
        .unwrap()["output"]
        .as_str()
        .unwrap();
    serde_json::from_str(output).unwrap()
}

fn tool_names(request: &Value) -> Vec<String> {
    let mut names: Vec<_> = request["tools"]
        .as_array()
        .unwrap()
        .iter()
        .map(|tool| tool["name"].as_str().unwrap().to_owned())
        .collect();
    names.sort();
    names
}

fn tool_response(name: &str, arguments: Value) -> Value {
    json!({
        "id": format!("response-{name}"),
        "status": "completed",
        "output": [{
            "type": "function_call",
            "status": "completed",
            "id": format!("item-{name}"),
            "call_id": format!("call-{name}"),
            "name": name,
            "arguments": arguments.to_string(),
        }]
    })
}

async fn send_http(socket: &mut TcpStream, value: Value) -> io::Result<()> {
    let body = serde_json::to_vec(&value).unwrap();
    let header = format!(
        "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
        body.len()
    );
    socket.write_all(header.as_bytes()).await?;
    socket.write_all(&body).await?;
    Ok(())
}

fn bytes(path: &Path) -> Vec<u8> {
    fs::read(path).unwrap()
}

#[tokio::test]
async fn stdio_context_views_reads_searches_releases_and_capabilities_are_bounded() {
    let directory = Directory::new();
    let (source_store, source) = seed_source(&directory, "SERVICE-UNIQUE-MARKER-中文", false);
    let source_dir = source_store.directory(&source).unwrap();
    let source_metadata = bytes(&source_dir.join("metadata.json"));
    let source_history = bytes(&source_dir.join("history.jsonl"));

    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    listener.set_nonblocking(true).unwrap();
    let mut driver = Driver::new(
        &directory,
        ModelClient::for_test(format!("http://{}/v1", listener.local_addr().unwrap())),
    )
    .await;
    let session = driver.create().await;

    let caps = driver.call("runtime.capabilities", json!({})).await;
    let methods = caps["result"]["methods"].as_array().unwrap();
    for method in [
        "context.open",
        "context.read",
        "context.search",
        "context.release",
    ] {
        assert!(methods.iter().any(|value| value == method), "{caps}");
    }
    assert!(!methods.iter().any(|value| {
        value
            .as_str()
            .is_some_and(|value| value.starts_with("analysis."))
    }));
    assert_eq!(
        caps["result"]["context_access"]["views"],
        json!(["history", "working"])
    );
    let default_tools = driver
        .call("session.config.get", json!({"session_id":session}))
        .await;
    assert_eq!(
        default_tools["result"]["effective_config"]["tool_selection"]["builtin"],
        json!(["bash", "edit", "read", "write"])
    );

    let opened = driver
        .call(
            "context.open",
            json!({"session_id":source,"view":"history"}),
        )
        .await;
    assert!(opened.get("error").is_none(), "{opened}");
    let reference = opened["result"]["context_ref"].as_str().unwrap().to_owned();
    assert!(!opened.to_string().contains("SERVICE-UNIQUE-MARKER"));

    let read = driver
        .call(
            "context.read",
            json!({"context_ref":reference,"offset":0,"max_bytes":8192}),
        )
        .await;
    assert!(read.get("error").is_none(), "{read}");
    let text = read["result"]["text"].as_str().unwrap();
    let cjk_offset = text.find("中").unwrap();
    let invalid = driver
        .call(
            "context.read",
            json!({"context_ref":reference,"offset":cjk_offset + 1,"max_bytes":8192}),
        )
        .await;
    assert_eq!(invalid["error"]["code"], "invalid_request");

    let unknown = driver
        .call(
            "context.read",
            json!({"context_ref":reference,"offset":0,"max_bytes":8192,"unexpected":true}),
        )
        .await;
    assert_eq!(unknown["error"]["code"], "invalid_request");

    let search = driver
        .call(
            "context.search",
            json!({"context_ref":reference,"query":"SERVICE-UNIQUE-MARKER","max_bytes":8192}),
        )
        .await;
    assert!(search.get("error").is_none(), "{search}");
    assert!(!search["result"]["hits"].as_array().unwrap().is_empty());

    let request = driver
        .call(
            "context.open",
            json!({"session_id":source,"view":"request"}),
        )
        .await;
    assert_eq!(request["error"]["code"], "context_unavailable");
    assert_eq!(request["error"]["reason"], "request_not_published");

    let released = driver
        .call("context.release", json!({"context_ref":reference}))
        .await;
    assert_eq!(released["result"]["released"], true);
    let released_again = driver
        .call("context.release", json!({"context_ref":reference}))
        .await;
    assert_eq!(released_again["result"]["released"], false);

    assert_eq!(bytes(&source_dir.join("metadata.json")), source_metadata);
    assert_eq!(bytes(&source_dir.join("history.jsonl")), source_history);
    assert_eq!(
        listener.accept().unwrap_err().kind(),
        io::ErrorKind::WouldBlock
    );
    driver.shutdown().await;
}

#[tokio::test]
async fn actual_agent_tools_open_search_and_read_after_two_compactions() {
    let directory = Directory::new();
    let marker = "B-TOOL-CHAIN-UNIQUE-MARKER";
    let (source_store, source) = seed_source(&directory, marker, true);
    let source_dir = source_store.directory(&source).unwrap();
    let source_metadata = bytes(&source_dir.join("metadata.json"));
    let source_history = bytes(&source_dir.join("history.jsonl"));

    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base = format!("http://{}/v1", listener.local_addr().unwrap());
    let server_marker = marker.to_owned();
    let server_source = source.clone();
    let server = tokio::spawn(async move {
        let mut requests = Vec::new();
        for step in 0..4 {
            let (mut socket, _) = listener.accept().await.unwrap();
            let request = read_request(&mut socket).await;
            requests.push(request.clone());
            let response = match step {
                0 => tool_response(
                    "open_context",
                    json!({"session_id":server_source,"view":"history"}),
                ),
                1 => {
                    let opened = function_output(&request);
                    assert!(!opened.to_string().contains(&server_marker));
                    let reference = opened["context_ref"].as_str().unwrap();
                    tool_response(
                        "search_context",
                        json!({"context_ref":reference,"query":server_marker,"max_bytes":8192}),
                    )
                }
                2 => {
                    let searched = function_output(&request);
                    let hit = &searched["hits"][0];
                    let reference = searched["context_ref"].as_str().unwrap();
                    tool_response(
                        "read_context",
                        json!({"context_ref":reference,"offset":hit["start"],"max_bytes":4096}),
                    )
                }
                3 => {
                    let read = function_output(&request);
                    assert!(read["text"].as_str().unwrap().contains(&server_marker));
                    completed(&format!("B read the stored marker {server_marker}"))
                }
                _ => unreachable!(),
            };
            send_http(&mut socket, response).await.unwrap();
        }
        requests
    });

    let mut driver = Driver::from_runtime(connected_runtime(&directory, base)).await;
    let b = create_context_b(&mut driver).await;
    let accepted = driver
        .call(
            "task.send",
            json!({
                "session_id":b,
                "message":{"text":"use the allowed context"},
                "context_sources":[source]
            }),
        )
        .await;
    assert!(accepted.get("error").is_none(), "{accepted}");
    assert_eq!(
        driver.finish(&accepted["result"]).await["event"]["status"],
        "completed"
    );
    let wire = requests(server).await;
    assert_eq!(wire.len(), 4);
    for request in &wire {
        assert_eq!(
            tool_names(request),
            ["open_context", "read_context", "search_context"]
        );
    }
    assert!(!wire[0]["input"].to_string().contains(marker));
    let opened = function_output(&wire[1]);
    let reference = opened["context_ref"].as_str().unwrap().to_owned();
    assert!(!opened.to_string().contains(marker));
    let searched = function_output(&wire[2]);
    assert!(!searched["hits"].as_array().unwrap().is_empty());
    let read = function_output(&wire[3]);
    assert!(read["text"].as_str().unwrap().contains(marker));
    assert!(
        driver
            .seen
            .iter()
            .any(|value| value["event"]["type"] == "tool_finished"
                && value["event"]["name"] == "open_context")
    );
    assert!(driver.seen.iter().any(|value| {
        value["event"]["type"] == "response_text"
            && value["event"]["text"]
                .as_str()
                .is_some_and(|text| text.contains(marker))
    }));

    assert_eq!(bytes(&source_dir.join("metadata.json")), source_metadata);
    assert_eq!(bytes(&source_dir.join("history.jsonl")), source_history);

    let expired = driver
        .call(
            "context.read",
            json!({"context_ref":reference,"offset":0,"max_bytes":1024}),
        )
        .await;
    assert_eq!(expired["error"]["code"], "context_unavailable");
    driver.shutdown().await;
}

#[tokio::test]
async fn context_tools_deny_unbound_source_and_default_task_can_read_own_session() {
    let directory = Directory::new();
    let (a_store, a) = seed_source(&directory, "SCOPE-ALLOWED-MARKER", false);
    let (c_store, c) = seed_source(&directory, "SCOPE-DENIED-C-MARKER", false);
    let a_dir = a_store.directory(&a).unwrap();
    let c_dir = c_store.directory(&c).unwrap();
    let a_metadata = bytes(&a_dir.join("metadata.json"));
    let a_history = bytes(&a_dir.join("history.jsonl"));
    let c_metadata = bytes(&c_dir.join("metadata.json"));
    let c_history = bytes(&c_dir.join("history.jsonl"));

    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base = format!("http://{}/v1", listener.local_addr().unwrap());
    let mut driver = Driver::from_runtime(connected_runtime(&directory, base)).await;
    let b = create_context_b(&mut driver).await;
    let server_c = c.clone();
    let server_b = b.clone();
    let server = tokio::spawn(async move {
        let mut requests = Vec::new();
        for step in 0..4 {
            let (mut socket, _) = listener.accept().await.unwrap();
            let request = read_request(&mut socket).await;
            requests.push(request.clone());
            let response = match step {
                0 => tool_response(
                    "open_context",
                    json!({"session_id":server_c,"view":"history"}),
                ),
                1 => completed("scope denied"),
                2 => tool_response(
                    "open_context",
                    json!({"session_id":server_b,"view":"history"}),
                ),
                3 => completed("own context works"),
                _ => unreachable!(),
            };
            send_http(&mut socket, response).await.unwrap();
        }
        requests
    });

    let first = driver
        .call(
            "task.send",
            json!({
                "session_id":b,
                "message":{"text":"do not read C"},
                "context_sources":[a]
            }),
        )
        .await;
    assert!(first.get("error").is_none(), "{first}");
    assert_eq!(
        driver.finish(&first["result"]).await["event"]["status"],
        "completed"
    );
    let first_tool = driver
        .seen
        .iter()
        .find(|value| {
            value["task_id"] == first["result"]["task_id"]
                && value["event"]["type"] == "tool_finished"
        })
        .unwrap()["event"]["output"]
        .clone();
    assert_eq!(first_tool["error"]["code"], "scope_denied");
    assert!(!first_tool.to_string().contains("SCOPE-DENIED-C-MARKER"));

    let second = driver
        .call(
            "task.send",
            json!({"session_id":b,"message":{"text":"use own context"}}),
        )
        .await;
    assert!(second.get("error").is_none(), "{second}");
    assert_eq!(
        driver.finish(&second["result"]).await["event"]["status"],
        "completed"
    );
    let wire = requests(server).await;
    assert_eq!(wire.len(), 4);
    assert!(
        !wire[0]["input"]
            .to_string()
            .contains("SCOPE-DENIED-C-MARKER")
    );
    let denied = function_output(&wire[1]);
    assert_eq!(denied["error"]["code"], "scope_denied");
    let own = function_output(&wire[3]);
    assert!(own.get("context_ref").is_some(), "{own}");
    assert!(!own.to_string().contains("SCOPE-DENIED-C-MARKER"));
    assert!(
        !serde_json::to_string(&driver.seen)
            .unwrap()
            .contains("SCOPE-DENIED-C-MARKER")
    );
    assert_eq!(bytes(&a_dir.join("metadata.json")), a_metadata);
    assert_eq!(bytes(&a_dir.join("history.jsonl")), a_history);
    assert_eq!(bytes(&c_dir.join("metadata.json")), c_metadata);
    assert_eq!(bytes(&c_dir.join("history.jsonl")), c_history);
    driver.shutdown().await;
}

#[tokio::test]
async fn large_source_open_keeps_control_and_source_tool_progress_independent() {
    let directory = Directory::new();
    let (store, source) = seed_source(&directory, "LARGE-HISTORY-MARKER", false);
    append(
        &store,
        &source,
        message(
            MessageRole::User,
            &"large history ".repeat((8_usize * 1024 * 1024).div_ceil(14)),
        ),
    );
    append(
        &store,
        &source,
        message(MessageRole::Assistant, "keep recent answer"),
    );
    append(
        &store,
        &source,
        JournalRecord::Compaction {
            summary: "bounded working context".into(),
            keep_from: 4,
            tokens_before: 10000,
            tokens_after: 30,
            usage: None,
        },
    );
    std::fs::write(directory.0.join("progress.txt"), "source-tool-progress").unwrap();
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let client = ModelClient::for_test(format!("http://{}/v1", listener.local_addr().unwrap()));
    let (arrived_tx, arrived) = tokio::sync::oneshot::channel();
    let (release_model, model_released) = tokio::sync::oneshot::channel();
    let server = tokio::spawn(async move {
        let (mut first, _) = listener.accept().await.unwrap();
        let first_request = read_request(&mut first).await;
        arrived_tx.send(()).unwrap();
        model_released.await.unwrap();
        send_http(
            &mut first,
            tool_response("read", json!({"path":"progress.txt"})),
        )
        .await
        .unwrap();
        let (mut second, _) = listener.accept().await.unwrap();
        let second_request = read_request(&mut second).await;
        send_http(&mut second, completed("source completed"))
            .await
            .unwrap();
        vec![first_request, second_request]
    });
    let runtime = Runtime::new(
        store,
        client,
        directory.0.clone(),
        "test".into(),
        CompactionConfig::new(262_144),
    );
    let resources = runtime.contexts.clone();
    let (handle, worker) = runtime.start();
    assert!(matches!(
        handle
            .call(RuntimeCommand::Load {
                session_id: source.clone()
            })
            .await
            .unwrap(),
        RuntimeReply::Session(_)
    ));
    let RuntimeReply::Accepted(mut task) = handle
        .call(RuntimeCommand::Send {
            session_id: source.clone(),
            message: "continue source".into(),
        })
        .await
        .unwrap()
    else {
        panic!("accepted")
    };
    arrived.await.unwrap();
    let (captured, ready) = tokio::sync::oneshot::channel();
    let (resume, resumed) = std::sync::mpsc::channel();
    resources.gate_next_open(crate::session::context::ReadGate {
        captured,
        resume: resumed,
    });
    let opened = handle
        .submit(RuntimeCommand::ContextOpen(
            crate::session::context::OpenContext {
                session_id: source.clone(),
                view: crate::session::context::ContextView::History,
            },
        ))
        .unwrap();
    ready.await.unwrap();
    let RuntimeReply::Session(status) = handle
        .call(RuntimeCommand::Status {
            session_id: source.clone(),
        })
        .await
        .unwrap()
    else {
        panic!("status")
    };
    assert_eq!(status.state, "running");
    release_model.send(()).unwrap();
    loop {
        match tokio::time::timeout(std::time::Duration::from_secs(5), task.events.next())
            .await
            .unwrap()
            .unwrap()
        {
            TaskEvent::Event(packet) => {
                if let crate::agent::AgentEvent::ToolFinished { name, output, .. } = &packet.event {
                    assert_eq!(name, "read");
                    assert!(output.to_string().contains("source-tool-progress"));
                    break;
                }
            }
            TaskEvent::Finished(_) => {
                panic!("source must complete a tool while the context read is still pending")
            }
        }
    }
    resume.send(()).unwrap();
    let RuntimeReply::ContextOpened(context) = opened.await.unwrap().unwrap() else {
        panic!("context")
    };
    assert!(context.text_bytes >= 8 * 1024 * 1024);
    assert!(matches!(
        handle
            .call(RuntimeCommand::ContextRelease {
                context_ref: context.context_ref
            })
            .await
            .unwrap(),
        RuntimeReply::ContextReleased { released: true }
    ));
    loop {
        if let Some(TaskEvent::Finished(result)) = task.events.next().await {
            assert_eq!(result.status, "completed");
            break;
        }
    }
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    worker.await.unwrap().unwrap();
    assert_eq!(server.await.unwrap().len(), 2);
    assert_eq!(resources.retained_bytes(), 0);
}

#[tokio::test]
async fn dropped_context_caller_is_drained_without_leaking_reservation_or_reference() {
    let directory = Directory::new();
    let (store, source) = seed_source(&directory, "drop-read", false);
    let (client, server) = scripted_http(vec![]).await;
    let runtime = Runtime::new(
        store,
        client,
        directory.0.clone(),
        "test".into(),
        CompactionConfig::new(262_144),
    );
    let resources = runtime.contexts.clone();
    let (captured, ready) = tokio::sync::oneshot::channel();
    let (resume, resumed) = std::sync::mpsc::channel();
    resources.gate_next_open(crate::session::context::ReadGate {
        captured,
        resume: resumed,
    });
    let (handle, worker) = runtime.start();
    let waiting = handle
        .submit(RuntimeCommand::ContextOpen(
            crate::session::context::OpenContext {
                session_id: source.clone(),
                view: crate::session::context::ContextView::History,
            },
        ))
        .unwrap();
    ready.await.unwrap();
    drop(waiting);
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    assert!(matches!(
        handle
            .call(RuntimeCommand::Status { session_id: source })
            .await
            .unwrap(),
        RuntimeReply::Session(_)
    ));
    assert!(matches!(
        handle
            .call(RuntimeCommand::ContextRelease {
                context_ref: "unknown".into()
            })
            .await
            .unwrap(),
        RuntimeReply::ContextReleased { released: false }
    ));
    resume.send(()).unwrap();
    worker.await.unwrap().unwrap();
    assert_eq!(resources.retained_bytes(), 0);
    assert!(requests(server).await.is_empty());
}
