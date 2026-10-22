#[cfg(test)]
use super::store::{LoadedSession, SessionStore};
use super::{
    SessionEventReceiver, SessionEventSender, channel,
    config::{ConfigPatch, ConfigView},
    runtime::{
        self, Association, Runtime, RuntimeCommand, RuntimeError, RuntimeHandle, RuntimeReply,
        SessionState, TaskEvent, TaskFinished,
    },
    store::{SessionMetadata, SessionSummary},
};
#[cfg(test)]
use crate::{agent::conversation::CompactionConfig, client::ModelClient};
use crate::{
    agent::{
        AgentEvent,
        conversation::Conversation,
        journal::{self, Replay},
        message::UserMessage,
    },
    tools::{ToolContext, register_builtins, tool_registry::ToolRegistry},
};
use anyhow::{Context, Result, bail};
use std::{collections::HashMap, path::PathBuf, sync::Arc};
use tokio::{
    sync::{Mutex, broadcast, mpsc::Receiver},
    task::JoinSet,
};

pub enum SessionCommand {
    Send {
        session_id: String,
        message: UserMessage,
    },
    List,
    Create,
    Open {
        session_id: String,
    },
    Rename {
        session_id: String,
        title: String,
    },
    Cancel {
        session_id: String,
        task_id: String,
    },
}

pub struct SessionRequest {
    pub request_id: u64,
    pub command: SessionCommand,
}

pub enum SessionEvent {
    Opened {
        metadata: SessionMetadata,
        replay: Replay,
        context: AgentEvent,
        state: SessionState,
        stream: Option<Association>,
    },
    HistoryRestored {
        metadata: SessionMetadata,
        replay: Replay,
        context: AgentEvent,
        state: SessionState,
    },
    Listed(Vec<SessionSummary>),
    Renamed(SessionMetadata),
    TitleUpdated(SessionMetadata),
    Agent {
        session_id: String,
        event: AgentEvent,
    },
    Accepted,
    Finished(TaskFinished),
    Cancelled {
        status: &'static str,
    },
    Error {
        operation: &'static str,
        error: RuntimeError,
    },
}

/// Terminal command/event adapter. Runtime alone owns sessions and execution.
pub struct SessionManager {
    runtime: Runtime,
    initial_session: Option<String>,
}

impl SessionManager {
    #[cfg(test)]
    pub fn new(
        store: SessionStore,
        client: ModelClient,
        loaded: LoadedSession,
        cwd: PathBuf,
        default_model: String,
        config: CompactionConfig,
    ) -> Result<Self> {
        let initial_session = Some(loaded.metadata.id.clone());
        drop(loaded);
        Ok(Self::from_runtime(
            Runtime::new(store, client, cwd, default_model, config),
            initial_session,
        ))
    }

    pub fn from_runtime(runtime: Runtime, initial_session: Option<String>) -> Self {
        Self {
            runtime: runtime.with_automatic_titles(true),
            initial_session,
        }
    }

    pub fn event_channel(&self) -> (SessionEventSender, SessionEventReceiver) {
        channel::channel(self.runtime.output_state())
    }

