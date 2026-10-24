use super::*;
use crate::protocol::ToolDefinition;
use futures_util::FutureExt;
use serde_json::json;
use std::sync::{
    Arc, Mutex,
    atomic::{AtomicUsize, Ordering},
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::{TcpListener, TcpStream},
    sync::oneshot,
    task::JoinHandle,
};

/// Serves exactly one scripted response and returns the request body it got.
async fn mock_once(
    status: u16,
    content_type: &'static str,
    body: String,
) -> (ChatCompletionsClient, JoinHandle<Value>) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let task = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
        let request = read_request(&mut socket).await;
        socket
            .write_all(
                format!(
                    "HTTP/1.1 {status} Test\r\nContent-Type: {content_type}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                    body.len()
                )
                .as_bytes(),
            )
            .await
            .unwrap();
        socket.write_all(body.as_bytes()).await.unwrap();
        request
    });
    (
        ChatCompletionsClient::for_test(format!("http://{address}/v1")),
        task,
    )
}

async fn read_request(socket: &mut TcpStream) -> Value {
    let mut data = Vec::new();
    let header_end = loop {
        let mut buffer = [0; 4096];
        let n = socket.read(&mut buffer).await.unwrap();
        assert!(n > 0);
        data.extend_from_slice(&buffer[..n]);
        if let Some(index) = data.windows(4).position(|window| window == b"\r\n\r\n") {
            break index + 4;
        }
    };
    let length: usize = std::str::from_utf8(&data[..header_end])
        .unwrap()
        .lines()
        .find_map(|line| {
            let (name, value) = line.split_once(':')?;
            name.eq_ignore_ascii_case("content-length")
                .then(|| value.trim().parse().unwrap())
        })
        .unwrap();
    while data.len() < header_end + length {
        let mut buffer = [0; 4096];
        let n = socket.read(&mut buffer).await.unwrap();
        assert!(n > 0);
        data.extend_from_slice(&buffer[..n]);
    }
    serde_json::from_slice(&data[header_end..header_end + length]).unwrap()
}

fn sse(event: Value) -> String {
    format!("data: {event}\n\n")
}

fn user(text: &str) -> InputItem {
    InputItem::Message {
        role: MessageRole::User,
        content: InputMessageContent::Text(text.to_owned()),
    }
}

#[test]
fn request_body_encodes_text_tools_calls_and_native_developer_role() {
    let input = vec![
        InputItem::Message {
            role: MessageRole::System,
            content: InputMessageContent::Text("sys".to_owned()),
        },
        InputItem::Message {
            role: MessageRole::Developer,
            content: InputMessageContent::Text("dev".to_owned()),
        },
        user("hi"),
        InputItem::ToolCall {
            call_id: "call_1".to_owned(),
            name: "read".to_owned(),
            arguments: "{\"p\":1}".to_owned(),
        },
        InputItem::ToolResult {
            call_id: "call_1".to_owned(),
            output: "ok".to_owned(),
        },
        InputItem::ToolCall {
            call_id: "call_2".to_owned(),
            name: "bash".to_owned(),
            arguments: "{}".to_owned(),
        },
        InputItem::ToolResult {
            call_id: "call_2".to_owned(),
            output: "done".to_owned(),
        },
    ];
    let tools = vec![ToolDefinition {
        tool_type: ToolType::Function,
        name: "read".to_owned(),
        description: "Read a file".to_owned(),
        parameters: json!({"type": "object"}),
    }];
    let request = ModelRequest::new("m".to_owned(), &input, tools).with_generation(
        Some(100),
        Some(0.5),
        None,
    );
    let body = ChatCompletionsClient::request_body(&request).unwrap();
    assert_eq!(body["model"], "m");
    assert_eq!(body["messages"][0]["role"], "system");
    assert_eq!(body["messages"][1]["role"], "developer");
    assert_eq!(body["messages"][2]["content"], "hi");
    assert_eq!(body["messages"][3]["role"], "assistant");
    assert_eq!(body["messages"][3]["content"], "");
    assert_eq!(body["messages"][3]["tool_calls"][0]["id"], "call_1");
    assert_eq!(
        body["messages"][3]["tool_calls"][0]["function"]["name"],
        "read"
    );
    assert_eq!(body["messages"][5]["tool_calls"][0]["id"], "call_2");
    assert_eq!(body["messages"][4]["role"], "tool");
    assert_eq!(body["messages"][6]["role"], "tool");
    assert_eq!(body["tools"][0]["type"], "function");
    assert_eq!(body["tools"][0]["function"]["name"], "read");
    assert_eq!(body["max_tokens"], 100);
    assert_eq!(body["temperature"], 0.5);
}

