use super::*;
use crate::client::ResponsesClient;
use serde_json::{Value, json};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpListener,
    task::JoinHandle,
};

fn config() -> CompactionConfig {
    CompactionConfig {
        context_budget_tokens: 8192,
        max_input_tokens: None,
        reserve_tokens: 2048,
        keep_recent_tokens: 256,
        max_summary_tokens: 1024,
    }
}

fn long_history() -> Conversation {
    let mut conversation = Conversation::new("System instructions must survive.".to_owned())
        .with_compaction_config(config())
        .unwrap();
    conversation.add_user_message(format!(
        "original-source: {}",
        "verified context ".repeat(1100)
    ));
    conversation.add_assistant_message("Completed initial work.".to_owned());
    conversation.add_user_message("Continue the unfinished goal.".to_owned());
    assert!(conversation.estimated_context_tokens("test", &[]).unwrap() >= config().input_limit());
    conversation
}

fn completed(summary: &str) -> Value {
    json!({"id":"summary-response", "status":"completed", "output":[{
        "type":"message", "id":"summary-message", "status":"completed", "role":"assistant",
        "content":[{"type":"output_text", "text":summary}]
    }], "usage":{"input_tokens":7000,"output_tokens":40}})
}

async fn mock_response(status: u16, response: Value) -> (ModelClient, JoinHandle<Value>) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let task = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
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
        let headers = std::str::from_utf8(&data[..header_end]).unwrap();
        let length: usize = headers
            .lines()
            .find_map(|line| {
                let (name, value) = line.split_once(':')?;
                name.eq_ignore_ascii_case("content-length")
                    .then(|| value.trim().parse().unwrap())
            })
            .unwrap_or(0);
        while data.len() < header_end + length {
            let mut buffer = [0; 4096];
            let n = socket.read(&mut buffer).await.unwrap();
            assert!(n > 0);
            data.extend_from_slice(&buffer[..n]);
        }
        let request = if length == 0 {
            json!({"request_line": std::str::from_utf8(&data[..header_end]).unwrap().lines().next().unwrap()})
        } else {
            serde_json::from_slice(&data[header_end..header_end + length]).unwrap()
        };
        let body = serde_json::to_vec(&response).unwrap();
        socket.write_all(format!("HTTP/1.1 {status} Test\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n", body.len()).as_bytes()).await.unwrap();
        socket.write_all(&body).await.unwrap();
        request
    });
    (ModelClient::for_test(format!("http://{address}/v1")), task)
}

fn snapshot(conversation: &Conversation) -> Value {
    serde_json::to_value(conversation).unwrap()
}

fn temp_path() -> std::path::PathBuf {
    static NEXT_ID: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
    std::env::temp_dir().join(format!(
        "saki-compact-{}-{}.jsonl",
        std::process::id(),
        NEXT_ID.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
    ))
}

#[tokio::test]
async fn small_context_does_not_contact_the_model() {
    let mut conversation = Conversation::new("system".to_owned())
        .with_compaction_config(config())
        .unwrap();
    conversation.add_user_message("hello".to_owned());
    let original = snapshot(&conversation);
    let client = ModelClient::for_test("http://127.0.0.1:1/v1".to_owned());
    assert!(
        conversation
            .compact_if_needed(&client, "test", &[])
            .await
            .unwrap()
            .is_none()
    );
    assert_eq!(snapshot(&conversation), original);
}

