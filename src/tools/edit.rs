use std::sync::Arc;

use serde::Deserialize;
use serde_json::{Value, json};

use super::tool_registry::{Tool, ToolError};
use super::{ToolContext, parse_args};
use crate::protocol::{ToolDefinition, ToolType};

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct EditArgs {
    path: String,
    edits: Vec<Replacement>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct Replacement {
    old_text: String,
    new_text: String,
}

fn original_offset(original: &str, normalized_offset: usize) -> usize {
    let bytes = original.as_bytes();
    let mut raw = 0;
    let mut normalized = 0;
    while normalized < normalized_offset {
        raw += if bytes[raw..].starts_with(b"\r\n") {
            2
        } else {
            1
        };
        normalized += 1;
    }
    raw
}

fn line_ending(original: &str, start: usize) -> &'static str {
    let text = &original[start..];
    let text = if text.contains('\n') { text } else { original };
    match text.find('\n') {
        Some(index) if index > 0 && text.as_bytes()[index - 1] == b'\r' => "\r\n",
        _ => "\n",
    }
}

async fn edit(args: Value, context: &ToolContext) -> Result<Value, ToolError> {
    let args: EditArgs = parse_args(args)?;
    if args.edits.is_empty() {
        return Err(ToolError::InvalidArgs("edits must not be empty".to_owned()));
    }
    let path = context.resolve_path(&args.path);
    let original = tokio::fs::read_to_string(&path).await?;
    let (bom, original) = match original.strip_prefix('\u{feff}') {
        Some(text) => ("\u{feff}", text),
        None => ("", original.as_str()),
    };
    let normalized = original.replace("\r\n", "\n");
    let mut matches = Vec::new();
    for (index, replacement) in args.edits.iter().enumerate() {
        let old = replacement.old_text.replace("\r\n", "\n");
        if old.is_empty() {
            return Err(ToolError::InvalidArgs(format!(
                "edits[{index}].oldText must not be empty"
            )));
        }
        let Some(start) = normalized.find(&old) else {
            return Err(ToolError::ExecutionFailed(format!(
                "edits[{index}].oldText was not found; read the file again"
            )));
        };
        let after_first = start + normalized[start..].chars().next().unwrap().len_utf8();
        if normalized[after_first..].contains(&old) {
            return Err(ToolError::ExecutionFailed(format!(
                "edits[{index}].oldText matches more than once; include unique context"
            )));
        }
        let end = original_offset(original, start + old.len());
        let start = original_offset(original, start);
        let new_text = replacement
            .new_text
            .replace("\r\n", "\n")
            .replace('\n', line_ending(original, start));
        matches.push((start, end, index, new_text));
    }
    matches.sort_by_key(|item| item.0);
    for pair in matches.windows(2) {
        if pair[0].1 > pair[1].0 {
            return Err(ToolError::ExecutionFailed(format!(
                "edits[{}] overlaps edits[{}]; merge the replacements",
                pair[0].2, pair[1].2
            )));
        }
    }
    let mut updated = original.to_owned();
    for (start, end, _, new_text) in matches.iter().rev() {
        updated.replace_range(*start..*end, new_text);
    }
    tokio::fs::write(&path, format!("{bom}{updated}")).await?;
    Ok(json!({"path":path, "applied":matches.len()}))
}

pub fn init(context: Arc<ToolContext>) -> Tool {
    Tool {
        definition: ToolDefinition {
            tool_type: ToolType::Function,
            name: "edit".to_owned(),
            description: "Make targeted edits to an existing UTF-8 file. Every oldText must match uniquely in the original file; edits must not overlap. All replacements are checked before writing. LF/CRLF are equivalent for matching; BOM and untouched bytes are preserved. Read the file before editing. Use write for new files or full rewrites.".to_owned(),
            parameters: json!({"type":"object", "properties":{
                "path":{"type":"string"}, "edits":{"type":"array","minItems":1,"items":{
                    "type":"object","properties":{"oldText":{"type":"string","minLength":1},"newText":{"type":"string"}},
                    "required":["oldText","newText"],"additionalProperties":false}}},
                "required":["path","edits"], "additionalProperties":false}),
        },
        handler: Arc::new(move |args| { let context = context.clone(); Box::pin(async move { edit(args, &context).await }) }),
    }
}
