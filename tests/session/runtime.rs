use super::*;
use crate::test_support::{completed, read_request, requests, scripted_http};
use tokio::io::{BufReader, DuplexStream, Lines};

struct Directory(PathBuf);
impl Directory {
    fn new() -> Self {
        static NEXT: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
        let path = std::env::temp_dir().join(format!(
            "saki-runtime-{}-{}",
            journal::new_id().unwrap(),
            NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        ));
        std::fs::create_dir(&path).unwrap();
        Self(path)
    }
}
impl Drop for Directory {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}
struct Driver {
    input: DuplexStream,
    output: Lines<BufReader<DuplexStream>>,
    worker: JoinHandle<anyhow::Result<()>>,
    seen: Vec<Value>,
    next_id: usize,
}
impl Driver {
    async fn new(directory: &Directory, client: ModelClient) -> Self {
        let runtime = Runtime::new(
            SessionStore::new(directory.0.join("sessions")).unwrap(),
            client,
            directory.0.clone(),
            "test".into(),
            CompactionConfig::new(65536),
        );
        Self::from_runtime(runtime).await
    }
    async fn from_runtime(runtime: Runtime) -> Self {
        let (input, runtime_input) = tokio::io::duplex(65536);
        let (runtime_output, output) = tokio::io::duplex(65536);
        let worker = tokio::spawn(runtime.run(BufReader::new(runtime_input), runtime_output));
        let mut driver = Self {
            input,
            output: BufReader::new(output).lines(),
            worker,
            seen: Vec::new(),
            next_id: 0,
        };
        assert_eq!(driver.next().await["type"], "ready");
        driver
    }
    async fn next(&mut self) -> Value {
        let line = tokio::time::timeout(std::time::Duration::from_secs(5), self.output.next_line())
            .await
            .unwrap()
            .unwrap()
            .expect("runtime disconnected");
        let value: Value = serde_json::from_str(&line).unwrap();
        self.seen.push(value.clone());
        value
    }
    async fn call(&mut self, method: &str, params: Value) -> Value {
        self.next_id += 1;
        let id = self.next_id.to_string();
        let mut bytes =
            serde_json::to_vec(&json!({"version":1,"id":id,"method":method,"params":params}))
                .unwrap();
        bytes.push(b'\n');
        self.input.write_all(&bytes).await.unwrap();
        loop {
            let value = self.next().await;
            if value["id"] == id {
                return value;
            }
        }
    }
    async fn create(&mut self) -> String {
        self.call("session.create", json!({})).await["result"]["session_id"]
            .as_str()
            .unwrap()
            .into()
    }
    async fn finish(&mut self, task: &Value) -> Value {
        loop {
            let value = self.next().await;
            if value["task_id"] == task["task_id"] && value["event"]["type"] == "task_finished" {
                return value;
            }
        }
    }
    async fn shutdown(mut self) -> Vec<Value> {
        assert!(
            self.call("runtime.shutdown", json!({}))
                .await
                .get("result")
                .is_some()
        );
        while let Some(line) = self.output.next_line().await.unwrap() {
            self.seen.push(serde_json::from_str(&line).unwrap());
        }
        self.worker.await.unwrap().unwrap();
        self.seen
    }
}

