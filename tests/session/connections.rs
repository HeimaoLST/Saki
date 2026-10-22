use std::{fs, net::TcpListener as StdTcpListener, path::PathBuf, time::Duration};

use serde_json::{Value, json};
use tokio::{
    io::AsyncWriteExt,
    net::{TcpListener, TcpStream},
    sync::oneshot,
    task::JoinHandle,
};

use crate::{
    agent::{
        conversation::CompactionConfig,
        journal::{self, JournalRecord, TaskConfiguration, TaskStatus},
        message::UserMessage,
    },
    client::{
        ClientConfig, ModelApi,
        config::RESPONSES_API,
        connections::{Connection, ConnectionRegistry},
    },
    protocol::ExternalToolBinding,
    session::{
        config::{ConfigPatch, ToolSelection},
        runtime::{
            HistoryPage, Runtime, RuntimeCommand, RuntimeHandle, RuntimeReply, SessionState,
            TaskEvent, TaskEvents, TaskFinished,
        },
        store::{SessionStore, metadata_fault},
    },
    test_support::{completed, read_request, requests, scripted_server},
};

struct Workspace(PathBuf);

impl Workspace {
    fn new() -> Self {
        static NEXT: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
        let path = std::env::temp_dir().join(format!(
            "saki-connections-runtime-{}-{}",
            journal::new_id().unwrap(),
            NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        ));
        fs::create_dir(&path).unwrap();
        Self(path)
    }

    fn store(&self) -> SessionStore {
        SessionStore::new(self.0.join("sessions")).unwrap()
    }

    fn metadata_path(&self, id: &str) -> PathBuf {
        self.store().directory(id).unwrap().join("metadata.json")
    }

    fn history_path(&self, id: &str) -> PathBuf {
        self.store().directory(id).unwrap().join("history.jsonl")
    }

    fn metadata_bytes(&self, id: &str) -> Vec<u8> {
        fs::read(self.metadata_path(id)).unwrap()
    }

    fn history_bytes(&self, id: &str) -> Vec<u8> {
        fs::read(self.history_path(id)).unwrap()
    }
}

impl Drop for Workspace {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn tools(names: &[&str]) -> ToolSelection {
    ToolSelection {
        builtin: names.iter().map(|name| (*name).to_owned()).collect(),
        external: Vec::new(),
    }
}

fn compaction() -> CompactionConfig {
    CompactionConfig::new(262_144)
}

fn legacy(store: &SessionStore, workspace: &Workspace) -> String {
    store
        .create_with_api(
            &workspace.0,
            "model-a",
            "legacy system",
            RESPONSES_API,
            compaction(),
        )
        .unwrap()
}

fn strip_revision(workspace: &Workspace, id: &str) {
    let path = workspace.metadata_path(id);
    let mut value: Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
    value.as_object_mut().unwrap().remove("config_revision");
    fs::write(&path, serde_json::to_vec_pretty(&value).unwrap()).unwrap();
}

fn append_old_task(workspace: &Workspace, id: &str) {
    let mut file = fs::OpenOptions::new()
        .append(true)
        .open(workspace.history_path(id))
        .unwrap();
    journal::write(
        &mut file,
        &JournalRecord::TaskStarted {
            task_id: "old-task".to_owned(),
            message_id: None,
            configuration: None,
        },
    )
    .unwrap();
    journal::write(
        &mut file,
        &JournalRecord::TaskFinished {
            task_id: "old-task".to_owned(),
            status: TaskStatus::Completed,
            error: None,
        },
    )
    .unwrap();
}

fn no_http_connection_api(
    id: &str,
    model: &str,
    api: ModelApi,
    listener: &StdTcpListener,
) -> Connection {
    listener.set_nonblocking(true).unwrap();
    let address = listener.local_addr().unwrap();
    Connection::new(
        id.to_owned(),
        ClientConfig::new(api, format!("{id}-key"), format!("http://{address}/v1")),
        model.to_owned(),
    )
    .unwrap()
}

fn no_http_connection(id: &str, model: &str, listener: &StdTcpListener) -> Connection {
    no_http_connection_api(id, model, ModelApi::Responses, listener)
}

async fn scripted_connection(
    id: &str,
    model: &str,
    responses: Vec<(u16, &'static str, Vec<u8>)>,
) -> (Connection, JoinHandle<Vec<Value>>) {
    let (base_url, server) = scripted_server(responses).await;
    let connection = Connection::new(
        id.to_owned(),
        ClientConfig::new(ModelApi::Responses, format!("{id}-key"), base_url),
        model.to_owned(),
    )
    .unwrap();
    (connection, server)
}

async fn gated_connection(
    id: &str,
    model: &str,
) -> (
    Connection,
    oneshot::Receiver<()>,
    oneshot::Sender<()>,
    JoinHandle<Vec<Value>>,
) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (arrived_tx, arrived) = oneshot::channel();
    let (release, release_rx) = oneshot::channel();
    let worker = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
        let request = read_request(&mut socket).await;
        let _ = arrived_tx.send(());
        let _ = release_rx.await;
        write_json(&mut socket, &completed("bound response")).await;
        vec![request]
    });
    let connection = Connection::new(
        id.to_owned(),
        ClientConfig::new(
            ModelApi::Responses,
            format!("{id}-key"),
            format!("http://{address}/v1"),
        ),
        model.to_owned(),
    )
    .unwrap();
    (connection, arrived, release, worker)
}