#[test]
fn request_body_refuses_images_and_responses_continuation_before_any_request() {
    let image = vec![InputItem::Message {
        role: MessageRole::User,
        content: InputMessageContent::Parts(vec![InputContentPart::Image {
            url: "https://example.test/i.png".to_owned(),
        }]),
    }];
    let error =
        ChatCompletionsClient::request_body(&ModelRequest::new("m".to_owned(), &image, Vec::new()))
            .unwrap_err();
    assert!(error.to_string().contains("images are not supported"));

    let reasoning = vec![InputItem::Reasoning {
        content: vec![crate::protocol::ReasoningText::new("hmm")],
        provider_data: None,
    }];
    let error = ChatCompletionsClient::request_body(&ModelRequest::new(
        "m".to_owned(),
        &reasoning,
        Vec::new(),
    ))
    .unwrap_err();
    assert!(error.to_string().contains("cannot send Responses-native"));
}

#[tokio::test]
async fn json_response_maps_text_tool_calls_and_usage() {
    let body = json!({
        "id": "chatcmpl-1", "model": "m",
        "choices": [{
            "message": {
                "content": "hello",
                "tool_calls": [{"id": "call_9", "type": "function",
                    "function": {"name": "read", "arguments": "{\"path\":\"a\"}"}}]
            },
            "finish_reason": "tool_calls"
        }],
        "usage": {"prompt_tokens": 10, "completion_tokens": 2}
    })
    .to_string();
    let (client, server) = mock_once(200, "application/json", body).await;
    let input = vec![user("hi")];
    let response = client
        .respond(ModelRequest::new("m".to_owned(), &input, Vec::new()))
        .await
        .unwrap();
    assert_eq!(response.status.as_deref(), Some("completed"));
    assert!(response.incomplete_details.is_none());
    let usage = response.usage.unwrap();
    assert_eq!(usage.input_tokens, Some(10));
    assert_eq!(usage.output_tokens, Some(2));
    assert!(
        matches!(&response.output[0], OutputItem::Message { content, .. } if content[0].text == "hello")
    );
    assert!(
        matches!(&response.output[1], OutputItem::ToolCall { call_id, name, arguments, .. }
        if call_id == "call_9" && name == "read" && arguments == "{\"path\":\"a\"}")
    );
    let sent = server.await.unwrap();
    assert_eq!(sent["model"], "m");
    assert_eq!(sent["stream"], Value::Null);
}

#[tokio::test]
async fn json_response_rejects_non_completing_finish_reasons() {
    for finish in [json!("length"), json!("content_filter"), json!("mystery")] {
        let body = json!({
            "id": "chatcmpl-1", "model": "m",
            "choices": [{"message": {"content": "partial"}, "finish_reason": finish}]
        })
        .to_string();
        let (client, _server) = mock_once(200, "application/json", body).await;
        let input = vec![user("hi")];
        let error = client
            .respond(ModelRequest::new("m".to_owned(), &input, Vec::new()))
            .await
            .unwrap_err();
        assert!(error.to_string().contains("did not finish"), "{error:?}");
    }
}

#[tokio::test]
async fn json_response_without_finish_reason_fails() {
    let body = json!({
        "id": "chatcmpl-1",
        "choices": [{"message": {"content": "partial"}}]
    })
    .to_string();
    let (client, _server) = mock_once(200, "application/json", body).await;
    let input = vec![user("hi")];
    let error = client
        .respond(ModelRequest::new("m".to_owned(), &input, Vec::new()))
        .await
        .unwrap_err();
    assert!(error.to_string().contains("no finish_reason"));
}

