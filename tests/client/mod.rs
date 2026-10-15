use super::*;
use crate::agent::conversation::Conversation;
use serde_json::json;

#[path = "../support/mod.rs"]
mod support;

use support::{completed, overflow, requests, scripted_http};

#[tokio::test]
async fn overflow_is_typed_and_is_not_retried() {
    let (client, server) = scripted_http(vec![(400, overflow())]).await;
    let conversation = Conversation::new("system".to_owned());
    let error = client
        .respond(ModelRequest::new(
            "test".to_owned(),
            conversation.items(),
            vec![],
        ))
        .await
        .unwrap_err();
    let Some(ApiError::ContextOverflow { status, body }) = error.downcast_ref::<ApiError>() else {
        panic!("expected typed context overflow, got {error:?}");
    };
    assert_eq!(*status, reqwest::StatusCode::BAD_REQUEST);
    assert_eq!(
        serde_json::from_str::<Value>(body).unwrap()["error"]["code"],
        "context_length_exceeded"
    );
    assert_eq!(requests(server).await.len(), 1);
}

#[tokio::test]
async fn other_bad_requests_do_not_trigger_retries_or_overflow_classification() {
    let body = json!({"error":{"code":"invalid_request_error", "message":"context_length_exceeded appears only in this message"}});
    let (client, server) = scripted_http(vec![(400, body)]).await;
    let conversation = Conversation::new("system".to_owned());
    let error = client
        .respond(ModelRequest::new(
            "test".to_owned(),
            conversation.items(),
            vec![],
        ))
        .await
        .unwrap_err();
    assert!(
        matches!(error.downcast_ref::<ApiError>(), Some(ApiError::Http { status, .. }) if *status == reqwest::StatusCode::BAD_REQUEST)
    );
    assert_eq!(requests(server).await.len(), 1);
}

#[tokio::test]
async fn transient_statuses_retry_the_same_request_with_a_three_attempt_limit() {
    for status in [408, 429, 503] {
        let (client, server) =
            scripted_http(vec![(status, json!({"error":{"code":"temporary"}})); 3]).await;
        let conversation = Conversation::new("system".to_owned());
        let error = client
            .respond(ModelRequest::new(
                "test".to_owned(),
                conversation.items(),
                vec![],
            ))
            .await
            .unwrap_err();
        assert!(
            matches!(error.downcast_ref::<ApiError>(), Some(ApiError::Http { status: actual, .. }) if actual.as_u16() == status)
        );
        let sent = requests(server).await;
        assert_eq!(sent.len(), 3);
        assert!(sent.iter().all(|request| request == &sent[0]));
    }
}

#[tokio::test]
async fn a_transient_status_can_recover_on_the_next_attempt() {
    let (client, server) = scripted_http(vec![
        (429, json!({"error":{"code":"rate_limit"}})),
        (200, completed("ready")),
    ])
    .await;
    let conversation = Conversation::new("system".to_owned());
    assert!(
        client
            .respond(ModelRequest::new(
                "test".to_owned(),
                conversation.items(),
                vec![]
            ))
            .await
            .is_ok()
    );
    let sent = requests(server).await;
    assert_eq!(sent.len(), 2);
    assert_eq!(sent[0], sent[1]);
}

#[tokio::test]
async fn a_successful_status_with_invalid_response_schema_is_not_retried() {
    let (client, server) = scripted_http(vec![(200, json!({"unexpected":"schema"}))]).await;
    let conversation = Conversation::new("system".to_owned());
    assert!(
        client
            .respond(ModelRequest::new(
                "test".to_owned(),
                conversation.items(),
                vec![]
            ))
            .await
            .is_err()
    );
    assert_eq!(requests(server).await.len(), 1);
}

#[tokio::test]
async fn common_protocol_is_translated_at_the_http_boundary() {
    use crate::protocol::{ToolDefinition, ToolType};
    let input = vec![
        InputItem::Message {
            role: MessageRole::User,
            content: InputMessageContent::Parts(vec![
                InputContentPart::Text {
                    text: "inspect".to_owned(),
                },
                InputContentPart::Image {
                    url: "data:image/png;base64,SNAPSHOT".to_owned(),
                },
            ]),
        },
        InputItem::ToolCall {
            call_id: "call-1".to_owned(),
            name: "read".to_owned(),
            arguments: "{invalid".to_owned(),
        },
        InputItem::ToolResult {
            call_id: "call-1".to_owned(),
            output: "{\"error\":\"invalid arguments\"}".to_owned(),
        },
    ];
    let tools = vec![ToolDefinition {
        tool_type: ToolType::Function,
        name: "read".to_owned(),
        description: "Read a file".to_owned(),
        parameters: json!({"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}),
    }];
    let request = ModelRequest::new("test".to_owned(), &input, tools).with_generation(
        Some(128),
        Some(0.1),
        Some("low".to_owned()),
    );
    let public = serde_json::to_value(&request).unwrap();
    let mut response = completed("ready");
    response["model"] = json!("served-model");
    response["output"].as_array_mut().unwrap().push(json!({"type":"function_call","id":"f","status":"completed","call_id":"call-2","name":"read","arguments":"{invalid"}));
    let (client, server) = scripted_http(vec![(200, response)]).await;
    let result = client.respond(request).await.unwrap();
    assert_eq!(result.model.as_deref(), Some("served-model"));
    assert_eq!(result.usage.unwrap().input_tokens, Some(6000));
    assert!(
        matches!(&result.output[1], OutputItem::ToolCall { call_id, arguments, .. } if call_id == "call-2" && arguments == "{invalid")
    );
    let sent = requests(server).await;
    let body = &sent[0];
    assert_eq!(public["input"][1]["type"], "tool_call");
    assert_eq!(body["input"][1]["type"], "function_call");
    assert_eq!(body["input"][1]["arguments"], "{invalid");
    assert_eq!(body["input"][2]["type"], "function_call_output");
    assert_eq!(body["input"][0]["content"][0]["type"], "input_text");
    assert_eq!(
        body["input"][0]["content"][1]["image_url"],
        "data:image/png;base64,SNAPSHOT"
    );
    assert_eq!(body["tools"][0]["type"], "function");
    assert_eq!(
        body["tools"][0]["parameters"],
        public["tools"][0]["parameters"]
    );
    assert_eq!(body["max_output_tokens"], 128);
    assert_eq!(body["reasoning"]["effort"], "low");
    assert_eq!(body["temperature"], public["temperature"]);
    assert!(body.get("reasoning_effort").is_none());
}

#[tokio::test]
async fn incompatible_continuation_data_fails_before_any_http_attempt() {
    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    listener.set_nonblocking(true).unwrap();
    let client = ResponsesClient::for_test(format!("http://{}/v1", listener.local_addr().unwrap()));
    let input = vec![InputItem::ProviderItem {
        data: ProviderData {
            api: "another-api".to_owned(),
            value: json!({"native":"payload"}),
        },
    }];
    let result = client
        .respond_once(ModelRequest::new("test".to_owned(), &input, vec![]))
        .await;
    assert_eq!(result.attempts, 0);
    assert!(
        result
            .result
            .unwrap_err()
            .to_string()
            .contains("another-api")
    );
    assert!(
        client
            .respond(ModelRequest::new("test".to_owned(), &input, vec![]))
            .await
            .unwrap_err()
            .to_string()
            .contains("another-api")
    );
    assert_eq!(
        listener.accept().unwrap_err().kind(),
        std::io::ErrorKind::WouldBlock
    );
}
