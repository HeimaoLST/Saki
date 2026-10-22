pub mod config;
pub mod host;
pub mod runtime;
pub mod stdio;
pub mod store;

use anyhow::{Result, bail};
use std::path::PathBuf;
use tokio::sync::mpsc::{self, UnboundedReceiver, UnboundedSender};

use crate::{
    agent::{
        Agent, AgentEvent, conversation::CompactionConfig, journal::Replay, message::UserMessage,
    },
    client::ModelClient,
    protocol::{InputItem, InputMessageContent, MessageRole, ModelRequest, OutputItem, OutputText},
    tools::{ToolContext, register_builtins, tool_registry::ToolRegistry},
};
use store::{LoadedSession, SessionMetadata, SessionStore, SessionSummary};

fn build_agent(store: &SessionStore, client: &ModelClient, loaded: LoadedSession) -> Result<Agent> {
    // The API match was checked by `SessionStore::open_for_api` before the
    // history was restored; every open path goes through that entry.
    let conversation = loaded
        .conversation
        .with_compaction_config(loaded.metadata.compaction)?;
    let mut conversation = conversation;
    if let Some(prompt) = &loaded.metadata.system_prompt {
        conversation.set_system_prompt(prompt.clone());
    }
    let registry = build_tool_registry(store, &loaded.metadata)?;
    Ok(Agent::new(client.clone(), registry, conversation).with_model(loaded.metadata.model))
}

fn build_tool_registry(store: &SessionStore, metadata: &SessionMetadata) -> Result<ToolRegistry> {
    let mut registry = ToolRegistry::new();
    register_builtins(&mut registry, tool_context(store, metadata)?);
    if let Some(selection) = &metadata.tool_selection {
        let selection = config::validate_tool_selection(selection)?;
        registry.retain_names(&selection.builtin);
    }
    Ok(registry)
}

fn tool_context(store: &SessionStore, metadata: &SessionMetadata) -> Result<ToolContext> {
    Ok(ToolContext {
        cwd: metadata.working_directory.clone(),
        output_dir: store.directory(&metadata.id)?.join("tool-output"),
    })
}

pub const SYSTEM_PROMPT: &str = "You are Saki, an agent that can use tools. Respond in English unless the user requests another language.";

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

/// Result of the one-shot background title job.
struct TitleJobResult {
    session_id: String,
    title: Option<String>,
}

/// Longest excerpt from each side of the first exchange given to the title job.
const TITLE_INPUT_CHARS: usize = 2000;
/// Upper bound on a stored generated title.
const TITLE_MAX_CHARS: usize = 60;

fn truncate_chars(text: &str, max: usize) -> String {
    text.chars().take(max).collect()
}

/// Sanitizes the model's reply into a one-line title, or `None` when it is
/// empty, unparsable, or just the default title.
fn title_from_output(output: &[OutputItem]) -> Option<String> {
    let text = output.iter().find_map(|item| match item {
        OutputItem::Message { content, .. } => {
            let joined: String = content
                .iter()
                .map(|OutputText { text }| text.as_str())
                .collect();
            (!joined.trim().is_empty()).then_some(joined)
        }
        _ => None,
    })?;
    let line = text.lines().map(str::trim).find(|line| !line.is_empty())?;
    let collapsed = line
        .split_whitespace()
        .collect::<Vec<_>>()
        .join(" ")
        .trim_matches(['"', '\'', '`', '*', '#'])
        .trim()
        .to_owned();
    let title: String = collapsed.chars().take(TITLE_MAX_CHARS).collect();
    let title = title.trim_matches(['"', '\'', '`']).trim();
    (!title.is_empty() && title != store::DEFAULT_TITLE).then(|| title.to_owned())
}

fn remember_reply(reply: &mut String, event: &AgentEvent) {
    if let AgentEvent::ResponseText { text, .. } = event {
        let remaining = TITLE_INPUT_CHARS.saturating_sub(reply.chars().count());
        if remaining > 0 {
            reply.extend(text.chars().take(remaining));
            reply.push('\n');
        }
    }
}

pub struct SessionManager {
    store: SessionStore,
    client: ModelClient,
    default_model: String,
    cwd: PathBuf,
    config: CompactionConfig,
    metadata: SessionMetadata,
    agent: Option<Agent>,
    initial_replay: Option<Replay>,
    /// Owns the one-shot title jobs and reaps them; dropping aborts leftovers.
    title_jobs: tokio::task::JoinSet<TitleJobResult>,
}

