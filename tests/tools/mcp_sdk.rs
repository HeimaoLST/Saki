use std::{num::NonZeroUsize, sync::Arc, time::Duration};

use rmcp::{
    ClientCacheConfig, ClientHandler, ClientLifecycleMode, ClientServiceExt, RoleClient,
    model::{
        ClientConfig, DiscoverResult, ProtocolVersion, ServerCapabilities, SubscriptionFilter,
    },
    service::NotificationContext,
    transport::async_rw::AsyncRwTransport,
};
use serde_json::{Value, json};
use tokio::{
    io::{AsyncBufReadExt, AsyncWriteExt, BufReader, DuplexStream},
    sync::{Mutex, Notify},
    task::JoinHandle,
};

#[derive(Clone)]
struct Client {
    changed: Arc<Notify>,
}
impl ClientHandler for Client {
    fn get_info(&self) -> ClientConfig {
        ClientConfig::default().with_protocol_version(ProtocolVersion::V_2025_11_25)
    }
    async fn on_tool_list_changed(&self, _: NotificationContext<RoleClient>) {
        self.changed.notify_one();
    }
}

async fn write(writer: &mut tokio::io::WriteHalf<DuplexStream>, value: Value) {
    let mut bytes = serde_json::to_vec(&value).unwrap();
    bytes.push(b'\n');
    writer.write_all(&bytes).await.unwrap();
}

fn fixture(
    modern: bool,
    burst: Option<usize>,
) -> (DuplexStream, Arc<Mutex<Vec<Value>>>, JoinHandle<()>) {
    let (client, server) = tokio::io::duplex(16 * 1024);
    let trace = Arc::new(Mutex::new(Vec::new()));
    let captured = trace.clone();
    let job = tokio::spawn(async move {
        let (read, mut writer) = tokio::io::split(server);
        let mut lines = BufReader::new(read).lines();
        let mut lists = 0;
        let mut listen_id = Value::Null;
        while let Some(line) = lines.next_line().await.unwrap() {
            let request: Value = serde_json::from_str(&line).unwrap();
            captured.lock().await.push(request.clone());
            let id = request.get("id").cloned().unwrap_or(Value::Null);
            match request["method"].as_str().unwrap() {
                "server/discover" => {
                    assert!(
                        modern,
                        "legacy fixture must not receive discovery in explicit mode"
                    );
                    assert_eq!(
                        request["params"]["_meta"]["io.modelcontextprotocol/protocolVersion"],
                        "2026-07-28"
                    );
                    let result = DiscoverResult::new(
                        vec![ProtocolVersion::V_2026_07_28],
                        serde_json::from_value::<ServerCapabilities>(
                            json!({"tools":{"listChanged":true}}),
                        )
                        .unwrap(),
                    );
                    write(
                        &mut writer,
                        json!({"jsonrpc":"2.0","id":id,"result":result}),
                    )
                    .await;
                }
                "initialize" => {
                    assert!(!modern, "modern fixture must not receive initialize");
                    assert_eq!(request["params"]["protocolVersion"], "2025-11-25");
                    write(&mut writer, json!({"jsonrpc":"2.0","id":id,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"legacy-fixture","version":"1"}}})).await;
                }
                "notifications/initialized" | "notifications/cancelled" => {}
                "tools/list" => {
                    lists += 1;
                    if modern
                        && lists == 2
                        && let Some(count) = burst
                    {
                        for _ in 0..count {
                            write(&mut writer,json!({"jsonrpc":"2.0","method":"notifications/tools/list_changed","params":{"_meta":{"io.modelcontextprotocol/subscriptionId":listen_id}}})).await;
                        }
                    }
                    let mut result = json!({"tools":[{"name":"read_marker","description":format!("revision-{lists}"),"inputSchema":{"type":"object"}}],"ttlMs":600000,"cacheScope":"private"});
                    if modern {
                        result["resultType"] = json!("complete");
                    }
                    write(
                        &mut writer,
                        json!({"jsonrpc":"2.0","id":id,"result":result}),
                    )
                    .await;
                    if !modern && lists == 1 {
                        write(
                            &mut writer,
                            json!({"jsonrpc":"2.0","method":"notifications/tools/list_changed"}),
                        )
                        .await;
                    }
                }
                "subscriptions/listen" => {
                    assert!(modern);
                    assert_eq!(
                        request["params"]["notifications"],
                        json!({"toolsListChanged":true})
                    );
                    listen_id = id.clone();
                    let metadata = json!({"io.modelcontextprotocol/subscriptionId":id});
                    write(&mut writer, json!({"jsonrpc":"2.0","method":"notifications/subscriptions/acknowledged","params":{"_meta":metadata,"notifications":{"toolsListChanged":true}}})).await;
                    if burst.is_none() {
                        write(&mut writer, json!({"jsonrpc":"2.0","method":"notifications/tools/list_changed","params":{"_meta":metadata}})).await;
                    }
                }
                other => panic!("unexpected fixture request {other}"),
            }
        }
    });
    (client, trace, job)
}

