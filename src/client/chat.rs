//! Chat Completions adapter: request JSON, complete JSON and SSE chunks mapped
//! onto the shared ModelRequest/ModelResponse/ModelStreamEvent types.
//!
//! First version: text and function tools. Responses-native continuation data
//! (reasoning, provider items) is refused before any request is sent.

use serde::Deserialize;
use serde_json::{Value, json};

use crate::protocol::{
    InputContentPart, InputItem, InputMessageContent, MessageRole, ModelRequest, ModelResponse,
    ModelStreamEvent, OutputItem, OutputText, ToolType, Usage,
};

use super::config::ClientConfig;
use super::responses::{ApiError, Attempted};
use super::transport::ApiTransport;

const API: &str = "openai-chat-completions";

#[derive(Clone)]
pub struct ChatCompletionsClient {
    base_url: String,
    transport: ApiTransport,
}

impl ChatCompletionsClient {
    /// Builds the client from explicit configuration; the base URL is the API
    /// root and `/chat/completions` is appended per request.
    pub fn from_config(config: &ClientConfig) -> anyhow::Result<Self> {
        Ok(Self {
            base_url: config.base_url().trim_end_matches('/').to_owned(),
            transport: ApiTransport::new(config.api_key())?,
        })
    }

    #[cfg(test)]
    pub(crate) fn for_test(base_url: String) -> Self {
        Self {
            base_url,
            transport: ApiTransport::for_test(std::time::Duration::from_secs(5)),
        }
    }

    /// The non-streaming JSON body. `respond_stream` uses the same body with
    /// `stream=true`.
    pub fn request_body(request: &ModelRequest<'_>) -> anyhow::Result<Value> {
        anyhow::ensure!(
            request.reasoning_effort.is_none(),
            "reasoning_effort is not supported by {API} in this version"
        );
        let mut body = json!({
            "model": request.model,
            "messages": encode_messages(request.input)?,
        });
        if !request.tools.is_empty() {
            body["tools"] = Value::Array(
                request
                    .tools
                    .iter()
                    .map(|tool| {
                        let kind = match tool.tool_type {
                            ToolType::Function => "function",
                        };
                        json!({
                            "type": kind,
                            "function": {
                                "name": tool.name,
                                "description": tool.description,
                                "parameters": tool.parameters,
                            },
                        })
                    })
                    .collect(),
            );
        }
        if let Some(tokens) = request.max_output_tokens {
            body["max_tokens"] = json!(tokens);
        }
        if let Some(temperature) = request.temperature {
            body["temperature"] = json!(temperature);
        }
        Ok(body)
    }

    /// One HTTP attempt with no retry; conversion failures report zero attempts.
    /// Kept for the experiment harness and compaction, which count attempts.
    pub async fn respond_once(&self, request: ModelRequest<'_>) -> Attempted<ModelResponse> {
        let body = match Self::request_body(&request) {
            Ok(body) => body,
            Err(error) => {
                return Attempted {
                    result: Err(error),
                    attempts: 0,
                };
            }
        };
        let url = format!("{}/chat/completions", self.base_url);
        let result = match self
            .transport
            .post_once(&url, &body, is_context_overflow)
            .await
        {
            Ok(response) => decode_response(response).await,
            Err(error) => Err(error),
        };
        Attempted {
            result,
            attempts: 1,
        }
    }

    /// Retries transport failures and retryable HTTP errors; a successful
    /// response is never replayed once decoding starts.
    pub async fn respond(&self, request: ModelRequest<'_>) -> anyhow::Result<ModelResponse> {
        let body = Self::request_body(&request)?;
        let url = format!("{}/chat/completions", self.base_url);
        let response = self
            .transport
            .post_with_retry(&url, &body, is_context_overflow)
            .await?;
        decode_response(response).await
    }

