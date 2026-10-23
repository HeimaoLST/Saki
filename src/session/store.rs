use std::{
    collections::BTreeMap,
    fs::{self, File},
    io::{Read, Write},
    path::{Component, Path, PathBuf},
};

use anyhow::{Context, Result, ensure};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use super::config::{CapacitySource, ToolSelection};
use crate::agent::{
    conversation::{CompactionConfig, Conversation},
    journal::{self, Replay, TaskStatus},
};
use crate::client::{
    config::{CHAT_COMPLETIONS_API, RESPONSES_API},
    connections::ConnectionIdentity,
};
use crate::protocol::ToolSchema;

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
    #[serde(default)]
    pub config_revision: u64,
    #[serde(default)]
    pub capacity_source: CapacitySource,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub connection_identity: Option<ConnectionIdentity>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub binding_origin: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub bound_at_revision: Option<u64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub system_prompt: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tool_selection: Option<ToolSelection>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub tool_schemas: BTreeMap<String, ToolSchema>,
}

#[derive(Debug, Clone)]
pub(crate) struct EnhancedCreation {
    pub connection_identity: ConnectionIdentity,
    pub tool_selection: ToolSelection,
    pub tool_schemas: BTreeMap<String, ToolSchema>,
}

pub(crate) struct SessionCreation<'a> {
    pub cwd: &'a Path,
    pub model: &'a str,
    pub system_prompt: &'a str,
    pub api: &'a str,
    pub compaction: CompactionConfig,
    pub capacity_source: CapacitySource,
    pub enhanced: Option<EnhancedCreation>,
}

#[derive(Serialize, Deserialize)]
struct SchemaOneBackupState {
    journal_len: u64,
    journal_sha256: String,
    bound_revision: u64,
}

const SCHEMA_ONE_BACKUP: &str = "metadata.schema1.backup.json";
const SCHEMA_ONE_BACKUP_STATE: &str = "metadata.schema1.backup.state.json";

pub(crate) struct ConfigurationLock {
    pub metadata: SessionMetadata,
    pub replay: Replay,
    _file: File,
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

    /// Creates a session that records the API it was created on. This is the
    /// legacy schema-1 path; enhanced callers use `create_configured`.
    pub fn create_with_api(
        &self,
        cwd: &Path,
        model: &str,
        system_prompt: &str,
        api: &str,
        config: CompactionConfig,
    ) -> Result<String> {
        self.create_configured(SessionCreation {
            cwd,
            model,
            system_prompt,
            api,
            compaction: config,
            capacity_source: CapacitySource::Stored,
            enhanced: None,
        })
    }

    /// The single session creation path. All validation that can fail without
    /// leaving a session behind runs before the directory is created.
    pub(crate) fn create_configured(&self, creation: SessionCreation<'_>) -> Result<String> {
        ensure!(
            creation.api == RESPONSES_API || creation.api == CHAT_COMPLETIONS_API,
            "unsupported session API: {}",
            creation.api
        );
        if let Some(enhanced) = &creation.enhanced {
            ensure!(
                enhanced.connection_identity.api == creation.api,
                "connection identity API does not match the session API"
            );
        }
        let conversation = Conversation::new(creation.system_prompt.to_owned())
            .with_compaction_config(creation.compaction)?;
        let working_directory = creation.cwd.canonicalize()?;
        ensure!(
            working_directory.is_dir(),
            "session working directory must be a directory"
        );
        let id = journal::new_id()?;
        let directory = self.directory(&id)?;
        let enhanced = creation.enhanced;
        let metadata = SessionMetadata {
            schema_version: if enhanced.is_some() { 2 } else { 1 },
            id: id.clone(),
            title: DEFAULT_TITLE.to_owned(),
            title_source: Some("pending".to_owned()),
            created_at: journal::now_ms()?,
            working_directory,
            model: creation.model.to_owned(),
            api: creation.api.to_owned(),
            compaction: creation.compaction,
            config_revision: 1,
            capacity_source: creation.capacity_source,
            connection_identity: enhanced
                .as_ref()
                .map(|enhanced| enhanced.connection_identity.clone()),
            binding_origin: enhanced.as_ref().map(|_| "created".to_owned()),
            bound_at_revision: enhanced.as_ref().map(|_| 1),
            system_prompt: enhanced.as_ref().map(|_| creation.system_prompt.to_owned()),
            tool_schemas: enhanced
                .as_ref()
                .map_or_else(BTreeMap::new, |enhanced| enhanced.tool_schemas.clone()),
            tool_selection: enhanced.map(|enhanced| enhanced.tool_selection),
        };
        validate_metadata(&metadata, &id)?;
        fs::create_dir(&directory)?;
        let conversation = conversation.with_history_file(&directory.join("history.jsonl"))?;
        self.save_metadata(&metadata)?;
        drop(conversation);
        Ok(id)
    }