#[tokio::test]
async fn real_read_tool_history_and_restart_keep_task_identity_without_reexecution() {
    let directory = Directory::new();
    std::fs::write(directory.0.join("probe.txt"), "marker-60").unwrap();
    let tool = json!({"id":"tool-response","status":"completed","output":[{"type":"function_call","status":"completed","id":"f","call_id":"read-60","name":"read","arguments":"{\"path\":\"probe.txt\"}"}]});
    let (client, server) = scripted_http(vec![(200, tool), (200, completed("marker-60"))]).await;
    let mut driver = Driver::new(&directory, client).await;
    assert!(
        std::fs::read_dir(directory.0.join("sessions"))
            .unwrap()
            .next()
            .is_none()
    );
    let id = driver.create().await;
    let accepted = driver
        .call(
            "task.send",
            json!({"session_id":id,"message":{"text":"read probe.txt"}}),
        )
        .await;
    let task = &accepted["result"];
    assert_eq!(driver.finish(task).await["event"]["status"], "completed");
    let history = driver
        .call("session.history", json!({"session_id":id,"limit":2}))
        .await;
    assert_eq!(history["result"]["next_offset"], 2);
    assert_eq!(
        history["result"]["records"][1]["record"]["task_id"],
        task["task_id"]
    );
    assert_eq!(
        history["result"]["records"][1]["record"]["message_id"],
        task["message_id"]
    );
    let seen = driver.shutdown().await;
    let acceptance = seen.iter().position(|v| v == &accepted).unwrap();
    let started = seen
        .iter()
        .position(|v| v["event"]["type"] == "task_started")
        .unwrap();
    assert!(acceptance < started);
    let events: Vec<_> = seen.iter().filter(|v| v["type"] == "event").collect();
    assert!(events.iter().all(|v| v["task_id"] == task["task_id"]
        && v["message_id"] == task["message_id"]
        && v["session_id"] == id));
    assert_eq!(
        events
            .iter()
            .filter(|v| v["event"]["type"] == "task_finished")
            .count(),
        1
    );
    assert!(events.iter().any(|v| v["event"]["type"] == "tool_finished"
        && v["event"]["output"].to_string().contains("marker-60")));
    let wire = requests(server).await;
    assert_eq!(wire.len(), 2);
    assert!(wire[1]["input"].to_string().contains("marker-60"));
    // A listener with one expected continuation proves load/history add no requests:
    // the only received request must contain the newly submitted text.
    let (client, server) = scripted_http(vec![(200, completed("continued"))]).await;
    let mut driver = Driver::new(&directory, client).await;
    let loaded = driver.call("session.load", json!({"session_id":id})).await;
    assert_eq!(loaded["result"]["loaded"], true);
    assert_eq!(loaded["result"]["status"], "completed");
    let history = driver
        .call("session.history", json!({"session_id":id}))
        .await;
    assert!(
        history["result"]["records"]
            .to_string()
            .contains("marker-60")
    );
    let task = driver
        .call(
            "task.send",
            json!({"session_id":id,"message":{"text":"continue-new-60"}}),
        )
        .await["result"]
        .clone();
    assert_eq!(driver.finish(&task).await["event"]["status"], "completed");
    driver.shutdown().await;
    let wire = requests(server).await;
    assert_eq!(wire.len(), 1);
    assert!(wire[0]["input"].to_string().contains("continue-new-60"));
    assert!(wire[0]["input"].to_string().contains("marker-60"));
}

async fn paused_model() -> (
    ModelClient,
    tokio::sync::oneshot::Receiver<()>,
    tokio::sync::oneshot::Sender<()>,
    JoinHandle<Value>,
) {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (arrived_tx, arrived) = tokio::sync::oneshot::channel();
    let (release, release_rx) = tokio::sync::oneshot::channel();
    let worker = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
        let request = read_request(&mut socket).await;
        arrived_tx.send(()).unwrap();
        let _ = release_rx.await;
        let body = serde_json::to_vec(&completed("done")).unwrap();
        let head = format!(
            "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
            body.len()
        );
        let _ = socket.write_all(head.as_bytes()).await;
        let _ = socket.write_all(&body).await;
        request
    });
    (
        ModelClient::for_test(format!("http://{address}/v1")),
        arrived,
        release,
        worker,
    )
}

