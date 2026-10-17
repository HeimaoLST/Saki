//! Deterministic cancellation contracts for the core agent.
//!
//! This file is registered by the owner of tests/agent/mod.rs; it deliberately
//! does not touch that module or any runtime file. Reuses super::* fixtures and
//! tests/support with one small controlled model server.

use super::*;
use std::time::Duration;

use serde_json::{Value, json};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::{TcpListener, TcpStream},
    sync::{mpsc::UnboundedReceiver, watch},
};

use crate::{
    agent::journal::TaskStatus,
    protocol::{InputItem, InputMessageContent, MessageRole},
};

/// One scripted HTTP step. `Held` writes SSE events and keeps the body open
/// until the client drops it; `Json` answers a complete non-streaming response.
/// One scripted HTTP step. `Held` writes SSE events and keeps the body open
/// until the client drops it; `Json` answers `200`; `Error` answers `400` with
/// a typed JSON error body (context overflow).
enum Step {
    Held(Vec<Value>),
    Json(Value),
    Error(Value),
}

struct ModelServer {
    address: std::net::SocketAddr,
    requests: UnboundedReceiver<Value>,
    handle: tokio::task::JoinHandle<()>,
}

impl Drop for ModelServer {
    fn drop(&mut self) {
        // Fixture cleanup: a failed or unfinished test must not leave the
        // scripted server task detached.
        self.handle.abort();
    }
}

impl ModelServer {
    /// Serves `steps` in order, one accepted connection each, and forwards
    /// every decoded request body to the test channel.
    async fn start(steps: Vec<Step>) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let (sender, requests) = tokio::sync::mpsc::unbounded_channel();
        let handle = tokio::spawn(async move {
            for step in steps {
                let (mut socket, _) = listener.accept().await.unwrap();
                let request = crate::test_support::read_request(&mut socket).await;
                let _ = sender.send(request);
                match step {
                    Step::Json(body) => write_json_status(&mut socket, &body, 200).await,
                    Step::Error(body) => write_json_status(&mut socket, &body, 400).await,
                    Step::Held(events) => {
                        socket
                            .write_all(
                                b"HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nConnection: close\r\n\r\n",
                            )
                            .await
                            .unwrap();
                        for event in events {
                            socket
                                .write_all(format!("data: {event}\n\n").as_bytes())
                                .await
                                .unwrap();
                        }
                        socket.flush().await.unwrap();
                        // Hold the body open until cancellation drops the client.
                        let mut buffer = [0u8; 64];
                        while let Ok(size) = socket.read(&mut buffer).await {
                            if size == 0 {
                                break;
                            }
                        }
                    }
                }
            }
        });
        Self {
            address,
            requests,
            handle,
        }
    }

    fn client(&self) -> crate::client::ModelClient {
        crate::client::ModelClient::for_test(format!("http://{}/v1", self.address))
    }

    async fn next_request(&mut self, timeout: Duration) -> Value {
        tokio::time::timeout(timeout, self.requests.recv())
            .await
            .expect("model request within bound")
            .expect("model server alive")
    }
}

async fn write_json_status(socket: &mut TcpStream, body: &Value, status: u16) {
    let payload = serde_json::to_vec(body).unwrap();
    socket
        .write_all(
            format!(
                "HTTP/1.1 {status} Test\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                payload.len()
            )
            .as_bytes(),
        )
        .await
        .unwrap();
    socket.write_all(&payload).await.unwrap();
    socket.flush().await.unwrap();
}

/// Waits for `wanted` stream events, ignoring status/context events.
async fn wait_for_stream_events(
    receiver: &mut UnboundedReceiver<AgentEvent>,
    wanted: usize,
    timeout: Duration,
) -> usize {
    let deadline = tokio::time::Instant::now() + timeout;
    let mut seen = 0;
    while seen < wanted {
        let event = tokio::time::timeout_at(deadline, receiver.recv())
            .await
            .unwrap()
            .unwrap();
        if matches!(
            event,
            AgentEvent::ResponseTextDelta { .. } | AgentEvent::ReasoningDelta { .. }
        ) {
            seen += 1;
        }
    }
    seen
}

fn interrupted_function_outputs(input: &Value) -> Vec<(String, bool)> {
    input
        .as_array()
        .map(Vec::as_slice)
        .unwrap_or_default()
        .iter()
        .filter_map(|item| {
            if item["type"] != "function_call_output" {
                return None;
            }
            let call_id = item["call_id"].as_str()?.to_owned();
            let output = match item["output"].clone() {
                Value::String(text) => serde_json::from_str::<Value>(&text).ok()?,
                other => other,
            };
            Some((call_id, output["interrupted"] == true))
        })
        .collect()
}

