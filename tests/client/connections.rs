use super::*;
use crate::client::ModelApi;

fn connection(id: &str, api: ModelApi, base_url: &str, key: &str, model: &str) -> Connection {
    Connection::new(
        id.to_owned(),
        ClientConfig::new(api, key, base_url),
        model.to_owned(),
    )
    .unwrap()
}

#[test]
fn identity_normalizes_url_and_ignores_key_rotation_but_not_api_or_endpoint() {
    let original = connection(
        "primary",
        ModelApi::Responses,
        "https://EXAMPLE.com:443/v1/",
        "secret-one",
        "model-a",
    );
    let rotated = connection(
        "primary",
        ModelApi::Responses,
        "https://example.com/v1",
        "secret-two",
        "model-b",
    );
    assert_eq!(original.identity, rotated.identity);
    assert_eq!(
        original.identity.endpoint,
        "https://example.com/v1".to_owned()
    );
    assert_eq!(original.identity.api, "openai-responses");

    let other_endpoint = connection(
        "primary",
        ModelApi::Responses,
        "https://example.com/v2",
        "secret-one",
        "model-a",
    );
    assert_ne!(original.identity, other_endpoint.identity);

    let other_api = connection(
        "primary",
        ModelApi::ChatCompletions,
        "https://example.com/v1",
        "secret-one",
        "model-a",
    );
    assert_ne!(original.identity, other_api.identity);
    assert_eq!(other_api.identity.api, "openai-chat-completions");
}

#[test]
fn credential_bearing_or_non_http_endpoints_are_rejected_without_echoing_secret() {
    let secret = "super-secret-api-key-value";
    for base_url in [
        "https://user:password@example.com/v1",
        "https://example.com/v1?token=secret",
        "https://example.com/v1#fragment",
        "ftp://example.com/v1",
        "/relative/path",
    ] {
        let error = match Connection::new(
            "id".to_owned(),
            ClientConfig::new(ModelApi::Responses, secret, base_url),
            "model".to_owned(),
        ) {
            Ok(_) => panic!("invalid endpoint was accepted: {base_url}"),
            Err(error) => error,
        };
        let message = format!("{error:?}");
        assert!(!message.contains(secret), "secret leaked for {base_url}");
        assert!(!message.contains("password"), "URL credentials were echoed");
    }
    assert!(
        Connection::new(
            "id".to_owned(),
            ClientConfig::new(ModelApi::Responses, secret, "https://example.com/v1"),
            String::new(),
        )
        .is_err()
    );
    assert!(
        Connection::new(
            String::new(),
            ClientConfig::new(ModelApi::Responses, secret, "https://example.com/v1"),
            "model".to_owned(),
        )
        .is_err()
    );
}
