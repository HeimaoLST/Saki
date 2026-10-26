//! One HTTP transport for model APIs: the reqwest client, bearer auth and the
//! retry policy shared by the Responses and Chat Completions adapters.

use std::time::Duration;

use futures_util::{StreamExt, stream::try_unfold};
use reqwest::header;
use serde_json::Value;

use super::responses::ApiError;

const MAX_RETRIES: u8 = 3;
/// Upper bound for one model response body, including SSE and JSON fallback.
pub(crate) const MAX_RESPONSE_BYTES: usize = 32 * 1024 * 1024;

#[derive(Clone)]
pub(crate) struct ApiTransport {
    http_client: reqwest::Client,
    pub(crate) request_timeout: Option<Duration>,
    redaction_key: Option<String>,
}

impl ApiTransport {
    /// Builds the shared client with `Bearer <api_key>` as the default header.
    /// The key is never logged.
    pub(crate) fn new(api_key: &str) -> anyhow::Result<Self> {
        let mut headers = header::HeaderMap::new();
        let value = header::HeaderValue::from_str(&format!("Bearer {api_key}")).map_err(|_| {
            anyhow::anyhow!("API_KEY contains characters invalid for an HTTP header")
        })?;
        headers.insert(header::AUTHORIZATION, value);
        let http_client = reqwest::ClientBuilder::new()
            .default_headers(headers)
            .build()
            .map_err(|e| anyhow::anyhow!("failed to build HTTP client: {e}"))?;
        Ok(Self {
            http_client,
            request_timeout: None,
            redaction_key: (!api_key.is_empty()).then(|| api_key.to_owned()),
        })
    }

    #[cfg(test)]
    pub(crate) fn for_test(timeout: Duration) -> Self {
        Self {
            http_client: reqwest::Client::new(),
            request_timeout: Some(timeout),
            redaction_key: None,
        }
    }

    /// One attempt: HTTP errors become `ApiError::Http`, or
    /// `ApiError::ContextOverflow` when `is_overflow` recognizes the body.
    pub(crate) async fn post_once(
        &self,
        url: &str,
        body: &Value,
        is_overflow: fn(&Value) -> bool,
    ) -> anyhow::Result<reqwest::Response> {
        let mut builder = self.http_client.post(url).json(body);
        if let Some(timeout) = self.request_timeout {
            builder = builder.timeout(timeout);
        }
        let response = builder
            .send()
            .await
            .map_err(|e| anyhow::anyhow!("request failed: {e}"))?;
        if !response.status().is_success() {
            let status = response.status();
            let body = match read_body_limited(response).await {
                Ok(bytes) => String::from_utf8_lossy(&bytes).into_owned(),
                Err(error) => format!("<failed to read error body: {error}>"),
            };
            let overflow = serde_json::from_str::<Value>(&body)
                .ok()
                .is_some_and(|value| is_overflow(&value));
            let body = match self.redaction_key.as_deref() {
                Some(key) => body.replace(key, "[redacted]"),
                None => body,
            };
            return Err(if overflow {
                ApiError::ContextOverflow { status, body }
            } else {
                ApiError::Http { status, body }
            }
            .into());
        }
        Ok(response)
    }

    /// Retry transport failures, HTTP 408/429 and server errors before decoding.
    /// A successful HTTP response is never replayed after body processing starts.
    pub(crate) async fn post_with_retry(
        &self,
        url: &str,
        body: &Value,
        is_overflow: fn(&Value) -> bool,
    ) -> anyhow::Result<reqwest::Response> {
        for attempt in 1..=MAX_RETRIES {
            match self.post_once(url, body, is_overflow).await {
                Ok(response) => return Ok(response),
                Err(error) => {
                    let retryable = match error.downcast_ref::<ApiError>() {
                        Some(ApiError::Http { status, .. }) => {
                            status.is_server_error()
                                || *status == reqwest::StatusCode::TOO_MANY_REQUESTS
                                || *status == reqwest::StatusCode::REQUEST_TIMEOUT
                        }
                        Some(_) => false,
                        None => true,
                    };
                    if !retryable {
                        return Err(error);
                    }
                    if attempt == MAX_RETRIES {
                        return Err(error.context("All retries failed"));
                    }
                }
            }
        }
        unreachable!()
    }
}

/// Bounds a response body while it is being read. The cap applies to every
/// chunk before it is handed to the SSE parser or the JSON fallback, so neither
/// path can grow an unbounded internal buffer first.
pub(crate) fn limited_body_stream(
    response: reqwest::Response,
) -> impl futures_util::Stream<Item = Result<Vec<u8>, anyhow::Error>> + Unpin {
    limited_body_stream_with_limit(response, MAX_RESPONSE_BYTES)
}

pub(crate) fn limited_body_stream_with_limit(
    response: reqwest::Response,
    limit: usize,
) -> impl futures_util::Stream<Item = Result<Vec<u8>, anyhow::Error>> + Unpin {
    Box::pin(try_unfold(
        (response.bytes_stream(), 0usize),
        move |(mut stream, mut used)| async move {
            match stream.next().await {
                None => Ok(None),
                Some(Err(error)) => Err(anyhow::anyhow!("response body read failed: {error}")),
                Some(Ok(chunk)) => {
                    used = used.saturating_add(chunk.len());
                    if used > limit {
                        return Err(anyhow::anyhow!(
                            "response body exceeded the {limit}-byte limit"
                        ));
                    }
                    Ok(Some((chunk.to_vec(), (stream, used))))
                }
            }
        },
    ))
}

/// Reads a complete non-streaming body under the same cap.
pub(crate) async fn read_body_limited(response: reqwest::Response) -> anyhow::Result<Vec<u8>> {
    let mut stream = limited_body_stream(response);
    let mut body = Vec::new();
    while let Some(chunk) = stream.next().await {
        body.extend_from_slice(&chunk?);
    }
    Ok(body)
}
