use std::sync::Arc;

use serde::Deserialize;
use serde_json::{Value, json};

use super::tool_registry::{Tool, ToolError};
use super::{ToolContext, parse_args};
use crate::protocol::{ToolDefinition, ToolType};

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct WriteArgs {
    path: String,
    content: String,
}

async fn write(args: Value, context: &ToolContext) -> Result<Value, ToolError> {
    let args: WriteArgs = parse_args(args)?;
    let path = context.resolve_path(&args.path);
    if let Some(parent) = path.parent() {
        tokio::fs::create_dir_all(parent).await?;
    }
    tokio::fs::write(&path, &args.content).await?;
    Ok(json!({"path":path, "bytes_written":args.content.len()}))
}

pub fn init(context: Arc<ToolContext>) -> Tool {
    Tool {
        definition: ToolDefinition {
            tool_type: ToolType::Function,
            name: "write".to_owned(),
            description: "Create or overwrite a UTF-8 file, creating missing parent directories. Use for new files or complete rewrites; prefer edit for targeted changes. Does not echo file contents.".to_owned(),
            parameters: json!({"type":"object", "properties":{
                "path":{"type":"string"}, "content":{"type":"string"}},
                "required":["path","content"], "additionalProperties":false}),
        },
        handler: Arc::new(move |args| { let context = context.clone(); Box::pin(async move { write(args, &context).await }) }),
    }
}
