use super::*;
use crate::client::config::RESPONSES_API;
use crate::{
    agent::journal::TaskStatus,
    protocol::{ToolDefinition, ToolType},
    tools::tool_registry::{Tool, ToolRegistry},
};
use serde_json::json;
use std::{
    fs,
    io::Write,
    sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    },
};

use crate::test_support::{completed, scripted_http};

struct Workspace(PathBuf);
impl Workspace {
    fn new() -> Self {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        let path = std::env::temp_dir().join(format!(
            "saki-sessions-{}-{}",
            crate::agent::journal::new_id().unwrap(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir(&path).unwrap();
        Self(path)
    }
    fn store(&self) -> SessionStore {
        SessionStore::new(self.0.join("sessions")).unwrap()
    }
    fn create(&self, store: &SessionStore) -> String {
        store
            .create(
                &self.0,
                "test",
                SYSTEM_PROMPT,
                CompactionConfig::new(262_144),
            )
            .unwrap()
    }
}
impl Drop for Workspace {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

#[test]
fn metadata_and_history_are_separate_and_a_session_has_one_writer() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = workspace.create(&store);
    let metadata = store.rename(&id, "Compiler work").unwrap();
    assert_eq!(metadata.title, "Compiler work");
    let mut loaded = store.open_for_api(&id, RESPONSES_API).unwrap();
    assert!(store.open_for_api(&id, RESPONSES_API).is_err());
    assert!(store.list().unwrap()[0].in_use);
    loaded.conversation.add_user_message("remember red-71");
    loaded.conversation.save_history().unwrap();
    drop(loaded);
    let loaded = store.open_for_api(&id, RESPONSES_API).unwrap();
    assert_eq!(loaded.metadata.title, "Compiler work");
    assert_eq!(loaded.metadata.model, "test");
    assert_eq!(loaded.replay.context.len(), 2);
    assert!(
        serde_json::to_string(&loaded.replay.context)
            .unwrap()
            .contains("red-71")
    );
    assert!(
        !fs::read_to_string(store.directory(&id).unwrap().join("metadata.json"))
            .unwrap()
            .contains("red-71")
    );
}

#[test]
fn torn_tail_is_removed_but_complete_corruption_and_unknown_versions_are_rejected() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = workspace.create(&store);
    let path = store.directory(&id).unwrap().join("history.jsonl");
    let original = fs::read(&path).unwrap();
    fs::OpenOptions::new()
        .append(true)
        .open(&path)
        .unwrap()
        .write_all(b"{\"schema_ver")
        .unwrap();
    let mut loaded = store.open_for_api(&id, RESPONSES_API).unwrap();
    assert_eq!(fs::read(&path).unwrap(), original);
    loaded.conversation.add_user_message("after torn write");
    loaded.conversation.save_history().unwrap();
    drop(loaded);
    assert_eq!(
        store
            .open_for_api(&id, RESPONSES_API)
            .unwrap()
            .replay
            .context
            .len(),
        2
    );
    fs::OpenOptions::new()
        .append(true)
        .open(&path)
        .unwrap()
        .write_all(b"{broken}\n")
        .unwrap();
    let corrupted = fs::read(&path).unwrap();
    assert!(
        store
            .open_for_api(&id, RESPONSES_API)
            .err()
            .unwrap()
            .to_string()
            .contains(":3:")
    );
    assert_eq!(fs::read(&path).unwrap(), corrupted);
    let mut unsupported = original.clone();
    unsupported.extend_from_slice(b"{\"schema_version\":99,\"type\":\"messages\",\"items\":[]}");
    fs::write(&path, &unsupported).unwrap();
    assert!(
        store
            .open(&id)
            .err()
            .unwrap()
            .to_string()
            .contains("version 99")
    );
    assert_eq!(fs::read(&path).unwrap(), unsupported);
}

#[test]
fn valid_final_record_without_newline_is_kept_and_can_be_appended() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = workspace.create(&store);
    let path = store.directory(&id).unwrap().join("history.jsonl");
    let mut original = fs::read(&path).unwrap();
    assert_eq!(original.pop(), Some(b'\n'));
    fs::write(&path, original).unwrap();
    let mut loaded = store.open_for_api(&id, RESPONSES_API).unwrap();
    loaded.conversation.add_user_message("second record");
    loaded.conversation.save_history().unwrap();
    drop(loaded);
    assert_eq!(
        store
            .open_for_api(&id, RESPONSES_API)
            .unwrap()
            .replay
            .context
            .len(),
        2
    );
}

#[test]
fn unsupported_old_sessions_are_visible_without_breaking_the_session_list() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let good = workspace.create(&store);
    let old = store.directory("old-session").unwrap();
    fs::create_dir(&old).unwrap();
    fs::write(
        old.join("history.jsonl"),
        "{\"type\":\"messages\",\"items\":[]}\n",
    )
    .unwrap();
    let sessions = store.list().unwrap();
    assert_eq!(sessions.len(), 2);
    assert!(
        sessions
            .iter()
            .find(|item| item.metadata.id == "old-session")
            .unwrap()
            .unavailable
            .is_some()
    );
    assert!(
        sessions
            .iter()
            .find(|item| item.metadata.id == good)
            .unwrap()
            .unavailable
            .is_none()
    );
    assert!(store.directory("../other").is_err());
}

