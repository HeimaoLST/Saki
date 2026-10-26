pub mod conversation;
pub mod events;
pub mod journal;
pub mod message;

use self::events::EventSink;
use serde::Serialize;
use serde_json::Value;
use std::sync::Arc;
#[cfg(test)]
use tokio::sync::mpsc::UnboundedReceiver;
use tokio::sync::mpsc::UnboundedSender;

use self::{conversation::Conversation, message::UserMessage};
use crate::{
    client::{ModelClient, responses::ApiError},
    protocol::{ModelRequest, ModelStreamEvent, OutputItem, OutputText},
    tools::tool_registry::{ToolCall, ToolRegistry},
};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ModelRequestPurpose {
    Generation,
    Compaction,
}

/// Display data emitted by the agent's single model/tool loop.
///
/// The agent never writes to the terminal. A UI adapter forwards these events
/// and keeps its own display history, so a compaction only adds an event and
/// never removes events that were already sent.
#[derive(Debug, Clone, PartialEq, Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum AgentEvent {
    /// Active context estimate and configured limits; `None` means unavailable.
    /// The estimate excludes unsent drafts and uses the same policy as compaction.
    ContextUpdated {
        model: String,
        context_tokens: Option<usize>,
        context_window: Option<usize>,
        input_limit: Option<usize>,
    },
    /// The start boundary was successfully written to the journal. The ids are
    /// the caller-visible ones and match the journal's TaskStarted record.
    TaskStarted {
        task_id: String,
        message_id: Option<String>,
    },
    /// A model request is about to be made. Compaction is a summary request;
    /// title generation is not a task and never emits this event.
    ModelRequestStarted {
        purpose: ModelRequestPurpose,
    },
    /// Assistant response text in the order it appeared in `response.output`,
    /// including the public explanation that precedes a tool call.
    ResponseText {
        item_id: String,
        text: String,
    },
    /// Display-only progress; finalized by ResponseText or ended by TurnFailed.
    ResponseTextDelta {
        item_id: String,
        delta: String,
    },
    /// Reasoning text returned with a response.
    Reasoning {
        item_id: String,
        text: String,
    },
    ReasoningDelta {
        item_id: String,
        delta: String,
    },
    /// One reasoning item ended; the model response may still be generating.
    ReasoningCompleted {
        item_id: String,
    },
    /// A tool call is about to execute. `arguments` is the raw JSON string, so
    /// the UI can show the original even when it cannot be parsed.
    ToolStarted {
        call_id: String,
        name: String,
        arguments: String,
    },
    /// A tool call finished and this is the raw result value, including
    /// argument, lookup and execution errors.
    ToolFinished {
        call_id: String,
        name: String,
        output: Value,
    },
    /// The conversation was compacted. Earlier display events stay valid.
    Compaction {
        tokens_before: usize,
        tokens_after: usize,
        /// Output tokens used by the summary request, when reported.
        summary_output_tokens: Option<u64>,
    },
    /// Usage reported for one model response.
    Usage {
        input_tokens: Option<u64>,
        output_tokens: Option<u64>,
    },
    /// Terminal event: the turn finished and the agent accepts the next message.
    /// Emitted once by the worker, not by the process loop.
    TurnCompleted,
    /// Terminal event: the turn was interrupted by an explicit cancellation.
    /// Emitted once by the worker at the `handle_user_message` return boundary.
    TurnInterrupted,
    /// Terminal event: the turn failed. Emitted once by the worker at the
    /// `handle_user_message` return boundary.
    TurnFailed {
        message: String,
    },
}

/// Typed interruption marker carried through `anyhow::Error`.
#[derive(Debug)]
pub struct Interrupted;

impl std::fmt::Display for Interrupted {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str("task was interrupted")
    }
}

impl std::error::Error for Interrupted {}

pub fn is_interrupted(error: &anyhow::Error) -> bool {
    error.downcast_ref::<Interrupted>().is_some()
}

fn interrupted() -> anyhow::Error {
    anyhow::Error::new(Interrupted)
}

fn is_cancelled(cancel: &Option<tokio::sync::watch::Receiver<bool>>) -> bool {
    cancel.as_ref().is_some_and(|receiver| *receiver.borrow())
}

