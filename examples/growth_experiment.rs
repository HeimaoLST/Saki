//! SAKI growth experiment harness (pilot).
//!
//! Scope protocol: docs/experiments/history-dependence.md
//! Reuses the existing client/protocol via `#[path]`; no duplicate client, no library target.
//!
//! Offline: `cargo test --locked --example growth_experiment`
//! Real:    `zsh -lic 'cd /Users/heimaolst/Workspace/Rust/Saki && \
//!           cargo run --locked --example growth_experiment -- --real --run-id <id> \
//!           --model <name> --base-url <url>'`

// The experiment imports only a subset of each module via `#[path]`, so unused
// items are expected. `not(test)` guards the expectation because the cfg(test)
// build also loads those modules' own test suites, which use more of the API.
#[cfg_attr(
    not(test),
    expect(dead_code, reason = "example uses a subset via #[path]")
)]
#[path = "../src/agent"]
mod agent {
    pub mod conversation;
    pub mod journal;
    pub mod message;
}
#[cfg_attr(
    not(test),
    expect(dead_code, reason = "example uses a subset via #[path]")
)]
#[path = "../src/client/mod.rs"]
mod client;
#[cfg_attr(
    not(test),
    expect(dead_code, reason = "example uses a subset via #[path]")
)]
#[path = "../src/protocol/mod.rs"]
mod protocol;

// Reuse the existing tool protocol by path, re-exported so
// Reuse the registry while keeping tool execution outside the protocol module.
#[cfg_attr(
    not(test),
    expect(dead_code, reason = "example uses a subset via #[path]")
)]
#[path = "../src/tools/tool_registry.rs"]
mod tool_registry_impl;
mod tools {
    pub mod tool_registry {
        pub use crate::tool_registry_impl::*;
    }
}

// Second crate root: the cfg(test) build loads tests/client/{mod,stream}.rs via
// the imported modules, and those refer to `crate::test_support`.
#[cfg(test)]
#[path = "../tests/support/mod.rs"]
mod test_support;

use std::collections::{BTreeMap, VecDeque};
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use serde::{Deserialize, Serialize};
use serde_json::{Value, json};

use agent::conversation::Conversation;
use client::ResponsesClient;
use protocol::{ModelRequest, ModelResponse, OutputItem, OutputText, ToolDefinition, ToolType};
use tools::tool_registry::{Tool, ToolCall, ToolError, ToolRegistry};

const NO_UPDATE: &str = "NO_UPDATE";

// --- Fixture: quirkbench (deterministic; arbitrary local convention) ---------

const DEFAULT_CONFIG: &[(&str, &str)] = &[
    ("retry_budget", "1"),
    ("drain_timeout", "0"),
    ("mode", "fast"),
];

fn required_for(symptom: &str) -> BTreeMap<&'static str, &'static str> {
    match symptom {
        "E17" => BTreeMap::from([("retry_budget", "3")]),
        "E42" => BTreeMap::from([("drain_timeout", "7")]),
        "E99" => BTreeMap::from([("mode", "safe")]),
        _ => BTreeMap::new(),
    }
}