impl SessionManager {
    pub fn new(
        store: SessionStore,
        client: ModelClient,
        loaded: LoadedSession,
        cwd: PathBuf,
        default_model: String,
        config: CompactionConfig,
    ) -> Result<Self> {
        let metadata = loaded.metadata.clone();
        let mut manager = Self {
            store,
            client,
            default_model,
            cwd,
            config,
            metadata,
            agent: None,
            initial_replay: Some(loaded.replay.clone()),
            title_jobs: tokio::task::JoinSet::new(),
        };
        manager.agent = Some(manager.build_agent(loaded)?);
        Ok(manager)
    }

    fn build_agent(&self, loaded: LoadedSession) -> Result<Agent> {
        build_agent(&self.store, &self.client, loaded)
    }

    fn opened(&self, replay: Replay) -> SessionEvent {
        SessionEvent::Opened {
            metadata: self.metadata.clone(),
            replay,
            context: self.agent.as_ref().unwrap().context_event(),
        }
    }

    fn control(&mut self, command: SessionCommand) -> Result<SessionEvent> {
        match command {
            SessionCommand::List => Ok(SessionEvent::Listed(self.store.list()?)),
            SessionCommand::Rename { session_id, title } => {
                let metadata = self.store.rename(&session_id, &title)?;
                if session_id == self.metadata.id {
                    self.metadata = metadata.clone();
                }
                Ok(SessionEvent::Renamed(metadata))
            }
            SessionCommand::Create => {
                if self.agent.is_none() {
                    bail!("Agent is busy. Wait for the current task to finish.");
                }
                let id = self.store.create_with_api(
                    &self.cwd,
                    &self.default_model,
                    SYSTEM_PROMPT,
                    self.client.api().as_str(),
                    self.config,
                )?;
                self.open(&id)
            }
            SessionCommand::Open { session_id } => {
                if self.agent.is_none() {
                    bail!("Agent is busy. Wait for the current task to finish.");
                }
                self.open(&session_id)
            }
            SessionCommand::Send { .. } => {
                bail!("Agent is busy. Wait for the current task to finish.")
            }
            SessionCommand::Cancel { .. } => bail!("No active task to cancel."),
        }
    }

    fn open(&mut self, id: &str) -> Result<SessionEvent> {
        if id == self.metadata.id {
            bail!("This session is already open.");
        }
        anyhow::ensure!(
            self.store.metadata(id)?.schema_version == 1,
            "enhanced sessions require the Runtime service (#109)"
        );
        let loaded = self.store.open_for_api(id, self.client.api().as_str())?;
        let metadata = loaded.metadata.clone();
        let replay = loaded.replay.clone();
        let agent = self.build_agent(loaded)?;
        self.agent = Some(agent);
        self.metadata = metadata;
        Ok(self.opened(replay))
    }

    fn spawn_title_job(&mut self, user_text: &str, reply_text: &str) {
        let session_id = self.metadata.id.clone();
        let client = self.client.clone();
        let model = self.metadata.model.clone();
        let prompt = format!(
            "User: {}\n\nAssistant: {}",
            truncate_chars(user_text, TITLE_INPUT_CHARS),
            truncate_chars(reply_text, TITLE_INPUT_CHARS),
        );
        self.title_jobs.spawn(async move {
            let items = [
                InputItem::Message {
                    role: MessageRole::System,
                    content: InputMessageContent::Text(
                        "You write concise English conversation titles. Reply with only the title: \
                         one line, at most 8 words, plain text, no quotes, no trailing punctuation."
                            .to_owned(),
                    ),
                },
                InputItem::Message {
                    role: MessageRole::User,
                    content: InputMessageContent::Text(prompt),
                },
            ];
            // Independent output budget: reasoning models may spend tokens on
            // reasoning before producing the short visible title.
            let request = ModelRequest::new(model, &items, Vec::new()).with_generation(
                Some(1024),
                None,
                None,
            );
            let title = match tokio::time::timeout(
                std::time::Duration::from_secs(30),
                client.respond(request),
            )
            .await
            {
                Ok(Ok(response)) => title_from_output(&response.output),
                _ => None,
            };
            TitleJobResult { session_id, title }
        });
    }

    /// Starts at most one title job per session. Eligibility is persisted as a
    /// `pending` provenance at creation; dispatching claims it (`auto`) before
    /// the job runs, so failures and restarts never retry and a manual rename
    /// always wins.
    fn maybe_schedule_title(&mut self, session_id: &str, user_text: &str, reply_text: &str) {
        if self.metadata.id != session_id
            || self.metadata.title_source.as_deref() != Some("pending")
        {
            return;
        }
        match self.store.claim_auto_title(session_id) {
            Ok(true) => {
                self.metadata.title_source = Some("auto".to_owned());
                self.spawn_title_job(user_text, reply_text);
            }
            Ok(false) => {}
            Err(_) => {}
        }
    }

