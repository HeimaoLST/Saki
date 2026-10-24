use std::{
    collections::{HashMap, HashSet},
    fs::File,
    io::Write,
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, AtomicUsize, Ordering},
    },
    time::Duration,
};

use serde::{Deserialize, Serialize};
use tokio::{
    sync::{OwnedSemaphorePermit, Semaphore},
    time::Instant,
};

use super::{runtime::RuntimeError, store::SessionStore};
use crate::{
    agent::journal::{self, JournalRecord, Replay},
    protocol::{InputItem, InputMessageContent},
};

type Result<T> = std::result::Result<T, RuntimeError>;
pub const RESULT_LIMIT: usize = 48 * 1024;
const SOURCE_LIMIT: usize = 64 * 1024 * 1024;
const RESOURCE_BYTES: usize = 256 * 1024 * 1024;
const RESOURCE_COUNT: usize = 32;
const TTL: Duration = Duration::from_secs(600);
const SEARCH_SCAN: usize = 1024 * 1024;
const OPERATION_BYTES: usize = 2 * 1024 * 1024;

#[derive(Debug, Clone, Serialize)]
pub struct ContextLimits {
    pub views: Vec<&'static str>,
    pub max_source_bytes: usize,
    pub max_resource_bytes: usize,
    pub max_resources: usize,
    pub max_readers: usize,
    pub idle_ttl_ms: u64,
    pub max_result_bytes: usize,
    pub max_search_scan_bytes: usize,
    pub operation_work_bytes: usize,
}
impl Default for ContextLimits {
    fn default() -> Self {
        Self {
            views: vec!["history", "working"],
            max_source_bytes: SOURCE_LIMIT,
            max_resource_bytes: RESOURCE_BYTES,
            max_resources: RESOURCE_COUNT,
            max_readers: 1,
            idle_ttl_ms: TTL.as_millis() as u64,
            max_result_bytes: RESULT_LIMIT,
            max_search_scan_bytes: SEARCH_SCAN,
            operation_work_bytes: OPERATION_BYTES,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ContextView {
    History,
    Working,
    Request,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct OpenContext {
    pub session_id: String,
    pub view: ContextView,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ReadContext {
    pub context_ref: String,
    #[serde(default)]
    pub offset: usize,
    #[serde(default = "default_result_bytes")]
    pub max_bytes: usize,
}
fn default_result_bytes() -> usize {
    16 * 1024
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SearchContext {
    pub context_ref: String,
    pub query: String,
    pub cursor: Option<String>,
    #[serde(default = "default_hits")]
    pub max_hits: usize,
    #[serde(default = "default_result_bytes")]
    pub max_bytes: usize,
}
fn default_hits() -> usize {
    20
}

#[derive(Debug, Clone, Serialize)]
pub struct ContextResource {
    pub context_ref: String,
    pub session_id: String,
    pub view: ContextView,
    pub captured_at_ms: u64,
    pub journal_record_count: usize,
    pub source_task_id: Option<String>,
    pub config_revision: Option<u64>,
    pub request_sequence: Option<u64>,
    pub text_bytes: usize,
    pub projection_omissions: Vec<ProjectionOmission>,
    pub expires_at_ms: u64,
}

#[derive(Debug, Clone, Serialize)]
pub struct ProjectionOmission {
    pub source: usize,
    pub kind: &'static str,
}

#[derive(Debug, Clone, Serialize)]
pub struct SourceLocation {
    pub index: usize,
    pub start: usize,
    pub end: usize,
}
#[derive(Debug, Clone, Serialize)]
pub struct ContextChunk {
    pub context_ref: String,
    pub text: String,
    pub start: usize,
    pub end: usize,
    pub sources: Vec<SourceLocation>,
    pub next_offset: Option<usize>,
}
#[derive(Debug, Clone, Serialize)]
pub struct SearchHit {
    pub start: usize,
    pub end: usize,
    pub text: String,
    pub sources: Vec<SourceLocation>,
}
#[derive(Debug, Clone, Serialize)]
pub struct SearchPage {
    pub context_ref: String,
    pub hits: Vec<SearchHit>,
    pub scanned_to: usize,
    pub next_cursor: Option<String>,
}

#[derive(Debug, Clone)]
pub(crate) struct ContextScope {
    pub task_id: String,
    pub sessions: HashSet<String>,
    pub cancelled: Arc<AtomicBool>,
}
impl ContextScope {
    pub(crate) fn check(&self, session: &str) -> Result<()> {
        if self.cancelled.load(Ordering::Acquire) {
            return Err(cancelled());
        }
        if self.sessions.contains(session) {
            Ok(())
        } else {
            Err(RuntimeError::new(
                "scope_denied",
                "source session is outside this task's context scope",
            ))
        }
    }
}

pub(crate) struct ReadCancellation {
    pub flag: Arc<AtomicBool>,
}
impl ReadCancellation {
    pub(crate) fn new() -> Self {
        Self {
            flag: Arc::new(AtomicBool::new(false)),
        }
    }
}
impl Drop for ReadCancellation {
    fn drop(&mut self) {
        self.flag.store(true, Ordering::Release);
    }
}

#[derive(Debug)]
struct MemoryLease {
    pool: Arc<AtomicUsize>,
    bytes: usize,
}
impl MemoryLease {
    fn shrink(&mut self, bytes: usize) {
        self.pool.fetch_sub(self.bytes - bytes, Ordering::AcqRel);
        self.bytes = bytes;
    }
}
impl Drop for MemoryLease {
    fn drop(&mut self) {
        self.pool.fetch_sub(self.bytes, Ordering::AcqRel);
    }
}
struct ContextData {
    descriptor: ContextResource,
    text: String,
    spans: Vec<SourceLocation>,
    _memory: MemoryLease,
}
struct Entry {
    data: Arc<ContextData>,
    owner: Option<String>,
    used_at: Instant,
    _slot: OwnedSemaphorePermit,
}

/// One resource table shared by the service and task-bound tool adapters.
#[derive(Clone)]
pub(crate) struct ContextStore {
    entries: Arc<Mutex<HashMap<String, Entry>>>,
    bytes: Arc<AtomicUsize>,
    slots: Arc<Semaphore>,
    readers: Arc<Semaphore>,
    #[cfg(test)]
    read_gate: Arc<Mutex<Option<ReadGate>>>,
}
impl Default for ContextStore {
    fn default() -> Self {
        Self {
            entries: Arc::new(Mutex::new(HashMap::new())),
            bytes: Arc::new(AtomicUsize::new(0)),
            slots: Arc::new(Semaphore::new(RESOURCE_COUNT)),
            readers: Arc::new(Semaphore::new(1)),
            #[cfg(test)]
            read_gate: Arc::new(Mutex::new(None)),
        }
    }
}
#[cfg(test)]
pub(crate) struct ReadGate {
    pub captured: tokio::sync::oneshot::Sender<()>,
    pub resume: std::sync::mpsc::Receiver<()>,
}

impl ContextStore {
    #[cfg(test)]
    pub(crate) fn gate_next_open(&self, gate: ReadGate) {
        *self.read_gate.lock().unwrap() = Some(gate);
    }
    #[cfg(test)]
    pub(crate) fn retained_bytes(&self) -> usize {
        self.bytes.load(Ordering::Acquire)
    }
    fn expire(entries: &mut HashMap<String, Entry>) {
        entries.retain(|_, entry| entry.used_at.elapsed() < TTL);
    }
    fn reserve(&self, requested: Option<usize>) -> Result<MemoryLease> {
        let mut used = self.bytes.load(Ordering::Acquire);
        loop {
            let bytes = requested.unwrap_or_else(|| RESOURCE_BYTES.saturating_sub(used));
            if bytes == 0 || bytes > RESOURCE_BYTES.saturating_sub(used) {
                return Err(busy("context_bytes"));
            }
            match self.bytes.compare_exchange_weak(
                used,
                used + bytes,
                Ordering::AcqRel,
                Ordering::Acquire,
            ) {
                Ok(_) => {
                    return Ok(MemoryLease {
                        pool: self.bytes.clone(),
                        bytes,
                    });
                }
                Err(current) => used = current,
            }
        }
    }
    pub(crate) fn open(
        &self,
        store: &SessionStore,
        request: OpenContext,
        scope: Option<&ContextScope>,
        cancel: &AtomicBool,
    ) -> Result<ContextResource> {
        if let Some(scope) = scope {
            scope.check(&request.session_id)?;
        }
        if request.view == ContextView::Request {
            return Err(unavailable("request_not_published"));
        }
        {
            let mut entries = self.entries.lock().unwrap();
            Self::expire(&mut entries);
        }
        let _reader = self
            .readers
            .clone()
            .try_acquire_owned()
            .map_err(|_| busy("context_read_capacity"))?;
        let slot = self
            .slots
            .clone()
            .try_acquire_owned()
            .map_err(|_| busy("context_refs"))?;
        let mut memory = self.reserve(None)?;
        let path = store
            .directory(&request.session_id)
            .map_err(|_| invalid("invalid session ID"))?
            .join("history.jsonl");
        let file = File::open(&path).map_err(|error| {
            RuntimeError::new(
                if error.kind() == std::io::ErrorKind::NotFound {
                    "not_found"
                } else {
                    "io_error"
                },
                "cannot open context source",
            )
        })?;
        let end = file
            .metadata()
            .map_err(|_| RuntimeError::new("io_error", "cannot inspect context source"))?
            .len();
        if end > SOURCE_LIMIT as u64 {
            return Err(too_large());
        }
        let captured_at_ms = journal::now_ms()
            .map_err(|_| RuntimeError::new("io_error", "cannot read capture time"))?;
        #[cfg(test)]
        if let Some(gate) = self.read_gate.lock().unwrap().take() {
            let _ = gate.captured.send(());
            let _ = gate.resume.recv();
        }
        let bounded = journal::read_bounded(&file, &path, end, memory.bytes, cancel)
            .map_err(map_read_error)?;
        let Replay {
            context,
            history,
            active_task,
            ..
        } = bounded.replay;
        let count = history.len();
        let revision = history.iter().rev().find_map(|record| match record {
            JournalRecord::TaskStarted {
                configuration: Some(config),
                ..
            } => Some(config.config_revision),
            _ => None,
        });
        let mut projection = Projection::new(memory.bytes.saturating_sub(bounded.accounted_bytes));
        match request.view {
            ContextView::History => {
                for (index, record) in history.iter().enumerate() {
                    if cancel.load(Ordering::Acquire) {
                        return Err(cancelled());
                    }
                    projection.record(index, record)?;
                }
            }
            ContextView::Working => {
                for (index, item) in context.iter().enumerate() {
                    if cancel.load(Ordering::Acquire) {
                        return Err(cancelled());
                    }
                    projection.item(index, item)?;
                }
            }
            ContextView::Request => unreachable!(),
        }
        // The structured replay is temporary for these two views. Request resources
        // retain a separate, native request snapshot when that capability is enabled.
        drop(context);
        drop(history);
        let context_ref = journal::new_id()
            .map_err(|_| RuntimeError::new("io_error", "cannot allocate context ID"))?;
        let descriptor = ContextResource {
            context_ref: context_ref.clone(),
            session_id: request.session_id,
            view: request.view,
            captured_at_ms,
            journal_record_count: count,
            source_task_id: active_task,
            config_revision: revision,
            request_sequence: None,
            text_bytes: projection.text.len(),
            projection_omissions: projection.omissions,
            expires_at_ms: captured_at_ms + TTL.as_millis() as u64,
        };
        if !fits(&descriptor, RESULT_LIMIT) {
            return Err(too_large());
        }
        let retained = projection.text.capacity()
            + projection.spans.capacity() * std::mem::size_of::<SourceLocation>()
            + descriptor.projection_omissions.capacity()
                * std::mem::size_of::<ProjectionOmission>()
            + descriptor.session_id.capacity()
            + descriptor.context_ref.capacity()
            + descriptor
                .source_task_id
                .as_ref()
                .map_or(0, String::capacity)
            + 1024;
        if retained > memory.bytes {
            return Err(too_large());
        }
        memory.shrink(retained);
        let data = Arc::new(ContextData {
            descriptor: descriptor.clone(),
            text: projection.text,
            spans: projection.spans,
            _memory: memory,
        });
        let mut entries = self.entries.lock().unwrap();
        if cancel.load(Ordering::Acquire) {
            return Err(cancelled());
        }
        if let Some(scope) = scope {
            scope.check(&data.descriptor.session_id)?;
        }
        entries.insert(
            context_ref,
            Entry {
                data,
                owner: scope.map(|scope| scope.task_id.clone()),
                used_at: Instant::now(),
                _slot: slot,
            },
        );
        Ok(descriptor)
    }
    fn get(&self, reference: &str, scope: Option<&ContextScope>) -> Result<Arc<ContextData>> {
        let mut entries = self.entries.lock().unwrap();
        Self::expire(&mut entries);
        let entry = entries
            .get_mut(reference)
            .ok_or_else(|| unavailable("expired_or_unknown"))?;
        if let Some(scope) = scope {
            scope.check(&entry.data.descriptor.session_id)?;
        }
        Ok(entry.data.clone())
    }
    fn touch(&self, reference: &str) {
        if let Some(entry) = self.entries.lock().unwrap().get_mut(reference) {
            entry.used_at = Instant::now();
        }
    }
    pub(crate) fn expire_idle(&self) {
        Self::expire(&mut self.entries.lock().unwrap());
    }
    pub(crate) fn release(&self, reference: &str, scope: Option<&ContextScope>) -> Result<bool> {
        let mut entries = self.entries.lock().unwrap();
        Self::expire(&mut entries);
        if let Some(entry) = entries.get(reference)
            && let Some(scope) = scope
        {
            scope.check(&entry.data.descriptor.session_id)?;
        }
        Ok(entries.remove(reference).is_some())
    }
    pub(crate) fn finish_task(&self, scope: &ContextScope) {
        let mut entries = self.entries.lock().unwrap();
        scope.cancelled.store(true, Ordering::Release);
        entries.retain(|_, entry| entry.owner.as_deref() != Some(scope.task_id.as_str()));
    }
    pub(crate) fn read(
        &self,
        request: ReadContext,
        scope: Option<&ContextScope>,
    ) -> Result<ContextChunk> {
        check_budget(request.max_bytes)?;
        let data = self.get(&request.context_ref, scope)?;
        let _work = self.reserve(Some(OPERATION_BYTES))?;
        let text = &data.text;
        if !text.is_char_boundary(request.offset) {
            return Err(invalid("offset must be an in-range UTF-8 boundary"));
        }
        let mut chunk = ContextChunk {
            context_ref: request.context_ref,
            text: String::new(),
            start: request.offset,
            end: request.offset,
            sources: Vec::new(),
            next_offset: (request.offset < text.len()).then_some(request.offset),
        };
        if !fits(&chunk, request.max_bytes) {
            return Err(invalid("max_bytes cannot hold the result metadata"));
        }
        let end = floor_boundary(
            text,
            request
                .offset
                .saturating_add(request.max_bytes)
                .min(text.len()),
        );
        let mut low = request.offset;
        let mut high = end;
        while low < high {
            let mid = ceil_boundary(text, low + (high - low).div_ceil(2));
            chunk.text = text[request.offset..mid].to_owned();
            chunk.end = mid;
            chunk.sources = locations(&data.spans, request.offset, mid);
            chunk.next_offset = (mid < text.len()).then_some(mid);
            if fits(&chunk, request.max_bytes) {
                low = mid;
            } else {
                high = floor_boundary(text, mid.saturating_sub(1));
            }
        }
        if low == request.offset && low < text.len() {
            return Err(invalid(
                "max_bytes cannot hold the next character and its source",
            ));
        }
        chunk.text = text[request.offset..low].to_owned();
        chunk.end = low;
        chunk.sources = locations(&data.spans, request.offset, low);
        chunk.next_offset = (low < text.len()).then_some(low);
        self.touch(&chunk.context_ref);
        Ok(chunk)
    }
    pub(crate) fn search(
        &self,
        request: SearchContext,
        scope: Option<&ContextScope>,
    ) -> Result<SearchPage> {
        check_budget(request.max_bytes)?;
        if request.query.is_empty()
            || request.query.len() > 4096
            || request.max_hits == 0
            || request.max_hits > 100
        {
            return Err(invalid("invalid query or max_hits"));
        }
        let data = self.get(&request.context_ref, scope)?;
        let _work = self.reserve(Some(OPERATION_BYTES))?;
        let text = &data.text;
        let mut position = match request.cursor {
            None => 0,
            Some(ref cursor) => {
                let cursor: Cursor =
                    serde_json::from_str(cursor).map_err(|_| invalid("invalid cursor"))?;
                if cursor.context_ref != request.context_ref
                    || cursor.query != request.query
                    || !text.is_char_boundary(cursor.position)
                {
                    return Err(invalid("cursor does not belong to this query/resource"));
                }
                cursor.position
            }
        };
        let boundary = floor_boundary(text, position.saturating_add(SEARCH_SCAN).min(text.len()));
        let mut page = SearchPage {
            context_ref: request.context_ref.clone(),
            hits: Vec::new(),
            scanned_to: position,
            next_cursor: Some(String::new()),
        };
        if !fits(&page, request.max_bytes) {
            return Err(invalid("max_bytes cannot hold search metadata"));
        }
        let mut scanned_to = position;
        while position < boundary {
            let found = text[position..boundary]
                .find(&request.query)
                .map(|offset| position + offset);
            let Some(start) = found else {
                scanned_to = boundary;
                position = if boundary == text.len() {
                    boundary
                } else {
                    position.max(ceil_boundary(
                        text,
                        boundary.saturating_sub(request.query.len().saturating_sub(1)),
                    ))
                };
                break;
            };
            let end = start + request.query.len();
            let snippet_end = floor_boundary(text, start.saturating_add(512).min(text.len()));
            page.hits.push(SearchHit {
                start,
                end,
                text: text[start..snippet_end].to_owned(),
                sources: locations(&data.spans, start, snippet_end),
            });
            let next = ceil_boundary(text, start + 1);
            page.scanned_to = next;
            page.next_cursor = cursor_for(&request, next, text.len());
            if !fits(&page, request.max_bytes) {
                page.hits.pop();
                if page.hits.is_empty() {
                    return Err(invalid("max_bytes cannot hold the next search hit"));
                }
                position = start;
                break;
            }
            position = next;
            scanned_to = next;
            if page.hits.len() == request.max_hits {
                break;
            }
        }
        page.scanned_to = scanned_to.max(position);
        page.next_cursor = cursor_for(&request, position, text.len());
        if !fits(&page, request.max_bytes) {
            return Err(invalid("max_bytes cannot hold the continuation cursor"));
        }
        self.touch(&page.context_ref);
        Ok(page)
    }
}
#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Cursor {
    context_ref: String,
    query: String,
    position: usize,
}
fn cursor_for(request: &SearchContext, position: usize, length: usize) -> Option<String> {
    (position < length).then(|| {
        serde_json::to_string(&Cursor {
            context_ref: request.context_ref.clone(),
            query: request.query.clone(),
            position,
        })
        .unwrap()
    })
}
fn locations(spans: &[SourceLocation], start: usize, end: usize) -> Vec<SourceLocation> {
    spans
        .iter()
        .filter(|span| span.end > start && span.start < end)
        .map(|span| SourceLocation {
            index: span.index,
            start: start.max(span.start),
            end: end.min(span.end),
        })
        .collect()
}
fn floor_boundary(text: &str, mut position: usize) -> usize {
    while !text.is_char_boundary(position) {
        position -= 1;
    }
    position
}
fn ceil_boundary(text: &str, mut position: usize) -> usize {
    while !text.is_char_boundary(position) {
        position += 1;
    }
    position
}
fn fits(value: &impl Serialize, limit: usize) -> bool {
    serde_json::to_writer(&mut crate::protocol::ByteCounter::new(limit), value).is_ok()
}
fn check_budget(bytes: usize) -> Result<()> {
    if bytes == 0 || bytes > RESULT_LIMIT {
        Err(invalid("max_bytes must be positive and at most 49152"))
    } else {
        Ok(())
    }
}
fn invalid(message: &'static str) -> RuntimeError {
    RuntimeError::new("invalid_request", message)
}
fn unavailable(reason: &'static str) -> RuntimeError {
    let mut error = RuntimeError::new("context_unavailable", "context resource is unavailable");
    error.reason = Some(reason);
    error
}
fn busy(reason: &'static str) -> RuntimeError {
    let mut error = RuntimeError::new("busy", "context capacity is full");
    error.reason = Some(reason);
    error
}
fn too_large() -> RuntimeError {
    RuntimeError::new(
        "resource_too_large",
        "context source exceeds its memory budget",
    )
}
fn cancelled() -> RuntimeError {
    RuntimeError::new("cancelled", "context read was cancelled")
}
fn map_read_error(error: anyhow::Error) -> RuntimeError {
    if error.is::<journal::BudgetExceeded>() {
        return too_large();
    }
    if error.is::<journal::ReadCancelled>() {
        return cancelled();
    }
    let code = if error
        .chain()
        .any(|cause| cause.downcast_ref::<std::io::Error>().is_some())
    {
        "io_error"
    } else {
        "invalid_history"
    };
    RuntimeError::new(code, "context source could not be replayed")
}

struct Projection {
    text: String,
    spans: Vec<SourceLocation>,
    omissions: Vec<ProjectionOmission>,
    budget: usize,
}
impl Projection {
    fn new(budget: usize) -> Self {
        Self {
            text: String::new(),
            spans: Vec::new(),
            omissions: Vec::new(),
            budget,
        }
    }
    fn append(&mut self, text: &str) -> Result<()> {
        let cost = self
            .text
            .len()
            .saturating_add(text.len())
            .saturating_add((self.spans.len() + 1) * std::mem::size_of::<SourceLocation>())
            .saturating_add((self.omissions.len() + 1) * std::mem::size_of::<ProjectionOmission>());
        if cost > self.budget || self.text.len().saturating_add(text.len()) > SOURCE_LIMIT {
            return Err(too_large());
        }
        self.text
            .try_reserve_exact(text.len())
            .map_err(|_| too_large())?;
        self.text.push_str(text);
        Ok(())
    }
    fn item(&mut self, index: usize, item: &InputItem) -> Result<()> {
        let start = self.text.len();
        match item {
            InputItem::Message { role, content } => {
                self.json(role)?;
                self.append("\n")?;
                match content {
                    InputMessageContent::Text(text) => self.append(text)?,
                    InputMessageContent::Parts(parts) => {
                        for part in parts {
                            match part {
                                crate::protocol::InputContentPart::Text { text } => {
                                    self.append(text)?
                                }
                                crate::protocol::InputContentPart::Image { .. } => {
                                    self.omit(index, "image")?
                                }
                            }
                        }
                    }
                }
            }
            InputItem::ToolCall {
                call_id,
                name,
                arguments,
            } => {
                self.append("tool_call ")?;
                self.append(call_id)?;
                self.append(" ")?;
                self.append(name)?;
                self.append("\n")?;
                self.append(arguments)?;
            }
            InputItem::ToolResult { call_id, output } => {
                self.append("tool_result ")?;
                self.append(call_id)?;
                self.append("\n")?;
                self.append(output)?;
            }
            InputItem::Reasoning {
                content,
                provider_data,
            } => {
                self.append("reasoning\n")?;
                for part in content {
                    self.append(&part.text)?;
                }
                if provider_data.is_some() {
                    self.omit(index, "provider_continuation")?;
                }
            }
            InputItem::ProviderItem { .. } => self.omit(index, "provider_item")?,
        }
        self.append("\n")?;
        self.spans.try_reserve_exact(1).map_err(|_| too_large())?;
        self.spans.push(SourceLocation {
            index,
            start,
            end: self.text.len(),
        });
        Ok(())
    }
    fn omit(&mut self, index: usize, kind: &'static str) -> Result<()> {
        self.append("[omitted ")?;
        self.append(kind)?;
        self.append("]")?;
        self.omissions
            .try_reserve_exact(1)
            .map_err(|_| too_large())?;
        self.omissions.push(ProjectionOmission {
            source: index,
            kind,
        });
        Ok(())
    }
    fn record(&mut self, index: usize, record: &JournalRecord) -> Result<()> {
        let start = self.text.len();
        match record {
            JournalRecord::Messages { items } => {
                for item in items {
                    self.item(index, item)?;
                }
            }
            JournalRecord::Compaction {
                summary, keep_from, ..
            } => {
                self.append("compaction keep_from=")?;
                self.json(keep_from)?;
                self.append("\n")?;
                self.append(summary)?;
                self.append("\n")?;
            }
            JournalRecord::TaskStarted {
                task_id,
                configuration,
                ..
            } => {
                self.append("task_started ")?;
                self.append(task_id)?;
                self.append("\n")?;
                if let Some(configuration) = configuration {
                    self.json(configuration)?;
                    self.append("\n")?;
                }
            }
            JournalRecord::TaskFinished {
                task_id,
                status,
                error,
            } => {
                self.append("task_finished ")?;
                self.append(task_id)?;
                self.append(" ")?;
                self.json(status)?;
                self.append("\n")?;
                if let Some(error) = error {
                    self.append(error)?;
                    self.append("\n")?;
                }
            }
        }
        if !matches!(record, JournalRecord::Messages { .. }) {
            self.spans.try_reserve_exact(1).map_err(|_| too_large())?;
            self.spans.push(SourceLocation {
                index,
                start,
                end: self.text.len(),
            });
        }
        Ok(())
    }
    fn json(&mut self, value: &impl Serialize) -> Result<()> {
        serde_json::to_writer(self, value).map_err(|_| too_large())
    }
}
impl Write for Projection {
    fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
        let text = std::str::from_utf8(bytes).map_err(std::io::Error::other)?;
        self.append(text).map_err(std::io::Error::other)?;
        Ok(bytes.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

#[cfg(test)]
#[path = "../../tests/session/context_access.rs"]
mod tests;