#[tokio::test]
async fn stream_aggregates_interleaved_fragments_and_usage_tail() {
    let mut stream = String::new();
    stream.push_str(&sse(json!({
        "id": "chatcmpl-2", "model": "m",
        "choices": [{"delta": {"role": "assistant", "content": "he"}}]
    })));
    stream.push_str(&sse(json!({
        "id": "chatcmpl-2",
        "choices": [{"delta": {"content": "llo",
            "tool_calls": [{"index": 0, "id": "call_a",
                "function": {"name": "read", "arguments": "{\"pa"}}]}}]
    })));
    stream.push_str(&sse(json!({
        "id": "chatcmpl-2",
        "choices": [{"delta": {
            "tool_calls": [
                {"index": 0, "function": {"arguments": "th\":\"a\"}"}},
                {"index": 1, "id": "call_b", "function": {"name": "bash", "arguments": "{}"}}
            ]}}]
    })));
    stream.push_str(&sse(json!({
        "id": "chatcmpl-2",
        "choices": [],
        "usage": {"prompt_tokens": 7, "completion_tokens": 3}
    })));
    stream.push_str(&sse(json!({
        "id": "chatcmpl-2",
        "choices": [{"delta": {}, "finish_reason": "tool_calls"}]
    })));
    stream.push_str("data: [DONE]\n\n");

    let (client, server) = mock_once(200, "text/event-stream", stream).await;
    let mut deltas = String::new();
    let input = vec![user("hi")];
    let response = client
        .respond_stream(
            ModelRequest::new("m".to_owned(), &input, Vec::new()),
            |event| {
                if let ModelStreamEvent::TextDelta { delta, .. } = event {
                    deltas.push_str(&delta);
                }
            },
        )
        .await
        .unwrap();
    assert_eq!(deltas, "hello");
    let usage = response.usage.unwrap();
    assert_eq!(usage.input_tokens, Some(7));
    assert_eq!(usage.output_tokens, Some(3));
    assert_eq!(response.output.len(), 3);
    assert!(
        matches!(&response.output[0], OutputItem::Message { content, .. } if content[0].text == "hello")
    );
    assert!(
        matches!(&response.output[1], OutputItem::ToolCall { call_id, arguments, .. }
        if call_id == "call_a" && arguments == "{\"path\":\"a\"}")
    );
    assert!(
        matches!(&response.output[2], OutputItem::ToolCall { call_id, name, .. }
        if call_id == "call_b" && name == "bash")
    );

    let sent = server.await.unwrap();
    assert_eq!(sent["stream"], true);
    assert_eq!(sent["stream_options"]["include_usage"], true);
}

#[tokio::test]
async fn stream_without_usage_keeps_stats_unknown() {
    let mut stream = String::new();
    stream.push_str(&sse(json!({
        "id": "chatcmpl-3",
        "choices": [{"delta": {"content": "ok"}}]
    })));
    stream.push_str(&sse(json!({
        "id": "chatcmpl-3",
        "choices": [{"delta": {}, "finish_reason": "stop"}]
    })));
    stream.push_str("data: [DONE]\n\n");
    let (client, _server) = mock_once(200, "text/event-stream", stream).await;
    let input = vec![user("hi")];
    let response = client
        .respond_stream(
            ModelRequest::new("m".to_owned(), &input, Vec::new()),
            |_| {},
        )
        .await
        .unwrap();
    assert!(response.usage.is_none());
}

