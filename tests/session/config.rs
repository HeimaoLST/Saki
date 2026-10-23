use std::{
    fs,
    path::PathBuf,
    sync::atomic::{AtomicUsize, Ordering},
    time::Duration,
};

use serde_json::{Value, json};
use tokio::{io::AsyncWriteExt, net::TcpListener, sync::oneshot, task::JoinHandle};

use crate::{
    agent::{
        conversation::CompactionConfig,
        journal::{JournalRecord, TaskConfiguration},
        message::{ImageAttachment, UserMessage},
    },
    client::{
        ClientConfig, ModelApi, ModelClient,
        config::{CHAT_COMPLETIONS_API, RESPONSES_API},
    },
    model::catalog::ModelCatalog,
    session::{
        SYSTEM_PROMPT,
        config::{CapacitySource, ConfigPatch, ConfigView},
        runtime::{
            AcceptedTask, HistoryPage, Runtime, RuntimeCommand, RuntimeError, RuntimeHandle,
            RuntimeReply, SessionState, TaskEvent, TaskEvents, TaskFinished,
        },
        store::{SessionStore, metadata_fault},
    },
    test_support::{completed, read_request, scripted_http},
};

struct Workspace(PathBuf);

impl Workspace {
    fn new() -> Self {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        let path = std::env::temp_dir().join(format!(
            "saki-config-{}-{}",
            crate::agent::journal::new_id().unwrap(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir(&path).unwrap();
        Self(path)
    }

    fn store(&self) -> SessionStore {
        SessionStore::new(self.0.join("sessions")).unwrap()
    }

    fn directory(&self, session_id: &str) -> PathBuf {
        self.store().directory(session_id).unwrap()
    }

    fn metadata_bytes(&self, session_id: &str) -> Vec<u8> {
        fs::read(self.directory(session_id).join("metadata.json")).unwrap()
    }

    fn history_bytes(&self, session_id: &str) -> Vec<u8> {
        fs::read(self.directory(session_id).join("history.jsonl")).unwrap()
    }
}

impl Drop for Workspace {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn catalog() -> ModelCatalog {
    let snapshot = json!({
        "models": {
            "model-a": {
                "name": "Model A",
                "limit": {"context": 128000, "input": 120000, "output": 8192}
            },
            "model-b": {
                "name": "Model B",
                "limit": {"context": 64000, "input": 60000, "output": 8192}
            }
        }
    });
    ModelCatalog::from_snapshot(&serde_json::to_vec(&snapshot).unwrap()).unwrap()
}

fn large_compaction() -> CompactionConfig {
    CompactionConfig::new(262_144)
}

fn small_compaction() -> CompactionConfig {
    CompactionConfig {
        context_budget_tokens: 8192,
        max_input_tokens: None,
        reserve_tokens: 2048,
        keep_recent_tokens: 256,
        max_summary_tokens: 1024,
    }
}

fn invalid_compaction() -> CompactionConfig {
    CompactionConfig {
        context_budget_tokens: 100,
        ..small_compaction()
    }
}

fn patch_model(model: &str) -> ConfigPatch {
    ConfigPatch {
        model: Some(model.to_owned()),
        ..ConfigPatch::default()
    }
}

fn patch_model_compaction(model: &str, compaction: CompactionConfig) -> ConfigPatch {
    ConfigPatch {
        model: Some(model.to_owned()),
        compaction: Some(compaction),
        ..ConfigPatch::default()
    }
}

fn runtime(
    client: ModelClient,
    workspace: &Workspace,
    model: &str,
    catalog: Option<ModelCatalog>,
) -> Runtime {
    Runtime::new(
        workspace.store(),
        client,
        workspace.0.clone(),
        model.to_owned(),
        large_compaction(),
    )
    .with_catalog(catalog)
}

fn session(reply: RuntimeReply) -> SessionState {
    match reply {
        RuntimeReply::Session(state) => state,
        _ => panic!("expected a session reply"),
    }
}

fn config(reply: RuntimeReply) -> ConfigView {
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

async fn config_get(handle: &RuntimeHandle, session_id: &str) -> ConfigView {
    config(
        handle
            .call(RuntimeCommand::ConfigGet {
                session_id: session_id.to_owned(),
            })
            .await
            .unwrap(),
    )
}

async fn config_update(
    handle: &RuntimeHandle,
    session_id: &str,
    expected_revision: u64,
    patch: ConfigPatch,
) -> std::result::Result<ConfigView, RuntimeError> {
    match handle
        .call(RuntimeCommand::ConfigUpdate {
            allow_schema_upgrade: false,
            acknowledge_unknown_origin: false,
            session_id: session_id.to_owned(),
            expected_revision,
            patch,
        })
        .await
    {
        Ok(RuntimeReply::ConfigUpdated { config, .. }) => Ok(config),
        Ok(_) => panic!("expected a config_updated reply"),
        Err(error) => Err(error),
    }
}

async fn resolve_config(
    response: oneshot::Receiver<std::result::Result<RuntimeReply, RuntimeError>>,
) -> std::result::Result<ConfigView, RuntimeError> {
    match response.await {
        Ok(Ok(RuntimeReply::ConfigUpdated { config, .. })) => Ok(config),
        Ok(Ok(_)) => panic!("expected a config_updated reply"),
        Ok(Err(error)) => Err(error),
        Err(_) => panic!("configuration reply channel closed"),
    }
}

async fn create_session(handle: &RuntimeHandle) -> SessionState {
    session(
        handle
            .call(RuntimeCommand::Create {
                working_directory: None,
                system_prompt: None,
            })
            .await
            .unwrap(),
    )
}

async fn send_task(handle: &RuntimeHandle, session_id: &str, text: &str) -> AcceptedTask {
    match handle
        .call(RuntimeCommand::Send {
            session_id: session_id.to_owned(),
            message: UserMessage::text(text),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Accepted(task) => task,
        _ => panic!("expected an accepted task"),
    }
}

async fn finish(mut events: TaskEvents) -> TaskFinished {
    let mut terminal = None;
    while let Some(event) = events.next().await {
        if let TaskEvent::Finished(finished) = event {
            assert!(terminal.is_none(), "terminal was delivered twice");
            terminal = Some(finished);
        }
    }
    assert!(events.next().await.is_none());
    terminal.expect("task did not deliver a terminal")
}

fn last_task_configuration(page: &HistoryPage) -> TaskConfiguration {
    page.records
        .iter()
        .rev()
        .find_map(|record| match &record.record {
            JournalRecord::TaskStarted {
                configuration: Some(configuration),
                ..
            } => Some(configuration.clone()),
            _ => None,
        })
        .expect("task history must contain a configuration snapshot")
}

async fn history_for(handle: &RuntimeHandle, session_id: &str) -> HistoryPage {
    history(
        handle
            .call(RuntimeCommand::History {
                session_id: session_id.to_owned(),
                offset: 0,
                limit: usize::MAX,
            })
            .await
            .unwrap(),
    )
}

async fn request_body(server: JoinHandle<Value>) -> Value {
    tokio::time::timeout(Duration::from_secs(5), server)
        .await
        .expect("model request did not complete")
        .unwrap()
}

async fn request_bodies(server: JoinHandle<Vec<Value>>) -> Vec<Value> {
    tokio::time::timeout(Duration::from_secs(5), server)
        .await
        .expect("model fixture did not observe the expected requests")
        .unwrap()
}

async fn request_one(server: JoinHandle<Vec<Value>>) -> Value {
    let mut bodies = request_bodies(server).await;
    assert_eq!(bodies.len(), 1);
    bodies.remove(0)
}

async fn paused_model() -> (
    ModelClient,
    oneshot::Receiver<()>,
    oneshot::Sender<()>,
    JoinHandle<Value>,
) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (arrived_tx, arrived) = oneshot::channel();
    let (release, release_rx) = oneshot::channel();
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

fn completed_with_native_reasoning() -> Value {
    let mut response = completed("native reasoning completed");
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

fn seed_long_session(workspace: &Workspace) -> String {
    let store = workspace.store();
    let id = store
        .create_with_api(
            &workspace.0,
            "model-a",
            SYSTEM_PROMPT,
            RESPONSES_API,
            large_compaction(),
        )
        .unwrap();
    let mut loaded = store.open_for_api(&id, RESPONSES_API).unwrap();
    loaded.conversation.add_user_message(format!(
        "original-source: {}",
        "verified context ".repeat(1100)
    ));
    loaded
        .conversation
        .add_assistant_message("Completed initial work.".to_owned());
    loaded
        .conversation
        .add_user_message("Continue the unfinished goal.".to_owned());
    loaded.conversation.save_history().unwrap();
    id
}

#[tokio::test]
async fn configuration_round_trip_keeps_task_snapshots_and_reloads_the_last_model() {
    let workspace = Workspace::new();
    let (client, server) = scripted_http(vec![
        (200, completed("a-one")),
        (200, completed("b-one")),
        (200, completed("a-two")),
    ])
    .await;
    let (handle, actor) = runtime(client, &workspace, "model-a", Some(catalog())).start();

    let created = create_session(&handle).await;
    let id = created.metadata.as_ref().unwrap().id.clone();
    assert_eq!(created.config_revision, Some(1));
    assert_eq!(
        created.effective_config.as_ref().unwrap().model.as_str(),
        "model-a"
    );

    let task = send_task(&handle, &id, "first a").await;
    assert_eq!(finish(task.events).await.status, "completed");
    let first = last_task_configuration(&history_for(&handle, &id).await);
    assert_eq!(first.model, "model-a");
    assert_eq!(first.api, RESPONSES_API);
    assert_eq!(first.config_revision, 1);
    assert_eq!(
        first.compaction,
        created.effective_config.as_ref().unwrap().compaction
    );

    let updated = config_update(&handle, &id, 1, patch_model("model-b"))
        .await
        .unwrap();
    assert_eq!(updated.config_revision, 2);
    assert_eq!(updated.effective_config.model, "model-b");
    assert_eq!(
        updated.effective_config.capacity_source,
        CapacitySource::Catalog {
            catalog_id: "model-b".to_owned()
        }
    );

    let task = send_task(&handle, &id, "then b").await;
    assert_eq!(finish(task.events).await.status, "completed");
    let second = last_task_configuration(&history_for(&handle, &id).await);
    assert_eq!(second.model, "model-b");
    assert_eq!(second.api, RESPONSES_API);
    assert_eq!(second.config_revision, 2);
    assert_eq!(second.compaction, updated.effective_config.compaction);
    let history_after_b = workspace.history_bytes(&id);

    let updated = config_update(&handle, &id, 2, patch_model("model-a"))
        .await
        .unwrap();
    assert_eq!(updated.config_revision, 3);
    assert_eq!(updated.effective_config.model, "model-a");
    let task = send_task(&handle, &id, "back to a").await;
    assert_eq!(finish(task.events).await.status, "completed");
    let third = last_task_configuration(&history_for(&handle, &id).await);
    assert_eq!(third.model, "model-a");
    assert_eq!(third.api, RESPONSES_API);
    assert_eq!(third.config_revision, 3);
    assert_eq!(third.compaction, updated.effective_config.compaction);
    assert!(workspace.history_bytes(&id).starts_with(&history_after_b));

    let updated = config_update(&handle, &id, 3, patch_model("model-b"))
        .await
        .unwrap();
    assert_eq!(updated.config_revision, 4);
    assert_eq!(updated.effective_config.model, "model-b");
    assert!(matches!(
        handle
            .call(RuntimeCommand::Release {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
        RuntimeReply::Released { .. }
    ));
    assert!(matches!(
        handle.call(RuntimeCommand::Shutdown).await.unwrap(),
        RuntimeReply::Shutdown { state: "draining" }
    ));
    drop(handle);
    actor.await.unwrap().unwrap();
    let bodies = request_bodies(server).await;
    assert_eq!(bodies.len(), 3);
    assert_eq!(bodies[0]["model"], "model-a");
    assert_eq!(bodies[1]["model"], "model-b");
    assert_eq!(bodies[2]["model"], "model-a");

    let (client, server) = scripted_http(vec![(200, completed("b-two"))]).await;
    let (handle, actor) = runtime(client, &workspace, "model-a", Some(catalog())).start();
    let loaded = session(
        handle
            .call(RuntimeCommand::Load {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    assert_eq!(loaded.config_revision, Some(4));
    assert_eq!(
        loaded.effective_config.as_ref().unwrap().model.as_str(),
        "model-b"
    );
    let loaded_config = config_get(&handle, &id).await;
    assert_eq!(loaded_config.config_revision, 4);
    assert_eq!(loaded_config.effective_config.model, "model-b");
    let task = send_task(&handle, &id, "continue on b").await;
    assert_eq!(finish(task.events).await.status, "completed");
    let resumed = last_task_configuration(&history_for(&handle, &id).await);
    assert_eq!(resumed.model, "model-b");
    assert_eq!(resumed.api, RESPONSES_API);
    assert_eq!(resumed.config_revision, 4);
    assert_eq!(
        resumed.compaction,
        loaded_config.effective_config.compaction
    );
    let body = request_one(server).await;
    assert_eq!(body["model"], "model-b");
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    drop(handle);
    actor.await.unwrap().unwrap();
}

#[tokio::test]
async fn revision_conflicts_noop_writes_and_busy_failures_are_isolated_per_session() {
    let workspace = Workspace::new();
    let (client, arrived, release, server) = paused_model().await;
    let (handle, actor) = runtime(client, &workspace, "model-a", Some(catalog())).start();
    let a = create_session(&handle).await.metadata.unwrap().id;
    let b = create_session(&handle).await.metadata.unwrap().id;
    let history_before = workspace.history_bytes(&a);
    let history_before_b = workspace.history_bytes(&b);

    // Both callers submit before either result is awaited. The actor must
    // reserve the session and reject the second with a revision conflict, not
    // a broad busy response.
    let first_response = handle
        .submit(RuntimeCommand::ConfigUpdate {
            allow_schema_upgrade: false,
            acknowledge_unknown_origin: false,
            session_id: a.clone(),
            expected_revision: 1,
            patch: patch_model("model-b"),
        })
        .unwrap();
    let second_response = handle
        .submit(RuntimeCommand::ConfigUpdate {
            allow_schema_upgrade: false,
            acknowledge_unknown_origin: false,
            session_id: a.clone(),
            expected_revision: 1,
            patch: patch_model("model-b"),
        })
        .unwrap();
    let first = resolve_config(first_response).await;
    let second = resolve_config(second_response).await;
    let results = [first, second];
    let successes: Vec<_> = results.iter().filter(|result| result.is_ok()).collect();
    assert_eq!(
        successes.len(),
        1,
        "exactly one caller must commit revision 1"
    );
    let success = successes[0].as_ref().unwrap();
    assert_eq!(success.config_revision, 2);
    let conflict = results
        .iter()
        .find_map(|result| result.as_ref().err())
        .expect("one caller must fail");
    assert_eq!(conflict.code, "config_conflict");
    assert_ne!(conflict.code, "busy");
    assert_eq!(config_get(&handle, &a).await.config_revision, 2);
    let metadata: Value = serde_json::from_slice(&workspace.metadata_bytes(&a)).unwrap();
    assert_eq!(metadata["model"], "model-b");
    assert_eq!(metadata["config_revision"].as_u64(), Some(2));
    assert_eq!(workspace.history_bytes(&a), history_before);

    // Same model and same explicit compaction are a no-op even though the
    // patch's explicit capacity provenance differs from the stored catalog
    // provenance. It must not rewrite metadata or bump the revision.
    let effective_compaction = success.effective_config.compaction;
    let metadata_before = workspace.metadata_bytes(&a);
    let noop = config_update(
        &handle,
        &a,
        2,
        patch_model_compaction("model-b", effective_compaction),
    )
    .await
    .unwrap();
    assert_eq!(noop.config_revision, 2);
    assert_eq!(noop.effective_config.model, "model-b");
    assert_eq!(noop.effective_config.compaction, effective_compaction);
    assert_eq!(
        noop.effective_config.capacity_source,
        CapacitySource::Catalog {
            catalog_id: "model-b".to_owned()
        }
    );
    assert_eq!(workspace.metadata_bytes(&a), metadata_before);
    assert_eq!(workspace.history_bytes(&a), history_before);

    let task = send_task(&handle, &a, "pause A").await;
    arrived.await.unwrap();
    let history_after_task_start = workspace.history_bytes(&a);
    let busy = config_update(&handle, &a, 2, patch_model("model-a"))
        .await
        .unwrap_err();
    assert_eq!(busy.code, "busy");
    let b_updated = config_update(&handle, &b, 1, patch_model("model-b"))
        .await
        .unwrap();
    assert_eq!(b_updated.config_revision, 2);
    assert_eq!(config_get(&handle, &a).await.config_revision, 2);
    assert_eq!(config_get(&handle, &b).await.config_revision, 2);
    assert_eq!(workspace.history_bytes(&a), history_after_task_start);
    assert_eq!(workspace.history_bytes(&b), history_before_b);

    assert!(matches!(
        handle
            .call(RuntimeCommand::Cancel {
                session_id: a.clone(),
                task_id: task.association.task_id.clone(),
            })
            .await
            .unwrap(),
        RuntimeReply::Cancelled { .. }
    ));
    release.send(()).unwrap();
    assert_ne!(finish(task.events).await.status, "completed");
    let snapshot = last_task_configuration(&history_for(&handle, &a).await);
    assert_eq!(snapshot.model, "model-b");
    assert_eq!(snapshot.config_revision, 2);
    let body = request_body(server).await;
    assert_eq!(body["model"], "model-b");
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    drop(handle);
    actor.await.unwrap().unwrap();
}

#[tokio::test]
async fn unknown_invalid_and_opaque_config_changes_leave_disk_and_history_untouched() {
    let workspace = Workspace::new();
    let (client, _server) = scripted_http(vec![]).await;
    let (handle, actor) = runtime(client, &workspace, "model-a", Some(catalog())).start();
    let session_id = create_session(&handle).await.metadata.unwrap().id;
    let metadata_before = workspace.metadata_bytes(&session_id);
    let history_before = workspace.history_bytes(&session_id);

    let unknown = config_update(&handle, &session_id, 1, patch_model("model-missing"))
        .await
        .unwrap_err();
    assert_eq!(unknown.code, "model_limits_required");
    assert_eq!(workspace.metadata_bytes(&session_id), metadata_before);
    assert_eq!(workspace.history_bytes(&session_id), history_before);

    let invalid = config_update(
        &handle,
        &session_id,
        1,
        patch_model_compaction("model-a", invalid_compaction()),
    )
    .await
    .unwrap_err();
    assert_eq!(invalid.code, "invalid_model_limits");
    assert_eq!(workspace.metadata_bytes(&session_id), metadata_before);
    assert_eq!(workspace.history_bytes(&session_id), history_before);
    assert_eq!(config_get(&handle, &session_id).await.config_revision, 1);
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    drop(handle);
    actor.await.unwrap().unwrap();

    let workspace = Workspace::new();
    let (client, server) = scripted_http(vec![(200, completed_with_native_reasoning())]).await;
    let (handle, actor) = runtime(client, &workspace, "model-a", Some(catalog())).start();
    let session_id = create_session(&handle).await.metadata.unwrap().id;
    let task = send_task(&handle, &session_id, "make opaque history").await;
    assert_eq!(finish(task.events).await.status, "completed");
    let metadata_before = workspace.metadata_bytes(&session_id);
    let history_before = workspace.history_bytes(&session_id);
    let opaque = config_update(&handle, &session_id, 1, patch_model("model-b"))
        .await
        .unwrap_err();
    assert_eq!(opaque.code, "continuation_not_supported");
    assert_eq!(workspace.metadata_bytes(&session_id), metadata_before);
    assert_eq!(workspace.history_bytes(&session_id), history_before);
    assert_eq!(config_get(&handle, &session_id).await.config_revision, 1);
    let _ = request_one(server).await;
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    drop(handle);
    actor.await.unwrap().unwrap();
}

#[tokio::test]
async fn config_incompatible_rejects_chat_image_history_without_writes() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = store
        .create_with_api(
            &workspace.0,
            "model-a",
            SYSTEM_PROMPT,
            CHAT_COMPLETIONS_API,
            large_compaction(),
        )
        .unwrap();
    {
        let mut loaded = store.open_for_api(&id, CHAT_COMPLETIONS_API).unwrap();
        loaded.conversation.add_user_message(UserMessage {
            text: "stored image turn".to_owned(),
            images: vec![ImageAttachment::new(
                "image.png",
                "data:image/png;base64,AAAA",
            )],
            context: Vec::new(),
        });
        loaded
            .conversation
            .add_assistant_message("stored image acknowledged".to_owned());
        loaded.conversation.save_history().unwrap();
    }

    let metadata_before = workspace.metadata_bytes(&id);
    let history_before = workspace.history_bytes(&id);
    let history_text = String::from_utf8_lossy(&history_before);
    assert!(history_text.contains("data:image/png;base64,AAAA"));

    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    listener.set_nonblocking(true).unwrap();
    let client = ModelClient::new(&ClientConfig::new(
        ModelApi::ChatCompletions,
        "offline-key",
        format!("http://{}/v1", listener.local_addr().unwrap()),
    ))
    .unwrap();
    let (handle, actor) = runtime(client, &workspace, "model-a", Some(catalog())).start();

    let loaded = session(
        handle
            .call(RuntimeCommand::Load {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    assert!(loaded.loaded);
    assert_eq!(loaded.config_revision, Some(1));
    let before = config_get(&handle, &id).await;
    assert_eq!(before.effective_config.api, CHAT_COMPLETIONS_API);
    assert_eq!(before.effective_config.model, "model-a");
    assert_eq!(before.config_revision, 1);
    let records_before = serde_json::to_value(history_for(&handle, &id).await.records).unwrap();

    let failed = config_update(
        &handle,
        &id,
        1,
        patch_model_compaction("model-b", small_compaction()),
    )
    .await
    .unwrap_err();
    assert_eq!(failed.code, "config_incompatible");

    let after = config_get(&handle, &id).await;
    assert_eq!(after.effective_config.api, CHAT_COMPLETIONS_API);
    assert_eq!(after.effective_config.model, "model-a");
    assert_eq!(after.config_revision, 1);
    assert_eq!(
        after.effective_config.compaction,
        before.effective_config.compaction
    );
    assert_eq!(
        serde_json::to_value(history_for(&handle, &id).await.records).unwrap(),
        records_before
    );
    assert_eq!(workspace.metadata_bytes(&id), metadata_before);
    assert_eq!(workspace.history_bytes(&id), history_before);
    assert_eq!(
        listener.accept().unwrap_err().kind(),
        std::io::ErrorKind::WouldBlock
    );

    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    drop(handle);
    actor.await.unwrap().unwrap();
}

#[tokio::test]
async fn legacy_metadata_without_revision_reads_as_zero() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = store
        .create_with_api(
            &workspace.0,
            "model-a",
            SYSTEM_PROMPT,
            RESPONSES_API,
            large_compaction(),
        )
        .unwrap();
    let path = store.directory(&id).unwrap().join("metadata.json");
    let mut metadata: Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
    metadata.as_object_mut().unwrap().remove("config_revision");
    metadata.as_object_mut().unwrap().remove("capacity_source");
    fs::write(&path, serde_json::to_vec_pretty(&metadata).unwrap()).unwrap();

    let (client, _server) = scripted_http(vec![]).await;
    let (handle, actor) = runtime(client, &workspace, "model-a", Some(catalog())).start();
    let loaded = session(
        handle
            .call(RuntimeCommand::Load {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    assert_eq!(loaded.config_revision, Some(0));
    let view = config_get(&handle, &id).await;
    assert_eq!(view.config_revision, 0);
    assert_eq!(
        view.effective_config.capacity_source,
        CapacitySource::Stored
    );
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    drop(handle);
    actor.await.unwrap().unwrap();
}

#[tokio::test]
async fn metadata_commit_failures_preserve_or_isolate_the_old_state_as_specified() {
    for point in [
        metadata_fault::Point::BeforeWrite,
        metadata_fault::Point::BeforeRename,
    ] {
        let workspace = Workspace::new();
        let (client, server) = scripted_http(vec![(200, completed("old model task"))]).await;
        let (handle, actor) = runtime(client, &workspace, "model-a", Some(catalog())).start();
        let id = create_session(&handle).await.metadata.unwrap().id;
        let metadata_before = workspace.metadata_bytes(&id);
        let history_before = workspace.history_bytes(&id);
        let guard = metadata_fault::Guard::new(workspace.directory(&id), point);
        let failed = config_update(&handle, &id, 1, patch_model("model-b"))
            .await
            .unwrap_err();
        drop(guard);
        assert_eq!(failed.code, "io_error");
        assert_eq!(workspace.metadata_bytes(&id), metadata_before);
        assert_eq!(workspace.history_bytes(&id), history_before);
        let view = config_get(&handle, &id).await;
        assert_eq!(view.config_revision, 1);
        assert_eq!(view.effective_config.model, "model-a");

        let task = send_task(&handle, &id, "after failed config").await;
        assert_eq!(finish(task.events).await.status, "completed");
        let body = request_one(server).await;
        assert_eq!(body["model"], "model-a");
        handle.call(RuntimeCommand::Shutdown).await.unwrap();
        drop(handle);
        actor.await.unwrap().unwrap();
    }
}

#[tokio::test]
async fn after_rename_failure_is_commit_unknown_and_reload_recovers_the_new_config() {
    let workspace = Workspace::new();
    let (client, server) = scripted_http(vec![(200, completed("new model task"))]).await;
    let (handle, actor) = runtime(client, &workspace, "model-a", Some(catalog())).start();
    let id = create_session(&handle).await.metadata.unwrap().id;
    let metadata_before = workspace.metadata_bytes(&id);
    let history_before = workspace.history_bytes(&id);
    let guard =
        metadata_fault::Guard::new(workspace.directory(&id), metadata_fault::Point::AfterRename);
    let failed = config_update(&handle, &id, 1, patch_model("model-b"))
        .await
        .unwrap_err();
    drop(guard);
    assert_eq!(failed.code, "commit_unknown");
    assert_ne!(workspace.metadata_bytes(&id), metadata_before);
    assert_eq!(workspace.history_bytes(&id), history_before);

    let view = config_get(&handle, &id).await;
    assert_eq!(view.config_revision, 2);
    assert_eq!(view.effective_config.model, "model-b");
    let rejected = match handle
        .call(RuntimeCommand::Send {
            session_id: id.clone(),
            message: UserMessage::text("must not send while unknown"),
        })
        .await
    {
        Err(error) => error,
        Ok(_) => panic!("send was accepted after commit_unknown"),
    };
    assert_eq!(rejected.code, "commit_unknown");

    assert!(matches!(
        handle
            .call(RuntimeCommand::Release {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
        RuntimeReply::Released { .. }
    ));
    let loaded = session(
        handle
            .call(RuntimeCommand::Load {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    assert_eq!(loaded.config_revision, Some(2));
    assert_eq!(
        loaded.effective_config.as_ref().unwrap().model.as_str(),
        "model-b"
    );
    let task = send_task(&handle, &id, "after reload").await;
    assert_eq!(finish(task.events).await.status, "completed");
    let body = request_one(server).await;
    assert_eq!(body["model"], "model-b");
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    drop(handle);
    actor.await.unwrap().unwrap();
}

#[tokio::test]
async fn lowered_budget_compacts_with_the_new_model_before_generation() {
    let workspace = Workspace::new();
    let id = seed_long_session(&workspace);
    let (client, server) = scripted_http(vec![
        (200, completed("summary from model b")),
        (200, completed("generation from model b")),
    ])
    .await;
    let (handle, actor) = runtime(client, &workspace, "model-a", None).start();
    let loaded = session(
        handle
            .call(RuntimeCommand::Load {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    assert_eq!(loaded.config_revision, Some(1));
    let updated = config_update(
        &handle,
        &id,
        1,
        patch_model_compaction("model-b", small_compaction()),
    )
    .await
    .unwrap();
    assert_eq!(updated.config_revision, 2);
    assert_eq!(updated.effective_config.model, "model-b");
    assert_eq!(updated.effective_config.compaction, small_compaction());

    let task = send_task(&handle, &id, "trigger compaction").await;
    assert_eq!(finish(task.events).await.status, "completed");
    let page = history_for(&handle, &id).await;
    let snapshot = last_task_configuration(&page);
    assert_eq!(snapshot.model, "model-b");
    assert_eq!(snapshot.config_revision, 2);
    assert_eq!(snapshot.compaction, small_compaction());
    assert!(
        page.records
            .iter()
            .any(|record| matches!(&record.record, JournalRecord::Compaction { .. }))
    );
    let bodies = request_bodies(server).await;
    assert_eq!(bodies.len(), 2);
    assert_eq!(bodies[0]["model"], "model-b");
    assert_eq!(bodies[1]["model"], "model-b");
    assert!(bodies[0]["input"].to_string().contains("original-source"));
    assert!(
        bodies[1]["input"]
            .to_string()
            .contains("summary from model b")
    );
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    drop(handle);
    actor.await.unwrap().unwrap();
}

#[tokio::test]
async fn failed_compaction_keeps_context_and_does_not_fallback_to_generation() {
    let workspace = Workspace::new();
    let id = seed_long_session(&workspace);
    let (client, server) = scripted_http(vec![
        (
            500,
            json!({"error": {"message": "summary fixture failed", "type": "server_error"}}),
        ),
        (200, completed("after failed compaction")),
    ])
    .await;
    let (handle, actor) = runtime(client, &workspace, "model-a", None).start();
    session(
        handle
            .call(RuntimeCommand::Load {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    config_update(
        &handle,
        &id,
        1,
        patch_model_compaction("model-b", small_compaction()),
    )
    .await
    .unwrap();
    let task = send_task(&handle, &id, "compaction must fail").await;
    let failed = finish(task.events).await;
    assert_ne!(failed.status, "completed");

    let updated = config_update(
        &handle,
        &id,
        2,
        patch_model_compaction("model-b", large_compaction()),
    )
    .await
    .unwrap();
    assert_eq!(updated.config_revision, 3);
    let task = send_task(&handle, &id, "after failure without compaction").await;
    assert_eq!(finish(task.events).await.status, "completed");
    let page = history_for(&handle, &id).await;
    assert!(
        !page
            .records
            .iter()
            .any(|record| matches!(&record.record, JournalRecord::Compaction { .. }))
    );
    let bodies = request_bodies(server).await;
    assert_eq!(bodies.len(), 2);
    assert_eq!(bodies[0]["model"], "model-b");
    assert_eq!(bodies[1]["model"], "model-b");
    assert!(bodies[1]["input"].to_string().contains("original-source"));
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    drop(handle);
    actor.await.unwrap().unwrap();
}

#[tokio::test]
async fn returning_to_a_model_does_not_resurrect_its_old_usage_calibration() {
    let workspace = Workspace::new();
    let mut first_response = completed("first answer");
    first_response["usage"] = json!({"input_tokens":600000,"output_tokens":10});
    let (client, server) = scripted_http(vec![
        (200, first_response),
        (200, completed("fresh estimate")),
    ])
    .await;
    let (handle, actor) = runtime(client, &workspace, "model-a", Some(catalog())).start();
    let id = create_session(&handle).await.metadata.unwrap().id;
    let task = send_task(&handle, &id, "initial message").await;
    assert_eq!(finish(task.events).await.status, "completed");
    config_update(&handle, &id, 1, patch_model("model-b"))
        .await
        .unwrap();
    config_update(&handle, &id, 2, patch_model("model-a"))
        .await
        .unwrap();
    let mut task = send_task(&handle, &id, "continue after changing back").await;
    let mut context_tokens = None;
    let mut terminal = None;
    while let Some(event) = task.events.next().await {
        match event {
            TaskEvent::Event(packet) => {
                if let crate::agent::AgentEvent::ContextUpdated {
                    context_tokens: tokens,
                    ..
                } = packet.event
                {
                    context_tokens = tokens;
                }
            }
            TaskEvent::Finished(finished) => terminal = Some(finished),
        }
    }
    assert_eq!(terminal.unwrap().status, "completed");
    assert!(context_tokens.unwrap() < large_compaction().context_budget_tokens);
    let bodies = request_bodies(server).await;
    assert_eq!(
        bodies.len(),
        2,
        "configuration must not cause stale-usage compaction requests"
    );
    assert!(
        bodies[1]["input"]
            .to_string()
            .contains("continue after changing back")
    );
    assert!(
        !bodies[1]["input"]
            .to_string()
            .contains("Write a concise handoff summary")
    );
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    drop(handle);
    actor.await.unwrap().unwrap();
}

#[tokio::test]
async fn each_connections_default_model_uses_its_own_loaded_capacity() {
    use crate::client::{
        ClientConfig, ModelApi,
        connections::{Connection, ConnectionRegistry},
    };
    let workspace = Workspace::new();
    let connection = |id: &str, model: &str| {
        Connection::new(
            id.into(),
            ClientConfig::new(
                ModelApi::Responses,
                "offline-key",
                format!("http://127.0.0.1:1/{id}"),
            ),
            model.into(),
        )
        .unwrap()
    };
    let directory = ConnectionRegistry::new(
        "a".into(),
        vec![connection("a", "model-a"), connection("b", "model-b")],
    )
    .unwrap();
    let (client, server) = scripted_http(vec![]).await;
    let (handle, actor) = runtime(client, &workspace, "model-a", Some(catalog()))
        .with_connections(directory)
        .start();
    let created = session(
        handle
            .call(RuntimeCommand::CreateConfigured {
                storage_schema: 2,
                config: ConfigPatch {
                    connection_id: Some("b".into()),
                    ..ConfigPatch::default()
                },
            })
            .await
            .unwrap(),
    );
    let view = config_get(&handle, &created.session_id).await;
    assert_eq!(view.effective_config.model, "model-b");
    assert_eq!(
        view.effective_config.compaction.context_budget_tokens,
        64000
    );
    assert_eq!(
        view.effective_config.compaction.max_input_tokens,
        Some(60000)
    );
    assert!(
        matches!(view.effective_config.capacity_source,CapacitySource::Catalog{ref catalog_id} if catalog_id=="model-b")
    );
    match handle
        .call(RuntimeCommand::Models {
            connection_id: "b".into(),
        })
        .await
        .unwrap()
    {
        RuntimeReply::Models {
            configured_models, ..
        } => assert_eq!(
            configured_models[0].compaction,
            view.effective_config.compaction
        ),
        _ => panic!("models expected"),
    }
    let a = session(
        handle
            .call(RuntimeCommand::CreateConfigured {
                storage_schema: 2,
                config: ConfigPatch {
                    connection_id: Some("a".into()),
                    ..ConfigPatch::default()
                },
            })
            .await
            .unwrap(),
    );
    let changed = config_update(
        &handle,
        &a.session_id,
        1,
        ConfigPatch {
            connection_id: Some("b".into()),
            model: Some("model-b".into()),
            ..ConfigPatch::default()
        },
    )
    .await
    .unwrap();
    assert_eq!(
        changed.effective_config.compaction,
        view.effective_config.compaction
    );
    assert!(
        matches!(changed.effective_config.capacity_source,CapacitySource::Catalog{ref catalog_id} if catalog_id=="model-b")
    );
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    drop(handle);
    actor.await.unwrap().unwrap();
    assert!(request_bodies(server).await.is_empty());
}

#[tokio::test]
async fn accepted_before_started_configuration_is_busy_but_idle_session_can_update() {
    let workspace = Workspace::new();
    let (client, server) = scripted_http(vec![(200, completed("done"))]).await;
    let (release_start, start_gate) = oneshot::channel();
    let (handle, actor) = runtime(client, &workspace, "model-a", Some(catalog()))
        .with_task_start_gate(start_gate)
        .start();
    let a = create_session(&handle).await.metadata.unwrap().id;
    let b = create_session(&handle).await.metadata.unwrap().id;
    let metadata_before = workspace.metadata_bytes(&a);
    let history_before = workspace.history_bytes(&a);
    let task = send_task(&handle, &a, "accepted configuration snapshot").await;
    match handle
        .call(RuntimeCommand::TaskStatus {
            session_id: a.clone(),
            task_id: task.association.task_id.clone(),
        })
        .await
        .unwrap()
    {
        RuntimeReply::TaskStatus(state) => assert_eq!(state.phase, "accepted"),
        _ => panic!("expected accepted task status"),
    }
    let error = config_update(&handle, &a, 1, patch_model("model-b"))
        .await
        .unwrap_err();
    assert_eq!(error.code, "busy");
    assert_eq!(workspace.metadata_bytes(&a), metadata_before);
    assert_eq!(workspace.history_bytes(&a), history_before);
    let unchanged = config_get(&handle, &a).await;
    assert_eq!(unchanged.config_revision, 1);
    assert_eq!(unchanged.effective_config.model, "model-a");
    let changed = config_update(&handle, &b, 1, patch_model("model-b"))
        .await
        .unwrap();
    assert_eq!(changed.config_revision, 2);
    assert_eq!(changed.effective_config.model, "model-b");
    release_start.send(()).unwrap();
    assert_eq!(finish(task.events).await.status, "completed");
    let snapshot = last_task_configuration(&history_for(&handle, &a).await);
    assert_eq!(snapshot.model, "model-a");
    assert_eq!(snapshot.config_revision, 1);
    assert_eq!(request_one(server).await["model"], "model-a");
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    drop(handle);
    actor.await.unwrap().unwrap();
}
