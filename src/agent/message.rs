use serde::{Deserialize, Serialize};

/// One image attachment kept as an immutable data-URL snapshot plus a display
/// name. The raw bytes are never re-read, so later file changes cannot alter a
/// draft or a recalled history entry.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct ImageAttachment {
    pub name: String,
    /// `data:<mime>;base64,<payload>`.
    pub data_url: String,
}

impl ImageAttachment {
    pub fn new(name: impl Into<String>, data_url: impl Into<String>) -> Self {
        Self {
            name: name.into(),
            data_url: data_url.into(),
        }
    }
}

/// One caller-provided context document carried by a user turn. It is user
/// data, never a system instruction.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ContextInput {
    pub name: String,
    pub text: String,
}

impl ContextInput {
    pub(crate) fn as_input_text(&self) -> String {
        serde_json::json!({
            "type": "caller_context",
            "label": "Caller-provided context (user data)",
            "name": self.name,
            "text": self.text,
        })
        .to_string()
    }
}

/// Maximum serialized context payload carried by one user turn.
pub const MAX_CONTEXT_BYTES: usize = 1024 * 1024;

/// A user turn: text, caller-provided context, and zero or more images.
///
/// `String` and `&str` convert into a text-only message so existing callers and
/// the console front end stay unchanged.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize)]
pub struct UserMessage {
    pub text: String,
    pub images: Vec<ImageAttachment>,
    pub context: Vec<ContextInput>,
}

impl UserMessage {
    pub fn text(text: impl Into<String>) -> Self {
        Self {
            text: text.into(),
            images: Vec::new(),
            context: Vec::new(),
        }
    }

    pub fn is_empty(&self) -> bool {
        self.text.is_empty() && self.images.is_empty() && self.context.is_empty()
    }

    /// Admission validation shared by typed and stdio callers. Context-only
    /// messages are valid; the serialized context payload is bounded here.
    pub fn validate(&self) -> Result<(), String> {
        if self.is_empty() {
            return Err("message is empty".to_owned());
        }
        let mut bytes = 0usize;
        for context in &self.context {
            bytes = bytes.saturating_add(context.as_input_text().len());
            if bytes > MAX_CONTEXT_BYTES {
                return Err(format!(
                    "caller context exceeds the {MAX_CONTEXT_BYTES}-byte limit"
                ));
            }
        }
        Ok(())
    }
}

impl From<String> for UserMessage {
    fn from(text: String) -> Self {
        Self::text(text)
    }
}

impl From<&str> for UserMessage {
    fn from(text: &str) -> Self {
        Self::text(text)
    }
}

impl From<&UserMessage> for UserMessage {
    fn from(message: &UserMessage) -> Self {
        message.clone()
    }
}
