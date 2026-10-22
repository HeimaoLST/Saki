use std::{
    collections::{HashMap, HashSet},
    fs::File,
    path::PathBuf,
    sync::Arc,
};

use serde::Serialize;
use tokio::{
    io::{AsyncBufRead, AsyncWrite},
    sync::{mpsc, oneshot, watch},
    task::JoinHandle,
};

#[cfg(test)]
use serde_json::{Value, json};
#[cfg(test)]
use tokio::io::{AsyncBufReadExt, AsyncWriteExt};

use super::{
    SYSTEM_PROMPT, build_agent,
    config::{
        self, CatalogModel, ConfigPatch, ConfigView, ConfiguredModel, EffectiveConfig,
        PreparedModelConfig,
    },
    store::{SessionMetadata, SessionStore, SessionSummary},
};
use crate::{
    agent::{
        Agent,
        conversation::CompactionConfig,
        events::{
            BufferMetrics, BufferStats, ByteCounter, EventLimits, EventPacket, EventSink,
            OutputState,
        },
        is_interrupted,
        journal::{self, JournalRecord, Replay, TaskStatus},
        message::UserMessage,
    },
    client::{ModelApi, ModelClient},
    model::catalog::ModelCatalog,
};

#[derive(Debug, Clone, Serialize)]
pub struct RuntimeError {
    pub code: &'static str,
    pub message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub session_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub current_revision: Option<u64>,
}

impl RuntimeError {
    pub(crate) fn new(code: &'static str, message: impl Into<String>) -> Self {
        Self {
            code,
            message: message.into(),
            session_id: None,
            current_revision: None,
        }
    }

    pub(crate) fn conflict(revision: u64) -> Self {
        let mut error = Self::new(
            "config_conflict",
            "configuration revision changed; read config before retrying",
        );
        error.current_revision = Some(revision);
        error
    }

    pub(crate) fn session(mut self, id: &str) -> Self {
        self.session_id = Some(id.to_owned());
        self
    }
}

impl std::fmt::Display for RuntimeError {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str(&self.message)
    }
}

impl std::error::Error for RuntimeError {}

#[derive(Debug, Clone, Serialize)]
pub struct ActiveTaskState {
    pub task_id: String,
    pub message_id: String,
    pub cancel_requested: bool,
}

#[derive(Debug, Clone, Serialize)]
pub struct SessionState {
    pub session_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub metadata: Option<SessionMetadata>,
    pub loaded: bool,
    pub state: &'static str,
    pub active_task: Option<ActiveTaskState>,
    pub status: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub effective_config: Option<Box<EffectiveConfig>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub config_revision: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub storage_schema: Option<u32>,
}

#[derive(Debug, Clone, Serialize)]
pub struct SessionSummaryView {
    pub metadata: SessionMetadata,
    pub updated_at: u64,
    pub status: String,
    pub in_use: bool,
    pub unavailable: Option<String>,
    pub loaded: bool,
    pub state: &'static str,
    pub active_task_id: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
pub struct HistoryRecord {
    pub offset: usize,
    pub timestamp_ms: u64,
    pub record: JournalRecord,
}

#[derive(Debug, Clone, Serialize)]
pub struct HistoryPage {
    pub session_id: String,
    pub records: Vec<HistoryRecord>,
    pub next_offset: Option<usize>,
    pub status: String,
    pub state: &'static str,
    pub active_task_id: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
pub struct Association {
    pub session_id: String,
    pub task_id: String,
    pub message_id: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct TaskFinished {
    pub status: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<RuntimeError>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub persistence: Option<&'static str>,
    #[serde(skip_serializing_if = "is_false")]
    pub history_required: bool,
}

fn is_false(value: &bool) -> bool {
    !*value
}

pub enum TaskEvent {
    Event(EventPacket),
    Finished(TaskFinished),
}

pub struct TaskEvents {
    metrics: BufferMetrics,
    events: mpsc::Receiver<EventPacket>,
    finished: Option<oneshot::Receiver<TaskFinished>>,
    release: mpsc::Sender<String>,
    output: OutputState,
    task_id: String,
    delivered: bool,
    defer_ack: bool,
    released: bool,
}

impl TaskEvents {
    #[cfg_attr(
        not(test),
        expect(
            dead_code,
            reason = "typed event consumer diagnostics; consumer migration is tracked in #109"
        )
    )]
    pub fn buffer_stats(&self) -> BufferStats {
        self.metrics.snapshot()
    }

    /// Stdio uses this when it must enqueue the final terminal frame before
    /// releasing the runtime's admission slot.
    pub(crate) fn defer_terminal_ack(&mut self) {
        self.defer_ack = true;
    }

    pub(crate) fn acknowledge_finished(&mut self) {
        if self.delivered {
            self.release_slot();
        }
    }

    fn release_slot(&mut self) {
        if self.released {
            return;
        }
        self.released = true;
        if self.release.try_send(self.task_id.clone()).is_err() {
            self.output.close();
        }
    }