    /// Delivers text deltas synchronously as they arrive and returns the final
    /// response. Tool calls are aggregated and only complete calls are handed
    /// to the caller. Stream errors and missing terminals fail without replay;
    /// a JSON body from a gateway is accepted as a complete response.
    pub async fn respond_stream(
        &self,
        request: ModelRequest<'_>,
        on_event: impl FnMut(ModelStreamEvent),
    ) -> anyhow::Result<ModelResponse> {
        let mut body = Self::request_body(&request)?;
        body["stream"] = json!(true);
        // Ask for the usage tail block; vendors that do not support it still
        // work and usage stays unknown, never zero.
        body["stream_options"] = json!({"include_usage": true});
        let url = format!("{}/chat/completions", self.base_url);
        let response = self
            .transport
            .post_with_retry(&url, &body, is_context_overflow)
            .await?;
        decode_stream(response, on_event).await
    }
}

/// A body that says the request exceeded the model's context window. The
/// vendor's structured error code decides; message wording never does.
fn is_context_overflow(value: &Value) -> bool {
    value["error"]["code"] == "context_length_exceeded"
}

fn chat_role(role: &MessageRole) -> &'static str {
    match role {
        MessageRole::User => "user",
        MessageRole::Assistant => "assistant",
        MessageRole::System => "system",
        // Native developer role is preserved; vendor-specific downgrades are
        // not this round's compatibility layer.
        MessageRole::Developer => "developer",
    }
}

fn encode_content(content: &InputMessageContent) -> anyhow::Result<Value> {
    match content {
        InputMessageContent::Text(text) => Ok(json!(text)),
        InputMessageContent::Parts(parts) => {
            let mut out = Vec::new();
            for part in parts {
                match part {
                    InputContentPart::Text { text } => {
                        out.push(json!({"type": "text", "text": text}));
                    }
                    // First version: text and function tools only. The existing
                    // Responses path keeps image support.
                    InputContentPart::Image { .. } => {
                        anyhow::bail!("images are not supported by {API} in this version");
                    }
                }
            }
            Ok(Value::Array(out))
        }
    }
}

/// Consecutive tool calls belong to one assistant message; a tool result flushes
/// the calls before its own message, so parallel calls keep their order.
fn flush_calls(messages: &mut Vec<Value>, calls: &mut Vec<Value>) {
    if calls.is_empty() {
        return;
    }
    messages.push(json!({
        "role": "assistant",
        "content": "",
        "tool_calls": std::mem::take(calls),
    }));
}

fn encode_messages(input: &[InputItem]) -> anyhow::Result<Vec<Value>> {
    let mut messages = Vec::new();
    let mut calls: Vec<Value> = Vec::new();
    for item in input {
        match item {
            InputItem::Message { role, content } => {
                flush_calls(&mut messages, &mut calls);
                messages.push(json!({
                    "role": chat_role(role),
                    "content": encode_content(content)?,
                }));
            }
            InputItem::ToolCall {
                call_id,
                name,
                arguments,
            } => {
                calls.push(json!({
                    "id": call_id,
                    "type": "function",
                    "function": {"name": name, "arguments": arguments},
                }));
            }
            InputItem::ToolResult { call_id, output } => {
                flush_calls(&mut messages, &mut calls);
                messages.push(json!({
                    "role": "tool",
                    "tool_call_id": call_id,
                    "content": output,
                }));
            }
            InputItem::Reasoning { .. } | InputItem::ProviderItem { .. } => {
                anyhow::bail!(
                    "cannot send Responses-native reasoning or provider items through {API}; \
                     start a session on {API} or use a client that speaks \
                     openai-responses"
                );
            }
        }
    }
    flush_calls(&mut messages, &mut calls);
    Ok(messages)
}

fn arguments_text(arguments: Option<Value>) -> String {
    match arguments {
        None | Some(Value::Null) => String::new(),
        Some(Value::String(text)) => text,
        Some(other) => other.to_string(),
    }
}

/// Only `stop` and `tool_calls` complete a turn. `length`, `content_filter`
/// and unknown reasons fail here, before the Agent can keep truncated text or
/// execute an incomplete tool call; partial visible text is never replayed.
fn completed(finish: Option<&str>) -> anyhow::Result<()> {
    match finish {
        Some("stop") | Some("tool_calls") => Ok(()),
        Some(reason) => anyhow::bail!("chat completion did not finish: {reason}"),
        None => anyhow::bail!("chat completion had no finish_reason"),
    }
}