    pub fn metadata(&self, id: &str) -> Result<SessionMetadata> {
        self.metadata_at(id).map(|(metadata, _)| metadata)
    }

    fn metadata_at(&self, id: &str) -> Result<(SessionMetadata, Vec<u8>)> {
        let path = self.directory(id)?.join("metadata.json");
        let bytes = fs::read(&path).with_context(|| format!("reading {}", path.display()))?;
        let metadata: SessionMetadata = serde_json::from_slice(&bytes)
            .with_context(|| format!("parsing {}", path.display()))?;
        validate_metadata(&metadata, id)?;
        Ok((metadata, bytes))
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
        let _metadata_lock = self.lock_metadata(id)?;
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
        let _metadata_lock = self.lock_metadata(id)?;
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
        let _metadata_lock = self.lock_metadata(id)?;
        let mut metadata = self.metadata(id)?;
        if metadata.title_source.as_deref() != Some("auto") || metadata.title != DEFAULT_TITLE {
            return Ok(None);
        }
        metadata.title = title.trim().to_owned();
        self.save_metadata(&metadata)?;
        Ok(Some(metadata))
    }

    /// CAS configuration commit. Only configuration fields are taken from the
    /// candidate; title/provenance, session id, creation time and cwd remain
    /// the latest values read under the CAS read.
    pub(crate) fn update_config(
        &self,
        id: &str,
        expected_revision: u64,
        next: SessionMetadata,
    ) -> std::result::Result<SessionMetadata, super::runtime::RuntimeError> {
        use super::runtime::RuntimeError;
        let _metadata_lock = self.lock_metadata(id).map_err(super::runtime::io_error)?;
        let (current, original_bytes) = self
            .metadata_at(id)
            .map_err(|error| RuntimeError::new("io_error", format!("{error:#}")))?;
        if current.config_revision != expected_revision {
            return Err(RuntimeError::conflict(current.config_revision));
        }
        if next.schema_version < current.schema_version {
            return Err(RuntimeError::new(
                "invalid_request",
                "configuration schema cannot be downgraded",
            )
            .session(id));
        }
        let mut candidate = current.clone();
        candidate.schema_version = next.schema_version;
        candidate.model = next.model;
        candidate.api = next.api;
        candidate.compaction = next.compaction;
        candidate.capacity_source = next.capacity_source;
        candidate.connection_identity = next.connection_identity;
        candidate.binding_origin = next.binding_origin;
        candidate.bound_at_revision = next.bound_at_revision;
        candidate.system_prompt = next.system_prompt;
        candidate.tool_selection = next.tool_selection;
        candidate.tool_schemas = next.tool_schemas;
        candidate.config_revision = expected_revision.checked_add(1).ok_or_else(|| {
            RuntimeError::new("config_conflict", "configuration revision exhausted")
        })?;
        validate_metadata(&candidate, id).map_err(|error| {
            RuntimeError::new("invalid_request", format!("{error:#}")).session(id)
        })?;

        if current.schema_version == 1 && candidate.schema_version == 2 {
            let bound_revision = candidate.bound_at_revision.ok_or_else(|| {
                RuntimeError::new("invalid_request", "schema 2 requires bound_at_revision")
                    .session(id)
            })?;
            self.write_schema1_backup(id, &original_bytes, bound_revision)
                .map_err(|error| RuntimeError::new("io_error", format!("{error:#}")).session(id))?;
        }
        self.save_metadata(&candidate)
            .map_err(super::runtime::io_error)?;
        Ok(candidate)
    }

