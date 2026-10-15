use super::*;
use std::sync::{
    Arc,
    atomic::{AtomicUsize, Ordering},
};

use crate::{
    agent::conversation::CompactionConfig,
    client::responses::ApiError,
    protocol::{ToolDefinition, ToolType},
    tools::tool_registry::Tool,
};
use serde_json::{Value, json};

#[path = "../support/mod.rs"]
mod support;

mod cancellation;

use support::{Journal, completed, overflow, requests, scripted_http};

fn conversation() -> Conversation {
    Conversation::new("Preserve project ion-42.".to_owned())
        .with_compaction_config(CompactionConfig {
            context_budget_tokens: 65_536,
            max_input_tokens: None,
            reserve_tokens: 2048,
            keep_recent_tokens: 256,
            max_summary_tokens: 1024,
        })
        .unwrap()
}

fn previous_history() -> Conversation {
    let mut conversation = conversation();
    conversation.add_user_message(format!(
        "Initial project context: {}",
        "verified old context ".repeat(600)
    ));
    conversation.add_assistant_message("Completed the initial work.".to_owned());
    conversation
}

fn has_checkpoint(records: &[Value]) -> bool {
    records.iter().any(|record| record["type"] == "compaction")
}

fn assert_overflow(error: &anyhow::Error) {
    assert!(
        matches!(
            error.downcast_ref::<ApiError>(),
            Some(ApiError::ContextOverflow { .. })
        ),
        "expected typed overflow, got {error:?}"
    );
}

fn event_channels() -> (
    tokio::sync::mpsc::UnboundedSender<AgentEvent>,
    tokio::sync::mpsc::UnboundedReceiver<AgentEvent>,
) {
    tokio::sync::mpsc::unbounded_channel()
}

fn drain_events(
    receiver: &mut tokio::sync::mpsc::UnboundedReceiver<AgentEvent>,
) -> Vec<AgentEvent> {
    let mut events = Vec::new();
    while let Ok(event) = receiver.try_recv() {
        events.push(event);
    }
    events
}

fn is_terminal(event: &AgentEvent) -> bool {
    matches!(
        event,
        AgentEvent::TurnCompleted | AgentEvent::TurnFailed { .. }
    )
}

#[tokio::test]
async fn streamed_tool_calls_execute_once_after_completion_and_continue_with_full_context() {
    let executions = Arc::new(AtomicUsize::new(0));
    let counter = executions.clone();
    let mut tools = ToolRegistry::new();
    tools.registry(Tool {
        definition: ToolDefinition {
            tool_type: ToolType::Function,
            name: "lookup".into(),
            description: "Read fixture".into(),
            parameters: json!({"type":"object","properties":{}}),
        },
        handler: Arc::new(move |_| {
            counter.fetch_add(1, Ordering::SeqCst);
            Box::pin(async { Ok(json!({"result":37})) })
        }),
    });
    let mut first = completed("Checking.");
    first["output"].as_array_mut().unwrap().extend([
        json!({"type":"reasoning","id":"r","status":"completed","content":[],"encrypted_content":"opaque"}),
        json!({"type":"function_call","id":"f","status":"completed","call_id":"c","name":"lookup","arguments":"{}"}),
    ]);
    let (client, server) = support::scripted_stream(vec![
        support::sse(json!({"type":"response.reasoning_text.delta","item_id":"r","delta":"Inspect fixture."}))
        + &support::sse(json!({"type":"response.output_item.done","item":{"type":"reasoning","id":"r"}}))
        + &support::sse(
            json!({"type":"response.output_text.delta","item_id":"message","delta":"Checking."}),
        ) + &support::sse(
            json!({"type":"response.function_call_arguments.delta","item_id":"f","delta":"{"}),
        ) + &support::sse(
            json!({"type":"response.function_call_arguments.delta","item_id":"f","delta":"}"}),
        ) + &support::sse(json!({"type":"response.completed","response":first})),
        support::sse(
            json!({"type":"response.output_text.delta","item_id":"message","delta":"Result: 37"}),
        ) + &support::sse(json!({"type":"response.completed","response":completed("Result: 37")})),
    ])
    .await;
    let journal = Journal::new();
    let (sender, mut receiver) = event_channels();
    let mut agent = Agent::new(
        client,
        tools,
        conversation().with_history_file(&journal.0).unwrap(),
    )
    .with_events(sender);
    agent.handle_user_message("Look it up").await.unwrap();
    let events = drain_events(&mut receiver);
    let delta = events
        .iter()
        .position(|e| matches!(e, AgentEvent::ResponseTextDelta { .. }))
        .unwrap();
    let final_text = events
        .iter()
        .position(|e| matches!(e, AgentEvent::ResponseText { .. }))
        .unwrap();
    let started = events
        .iter()
        .position(|e| matches!(e, AgentEvent::ToolStarted { .. }))
        .unwrap();
    assert!(delta < final_text && final_text < started);
    let reasoning_done = events
        .iter()
        .position(
            |event| matches!(event, AgentEvent::ReasoningCompleted { item_id } if item_id == "r"),
        )
        .unwrap();
    assert!(
        reasoning_done < delta,
        "reasoning end must precede answer generation, not wait for the full response"
    );
    assert_eq!(executions.load(Ordering::SeqCst), 1);
    let sent = requests(server).await;
    let input = sent[1]["input"].as_array().unwrap();
    assert_eq!(
        input
            .iter()
            .filter(|item| item["content"] == "Checking.")
            .count(),
        1
    );
    assert!(
        input
            .iter()
            .any(|item| item["encrypted_content"] == "opaque")
    );
    assert!(
        input
            .iter()
            .any(|item| item["type"] == "function_call" && item["arguments"] == "{}")
    );
    assert!(
        input
            .iter()
            .any(|item| item["type"] == "function_call_output" && item["call_id"] == "c")
    );
    let logged: Vec<_> = journal
        .records()
        .into_iter()
        .flat_map(|r| r["items"].as_array().cloned().unwrap_or_default())
        .collect();
    assert_eq!(
        logged
            .iter()
            .filter(|item| item["content"] == "Checking.")
            .count(),
        1
    );
}