#[tokio::test]
async fn active_a_allows_real_load_of_b_and_cancel_is_directed_and_unique() {
    let directory = Directory::new();
    let (client, arrived, release, server) = paused_model().await;
    let mut driver = Driver::new(&directory, client).await;
    let a = driver.create().await;
    let b = driver.create().await;
    driver
        .call("session.release", json!({"session_id":b}))
        .await;
    let task = driver
        .call(
            "task.send",
            json!({"session_id":a,"message":{"text":"pause"}}),
        )
        .await["result"]
        .clone();
    arrived.await.unwrap();
    for session_id in [&b, "missing"] {
        assert_eq!(
            driver
                .call(
                    "task.send",
                    json!({"session_id":session_id,"message":{"text":"not loaded"}})
                )
                .await["error"]["code"],
            "not_found"
        );
    }
    assert_eq!(
        driver.call("session.load", json!({"session_id":b})).await["result"]["loaded"],
        true
    );
    assert_eq!(
        driver
            .call("session.history", json!({"session_id":b}))
            .await["result"]["state"],
        "idle"
    );
    assert_eq!(
        driver.call("session.status", json!({"session_id":a})).await["result"]["active_task"]["task_id"],
        task["task_id"]
    );
    assert_eq!(
        driver
            .call(
                "task.send",
                json!({"session_id":b,"message":{"text":"busy"}})
            )
            .await["error"]["code"],
        "busy"
    );
    assert_eq!(
        driver
            .call(
                "task.cancel",
                json!({"session_id":b,"task_id":task["task_id"]})
            )
            .await["result"]["status"],
        "not_running"
    );
    assert_eq!(
        driver
            .call(
                "task.cancel",
                json!({"session_id":a,"task_id":task["task_id"]})
            )
            .await["result"]["status"],
        "requested"
    );
    assert_eq!(driver.finish(&task).await["event"]["status"], "interrupted");
    assert_eq!(
        driver
            .call(
                "task.cancel",
                json!({"session_id":a,"task_id":task["task_id"]})
            )
            .await["result"]["status"],
        "not_running"
    );
    release.send(()).unwrap();
    server.await.unwrap();
    assert_eq!(
        driver
            .call("session.history", json!({"session_id":a}))
            .await["result"]["status"],
        "interrupted"
    );
    let seen = driver.shutdown().await;
    assert_eq!(
        seen.iter()
            .filter(|v| v["event"]["type"] == "task_finished")
            .count(),
        1
    );
}

#[tokio::test]
async fn eof_drains_accepted_task() {
    let directory = Directory::new();
    let (client, arrived, release, server) = paused_model().await;
    let mut driver = Driver::new(&directory, client).await;
    let id = driver.create().await;
    let task = driver
        .call(
            "task.send",
            json!({"session_id":id,"message":{"text":"finish"}}),
        )
        .await["result"]
        .clone();
    arrived.await.unwrap();
    driver.input.shutdown().await.unwrap();
    release.send(()).unwrap();
    assert_eq!(driver.finish(&task).await["event"]["status"], "completed");
    assert!(driver.output.next_line().await.unwrap().is_none());
    driver.worker.await.unwrap().unwrap();
    server.await.unwrap();
}

#[tokio::test]
async fn completion_before_cancel_keeps_one_completed_terminal_in_the_same_runtime() {
    let directory = Directory::new();
    let (client, arrived, release, server) = paused_model().await;
    let mut driver = Driver::new(&directory, client).await;
    let id = driver.create().await;
    let task = driver
        .call(
            "task.send",
            json!({"session_id":id,"message":{"text":"finish first"}}),
        )
        .await["result"]
        .clone();
    arrived.await.unwrap();
    release.send(()).unwrap();
    assert_eq!(driver.finish(&task).await["event"]["status"], "completed");
    assert_eq!(
        driver
            .call(
                "task.cancel",
                json!({"session_id":id,"task_id":task["task_id"]})
            )
            .await["result"]["status"],
        "not_running"
    );
    assert_eq!(
        driver
            .call("session.history", json!({"session_id":id}))
            .await["result"]["status"],
        "completed"
    );
    let seen = driver.shutdown().await;
    assert_eq!(
        seen.iter()
            .filter(|v| v["task_id"] == task["task_id"] && v["event"]["type"] == "task_finished")
            .count(),
        1
    );
    server.await.unwrap();
}

