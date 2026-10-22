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
    SYSTEM_PROMPT, build_agent, build_tool_registry,
    config::{
        self, CatalogModel, ConfigPatch, ConfigView, ConfiguredModel, EffectiveConfig,
        HostConfiguration, PreparedModelConfig, UpgradeOptions, prepare_configuration,
        resolve_connection, select_connection,
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
    client::{
        ModelApi, ModelClient,
        connections::{ConnectionRegistry, ConnectionView},
    },
    model::catalog::ModelCatalog,
    tools::tool_registry::ToolRegistry,
};

#[derive(Debug, Clone, Serialize)]
pub struct RuntimeError {
    pub code: &'static str,
    pub message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub session_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub current_revision: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub reason: Option<&'static str>,
}

impl RuntimeError {
    pub(crate) fn new(code: &'static str, message: impl Into<String>) -> Self {
        Self {
            code,
            message: message.into(),
            session_id: None,
            current_revision: None,
            reason: None,
        }
    }

    fn busy(reason: &'static str, message: &'static str) -> Self {
        let mut error = Self::new("busy", message);
        error.reason = Some(reason);
        error
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
pub struct TaskState {
    pub session_id: String,
    pub task_id: String,
    pub status: String,
    pub phase: &'static str,
    pub cancel_requested: bool,
    pub config_revision: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub persistence: Option<&'static str>,
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
    #[cfg(test)]
    pub(crate) fn buffer_metrics(&self) -> BufferMetrics {
        self.metrics.clone()
    }

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
    TaskStatus {
        session_id: String,
        task_id: String,
    },
    Capabilities,
    Connections,
    CreateConfigured {
        config: ConfigPatch,
        storage_schema: u32,
    },
    Rollback {
        session_id: String,
        expected_revision: u64,
    },
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
        allow_schema_upgrade: bool,
        acknowledge_unknown_origin: bool,
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
    TaskStatus(TaskState),
    Capabilities(Capabilities),
    Connections(Vec<ConnectionView>),
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
    pub storage_schemas: Vec<u32>,
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
    connections: Option<Arc<ConnectionRegistry>>,
    max_active_tasks: usize,
    #[cfg(test)]
    panicking_sessions: HashSet<String>,
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
            connections: None,
            max_active_tasks: 1,
            #[cfg(test)]
            panicking_sessions: HashSet::new(),
        }
    }

    pub fn with_max_active_tasks(mut self, capacity: usize) -> anyhow::Result<Self> {
        anyhow::ensure!(
            capacity > 0 && capacity <= tokio::sync::Semaphore::MAX_PERMITS,
            "max_active_tasks is outside the supported channel capacity"
        );
        self.max_active_tasks = capacity;
        Ok(self)
    }

    pub fn max_active_tasks(&self) -> usize {
        self.max_active_tasks
    }

    #[cfg(test)]
    pub(crate) fn with_panicking_session(mut self, session_id: String) -> Self {
        self.panicking_sessions.insert(session_id);
        self
    }

    pub fn with_catalog(mut self, catalog: Option<ModelCatalog>) -> Self {
        self.catalog = catalog.map(Arc::new);
        self
    }

