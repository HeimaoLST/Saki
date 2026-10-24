use super::*;
use crate::protocol::{
    InputContentPart, InputItem, InputMessageContent, MessageRole, ProviderData,
};
use serde_json::json;
use std::{
    fs::{self, File},
    path::PathBuf,
    sync::atomic::{AtomicBool, AtomicUsize, Ordering},
};

struct TempFile(PathBuf);

impl TempFile {
    fn new(label: &str) -> Self {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        Self(std::env::temp_dir().join(format!(
            "saki-journal-context-{}-{}-{}.jsonl",
            label,
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        )))
    }
}

impl Drop for TempFile {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.0);
    }
}

fn line(record: &JournalRecord, timestamp_ms: u64) -> Vec<u8> {
    let mut value = serde_json::to_value(record).unwrap();
    let object = value.as_object_mut().unwrap();
    object.insert("schema_version".to_owned(), json!(1));
    object.insert("timestamp_ms".to_owned(), json!(timestamp_ms));
    let mut bytes = serde_json::to_vec(&value).unwrap();
    bytes.push(b'\n');
    bytes
}

fn records_bytes(records: &[(u64, JournalRecord)]) -> Vec<u8> {
    let mut bytes = Vec::new();
    for (timestamp_ms, record) in records {
        bytes.extend(line(record, *timestamp_ms));
    }
    bytes
}

fn write_bytes(label: &str, bytes: &[u8]) -> TempFile {
    let file = TempFile::new(label);
    fs::write(&file.0, bytes).unwrap();
    file
}

fn read_bounded_file(
    file: &TempFile,
    end: u64,
    max_memory: usize,
    cancel: &AtomicBool,
) -> Result<BoundedReplay> {
    let handle = File::open(&file.0).unwrap();
    read_bounded(&handle, &file.0, end, max_memory, cancel)
}

