use std::{
    fs::{self, File},
    io::Write,
    path::{Component, Path, PathBuf},
};

use anyhow::{Context, Result, ensure};
use serde::{Deserialize, Serialize};

use crate::agent::{
    conversation::{CompactionConfig, Conversation},
    journal::{self, Replay, TaskStatus},
};
use crate::client::config::{CHAT_COMPLETIONS_API, RESPONSES_API};

/// Title used until the first successful turn can generate one.
pub const DEFAULT_TITLE: &str = "New session";

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionMetadata {
    pub schema_version: u32,
    pub id: String,
    pub title: String,
    /// How the title was chosen. Missing in old metadata, which is interpreted
    /// as user/manual provenance so automatic titles never overwrite it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title_source: Option<String>,
    pub created_at: u64,
    pub working_directory: PathBuf,
    pub model: String,
    pub api: String,
    pub compaction: CompactionConfig,
}

#[derive(Debug, Clone)]
pub struct SessionSummary {
    pub metadata: SessionMetadata,
    pub updated_at: u64,
    pub status: TaskStatus,
    pub in_use: bool,
    pub unavailable: Option<String>,
}

pub struct LoadedSession {
    pub metadata: SessionMetadata,
    pub conversation: Conversation,
    pub replay: Replay,
}

pub struct SessionStore {
    root: PathBuf,
}

impl SessionStore {
    pub fn new(root: impl Into<PathBuf>) -> Result<Self> {
        let root = root.into();
        fs::create_dir_all(&root)?;
        Ok(Self {
            root: root.canonicalize()?,
        })
    }

    pub fn directory(&self, id: &str) -> Result<PathBuf> {
        let mut parts = Path::new(id).components();
        ensure!(
            matches!(parts.next(), Some(Component::Normal(_))) && parts.next().is_none(),
            "invalid session ID"
        );
        Ok(self.root.join(id))
    }

    pub fn create(
        &self,
        cwd: &Path,
        model: &str,
        system_prompt: &str,
        config: CompactionConfig,
    ) -> Result<String> {
        self.create_with_api(cwd, model, system_prompt, RESPONSES_API, config)
    }

    /// Creates a session that records the API it was created on. The runtime
    /// uses this with the selected protocol; `create` stays the Responses
    /// default for existing callers.
    pub fn create_with_api(
        &self,
        cwd: &Path,
        model: &str,
        system_prompt: &str,
        api: &str,
        config: CompactionConfig,
    ) -> Result<String> {
        ensure!(
            api == RESPONSES_API || api == CHAT_COMPLETIONS_API,
            "unsupported session API: {api}"
        );
        let conversation =
            Conversation::new(system_prompt.to_owned()).with_compaction_config(config)?;
        let working_directory = cwd.canonicalize()?;
        ensure!(
            working_directory.is_dir(),
            "session working directory must be a directory"
        );
        let id = journal::new_id()?;
        let directory = self.directory(&id)?;
        fs::create_dir(&directory)?;
        let metadata = SessionMetadata {
            schema_version: 1,
            id: id.clone(),
            title: DEFAULT_TITLE.to_owned(),
            title_source: Some("pending".to_owned()),
            created_at: journal::now_ms()?,
            working_directory,
            model: model.to_owned(),
            api: api.to_owned(),
            compaction: config,
        };
        let conversation = conversation.with_history_file(&directory.join("history.jsonl"))?;
        self.save_metadata(&metadata)?;
        drop(conversation);
        Ok(id)
    }

    pub fn metadata(&self, id: &str) -> Result<SessionMetadata> {
        let path = self.directory(id)?.join("metadata.json");
        let metadata: SessionMetadata = serde_json::from_slice(
            &fs::read(&path).with_context(|| format!("reading {}", path.display()))?,
        )?;
        ensure!(
            metadata.schema_version == 1 && metadata.id == id,
            "unsupported or mismatched session metadata: {}",
            path.display()
        );
        ensure!(
            metadata.api == RESPONSES_API || metadata.api == CHAT_COMPLETIONS_API,
            "unsupported session API: {}",
            metadata.api
        );
        Ok(metadata)
    }

    /// Loads a session only for the expected API. The check runs before the
    /// history is restored: `Conversation::restore` repairs a torn tail, so a
    /// mismatched protocol must fail without touching the file. Startup and
    /// `SessionCommand::Open` both use this entry.
    pub fn open_for_api(&self, id: &str, expected_api: &str) -> Result<LoadedSession> {
        let metadata = self.metadata(id)?;
        ensure!(
            metadata.api == expected_api,
            "session {id} was created for API {}; this run speaks {expected_api} — run with SAKI_API={} to open it",
            metadata.api,
            metadata.api
        );
        self.restore(metadata)
    }

    /// Restores a session without an API expectation, for callers that already
    /// know it belongs to the running client. Production startup and resume
    /// use `open_for_api`; this unchecked form is used by tests.
    #[cfg(test)]
    pub fn open(&self, id: &str) -> Result<LoadedSession> {
        let metadata = self.metadata(id)?;
        self.restore(metadata)
    }

