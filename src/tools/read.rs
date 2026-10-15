use std::sync::Arc;

use serde::Deserialize;
use serde_json::{Value, json};
use tokio::{
    fs::File,
    io::{AsyncBufReadExt, BufReader},
};

use super::tool_registry::{Tool, ToolError};
use super::{
    ToolContext,
    output::{MAX_OUTPUT_BYTES, MAX_OUTPUT_LINES},
    parse_args,
};
use crate::protocol::{ToolDefinition, ToolType};

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ReadArgs {
    path: String,
    #[serde(default = "first_line")]
    offset: usize,
    #[serde(default = "default_limit")]
    limit: usize,
}

fn first_line() -> usize {
    1
}
fn default_limit() -> usize {
    MAX_OUTPUT_LINES
}

struct Line {
    bytes: Vec<u8>,
    oversized: bool,
}

async fn read_line(reader: &mut BufReader<File>, collect: bool) -> Result<Option<Line>, ToolError> {
    let mut bytes = Vec::new();
    let mut seen = false;
    loop {
        let buffer = reader.fill_buf().await?;
        if buffer.is_empty() {
            return Ok(seen.then_some(Line {
                bytes,
                oversized: false,
            }));
        }
        seen = true;
        let newline = buffer.iter().position(|byte| *byte == b'\n');
        let length = newline.map_or(buffer.len(), |index| index + 1);
        if collect {
            if bytes.len() + length > MAX_OUTPUT_BYTES {
                return Ok(Some(Line {
                    bytes: Vec::new(),
                    oversized: true,
                }));
            }
            bytes.extend_from_slice(&buffer[..length]);
        }
        reader.consume(length);
        if newline.is_some() {
            return Ok(Some(Line {
                bytes,
                oversized: false,
            }));
        }
    }
}

async fn read(args: Value, context: &ToolContext) -> Result<Value, ToolError> {
    let args: ReadArgs = parse_args(args)?;
    if args.offset == 0 || args.limit == 0 {
        return Err(ToolError::InvalidArgs(
            "offset and limit must be positive".to_owned(),
        ));
    }
    let mut reader = BufReader::new(File::open(context.resolve_path(&args.path)).await?);
    for _ in 1..args.offset {
        if read_line(&mut reader, false).await?.is_none() {
            return Ok(
                json!({"content":"", "start_line":args.offset, "end_line":args.offset - 1,
                "truncated":false, "next_offset":null}),
            );
        }
    }
    let mut content = String::new();
    let mut count = 0;
    let mut next_offset = None;
    let mut note = None;
    while count < args.limit.min(MAX_OUTPUT_LINES) {
        let Some(line) = read_line(&mut reader, true).await? else {
            break;
        };
        if line.oversized {
            if count == 0 {
                note = Some(format!(
                    "Line {} exceeds the {} byte limit. Use bash to read a byte slice of this line; read cannot paginate within a line.",
                    args.offset, MAX_OUTPUT_BYTES
                ));
            } else {
                next_offset = Some(args.offset + count);
            }
            break;
        }
        if content.len() + line.bytes.len() > MAX_OUTPUT_BYTES {
            next_offset = Some(args.offset + count);
            break;
        }
        content.push_str(std::str::from_utf8(&line.bytes).map_err(|error| {
            ToolError::ExecutionFailed(format!("File is not UTF-8 text: {error}"))
        })?);
        count += 1;
    }
    if note.is_none() && next_offset.is_none() && !reader.fill_buf().await?.is_empty() {
        next_offset = Some(args.offset + count);
    }
    let mut result = json!({"content":content, "start_line":args.offset,
        "end_line":args.offset + count - 1, "truncated":next_offset.is_some() || note.is_some(),
        "next_offset":next_offset});
    if let Some(note) = note {
        result["note"] = json!(note);
    }
    Ok(result)
}

pub fn init(context: Arc<ToolContext>) -> Tool {
    Tool {
        definition: ToolDefinition {
            tool_type: ToolType::Function,
            name: "read".to_owned(),
            description: "Read UTF-8 text, preserving original content. offset is a 1-based line number. Returns at most 2000 lines / 50 KiB with next_offset to continue. Use bash for directories, binary files, and byte slices of oversized lines.".to_owned(),
            parameters: json!({"type":"object", "properties":{
                "path":{"type":"string"}, "offset":{"type":"integer","minimum":1},
                "limit":{"type":"integer","minimum":1}}, "required":["path"], "additionalProperties":false}),
        },
        handler: Arc::new(move |args| { let context = context.clone(); Box::pin(async move { read(args, &context).await }) }),
    }
}
