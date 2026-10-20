use std::{
    collections::{HashMap, VecDeque},
    io::{IsTerminal, Write},
};

use tokio::io::{AsyncBufRead, AsyncBufReadExt, BufReader};
use tokio::sync::mpsc::{UnboundedReceiver, UnboundedSender};

use crate::agent::AgentEvent;
use crate::{
    agent::{
        journal::{JournalRecord, TaskStatus},
        message::UserMessage,
    },
    command::{self, Command, ParsedInput},
    protocol::{InputItem, InputMessageContent, MessageRole},
    session::{SessionCommand, SessionEvent},
};

/// Independent plain-text front end.
///
/// It owns stdin/stdout and connects to the agent worker through the same
/// channels the TUI will use. On stdin EOF it closes the input channel but keeps
/// consuming events until the worker ends, so a piped single message still gets
/// its answer printed.
pub async fn run(
    input: UnboundedSender<SessionCommand>,
    events: UnboundedReceiver<SessionEvent>,
) -> anyhow::Result<()> {
    run_with_io(
        input,
        events,
        BufReader::new(tokio::io::stdin()),
        std::io::stdout(),
        std::io::stdin().is_terminal(),
    )
    .await
}

async fn run_with_io<R: AsyncBufRead + Unpin, W: Write>(
    input: UnboundedSender<SessionCommand>,
    mut events: UnboundedReceiver<SessionEvent>,
    reader: R,
    writer: W,
    interactive: bool,
) -> anyhow::Result<()> {
    let mut output = ConsoleOutput::new(writer);
    let mut input = Some(input);
    let mut lines = reader.lines();
    let mut session_id: Option<String> = None;
    let mut running = false;
    let mut eof = false;
    let mut queue = VecDeque::<UserMessage>::new();
    let mut pending: Option<(Command, String)> = None;
    loop {
        if !running
            && pending.is_none()
            && let Some(message) = queue.pop_front()
        {
            let request = SessionCommand::Send {
                session_id: session_id.clone().unwrap(),
                message,
            };
            if input.as_ref().unwrap().send(request).is_err() {
                break;
            }
            running = true;
        }
        if eof && queue.is_empty() && pending.is_none() {
            input = None;
        }
        tokio::select! {
            line = lines.next_line(), if !eof && session_id.is_some() && pending.is_none() => {
                let Some(line) = line? else { eof = true; continue; };
                match command::parse(&line, false) {
                    Ok(ParsedInput::Message(text)) => {
                        if interactive && running {
                            output.feedback("Agent is busy. Wait for the current task to finish.", Some(&line))?;
                        } else { queue.push_back(text.into()); }
                    }
                    Ok(ParsedInput::Command(command)) => {
                        if command.needs_idle() && running {
                            output.feedback("Agent is busy. Wait for the current task to finish.", Some(&line))?;
                        } else if command == Command::Help {
                            output.close_block()?;
                            writeln!(output.writer, "Local commands:")?;
                            for definition in command::DEFINITIONS { writeln!(output.writer, "  {}  {}", definition.usage, definition.description)?; }
                            writeln!(output.writer, "Use // to send literal slash text. Multiline input stays a message.")?;
                            output.prompt()?;
                        } else {
                            let request = command.request(session_id.as_deref().unwrap()).unwrap();
                            if input.as_ref().unwrap().send(request).is_err() { break; }
                            pending = Some((command, line));
                        }
                    }
                    Err(error) => output.feedback(&error.to_string(), Some(&line))?,
                }
            }
            event = events.recv() => {
                let Some(event) = event else { break; };
                if pending.as_ref().is_some_and(|(command, _)| command.acknowledged_by(&event)) { pending = None; }
                match event {
                    SessionEvent::Opened { metadata, replay, .. } => {
                        output.close_block()?;
                        writeln!(output.writer, "Session: {} ({})", metadata.title, metadata.id)?;
                        output.restore(&replay)?;
                        if matches!(replay.status, TaskStatus::Interrupted | TaskStatus::Unknown) && replay.context.len() > 1 {
                            writeln!(output.writer, "Previous task: {:?}. Nothing was re-executed.", replay.status)?;
                        }
                        session_id = Some(metadata.id);
                        output.prompt()?;
                    }
                    SessionEvent::Listed(sessions) => {
                        output.close_block()?;
                        writeln!(output.writer, "Sessions:")?;
                        for session in sessions {
                            let state = if session.unavailable.is_some() { "unavailable" } else if session.in_use { "in use" } else { "available" };
                            writeln!(output.writer, "  {}  {}  [{state}]  {}", session.metadata.id, session.metadata.title, session.metadata.working_directory.display())?;
                        }
                        output.prompt()?;
                    }
                    SessionEvent::Renamed(metadata) => {
                        output.feedback(&format!("Session renamed: {}", metadata.title), None)?;
                    }
                    SessionEvent::Agent { session_id: id, event } if session_id.as_ref() == Some(&id) => {
                        let terminal = matches!(event, AgentEvent::TurnCompleted | AgentEvent::TurnInterrupted | AgentEvent::TurnFailed { .. });
                        output.apply_event(event)?;
                        if terminal { running = false; if !eof { output.prompt()?; } }
                    }
                    SessionEvent::Error { message } => {
                        let kept = pending.take().map(|(_, text)| text);
                        output.feedback(&message, kept.as_deref())?;
                    }
                    _ => {}
                }
            }
        }
    }
    output.writer.flush()?;
    Ok(())
}

