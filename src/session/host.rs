use std::path::Path;

use anyhow::{Context, Result, ensure};
use serde::Deserialize;
use tokio::sync::Semaphore;

use crate::client::{
    ClientConfig, ModelApi,
    connections::{Connection, ConnectionDefinition, ConnectionRegistry},
};

#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct HostConfig {
    pub default_connection_id: String,
    pub connections: Vec<ConnectionDefinition>,
    #[serde(default = "default_max_active_tasks")]
    pub max_active_tasks: usize,
    #[serde(default)]
    pub tool_sources: Vec<crate::tools::mcp::config::ToolSourceDefinition>,
    #[serde(default = "default_max_tool_processes")]
    pub max_tool_processes: usize,
}

fn default_max_tool_processes() -> usize {
    8
}

fn default_max_active_tasks() -> usize {
    1
}

impl HostConfig {
    /// Read exactly the JSON host configuration. Environment lookup is
    /// deliberately deferred to `build_connections`.
    pub fn load(path: &Path) -> Result<Self> {
        let bytes = std::fs::read(path)
            .with_context(|| format!("reading host config {}", path.display()))?;
        serde_json::from_slice(&bytes)
            .with_context(|| format!("parsing host config {}", path.display()))
    }

    pub fn build_tools(
        &self,
        base: &Path,
        read_env: impl Fn(&str) -> Option<String>,
    ) -> Result<crate::tools::mcp::McpTools> {
        crate::tools::mcp::McpTools::new(
            self.tool_sources.clone(),
            self.max_tool_processes,
            base,
            read_env,
        )
    }

    /// Build the immutable connection directory. The callback is the only
    /// credential source; this function never reads `.env` or the process
    /// environment and never performs network I/O.
    pub fn build_connections(
        &self,
        read_env: impl Fn(&str) -> Option<String>,
    ) -> Result<ConnectionRegistry> {
        ensure!(
            self.max_active_tasks > 0 && self.max_active_tasks <= Semaphore::MAX_PERMITS,
            "max_active_tasks is outside the supported capacity"
        );
        let mut connections = Vec::with_capacity(self.connections.len());
        for definition in &self.connections {
            let api = ModelApi::parse(&definition.api)?;
            let key = read_env(&definition.api_key_env)
                .filter(|key| !key.is_empty())
                .ok_or_else(|| {
                    anyhow::anyhow!(
                        "API key environment variable {} is missing or empty",
                        definition.api_key_env
                    )
                })?;
            let config = ClientConfig::new(api, key, definition.base_url.clone());
            connections.push(Connection::new(
                definition.id.clone(),
                config,
                definition.default_model.clone(),
            )?);
        }
        ConnectionRegistry::new(self.default_connection_id.clone(), connections)
    }
}

#[cfg(test)]
#[path = "../../tests/session/host.rs"]
mod tests;