#[tokio::test]
async fn successful_compaction_preserves_system_and_native_tail() {
    let mut conversation = long_history();
    let before = snapshot(&conversation);
    let cut = conversation
        .compaction_cut_point(config())
        .unwrap()
        .unwrap();
    let (client, server) = mock_response(
        200,
        completed("Goal: continue initial work; verified earlier results."),
    )
    .await;
    let result = conversation
        .compact_if_needed(&client, "test", &[])
        .await
        .unwrap()
        .unwrap();
    let request = server.await.unwrap();
    assert!(request.get("tools").is_none());
    assert_eq!(request["max_output_tokens"], config().max_summary_tokens);
    let source: Value =
        serde_json::from_str(request["input"][1]["content"].as_str().unwrap()).unwrap();
    assert_eq!(source, json!(&before.as_array().unwrap()[1..cut]));
    let after = snapshot(&conversation);
    assert_eq!(after[0], before[0]);
    assert_eq!(
        &after.as_array().unwrap()[2..],
        &before.as_array().unwrap()[cut..]
    );
    assert!(result.tokens_after < result.tokens_before);
    assert!(conversation.usage_baseline.is_none());
    assert_eq!(result.usage.unwrap().input_tokens, Some(7000));
    assert!(
        conversation
            .compact_if_needed(&client, "test", &[])
            .await
            .unwrap()
            .is_none()
    );
}

#[tokio::test]
async fn forced_compaction_uses_the_same_size_measure_before_and_after_replacement() {
    let mut conversation = long_history();
    conversation
        .record_usage(
            "test",
            &[],
            Some(&Usage {
                input_tokens: Some(1),
                ..Usage::default()
            }),
        )
        .unwrap();
    let original_bytes = serde_json::to_vec(&conversation).unwrap().len();
    let (client, server) = mock_response(200, completed("Continue the original goal.")).await;
    assert!(
        conversation
            .compact_if_needed(&client, "test", &[])
            .await
            .unwrap()
            .is_none()
    );
    let result = conversation.compact(&client, "test", &[]).await.unwrap();
    server.await.unwrap();
    assert_eq!(result.tokens_before, 1);
    assert!(result.tokens_after > result.tokens_before);
    assert!(serde_json::to_vec(&conversation).unwrap().len() < original_bytes);
}

#[tokio::test]
async fn parallel_calls_and_reasoning_are_kept_together() {
    let mut conversation = long_history();
    conversation.add_user_message("long ongoing task ".repeat(55));
    conversation.add_reasoning(vec![ReasoningText::new("Use two tools.")], None);
    conversation.add_tool_call("a".to_owned(), "tool".to_owned(), "{}".to_owned());
    conversation.add_tool_call("b".to_owned(), "tool".to_owned(), "{}".to_owned());
    conversation.add_tool_result("b".to_owned(), "second".to_owned());
    conversation.add_tool_result("a".to_owned(), "first".to_owned());
    let cut = conversation
        .compaction_cut_point(config())
        .unwrap()
        .unwrap();
    assert!(matches!(
        conversation.items[cut],
        InputItem::Message {
            role: MessageRole::User,
            ..
        }
    ));
    let tail = snapshot(&conversation).as_array().unwrap()[cut..].to_vec();
    let (client, server) = mock_response(
        200,
        completed("The ongoing task still needs two tool results reviewed."),
    )
    .await;
    conversation
        .compact_if_needed(&client, "test", &[])
        .await
        .unwrap();
    server.await.unwrap();
    assert_eq!(&snapshot(&conversation).as_array().unwrap()[2..], tail);
}

#[tokio::test]
async fn pending_and_orphaned_tools_cannot_be_compacted() {
    let client = ModelClient::for_test("http://127.0.0.1:1/v1".to_owned());
    for orphan in [false, true] {
        let mut conversation = long_history();
        if orphan {
            conversation.add_tool_result("missing".to_owned(), "output".to_owned());
        } else {
            conversation.add_tool_call("pending".to_owned(), "tool".to_owned(), "{}".to_owned());
        }
        let before = snapshot(&conversation);
        let error = conversation
            .compact_if_needed(&client, "test", &[])
            .await
            .err()
            .unwrap();
        assert!(error.to_string().contains(if orphan {
            "without a matching"
        } else {
            "pending tool"
        }));
        assert_eq!(snapshot(&conversation), before);
    }
}