fn system() -> JournalRecord {
    JournalRecord::Messages {
        items: vec![InputItem::Message {
            role: MessageRole::System,
            content: InputMessageContent::Text("system".to_owned()),
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

fn started(task_id: &str) -> JournalRecord {
    JournalRecord::TaskStarted {
        task_id: task_id.to_owned(),
        message_id: Some(format!("message-{task_id}")),
        configuration: None,
    }
}

fn finished(task_id: &str) -> JournalRecord {
    JournalRecord::TaskFinished {
        task_id: task_id.to_owned(),
        status: TaskStatus::Completed,
        error: None,
    }
}

#[test]
fn bounded_read_matches_read_and_freezes_torn_and_appended_prefix() {
    let records = vec![
        (10, system()),
        (20, user("first")),
        (30, started("task")),
        (40, finished("task")),
    ];
    let prefix = records_bytes(&records);
    let file = write_bytes("prefix", &prefix);
    let valid_end = prefix.len() as u64;
    let full = read(&File::open(&file.0).unwrap(), &file.0).unwrap();
    let bounded =
        read_bounded_file(&file, valid_end, 8 * 1024 * 1024, &AtomicBool::new(false)).unwrap();

    assert_eq!(
        serde_json::to_value(&bounded.replay.context).unwrap(),
        serde_json::to_value(&full.context).unwrap()
    );
    assert_eq!(
        serde_json::to_value(&bounded.replay.history).unwrap(),
        serde_json::to_value(&full.history).unwrap()
    );
    assert_eq!(bounded.replay.history_timestamps, full.history_timestamps);
    assert_eq!(bounded.replay.status, full.status);
    assert_eq!(bounded.replay.has_summary, full.has_summary);
    assert_eq!(bounded.replay.active_task, full.active_task);
    assert_eq!(bounded.replay.valid_bytes, full.valid_bytes);
    assert_eq!(bounded.replay.needs_newline, full.needs_newline);
    assert!(bounded.accounted_bytes > 0);
    assert!(bounded.accounted_bytes < 8 * 1024 * 1024);

    let mut with_torn = prefix.clone();
    with_torn.extend_from_slice(b"{\"schema_version\":1,\"timestamp_ms\":99,\"type\":\"messages\"");
    fs::write(&file.0, &with_torn).unwrap();
    let torn_end = with_torn.len() as u64;
    let torn =
        read_bounded_file(&file, torn_end, 8 * 1024 * 1024, &AtomicBool::new(false)).unwrap();
    assert_eq!(torn.replay.history.len(), full.history.len());
    assert_eq!(torn.replay.valid_bytes, valid_end);
    assert_eq!(fs::read(&file.0).unwrap(), with_torn);

    let late = line(&user("must stay outside the captured prefix"), 200);
    with_torn.extend_from_slice(&late);
    fs::write(&file.0, &with_torn).unwrap();
    let frozen =
        read_bounded_file(&file, torn_end, 8 * 1024 * 1024, &AtomicBool::new(false)).unwrap();
    assert_eq!(frozen.replay.history.len(), full.history.len());
    assert!(
        !serde_json::to_string(&frozen.replay.history)
            .unwrap()
            .contains("must stay outside the captured prefix")
    );
}

#[test]
fn bounded_read_accepts_eight_mib_plain_text_under_global_budget() {
    let text = "x".repeat(8 * 1024 * 1024);
    let data = records_bytes(&[(1, system()), (2, user(&text))]);
    let file = write_bytes("eight-mib", &data);
    let bounded = read_bounded_file(
        &file,
        data.len() as u64,
        256 * 1024 * 1024,
        &AtomicBool::new(false),
    )
    .unwrap();
    assert_eq!(bounded.replay.valid_bytes, data.len() as u64);
    assert!(bounded.accounted_bytes > text.len());
    assert!(bounded.accounted_bytes < 256 * 1024 * 1024);
    let stored = bounded
        .replay
        .context
        .iter()
        .find_map(|item| match item {
            InputItem::Message {
                role: MessageRole::User,
                content: InputMessageContent::Text(value),
            } => Some(value),
            _ => None,
        })
        .unwrap();
    assert_eq!(stored.len(), text.len());
    assert_eq!(stored, &text);
}

#[test]
fn bounded_read_rejects_dense_structure_and_single_oversized_record() {
    let parts = (0..100_000)
        .map(|_| InputContentPart::Text {
            text: "x".to_owned(),
        })
        .collect();
    let dense = JournalRecord::Messages {
        items: vec![InputItem::Message {
            role: MessageRole::User,
            content: InputMessageContent::Parts(parts),
        }],
    };
    let dense_file = write_bytes("dense", &records_bytes(&[(1, system()), (2, dense)]));
    let error = read_bounded_file(
        &dense_file,
        fs::metadata(&dense_file.0).unwrap().len(),
        8 * 1024 * 1024,
        &AtomicBool::new(false),
    )
    .expect_err("dense structure must exceed the small budget");
    assert!(error.downcast_ref::<BudgetExceeded>().is_some(), "{error}");
    assert!(error.to_string().len() < 128, "{error}");

    let large = "x".repeat(8 * 1024 * 1024);
    let large_file = write_bytes("large", &records_bytes(&[(1, system()), (2, user(&large))]));
    let error = read_bounded_file(
        &large_file,
        fs::metadata(&large_file.0).unwrap().len(),
        8 * 1024 * 1024,
        &AtomicBool::new(false),
    )
    .expect_err("single large record must exceed the budget");
    assert!(error.downcast_ref::<BudgetExceeded>().is_some(), "{error}");
    assert!(error.to_string().len() < 128, "{error}");

    let native = JournalRecord::Messages {
        items: vec![InputItem::ProviderItem {
            data: ProviderData {
                api: "native-fixture".to_owned(),
                value: serde_json::Value::Array((0..50_000).map(|_| json!({})).collect()),
            },
        }],
    };
    let native_file = write_bytes("native", &records_bytes(&[(1, system()), (2, native)]));
    let error = read_bounded_file(
        &native_file,
        fs::metadata(&native_file.0).unwrap().len(),
        8 * 1024 * 1024,
        &AtomicBool::new(false),
    )
    .expect_err("dense native structure must exceed the budget");
    assert!(error.downcast_ref::<BudgetExceeded>().is_some(), "{error}");
    assert!(error.to_string().len() < 128, "{error}");
}

#[test]
fn bounded_read_reports_cancellation_without_reading() {
    let data = records_bytes(&[(1, system()), (2, user("payload"))]);
    let file = write_bytes("cancel", &data);
    let cancel = AtomicBool::new(true);
    let error = read_bounded_file(&file, data.len() as u64, 1024 * 1024, &cancel)
        .expect_err("pre-cancelled read must fail");
    assert!(error.downcast_ref::<ReadCancelled>().is_some(), "{error}");
    assert!(error.to_string().len() < 128, "{error}");
    assert_eq!(fs::read(&file.0).unwrap(), data);
}

#[test]
fn bounded_read_keeps_bad_intermediate_errors_but_ignores_torn_final() {
    let mut bad = records_bytes(&[(1, system())]);
    bad.extend_from_slice(b"not-json\n");
    bad.extend_from_slice(&line(&user("after"), 3));
    let file = write_bytes("bad", &bad);
    let error = read_bounded_file(
        &file,
        bad.len() as u64,
        8 * 1024 * 1024,
        &AtomicBool::new(false),
    )
    .expect_err("bad intermediate record must fail");
    assert!(error.to_string().contains(":2:"), "{error}");
    assert!(
        error.to_string().contains("invalid journal record"),
        "{error}"
    );

    let mut value: serde_json::Value =
        serde_json::from_slice(line(&user("bad-schema"), 2).as_slice()).unwrap();
    value["schema_version"] = json!(99);
    let mut bad_schema = records_bytes(&[(1, system())]);
    bad_schema.extend(serde_json::to_vec(&value).unwrap());
    bad_schema.push(b'\n');
    let schema_file = write_bytes("schema", &bad_schema);
    let error = read_bounded_file(
        &schema_file,
        bad_schema.len() as u64,
        8 * 1024 * 1024,
        &AtomicBool::new(false),
    )
    .expect_err("unsupported schema must fail");
    assert!(error.to_string().contains(":2:"), "{error}");
    assert!(
        error.to_string().contains("unsupported schema version 99"),
        "{error}"
    );

    let mut torn = records_bytes(&[(1, system())]);
    torn.extend_from_slice(b"{\"schema_version\":1,\"timestamp_ms\":2,\"type\":\"messages\"");
    let torn_file = write_bytes("torn", &torn);
    let replay = read_bounded_file(
        &torn_file,
        torn.len() as u64,
        8 * 1024 * 1024,
        &AtomicBool::new(false),
    )
    .unwrap();
    assert_eq!(replay.replay.history.len(), 1);
    assert_eq!(replay.replay.valid_bytes, line(&system(), 1).len() as u64);
}

#[test]
fn empty_source_with_no_read_budget_is_rejected_before_replay() {
    let file = write_bytes("zero-budget", &[]);
    let error = read_bounded_file(&file, 0, 0, &AtomicBool::new(false)).unwrap_err();
    assert!(error.is::<BudgetExceeded>());
}