#[tokio::test]
async fn pinned_sdk_modern_discovery_acknowledges_only_tools_and_lists_fresh() {
    tokio::time::timeout(Duration::from_secs(10), async {
        let (wire, trace, job) = fixture(true, None);
        let changed = Arc::new(Notify::new());
        let (read, writer) = tokio::io::split(wire);
        let client = Client { changed }
            .serve_with_lifecycle(
                AsyncRwTransport::new(read, writer).with_max_line_length(1024 * 1024),
                ClientLifecycleMode::Discover {
                    preferred_versions: vec![ProtocolVersion::V_2026_07_28],
                },
            )
            .await
            .unwrap();
        assert_eq!(
            client.peer_info().unwrap().protocol_version,
            ProtocolVersion::V_2026_07_28
        );
        client
            .set_response_cache_config(ClientCacheConfig::disabled())
            .await;
        assert_eq!(
            client.list_tools(None).await.unwrap().tools[0]
                .description
                .as_deref(),
            Some("revision-1")
        );
        let mut subscription = client
            .listen_with_capacity(
                SubscriptionFilter::builder().tools_list_changed().build(),
                NonZeroUsize::new(2).unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(subscription.acknowledged().tools_list_changed, Some(true));
        let notification = subscription.next().await.unwrap().unwrap();
        use rmcp::model::GetMeta;
        assert_eq!(
            notification.get_meta().subscription_id().as_ref(),
            Some(subscription.id())
        );
        assert_eq!(
            client.list_tools(None).await.unwrap().tools[0]
                .description
                .as_deref(),
            Some("revision-2")
        );
        subscription.cancel().await.unwrap();
        client.cancel().await.unwrap();
        job.await.unwrap();
        let trace = trace.lock().await;
        assert_eq!(
            trace.iter().filter(|r| r["method"] == "tools/list").count(),
            2
        );
        assert!(!trace.iter().any(|r| r["method"] == "initialize"));
    })
    .await
    .unwrap();
}

#[tokio::test]
async fn pinned_sdk_legacy_uses_notifications_without_listen_and_lists_fresh() {
    tokio::time::timeout(Duration::from_secs(10), async {
        let (wire, trace, job) = fixture(false, None);
        let changed = Arc::new(Notify::new());
        let (read, writer) = tokio::io::split(wire);
        let client = Client {
            changed: changed.clone(),
        }
        .serve_with_lifecycle(
            AsyncRwTransport::new(read, writer),
            ClientLifecycleMode::Initialize,
        )
        .await
        .unwrap();
        assert_eq!(
            client.peer_info().unwrap().protocol_version,
            ProtocolVersion::V_2025_11_25
        );
        client
            .set_response_cache_config(ClientCacheConfig::disabled())
            .await;
        assert_eq!(
            client.list_tools(None).await.unwrap().tools[0]
                .description
                .as_deref(),
            Some("revision-1")
        );
        changed.notified().await;
        assert_eq!(
            client.list_tools(None).await.unwrap().tools[0]
                .description
                .as_deref(),
            Some("revision-2")
        );
        client.cancel().await.unwrap();
        job.await.unwrap();
        let trace = trace.lock().await;
        assert_eq!(
            trace.iter().filter(|r| r["method"] == "tools/list").count(),
            2
        );
        assert!(
            !trace
                .iter()
                .any(|r| r["method"] == "subscriptions/listen" || r["method"] == "server/discover")
        );
    })
    .await
    .unwrap();
}

#[tokio::test]
#[ignore = "requires independently built example; set SAKI_TEST_MCP_BIN and run this exact test"]
async fn standalone_rust_server_negotiates_both_versions_reads_files_and_keeps_process_state() {
    use rmcp::model::{CallToolRequestParams, CallToolResponse};
    use std::process::Stdio;
    let binary = std::env::var_os("SAKI_TEST_MCP_BIN").expect("set SAKI_TEST_MCP_BIN");
    let root = std::env::temp_dir().join(format!("saki-mcp-sdk-{}", std::process::id()));
    std::fs::create_dir(&root).unwrap();
    let marker = format!("actual-mcp-marker-{}\n", std::process::id());
    std::fs::write(root.join("probe.txt"), &marker).unwrap();
    for modern in [true, false] {
        let mut command = tokio::process::Command::new(&binary);
        if !modern {
            command.arg("--legacy");
        }
        let mut child = command
            .env_clear()
            .current_dir(&root)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::inherit())
            .kill_on_drop(true)
            .spawn()
            .unwrap();
        let client = tokio::time::timeout(
            Duration::from_secs(10),
            Client {
                changed: Arc::new(Notify::new()),
            }
            .serve_with_lifecycle(
                AsyncRwTransport::new(child.stdout.take().unwrap(), child.stdin.take().unwrap()),
                if modern {
                    ClientLifecycleMode::Discover {
                        preferred_versions: vec![ProtocolVersion::V_2026_07_28],
                    }
                } else {
                    ClientLifecycleMode::Initialize
                },
            ),
        )
        .await
        .unwrap()
        .unwrap();
        client
            .set_response_cache_config(ClientCacheConfig::disabled())
            .await;
        assert_eq!(
            client.peer_info().unwrap().protocol_version,
            if modern {
                ProtocolVersion::V_2026_07_28
            } else {
                ProtocolVersion::V_2025_11_25
            }
        );
        let tools = client.list_tools(None).await.unwrap();
        assert!(tools.tools.iter().any(|tool| tool.name == "read_marker"));
        let result = client
            .call_tool_once(
                CallToolRequestParams::new("read_marker")
                    .with_arguments(rmcp::model::object(json!({"path":"probe.txt"}))),
            )
            .await
            .unwrap();
        let CallToolResponse::Complete(result) = result else {
            panic!("unexpected non-complete call")
        };
        assert_eq!(result.structured_content.unwrap()["marker"], marker);
        assert_eq!(
            serde_json::to_value(&result.content).unwrap()[0]["text"],
            marker
        );
        for expected in [1, 2] {
            let response = client
                .call_tool_once(CallToolRequestParams::new("increment"))
                .await
                .unwrap();
            let CallToolResponse::Complete(response) = response else {
                panic!("unexpected non-complete call")
            };
            assert_eq!(response.structured_content.unwrap()["count"], expected);
        }
        client.cancel().await.unwrap();
        assert!(
            tokio::time::timeout(Duration::from_secs(2), child.wait())
                .await
                .unwrap()
                .unwrap()
                .success()
        );
        println!(
            "standalone MCP {}: actual file bytes, process-local counter 1/2 and clean exit PASS",
            if modern { "2026-07-28" } else { "2025-11-25" }
        );
    }
    std::fs::remove_file(root.join("probe.txt")).unwrap();
    std::fs::remove_dir(root).unwrap();
}