#[tokio::test]
async fn failed_incomplete_empty_and_oversized_summaries_preserve_context() {
    let mut incomplete = completed("partial");
    incomplete["status"] = json!("incomplete");
    incomplete["incomplete_details"] = json!({"reason":"max_output_tokens"});
    let mut incomplete_message = completed("partial message");
    incomplete_message["output"][0]["status"] = json!("incomplete");
    let unexpected = json!({"id":"response", "status":"completed", "output":[{
        "type":"function_call","id":"tool","status":"completed","call_id":"call","name":"tool","arguments":"{}"
    }]});
    for (status, response) in [
        (500, json!({"error":"failed"})),
        (200, incomplete),
        (200, incomplete_message),
        (200, completed("   ")),
        (200, unexpected),
        (200, completed(&"larger summary ".repeat(2000))),
    ] {
        let mut conversation = long_history();
        let before = snapshot(&conversation);
        let (client, server) = mock_response(status, response).await;
        assert!(
            conversation
                .compact_if_needed(&client, "test", &[])
                .await
                .is_err()
        );
        server.await.unwrap();
        assert_eq!(snapshot(&conversation), before);
        assert!(!conversation.has_summary);
    }
}

#[test]
fn usage_calibrates_input_without_double_counting_cached_or_output_tokens() {
    let mut conversation = Conversation::new("system".to_owned());
    conversation.add_user_message("goal".to_owned());
    let usage = Usage {
        input_tokens: Some(500),
        output_tokens: Some(2000),
        input_tokens_details: Some(json!({"cached_tokens":400})),
        output_tokens_details: None,
    };
    let before = conversation.request_tokens("test", &[]).unwrap();
    conversation
        .record_usage("test", &[], Some(&usage))
        .unwrap();
    assert_eq!(
        conversation.estimated_context_tokens("test", &[]).unwrap(),
        2500
    );
    conversation.add_assistant_message("new output ".repeat(90));
    let after = conversation.request_tokens("test", &[]).unwrap();
    assert_eq!(
        conversation.estimated_context_tokens("test", &[]).unwrap(),
        2500 + after - before
    );
    conversation
        .record_usage("test", &[], Some(&Usage::default()))
        .unwrap();
    assert_eq!(
        conversation.estimated_context_tokens("test", &[]).unwrap(),
        2500 + after - before
    );
    assert_eq!(
        conversation
            .estimated_context_tokens("different-model", &[])
            .unwrap(),
        conversation.request_tokens("different-model", &[]).unwrap()
    );
}

#[tokio::test]
async fn oversized_single_block_is_reported_without_dropping_it() {
    let mut conversation = Conversation::new("system".to_owned())
        .with_compaction_config(config())
        .unwrap();
    conversation.add_user_message("huge current request ".repeat(1200));
    let before = snapshot(&conversation);
    let client = ModelClient::for_test("http://127.0.0.1:1/v1".to_owned());
    assert!(
        conversation
            .compact_if_needed(&client, "test", &[])
            .await
            .err()
            .unwrap()
            .to_string()
            .contains("no complete history prefix")
    );
    assert_eq!(snapshot(&conversation), before);
}

#[tokio::test]
async fn oversized_tool_definitions_cannot_be_fixed_by_deleting_history() {
    let mut conversation = long_history();
    let tools = vec![ToolDefinition {
        tool_type: crate::protocol::ToolType::Function,
        name: "tool".to_owned(),
        description: "large schema ".repeat(2000),
        parameters: json!({"type":"object"}),
    }];
    assert!(
        conversation.request_tokens("test", &tools).unwrap()
            > conversation.request_tokens("test", &[]).unwrap()
    );
    let before = snapshot(&conversation);
    let (client, server) = mock_response(200, completed("small summary")).await;
    assert!(
        conversation
            .compact_if_needed(&client, "test", &tools)
            .await
            .err()
            .unwrap()
            .to_string()
            .contains("still exceed")
    );
    server.await.unwrap();
    assert_eq!(snapshot(&conversation), before);
}