    pub async fn next(&mut self) -> Option<TaskEvent> {
        if let Some(packet) = self.events.recv().await {
            return Some(TaskEvent::Event(packet));
        }
        let finished = self.finished.as_mut()?.await;
        self.finished.take();
        match finished {
            Ok(finished) => {
                self.delivered = true;
                if !self.defer_ack {
                    self.release_slot();
                }
                Some(TaskEvent::Finished(finished))
            }
            Err(_) => None,
        }
    }
}

impl Drop for TaskEvents {
    fn drop(&mut self) {
        if !self.released {
            self.output.close();
        }
    }
}

pub struct AcceptedTask {
    pub association: Association,
    pub events: TaskEvents,
}

#[derive(Serialize)]
pub enum RuntimeCommand {
    Capabilities,
    Models {
        connection_id: String,
    },
    ConfigGet {
        session_id: String,
    },
    ConfigUpdate {
        session_id: String,
        expected_revision: u64,
        patch: ConfigPatch,
    },
    Create {
        working_directory: Option<PathBuf>,
        system_prompt: Option<String>,
    },
    Load {
        session_id: String,
    },
    List,
    Rename {
        session_id: String,
        title: String,
    },
    History {
        session_id: String,
        offset: usize,
        limit: usize,
    },
    Status {
        session_id: String,
    },
    Release {
        session_id: String,
    },
    Send {
        session_id: String,
        message: UserMessage,
    },
    Cancel {
        session_id: String,
        task_id: String,
    },
    Shutdown,
}

pub enum RuntimeReply {
    Capabilities(Capabilities),
    Models {
        connection_id: String,
        models: Vec<CatalogModel>,
        configured_models: Vec<ConfiguredModel>,
    },
    Config(ConfigView),
    ConfigUpdated {
        config: ConfigView,
        effective_from: &'static str,
    },
    Session(SessionState),
    List {
        sessions: Vec<SessionSummaryView>,
    },
    Renamed {
        session_id: String,
        metadata: SessionMetadata,
    },
    History(HistoryPage),
    Released {
        session_id: String,
        loaded: bool,
    },
    Accepted(AcceptedTask),
    Cancelled {
        status: &'static str,
    },
    Shutdown {
        state: &'static str,
    },
}

#[derive(Debug, Clone, Serialize)]
pub struct Capabilities {
    pub protocol_version: u32,
    pub methods: Vec<&'static str>,
    pub max_active_tasks: usize,
    pub session_configuration: bool,
    pub multi_connection: bool,
    pub external_tools: bool,
    pub caller_context: bool,
}

#[derive(Clone)]
pub struct RuntimeHandle {
    commands: mpsc::Sender<Envelope>,
    output: OutputState,
    work_drained: watch::Receiver<bool>,
}

type ReplyResult = std::result::Result<RuntimeReply, RuntimeError>;
type ReplyReceiver = oneshot::Receiver<ReplyResult>;

impl RuntimeHandle {
    /// Submit a command without waiting for the reply. `try_send` preserves the
    /// order in which the stdio adapter submits requests while allowing each
    /// caller to await independently.
    pub fn submit(
        &self,
        command: RuntimeCommand,
    ) -> std::result::Result<ReplyReceiver, RuntimeError> {
        let mut size = ByteCounter::new(8 * 1024 * 1024);
        serde_json::to_writer(&mut size, &command).map_err(|_| {
            RuntimeError::new("invalid_request", "runtime command exceeds its 8 MiB limit")
        })?;
        let (reply, response) = oneshot::channel();
        match self.commands.try_send(Envelope { command, reply }) {
            Ok(()) => Ok(response),
            Err(mpsc::error::TrySendError::Full(_)) => {
                Err(RuntimeError::new("busy", "runtime command mailbox is full"))
            }
            Err(mpsc::error::TrySendError::Closed(_)) => {
                Err(RuntimeError::new("io_error", "runtime is not running"))
            }
        }
    }

    pub async fn call(
        &self,
        command: RuntimeCommand,
    ) -> std::result::Result<RuntimeReply, RuntimeError> {
        let response = self.submit(command)?;
        match response.await {
            Ok(result) => result,
            Err(_) => Err(RuntimeError::new(
                "runtime_error",
                "runtime stopped before replying",
            )),
        }
    }

    pub(crate) fn work_drained(&self) -> watch::Receiver<bool> {
        self.work_drained.clone()
    }

    pub fn output_state(&self) -> OutputState {
        self.output.clone()
    }
}

pub struct Runtime {
    store: Arc<SessionStore>,
    client: ModelClient,
    cwd: PathBuf,
    model: String,
    config: CompactionConfig,
    limits: EventLimits,
    catalog: Option<Arc<ModelCatalog>>,
}

impl Runtime {
    pub fn new(
        store: SessionStore,
        client: ModelClient,
        cwd: PathBuf,
        model: String,
        config: CompactionConfig,
    ) -> Self {
        Self {
            store: Arc::new(store),
            client,
            cwd,
            model,
            config,
            limits: EventLimits::default(),
            catalog: None,
        }
    }

    pub fn with_catalog(mut self, catalog: Option<ModelCatalog>) -> Self {
        self.catalog = catalog.map(Arc::new);
        self
    }

    pub fn api(&self) -> ModelApi {
        self.client.api()
    }

    #[cfg_attr(
        not(test),
        expect(
            dead_code,
            reason = "typed service capacity configuration; production defaults until concurrent sessions"
        )
    )]
    pub fn with_event_limits(mut self, limits: EventLimits) -> Self {
        self.limits = limits;
        self
    }

    pub async fn run<R, W>(self, input: R, writer: W) -> anyhow::Result<()>
    where
        R: AsyncBufRead + Unpin,
        W: AsyncWrite + Unpin + Send + 'static,
    {
        super::stdio::run(self, input, writer).await
    }

    pub fn start(self) -> (RuntimeHandle, JoinHandle<anyhow::Result<()>>) {
        let (commands_tx, commands_rx) = mpsc::channel(32);
        let (ops_tx, ops_rx) = mpsc::channel(MAX_PREPARED);
        let (workers_tx, workers_rx) = mpsc::channel(1);
        let (releases_tx, releases_rx) = mpsc::channel(8);
        let output = OutputState::default();
        let (drained_tx, drained_rx) = watch::channel(false);
        let handle = RuntimeHandle {
            commands: commands_tx,
            output: output.clone(),
            work_drained: drained_rx,
        };
        let actor = RuntimeActor {
            store: self.store,
            client: self.client,
            cwd: self.cwd,
            model: self.model,
            config: self.config,
            limits: self.limits,
            catalog: self.catalog,
            output,
            work_drained: drained_tx,
            sessions: HashMap::new(),
            active: None,
            preparing: HashSet::new(),
            configuring: HashSet::new(),
            in_flight: 0,
            accepting: true,
            commands: commands_rx,
            ops_tx,
            ops_rx,
            workers_tx,
            workers_rx,
            releases_tx,
            releases_rx,
        };
        (handle, tokio::spawn(actor.run()))
    }
}