fn output_from_message(
    response_id: &str,
    content: Option<String>,
    tool_calls: Vec<ChatToolCall>,
) -> Vec<OutputItem> {
    let mut output = Vec::new();
    if let Some(content) = content.filter(|text| !text.is_empty()) {
        output.push(OutputItem::Message {
            id: format!("{response_id}-text"),
            status: "completed".to_owned(),
            role: MessageRole::Assistant,
            content: vec![OutputText { text: content }],
        });
    }
    for call in tool_calls {
        output.push(OutputItem::ToolCall {
            id: call.id.clone(),
            status: "completed".to_owned(),
            call_id: call.id,
            name: call.function.name,
            arguments: arguments_text(call.function.arguments),
        });
    }
    output
}

#[derive(Deserialize)]
struct ChatResponse {
    id: String,
    model: Option<String>,
    #[serde(default)]
    choices: Vec<ChatChoice>,
    usage: Option<ChatUsage>,
}

#[derive(Deserialize)]
struct ChatChoice {
    message: ChatMessage,
    finish_reason: Option<String>,
}

#[derive(Deserialize)]
struct ChatMessage {
    content: Option<String>,
    #[serde(default)]
    tool_calls: Vec<ChatToolCall>,
}

#[derive(Deserialize)]
struct ChatToolCall {
    #[serde(default)]
    id: String,
    function: ChatFunction,
}

#[derive(Deserialize)]
struct ChatFunction {
    #[serde(default)]
    name: String,
    arguments: Option<Value>,
}

#[derive(Deserialize)]
struct ChatUsage {
    prompt_tokens: Option<u64>,
    completion_tokens: Option<u64>,
}

impl From<ChatUsage> for Usage {
    fn from(usage: ChatUsage) -> Self {
        Self {
            input_tokens: usage.prompt_tokens,
            output_tokens: usage.completion_tokens,
            input_tokens_details: None,
            output_tokens_details: None,
        }
    }
}

fn decode_response_value(response: ChatResponse) -> anyhow::Result<ModelResponse> {
    let choice = response
        .choices
        .into_iter()
        .next()
        .ok_or_else(|| anyhow::anyhow!("chat completion contained no choices"))?;
    completed(choice.finish_reason.as_deref())?;
    Ok(ModelResponse {
        id: response.id.clone(),
        model: response.model,
        status: Some("completed".to_owned()),
        incomplete_details: None,
        output: output_from_message(
            &response.id,
            choice.message.content,
            choice.message.tool_calls,
        ),
        usage: response.usage.map(Into::into),
    })
}

async fn decode_response(response: reqwest::Response) -> anyhow::Result<ModelResponse> {
    decode_response_value(response.json::<ChatResponse>().await?)
}

#[derive(Deserialize)]
struct StreamChunk {
    #[serde(default)]
    id: String,
    model: Option<String>,
    #[serde(default)]
    choices: Vec<StreamChoice>,
    usage: Option<ChatUsage>,
    error: Option<Value>,
}

#[derive(Deserialize)]
struct StreamChoice {
    #[serde(default)]
    delta: StreamDelta,
    finish_reason: Option<String>,
}

#[derive(Deserialize, Default)]
struct StreamDelta {
    content: Option<String>,
    #[serde(default)]
    tool_calls: Vec<ToolCallDelta>,
}

#[derive(Deserialize)]
struct ToolCallDelta {
    index: u64,
    id: Option<String>,
    function: Option<FunctionDelta>,
}

#[derive(Deserialize)]
struct FunctionDelta {
    #[serde(default)]
    name: String,
    arguments: Option<Value>,
}

#[derive(Default)]
struct PartialCall {
    id: Option<String>,
    name: String,
    arguments: String,
}