#[tokio::test]
async fn repeated_compaction_journal_replays_to_the_active_context() {
    let path = temp_path();
    let mut conversation = long_history().with_history_file(&path).unwrap();
    let (client, server) = mock_response(
        200,
        completed("First summary: preserve original-source goal."),
    )
    .await;
    conversation
        .compact_if_needed(&client, "test", &[])
        .await
        .unwrap();
    server.await.unwrap();
    conversation.add_assistant_message("second-source: ".repeat(1300));
    conversation.add_user_message("continue second goal".to_owned());
    let (client, server) =
        mock_response(200, completed("Second summary includes both goals.")).await;
    conversation
        .compact_if_needed(&client, "test", &[])
        .await
        .unwrap();
    let request = server.await.unwrap();
    let source: Value =
        serde_json::from_str(request["input"][1]["content"].as_str().unwrap()).unwrap();
    assert_eq!(
        source
            .as_array()
            .unwrap()
            .iter()
            .filter(|item| item["content"]
                .as_str()
                .is_some_and(|text| text.contains("First summary:")))
            .count(),
        1
    );
    let journal = std::fs::read_to_string(&path).unwrap();
    assert!(journal.contains("original-source:"));
    assert!(journal.contains("second-source:"));
    let mut replay = Vec::new();
    let mut checkpoints = 0;
    for line in journal.lines() {
        let record: Value = serde_json::from_str(line).unwrap();
        assert_eq!(record["schema_version"], 1);
        match record["type"].as_str().unwrap() {
            "messages" => replay
                .extend(serde_json::from_value::<Vec<InputItem>>(record["items"].clone()).unwrap()),
            "compaction" => {
                let cut = record["keep_from"].as_u64().unwrap() as usize;
                let mut next = vec![
                    replay[0].clone(),
                    summary_item(record["summary"].as_str().unwrap()),
                ];
                next.extend_from_slice(&replay[cut..]);
                replay = next;
                checkpoints += 1;
            }
            _ => unreachable!(),
        }
    }
    assert_eq!(checkpoints, 2);
    let expected = snapshot(&conversation);
    assert_eq!(json!(replay), expected);
    drop(conversation);
    let (mut restored, history) = Conversation::restore(&path).unwrap();
    assert_eq!(snapshot(&restored), expected);
    assert!(history.has_summary);
    assert_eq!(
        history
            .history
            .iter()
            .filter(|record| matches!(record, JournalRecord::Compaction { .. }))
            .count(),
        2
    );
    assert!(
        serde_json::to_string(&history.history)
            .unwrap()
            .contains("original-source:")
    );
    restored.add_user_message("after process restart");
    restored.save_history().unwrap();
    drop(restored);
    let (restored, _) = Conversation::restore(&path).unwrap();
    assert_eq!(restored.items().len(), replay.len() + 1);
    drop(restored);
    std::fs::remove_file(path).unwrap();
}

#[tokio::test]
async fn checkpoint_write_failure_does_not_install_the_summary() {
    let path = temp_path();
    std::fs::write(&path, "").unwrap();
    let mut conversation = long_history();
    conversation.history_file = Some(File::open(&path).unwrap());
    conversation.persisted_items = conversation.items.len();
    let before = snapshot(&conversation);
    let (client, server) = mock_response(200, completed("Valid summary")).await;
    assert!(
        conversation
            .compact_if_needed(&client, "test", &[])
            .await
            .is_err()
    );
    server.await.unwrap();
    assert_eq!(snapshot(&conversation), before);
    drop(conversation);
    std::fs::remove_file(path).unwrap();
}