    pub async fn run(
        self,
        mut input: Receiver<SessionRequest>,
        output: SessionEventSender,
    ) -> Result<()> {
        let (handle, mut actor) = self.runtime.start();
        let mut metadata = handle.subscribe_metadata();
        let mut operations: CommandJobs = JoinSet::new();
        let mut forwarders = JoinSet::new();
        let delivery = Arc::new(Mutex::new(HashMap::new()));
        let mut input_closed = false;
        let mut stopping = false;
        let initial = match self.initial_session {
            Some(session_id) => SessionCommand::Open { session_id },
            None => SessionCommand::Create,
        };
        match execute_command(&handle, initial, &*delivery.lock().await).await {
            Ok(CommandReply::Event(event)) => {
                let _ = output.send(*event).await;
            }
            Ok(CommandReply::Task(_)) => unreachable!("startup only creates or loads a session"),
            Err(error) => {
                let _ = output.send(error_event("startup", None, &error)).await;
                let _ = handle.call(RuntimeCommand::Shutdown).await;
                actor.await??;
                return Err(error);
            }
        }

        loop {
            if input_closed && operations.is_empty() && !stopping {
                let _ = handle.call(RuntimeCommand::Shutdown).await;
                stopping = true;
            }
            tokio::select! {
                biased;
                result=&mut actor=>{
                    while let Some(result)=operations.join_next().await {result??;}
                    while let Some(result)=forwarders.join_next().await {result??;}
                    result??;return Ok(());
                }
                Some(result)=operations.join_next(),if !operations.is_empty()=>{
                    if let Some(task)=result?? {
                        let handle=handle.clone();let output=output.clone();let delivery=delivery.clone();
                        forwarders.spawn(async move {forward_task(handle,output,*task,delivery).await});
                    }
                }
                Some(result)=forwarders.join_next(),if !forwarders.is_empty()=>{result??;}
                update=metadata.recv(),if operations.len()<8 && !stopping=>{
                    match update {
                        Ok(update)=>spawn_metadata(&mut operations,handle.clone(),output.clone(),delivery.clone(),Some(update.session_id)),
                        Err(broadcast::error::RecvError::Lagged(_))=>spawn_metadata(&mut operations,handle.clone(),output.clone(),delivery.clone(),None),
                        Err(broadcast::error::RecvError::Closed)=>{}
                    }
                }
                request=input.recv(),if !input_closed=>{
                    match request {
                        Some(SessionRequest{request_id,command:SessionCommand::Cancel{session_id,task_id}})=>{
                            let event=match handle.call(RuntimeCommand::Cancel{session_id:session_id.clone(),task_id}).await {
                                Ok(RuntimeReply::Cancelled{status})=>SessionEvent::Cancelled{status},
                                Ok(_)=>error_event("cancel",Some(&session_id),&anyhow::anyhow!("unexpected cancel reply")),
                                Err(error)=>SessionEvent::Error{operation:"cancel",error},
                            };
                            if output.try_send_reply(request_id,event).is_err(){output.state().close();}
                        }
                        Some(request) if operations.len()<8=>spawn_command(&mut operations,handle.clone(),output.clone(),delivery.clone(),request),
                        Some(request)=>{
                            let mut error=RuntimeError::new("busy","too many terminal operations are being prepared");
                            error.session_id=request.command.session_id().map(str::to_owned);
                            let event=SessionEvent::Error{operation:request.command.operation(),error};
                            if output.try_send_reply(request.request_id,event).is_err(){output.state().close();}
                        }
                        None=>input_closed=true,
                    }
                }
                _=output.closed(),if !input_closed=>{input_closed=true;}
            }
        }
    }
}

// This map tracks undelivered display streams, not business task state. The
// gate orders snapshots with Accepted/Finished without blocking cancellation.
type Delivery = Arc<Mutex<HashMap<String, Association>>>;
type CommandJobs = JoinSet<Result<Option<Box<runtime::AcceptedTask>>>>;

impl SessionCommand {
    fn operation(&self) -> &'static str {
        match self {
            Self::Send { .. } => "send",
            Self::List => "list",
            Self::Create => "create",
            Self::Open { .. } => "open",
            Self::Rename { .. } => "rename",
            Self::Cancel { .. } => "cancel",
        }
    }

    fn session_id(&self) -> Option<&str> {
        match self {
            Self::Send { session_id, .. }
            | Self::Open { session_id }
            | Self::Rename { session_id, .. }
            | Self::Cancel { session_id, .. } => Some(session_id),
            Self::List | Self::Create => None,
        }
    }
}

fn error_event(
    operation: &'static str,
    session_id: Option<&str>,
    error: &anyhow::Error,
) -> SessionEvent {
    let mut error = error
        .downcast_ref::<RuntimeError>()
        .cloned()
        .unwrap_or_else(|| RuntimeError::new("io_error", format!("{error:#}")));
    if error.session_id.is_none() {
        error.session_id = session_id.map(str::to_owned);
    }
    SessionEvent::Error { operation, error }
}

fn spawn_metadata(
    jobs: &mut CommandJobs,
    handle: RuntimeHandle,
    output: SessionEventSender,
    delivery: Delivery,
    id: Option<String>,
) {
    jobs.spawn(async move {
        let _gate = delivery.lock().await;
        match handle.call(RuntimeCommand::List).await {
            Ok(RuntimeReply::List { sessions }) => {
                for session in sessions {
                    if id.as_ref().is_none_or(|id| *id == session.metadata.id) {
                        let _ = output
                            .send(SessionEvent::TitleUpdated(session.metadata))
                            .await;
                    }
                }
            }
            Ok(_) => {}
            Err(error) => {
                let _ = output
                    .send(SessionEvent::Error {
                        operation: "metadata",
                        error,
                    })
                    .await;
            }
        }
        Ok(None)
    });
}

