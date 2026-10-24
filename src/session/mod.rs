pub mod channel;
pub mod config;
pub mod host;
pub mod runtime;
pub mod stdio;
pub mod store;
mod terminal;
mod title;

pub use channel::{SessionEventPacket, SessionEventReceiver, SessionEventSender};
pub use terminal::{SessionCommand, SessionEvent, SessionManager, SessionRequest};

#[cfg(test)]
use crate::agent::{AgentEvent, conversation::CompactionConfig};
use crate::{
    agent::Agent,
    client::ModelClient,
    tools::{ToolContext, register_builtins, tool_registry::ToolRegistry},
};
use anyhow::Result;
#[cfg(test)]
use std::path::PathBuf;
use store::{LoadedSession, SessionMetadata, SessionStore};
#[cfg(test)]
use title::title_from_output;

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

#[cfg(test)]
#[path = "../../tests/session/mod.rs"]
mod tests;