#[tokio::test]
async fn model_input_limit_triggers_before_the_larger_total_window() {
    let mut config = config();
    config.context_budget_tokens = 16_384;
    config.max_input_tokens = Some(8192);
    let mut conversation = long_history().with_compaction_config(config).unwrap();
    conversation
        .record_usage(
            "test",
            &[],
            Some(&Usage {
                input_tokens: Some(8192),
                ..Usage::default()
            }),
        )
        .unwrap();
    let (client, server) =
        mock_response(200, completed("Preserve the goal and verified results.")).await;
    let result = conversation
        .compact_if_needed(&client, "test", &[])
        .await
        .unwrap()
        .unwrap();
    server.await.unwrap();
    assert_eq!(result.tokens_before, 8192);
    assert!(result.tokens_before < config.context_budget_tokens - config.reserve_tokens as usize);
    assert!(result.tokens_after < 8192);
}

#[test]
fn large_base64_is_not_counted_as_text_tokens() {
    let image = ImageAttachment::new(
        "big.png",
        format!("data:image/png;base64,{}", "A".repeat(3_000_000)),
    );
    let mut conversation = Conversation::new("system".to_owned());
    conversation.add_user_message(UserMessage {
        text: "hi".to_owned(),
        images: vec![image],
    });
    let tokens = estimated_tokens(&conversation.items[1..2]).unwrap();
    assert!(tokens >= IMAGE_TOKEN_ESTIMATE);
    assert!(tokens < 5000, "base64 counted as text tokens: {tokens}");
}

#[tokio::test]
async fn compaction_attaches_prefix_images_as_input_image_parts() {
    let mut conversation = Conversation::new("system".to_owned())
        .with_compaction_config(config())
        .unwrap();
    conversation.add_user_message(UserMessage {
        text: "look at this".to_owned(),
        images: vec![ImageAttachment::new(
            "old.png",
            "data:image/png;base64,OLDPAYLOAD",
        )],
    });
    conversation.add_assistant_message("saw it".to_owned());
    conversation.add_user_message("recent goal ".repeat(200));
    conversation.add_assistant_message("recent done".to_owned());

    let (client, request) = mock_response(200, completed("summary text")).await;
    conversation.compact(&client, "test", &[]).await.unwrap();
    let request = request.await.unwrap();
    let content = request["input"][1]["content"].as_array().unwrap();
    assert_eq!(content[0]["type"], "input_text");
    let transcript = content[0]["text"].as_str().unwrap();
    assert!(transcript.contains("[image #1]"), "{transcript}");
    assert!(
        !transcript.contains("OLDPAYLOAD"),
        "base64 must not stay as text: {transcript}"
    );
    let image = content
        .iter()
        .find(|part| part["type"] == "input_image")
        .expect("summary user content carries the real image");
    assert_eq!(image["image_url"], "data:image/png;base64,OLDPAYLOAD");
}

#[tokio::test]
async fn compaction_keeps_the_latest_turn_image() {
    let mut conversation = Conversation::new("system".to_owned())
        .with_compaction_config(config())
        .unwrap();
    conversation.add_user_message("old context ".repeat(1100));
    conversation.add_assistant_message("done".to_owned());
    conversation.add_user_message(UserMessage {
        text: "latest".to_owned(),
        images: vec![ImageAttachment::new("new.png", "data:image/png;base64,NEW")],
    });

    let (client, _request) = mock_response(200, completed("summary")).await;
    conversation.compact(&client, "test", &[]).await.unwrap();
    let snapshot = serde_json::to_value(&conversation).unwrap();
    let retained = snapshot.as_array().unwrap().iter().any(|item| {
        item["content"].as_array().is_some_and(|parts| {
            parts
                .iter()
                .any(|part| part["type"] == "image" && part["url"] == "data:image/png;base64,NEW")
        })
    });
    assert!(retained, "the newest turn's image must survive compaction");
    let body = ResponsesClient::request_body(&ModelRequest::new(
        "test".to_owned(),
        conversation.items(),
        vec![],
    ))
    .unwrap();
    assert_eq!(
        body["input"][2]["content"][1]["image_url"],
        "data:image/png;base64,NEW"
    );
}

