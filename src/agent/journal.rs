use std::{
    collections::HashSet,
    fs::File,
    io::{BufRead, BufReader, Write},
    path::Path,
    time::{SystemTime, UNIX_EPOCH},
};

use anyhow::{Context, Result, bail, ensure};
use serde::{Deserialize, Serialize};

use crate::protocol::{InputItem, InputMessageContent, MessageRole, Usage};

pub(crate) struct JournalLock(File);

impl JournalLock {
    pub(crate) fn from_locked_file(file: File) -> Self {
        Self(file)
    }
}

impl std::ops::Deref for JournalLock {
    type Target = File;

    fn deref(&self) -> &File {
        &self.0
    }
}

impl std::ops::DerefMut for JournalLock {
    fn deref_mut(&mut self) -> &mut File {
        &mut self.0
    }
}

impl Drop for JournalLock {
    fn drop(&mut self) {
        // A forked child can retain the descriptor until exec. The owner's
        // release must unlock even while that copied descriptor remains open.
        let _ = self.0.unlock();
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum TaskStatus {
    Completed,
    Failed,
    Interrupted,
    Unknown,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TaskConfiguration {
    pub model: String,
    pub api: String,
    pub compaction: super::conversation::CompactionConfig,
    pub config_revision: u64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub connection_identity: Option<crate::client::connections::ConnectionIdentity>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub execution_connection_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub system_prompt: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tool_selection: Option<crate::protocol::ToolSelection>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tool_schemas: Option<std::collections::BTreeMap<String, crate::protocol::ToolSchema>>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum JournalRecord {
    Messages {
        items: Vec<InputItem>,
    },
    Compaction {
        summary: String,
        keep_from: usize,
        tokens_before: usize,
        tokens_after: usize,
        usage: Option<Usage>,
    },
    TaskStarted {
        task_id: String,
        /// The caller-visible input message, when the entry point supplied one.
        /// Older journals omit it and read back as `None`.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        message_id: Option<String>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        configuration: Option<TaskConfiguration>,
    },
    TaskFinished {
        task_id: String,
        status: TaskStatus,
        error: Option<String>,
    },
}

#[derive(Serialize, Deserialize)]
struct Envelope {
    schema_version: u32,
    #[serde(default)]
    timestamp_ms: u64,
    #[serde(flatten)]
    record: JournalRecord,
}

#[derive(Debug, Clone)]
pub struct Replay {
    pub context: Vec<InputItem>,
    pub history: Vec<JournalRecord>,
    /// Envelope `timestamp_ms` aligned with `history`; old records read as 0.
    pub history_timestamps: Vec<u64>,
    pub has_summary: bool,
    pub status: TaskStatus,
    pub updated_at: u64,
    pub valid_bytes: u64,
    pub needs_newline: bool,
    pub active_task: Option<String>,
}

pub fn now_ms() -> Result<u64> {
    Ok(SystemTime::now()
        .duration_since(UNIX_EPOCH)?
        .as_millis()
        .try_into()?)
}

pub fn new_id() -> Result<String> {
    Ok(format!(
        "{}-{}",
        SystemTime::now().duration_since(UNIX_EPOCH)?.as_nanos(),
        std::process::id()
    ))
}

pub fn write(file: &mut File, record: &JournalRecord) -> Result<()> {
    #[cfg(all(test, unix))]
    fault::check(file, record)?;
    let mut bytes = serde_json::to_vec(&Envelope {
        schema_version: 1,
        timestamp_ms: now_ms()?,
        record: record.clone(),
    })?;
    bytes.push(b'\n');
    file.write_all(&bytes)?;
    file.sync_data()?;
    Ok(())
}

/// Replays both model context and full history. Only a torn final record is ignored.
/// The caller must hold the write lock before truncating to `valid_bytes`.
pub fn read(file: &File, path: &Path) -> Result<Replay> {
    let mut reader = BufReader::new(file);
    let mut state = ReplayState::new();
    let mut bytes = Vec::new();
    let mut line_number = 0;
    loop {
        bytes.clear();
        let count = reader.read_until(b'\n', &mut bytes)?;
        if count == 0 {
            break;
        }
        line_number += 1;
        let terminated = bytes.last() == Some(&b'\n');
        // Parse JSON before the envelope so an unsupported version or schema
        // at EOF cannot be mistaken for a torn write.
        let value: serde_json::Value = match serde_json::from_slice(&bytes) {
            Ok(value) => value,
            Err(error) if !terminated && error.is_eof() => break,
            Err(error) => {
                return Err(error).with_context(|| {
                    format!("{}:{line_number}: invalid journal record", path.display())
                });
            }
        };
        let envelope: Envelope = serde_json::from_value(value).with_context(|| {
            format!(
                "{}:{line_number}: unsupported journal format",
                path.display()
            )
        })?;
        ensure!(
            envelope.schema_version == 1,
            "{}:{line_number}: unsupported schema version {}",
            path.display(),
            envelope.schema_version
        );
        state.apply(
            envelope.record,
            envelope.timestamp_ms,
            &format!("{}:{line_number}", path.display()),
        )?;
        state.replay.valid_bytes += count as u64;
        state.replay.needs_newline = !terminated;
    }
    state.finish(&path.display().to_string())
}

/// Replays decoded journal records through the same reducer as [`read`].
///
/// This is for projections over already-loaded typed history. It never sets
/// file repair offsets: `valid_bytes` is 0 and `needs_newline` is false.
pub(crate) fn replay_records(
    records: impl IntoIterator<Item = (u64, JournalRecord)>,
) -> Result<Replay> {
    let mut state = ReplayState::new();
    for (index, (timestamp_ms, record)) in records.into_iter().enumerate() {
        state.apply(
            record,
            timestamp_ms,
            &format!("journal record {}", index + 1),
        )?;
    }
    state.finish("journal replay")
}

struct ReplayState {
    replay: Replay,
    pending: HashSet<String>,
    active_task: Option<String>,
}

impl ReplayState {
    fn new() -> Self {
        Self {
            replay: Replay {
                context: Vec::new(),
                history: Vec::new(),
                history_timestamps: Vec::new(),
                has_summary: false,
                status: TaskStatus::Unknown,
                updated_at: 0,
                valid_bytes: 0,
                needs_newline: false,
                active_task: None,
            },
            pending: HashSet::new(),
            active_task: None,
        }
    }

    fn apply(&mut self, record: JournalRecord, timestamp_ms: u64, location: &str) -> Result<()> {
        match &record {
            JournalRecord::Messages { items } => {
                for item in items {
                    match item {
                        InputItem::ToolCall { call_id, .. } => {
                            ensure!(
                                self.pending.insert(call_id.clone()),
                                "{location}: duplicate tool call"
                            );
                        }
                        InputItem::ToolResult { call_id, .. } => {
                            ensure!(
                                self.pending.remove(call_id),
                                "{location}: unmatched tool result"
                            );
                        }
                        _ => {}
                    }
                }
                self.replay.context.extend_from_slice(items);
            }
            JournalRecord::Compaction {
                summary, keep_from, ..
            } => {
                ensure!(
                    self.pending.is_empty()
                        && *keep_from > 0
                        && *keep_from < self.replay.context.len(),
                    "{location}: invalid compaction checkpoint"
                );
                let system = self
                    .replay
                    .context
                    .first()
                    .cloned()
                    .with_context(|| format!("{location}: missing system message"))?;
                let mut next = vec![system, super::conversation::summary_item(summary)];
                next.extend_from_slice(&self.replay.context[*keep_from..]);
                self.replay.context = next;
                self.replay.has_summary = true;
            }
            JournalRecord::TaskStarted { task_id, .. } => {
                ensure!(
                    self.active_task.is_none(),
                    "{location}: task started before previous task ended"
                );
                self.active_task = Some(task_id.clone());
                self.replay.status = TaskStatus::Interrupted;
            }
            JournalRecord::TaskFinished {
                task_id, status, ..
            } => {
                ensure!(
                    self.active_task.as_ref() == Some(task_id),
                    "{location}: unmatched task completion"
                );
                self.active_task = None;
                self.replay.status = status.clone();
            }
        }
        self.replay.updated_at = self.replay.updated_at.max(timestamp_ms);
        self.replay.history_timestamps.push(timestamp_ms);
        self.replay.history.push(record);
        Ok(())
    }

    fn finish(mut self, location: &str) -> Result<Replay> {
        match self.replay.context.first() {
            Some(InputItem::Message {
                role: MessageRole::System,
                content: InputMessageContent::Text(_),
                ..
            }) => {}
            _ => bail!("{location}: journal has no initial system message"),
        }
        if !self.pending.is_empty() || self.active_task.is_some() {
            self.replay.status = TaskStatus::Interrupted;
        }
        self.replay.active_task = self.active_task;
        Ok(self.replay)
    }
}

#[cfg(all(test, unix))]
pub(crate) mod fault {
    use super::*;
    use std::{
        collections::HashMap,
        os::unix::fs::MetadataExt,
        sync::{Mutex, OnceLock},
    };

    #[derive(Clone, Copy, PartialEq, Eq)]
    pub enum Point {
        Start,
        User,
        ToolResult,
        Finish,
    }

    type Identity = (u64, u64);
    fn faults() -> &'static Mutex<HashMap<Identity, Point>> {
        static FAULTS: OnceLock<Mutex<HashMap<Identity, Point>>> = OnceLock::new();
        FAULTS.get_or_init(Default::default)
    }
    fn identity(file: &File) -> std::io::Result<Identity> {
        let metadata = file.metadata()?;
        Ok((metadata.dev(), metadata.ino()))
    }
    pub struct Guard(Identity);
    impl Guard {
        pub fn new(path: &Path, point: Point) -> Self {
            let id = identity(&File::open(path).unwrap()).unwrap();
            assert!(faults().lock().unwrap().insert(id, point).is_none());
            Self(id)
        }
    }
    impl Drop for Guard {
        fn drop(&mut self) {
            faults().lock().unwrap().remove(&self.0);
        }
    }
    pub(super) fn check(file: &File, record: &JournalRecord) -> std::io::Result<()> {
        let point = match record {
            JournalRecord::TaskStarted { .. } => Some(Point::Start),
            JournalRecord::TaskFinished { .. } => Some(Point::Finish),
            JournalRecord::Messages { items } => {
                if items
                    .iter()
                    .any(|item| matches!(item, InputItem::ToolResult { .. }))
                {
                    Some(Point::ToolResult)
                } else if items.iter().any(|item| {
                    matches!(
                        item,
                        InputItem::Message {
                            role: MessageRole::User,
                            ..
                        }
                    )
                }) {
                    Some(Point::User)
                } else {
                    None
                }
            }
            _ => None,
        };
        let id = identity(file)?;
        let mut registry = faults().lock().unwrap();
        if point.is_some() && registry.get(&id).copied() == point {
            registry.remove(&id);
            return Err(std::io::Error::other("injected journal write failure"));
        }
        Ok(())
    }
}

#[cfg(test)]
#[path = "../../tests/agent/journal.rs"]
mod tests;
