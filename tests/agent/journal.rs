use super::*;
use crate::protocol::{InputItem, InputMessageContent, MessageRole};
use serde_json::json;
use std::{
    fs::File,
    path::PathBuf,
    sync::atomic::{AtomicUsize, Ordering},
};

struct TempFile(PathBuf);

impl TempFile {
    fn new() -> Self {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        Self(std::env::temp_dir().join(format!(
            "saki-journal-replay-{}-{}.jsonl",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        )))
    }
}

impl Drop for TempFile {
    fn drop(&mut self) {
        let _ = std::fs::remove_file(&self.0);
    }
}

fn write_records(records: &[(u64, JournalRecord)]) -> TempFile {
    let file = TempFile::new();
    let mut serialized = Vec::new();
    for (timestamp_ms, record) in records {
        let mut value = serde_json::to_value(record).unwrap();
        let object = value.as_object_mut().unwrap();
        object.insert("schema_version".to_owned(), json!(1));
        object.insert("timestamp_ms".to_owned(), json!(timestamp_ms));
        serialized.extend(serde_json::to_vec(&value).unwrap());
        serialized.push(b'\n');
    }
    std::fs::write(&file.0, serialized).unwrap();
    file
}

fn read_file(file: &TempFile) -> Result<Replay> {
    let handle = File::open(&file.0).unwrap();
    read(&handle, &file.0)
}

fn replay(records: &[(u64, JournalRecord)]) -> Result<Replay> {
    replay_records(records.iter().cloned())
}

fn system(text: &str) -> JournalRecord {
    JournalRecord::Messages {
        items: vec![InputItem::Message {
            role: MessageRole::System,
            content: InputMessageContent::Text(text.to_owned()),
        }],
    }
}

fn user(text: &str) -> JournalRecord {
    JournalRecord::Messages {
        items: vec![InputItem::Message {
            role: MessageRole::User,
            content: InputMessageContent::Text(text.to_owned()),
        }],
    }
}

fn tool_call(call_id: &str) -> JournalRecord {
    JournalRecord::Messages {
        items: vec![InputItem::ToolCall {
            call_id: call_id.to_owned(),
            name: "read".to_owned(),
            arguments: "{}".to_owned(),
        }],
    }
}

fn tool_result(call_id: &str) -> JournalRecord {
    JournalRecord::Messages {
        items: vec![InputItem::ToolResult {
            call_id: call_id.to_owned(),
            output: "result".to_owned(),
        }],
    }
}

fn started(task_id: &str) -> JournalRecord {
    JournalRecord::TaskStarted {
        task_id: task_id.to_owned(),
        message_id: Some(format!("message-{task_id}")),
        configuration: None,
    }
}

fn finished(task_id: &str, status: TaskStatus) -> JournalRecord {
    JournalRecord::TaskFinished {
        task_id: task_id.to_owned(),
        status,
        error: None,
    }
}

fn full_records() -> Vec<(u64, JournalRecord)> {
    vec![
        (10, system("system")),
        (20, user("first")),
        (30, started("first-task")),
        (40, tool_call("shared-call")),
        (50, tool_result("shared-call")),
        (60, finished("first-task", TaskStatus::Completed)),
        (
            70,
            JournalRecord::Compaction {
                summary: "compacted summary".to_owned(),
                keep_from: 1,
                tokens_before: 100,
                tokens_after: 20,
                usage: None,
            },
        ),
        (80, user("second")),
        (90, started("second-task")),
        (100, tool_call("shared-call")),
        (110, tool_result("shared-call")),
        (120, finished("second-task", TaskStatus::Completed)),
    ]
}

#[test]
fn file_read_and_typed_replay_have_the_same_projection() {
    let records = full_records();
    let file = write_records(&records);
    let from_file = read_file(&file).unwrap();
    let from_records = replay(&records).unwrap();

    assert_eq!(
        serde_json::to_value(&from_records.context).unwrap(),
        serde_json::to_value(&from_file.context).unwrap()
    );
    assert_eq!(
        serde_json::to_value(&from_records.history).unwrap(),
        serde_json::to_value(&from_file.history).unwrap()
    );
    assert_eq!(
        from_records.history_timestamps,
        from_file.history_timestamps
    );
    assert_eq!(from_records.updated_at, from_file.updated_at);
    assert_eq!(from_records.status, from_file.status);
    assert_eq!(from_records.has_summary, from_file.has_summary);
    assert_eq!(from_records.active_task, from_file.active_task);
    assert_eq!(from_records.status, TaskStatus::Completed);
    assert!(from_records.has_summary);
    assert!(from_file.valid_bytes > 0);
    assert_eq!(from_records.valid_bytes, 0);
    assert!(!from_records.needs_newline);
    assert!(!from_file.needs_newline);
}

#[test]
fn unfinished_task_and_unpaired_tool_are_interrupted() {
    let active = replay(&[
        (1, system("system")),
        (2, user("task")),
        (3, started("unfinished-task")),
    ])
    .unwrap();
    assert_eq!(active.status, TaskStatus::Interrupted);
    assert_eq!(active.active_task.as_deref(), Some("unfinished-task"));

    let tool_only = replay(&[(1, system("system")), (2, tool_call("unfinished-tool"))]).unwrap();
    assert_eq!(tool_only.status, TaskStatus::Interrupted);
    assert_eq!(tool_only.active_task, None);
}

fn assert_both_reject(records: &[(u64, JournalRecord)], needle: &str) {
    let typed_error = replay(records).expect_err("typed replay must fail");
    assert!(
        typed_error.to_string().contains(needle),
        "typed error {typed_error} does not contain {needle}"
    );

    let file = write_records(records);
    let file_error = read_file(&file).expect_err("file read must fail");
    assert!(
        file_error.to_string().contains(needle),
        "file error {file_error} does not contain {needle}"
    );
}

#[test]
fn invalid_pairings_task_order_compaction_and_system_are_rejected_by_both_paths() {
    assert_both_reject(
        &[(1, system("system")), (2, tool_result("missing-call"))],
        "unmatched tool result",
    );
    assert_both_reject(
        &[(
            1,
            JournalRecord::Messages {
                items: vec![
                    match tool_call("duplicate") {
                        JournalRecord::Messages { items } => items[0].clone(),
                        _ => unreachable!(),
                    },
                    match tool_call("duplicate") {
                        JournalRecord::Messages { items } => items[0].clone(),
                        _ => unreachable!(),
                    },
                ],
            },
        )],
        "duplicate tool call",
    );
    assert_both_reject(
        &[
            (1, system("system")),
            (2, user("user")),
            (
                3,
                JournalRecord::Compaction {
                    summary: "bad checkpoint".to_owned(),
                    keep_from: 2,
                    tokens_before: 10,
                    tokens_after: 1,
                    usage: None,
                },
            ),
        ],
        "invalid compaction checkpoint",
    );
    assert_both_reject(
        &[
            (1, system("system")),
            (2, finished("never-started", TaskStatus::Completed)),
        ],
        "unmatched task completion",
    );
    assert_both_reject(
        &[
            (1, system("system")),
            (2, started("first")),
            (3, started("second")),
        ],
        "task started before previous task ended",
    );
    assert_both_reject(
        &[(1, user("no system"))],
        "journal has no initial system message",
    );
}

#[test]
fn file_errors_keep_path_and_line_diagnostics() {
    let file = write_records(&[(1, system("system")), (2, tool_result("missing"))]);
    let error = read_file(&file).err().unwrap().to_string();
    assert!(error.contains(":2:"), "{error}");
    assert!(error.contains("unmatched tool result"), "{error}");
}
