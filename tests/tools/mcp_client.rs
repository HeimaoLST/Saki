use super::super::test_support::*;
use super::*;
use crate::tools::mcp::config::{PreparedSource, ToolSourceDefinition};
use anyhow::{Context, Result, ensure};
use serde_json::{Value, json};
use std::{
    path::{Path, PathBuf},
    sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    },
    time::Duration,
};
use tokio::{
    net::TcpListener,
    sync::{Semaphore, oneshot},
    time::timeout,
};

#[tokio::test]
#[ignore = "requires independent example; set SAKI_TEST_MCP_BIN and run this exact test"]
async fn owned_source_reads_calls_discovers_and_reaps_both_protocols_and_strict_auto_fallback() {
    let binary = std::env::var("SAKI_TEST_MCP_BIN").expect("set SAKI_TEST_MCP_BIN to a UTF-8 path");
    let root = std::env::temp_dir().join(format!("saki-mcp-owned-{}", std::process::id()));
    std::fs::create_dir(&root).unwrap();
    let marker = format!("owned-source-marker-{}\n", std::process::id());
    std::fs::write(root.join("probe.txt"), &marker).unwrap();
    let slots = Arc::new(Semaphore::new(1));
    for (generation, policy) in ["modern", "legacy", "auto"].iter().enumerate() {
        let definition: ToolSourceDefinition = serde_json::from_value(json!({
            "id":"example", "transport":"stdio", "command":binary,
            "args":if *policy=="modern" {Vec::<&str>::new()}else{vec!["--legacy"]},
            "protocol":policy,
        }))
        .unwrap();
        let config = Arc::new(definition.prepare(&root, |_| None).unwrap());
        let permit = slots.clone().try_acquire_owned().unwrap();
        let (mut source, tools) =
            SourceProcess::start(config, &root, generation as u64 + 1, permit)
                .await
                .unwrap();
        assert_eq!(slots.available_permits(), 0);
        assert_eq!(source.generation, generation as u64 + 1);
        assert_eq!(tools.len(), 2);
        assert!(!source.is_closed());
        let read = source
            .call("read_marker", json!({"path":"probe.txt"}))
            .await
            .unwrap();
        assert_eq!(read.structured_content.unwrap()["marker"], marker);
        let failed = source
            .call("read_marker", json!({"path":"missing.txt"}))
            .await
            .unwrap();
        assert_eq!(failed.is_error, Some(true));
        assert!(!source.needs_cleanup());
        for count in [1, 2] {
            let result = source.call("increment", json!({})).await.unwrap();
            assert_eq!(result.structured_content.unwrap()["count"], count);
        }
        assert_eq!(source.discover().await.unwrap().len(), 2);
        source.close().await.unwrap();
        assert!(source.is_closed());
        assert_eq!(slots.available_permits(), 1);
        source.close().await.unwrap();
        println!("owned source {policy}: marker/business-error/count/discovery/reap PASS");
    }
    std::fs::remove_file(root.join("probe.txt")).unwrap();
    std::fs::remove_dir(root).unwrap();
}

fn test_root(label: &str) -> PathBuf {
    static NEXT: AtomicUsize = AtomicUsize::new(0);
    let path = std::env::temp_dir().join(format!(
        "saki-mcp-client-{}-{}-{}",
        std::process::id(),
        label,
        NEXT.fetch_add(1, Ordering::Relaxed)
    ));
    std::fs::create_dir_all(&path).unwrap();
    path
}

fn source_config(
    root: &Path,
    port: u16,
    protocol: &str,
    call_timeout_ms: u64,
    cancel_grace_ms: u64,
) -> Arc<PreparedSource> {
    let definition: ToolSourceDefinition = serde_json::from_value(json!({
        "id": "controlled",
        "transport": "stdio",
        "command": python_command().to_string_lossy(),
        "args": [
            proxy_script().to_string_lossy(),
            "--control-port",
            port.to_string(),
        ],
        "env_from": ["MCP_TEST_TOKEN"],
        "protocol": protocol,
        "startup_timeout_ms": 5000,
        "call_timeout_ms": call_timeout_ms,
        "cancel_grace_ms": cancel_grace_ms,
    }))
    .expect("test source definition");
    Arc::new(
        definition
            .prepare(root, |name| {
                (name == "MCP_TEST_TOKEN").then(|| "wire-test-token".to_owned())
            })
            .expect("test source preparation"),
    )
}

