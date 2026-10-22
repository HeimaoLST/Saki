use super::*;
use crate::{
    agent::{
        conversation::CompactionConfig,
        journal::{self, JournalRecord},
    },
    client::{config::RESPONSES_API, connections::ConnectionIdentity},
    session::config::{CapacitySource, ToolSelection},
};
use serde_json::Value;
use sha2::{Digest, Sha256};
use std::{
    fs,
    path::PathBuf,
    sync::atomic::{AtomicUsize, Ordering},
};

struct Workspace(PathBuf);

impl Workspace {
    fn new() -> Self {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        let path = std::env::temp_dir().join(format!(
            "saki-store-config-{}-{}",
            journal::new_id().unwrap(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir(&path).unwrap();
        Self(path)
    }

    fn store(&self) -> SessionStore {
        SessionStore::new(self.0.join("sessions")).unwrap()
    }
}

impl Drop for Workspace {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn compaction() -> CompactionConfig {
    CompactionConfig::new(262_144)
}

fn identity() -> ConnectionIdentity {
    ConnectionIdentity {
        connection_id: "provider-a".to_owned(),
        api: RESPONSES_API.to_owned(),
        endpoint: "https://example.com/v1".to_owned(),
    }
}

fn legacy(store: &SessionStore, workspace: &Workspace) -> String {
    store
        .create_with_api(
            &workspace.0,
            "model-a",
            "system prompt",
            RESPONSES_API,
            compaction(),
        )
        .unwrap()
}

fn schema2_candidate(metadata: &SessionMetadata) -> SessionMetadata {
    let mut next = metadata.clone();
    next.schema_version = 2;
    next.model = "model-b".to_owned();
    next.connection_identity = Some(identity());
    next.binding_origin = Some("explicit_legacy_binding".to_owned());
    next.bound_at_revision = Some(metadata.config_revision + 1);
    next.system_prompt = Some("new prompt".to_owned());
    next.tool_selection = Some(ToolSelection {
        builtin: vec!["read".to_owned(), "bash".to_owned()],
        external: Vec::new(),
    });
    next.capacity_source = CapacitySource::Explicit;
    next
}

fn metadata_path(store: &SessionStore, id: &str) -> PathBuf {
    store.directory(id).unwrap().join("metadata.json")
}

fn history_path(store: &SessionStore, id: &str) -> PathBuf {
    store.directory(id).unwrap().join("history.jsonl")
}

fn digest(bytes: &[u8]) -> String {
    let mut hasher = Sha256::new();
    hasher.update(bytes);
    format!("{:x}", hasher.finalize())
}

fn backup_paths(store: &SessionStore, id: &str) -> (PathBuf, PathBuf) {
    let directory = store.directory(id).unwrap();
    (
        directory.join("metadata.schema1.backup.json"),
        directory.join("metadata.schema1.backup.state.json"),
    )
}

#[test]
fn schema_one_and_schema_two_creation_have_the_expected_fields() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let old = legacy(&store, &workspace);
    let old_meta = store.metadata(&old).unwrap();
    assert_eq!(old_meta.schema_version, 1);
    assert!(old_meta.connection_identity.is_none());
    assert!(old_meta.binding_origin.is_none());
    assert!(old_meta.bound_at_revision.is_none());
    assert!(old_meta.system_prompt.is_none());
    assert!(old_meta.tool_selection.is_none());

    let enhanced = store
        .create_configured(SessionCreation {
            cwd: &workspace.0,
            model: "model-a",
            system_prompt: "enhanced prompt",
            api: RESPONSES_API,
            compaction: compaction(),
            capacity_source: CapacitySource::Explicit,
            enhanced: Some(EnhancedCreation {
                connection_identity: identity(),
                tool_selection: ToolSelection {
                    builtin: vec!["read".to_owned(), "edit".to_owned()],
                    external: Vec::new(),
                },
            }),
        })
        .unwrap();
    let meta = store.metadata(&enhanced).unwrap();
    assert_eq!(meta.schema_version, 2);
    assert_eq!(meta.config_revision, 1);
    assert_eq!(meta.binding_origin.as_deref(), Some("created"));
    assert_eq!(meta.bound_at_revision, Some(1));
    assert_eq!(meta.system_prompt.as_deref(), Some("enhanced prompt"));
    assert_eq!(
        meta.tool_selection,
        Some(ToolSelection {
            builtin: vec!["read".to_owned(), "edit".to_owned()],
            external: Vec::new(),
        })
    );
    assert_eq!(meta.connection_identity.as_ref(), Some(&identity()));
}

#[test]
fn lock_config_locks_and_reads_without_repairing_a_torn_tail() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = legacy(&store, &workspace);
    let path = history_path(&store, &id);
    fs::OpenOptions::new()
        .append(true)
        .open(&path)
        .unwrap()
        .write_all(b"{\"schema_version\":1")
        .unwrap();
    let before = fs::read(&path).unwrap();

    let first = store.lock_config(&id).unwrap();
    assert_eq!(first.metadata.config_revision, 1);
    assert!(first.replay.valid_bytes < before.len() as u64);
    let locked = store.lock_config(&id).err().expect("second lock must fail");
    assert_eq!(locked.code, "locked");
    assert_eq!(fs::read(&path).unwrap(), before);
    drop(first);
    assert_eq!(fs::read(&path).unwrap(), before);
}

#[test]
fn update_config_cas_preserves_identity_and_schema_one_to_two_backs_up_exact_bytes() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = legacy(&store, &workspace);
    let metadata_before = fs::read(metadata_path(&store, &id)).unwrap();
    let journal_before = fs::read(history_path(&store, &id)).unwrap();
    let old = store.metadata(&id).unwrap();

    let updated = store
        .update_config(&id, 1, schema2_candidate(&old))
        .unwrap();
    assert_eq!(updated.schema_version, 2);
    assert_eq!(updated.config_revision, 2);
    assert_eq!(updated.created_at, old.created_at);
    assert_eq!(updated.working_directory, old.working_directory);
    assert_eq!(updated.title, old.title);
    assert_eq!(updated.api, RESPONSES_API);

    let (backup, state) = backup_paths(&store, &id);
    assert_eq!(fs::read(&backup).unwrap(), metadata_before);
    assert_eq!(fs::read(history_path(&store, &id)).unwrap(), journal_before);
    let state: Value = serde_json::from_slice(&fs::read(state).unwrap()).unwrap();
    assert_eq!(
        state["journal_len"].as_u64(),
        Some(journal_before.len() as u64)
    );
    assert_eq!(
        state["journal_sha256"].as_str(),
        Some(digest(&journal_before).as_str())
    );

    let conflict = store.update_config(&id, 1, old).unwrap_err();
    assert_eq!(conflict.code, "config_conflict");
    assert_eq!(conflict.current_revision, Some(2));
}

#[test]
fn schema_one_to_two_primary_faults_preserve_old_metadata_and_journal() {
    for point in [
        metadata_fault::Point::BeforeWrite,
        metadata_fault::Point::BeforeRename,
    ] {
        let workspace = Workspace::new();
        let store = workspace.store();
        let id = legacy(&store, &workspace);
        let before_metadata = fs::read(metadata_path(&store, &id)).unwrap();
        let before_journal = fs::read(history_path(&store, &id)).unwrap();
        let candidate = schema2_candidate(&store.metadata(&id).unwrap());
        let guard = metadata_fault::Guard::new(store.directory(&id).unwrap(), point);
        let error = store.update_config(&id, 1, candidate).unwrap_err();
        drop(guard);
        assert_eq!(error.code, "io_error");
        assert_eq!(
            fs::read(metadata_path(&store, &id)).unwrap(),
            before_metadata
        );
        assert_eq!(fs::read(history_path(&store, &id)).unwrap(), before_journal);
    }

    let workspace = Workspace::new();
    let store = workspace.store();
    let id = legacy(&store, &workspace);
    let candidate = schema2_candidate(&store.metadata(&id).unwrap());
    let guard = metadata_fault::Guard::new(
        store.directory(&id).unwrap(),
        metadata_fault::Point::AfterRename,
    );
    let error = store.update_config(&id, 1, candidate).unwrap_err();
    drop(guard);
    assert_eq!(error.code, "commit_unknown");
    assert_eq!(store.metadata(&id).unwrap().schema_version, 2);
    assert_eq!(store.metadata(&id).unwrap().config_revision, 2);
}

#[test]
fn rollback_restores_exact_schema_one_bytes_but_rejects_changed_journal() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = legacy(&store, &workspace);
    let original_metadata = fs::read(metadata_path(&store, &id)).unwrap();
    let original_journal = fs::read(history_path(&store, &id)).unwrap();
    let candidate = schema2_candidate(&store.metadata(&id).unwrap());
    store.update_config(&id, 1, candidate).unwrap();
    assert_eq!(store.metadata(&id).unwrap().schema_version, 2);