    pub(crate) fn lock_config(
        &self,
        id: &str,
    ) -> std::result::Result<ConfigurationLock, super::runtime::RuntimeError> {
        use super::runtime::RuntimeError;
        use std::fs::TryLockError;

        let directory = self
            .directory(id)
            .map_err(|error| RuntimeError::new("invalid_request", error.to_string()).session(id))?;
        let path = directory.join("history.jsonl");
        let file = File::options()
            .read(true)
            .write(true)
            .open(&path)
            .map_err(|error| RuntimeError::new("io_error", format!("{error:#}")).session(id))?;
        match file.try_lock() {
            Ok(()) => {}
            Err(TryLockError::WouldBlock) => {
                return Err(
                    RuntimeError::new("locked", "session journal is already locked").session(id),
                );
            }
            Err(TryLockError::Error(error)) => {
                return Err(RuntimeError::new("io_error", format!("{error:#}")).session(id));
            }
        }
        let (metadata, _) = self
            .metadata_at(id)
            .map_err(|error| RuntimeError::new("io_error", format!("{error:#}")).session(id))?;
        let replay = journal::read(&file, &path).map_err(|error| {
            let code = if error
                .chain()
                .any(|cause| cause.downcast_ref::<std::io::Error>().is_some())
            {
                "io_error"
            } else {
                "invalid_history"
            };
            RuntimeError::new(code, format!("{error:#}")).session(id)
        })?;
        Ok(ConfigurationLock {
            metadata,
            replay,
            _file: file,
        })
    }

    pub(crate) fn rollback_config(
        &self,
        id: &str,
        expected_revision: u64,
    ) -> std::result::Result<SessionMetadata, super::runtime::RuntimeError> {
        use super::runtime::RuntimeError;
        let mut lock = self.lock_config(id)?;
        let _metadata_lock = self.lock_metadata(id).map_err(super::runtime::io_error)?;
        lock.metadata = self.metadata(id).map_err(super::runtime::io_error)?;
        if lock.metadata.schema_version != 2 {
            return Err(RuntimeError::new(
                "rollback_unsafe",
                "configuration rollback requires a schema 2 session",
            )
            .session(id));
        }
        if lock.metadata.config_revision != expected_revision {
            return Err(RuntimeError::conflict(lock.metadata.config_revision).session(id));
        }
        let directory = self
            .directory(id)
            .map_err(|error| RuntimeError::new("io_error", error.to_string()).session(id))?;
        let backup_path = directory.join(SCHEMA_ONE_BACKUP);
        let backup_bytes = fs::read(&backup_path).map_err(|_| {
            RuntimeError::new("rollback_unsafe", "schema 1 backup is missing").session(id)
        })?;
        let backup: SessionMetadata = serde_json::from_slice(&backup_bytes).map_err(|_| {
            RuntimeError::new("rollback_unsafe", "schema 1 backup is invalid").session(id)
        })?;
        if backup.schema_version != 1
            || backup.id != id
            || backup.api != lock.metadata.api
            || validate_metadata(&backup, id).is_err()
        {
            return Err(RuntimeError::new(
                "rollback_unsafe",
                "schema 1 backup identity is inconsistent",
            )
            .session(id));
        }
        let state: SchemaOneBackupState = serde_json::from_slice(
            &fs::read(directory.join(SCHEMA_ONE_BACKUP_STATE)).map_err(|_| {
                RuntimeError::new("rollback_unsafe", "schema 1 backup state is missing").session(id)
            })?,
        )
        .map_err(|_| {
            RuntimeError::new("rollback_unsafe", "schema 1 backup state is invalid").session(id)
        })?;
        let (journal_len, journal_sha256) =
            journal_fingerprint(&directory.join("history.jsonl"))
                .map_err(|error| RuntimeError::new("io_error", format!("{error:#}")).session(id))?;
        if state.bound_revision != lock.metadata.bound_at_revision.unwrap_or(0)
            || backup.config_revision.checked_add(1) != Some(state.bound_revision)
            || state.journal_len != journal_len
            || state.journal_sha256 != journal_sha256
        {
            return Err(RuntimeError::new(
                "rollback_unsafe",
                "journal changed after the schema 1 backup",
            )
            .session(id));
        }
        self.save_metadata_bytes(id, &backup_bytes)
            .map_err(super::runtime::io_error)?;
        Ok(backup)
    }