async fn serve_discovery_attempt(
    control: &mut ControlConnection,
    listen_id: &Value,
    notify: bool,
) -> Result<()> {
    let first = control.next_request().await?;
    ensure!(first["method"].as_str() == Some("tools/list"));
    ensure!(
        first["params"].get("cursor").is_none(),
        "expected first tools/list page"
    );
    if notify {
        control.send(list_changed(listen_id)).await?;
    }
    control
        .respond(
            &first["id"],
            tools_page(vec![tool("alpha")], Some("retry-page"), true),
        )
        .await?;

    let second = control.next_request().await?;
    ensure!(second["method"].as_str() == Some("tools/list"));
    ensure!(
        second["params"].get("cursor").and_then(Value::as_str) == Some("retry-page"),
        "expected retry page cursor"
    );
    control
        .respond(&second["id"], tools_page(vec![tool("beta")], None, true))
        .await?;
    Ok(())
}

fn names(tools: &[rmcp::model::Tool]) -> Vec<String> {
    tools.iter().map(|tool| tool.name.to_string()).collect()
}

async fn start_source(
    config: Arc<PreparedSource>,
    root: &Path,
    generation: u64,
    slots: &Arc<Semaphore>,
) -> Result<(SourceProcess, Vec<rmcp::model::Tool>)> {
    let permit = slots
        .clone()
        .try_acquire_owned()
        .context("test source slot")?;
    timeout(
        WIRE_TIMEOUT,
        SourceProcess::start(config, root, generation, permit),
    )
    .await
    .context("MCP source startup timed out")?
}

#[tokio::test]
async fn modern_catalog_paginates_retries_and_reports_instability() {
    let root = test_root("modern");
    let listener = TcpListener::bind(("127.0.0.1", 0)).await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let handler = tokio::spawn(async move {
        let mut control = accept_ready(&listener).await?;
        let listen = serve_modern_startup(
            &mut control,
            true,
            &[vec![tool("alpha")], vec![tool("beta")]],
        )
        .await?
        .context("listen request missing")?;

        serve_discovery_attempt(&mut control, &listen, true).await?;
        serve_discovery_attempt(&mut control, &listen, false).await?;
        serve_discovery_attempt(&mut control, &listen, true).await?;
        serve_discovery_attempt(&mut control, &listen, true).await?;

        while control.recv().await.is_ok() {}
        Ok::<(), anyhow::Error>(())
    });

    let slots = Arc::new(Semaphore::new(1));
    let config = source_config(&root, port, "modern", 120_000, 2_000);
    let (mut source, tools) = start_source(config, &root, 1, &slots)
        .await
        .expect("modern source should start");
    assert_eq!(names(&tools), ["alpha", "beta"]);

    let refreshed = timeout(WIRE_TIMEOUT, source.discover())
        .await
        .expect("first rediscovery timed out")
        .expect("catalog should recover after one change");
    assert_eq!(names(&refreshed), ["alpha", "beta"]);

    let error = timeout(WIRE_TIMEOUT, source.discover())
        .await
        .expect("unstable rediscovery timed out")
        .expect_err("two catalog changes must be unstable");
    assert!(error.to_string().contains("catalog_unstable"), "{error}");

    source.close().await.expect("source close");
    assert_eq!(slots.available_permits(), 1);
    assert!(handler.await.expect("control handler").is_ok());
    std::fs::remove_dir_all(root).unwrap();
}

#[tokio::test]
async fn legacy_initializes_without_listen_and_rediscovers() {
    let root = test_root("legacy");
    let listener = TcpListener::bind(("127.0.0.1", 0)).await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let handler = tokio::spawn(async move {
        let mut control = accept_ready(&listener).await?;
        serve_legacy_startup(&mut control, &[vec![tool("legacy")]]).await?;
        serve_tools_pages(&mut control, &[vec![tool("legacy")]], false).await?;
        while control.recv().await.is_ok() {}
        Ok::<(), anyhow::Error>(())
    });

    let slots = Arc::new(Semaphore::new(1));
    let config = source_config(&root, port, "legacy", 120_000, 2_000);
    let (mut source, tools) = start_source(config, &root, 1, &slots)
        .await
        .expect("legacy source should start");
    assert_eq!(names(&tools), ["legacy"]);
    let refreshed = timeout(WIRE_TIMEOUT, source.discover())
        .await
        .expect("legacy discovery timed out")
        .expect("legacy discovery should succeed");
    assert_eq!(names(&refreshed), ["legacy"]);
    source.close().await.expect("source close");
    assert_eq!(slots.available_permits(), 1);
    assert!(handler.await.expect("control handler").is_ok());
    std::fs::remove_dir_all(root).unwrap();
}

