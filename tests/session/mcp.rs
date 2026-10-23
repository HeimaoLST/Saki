use super::*;
use crate::{
    client::{
        ClientConfig,
        connections::{Connection, ConnectionRegistry},
    },
    test_support::scripted_server,
    tools::mcp::{McpTools, config::ToolSourceDefinition, test_support::*},
};
use std::sync::{Arc, Mutex, RwLock};
use tokio::{
    net::TcpListener,
    sync::{mpsc, oneshot},
    task::JoinSet,
};

struct Fixture {
    definition: Value,
    catalog: Arc<RwLock<Vec<Value>>>,
    trace: Arc<Mutex<Vec<(u32, Value)>>>,
    calls: mpsc::Receiver<(u32, Value, ControlSender)>,
    stop: oneshot::Sender<()>,
    worker: JoinHandle<()>,
}

impl Fixture {
    async fn new(hold_calls: bool) -> Self {
        Self::build(hold_calls, false).await
    }

    async fn with_list_changed(hold_calls: bool) -> Self {
        Self::build(hold_calls, true).await
    }

    async fn build(hold_calls: bool, list_changed: bool) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let definition = json!({
            "id":"controlled", "transport":"stdio", "command":python_command(),
            "args":[proxy_script().to_string_lossy(),"--control-port",listener.local_addr().unwrap().port().to_string()],
            "env_from":["MCP_TEST_TOKEN"], "protocol":"modern",
            "startup_timeout_ms":5000,"call_timeout_ms":30000,"cancel_grace_ms":50
        });
        let catalog = Arc::new(RwLock::new(vec![tool("alpha")]));
        let trace = Arc::new(Mutex::new(Vec::new()));
        let (called, calls) = mpsc::channel(16);
        let (stop, mut stopped) = oneshot::channel();
        let worker_catalog = catalog.clone();
        let worker_trace = trace.clone();
        let worker = tokio::spawn(async move {
            let mut sources = JoinSet::new();
            loop {
                tokio::select! {
                    _ = &mut stopped => break,
                    next = sources.join_next(), if !sources.is_empty() => { next.unwrap().unwrap(); },
                    connected = listener.accept() => {
                        assert!(sources.len() < 16, "fixture process limit exceeded");
                        let (socket, _) = connected.unwrap();
                        let catalog = worker_catalog.clone();
                        let trace = worker_trace.clone();
                        let called = called.clone();
                        sources.spawn(async move {
                            let mut control = ControlConnection::from_stream(socket);
                            let ready = control.recv().await.unwrap();
                            assert_eq!(ready["event"], "ready");
                            let pid = ready["pid"].as_u64().unwrap() as u32;
                            let cwd = ready["cwd"].clone();
                            trace.lock().unwrap().push((pid,ready));
                            let mut count = 0;
                            loop {
                                let message = match control.recv().await {
                                    Ok(message) => message,
                                    Err(error) if error.to_string() == "control EOF" => break,
                                    Err(error) => panic!("invalid fixture frame: {error:#}"),
                                };
                                trace.lock().unwrap().push((pid,message.clone()));
                                if message["event"] == "stdin_eof" { break; }
                                let method = message["method"].as_str().unwrap_or("");
                                let result = match method {
                                    "server/discover" => discover_result(list_changed),
                                    "subscriptions/listen" => {
                                        assert_eq!(
                                            message["params"]["notifications"],
                                            json!({"toolsListChanged": true})
                                        );
                                        let ack = listen_ack(&message["id"]);
                                        control.send(ack.clone()).await.unwrap();
                                        trace.lock().unwrap().push((pid, ack));
                                        continue;
                                    }
                                    "tools/list" => tools_page(catalog.read().unwrap().clone(),None,true),
                                    "tools/call" => {
                                        count += 1;
                                        called.send((pid,message.clone(),control.sender.clone())).await.unwrap();
                                        if hold_calls { continue; }
                                        json!({"resultType":"complete","content":[{"type":"text","text":"wire result"}],"structuredContent":{"cwd":cwd,"count":count},"isError":false})
                                    }
                                    "" => continue,
                                    _ if !message["id"].is_null() => panic!("unexpected request: {message}"),
                                    _ => continue,
                                };
                                control.respond(&message["id"],result).await.unwrap();
                            }
                        });
                    }
                }
            }
            while let Some(result) = sources.join_next().await {
                result.unwrap();
            }
        });
        Self {
            definition,
            catalog,
            trace,
            calls,
            stop,
            worker,
        }
    }

    fn sources(&self, root: &std::path::Path, capacity: usize) -> McpTools {
        let definition: ToolSourceDefinition =
            serde_json::from_value(self.definition.clone()).unwrap();
        McpTools::new(vec![definition], capacity, root, |name| {
            (name == "MCP_TEST_TOKEN").then(|| "fixture-token".into())
        })
        .unwrap()
    }

    async fn finish(self) -> Vec<(u32, Value)> {
        self.stop.send(()).unwrap();
        tokio::time::timeout(WIRE_TIMEOUT, self.worker)
            .await
            .unwrap()
            .unwrap();
        Arc::try_unwrap(self.trace).unwrap().into_inner().unwrap()
    }
}

fn selection() -> Value {
    json!({"builtin":[],"external":[{"source_id":"controlled","tool_name":"alpha","exposed_name":"project_alpha"}]})
}