    fn write_schema1_backup(
        &self,
        id: &str,
        metadata_bytes: &[u8],
        bound_revision: u64,
    ) -> Result<()> {
        let directory = self.directory(id)?;
        let (journal_len, journal_sha256) = journal_fingerprint(&directory.join("history.jsonl"))?;
        write_sync(&directory.join(SCHEMA_ONE_BACKUP), metadata_bytes)?;
        let state = SchemaOneBackupState {
            journal_len,
            journal_sha256,
            bound_revision,
        };
        write_sync(
            &directory.join(SCHEMA_ONE_BACKUP_STATE),
            &serde_json::to_vec_pretty(&state)?,
        )?;
        File::open(&directory)?.sync_all()?;
        Ok(())
    }

    fn lock_metadata(&self, id: &str) -> Result<File> {
        let file = File::options()
            .read(true)
            .write(true)
            .create(true)
            .truncate(false)
            .open(self.directory(id)?.join("metadata.lock"))?;
        match file.try_lock() {
            Ok(()) => Ok(file),
            Err(std::fs::TryLockError::WouldBlock) => Err(MetadataBusy.into()),
            Err(std::fs::TryLockError::Error(error)) => Err(error.into()),
        }
    }

    fn save_metadata(&self, metadata: &SessionMetadata) -> Result<()> {
        self.save_metadata_bytes(&metadata.id, &serde_json::to_vec_pretty(metadata)?)
    }