#[tokio::test]
async fn modern_without_list_changed_still_lists_on_each_discover() {
    let root = test_root("polling");
    let listener = TcpListener::bind(("127.0.0.1", 0)).await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let handler = tokio::spawn(async move {
        let mut control = accept_ready(&listener).await?;
        serve_modern_startup(&mut control, false, &[vec![tool("polled")]]).await?;
        serve_tools_pages(&mut control, &[vec![tool("polled")]], true).await?;
        while control.recv().await.is_ok() {}
        Ok::<(), anyhow::Error>(())
    });

    let slots = Arc::new(Semaphore::new(1));
    let config = source_config(&root, port, "modern", 120_000, 2_000);
    let (mut source, tools) = start_source(config, &root, 1, &slots)
        .await
        .expect("polling source should start");
    assert_eq!(names(&tools), ["polled"]);
    let refreshed = timeout(WIRE_TIMEOUT, source.discover())
        .await
        .expect("polling discovery timed out")
        .expect("polling discovery should succeed");
    assert_eq!(names(&refreshed), ["polled"]);
    source.close().await.expect("source close");
    assert_eq!(slots.available_permits(), 1);
    assert!(handler.await.expect("control handler").is_ok());
    std::fs::remove_dir_all(root).unwrap();
}

#[tokio::test]
async fn auth_error_does_not_fallback_or_spawn_a_second_source() {
    let root = test_root("auth");
    let listener = TcpListener::bind(("127.0.0.1", 0)).await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let attempts = Arc::new(AtomicUsize::new(0));
    let attempts_for_task = attempts.clone();
    let accept_task = tokio::spawn(async move {
        loop {
            let (stream, _) = listener.accept().await.expect("control accept");
            attempts_for_task.fetch_add(1, Ordering::SeqCst);
            tokio::spawn(async move {
                let mut control = ControlConnection::from_stream(stream);
                if control.expect_ready().await.is_err() {
                    return;
                }
                let request = match control.next_request().await {
                    Ok(request) => request,
                    Err(_) => return,
                };
                let _ = control
                    .send(json!({
                        "jsonrpc": "2.0",
                        "id": request["id"],
                        "error": {"code": -32001, "message": "auth required"}
                    }))
                    .await;
            });
        }
    });

    let slots = Arc::new(Semaphore::new(1));
    let config = source_config(&root, port, "auto", 120_000, 2_000);
    let permit = slots.clone().try_acquire_owned().unwrap();
    let result = timeout(WIRE_TIMEOUT, SourceProcess::start(config, &root, 1, permit))
        .await
        .expect("auth startup timed out");
    assert!(result.is_err(), "auth error must fail startup");
    assert_eq!(
        attempts.load(Ordering::SeqCst),
        1,
        "auth error must not trigger legacy fallback"
    );
    assert_eq!(slots.available_permits(), 1);
    accept_task.abort();
    let _ = accept_task.await;
    std::fs::remove_dir_all(root).unwrap();
}