/// Resolves only when the receiver's value becomes `true`. A dropped sender
/// (closed channel) stops further cancellation and waits forever here: dropping
/// the control handle must not cancel a running turn.
async fn cancelled(cancel: &mut Option<tokio::sync::watch::Receiver<bool>>) {
    match cancel {
        Some(receiver) => loop {
            if *receiver.borrow() {
                return;
            }
            if receiver.changed().await.is_err() {
                std::future::pending::<()>().await;
            }
        },
        None => std::future::pending().await,
    }
}

pub struct Agent {
    client: ModelClient,
    tool_registry: ToolRegistry,
    conversation: Conversation,
    model: String,
    started_signal: Option<tokio::sync::watch::Sender<bool>>,
    events: Option<EventSink>,
    cancel: Option<tokio::sync::watch::Receiver<bool>>,
}

impl Agent {
    pub fn new(
        client: ModelClient,
        tool_registry: ToolRegistry,
        conversation: Conversation,
    ) -> Self {
        Self {
            client,
            tool_registry,
            conversation,
            model: "deepseek-v4-flash".to_owned(),
            started_signal: None,
            events: None,
            cancel: None,
        }
    }

    /// Attach a per-turn cancellation receiver. Sending `true` interrupts at
    /// the next await boundary; sending `false` or dropping the sender does
    /// not cancel. Cancellation is cooperative and never aborts the agent's
    /// conversation state.
    pub fn with_cancel(mut self, cancel: tokio::sync::watch::Receiver<bool>) -> Self {
        self.cancel = Some(cancel);
        self
    }

    pub(crate) fn with_started_signal(mut self, signal: tokio::sync::watch::Sender<bool>) -> Self {
        self.started_signal = Some(signal);
        self
    }

    pub(crate) fn context_items(&self) -> &[crate::protocol::InputItem] {
        self.conversation.items()
    }

    pub(crate) fn apply_model_config(
        &mut self,
        model: String,
        config: conversation::ValidatedCompactionConfig,
    ) {
        self.model = model;
        self.conversation.apply_compaction_config(config);
    }

    pub fn with_model(mut self, model: String) -> Self {
        self.model = model;
        self
    }

    pub(crate) fn apply_execution_config(
        &mut self,
        client: ModelClient,
        model: String,
        config: conversation::ValidatedCompactionConfig,
        prompt: String,
        registry: ToolRegistry,
    ) {
        self.client = client;
        self.tool_registry = registry;
        self.conversation.set_system_prompt(prompt);
        self.apply_model_config(model, config);
    }