fn assistant_texts(items: &[InputItem]) -> Vec<String> {
    items
        .iter()
        .filter_map(|item| match item {
            InputItem::Message {
                role: MessageRole::Assistant,
                content: InputMessageContent::Text(text),
            } => Some(text.clone()),
            _ => None,
        })
        .collect()
}

fn task_statuses(records: &[Value]) -> Vec<String> {
    records
        .iter()
        .filter(|record| record["type"] == "task_finished")
        .map(|record| record["status"].as_str().unwrap().to_owned())
        .collect()
}

fn tool_results(items: &[InputItem]) -> Vec<(String, Value)> {
    items
        .iter()
        .filter_map(|item| match item {
            InputItem::ToolResult { call_id, output } => {
                Some((call_id.clone(), serde_json::from_str(output).ok()?))
            }
            _ => None,
        })
        .collect()
}

fn unmatched_tool_calls(items: &[InputItem]) -> Vec<String> {
    let mut calls = Vec::new();
    let mut results = Vec::new();
    for item in items {
        match item {
            InputItem::ToolCall { call_id, .. } => calls.push(call_id.clone()),
            InputItem::ToolResult { call_id, .. } => results.push(call_id.clone()),
            _ => {}
        }
    }
    calls.retain(|call_id| !results.contains(call_id));
    calls
}

#[tokio::test]
async fn cancel_held_stream_persists_ordered_partials_and_replays_interrupted() {
    let events = vec![
        json!({"type":"response.output_text.delta","item_id":"m1","delta":"alpha "}),
        json!({"type":"response.output_text.delta","item_id":"m2","delta":"beta "}),
        json!({"type":"response.reasoning_text.delta","item_id":"r","delta":"hidden reasoning"}),
        json!({"type":"response.output_text.delta","item_id":"m1","delta":"ALPHA2"}),
        json!({"type":"response.output_text.delta","item_id":"m2","delta":"BETA2"}),
    ];
    let mut server = ModelServer::start(vec![
        Step::Held(events),
        Step::Json(completed("next answer")),
    ])
    .await;
    let journal = Journal::new();
    let (event_tx, mut event_rx) = event_channels();
    let (cancel_tx, cancel_rx) = watch::channel(false);
    let conversation = conversation().with_history_file(&journal.0).unwrap();
    let mut agent = Agent::new(server.client(), ToolRegistry::new(), conversation)
        .with_model("test".to_owned())
        .with_events(event_tx)
        .with_cancel(cancel_rx);
    let task = tokio::spawn(async move {
        let result = agent.handle_user_message("go".to_owned()).await;
        (agent, result)
    });
    assert_eq!(
        wait_for_stream_events(&mut event_rx, 5, Duration::from_secs(5)).await,
        5
    );
    cancel_tx.send(true).unwrap();
    let (agent, result) = tokio::time::timeout(Duration::from_secs(5), task)
        .await
        .unwrap()
        .unwrap();
    assert!(is_interrupted(result.as_ref().unwrap_err()));
    let first_request = server.next_request(Duration::from_secs(5)).await;
    assert_eq!(first_request["stream"], true);

    let items = agent.conversation.items().to_vec();
    assert_eq!(
        assistant_texts(&items),
        vec!["alpha ALPHA2".to_owned(), "beta BETA2".to_owned()],
        "partial assistant items keep first-seen order and their accumulated text"
    );
    assert!(
        !items
            .iter()
            .any(|item| matches!(item, InputItem::Reasoning { .. })),
        "streamed reasoning must stay display-only"
    );
    assert!(unmatched_tool_calls(&items).is_empty());

    // Valid interrupted replay while the cancelled turn is the latest task.
    drop(agent);
    let (replay_conversation, replay) = Conversation::restore(&journal.0).unwrap();
    assert_eq!(replay.status, TaskStatus::Interrupted);
    let context = serde_json::to_string(&replay.context).unwrap();
    assert!(context.contains("alpha ALPHA2") && context.contains("beta BETA2"));
    assert!(!context.contains("hidden reasoning"));

    // Next turn succeeds on the restored conversation.
    let mut next_agent = Agent::new(server.client(), ToolRegistry::new(), replay_conversation)
        .with_model("test".to_owned());
    tokio::time::timeout(
        Duration::from_secs(5),
        next_agent.handle_user_message("next".to_owned()),
    )
    .await
    .unwrap()
    .unwrap();
    let second_request = server.next_request(Duration::from_secs(5)).await;
    let second_input = serde_json::to_string(&second_request["input"]).unwrap();
    assert!(second_input.contains("alpha ALPHA2"));
    assert!(second_input.contains("beta BETA2"));
    drop(next_agent);

    assert_eq!(
        task_statuses(&journal.records()),
        vec!["interrupted".to_owned(), "completed".to_owned()]
    );
    let (_, final_replay) = Conversation::restore(&journal.0).unwrap();
    assert_eq!(final_replay.status, TaskStatus::Completed);
    assert!(
        serde_json::to_string(&final_replay.context)
            .unwrap()
            .contains("next answer")
    );
}