struct ConsoleOutput<W> {
    writer: W,
    active: Option<(bool, String)>,
    streamed: HashMap<(bool, String), String>,
}

impl<W: Write> ConsoleOutput<W> {
    fn new(writer: W) -> Self {
        Self {
            writer,
            active: None,
            streamed: HashMap::new(),
        }
    }

    fn prompt(&mut self) -> std::io::Result<()> {
        write!(self.writer, "User:\t")?;
        self.writer.flush()
    }

    fn feedback(&mut self, message: &str, kept: Option<&str>) -> std::io::Result<()> {
        self.close_block()?;
        writeln!(self.writer, "{message}")?;
        if let Some(text) = kept {
            writeln!(self.writer, "Input kept: {text}")?;
        }
        self.prompt()
    }

    fn restore(&mut self, replay: &crate::agent::journal::Replay) -> std::io::Result<()> {
        let mut tools = HashMap::new();
        for (index, record) in replay.history.iter().enumerate() {
            match record {
                JournalRecord::Messages { items } => {
                    for (part, item) in items.iter().enumerate() {
                        let item_id = format!("history-{index}-{part}");
                        match item {
                            InputItem::Message { role, content } => {
                                let text = match content {
                                    InputMessageContent::Text(text) => text.clone(),
                                    InputMessageContent::Parts(parts) => parts
                                        .iter()
                                        .map(|part| match part {
                                            crate::protocol::InputContentPart::Text { text } => {
                                                text.clone()
                                            }
                                            crate::protocol::InputContentPart::Image { .. } => {
                                                "[image]".to_owned()
                                            }
                                        })
                                        .collect::<Vec<_>>()
                                        .join("\n"),
                                };
                                match role {
                                    MessageRole::User => writeln!(self.writer, "User: {text}")?,
                                    MessageRole::Assistant => {
                                        self.complete(false, item_id, text)?
                                    }
                                    _ => {}
                                }
                            }
                            InputItem::Reasoning { content, .. } => self.complete(
                                true,
                                item_id,
                                content.iter().map(|part| part.text.as_str()).collect(),
                            )?,
                            InputItem::ToolCall {
                                call_id,
                                name,
                                arguments,
                            } => {
                                tools.insert(call_id.clone(), name.clone());
                                self.apply_event(AgentEvent::ToolStarted {
                                    call_id: call_id.clone(),
                                    name: name.clone(),
                                    arguments: arguments.clone(),
                                })?;
                            }
                            InputItem::ToolResult { call_id, output } => {
                                self.apply_event(AgentEvent::ToolFinished {
                                    call_id: call_id.clone(),
                                    name: tools.remove(call_id).unwrap_or_default(),
                                    output: serde_json::from_str(output).unwrap_or_else(|_| {
                                        serde_json::Value::String(output.clone())
                                    }),
                                })?
                            }
                            _ => {}
                        }
                    }
                }
                JournalRecord::Compaction {
                    tokens_before,
                    tokens_after,
                    usage,
                    ..
                } => self.apply_event(AgentEvent::Compaction {
                    tokens_before: *tokens_before,
                    tokens_after: *tokens_after,
                    summary_output_tokens: usage.as_ref().and_then(|usage| usage.output_tokens),
                })?,
                JournalRecord::TaskFinished {
                    error: Some(error), ..
                } => writeln!(self.writer, "Task failed: {error}")?,
                _ => {}
            }
        }
        Ok(())
    }

