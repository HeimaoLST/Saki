mod catalog;
#[cfg(unix)]
mod client;
pub mod config;
mod transport;

use self::{
    catalog::{PreparedToolSchema, prepare_tool_schema},
    client::{CatalogUnstable, SourceProcess},
    config::{PreparedSource, ToolSourceDefinition},
};
use super::{
    ToolContext,
    output::{MAX_OUTPUT_BYTES, OutputAccumulator},
    tool_registry::{Tool, ToolError, ToolRegistry},
};
use crate::protocol::{ByteCounter, ToolDefinition, ToolSchema, ToolSelection, ToolType};
use anyhow::{Result, ensure};
use serde::Serialize;
use serde_json::{Value, json};
use std::{
    collections::{BTreeMap, BTreeSet},
    path::Path,
    sync::{
        Arc,
        atomic::{AtomicU64, Ordering},
    },
};
use tokio::sync::{Mutex, Semaphore};

#[derive(Debug, thiserror::Error)]
pub enum ToolSourceError {
    #[error("MCP tool process capacity is full")]
    Capacity,
    #[error("MCP source {source_id} is unavailable: {error:#}")]
    Unavailable {
        source_id: String,
        #[source]
        error: anyhow::Error,
    },
    #[error("MCP source {source_id} catalog changed during both discovery attempts")]
    CatalogUnstable { source_id: String },
    #[error(
        "selected MCP tool declaration changed: {exposed_name}; explicitly confirm tool_selection"
    )]
    SchemaChanged { exposed_name: String },
}

fn source_error(id: &str, error: anyhow::Error) -> ToolSourceError {
    if error.downcast_ref::<CatalogUnstable>().is_some() {
        ToolSourceError::CatalogUnstable {
            source_id: id.into(),
        }
    } else {
        ToolSourceError::Unavailable {
            source_id: id.into(),
            error,
        }
    }
}

pub struct McpTools {
    sources: BTreeMap<String, Arc<PreparedSource>>,
    slots: Arc<Semaphore>,
    generation: AtomicU64,
}

#[derive(Clone, Default)]
pub(crate) struct SessionTools {
    sources: BTreeMap<String, Arc<Mutex<SourceProcess>>>,
}

pub(crate) struct PreparedTools {
    pub(crate) resources: SessionTools,
    pub(crate) schemas: BTreeMap<String, ToolSchema>,
    bindings: Vec<PreparedBinding>,
}

struct PreparedBinding {
    name: String,
    exposed_name: String,
    schema: Arc<PreparedToolSchema>,
    source: Arc<Mutex<SourceProcess>>,
}

impl PreparedTools {
    pub(crate) fn empty() -> Self {
        Self {
            resources: SessionTools::default(),
            schemas: BTreeMap::new(),
            bindings: Vec::new(),
        }
    }
    pub(crate) fn install(
        &mut self,
        selection: &ToolSelection,
        context: Arc<ToolContext>,
        registry: &mut ToolRegistry,
    ) {
        let resources = self.resources.clone();
        registry.set_task_cleanup(move || {
            let resources = resources.clone();
            Box::pin(async move {
                resources.finish_task().await.map_err(|error| {
                    ToolError::ExecutionFailed(format!("tool cleanup failed: {error}"))
                })
            })
        });
        let can_read = selection.builtin.iter().any(|name| name == "read");
        let can_bash = selection.builtin.iter().any(|name| name == "bash");
        for binding in self.bindings.drain(..) {
            let definition = ToolDefinition {
                name: binding.exposed_name,
                tool_type: ToolType::Function,
                description: binding.schema.schema.description.clone(),
                parameters: binding.schema.schema.input_schema.clone(),
            };
            let context = context.clone();
            registry.registry(Tool {
                definition,
                handler: Arc::new(move |arguments| {
                    let source = binding.source.clone();
                    let schema = binding.schema.clone();
                    let name = binding.name.clone();
                    let context = context.clone();
                    Box::pin(async move {
                        schema
                            .validate_input(&arguments)
                            .map_err(|error| ToolError::InvalidArgs(format!("{error:#}")))?;
                        let result = source
                            .lock()
                            .await
                            .call(&name, arguments)
                            .await
                            .map_err(|error| ToolError::ExecutionFailed(format!("{error:#}")))?;
                        if !result.is_error.unwrap_or(false) {
                            schema
                                .validate_output(result.structured_content.as_ref())
                                .map_err(|error| {
                                    ToolError::ExecutionFailed(format!("{error:#}"))
                                })?;
                        }
                        convert_result(result, &context, can_read, can_bash).await
                    })
                }),
            });
        }
    }
}