#[tokio::test]
async fn interrupted_tool_is_closed_on_next_input_and_is_never_reexecuted() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = workspace.create(&store);
    let mut loaded = store.open_for_api(&id, RESPONSES_API).unwrap();
    loaded.conversation.begin_task().unwrap();
    loaded.conversation.add_user_message("change a file");
    loaded
        .conversation
        .add_tool_call("uncertain-call".into(), "count".into(), "{}".into());
    loaded.conversation.save_history().unwrap();
    fs::write(
        workspace.0.join("already-written.txt"),
        "existing side effect",
    )
    .unwrap();
    drop(loaded);
    let loaded = store.open_for_api(&id, RESPONSES_API).unwrap();
    assert_eq!(loaded.replay.status, TaskStatus::Interrupted);
    let count = Arc::new(AtomicUsize::new(0));
    let executed = count.clone();
    let mut registry = ToolRegistry::new();
    registry.registry(Tool {
        definition: ToolDefinition {
            tool_type: ToolType::Function,
            name: "count".to_owned(),
            description: "count calls".to_owned(),
            parameters: json!({"type":"object"}),
        },
        handler: Arc::new(move |_| {
            let executed = executed.clone();
            Box::pin(async move {
                executed.fetch_add(1, Ordering::SeqCst);
                Ok(json!({"ok":true}))
            })
        }),
    });
    let (client, server) = scripted_http(vec![(200, completed("inspect current state"))]).await;
    let mut agent = Agent::new(client, registry, loaded.conversation).with_model("test".into());
    agent
        .handle_user_message("check what happened")
        .await
        .unwrap();
    let request = &server.await.unwrap()[0];
    assert!(request.to_string().contains("uncertain-call"));
    assert!(
        request
            .to_string()
            .contains("inspect current state before retrying")
    );
    assert_eq!(count.load(Ordering::SeqCst), 0);
    assert_eq!(
        fs::read_to_string(workspace.0.join("already-written.txt")).unwrap(),
        "existing side effect"
    );
    drop(agent);
    assert_eq!(
        store
            .open_for_api(&id, RESPONSES_API)
            .unwrap()
            .replay
            .status,
        TaskStatus::Completed
    );
}

#[test]
fn title_attempt_is_claimed_once_and_failures_do_not_retry() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = workspace.create(&store);
    assert_eq!(store.metadata(&id).unwrap().title, store::DEFAULT_TITLE);
    assert_eq!(
        store.metadata(&id).unwrap().title_source.as_deref(),
        Some("pending")
    );
    assert!(store.claim_auto_title(&id).unwrap(), "first claim wins");
    assert!(!store.claim_auto_title(&id).unwrap(), "never claims twice");
    // A failed or empty job leaves provenance `auto` and the default title:
    // a later turn or restart must not retry.
    assert_eq!(
        store.metadata(&id).unwrap().title_source.as_deref(),
        Some("auto")
    );
    assert_eq!(store.metadata(&id).unwrap().title, store::DEFAULT_TITLE);
    let applied = store.apply_auto_title(&id, "Generated title").unwrap();
    assert_eq!(applied.unwrap().title, "Generated title");
    assert!(
        store
            .apply_auto_title(&id, "Second title")
            .unwrap()
            .is_none(),
        "an applied title is not replaced"
    );
    assert!(!store.claim_auto_title(&id).unwrap());
}