const MAX_PREPARED: usize = 8;

struct Envelope {
    command: RuntimeCommand,
    reply: oneshot::Sender<ReplyResult>,
}

struct OpDone {
    reply: oneshot::Sender<ReplyResult>,
    session_id: Option<String>,
    result: std::result::Result<Prepared, RuntimeError>,
}

enum Prepared {
    Configuration(ConfigView),
    Configured {
        session_id: String,
        metadata: SessionMetadata,
        selection: PreparedModelConfig,
    },
    Created {
        metadata: SessionMetadata,
        agent: Agent,
        status: TaskStatus,
    },
    Loaded {
        metadata: SessionMetadata,
        agent: Agent,
        status: TaskStatus,
    },
    Listed(Vec<SessionSummary>),
    History {
        session_id: String,
        replay: Replay,
        offset: usize,
        limit: usize,
    },
    Status {
        session_id: String,
        replay: Replay,
    },
    Renamed {
        session_id: String,
        metadata: SessionMetadata,
    },
    Released {
        session_id: String,
    },
}

struct SessionEntry {
    metadata: SessionMetadata,
    agent: Option<Agent>,
    status: TaskStatus,
    system_prompt: String,
    commit_unknown: bool,
}

struct Active {
    association: Association,
    cancel: watch::Sender<bool>,
    terminal: Option<oneshot::Sender<TaskFinished>>,
    sink: Option<EventSink>,
    done: bool,
}

struct WorkerDone {
    task_id: String,
    outcome: std::result::Result<(Agent, anyhow::Result<()>), tokio::task::JoinError>,
}

struct RuntimeActor {
    store: Arc<SessionStore>,
    client: ModelClient,
    cwd: PathBuf,
    model: String,
    config: CompactionConfig,
    limits: EventLimits,
    catalog: Option<Arc<ModelCatalog>>,
    output: OutputState,
    work_drained: watch::Sender<bool>,
    sessions: HashMap<String, SessionEntry>,
    active: Option<Active>,
    preparing: HashSet<String>,
    configuring: HashSet<String>,
    in_flight: usize,
    accepting: bool,
    commands: mpsc::Receiver<Envelope>,
    ops_tx: mpsc::Sender<OpDone>,
    ops_rx: mpsc::Receiver<OpDone>,
    workers_tx: mpsc::Sender<WorkerDone>,
    workers_rx: mpsc::Receiver<WorkerDone>,
    releases_tx: mpsc::Sender<String>,
    releases_rx: mpsc::Receiver<String>,
}
impl RuntimeActor {
    async fn run(mut self) -> anyhow::Result<()> {
        let output_observer = self.output.clone();
        let mut output_closed = Box::pin(async move { output_observer.closed().await });
        let mut output_closed_seen = self.output.is_closed();
        let mut commands_closed = false;
        if output_closed_seen {
            self.accepting = false;
        }
        loop {
            if !self.accepting
                && self.in_flight == 0
                && self.active.as_ref().is_none_or(|active| active.done)
            {
                self.work_drained.send_replace(true);
            }
            if !self.accepting && self.active.is_none() && self.in_flight == 0 {
                self.drain_stopped_commands();
                break;
            }
            tokio::select! {
                biased;
                _ = &mut output_closed, if !output_closed_seen => {
                    output_closed_seen = true;
                    self.accepting = false;
                    if self.active.as_ref().is_some_and(|active| active.done) {
                        self.active = None;
                    }
                }
                Some(done) = self.ops_rx.recv() => self.finish_op(done),
                Some(done) = self.workers_rx.recv() => self.finish_worker(done),
                Some(task_id) = self.releases_rx.recv(), if self.active.as_ref().is_some_and(|active| active.done) => {
                    if self.active.as_ref().is_some_and(|active| active.association.task_id == task_id) {
                        self.active = None;
                    }
                }
                command = self.commands.recv(), if !commands_closed => {
                    match command {
                        Some(envelope) => {
                            let stopped_control = matches!(
                                &envelope.command,
                                RuntimeCommand::Cancel { .. } | RuntimeCommand::Shutdown
                            );
                            if self.accepting || stopped_control {
                                self.handle_command(envelope);
                            } else {
                                let _ = envelope.reply.send(Err(RuntimeError::new(
                                    "io_error",
                                    "runtime is no longer accepting commands",
                                )));
                            }
                        }
                        None => {
                            commands_closed = true;
                            self.accepting = false;
                        }
                    }
                }
            }
        }
        Ok(())
    }

    fn admit(&self) -> bool {
        self.in_flight < MAX_PREPARED
    }

    fn drain_stopped_commands(&mut self) {
        while let Ok(envelope) = self.commands.try_recv() {
            if matches!(
                &envelope.command,
                RuntimeCommand::Cancel { .. } | RuntimeCommand::Shutdown
            ) {
                self.handle_command(envelope);
            } else {
                let _ = envelope.reply.send(Err(RuntimeError::new(
                    "io_error",
                    "runtime is no longer accepting commands",
                )));
            }
        }
    }

    fn reject_busy(&self, reply: oneshot::Sender<ReplyResult>) {
        let _ = reply.send(Err(RuntimeError::new(
            "busy",
            "too many runtime operations are being prepared",
        )));
    }