#[test]
fn journal_roundtrip_preserves_images_tools_and_native_reasoning() {
    let path = temp_path();
    let mut conversation = Conversation::new("system".to_owned())
        .with_history_file(&path)
        .unwrap();
    conversation.add_user_message(UserMessage {
        text: "inspect".to_owned(),
        images: vec![ImageAttachment::new(
            "fixture.png",
            "data:image/png;base64,SNAPSHOT",
        )],
    });
    conversation.add_reasoning(vec![ReasoningText::new("inspect first")], Some(ProviderData {
        api: "openai-responses".to_owned(),
        value: json!({"type":"reasoning","encrypted_content":"opaque","content":[{"type":"reasoning_text","text":"inspect first"}]}),
    }));
    conversation.add_tool_call(
        "call-1".to_owned(),
        "read".to_owned(),
        "{invalid".to_owned(),
    );
    conversation.add_tool_result(
        "call-1".to_owned(),
        "{\"error\":\"invalid arguments\"}".to_owned(),
    );
    conversation.save_history().unwrap();
    let records: Vec<Value> = std::fs::read_to_string(&path)
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect();
    let mut restored = Vec::new();
    for record in records {
        assert_eq!(record["schema_version"], 1);
        restored.extend(serde_json::from_value::<Vec<InputItem>>(record["items"].clone()).unwrap());
    }
    assert_eq!(
        serde_json::to_value(&restored).unwrap(),
        snapshot(&conversation)
    );
    let original = ResponsesClient::request_body(&ModelRequest::new(
        "test".to_owned(),
        conversation.items(),
        vec![],
    ))
    .unwrap();
    let replay =
        ResponsesClient::request_body(&ModelRequest::new("test".to_owned(), &restored, vec![]))
            .unwrap();
    assert_eq!(replay, original);
    drop(conversation);
    let (restored_conversation, _) = Conversation::restore(&path).unwrap();
    let restored_body = ResponsesClient::request_body(&ModelRequest::new(
        "test".to_owned(),
        restored_conversation.items(),
        vec![],
    ))
    .unwrap();
    assert_eq!(restored_body, original);
    assert_eq!(
        replay["input"][1]["content"][1]["image_url"],
        "data:image/png;base64,SNAPSHOT"
    );
    assert_eq!(replay["input"][2]["encrypted_content"], "opaque");
    assert_eq!(replay["input"][3]["arguments"], "{invalid");
    assert_eq!(replay["input"][4]["call_id"], "call-1");
    drop(restored_conversation);
    std::fs::remove_file(path).unwrap();
}

#[test]
fn restore_waits_out_a_briefly_held_journal_lock() {
    let path = temp_path();
    let conversation = Conversation::new("system".to_owned())
        .with_history_file(&path)
        .unwrap();
    let holder = std::thread::spawn(move || {
        std::thread::sleep(std::time::Duration::from_millis(20));
        drop(conversation);
    });
    let (restored, _) = Conversation::restore(&path).unwrap();
    holder.join().unwrap();
    drop(restored);
    std::fs::remove_file(path).unwrap();
}

#[test]
fn restore_reports_a_lock_that_is_still_held() {
    let path = temp_path();
    let holder = Conversation::new("system".to_owned())
        .with_history_file(&path)
        .unwrap();
    let error = match Conversation::restore(&path) {
        Ok(_) => panic!("restore must not succeed while the journal is locked"),
        Err(error) => error.to_string(),
    };
    assert!(error.contains("session is already in use"), "{error}");
    assert!(error.contains(&path.display().to_string()), "{error}");
    drop(holder);
    std::fs::remove_file(path).unwrap();
}