fn runtime(directory: &Directory, base: String, fixture: &Fixture, capacity: usize) -> Runtime {
    let connection = Connection::new(
        "test".into(),
        ClientConfig::new(ModelApi::Responses, "test-key", base),
        "test".into(),
    )
    .unwrap();
    Runtime::new(
        SessionStore::new(directory.0.join("sessions")).unwrap(),
        connection.client().clone(),
        directory.0.clone(),
        "test".into(),
        CompactionConfig::new(262144),
    )
    .with_connections(ConnectionRegistry::new("test".into(), vec![connection]).unwrap())
    .with_tool_sources(Some(fixture.sources(&directory.0, capacity)))
}

async fn create(driver: &mut Driver, cwd: &std::path::Path) -> String {
    let reply = driver.call("session.create",json!({"storage_schema":2,"config":{"working_directory":cwd,"tool_selection":selection()}})).await;
    assert!(reply.get("error").is_none(), "{reply}");
    reply["result"]["session_id"].as_str().unwrap().into()
}

fn tool_call() -> Value {
    json!({"id":"mcp-response","status":"completed","output":[{"type":"function_call","status":"completed","id":"f","call_id":"mcp-call","name":"project_alpha","arguments":"{}"}]})
}

#[derive(Clone, Copy)]
enum Teardown {
    Release,
    Shutdown,
}

fn runtime_with_shared_sources(
    directory: &Directory,
    base: String,
    tools: Arc<McpTools>,
) -> Runtime {
    let connection = Connection::new(
        "test".into(),
        ClientConfig::new(ModelApi::Responses, "test-key", base),
        "test".into(),
    )
    .unwrap();
    let mut runtime = Runtime::new(
        SessionStore::new(directory.0.join("sessions")).unwrap(),
        connection.client().clone(),
        directory.0.clone(),
        "test".into(),
        CompactionConfig::new(262144),
    )
    .with_connections(ConnectionRegistry::new("test".into(), vec![connection]).unwrap());
    runtime.tools = Some(tools);
    runtime
}

fn latest_source_pid(fixture: &Fixture) -> u32 {
    let trace = fixture.trace.lock().unwrap();
    trace
        .iter()
        .rev()
        .find(|(_, value)| value["event"] == "ready")
        .map(|(pid, _)| *pid)
        .expect("source did not announce a pid")
}

fn assert_live_modern_subscription(fixture: &Fixture, pid: u32) {
    let trace = fixture.trace.lock().unwrap();
    let position = |method: &str| {
        trace
            .iter()
            .position(|(owner, value)| *owner == pid && value["method"] == method)
            .unwrap_or_else(|| panic!("source {pid} did not send {method}"))
    };
    let ready = trace
        .iter()
        .position(|(owner, value)| *owner == pid && value["event"] == "ready")
        .expect("source did not send ready");
    let discover = position("server/discover");
    let listen = position("subscriptions/listen");
    let ack = position("notifications/subscriptions/acknowledged");
    let list = position("tools/list");

    assert!(ready < discover && discover < listen, "{trace:?}");
    assert_eq!(
        trace[discover].1["params"]["_meta"]["io.modelcontextprotocol/protocolVersion"],
        json!("2026-07-28")
    );
    assert_eq!(
        trace[listen].1["params"]["notifications"],
        json!({"toolsListChanged": true})
    );
    assert!(listen < ack && ack < list, "{trace:?}");
    assert_eq!(
        trace[ack].1["params"]["_meta"]["io.modelcontextprotocol/subscriptionId"],
        trace[listen].1["id"]
    );
    assert!(
        !trace.iter().any(|(owner, value)| {
            *owner == pid
                && (value["method"] == "initialize" || value["method"] == "notifications/cancelled")
        }),
        "source did not stay on the modern live subscription: {trace:?}"
    );
    assert_eq!(
        unsafe { libc::kill(pid as i32, 0) },
        0,
        "source was not live"
    );
}

fn assert_all_sources_reaped(trace: &[(u32, Value)]) {
    for (pid, _) in trace.iter().filter(|(_, value)| value["event"] == "ready") {
        assert_eq!(
            unsafe { libc::kill(*pid as i32, 0) },
            -1,
            "source {pid} survived teardown"
        );
    }
}