    fn spawn_op<F>(
        &mut self,
        reply: oneshot::Sender<ReplyResult>,
        session_id: Option<String>,
        job: F,
    ) where
        F: FnOnce() -> std::result::Result<Prepared, RuntimeError> + Send + 'static,
    {
        self.in_flight += 1;
        let sender = self.ops_tx.clone();
        tokio::spawn(async move {
            let result = tokio::task::spawn_blocking(job).await;
            let result = match result {
                Ok(result) => result,
                Err(error) => Err(RuntimeError::new(
                    "runtime_error",
                    format!("preparation task failed: {error}"),
                )),
            };
            let _ = sender
                .send(OpDone {
                    reply,
                    session_id,
                    result,
                })
                .await;
        });
    }

    fn finish_op(&mut self, done: OpDone) {
        self.in_flight = self.in_flight.saturating_sub(1);
        if let Some(session_id) = &done.session_id {
            self.preparing.remove(session_id);
            self.configuring.remove(session_id);
        }
        let reply = match done.result {
            Ok(prepared) => self.apply(prepared),
            Err(error) => {
                if error.code == "commit_unknown"
                    && let Some(session) = done
                        .session_id
                        .as_ref()
                        .and_then(|id| self.sessions.get_mut(id))
                {
                    session.agent = None;
                    session.commit_unknown = true;
                }
                Err(error)
            }
        };
        let _ = done.reply.send(reply);
    }

    fn handle_command(&mut self, envelope: Envelope) {
        let Envelope { command, reply } = envelope;
        match command {
            RuntimeCommand::Capabilities => {
                let _ = reply.send(Ok(RuntimeReply::Capabilities(Capabilities {
                    protocol_version: 1,
                    methods: vec![
                        "runtime.capabilities",
                        "model.list",
                        "session.config.get",
                        "session.config.update",
                        "session.create",
                        "session.load",
                        "session.list",
                        "session.rename",
                        "session.history",
                        "session.status",
                        "session.release",
                        "task.send",
                        "task.cancel",
                        "runtime.shutdown",
                    ],
                    max_active_tasks: 1,
                    session_configuration: true,
                    multi_connection: false,
                    external_tools: false,
                    caller_context: true,
                })));
            }
            RuntimeCommand::Models { connection_id } => {
                if connection_id != "default" {
                    let _ = reply.send(Err(RuntimeError::new(
                        "unknown_connection",
                        "connection is not configured",
                    )));
                    return;
                }
                let models = self.catalog.as_ref().map_or_else(Vec::new, |catalog| {
                    catalog
                        .entries()
                        .into_iter()
                        .map(|entry| CatalogModel {
                            catalog_id: entry.id,
                            name: entry.name,
                            limits: entry.limits,
                        })
                        .collect()
                });
                let configured_models = vec![ConfiguredModel {
                    model: self.model.clone(),
                    compaction: self.config,
                }];
                let _ = reply.send(Ok(RuntimeReply::Models {
                    connection_id,
                    models,
                    configured_models,
                }));
            }
            RuntimeCommand::ConfigGet { session_id } => {
                if !self.admit() {
                    self.reject_busy(reply);
                    return;
                }
                let store = self.store.clone();
                let api = self.client.api().as_str().to_owned();
                self.spawn_op(reply, None, move || {
                    let metadata = metadata_for(&store, &api, &session_id)?;
                    let replay = replay_for(&store, &api, &session_id)?;
                    Ok(Prepared::Configuration(ConfigView::from_metadata(
                        &metadata,
                        config::system_prompt(&replay.context),
                    )))
                });
            }
            RuntimeCommand::ConfigUpdate {
                session_id,
                expected_revision,
                patch,
            } => {
                self.configure(reply, session_id, expected_revision, patch);
            }
            RuntimeCommand::Create {
                working_directory,
                system_prompt,
            } => {
                let cwd = working_directory.unwrap_or_else(|| self.cwd.clone());
                if !self.admit() {
                    self.reject_busy(reply);
                    return;
                }
                let store = self.store.clone();
                let client = self.client.clone();
                let model = self.model.clone();
                let config = self.config;
                let api = self.client.api().as_str().to_owned();
                let system_prompt = system_prompt.unwrap_or_else(|| SYSTEM_PROMPT.to_owned());
                self.spawn_op(reply, None, move || {
                    if !cwd.is_dir() {
                        return Err(RuntimeError::new(
                            "invalid_request",
                            "working_directory must exist and be a directory",
                        ));
                    }
                    let id = store
                        .create_with_api(&cwd, &model, &system_prompt, &api, config)
                        .map_err(io_error)?;
                    create_loaded(store, client, api, id)
                });
            }
            RuntimeCommand::Load { session_id } => {
                if let Some(session) = self.sessions.get(&session_id)
                    && (session.agent.is_some()
                        || self
                            .active
                            .as_ref()
                            .is_some_and(|active| active.association.session_id == session_id))
                {
                    let metadata = Some(session.metadata.clone());
                    let _ = reply.send(Ok(RuntimeReply::Session(self.session_state(
                        &session_id,
                        metadata,
                        None,
                    ))));
                    return;
                }
                if self.preparing.contains(&session_id) {
                    let _ = reply
                        .send(Err(RuntimeError::new("busy", "session is being prepared")
                            .session(&session_id)));
                    return;
                }
                if !self.admit() {
                    self.reject_busy(reply);
                    return;
                }
                self.preparing.insert(session_id.clone());
                let store = self.store.clone();
                let client = self.client.clone();
                let api = self.client.api().as_str().to_owned();
                let id = session_id.clone();
                self.spawn_op(reply, Some(session_id), move || {
                    let metadata = metadata_for(&store, &api, &id)?;
                    let loaded = open_for_api(&store, &api, &id)?;
                    let status = loaded.replay.status.clone();
                    let agent = build_agent(&store, &client, loaded).map_err(io_error)?;
                    Ok(Prepared::Loaded {
                        metadata,
                        agent,
                        status,
                    })
                });
            }
            RuntimeCommand::List => {
                if !self.admit() {
                    self.reject_busy(reply);
                    return;
                }
                let store = self.store.clone();
                self.spawn_op(reply, None, move || {
                    Ok(Prepared::Listed(store.list().map_err(io_error)?))
                });
            }
            RuntimeCommand::Rename { session_id, title } => {
                if title.trim().is_empty() || title.contains(['\r', '\n']) {
                    let _ = reply.send(Err(RuntimeError::new(
                        "invalid_request",
                        "title must be a nonempty single line",
                    )
                    .session(&session_id)));
                    return;
                }
                if self.preparing.contains(&session_id) {
                    let _ = reply
                        .send(Err(RuntimeError::new("busy", "session is being prepared")
                            .session(&session_id)));
                    return;
                }
                if !self.admit() {
                    self.reject_busy(reply);
                    return;
                }
                self.preparing.insert(session_id.clone());
                let store = self.store.clone();
                let api = self.client.api().as_str().to_owned();
                let id = session_id.clone();
                self.spawn_op(reply, Some(session_id), move || {
                    metadata_for(&store, &api, &id)?;
                    let metadata = store.rename(&id, &title).map_err(io_error)?;
                    Ok(Prepared::Renamed {
                        session_id: id,
                        metadata,
                    })
                });
            }
            RuntimeCommand::History {
                session_id,
                offset,
                limit,
            } => {
                if limit == 0 {
                    let _ = reply.send(Err(RuntimeError::new(
                        "invalid_request",
                        "history limit must be positive",
                    )));
                    return;
                }
                if !self.admit() {
                    self.reject_busy(reply);
                    return;
                }
                let store = self.store.clone();
                let api = self.client.api().as_str().to_owned();
                let id = session_id.clone();
                self.spawn_op(reply, None, move || {
                    let replay = replay_for(&store, &api, &id)?;
                    Ok(Prepared::History {
                        session_id: id,
                        replay,
                        offset,
                        limit,
                    })
                });
            }
            RuntimeCommand::Status { session_id } => {
                if self.sessions.contains_key(&session_id) {
                    let _ = reply.send(Ok(RuntimeReply::Session(self.session_state(
                        &session_id,
                        None,
                        None,
                    ))));
                    return;
                }
                if !self.admit() {
                    self.reject_busy(reply);
                    return;
                }
                let store = self.store.clone();
                let api = self.client.api().as_str().to_owned();
                let id = session_id.clone();
                self.spawn_op(reply, None, move || {
                    let replay = replay_for(&store, &api, &id)?;
                    Ok(Prepared::Status {
                        session_id: id,
                        replay,
                    })
                });
            }
            RuntimeCommand::Release { session_id } => {
                if self
                    .active
                    .as_ref()
                    .is_some_and(|active| active.association.session_id == session_id)
                {
                    let _ =
                        reply.send(Err(RuntimeError::new("busy", "session has an active task")
                            .session(&session_id)));
                    return;
                }
                if self.preparing.contains(&session_id) {
                    let _ = reply
                        .send(Err(RuntimeError::new("busy", "session is being prepared")
                            .session(&session_id)));
                    return;
                }
                if !self.admit() {
                    self.reject_busy(reply);
                    return;
                }
                self.preparing.insert(session_id.clone());
                let store = self.store.clone();
                let api = self.client.api().as_str().to_owned();
                let id = session_id.clone();
                self.spawn_op(reply, Some(session_id), move || {
                    metadata_for(&store, &api, &id)?;
                    Ok(Prepared::Released { session_id: id })
                });
            }
            RuntimeCommand::Send {
                session_id,
                message,
            } => {
                self.send_command(reply, session_id, message);
            }
            RuntimeCommand::Cancel {
                session_id,
                task_id,
            } => {
                let status = match &self.active {
                    Some(active)
                        if active.association.session_id == session_id
                            && active.association.task_id == task_id
                            && !active.done =>
                    {
                        if *active.cancel.borrow() {
                            "already_requested"
                        } else {
                            active.cancel.send_replace(true);
                            "requested"
                        }
                    }
                    _ => "not_running",
                };
                let _ = reply.send(Ok(RuntimeReply::Cancelled { status }));
            }
            RuntimeCommand::Shutdown => {
                self.accepting = false;
                let _ = reply.send(Ok(RuntimeReply::Shutdown { state: "draining" }));
            }
        }
    }