#[tokio::test]
async fn stream_requires_a_completing_finish_reason() {
    // [DONE] alone is not completion.
    let mut done_only = String::new();
    done_only.push_str(&sse(json!({
        "id": "chatcmpl-4",
        "choices": [{"delta": {"content": "partial"}}]
    })));
    done_only.push_str("data: [DONE]\n\n");
    let (client, _server) = mock_once(200, "text/event-stream", done_only).await;
    let input = vec![user("hi")];
    let error = client
        .respond_stream(
            ModelRequest::new("m".to_owned(), &input, Vec::new()),
            |_| {},
        )
        .await
        .unwrap_err();
    assert!(error.to_string().contains("no finish_reason"), "{error:?}");

    // Truncation with visible text still fails and commits no tools.
    let mut truncated = String::new();
    truncated.push_str(&sse(json!({
        "id": "chatcmpl-5",
        "choices": [{"delta": {"content": "partial",
            "tool_calls": [{"index": 0, "id": "call_x", "function": {"name": "read", "arguments": "{}"}}]}}]
    })));
    truncated.push_str(&sse(json!({
        "id": "chatcmpl-5",
        "choices": [{"delta": {}, "finish_reason": "length"}]
    })));
    truncated.push_str("data: [DONE]\n\n");
    let (client, _server) = mock_once(200, "text/event-stream", truncated).await;
    let error = client
        .respond_stream(
            ModelRequest::new("m".to_owned(), &input, Vec::new()),
            |_| {},
        )
        .await
        .unwrap_err();
    assert!(
        error.to_string().contains("did not finish: length"),
        "{error:?}"
    );
}

#[tokio::test]
async fn stream_json_fallback_is_a_complete_response() {
    let body = json!({
        "id": "chatcmpl-6",
        "choices": [{"message": {"content": "direct"}, "finish_reason": "stop"}]
    })
    .to_string();
    let (client, _server) = mock_once(200, "application/json", body).await;
    let input = vec![user("hi")];
    let response = client
        .respond_stream(
            ModelRequest::new("m".to_owned(), &input, Vec::new()),
            |_| {},
        )
        .await
        .unwrap();
    assert!(
        matches!(&response.output[0], OutputItem::Message { content, .. } if content[0].text == "direct")
    );
}

#[tokio::test]
async fn overflow_needs_the_structured_code_not_message_wording() {
    let wording = json!({
        "error": {"code": "invalid_request_error",
            "message": "this mentions the maximum context length but is not overflow"}
    });
    let (client, server) = mock_once(400, "application/json", wording.to_string()).await;
    let input = vec![user("hi")];
    let attempted = client
        .respond_once(ModelRequest::new("m".to_owned(), &input, Vec::new()))
        .await;
    assert_eq!(attempted.attempts, 1);
    assert!(matches!(
        attempted.result.unwrap_err().downcast_ref::<ApiError>(),
        Some(ApiError::Http { status, .. }) if *status == reqwest::StatusCode::BAD_REQUEST
    ));
    let _ = server.await.unwrap();

    let real = json!({"error": {"code": "context_length_exceeded", "message": "too long"}});
    let (client, _server) = mock_once(400, "application/json", real.to_string()).await;
    let error = client
        .respond(ModelRequest::new("m".to_owned(), &input, Vec::new()))
        .await
        .unwrap_err();
    assert!(matches!(
        error.downcast_ref::<ApiError>(),
        Some(ApiError::ContextOverflow { .. })
    ));
}

#[tokio::test]
async fn compaction_on_the_chat_client_uses_one_request_and_keeps_context() {
    use crate::agent::conversation::{CompactionConfig, Conversation};
    let mut conversation = Conversation::new("System instructions must survive.".to_owned())
        .with_compaction_config(CompactionConfig {
            context_budget_tokens: 8192,
            max_input_tokens: None,
            reserve_tokens: 2048,
            keep_recent_tokens: 256,
            max_summary_tokens: 1024,
        })
        .unwrap();
    conversation.add_user_message(format!(
        "original-source: {}",
        "verified context ".repeat(1100)
    ));
    conversation.add_assistant_message("Completed initial work.".to_owned());
    conversation.add_user_message("Continue the unfinished goal.".to_owned());
    let before = serde_json::to_value(&conversation).unwrap();
    let (client, server) = mock_once(
        500,
        "application/json",
        json!({"error":{"code":"server_error"}}).to_string(),
    )
    .await;
    let client = crate::client::ModelClient::ChatCompletions(client);
    let error = conversation
        .compact_if_needed(&client, "test", &[])
        .await
        .map(|_| ())
        .unwrap_err();
    // The summary went through exactly one HTTP attempt: a retry would have
    // failed on the closed listener with a transport error instead.
    assert!(
        matches!(
            error.downcast_ref::<ApiError>(),
            Some(ApiError::Http { status, .. })
                if *status == reqwest::StatusCode::INTERNAL_SERVER_ERROR
        ),
        "{error:?}"
    );
    assert_eq!(serde_json::to_value(&conversation).unwrap(), before);
    let sent = tokio::time::timeout(std::time::Duration::from_secs(5), server)
        .await
        .unwrap()
        .unwrap();
    assert!(sent["messages"].is_array());
}

