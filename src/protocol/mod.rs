use serde::{
    Deserialize, Serialize,
    de::{self, Deserializer, MapAccess, Visitor},
};
use serde_json::Value;

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize)]
pub struct ToolSelection {
    pub builtin: Vec<String>,
    pub external: Vec<ExternalToolBinding>,
}

// A derived struct decoder also accepts positional arrays; this wire field requires an object.
impl<'de> Deserialize<'de> for ToolSelection {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct SelectionVisitor;
        impl<'de> Visitor<'de> for SelectionVisitor {
            type Value = ToolSelection;

            fn expecting(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
                formatter.write_str("a tool selection object with builtin and external fields")
            }

            fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<Self::Value, A::Error> {
                let mut builtin = None;
                let mut external = None;
                while let Some(key) = map.next_key::<String>()? {
                    match key.as_str() {
                        "builtin" => {
                            if builtin.is_some() {
                                return Err(de::Error::duplicate_field("builtin"));
                            }
                            builtin = Some(map.next_value::<Vec<String>>()?);
                        }
                        "external" => {
                            if external.is_some() {
                                return Err(de::Error::duplicate_field("external"));
                            }
                            external = Some(map.next_value::<Vec<ExternalToolBinding>>()?);
                        }
                        other => {
                            return Err(de::Error::unknown_field(other, &["builtin", "external"]));
                        }
                    }
                }
                Ok(ToolSelection {
                    builtin: builtin.unwrap_or_default(),
                    external: external.unwrap_or_default(),
                })
            }
        }
        deserializer.deserialize_map(SelectionVisitor)
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ExternalToolBinding {
    pub source_id: String,
    pub tool_name: String,
    pub exposed_name: String,
}

/// Immutable continuation data owned by the named API adapter.
/// When present on reasoning, this is the authoritative native item to replay.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProviderData {
    pub api: String,
    pub value: Value,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum InputItem {
    Message {
        role: MessageRole,
        content: InputMessageContent,
    },
    ToolCall {
        call_id: String,
        name: String,
        arguments: String,
    },
    ToolResult {
        call_id: String,
        output: String,
    },
    Reasoning {
        content: Vec<ReasoningText>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        provider_data: Option<ProviderData>,
    },
    ProviderItem {
        data: ProviderData,
    },
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum MessageRole {
    User,
    Assistant,
    System,
    Developer,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(untagged)]
pub enum InputMessageContent {
    Text(String),
    Parts(Vec<InputContentPart>),
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum InputContentPart {
    Text { text: String },
    Image { url: String },
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ToolDefinition {
    pub tool_type: ToolType,
    pub name: String,
    pub description: String,
    pub parameters: Value,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ToolType {
    Function,
}

#[derive(Serialize)]
pub struct ModelRequest<'a> {
    pub model: String,
    pub input: &'a [InputItem],
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub tools: Vec<ToolDefinition>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_output_tokens: Option<u32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub temperature: Option<f32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub reasoning_effort: Option<String>,
}

impl<'a> ModelRequest<'a> {
    pub fn new(model: String, input: &'a [InputItem], tools: Vec<ToolDefinition>) -> Self {
        Self {
            model,
            input,
            tools,
            max_output_tokens: None,
            temperature: None,
            reasoning_effort: None,
        }
    }

    pub fn with_generation(
        mut self,
        max_output_tokens: Option<u32>,
        temperature: Option<f32>,
        reasoning_effort: Option<String>,
    ) -> Self {
        self.max_output_tokens = max_output_tokens;
        self.temperature = temperature;
        self.reasoning_effort = reasoning_effort;
        self
    }
}

#[derive(Debug, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum OutputItem {
    Message {
        id: String,
        status: String,
        role: MessageRole,
        content: Vec<OutputText>,
    },
    Reasoning {
        id: String,
        status: String,
        content: Vec<ReasoningText>,
        provider_data: Option<ProviderData>,
    },
    ToolCall {
        id: String,
        status: String,
        call_id: String,
        name: String,
        arguments: String,
    },
    ProviderItem {
        data: ProviderData,
    },
}

#[derive(Debug, Serialize, Deserialize)]
pub struct OutputText {
    pub text: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ReasoningText {
    pub text: String,
}

impl ReasoningText {
    // Test-only constructor with real callers in tests/conversation and tests/tui;
    // production code builds ReasoningText through provider decoding.
    #[cfg(test)]
    pub fn new(text: impl Into<String>) -> Self {
        Self { text: text.into() }
    }
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct Usage {
    pub input_tokens: Option<u64>,
    pub output_tokens: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub input_tokens_details: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub output_tokens_details: Option<Value>,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct ModelResponse {
    pub id: String,
    pub model: Option<String>,
    pub status: Option<String>,
    pub incomplete_details: Option<Value>,
    pub output: Vec<OutputItem>,
    pub usage: Option<Usage>,
}

/// Incremental display data. The final ModelResponse is authoritative for
/// context, tool execution and usage; deltas alone do not commit output.
#[derive(Debug, Clone, PartialEq)]
pub enum ModelStreamEvent {
    TextDelta { item_id: String, delta: String },
    ReasoningDelta { item_id: String, delta: String },
    ReasoningCompleted { item_id: String },
}
