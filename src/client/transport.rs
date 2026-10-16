//! One HTTP transport for model APIs: the reqwest client, bearer auth and the
//! retry policy shared by the Responses and Chat Completions adapters.

use std::time::Duration;

use reqwest::header;
use serde_json::Value;

use super::responses::ApiError;

const MAX_RETRIES: u8 = 3;

#[derive(Clone)]
pub(crate) struct ApiTransport {
    http_client: reqwest::Client,
    request_timeout: Option<Duration>,
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
        })
    }

    #[cfg(test)]
    pub(crate) fn for_test(timeout: Duration) -> Self {
        Self {
            http_client: reqwest::Client::new(),
            request_timeout: Some(timeout),
        }
    }

    pub(crate) fn with_timeout(mut self, timeout: Duration) -> Self {
        self.request_timeout = Some(timeout);
        self
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
            let body = response
                .text()
                .await
                .unwrap_or_else(|err| format!("<failed to read error body: {err}>"));
            let overflow = serde_json::from_str::<Value>(&body)
                .ok()
                .is_some_and(|value| is_overflow(&value));
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