#[tokio::test]
async fn pinned_sdk_lag_ends_bounded_subscription_and_replacement_uses_new_id() {
    tokio::time::timeout(Duration::from_secs(5), async {
        let (wire, trace, job) = fixture(true, Some(4));
        let (read, writer) = tokio::io::split(wire);
        let client = Client {
            changed: Arc::new(Notify::new()),
        }
        .serve_with_lifecycle(
            AsyncRwTransport::new(read, writer),
            ClientLifecycleMode::Discover {
                preferred_versions: vec![ProtocolVersion::V_2026_07_28],
            },
        )
        .await
        .unwrap();
        client
            .set_response_cache_config(ClientCacheConfig::disabled())
            .await;
        client.list_tools(None).await.unwrap();
        let filter = SubscriptionFilter::builder().tools_list_changed().build();
        let mut subscription = client
            .listen_with_capacity(filter.clone(), NonZeroUsize::new(2).unwrap())
            .await
            .unwrap();
        let old = subscription.id().clone();
        // The response follows the burst on the same wire, proving SDK delivery
        // reached the full subscription queue before this test consumes it.
        client.list_tools(None).await.unwrap();
        let mut received = 0;
        while subscription.next().await.unwrap().is_some() {
            received += 1;
        }
        assert!(received <= 2);
        assert!(matches!(
            subscription.end(),
            Some(rmcp::service::SubscriptionEnd::Lagged { capacity: 2 })
        ));
        let mut replacement = client
            .listen_with_capacity(filter, NonZeroUsize::new(2).unwrap())
            .await
            .unwrap();
        assert_ne!(replacement.id(), &old);
        let tools = client.list_tools(None).await.unwrap();
        assert_eq!(tools.tools[0].description.as_deref(), Some("revision-3"));
        replacement.cancel().await.unwrap();
        client.cancel().await.unwrap();
        job.await.unwrap();
        let trace = trace.lock().await;
        assert_eq!(
            trace.iter().filter(|r| r["method"] == "tools/list").count(),
            3
        );
        assert_eq!(
            trace
                .iter()
                .filter(|r| r["method"] == "subscriptions/listen")
                .count(),
            2
        );
    })
    .await
    .unwrap();
}
