pub mod chat;
pub mod config;
pub mod connections;
pub mod responses;
mod transport;

pub use chat::ChatCompletionsClient;
pub use config::{ClientConfig, ModelApi};
pub use responses::{Attempted, ResponsesClient};

use std::future::Future;

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
    #[cfg_attr(
        not(test),
        expect(dead_code, reason = "synchronous callback compatibility adapter")
    )]
    pub async fn respond_stream(
        &self,
        request: ModelRequest<'_>,
        mut on_event: impl FnMut(ModelStreamEvent),
    ) -> anyhow::Result<ModelResponse> {
        self.respond_stream_async(request, |event| {
            on_event(event);
            std::future::ready(())
        })
        .await
    }

    /// Async-callback streaming for callers that need to await event handling
    /// before the next upstream frame is read.
    pub async fn respond_stream_async<F, Fut>(
        &self,
        request: ModelRequest<'_>,
        on_event: F,
    ) -> anyhow::Result<ModelResponse>
    where
        F: FnMut(ModelStreamEvent) -> Fut,
        Fut: Future<Output = ()>,
    {
        match self {
            Self::Responses(client) => client.respond_stream_async(request, on_event).await,
            Self::ChatCompletions(client) => client.respond_stream_async(request, on_event).await,
        }
    }
}
