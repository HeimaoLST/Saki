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

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum TaskStatus {
    Completed,
    Failed,
    Interrupted,
    Unknown,
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
    let mut replay = Replay {
        context: Vec::new(),
        history: Vec::new(),
        history_timestamps: Vec::new(),
        has_summary: false,
        status: TaskStatus::Unknown,
        updated_at: 0,
        valid_bytes: 0,
        needs_newline: false,
        active_task: None,
    };
    let mut bytes = Vec::new();
    let mut active_task = None;
    let mut pending = HashSet::new();
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
        match &envelope.record {
            JournalRecord::Messages { items } => {
                for item in items {
                    match item {
                        InputItem::ToolCall { call_id, .. } => {
                            ensure!(
                                pending.insert(call_id.clone()),
                                "{}:{line_number}: duplicate tool call",
                                path.display()
                            );
                        }
                        InputItem::ToolResult { call_id, .. } => {
                            ensure!(
                                pending.remove(call_id),
                                "{}:{line_number}: unmatched tool result",
                                path.display()
                            );
                        }
                        _ => {}
                    }
                }
                replay.context.extend_from_slice(items);
            }
            JournalRecord::Compaction {
                summary, keep_from, ..
            } => {
                ensure!(
                    pending.is_empty() && *keep_from > 0 && *keep_from < replay.context.len(),
                    "{}:{line_number}: invalid compaction checkpoint",
                    path.display()
                );
                let system = replay
                    .context
                    .first()
                    .context("missing system message")?
                    .clone();
                let mut next = vec![system, super::conversation::summary_item(summary)];
                next.extend_from_slice(&replay.context[*keep_from..]);
                replay.context = next;
                replay.has_summary = true;
            }
            JournalRecord::TaskStarted { task_id, .. } => {
                ensure!(
                    active_task.is_none(),
                    "{}:{line_number}: task started before previous task ended",
                    path.display()
                );
                active_task = Some(task_id.clone());
                replay.status = TaskStatus::Interrupted;
            }
            JournalRecord::TaskFinished {
                task_id, status, ..
            } => {
                ensure!(
                    active_task.as_ref() == Some(task_id),
                    "{}:{line_number}: unmatched task completion",
                    path.display()
                );
                active_task = None;
                replay.status = status.clone();
            }
        }
        replay.updated_at = replay.updated_at.max(envelope.timestamp_ms);
        replay.history_timestamps.push(envelope.timestamp_ms);
        replay.history.push(envelope.record);
        replay.valid_bytes += count as u64;
        replay.needs_newline = !terminated;
    }
    match replay.context.first() {
        Some(InputItem::Message {
            role: MessageRole::System,
            content: InputMessageContent::Text(_),
            ..
        }) => {}
        _ => bail!("{}: journal has no initial system message", path.display()),
    }
    if !pending.is_empty() || active_task.is_some() {
        replay.status = TaskStatus::Interrupted;
    }
    replay.active_task = active_task;
    Ok(replay)
}
