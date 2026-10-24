use super::*;
use crate::{
    agent::conversation::CompactionConfig,
    protocol::{InputContentPart, MessageRole},
};
use std::{
    fs::{self, OpenOptions},
    path::PathBuf,
};

struct Fixture {
    root: PathBuf,
    store: SessionStore,
    id: String,
}
impl Fixture {
    fn new() -> Self {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        let root = std::env::temp_dir().join(format!(
            "saki-context-resource-{}-{}",
            journal::new_id().unwrap(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        let store = SessionStore::new(root.join("sessions")).unwrap();
        let id = store
            .create(&root, "test", "system", CompactionConfig::new(262_144))
            .unwrap();
        Self { root, store, id }
    }
    fn append(&self, record: JournalRecord) {
        let path = self
            .store
            .directory(&self.id)
            .unwrap()
            .join("history.jsonl");
        journal::write(
            &mut OpenOptions::new().append(true).open(path).unwrap(),
            &record,
        )
        .unwrap();
    }
    fn user(&self, text: &str) {
        self.append(message(MessageRole::User, text));
    }
    fn open(&self, resources: &ContextStore, view: ContextView) -> ContextResource {
        resources
            .open(
                &self.store,
                OpenContext {
                    session_id: self.id.clone(),
                    view,
                },
                None,
                &AtomicBool::new(false),
            )
            .unwrap()
    }
}
impl Drop for Fixture {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.root);
    }
}
fn message(role: MessageRole, text: &str) -> JournalRecord {
    JournalRecord::Messages {
        items: vec![InputItem::Message {
            role,
            content: InputMessageContent::Text(text.into()),
        }],
    }
}
fn read_all(resources: &ContextStore, reference: &str, max_bytes: usize) -> String {
    let mut text = String::new();
    let mut offset = 0;
    loop {
        let chunk = resources
            .read(
                ReadContext {
                    context_ref: reference.into(),
                    offset,
                    max_bytes,
                },
                None,
            )
            .unwrap();
        assert!(serde_json::to_vec(&chunk).unwrap().len() <= max_bytes);
        assert_eq!(chunk.start, offset);
        text.push_str(&chunk.text);
        match chunk.next_offset {
            None => break,
            Some(next) => {
                assert!(next > offset);
                offset = next;
            }
        }
    }
    text
}
fn search(reference: &str, query: &str) -> SearchContext {
    SearchContext {
        context_ref: reference.into(),
        query: query.into(),
        cursor: None,
        max_hits: 20,
        max_bytes: 16 * 1024,
    }
}
#[test]
fn snapshots_are_immutable_and_history_survives_two_compactions() {
    let fixture = Fixture::new();
    let resources = ContextStore::default();
    fixture.user("UNIQUE-original-evidence");
    fixture.append(message(MessageRole::Assistant, "old answer"));
    let old = fixture.open(&resources, ContextView::Working);
    let compact = |summary: &str, keep_from| JournalRecord::Compaction {
        summary: summary.into(),
        keep_from,
        tokens_before: 20,
        tokens_after: 10,
        usage: None,
    };
    fixture.append(compact("first summary", 2));
    fixture.user("later input");
    fixture.append(compact("second summary", 3));
    let history = fixture.open(&resources, ContextView::History);
    let working = fixture.open(&resources, ContextView::Working);
    assert!(read_all(&resources, &old.context_ref, 512).contains("UNIQUE-original-evidence"));
    let latest = read_all(&resources, &working.context_ref, 512);
    assert!(latest.contains("second summary"));
    assert!(!latest.contains("UNIQUE-original-evidence"));
    let matches = resources
        .search(
            search(&history.context_ref, "UNIQUE-original-evidence"),
            None,
        )
        .unwrap();
    assert_eq!(matches.hits.len(), 1);
    assert_eq!(matches.hits[0].sources[0].index, 1);
}
#[test]
fn reads_reassemble_utf8_escaped_text_and_reject_invalid_offsets() {
    let fixture = Fixture::new();
    let resources = ContextStore::default();
    fixture.user(&"中文😀\"\\\n\u{1}".repeat(500));
    let opened = fixture.open(&resources, ContextView::Working);
    let expected = resources
        .get(&opened.context_ref, None)
        .unwrap()
        .text
        .clone();
    assert_eq!(read_all(&resources, &opened.context_ref, 400), expected);
    let bad = expected.find('中').unwrap() + 1;
    assert_eq!(
        resources
            .read(
                ReadContext {
                    context_ref: opened.context_ref.clone(),
                    offset: bad,
                    max_bytes: 1024
                },
                None
            )
            .unwrap_err()
            .code,
        "invalid_request"
    );
    let end = resources
        .read(
            ReadContext {
                context_ref: opened.context_ref.clone(),
                offset: opened.text_bytes,
                max_bytes: 1024,
            },
            None,
        )
        .unwrap();
    assert!(end.text.is_empty());
    assert!(end.next_offset.is_none());
    assert!(
        resources
            .read(
                ReadContext {
                    context_ref: opened.context_ref,
                    offset: opened.text_bytes + 1,
                    max_bytes: 1024
                },
                None
            )
            .is_err()
    );
}
#[test]
fn search_crosses_scan_boundary_without_duplicates_and_binds_cursor() {
    let fixture = Fixture::new();
    let resources = ContextStore::default();
    fixture.user(&format!("{}needle needle", "a".repeat(SEARCH_SCAN - 20)));
    let opened = fixture.open(&resources, ContextView::Working);
    let data = resources.get(&opened.context_ref, None).unwrap();
    let mut request = search(&opened.context_ref, "needle");
    request.max_hits = 1;
    let expected: Vec<_> = data
        .text
        .match_indices("needle")
        .map(|(start, _)| start)
        .collect();
    let mut found = Vec::new();
    loop {
        let page = resources.search(request.clone(), None).unwrap();
        found.extend(page.hits.iter().map(|hit| hit.start));
        match page.next_cursor {
            None => break,
            Some(cursor) => request.cursor = Some(cursor),
        }
    }
    assert_eq!(found, expected);
    let mut other = search(&opened.context_ref, "different");
    other.cursor = request.cursor;
    assert_eq!(
        resources.search(other, None).unwrap_err().code,
        "invalid_request"
    );
}
#[test]
fn nontext_payloads_are_omitted_and_not_searchable() {
    let fixture = Fixture::new();
    let resources = ContextStore::default();
    fixture.append(JournalRecord::Messages {
        items: vec![
            InputItem::Message {
                role: MessageRole::User,
                content: InputMessageContent::Parts(vec![InputContentPart::Image {
                    url: "secret-image-base64".into(),
                }]),
            },
            InputItem::ProviderItem {
                data: crate::protocol::ProviderData {
                    api: "test".into(),
                    value: serde_json::json!({"opaque":"secret-native-payload"}),
                },
            },
        ],
    });
    let opened = fixture.open(&resources, ContextView::History);
    assert_eq!(opened.projection_omissions.len(), 2);
    assert!(
        resources
            .search(search(&opened.context_ref, "secret"), None)
            .unwrap()
            .hits
            .is_empty()
    );
}
#[tokio::test(start_paused = true)]
async fn scope_release_task_cleanup_and_expiry_reclaim_capacity() {
    let fixture = Fixture::new();
    let resources = ContextStore::default();
    let allowed = ContextScope {
        task_id: "parent".into(),
        sessions: HashSet::from([fixture.id.clone()]),
        cancelled: Arc::new(AtomicBool::new(false)),
    };
    let denied = ContextScope {
        task_id: "other".into(),
        sessions: HashSet::new(),
        cancelled: Arc::new(AtomicBool::new(false)),
    };
    assert_eq!(
        resources
            .open(
                &fixture.store,
                OpenContext {
                    session_id: fixture.id.clone(),
                    view: ContextView::History
                },
                Some(&denied),
                &AtomicBool::new(false)
            )
            .unwrap_err()
            .code,
        "scope_denied"
    );
    let opened = resources
        .open(
            &fixture.store,
            OpenContext {
                session_id: fixture.id.clone(),
                view: ContextView::History,
            },
            Some(&allowed),
            &AtomicBool::new(false),
        )
        .unwrap();
    assert_eq!(
        resources
            .search(search(&opened.context_ref, "system"), Some(&denied))
            .unwrap_err()
            .code,
        "scope_denied"
    );
    assert_eq!(
        resources
            .release(&opened.context_ref, Some(&denied))
            .unwrap_err()
            .code,
        "scope_denied"
    );
    resources.finish_task(&allowed);
    assert_eq!(resources.bytes.load(Ordering::Acquire), 0);
    assert!(!resources.release(&opened.context_ref, None).unwrap());
    let opened = fixture.open(&resources, ContextView::History);
    tokio::time::advance(TTL).await;
    resources.expire_idle();
    assert!(
        resources
            .read(
                ReadContext {
                    context_ref: opened.context_ref,
                    offset: 0,
                    max_bytes: 1024
                },
                None
            )
            .is_err()
    );
    assert_eq!(resources.bytes.load(Ordering::Acquire), 0);
}
#[test]
fn reservations_are_atomic_and_failure_returns_budget_and_slots() {
    let fixture = Fixture::new();
    let resources = ContextStore::default();
    let lease = resources.reserve().unwrap();
    assert_eq!(resources.reserve().unwrap_err().code, "busy");
    drop(lease);
    assert_eq!(resources.bytes.load(Ordering::Acquire), 0);
    assert!(
        resources
            .open(
                &fixture.store,
                OpenContext {
                    session_id: fixture.id.clone(),
                    view: ContextView::History
                },
                None,
                &AtomicBool::new(true)
            )
            .is_err()
    );
    assert_eq!(resources.bytes.load(Ordering::Acquire), 0);
    assert_eq!(resources.slots.available_permits(), RESOURCE_COUNT);
}

#[test]
fn eight_mib_resource_reports_bounded_read_search_and_open_costs() {
    let fixture = Fixture::new();
    let resources = ContextStore::default();
    fixture.user(&format!(
        "{}PERF-MARKER",
        "plain context ".repeat((8_usize * 1024 * 1024).div_ceil(14))
    ));
    let mut opens = Vec::new();
    let mut reads = Vec::new();
    let mut searches = Vec::new();
    let mut peak_retained = 0;
    for _ in 0..10 {
        let start = std::time::Instant::now();
        let opened = fixture.open(&resources, ContextView::History);
        opens.push(start.elapsed().as_micros());
        assert!(opened.text_bytes >= 8 * 1024 * 1024);
        peak_retained = peak_retained.max(resources.bytes.load(Ordering::Acquire));
        let start = std::time::Instant::now();
        let chunk = resources
            .read(
                ReadContext {
                    context_ref: opened.context_ref.clone(),
                    offset: 0,
                    max_bytes: 16384,
                },
                None,
            )
            .unwrap();
        reads.push(start.elapsed().as_micros());
        assert!(serde_json::to_vec(&chunk).unwrap().len() <= 16384);
        let start = std::time::Instant::now();
        let page = resources
            .search(search(&opened.context_ref, "PERF-MARKER"), None)
            .unwrap();
        searches.push(start.elapsed().as_micros());
        assert!(page.scanned_to <= SEARCH_SCAN);
        assert!(page.next_cursor.is_some());
        assert!(resources.release(&opened.context_ref, None).unwrap());
        assert_eq!(resources.bytes.load(Ordering::Acquire), 0);
    }
    opens.sort_unstable();
    reads.sort_unstable();
    searches.sort_unstable();
    println!(
        "CONTEXT_PERF {}",
        serde_json::json!({"source_min_bytes":8*1024*1024,"samples":10,"open_us":opens,"read_us":reads,"search_us":searches,"p50_us":{"open":opens[4],"read":reads[4],"search":searches[4]},"p95_us":{"open":opens[9],"read":reads[9],"search":searches[9]},"peak_retained_bytes":peak_retained,"reservation_limit_bytes":RESOURCE_BYTES})
    );
    assert!(peak_retained <= RESOURCE_BYTES);
}

#[tokio::test]
async fn compaction_during_captured_read_returns_one_complete_generation() {
    let fixture = Fixture::new();
    let resources = ContextStore::default();
    fixture.user("old-generation-marker");
    fixture.append(message(MessageRole::Assistant, "keep"));
    let (captured, ready) = tokio::sync::oneshot::channel();
    let (resume, resumed) = std::sync::mpsc::channel();
    resources.gate_next_open(ReadGate {
        captured,
        resume: resumed,
    });
    let source = fixture.id.clone();
    let store = SessionStore::new(fixture.root.join("sessions")).unwrap();
    let reader = resources.clone();
    let job = tokio::task::spawn_blocking(move || {
        reader.open(
            &store,
            OpenContext {
                session_id: source,
                view: ContextView::Working,
            },
            None,
            &AtomicBool::new(false),
        )
    });
    ready.await.unwrap();
    fixture.append(JournalRecord::Compaction {
        summary: "new-generation-summary".into(),
        keep_from: 2,
        tokens_before: 20,
        tokens_after: 10,
        usage: None,
    });
    resume.send(()).unwrap();
    let old = job.await.unwrap().unwrap();
    let new = fixture.open(&resources, ContextView::Working);
    let old_text = read_all(&resources, &old.context_ref, 1024);
    let new_text = read_all(&resources, &new.context_ref, 1024);
    assert!(old_text.contains("old-generation-marker"));
    assert!(!old_text.contains("new-generation-summary"));
    assert!(!new_text.contains("old-generation-marker"));
    assert!(new_text.contains("new-generation-summary"));
}

#[tokio::test]
async fn finishing_parent_while_open_is_pending_prevents_orphan_publication() {
    let fixture = Fixture::new();
    let resources = ContextStore::default();
    let scope = ContextScope {
        task_id: "ending-parent".into(),
        sessions: HashSet::from([fixture.id.clone()]),
        cancelled: Arc::new(AtomicBool::new(false)),
    };
    let (captured, ready) = tokio::sync::oneshot::channel();
    let (resume, resumed) = std::sync::mpsc::channel();
    resources.gate_next_open(ReadGate {
        captured,
        resume: resumed,
    });
    let source = fixture.id.clone();
    let store = SessionStore::new(fixture.root.join("sessions")).unwrap();
    let reader = resources.clone();
    let reader_scope = scope.clone();
    let job = tokio::task::spawn_blocking(move || {
        reader.open(
            &store,
            OpenContext {
                session_id: source,
                view: ContextView::History,
            },
            Some(&reader_scope),
            &AtomicBool::new(false),
        )
    });
    ready.await.unwrap();
    let excess = resources
        .open(
            &fixture.store,
            OpenContext {
                session_id: fixture.id.clone(),
                view: ContextView::History,
            },
            None,
            &AtomicBool::new(false),
        )
        .unwrap_err();
    assert_eq!(excess.code, "busy");
    assert_eq!(excess.reason, Some("context_read_capacity"));
    resources.finish_task(&scope);
    resume.send(()).unwrap();
    assert_eq!(job.await.unwrap().unwrap_err().code, "cancelled");
    assert_eq!(resources.retained_bytes(), 0);
    assert_eq!(resources.slots.available_permits(), RESOURCE_COUNT);
}

#[test]
fn reference_and_source_limits_reject_before_unbounded_work() {
    let fixture = Fixture::new();
    let resources = ContextStore::default();
    let references: Vec<_> = (0..RESOURCE_COUNT)
        .map(|_| fixture.open(&resources, ContextView::History))
        .collect();
    let excess = resources
        .open(
            &fixture.store,
            OpenContext {
                session_id: fixture.id.clone(),
                view: ContextView::History,
            },
            None,
            &AtomicBool::new(false),
        )
        .unwrap_err();
    assert_eq!(excess.code, "busy");
    assert_eq!(excess.reason, Some("context_refs"));
    for reference in references {
        assert!(resources.release(&reference.context_ref, None).unwrap());
    }
    let path = fixture
        .store
        .directory(&fixture.id)
        .unwrap()
        .join("history.jsonl");
    OpenOptions::new()
        .write(true)
        .open(path)
        .unwrap()
        .set_len((SOURCE_LIMIT + 1) as u64)
        .unwrap();
    let excess = resources
        .open(
            &fixture.store,
            OpenContext {
                session_id: fixture.id.clone(),
                view: ContextView::History,
            },
            None,
            &AtomicBool::new(false),
        )
        .unwrap_err();
    assert_eq!(excess.code, "resource_too_large");
    assert_eq!(resources.retained_bytes(), 0);
}