    fn restore(&self, metadata: SessionMetadata) -> Result<LoadedSession> {
        ensure!(
            metadata.working_directory.is_dir(),
            "session working directory is missing: {}",
            metadata.working_directory.display()
        );
        let (conversation, replay) =
            Conversation::restore(&self.directory(&metadata.id)?.join("history.jsonl"))?;
        Ok(LoadedSession {
            metadata,
            conversation,
            replay,
        })
    }

    pub fn rename(&self, id: &str, title: &str) -> Result<SessionMetadata> {
        ensure!(!title.trim().is_empty(), "session title cannot be empty");
        ensure!(
            !title.contains(['\r', '\n']),
            "session title must be one line"
        );
        let mut metadata = self.metadata(id)?;
        metadata.title = title.trim().to_owned();
        metadata.title_source = Some("manual".to_owned());
        self.save_metadata(&metadata)?;
        Ok(metadata)
    }

    /// Claims the one automatic title attempt before its job is dispatched.
    /// Persisting this up front means a failed, empty, or interrupted job is
    /// never retried on a later turn or after a restart.
    pub fn claim_auto_title(&self, id: &str) -> Result<bool> {
        let mut metadata = self.metadata(id)?;
        if metadata.title_source.as_deref() != Some("pending") {
            return Ok(false);
        }
        metadata.title_source = Some("auto".to_owned());
        self.save_metadata(&metadata)?;
        Ok(true)
    }

    /// Applies a generated title only while the automatic attempt is still in
    /// place and the title is still the default. A manual rename changes the
    /// provenance to `manual`, so a late job can never overwrite it.
    pub fn apply_auto_title(&self, id: &str, title: &str) -> Result<Option<SessionMetadata>> {
        ensure!(!title.trim().is_empty(), "session title cannot be empty");
        ensure!(
            !title.contains(['\r', '\n']),
            "session title must be one line"
        );
        let mut metadata = self.metadata(id)?;
        if metadata.title_source.as_deref() != Some("auto") || metadata.title != DEFAULT_TITLE {
            return Ok(None);
        }
        metadata.title = title.trim().to_owned();
        self.save_metadata(&metadata)?;
        Ok(Some(metadata))
    }

    fn save_metadata(&self, metadata: &SessionMetadata) -> Result<()> {
        let directory = self.directory(&metadata.id)?;
        let temporary = directory.join(format!("metadata-{}.tmp", journal::new_id()?));
        let result = (|| -> Result<()> {
            let mut file = File::options()
                .write(true)
                .create_new(true)
                .open(&temporary)?;
            file.write_all(&serde_json::to_vec_pretty(metadata)?)?;
            file.sync_all()?;
            fs::rename(&temporary, directory.join("metadata.json"))?;
            Ok(())
        })();
        if result.is_err() {
            let _ = fs::remove_file(temporary);
        }
        result
    }

    pub fn list(&self) -> Result<Vec<SessionSummary>> {
        let mut sessions = Vec::new();
        for entry in fs::read_dir(&self.root)? {
            let entry = entry?;
            if !entry.file_type()?.is_dir() {
                continue;
            }
            let id = entry.file_name().to_string_lossy().into_owned();
            let path = entry.path().join("history.jsonl");
            let metadata = self.metadata(&id);
            let file = File::options().read(true).write(true).open(&path);
            let in_use = file.as_ref().is_ok_and(|file| file.try_lock().is_err());
            let replay = metadata
                .as_ref()
                .map_err(|error| anyhow::anyhow!("{error:#}"))
                .and_then(|_| {
                    file.as_ref()
                        .map_err(|error| anyhow::anyhow!("{error}"))
                        .and_then(|file| journal::read(file, &path))
                });
            let unavailable = replay.as_ref().err().map(|error| format!("{error:#}"));
            let metadata = metadata.unwrap_or_else(|_| SessionMetadata {
                schema_version: 0,
                id: id.clone(),
                title: id,
                title_source: None,
                created_at: 0,
                working_directory: PathBuf::new(),
                model: String::new(),
                api: String::new(),
                compaction: CompactionConfig::new(262_144),
            });
            let modified = fs::metadata(&path)
                .and_then(|value| value.modified())
                .ok()
                .and_then(|time| time.duration_since(std::time::UNIX_EPOCH).ok())
                .map_or(metadata.created_at, |time| time.as_millis() as u64);
            sessions.push(SessionSummary {
                updated_at: replay
                    .as_ref()
                    .map_or(modified, |value| value.updated_at.max(modified)),
                status: replay
                    .as_ref()
                    .map_or(TaskStatus::Unknown, |value| value.status.clone()),
                metadata,
                in_use,
                unavailable,
            });
        }
        sessions.sort_by(|a, b| {
            b.updated_at
                .cmp(&a.updated_at)
                .then_with(|| a.metadata.id.cmp(&b.metadata.id))
        });
        Ok(sessions)
    }
}
