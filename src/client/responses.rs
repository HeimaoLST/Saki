mod stream;

use std::time::Duration;

use serde::Deserialize;
use serde_json::{Value, json};

use crate::protocol::{
    InputContentPart, InputItem, InputMessageContent, MessageRole, ModelRequest, ModelResponse,
    ModelStreamEvent, OutputItem, OutputText, ProviderData, ReasoningText, ToolType, Usage,
};

use super::config::ClientConfig;
use super::transport::ApiTransport;

const API: &str = "openai-responses";

/// The Responses error code for a request that exceeded the context window.
fn is_context_overflow(value: &Value) -> bool {
    value["error"]["code"] == "context_length_exceeded"
}

#[derive(Debug, thiserror::Error)]
pub enum ApiError {
    #[error("HTTP {status}: {body}")]
    ContextOverflow {
        status: reqwest::StatusCode,
        body: String,
    },
    #[error("Stream error {code:?}: {message}")]
    Stream {
        code: Option<String>,
        message: String,
    },
    #[error("HTTP {status}: {body}")]
    Http {
        status: reqwest::StatusCode,
        body: String,
    },
}

impl ApiError {
    pub fn is_context_overflow(&self) -> bool {
        matches!(self, Self::ContextOverflow { .. })
            || matches!(self, Self::Stream { code: Some(code), .. } if code == "context_length_exceeded")
    }
}

#[derive(Clone)]
pub struct ResponsesClient {
    base_url: String,
    transport: ApiTransport,
}
/// One experiment call: the result and the exact number of HTTP attempts made
/// (zero when protocol conversion fails before any HTTP request).
pub struct Attempted<T> {
    pub result: anyhow::Result<T>,
    /// Read by the standalone growth experiment and the client module tests; the
    /// binary carries it but never reads it.
    #[allow(
        dead_code,
        reason = "read only by the experiment harness and client module tests"
    )]
    pub attempts: u8,
}

impl ResponsesClient {
    /// Returns the non-streaming JSON body, without credentials.
    /// respond_stream uses the same body with stream=true.
    pub fn request_body(request: &ModelRequest<'_>) -> anyhow::Result<Value> {
        let input = request
            .input
            .iter()
            .map(encode_input)
            .collect::<anyhow::Result<Vec<_>>>()?;
        let mut body = json!({"model": request.model, "input": input});
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
                            "type": kind, "name": tool.name,
                            "description": tool.description, "parameters": tool.parameters,
                        })
                    })
                    .collect(),
            );
        }
        if let Some(tokens) = request.max_output_tokens {
            body["max_output_tokens"] = json!(tokens);
        }
        if let Some(temperature) = request.temperature {
            body["temperature"] = json!(temperature);
        }
        if let Some(effort) = &request.reasoning_effort {
            body["reasoning"] = json!({"effort": effort});
        }
        Ok(body)
    }

    #[cfg(test)]
    pub(crate) fn for_test(base_url: String) -> Self {
        Self {
            base_url,
            transport: ApiTransport::for_test(Duration::from_secs(5)),
        }
    }

    /// Builds the client from explicit configuration (read once at assembly).
    pub fn from_config(config: &ClientConfig) -> anyhow::Result<Self> {
        Ok(Self {
            base_url: config.base_url().trim_end_matches('/').to_owned(),
            transport: ApiTransport::new(config.api_key())?,
        })
    }

    /// Set a per-request timeout for all subsequent calls. Used by the
    /// experiment harness; the main agent keeps the default (`None`).
    #[allow(dead_code, reason = "called only by the standalone growth experiment")]
    pub fn with_timeout(mut self, timeout: Duration) -> Self {
        self.transport.request_timeout = Some(timeout);
        self
    }

    /// One HTTP attempt with no retry; conversion failures report zero attempts.
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
        let result = match self.send_request(&body).await {
            Ok(response) => decode_response(response).await,
            Err(err) => Err(err),
        };
        Attempted {
            result,
            attempts: 1,
        }
    }

    async fn send_request(&self, body: &Value) -> anyhow::Result<reqwest::Response> {
        self.transport
            .post_once(
                &format!("{}/responses", self.base_url),
                body,
                is_context_overflow,
            )
            .await
    }

    /// Retry transport failures, HTTP 408/429 and server errors before decoding.
    /// A successful HTTP response is never replayed after body processing starts.
    async fn send_with_retry(&self, body: &Value) -> anyhow::Result<reqwest::Response> {
        self.transport
            .post_with_retry(
                &format!("{}/responses", self.base_url),
                body,
                is_context_overflow,
            )
            .await
    }

    pub async fn respond(&self, request: ModelRequest<'_>) -> anyhow::Result<ModelResponse> {
        let body = Self::request_body(&request)?;
        decode_response(self.send_with_retry(&body).await?).await
    }

    /// Delivers deltas synchronously as they arrive and returns the final response.
    /// The callback must stay cheap. Dropping this future closes the HTTP body.
    /// Stream errors and missing terminal events fail without automatic replay.
    /// Gateways that return application/json are accepted as a complete response.
    pub async fn respond_stream(
        &self,
        request: ModelRequest<'_>,
        on_event: impl FnMut(ModelStreamEvent),
    ) -> anyhow::Result<ModelResponse> {
        let mut body = Self::request_body(&request)?;
        body["stream"] = json!(true);
        let response = stream::decode(self.send_with_retry(&body).await?, on_event).await?;
        anyhow::ensure!(
            response.incomplete_details.is_none()
                && response
                    .status
                    .as_deref()
                    .is_none_or(|status| status == "completed"),
            "model response was not completed: {:?} ({:?})",
            response.status,
            response.incomplete_details
        );
        Ok(response)
    }
}