    let restored = store.rollback_config(&id, 2).unwrap();
    assert_eq!(restored.schema_version, 1);
    assert_eq!(
        fs::read(metadata_path(&store, &id)).unwrap(),
        original_metadata
    );
    assert_eq!(
        fs::read(history_path(&store, &id)).unwrap(),
        original_journal
    );

    let workspace = Workspace::new();
    let store = workspace.store();
    let id = legacy(&store, &workspace);
    let candidate = schema2_candidate(&store.metadata(&id).unwrap());
    store.update_config(&id, 1, candidate).unwrap();
    let mut journal_file = fs::OpenOptions::new()
        .append(true)
        .open(history_path(&store, &id))
        .unwrap();
    journal::write(
        &mut journal_file,
        &JournalRecord::Messages { items: Vec::new() },
    )
    .unwrap();
    let changed_journal = fs::read(history_path(&store, &id)).unwrap();
    let error = store.rollback_config(&id, 2).unwrap_err();
    assert_eq!(error.code, "rollback_unsafe");
    assert_eq!(store.metadata(&id).unwrap().schema_version, 2);
    assert_eq!(
        fs::read(history_path(&store, &id)).unwrap(),
        changed_journal
    );
}

#[test]
fn create_configured_validates_before_creating_a_directory() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let mut wrong_identity = identity();
    wrong_identity.api = "openai-chat-completions".to_owned();
    let result = store.create_configured(SessionCreation {
        cwd: &workspace.0,
        model: "model-a",
        system_prompt: "prompt",
        api: RESPONSES_API,
        compaction: compaction(),
        capacity_source: CapacitySource::Explicit,
        enhanced: Some(EnhancedCreation {
            connection_identity: wrong_identity,
            tool_selection: ToolSelection {
                builtin: vec!["read".to_owned()],
                external: Vec::new(),
            },
        }),
    });
    assert!(result.is_err());
    assert!(store.list().unwrap().is_empty());
}

