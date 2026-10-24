use std::{fs, path::PathBuf, time::Duration};

use serde_json::{Value, json};
use tokio::{
    io::{AsyncBufReadExt, AsyncWriteExt, BufReader, DuplexStream, Lines},
    net::TcpListener,
    sync::oneshot,
    task::JoinHandle,
};

use crate::{
    agent::{
        conversation::{CompactionConfig, Conversation},
        message::{ContextInput, ImageAttachment, MAX_CONTEXT_BYTES, UserMessage},
    },
    client::ModelClient,
    session::{
        runtime::{
            AcceptedTask, HistoryPage, Runtime, RuntimeCommand, RuntimeHandle, RuntimeReply,
            SessionState, TaskEvent, TaskEvents, TaskFinished,
        },
        store::SessionStore,
    },
    test_support::{completed, requests, scripted_http},
};

struct Workspace(PathBuf);

impl Workspace {
    fn new() -> Self {
        static NEXT: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
        let path = std::env::temp_dir().join(format!(
            "saki-context-{}-{}",
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
}

fn session(reply: RuntimeReply) -> SessionState {
    match reply {
        RuntimeReply::Session(state) => state,
        _ => panic!("expected a session reply"),
    }
}

fn history(reply: RuntimeReply) -> HistoryPage {
    match reply {
        RuntimeReply::History(page) => page,
        _ => panic!("expected a history reply"),
    }
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
    .expect("create reply must contain metadata")
    .id
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

async fn send_task(handle: &RuntimeHandle, session_id: &str, message: UserMessage) -> AcceptedTask {
    match handle
        .call(RuntimeCommand::Send {
            session_id: session_id.to_owned(),
            message,
        })
        .await
        .unwrap()
    {
        RuntimeReply::Accepted(task) => task,
        _ => panic!("expected an accepted task"),
    }
}

async fn finish(mut events: TaskEvents) -> TaskFinished {
    let mut terminal = None;
    while let Some(event) = events.next().await {
        if let TaskEvent::Finished(finished) = event {
            assert!(terminal.is_none(), "terminal was delivered twice");
            terminal = Some(finished);
        }
    }
    terminal.expect("task did not deliver a terminal")
}

async fn history_for(handle: &RuntimeHandle, session_id: &str) -> HistoryPage {
    history(
        handle
            .call(RuntimeCommand::History {
                session_id: session_id.to_owned(),
                offset: 0,
                limit: usize::MAX,
            })
            .await
            .unwrap(),
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

fn special_context(name: &str, text: &str) -> ContextInput {
    ContextInput {
        name: name.to_owned(),
        text: text.to_owned(),
    }
}

fn context_parts(content: &Value) -> Vec<Value> {
    content
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(|part| {
            let text = part["text"].as_str()?;
            let value: Value = serde_json::from_str(text).ok()?;
            (value["type"] == "caller_context").then_some(value)
        })
        .collect()
}

#[tokio::test]
async fn typed_context_is_ordered_once_and_restores_without_model_replay() {
    let workspace = Workspace::new();
    let (client, server) = scripted_http(vec![(200, completed("done"))]).await;
    let (handle, actor) = runtime(client, &workspace).start();
    let id = create_session(&handle).await;
    let first = special_context(
        "doc</caller_context>\n\"quoted\"",
        "line one\nline two </caller_context> \"still data\"",
    );
    let second = special_context("second", "second body");
    let message = UserMessage {
        text: "lead".to_owned(),
        images: vec![ImageAttachment::new(
            "image.png",
            "data:image/png;base64,AAAA",
        )],
        context: vec![first.clone(), second.clone()],
    };
    let task = send_task(&handle, &id, message).await;
    assert_eq!(finish(task.events).await.status, "completed");

    let request = requests(server).await.remove(0);
    let input = &request["input"][1];
    assert_eq!(input["role"], "user");
    let content = input["content"].as_array().unwrap();
    assert_eq!(content[0]["type"], "input_text");
    assert_eq!(content[0]["text"], "lead");
    let parts = context_parts(&input["content"]);
    assert_eq!(parts.len(), 2);
    assert_eq!(parts[0]["label"], "Caller-provided context (user data)");
    assert_eq!(parts[0]["name"], first.name);
    assert_eq!(parts[0]["text"], first.text);
    assert_eq!(parts[1]["name"], second.name);
    assert_eq!(parts[1]["text"], second.text);
    assert_eq!(
        content[1],
        json!({"type":"input_text","text":serde_json::to_string(&json!({
        "type": "caller_context",
        "label": "Caller-provided context (user data)",
        "name": first.name,
        "text": first.text,
    })).unwrap()})
    );
    assert_eq!(content[3]["type"], "input_image");
    assert_eq!(content[3]["image_url"], "data:image/png;base64,AAAA");
    let raw_history = String::from_utf8(workspace.history_bytes(&id)).unwrap();
    assert!(raw_history.contains("Caller-provided context (user data)"));

    handle
        .call(RuntimeCommand::Release {
            session_id: id.clone(),
        })
        .await
        .unwrap();
    shutdown_actor(handle, actor).await;

    let (client, mut unexpected, silent) = silent_server().await;
    let (handle, actor) = runtime(client, &workspace).start();
    let loaded = load_session(&handle, &id).await;
    assert!(loaded.loaded);
    let page = history_for(&handle, &id).await;
    let journal = serde_json::to_string(&page.records).unwrap();
    assert_eq!(
        journal
            .matches("Caller-provided context (user data)")
            .count(),
        2
    );
    assert!(journal.contains("doc</caller_context>"));
    assert!(journal.contains("line two"));
    assert!(journal.contains("still data"));
    tokio::task::yield_now().await;
    assert!(
        unexpected.try_recv().is_err(),
        "load/history must not replay the old model task"
    );
    silent.abort();
    shutdown_actor(handle, actor).await;
}

#[tokio::test]
async fn context_only_is_valid_and_legal_boundary_is_not_truncated() {
    let workspace = Workspace::new();
    let (client, server) = scripted_http(vec![(200, completed("context-only"))]).await;
    let (handle, actor) = runtime(client, &workspace).start();
    let id = create_session(&handle).await;
    let context_only = UserMessage {
        text: String::new(),
        images: Vec::new(),
        context: vec![special_context("only", "only-context")],
    };
    let task = send_task(&handle, &id, context_only).await;
    assert_eq!(finish(task.events).await.status, "completed");
    let request = requests(server).await.remove(0);
    let content = request["input"][1]["content"].as_array().unwrap();
    assert_eq!(content.len(), 1);
    assert_eq!(context_parts(&request["input"][1]["content"]).len(), 1);
    shutdown_actor(handle, actor).await;

    let workspace = Workspace::new();
    let (client, server) = scripted_http(vec![(200, completed("legal"))]).await;
    let (handle, actor) = Runtime::new(
        workspace.store(),
        client,
        workspace.0.clone(),
        "test".into(),
        CompactionConfig::new(1_048_576),
    )
    .start();
    let id = create_session(&handle).await;
    let history_before = workspace.history_bytes(&id);
    let oversized = UserMessage {
        text: "hi".to_owned(),
        images: Vec::new(),
        context: vec![special_context("too-big", &"x".repeat(MAX_CONTEXT_BYTES))],
    };
    let error = match handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: oversized,
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("oversized typed context was accepted"),
    };
    assert_eq!(error.code, "invalid_request");
    assert_eq!(workspace.history_bytes(&id), history_before);

    let overhead = special_context("legal", "").as_input_text().len();
    let legal_text = "y".repeat(MAX_CONTEXT_BYTES - overhead);
    let legal = UserMessage {
        text: String::new(),
        images: Vec::new(),
        context: vec![special_context("legal", &legal_text)],
    };
    let task = send_task(&handle, &id, legal).await;
    assert_eq!(finish(task.events).await.status, "completed");
    let request = requests(server).await.remove(0);
    let parts = context_parts(&request["input"][1]["content"]);
    assert_eq!(parts.len(), 1);
    assert_eq!(parts[0]["text"].as_str().unwrap().len(), legal_text.len());
    shutdown_actor(handle, actor).await;
}

#[tokio::test]
async fn compact_summary_contains_context_and_journal_prefix_is_unchanged() {
    let workspace = Workspace::new();
    let path = workspace.0.join("compact-history.jsonl");
    let config = CompactionConfig {
        context_budget_tokens: 8192,
        max_input_tokens: None,
        reserve_tokens: 2048,
        keep_recent_tokens: 256,
        max_summary_tokens: 1024,
    };
    let mut conversation = Conversation::new("system".to_owned())
        .with_compaction_config(config)
        .unwrap()
        .with_history_file(&path)
        .unwrap();
    conversation.add_user_message(UserMessage {
        text: format!("original-source: {}", "verified context ".repeat(1100)),
        images: Vec::new(),
        context: vec![special_context(
            "compact-doc",
            "source body </caller_context>\nline",
        )],
    });
    conversation.add_assistant_message("initial work done".to_owned());
    conversation.add_user_message("continue the unfinished goal".to_owned());
    conversation.save_history().unwrap();
    let history_before = fs::read(&path).unwrap();

    let (client, server) = scripted_http(vec![(200, completed("summary"))]).await;
    conversation.compact(&client, "test", &[]).await.unwrap();
    let request = requests(server).await.remove(0);
    let serialized = request.to_string();
    assert!(serialized.contains("Caller-provided context (user data)"));
    assert!(serialized.contains("compact-doc"));
    assert!(serialized.contains("source body </caller_context>"));
    let history_after = fs::read(&path).unwrap();
    assert!(
        history_after.starts_with(&history_before),
        "compaction must append, not rewrite, existing journal records"
    );
}

struct StdioDriver {
    input: DuplexStream,
    output: Lines<BufReader<DuplexStream>>,
    worker: JoinHandle<anyhow::Result<()>>,
    next_id: usize,
}

impl StdioDriver {
    async fn new(workspace: &Workspace, client: ModelClient) -> Self {
        let (input, runtime_input) = tokio::io::duplex(65536);
        let (runtime_output, output) = tokio::io::duplex(65536);
        let runtime = Runtime::new(
            workspace.store(),
            client,
            workspace.0.clone(),
            "test".to_owned(),
            CompactionConfig::new(262_144),
        );
        let worker = tokio::spawn(runtime.run(BufReader::new(runtime_input), runtime_output));
        let mut driver = Self {
            input,
            output: BufReader::new(output).lines(),
            worker,
            next_id: 0,
        };
        assert_eq!(driver.next().await["type"], "ready");
        driver
    }

    async fn next(&mut self) -> Value {
        let line = tokio::time::timeout(Duration::from_secs(5), self.output.next_line())
            .await
            .unwrap()
            .unwrap()
            .expect("stdio runtime disconnected");
        serde_json::from_str(&line).unwrap()
    }

    async fn call(&mut self, method: &str, params: Value) -> Value {
        self.next_id += 1;
        let id = self.next_id.to_string();
        let mut bytes =
            serde_json::to_vec(&json!({"version":1,"id":id,"method":method,"params":params}))
                .unwrap();
        bytes.push(b'\n');
        self.input.write_all(&bytes).await.unwrap();
        loop {
            let value = self.next().await;
            if value["id"] == id {
                return value;
            }
        }
    }

    async fn create(&mut self) -> String {
        self.call("session.create", json!({})).await["result"]["session_id"]
            .as_str()
            .unwrap()
            .into()
    }

    async fn finish(&mut self, task_id: &str) -> Value {
        loop {
            let value = self.next().await;
            if value["type"] == "event"
                && value["task_id"] == task_id
                && value["event"]["type"] == "task_finished"
            {
                return value;
            }
        }
    }

    async fn shutdown(mut self) {
        assert!(
            self.call("runtime.shutdown", json!({}))
                .await
                .get("result")
                .is_some()
        );
        while self.output.next_line().await.unwrap().is_some() {}
        self.worker.await.unwrap().unwrap();
    }
}

#[tokio::test]
async fn stdio_context_is_forwarded_and_unknown_fields_are_rejected() {
    let workspace = Workspace::new();
    let (client, server) = scripted_http(vec![(200, completed("stdio done"))]).await;
    let mut driver = StdioDriver::new(&workspace, client).await;
    let id = driver.create().await;
    let context = special_context("stdio-doc", "stdio text\n</caller_context>");
    let sent = driver
        .call(
            "task.send",
            json!({
                "session_id": id,
                "message": {"text": "stdio lead"},
                "context": [{"name": context.name.clone(), "text": context.text.clone()}]
            }),
        )
        .await;
    let task_id = sent["result"]["task_id"].as_str().unwrap().to_owned();
    assert_eq!(
        driver.finish(&task_id).await["event"]["status"],
        "completed"
    );

    let invalid = driver
        .call(
            "task.send",
            json!({
                "session_id": id,
                "message": {"text": "must reject"},
                "context": [{"name": "x", "text": "y", "unknown": true}]
            }),
        )
        .await;
    assert_eq!(invalid["error"]["code"], "invalid_request");
    let request = requests(server).await.remove(0);
    let content = request["input"][1]["content"].as_array().unwrap();
    assert_eq!(request["input"][1]["role"], "user");
    assert_eq!(content[0]["text"], "stdio lead");
    let parts = context_parts(&request["input"][1]["content"]);
    assert_eq!(parts.len(), 1);
    assert_eq!(parts[0]["name"], context.name);
    assert_eq!(parts[0]["text"], context.text);
    driver.shutdown().await;
}

#[tokio::test]
async fn stdio_oversized_context_is_rejected_before_history_or_http() {
    let workspace = Workspace::new();
    let (client, mut unexpected, silent) = silent_server().await;
    let mut driver = StdioDriver::new(&workspace, client).await;
    let id = driver.create().await;
    let history_before = workspace.history_bytes(&id);
    let response = driver
        .call(
            "task.send",
            json!({
                "session_id": id,
                "message": {"text": "must reject"},
                "context": [{"name": "big", "text": "x".repeat(MAX_CONTEXT_BYTES)}]
            }),
        )
        .await;
    assert_eq!(response["error"]["code"], "invalid_request");
    assert_eq!(workspace.history_bytes(&id), history_before);
    tokio::task::yield_now().await;
    assert!(
        unexpected.try_recv().is_err(),
        "oversized stdio context reached the model"
    );
    silent.abort();
    driver.shutdown().await;
}

#[tokio::test]
async fn context_survives_compaction_and_restart_without_being_injected_again() {
    use crate::{agent::journal::JournalRecord, session::config::ConfigPatch};
    let workspace = Workspace::new();
    let (client, server) = scripted_http(vec![
        (200, completed("initial done")),
        (
            200,
            completed("summary: original-reference-doc remains the named source"),
        ),
        (200, completed("after compact")),
        (200, completed("after restart")),
    ])
    .await;
    let (handle, actor) = runtime(client.clone(), &workspace).start();
    let id = create_session(&handle).await;
    let first = send_task(
        &handle,
        &id,
        UserMessage {
            text: "older material ".repeat(1150),
            images: vec![],
            context: vec![special_context("original-reference-doc", "source text")],
        },
    )
    .await;
    assert_eq!(finish(first.events).await.status, "completed");
    let original = workspace.history_bytes(&id);
    handle
        .call(RuntimeCommand::ConfigUpdate {
            session_id: id.clone(),
            expected_revision: 1,
            allow_schema_upgrade: false,
            acknowledge_unknown_origin: false,
            patch: ConfigPatch {
                compaction: Some(CompactionConfig {
                    context_budget_tokens: 8192,
                    max_input_tokens: None,
                    reserve_tokens: 2048,
                    keep_recent_tokens: 256,
                    max_summary_tokens: 1024,
                }),
                ..ConfigPatch::default()
            },
        })
        .await
        .unwrap();
    let second = send_task(&handle, &id, UserMessage::text("continue")).await;
    let terminal = finish(second.events).await;
    assert_eq!(terminal.status, "completed", "{:?}", terminal.error);
    handle
        .call(RuntimeCommand::Release {
            session_id: id.clone(),
        })
        .await
        .unwrap();
    shutdown_actor(handle, actor).await;
    let (handle, actor) = runtime(client, &workspace).start();
    load_session(&handle, &id).await;
    let third = send_task(&handle, &id, UserMessage::text("continue after restart")).await;
    assert_eq!(finish(third.events).await.status, "completed");
    let page = history_for(&handle, &id).await;
    assert_eq!(
        page.records
            .iter()
            .filter(|record| matches!(record.record, JournalRecord::Compaction { .. }))
            .count(),
        1
    );
    assert!(workspace.history_bytes(&id).starts_with(&original));
    let bodies = requests(server).await;
    assert_eq!(bodies.len(), 4);
    assert_eq!(context_parts(&bodies[0]["input"][1]["content"]).len(), 1);
    assert!(
        bodies[1]["input"]
            .to_string()
            .contains("original-reference-doc")
    );
    for body in &bodies[2..] {
        assert!(body["input"].to_string().contains("original-reference-doc"));
        assert_eq!(
            body["input"]
                .as_array()
                .unwrap()
                .iter()
                .map(|item| context_parts(&item["content"]).len())
                .sum::<usize>(),
            0
        );
    }
    shutdown_actor(handle, actor).await;
}