#[tokio::test]
async fn cancel_pending_multi_tool_closes_running_and_never_runs_second() {
    let first_entries = Arc::new(AtomicUsize::new(0));
    let second_entries = Arc::new(AtomicUsize::new(0));
    let first_entered = Arc::new(tokio::sync::Notify::new());
    let mut tools = ToolRegistry::new();
    let first_count = first_entries.clone();
    let first_entered_handler = first_entered.clone();
    tools.registry(Tool {
        definition: ToolDefinition {
            tool_type: ToolType::Function,
            name: "first".to_owned(),
            description: "Blocks until cancelled.".to_owned(),
            parameters: json!({"type":"object","properties":{}}),
        },
        handler: Arc::new(move |_| {
            let count = first_count.clone();
            let entered = first_entered_handler.clone();
            Box::pin(async move {
                count.fetch_add(1, Ordering::SeqCst);
                entered.notify_one();
                std::future::pending::<()>().await;
                Ok(json!({"ok": true}))
            })
        }),
    });
    let second_count = second_entries.clone();
    tools.registry(Tool {
        definition: ToolDefinition {
            tool_type: ToolType::Function,
            name: "second".to_owned(),
            description: "Must never run.".to_owned(),
            parameters: json!({"type":"object","properties":{}}),
        },
        handler: Arc::new(move |_| {
            let count = second_count.clone();
            Box::pin(async move {
                count.fetch_add(1, Ordering::SeqCst);
                Ok(json!({"ok": true}))
            })
        }),
    });
    let tool_response = json!({"id":"tools", "status":"completed", "output":[
        {"type":"function_call", "id":"f1", "status":"completed", "call_id":"c1", "name":"first", "arguments":"{}"},
        {"type":"function_call", "id":"f2", "status":"completed", "call_id":"c2", "name":"second", "arguments":"{}"}
    ], "usage":{"input_tokens":6000,"output_tokens":100}});
    let mut server = ModelServer::start(vec![
        Step::Json(tool_response),
        Step::Json(completed("after tools")),
    ])
    .await;
    let journal = Journal::new();
    let (event_tx, _event_rx) = event_channels();
    let (cancel_tx, cancel_rx) = watch::channel(false);
    let conversation = conversation().with_history_file(&journal.0).unwrap();
    let mut agent = Agent::new(server.client(), tools, conversation)
        .with_model("test".to_owned())
        .with_events(event_tx)
        .with_cancel(cancel_rx);
    let task = tokio::spawn(async move {
        let result = agent.handle_user_message("two tools".to_owned()).await;
        (agent, result)
    });

    // Wait until the blocking tool handler is actually running before cancelling
    // so the started/completed assertions are deterministic.
    tokio::time::timeout(Duration::from_secs(5), first_entered.notified())
        .await
        .expect("first tool entered within bound");
    cancel_tx.send(true).unwrap();
    let (mut agent, result) = tokio::time::timeout(Duration::from_secs(5), task)
        .await
        .unwrap()
        .unwrap();
    assert!(is_interrupted(result.as_ref().unwrap_err()));
    assert_eq!(first_entries.load(Ordering::SeqCst), 1);
    assert_eq!(second_entries.load(Ordering::SeqCst), 0, "second tool ran");

    let items = agent.conversation.items().to_vec();
    let results = tool_results(&items);
    let call_ids: Vec<&str> = results.iter().map(|(id, _)| id.as_str()).collect();
    assert!(
        call_ids.contains(&"c1") && call_ids.contains(&"c2"),
        "both pairs close immediately: {results:?}"
    );
    assert!(
        results
            .iter()
            .all(|(_, value)| value["interrupted"] == true),
        "every closed pair uses the interrupted result shape: {results:?}"
    );
    assert!(unmatched_tool_calls(&items).is_empty());

    let _tools_request = server.next_request(Duration::from_secs(5)).await;
    // Re-attach a fresh receiver: the cancelled receiver stays true and would
    // pre-cancel the next turn.
    let (_next_cancel_tx, next_cancel_rx) = watch::channel(false);
    agent = agent.with_cancel(next_cancel_rx);
    tokio::time::timeout(
        Duration::from_secs(5),
        agent.handle_user_message("next".to_owned()),
    )
    .await
    .unwrap()
    .unwrap();
    let next_request = server.next_request(Duration::from_secs(5)).await;
    let mut outputs = interrupted_function_outputs(&next_request["input"]);
    outputs.sort_by(|left, right| left.0.cmp(&right.0));
    assert_eq!(
        outputs,
        vec![("c1".to_owned(), true), ("c2".to_owned(), true)],
        "exactly one interrupted output per call id reaches the next request"
    );
    drop(agent);

    assert_eq!(
        task_statuses(&journal.records()),
        vec!["interrupted".to_owned(), "completed".to_owned()]
    );
    let (_, replay) = Conversation::restore(&journal.0).unwrap();
    assert_eq!(replay.status, TaskStatus::Completed);
    assert!(unmatched_tool_calls(&replay.context).is_empty());
}

