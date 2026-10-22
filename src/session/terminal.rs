#[cfg(test)]
use super::store::{LoadedSession, SessionStore};
use super::{
    config::{ConfigPatch, ConfigView},
    runtime::{self, Runtime, RuntimeCommand, RuntimeHandle, RuntimeReply, TaskEvent},
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
use std::path::PathBuf;
use tokio::{
    sync::{
        broadcast,
        mpsc::{UnboundedReceiver, UnboundedSender},
    },
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
    },
}

pub enum SessionEvent {
    Opened {
        metadata: SessionMetadata,
        replay: Replay,
        context: AgentEvent,
    },
    Listed(Vec<SessionSummary>),
    Renamed(SessionMetadata),
    TitleUpdated(SessionMetadata),
    Agent {
        session_id: String,
        event: AgentEvent,
    },
    Error {
        message: String,
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

    pub async fn run(
        self,
        mut input: UnboundedReceiver<SessionCommand>,
        output: UnboundedSender<SessionEvent>,
    ) -> Result<()> {
        let (handle, mut actor) = self.runtime.start();
        let mut metadata = handle.subscribe_metadata();
        let mut operations: CommandJobs = JoinSet::new();
        let mut forwarders = JoinSet::new();
        let mut input_closed = false;
        let mut stopping = false;
        let initial = match self.initial_session {
            Some(session_id) => SessionCommand::Open { session_id },
            None => SessionCommand::Create,
        };
        match execute_command(&handle, initial).await {
            Ok(CommandReply::Event(event)) => emit(&handle, &output, *event),
            Ok(CommandReply::Task(_)) => unreachable!("startup only creates or loads a session"),
            Err(error) => {
                emit(
                    &handle,
                    &output,
                    SessionEvent::Error {
                        message: display_error(&error),
                    },
                );
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
                        let handle=handle.clone();let output=output.clone();
                        forwarders.spawn(async move {
                            forward_task(handle,output,*task).await
                        });
                    }
                }
                Some(result)=forwarders.join_next(),if !forwarders.is_empty()=>{result??;}
                update=metadata.recv(),if operations.len()<8 && !stopping=>{
                    match update {
                        Ok(update)=>{
                            let handle=handle.clone();let output=output.clone();
                            operations.spawn(async move {
                                match metadata_for_display(&handle,&update.session_id).await {
                                    Ok(metadata)=>emit(&handle,&output,SessionEvent::TitleUpdated(metadata)),
                                    Err(error)=>emit(&handle,&output,SessionEvent::Error{message:format!("{error:#}")}),
                                }
                                Ok(None)
                            });
                        }
                        Err(broadcast::error::RecvError::Lagged(_))=>spawn_command(&mut operations,handle.clone(),output.clone(),SessionCommand::List),
                        Err(broadcast::error::RecvError::Closed)=>{}
                    }
                }
                command=input.recv(),if !input_closed=>{
                    match command {
                        Some(SessionCommand::Cancel{session_id})=>{
                            // Cancellation uses authoritative state, including acceptance
                            // before the task's first display event reaches the terminal.
                            if let Ok(RuntimeReply::Session(state))=handle.call(RuntimeCommand::Status{session_id:session_id.clone()}).await
                                && let Some(task)=state.active_task
                            {
                                let _=handle.call(RuntimeCommand::Cancel{session_id,task_id:task.task_id}).await;
                            }
                        }
                        Some(command) if operations.len()<8=>spawn_command(&mut operations,handle.clone(),output.clone(),command),
                        Some(_)=>emit(&handle,&output,SessionEvent::Error{message:"Too many terminal operations are being prepared.".into()}),
                        None=>input_closed=true,
                    }
                }
                _=output.closed(),if !input_closed=>{handle.output_state().close();input_closed=true;}
            }
        }
    }
}

type CommandJobs = JoinSet<Result<Option<Box<runtime::AcceptedTask>>>>;

fn emit(handle: &RuntimeHandle, output: &UnboundedSender<SessionEvent>, event: SessionEvent) {
    if output.send(event).is_err() {
        handle.output_state().close();
    }
}

fn spawn_command(
    jobs: &mut CommandJobs,
    handle: RuntimeHandle,
    output: UnboundedSender<SessionEvent>,
    command: SessionCommand,
) {
    jobs.spawn(async move {
        match execute_command(&handle, command).await {
            Ok(CommandReply::Task(task)) => Ok(Some(task)),
            Ok(CommandReply::Event(event)) => {
                emit(&handle, &output, *event);
                Ok(None)
            }
            Err(error) => {
                emit(
                    &handle,
                    &output,
                    SessionEvent::Error {
                        message: display_error(&error),
                    },
                );
                Ok(None)
            }
        }
    });
}

enum CommandReply {
    Event(Box<SessionEvent>),
    Task(Box<runtime::AcceptedTask>),
}

async fn execute_command(handle: &RuntimeHandle, command: SessionCommand) -> Result<CommandReply> {
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
            assert_idle_terminal(handle).await?;
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
            snapshot(handle, &state.session_id).await?
        }
        SessionCommand::Open { session_id } => {
            assert_idle_terminal(handle).await?;
            handle
                .call(RuntimeCommand::Load {
                    session_id: session_id.clone(),
                })
                .await?;
            snapshot(handle, &session_id).await?
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
            unreachable!("cancellation stays on the independent control path")
        }
    };
    Ok(CommandReply::Event(Box::new(reply)))
}

fn display_error(error: &anyhow::Error) -> String {
    if let Some(error) = error.downcast_ref::<runtime::RuntimeError>() {
        format!("{}: {}", error.code, error.message)
    } else {
        format!("{error:#}")
    }
}

async fn assert_idle_terminal(handle: &RuntimeHandle) -> Result<()> {
    if let RuntimeReply::List { sessions } = handle.call(RuntimeCommand::List).await?
        && sessions.iter().any(|session| session.state == "running")
    {
        bail!("Agent is busy. Wait for the current task to finish.");
    }
    Ok(())
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

async fn snapshot(handle: &RuntimeHandle, id: &str) -> Result<SessionEvent> {
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
    let replay = journal::replay_records(records)?;
    let context = context_for_display(&replay, &config)?;
    Ok(SessionEvent::Opened {
        metadata,
        replay,
        context,
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
    output: UnboundedSender<SessionEvent>,
    mut task: runtime::AcceptedTask,
) -> Result<()> {
    task.events.defer_terminal_ack();
    while let Some(event) = task.events.next().await {
        let event = match event {
            TaskEvent::Event(packet) => packet.event,
            TaskEvent::Finished(finished) => {
                let event = match finished.status {
                    "completed" => AgentEvent::TurnCompleted,
                    "interrupted" => AgentEvent::TurnInterrupted,
                    _ => AgentEvent::TurnFailed {
                        message: finished
                            .error
                            .map(|error| error.to_string())
                            .unwrap_or_else(|| "Task failed.".into()),
                    },
                };
                emit(
                    &handle,
                    &output,
                    SessionEvent::Agent {
                        session_id: task.association.session_id.clone(),
                        event,
                    },
                );
                task.events.acknowledge_finished();
                continue;
            }
        };
        emit(
            &handle,
            &output,
            SessionEvent::Agent {
                session_id: task.association.session_id.clone(),
                event,
            },
        );
    }
    Ok(())
}
