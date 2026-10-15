use eventsource_stream::Eventsource;
use futures_util::StreamExt;
use serde::Deserialize;

use super::{
    ApiError, ModelResponse, ModelStreamEvent, Response, decode_response, decode_response_value,
};

#[derive(Deserialize)]
#[serde(tag = "type")]
enum Event {
    #[serde(rename = "response.output_text.delta")]
    TextDelta { item_id: String, delta: String },
    #[serde(rename = "response.reasoning_text.delta")]
    ReasoningDelta { item_id: String, delta: String },
    #[serde(rename = "response.output_item.done")]
    ItemDone { item: serde_json::Value },
    #[serde(rename = "response.completed")]
    Completed { response: Response },
    #[serde(rename = "response.failed")]
    Failed { response: serde_json::Value },
    #[serde(rename = "response.incomplete")]
    Incomplete { response: serde_json::Value },
    #[serde(rename = "error")]
    Error {
        code: Option<String>,
        message: String,
    },
    #[serde(other)]
    Other,
}

pub(super) async fn decode(
    response: reqwest::Response,
    mut on_event: impl FnMut(ModelStreamEvent),
) -> anyhow::Result<ModelResponse> {
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
    let mut events = response.bytes_stream().eventsource();
    while let Some(event) = events.next().await {
        let event =
            event.map_err(|error| anyhow::anyhow!("response stream interrupted: {error}"))?;
        if event.data == "[DONE]" {
            break;
        }
        let event: Event = serde_json::from_str(&event.data)
            .map_err(|error| anyhow::anyhow!("invalid response stream event: {error}"))?;
        match event {
            Event::TextDelta { item_id, delta } => {
                on_event(ModelStreamEvent::TextDelta { item_id, delta });
            }
            Event::ReasoningDelta { item_id, delta } => {
                on_event(ModelStreamEvent::ReasoningDelta { item_id, delta });
            }
            Event::ItemDone { item } => {
                if item["type"] == "reasoning" {
                    let item_id = item["id"]
                        .as_str()
                        .ok_or_else(|| anyhow::anyhow!("completed reasoning item has no id"))?;
                    on_event(ModelStreamEvent::ReasoningCompleted {
                        item_id: item_id.to_owned(),
                    });
                }
            }
            Event::Completed { response } => {
                anyhow::ensure!(
                    response.status.as_deref() == Some("completed"),
                    "response.completed contained a non-completed response"
                );
                return decode_response_value(response);
            }
            Event::Failed { response } => {
                let error = &response["error"];
                return Err(ApiError::Stream {
                    code: error["code"].as_str().map(str::to_owned),
                    message: error["message"]
                        .as_str()
                        .unwrap_or("model response failed")
                        .to_owned(),
                }
                .into());
            }
            Event::Incomplete { response } => {
                anyhow::bail!(
                    "model response incomplete: {}",
                    response["incomplete_details"]
                );
            }
            Event::Error { code, message } => return Err(ApiError::Stream { code, message }.into()),
            Event::Other => {}
        }
    }
    anyhow::bail!("response stream ended before response.completed")
}

#[cfg(test)]
#[path = "../../../tests/client/stream.rs"]
mod tests;
