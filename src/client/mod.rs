pub mod chat;
pub mod config;
pub mod responses;
mod transport;

pub use chat::ChatCompletionsClient;
pub use config::{ClientConfig, ModelApi};
pub use responses::{Attempted, ResponsesClient};

use crate::protocol::{ModelRequest, ModelResponse, ModelStreamEvent};

/// The runtime's one model-call boundary. Protocol selection is a simple enum
/// match; Agent, tools, compaction and titles all go through this type.
#[derive(Clone)]
pub enum ModelClient {
    Responses(ResponsesClient),
    ChatCompletions(ChatCompletionsClient),
}

impl ModelClient {
    /// Builds the client for the configured API.
    pub fn new(config: &ClientConfig) -> anyhow::Result<Self> {
        Ok(match config.api() {
            ModelApi::Responses => Self::Responses(ResponsesClient::from_config(config)?),
            ModelApi::ChatCompletions => {
                Self::ChatCompletions(ChatCompletionsClient::from_config(config)?)
            }
        })
    }

    /// A Responses client pointed at a scripted test server.
    #[cfg(test)]
    pub(crate) fn for_test(base_url: String) -> Self {
        Self::Responses(ResponsesClient::for_test(base_url))
    }

    pub fn api(&self) -> ModelApi {
        match self {
            Self::Responses(_) => ModelApi::Responses,
            Self::ChatCompletions(_) => ModelApi::ChatCompletions,
        }
    }

    pub async fn respond_once(&self, request: ModelRequest<'_>) -> Attempted<ModelResponse> {
        match self {
            Self::Responses(client) => client.respond_once(request).await,
            Self::ChatCompletions(client) => client.respond_once(request).await,
        }
    }

    pub async fn respond(&self, request: ModelRequest<'_>) -> anyhow::Result<ModelResponse> {
        match self {
            Self::Responses(client) => client.respond(request).await,
            Self::ChatCompletions(client) => client.respond(request).await,
        }
    }

    /// Delivers deltas synchronously as they arrive; the final response is
    /// authoritative for context, tools and usage.
    pub async fn respond_stream(
        &self,
        request: ModelRequest<'_>,
        on_event: impl FnMut(ModelStreamEvent),
    ) -> anyhow::Result<ModelResponse> {
        match self {
            Self::Responses(client) => client.respond_stream(request, on_event).await,
            Self::ChatCompletions(client) => client.respond_stream(request, on_event).await,
        }
    }
}