    fn configure(
        &mut self,
        reply: oneshot::Sender<ReplyResult>,
        session_id: String,
        expected_revision: u64,
        patch: ConfigPatch,
    ) {
        if self.configuring.contains(&session_id) {
            let revision = self
                .sessions
                .get(&session_id)
                .expect("configuration reserves a loaded session")
                .metadata
                .config_revision;
            let mut error = RuntimeError::conflict(revision);
            error.message =
                "another configuration update is being committed; read config before retrying"
                    .to_owned();
            let _ = reply.send(Err(error.session(&session_id)));
            return;
        }
        if self.preparing.contains(&session_id)
            || self
                .active
                .as_ref()
                .is_some_and(|active| active.association.session_id == session_id)
        {
            let _ = reply.send(Err(RuntimeError::new(
                "busy",
                "session has accepted work or a configuration operation",
            )
            .session(&session_id)));
            return;
        }
        let Some(session) = self.sessions.get(&session_id) else {
            let _ = reply.send(Err(RuntimeError::new(
                "not_found",
                "load the session before updating configuration",
            )
            .session(&session_id)));
            return;
        };
        let Some(agent) = session.agent.as_ref() else {
            let _ = reply.send(Err(RuntimeError::new(
                "commit_unknown",
                "reload the session before changing its configuration",
            )
            .session(&session_id)));
            return;
        };
        if session.metadata.config_revision != expected_revision {
            let _ = reply.send(Err(RuntimeError::conflict(
                session.metadata.config_revision,
            )
            .session(&session_id)));
            return;
        }
        let selection = match config::prepare_model_config(
            &session.metadata,
            agent.context_items(),
            patch,
            self.catalog.as_deref(),
            &self.model,
            self.config,
        ) {
            Ok(selection) => selection,
            Err(error) => {
                let _ = reply.send(Err(error.session(&session_id)));
                return;
            }
        };
        if !selection.changed {
            let view = ConfigView::from_metadata(&session.metadata, session.system_prompt.clone());
            let _ = reply.send(Ok(RuntimeReply::ConfigUpdated {
                config: view,
                effective_from: "next_task",
            }));
            return;
        }
        if !self.admit() {
            self.reject_busy(reply);
            return;
        }
        self.preparing.insert(session_id.clone());
        self.configuring.insert(session_id.clone());
        let store = self.store.clone();
        let id = session_id.clone();
        self.spawn_op(reply, Some(session_id), move || {
            let metadata = store
                .update_model_config(
                    &id,
                    expected_revision,
                    selection.model.clone(),
                    selection.compaction.get(),
                    selection.capacity_source.clone(),
                )
                .map_err(|error| error.session(&id))?;
            Ok(Prepared::Configured {
                session_id: id,
                metadata,
                selection,
            })
        });
    }