#[tokio::test]
async fn dropped_call_cancels_once_and_close_reaps_the_process() {
    let root = test_root("cancel");
    let listener = TcpListener::bind(("127.0.0.1", 0)).await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let (call_seen_tx, call_seen_rx) = oneshot::channel();
    let (cancel_seen_tx, cancel_seen_rx) = oneshot::channel();
    let handler = tokio::spawn(async move {
        let mut control = accept_ready(&listener).await?;
        serve_modern_startup(&mut control, false, &[vec![tool("slow")]]).await?;

        let call = control.next_request().await?;
        ensure!(call["method"].as_str() == Some("tools/call"));
        ensure!(call["params"]["name"].as_str() == Some("slow"));
        let _ = call_seen_tx.send(());
        let mut cancelled = false;
        while let Ok(message) = control.recv().await {
            if message.get("method").and_then(Value::as_str) == Some("notifications/cancelled") {
                ensure!(message["params"]["requestId"] == call["id"]);
                let _ = cancel_seen_tx.send(());
                cancelled = true;
                break;
            }
        }
        ensure!(cancelled, "tool call was not cancelled");
        while control.recv().await.is_ok() {}
        Ok::<(), anyhow::Error>(())
    });

    let slots = Arc::new(Semaphore::new(1));
    let config = source_config(&root, port, "modern", 120_000, 2_000);
    let (mut source, _) = start_source(config, &root, 1, &slots)
        .await
        .expect("source should start");

    let mut call = Box::pin(source.call("slow", json!({})));
    timeout(WIRE_TIMEOUT, async {
        tokio::select! {
            _ = call_seen_rx => {}
            result = &mut call => panic!("call unexpectedly completed: {result:?}"),
        }
    })
    .await
    .expect("tools/call was not observed");
    drop(call);

    timeout(WIRE_TIMEOUT, cancel_seen_rx)
        .await
        .expect("cancel was not sent")
        .expect("cancel signal");
    assert!(source.needs_cleanup());

    source.close().await.expect("source close");
    assert_eq!(slots.available_permits(), 1);
    assert!(handler.await.expect("control handler").is_ok());
    std::fs::remove_dir_all(root).unwrap();
}

#[tokio::test]
async fn call_timeout_is_advanced_only_after_the_request_arrives() {
    let root = test_root("timeout");
    let listener = TcpListener::bind(("127.0.0.1", 0)).await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let (call_seen_tx, call_seen_rx) = oneshot::channel();
    let handler = tokio::spawn(async move {
        let mut control = accept_ready(&listener).await?;
        serve_modern_startup(&mut control, false, &[vec![tool("slow")]]).await?;
        let call = control.next_request().await?;
        ensure!(call["method"].as_str() == Some("tools/call"));
        let _ = call_seen_tx.send(());
        while control.recv().await.is_ok() {}
        Ok::<(), anyhow::Error>(())
    });

    let slots = Arc::new(Semaphore::new(1));
    let config = source_config(&root, port, "modern", 100, 2_000);
    let (mut source, _) = start_source(config, &root, 1, &slots)
        .await
        .expect("source should start");

    let mut call = Box::pin(source.call("slow", json!({})));
    timeout(WIRE_TIMEOUT, async {
        tokio::select! {
            _ = call_seen_rx => {}
            result = &mut call => panic!("call unexpectedly completed: {result:?}"),
        }
    })
    .await
    .expect("tools/call was not observed");

    tokio::time::pause();
    tokio::time::advance(Duration::from_millis(150)).await;
    tokio::time::resume();
    let error = call.await.expect_err("call must time out");
    assert!(error.to_string().contains("timed out"), "{error}");
    assert!(!source.needs_cleanup());
    assert!(source.is_closed());
    assert_eq!(slots.available_permits(), 1);

    source.close().await.expect("source close");
    assert_eq!(slots.available_permits(), 1);
    assert!(handler.await.expect("control handler").is_ok());
    std::fs::remove_dir_all(root).unwrap();
}

