use std::{
    collections::BTreeMap,
    path::{Path, PathBuf},
    time::Duration,
};

use anyhow::{Result, bail};
use serde::{Deserialize, Serialize};

const DEFAULT_STARTUP_TIMEOUT_MS: u64 = 10_000;
const DEFAULT_CALL_TIMEOUT_MS: u64 = 120_000;
const DEFAULT_CANCEL_GRACE_MS: u64 = 2_000;

#[derive(Clone, Copy, Debug, Default, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum ProtocolPolicy {
    Modern,
    Legacy,
    #[default]
    Auto,
}

#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum ToolTransport {
    Stdio,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct ToolSourceDefinition {
    pub id: String,
    pub transport: ToolTransport,
    pub command: PathBuf,
    #[serde(default)]
    pub args: Vec<String>,
    #[serde(default)]
    pub env_from: Vec<String>,
    #[serde(default)]
    pub protocol: ProtocolPolicy,
    #[serde(default = "default_startup_timeout_ms")]
    pub startup_timeout_ms: u64,
    #[serde(default = "default_call_timeout_ms")]
    pub call_timeout_ms: u64,
    #[serde(default = "default_cancel_grace_ms")]
    pub cancel_grace_ms: u64,
}

#[derive(Clone)]
pub(crate) struct PreparedSource {
    pub(crate) id: String,
    pub(crate) command: PathBuf,
    pub(crate) args: Vec<String>,
    pub(crate) environment: BTreeMap<String, String>,
    pub(crate) protocol: ProtocolPolicy,
    pub(crate) startup_timeout: Duration,
    pub(crate) call_timeout: Duration,
    pub(crate) cancel_grace: Duration,
}

impl ToolSourceDefinition {
    pub(crate) fn prepare(
        &self,
        base_directory: &Path,
        read_env: impl Fn(&str) -> Option<String>,
    ) -> Result<PreparedSource> {
        if self.id.trim().is_empty() {
            bail!("tool source id must not be empty");
        }
        if self.command.as_os_str().is_empty() {
            bail!("tool source command must not be empty");
        }
        if self.startup_timeout_ms == 0 {
            bail!("tool source startup_timeout_ms must be greater than zero");
        }
        if self.call_timeout_ms == 0 {
            bail!("tool source call_timeout_ms must be greater than zero");
        }
        if self.cancel_grace_ms == 0 {
            bail!("tool source cancel_grace_ms must be greater than zero");
        }

        if !self.command.is_absolute() && !base_directory.is_absolute() {
            bail!("relative tool source command requires an absolute base directory");
        }

        let command = if self.command.is_absolute() {
            self.command.clone()
        } else {
            base_directory.join(&self.command)
        };

        let mut environment = BTreeMap::new();
        for name in &self.env_from {
            if environment.contains_key(name) {
                continue;
            }
            let Some(value) = read_env(name) else {
                bail!("missing environment variable {name:?}");
            };
            environment.insert(name.clone(), value);
        }

        Ok(PreparedSource {
            id: self.id.clone(),
            command,
            args: self.args.clone(),
            environment,
            protocol: self.protocol,
            startup_timeout: Duration::from_millis(self.startup_timeout_ms),
            call_timeout: Duration::from_millis(self.call_timeout_ms),
            cancel_grace: Duration::from_millis(self.cancel_grace_ms),
        })
    }
}

fn default_startup_timeout_ms() -> u64 {
    DEFAULT_STARTUP_TIMEOUT_MS
}

fn default_call_timeout_ms() -> u64 {
    DEFAULT_CALL_TIMEOUT_MS
}

fn default_cancel_grace_ms() -> u64 {
    DEFAULT_CANCEL_GRACE_MS
}

#[cfg(test)]
#[path = "../../../tests/tools/mcp_config.rs"]
mod tests;