#[test]
fn manual_rename_always_wins_over_a_late_auto_title() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = workspace.create(&store);
    assert!(store.claim_auto_title(&id).unwrap());
    let manual = store.rename(&id, "My choice").unwrap();
    assert_eq!(manual.title_source.as_deref(), Some("manual"));
    assert!(
        store.apply_auto_title(&id, "Late title").unwrap().is_none(),
        "auto must not overwrite a manual rename"
    );
    assert_eq!(store.metadata(&id).unwrap().title, "My choice");
}

#[test]
fn metadata_without_provenance_keeps_its_explicit_title() {
    let workspace = Workspace::new();
    let store = workspace.store();
    let id = workspace.create(&store);
    let path = store.directory(&id).unwrap().join("metadata.json");
    let mut metadata: serde_json::Value =
        serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
    metadata["title"] = json!("Existing explicit name");
    metadata.as_object_mut().unwrap().remove("title_source");
    fs::write(&path, serde_json::to_vec_pretty(&metadata).unwrap()).unwrap();
    let loaded = store.metadata(&id).unwrap();
    assert_eq!(loaded.title, "Existing explicit name");
    assert!(
        loaded.title_source.is_none(),
        "old metadata has no provenance"
    );
    assert!(!store.claim_auto_title(&id).unwrap());
    assert!(
        store
            .apply_auto_title(&id, "Never applied")
            .unwrap()
            .is_none()
    );
    assert_eq!(store.metadata(&id).unwrap().title, "Existing explicit name");
}

#[test]
fn title_from_output_sanitizes_lines_and_rejects_the_default() {
    let message = |text: &str| crate::protocol::OutputItem::Message {
        id: "m".to_owned(),
        status: "completed".to_owned(),
        role: crate::protocol::MessageRole::Assistant,
        content: vec![crate::protocol::OutputText {
            text: text.to_owned(),
        }],
    };
    assert_eq!(
        title_from_output(&[message("  \"Fix login bug\"  ")]).as_deref(),
        Some("Fix login bug")
    );
    assert_eq!(
        title_from_output(&[message("First line\nsecond line")]).as_deref(),
        Some("First line")
    );
    assert_eq!(title_from_output(&[message("   ")]), None);
    assert_eq!(title_from_output(&[message("New session")]), None);
    assert_eq!(title_from_output(&[]), None);
    let long = "word ".repeat(40);
    let title = title_from_output(&[message(&long)]).unwrap();
    assert!(title.chars().count() <= 60);
}

#[test]
fn cross_api_open_with_torn_tail_fails_before_repairing_the_history() {
    use crate::client::config::CHAT_COMPLETIONS_API;
    let workspace = Workspace::new();
    let store = workspace.store();
    let chat_session = store
        .create_with_api(
            &workspace.0,
            "test",
            SYSTEM_PROMPT,
            CHAT_COMPLETIONS_API,
            CompactionConfig::new(262_144),
        )
        .unwrap();
    let path = store
        .directory(&chat_session)
        .unwrap()
        .join("history.jsonl");
    fs::OpenOptions::new()
        .append(true)
        .open(&path)
        .unwrap()
        .write_all(b"{\"schema_ver")
        .unwrap();
    let torn = fs::read(&path).unwrap();

    // Wrong API: fails before `Conversation::restore` can trim the torn tail.
    let error = store
        .open_for_api(&chat_session, RESPONSES_API)
        .map(|_| ())
        .unwrap_err();
    assert!(
        error
            .to_string()
            .contains("was created for API openai-chat-completions"),
        "{error:?}"
    );
    assert_eq!(fs::read(&path).unwrap(), torn);

    // The right API still restores and repairs the torn tail as before.
    let loaded = store
        .open_for_api(&chat_session, CHAT_COMPLETIONS_API)
        .unwrap();
    drop(loaded);
    assert!(fs::read(&path).unwrap().len() < torn.len());
}