#[tokio::test]
async fn history_is_readonly_and_errors_do_not_create_replacement_sessions() {
    let directory = Directory::new();
    let (client, server) = scripted_http(vec![]).await;
    let mut driver = Driver::new(&directory, client).await;
    let id = driver.create().await;
    let path = directory.0.join("sessions").join(&id).join("history.jsonl");
    let before = std::fs::read(&path).unwrap();
    let locked = driver
        .call("session.load", json!({"session_id":"missing"}))
        .await;
    assert_eq!(locked["error"]["code"], "not_found");
    assert_eq!(
        driver
            .call("session.history", json!({"session_id":"../escape"}))
            .await["error"]["code"],
        "invalid_request"
    );
    let mut torn = before.clone();
    torn.extend_from_slice(b"{\"schema_version\":");
    std::fs::write(&path, &torn).unwrap();
    assert!(
        driver
            .call("session.history", json!({"session_id":id}))
            .await
            .get("result")
            .is_some()
    );
    assert_eq!(std::fs::read(&path).unwrap(), torn);
    driver
        .call("session.release", json!({"session_id":id}))
        .await;
    assert!(
        driver
            .call("session.load", json!({"session_id":id}))
            .await
            .get("result")
            .is_some()
    );
    assert_eq!(std::fs::read(&path).unwrap(), before);
    driver
        .call("session.release", json!({"session_id":id}))
        .await;
    std::fs::write(
        &path,
        b"{\"schema_version\":99,\"type\":\"messages\",\"items\":[]}\n",
    )
    .unwrap();
    assert_eq!(
        driver.call("session.load", json!({"session_id":id})).await["error"]["code"],
        "invalid_history"
    );
    assert_eq!(
        std::fs::read_dir(directory.0.join("sessions"))
            .unwrap()
            .count(),
        1
    );
    driver.shutdown().await;
    requests(server).await;
}

#[path = "runtime_lifecycle.rs"]
mod lifecycle;

#[tokio::test]
async fn tool_business_failure_can_complete_but_model_failure_has_one_persisted_failed_terminal() {
    let directory = Directory::new();
    let tool = json!({"id":"tool","status":"completed","output":[{"type":"function_call","status":"completed","id":"f","call_id":"missing-read","name":"read","arguments":"{\"path\":\"absent.txt\"}"}]});
    let (client,server)=scripted_http(vec![(200,tool),(200,completed("File was missing.")),(400,json!({"error":{"message":"fixture failure","type":"invalid_request_error","code":"invalid_request"}}))]).await;
    let mut driver = Driver::new(&directory, client).await;
    let id = driver.create().await;
    let first = driver
        .call(
            "task.send",
            json!({"session_id":id,"message":{"text":"read absent.txt"}}),
        )
        .await["result"]
        .clone();
    assert_eq!(driver.finish(&first).await["event"]["status"], "completed");
    assert!(
        driver
            .seen
            .iter()
            .any(|v| v["event"]["type"] == "tool_finished"
                && v["event"]["output"].to_string().contains("error"))
    );
    let second = driver
        .call(
            "task.send",
            json!({"session_id":id,"message":{"text":"fail model"}}),
        )
        .await["result"]
        .clone();
    assert_ne!(first["task_id"], second["task_id"]);
    let failed = driver.finish(&second).await;
    assert_eq!(failed["event"]["status"], "failed");
    assert_eq!(failed["event"]["error"]["code"], "model_error");
    let history = driver
        .call("session.history", json!({"session_id":id}))
        .await;
    assert_eq!(history["result"]["status"], "failed");
    let last = history["result"]["records"]
        .as_array()
        .unwrap()
        .last()
        .unwrap();
    assert_eq!(last["record"]["task_id"], second["task_id"]);
    assert_eq!(last["record"]["status"], "failed");
    let seen = driver.shutdown().await;
    assert_eq!(
        seen.iter()
            .filter(|v| v["task_id"] == second["task_id"] && v["event"]["type"] == "task_finished")
            .count(),
        1
    );
    let wire = requests(server).await;
    assert!(wire[1]["input"].to_string().contains("absent.txt"));
}

