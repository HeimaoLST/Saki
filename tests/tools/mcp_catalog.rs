use super::*;
use rmcp::model::{JsonObject, Tool, ToolAnnotations};
use serde_json::{Value, json};
use std::sync::Arc;

fn schema(value: Value) -> Arc<JsonObject> {
    Arc::new(
        value
            .as_object()
            .expect("test tool schema must be an object")
            .clone(),
    )
}

fn tool(description: &str, input: Value, output: Option<Value>) -> Tool {
    let mut tool = Tool::new("test_tool", description.to_owned(), schema(input));
    if let Some(output) = output {
        tool = tool.with_raw_output_schema(schema(output));
    }
    tool
}

fn prepared(tool: &Tool) -> PreparedToolSchema {
    prepare_tool_schema(tool).expect("test schema should prepare")
}

#[test]
fn fingerprint_ignores_object_key_order_but_preserves_array_order() {
    let input_a = json!({
        "type": "object",
        "properties": {
            "alpha": {"type": "string"},
            "beta": {"type": "integer"}
        },
        "required": ["alpha", "beta"]
    });
    let input_b = json!({
        "required": ["alpha", "beta"],
        "properties": {
            "beta": {"type": "integer"},
            "alpha": {"type": "string"}
        },
        "type": "object"
    });
    let first = prepared(&tool("same description", input_a.clone(), None));
    let second = prepared(&tool("same description", input_b, None));
    assert_eq!(first.schema.fingerprint, second.schema.fingerprint);

    let reordered_array = json!({
        "type": "object",
        "properties": {
            "alpha": {"type": "string"},
            "beta": {"type": "integer"}
        },
        "required": ["beta", "alpha"]
    });
    let third = prepared(&tool("same description", reordered_array, None));
    assert_ne!(first.schema.fingerprint, third.schema.fingerprint);
}

#[test]
fn fingerprint_changes_for_description_input_and_output() {
    let input = json!({"type": "string"});
    let base = prepared(&tool(
        "description",
        input.clone(),
        Some(json!({"type": "string"})),
    ));
    let description_changed = prepared(&tool(
        "different description",
        input.clone(),
        Some(json!({"type": "string"})),
    ));
    let input_changed = prepared(&tool(
        "description",
        json!({"type": "integer"}),
        Some(json!({"type": "string"})),
    ));
    let output_changed = prepared(&tool("description", input, Some(json!({"type": "number"}))));

    assert_ne!(
        base.schema.fingerprint,
        description_changed.schema.fingerprint
    );
    assert_ne!(base.schema.fingerprint, input_changed.schema.fingerprint);
    assert_ne!(base.schema.fingerprint, output_changed.schema.fingerprint);
}

#[test]
fn fingerprint_ignores_tool_title_and_annotations() {
    let input = json!({"type": "string"});
    let first = prepared(
        &tool("description", input.clone(), None)
            .with_title("first title")
            .with_annotations(ToolAnnotations::new().read_only(true)),
    );
    let second = prepared(
        &tool("description", input, None)
            .with_title("second title")
            .with_annotations(ToolAnnotations::new().read_only(false)),
    );
    assert_eq!(first.schema.fingerprint, second.schema.fingerprint);
}

#[test]
fn invalid_input_and_output_schemas_are_rejected() {
    let invalid_input = tool("description", json!({"type": 42}), None);
    let error = prepare_tool_schema(&invalid_input)
        .err()
        .expect("invalid input schema must fail");
    assert!(error.to_string().contains("invalid input schema"));

    let invalid_output = tool(
        "description",
        json!({"type": "string"}),
        Some(json!({"type": 42})),
    );
    let error = prepare_tool_schema(&invalid_output)
        .err()
        .expect("invalid output schema must fail");
    assert!(error.to_string().contains("invalid output schema"));
}

#[test]
fn compiled_input_and_structured_output_are_checked() {
    let prepared = prepared(&tool(
        "checked",
        json!({
            "type": "object",
            "properties": {"name": {"type": "string"}},
            "required": ["name"],
            "additionalProperties": false
        }),
        Some(json!({
            "type": "object",
            "properties": {"ok": {"type": "boolean"}},
            "required": ["ok"],
            "additionalProperties": false
        })),
    ));

    prepared
        .validate_input(&json!({"name": "valid"}))
        .expect("valid input");
    assert!(prepared.validate_input(&json!({"name": 1})).is_err());

    prepared
        .validate_output(Some(&json!({"ok": true})))
        .expect("valid structured output");
    assert!(
        prepared
            .validate_output(Some(&json!({"ok": "not a boolean"})))
            .is_err()
    );
    assert!(prepared.validate_output(None).is_err());
}

#[test]
fn missing_output_schema_accepts_optional_structured_content() {
    let prepared = prepared(&tool("no output", json!({"type": "string"}), None));
    prepared
        .validate_output(None)
        .expect("absent structured output");
    prepared
        .validate_output(Some(&json!({"anything": true})))
        .expect("unconstrained structured output");
}

#[test]
fn explicit_schema_draft_is_honored() {
    let input = json!({
        "$schema": "http://json-schema.org/draft-07/schema#",
        "type": "array",
        "items": [{"type": "string"}]
    });
    let prepared = prepared(&tool("draft seven", input, None));

    prepared
        .validate_input(&json!(["valid"]))
        .expect("draft-07 tuple validation");
    assert!(prepared.validate_input(&json!([1])).is_err());
}

#[test]
fn local_refs_and_composition_without_top_level_object_compile_and_validate() {
    let input = json!({
        "$defs": {
            "word": {
                "type": "string",
                "minLength": 2
            }
        },
        "allOf": [
            {"$ref": "#/$defs/word"},
            {"not": {"const": "forbidden"}}
        ]
    });
    let prepared = prepared(&tool("local refs", input, None));

    prepared.validate_input(&json!("ok")).expect("valid word");
    assert!(prepared.validate_input(&json!("a")).is_err());
    assert!(prepared.validate_input(&json!("forbidden")).is_err());
}

#[test]
fn remote_references_fail_without_a_retriever() {
    let input = json!({"$ref": "https://example.invalid/schema.json"});
    let error = prepare_tool_schema(&tool("remote", input, None))
        .err()
        .expect("remote references must fail offline");
    assert!(!error.to_string().is_empty());
}