#[tokio::test]
async fn cancel_blocked_compaction_leaves_context_consistent() {
    let journal = Journal::new();
    let conversation = previous_history().with_history_file(&journal.0).unwrap();
    let mut server = ModelServer::start(vec![
        Step::Error(overflow()),
        Step::Held(Vec::new()),
        Step::Error(overflow()),
        Step::Json(completed("Preserve project ion-42")),
        Step::Json(completed("after compact cancel")),
    ])
    .await;
    let (event_tx, _event_rx) = event_channels();
    let (cancel_tx, cancel_rx) = watch::channel(false);
    let mut agent = Agent::new(server.client(), ToolRegistry::new(), conversation)
        .with_model("test".to_owned())
        .with_events(event_tx)
        .with_cancel(cancel_rx);
    let task = tokio::spawn(async move {
        let result = agent.handle_user_message("continue".to_owned()).await;
        (agent, result)
    });

    let first_request = server.next_request(Duration::from_secs(5)).await;
    assert_eq!(first_request["stream"], true);
    let summary_request = server.next_request(Duration::from_secs(5)).await;
    assert!(
        serde_json::to_string(&summary_request["input"])
            .unwrap()
            .contains("handoff summary"),
        "the blocked request is the compaction summary"
    );
    cancel_tx.send(true).unwrap();
    let (mut agent, result) = tokio::time::timeout(Duration::from_secs(5), task)
        .await
        .unwrap()
        .unwrap();
    assert!(is_interrupted(result.as_ref().unwrap_err()));
    let records = journal.records();
    assert!(
        !records.iter().any(|record| record["type"] == "compaction"),
        "a cancelled compaction must not write a checkpoint: {records:?}"
    );
    let items = agent.conversation.items().to_vec();
    assert!(
        !serde_json::to_string(&items)
            .unwrap()
            .contains("[Earlier conversation summary]"),
        "a cancelled compaction must leave the active context intact"
    );

    // The next turn recovers through a real overflow: stream, compact, retry.
    let (_next_cancel_tx, next_cancel_rx) = watch::channel(false);
    agent = agent.with_cancel(next_cancel_rx);
    tokio::time::timeout(
        Duration::from_secs(5),
        agent.handle_user_message("next".to_owned()),
    )
    .await
    .unwrap()
    .unwrap();
    let retry_request = server.next_request(Duration::from_secs(5)).await;
    assert_eq!(retry_request["stream"], true);
    let summary_retry = server.next_request(Duration::from_secs(5)).await;
    assert!(
        serde_json::to_string(&summary_retry["input"])
            .unwrap()
            .contains("handoff summary")
    );
    let reply_request = server.next_request(Duration::from_secs(5)).await;
    assert_eq!(reply_request["stream"], true);
    assert!(
        serde_json::to_string(&reply_request["input"])
            .unwrap()
            .contains("[Earlier conversation summary]"),
        "the successful compaction must rebuild the request"
    );
    drop(agent);

    assert_eq!(task_statuses(&journal.records()).len(), 2);
    let (_, replay) = Conversation::restore(&journal.0).unwrap();
    assert_eq!(replay.status, TaskStatus::Completed);
    assert!(
        journal
            .records()
            .iter()
            .any(|record| record["type"] == "compaction"),
        "the successful retry writes the checkpoint it completed"
    );
    assert!(unmatched_tool_calls(&replay.context).is_empty());
}