#[tokio::test]
async fn compaction_request_hook_fires_only_for_a_real_summary_request() {
    use std::sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    };

    let mut small = Conversation::new("system".to_owned())
        .with_compaction_config(config())
        .unwrap();
    small.add_user_message("hello".to_owned());
    let small_calls = Arc::new(AtomicUsize::new(0));
    let small_counter = small_calls.clone();
    small.set_model_request_hook(Arc::new(move || {
        small_counter.fetch_add(1, Ordering::SeqCst);
        Box::pin(std::future::ready(()))
    }));
    let unreachable = ModelClient::for_test("http://127.0.0.1:1/v1".to_owned());
    assert!(
        small
            .compact_if_needed(&unreachable, "test", &[])
            .await
            .unwrap()
            .is_none()
    );
    assert_eq!(
        small_calls.load(Ordering::SeqCst),
        0,
        "a no-op compact_if_needed must not announce a model request"
    );

    let mut long = long_history();
    let long_calls = Arc::new(AtomicUsize::new(0));
    let long_counter = long_calls.clone();
    long.set_model_request_hook(Arc::new(move || {
        long_counter.fetch_add(1, Ordering::SeqCst);
        Box::pin(std::future::ready(()))
    }));
    let (client, server) = mock_response(
        200,
        completed("The ongoing task still needs two tool results reviewed."),
    )
    .await;
    long.compact_if_needed(&client, "test", &[]).await.unwrap();
    server.await.unwrap();
    assert_eq!(
        long_calls.load(Ordering::SeqCst),
        1,
        "a real summary request must emit exactly one hook call"
    );
}

#[test]
fn old_task_records_without_message_id_remain_readable_with_timestamps() {
    let path = temp_path();
    let contents = concat!(
        "{\"schema_version\":1,\"timestamp_ms\":11,\"type\":\"messages\",\"items\":[{\"type\":\"message\",\"role\":\"system\",\"content\":\"sys\"}]}\n",
        "{\"schema_version\":1,\"type\":\"task_started\",\"task_id\":\"old-task\"}\n",
        "{\"schema_version\":1,\"timestamp_ms\":13,\"type\":\"task_finished\",\"task_id\":\"old-task\",\"status\":\"completed\",\"error\":null}\n",
    );
    std::fs::write(&path, contents).unwrap();
    let file = std::fs::File::open(&path).unwrap();
    let replay = journal::read(&file, &path).unwrap();
    assert_eq!(replay.history.len(), 3);
    assert_eq!(replay.history_timestamps, vec![11, 0, 13]);
    match &replay.history[1] {
        JournalRecord::TaskStarted {
            task_id,
            message_id,
        } => {
            assert_eq!(task_id, "old-task");
            assert_eq!(*message_id, None);
        }
        other => panic!("expected TaskStarted, got {other:?}"),
    }
    std::fs::remove_file(path).unwrap();
}

#[tokio::test]
async fn cancelling_while_compaction_hook_is_backpressured_preserves_context() {
    use std::{
        future::Future,
        sync::{
            Arc,
            atomic::{AtomicBool, Ordering},
        },
        task::Poll,
    };
    let mut conversation = long_history();
    let before = conversation.items().to_vec();
    let (sender, mut receiver) = tokio::sync::mpsc::channel(1);
    sender.send(()).await.unwrap();
    let entered = Arc::new(AtomicBool::new(false));
    let hook_entered = entered.clone();
    conversation.set_model_request_hook(Arc::new(move || {
        let sender = sender.clone();
        let entered = hook_entered.clone();
        Box::pin(async move {
            entered.store(true, Ordering::SeqCst);
            sender.send(()).await.unwrap();
        })
    }));
    let client = ModelClient::for_test("http://127.0.0.1:1/v1".to_owned());
    let mut compact = Box::pin(conversation.compact_if_needed(&client, "test", &[]));
    std::future::poll_fn(|cx| {
        assert!(compact.as_mut().poll(cx).is_pending());
        Poll::Ready(())
    })
    .await;
    assert!(
        entered.load(Ordering::SeqCst),
        "compaction must await the hook inline"
    );
    drop(compact);
    assert_eq!(
        serde_json::to_value(conversation.items()).unwrap(),
        serde_json::to_value(before).unwrap()
    );
    assert_eq!(receiver.try_recv(), Ok(()));
    assert!(receiver.try_recv().is_err());
}