    fn save_metadata_bytes(&self, id: &str, bytes: &[u8]) -> Result<()> {
        let directory = self.directory(id)?;
        let temporary = directory.join(format!("metadata-{}.tmp", journal::new_id()?));
        let result = (|| -> Result<()> {
            let mut file = File::options()
                .write(true)
                .create_new(true)
                .open(&temporary)?;
            #[cfg(test)]
            metadata_fault::check(&directory, metadata_fault::Point::BeforeWrite)?;
            file.write_all(bytes)?;
            file.sync_all()?;
            #[cfg(test)]
            metadata_fault::check(&directory, metadata_fault::Point::BeforeRename)?;
            fs::rename(&temporary, directory.join("metadata.json"))?;
            #[cfg(test)]
            metadata_fault::check(&directory, metadata_fault::Point::AfterRename)
                .map_err(|error| CommitUnknown(error.into()))?;
            File::open(&directory)
                .and_then(|file| file.sync_all())
                .map_err(|error| CommitUnknown(error.into()))?;
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
                config_revision: 0,
                capacity_source: CapacitySource::Stored,
                connection_identity: None,
                binding_origin: None,
                bound_at_revision: None,
                system_prompt: None,
                tool_selection: None,
                tool_schemas: BTreeMap::new(),
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

fn validate_metadata(metadata: &SessionMetadata, id: &str) -> Result<()> {
    ensure!(
        metadata.id == id,
        "session metadata id does not match: {id}"
    );
    ensure!(
        metadata.schema_version == 1 || metadata.schema_version == 2,
        "unsupported session metadata schema version {}",
        metadata.schema_version
    );
    ensure!(
        metadata.api == RESPONSES_API || metadata.api == CHAT_COMPLETIONS_API,
        "unsupported session API: {}",
        metadata.api
    );
    if metadata.schema_version == 1 {
        ensure!(
            metadata.connection_identity.is_none()
                && metadata.binding_origin.is_none()
                && metadata.bound_at_revision.is_none()
                && metadata.system_prompt.is_none()
                && metadata.tool_selection.is_none()
                && metadata.tool_schemas.is_empty(),
            "enhanced configuration fields require metadata schema 2"
        );
    }
    if metadata.schema_version == 2 {
        let identity = metadata
            .connection_identity
            .as_ref()
            .context("schema 2 metadata is missing connection_identity")?;
        ensure!(
            identity.api == metadata.api,
            "schema 2 connection identity API does not match metadata API"
        );
        ensure!(
            metadata.binding_origin.is_some(),
            "schema 2 metadata is missing binding_origin"
        );
        let bound_at_revision = metadata
            .bound_at_revision
            .context("schema 2 metadata is missing bound_at_revision")?;
        ensure!(
            bound_at_revision <= metadata.config_revision,
            "schema 2 bound_at_revision exceeds config_revision"
        );
        ensure!(
            metadata.system_prompt.is_some(),
            "schema 2 metadata is missing system_prompt"
        );
        let tools = metadata
            .tool_selection
            .as_ref()
            .context("schema 2 metadata is missing tool_selection")?;
        super::config::validate_tool_selection(tools)
            .map_err(|error| anyhow::anyhow!("unsupported session tools: {}", error.message))?;
        let names: std::collections::BTreeSet<_> = tools
            .external
            .iter()
            .map(|binding| binding.exposed_name.as_str())
            .collect();
        ensure!(
            names.len() == metadata.tool_schemas.len()
                && metadata
                    .tool_schemas
                    .keys()
                    .all(|name| names.contains(name.as_str())),
            "schema 2 tool declarations do not match the selected external tools"
        );
    }
    Ok(())
}

fn journal_fingerprint(path: &Path) -> Result<(u64, String)> {
    let mut file = File::open(path)?;
    let mut hasher = Sha256::new();
    let mut length = 0u64;
    let mut buffer = [0u8; 64 * 1024];
    loop {
        let count = file.read(&mut buffer)?;
        if count == 0 {
            break;
        }
        length = length.saturating_add(count as u64);
        hasher.update(&buffer[..count]);
    }
    Ok((length, format!("{:x}", hasher.finalize())))
}

fn write_sync(path: &Path, bytes: &[u8]) -> Result<()> {
    let mut file = File::options()
        .write(true)
        .create(true)
        .truncate(true)
        .open(path)?;
    file.write_all(bytes)?;
    file.sync_all()?;
    Ok(())
}

#[derive(Debug, thiserror::Error)]
#[error("session metadata is being updated")]
pub(crate) struct MetadataBusy;

#[derive(Debug, thiserror::Error)]
#[error("metadata publication could not be confirmed: {0:#}")]
pub(crate) struct CommitUnknown(anyhow::Error);

#[cfg(test)]
pub(crate) mod metadata_fault {
    use super::*;
    use std::{
        collections::HashMap,
        sync::{Mutex, OnceLock},
    };
    #[derive(Clone, Copy, PartialEq, Eq)]
    pub enum Point {
        BeforeWrite,
        BeforeRename,
        AfterRename,
    }
    fn faults() -> &'static Mutex<HashMap<PathBuf, Point>> {
        static FAULTS: OnceLock<Mutex<HashMap<PathBuf, Point>>> = OnceLock::new();
        FAULTS.get_or_init(Default::default)
    }
    pub struct Guard(PathBuf);
    impl Guard {
        pub fn new(directory: PathBuf, point: Point) -> Self {
            assert!(
                faults()
                    .lock()
                    .unwrap()
                    .insert(directory.clone(), point)
                    .is_none()
            );
            Self(directory)
        }
    }
    impl Drop for Guard {
        fn drop(&mut self) {
            faults().lock().unwrap().remove(&self.0);
        }
    }
    pub(super) fn check(directory: &Path, point: Point) -> std::io::Result<()> {
        let mut registry = faults().lock().unwrap();
        if registry.get(directory) == Some(&point) {
            registry.remove(directory);
            return Err(std::io::Error::other("injected metadata commit failure"));
        }
        Ok(())
    }
}

#[cfg(test)]
#[path = "../../tests/session/store_config.rs"]
mod store_config_tests;
