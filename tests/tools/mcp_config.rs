use super::*;
use serde_json::{Value, json};
use std::{
    cell::RefCell,
    path::{Path, PathBuf},
};

fn valid() -> Value {
    json!({
        "id": "source",
        "transport": "stdio",
        "command": "bin/tool",
        "args": ["--flag", "value"],
        "env_from": [],
        "protocol": "auto",
        "startup_timeout_ms": 10,
        "call_timeout_ms": 20,
        "cancel_grace_ms": 30
    })
}

fn valid_definition() -> ToolSourceDefinition {
    serde_json::from_value(valid()).unwrap()
}

#[test]
fn rejects_unknown_transport_unknown_fields_missing_transport_and_nulls() {
    let mut value = valid();
    value["transport"] = json!("sse");
    assert!(serde_json::from_value::<ToolSourceDefinition>(value).is_err());

    let mut value = valid();
    value["extra"] = json!(true);
    assert!(serde_json::from_value::<ToolSourceDefinition>(value).is_err());

    let mut value = valid();
    value.as_object_mut().unwrap().remove("transport");
    assert!(serde_json::from_value::<ToolSourceDefinition>(value).is_err());

    for field in [
        "id",
        "transport",
        "command",
        "args",
        "env_from",
        "protocol",
        "startup_timeout_ms",
        "call_timeout_ms",
        "cancel_grace_ms",
    ] {
        let mut value = valid();
        value[field] = Value::Null;
        assert!(
            serde_json::from_value::<ToolSourceDefinition>(value).is_err(),
            "{field} must reject null"
        );
    }
}

#[test]
fn resolves_relative_command_and_keeps_absolute_command_without_io() {
    let base = std::env::temp_dir().join("saki-mcp-config-base-that-does-not-exist");
    let mut definition = valid_definition();
    definition.command = PathBuf::from("missing/bin/tool");
    let source = definition
        .prepare(&base, |_| None)
        .expect("prepare must not require the command to exist");
    assert_eq!(source.command, base.join("missing/bin/tool"));

    let absolute = std::env::temp_dir().join("saki-mcp-absolute-tool-that-does-not-exist");
    definition.command = absolute.clone();
    let source = definition.prepare(&base, |_| None).unwrap();
    assert_eq!(source.command, absolute);

    definition.command = PathBuf::from("relative/tool");
    assert!(
        definition
            .prepare(Path::new("relative-base"), |_| None)
            .is_err()
    );
}

#[test]
fn copies_only_named_environment_and_deduplicates_names() {
    let mut definition = valid_definition();
    definition.env_from = vec!["TOKEN".to_owned(), "TOKEN".to_owned()];
    let calls = RefCell::new(Vec::new());
    let source = definition
        .prepare(Path::new("/base"), |name| {
            calls.borrow_mut().push(name.to_owned());
            if name == "TOKEN" {
                Some("secret-value".to_owned())
            } else {
                Some("must-not-be-copied".to_owned())
            }
        })
        .unwrap();

    assert_eq!(calls.borrow().len(), 1);
    assert_eq!(calls.borrow().first().map(String::as_str), Some("TOKEN"));
    assert_eq!(source.environment.len(), 1);
    assert_eq!(
        source.environment.get("TOKEN").map(String::as_str),
        Some("secret-value")
    );
    assert!(!source.environment.contains_key("HOME"));
}

#[test]
fn missing_environment_reports_only_the_variable_name() {
    let mut definition = valid_definition();
    definition.id = "source-with-secret-id".to_owned();
    definition.env_from = vec!["MISSING_TOKEN".to_owned()];

    let error = definition
        .prepare(Path::new("/base"), |name| {
            assert_eq!(name, "MISSING_TOKEN");
            None
        })
        .err()
        .expect("missing environment must fail");

    let message = format!("{error}");
    assert!(message.contains("MISSING_TOKEN"));
    assert!(!message.contains("source-with-secret-id"));
    assert!(!message.contains("secret-value"));
}

#[test]
fn rejects_empty_identity_command_and_zero_timers() {
    let mut definition = valid_definition();
    definition.id.clear();
    assert!(definition.prepare(Path::new("/base"), |_| None).is_err());

    let mut definition = valid_definition();
    definition.command = PathBuf::new();
    assert!(definition.prepare(Path::new("/base"), |_| None).is_err());

    for field in ["startup_timeout_ms", "call_timeout_ms", "cancel_grace_ms"] {
        let mut definition = valid_definition();
        match field {
            "startup_timeout_ms" => definition.startup_timeout_ms = 0,
            "call_timeout_ms" => definition.call_timeout_ms = 0,
            "cancel_grace_ms" => definition.cancel_grace_ms = 0,
            _ => unreachable!(),
        }
        assert!(definition.prepare(Path::new("/base"), |_| None).is_err());
    }
}