#[test]
fn schema_two_missing_fields_are_rejected() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = store
        .create_configured(SessionCreation {
            cwd: &workspace.0,
            model: "model-a",
            system_prompt: "prompt",
            api: RESPONSES_API,
            compaction: compaction(),
            capacity_source: CapacitySource::Stored,
            enhanced: Some(EnhancedCreation {
                connection_identity: identity(),
                tool_selection: ToolSelection {
                    builtin: vec!["read".to_owned()],
                    external: Vec::new(),
                },
            }),
        })
        .unwrap();
    let path = metadata_path(&store, &id);
    let mut metadata: Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
    metadata.as_object_mut().unwrap().remove("system_prompt");
    fs::write(&path, serde_json::to_vec_pretty(&metadata).unwrap()).unwrap();
    assert!(store.metadata(&id).is_err());
}

#[test]
fn schema_two_connection_api_and_bound_revision_are_validated() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = store
        .create_configured(SessionCreation {
            cwd: &workspace.0,
            model: "model-a",
            system_prompt: "prompt",
            api: RESPONSES_API,
            compaction: compaction(),
            capacity_source: CapacitySource::Stored,
            enhanced: Some(EnhancedCreation {
                connection_identity: identity(),
                tool_selection: ToolSelection {
                    builtin: vec!["read".to_owned()],
                    external: Vec::new(),
                },
            }),
        })
        .unwrap();
    let path = metadata_path(&store, &id);
    let original = fs::read(&path).unwrap();
    let mut metadata: Value = serde_json::from_slice(&original).unwrap();
    metadata["connection_identity"]["api"] = Value::String("openai-chat-completions".to_owned());
    fs::write(&path, serde_json::to_vec_pretty(&metadata).unwrap()).unwrap();
    assert!(store.metadata(&id).is_err());

    fs::write(&path, &original).unwrap();
    let mut metadata: Value = serde_json::from_slice(&original).unwrap();
    metadata["bound_at_revision"] = Value::from(99);
    fs::write(&path, serde_json::to_vec_pretty(&metadata).unwrap()).unwrap();
    assert!(store.metadata(&id).is_err());
}