    fn send_command(
        &mut self,
        reply: oneshot::Sender<ReplyResult>,
        session_id: String,
        message: UserMessage,
    ) {
        let Some(session) = self.sessions.get(&session_id) else {
            let _ =
                reply
                    .send(Err(RuntimeError::new("not_found", "session is not loaded")
                        .session(&session_id)));
            return;
        };
        if self.active.is_some() {
            let _ = reply.send(Err(RuntimeError::new(
                "busy",
                "runtime already has an active task",
            )
            .session(&session_id)));
            return;
        }
        if self.preparing.contains(&session_id) {
            let _ = reply.send(Err(
                RuntimeError::new("busy", "session is being prepared").session(&session_id)
            ));
            return;
        }
        if session.commit_unknown {
            let _ = reply.send(Err(RuntimeError::new(
                "commit_unknown",
                "configuration publication is uncertain; inspect config and reload",
            )
            .session(&session_id)));
            return;
        }
        if session.agent.is_none() {
            let _ = reply.send(Err(RuntimeError::new(
                "io_error",
                "session is unavailable; release/load it from the journal to recover",
            )
            .session(&session_id)));
            return;
        }
        if let Err(error) = message.validate() {
            let _ = reply.send(Err(
                RuntimeError::new("invalid_request", error).session(&session_id)
            ));
            return;
        }
        if self.client.api() == ModelApi::ChatCompletions && !message.images.is_empty() {
            let _ = reply.send(Err(RuntimeError::new(
                "invalid_request",
                "images are unsupported by this API",
            )
            .session(&session_id)));
            return;
        }
        let task_id = match journal::new_id() {
            Ok(id) => id,
            Err(error) => {
                let _ = reply.send(Err(io_error(error).session(&session_id)));
                return;
            }
        };
        let message_id = match journal::new_id() {
            Ok(id) => id,
            Err(error) => {
                let _ = reply.send(Err(io_error(error).session(&session_id)));
                return;
            }
        };
        let configuration = journal::TaskConfiguration {
            model: session.metadata.model.clone(),
            api: session.metadata.api.clone(),
            compaction: session.metadata.compaction,
            config_revision: session.metadata.config_revision,
        };
        let Some(agent) = self
            .sessions
            .get_mut(&session_id)
            .and_then(|session| session.agent.take())
        else {
            let _ = reply.send(Err(RuntimeError::new(
                "io_error",
                "session is unavailable; release/load it from the journal to recover",
            )
            .session(&session_id)));
            return;
        };

        let (cancel, cancel_rx) = watch::channel(false);
        let (sink, events) =
            EventSink::bounded(self.limits, cancel_rx.clone(), self.output.clone());
        let history_sink = sink.clone();
        let agent = agent.with_event_sink(sink).with_cancel(cancel_rx);
        let (terminal, finished) = oneshot::channel();
        let association = Association {
            session_id,
            task_id: task_id.clone(),
            message_id: message_id.clone(),
        };
        let worker_task_id = task_id.clone();
        let worker_message_id = message_id.clone();
        let worker = tokio::spawn(async move {
            let mut agent = agent;
            let result = agent
                .handle_configured_task(
                    message,
                    worker_task_id,
                    worker_message_id,
                    Some(configuration),
                )
                .await;
            (agent, result)
        });
        let workers = self.workers_tx.clone();
        let done_task_id = task_id.clone();
        tokio::spawn(async move {
            let outcome = worker.await;
            let _ = workers
                .send(WorkerDone {
                    task_id: done_task_id,
                    outcome,
                })
                .await;
        });
        let metrics = history_sink.metrics();
        self.active = Some(Active {
            association: association.clone(),
            cancel,
            terminal: Some(terminal),
            sink: Some(history_sink),
            done: false,
        });
        let _ = reply.send(Ok(RuntimeReply::Accepted(AcceptedTask {
            association,
            events: TaskEvents {
                metrics,
                events,
                finished: Some(finished),
                release: self.releases_tx.clone(),
                output: self.output.clone(),
                task_id,
                delivered: false,
                defer_ack: false,
                released: false,
            },
        })));
    }
}
impl RuntimeActor {
    fn finish_worker(&mut self, done: WorkerDone) {
        let Some(mut active) = self.active.take() else {
            return;
        };
        if active.association.task_id != done.task_id {
            self.active = Some(active);
            return;
        }
        let history_required = active
            .sink
            .as_ref()
            .is_some_and(EventSink::history_required);
        let session_id = active.association.session_id.clone();
        let mut persistence = None;
        let mut restored_agent = None;
        let status;
        let finished_status;
        let error;
        match done.outcome {
            Ok((mut agent, result)) => {
                let io_failure = result.as_ref().err().is_some_and(is_io_error);
                if io_failure {
                    persistence = Some("unconfirmed");
                }
                agent.clear_event_sink();
                active.sink.take();
                match result {
                    Ok(()) => {
                        status = TaskStatus::Completed;
                        finished_status = "completed";
                        error = None;
                        restored_agent = Some(agent);
                    }
                    Err(worker_error) => {
                        if is_interrupted(&worker_error) {
                            status = TaskStatus::Interrupted;
                            finished_status = "interrupted";
                            error = None;
                            restored_agent = Some(agent);
                        } else {
                            status = TaskStatus::Failed;
                            finished_status = "failed";
                            if io_failure {
                                error = Some(
                                    RuntimeError::new("io_error", format!("{worker_error:#}"))
                                        .session(&session_id),
                                );
                            } else {
                                error = Some(
                                    RuntimeError::new("model_error", format!("{worker_error:#}"))
                                        .session(&session_id),
                                );
                                restored_agent = Some(agent);
                            }
                        }
                    }
                }
            }
            Err(join_error) => {
                persistence = Some("unconfirmed");
                active.sink.take();
                status = TaskStatus::Interrupted;
                finished_status = "failed";
                error = Some(
                    RuntimeError::new(
                        "runtime_error",
                        format!("agent worker panicked: {join_error}"),
                    )
                    .session(&session_id),
                );
            }
        }
        if let Some(session) = self.sessions.get_mut(&session_id) {
            session.status = status;
            session.agent = restored_agent;
        }
        active.done = true;
        let finished = TaskFinished {
            status: finished_status,
            error,
            persistence,
            history_required,
        };
        if let Some(terminal) = active.terminal.take() {
            let _ = terminal.send(finished);
        }
        if self.output.is_closed() {
            self.active = None;
        } else {
            self.active = Some(active);
        }
    }