fn spawn_command(
    jobs: &mut CommandJobs,
    handle: RuntimeHandle,
    output: SessionEventSender,
    delivery: Delivery,
    request: SessionRequest,
) {
    jobs.spawn(async move {
        let operation = request.command.operation();
        let session_id = request.command.session_id().map(str::to_owned);
        let mut streams = delivery.lock().await;
        match execute_command(&handle, request.command, &streams).await {
            Ok(CommandReply::Task(task)) => {
                streams.insert(
                    task.association.session_id.clone(),
                    task.association.clone(),
                );
                let _ = output
                    .send_reply(
                        request.request_id,
                        Some(task.association.clone()),
                        SessionEvent::Accepted,
                    )
                    .await;
                Ok(Some(task))
            }
            Ok(CommandReply::Event(event)) => {
                let _ = output.send_reply(request.request_id, None, *event).await;
                Ok(None)
            }
            Err(error) => {
                let _ = output
                    .send_reply(
                        request.request_id,
                        None,
                        error_event(operation, session_id.as_deref(), &error),
                    )
                    .await;
                Ok(None)
            }
        }
    });
}

enum CommandReply {
    Event(Box<SessionEvent>),
    Task(Box<runtime::AcceptedTask>),
}

async fn execute_command(
    handle: &RuntimeHandle,
    command: SessionCommand,
    streams: &HashMap<String, Association>,
) -> Result<CommandReply> {
    let reply = match command {
        SessionCommand::Send {
            session_id,
            message,
        } => match handle
            .call(RuntimeCommand::Send {
                session_id,
                message,
            })
            .await?
        {
            RuntimeReply::Accepted(task) => return Ok(CommandReply::Task(Box::new(task))),
            _ => bail!("unexpected task reply"),
        },
        SessionCommand::Create => {
            let connections = match handle.call(RuntimeCommand::Connections).await? {
                RuntimeReply::Connections(connections) => connections.len(),
                _ => bail!("unexpected connections reply"),
            };
            let command = if connections > 1 {
                RuntimeCommand::CreateConfigured {
                    storage_schema: 2,
                    config: ConfigPatch::default(),
                }
            } else {
                RuntimeCommand::Create {
                    working_directory: None,
                    system_prompt: None,
                }
            };
            let state = match handle.call(command).await? {
                RuntimeReply::Session(state) => state,
                _ => bail!("unexpected create reply"),
            };
            snapshot(
                handle,
                &state.session_id,
                streams.get(&state.session_id).cloned(),
                false,
            )
            .await?
        }
        SessionCommand::Open { session_id } => {
            handle
                .call(RuntimeCommand::Load {
                    session_id: session_id.clone(),
                })
                .await?;
            snapshot(
                handle,
                &session_id,
                streams.get(&session_id).cloned(),
                false,
            )
            .await?
        }
        SessionCommand::List => match handle.call(RuntimeCommand::List).await? {
            RuntimeReply::List { sessions } => SessionEvent::Listed(
                sessions
                    .into_iter()
                    .map(|view| SessionSummary {
                        metadata: view.metadata,
                        updated_at: view.updated_at,
                        status: display_status(&view.status),
                        in_use: view.in_use,
                        unavailable: view.unavailable,
                    })
                    .collect(),
            ),
            _ => bail!("unexpected list reply"),
        },
        SessionCommand::Rename { session_id, title } => match handle
            .call(RuntimeCommand::Rename { session_id, title })
            .await?
        {
            RuntimeReply::Renamed { metadata, .. } => SessionEvent::Renamed(metadata),
            _ => bail!("unexpected rename reply"),
        },
        SessionCommand::Cancel { .. } => {
            unreachable!("cancellation uses the independent control path")
        }
    };
    Ok(CommandReply::Event(Box::new(reply)))
}

fn display_status(status: &str) -> journal::TaskStatus {
    match status {
        "completed" => journal::TaskStatus::Completed,
        "failed" => journal::TaskStatus::Failed,
        "interrupted" => journal::TaskStatus::Interrupted,
        _ => journal::TaskStatus::Unknown,
    }
}

async fn metadata_for_display(handle: &RuntimeHandle, id: &str) -> Result<SessionMetadata> {
    match handle.call(RuntimeCommand::List).await? {
        RuntimeReply::List { sessions } => sessions
            .into_iter()
            .find(|view| view.metadata.id == id)
            .map(|view| view.metadata)
            .context("session metadata is unavailable"),
        _ => bail!("unexpected metadata reply"),
    }
}

