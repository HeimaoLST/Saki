//! Assembly-boundary configuration for the model client. Environment variables
//! are read once here; the clients receive explicit values.

use std::env;

/// Which wire protocol the runtime speaks. Kept to a simple enum: protocol
/// dispatch is one match, never a dynamic registry.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ModelApi {
    Responses,
    ChatCompletions,
}

pub const RESPONSES_API: &str = "openai-responses";
pub const CHAT_COMPLETIONS_API: &str = "openai-chat-completions";
pub const DEFAULT_BASE_URL: &str = "https://api.deepseek.com";

impl ModelApi {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Responses => RESPONSES_API,
            Self::ChatCompletions => CHAT_COMPLETIONS_API,
        }
    }

    /// Unknown values fail before any request is sent.
    pub fn parse(value: &str) -> anyhow::Result<Self> {
        match value.trim() {
            RESPONSES_API => Ok(Self::Responses),
            CHAT_COMPLETIONS_API => Ok(Self::ChatCompletions),
            other => anyhow::bail!(
                "unknown SAKI_API={other:?}; expected {RESPONSES_API} or {CHAT_COMPLETIONS_API}"
            ),
        }
    }
}

/// Explicit client configuration: API, key and base URL. The base URL is the
/// API root; each adapter appends its own path.
pub struct ClientConfig {
    api: ModelApi,
    api_key: String,
    base_url: String,
}

impl ClientConfig {
    /// Reads the environment once: a missing `.env` is fine; a malformed `.env`
    /// is an error; `API_KEY` is required. Errors never echo secret values.
    pub fn from_env() -> anyhow::Result<Self> {
        match dotenvy::dotenv() {
            Ok(_) => {}
            Err(e) if e.not_found() => {}
            // Static message: a dotenv parse error can echo the offending line,
            // which may contain a secret. Never include it.
            Err(_) => {
                return Err(anyhow::anyhow!(
                    ".env exists but could not be parsed (details redacted)"
                ));
            }
        }
        let api_key = env::var("API_KEY").map_err(|_| {
            anyhow::anyhow!(
                "API_KEY is not set (checked the environment and an optional .env file)"
            )
        })?;
        let api = match env::var("SAKI_API") {
            Ok(value) => ModelApi::parse(&value)?,
            Err(_) => ModelApi::Responses,
        };
        let base_url = env::var("SAKI_BASE_URL").unwrap_or_else(|_| DEFAULT_BASE_URL.to_owned());
        Ok(Self {
            api,
            api_key,
            base_url,
        })
    }

    pub fn api(&self) -> ModelApi {
        self.api
    }

    pub fn api_key(&self) -> &str {
        &self.api_key
    }

    pub fn base_url(&self) -> &str {
        &self.base_url
    }
}