#[test]
fn failed_schema_two_publish_refreshes_the_unused_schema_one_backup() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = legacy(&store, &workspace);
    let metadata_before = fs::read(metadata_path(&store, &id)).unwrap();
    let candidate = schema2_candidate(&store.metadata(&id).unwrap());
    let guard = metadata_fault::Guard::new(
        store.directory(&id).unwrap(),
        metadata_fault::Point::BeforeRename,
    );
    assert_eq!(
        store.update_config(&id, 1, candidate).unwrap_err().code,
        "io_error"
    );
    drop(guard);

    let mut journal_file = fs::OpenOptions::new()
        .append(true)
        .open(history_path(&store, &id))
        .unwrap();
    journal::write(
        &mut journal_file,
        &JournalRecord::Messages { items: Vec::new() },
    )
    .unwrap();
    let journal_after = fs::read(history_path(&store, &id)).unwrap();
    let candidate = schema2_candidate(&store.metadata(&id).unwrap());
    let updated = store.update_config(&id, 1, candidate).unwrap();
    assert_eq!(updated.schema_version, 2);
    let (backup, state) = backup_paths(&store, &id);
    assert_eq!(fs::read(backup).unwrap(), metadata_before);
    let state: Value = serde_json::from_slice(&fs::read(state).unwrap()).unwrap();
    assert_eq!(
        state["journal_len"].as_u64(),
        Some(journal_after.len() as u64)
    );
    assert_eq!(
        state["journal_sha256"].as_str(),
        Some(digest(&journal_after).as_str())
    );
    assert_eq!(state["bound_revision"].as_u64(), Some(2));
}

#[test]
fn rollback_requires_schema_two_cas_and_a_present_backup() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = legacy(&store, &workspace);
    let candidate = schema2_candidate(&store.metadata(&id).unwrap());
    store.update_config(&id, 1, candidate).unwrap();

    assert_eq!(
        store.rollback_config(&id, 1).unwrap_err().code,
        "config_conflict"
    );
    let (backup, state) = backup_paths(&store, &id);
    fs::remove_file(backup).unwrap();
    fs::remove_file(state).unwrap();
    let error = store.rollback_config(&id, 2).unwrap_err();
    assert_eq!(error.code, "rollback_unsafe");
    assert_eq!(store.metadata(&id).unwrap().schema_version, 2);
}

#[test]
fn changing_an_enhanced_schema_tag_does_not_enable_legacy_execution_or_repair_history() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = legacy(&store, &workspace);
    let old = store.metadata(&id).unwrap();
    store
        .update_config(&id, old.config_revision, schema2_candidate(&old))
        .unwrap();
    let metadata_path = metadata_path(&store, &id);
    let mut value: Value = serde_json::from_slice(&fs::read(&metadata_path).unwrap()).unwrap();
    value["schema_version"] = serde_json::json!(1);
    fs::write(&metadata_path, serde_json::to_vec(&value).unwrap()).unwrap();
    let history = store.directory(&id).unwrap().join("history.jsonl");
    let mut bytes = fs::read(&history).unwrap();
    bytes.extend_from_slice(b"{torn");
    fs::write(&history, &bytes).unwrap();
    let error = match store.open_for_api(&id, RESPONSES_API) {
        Ok(_) => panic!("enhanced fields were interpreted as legacy"),
        Err(error) => error,
    };
    assert!(error.to_string().contains("schema 2"));
    assert_eq!(fs::read(history).unwrap(), bytes);
    assert!(store.list().unwrap()[0].unavailable.is_some());
}