fn encode_input(item: &InputItem) -> anyhow::Result<Value> {
    Ok(match item {
        InputItem::Message { role, content } => {
            let content = match content {
                InputMessageContent::Text(text) => json!(text),
                InputMessageContent::Parts(parts) => Value::Array(
                    parts
                        .iter()
                        .map(|part| match part {
                            InputContentPart::Text { text } => {
                                json!({"type": "input_text", "text": text})
                            }
                            InputContentPart::Image { url } => {
                                json!({"type": "input_image", "image_url": url})
                            }
                        })
                        .collect(),
                ),
            };
            json!({"type": "message", "role": role, "content": content})
        }
        InputItem::ToolCall {
            call_id,
            name,
            arguments,
        } => {
            json!({"type": "function_call", "call_id": call_id, "name": name, "arguments": arguments})
        }
        InputItem::ToolResult { call_id, output } => {
            json!({"type": "function_call_output", "call_id": call_id, "output": output})
        }
        InputItem::Reasoning {
            provider_data: Some(data),
            ..
        }
        | InputItem::ProviderItem { data } => {
            anyhow::ensure!(
                data.api == API,
                "cannot send {} continuation data through {API}",
                data.api
            );
            data.value.clone()
        }
        InputItem::Reasoning {
            content,
            provider_data: None,
        } => {
            let content: Vec<_> = content
                .iter()
                .map(|part| json!({"type": "reasoning_text", "text": part.text}))
                .collect();
            json!({"type": "reasoning", "content": content})
        }
    })
}

#[derive(Deserialize)]
struct Response {
    id: String,
    model: Option<String>,
    status: Option<String>,
    incomplete_details: Option<Value>,
    output: Vec<Value>,
    usage: Option<Usage>,
}

#[derive(Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
enum ResponseItem {
    Message {
        id: String,
        status: String,
        role: MessageRole,
        content: Vec<OutputText>,
    },
    Reasoning {
        id: String,
        status: String,
        #[serde(default)]
        content: Vec<ReasoningText>,
    },
    FunctionCall {
        id: String,
        status: String,
        call_id: String,
        name: String,
        arguments: String,
    },
    #[serde(other)]
    Other,
}

fn decode_item(mut value: Value) -> anyhow::Result<OutputItem> {
    Ok(
        match serde_json::from_value::<ResponseItem>(value.clone())? {
            ResponseItem::Message {
                id,
                status,
                role,
                content,
            } => OutputItem::Message {
                id,
                status,
                role,
                content,
            },
            ResponseItem::FunctionCall {
                id,
                status,
                call_id,
                name,
                arguments,
            } => OutputItem::ToolCall {
                id,
                status,
                call_id,
                name,
                arguments,
            },
            ResponseItem::Reasoning {
                id,
                status,
                content,
            } => {
                let item = value.as_object_mut().unwrap();
                item.remove("id");
                item.remove("status");
                OutputItem::Reasoning {
                    id,
                    status,
                    content,
                    provider_data: Some(ProviderData {
                        api: API.to_owned(),
                        value,
                    }),
                }
            }
            ResponseItem::Other => OutputItem::ProviderItem {
                data: ProviderData {
                    api: API.to_owned(),
                    value,
                },
            },
        },
    )
}

async fn decode_response(response: reqwest::Response) -> anyhow::Result<ModelResponse> {
    decode_response_value(response.json::<Response>().await?)
}

fn decode_response_value(response: Response) -> anyhow::Result<ModelResponse> {
    Ok(ModelResponse {
        id: response.id,
        model: response.model,
        status: response.status,
        incomplete_details: response.incomplete_details,
        output: response
            .output
            .into_iter()
            .map(decode_item)
            .collect::<anyhow::Result<_>>()?,
        usage: response.usage,
    })
}

#[cfg(test)]
#[path = "../../tests/client/mod.rs"]
mod tests;
