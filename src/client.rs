use std::env;

use reqwest::header;
use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::{conversation::Conversation, tools::tool_registry::ToolDefinition};

const MAX_RETRIES: u8 = 3;
pub struct DeepseekClient {
    base_url: String,
    http_client: reqwest::Client,
}
/// An item sent to the Responses API.
///
/// This is intentionally separate from `OutputItem`: response items include
/// server-owned metadata (`id`, `status`) and must not be sent back verbatim.
#[derive(Debug, Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum InputItem {
    Message {
        role: MessageRole,
        content: InputMessageContent,
    },
    FunctionCall {
        call_id: String,
        name: String,
        arguments: String,
    },
    FunctionCallOutput {
        call_id: String,
        output: String,
    },
    CustomToolCall {
        call_id: String,
        name: String,
        input: String,
    },
    CustomToolCallOutput {
        call_id: String,
        output: Value,
    },
    /// A reasoning item carried forward as part of the conversation history.
    Reasoning {
        content: Vec<ReasoningText>,
    },
    /// Preserves provider-specific web-search fields without losing their type.
    WebSearchCall {
        #[serde(flatten)]
        data: Value,
    },
}

#[derive(Debug, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum MessageRole {
    User,
    Assistant,
    System,
    Developer,
}

/// Message content is either the text shorthand or a typed list of parts.
///
/// The two variants serialize respectively as a JSON string and JSON array.
#[derive(Debug, Serialize)]
#[serde(untagged)]
pub enum InputMessageContent {
    Text(String),
    Parts(Vec<InputContentPart>),
}

/// Content parts valid inside a `message` input item.
#[derive(Debug, Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum InputContentPart {
    InputText { text: String },
    OutputText { text: String },
    InputImage { image_url: String },
}

#[derive(Serialize)]
pub struct ModelRequest<'a> {
    pub model: String,
    pub input: &'a Conversation,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub tools: Vec<ToolDefinition>,
}
impl<'a> ModelRequest<'a> {
    pub fn new(model: String, input: &'a Conversation, tools: Vec<ToolDefinition>) -> Self {
        Self {
            model,
            input,
            tools,
        }
    }
}
#[derive(Debug, Deserialize)]
#[serde(tag = "type")]
pub enum OutputItem {
    #[serde(rename = "message")]
    Message {
        id: String,
        status: String,
        role: MessageRole,
        content: Vec<OutputText>,
    },
    #[serde(rename = "reasoning")]
    Reasoning {
        id: String,
        status: String,
        content: Vec<ReasoningText>,
    },
    #[serde(rename = "function_call")]
    FunctionCall {
        id: String,
        status: String,
        call_id: String,
        name: String,
        arguments: String,
    },
    #[serde(rename = "custom_tool_call")]
    CustomToolCall {
        id: String,
        status: String,
        call_id: String,
        name: String,
        input: String,
    },
    #[serde(rename = "web_search_call")]
    WebSearchCall {
        id: String,
        status: String,
        action: Value,
    },
}
/// Text returned inside an assistant `message` item.
///
/// DeepSeek returns an array of these objects in `message.content`; fields
/// such as `type` and `annotations` are intentionally ignored for now.
#[derive(Debug, Deserialize)]
pub struct OutputText {
    pub text: String,
}

/// Text returned by, or sent back as, a `reasoning` item.
#[derive(Debug, Serialize, Deserialize)]
pub struct ReasoningText {
    #[serde(rename = "type")]
    kind: String,
    pub text: String,
}

impl ReasoningText {
    pub fn new(text: impl Into<String>) -> Self {
        Self {
            kind: "reasoning_text".to_owned(),
            text: text.into(),
        }
    }
}
#[derive(Debug, Deserialize)]
pub struct ModelResponse {
    pub id: String,
    pub output: Vec<OutputItem>,
}
impl DeepseekClient {
    pub fn new() -> Self {
        dotenvy::dotenv().expect("Faild to load the .env");
        let api_key = env::var("API_KEY").expect("Can't load the api_key");
        let api_key = format!("Bearer {}", api_key);
        const BASE_URL: &str = "https://api.deepseek.com";
        let mut header = header::HeaderMap::new();
        header.insert(
            header::AUTHORIZATION,
            header::HeaderValue::from_str(&api_key).unwrap(),
        );
        let clinet = reqwest::ClientBuilder::new()
            .default_headers(header)
            .build()
            .unwrap();

        Self {
            base_url: BASE_URL.to_owned(),
            http_client: clinet,
        }
    }
    pub async fn respond(&self, request: ModelRequest<'_>) -> anyhow::Result<ModelResponse> {
        let mut last_err = String::new();
        for _ in 0..MAX_RETRIES {
            let response = self
                .http_client
                .post(format!("{}/responses", self.base_url))
                .json(&request)
                .send()
                .await;
            let response = match response {
                Ok(res) => res,
                Err(err) => {
                    last_err = err.to_string();
                    continue;
                }
            };

            if !response.status().is_success() {
                let status = response.status();
                let body = response
                    .text()
                    .await
                    .unwrap_or_else(|err| format!("<failed to read error body: {err}>"));
                last_err = format!("HTTP {status}: {body}");
                continue;
            }

            let response_json = response.json::<serde_json::Value>().await?;
            return Ok(serde_json::from_value::<ModelResponse>(response_json)?);
        }
        Err(anyhow::anyhow!("All retries failed: {last_err}"))
    }
}