#[derive(Serialize)]
pub struct AvailableTool {
    pub name: String,
    pub schema: ToolSchema,
}
#[derive(Serialize)]
pub struct SourceCatalog {
    pub source_id: String,
    pub available: bool,
    pub protocol_version: Option<String>,
    pub generation: Option<u64>,
    pub monitoring: Option<&'static str>,
    pub tools: Vec<AvailableTool>,
    pub error: Option<String>,
}

impl McpTools {
    pub fn new(
        definitions: Vec<ToolSourceDefinition>,
        max_processes: usize,
        base: &Path,
        read_env: impl Fn(&str) -> Option<String>,
    ) -> Result<Self> {
        ensure!(
            max_processes > 0 && max_processes <= Semaphore::MAX_PERMITS,
            "max_tool_processes is outside the supported capacity"
        );
        let mut sources = BTreeMap::new();
        for definition in definitions {
            let source = definition.prepare(base, &read_env)?;
            let id = source.id.clone();
            ensure!(
                sources.insert(id.clone(), Arc::new(source)).is_none(),
                "duplicate tool source id: {id}"
            );
        }
        Ok(Self {
            sources,
            slots: Arc::new(Semaphore::new(max_processes)),
            generation: AtomicU64::new(0),
        })
    }

    async fn start(
        &self,
        id: &str,
        cwd: &Path,
    ) -> std::result::Result<(Arc<Mutex<SourceProcess>>, Vec<rmcp::model::Tool>), ToolSourceError>
    {
        let config = self
            .sources
            .get(id)
            .cloned()
            .ok_or_else(|| source_error(id, anyhow::anyhow!("source is not configured")))?;
        let permit = self
            .slots
            .clone()
            .try_acquire_owned()
            .map_err(|_| ToolSourceError::Capacity)?;
        let generation = self.generation.fetch_add(1, Ordering::Relaxed) + 1;
        let (source, tools) = SourceProcess::start(config, cwd, generation, permit)
            .await
            .map_err(|error| source_error(id, error))?;
        Ok((Arc::new(Mutex::new(source)), tools))
    }

    async fn discover_source(
        &self,
        id: &str,
        cwd: &Path,
        previous: Option<&SessionTools>,
    ) -> std::result::Result<(Arc<Mutex<SourceProcess>>, Vec<rmcp::model::Tool>), ToolSourceError>
    {
        if let Some(source) = previous.and_then(|old| old.sources.get(id)) {
            let mut connection = source.lock().await;
            if !connection.is_closed() {
                match connection.discover().await {
                    Ok(tools) => return Ok((source.clone(), tools)),
                    Err(error) if !connection.is_closed() => return Err(source_error(id, error)),
                    Err(_) => {}
                }
            }
            connection
                .close()
                .await
                .map_err(|error| source_error(id, error))?;
        }
        self.start(id, cwd).await
    }