async fn live_subscription_teardown(teardown: Teardown) {
    let directory = Directory::new();
    let fixture = Fixture::with_list_changed(false).await;
    let tools = Arc::new(fixture.sources(&directory.0, 1));
    let mut driver = Driver::from_runtime(runtime_with_shared_sources(
        &directory,
        "http://127.0.0.1:9/v1".into(),
        tools.clone(),
    ))
    .await;
    let id = create(&mut driver, &directory.0).await;
    let pid = latest_source_pid(&fixture);
    assert_live_modern_subscription(&fixture, pid);

    let seen = match teardown {
        Teardown::Release => {
            let released = driver
                .call("session.release", json!({"session_id":id}))
                .await;
            assert!(released.get("error").is_none(), "{released}");
            assert_eq!(released["result"]["session_id"], json!(id));
            assert_eq!(released["result"]["loaded"], false);
            assert_eq!(
                unsafe { libc::kill(pid as i32, 0) },
                -1,
                "release reply preceded source reap"
            );

            let store = SessionStore::new(directory.0.join("sessions")).unwrap();
            drop(
                store
                    .lock_config(&id)
                    .expect("release retained the journal lock"),
            );

            let catalogs = tools
                .list(Some("controlled"), &directory.0)
                .await
                .expect("release leaked a source slot");
            assert_eq!(catalogs.len(), 1);
            assert!(catalogs[0].available, "{:?}", catalogs[0].error);
            assert_eq!(catalogs[0].monitoring, Some("subscribed"));

            let next = create(&mut driver, &directory.0).await;
            assert_ne!(next, id, "released slot was not reusable");
            let released_next = driver
                .call("session.release", json!({"session_id":next}))
                .await;
            assert!(released_next.get("error").is_none(), "{released_next}");
            driver.shutdown().await
        }
        Teardown::Shutdown => {
            let seen = tokio::time::timeout(WIRE_TIMEOUT, driver.shutdown())
                .await
                .expect("runtime shutdown did not complete");
            assert_eq!(
                unsafe { libc::kill(pid as i32, 0) },
                -1,
                "shutdown reply preceded source reap"
            );

            let store = SessionStore::new(directory.0.join("sessions")).unwrap();
            drop(
                store
                    .lock_config(&id)
                    .expect("shutdown retained the journal lock"),
            );

            let catalogs = tools
                .list(Some("controlled"), &directory.0)
                .await
                .expect("shutdown leaked a source slot");
            assert_eq!(catalogs.len(), 1);
            assert!(catalogs[0].available, "{:?}", catalogs[0].error);
            assert_eq!(catalogs[0].monitoring, Some("subscribed"));
            seen
        }
    };
    assert!(
        seen.iter()
            .any(|value| { value["type"] == "response" && value["result"]["state"] == "draining" }),
        "runtime shutdown response was not observed: {seen:?}"
    );
    let trace = fixture.finish().await;
    assert_all_sources_reaped(&trace);
    assert!(
        !trace
            .iter()
            .any(|(_, value)| value["method"] == "tools/call")
    );
}

#[tokio::test]
async fn release_reaps_live_modern_subscription_and_returns_source_capacity() {
    live_subscription_teardown(Teardown::Release).await;
}

#[tokio::test]
async fn shutdown_reaps_live_modern_subscription_and_returns_source_capacity() {
    live_subscription_teardown(Teardown::Shutdown).await;
}

async fn send(driver: &mut Driver, id: &str) -> Value {
    let reply = driver
        .call(
            "task.send",
            json!({"session_id":id,"message":{"text":"use project_alpha"}}),
        )
        .await;
    assert!(reply.get("error").is_none(), "{reply}");
    driver.finish(&reply["result"]).await
}

#[tokio::test]
async fn mcp_results_match_events_and_model_input_and_sources_are_session_owned() {
    let directory = Directory::new();
    let a = directory.0.join("a");
    let b = directory.0.join("b");
    std::fs::create_dir(&a).unwrap();
    std::fs::create_dir(&b).unwrap();
    let fixture = Fixture::new(false).await;
    let responses = (0..3)
        .flat_map(|_| {
            [
                (
                    200,
                    "application/json",
                    serde_json::to_vec(&tool_call()).unwrap(),
                ),
                (
                    200,
                    "application/json",
                    serde_json::to_vec(&completed("done")).unwrap(),
                ),
            ]
        })
        .collect();
    let (base, server) = scripted_server(responses).await;
    let mut driver = Driver::from_runtime(runtime(&directory, base, &fixture, 2)).await;
    let id_a = create(&mut driver, &a).await;
    let id_b = create(&mut driver, &b).await;
    for id in [&id_a, &id_b, &id_a] {
        assert_eq!(send(&mut driver, id).await["event"]["status"], "completed");
    }
    let wire = requests(server).await;
    let outputs: Vec<_> = driver
        .seen
        .iter()
        .filter(|v| v["event"]["type"] == "tool_finished")
        .map(|v| v["event"]["output"].clone())
        .collect();
    assert_eq!(outputs.len(), 3);
    for (index, cwd, count) in [(0, &a, 1), (1, &b, 1), (2, &a, 2)] {
        assert_eq!(
            outputs[index]["structuredContent"]["cwd"],
            cwd.canonicalize().unwrap().to_string_lossy().as_ref()
        );
        assert_eq!(outputs[index]["structuredContent"]["count"], count);
        let input = wire[index * 2 + 1]["input"]
            .as_array()
            .unwrap()
            .iter()
            .rev()
            .find(|item| item["type"] == "function_call_output")
            .unwrap();
        assert_eq!(
            serde_json::from_str::<Value>(input["output"].as_str().unwrap()).unwrap(),
            outputs[index]
        );
    }
    let metadata: Value = serde_json::from_slice(
        &std::fs::read(
            directory
                .0
                .join("sessions")
                .join(&id_a)
                .join("metadata.json"),
        )
        .unwrap(),
    )
    .unwrap();
    assert!(
        !metadata["tool_schemas"]["project_alpha"]["fingerprint"]
            .as_str()
            .unwrap()
            .is_empty()
    );
    driver
        .call("session.release", json!({"session_id":id_a}))
        .await;
    driver.shutdown().await;
    let trace = fixture.finish().await;
    let pids: std::collections::BTreeSet<_> = trace
        .iter()
        .filter(|(_, v)| v["event"] == "ready")
        .map(|(p, _)| *p)
        .collect();
    assert_eq!(pids.len(), 2);
    for pid in pids {
        assert_eq!(
            unsafe { libc::kill(pid as i32, 0) },
            -1,
            "source survived release/shutdown"
        );
    }
}