async fn snapshot(
    handle: &RuntimeHandle,
    id: &str,
    stream: Option<Association>,
    restore: bool,
) -> Result<SessionEvent> {
    let metadata = metadata_for_display(handle, id).await?;
    let config = match handle
        .call(RuntimeCommand::ConfigGet {
            session_id: id.into(),
        })
        .await?
    {
        RuntimeReply::Config(config) => config,
        _ => bail!("unexpected configuration reply"),
    };
    let mut records = Vec::new();
    let mut offset = 0;
    let mut size = crate::protocol::ByteCounter::new(8 * 1024 * 1024);
    loop {
        let page = match handle
            .call(RuntimeCommand::History {
                session_id: id.into(),
                offset,
                limit: 1000,
            })
            .await?
        {
            RuntimeReply::History(page) => page,
            _ => bail!("unexpected history reply"),
        };
        serde_json::to_writer(&mut size, &page.records)
            .context("terminal history exceeds its 8 MiB projection limit")?;
        records.extend(
            page.records
                .into_iter()
                .map(|record| (record.timestamp_ms, record.record)),
        );
        match page.next_offset {
            Some(next) => offset = next,
            None => break,
        }
    }
    if let Some(stream) = &stream {
        let boundary=records.iter().position(|(_,record)|matches!(record,journal::JournalRecord::TaskStarted{task_id,..} if task_id==&stream.task_id))
            .context("active display stream has no persisted task boundary")?;
        records.truncate(boundary);
    }
    let replay = journal::replay_records(records)?;
    let context = context_for_display(&replay, &config)?;
    let state = match handle
        .call(RuntimeCommand::Status {
            session_id: id.into(),
        })
        .await?
    {
        RuntimeReply::Session(state) => state,
        _ => bail!("unexpected status reply"),
    };
    Ok(if restore {
        SessionEvent::HistoryRestored {
            metadata,
            replay,
            context,
            state,
        }
    } else {
        SessionEvent::Opened {
            metadata,
            replay,
            context,
            state,
            stream,
        }
    })
}

fn context_for_display(replay: &Replay, config: &ConfigView) -> Result<AgentEvent> {
    let config = &config.effective_config;
    let mut conversation =
        Conversation::from_replay(replay).with_compaction_config(config.compaction)?;
    conversation.set_system_prompt(config.system_prompt.clone());
    let mut registry = ToolRegistry::new();
    register_builtins(
        &mut registry,
        ToolContext {
            cwd: config.working_directory.clone(),
            output_dir: PathBuf::new(),
        },
    );
    registry.retain_names(&config.tool_selection.builtin);
    let mut tools = registry.list_definition();
    tools.extend(config.tool_schemas.iter().map(|(name, schema)| {
        crate::protocol::ToolDefinition {
            name: name.clone(),
            tool_type: crate::protocol::ToolType::Function,
            description: schema.description.clone(),
            parameters: schema.input_schema.clone(),
        }
    }));
    Ok(AgentEvent::ContextUpdated {
        model: config.model.clone(),
        context_tokens: conversation
            .estimated_context_tokens(&config.model, &tools)
            .ok(),
        context_window: conversation.context_window(),
        input_limit: conversation.input_limit(),
    })
}

async fn forward_task(
    handle: RuntimeHandle,
    output: SessionEventSender,
    mut task: runtime::AcceptedTask,
    delivery: Delivery,
) -> Result<()> {
    task.events.defer_terminal_ack();
    while let Some(event) = task.events.next().await {
        let mut streams = delivery.lock().await;
        match event {
            TaskEvent::Event(packet) => {
                let _ = output.send_task(task.association.clone(), packet).await;
            }
            TaskEvent::Finished(finished) => {
                if finished.history_required && !output.state().is_closed() {
                    match snapshot(&handle, &task.association.session_id, None, true).await {
                        Ok(snapshot) => {
                            let _ = output
                                .send_associated(task.association.clone(), snapshot)
                                .await;
                        }
                        Err(error) => {
                            let _ = output
                                .send_associated(
                                    task.association.clone(),
                                    error_event(
                                        "history",
                                        Some(&task.association.session_id),
                                        &error,
                                    ),
                                )
                                .await;
                        }
                    }
                }
                let _ = output
                    .send_associated(task.association.clone(), SessionEvent::Finished(finished))
                    .await;
                streams.remove(&task.association.session_id);
                task.events.acknowledge_finished();
            }
        }
    }
    Ok(())
}

#[cfg(test)]
#[path = "../../tests/session/terminal_delivery.rs"]
mod tests;
