use super::*;
use crate::client::ResponsesClient;
use crate::protocol::{ModelRequest, OutputItem};
use serde_json::json;
use tokio::{io::AsyncWriteExt, net::TcpListener, sync::oneshot};

#[path = "../support/mod.rs"]
mod support;
use support::{completed, read_request, requests, scripted_http, scripted_stream, sse};

#[tokio::test]
async fn deltas_arrive_before_completion_across_byte_boundaries() {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let client = ResponsesClient::for_test(format!("http://{}/v1", listener.local_addr().unwrap()));
    let (seen_tx, seen_rx) = oneshot::channel();
    let (reasoning_done_tx, reasoning_done_rx) = oneshot::channel();
    let mut final_response = completed("你好 🌱");
    final_response["model"] = json!("served-model");
    final_response["output"].as_array_mut().unwrap().extend([
        json!({"id":"r", "type":"reasoning", "status":"completed", "content":[{"type":"reasoning_text","text":"checking"}], "encrypted_content":"opaque", "summary":[]}),
        json!({"id":"t", "type":"function_call", "status":"completed", "call_id":"call-1", "name":"read", "arguments":"{\"path\":\"a.txt\"}"}),
    ]);
    let server = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
        let request = read_request(&mut socket).await;
        socket.write_all(b"HTTP/1.1 200 OK\r\nContent-Type: text/event-stream; charset=utf-8\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n").await.unwrap();
        let first = format!(
            ": heartbeat\r\n\r\ndata: {{\"type\":\"response.created\"}}\r\n\r\nevent: response.output_text.delta\r\ndata: {{\"type\":\"response.output_text.delta\",\r\ndata: \"item_id\":\"message\",\"delta\":\"你好 🌱\"}}\r\n\r\n"
        );
        // Every UTF-8 byte is in a separate HTTP chunk, independent of TCP coalescing.
        for byte in first.bytes() {
            socket
                .write_all(&[b'1', b'\r', b'\n', byte, b'\r', b'\n'])
                .await
                .unwrap();
        }
        // Completion cannot be sent until the callback observes the delta.
        seen_rx.await.unwrap();
        let reasoning = sse(
            json!({"type":"response.reasoning_text.delta","item_id":"r","delta":"checking"}),
        ) + &sse(
            json!({"type":"response.output_item.done", "item":{"type":"reasoning","id":"r"}}),
        );
        socket
            .write_all(format!("{:x}\r\n{reasoning}\r\n", reasoning.len()).as_bytes())
            .await
            .unwrap();
        // Reasoning completion must reach the caller before the response ends.
        reasoning_done_rx.await.unwrap();
        let rest = sse(json!({"type":"response.completed", "response":final_response}));
        socket
            .write_all(format!("{:x}\r\n{rest}\r\n0\r\n\r\n", rest.len()).as_bytes())
            .await
            .unwrap();
        vec![request]
    });
    let mut seen_tx = Some(seen_tx);
    let mut reasoning_done_tx = Some(reasoning_done_tx);
    let mut deltas = Vec::new();
    let response = client
        .respond_stream(ModelRequest::new("test".to_owned(), &[], vec![]), |event| {
            if let Some(sender) = seen_tx.take() {
                sender.send(()).unwrap();
            }
            if matches!(&event, ModelStreamEvent::ReasoningCompleted { .. }) {
                reasoning_done_tx.take().unwrap().send(()).unwrap();
            }
            deltas.push(event);
        })
        .await
        .unwrap();
    assert_eq!(
        deltas,
        vec![
            ModelStreamEvent::TextDelta {
                item_id: "message".to_owned(),
                delta: "你好 🌱".to_owned()
            },
            ModelStreamEvent::ReasoningDelta {
                item_id: "r".to_owned(),
                delta: "checking".to_owned()
            },
            ModelStreamEvent::ReasoningCompleted {
                item_id: "r".to_owned()
            },
        ]
    );
    assert_eq!(response.model.as_deref(), Some("served-model"));
    assert_eq!(response.usage.unwrap().input_tokens, Some(6000));
    assert!(
        matches!(&response.output[1], OutputItem::Reasoning { provider_data: Some(data), .. } if data.value["encrypted_content"] == "opaque")
    );
    assert!(
        matches!(&response.output[2], OutputItem::ToolCall { call_id, arguments, .. } if call_id == "call-1" && arguments == "{\"path\":\"a.txt\"}")
    );
    assert_eq!(requests(server).await[0]["stream"], true);
}

#[tokio::test]
async fn stream_failures_never_replay_partial_output() {
    for ending in [
        String::new(),
        "data: [DONE]\n\n".to_owned(),
        "data: {invalid\n\n".to_owned(),
        sse(json!({"type":"error", "code":"server_error", "message":"backend lost"})),
        sse(
            json!({"type":"response.failed", "response":{"error":{"code":"server_error","message":"backend lost"}}}),
        ),
        sse(
            json!({"type":"response.incomplete", "response":{"incomplete_details":{"reason":"max_output_tokens"}}}),
        ),
    ] {
        let body = sse(
            json!({"type":"response.output_text.delta", "item_id":"message", "delta":"partial"}),
        ) + &ending;
        let (client, server) = scripted_stream(vec![body]).await;
        let mut deltas = Vec::new();
        assert!(
            client
                .respond_stream(ModelRequest::new("test".to_owned(), &[], vec![]), |e| {
                    deltas.push(e)
                })
                .await
                .is_err()
        );
        assert_eq!(deltas.len(), 1);
        assert_eq!(requests(server).await.len(), 1);
    }
}

#[tokio::test]
async fn streaming_reuses_http_retry_and_overflow_rules_and_accepts_json_gateways() {
    let (client, server) = scripted_http(vec![
        (429, json!({"error":{"code":"temporary"}})),
        (200, completed("fallback")),
    ])
    .await;
    let mut deltas = Vec::new();
    let response = client
        .respond_stream(ModelRequest::new("test".to_owned(), &[], vec![]), |e| {
            deltas.push(e)
        })
        .await
        .unwrap();
    assert!(deltas.is_empty());
    assert!(
        matches!(&response.output[0], OutputItem::Message { content, .. } if content[0].text == "fallback")
    );
    let sent = requests(server).await;
    assert_eq!(sent.len(), 2);
    assert_eq!(sent[0], sent[1]);
    assert_eq!(sent[0]["stream"], true);

    let (client, server) = scripted_http(vec![(400, support::overflow())]).await;
    let error = client
        .respond_stream(ModelRequest::new("test".to_owned(), &[], vec![]), |_| {})
        .await
        .unwrap_err();
    assert!(
        error
            .downcast_ref::<ApiError>()
            .unwrap()
            .is_context_overflow()
    );
    assert_eq!(requests(server).await.len(), 1);

    let (client, server) = scripted_stream(vec![sse(
        json!({"type":"error","code":"context_length_exceeded","message":"too long"}),
    )])
    .await;
    let error = client
        .respond_stream(ModelRequest::new("test".to_owned(), &[], vec![]), |_| {})
        .await
        .unwrap_err();
    assert!(
        error
            .downcast_ref::<ApiError>()
            .unwrap()
            .is_context_overflow()
    );
    assert_eq!(requests(server).await.len(), 1);
}