#[derive(Clone, Copy)]
enum SelectedSchemaChange {
    Description,
    Input,
    Output,
}

async fn changed_selected_schema_case(change: SelectedSchemaChange) {
    let directory = Directory::new();
    let fixture = Fixture::new(false).await;
    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    listener.set_nonblocking(true).unwrap();
    let mut driver = Driver::from_runtime(runtime(
        &directory,
        format!("http://{}/v1", listener.local_addr().unwrap()),
        &fixture,
        1,
    ))
    .await;
    let id = create(&mut driver, &directory.0).await;
    let folder = directory.0.join("sessions").join(&id);
    let metadata = std::fs::read(folder.join("metadata.json")).unwrap();
    let history = std::fs::read(folder.join("history.jsonl")).unwrap();
    fixture.catalog.write().unwrap().push(tool("unselected"));
    let config = driver
        .call("session.config.get", json!({"session_id":id}))
        .await;
    let revision = config["result"]["config_revision"].clone();

    let mut changed = tool("alpha");
    match change {
        SelectedSchemaChange::Description => {
            changed["description"] = json!("changed selected description");
        }
        SelectedSchemaChange::Input => {
            changed["inputSchema"] = json!({
                "type": "object",
                "properties": {"value": {"type": "string"}},
                "required": ["value"],
                "additionalProperties": false
            });
        }
        SelectedSchemaChange::Output => {
            changed["outputSchema"] = json!({
                "type": "object",
                "properties": {"status": {"type": "string"}},
                "required": ["status"],
                "additionalProperties": false
            });
        }
    }
    let expected_description = changed["description"].clone();
    let expected_input = changed["inputSchema"].clone();
    let expected_output = changed.get("outputSchema").cloned().unwrap_or(Value::Null);
    fixture.catalog.write().unwrap()[0] = changed;

    let refused = driver
        .call(
            "task.send",
            json!({"session_id":id,"message":{"text":"must not run"}}),
        )
        .await;
    assert_eq!(refused["error"]["code"], "tool_schema_changed");
    assert_eq!(
        std::fs::read(folder.join("metadata.json")).unwrap(),
        metadata
    );
    assert_eq!(
        std::fs::read(folder.join("history.jsonl")).unwrap(),
        history
    );
    assert_eq!(
        listener.accept().unwrap_err().kind(),
        std::io::ErrorKind::WouldBlock
    );
    let list = driver.call("session.list", json!({})).await;
    assert_eq!(list["result"]["sessions"][0]["state"], "unavailable");

    let confirmed=driver.call("session.config.update",json!({"session_id":id,"expected_revision":revision,"patch":{"tool_selection":selection()}})).await;
    assert!(confirmed.get("error").is_none(), "{confirmed}");
    assert_eq!(
        confirmed["result"]["config_revision"].as_u64().unwrap(),
        revision.as_u64().unwrap() + 1
    );
    assert_eq!(
        std::fs::read(folder.join("history.jsonl")).unwrap(),
        history
    );
    assert_eq!(
        listener.accept().unwrap_err().kind(),
        std::io::ErrorKind::WouldBlock
    );

    let metadata: Value =
        serde_json::from_slice(&std::fs::read(folder.join("metadata.json")).unwrap()).unwrap();
    let schema = &metadata["tool_schemas"]["project_alpha"];
    assert_eq!(schema["description"], expected_description);
    assert_eq!(schema["input_schema"], expected_input);
    assert_eq!(schema["output_schema"], expected_output);
    assert_eq!(
        confirmed["result"]["effective_config"]["tool_schemas"]["project_alpha"]["description"],
        expected_description
    );
    assert_eq!(
        confirmed["result"]["effective_config"]["tool_schemas"]["project_alpha"]["input_schema"],
        expected_input
    );
    assert_eq!(
        confirmed["result"]["effective_config"]["tool_schemas"]["project_alpha"]["output_schema"],
        expected_output
    );

    let state = driver
        .call("session.status", json!({"session_id":id}))
        .await;
    assert_eq!(state["result"]["state"], "idle");
    driver.shutdown().await;
    let trace = fixture.finish().await;
    assert!(
        !trace
            .iter()
            .any(|(_, value)| value["method"] == "tools/call")
    );
}

#[tokio::test]
async fn changed_selected_schema_blocks_admission_without_writes_until_explicit_confirmation() {
    changed_selected_schema_case(SelectedSchemaChange::Description).await;
}

#[tokio::test]
async fn changed_selected_input_schema_blocks_admission_until_explicit_confirmation() {
    changed_selected_schema_case(SelectedSchemaChange::Input).await;
}

#[tokio::test]
async fn changed_selected_output_schema_blocks_admission_until_explicit_confirmation() {
    changed_selected_schema_case(SelectedSchemaChange::Output).await;
}

