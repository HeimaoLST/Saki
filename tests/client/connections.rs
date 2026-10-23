use super::*;
use std::{
    fs,
    io::ErrorKind,
    net::TcpListener,
    path::PathBuf,
    sync::atomic::{AtomicUsize, Ordering},
};

fn temp_path() -> PathBuf {
    static NEXT: AtomicUsize = AtomicUsize::new(0);
    std::env::temp_dir().join(format!(
        "saki-connections-{}-{}.json",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    ))
}

fn definition(
    id: &str,
    api: &str,
    base_url: &str,
    api_key_env: &str,
    default_model: &str,
) -> ConnectionDefinition {
    ConnectionDefinition {
        id: id.to_owned(),
        api: api.to_owned(),
        base_url: base_url.to_owned(),
        api_key_env: api_key_env.to_owned(),
        default_model: default_model.to_owned(),
    }
}

fn host(
    default_connection_id: &str,
    connections: Vec<ConnectionDefinition>,
    max_active_tasks: usize,
) -> HostConfig {
    HostConfig {
        default_connection_id: default_connection_id.to_owned(),
        connections,
        max_active_tasks,
        tool_sources: Vec::new(),
    }
}

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

#[test]
fn host_config_load_and_build_is_static_and_views_do_not_expose_credentials() {
    let path = temp_path();
    fs::write(
        &path,
        serde_json::to_vec(&serde_json::json!({
            "default_connection_id": "zeta",
            "connections": [
                {
                    "id": "zeta",
                    "api": "openai-responses",
                    "base_url": "https://zeta.example/v1/",
                    "api_key_env": "ZETA_KEY",
                    "default_model": "zeta-model"
                },
                {
                    "id": "alpha",
                    "api": "openai-chat-completions",
                    "base_url": "https://alpha.example/v1",
                    "api_key_env": "ALPHA_KEY",
                    "default_model": "alpha-model"
                }
            ],
            "max_active_tasks": 2
        }))
        .unwrap(),
    )
    .unwrap();
    let config = HostConfig::load(&path).unwrap();
    fs::remove_file(path).unwrap();
    assert_eq!(config.max_active_tasks, 2);
    assert!(config.tool_sources.is_empty());
    let registry = config
        .build_connections(|name| match name {
            "ZETA_KEY" => Some("zeta-secret".to_owned()),
            "ALPHA_KEY" => Some("alpha-secret".to_owned()),
            _ => None,
        })
        .unwrap();
    assert_eq!(registry.len(), 2);
    assert_eq!(registry.default().identity.connection_id, "zeta");
    assert_eq!(registry.get("alpha").unwrap().default_model, "alpha-model");
    assert_eq!(
        registry
            .views()
            .iter()
            .map(|view| view.id.as_str())
            .collect::<Vec<_>>(),
        ["alpha", "zeta"]
    );
    let views = serde_json::to_string(&registry.views()).unwrap();
    assert!(!views.contains("zeta-secret"));
    assert!(!views.contains("alpha-secret"));
    assert!(!views.contains("api_key"));
    assert!(views.contains("https://zeta.example/v1"));
}

#[test]
fn host_config_rejects_invalid_capacity_directory_credentials_and_tool_sources() {
    let base = definition(
        "main",
        "openai-responses",
        "https://example.com/v1",
        "MAIN_KEY",
        "model-a",
    );
    let key = |_: &str| Some("secret".to_owned());

    assert!(
        host("main", vec![base.clone()], 0)
            .build_connections(key)
            .is_err()
    );
    assert!(
        host("main", vec![base.clone()], Semaphore::MAX_PERMITS + 1)
            .build_connections(key)
            .is_err()
    );
    assert!(
        host("missing", vec![base.clone()], 1)
            .build_connections(key)
            .is_err()
    );
    assert!(
        host("main", vec![base.clone(), base.clone()], 1)
            .build_connections(key)
            .is_err()
    );
    let mut unknown_api = base.clone();
    unknown_api.api = "unknown-api".to_owned();
    assert!(
        host("main", vec![unknown_api], 1)
            .build_connections(key)
            .is_err()
    );
    assert!(
        host("main", vec![base.clone()], 1)
            .build_connections(|_| None)
            .is_err()
    );
    assert!(
        host("main", vec![base.clone()], 1)
            .build_connections(|_| Some(String::new()))
            .is_err()
    );

    let mut tools = host("main", vec![base.clone()], 1);
    tools.tool_sources = vec![serde_json::json!({"id": "project"})];
    let error = tools
        .build_connections(key)
        .err()
        .expect("tool_sources must be rejected");
    assert!(format!("{error}").contains("tool_sources"));

    let mut credentials = base;
    credentials.base_url = "https://user:password@example.com/v1".to_owned();
    let error = host("main", vec![credentials], 1)
        .build_connections(|_| Some("super-secret-key".to_owned()))
        .err()
        .expect("credential-bearing endpoint must be rejected");
    let message = format!("{error:?}");
    assert!(!message.contains("super-secret-key"));
    assert!(!message.contains("password"));
}

#[test]
fn host_config_load_denies_unknown_fields_and_defaults_runtime_fields() {
    let path = temp_path();
    fs::write(
        &path,
        br#"{
            "default_connection_id":"main",
            "connections":[{
                "id":"main",
                "api":"openai-responses",
                "base_url":"https://example.com/v1",
                "api_key_env":"MAIN_KEY",
                "default_model":"model-a"
            }],
            "unexpected":true
        }"#,
    )
    .unwrap();
    assert!(HostConfig::load(&path).is_err());
    fs::remove_file(&path).unwrap();

    fs::write(
        &path,
        br#"{
            "default_connection_id":"main",
            "connections":[{
                "id":"main",
                "api":"openai-responses",
                "base_url":"https://example.com/v1",
                "api_key_env":"MAIN_KEY",
                "default_model":"model-a",
                "unexpected":true
            }]
        }"#,
    )
    .unwrap();
    assert!(HostConfig::load(&path).is_err());
    fs::remove_file(&path).unwrap();

    fs::write(
        &path,
        br#"{
            "default_connection_id":"main",
            "connections":[{
                "id":"main",
                "api":"openai-responses",
                "base_url":"https://example.com/v1",
                "api_key_env":"MAIN_KEY",
                "default_model":"model-a"
            }]
        }"#,
    )
    .unwrap();
    let config = HostConfig::load(&path).unwrap();
    fs::remove_file(path).unwrap();
    assert_eq!(config.max_active_tasks, 1);
    assert!(config.tool_sources.is_empty());
}

#[test]
fn building_connections_does_not_open_http_connections() {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    listener.set_nonblocking(true).unwrap();
    let address = listener.local_addr().unwrap();
    let config = host(
        "main",
        vec![definition(
            "main",
            "openai-responses",
            &format!("http://{address}/v1"),
            "MAIN_KEY",
            "model-a",
        )],
        1,
    );
    let registry = config
        .build_connections(|_| Some("not-a-real-key".to_owned()))
        .unwrap();
    assert_eq!(registry.len(), 1);
    assert_eq!(listener.accept().unwrap_err().kind(), ErrorKind::WouldBlock);
}