    /// Attach the event channel used to report display data to the UI.
    #[cfg_attr(
        not(test),
        expect(
            dead_code,
            reason = "direct SDK compatibility; production hosts use Runtime"
        )
    )]
    pub fn with_events(self, events: UnboundedSender<AgentEvent>) -> Self {
        self.with_event_sink(EventSink::legacy(events))
    }

    pub fn with_event_sink(mut self, events: EventSink) -> Self {
        let hook_events = events.clone();
        self.conversation.set_model_request_hook(Arc::new(move || {
            let events = hook_events.clone();
            Box::pin(async move {
                events
                    .send(AgentEvent::ModelRequestStarted {
                        purpose: ModelRequestPurpose::Compaction,
                    })
                    .await;
            })
        }));
        self.events = Some(events);
        self
    }

    pub(crate) fn replace_tools(&mut self, registry: ToolRegistry) {
        self.tool_registry = registry;
    }

    pub fn clear_event_sink(&mut self) {
        self.started_signal = None;
        self.events = None;
        self.conversation.clear_model_request_hook();
    }

    /// Send one display event. A closed channel means the viewer is gone, which
    /// must not interrupt the single model/tool loop.
    async fn emit(&self, event: AgentEvent) {
        if let Some(events) = &self.events {
            events.send(event).await;
        }
    }

    async fn emit_context(&self) {
        if self.events.is_some() {
            self.emit(self.context_event()).await;
        }
    }

    /// Process a user message through model and tool calls while preserving the conversation on errors.
    #[cfg_attr(
        not(test),
        expect(
            dead_code,
            reason = "direct SDK compatibility; production hosts use Runtime"
        )
    )]
    pub async fn handle_user_message(
        &mut self,
        message: impl Into<UserMessage>,
    ) -> anyhow::Result<()> {
        self.conversation.begin_task()?;
        let task_id = self
            .conversation
            .active_task_id()
            .map(str::to_owned)
            .ok_or_else(|| anyhow::anyhow!("task start did not establish an active task"))?;
        self.run_started_task(message.into(), task_id, None).await
    }

    /// Starts a task with caller-supplied ids. The task id is written to the
    /// journal before `TaskStarted` is emitted; terminal events still belong to
    /// the worker at the `handle_*` return boundary.
    #[cfg_attr(
        not(test),
        expect(dead_code, reason = "unconfigured task compatibility entry point")
    )]
    pub async fn handle_task(
        &mut self,
        message: UserMessage,
        task_id: String,
        message_id: String,
    ) -> anyhow::Result<()> {
        self.handle_configured_task(message, task_id, message_id, None)
            .await
    }

    pub async fn handle_configured_task(
        &mut self,
        message: UserMessage,
        task_id: String,
        message_id: String,
        configuration: Option<journal::TaskConfiguration>,
    ) -> anyhow::Result<()> {
        self.conversation.begin_configured_task(
            task_id.clone(),
            Some(message_id.clone()),
            configuration,
        )?;
        self.run_started_task(message, task_id, Some(message_id))
            .await
    }

    async fn run_started_task(
        &mut self,
        message: UserMessage,
        task_id: String,
        message_id: Option<String>,
    ) -> anyhow::Result<()> {
        if let Some(signal) = &self.started_signal {
            signal.send_replace(true);
        }
        self.emit(AgentEvent::TaskStarted {
            task_id,
            message_id,
        })
        .await;
        self.conversation.add_user_message(message);
        self.conversation.save_history()?;
        let result = self.process_messages().await;
        let result = match self.tool_registry.finish_task().await {
            Ok(()) => result,
            Err(cleanup) => Err(anyhow::Error::new(
                crate::tools::tool_registry::ToolError::ExecutionFailed(format!(
                    "{}; {cleanup}",
                    result
                        .as_ref()
                        .err()
                        .map_or("task execution completed".to_owned(), |error| format!(
                            "{error:#}"
                        ))
                )),
            )),
        };
        match &result {
            Err(error) if is_interrupted(error) => self.conversation.interrupt_task()?,
            _ => {
                self.conversation
                    .finish_task(result.as_ref().err().map(|error| format!("{error:#}")))?;
            }
        }
        result
    }

    pub fn context_event(&self) -> AgentEvent {
        let tools = self.tool_registry.list_definition();
        AgentEvent::ContextUpdated {
            model: self.model.clone(),
            context_tokens: self
                .conversation
                .estimated_context_tokens(&self.model, &tools)
                .ok(),
            context_window: self.conversation.context_window(),
            input_limit: self.conversation.input_limit(),
        }
    }

    async fn process_messages(&mut self) -> anyhow::Result<()> {
        loop {
            if is_cancelled(&self.cancel) {
                return Err(interrupted());
            }
            let tools = self.tool_registry.list_definition();
            self.emit_context().await;
            let compaction = {
                let client = &self.client;
                let conversation = &mut self.conversation;
                let model = self.model.clone();
                let cancel = &mut self.cancel;
                tokio::select! {
                    biased;
                    _ = cancelled(cancel) => return Err(interrupted()),
                    result = conversation.compact_if_needed(client, &model, &tools) => result?,
                }
            };
            if let Some(compaction) = compaction {
                self.emit(AgentEvent::Compaction {
                    tokens_before: compaction.tokens_before,
                    tokens_after: compaction.tokens_after,
                    summary_output_tokens: compaction
                        .usage
                        .as_ref()
                        .and_then(|usage| usage.output_tokens),
                })
                .await;
                self.emit_context().await;
            }
            let mut overflow_recovery_attempted = false;
            let respond = loop {
                if is_cancelled(&self.cancel) {
                    return Err(interrupted());
                }
                let request =
                    ModelRequest::new(self.model.clone(), self.conversation.items(), tools.clone())
                        .with_generation(Some(self.conversation.max_output_tokens()), None, None);
                let mut stream_has_output = false;
                let mut partials: Vec<(String, String)> = Vec::new();
                let outcome = {
                    let events = self.events.clone();
                    let client = &self.client;
                    let cancel = &mut self.cancel;
                    tokio::select! {
                        biased;
                        _ = cancelled(cancel) => None,
                        result = async {
                            if let Some(events) = &events {
                                events.send(AgentEvent::ModelRequestStarted {
                                    purpose: ModelRequestPurpose::Generation,
                                }).await;
                            }
                            client.respond_stream_async(request, |event| {
                                match &event {
                                    ModelStreamEvent::TextDelta { item_id, delta } => {
                                        stream_has_output = true;
                                        match partials.iter_mut().find(|(id, _)| id == item_id) {
                                            Some((_, text)) => text.push_str(delta),
                                            None => partials.push((item_id.clone(), delta.clone())),
                                        }
                                    }
                                    ModelStreamEvent::ReasoningDelta { .. } => {
                                        stream_has_output = true;
                                    }
                                    _ => {}
                                }
                                let event = match event {
                                    ModelStreamEvent::TextDelta { item_id, delta } => {
                                        AgentEvent::ResponseTextDelta { item_id, delta }
                                    }
                                    ModelStreamEvent::ReasoningDelta { item_id, delta } => {
                                        AgentEvent::ReasoningDelta { item_id, delta }
                                    }
                                    ModelStreamEvent::ReasoningCompleted { item_id } => {
                                        AgentEvent::ReasoningCompleted { item_id }
                                    }
                                };
                                let events = events.clone();
                                async move {
                                    if let Some(events) = events {
                                        events.send(event).await;
                                    }
                                }
                            }).await
                        } => Some(result),
                    }
                };
                let response = match outcome {
                    None => {
                        // Cancellation keeps each ordered assistant partial
                        // durable; streamed reasoning stays display-only and
                        // never becomes a fake provider payload.
                        for (_, text) in partials {
                            if !text.is_empty() {
                                self.conversation.add_assistant_message(text);
                            }
                        }
                        return Err(interrupted());
                    }
                    Some(Ok(response)) => response,
                    Some(Err(error)) => {
                        if stream_has_output
                            || !error
                                .downcast_ref::<ApiError>()
                                .is_some_and(ApiError::is_context_overflow)
                        {
                            return Err(error);
                        }
                        if overflow_recovery_attempted {
                            return Err(error.context("Context overflow recovery failed after one compact-and-retry attempt; reduce input or tool output"));
                        }
                        overflow_recovery_attempted = true;
                        let compaction = {
                            let client = &self.client;
                            let conversation = &mut self.conversation;
                            let model = self.model.clone();
                            let tools = tools.clone();
                            let cancel = &mut self.cancel;
                            tokio::select! {
                                biased;
                                _ = cancelled(cancel) => return Err(interrupted()),
                                result = conversation.compact(client, &model, &tools) => result
                                    .map_err(|error| error.context("Context overflow recovery could not compact; reduce input or tool output"))?,
                            }
                        };
                        self.emit(AgentEvent::Compaction {
                            tokens_before: compaction.tokens_before,
                            tokens_after: compaction.tokens_after,
                            summary_output_tokens: compaction
                                .usage
                                .as_ref()
                                .and_then(|usage| usage.output_tokens),
                        })
                        .await;
                        self.emit_context().await;
                        continue;
                    }
                };
                break response;
            };
            let output = respond.output;
            let mut pending_function_call = Vec::new();
            for output_item in output {
                match output_item {
                    OutputItem::Message { id, content, .. } => {
                        let text: String = content
                            .into_iter()
                            .map(|OutputText { text }| text)
                            .collect::<String>();

                        self.conversation.add_assistant_message(text.clone());
                        self.emit(AgentEvent::ResponseText { item_id: id, text })
                            .await;
                    }
                    OutputItem::Reasoning {
                        id,
                        content,
                        provider_data,
                        ..
                    } => {
                        let thinking_text = content
                            .iter()
                            .map(|part| part.text.as_str())
                            .collect::<String>();
                        if !thinking_text.is_empty() {
                            self.emit(AgentEvent::Reasoning {
                                item_id: id,
                                text: thinking_text,
                            })
                            .await;
                        }
                        self.conversation.add_reasoning(content, provider_data);
                    }
                    OutputItem::ToolCall {
                        call_id,
                        name,
                        arguments,
                        ..
                    } => {
                        self.conversation.add_tool_call(
                            call_id.clone(),
                            name.clone(),
                            arguments.clone(),
                        );
                        pending_function_call.push((call_id, name, arguments));
                    }
                    _ => {}
                };
            }
            self.conversation
                .record_usage(&self.model, &tools, respond.usage.as_ref())?;
            self.emit(AgentEvent::Usage {
                input_tokens: respond.usage.as_ref().and_then(|usage| usage.input_tokens),
                output_tokens: respond.usage.as_ref().and_then(|usage| usage.output_tokens),
            })
            .await;
            self.emit_context().await;
            self.conversation.save_history()?;
            if pending_function_call.is_empty() {
                break;
            }
            for (call_id, name, arguments) in pending_function_call {
                if is_cancelled(&self.cancel) {
                    return Err(interrupted());
                }
                self.emit(AgentEvent::ToolStarted {
                    call_id: call_id.clone(),
                    name: name.clone(),
                    arguments: arguments.clone(),
                })
                .await;
                let function_call_output = match serde_json::from_str(&arguments) {
                    Ok(arg) => {
                        let call = ToolCall::new(call_id.clone(), name.clone(), arg);
                        let result = {
                            let tool_registry = &self.tool_registry;
                            let cancel = &mut self.cancel;
                            tokio::select! {
                                biased;
                                _ = cancelled(cancel) => {
                                    // Close the running tool pair for live UI and
                                    // journal replay before the terminal event,
                                    // using the same shape replay reconciliation uses.
                                    let interrupted_output =
                                        Conversation::interrupted_tool_output();
                                    self.conversation.add_tool_result(
                                        call_id.clone(),
                                        serde_json::to_string(&interrupted_output)?,
                                    );
                                    self.conversation.save_history()?;
                                    self.emit(AgentEvent::ToolFinished {
                                        call_id: call_id.clone(),
                                        name: name.clone(),
                                        output: interrupted_output,
                                    }).await;
                                    return Err(interrupted());
                                }
                                result = tool_registry.execution_tool(call) => result,
                            }
                        };
                        match result {
                            Ok(output) => output,
                            Err(error) => serde_json::json!({
                                "error" : error.to_string()
                            }),
                        }
                    }
                    Err(error) => serde_json::json!({
                        "error" : format!("Invalid tool arguments: {error}")
                    }),
                };
                self.conversation.add_tool_result(
                    call_id.clone(),
                    serde_json::to_string(&function_call_output)?,
                );
                self.conversation.save_history()?;
                self.emit(AgentEvent::ToolFinished {
                    call_id,
                    name,
                    output: function_call_output,
                })
                .await;
                self.emit_context().await;
            }
            self.conversation.save_history()?;
        }
        Ok(())
    }
}

/// Test-facing worker entry point: forwards user messages to
/// [`Agent::handle_user_message`] and emits exactly one terminal event per
/// message at that return boundary, so a turn never has both a success and a
/// failure event. Closing the input channel ends the worker.
///
/// The binary drives the same loop through `SessionManager::run`, so this helper
/// is compiled only for tests.
#[cfg(test)]
pub async fn run_agent(mut agent: Agent, mut input: UnboundedReceiver<UserMessage>) {
    agent.emit_context().await;
    while let Some(message) = input.recv().await {
        match agent.handle_user_message(message).await {
            Ok(()) => agent.emit(AgentEvent::TurnCompleted).await,
            Err(error) if is_interrupted(&error) => agent.emit(AgentEvent::TurnInterrupted).await,
            Err(error) => {
                agent
                    .emit(AgentEvent::TurnFailed {
                        message: format!("{error:#}"),
                    })
                    .await
            }
        }
    }
}

#[cfg(test)]
#[path = "../../tests/agent/mod.rs"]
mod tests;