    fn apply(&mut self, prepared: Prepared) -> std::result::Result<RuntimeReply, RuntimeError> {
        match prepared {
            Prepared::Configuration(view) => Ok(RuntimeReply::Config(view)),
            Prepared::Configured {
                session_id,
                metadata,
                selection,
            } => {
                let session = self
                    .sessions
                    .get_mut(&session_id)
                    .expect("configuration holds session admission");
                let agent = session
                    .agent
                    .as_mut()
                    .expect("configuration preserves the session agent");
                agent.apply_model_config(selection.model, selection.compaction);
                session.metadata = metadata;
                Ok(RuntimeReply::ConfigUpdated {
                    config: ConfigView::from_metadata(
                        &session.metadata,
                        session.system_prompt.clone(),
                    ),
                    effective_from: "next_task",
                })
            }
            Prepared::Created {
                metadata,
                agent,
                status,
            }
            | Prepared::Loaded {
                metadata,
                agent,
                status,
            } => {
                let id = metadata.id.clone();
                let system_prompt = config::system_prompt(agent.context_items());
                self.sessions.insert(
                    id.clone(),
                    SessionEntry {
                        metadata: metadata.clone(),
                        agent: Some(agent),
                        status,
                        system_prompt,
                        commit_unknown: false,
                    },
                );
                Ok(RuntimeReply::Session(self.session_state(
                    &id,
                    Some(metadata),
                    None,
                )))
            }
            Prepared::Listed(summaries) => Ok(RuntimeReply::List {
                sessions: summaries
                    .into_iter()
                    .map(|summary| self.summary_view(summary))
                    .collect(),
            }),
            Prepared::History {
                session_id,
                replay,
                offset,
                limit,
            } => Ok(RuntimeReply::History(self.history_page(
                &session_id,
                replay,
                offset,
                limit,
            ))),
            Prepared::Status { session_id, replay } => Ok(RuntimeReply::Session(
                self.session_state(&session_id, None, Some(replay.status)),
            )),
            Prepared::Renamed {
                session_id,
                metadata,
            } => {
                if let Some(session) = self.sessions.get_mut(&session_id) {
                    session.metadata = metadata.clone();
                }
                Ok(RuntimeReply::Renamed {
                    session_id,
                    metadata,
                })
            }
            Prepared::Released { session_id } => {
                self.sessions.remove(&session_id);
                Ok(RuntimeReply::Released {
                    session_id,
                    loaded: false,
                })
            }
        }
    }

    fn session_state(
        &self,
        session_id: &str,
        metadata: Option<SessionMetadata>,
        fallback: Option<TaskStatus>,
    ) -> SessionState {
        let active = self
            .active
            .as_ref()
            .filter(|active| !active.done && active.association.session_id == session_id);
        let running = active.is_some();
        let status = if running {
            "running".to_owned()
        } else if let Some(session) = self.sessions.get(session_id) {
            status_str(&session.status).to_owned()
        } else {
            fallback
                .map(|status| status_str(&status))
                .unwrap_or("unknown")
                .to_owned()
        };
        let config = metadata.as_ref().and_then(|metadata| {
            self.sessions
                .get(session_id)
                .map(|session| ConfigView::from_metadata(metadata, session.system_prompt.clone()))
        });
        SessionState {
            session_id: session_id.to_owned(),
            effective_config: config
                .as_ref()
                .map(|view| Box::new(view.effective_config.clone())),
            config_revision: config.as_ref().map(|view| view.config_revision),
            storage_schema: config.as_ref().map(|view| view.storage_schema),
            metadata,
            loaded: self.sessions.contains_key(session_id),
            state: if running { "running" } else { "idle" },
            active_task: active.map(|active| ActiveTaskState {
                task_id: active.association.task_id.clone(),
                message_id: active.association.message_id.clone(),
                cancel_requested: *active.cancel.borrow(),
            }),
            status,
        }
    }