#[tokio::test]
async fn cancelled_close_retains_cleanup_and_force_kills_source_after_stdin_eof() {
    let root = test_root("grace");
    let listener = TcpListener::bind(("127.0.0.1", 0)).await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let (sender_tx, sender_rx) = oneshot::channel();
    let (hold_seen_tx, hold_seen_rx) = oneshot::channel();
    let (eof_seen_tx, eof_seen_rx) = oneshot::channel();
    let mut hold_seen_tx = Some(hold_seen_tx);
    let mut eof_seen_tx = Some(eof_seen_tx);
    let handler = tokio::spawn(async move {
        let mut control = accept_ready(&listener).await?;
        serve_modern_startup(&mut control, false, &[vec![tool("idle")]]).await?;
        let _ = sender_tx.send(control.sender.clone());
        loop {
            let message = match control.recv().await {
                Ok(message) => message,
                Err(_) => break,
            };
            match message.get("event").and_then(Value::as_str) {
                Some("hold_after_stdin_eof") => {
                    ensure!(message["enabled"] == true);
                    if let Some(sender) = hold_seen_tx.take() {
                        let _ = sender.send(());
                    }
                }
                Some("stdin_eof") => {
                    if let Some(sender) = eof_seen_tx.take() {
                        let _ = sender.send(());
                    }
                }
                _ => {}
            }
        }
        Ok::<(), anyhow::Error>(())
    });

    let slots = Arc::new(Semaphore::new(1));
    let config = source_config(&root, port, "modern", 120_000, 2_000);
    let (mut source, _) = start_source(config, &root, 1, &slots)
        .await
        .expect("source should start");
    let sender = timeout(WIRE_TIMEOUT, sender_rx)
        .await
        .expect("control sender timed out")
        .expect("control sender");
    sender
        .send(json!({"hold_after_stdin_eof": true}))
        .await
        .expect("hold command");
    timeout(WIRE_TIMEOUT, hold_seen_rx)
        .await
        .expect("hold acknowledgment timed out")
        .expect("hold acknowledgment");

    let mut close = Box::pin(source.close());
    timeout(WIRE_TIMEOUT, async {
        tokio::select! {
            _ = eof_seen_rx => {}
            result = &mut close => panic!("close finished before stdin_eof: {result:?}"),
        }
    })
    .await
    .expect("stdin_eof was not observed");

    drop(close);
    assert_eq!(slots.available_permits(), 0);
    assert!(source.is_closed());
    tokio::time::pause();
    tokio::time::advance(Duration::from_millis(2_100)).await;
    tokio::time::resume();
    timeout(WIRE_TIMEOUT, source.close())
        .await
        .expect("close timed out after grace")
        .expect("close after force kill");
    assert_eq!(slots.available_permits(), 1);
    assert!(handler.await.expect("control handler").is_ok());
    std::fs::remove_dir_all(root).unwrap();
}

#[tokio::test]
async fn rejected_subscription_filter_falls_back_to_fresh_lists_and_ended_subscription_rebuilds() {
    let root = test_root("subscription-recovery");
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let handler = tokio::spawn(async move {
        let mut control = accept_ready(&listener).await?;
        let discover = control.next_request().await?;
        ensure!(discover["method"] == "server/discover");
        control
            .respond(&discover["id"], discover_result(true))
            .await?;
        let first = control.next_request().await?;
        ensure!(first["method"] == "subscriptions/listen");
        let mut invalid = listen_ack(&first["id"]);
        invalid["params"]["notifications"]["promptsListChanged"] = json!(true);
        control.send(invalid).await?;
        serve_tools_pages(&mut control, &[vec![tool("alpha")]], true).await?;

        let second = control.next_request().await?;
        ensure!(second["method"] == "subscriptions/listen");
        control.send(listen_ack(&second["id"])).await?;
        serve_tools_pages(&mut control, &[vec![tool("beta")]], true).await?;
        let barrier = control.next_request().await?;
        ensure!(barrier["method"] == "tools/call");
        control.send(json!({"jsonrpc":"2.0","id":second["id"],"error":{"code":-32603,"message":"subscription ended"}})).await?;
        control
            .respond(
                &barrier["id"],
                json!({"resultType":"complete","content":[],"isError":false}),
            )
            .await?;

        let third = control.next_request().await?;
        ensure!(third["method"] == "subscriptions/listen");
        ensure!(third["id"] != second["id"]);
        control.send(listen_ack(&third["id"])).await?;
        control.send(list_changed(&second["id"])).await?;
        serve_tools_pages(&mut control, &[vec![tool("gamma")]], true).await?;
        loop {
            match control.recv().await {
                Ok(message) if message["event"] == "stdin_eof" => break,
                Ok(_) => {}
                Err(error) if error.to_string() == "control EOF" => break,
                Err(error) => return Err(error),
            }
        }
        Ok::<_, anyhow::Error>(())
    });
    let slots = Arc::new(Semaphore::new(1));
    let config = source_config(&root, port, "modern", 120000, 2000);
    let (mut source, tools) = start_source(config, &root, 1, &slots).await.unwrap();
    assert_eq!(names(&tools), ["alpha"]);
    assert_eq!(source.monitoring(), "polling");
    assert_eq!(names(&source.discover().await.unwrap()), ["beta"]);
    assert_eq!(source.monitoring(), "subscribed");
    source.call("barrier", json!({})).await.unwrap();
    assert_eq!(source.monitoring(), "polling");
    assert_eq!(names(&source.discover().await.unwrap()), ["gamma"]);
    assert_eq!(source.monitoring(), "subscribed");
    source.close().await.unwrap();
    assert_eq!(slots.available_permits(), 1);
    handler.await.unwrap().unwrap();
    std::fs::remove_dir_all(root).unwrap();
}

