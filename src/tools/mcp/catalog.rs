use anyhow::{Result, anyhow, bail};
use rmcp::model::Tool;
use serde_json::{Map, Value, json};
use sha2::{Digest, Sha256};

use crate::protocol::ToolSchema;

pub(super) struct PreparedToolSchema {
    pub(super) schema: ToolSchema,
    pub(super) input: jsonschema::Validator,
    pub(super) output: Option<jsonschema::Validator>,
}

pub(super) fn prepare_tool_schema(tool: &Tool) -> Result<PreparedToolSchema> {
    let description = tool.description.as_deref().unwrap_or("").to_owned();
    let input = Value::Object(tool.input_schema.as_ref().clone());
    let output = tool
        .output_schema
        .as_ref()
        .map(|schema| Value::Object(schema.as_ref().clone()));

    let input_validator = jsonschema::validator_for(&input)
        .map_err(|error| anyhow!("invalid input schema: {error}"))?;
    let output_validator = match output.as_ref() {
        Some(schema) => Some(
            jsonschema::validator_for(schema)
                .map_err(|error| anyhow!("invalid output schema: {error}"))?,
        ),
        None => None,
    };
    let fingerprint = fingerprint(&description, &input, output.as_ref());

    Ok(PreparedToolSchema {
        schema: ToolSchema {
            description,
            input_schema: input,
            output_schema: output,
            fingerprint,
        },
        input: input_validator,
        output: output_validator,
    })
}

impl PreparedToolSchema {
    pub(super) fn validate_input(&self, value: &Value) -> Result<()> {
        self.input
            .validate(value)
            .map_err(|error| anyhow!("input validation failed: {error}"))
    }

    pub(super) fn validate_output(&self, value: Option<&Value>) -> Result<()> {
        let Some(output) = &self.output else {
            return Ok(());
        };
        let Some(value) = value else {
            bail!("successful result is missing structuredContent required by outputSchema");
        };
        output
            .validate(value)
            .map_err(|error| anyhow!("output validation failed: {error}"))
    }
}

fn fingerprint(description: &str, input: &Value, output: Option<&Value>) -> String {
    let declaration = json!({
        "description": description,
        "input": input,
        "output": output,
    });
    let canonical = canonicalize(&declaration);
    let bytes = serde_json::to_vec(&canonical).expect("JSON value is serializable");
    let mut hasher = Sha256::new();
    hasher.update(bytes);
    format!("{:x}", hasher.finalize())
}

fn canonicalize(value: &Value) -> Value {
    match value {
        Value::Object(map) => {
            let mut keys: Vec<_> = map.keys().collect();
            keys.sort();
            let mut canonical = Map::new();
            for key in keys {
                let value = map.get(key.as_str()).expect("key from map");
                let _ = canonical.insert(key.clone(), canonicalize(value));
            }
            Value::Object(canonical)
        }
        Value::Array(values) => Value::Array(values.iter().map(canonicalize).collect()),
        other => other.clone(),
    }
}

#[cfg(test)]
#[path = "../../../tests/tools/mcp_catalog.rs"]
mod tests;