#[test]
fn every_metadata_mutation_honors_one_lock_and_retains_other_configuration_fields() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = legacy(&store, &workspace);
    let original = store.metadata(&id).unwrap();
    let original_bytes = fs::read(metadata_path(&store, &id)).unwrap();
    let lock = store.lock_metadata(&id).unwrap();
    assert!(
        store
            .rename(&id, "manual")
            .unwrap_err()
            .downcast_ref::<MetadataBusy>()
            .is_some()
    );
    assert!(
        store
            .claim_auto_title(&id)
            .unwrap_err()
            .downcast_ref::<MetadataBusy>()
            .is_some()
    );
    assert!(
        store
            .apply_auto_title(&id, "automatic")
            .unwrap_err()
            .downcast_ref::<MetadataBusy>()
            .is_some()
    );
    let error = store
        .update_config(&id, original.config_revision, schema2_candidate(&original))
        .unwrap_err();
    assert_eq!(error.code, "locked");
    assert_eq!(
        fs::read(metadata_path(&store, &id)).unwrap(),
        original_bytes
    );
    drop(lock);
    let updated = store
        .update_config(&id, original.config_revision, schema2_candidate(&original))
        .unwrap();
    let renamed = store.rename(&id, "manual").unwrap();
    assert_eq!(renamed.model, updated.model);
    assert_eq!(renamed.config_revision, updated.config_revision);
    assert_eq!(renamed.connection_identity, updated.connection_identity);
    let mut next = renamed.clone();
    next.model = "another-model".into();
    let committed = store
        .update_config(&id, renamed.config_revision, next)
        .unwrap();
    assert_eq!(committed.title, "manual");
    assert_eq!(committed.title_source.as_deref(), Some("manual"));
    let lock = store.lock_metadata(&id).unwrap();
    assert_eq!(
        store
            .rollback_config(&id, committed.config_revision)
            .unwrap_err()
            .code,
        "locked"
    );
    drop(lock);
}

#[test]
fn unsupported_persisted_tool_selection_is_rejected_before_history_repair() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = legacy(&store, &workspace);
    let old = store.metadata(&id).unwrap();
    store
        .update_config(&id, old.config_revision, schema2_candidate(&old))
        .unwrap();
    let metadata_path = metadata_path(&store, &id);
    let metadata: Value = serde_json::from_slice(&fs::read(&metadata_path).unwrap()).unwrap();
    let history_path = store.directory(&id).unwrap().join("history.jsonl");
    let mut history = fs::read(&history_path).unwrap();
    history.extend_from_slice(b"{\"torn\":");
    fs::write(&history_path, &history).unwrap();
    for selection in [
        serde_json::json!({"builtin":["unknown_builtin"],"external":[]}),
        serde_json::json!({"builtin":["read"],"external":[{"source_id":"project","tool_name":"lookup","exposed_name":"project_lookup"}]}),
    ] {
        let mut candidate = metadata.clone();
        candidate["tool_selection"] = selection;
        let bytes = serde_json::to_vec_pretty(&candidate).unwrap();
        fs::write(&metadata_path, &bytes).unwrap();
        let error = store
            .open_for_api(&id, RESPONSES_API)
            .err()
            .expect("unsupported tools must reject load");
        assert!(format!("{error:#}").contains("unsupported session tools"));
        assert_eq!(fs::read(&metadata_path).unwrap(), bytes);
        assert_eq!(fs::read(&history_path).unwrap(), history);
    }
}