async fn write_json(socket: &mut TcpStream, value: &Value) {
    let body = serde_json::to_vec(value).unwrap();
    let head = format!(
        "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
        body.len()
    );
    let _ = socket.write_all(head.as_bytes()).await;
    let _ = socket.write_all(&body).await;
}

fn registry(default: &str, connections: Vec<Connection>) -> ConnectionRegistry {
    ConnectionRegistry::new(default.to_owned(), connections).unwrap()
}

fn runtime(workspace: &Workspace, default: &Connection, registry: ConnectionRegistry) -> Runtime {
    Runtime::new(
        workspace.store(),
        default.client().clone(),
        workspace.0.clone(),
        default.default_model().to_owned(),
        compaction(),
    )
    .with_connections(registry)
    .with_max_active_tasks(2)
    .unwrap()
}

fn session(reply: RuntimeReply) -> SessionState {
    match reply {
        RuntimeReply::Session(state) => state,
        _ => panic!("expected a session reply"),
    }
}

fn config(reply: RuntimeReply) -> crate::session::config::ConfigView {
    match reply {
        RuntimeReply::Config(view) => view,
        _ => panic!("expected a config reply"),
    }
}

fn history(reply: RuntimeReply) -> HistoryPage {
    match reply {
        RuntimeReply::History(page) => page,
        _ => panic!("expected a history reply"),
    }
}

async fn bind(
    handle: &RuntimeHandle,
    id: &str,
    expected_revision: u64,
    connection_id: &str,
) -> std::result::Result<crate::session::config::ConfigView, crate::session::runtime::RuntimeError>
{
    let reply = handle
        .call(RuntimeCommand::ConfigUpdate {
            session_id: id.to_owned(),
            expected_revision,
            patch: ConfigPatch {
                connection_id: Some(connection_id.to_owned()),
                ..ConfigPatch::default()
            },
            allow_schema_upgrade: true,
            acknowledge_unknown_origin: true,
        })
        .await;
    match reply {
        Ok(RuntimeReply::ConfigUpdated { config, .. }) => Ok(config),
        Ok(_) => panic!("expected a config_updated reply"),
        Err(error) => Err(error),
    }
}

async fn finish(mut events: TaskEvents) -> TaskFinished {
    let mut terminal = None;
    while let Some(event) = events.next().await {
        if let TaskEvent::Finished(finished) = event {
            assert!(terminal.is_none(), "terminal delivered twice");
            terminal = Some(finished);
        }
    }
    terminal.expect("missing terminal")
}

fn task_configuration(page: &HistoryPage, task_id: &str) -> TaskConfiguration {
    page.records
        .iter()
        .find_map(|record| match &record.record {
            JournalRecord::TaskStarted {
                task_id: id,
                configuration: Some(configuration),
                ..
            } if id == task_id => Some(configuration.clone()),
            _ => None,
        })
        .expect("task configuration missing")
}

async fn shutdown(handle: RuntimeHandle, actor: JoinHandle<anyhow::Result<()>>) {
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    drop(handle);
    actor.await.unwrap().unwrap();
}