async fn unconfirmed_subscription_case(wrong_ack: bool) {
    let root = test_root(if wrong_ack {
        "wrong-ack"
    } else {
        "missing-ack"
    });
    let listener = TcpListener::bind(("127.0.0.1", 0)).await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let (listen_seen_tx, listen_seen_rx) = oneshot::channel();
    let handler = tokio::spawn(async move {
        let mut control = accept_ready(&listener).await?;
        let discover = control.next_request().await?;
        ensure!(discover["method"].as_str() == Some("server/discover"));
        control
            .respond(&discover["id"], discover_result(true))
            .await?;

        let first = control.next_request().await?;
        ensure!(first["method"].as_str() == Some("subscriptions/listen"));
        ensure!(
            first["params"]["notifications"] == json!({"toolsListChanged": true}),
            "initial subscription was not tools-only"
        );
        let _ = listen_seen_tx.send(first["id"].clone());
        if wrong_ack {
            let wrong_id = first["id"]
                .as_i64()
                .map(|id| json!(id + 1))
                .unwrap_or_else(|| json!("wrong-subscription"));
            let mut ack = listen_ack(&first["id"]);
            ack["params"]["_meta"]["io.modelcontextprotocol/subscriptionId"] = wrong_id;
            control.send(ack).await?;
        }
        serve_tools_pages(&mut control, &[vec![tool("initial")]], true).await?;

        let second = control.next_request().await?;
        ensure!(second["method"].as_str() == Some("subscriptions/listen"));
        ensure!(
            second["params"]["notifications"] == json!({"toolsListChanged": true}),
            "retry subscription was not tools-only"
        );
        control.send(listen_ack(&second["id"])).await?;
        serve_tools_pages(&mut control, &[vec![tool("refreshed")]], true).await?;
        loop {
            match control.recv().await {
                Ok(message) if message["event"] == "stdin_eof" => break,
                Ok(_) => {}
                Err(error) if error.to_string() == "control EOF" => break,
                Err(error) => return Err(error),
            }
        }
        Ok::<(), anyhow::Error>(())
    });

    let slots = Arc::new(Semaphore::new(1));
    let config = source_config(&root, port, "modern", 120_000, 2_000);
    let permit = slots.clone().try_acquire_owned().unwrap();
    let mut start = Box::pin(SourceProcess::start(config, &root, 1, permit));
    timeout(WIRE_TIMEOUT, async {
        tokio::select! {
            _ = listen_seen_rx => {}
            _ = &mut start => panic!("startup finished before listen"),
        }
    })
    .await
    .expect("listen request was not observed");

    // Do not start the runtime paused: the Python process must first complete the
    // real discover/listen exchange. Only after the listen frame arrives do we
    // move the ACK deadline forward.
    tokio::time::pause();
    tokio::time::advance(Duration::from_millis(1_500)).await;
    tokio::time::resume();

    let (mut source, tools) = timeout(WIRE_TIMEOUT, start)
        .await
        .expect("startup timed out after ACK deadline")
        .expect("startup should finish with polling discovery");
    assert_eq!(names(&tools), ["initial"]);
    assert_eq!(source.monitoring(), "polling");

    let refreshed = timeout(WIRE_TIMEOUT, source.discover())
        .await
        .expect("rediscovery timed out")
        .expect("rediscovery should use a real tools/list");
    assert_eq!(names(&refreshed), ["refreshed"]);

    source.close().await.expect("source close");
    assert_eq!(slots.available_permits(), 1);
    timeout(WIRE_TIMEOUT, handler)
        .await
        .expect("control handler timed out")
        .expect("control handler")
        .expect("control handler result");
    std::fs::remove_dir_all(root).unwrap();
}

#[tokio::test]
async fn missing_listen_ack_keeps_polling_and_rediscovers_from_wire() {
    unconfirmed_subscription_case(false).await;
}

#[tokio::test]
async fn wrong_listen_ack_subscription_id_keeps_polling_and_rediscovers_from_wire() {
    unconfirmed_subscription_case(true).await;
}
