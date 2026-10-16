use super::*;
use crate::protocol::ToolDefinition;
use serde_json::json;
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::{TcpListener, TcpStream},
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
