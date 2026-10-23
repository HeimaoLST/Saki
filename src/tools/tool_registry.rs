use std::collections::HashMap;
use std::{pin::Pin, sync::Arc};

use crate::protocol::ToolDefinition;
use serde_json::Value;
use thiserror::Error;
#[derive(Debug, Error)]
pub enum ToolError {
    #[error("Invalid arguments: {0}")]
    InvalidArgs(String),
    #[error("Execution Failed: {0}")]
    ExecutionFailed(String),
    #[error("Don't find the valid tool named: {0}")]
    NotFound(String),
}

impl From<std::io::Error> for ToolError {
    fn from(error: std::io::Error) -> Self {
        Self::ExecutionFailed(error.to_string())
    }
}
type ToolFutute = Pin<Box<dyn Future<Output = Result<Value, ToolError>> + Send>>;
type ToolHandler = Arc<dyn Fn(Value) -> ToolFutute + Send + Sync>;
type TaskCleanupFuture = Pin<Box<dyn Future<Output = Result<(), ToolError>> + Send>>;
type TaskCleanup = Arc<dyn Fn() -> TaskCleanupFuture + Send + Sync>;
pub struct Tool {
    pub definition: ToolDefinition,
    pub handler: ToolHandler,
}
#[derive(Clone)]
pub struct ToolCall {
    pub call_id: String,
    name: String,
    arguments: Value,
}

impl ToolCall {
    pub fn new(call_id: String, name: String, arguments: Value) -> Self {
        Self {
            call_id,
            name,
            arguments,
        }
    }
}
pub struct ToolRegistry {
    tools: HashMap<String, Tool>,
    task_cleanup: Option<TaskCleanup>,
}
impl Default for ToolRegistry {
    fn default() -> Self {
        Self::new()
    }
}
impl ToolRegistry {
    pub fn new() -> Self {
        Self {
            tools: HashMap::new(),
            task_cleanup: None,
        }
    }
    pub fn registry(&mut self, tool: Tool) {
        self.tools.insert(tool.definition.name.clone(), tool);
    }
    pub fn get(&self, name: &str) -> Option<&Tool> {
        self.tools.get(name)
    }
    pub async fn execution_tool(&self, call: ToolCall) -> Result<Value, ToolError> {
        let tool = self
            .tools
            .get(&call.name)
            .ok_or_else(|| ToolError::NotFound(call.name.clone()))?;
        (tool.handler)(call.arguments).await
    }
    pub(crate) fn retain_names(&mut self, names: &[String]) {
        self.tools.retain(|name, _| names.contains(name));
    }

    pub(crate) fn set_task_cleanup(
        &mut self,
        cleanup: impl Fn() -> TaskCleanupFuture + Send + Sync + 'static,
    ) {
        self.task_cleanup = Some(Arc::new(cleanup));
    }
    pub(crate) async fn finish_task(&self) -> Result<(), ToolError> {
        if let Some(cleanup) = &self.task_cleanup {
            cleanup().await?;
        }
        Ok(())
    }
    pub fn list_definition(&self) -> Vec<ToolDefinition> {
        let mut definitions: Vec<_> = self
            .tools
            .values()
            .map(|tool| tool.definition.clone())
            .collect();
        definitions.sort_by(|left, right| left.name.cmp(&right.name));
        definitions
    }
}