    pub async fn list(
        &self,
        source_id: Option<&str>,
        cwd: &Path,
    ) -> std::result::Result<Vec<SourceCatalog>, ToolSourceError> {
        let ids = match source_id {
            Some(id) if self.sources.contains_key(id) => vec![id.to_owned()],
            Some(id) => {
                return Err(source_error(
                    id,
                    anyhow::anyhow!("source is not configured"),
                ));
            }
            None => self.sources.keys().cloned().collect(),
        };
        let mut catalogs = Vec::new();
        for id in ids {
            match self.start(&id, cwd).await {
                Err(error) => catalogs.push(SourceCatalog {
                    source_id: id,
                    available: false,
                    protocol_version: None,
                    generation: None,
                    monitoring: None,
                    tools: Vec::new(),
                    error: Some(error.to_string()),
                }),
                Ok((source, tools)) => {
                    let mut connection = source.lock().await;
                    let protocol_version = Some(connection.protocol_version());
                    let generation = Some(connection.generation);
                    let monitoring = Some(connection.monitoring());
                    let prepared: Result<Vec<_>> = tools
                        .iter()
                        .map(|tool| {
                            Ok(AvailableTool {
                                name: tool.name.to_string(),
                                schema: prepare_tool_schema(tool)?.schema,
                            })
                        })
                        .collect();
                    connection
                        .close()
                        .await
                        .map_err(|error| source_error(&id, error))?;
                    let (tools, error) = match prepared {
                        Ok(tools) => (tools, None),
                        Err(error) => (Vec::new(), Some(format!("{error:#}"))),
                    };
                    catalogs.push(SourceCatalog {
                        source_id: id,
                        available: error.is_none(),
                        protocol_version,
                        generation,
                        monitoring,
                        tools,
                        error,
                    });
                }
            }
        }
        Ok(catalogs)
    }

    pub(crate) async fn prepare(
        &self,
        selection: &ToolSelection,
        expected: &BTreeMap<String, ToolSchema>,
        confirm: bool,
        previous: Option<&SessionTools>,
        cwd: &Path,
    ) -> std::result::Result<PreparedTools, ToolSourceError> {
        let mut resources = SessionTools::default();
        let mut catalogs = BTreeMap::new();
        let mut schemas = BTreeMap::new();
        let mut bindings = Vec::new();
        let prepared = async {
            for binding in &selection.external {
                if !resources.sources.contains_key(&binding.source_id) {
                    let (source, catalog) = self
                        .discover_source(&binding.source_id, cwd, previous)
                        .await?;
                    resources.sources.insert(binding.source_id.clone(), source);
                    catalogs.insert(binding.source_id.clone(), catalog);
                }
                let tool = catalogs[&binding.source_id]
                    .iter()
                    .find(|tool| tool.name.as_ref() == binding.tool_name)
                    .ok_or_else(|| {
                        source_error(
                            &binding.source_id,
                            anyhow::anyhow!("selected tool {} is unavailable", binding.tool_name),
                        )
                    })?;
                let prepared = prepare_tool_schema(tool)
                    .map_err(|error| source_error(&binding.source_id, error))?;
                if !confirm && expected.get(&binding.exposed_name) != Some(&prepared.schema) {
                    return Err(ToolSourceError::SchemaChanged {
                        exposed_name: binding.exposed_name.clone(),
                    });
                }
                schemas.insert(binding.exposed_name.clone(), prepared.schema.clone());
                bindings.push(PreparedBinding {
                    name: binding.tool_name.clone(),
                    exposed_name: binding.exposed_name.clone(),
                    schema: Arc::new(prepared),
                    source: resources.sources[&binding.source_id].clone(),
                });
            }
            Ok(())
        }
        .await;
        if let Err(error) = prepared {
            resources.close_except(previous).await?;
            return Err(error);
        }
        Ok(PreparedTools {
            resources,
            schemas,
            bindings,
        })
    }
}