#[tokio::test]
async fn legacy_single_connection_stays_unknown_and_multi_requires_binding_without_http() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = legacy(&store, &workspace);
    strip_revision(&workspace, &id);
    append_old_task(&workspace, &id);
    let metadata_before = workspace.metadata_bytes(&id);
    let (connection, server) = scripted_connection(
        "a",
        "model-a",
        vec![(
            200,
            "application/json",
            serde_json::to_vec(&completed("legacy done")).unwrap(),
        )],
    )
    .await;
    let (handle, actor) = runtime(
        &workspace,
        &connection,
        registry("a", vec![connection.clone()]),
    )
    .start();
    let loaded = session(
        handle
            .call(RuntimeCommand::Load {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    let effective = loaded.effective_config.as_ref().unwrap();
    assert_eq!(effective.connection_binding.state, "unknown");
    assert!(effective.connection_identity.is_none());
    assert_eq!(effective.execution_connection_id.as_deref(), Some("a"));
    assert_eq!(loaded.config_revision, Some(0));
    let accepted = match handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("legacy send"),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Accepted(task) => task,
        _ => panic!("expected accepted task"),
    };
    assert_eq!(finish(accepted.events).await.status, "completed");
    assert_eq!(workspace.metadata_bytes(&id), metadata_before);
    let page = history(
        handle
            .call(RuntimeCommand::History {
                session_id: id.clone(),
                offset: 0,
                limit: usize::MAX,
            })
            .await
            .unwrap(),
    );
    let old = page
        .records
        .iter()
        .find_map(|record| match &record.record {
            JournalRecord::TaskStarted {
                task_id,
                configuration,
                ..
            } if task_id == "old-task" => Some(configuration.clone()),
            _ => None,
        })
        .unwrap();
    assert!(old.is_none());
    let snapshot = task_configuration(&page, &accepted.association.task_id);
    assert!(snapshot.connection_identity.is_none());
    assert_eq!(snapshot.execution_connection_id.as_deref(), Some("a"));
    assert_eq!(snapshot.config_revision, 0);
    let bodies = requests(server).await;
    assert_eq!(bodies.len(), 1);
    assert!(bodies[0]["input"].to_string().contains("legacy send"));
    shutdown(handle, actor).await;

    let a_listener = StdTcpListener::bind("127.0.0.1:0").unwrap();
    let b_listener = StdTcpListener::bind("127.0.0.1:0").unwrap();
    let a = no_http_connection("a", "model-a", &a_listener);
    let b = no_http_connection("b", "model-a", &b_listener);
    let mut send_codes = Vec::new();
    for default in ["a", "b"] {
        let registry = registry(default, vec![a.clone(), b.clone()]);
        let default_conn = if default == "a" { &a } else { &b };
        let (handle, actor) = runtime(&workspace, default_conn, registry).start();
        let load_error = match handle
            .call(RuntimeCommand::Load {
                session_id: id.clone(),
            })
            .await
        {
            Err(error) => error,
            Ok(_) => panic!("multi-connection legacy load was accepted"),
        };
        assert_eq!(load_error.code, "connection_binding_required");
        let send_error = match handle
            .call(RuntimeCommand::Send {
                session_id: id.clone(),
                message: UserMessage::text("must not send"),
            })
            .await
        {
            Err(error) => error,
            Ok(_) => panic!("multi-connection legacy send was accepted"),
        };
        send_codes.push(send_error.code);
        let view = config(
            handle
                .call(RuntimeCommand::ConfigGet {
                    session_id: id.clone(),
                })
                .await
                .unwrap(),
        );
        assert_eq!(view.effective_config.connection_binding.state, "unknown");
        history(
            handle
                .call(RuntimeCommand::History {
                    session_id: id.clone(),
                    offset: 0,
                    limit: 10,
                })
                .await
                .unwrap(),
        );
        handle.call(RuntimeCommand::List).await.unwrap();
        shutdown(handle, actor).await;
    }
    assert_eq!(workspace.metadata_bytes(&id), metadata_before);
    assert_eq!(
        a_listener.accept().unwrap_err().kind(),
        std::io::ErrorKind::WouldBlock
    );
    assert_eq!(
        b_listener.accept().unwrap_err().kind(),
        std::io::ErrorKind::WouldBlock
    );
    assert_eq!(
        send_codes,
        ["connection_binding_required", "connection_binding_required"]
    );
}

async fn release_session(handle: &RuntimeHandle, id: &str) {
    tokio::time::timeout(Duration::from_secs(5), async {
        loop {
            match handle
                .call(RuntimeCommand::Release {
                    session_id: id.to_owned(),
                })
                .await
            {
                Ok(RuntimeReply::Released { .. }) => return,
                Err(error) if error.code == "busy" => tokio::task::yield_now().await,
                Err(error) => panic!("release failed: {error:?}"),
                Ok(_) => panic!("unexpected release reply"),
            }
        }
    })
    .await
    .expect("release did not settle");
}

#[tokio::test]
async fn explicit_legacy_binding_backup_rollback_and_connection_identity_are_enforced() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = legacy(&store, &workspace);
    strip_revision(&workspace, &id);
    let metadata_before = workspace.metadata_bytes(&id);
    let journal_before = workspace.history_bytes(&id);
    let (a, arrived, release, server) = gated_connection("a", "model-a").await;
    let b_listener = StdTcpListener::bind("127.0.0.1:0").unwrap();
    let b = no_http_connection("b", "model-a", &b_listener);
    let (handle, actor) =
        runtime(&workspace, &a, registry("a", vec![a.clone(), b.clone()])).start();
    let mut arrived = arrived;
    let bound = bind(&handle, &id, 0, "a").await.unwrap();
    assert_eq!(bound.storage_schema, 2);
    assert_eq!(bound.config_revision, 1);
    assert_eq!(bound.effective_config.connection_binding.state, "bound");
    assert_eq!(
        bound.effective_config.connection_identity.as_ref(),
        Some(a.identity())
    );
    assert_eq!(
        bound.effective_config.execution_connection_id.as_deref(),
        Some("a")
    );
    let backup = store
        .directory(&id)
        .unwrap()
        .join("metadata.schema1.backup.json");
    assert_eq!(fs::read(backup).unwrap(), metadata_before);
    assert_eq!(workspace.history_bytes(&id), journal_before);
    assert!(arrived.try_recv().is_err(), "binding made an HTTP request");
    assert_eq!(
        b_listener.accept().unwrap_err().kind(),
        std::io::ErrorKind::WouldBlock
    );

    let restored = config(
        handle
            .call(RuntimeCommand::Rollback {
                session_id: id.clone(),
                expected_revision: 1,
            })
            .await
            .unwrap(),
    );
    assert_eq!(restored.storage_schema, 1);
    assert_eq!(
        restored.effective_config.connection_binding.state,
        "unknown"
    );
    assert_eq!(workspace.metadata_bytes(&id), metadata_before);
    assert_eq!(workspace.history_bytes(&id), journal_before);

    let bound = bind(&handle, &id, 0, "a").await.unwrap();
    assert_eq!(bound.storage_schema, 2);
    let loaded = session(
        handle
            .call(RuntimeCommand::Load {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    assert_eq!(
        loaded
            .effective_config
            .as_ref()
            .unwrap()
            .connection_binding
            .state,
        "bound"
    );
    let task = match handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("bound task"),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Accepted(task) => task,
        _ => panic!("expected accepted task"),
    };
    arrived.await.unwrap();
    release.send(()).unwrap();
    assert_eq!(finish(task.events).await.status, "completed");
    let page = history(
        handle
            .call(RuntimeCommand::History {
                session_id: id.clone(),
                offset: 0,
                limit: usize::MAX,
            })
            .await
            .unwrap(),
    );
    let snapshot = task_configuration(&page, &task.association.task_id);
    assert_eq!(snapshot.connection_identity.as_ref(), Some(a.identity()));
    assert_eq!(snapshot.execution_connection_id.as_deref(), Some("a"));
    let bodies = requests(server).await;
    assert_eq!(bodies.len(), 1);
    let journal_after_task = workspace.history_bytes(&id);
    release_session(&handle, &id).await;
    let error = match handle
        .call(RuntimeCommand::Rollback {
            session_id: id.clone(),
            expected_revision: 1,
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("rollback after a new task was accepted"),
    };
    assert_eq!(error.code, "rollback_unsafe");
    assert_eq!(store.metadata(&id).unwrap().schema_version, 2);
    assert_eq!(workspace.history_bytes(&id), journal_after_task);
    shutdown(handle, actor).await;

    let changed_listener = StdTcpListener::bind("127.0.0.1:0").unwrap();
    let changed = no_http_connection("a", "model-a", &changed_listener);
    let (handle, actor) =
        runtime(&workspace, &changed, registry("a", vec![changed.clone()])).start();
    let mismatch = match handle
        .call(RuntimeCommand::Load {
            session_id: id.clone(),
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("load accepted a changed endpoint"),
    };
    assert_eq!(mismatch.code, "connection_mismatch");
    shutdown(handle, actor).await;

    let rotated = Connection::new(
        "a".to_owned(),
        ClientConfig::new(
            ModelApi::Responses,
            "rotated-key",
            a.identity().endpoint.clone(),
        ),
        "model-a".to_owned(),
    )
    .unwrap();
    let (handle, actor) =
        runtime(&workspace, &rotated, registry("a", vec![rotated.clone()])).start();
    let loaded = session(
        handle
            .call(RuntimeCommand::Load {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    assert_eq!(
        loaded
            .effective_config
            .as_ref()
            .unwrap()
            .connection_identity,
        Some(a.identity().clone())
    );
    shutdown(handle, actor).await;
}

#[tokio::test]
async fn enhanced_sessions_route_by_connection_and_switch_same_api_without_rewriting_history() {
    let workspace = Workspace::new();
    let dir_a = workspace.0.join("a");
    let dir_b = workspace.0.join("b");
    fs::create_dir(&dir_a).unwrap();
    fs::create_dir(&dir_b).unwrap();
    fs::write(dir_a.join("probe.txt"), "marker-a-cwd").unwrap();
    fs::write(dir_b.join("probe.txt"), "marker-b-cwd").unwrap();

    let tool = json!({
        "id": "tool-response",
        "status": "completed",
        "output": [{
            "type": "function_call",
            "status": "completed",
            "id": "same-item",
            "call_id": "same-call",
            "name": "read",
            "arguments": "{\"path\":\"probe.txt\"}"
        }]
    });
    let (a, server_a) = scripted_connection(
        "a",
        "model-a",
        vec![
            (200, "application/json", serde_json::to_vec(&tool).unwrap()),
            (
                200,
                "application/json",
                serde_json::to_vec(&completed("A done")).unwrap(),
            ),
        ],
    )
    .await;
    let (b, server_b) = scripted_connection(
        "b",
        "model-b",
        vec![
            (
                200,
                "application/json",
                serde_json::to_vec(&completed("B done")).unwrap(),
            ),
            (
                200,
                "application/json",
                serde_json::to_vec(&completed("A on B done")).unwrap(),
            ),
        ],
    )
    .await;
    let c_listener = StdTcpListener::bind("127.0.0.1:0").unwrap();
    let c = no_http_connection_api("c", "model-c", ModelApi::ChatCompletions, &c_listener);
    let (handle, actor) = runtime(
        &workspace,
        &a,
        registry("a", vec![a.clone(), b.clone(), c.clone()]),
    )
    .start();

    let create_error = match handle
        .call(RuntimeCommand::Create {
            working_directory: None,
            system_prompt: None,
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("legacy Create must be rejected in multi-connection mode"),
    };
    assert_eq!(create_error.code, "connection_binding_required");

    let a_state = session(
        handle
            .call(RuntimeCommand::CreateConfigured {
                config: ConfigPatch {
                    connection_id: Some("a".to_owned()),
                    system_prompt: Some("prompt A".to_owned()),
                    tool_selection: Some(tools(&["read"])),
                    working_directory: Some(dir_a.clone()),
                    ..ConfigPatch::default()
                },
                storage_schema: 2,
            })
            .await
            .unwrap(),
    );
    let b_state = session(
        handle
            .call(RuntimeCommand::CreateConfigured {
                config: ConfigPatch {
                    connection_id: Some("b".to_owned()),
                    system_prompt: Some("prompt B".to_owned()),
                    tool_selection: Some(ToolSelection::default()),
                    working_directory: Some(dir_b.clone()),
                    ..ConfigPatch::default()
                },
                storage_schema: 2,
            })
            .await
            .unwrap(),
    );
    let a_id = a_state.metadata.as_ref().unwrap().id.clone();
    let b_id = b_state.metadata.as_ref().unwrap().id.clone();
    assert_eq!(a_state.storage_schema, Some(2));
    assert_eq!(b_state.storage_schema, Some(2));
    assert_eq!(
        a_state
            .effective_config
            .as_ref()
            .unwrap()
            .connection_identity,
        Some(a.identity().clone())
    );
    assert_eq!(
        b_state
            .effective_config
            .as_ref()
            .unwrap()
            .connection_identity,
        Some(b.identity().clone())
    );
    assert_eq!(
        a_state.effective_config.as_ref().unwrap().system_prompt,
        "prompt A"
    );
    assert_eq!(
        b_state.effective_config.as_ref().unwrap().tool_selection,
        ToolSelection::default()
    );

    let a_task = match handle
        .call(RuntimeCommand::Send {
            session_id: a_id.clone(),
            message: UserMessage::text("A read"),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Accepted(task) => task,
        _ => panic!("expected A task"),
    };
    assert_eq!(finish(a_task.events).await.status, "completed");
    let b_task = match handle
        .call(RuntimeCommand::Send {
            session_id: b_id.clone(),
            message: UserMessage::text("B only"),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Accepted(task) => task,
        _ => panic!("expected B task"),
    };
    assert_eq!(finish(b_task.events).await.status, "completed");
    let a_history_before_switch = workspace.history_bytes(&a_id);
    let a_page = history(
        handle
            .call(RuntimeCommand::History {
                session_id: a_id.clone(),
                offset: 0,
                limit: usize::MAX,
            })
            .await
            .unwrap(),
    );
    let a_before = task_configuration(&a_page, &a_task.association.task_id);
    assert_eq!(a_before.connection_identity, Some(a.identity().clone()));
    assert_eq!(a_before.execution_connection_id.as_deref(), Some("a"));
    let a_requests = requests(server_a).await;
    assert_eq!(a_requests.len(), 2);
    assert!(a_requests[0]["tools"].to_string().contains("read"));
    assert!(a_requests[1]["input"].to_string().contains("marker-a-cwd"));
    let switched = match handle
        .call(RuntimeCommand::ConfigUpdate {
            session_id: a_id.clone(),
            expected_revision: 1,
            patch: ConfigPatch {
                connection_id: Some("b".to_owned()),
                ..ConfigPatch::default()
            },
            allow_schema_upgrade: false,
            acknowledge_unknown_origin: false,
        })
        .await
        .unwrap()
    {
        RuntimeReply::ConfigUpdated { config, .. } => config,
        _ => panic!("expected config update"),
    };
    assert_eq!(switched.config_revision, 2);
    assert_eq!(
        switched.effective_config.connection_identity,
        Some(b.identity().clone())
    );
    assert_eq!(
        switched.effective_config.execution_connection_id.as_deref(),
        Some("b")
    );
    assert_eq!(workspace.history_bytes(&a_id), a_history_before_switch);

    let switch_task = match handle
        .call(RuntimeCommand::Send {
            session_id: a_id.clone(),
            message: UserMessage::text("after switch"),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Accepted(task) => task,
        _ => panic!("expected switched task"),
    };
    assert_eq!(finish(switch_task.events).await.status, "completed");
    let page = history(
        handle
            .call(RuntimeCommand::History {
                session_id: a_id.clone(),
                offset: 0,
                limit: usize::MAX,
            })
            .await
            .unwrap(),
    );
    let switched_snapshot = task_configuration(&page, &switch_task.association.task_id);
    assert_eq!(
        switched_snapshot.connection_identity,
        Some(b.identity().clone())
    );
    assert_eq!(
        switched_snapshot.execution_connection_id.as_deref(),
        Some("b")
    );
    assert!(
        workspace
            .history_bytes(&a_id)
            .starts_with(&a_history_before_switch)
    );
    let b_after_requests = requests(server_b).await;
    assert_eq!(b_after_requests.len(), 2);
    assert!(b_after_requests[0]["tools"].is_null());
    assert!(b_after_requests[0]["input"].to_string().contains("B only"));
    assert!(
        b_after_requests[1]["input"]
            .to_string()
            .contains("after switch")
    );

    let metadata_before_cross_api = workspace.metadata_bytes(&a_id);
    let cross_api = match handle
        .call(RuntimeCommand::ConfigUpdate {
            session_id: a_id.clone(),
            expected_revision: 2,
            patch: ConfigPatch {
                connection_id: Some("c".to_owned()),
                ..ConfigPatch::default()
            },
            allow_schema_upgrade: false,
            acknowledge_unknown_origin: false,
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("cross-API switch was accepted"),
    };
    assert_eq!(cross_api.code, "api_mismatch");
    assert_eq!(workspace.metadata_bytes(&a_id), metadata_before_cross_api);
    assert_eq!(
        c_listener.accept().unwrap_err().kind(),
        std::io::ErrorKind::WouldBlock
    );
    let views = match handle.call(RuntimeCommand::Connections).await.unwrap() {
        RuntimeReply::Connections(views) => views,
        _ => panic!("expected connections"),
    };
    assert_eq!(
        views
            .iter()
            .map(|view| view.id.as_str())
            .collect::<Vec<_>>(),
        ["a", "b", "c"]
    );
    shutdown(handle, actor).await;
}

#[tokio::test]
async fn runtime_after_rename_binding_fault_is_commit_unknown_and_reloadable() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = legacy(&store, &workspace);
    strip_revision(&workspace, &id);
    let listener = StdTcpListener::bind("127.0.0.1:0").unwrap();
    let a = no_http_connection("a", "model-a", &listener);
    let (handle, actor) = runtime(&workspace, &a, registry("a", vec![a.clone()])).start();
    let guard = metadata_fault::Guard::new(
        store.directory(&id).unwrap(),
        metadata_fault::Point::AfterRename,
    );
    let error = bind(&handle, &id, 0, "a").await.unwrap_err();
    drop(guard);
    assert_eq!(error.code, "commit_unknown");
    assert_eq!(store.metadata(&id).unwrap().schema_version, 2);
    let view = config(
        handle
            .call(RuntimeCommand::ConfigGet {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    assert_eq!(view.storage_schema, 2);
    assert_eq!(view.effective_config.connection_binding.state, "bound");
    let loaded = session(
        handle
            .call(RuntimeCommand::Load {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    assert!(loaded.loaded);
    assert_eq!(
        loaded
            .effective_config
            .as_ref()
            .unwrap()
            .connection_binding
            .state,
        "bound"
    );
    assert_eq!(
        listener.accept().unwrap_err().kind(),
        std::io::ErrorKind::WouldBlock
    );
    shutdown(handle, actor).await;
}

#[tokio::test]
async fn prompt_and_tool_updates_apply_to_next_request_without_rewriting_history() {
    let workspace = Workspace::new();
    let dir = workspace.0.join("prompt-tools");
    fs::create_dir(&dir).unwrap();
    fs::write(dir.join("probe.txt"), "old-tool-result").unwrap();
    let tool = json!({
        "id": "tool-response",
        "status": "completed",
        "output": [{
            "type": "function_call",
            "status": "completed",
            "id": "same-item",
            "call_id": "same-call",
            "name": "read",
            "arguments": "{\"path\":\"probe.txt\"}"
        }]
    });
    let (a, server) = scripted_connection(
        "a",
        "model-a",
        vec![
            (200, "application/json", serde_json::to_vec(&tool).unwrap()),
            (
                200,
                "application/json",
                serde_json::to_vec(&completed("first")).unwrap(),
            ),
            (
                200,
                "application/json",
                serde_json::to_vec(&completed("after update")).unwrap(),
            ),
        ],
    )
    .await;
    let (handle, actor) = runtime(&workspace, &a, registry("a", vec![a.clone()])).start();
    let id = session(
        handle
            .call(RuntimeCommand::CreateConfigured {
                config: ConfigPatch {
                    connection_id: Some("a".to_owned()),
                    system_prompt: Some("old system".to_owned()),
                    tool_selection: Some(tools(&["read"])),
                    working_directory: Some(dir),
                    ..ConfigPatch::default()
                },
                storage_schema: 2,
            })
            .await
            .unwrap(),
    )
    .metadata
    .unwrap()
    .id;
    let first = match handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("first read"),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Accepted(task) => task,
        _ => panic!("expected first task"),
    };
    assert_eq!(finish(first.events).await.status, "completed");
    let history_before = workspace.history_bytes(&id);

    let updated = match handle
        .call(RuntimeCommand::ConfigUpdate {
            session_id: id.clone(),
            expected_revision: 1,
            patch: ConfigPatch {
                system_prompt: Some("new system".to_owned()),
                tool_selection: Some(ToolSelection::default()),
                ..ConfigPatch::default()
            },
            allow_schema_upgrade: false,
            acknowledge_unknown_origin: false,
        })
        .await
        .unwrap()
    {
        RuntimeReply::ConfigUpdated { config, .. } => config,
        _ => panic!("expected config update"),
    };
    assert_eq!(updated.config_revision, 2);
    assert_eq!(updated.effective_config.system_prompt, "new system");
    assert_eq!(
        updated.effective_config.tool_selection,
        ToolSelection::default()
    );
    assert_eq!(workspace.history_bytes(&id), history_before);
    let metadata_after_update = workspace.metadata_bytes(&id);

    let noop = match handle
        .call(RuntimeCommand::ConfigUpdate {
            session_id: id.clone(),
            expected_revision: 2,
            patch: ConfigPatch {
                system_prompt: Some("new system".to_owned()),
                tool_selection: Some(ToolSelection::default()),
                ..ConfigPatch::default()
            },
            allow_schema_upgrade: false,
            acknowledge_unknown_origin: false,
        })
        .await
        .unwrap()
    {
        RuntimeReply::ConfigUpdated { config, .. } => config,
        _ => panic!("expected no-op config update"),
    };
    assert_eq!(noop.config_revision, 2);
    assert_eq!(workspace.metadata_bytes(&id), metadata_after_update);

    let second = match handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("after update"),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Accepted(task) => task,
        _ => panic!("expected second task"),
    };
    assert_eq!(finish(second.events).await.status, "completed");
    let page = history(
        handle
            .call(RuntimeCommand::History {
                session_id: id.clone(),
                offset: 0,
                limit: usize::MAX,
            })
            .await
            .unwrap(),
    );
    let snapshot = task_configuration(&page, &second.association.task_id);
    assert_eq!(snapshot.system_prompt.as_deref(), Some("new system"));
    assert_eq!(
        snapshot.tool_selection.as_ref(),
        Some(&ToolSelection::default())
    );
    assert!(workspace.history_bytes(&id).starts_with(&history_before));
    assert!(
        serde_json::to_string(&page.records)
            .unwrap()
            .contains("old-tool-result")
    );
    let bodies = requests(server).await;
    assert_eq!(bodies.len(), 3);
    let after = bodies[2]["input"].to_string();
    assert!(after.contains("new system"));
    assert!(!after.contains("old system"));
    assert!(bodies[2]["tools"].is_null());
    shutdown(handle, actor).await;
}

fn native_reasoning_response() -> Value {
    let mut response = completed("native done");
    response["output"].as_array_mut().unwrap().insert(
        0,
        json!({
            "type": "reasoning",
            "id": "native-reasoning",
            "status": "completed",
            "content": [{"type": "reasoning_text", "text": "opaque"}],
            "encrypted_content": "opaque-native-continuation"
        }),
    );
    response
}

#[tokio::test]
async fn native_continuation_blocks_prompt_tool_and_connection_reinterpretation() {
    let workspace = Workspace::new();
    let (a, server) = scripted_connection(
        "a",
        "model-a",
        vec![(
            200,
            "application/json",
            serde_json::to_vec(&native_reasoning_response()).unwrap(),
        )],
    )
    .await;
    let b_listener = StdTcpListener::bind("127.0.0.1:0").unwrap();
    let b = no_http_connection("b", "model-b", &b_listener);
    let (handle, actor) =
        runtime(&workspace, &a, registry("a", vec![a.clone(), b.clone()])).start();
    let id = session(
        handle
            .call(RuntimeCommand::CreateConfigured {
                config: ConfigPatch {
                    connection_id: Some("a".to_owned()),
                    system_prompt: Some("native prompt".to_owned()),
                    tool_selection: Some(tools(&["read"])),
                    ..ConfigPatch::default()
                },
                storage_schema: 2,
            })
            .await
            .unwrap(),
    )
    .metadata
    .unwrap()
    .id;
    let task = match handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("native task"),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Accepted(task) => task,
        _ => panic!("expected task"),
    };
    assert_eq!(finish(task.events).await.status, "completed");
    let metadata_before = workspace.metadata_bytes(&id);
    let _ = requests(server).await;
    for patch in [
        ConfigPatch {
            system_prompt: Some("changed prompt".to_owned()),
            ..ConfigPatch::default()
        },
        ConfigPatch {
            tool_selection: Some(ToolSelection::default()),
            ..ConfigPatch::default()
        },
        ConfigPatch {
            connection_id: Some("b".to_owned()),
            ..ConfigPatch::default()
        },
    ] {
        let error = match handle
            .call(RuntimeCommand::ConfigUpdate {
                session_id: id.clone(),
                expected_revision: 1,
                patch,
                allow_schema_upgrade: false,
                acknowledge_unknown_origin: false,
            })
            .await
        {
            Err(error) => error,
            Ok(_) => panic!("native continuation reconfiguration was accepted"),
        };
        assert_eq!(error.code, "continuation_not_supported");
        assert_eq!(workspace.metadata_bytes(&id), metadata_before);
    }
    assert_eq!(
        b_listener.accept().unwrap_err().kind(),
        std::io::ErrorKind::WouldBlock
    );
    shutdown(handle, actor).await;
}

#[tokio::test]
async fn unloaded_binding_validates_flags_cas_and_external_writer_lock() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = legacy(&store, &workspace);
    strip_revision(&workspace, &id);
    let metadata_before = workspace.metadata_bytes(&id);
    let journal_before = workspace.history_bytes(&id);
    let listener = StdTcpListener::bind("127.0.0.1:0").unwrap();
    let a = no_http_connection("a", "model-a", &listener);
    let (handle, actor) = runtime(&workspace, &a, registry("a", vec![a.clone()])).start();
    let patch = ConfigPatch {
        connection_id: Some("a".to_owned()),
        ..ConfigPatch::default()
    };

    let missing_allow = match handle
        .call(RuntimeCommand::ConfigUpdate {
            session_id: id.clone(),
            expected_revision: 0,
            patch: patch.clone(),
            allow_schema_upgrade: false,
            acknowledge_unknown_origin: false,
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("binding without allow flag was accepted"),
    };
    assert_eq!(missing_allow.code, "schema_upgrade_required");

    let missing_ack = match handle
        .call(RuntimeCommand::ConfigUpdate {
            session_id: id.clone(),
            expected_revision: 0,
            patch: patch.clone(),
            allow_schema_upgrade: true,
            acknowledge_unknown_origin: false,
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("binding without acknowledgement was accepted"),
    };
    assert_eq!(missing_ack.code, "acknowledgement_required");

    let wrong_revision = match handle
        .call(RuntimeCommand::ConfigUpdate {
            session_id: id.clone(),
            expected_revision: 9,
            patch: patch.clone(),
            allow_schema_upgrade: true,
            acknowledge_unknown_origin: true,
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("binding with stale revision was accepted"),
    };
    assert_eq!(wrong_revision.code, "config_conflict");

    let guard = store.lock_config(&id).unwrap();
    let locked = match handle
        .call(RuntimeCommand::ConfigUpdate {
            session_id: id.clone(),
            expected_revision: 0,
            patch,
            allow_schema_upgrade: true,
            acknowledge_unknown_origin: true,
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("binding ignored an external writer lock"),
    };
    assert_eq!(locked.code, "locked");
    drop(guard);
    assert_eq!(workspace.metadata_bytes(&id), metadata_before);
    assert_eq!(workspace.history_bytes(&id), journal_before);
    assert_eq!(
        listener.accept().unwrap_err().kind(),
        std::io::ErrorKind::WouldBlock
    );
    // The lock must be released and a later binding must succeed.
    assert_eq!(bind(&handle, &id, 0, "a").await.unwrap().storage_schema, 2);
    shutdown(handle, actor).await;
}

#[tokio::test]
async fn updated_agent_system_prefix_survives_compaction_and_restart_without_rewriting_its_origin()
{
    let workspace = Workspace::new();
    let replies = [
        "initial done",
        "short summary",
        "after compact",
        "after restart",
    ]
    .into_iter()
    .map(|text| {
        (
            200,
            "application/json",
            serde_json::to_vec(&completed(text)).unwrap(),
        )
    })
    .collect();
    let (a, server) = scripted_connection("a", "model-a", replies).await;
    let (handle, actor) = runtime(&workspace, &a, registry("a", vec![a.clone()])).start();
    let created = session(
        handle
            .call(RuntimeCommand::CreateConfigured {
                storage_schema: 2,
                config: ConfigPatch {
                    system_prompt: Some("original system prefix".into()),
                    tool_selection: Some(tools(&["read"])),
                    ..ConfigPatch::default()
                },
            })
            .await
            .unwrap(),
    );
    let id = created.session_id;
    let first = match handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("verified context ".repeat(1100)),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Accepted(task) => task,
        _ => panic!("task expected"),
    };
    assert_eq!(finish(first.events).await.status, "completed");
    let original = workspace.history_bytes(&id);
    handle
        .call(RuntimeCommand::ConfigUpdate {
            session_id: id.clone(),
            expected_revision: 1,
            allow_schema_upgrade: false,
            acknowledge_unknown_origin: false,
            patch: ConfigPatch {
                system_prompt: Some("updated system prefix".into()),
                compaction: Some(CompactionConfig {
                    context_budget_tokens: 8192,
                    max_input_tokens: None,
                    reserve_tokens: 2048,
                    keep_recent_tokens: 256,
                    max_summary_tokens: 1024,
                }),
                ..ConfigPatch::default()
            },
        })
        .await
        .unwrap();
    let second = match handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("continue"),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Accepted(task) => task,
        _ => panic!("task expected"),
    };
    let terminal = finish(second.events).await;
    assert_eq!(terminal.status, "completed", "{:?}", terminal.error);
    release_session(&handle, &id).await;
    shutdown(handle, actor).await;
    let (handle, actor) = runtime(&workspace, &a, registry("a", vec![a.clone()])).start();
    handle
        .call(RuntimeCommand::Load {
            session_id: id.clone(),
        })
        .await
        .unwrap();
    let third = match handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("continue after restart"),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Accepted(task) => task,
        _ => panic!("task expected"),
    };
    assert_eq!(finish(third.events).await.status, "completed");
    let page = history(
        handle
            .call(RuntimeCommand::History {
                session_id: id.clone(),
                offset: 0,
                limit: usize::MAX,
            })
            .await
            .unwrap(),
    );
    assert_eq!(
        page.records
            .iter()
            .filter(|record| matches!(record.record, JournalRecord::Compaction { .. }))
            .count(),
        1
    );
    assert!(workspace.history_bytes(&id).starts_with(&original));
    assert!(
        String::from_utf8(original)
            .unwrap()
            .contains("original system prefix")
    );
    let bodies = requests(server).await;
    assert_eq!(bodies.len(), 4);
    for (body, expected) in [
        (&bodies[0], "original system prefix"),
        (&bodies[2], "updated system prefix"),
        (&bodies[3], "updated system prefix"),
    ] {
        let system = body["input"]
            .as_array()
            .unwrap()
            .iter()
            .filter(|item| item["role"] == "system")
            .collect::<Vec<_>>();
        assert_eq!(system.len(), 1);
        assert!(system[0].to_string().contains(expected));
    }
    assert!(
        !bodies[2]["input"]
            .to_string()
            .contains("original system prefix")
    );
    assert!(
        !bodies[3]["input"]
            .to_string()
            .contains("original system prefix")
    );
    assert!(bodies[1]["input"].to_string().contains("verified context"));
    shutdown(handle, actor).await;
}

#[tokio::test]
async fn missing_external_tool_source_is_rejected_before_creating_a_session() {
    let workspace = Workspace::new();
    let listener = StdTcpListener::bind("127.0.0.1:0").unwrap();
    let a = no_http_connection("a", "model-a", &listener);
    let (handle, actor) = runtime(&workspace, &a, registry("a", vec![a.clone()])).start();
    let error = match handle
        .call(RuntimeCommand::CreateConfigured {
            storage_schema: 2,
            config: ConfigPatch {
                connection_id: Some("a".to_owned()),
                tool_selection: Some(ToolSelection {
                    builtin: vec!["read".to_owned()],
                    external: vec![ExternalToolBinding {
                        source_id: "project".to_owned(),
                        tool_name: "lookup".to_owned(),
                        exposed_name: "project_lookup".to_owned(),
                    }],
                }),
                ..ConfigPatch::default()
            },
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("external tool selection was accepted"),
    };
    assert_eq!(error.code, "tool_source_unavailable");
    assert!(workspace.store().list().unwrap().is_empty());
    assert_eq!(
        listener.accept().unwrap_err().kind(),
        std::io::ErrorKind::WouldBlock
    );
    shutdown(handle, actor).await;
}