#[tokio::test]
async fn tool_process_capacity_rejects_creation_before_store_and_release_returns_capacity() {
    let directory = Directory::new();
    let fixture = Fixture::new(false).await;
    let mut driver = Driver::from_runtime(runtime(
        &directory,
        "http://127.0.0.1:9/v1".into(),
        &fixture,
        1,
    ))
    .await;
    let id = create(&mut driver, &directory.0).await;
    let rejected = driver
        .call(
            "session.create",
            json!({"storage_schema":2,"config":{"tool_selection":selection()}}),
        )
        .await;
    assert_eq!(rejected["error"]["code"], "tool_capacity_exceeded");
    assert_eq!(
        std::fs::read_dir(directory.0.join("sessions"))
            .unwrap()
            .count(),
        1
    );
    driver
        .call("session.release", json!({"session_id":id}))
        .await;
    let next = create(&mut driver, &directory.0).await;
    assert_ne!(id, next);
    driver.shutdown().await;
    let trace = fixture.finish().await;
    assert_eq!(
        trace.iter().filter(|(_, v)| v["event"] == "ready").count(),
        2
    );
}

#[tokio::test]
async fn cancellation_reaps_inflight_source_before_terminal_and_does_not_replay_the_call() {
    let directory = Directory::new();
    let mut fixture = Fixture::new(true).await;
    let (base, server) = scripted_server(vec![(
        200,
        "application/json",
        serde_json::to_vec(&tool_call()).unwrap(),
    )])
    .await;
    let mut driver = Driver::from_runtime(runtime(&directory, base, &fixture, 1)).await;
    let id = create(&mut driver, &directory.0).await;
    let accepted = driver
        .call(
            "task.send",
            json!({"session_id":id,"message":{"text":"call and wait"}}),
        )
        .await;
    let (pid, call, _) = tokio::time::timeout(WIRE_TIMEOUT, fixture.calls.recv())
        .await
        .unwrap()
        .unwrap();
    let cancelled = driver
        .call(
            "task.cancel",
            json!({"session_id":id,"task_id":accepted["result"]["task_id"]}),
        )
        .await;
    assert!(cancelled.get("error").is_none(), "{cancelled}");
    assert_eq!(
        driver.finish(&accepted["result"]).await["event"]["status"],
        "interrupted"
    );
    assert_eq!(
        unsafe { libc::kill(pid as i32, 0) },
        -1,
        "terminal preceded process reap"
    );
    assert_eq!(requests(server).await.len(), 1);
    driver.shutdown().await;
    let trace = fixture.finish().await;
    assert_eq!(
        trace
            .iter()
            .filter(|(_, v)| v["method"] == "tools/call")
            .count(),
        1
    );
    let cancellations: Vec<_> = trace
        .iter()
        .filter(|(_, v)| {
            v["method"] == "notifications/cancelled" && v["params"]["requestId"] == call["id"]
        })
        .collect();
    assert_eq!(cancellations.len(), 1);
}

#[tokio::test]
async fn restore_without_source_keeps_readonly_history_and_allows_loaded_configuration_repair() {
    let directory = Directory::new();
    let fixture = Fixture::new(false).await;
    let (base, server) = scripted_server(vec![(
        200,
        "application/json",
        serde_json::to_vec(&completed("repaired")).unwrap(),
    )])
    .await;
    let mut driver = Driver::from_runtime(runtime(&directory, base.clone(), &fixture, 1)).await;
    let id = create(&mut driver, &directory.0).await;
    driver.shutdown().await;
    let history_path = directory.0.join("sessions").join(&id).join("history.jsonl");
    let original = std::fs::read(&history_path).unwrap();
    let mut driver =
        Driver::from_runtime(runtime(&directory, base, &fixture, 1).with_tool_sources(None)).await;
    let history = driver
        .call("session.history", json!({"session_id":id}))
        .await;
    assert!(history.get("error").is_none(), "{history}");
    let loaded = driver.call("session.load", json!({"session_id":id})).await;
    assert_eq!(loaded["error"]["code"], "tool_source_unavailable");
    let state = driver
        .call("session.status", json!({"session_id":id}))
        .await;
    assert_eq!(state["result"]["state"], "unavailable");
    assert_eq!(state["result"]["loaded"], true);
    assert_eq!(std::fs::read(&history_path).unwrap(), original);
    let repaired=driver.call("session.config.update",json!({"session_id":id,"expected_revision":1,"patch":{"tool_selection":{"builtin":[],"external":[]}}})).await;
    assert!(repaired.get("error").is_none(), "{repaired}");
    assert_eq!(repaired["result"]["config_revision"], 2);
    let sent = driver
        .call(
            "task.send",
            json!({"session_id":id,"message":{"text":"after repair"}}),
        )
        .await;
    assert_eq!(
        driver.finish(&sent["result"]).await["event"]["status"],
        "completed"
    );
    assert_eq!(requests(server).await.len(), 1);
    driver.shutdown().await;
    let trace = fixture.finish().await;
    assert_eq!(
        trace.iter().filter(|(_, v)| v["event"] == "ready").count(),
        1,
        "readonly/load/repair launched an unavailable source"
    );
    assert!(!trace.iter().any(|(_, v)| v["method"] == "tools/call"));
}