impl SessionTools {
    pub(crate) async fn close_except(
        &self,
        keep: Option<&Self>,
    ) -> std::result::Result<(), ToolSourceError> {
        let mut failure = None;
        for (id, source) in &self.sources {
            if keep
                .and_then(|other| other.sources.get(id))
                .is_some_and(|other| Arc::ptr_eq(source, other))
            {
                continue;
            }
            if let Err(error) = source.lock().await.close().await {
                failure = Some(source_error(id, error));
            }
        }
        failure.map_or(Ok(()), Err)
    }
    pub(crate) async fn finish_task(&self) -> std::result::Result<(), ToolSourceError> {
        let mut failure = None;
        for (id, source) in &self.sources {
            let mut source = source.lock().await;
            if source.needs_cleanup()
                && let Err(error) = source.close().await
            {
                failure = Some(source_error(id, error));
            }
        }
        failure.map_or(Ok(()), Err)
    }
}

pub(crate) fn validate_selection(selection: &ToolSelection) -> Result<()> {
    let mut names: BTreeSet<&str> = selection.builtin.iter().map(String::as_str).collect();
    for binding in &selection.external {
        ensure!(
            !binding.source_id.trim().is_empty() && !binding.tool_name.trim().is_empty(),
            "external tool source/name must not be empty"
        );
        let name = &binding.exposed_name;
        ensure!(
            !name.is_empty()
                && name.len() <= 64
                && name
                    .bytes()
                    .all(|byte| byte.is_ascii_alphanumeric() || byte == b'_' || byte == b'-'),
            "exposed tool name must use 1..64 ASCII letters, digits, underscores or hyphens"
        );
        ensure!(
            !super::builtin_names().contains(&name.as_str()) && names.insert(name),
            "exposed tool names must be unique and cannot shadow builtin tools"
        );
    }
    ensure!(names.len() <= 1024, "tool selection exceeds its limit");
    Ok(())
}

async fn convert_result(
    result: rmcp::model::CallToolResult,
    context: &ToolContext,
    can_read: bool,
    can_bash: bool,
) -> std::result::Result<Value, ToolError> {
    if result
        .content
        .iter()
        .any(|part| !matches!(part, rmcp::model::ContentBlock::Text(_)))
    {
        return Err(ToolError::ExecutionFailed("MCP returned unsupported image/audio/resource content; no complete text result was supplied".into()));
    }
    let bytes =
        if serde_json::to_writer_pretty(&mut ByteCounter::new(transport::FRAME_MAX_BYTES), &result)
            .is_ok()
        {
            serde_json::to_vec_pretty(&result)
        } else {
            serde_json::to_vec(&result)
        }
        .map_err(|error| ToolError::ExecutionFailed(error.to_string()))?;
    let mut output = OutputAccumulator::new();
    output.append(&bytes, &context.output_dir).await?;
    output.finish().await?;
    if let Some(path) = &output.path {
        let readback = if can_read
            && bytes
                .split_inclusive(|byte| *byte == b'\n')
                .all(|line| line.len() <= MAX_OUTPUT_BYTES)
        {
            Some("read")
        } else if can_bash {
            Some("bash")
        } else {
            None
        };
        let notice = match readback {
            Some("read") => {
                "Full MCP JSON result was saved; use read with offset/next_offset to inspect output_path."
            }
            Some("bash") => {
                "Full MCP JSON result was saved; use selected bash to inspect bounded byte slices of output_path."
            }
            _ => {
                "Only this truncated preview is available to the model. Selected tools cannot read the full encoded result; the artifact is available to the host."
            }
        };
        Ok(
            json!({"isError":result.is_error.unwrap_or(false),"truncated":true,"preview":output.preview(),"output_path":path,"readback_available":readback.is_some(),"readback_tool":readback,"notice":notice}),
        )
    } else {
        serde_json::from_slice(&bytes)
            .map_err(|error| ToolError::ExecutionFailed(error.to_string()))
    }
}

#[cfg(test)]
#[path = "../../../tests/support/mcp.rs"]
pub(crate) mod test_support;

#[cfg(test)]
#[path = "../../../tests/tools/mcp.rs"]
mod tests;