#[tokio::test]
async fn null_tool_calls_mean_no_calls() {
    let input = vec![user("hi")];
    // Complete JSON: the standard schema allows tool_calls: null.
    let body = json!({
        "id": "chatcmpl-null",
        "choices": [{"message": {"content": "hello", "tool_calls": null}, "finish_reason": "stop"}]
    })
    .to_string();
    let (client, _server) = mock_once(200, "application/json", body).await;
    let response = client
        .respond(ModelRequest::new("m".to_owned(), &input, Vec::new()))
        .await
        .unwrap();
    assert!(
        matches!(&response.output[0], OutputItem::Message { content, .. } if content[0].text == "hello")
    );

    // The stream JSON fallback shares this decoder (compact and titles too).
    let body = json!({
        "id": "chatcmpl-null-2",
        "choices": [{"message": {"content": "direct", "tool_calls": null}, "finish_reason": "stop"}]
    })
    .to_string();
    let (client, _server) = mock_once(200, "application/json", body).await;
    let response = client
        .respond_stream(
            ModelRequest::new("m".to_owned(), &input, Vec::new()),
            |_| {},
        )
        .await
        .unwrap();
    assert!(
        matches!(&response.output[0], OutputItem::Message { content, .. } if content[0].text == "direct")
    );
}