    fn summary_view(&self, summary: SessionSummary) -> SessionSummaryView {
        let id = summary.metadata.id.clone();
        let active = self
            .active
            .as_ref()
            .filter(|active| !active.done && active.association.session_id == id);
        let running = active.is_some();
        let loaded = self.sessions.contains_key(&id);
        let status = if running {
            "running".to_owned()
        } else if let Some(session) = self.sessions.get(&id) {
            status_str(&session.status).to_owned()
        } else {
            status_str(&summary.status).to_owned()
        };
        SessionSummaryView {
            metadata: summary.metadata,
            updated_at: summary.updated_at,
            status,
            in_use: summary.in_use,
            unavailable: summary.unavailable,
            loaded,
            state: if running { "running" } else { "idle" },
            active_task_id: active.map(|active| active.association.task_id.clone()),
        }
    }

    fn history_page(
        &self,
        session_id: &str,
        replay: Replay,
        offset: usize,
        limit: usize,
    ) -> HistoryPage {
        let total = replay.history.len().min(replay.history_timestamps.len());
        let start = offset.min(total);
        let end = start.saturating_add(limit).min(total);
        let records = replay
            .history
            .iter()
            .zip(&replay.history_timestamps)
            .enumerate()
            .skip(start)
            .take(end - start)
            .map(|(offset, (record, timestamp_ms))| HistoryRecord {
                offset,
                timestamp_ms: *timestamp_ms,
                record: record.clone(),
            })
            .collect();
        let active = self
            .active
            .as_ref()
            .filter(|active| !active.done && active.association.session_id == session_id);
        let status = if active.is_some() {
            "running".to_owned()
        } else if let Some(session) = self.sessions.get(session_id) {
            status_str(&session.status).to_owned()
        } else {
            status_str(&replay.status).to_owned()
        };
        HistoryPage {
            session_id: session_id.to_owned(),
            records,
            next_offset: (end < total).then_some(end),
            status,
            state: if active.is_some() { "running" } else { "idle" },
            active_task_id: active.map(|active| active.association.task_id.clone()),
        }
    }
}

fn is_io_error(error: &anyhow::Error) -> bool {
    error
        .chain()
        .any(|cause| cause.downcast_ref::<std::io::Error>().is_some())
}

fn status_str(status: &TaskStatus) -> &'static str {
    match status {
        TaskStatus::Completed => "completed",
        TaskStatus::Failed => "failed",
        TaskStatus::Interrupted => "interrupted",
        TaskStatus::Unknown => "unknown",
    }
}

fn io_error(error: anyhow::Error) -> RuntimeError {
    let code = if error.chain().any(|cause| {
        cause
            .downcast_ref::<std::io::Error>()
            .is_some_and(|error| error.kind() == std::io::ErrorKind::NotFound)
    }) {
        "not_found"
    } else {
        "io_error"
    };
    RuntimeError::new(code, format!("{error:#}"))
}

fn metadata_for(
    store: &SessionStore,
    api: &str,
    session_id: &str,
) -> std::result::Result<SessionMetadata, RuntimeError> {
    store.directory(session_id).map_err(|error| {
        RuntimeError::new("invalid_request", error.to_string()).session(session_id)
    })?;
    let metadata = store.metadata(session_id).map_err(|error| {
        if is_io_error(&error) {
            io_error(error).session(session_id)
        } else {
            RuntimeError::new("invalid_history", format!("{error:#}")).session(session_id)
        }
    })?;
    if metadata.api != api {
        return Err(RuntimeError::new(
            "api_mismatch",
            format!("session uses {}; runtime uses {api}", metadata.api),
        )
        .session(session_id));
    }
    Ok(metadata)
}

fn replay_for(
    store: &SessionStore,
    api: &str,
    session_id: &str,
) -> std::result::Result<Replay, RuntimeError> {
    metadata_for(store, api, session_id)?;
    let path = store
        .directory(session_id)
        .map_err(io_error)?
        .join("history.jsonl");
    let file = File::open(&path).map_err(|error| io_error(error.into()).session(session_id))?;
    journal::read(&file, &path).map_err(|error| {
        RuntimeError::new("invalid_history", format!("{error:#}")).session(session_id)
    })
}

fn open_for_api(
    store: &SessionStore,
    api: &str,
    session_id: &str,
) -> std::result::Result<super::store::LoadedSession, RuntimeError> {
    store.open_for_api(session_id, api).map_err(|error| {
        let message = format!("{error:#}");
        if matches!(
            error.downcast_ref::<std::fs::TryLockError>(),
            Some(std::fs::TryLockError::WouldBlock)
        ) {
            RuntimeError::new("locked", message).session(session_id)
        } else if is_io_error(&error) {
            io_error(error).session(session_id)
        } else {
            RuntimeError::new("invalid_history", message).session(session_id)
        }
    })
}

fn create_loaded(
    store: Arc<SessionStore>,
    client: ModelClient,
    api: String,
    session_id: String,
) -> std::result::Result<Prepared, RuntimeError> {
    let metadata = metadata_for(&store, &api, &session_id)?;
    let loaded = open_for_api(&store, &api, &session_id)?;
    let status = loaded.replay.status.clone();
    let agent = build_agent(&store, &client, loaded).map_err(io_error)?;
    Ok(Prepared::Created {
        metadata,
        agent,
        status,
    })
}

#[cfg(test)]
#[path = "../../tests/session/runtime.rs"]
mod tests;

#[cfg(test)]
#[path = "../../tests/session/service.rs"]
mod service_tests;

#[cfg(test)]
#[path = "../../tests/session/config.rs"]
mod config_tests;

#[cfg(test)]
#[path = "../../tests/session/context.rs"]
mod context_tests;