#[tokio::test]
async fn a_failed_stream_keeps_partial_display_out_of_context_and_accepts_the_next_turn() {
    for ending in [
        String::new(),
        support::sse(json!({"type":"error","code":"context_length_exceeded","message":"too long"})),
    ] {
        let (client, server) = support::scripted_stream(vec![
            support::sse(json!({"type":"response.output_text.delta","item_id":"message","delta":"UNCOMMITTED"}))
                + &support::sse(json!({"type":"response.function_call_arguments.delta","item_id":"f","delta":"{"}))
                + &support::sse(json!({"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"f","status":"completed","call_id":"c","name":"lookup","arguments":"{}"}}))
                + &ending,
            support::sse(json!({"type":"response.completed","response":completed("Recovered")})),
        ]).await;
        let (events_tx, mut events_rx) = event_channels();
        let agent =
            Agent::new(client, ToolRegistry::new(), previous_history()).with_events(events_tx);
        let (input_tx, input_rx) = tokio::sync::mpsc::unbounded_channel();
        input_tx.send("First".into()).unwrap();
        input_tx.send("Second".into()).unwrap();
        drop(input_tx);
        run_agent(agent, input_rx).await;
        let events = drain_events(&mut events_rx);
        assert!(events.iter().any(
            |e| matches!(e, AgentEvent::ResponseTextDelta { delta, .. } if delta == "UNCOMMITTED")
        ));
        assert_eq!(
            events
                .iter()
                .filter(|e| matches!(e, AgentEvent::TurnFailed { .. }))
                .count(),
            1
        );
        assert_eq!(
            events
                .iter()
                .filter(|e| matches!(e, AgentEvent::TurnCompleted))
                .count(),
            1
        );
        assert!(!events.iter().any(|e| matches!(
            e,
            AgentEvent::ToolStarted { .. } | AgentEvent::Compaction { .. }
        )));
        let sent = requests(server).await;
        assert_eq!(
            sent.len(),
            2,
            "partial overflow must not trigger compact/replay"
        );
        assert!(!sent[1]["input"].to_string().contains("UNCOMMITTED"));
    }
}

#[tokio::test]
async fn in_band_overflow_before_output_compacts_once_and_retries_the_stream() {
    let (client, server) = support::scripted_bodies(vec![
        (200, "text/event-stream", support::sse(json!({"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"too long"}}})).into_bytes()),
        (200, "application/json", serde_json::to_vec(&completed("Preserve project ion-42")).unwrap()),
        (200, "text/event-stream", support::sse(json!({"type":"response.completed","response":completed("Recovered")})).into_bytes()),
    ]).await;
    let mut agent = Agent::new(client, ToolRegistry::new(), previous_history());
    agent.handle_user_message("Continue").await.unwrap();
    let sent = requests(server).await;
    assert_eq!(sent.len(), 3);
    assert_eq!(sent[0]["stream"], true);
    assert!(sent[1].get("stream").is_none());
    assert_eq!(sent[2]["stream"], true);
    assert_ne!(sent[0]["input"], sent[2]["input"]);
}

#[cfg(unix)]
#[tokio::test]
async fn builtin_tools_complete_a_turn_and_recover_from_edit_validation_failure() {
    let journal = Journal::new();
    let workspace = journal.0.with_extension("workspace");
    std::fs::create_dir_all(&workspace).unwrap();
    std::fs::write(workspace.join("source.txt"), "old\n").unwrap();
    let mut tools = ToolRegistry::new();
    crate::tools::register_builtins(
        &mut tools,
        crate::tools::ToolContext {
            cwd: workspace.clone(),
            output_dir: workspace.join("output"),
        },
    );
    let calls = [
        ("read", json!({"path":"source.txt"})),
        ("bash", json!({"command":"printf FAILED >&2; exit 7"})),
        (
            "edit",
            json!({"path":"source.txt","edits":[{"oldText":"old","newText":"bad"},{"oldText":"missing","newText":"x"}]}),
        ),
        (
            "edit",
            json!({"path":"source.txt","edits":[{"oldText":"old","newText":"new"}]}),
        ),
        (
            "write",
            json!({"path":"nested/report.txt","content":"complete\n"}),
        ),
        (
            "bash",
            json!({"command":"test \"$(cat source.txt)\" = new && test -f nested/report.txt"}),
        ),
    ];
    let mut responses = calls.iter().enumerate().map(|(i, (name,args))| {
        (200, json!({"id":format!("r{i}"),"status":"completed","output":[{
            "type":"function_call","id":format!("f{i}"),"status":"completed", "call_id":format!("c{i}"),
            "name":name,"arguments":args.to_string()}],"usage":{"input_tokens":6000,"output_tokens":100}}))
    }).collect::<Vec<_>>();
    responses.push((200, completed("Task verified.")));
    let (client, server) = scripted_http(responses).await;
    let mut agent = Agent::new(
        client,
        tools,
        conversation().with_history_file(&journal.0).unwrap(),
    )
    .with_model("test".to_owned());
    agent
        .handle_user_message("Read, modify, and verify the fixture.".to_owned())
        .await
        .unwrap();
    let sent = requests(server).await;
    assert_eq!(sent.len(), 7);
    let result = |request: usize| -> Value {
        serde_json::from_str(
            sent[request]["input"].as_array().unwrap().last().unwrap()["output"]
                .as_str()
                .unwrap(),
        )
        .unwrap()
    };
    assert_eq!(result(1)["content"], "old\n");
    assert_eq!(result(2)["exit_code"], 7);
    assert!(result(2)["output"].as_str().unwrap().contains("FAILED"));
    assert!(result(3)["error"].as_str().unwrap().contains("not found"));
    assert_eq!(result(4)["applied"], 1);
    assert_eq!(result(6)["exit_code"], 0);
    assert_eq!(
        std::fs::read_to_string(workspace.join("source.txt")).unwrap(),
        "new\n"
    );
    assert_eq!(
        std::fs::read_to_string(workspace.join("nested/report.txt")).unwrap(),
        "complete\n"
    );
    std::fs::remove_dir_all(workspace).unwrap();
}

#[tokio::test]
async fn overflow_below_local_threshold_compacts_and_rebuilds_the_request() {
    let journal = Journal::new();
    let conversation = previous_history().with_history_file(&journal.0).unwrap();
    let (client, server) = scripted_http(vec![
        (400, overflow()),
        (200, completed("Preserve ion-42; initial work is complete.")),
        (200, completed("Recovered.")),
    ])
    .await;
    let mut agent =
        Agent::new(client, ToolRegistry::new(), conversation).with_model("test".to_owned());
    agent
        .handle_user_message("Continue project ion-42.".to_owned())
        .await
        .unwrap();
    let sent = requests(server).await;
    assert_eq!(sent.len(), 3);
    assert_eq!(sent[0]["input"][1]["role"], "user");
    assert_eq!(sent[1]["max_output_tokens"], 1024);
    assert!(sent[1].get("tools").is_none());
    assert!(
        sent[2]["input"][1]["content"]
            .as_str()
            .unwrap()
            .contains("[Earlier conversation summary]")
    );
    assert_ne!(sent[0]["input"], sent[2]["input"]);
    assert_eq!(
        sent[0]["input"].as_array().unwrap().last(),
        sent[2]["input"].as_array().unwrap().last()
    );
    assert!(has_checkpoint(&journal.records()));
}

#[tokio::test]
async fn a_second_overflow_stops_without_another_compaction_and_the_next_turn_can_run() {
    let (client, server) = scripted_http(vec![
        (400, overflow()),
        (200, completed("Preserve ion-42.")),
        (400, overflow()),
        (200, completed("New turn works.")),
    ])
    .await;
    let mut agent =
        Agent::new(client, ToolRegistry::new(), previous_history()).with_model("test".to_owned());
    let error = agent
        .handle_user_message("Continue project ion-42.".to_owned())
        .await
        .unwrap_err();
    assert_overflow(&error);
    agent
        .handle_user_message("Try a new task.".to_owned())
        .await
        .unwrap();
    let sent = requests(server).await;
    assert_eq!(sent.len(), 4);
    assert_eq!(sent[2]["input"][1], sent[3]["input"][1]);
    assert_eq!(
        sent[3]["input"].as_array().unwrap().last().unwrap()["content"],
        "Try a new task."
    );
}

#[tokio::test]
async fn summary_overflow_does_not_recursively_compact_or_discard_original_history() {
    let journal = Journal::new();
    let conversation = previous_history().with_history_file(&journal.0).unwrap();
    let before = serde_json::to_value(&conversation).unwrap();
    let (client, server) = scripted_http(vec![(400, overflow()), (400, overflow())]).await;
    let mut agent =
        Agent::new(client, ToolRegistry::new(), conversation).with_model("test".to_owned());
    let error = agent
        .handle_user_message("Continue project ion-42.".to_owned())
        .await
        .unwrap_err();
    assert_overflow(&error);
    let sent = requests(server).await;
    assert_eq!(sent.len(), 2);
    let after = serde_json::to_value(&agent.conversation).unwrap();
    assert_eq!(
        &after.as_array().unwrap()[..before.as_array().unwrap().len()],
        before.as_array().unwrap()
    );
    let records = journal.records();
    assert!(!has_checkpoint(&records));
    assert!(
        records
            .iter()
            .flat_map(|record| record["items"].as_array().into_iter().flatten())
            .any(|item| item["content"] == "Continue project ion-42.")
    );
}

#[tokio::test]
async fn overflow_without_old_history_sends_no_summary_and_keeps_the_input_in_the_journal() {
    let journal = Journal::new();
    let conversation = conversation().with_history_file(&journal.0).unwrap();
    let (client, server) = scripted_http(vec![(400, overflow())]).await;
    let mut agent =
        Agent::new(client, ToolRegistry::new(), conversation).with_model("test".to_owned());
    assert!(
        agent
            .handle_user_message("Only input, no old history.".to_owned())
            .await
            .is_err()
    );
    assert_eq!(requests(server).await.len(), 1);
    let records = journal.records();
    assert!(!has_checkpoint(&records));
    assert!(
        records
            .iter()
            .flat_map(|record| record["items"].as_array().into_iter().flatten())
            .any(|item| item["content"] == "Only input, no old history.")
    );
    assert_eq!(
        serde_json::to_value(&agent.conversation).unwrap()[1]["content"],
        "Only input, no old history."
    );
}

#[tokio::test]
async fn tool_continuation_recovery_preserves_native_tail_and_does_not_execute_the_tool_again() {
    let executions = Arc::new(AtomicUsize::new(0));
    let counter = executions.clone();
    let mut tools = ToolRegistry::new();
    tools.registry(Tool {
        definition: ToolDefinition {
            tool_type: ToolType::Function,
            name: "lookup".to_owned(),
            description: "Read fixture.".to_owned(),
            parameters: json!({"type":"object","properties":{}}),
        },
        handler: Arc::new(move |_| {
            counter.fetch_add(1, Ordering::SeqCst);
            Box::pin(async { Ok(json!({"result":37,"source":"native tool result ".repeat(100)})) })
        }),
    });
    let tool_response = json!({"id":"tools", "status":"completed", "output":[
        {"type":"reasoning", "id":"reasoning", "status":"completed", "encrypted_content":"native-continuation", "summary":[{"type":"summary_text","text":"Inspect first."}], "content":[{"type":"reasoning_text","text":"Inspect the fixture once.","native_field":"preserved"}]},
        {"type":"function_call", "id":"call", "status":"completed", "call_id":"lookup-1", "name":"lookup", "arguments":"{}"}
    ], "usage":{"input_tokens":6000,"output_tokens":100}});
    let (client, server) = scripted_http(vec![
        (200, tool_response),
        (400, overflow()),
        (200, completed("Initial work was completed for ion-42.")),
        (200, completed("Result is 37.")),
    ])
    .await;
    let mut agent = Agent::new(client, tools, previous_history()).with_model("test".to_owned());
    agent
        .handle_user_message("Look up the result for ion-42.".to_owned())
        .await
        .unwrap();
    let sent = requests(server).await;
    assert_eq!(executions.load(Ordering::SeqCst), 1);
    assert_eq!(sent.len(), 4);
    let original = sent[1]["input"].as_array().unwrap();
    let rebuilt = sent[3]["input"].as_array().unwrap();
    assert_eq!(&rebuilt[2..], &original[original.len() - 4..]);
    assert_eq!(rebuilt[2]["role"], "user");
    assert_eq!(rebuilt[3]["type"], "reasoning");
    assert_eq!(rebuilt[3]["encrypted_content"], "native-continuation");
    assert_eq!(rebuilt[3]["summary"][0]["type"], "summary_text");
    assert_eq!(rebuilt[3]["content"][0]["native_field"], "preserved");
    assert!(rebuilt[3].get("id").is_none());
    assert!(rebuilt[3].get("status").is_none());
    assert_eq!(rebuilt[4]["call_id"], "lookup-1");
    assert_eq!(rebuilt[5]["type"], "function_call_output");
    assert_eq!(rebuilt[5]["call_id"], "lookup-1");
    assert_eq!(
        serde_json::from_str::<Value>(rebuilt[5]["output"].as_str().unwrap()).unwrap()["result"],
        37
    );
}

#[tokio::test]
async fn a_successful_tool_call_allows_one_new_recovery_for_its_continuation() {
    let mut history = previous_history()
        .with_compaction_config(CompactionConfig {
            context_budget_tokens: 65_536,
            max_input_tokens: None,
            reserve_tokens: 2048,
            keep_recent_tokens: 1000,
            max_summary_tokens: 1024,
        })
        .unwrap();
    history.add_user_message("recent completed work ".repeat(70));
    history.add_assistant_message("Recent work complete.".to_owned());
    let executions = Arc::new(AtomicUsize::new(0));
    let counter = executions.clone();
    let mut tools = ToolRegistry::new();
    tools.registry(Tool {
        definition: ToolDefinition {
            tool_type: ToolType::Function,
            name: "lookup".to_owned(),
            description: "Read fixture.".to_owned(),
            parameters: json!({"type":"object","properties":{}}),
        },
        handler: Arc::new(move |_| {
            counter.fetch_add(1, Ordering::SeqCst);
            Box::pin(async { Ok(json!({"result":37,"source":"native result ".repeat(140)})) })
        }),
    });
    let tool_response = json!({"id":"tools", "status":"completed", "output":[
        {"type":"function_call", "id":"call", "status":"completed", "call_id":"lookup-2", "name":"lookup", "arguments":"{}"}
    ], "usage":{"input_tokens":6000,"output_tokens":100}});
    let (client, server) = scripted_http(vec![
        (400, overflow()),
        (200, completed("Initial work is complete.")),
        (200, tool_response),
        (400, overflow()),
        (200, completed("Both initial and recent work are complete.")),
        (200, completed("Result is 37.")),
    ])
    .await;
    let mut agent = Agent::new(client, tools, history).with_model("test".to_owned());
    agent
        .handle_user_message("Look up the result for ion-42.".to_owned())
        .await
        .unwrap();
    let sent = requests(server).await;
    assert_eq!(sent.len(), 6);
    assert_eq!(executions.load(Ordering::SeqCst), 1);
    for index in [1, 4] {
        assert!(sent[index].get("tools").is_none());
        assert_eq!(sent[index]["max_output_tokens"], 1024);
    }
    assert_eq!(
        sent[2]["input"][2]["content"],
        "recent completed work ".repeat(70)
    );
    assert_eq!(
        sent[5]["input"][2]["content"],
        "Look up the result for ion-42."
    );
    let last = sent[5]["input"].as_array().unwrap().last().unwrap();
    assert_eq!(last["type"], "function_call_output");
    assert_eq!(last["call_id"], "lookup-2");
}

#[tokio::test]
async fn events_follow_response_output_order_and_route_tool_results_by_call_id() {
    let mut tools = ToolRegistry::new();
    tools.registry(Tool {
        definition: ToolDefinition {
            tool_type: ToolType::Function,
            name: "lookup".to_owned(),
            description: "Read fixture.".to_owned(),
            parameters: json!({"type":"object","properties":{}}),
        },
        handler: Arc::new(|_| Box::pin(async { Ok(json!({"result": 37})) })),
    });
    let tool_response = json!({"id":"tools", "status":"completed", "output":[
        {"type":"message", "id":"m1", "status":"completed", "role":"assistant", "content":[{"type":"output_text", "text":"Inspecting source."}]},
        {"type":"message", "id":"m2", "status":"completed", "role":"assistant", "content":[{"type":"output_text", "text":"Still checking."}]},
        {"type":"function_call", "id":"f1", "status":"completed", "call_id":"call-1", "name":"lookup", "arguments":"{}"}
    ], "usage":{"input_tokens":6000,"output_tokens":100}});
    let (client, server) = scripted_http(vec![
        (200, tool_response),
        (200, completed("Task verified.")),
    ])
    .await;
    let (sender, mut receiver) = event_channels();
    let mut agent = Agent::new(client, tools, conversation())
        .with_model("test".to_owned())
        .with_events(sender);
    agent
        .handle_user_message("Look it up.".to_owned())
        .await
        .unwrap();
    assert_eq!(
        drain_events(&mut receiver)
            .into_iter()
            .filter(|event| !matches!(event, AgentEvent::ContextUpdated { .. }))
            .collect::<Vec<_>>(),
        vec![
            AgentEvent::ResponseText {
                item_id: "m1".to_owned(),
                text: "Inspecting source.".to_owned()
            },
            AgentEvent::ResponseText {
                item_id: "m2".to_owned(),
                text: "Still checking.".to_owned()
            },
            AgentEvent::Usage {
                input_tokens: Some(6000),
                output_tokens: Some(100)
            },
            AgentEvent::ToolStarted {
                call_id: "call-1".to_owned(),
                name: "lookup".to_owned(),
                arguments: "{}".to_owned()
            },
            AgentEvent::ToolFinished {
                call_id: "call-1".to_owned(),
                name: "lookup".to_owned(),
                output: json!({"result": 37})
            },
            AgentEvent::ResponseText {
                item_id: "message".to_owned(),
                text: "Task verified.".to_owned()
            },
            AgentEvent::Usage {
                input_tokens: Some(6000),
                output_tokens: Some(40)
            },
        ]
    );
    assert_eq!(requests(server).await.len(), 2);
}

#[tokio::test]
async fn malformed_tool_arguments_close_the_call_and_the_next_turn_runs() {
    let mut tools = ToolRegistry::new();
    tools.registry(Tool {
        definition: ToolDefinition {
            tool_type: ToolType::Function,
            name: "lookup".to_owned(),
            description: "Read fixture.".to_owned(),
            parameters: json!({"type":"object","properties":{}}),
        },
        handler: Arc::new(|_| Box::pin(async { Ok(json!({"result": 37})) })),
    });
    let bad_call = json!({"id":"r1", "status":"completed", "output":[
        {"type":"function_call", "id":"f1", "status":"completed", "call_id":"bad-1", "name":"lookup", "arguments":"{not json"}
    ], "usage":{"input_tokens":6000,"output_tokens":10}});
    let (client, server) = scripted_http(vec![
        (200, bad_call),
        (200, completed("Recovered from bad arguments.")),
        (200, completed("Second turn works.")),
    ])
    .await;
    let (sender, mut receiver) = event_channels();
    let mut agent = Agent::new(client, tools, conversation())
        .with_model("test".to_owned())
        .with_events(sender);
    agent
        .handle_user_message("Try the tool.".to_owned())
        .await
        .unwrap();
    let events = drain_events(&mut receiver);
    assert_eq!(
        *events
            .iter()
            .find(|event| matches!(event, AgentEvent::ToolStarted { .. }))
            .unwrap(),
        AgentEvent::ToolStarted {
            call_id: "bad-1".to_owned(),
            name: "lookup".to_owned(),
            arguments: "{not json".to_owned()
        }
    );
    match events
        .iter()
        .find(|event| matches!(event, AgentEvent::ToolFinished { .. }))
        .unwrap()
    {
        AgentEvent::ToolFinished {
            call_id, output, ..
        } => {
            assert_eq!(call_id, "bad-1");
            assert!(
                output["error"]
                    .as_str()
                    .unwrap()
                    .contains("Invalid tool arguments")
            );
        }
        other => panic!("expected a tool failure, got {other:?}"),
    }
    agent
        .handle_user_message("New turn.".to_owned())
        .await
        .unwrap();
    let next = drain_events(&mut receiver);
    assert!(next.iter().any(
        |event| matches!(event, AgentEvent::ResponseText { text, .. } if text == "Second turn works.")
    ));
    assert_eq!(requests(server).await.len(), 3);
}

#[tokio::test]
async fn unknown_tool_closes_the_call_with_the_not_found_error() {
    let unknown_call = json!({"id":"r1", "status":"completed", "output":[
        {"type":"function_call", "id":"f1", "status":"completed", "call_id":"missing-1", "name":"does_not_exist", "arguments":"{}"}
    ], "usage":{"input_tokens":6000,"output_tokens":10}});
    let (client, server) =
        scripted_http(vec![(200, unknown_call), (200, completed("Recovered."))]).await;
    let (sender, mut receiver) = event_channels();
    let mut agent = Agent::new(client, ToolRegistry::new(), conversation())
        .with_model("test".to_owned())
        .with_events(sender);
    agent
        .handle_user_message("Call a missing tool.".to_owned())
        .await
        .unwrap();
    let events = drain_events(&mut receiver);
    assert_eq!(
        *events
            .iter()
            .find(|event| matches!(event, AgentEvent::ToolStarted { .. }))
            .unwrap(),
        AgentEvent::ToolStarted {
            call_id: "missing-1".to_owned(),
            name: "does_not_exist".to_owned(),
            arguments: "{}".to_owned()
        }
    );
    match events
        .iter()
        .find(|event| matches!(event, AgentEvent::ToolFinished { .. }))
        .unwrap()
    {
        AgentEvent::ToolFinished {
            call_id,
            name,
            output,
            ..
        } => {
            assert_eq!(call_id, "missing-1");
            assert_eq!(name, "does_not_exist");
            assert!(output["error"].as_str().unwrap().contains("does_not_exist"));
        }
        other => panic!("expected a tool failure, got {other:?}"),
    }
    assert_eq!(requests(server).await.len(), 2);
}

#[tokio::test]
async fn worker_emits_one_terminal_event_per_input_and_compaction_keeps_history() {
    let (client, server) = scripted_http(vec![
        (200, completed("First answer.")),
        (400, overflow()),
        (200, completed("Summary of earlier work.")),
        (200, completed("Second answer.")),
    ])
    .await;
    let (events_tx, mut events_rx) = event_channels();
    let (input_tx, input_rx) = tokio::sync::mpsc::unbounded_channel();
    let agent = Agent::new(client, ToolRegistry::new(), previous_history())
        .with_model("test".to_owned())
        .with_events(events_tx);
    let worker = tokio::spawn(run_agent(agent, input_rx));
    input_tx.send("First task.".into()).unwrap();
    input_tx.send("Second task.".into()).unwrap();
    drop(input_tx);
    worker.await.unwrap();
    let events = drain_events(&mut events_rx);
    assert_eq!(requests(server).await.len(), 4);

    let terminal_count = events.iter().filter(|event| is_terminal(event)).count();
    let completed_count = events
        .iter()
        .filter(|event| matches!(**event, AgentEvent::TurnCompleted))
        .count();
    assert_eq!(terminal_count, 2);
    assert_eq!(completed_count, 2);
    assert!(events.iter().any(
        |event| matches!(event, AgentEvent::ResponseText { text, .. } if text == "First answer.")
    ));
    assert!(
        events
            .iter()
            .any(|event| matches!(event, AgentEvent::Compaction { .. }))
    );
    assert!(events.iter().any(
        |event| matches!(event, AgentEvent::ResponseText { text, .. } if text == "Second answer.")
    ));
    let compact_index = events
        .iter()
        .position(|event| matches!(event, AgentEvent::Compaction { .. }))
        .unwrap();
    let AgentEvent::Compaction { tokens_after, .. } = &events[compact_index] else {
        unreachable!()
    };
    assert!(
        matches!(&events[compact_index + 1], AgentEvent::ContextUpdated {
        context_tokens: Some(tokens), context_window: Some(65_536), ..
    } if tokens == tokens_after)
    );
}

#[tokio::test]
async fn context_events_include_startup_limits_and_tool_growth_without_new_usage() {
    let mut tools = ToolRegistry::new();
    tools.registry(Tool {
        definition: ToolDefinition {
            tool_type: ToolType::Function,
            name: "lookup".to_owned(),
            description: "Read fixture.".to_owned(),
            parameters: json!({"type":"object","properties":{}}),
        },
        handler: Arc::new(|_| Box::pin(async { Ok(json!({"text":"tool result ".repeat(500)})) })),
    });
    let tool_response = json!({"id":"call", "status":"completed", "output":[
        {"type":"function_call", "id":"f", "status":"completed", "call_id":"c", "name":"lookup", "arguments":"{}"}
    ], "usage":{"input_tokens":6000,"output_tokens":100}});
    let mut final_response = completed("Finished without usage metadata.");
    final_response.as_object_mut().unwrap().remove("usage");
    let (client, server) = scripted_http(vec![(200, tool_response), (200, final_response)]).await;
    let conversation = conversation();
    let initial = conversation
        .estimated_context_tokens("provider/test", &tools.list_definition())
        .unwrap();
    let (events_tx, mut events_rx) = event_channels();
    let (input_tx, input_rx) = tokio::sync::mpsc::unbounded_channel();
    let agent = Agent::new(client, tools, conversation)
        .with_model("provider/test".to_owned())
        .with_events(events_tx);
    let worker = tokio::spawn(run_agent(agent, input_rx));
    assert_eq!(
        events_rx.recv().await,
        Some(AgentEvent::ContextUpdated {
            model: "provider/test".to_owned(),
            context_tokens: Some(initial),
            context_window: Some(65_536),
            input_limit: Some(63_488),
        })
    );
    input_tx.send("Inspect the fixture.".into()).unwrap();
    drop(input_tx);
    worker.await.unwrap();
    let events = drain_events(&mut events_rx);
    let finished = events
        .iter()
        .position(|event| matches!(event, AgentEvent::ToolFinished { .. }))
        .unwrap();
    assert!(
        matches!(events[finished + 1], AgentEvent::ContextUpdated { context_tokens: Some(tokens), .. } if tokens > 6100)
    );
    let final_context = events
        .iter()
        .rfind(|event| matches!(event, AgentEvent::ContextUpdated { .. }))
        .unwrap();
    assert!(
        matches!(final_context, AgentEvent::ContextUpdated { context_tokens: Some(tokens), .. } if *tokens > 6100)
    );
    assert_eq!(requests(server).await.len(), 2);
}

#[tokio::test]
async fn a_closed_event_channel_does_not_stop_the_agent() {
    let (client, server) = scripted_http(vec![(200, completed("Still works."))]).await;
    let (sender, receiver) = event_channels();
    drop(receiver);
    let mut agent = Agent::new(client, ToolRegistry::new(), conversation())
        .with_model("test".to_owned())
        .with_events(sender);
    agent
        .handle_user_message("Continue.".to_owned())
        .await
        .unwrap();
    assert_eq!(requests(server).await.len(), 1);
}

#[tokio::test]
async fn worker_reports_one_turn_failed_and_the_next_turn_succeeds() {
    let (client, server) = scripted_http(vec![
        (400, overflow()),
        (400, overflow()),
        (200, completed("Second turn works.")),
    ])
    .await;
    let (events_tx, mut events_rx) = event_channels();
    let (input_tx, input_rx) = tokio::sync::mpsc::unbounded_channel();
    let agent = Agent::new(client, ToolRegistry::new(), previous_history())
        .with_model("test".to_owned())
        .with_events(events_tx);
    let worker = tokio::spawn(run_agent(agent, input_rx));
    input_tx.send("Fail this turn.".into()).unwrap();
    input_tx.send("Retry as a new turn.".into()).unwrap();
    drop(input_tx);
    worker.await.unwrap();
    let events = drain_events(&mut events_rx);
    assert_eq!(requests(server).await.len(), 3);
    assert_eq!(events.iter().filter(|event| is_terminal(event)).count(), 2);
    assert!(
        events
            .iter()
            .any(|event| matches!(event, AgentEvent::TurnFailed { .. }))
    );
    assert!(
        events
            .iter()
            .any(|event| matches!(event, AgentEvent::TurnCompleted))
    );
    assert!(events.iter().any(
        |event| matches!(event, AgentEvent::ResponseText { text, .. } if text == "Second turn works.")
    ));
    assert_eq!(events.last(), Some(&AgentEvent::TurnCompleted));
}

#[tokio::test]
async fn mixed_text_and_image_message_serializes_as_input_parts() {
    let (client, server) = scripted_http(vec![(200, completed("ok"))]).await;
    let (sender, _receiver) = event_channels();
    let mut agent = Agent::new(client, ToolRegistry::new(), conversation())
        .with_model("test".to_owned())
        .with_events(sender);
    let message = crate::agent::message::UserMessage {
        text: "look".to_owned(),
        images: vec![
            crate::agent::message::ImageAttachment::new("a.png", "data:image/png;base64,AAAA"),
            crate::agent::message::ImageAttachment::new("b.jpg", "data:image/jpeg;base64,BBBB"),
        ],
    };
    agent.handle_user_message(message).await.unwrap();
    let sent = requests(server).await;
    let content = sent[0]["input"][1]["content"].as_array().unwrap();
    assert_eq!(content.len(), 3);
    assert_eq!(content[0]["type"], "input_text");
    assert_eq!(content[0]["text"], "look");
    assert_eq!(content[1]["type"], "input_image");
    assert_eq!(content[1]["image_url"], "data:image/png;base64,AAAA");
    assert_eq!(content[2]["type"], "input_image");
    assert_eq!(content[2]["image_url"], "data:image/jpeg;base64,BBBB");
}

#[tokio::test]
async fn image_only_message_has_no_text_part_and_text_stays_a_string() {
    let (client, server) = scripted_http(vec![(200, completed("ok"))]).await;
    let (sender, _receiver) = event_channels();
    let mut agent = Agent::new(client, ToolRegistry::new(), conversation())
        .with_model("test".to_owned())
        .with_events(sender);
    agent
        .handle_user_message(crate::agent::message::UserMessage {
            text: String::new(),
            images: vec![crate::agent::message::ImageAttachment::new(
                "a.png",
                "data:image/png;base64,AAAA",
            )],
        })
        .await
        .unwrap();
    let sent = requests(server).await;
    let content = sent[0]["input"][1]["content"].as_array().unwrap();
    assert_eq!(content.len(), 1);
    assert_eq!(content[0]["type"], "input_image");

    // Plain text keeps the string shorthand.
    let (client, server) = scripted_http(vec![(200, completed("ok"))]).await;
    let (sender, _receiver) = event_channels();
    let mut agent = Agent::new(client, ToolRegistry::new(), conversation())
        .with_model("test".to_owned())
        .with_events(sender);
    agent.handle_user_message("hello".to_owned()).await.unwrap();
    let sent = requests(server).await;
    assert_eq!(sent[0]["input"][1]["content"], "hello");
}

#[tokio::test]
async fn cancelled_waits_for_true_survives_false_and_closed_senders() {
    let (tx, rx) = tokio::sync::watch::channel(false);
    let mut option = Some(rx);
    tx.send(false).unwrap();
    assert!(
        tokio::time::timeout(std::time::Duration::from_millis(20), cancelled(&mut option))
            .await
            .is_err(),
        "false must not cancel"
    );
    drop(tx);
    assert!(
        tokio::time::timeout(std::time::Duration::from_millis(20), cancelled(&mut option))
            .await
            .is_err(),
        "a closed sender must not cancel"
    );
    let (tx, rx) = tokio::sync::watch::channel(true);
    let mut option = Some(rx);
    tokio::time::timeout(std::time::Duration::from_millis(20), cancelled(&mut option))
        .await
        .expect("true cancels");
    drop(tx);
}

#[tokio::test]
async fn dropping_the_cancel_handle_does_not_cancel_a_running_turn() {
    let (client, server) = scripted_http(vec![(200, completed("Still works."))]).await;
    let (cancel_tx, cancel_rx) = tokio::sync::watch::channel(false);
    cancel_tx.send(false).unwrap();
    drop(cancel_tx);
    let mut agent = Agent::new(client, ToolRegistry::new(), conversation())
        .with_model("test".to_owned())
        .with_cancel(cancel_rx);
    agent
        .handle_user_message("Continue.".to_owned())
        .await
        .unwrap();
    assert_eq!(requests(server).await.len(), 1);
}

#[tokio::test]
async fn a_pre_cancelled_turn_never_calls_the_model_or_a_tool() {
    let (client, mut server) = scripted_http(vec![(200, completed("must not run"))]).await;
    let (cancel_tx, cancel_rx) = tokio::sync::watch::channel(false);
    cancel_tx.send(true).unwrap();
    let registry = ToolRegistry::new();
    let mut agent = Agent::new(client, registry, conversation())
        .with_model("test".to_owned())
        .with_cancel(cancel_rx);
    let error = agent
        .handle_user_message("Do not run.".to_owned())
        .await
        .unwrap_err();
    assert!(is_interrupted(&error));
    assert!(
        tokio::time::timeout(std::time::Duration::from_millis(100), &mut server)
            .await
            .is_err(),
        "no request should reach the model"
    );
    server.abort();
}