#[derive(Default)]
struct StreamState {
    id: String,
    model: Option<String>,
    text: String,
    text_seen: bool,
    calls: Vec<PartialCall>,
    finish: Option<String>,
    usage: Option<ChatUsage>,
}

impl StreamState {
    fn apply(
        &mut self,
        chunk: StreamChunk,
        on_event: &mut impl FnMut(ModelStreamEvent),
    ) -> anyhow::Result<()> {
        if let Some(error) = chunk.error {
            return Err(ApiError::Stream {
                code: error["code"].as_str().map(str::to_owned),
                message: error["message"]
                    .as_str()
                    .unwrap_or("chat stream failed")
                    .to_owned(),
            }
            .into());
        }
        if !chunk.id.is_empty() {
            self.id = chunk.id;
        }
        if chunk.model.is_some() {
            self.model = chunk.model;
        }
        if let Some(usage) = chunk.usage {
            self.usage = Some(usage);
        }
        for choice in chunk.choices {
            if let Some(reason) = choice.finish_reason {
                self.finish = Some(reason);
            }
            if let Some(content) = choice.delta.content.filter(|text| !text.is_empty()) {
                self.text.push_str(&content);
                self.text_seen = true;
                on_event(ModelStreamEvent::TextDelta {
                    item_id: format!("{}-text", self.id),
                    delta: content,
                });
            }
            for delta in choice.delta.tool_calls {
                let index = delta.index as usize;
                while self.calls.len() <= index {
                    self.calls.push(PartialCall::default());
                }
                let call = &mut self.calls[index];
                if let Some(id) = delta.id.filter(|id| !id.is_empty()) {
                    call.id = Some(id);
                }
                if let Some(function) = delta.function {
                    if !function.name.is_empty() {
                        call.name = function.name;
                    }
                    call.arguments.push_str(&arguments_text(function.arguments));
                }
            }
        }
        Ok(())
    }

    fn finish(self) -> anyhow::Result<ModelResponse> {
        completed(self.finish.as_deref())?;
        let mut output = Vec::new();
        if self.text_seen {
            output.push(OutputItem::Message {
                id: format!("{}-text", self.id),
                status: "completed".to_owned(),
                role: MessageRole::Assistant,
                content: vec![OutputText { text: self.text }],
            });
        }
        for call in self.calls {
            let id = call.id.ok_or_else(|| {
                anyhow::anyhow!("chat stream finished with a tool call that has no id")
            })?;
            output.push(OutputItem::ToolCall {
                id: id.clone(),
                status: "completed".to_owned(),
                call_id: id,
                name: call.name,
                arguments: call.arguments,
            });
        }
        Ok(ModelResponse {
            id: self.id,
            model: self.model,
            status: Some("completed".to_owned()),
            incomplete_details: None,
            output,
            usage: self.usage.map(Into::into),
        })
    }
}

async fn decode_stream(
    response: reqwest::Response,
    mut on_event: impl FnMut(ModelStreamEvent),
) -> anyhow::Result<ModelResponse> {
    use eventsource_stream::Eventsource;
    use futures_util::StreamExt;

    let content_type = response
        .headers()
        .get(reqwest::header::CONTENT_TYPE)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.split(';').next())
        .unwrap_or("")
        .trim();
    if content_type.eq_ignore_ascii_case("application/json") {
        return decode_response(response).await;
    }
    anyhow::ensure!(
        content_type.eq_ignore_ascii_case("text/event-stream"),
        "expected text/event-stream, received {content_type:?}"
    );
    let mut state = StreamState::default();
    let mut events = response.bytes_stream().eventsource();
    while let Some(event) = events.next().await {
        let event = event.map_err(|error| anyhow::anyhow!("chat stream interrupted: {error}"))?;
        if event.data == "[DONE]" {
            break;
        }
        let chunk: StreamChunk = serde_json::from_str(&event.data)
            .map_err(|error| anyhow::anyhow!("invalid chat stream chunk: {error}"))?;
        state.apply(chunk, &mut on_event)?;
    }
    state.finish()
}

#[cfg(test)]
#[path = "../../tests/client/chat.rs"]
mod tests;