    fn close_block(&mut self) -> std::io::Result<()> {
        if self.active.take().is_some() {
            writeln!(self.writer)?;
        }
        Ok(())
    }

    fn delta(&mut self, reasoning: bool, item_id: String, delta: String) -> std::io::Result<()> {
        if delta.is_empty() {
            return Ok(());
        }
        let key = (reasoning, item_id);
        if self.active.as_ref() != Some(&key) {
            self.close_block()?;
            writeln!(
                self.writer,
                "{}:\n",
                if reasoning { "Thinking..." } else { "Agent" }
            )?;
            self.active = Some(key.clone());
        }
        write!(self.writer, "{delta}")?;
        self.writer.flush()?;
        self.streamed.entry(key).or_default().push_str(&delta);
        Ok(())
    }

    fn complete(&mut self, reasoning: bool, item_id: String, text: String) -> std::io::Result<()> {
        let key = (reasoning, item_id.clone());
        let remaining = match self.streamed.remove(&key) {
            Some(shown) => text.strip_prefix(&shown).unwrap_or(&text).to_owned(),
            None => text,
        };
        self.delta(reasoning, item_id, remaining)?;
        self.streamed.remove(&key);
        self.close_block()
    }

    fn apply_event(&mut self, event: AgentEvent) -> std::io::Result<()> {
        match event {
            AgentEvent::TaskStarted { .. } | AgentEvent::ModelRequestStarted { .. } => {}
            AgentEvent::ResponseTextDelta { item_id, delta } => {
                self.delta(false, item_id, delta)?
            }
            AgentEvent::ReasoningDelta { item_id, delta } => self.delta(true, item_id, delta)?,
            AgentEvent::ResponseText { item_id, text } => self.complete(false, item_id, text)?,
            AgentEvent::Reasoning { item_id, text } => self.complete(true, item_id, text)?,
            AgentEvent::ReasoningCompleted { item_id } => {
                if self.active.as_ref() == Some(&(true, item_id)) {
                    self.close_block()?;
                }
            }
            AgentEvent::ToolStarted { name, .. } => {
                self.close_block()?;
                writeln!(self.writer, "Tool started: {name}")?;
            }
            AgentEvent::ToolFinished { name, call_id, .. } => {
                writeln!(self.writer, "Tool finished: {name} ({call_id})")?;
            }
            AgentEvent::Compaction {
                tokens_before,
                tokens_after,
                summary_output_tokens,
            } => {
                self.close_block()?;
                let summary = summary_output_tokens
                    .map(|tokens| format!(", summary output: {tokens}"))
                    .unwrap_or_default();
                writeln!(
                    self.writer,
                    "Context compacted: ~{tokens_before} → ~{tokens_after} input tokens{summary}"
                )?;
            }
            AgentEvent::Usage { .. } | AgentEvent::TurnCompleted => {
                self.close_block()?;
                self.streamed.clear();
            }
            AgentEvent::TurnInterrupted => {
                self.close_block()?;
                self.streamed.clear();
                eprintln!("Task interrupted.");
            }
            AgentEvent::TurnFailed { message } => {
                self.close_block()?;
                self.streamed.clear();
                eprintln!("Task failed: {message}");
            }
            AgentEvent::ContextUpdated { .. } => {}
        }
        Ok(())
    }
}

#[cfg(test)]
#[path = "../../tests/console/mod.rs"]
mod tests;