    /// A job result never fails the task; a store error just leaves the title
    /// untouched, and a manual rename always wins through the store condition.
    fn apply_title(&mut self, result: TitleJobResult, output: &UnboundedSender<SessionEvent>) {
        let Some(title) = result.title else {
            return;
        };
        if let Ok(Some(metadata)) = self.store.apply_auto_title(&result.session_id, &title) {
            if metadata.id == self.metadata.id {
                self.metadata = metadata.clone();
            }
            let _ = output.send(SessionEvent::TitleUpdated(metadata));
        }
    }

    /// A single active job; control requests are handled while generation runs.
    /// Closing the command channel allows the current job to finish before exit.
    pub async fn run(
        mut self,
        mut input: UnboundedReceiver<SessionCommand>,
        output: UnboundedSender<SessionEvent>,
    ) {
        let replay = self.initial_replay.take().unwrap();
        let _ = output.send(self.opened(replay));
        loop {
            let command = tokio::select! {
                result = self.title_jobs.join_next(), if !self.title_jobs.is_empty() => {
                    if let Some(Ok(result)) = result {
                        self.apply_title(result, &output);
                    }
                    continue;
                }
                command = input.recv() => {
                    let Some(command) = command else { break; };
                    command
                }
            };
            if matches!(command, SessionCommand::Cancel { .. }) {
                continue;
            }
            let SessionCommand::Send {
                session_id,
                message,
            } = command
            else {
                let event = self
                    .control(command)
                    .unwrap_or_else(|error| SessionEvent::Error {
                        message: format!("{error:#}"),
                    });
                let _ = output.send(event);
                continue;
            };
            if session_id != self.metadata.id {
                let _ = output.send(SessionEvent::Error {
                    message: "The selected session has changed. Message was not sent.".to_owned(),
                });
                continue;
            }
            let user_text = truncate_chars(&message.text, TITLE_INPUT_CHARS);
            let mut reply_text = String::new();
            let (agent_tx, mut agent_rx) = mpsc::unbounded_channel();
            let (cancel_tx, cancel_rx) = tokio::sync::watch::channel(false);
            let mut agent = self
                .agent
                .take()
                .unwrap()
                .with_events(agent_tx)
                .with_cancel(cancel_rx);
            let execution = async move {
                let result = agent.handle_user_message(message).await;
                (agent, result)
            };
            tokio::pin!(execution);
            let mut input_closed = false;
            loop {
                tokio::select! {
                    result = &mut execution => {
                        let (agent, result) = result;
                        while let Ok(event) = agent_rx.try_recv() {
                            remember_reply(&mut reply_text, &event);
                            let _ = output.send(SessionEvent::Agent { session_id: session_id.clone(), event });
                        }
                        self.agent = Some(agent);
                        let completed = result.is_ok();
                        let event = match result {
                            Ok(()) => AgentEvent::TurnCompleted,
                            Err(error) if crate::agent::is_interrupted(&error) => {
                                AgentEvent::TurnInterrupted
                            }
                            Err(error) => AgentEvent::TurnFailed { message: format!("{error:#}") },
                        };
                        let _ = output.send(SessionEvent::Agent { session_id: session_id.clone(), event });
                        if completed {
                            self.maybe_schedule_title(&session_id, &user_text, &reply_text);
                        }
                        break;
                    }
                    Some(event) = agent_rx.recv() => {
                        remember_reply(&mut reply_text, &event);
                        let _ = output.send(SessionEvent::Agent { session_id: session_id.clone(), event });
                    }
                    result = self.title_jobs.join_next(), if !self.title_jobs.is_empty() => {
                        if let Some(Ok(result)) = result {
                            self.apply_title(result, &output);
                        }
                    }
                    command = input.recv(), if !input_closed => {
                        if let Some(command) = command {
                            if let SessionCommand::Cancel { session_id: id } = &command {
                                if id == &session_id {
                                    let _ = cancel_tx.send(true);
                                }
                            } else {
                                let event = self.control(command).unwrap_or_else(|error| SessionEvent::Error { message: format!("{error:#}") });
                                let _ = output.send(event);
                            }
                        } else { input_closed = true; }
                    }
                }
            }
            if input_closed {
                break;
            }
        }
    }
}

#[cfg(test)]
#[path = "../../tests/session/mod.rs"]
mod tests;