#[tokio::test]
async fn invalid_calls_locked_history_and_api_mismatch_have_no_replacement_side_effects() {
    let directory = Directory::new();
    let (client, server) = scripted_http(vec![]).await;
    let mut driver = Driver::new(&directory, client).await;
    let id = driver.create().await;
    assert_eq!(
        driver
            .call("session.create", json!({"model":"unapproved-override"}))
            .await["error"]["code"],
        "invalid_request"
    );
    assert_eq!(
        driver.call("unknown.method", json!({})).await["error"]["code"],
        "unknown_method"
    );
    driver.input.write_all(b"{\"version\":2,\"id\":\"v2\",\"method\":\"session.create\",\"params\":{}}\nnot-json\n").await.unwrap();
    let version = driver.next().await;
    assert_eq!(version["id"], "v2");
    assert_eq!(version["error"]["code"], "unsupported_version");
    let malformed = driver.next().await;
    assert!(malformed["id"].is_null());
    assert_eq!(malformed["error"]["code"], "invalid_request");
    driver
        .call("session.release", json!({"session_id":id}))
        .await;
    let store = SessionStore::new(directory.0.join("sessions")).unwrap();
    let held = store.open_for_api(&id, "openai-responses").unwrap();
    assert_eq!(
        driver.call("session.load", json!({"session_id":id})).await["error"]["code"],
        "locked"
    );
    assert!(
        driver
            .call("session.history", json!({"session_id":id}))
            .await
            .get("result")
            .is_some()
    );
    drop(held);
    let path = store.directory(&id).unwrap().join("history.jsonl");
    let mut torn = std::fs::read(&path).unwrap();
    torn.extend_from_slice(b"{\"schema_version\":");
    std::fs::write(&path, &torn).unwrap();
    let metadata_path = store.directory(&id).unwrap().join("metadata.json");
    let mut metadata: Value =
        serde_json::from_slice(&std::fs::read(&metadata_path).unwrap()).unwrap();
    metadata["api"] = json!("openai-chat-completions");
    std::fs::write(&metadata_path, serde_json::to_vec(&metadata).unwrap()).unwrap();
    assert_eq!(
        driver.call("session.load", json!({"session_id":id})).await["error"]["code"],
        "api_mismatch"
    );
    assert_eq!(std::fs::read(&path).unwrap(), torn);
    assert_eq!(
        std::fs::read_dir(directory.0.join("sessions"))
            .unwrap()
            .count(),
        1
    );
    let seen = driver.shutdown().await;
    assert!(!seen.iter().any(|v| v["type"] == "event"));
    requests(server).await;
}

#[tokio::test]
async fn stdio_configuration_discovery_revision_and_readonly_queries_share_service_state() {
    let directory = Directory::new();
    let client = ModelClient::for_test("http://127.0.0.1:1/v1".to_owned());
    let mut driver = Driver::new(&directory, client).await;
    let capabilities = driver.call("runtime.capabilities", json!({})).await;
    assert!(
        capabilities["result"]["methods"]
            .as_array()
            .unwrap()
            .iter()
            .any(|method| method == "session.config.update")
    );
    assert_eq!(capabilities["result"]["multi_connection"], false);
    let models = driver.call("model.list", json!({})).await;
    assert!(
        models["result"]["configured_models"]
            .as_array()
            .unwrap()
            .iter()
            .any(|model| model["model"] == "test")
    );
    let id = driver.create().await;
    let view = driver
        .call("session.config.get", json!({"session_id":id}))
        .await;
    assert_eq!(view["result"]["config_revision"], 1);
    assert_eq!(
        view["result"]["effective_config"]["connection_binding"]["state"],
        "unknown"
    );
    assert!(view["result"]["effective_config"]["connection_identity"].is_null());
    let metadata = directory.0.join("sessions").join(&id).join("metadata.json");
    let history = directory.0.join("sessions").join(&id).join("history.jsonl");
    let old_history = std::fs::read(&history).unwrap();
    for patch in [
        json!({"model":null}),
        json!({"compaction":null}),
        json!({"unexpected":1}),
    ] {
        assert_eq!(
            driver
                .call(
                    "session.config.update",
                    json!({"session_id":id,"expected_revision":1,"patch":patch})
                )
                .await["error"]["code"],
            "invalid_request"
        );
    }
    assert_eq!(driver.call("session.config.update", json!({"session_id":id,"expected_revision":1,"patch":{"working_directory":directory.0}})).await["error"]["code"], "immutable_field");
    let updated = driver.call("session.config.update", json!({"session_id":id,"expected_revision":1,"patch":{"model":"custom-b","compaction":CompactionConfig::new(32768)}})).await;
    assert_eq!(updated["result"]["effective_config"]["model"], "custom-b");
    assert_eq!(updated["result"]["config_revision"], 2);
    assert_eq!(updated["result"]["effective_from"], "next_task");
    let committed = std::fs::read(&metadata).unwrap();
    let stale = driver
        .call(
            "session.config.update",
            json!({"session_id":id,"expected_revision":1,"patch":{"model":"bad"}}),
        )
        .await;
    assert_eq!(stale["error"]["code"], "config_conflict");
    assert_eq!(stale["error"]["current_revision"], 2);
    let noop = driver
        .call(
            "session.config.update",
            json!({"session_id":id,"expected_revision":2,"patch":{}}),
        )
        .await;
    assert_eq!(noop["result"]["config_revision"], 2);
    assert_eq!(std::fs::read(&metadata).unwrap(), committed);
    assert_eq!(std::fs::read(&history).unwrap(), old_history);
    let released = driver
        .call("session.release", json!({"session_id":id}))
        .await;
    assert_eq!(released["result"]["loaded"], false);
    let unloaded = driver
        .call("session.config.get", json!({"session_id":id}))
        .await;
    assert_eq!(unloaded["result"]["effective_config"]["model"], "custom-b");
    assert_eq!(unloaded["result"]["config_revision"], 2);
    assert_eq!(std::fs::read(history).unwrap(), old_history);
    driver.shutdown().await;
}