fn resolve_for(symptom: &str) -> Option<(&'static str, &'static str)> {
    match symptom {
        "E17" => Some(("retry_budget", "3")),
        "E42" => Some(("drain_timeout", "7")),
        // Stale/plausible-but-wrong entry for the boundary symptom.
        "E99" => Some(("retry_budget", "3")),
        _ => None,
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
struct RecordedCall {
    tool: String,
    args: Value,
    result: Value,
}

struct FixtureState {
    config: BTreeMap<String, String>,
    symptom: String,
    calls: Vec<RecordedCall>,
    tool_errors: u32,
}

#[derive(Clone)]
struct Fixture {
    state: Arc<Mutex<FixtureState>>,
}

impl Fixture {
    fn new() -> Self {
        Self {
            state: Arc::new(Mutex::new(FixtureState {
                config: BTreeMap::new(),
                symptom: String::new(),
                calls: Vec::new(),
                tool_errors: 0,
            })),
        }
    }

    fn reset(&self, symptom: &str) {
        let mut s = self.state.lock().unwrap();
        s.config = DEFAULT_CONFIG
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect();
        s.symptom = symptom.to_string();
        s.calls.clear();
        s.tool_errors = 0;
    }

    fn snapshot(&self) -> (Vec<RecordedCall>, u32, String) {
        let s = self.state.lock().unwrap();
        (s.calls.clone(), s.tool_errors, s.symptom.clone())
    }

    fn check(&self) -> (bool, Vec<String>) {
        let s = self.state.lock().unwrap();
        let required = required_for(&s.symptom);
        let pass = required
            .iter()
            .all(|(k, v)| s.config.get(*k).map(String::as_str) == Some(*v));
        (pass, vec![s.symptom.clone()])
    }

    fn record(&self, tool: &str, args: Value, result: Value) {
        self.state.lock().unwrap().calls.push(RecordedCall {
            tool: tool.to_string(),
            args,
            result,
        });
    }

    /// Record an invalid/errored tool call so it appears in the real trace.
    fn record_error(&self, tool: &str, args: Value, message: &str) {
        let mut s = self.state.lock().unwrap();
        s.tool_errors += 1;
        s.calls.push(RecordedCall {
            tool: tool.to_string(),
            args,
            result: json!({ "error": message }),
        });
    }

    fn read(&self, args: Value) -> Result<Value, ToolError> {
        let path = args["path"].as_str().unwrap_or_default().to_string();
        let result = match path.as_str() {
            "README.md" => json!({
                "content": "Runbook: for any check failure, increase retry_budget."
            }),
            "config/current.json" => {
                let s = self.state.lock().unwrap();
                json!({ "content": serde_json::to_string(&s.config).unwrap_or_default() })
            }
            _ => return Err(ToolError::InvalidArgs(format!("unknown path: {path}"))),
        };
        self.record("read", args, result.clone());
        Ok(result)
    }

    fn run(&self, args: Value) -> Result<Value, ToolError> {
        let command = args["command"].as_str().unwrap_or_default().to_string();
        let mut parts = command.split_whitespace();
        let verb = parts.next().unwrap_or_default();
        let result = match verb {
            "check" => {
                let (pass, symptoms) = self.check();
                json!({ "pass": pass, "symptoms": symptoms })
            }
            "inspect" => {
                let s = self.state.lock().unwrap();
                json!({ "symptom": s.symptom, "observed_required": required_for(&s.symptom) })
            }
            "resolve" => match parts.next().and_then(resolve_for) {
                Some((key, value)) => json!({ "key": key, "value": value }),
                None => json!({ "unmapped": true }),
            },
            _ => {
                return Err(ToolError::InvalidArgs(format!(
                    "unknown command: {command}"
                )));
            }
        };
        self.record("run", args, result.clone());
        Ok(result)
    }

    fn edit(&self, args: Value) -> Result<Value, ToolError> {
        let key = args["key"].as_str().unwrap_or_default().to_string();
        let value = args["value"].as_str().unwrap_or_default().to_string();
        if key.is_empty() {
            return Err(ToolError::InvalidArgs("key is required".to_string()));
        }
        {
            let mut s = self.state.lock().unwrap();
            s.config.insert(key, value);
        }
        let (pass, symptoms) = self.check();
        let result = json!({ "applied": true, "check": { "pass": pass, "symptoms": symptoms } });
        self.record("edit", args, result.clone());
        Ok(result)
    }
}

fn make_tool<F>(name: &str, description: &str, parameters: Value, f: Arc<F>) -> Tool
where
    F: Fn(Value) -> Result<Value, ToolError> + Send + Sync + 'static,
{
    Tool {
        definition: ToolDefinition {
            tool_type: ToolType::Function,
            name: name.to_string(),
            description: description.to_string(),
            parameters,
        },
        handler: Arc::new(move |args| Box::pin(std::future::ready(f(args)))),
    }
}

fn build_registry(fixture: &Fixture) -> ToolRegistry {
    let mut reg = ToolRegistry::new();
    let f = fixture.clone();
    reg.registry(make_tool(
        "read",
        "Read an allowed file (README.md, config/current.json).",
        json!({"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}),
        Arc::new(move |a| f.read(a)),
    ));
    let f = fixture.clone();
    reg.registry(make_tool(
        "run",
        "Run a limited command: `check`; `inspect` (observed required conditions); `resolve <code>` (table lookup).",
        json!({"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}),
        Arc::new(move |a| f.run(a)),
    ));
    let f = fixture.clone();
    reg.registry(make_tool(
        "edit",
        "Set a config key to a value and re-run check.",
        json!({"type":"object","properties":{"key":{"type":"string"},"value":{"type":"string"}},"required":["key","value"]}),
        Arc::new(move |a| f.edit(a)),
    ));
    reg
}

// --- Backend + budget --------------------------------------------------------

struct GenParams {
    max_output_tokens: Option<u32>,
    temperature: Option<f32>,
    reasoning: Option<String>,
}

impl GenParams {
    fn pilot() -> Self {
        Self {
            // 4096 accommodates reasoning tokens. `none` is only a request
            // setting; the proxy did not guarantee thinking is disabled.
            max_output_tokens: Some(4096),
            temperature: Some(0.0),
            reasoning: Some("none".to_string()),
        }
    }
}

struct Completion {
    response: Option<ModelResponse>,
    attempts: u8,
    error: Option<String>,
}

enum Backend {
    Real {
        client: ResponsesClient,
        model: String,
    },
    #[cfg_attr(not(test), expect(dead_code, reason = "constructed only in cfg(test)"))]
    Scripted(Mutex<VecDeque<ModelResponse>>),
}

impl Backend {
    fn model_name(&self) -> String {
        match self {
            Backend::Real { model, .. } => model.clone(),
            Backend::Scripted(_) => "scripted".to_string(),
        }
    }

    /// Returns the completion and the full serialized request body (no headers).
    async fn complete(
        &self,
        conv: &Conversation,
        tools: Vec<ToolDefinition>,
        generation: &GenParams,
    ) -> (Completion, Value) {
        let req = ModelRequest::new(self.model_name(), conv.items(), tools).with_generation(
            generation.max_output_tokens,
            generation.temperature,
            generation.reasoning.clone(),
        );
        let request_body = ResponsesClient::request_body(&req).unwrap_or(Value::Null);
        match self {
            Backend::Real { client, .. } => {
                let attempted = client.respond_once(req).await;
                let completion = match attempted.result {
                    Ok(response) => Completion {
                        response: Some(response),
                        attempts: attempted.attempts,
                        error: None,
                    },
                    Err(e) => Completion {
                        response: None,
                        attempts: attempted.attempts,
                        error: Some(e.to_string()),
                    },
                };
                (completion, request_body)
            }
            Backend::Scripted(q) => {
                let completion = match q.lock().unwrap().pop_front() {
                    Some(response) => Completion {
                        response: Some(response),
                        attempts: 1,
                        error: None,
                    },
                    None => Completion {
                        response: None,
                        attempts: 0,
                        error: Some("scripted backend exhausted".to_string()),
                    },
                };
                (completion, request_body)
            }
        }
    }
}

struct Budget {
    logical_used: u32,
    logical_cap: u32,
    http_attempts: u32,
    http_cap: u32,
}

impl Budget {
    fn new(logical_cap: u32) -> Self {
        Self {
            logical_used: 0,
            logical_cap,
            http_attempts: 0,
            http_cap: logical_cap,
        }
    }

    fn can_spend(&self) -> bool {
        self.logical_used < self.logical_cap && self.http_attempts < self.http_cap
    }

    fn spend(&mut self) {
        self.logical_used += 1;
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
enum StopReason {
    ObjectiveComplete,
    InstanceCallBudget,
    GlobalBudgetCap,
    ApiError,
    Truncated,
    NoFurtherAction,
}

#[derive(Debug, Serialize)]
struct CallRecord {
    index: u32,
    group: String,
    task: String,
    requested_model: String,
    returned_model: Option<String>,
    status: Option<String>,
    truncated: bool,
    attempts: u8,
    elapsed_ms: u64,
    input_tokens: Option<u64>,
    output_tokens: Option<u64>,
    output_tokens_details: Option<Value>,
    request_body: Value,
    response_output: Value,
    error: Option<String>,
    tool_calls: Vec<String>,
}

#[derive(Debug, Serialize)]
struct TaskOutcome {
    group: String,
    task: String,
    symptom: String,
    pass: bool,
    stop_reason: StopReason,
    first_action: String,
    model_calls: u32,
    tool_calls: u32,
    tool_errors: u32,
    reply: String,
}

fn first_action(calls: &[RecordedCall]) -> String {
    let Some(first) = calls.first() else {
        return "none".to_string();
    };
    match first.tool.as_str() {
        "run" => {
            let cmd = first.args["command"].as_str().unwrap_or_default();
            if cmd.starts_with("resolve") || cmd.starts_with("inspect") {
                "diagnostic_first".to_string()
            } else {
                format!("run:{cmd}")
            }
        }
        "read" => "diagnostic_first".to_string(),
        "edit" => "blind_first".to_string(),
        other => other.to_string(),
    }
}

// One CallOutcome per model call; keeping `Response` inline avoids boxing with
// no measured benefit.
#[expect(
    clippy::large_enum_variant,
    reason = "one per model call; boxing adds no benefit"
)]
enum CallOutcome {
    Response {
        resp: ModelResponse,
        truncated: bool,
    },
    ApiError(String),
    BudgetCap,
}

#[allow(clippy::too_many_arguments)]
async fn call_model(
    backend: &Backend,
    budget: &mut Budget,
    conv: &Conversation,
    tools: Vec<ToolDefinition>,
    generation: &GenParams,
    group: &str,
    task: &str,
    calls_out: &mut Vec<CallRecord>,
) -> CallOutcome {
    if !budget.can_spend() {
        return CallOutcome::BudgetCap;
    }
    budget.spend();
    let started = Instant::now();
    let (completion, request_body) = backend.complete(conv, tools, generation).await;
    let elapsed_ms = started.elapsed().as_millis() as u64;
    budget.http_attempts += completion.attempts as u32;

    match completion.error {
        Some(err) => {
            calls_out.push(CallRecord {
                index: calls_out.len() as u32,
                group: group.to_string(),
                task: task.to_string(),
                requested_model: backend.model_name(),
                returned_model: None,
                status: None,
                truncated: false,
                attempts: completion.attempts,
                elapsed_ms,
                input_tokens: None,
                output_tokens: None,
                output_tokens_details: None,
                request_body,
                response_output: Value::Null,
                error: Some(err.clone()),
                tool_calls: Vec::new(),
            });
            CallOutcome::ApiError(err)
        }
        None => {
            let resp = completion.response.expect("response present when no error");
            let truncated =
                resp.incomplete_details.is_some() || resp.status.as_deref() == Some("incomplete");
            let response_output = serde_json::to_value(&resp.output).unwrap_or(Value::Null);
            let tool_calls: Vec<String> = resp
                .output
                .iter()
                .filter_map(|item| match item {
                    OutputItem::ToolCall {
                        name, arguments, ..
                    } => Some(format!("{name}({arguments})")),
                    _ => None,
                })
                .collect();
            calls_out.push(CallRecord {
                index: calls_out.len() as u32,
                group: group.to_string(),
                task: task.to_string(),
                requested_model: backend.model_name(),
                returned_model: resp.model.clone(),
                status: resp.status.clone(),
                truncated,
                attempts: completion.attempts,
                elapsed_ms,
                input_tokens: resp.usage.as_ref().and_then(|u| u.input_tokens),
                output_tokens: resp.usage.as_ref().and_then(|u| u.output_tokens),
                output_tokens_details: resp
                    .usage
                    .as_ref()
                    .and_then(|u| u.output_tokens_details.clone()),
                request_body,
                response_output,
                error: None,
                tool_calls,
            });
            CallOutcome::Response { resp, truncated }
        }
    }
}

fn extract_text(resp: &ModelResponse) -> String {
    resp.output
        .iter()
        .filter_map(|item| match item {
            OutputItem::Message { content, .. } => {
                Some(content.iter().map(|o| o.text.clone()).collect::<String>())
            }
            _ => None,
        })
        .collect()
}

#[allow(clippy::too_many_arguments)]
async fn run_task(
    backend: &Backend,
    budget: &mut Budget,
    fixture: &Fixture,
    reg: &ToolRegistry,
    system_prompt: &str,
    user_message: &str,
    max_calls: u32,
    generation: &GenParams,
    group: &str,
    task: &str,
    calls_out: &mut Vec<CallRecord>,
) -> TaskOutcome {
    let mut conv = Conversation::new(system_prompt.to_string());
    conv.add_user_message(user_message.to_string());
    let mut reply = String::new();
    let start_index = calls_out.len();
    let mut stop = StopReason::InstanceCallBudget;

    for _ in 0..max_calls {
        match call_model(
            backend,
            budget,
            &conv,
            reg.list_definition(),
            generation,
            group,
            task,
            calls_out,
        )
        .await
        {
            CallOutcome::BudgetCap => {
                stop = StopReason::GlobalBudgetCap;
                break;
            }
            CallOutcome::ApiError(_) => {
                stop = StopReason::ApiError;
                break;
            }
            CallOutcome::Response { resp, truncated } => {
                let mut pending = Vec::new();
                for item in resp.output {
                    match item {
                        OutputItem::Message { content, .. } => {
                            let text: String = content
                                .into_iter()
                                .map(|OutputText { text }| text)
                                .collect();
                            conv.add_assistant_message(text.clone());
                            if !text.trim().is_empty() {
                                reply = text;
                            }
                        }
                        // Echo provider reasoning back into the conversation.
                        OutputItem::Reasoning {
                            content,
                            provider_data,
                            ..
                        } => conv.add_reasoning(content, provider_data),
                        OutputItem::ToolCall {
                            call_id,
                            name,
                            arguments,
                            ..
                        } => {
                            conv.add_tool_call(call_id.clone(), name.clone(), arguments.clone());
                            pending.push((call_id, name, arguments));
                        }
                        _ => {}
                    }
                }

                if truncated {
                    stop = StopReason::Truncated;
                    break;
                }
                if pending.is_empty() {
                    stop = if fixture.check().0 {
                        StopReason::ObjectiveComplete
                    } else {
                        StopReason::NoFurtherAction
                    };
                    break;
                }

                for (call_id, name, arguments) in pending {
                    match serde_json::from_str::<Value>(&arguments) {
                        Err(e) => {
                            let msg = format!("invalid tool arguments: {e}");
                            fixture.record_error(&name, json!({ "raw": arguments }), &msg);
                            conv.add_tool_result(
                                call_id,
                                serde_json::to_string(&json!({ "error": msg })).unwrap_or_default(),
                            );
                        }
                        Ok(args) => {
                            let call = ToolCall::new(call_id.clone(), name.clone(), args.clone());
                            match reg.execution_tool(call).await {
                                Ok(v) => conv.add_tool_result(
                                    call_id,
                                    serde_json::to_string(&v).unwrap_or_default(),
                                ),
                                Err(e) => {
                                    let msg = e.to_string();
                                    fixture.record_error(&name, args, &msg);
                                    conv.add_tool_result(
                                        call_id,
                                        serde_json::to_string(&json!({ "error": msg }))
                                            .unwrap_or_default(),
                                    );
                                }
                            }
                        }
                    }
                }

                // Objective success ends the instance immediately (no "confirm I'm done" call).
                if fixture.check().0 {
                    stop = StopReason::ObjectiveComplete;
                    break;
                }
            }
        }
    }

    let (calls, tool_errors, symptom) = fixture.snapshot();
    let (pass, _) = fixture.check();
    TaskOutcome {
        group: group.to_string(),
        task: task.to_string(),
        symptom,
        pass,
        stop_reason: stop,
        first_action: first_action(&calls),
        model_calls: (calls_out.len() - start_index) as u32,
        tool_calls: calls.len() as u32,
        tool_errors,
        reply,
    }
}

// --- State persistence -------------------------------------------------------

fn state_path(base: &Path, group: &str, key: &str) -> PathBuf {
    base.join("state").join(group).join(key)
}

fn write_state(base: &Path, group: &str, key: &str, content: &str) -> anyhow::Result<()> {
    let p = state_path(base, group, key);
    if let Some(dir) = p.parent() {
        fs::create_dir_all(dir)?;
    }
    fs::write(&p, content)?;
    Ok(())
}

fn read_state(base: &Path, group: &str, key: &str) -> anyhow::Result<String> {
    Ok(fs::read_to_string(state_path(base, group, key))?)
}

fn base_system() -> String {
    "You are Saki, an agent with tools operating in a controlled environment. \
     Use tools to act; do not claim an action you did not execute. \
     Goal: make the environment's `check` pass."
        .to_string()
}

/// Single `[memory]` section for every group that has persistent state; only the
/// raw content differs. Read failures are surfaced, never defaulted to empty.
fn system_for_group(base: &str, group: &str, base_dir: &Path) -> anyhow::Result<(String, usize)> {
    let mut s = base.to_string();
    let mut injected = 0usize;
    let content = match group {
        "A" => Some(read_state(base_dir, "A", "belief.txt")?),
        "C" => Some(read_state(base_dir, "C", "trace.json")?),
        _ => None,
    };
    if let Some(content) = content {
        let block = format!("\n\n[memory]\n{content}");
        injected = block.len();
        s.push_str(&block);
    }
    Ok((s, injected))
}

// --- Reflection --------------------------------------------------------------

#[derive(Debug, Serialize)]
struct ReflectionRecord {
    produced_update: bool,
    text: String,
}

async fn run_reflection(
    backend: &Backend,
    budget: &mut Budget,
    base_dir: &Path,
    trace_json: &str,
    generation: &GenParams,
    calls_out: &mut Vec<CallRecord>,
) -> anyhow::Result<(ReflectionRecord, Option<String>)> {
    let prompt = format!(
        "Below is the real tool trace from a training session. Write at most 400 characters of \
         conditional, reusable guidance for future sessions in this environment. Do not invent \
         facts not in the trace. If there is no successful observable experience, reply exactly \
         {NO_UPDATE}.\n\n{trace_json}"
    );
    let mut conv = Conversation::new(base_system());
    conv.add_user_message(prompt);
    let outcome = call_model(
        backend,
        budget,
        &conv,
        Vec::new(),
        generation,
        "A",
        "reflection",
        calls_out,
    )
    .await;
    let (record, abort) = match outcome {
        CallOutcome::Response { resp, truncated } => {
            let text = extract_text(&resp);
            // Truncated reflection must never be written as a belief.
            let produced = !truncated && !text.trim().is_empty() && !text.contains(NO_UPDATE);
            (
                ReflectionRecord {
                    produced_update: produced,
                    text,
                },
                None,
            )
        }
        CallOutcome::ApiError(e) => (
            ReflectionRecord {
                produced_update: false,
                text: format!("api error: {e}"),
            },
            Some(format!("reflection api error: {e}")),
        ),
        CallOutcome::BudgetCap => (
            ReflectionRecord {
                produced_update: false,
                text: "budget cap reached; no update".to_string(),
            },
            Some("global budget cap reached during reflection".to_string()),
        ),
    };
    let stored = if record.produced_update {
        record.text.clone()
    } else {
        String::new()
    };
    write_state(base_dir, "A", "belief.txt", &stored)?;
    Ok((record, abort))
}

// --- Artifacts ---------------------------------------------------------------

/// Prepare an independent run directory. Preflight records are manual audit
/// material and are not produced or required by the harness.
fn prepare_artifacts(base_dir: &Path) -> anyhow::Result<()> {
    if base_dir.exists() {
        let mut entries = fs::read_dir(base_dir)?;
        if entries.next().is_some() {
            anyhow::bail!(
                "artifact dir exists and is non-empty; refusing to overwrite: {}",
                base_dir.display()
            );
        }
    }
    fs::create_dir_all(base_dir)?;
    Ok(())
}

// --- Pilot orchestration -----------------------------------------------------

#[derive(Debug, Serialize)]
struct PilotReport {
    run_id: String,
    requested_model: String,
    aborted: Option<String>,
    params_note: String,
    reasoning_effort: Option<String>,
    max_output_tokens: Option<u32>,
    note: String,
    budget: BudgetSummary,
    training: TaskOutcome,
    reflection: ReflectionRecord,
    tests: Vec<TaskOutcome>,
    injected_bytes: BTreeMap<String, usize>,
    calls: Vec<CallRecord>,
}

#[derive(Debug, Serialize)]
struct BudgetSummary {
    logical_used: u32,
    logical_cap: u32,
    http_attempts: u32,
    http_cap: u32,
}

async fn run_pilot(
    backend: &Backend,
    base_dir: &Path,
    generation: &GenParams,
    logical_cap: u32,
    run_id: &str,
) -> anyhow::Result<PilotReport> {
    let fixture = Fixture::new();
    let reg = build_registry(&fixture);
    let mut budget = Budget::new(logical_cap);
    let mut calls_out: Vec<CallRecord> = Vec::new();
    let base = base_system();

    fixture.reset("E17");
    let training = run_task(
        backend,
        &mut budget,
        &fixture,
        &reg,
        &base,
        "Make check pass.",
        3,
        generation,
        "train",
        "E17",
        &mut calls_out,
    )
    .await;

    let training_calls = fixture.snapshot().0;
    let trace_json = serde_json::to_string_pretty(&training_calls)?;
    write_state(base_dir, "C", "trace.json", &trace_json)?;

    // API/budget failures stop the whole pilot; unrun tests are simply absent, not failed.
    let mut aborted: Option<String> = match training.stop_reason {
        StopReason::ApiError => Some("training api error".to_string()),
        StopReason::GlobalBudgetCap => Some("global budget cap during training".to_string()),
        _ => None,
    };

    let mut reflection = ReflectionRecord {
        produced_update: false,
        text: "not run".to_string(),
    };
    if aborted.is_none() {
        if training.pass {
            let (record, abort) = run_reflection(
                backend,
                &mut budget,
                base_dir,
                &trace_json,
                generation,
                &mut calls_out,
            )
            .await?;
            reflection = record;
            if abort.is_some() {
                aborted = abort;
            }
        } else {
            write_state(base_dir, "A", "belief.txt", "")?;
            reflection = ReflectionRecord {
                produced_update: false,
                text: "no successful observable experience; no update".to_string(),
            };
        }
    } else {
        write_state(base_dir, "A", "belief.txt", "")?;
    }

    let mut tests = Vec::new();
    let mut injected_bytes = BTreeMap::new();
    if aborted.is_none() {
        'outer: for group in ["A", "B", "C"] {
            let (sys, bytes) = system_for_group(&base, group, base_dir)?;
            injected_bytes.insert(group.to_string(), bytes);
            for (task, symptom) in [("S", "E42"), ("X", "E99")] {
                fixture.reset(symptom);
                let outcome = run_task(
                    backend,
                    &mut budget,
                    &fixture,
                    &reg,
                    &sys,
                    "Make check pass.",
                    3,
                    generation,
                    group,
                    task,
                    &mut calls_out,
                )
                .await;
                let bad = matches!(
                    outcome.stop_reason,
                    StopReason::ApiError | StopReason::GlobalBudgetCap
                );
                tests.push(outcome);
                if bad {
                    aborted = Some(format!("{group}/{task}: api error or global budget cap"));
                    break 'outer;
                }
            }
        }
    }

    let report = PilotReport {
        run_id: run_id.to_string(),
        requested_model: backend.model_name(),
        aborted,
        params_note: "Proxy forwarding of control params is unverified except max_output_tokens \
                      (observed to truncate). reasoning was produced even at effort=none; the \
                      returned model name is a routing alias, not an exact weight version."
            .to_string(),
        reasoning_effort: generation.reasoning.clone(),
        max_output_tokens: generation.max_output_tokens,
        note: "Tool/model calls are execution overhead, not user effort; user effort was not \
               measured (no unified rescue protocol)."
            .to_string(),
        budget: BudgetSummary {
            logical_used: budget.logical_used,
            logical_cap: budget.logical_cap,
            http_attempts: budget.http_attempts,
            http_cap: budget.http_cap,
        },
        training,
        reflection,
        tests,
        injected_bytes,
        calls: calls_out,
    };

    fs::write(
        base_dir.join("raw_results.json"),
        serde_json::to_string_pretty(&report)?,
    )?;
    Ok(report)
}

// --- Offline tests -----------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    fn fn_call(name: &str, args: Value) -> ModelResponse {
        ModelResponse {
            id: "resp".to_string(),
            model: Some("test".to_string()),
            status: Some("completed".to_string()),
            incomplete_details: None,
            output: vec![OutputItem::ToolCall {
                id: "fc".to_string(),
                status: "completed".to_string(),
                call_id: format!("call_{name}"),
                name: name.to_string(),
                arguments: args.to_string(),
            }],
            usage: None,
        }
    }

    fn truncated_text(text: &str) -> ModelResponse {
        ModelResponse {
            id: "resp".to_string(),
            model: Some("test".to_string()),
            status: Some("incomplete".to_string()),
            incomplete_details: Some(json!({"reason":"max_output_tokens"})),
            output: vec![OutputItem::Message {
                id: "msg".to_string(),
                status: "incomplete".to_string(),
                role: protocol::MessageRole::Assistant,
                content: vec![OutputText {
                    text: text.to_string(),
                }],
            }],
            usage: None,
        }
    }

    fn scripted(responses: Vec<ModelResponse>) -> Backend {
        Backend::Scripted(Mutex::new(responses.into_iter().collect()))
    }

    async fn run_one(
        responses: Vec<ModelResponse>,
        symptom: &str,
        max_calls: u32,
    ) -> (TaskOutcome, Fixture, Vec<CallRecord>) {
        let fixture = Fixture::new();
        let reg = build_registry(&fixture);
        fixture.reset(symptom);
        let backend = scripted(responses);
        let mut budget = Budget::new(24);
        let mut rec = Vec::new();
        let outcome = run_task(
            &backend,
            &mut budget,
            &fixture,
            &reg,
            &base_system(),
            "Make check pass.",
            max_calls,
            &GenParams::pilot(),
            "T",
            symptom,
            &mut rec,
        )
        .await;
        (outcome, fixture, rec)
    }

    #[test]
    fn fixture_is_deterministic() {
        let f = Fixture::new();
        f.reset("E17");
        assert!(!f.check().0);
        f.edit(json!({"key":"retry_budget","value":"3"})).unwrap();
        assert!(f.check().0);
        f.reset("E99");
        f.edit(json!({"key":"retry_budget","value":"3"})).unwrap();
        assert!(!f.check().0, "stale resolve value must not pass E99");
        f.edit(json!({"key":"mode","value":"safe"})).unwrap();
        assert!(f.check().0);
    }

    #[tokio::test]
    async fn every_test_solvable_within_three_calls_and_objective_valid() {
        for (symptom, key, value) in [
            ("E17", "retry_budget", "3"),
            ("E42", "drain_timeout", "7"),
            ("E99", "mode", "safe"),
        ] {
            let (outcome, _, rec) = run_one(
                vec![
                    fn_call("run", json!({"command":"inspect"})),
                    fn_call("edit", json!({"key":key,"value":value})),
                ],
                symptom,
                3,
            )
            .await;
            assert!(outcome.pass);
            assert_eq!(outcome.stop_reason, StopReason::ObjectiveComplete);
            assert!(rec.iter().all(|c| c.error.is_none()));
            assert!(outcome.model_calls <= 3);
        }
    }

    #[tokio::test]
    async fn stale_resolve_path_fails_within_budget() {
        let (outcome, _, rec) = run_one(
            vec![
                fn_call("run", json!({"command":"resolve E99"})),
                fn_call("edit", json!({"key":"retry_budget","value":"3"})),
                fn_call("edit", json!({"key":"retry_budget","value":"3"})),
            ],
            "E99",
            3,
        )
        .await;
        assert!(!outcome.pass);
        assert_ne!(outcome.stop_reason, StopReason::ObjectiveComplete);
        assert!(rec.iter().all(|c| c.error.is_none()));
    }

    #[tokio::test]
    async fn invalid_tool_args_are_recorded() {
        let (outcome, fixture, _) =
            run_one(vec![fn_call("edit", json!({"nonsense": true}))], "E17", 2).await;
        assert!(!outcome.pass);
        assert_eq!(outcome.first_action, "blind_first");
        let (calls, errors, _) = fixture.snapshot();
        assert_eq!(calls.len(), 1);
        assert!(errors >= 1, "invalid tool call must count as a tool error");
    }

    #[tokio::test]
    async fn call_ledger_contains_full_request_and_response() {
        let (_, _, rec) =
            run_one(vec![fn_call("run", json!({"command":"inspect"}))], "E17", 1).await;
        assert_eq!(rec.len(), 1);
        assert!(rec[0].request_body.get("input").is_some());
        assert!(rec[0].response_output.is_array());
        assert!(!rec[0].tool_calls.is_empty());
        assert!(rec[0].elapsed_ms < 60_000);
    }

    #[tokio::test]
    async fn reflection_truncation_writes_no_belief() {
        let base = std::env::temp_dir().join(format!("saki-refl-{}", std::process::id()));
        let _ = fs::remove_dir_all(&base);
        let backend = scripted(vec![truncated_text("partial lesson")]);
        let mut budget = Budget::new(4);
        let mut rec = Vec::new();
        let record = run_reflection(
            &backend,
            &mut budget,
            &base,
            "[]",
            &GenParams::pilot(),
            &mut rec,
        )
        .await
        .unwrap()
        .0;
        assert!(!record.produced_update);
        assert_eq!(read_state(&base, "A", "belief.txt").unwrap(), "");
        assert!(rec[0].truncated);
        let _ = fs::remove_dir_all(&base);
    }

    #[tokio::test]
    async fn reflection_respects_budget_cap() {
        let base = std::env::temp_dir().join(format!("saki-reflb-{}", std::process::id()));
        let _ = fs::remove_dir_all(&base);
        let backend = scripted(vec![fn_call("run", json!({"command":"inspect"}))]);
        let mut budget = Budget::new(0);
        let mut rec = Vec::new();
        let record = run_reflection(
            &backend,
            &mut budget,
            &base,
            "[]",
            &GenParams::pilot(),
            &mut rec,
        )
        .await
        .unwrap()
        .0;
        assert!(!record.produced_update);
        assert!(rec.is_empty());
        assert_eq!(budget.logical_used, 0);
        let _ = fs::remove_dir_all(&base);
    }

    #[tokio::test]
    async fn budget_guard_stops_before_extra_call() {
        let fixture = Fixture::new();
        let reg = build_registry(&fixture);
        fixture.reset("E17");
        let backend = scripted(vec![
            fn_call("run", json!({"command":"inspect"})),
            fn_call("edit", json!({"key":"retry_budget","value":"3"})),
        ]);
        let mut budget = Budget::new(1);
        let mut rec = Vec::new();
        let outcome = run_task(
            &backend,
            &mut budget,
            &fixture,
            &reg,
            &base_system(),
            "Make check pass.",
            3,
            &GenParams::pilot(),
            "T",
            "E17",
            &mut rec,
        )
        .await;
        assert_eq!(outcome.stop_reason, StopReason::GlobalBudgetCap);
        assert!(!outcome.pass);
        assert_eq!(budget.logical_used, 1);
    }

    #[tokio::test]
    async fn instance_call_budget_is_reported_separately() {
        // Large global budget, but this instance is capped at 1 call and needs 2.
        let (outcome, _, _) =
            run_one(vec![fn_call("run", json!({"command":"inspect"}))], "E17", 1).await;
        assert_eq!(outcome.stop_reason, StopReason::InstanceCallBudget);
        assert!(!outcome.pass);
    }

    #[test]
    fn state_roundtrip_and_missing_state_errors() {
        let base = std::env::temp_dir().join(format!("saki-state-{}", std::process::id()));
        let _ = fs::remove_dir_all(&base);
        let belief = "inspect then edit the observed key";
        write_state(&base, "A", "belief.txt", belief).unwrap();
        assert_eq!(read_state(&base, "A", "belief.txt").unwrap(), belief);
        let (prompt, injected) = system_for_group(&base_system(), "A", &base).unwrap();
        assert_eq!(injected, "\n\n[memory]\n".len() + belief.len());
        assert!(prompt.contains("[memory]"));
        // Missing state must surface an error, not silently inject empty context.
        assert!(system_for_group(&base_system(), "C", &base).is_err());
        // B has no state section.
        let (b, bytes) = system_for_group(&base_system(), "B", &base).unwrap();
        assert_eq!(bytes, 0);
        assert_eq!(b.trim(), base_system().trim());
        let _ = fs::remove_dir_all(&base);
    }

    #[test]
    fn artifact_dir_refuses_overwrite() {
        let base = std::env::temp_dir().join(format!("saki-art-{}", std::process::id()));
        let _ = fs::remove_dir_all(&base);
        fs::create_dir_all(&base).unwrap();
        fs::write(base.join("existing.json"), "{}").unwrap();
        assert!(prepare_artifacts(&base).is_err());
        let _ = fs::remove_dir_all(&base);
    }

    #[tokio::test]
    async fn pilot_aborts_on_api_error_and_keeps_partial() {
        let base = std::env::temp_dir().join(format!("saki-abort-{}", std::process::id()));
        let _ = fs::remove_dir_all(&base);
        fs::create_dir_all(&base).unwrap();
        // Empty scripted queue => first call is an API error => whole pilot aborts.
        let backend = Backend::Scripted(Mutex::new(VecDeque::new()));
        let report = run_pilot(&backend, &base, &GenParams::pilot(), 22, "abort-test")
            .await
            .unwrap();
        assert!(report.aborted.is_some());
        assert!(
            report.tests.is_empty(),
            "unrun tests must be absent, not failed"
        );
        assert!(!report.calls.is_empty(), "partial ledger preserved");
        assert!(base.join("raw_results.json").exists());
        let _ = fs::remove_dir_all(&base);
    }
}

// --- Entry point -------------------------------------------------------------

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let args: Vec<String> = std::env::args().collect();
    let real = args.iter().any(|a| a == "--real");
    if !real {
        println!(
            "growth_experiment: offline mode. Run `cargo test --locked --example growth_experiment`. \
             Add --real --run-id <id> (requires API_KEY and main-pane scheduling) for the paid pilot."
        );
        return Ok(());
    }

    let run_id = args
        .iter()
        .position(|a| a == "--run-id")
        .and_then(|i| args.get(i + 1))
        .cloned()
        .unwrap_or_else(|| "pilot".to_string());

    let model = args
        .iter()
        .position(|a| a == "--model")
        .and_then(|i| args.get(i + 1))
        .cloned()
        .or_else(|| std::env::var("SAKI_EXPERIMENT_MODEL").ok())
        .ok_or_else(|| {
            anyhow::anyhow!("missing model: pass --model <name> or set SAKI_EXPERIMENT_MODEL")
        })?;
    let base_url = args
        .iter()
        .position(|a| a == "--base-url")
        .and_then(|i| args.get(i + 1))
        .cloned()
        .or_else(|| std::env::var("SAKI_BASE_URL").ok());

    let mut client = ResponsesClient::new()?.with_timeout(Duration::from_secs(60));
    if let Some(url) = base_url {
        client = client.with_base_url(url);
    }
    let backend = Backend::Real { client, model };
    let base_dir = PathBuf::from(format!("examples/growth_experiment_artifacts/{run_id}"));
    prepare_artifacts(&base_dir)?;
    let generation = GenParams::pilot();

    // Harness cap 22 requests; 3 preflight requests bring the total to 25.
    let report = run_pilot(&backend, &base_dir, &generation, 22, &run_id).await?;
    println!("run_id={}", report.run_id);
    println!("budget={:?}", report.budget);
    println!(
        "training: pass={} stop={:?} first={}",
        report.training.pass, report.training.stop_reason, report.training.first_action
    );
    println!(
        "reflection: produced_update={} chars={}",
        report.reflection.produced_update,
        report.reflection.text.len()
    );
    for t in &report.tests {
        println!(
            "test {}/{}: pass={} stop={:?} first={} calls={}",
            t.group, t.task, t.pass, t.stop_reason, t.first_action, t.model_calls
        );
    }
    println!(
        "raw results: {}",
        base_dir.join("raw_results.json").display()
    );
    Ok(())
}