    pub fn with_connections(mut self, connections: ConnectionRegistry) -> Self {
        self.client = connections.default().client().clone();
        self.model = connections.default().default_model().to_owned();
        self.connections = Some(Arc::new(connections));
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
        let (workers_tx, workers_rx) = mpsc::channel(self.max_active_tasks);
        let (releases_tx, releases_rx) = mpsc::channel(self.max_active_tasks);
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
            connections: self.connections,
            output,
            work_drained: drained_tx,
            sessions: HashMap::new(),
            active: HashMap::new(),
            max_active_tasks: self.max_active_tasks,
            #[cfg(test)]
            panicking_sessions: self.panicking_sessions,
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
    TaskStatus(TaskState),
    Configuration(ConfigView),
    Configured {
        session_id: String,
        metadata: SessionMetadata,
        selection: PreparedModelConfig,
        client: ModelClient,
        registry: Option<ToolRegistry>,
        prompt: String,
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
    result: Option<TaskFinished>,
    started: watch::Receiver<bool>,
    config_revision: u64,
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
    connections: Option<Arc<ConnectionRegistry>>,
    output: OutputState,
    work_drained: watch::Sender<bool>,
    sessions: HashMap<String, SessionEntry>,
    active: HashMap<String, Active>,
    max_active_tasks: usize,
    #[cfg(test)]
    panicking_sessions: HashSet<String>,
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
                && self.active.values().all(|active| active.result.is_some())
            {
                self.work_drained.send_replace(true);
            }
            if !self.accepting && self.active.is_empty() && self.in_flight == 0 {
                self.drain_stopped_commands();
                break;
            }
            tokio::select! {
                biased;
                _ = &mut output_closed, if !output_closed_seen => {
                    output_closed_seen = true;
                    self.accepting = false;
                    self.active.retain(|_, active| active.result.is_none());
                }
                Some(done) = self.ops_rx.recv() => self.finish_op(done),
                Some(done) = self.workers_rx.recv() => self.finish_worker(done),
                Some(task_id) = self.releases_rx.recv(), if self.active.values().any(|active| active.result.is_some()) => {
                    if self.active.get(&task_id).is_some_and(|active| active.result.is_some()) {
                        self.active.remove(&task_id);
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

    fn decorate_config(&self, mut view: ConfigView) -> ConfigView {
        view.effective_config.execution_connection_id = if let Some(identity) =
            &view.effective_config.connection_identity
        {
            self.connections
                .as_ref()
                .and_then(|connections| connections.get(&identity.connection_id))
                .filter(|connection| connection.identity() == identity)
                .map(|connection| connection.identity().connection_id.clone())
        } else if let Some(connections) = &self.connections {
            (connections.len() == 1
                && connections.default().identity().api == view.effective_config.api)
                .then(|| connections.default().identity().connection_id.clone())
        } else {
            (self.client.api().as_str() == view.effective_config.api).then(|| "default".to_owned())
        };
        view
    }

    fn active_for_session(&self, session_id: &str) -> Option<&Active> {
        self.active
            .values()
            .find(|active| active.association.session_id == session_id)
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
            RuntimeCommand::TaskStatus {
                session_id,
                task_id,
            } => {
                if let Some(active) = self
                    .active
                    .get(&task_id)
                    .filter(|active| active.association.session_id == session_id)
                {
                    let (status, phase, persistence) = match &active.result {
                        Some(result) => (result.status, "finished", result.persistence),
                        None if *active.started.borrow() => ("running", "running", None),
                        None => ("accepted", "accepted", None),
                    };
                    let _ = reply.send(Ok(RuntimeReply::TaskStatus(TaskState {
                        session_id,
                        task_id,
                        status: status.to_owned(),
                        phase,
                        cancel_requested: *active.cancel.borrow(),
                        config_revision: Some(active.config_revision),
                        persistence,
                    })));
                    return;
                }
                if !self.admit() {
                    self.reject_busy(reply);
                    return;
                }
                let store = self.store.clone();
                self.spawn_op(reply, None, move || {
                    let replay = replay_for(&store, &session_id)?;
                    Ok(Prepared::TaskStatus(replayed_task_state(
                        session_id, task_id, replay,
                    )))
                });
            }
            RuntimeCommand::Connections => {
                let views = self
                    .connections
                    .as_ref()
                    .map_or_else(Vec::new, |connections| connections.views());
                let _ = reply.send(Ok(RuntimeReply::Connections(views)));
            }
            RuntimeCommand::Rollback {
                session_id,
                expected_revision,
            } => {
                if self.sessions.contains_key(&session_id) || self.preparing.contains(&session_id) {
                    let _ = reply.send(Err(RuntimeError::new(
                        "busy",
                        "release the session before rolling back its storage upgrade",
                    )
                    .session(&session_id)));
                    return;
                }
                if !self.admit() {
                    self.reject_busy(reply);
                    return;
                }
                self.preparing.insert(session_id.clone());
                let store = self.store.clone();
                let id = session_id.clone();
                self.spawn_op(reply, Some(session_id), move || {
                    let metadata = store.rollback_config(&id, expected_revision)?;
                    let replay = replay_for(&store, &id)?;
                    Ok(Prepared::Configuration(ConfigView::from_metadata(
                        &metadata,
                        config::system_prompt(&replay.context),
                    )))
                });
            }
            RuntimeCommand::CreateConfigured {
                config,
                storage_schema,
            } => self.create_configured(reply, config, storage_schema),
            RuntimeCommand::Capabilities => {
                let _ = reply.send(Ok(RuntimeReply::Capabilities(Capabilities {
                    protocol_version: 1,
                    storage_schemas: if self.connections.is_some() {
                        vec![1, 2]
                    } else {
                        vec![1]
                    },
                    methods: vec![
                        "runtime.capabilities",
                        "model.list",
                        "connection.list",
                        "session.config.rollback",
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
                        "task.status",
                        "task.cancel",
                        "runtime.shutdown",
                    ],
                    max_active_tasks: self.max_active_tasks,
                    session_configuration: true,
                    multi_connection: self.connections.is_some(),
                    external_tools: false,
                    caller_context: true,
                })));
            }
            RuntimeCommand::Models { connection_id } => {
                let connection_id = if connection_id.is_empty() {
                    self.connections.as_ref().map_or_else(
                        || "default".into(),
                        |directory| directory.default().identity().connection_id.clone(),
                    )
                } else {
                    connection_id
                };
                let configured_model = if let Some(directory) = &self.connections {
                    match directory.get(&connection_id) {
                        Some(connection) => connection.default_model().to_owned(),
                        None => {
                            let _ = reply.send(Err(RuntimeError::new(
                                "unknown_connection",
                                "connection is not configured",
                            )));
                            return;
                        }
                    }
                } else if connection_id == "default" {
                    self.model.clone()
                } else {
                    let _ = reply.send(Err(RuntimeError::new(
                        "unknown_connection",
                        "connection is not configured",
                    )));
                    return;
                };
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
                    model: configured_model.clone(),
                    compaction: self
                        .host_configuration()
                        .model_capacity(&configured_model)
                        .0,
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
                self.spawn_op(reply, None, move || {
                    let metadata = metadata_for(&store, &session_id)?;
                    let replay = replay_for(&store, &session_id)?;
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
                allow_schema_upgrade,
                acknowledge_unknown_origin,
            } => {
                self.configure(
                    reply,
                    session_id,
                    expected_revision,
                    patch,
                    UpgradeOptions {
                        allow_schema_upgrade,
                        acknowledge_unknown_origin,
                    },
                );
            }
            RuntimeCommand::Create {
                working_directory,
                system_prompt,
            } => {
                if self
                    .connections
                    .as_ref()
                    .is_some_and(|connections| connections.len() > 1)
                {
                    let _ = reply.send(Err(RuntimeError::new(
                        "connection_binding_required",
                        "multiple connections require explicit storage_schema:2 for a new session",
                    )));
                    return;
                }
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
                    && (session.agent.is_some() || self.active_for_session(&session_id).is_some())
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
                let host = self.host_configuration();
                let id = session_id.clone();
                self.spawn_op(reply, Some(session_id), move || {
                    let metadata = metadata_for(&store, &id)?;
                    let client =
                        resolve_connection(&metadata, &host.client, host.connections.as_deref())
                            .map_err(|error| error.session(&id))?;
                    let loaded = open_for_api(&store, &metadata.api, &id)?;
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
                let id = session_id.clone();
                self.spawn_op(reply, Some(session_id), move || {
                    metadata_for(&store, &id)?;
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
                let id = session_id.clone();
                self.spawn_op(reply, None, move || {
                    let replay = replay_for(&store, &id)?;
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
                let id = session_id.clone();
                self.spawn_op(reply, None, move || {
                    let replay = replay_for(&store, &id)?;
                    Ok(Prepared::Status {
                        session_id: id,
                        replay,
                    })
                });
            }
            RuntimeCommand::Release { session_id } => {
                if self.active_for_session(&session_id).is_some() {
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
                let id = session_id.clone();
                self.spawn_op(reply, Some(session_id), move || {
                    metadata_for(&store, &id)?;
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
                let status = match self.active.get(&task_id) {
                    Some(active)
                        if active.association.session_id == session_id
                            && active.result.is_none() =>
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

    fn host_configuration(&self) -> HostConfiguration {
        HostConfiguration {
            client: self.client.clone(),
            connections: self.connections.clone(),
            catalog: self.catalog.clone(),
            model: self.model.clone(),
            compaction: self.config,
        }
    }

    fn create_configured(
        &mut self,
        reply: oneshot::Sender<ReplyResult>,
        patch: ConfigPatch,
        storage_schema: u32,
    ) {
        if ![1, 2].contains(&storage_schema) {
            let _ = reply.send(Err(RuntimeError::new(
                "unsupported_schema",
                "supported storage schemas are 1 and 2",
            )));
            return;
        }
        if !self.admit() {
            self.reject_busy(reply);
            return;
        }
        let host = self.host_configuration();
        let selection = select_connection(&host, patch.connection_id.as_deref());
        let (client, identity, default_model) = match selection {
            Ok(value) => value,
            Err(error) => {
                let _ = reply.send(Err(error));
                return;
            }
        };
        if storage_schema == 1
            && (patch.connection_id.is_some()
                || patch.tool_selection.is_some()
                || host
                    .connections
                    .as_ref()
                    .is_some_and(|connections| connections.len() > 1))
        {
            let _ = reply.send(Err(RuntimeError::new(
                "schema_upgrade_required",
                "explicit storage_schema:2 is required for connection or tool selection",
            )));
            return;
        }
        if storage_schema == 2 && identity.is_none() {
            let _ = reply.send(Err(RuntimeError::new(
                "unknown_connection",
                "enhanced sessions require an explicit host connection directory",
            )));
            return;
        }
        let default_tools = config::default_tool_selection();
        let tools = match config::validate_tool_selection(
            patch.tool_selection.as_ref().unwrap_or(&default_tools),
        ) {
            Ok(value) => value,
            Err(error) => {
                let _ = reply.send(Err(error));
                return;
            }
        };
        let mut model_patch = patch.clone();
        model_patch.working_directory = None;
        let api = client.api().as_str().to_owned();
        let (default_capacity, default_source) = host.model_capacity(&default_model);
        let model = match config::prepare_model_config(
            config::ModelConfigInput {
                model: &default_model,
                api: &api,
                compaction: default_capacity,
                capacity_source: &default_source,
            },
            &[],
            model_patch,
            host.catalog.as_deref(),
            &default_model,
            host.model_capacity(&default_model),
        ) {
            Ok(value) => value,
            Err(error) => {
                let _ = reply.send(Err(error));
                return;
            }
        };
        let cwd = patch.working_directory.unwrap_or_else(|| self.cwd.clone());
        let prompt = patch.system_prompt.unwrap_or_else(|| SYSTEM_PROMPT.into());
        let enhanced = if storage_schema == 2 {
            Some(super::store::EnhancedCreation {
                connection_identity: identity.expect("enhanced identity was validated"),
                tool_selection: tools,
            })
        } else {
            None
        };
        let store = self.store.clone();
        self.spawn_op(reply, None, move || {
            let id = store
                .create_configured(super::store::SessionCreation {
                    cwd: &cwd,
                    model: &model.model,
                    system_prompt: &prompt,
                    api: &api,
                    compaction: model.compaction.get(),
                    capacity_source: model.capacity_source,
                    enhanced,
                })
                .map_err(io_error)?;
            create_loaded(store, client, api, id)
        });
    }

    fn configure(
        &mut self,
        reply: oneshot::Sender<ReplyResult>,
        session_id: String,
        expected_revision: u64,
        patch: ConfigPatch,
        upgrade: UpgradeOptions,
    ) {
        if self.configuring.contains(&session_id) {
            let revision = self
                .sessions
                .get(&session_id)
                .map_or(expected_revision, |session| {
                    session.metadata.config_revision
                });
            let _ = reply.send(Err(RuntimeError::conflict(revision).session(&session_id)));
            return;
        }
        if self.preparing.contains(&session_id) || self.active_for_session(&session_id).is_some() {
            let _ = reply.send(Err(RuntimeError::new(
                "busy",
                "session has accepted work or a configuration operation",
            )
            .session(&session_id)));
            return;
        }
        if !self.admit() {
            self.reject_busy(reply);
            return;
        }
        let host = self.host_configuration();
        let prepared = if let Some(session) = self.sessions.get(&session_id) {
            let Some(agent) = session.agent.as_ref() else {
                let _ = reply.send(Err(RuntimeError::new(
                    "commit_unknown",
                    "reload the session before changing configuration",
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
            let candidate = match prepare_configuration(
                &host,
                &session.metadata,
                agent.context_items(),
                &session.system_prompt,
                &patch,
                upgrade,
            ) {
                Ok(candidate) => candidate,
                Err(error) => {
                    let _ = reply.send(Err(error.session(&session_id)));
                    return;
                }
            };
            if !candidate.changed {
                let view = self.decorate_config(ConfigView::from_metadata(
                    &session.metadata,
                    session.system_prompt.clone(),
                ));
                let _ = reply.send(Ok(RuntimeReply::ConfigUpdated {
                    config: view,
                    effective_from: "next_task",
                }));
                return;
            }
            let registry = match build_tool_registry(&self.store, &candidate.metadata) {
                Ok(registry) => registry,
                Err(error) => {
                    let _ = reply.send(Err(io_error(error).session(&session_id)));
                    return;
                }
            };
            Some((candidate, registry))
        } else {
            if !patch.is_only_binding() {
                let _=reply.send(Err(RuntimeError::new("not_found","load the session before updating configuration; an unloaded legacy session only permits explicit binding").session(&session_id)));
                return;
            }
            None
        };
        self.preparing.insert(session_id.clone());
        self.configuring.insert(session_id.clone());
        let store = self.store.clone();
        let id = session_id.clone();
        self.spawn_op(reply, Some(session_id), move || {
            let (candidate, registry, _lock) = match prepared {
                Some((candidate, registry)) => (candidate, Some(registry), None),
                None => {
                    let lock = store.lock_config(&id)?;
                    if lock.metadata.schema_version != 1 {
                        return Err(RuntimeError::new(
                            "not_found",
                            "load an enhanced session before updating its configuration",
                        )
                        .session(&id));
                    }
                    if lock.metadata.config_revision != expected_revision {
                        return Err(
                            RuntimeError::conflict(lock.metadata.config_revision).session(&id)
                        );
                    }
                    let prompt = config::system_prompt(&lock.replay.context);
                    let candidate = prepare_configuration(
                        &host,
                        &lock.metadata,
                        &lock.replay.context,
                        &prompt,
                        &patch,
                        upgrade,
                    )?;
                    (candidate, None, Some(lock))
                }
            };
            let metadata = store
                .update_config(&id, expected_revision, candidate.metadata)
                .map_err(|error| error.session(&id))?;
            Ok(Prepared::Configured {
                session_id: id,
                metadata,
                selection: candidate.selection,
                client: candidate.client,
                registry,
                prompt: candidate.prompt,
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
            if self
                .connections
                .as_ref()
                .is_some_and(|connections| connections.len() > 1)
            {
                if !self.admit() {
                    self.reject_busy(reply);
                    return;
                }
                let store = self.store.clone();
                let id = session_id.clone();
                self.spawn_op(reply,None,move || {
                    let metadata=metadata_for(&store, &id)?;
                    if metadata.schema_version==1 {
                        Err(RuntimeError::new("connection_binding_required","legacy session origin is unknown; explicitly bind a connection before execution").session(&id))
                    } else {Err(RuntimeError::new("not_found","session is not loaded").session(&id))}
                });
            } else {
                let _ = reply
                    .send(Err(RuntimeError::new("not_found", "session is not loaded")
                        .session(&session_id)));
            }
            return;
        };
        if self.active_for_session(&session_id).is_some() {
            let _ = reply.send(Err(RuntimeError::busy(
                "session_busy",
                "session already has an accepted task",
            )
            .session(&session_id)));
            return;
        }
        if self.active.len() >= self.max_active_tasks {
            let _ = reply.send(Err(RuntimeError::busy(
                "capacity_exceeded",
                "runtime task capacity is full",
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
        if session.metadata.api == crate::client::config::CHAT_COMPLETIONS_API
            && !message.images.is_empty()
        {
            let _ = reply.send(Err(RuntimeError::new(
                "invalid_request",
                "images are unsupported by this API",
            )
            .session(&session_id)));
            return;
        }
        if let Err(error) =
            resolve_connection(&session.metadata, &self.client, self.connections.as_deref())
        {
            let _ = reply.send(Err(error.session(&session_id)));
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
            connection_identity: session.metadata.connection_identity.clone(),
            execution_connection_id: self
                .decorate_config(ConfigView::from_metadata(
                    &session.metadata,
                    session.system_prompt.clone(),
                ))
                .effective_config
                .execution_connection_id,
            system_prompt: Some(session.system_prompt.clone()),
            tool_selection: Some(
                session
                    .metadata
                    .tool_selection
                    .clone()
                    .unwrap_or_else(config::default_tool_selection),
            ),
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
        let (started_tx, started) = watch::channel(false);
        let agent = agent
            .with_event_sink(sink)
            .with_cancel(cancel_rx)
            .with_started_signal(started_tx);
        let (terminal, finished) = oneshot::channel();
        let association = Association {
            session_id,
            task_id: task_id.clone(),
            message_id: message_id.clone(),
        };
        let worker_task_id = task_id.clone();
        let worker_message_id = message_id.clone();
        #[cfg(test)]
        let should_panic = self.panicking_sessions.remove(&association.session_id);
        let config_revision = configuration.config_revision;
        let worker = tokio::spawn(async move {
            let mut agent = agent;
            #[cfg(test)]
            if should_panic {
                panic!("injected task worker panic");
            }
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
        self.active.insert(
            task_id.clone(),
            Active {
                association: association.clone(),
                cancel,
                terminal: Some(terminal),
                sink: Some(history_sink),
                result: None,
                started,
                config_revision,
            },
        );
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
        let Some(mut active) = self.active.remove(&done.task_id) else {
            return;
        };
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
        let finished = TaskFinished {
            status: finished_status,
            error,
            persistence,
            history_required,
        };
        active.result = Some(finished.clone());
        if let Some(terminal) = active.terminal.take() {
            let _ = terminal.send(finished);
        }
        if !self.output.is_closed() {
            self.active.insert(done.task_id, active);
        }
    }

    fn apply(&mut self, prepared: Prepared) -> std::result::Result<RuntimeReply, RuntimeError> {
        match prepared {
            Prepared::TaskStatus(state) => Ok(RuntimeReply::TaskStatus(state)),
            Prepared::Configuration(view) => Ok(RuntimeReply::Config(self.decorate_config(view))),
            Prepared::Configured {
                session_id,
                metadata,
                selection,
                client,
                registry,
                prompt,
            } => {
                if let Some(session) = self.sessions.get_mut(&session_id) {
                    let agent = session
                        .agent
                        .as_mut()
                        .expect("configuration holds the idle agent");
                    agent.apply_execution_config(
                        client,
                        selection.model,
                        selection.compaction,
                        prompt.clone(),
                        registry.expect("loaded configuration prepares its tool registry"),
                    );
                    session.system_prompt = prompt.clone();
                    session.metadata = metadata.clone();
                }
                let view = self.decorate_config(ConfigView::from_metadata(&metadata, prompt));
                Ok(RuntimeReply::ConfigUpdated {
                    config: view,
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
            .active_for_session(session_id)
            .filter(|active| active.result.is_none());
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
            self.sessions.get(session_id).map(|session| {
                self.decorate_config(ConfigView::from_metadata(
                    metadata,
                    session.system_prompt.clone(),
                ))
            })
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
            .active_for_session(&id)
            .filter(|active| active.result.is_none());
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
            .active_for_session(session_id)
            .filter(|active| active.result.is_none());
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

fn replayed_task_state(session_id: String, task_id: String, replay: Replay) -> TaskState {
    let mut known = false;
    let mut status = None;
    let mut config_revision = None;
    for record in replay.history {
        match record {
            JournalRecord::TaskStarted {
                task_id: id,
                configuration,
                ..
            } if id == task_id => {
                known = true;
                config_revision = configuration.map(|config| config.config_revision);
            }
            JournalRecord::TaskFinished {
                task_id: id,
                status: finished,
                ..
            } if id == task_id => status = Some(status_str(&finished)),
            _ => {}
        }
    }
    TaskState {
        session_id,
        task_id,
        status: status.unwrap_or("unknown").to_owned(),
        phase: if status.is_some() {
            "finished"
        } else if known {
            "recovery"
        } else {
            "unknown"
        },
        cancel_requested: false,
        config_revision,
        persistence: (known && status.is_none()).then_some("unconfirmed"),
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

pub(crate) fn io_error(error: anyhow::Error) -> RuntimeError {
    let code = if error.downcast_ref::<super::store::MetadataBusy>().is_some() {
        "locked"
    } else if error
        .downcast_ref::<super::store::CommitUnknown>()
        .is_some()
    {
        "commit_unknown"
    } else if error.chain().any(|cause| {
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
    Ok(metadata)
}

fn replay_for(store: &SessionStore, session_id: &str) -> std::result::Result<Replay, RuntimeError> {
    metadata_for(store, session_id)?;
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
    let metadata = metadata_for(&store, &session_id)?;
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
#[path = "../../tests/session/concurrent.rs"]
mod concurrent_tests;

#[cfg(test)]
#[path = "../../tests/session/context.rs"]
mod context_tests;

#[cfg(test)]
#[path = "../../tests/session/connections.rs"]
mod connection_tests;