#[tokio::test]
async fn stdio_connection_binding_creation_and_rollback_use_the_same_service_contract() {
    use crate::client::{
        ClientConfig,
        connections::{Connection, ConnectionRegistry},
    };
    let directory = Directory::new();
    let store = SessionStore::new(directory.0.join("sessions")).unwrap();
    let legacy = store
        .create(
            &directory.0,
            "test",
            "legacy prompt",
            CompactionConfig::new(65536),
        )
        .unwrap();
    let metadata_path = store.directory(&legacy).unwrap().join("metadata.json");
    let before = std::fs::read(&metadata_path).unwrap();
    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    listener.set_nonblocking(true).unwrap();
    let connection = |id: &str| {
        Connection::new(
            id.into(),
            ClientConfig::new(
                ModelApi::Responses,
                "not-a-public-key",
                format!("http://{}/{id}", listener.local_addr().unwrap()),
            ),
            format!("model-{id}"),
        )
        .unwrap()
    };
    let connections = ConnectionRegistry::new(
        "secondary".into(),
        vec![connection("primary"), connection("secondary")],
    )
    .unwrap();
    let client = connections.default().client().clone();
    let runtime = Runtime::new(
        store,
        client,
        directory.0.clone(),
        "test".into(),
        CompactionConfig::new(65536),
    )
    .with_connections(connections)
    .with_max_active_tasks(2)
    .unwrap();
    let mut driver = Driver::from_runtime(runtime).await;
    assert_eq!(
        driver.call("runtime.capabilities", json!({})).await["result"]["storage_schemas"],
        json!([1, 2])
    );
    let listed = driver.call("connection.list", json!({})).await;
    assert_eq!(listed["result"]["connections"].as_array().unwrap().len(), 2);
    assert!(!listed.to_string().contains("not-a-public-key"));
    assert_eq!(
        driver.call("model.list", json!({})).await["result"]["connection_id"],
        "secondary"
    );
    assert_eq!(
        driver.call("session.create", json!({})).await["error"]["code"],
        "connection_binding_required"
    );
    let duplicate=driver.call("session.create",json!({"storage_schema":2,"working_directory":directory.0,"config":{"working_directory":directory.0}})).await;
    assert_eq!(duplicate["error"]["code"], "invalid_request");
    let unknown = driver
        .call("session.config.get", json!({"session_id":legacy}))
        .await;
    assert_eq!(
        unknown["result"]["effective_config"]["connection_binding"]["state"],
        "unknown"
    );
    assert!(unknown["result"]["effective_config"]["execution_connection_id"].is_null());
    for (method, params) in [
        ("session.load", json!({"session_id":legacy})),
        (
            "task.send",
            json!({"session_id":legacy,"message":{"text":"no implicit execution"}}),
        ),
    ] {
        assert_eq!(
            driver.call(method, params).await["error"]["code"],
            "connection_binding_required"
        );
    }
    let missing_ack=driver.call("session.config.update",json!({"session_id":legacy,"expected_revision":1,"allow_schema_upgrade":true,"patch":{"connection_id":"secondary"}})).await;
    assert_eq!(missing_ack["error"]["code"], "acknowledgement_required");
    assert_eq!(std::fs::read(&metadata_path).unwrap(), before);
    let bound=driver.call("session.config.update",json!({"session_id":legacy,"expected_revision":1,"allow_schema_upgrade":true,"acknowledge_unknown_origin":true,"patch":{"connection_id":"secondary"}})).await;
    assert_eq!(bound["result"]["storage_schema"], 2);
    assert_eq!(bound["result"]["config_revision"], 2);
    assert_eq!(
        bound["result"]["effective_config"]["connection_id"],
        "secondary"
    );
    assert_eq!(
        bound["result"]["effective_config"]["connection_binding"]["origin"],
        "explicit_legacy_binding"
    );
    assert_eq!(
        driver
            .call("session.status", json!({"session_id":legacy}))
            .await["result"]["loaded"],
        false
    );
    assert_eq!(
        driver
            .call(
                "session.config.rollback",
                json!({"session_id":legacy,"expected_revision":2})
            )
            .await["result"]["storage_schema"],
        1
    );
    assert_eq!(std::fs::read(metadata_path).unwrap(), before);
    let enhanced = driver
        .call(
            "session.create",
            json!({"storage_schema":2,"config":{"connection_id":"secondary","system_prompt":"new prompt","tool_selection":{}}}),
        )
        .await;
    let id = enhanced["result"]["session_id"].as_str().unwrap();
    assert_eq!(
        enhanced["result"]["effective_config"]["model"],
        "model-secondary"
    );
    assert_eq!(
        enhanced["result"]["effective_config"]["tool_selection"],
        json!({"builtin":[],"external":[]})
    );
    let noop = driver
        .call(
            "session.config.update",
            json!({"session_id":id,"expected_revision":1,"patch":{"system_prompt":"new prompt","tool_selection":{}}}),
        )
        .await;
    assert_eq!(noop["result"]["config_revision"], 1);
    assert_eq!(
        driver
            .call(
                "session.config.update",
                json!({"session_id":id,"expected_revision":1,"patch":{"tool_selection":[]}}),
            )
            .await["error"]["code"],
        "invalid_request"
    );
    assert_eq!(
        driver
            .call(
                "session.config.update",
                json!({"session_id":id,"expected_revision":1,"patch":{"tool_selection":null}})
            )
            .await["error"]["code"],
        "invalid_request"
    );
    let default_tools = driver
        .call(
            "session.create",
            json!({"storage_schema":2,"config":{"connection_id":"secondary","system_prompt":"default tools"}}),
        )
        .await;
    assert_eq!(
        default_tools["result"]["effective_config"]["tool_selection"],
        json!({"builtin":["bash","edit","read","write"],"external":[]})
    );
    assert_eq!(
        driver
            .call(
                "session.config.rollback",
                json!({"session_id":id,"expected_revision":1})
            )
            .await["error"]["code"],
        "busy"
    );
    driver
        .call("session.release", json!({"session_id":id}))
        .await;
    assert_eq!(
        driver
            .call(
                "session.config.rollback",
                json!({"session_id":id,"expected_revision":1})
            )
            .await["error"]["code"],
        "rollback_unsafe"
    );
    driver.shutdown().await;
    assert_eq!(
        listener.accept().unwrap_err().kind(),
        std::io::ErrorKind::WouldBlock
    );
}