#[tokio::test]
async fn worker_panic_reaps_owned_source_before_terminal_and_releases_session_lock() {
    let directory = Directory::new();
    let fixture = Fixture::new(false).await;
    let base = "http://127.0.0.1:9/v1".to_owned();
    let mut driver = Driver::from_runtime(runtime(&directory, base.clone(), &fixture, 1)).await;
    let id = create(&mut driver, &directory.0).await;
    driver.shutdown().await;
    let mut driver = Driver::from_runtime(
        runtime(&directory, base, &fixture, 1).with_panicking_session(id.clone()),
    )
    .await;
    let loaded = driver.call("session.load", json!({"session_id":id})).await;
    assert!(loaded.get("error").is_none(), "{loaded}");
    let pid = fixture
        .trace
        .lock()
        .unwrap()
        .iter()
        .rev()
        .find(|(_, v)| v["event"] == "ready")
        .unwrap()
        .0;
    let sent = driver
        .call(
            "task.send",
            json!({"session_id":id,"message":{"text":"panic"}}),
        )
        .await;
    let terminal = driver.finish(&sent["result"]).await;
    assert_eq!(terminal["event"]["status"], "failed");
    assert_eq!(terminal["event"]["persistence"], "unconfirmed");
    assert_eq!(
        unsafe { libc::kill(pid as i32, 0) },
        -1,
        "worker panic terminal preceded source cleanup"
    );
    let store = SessionStore::new(directory.0.join("sessions")).unwrap();
    drop(store.open(&id).expect("panic must release writer lock"));
    driver.shutdown().await;
    fixture.finish().await;
}

#[tokio::test]
async fn failed_tool_preparation_preserves_configuration_and_reclaims_new_processes() {
    let directory = Directory::new();
    let fixture = Fixture::new(false).await;
    let mut driver = Driver::from_runtime(runtime(
        &directory,
        "http://127.0.0.1:9/v1".into(),
        &fixture,
        1,
    ))
    .await;
    let created = driver
        .call(
            "session.create",
            json!({"storage_schema":2,"config":{"tool_selection":{"builtin":[],"external":[]}}}),
        )
        .await;
    let id = created["result"]["session_id"].as_str().unwrap();
    let folder = directory.0.join("sessions").join(id);
    let metadata = std::fs::read(folder.join("metadata.json")).unwrap();
    let history = std::fs::read(folder.join("history.jsonl")).unwrap();
    let mut invalid = selection();
    invalid["external"]
        .as_array_mut()
        .unwrap()
        .push(json!({"source_id":"missing","tool_name":"beta","exposed_name":"zzz_missing"}));
    let failed = driver
        .call(
            "session.config.update",
            json!({"session_id":id,"expected_revision":1,"patch":{"tool_selection":invalid}}),
        )
        .await;
    assert_eq!(failed["error"]["code"], "tool_source_unavailable");
    let first_pid = fixture
        .trace
        .lock()
        .unwrap()
        .iter()
        .find(|(_, v)| v["event"] == "ready")
        .unwrap()
        .0;
    assert_eq!(
        unsafe { libc::kill(first_pid as i32, 0) },
        -1,
        "preparation failure leaked new process"
    );
    assert_eq!(
        std::fs::read(folder.join("metadata.json")).unwrap(),
        metadata
    );
    assert_eq!(
        std::fs::read(folder.join("history.jsonl")).unwrap(),
        history
    );
    fixture.catalog.write().unwrap()[0]["inputSchema"] = json!({"type":42});
    let invalid_schema = driver
        .call(
            "session.config.update",
            json!({"session_id":id,"expected_revision":1,"patch":{"tool_selection":selection()}}),
        )
        .await;
    assert_eq!(invalid_schema["error"]["code"], "tool_source_unavailable");
    assert_eq!(
        std::fs::read(folder.join("metadata.json")).unwrap(),
        metadata
    );
    let mut conflict = selection();
    conflict["external"][0]["exposed_name"] = json!("read");
    let bad_name = driver
        .call(
            "session.config.update",
            json!({"session_id":id,"expected_revision":1,"patch":{"tool_selection":conflict}}),
        )
        .await;
    assert_eq!(bad_name["error"]["code"], "invalid_tool_selection");
    fixture.catalog.write().unwrap()[0] = tool("alpha");
    let repaired = driver
        .call(
            "session.config.update",
            json!({"session_id":id,"expected_revision":1,"patch":{"tool_selection":selection()}}),
        )
        .await;
    assert!(repaired.get("error").is_none(), "{repaired}");
    driver.shutdown().await;
    let trace = fixture.finish().await;
    assert_eq!(
        trace.iter().filter(|(_, v)| v["event"] == "ready").count(),
        3,
        "invalid name must be rejected before process startup"
    );
    for (pid, _) in trace.iter().filter(|(_, v)| v["event"] == "ready") {
        assert_eq!(unsafe { libc::kill(*pid as i32, 0) }, -1);
    }
}