#[tokio::test]
async fn async_callback_backpressure_holds_chat_deltas_until_released() {
    let body = sse(json!({"id":"c1","choices":[{"delta":{"content":"Hel"}}]}))
        + &sse(json!({"id":"c1","choices":[{"delta":{"content":"lo"}}]}))
        + &sse(json!({"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}))
        + "data: [DONE]\n\n";
    let (client, server) = mock_once(200, "text/event-stream", body).await;
    let (seen_tx, seen_rx) = oneshot::channel();
    let (release_tx, release_rx) = oneshot::channel();
    let calls = Arc::new(AtomicUsize::new(0));
    let seen_events = Arc::new(Mutex::new(Vec::new()));
    let mut gate = Some(release_rx);
    let mut notify = Some(seen_tx);
    let task_calls = calls.clone();
    let task_events = seen_events.clone();
    let task = tokio::spawn(async move {
        client
            .respond_stream_async(
                ModelRequest::new("m".to_owned(), &[], Vec::new()),
                move |event| {
                    task_calls.fetch_add(1, Ordering::SeqCst);
                    task_events.lock().unwrap().push(event.clone());
                    let gate = gate.take();
                    let notify = notify.take();
                    async move {
                        if let Some(notify) = notify {
                            let _ = notify.send(());
                        }
                        if let Some(gate) = gate {
                            let _ = gate.await;
                        }
                    }
                    .boxed()
                },
            )
            .await
    });
    seen_rx.await.unwrap();
    assert_eq!(
        calls.load(Ordering::SeqCst),
        1,
        "the decoder must not deliver another Chat delta while the first callback is gated"
    );
    release_tx.send(()).unwrap();
    let response = task.await.unwrap().unwrap();
    let _ = server.await.unwrap();
    let events = seen_events.lock().unwrap();
    assert_eq!(
        events.as_slice(),
        &[
            ModelStreamEvent::TextDelta {
                item_id: "c1-text".to_owned(),
                delta: "Hel".to_owned()
            },
            ModelStreamEvent::TextDelta {
                item_id: "c1-text".to_owned(),
                delta: "lo".to_owned()
            }
        ]
    );
    assert!(
        matches!(&response.output[0], OutputItem::Message { content, .. } if content[0].text == "Hello")
    );
}

#[tokio::test]
async fn reasoning_continuation_survives_json_sse_and_serialized_context() {
    for streaming in [false, true] {
        for reasoning in [Some("inspect file\nthen answer"), Some(""), None] {
            let tool = json!({"id":"read-1", "type":"function", "function":{"name":"read", "arguments":"{}"}});
            let (kind, body) = if streaming {
                let (first, second) = reasoning
                    .map(|text| {
                        let cut = text.len() / 2;
                        (json!(&text[..cut]), json!(&text[cut..]))
                    })
                    .unwrap_or((Value::Null, Value::Null));
                (
                    "text/event-stream",
                    format!(
                        "{}{}{}data: [DONE]\n\n",
                        sse(json!({"id":"r", "choices":[{"delta":{"reasoning_content": first}}]})),
                        sse(
                            json!({"id":"r", "choices":[{"delta":{"reasoning_content": second, "content":"checking", "tool_calls":[{"index":0,"id":"read-1","function":{"name":"read","arguments":"{}"}}]}}]})
                        ),
                        sse(
                            json!({"id":"r", "choices":[{"delta":{"reasoning_content":null},"finish_reason":"tool_calls"}]})
                        )
                    ),
                )
            } else {
                ("application/json", json!({"id":"r", "choices":[{"message":{"reasoning_content":reasoning,"content":"checking","tool_calls":[tool.clone()]},"finish_reason":"tool_calls"}]}).to_string())
            };
            let (client, server) = mock_once(200, kind, body).await;
            let mut deltas = String::new();
            let response = client
                .respond_stream(
                    ModelRequest::new("m".into(), &[user("read")], vec![]),
                    |event| {
                        if let ModelStreamEvent::ReasoningDelta { delta, .. } = event {
                            deltas.push_str(&delta);
                        }
                    },
                )
                .await
                .unwrap();
            server.await.unwrap();
            if streaming {
                assert_eq!(deltas, reasoning.unwrap_or_default());
            }
            let mut input = vec![user("read")];
            for item in response.output {
                input.push(match item {
                    OutputItem::Reasoning {
                        content,
                        provider_data,
                        ..
                    } => InputItem::Reasoning {
                        content,
                        provider_data,
                    },
                    OutputItem::Message { role, content, .. } => InputItem::Message {
                        role,
                        content: InputMessageContent::Text(
                            content.into_iter().map(|part| part.text).collect(),
                        ),
                    },
                    OutputItem::ToolCall {
                        call_id,
                        name,
                        arguments,
                        ..
                    } => InputItem::ToolCall {
                        call_id,
                        name,
                        arguments,
                    },
                    _ => panic!("unexpected output"),
                });
            }
            input.push(InputItem::ToolResult {
                call_id: "read-1".into(),
                output: "marker".into(),
            });
            // This is the same serde boundary used by persisted conversation context.
            let restored: Vec<InputItem> =
                serde_json::from_slice(&serde_json::to_vec(&input).unwrap()).unwrap();
            let messages = encode_messages(&restored).unwrap();
            assert_eq!(messages, json!([
                {"role":"user","content":"read"},
                {"role":"assistant","content":"checking","reasoning_content":reasoning,"tool_calls":[tool]},
                {"role":"tool","tool_call_id":"read-1","content":"marker"}
            ]).as_array().unwrap().clone());
        }
    }
}

#[test]
fn final_assistant_reasoning_is_retained_across_user_turns() {
    let input = vec![
        user("first"),
        InputItem::Reasoning {
            content: vec![],
            provider_data: Some(ProviderData {
                api: API.into(),
                value: json!({"reasoning_content":"complete reasoning"}),
            }),
        },
        InputItem::Message {
            role: MessageRole::Assistant,
            content: InputMessageContent::Text("answer".into()),
        },
        user("next"),
    ];
    let messages = encode_messages(&input).unwrap();
    assert_eq!(messages.len(), 3);
    assert_eq!(
        messages[1],
        json!({"role":"assistant","reasoning_content":"complete reasoning","content":"answer"})
    );
    assert!(messages[1].get("tool_calls").is_none());
}
