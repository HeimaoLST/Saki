use std::sync::Arc;

use serde::{Serialize, de::DeserializeOwned};
use serde_json::{Value, json};

use super::{
    output::MAX_OUTPUT_BYTES,
    tool_registry::{Tool, ToolError, ToolRegistry},
};
use crate::{
    protocol::{ByteCounter, ToolDefinition, ToolType},
    session::{
        context::{
            ContextScope, ContextStore, OpenContext, RESULT_LIMIT, ReadCancellation, ReadContext,
            SearchContext,
        },
        runtime::RuntimeError,
        store::SessionStore,
    },
};

pub(crate) fn definitions() -> Vec<ToolDefinition> {
    [
        ("open_context", "Open an immutable, read-only context snapshot from an allowed session. History preserves original records; working reflects committed compaction. Returns a reference, not the full contents. References expire and are released when this task ends.", json!({"session_id":{"type":"string"},"view":{"type":"string","enum":["history","working"]}}), vec!["session_id", "view"]),
        ("read_context", "Read a bounded UTF-8 byte range from an open context reference. Use next_offset to continue. Source indices apply only to this reference. max_bytes includes JSON metadata and escaping; maximum 49152.", json!({"context_ref":{"type":"string"},"offset":{"type":"integer","minimum":0},"max_bytes":{"type":"integer","minimum":1,"maximum":49152}}), vec!["context_ref"]),
        ("search_context", "Search a context snapshot for a case-sensitive literal, without model calls. Follow next_cursor even when a scan page has no hits. Cursors are bound to the reference and query. Results and scan work are bounded.", json!({"context_ref":{"type":"string"},"query":{"type":"string","minLength":1},"cursor":{"type":["string","null"]},"max_hits":{"type":"integer","minimum":1,"maximum":100},"max_bytes":{"type":"integer","minimum":1,"maximum":49152}}), vec!["context_ref","query"]),
    ].into_iter().map(|(name, description, properties, required)| ToolDefinition {
        tool_type: ToolType::Function, name: name.into(), description: description.into(),
        parameters: json!({"type":"object","properties":properties,"required":required,"additionalProperties":false}),
    }).collect()
}

pub(crate) struct TaskContext {
    pub scope: ContextScope,
    pub resources: ContextStore,
    pub sessions: Arc<SessionStore>,
}
impl Drop for TaskContext {
    fn drop(&mut self) {
        self.resources.finish_task(&self.scope);
    }
}

pub(crate) fn install(registry: &mut ToolRegistry, selected: &[String], task: &TaskContext) {
    for definition in definitions()
        .into_iter()
        .filter(|definition| selected.contains(&definition.name))
    {
        let scope = task.scope.clone();
        let resources = task.resources.clone();
        let sessions = task.sessions.clone();
        let name = definition.name.clone();
        registry.registry(Tool {
            definition,
            handler: Arc::new(move |arguments| {
                let scope = scope.clone();
                let resources = resources.clone();
                let sessions = sessions.clone();
                let name = name.clone();
                Box::pin(async move {
                    let cancellation = ReadCancellation::new();
                    let cancel = cancellation.flag.clone();
                    tokio::task::spawn_blocking(move || match name.as_str() {
                        "open_context" => output(resources.open(
                            &sessions,
                            decode::<OpenContext>(arguments)?,
                            Some(&scope),
                            &cancel,
                        )),
                        "read_context" => {
                            output(resources.read(decode::<ReadContext>(arguments)?, Some(&scope)))
                        }
                        "search_context" => output(
                            resources.search(decode::<SearchContext>(arguments)?, Some(&scope)),
                        ),
                        _ => unreachable!(),
                    })
                    .await
                    .map_err(|_| ToolError::ExecutionFailed("context operation failed".into()))?
                })
            }),
        });
    }
}

fn decode<T: DeserializeOwned>(arguments: Value) -> Result<T, ToolError> {
    serde_json::from_value(arguments)
        .map_err(|_| ToolError::InvalidArgs("invalid context tool parameters".into()))
}
fn output<T: Serialize>(result: Result<T, RuntimeError>) -> Result<Value, ToolError> {
    let value = match result {
        Ok(value) => serde_json::to_value(value),
        Err(error) => serde_json::to_value(json!({"error":error})),
    }
    .map_err(|_| ToolError::ExecutionFailed("cannot encode context result".into()))?;
    let mut size = ByteCounter::new(RESULT_LIMIT.min(MAX_OUTPUT_BYTES));
    serde_json::to_writer(&mut size, &value).map_err(|_| {
        ToolError::ExecutionFailed("context result exceeds its serialized budget".into())
    })?;
    Ok(value)
}