#[tokio::test]
async fn crashed_business_call_is_not_replayed_and_only_next_task_reconnects() {
    let directory = Directory::new();
    let mut fixture = Fixture::new(true).await;
    let replies = vec![
        tool_call(),
        completed("reported tool failure"),
        completed("next task without a call"),
    ];
    let (base, server) = scripted_server(
        replies
            .into_iter()
            .map(|reply| (200, "application/json", serde_json::to_vec(&reply).unwrap()))
            .collect(),
    )
    .await;
    let mut driver = Driver::from_runtime(runtime(&directory, base, &fixture, 1)).await;
    let id = create(&mut driver, &directory.0).await;
    let accepted = driver
        .call(
            "task.send",
            json!({"session_id":id,"message":{"text":"first call"}}),
        )
        .await;
    let (pid, _, _) = tokio::time::timeout(WIRE_TIMEOUT, fixture.calls.recv())
        .await
        .unwrap()
        .unwrap();
    assert_eq!(unsafe { libc::kill(pid as i32, libc::SIGKILL) }, 0);
    assert_eq!(
        driver.finish(&accepted["result"]).await["event"]["status"],
        "completed"
    );
    let tool = driver
        .seen
        .iter()
        .find(|event| {
            event["task_id"] == accepted["result"]["task_id"]
                && event["event"]["type"] == "tool_finished"
        })
        .unwrap()["event"]["output"]
        .clone();
    assert!(tool["error"].as_str().is_some());
    assert_eq!(
        fixture
            .trace
            .lock()
            .unwrap()
            .iter()
            .filter(|(_, v)| v["event"] == "ready")
            .count(),
        1,
        "failed business call was silently restarted"
    );
    assert_eq!(
        unsafe { libc::kill(pid as i32, 0) },
        -1,
        "failed-call cleanup must precede model continuation"
    );
    assert_eq!(send(&mut driver, &id).await["event"]["status"], "completed");
    let wire = requests(server).await;
    let replay = wire[1]["input"]
        .as_array()
        .unwrap()
        .iter()
        .find(|item| item["type"] == "function_call_output")
        .unwrap();
    assert_eq!(
        serde_json::from_str::<Value>(replay["output"].as_str().unwrap()).unwrap(),
        tool
    );
    driver.shutdown().await;
    let trace = fixture.finish().await;
    assert_eq!(
        trace.iter().filter(|(_, v)| v["event"] == "ready").count(),
        2,
        "new task did not prepare a fresh process"
    );
    assert_eq!(
        trace
            .iter()
            .filter(|(_, v)| v["method"] == "tools/call")
            .count(),
        1,
        "business side effect was replayed"
    );
}

#[tokio::test]
async fn global_discovery_is_ephemeral_and_noop_confirmation_preserves_revision() {
    let directory = Directory::new();
    let fixture = Fixture::new(false).await;
    let mut driver = Driver::from_runtime(runtime(
        &directory,
        "http://127.0.0.1:9/v1".into(),
        &fixture,
        1,
    ))
    .await;
    let listed = driver
        .call("tool.list", json!({"source_id":"controlled"}))
        .await;
    assert_eq!(listed["result"]["sources"][0]["available"], true);
    assert_eq!(listed["result"]["sources"][0]["monitoring"], "polling");
    assert_eq!(listed["result"]["sources"][0]["tools"][0]["name"], "alpha");
    let pid = fixture
        .trace
        .lock()
        .unwrap()
        .iter()
        .find(|(_, v)| v["event"] == "ready")
        .unwrap()
        .0;
    assert_eq!(
        unsafe { libc::kill(pid as i32, 0) },
        -1,
        "tool.list replied before reclaiming source"
    );
    let id = create(&mut driver, &directory.0).await;
    fixture.catalog.write().unwrap().push(tool("unselected"));
    let confirmed = driver
        .call(
            "session.config.update",
            json!({"session_id":id,"expected_revision":1,"patch":{"tool_selection":selection()}}),
        )
        .await;
    assert_eq!(
        confirmed["result"]["config_revision"], 1,
        "unselected change or identical selection bumped revision"
    );
    let full = driver
        .call("tool.list", json!({"source_id":"controlled"}))
        .await;
    assert_eq!(full["result"]["sources"][0]["available"], false);
    assert!(
        full["result"]["sources"][0]["error"]
            .as_str()
            .unwrap()
            .contains("capacity")
    );
    driver.shutdown().await;
    let trace = fixture.finish().await;
    assert_eq!(
        trace.iter().filter(|(_, v)| v["event"] == "ready").count(),
        2
    );
    assert!(!trace.iter().any(|(_, v)| v["method"] == "tools/call"));
}

#[tokio::test]
async fn cancelling_one_session_with_an_inflight_mcp_call_preserves_the_other_call() {
    let directory = Directory::new();
    let mut a = Fixture::new(true).await;
    let mut b = Fixture::new(true).await;
    let (base, server) = scripted_server(vec![
        (
            200,
            "application/json",
            serde_json::to_vec(&tool_call()).unwrap(),
        ),
        (
            200,
            "application/json",
            serde_json::to_vec(&tool_call()).unwrap(),
        ),
        (
            200,
            "application/json",
            serde_json::to_vec(&completed("B survived A cancellation")).unwrap(),
        ),
    ])
    .await;
    let def_a: ToolSourceDefinition = serde_json::from_value(a.definition.clone()).unwrap();
    let mut definition_b = b.definition.clone();
    definition_b["id"] = json!("continuing");
    let def_b: ToolSourceDefinition = serde_json::from_value(definition_b).unwrap();
    let sources = McpTools::new(vec![def_a, def_b], 2, &directory.0, |name| {
        (name == "MCP_TEST_TOKEN").then(|| "fixture-token".into())
    })
    .unwrap();
    let mut driver = Driver::from_runtime(
        runtime(&directory, base, &a, 2)
            .with_tool_sources(Some(sources))
            .with_max_active_tasks(2)
            .unwrap(),
    )
    .await;
    let id_a = create(&mut driver, &directory.0).await;
    let mut selection_b = selection();
    selection_b["external"][0]["source_id"] = json!("continuing");
    let created_b = driver
        .call(
            "session.create",
            json!({"storage_schema":2,"config":{"tool_selection":selection_b}}),
        )
        .await;
    let id_b = created_b["result"]["session_id"].as_str().unwrap();
    let task_a = driver
        .call(
            "task.send",
            json!({"session_id":id_a,"message":{"text":"A held call"}}),
        )
        .await;
    let task_b = driver
        .call(
            "task.send",
            json!({"session_id":id_b,"message":{"text":"B held call"}}),
        )
        .await;
    let (pid_a, _, _) = tokio::time::timeout(WIRE_TIMEOUT, a.calls.recv())
        .await
        .unwrap()
        .unwrap();
    let (pid_b, call_b, respond_b) = tokio::time::timeout(WIRE_TIMEOUT, b.calls.recv())
        .await
        .unwrap()
        .unwrap();
    assert_ne!(pid_a, pid_b);
    driver
        .call(
            "task.cancel",
            json!({"session_id":id_a,"task_id":task_a["result"]["task_id"]}),
        )
        .await;
    assert_eq!(
        driver.finish(&task_a["result"]).await["event"]["status"],
        "interrupted"
    );
    assert_eq!(unsafe { libc::kill(pid_a as i32, 0) }, -1);
    assert_eq!(
        unsafe { libc::kill(pid_b as i32, 0) },
        0,
        "A cancellation killed B source"
    );
    let state_b = driver
        .call("session.status", json!({"session_id":id_b}))
        .await;
    assert_eq!(state_b["result"]["state"], "running");
    respond_b.send(json!({"send":{"jsonrpc":"2.0","id":call_b["id"],"result":{"resultType":"complete","content":[{"type":"text","text":"B-result"}],"isError":false}}})).await.unwrap();
    assert_eq!(
        driver.finish(&task_b["result"]).await["event"]["status"],
        "completed"
    );
    assert_eq!(requests(server).await.len(), 3);
    driver.shutdown().await;
    let trace_a = a.finish().await;
    let trace_b = b.finish().await;
    assert_eq!(
        trace_a
            .iter()
            .filter(|(_, v)| v["method"] == "tools/call")
            .count(),
        1
    );
    assert_eq!(
        trace_b
            .iter()
            .filter(|(_, v)| v["method"] == "tools/call")
            .count(),
        1
    );
    assert!(
        !trace_b
            .iter()
            .any(|(_, v)| v["method"] == "notifications/cancelled"
                && v["params"]["requestId"] == call_b["id"])
    );
}

#[tokio::test]
async fn output_loss_during_mcp_call_keeps_real_result_and_drains_sources_without_cancellation() {
    let directory = Directory::new();
    let mut fixture = Fixture::new(true).await;
    let (base, server) = scripted_server(vec![
        (
            200,
            "application/json",
            serde_json::to_vec(&tool_call()).unwrap(),
        ),
        (
            200,
            "application/json",
            serde_json::to_vec(&completed("completed after output loss")).unwrap(),
        ),
    ])
    .await;
    let (handle, actor) = runtime(&directory, base, &fixture, 1).start();
    let state = match handle
        .call(RuntimeCommand::CreateConfigured {
            storage_schema: 2,
            config: serde_json::from_value(json!({"tool_selection":selection()})).unwrap(),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Session(state) => state,
        _ => panic!("expected session"),
    };
    let task = match handle
        .call(RuntimeCommand::Send {
            session_id: state.session_id.clone(),
            message: UserMessage::text("held tool before output loss"),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Accepted(task) => task,
        _ => panic!("expected acceptance"),
    };
    let (pid, call, respond) = tokio::time::timeout(WIRE_TIMEOUT, fixture.calls.recv())
        .await
        .unwrap()
        .unwrap();
    handle.output_state().close();
    assert_eq!(
        unsafe { libc::kill(pid as i32, 0) },
        0,
        "display loss cancelled ongoing business work"
    );
    respond.send(json!({"send":{"jsonrpc":"2.0","id":call["id"],"result":{"resultType":"complete","content":[{"type":"text","text":"real-output-loss-result"}],"isError":false}}})).await.unwrap();
    let mut events = task.events;
    let mut terminal = None;
    while let Some(event) = events.next().await {
        if let TaskEvent::Finished(finished) = event {
            assert!(terminal.is_none());
            terminal = Some(finished);
        }
    }
    assert_eq!(terminal.unwrap().status, "completed");
    tokio::time::timeout(WIRE_TIMEOUT, actor)
        .await
        .unwrap()
        .unwrap()
        .unwrap();
    assert_eq!(
        unsafe { libc::kill(pid as i32, 0) },
        -1,
        "drained actor left tool process running"
    );
    let wire = requests(server).await;
    assert_eq!(wire.len(), 2);
    assert!(
        wire[1]["input"]
            .to_string()
            .contains("real-output-loss-result")
    );
    let store = SessionStore::new(directory.0.join("sessions")).unwrap();
    let restored = store.open(&state.session_id).unwrap();
    assert!(matches!(restored.replay.status, TaskStatus::Completed));
    let text = std::fs::read_to_string(
        directory
            .0
            .join("sessions")
            .join(&state.session_id)
            .join("history.jsonl"),
    )
    .unwrap();
    assert!(text.contains("real-output-loss-result"));
    assert!(!text.contains("\"status\":\"interrupted\""));
    drop(restored);
    drop(handle);
    let trace = fixture.finish().await;
    assert!(
        !trace
            .iter()
            .any(|(_, v)| v["method"] == "notifications/cancelled"
                && v["params"]["requestId"] == call["id"])
    );
}
