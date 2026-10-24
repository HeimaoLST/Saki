use super::*;
use serde_json::{Value, json};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpListener,
    task::JoinHandle,
};

fn fixture(context: usize) -> Value {
    json!({
        "providers":{"provider":{"models":{"model":{"name":"Unified Model","limit":{"context":context,"input":context-4096,"output":8192}}}}},
        "models":{"lab/model":{"name":"Unified Model","limit":{"context":1_000_000,"output":32768}}}
    })
}

fn temp_dir() -> std::path::PathBuf {
    static NEXT_ID: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
    let path = std::env::temp_dir().join(format!(
        "saki-models-{}-{}",
        std::process::id(),
        NEXT_ID.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
    ));
    fs::create_dir(&path).unwrap();
    path
}

async fn mock_catalog(status: u16, body: Vec<u8>) -> (String, JoinHandle<String>) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let task = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
        let mut data = Vec::new();
        while !data.windows(4).any(|window| window == b"\r\n\r\n") {
            let mut buffer = [0; 4096];
            let length = socket.read(&mut buffer).await.unwrap();
            assert!(length > 0);
            data.extend_from_slice(&buffer[..length]);
        }
        let headers = String::from_utf8(data).unwrap();
        socket.write_all(format!("HTTP/1.1 {status} Test\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",body.len()).as_bytes()).await.unwrap();
        socket.write_all(&body).await.unwrap();
        headers
    });
    (format!("http://{address}/catalog.json"), task)
}

#[test]
fn exact_match_precedes_name_and_unknown_models_have_no_fake_limits() {
    let catalog = ModelCatalog::parse(&serde_json::to_vec(&fixture(131072)).unwrap()).unwrap();
    assert_eq!(catalog.limits("provider/model").unwrap().context, 131072);
    assert_eq!(catalog.limits("lab/model").unwrap().context, 1_000_000);
    assert_eq!(
        catalog.limits("new-provider/model").unwrap().context,
        1_000_000
    );
    assert_eq!(
        catalog.limits("proxy/lab/model").unwrap().context,
        1_000_000
    );
    assert_eq!(catalog.limits("Unified Model").unwrap().context, 1_000_000);
    assert!(catalog.limits("provider/model-new-version").is_none());
    assert!(catalog.limits("unknown").is_none());
}

#[test]
fn ambiguous_model_names_do_not_depend_on_hash_map_order() {
    let mut data = fixture(131072);
    data["models"]["other-lab/model"] = json!({"limit":{"context":65536,"output":8192}});
    let catalog = ModelCatalog::parse(&serde_json::to_vec(&data).unwrap()).unwrap();
    assert!(catalog.limits("new-provider/model").is_none());
    assert_eq!(catalog.limits("provider/model").unwrap().context, 131072);
}

#[tokio::test]
async fn fresh_cache_does_not_make_a_network_request() {
    let directory = temp_dir();
    let path = directory.join("models.json");
    fs::write(&path, serde_json::to_vec(&fixture(131072)).unwrap()).unwrap();
    let catalog = ModelCatalog::load_from(
        &path,
        "http://127.0.0.1:1/catalog.json",
        Duration::from_secs(86400),
    )
    .await
    .unwrap();
    assert_eq!(catalog.limits("provider/model").unwrap().context, 131072);
    fs::remove_dir_all(directory).unwrap();
}

#[tokio::test]
async fn expired_cache_refreshes_without_credentials() {
    let directory = temp_dir();
    let path = directory.join("models.json");
    fs::write(&path, serde_json::to_vec(&fixture(131072)).unwrap()).unwrap();
    let (url, server) = mock_catalog(200, serde_json::to_vec(&fixture(262144)).unwrap()).await;
    let catalog = ModelCatalog::load_from(&path, &url, Duration::ZERO)
        .await
        .unwrap();
    assert_eq!(catalog.limits("provider/model").unwrap().context, 262144);
    let headers = server.await.unwrap().to_lowercase();
    assert!(headers.starts_with("get /catalog.json "));
    assert!(!headers.contains("authorization:"));
    assert!(headers.contains("user-agent: saki/0.1"));
    let cached = ModelCatalog::parse(&fs::read(&path).unwrap()).unwrap();
    assert_eq!(cached.limits("provider/model").unwrap().context, 262144);
    fs::remove_dir_all(directory).unwrap();
}

#[tokio::test]
async fn refresh_failures_preserve_the_previous_snapshot() {
    for (status, body) in [
        (500, b"failed".to_vec()),
        (200, b"not json".to_vec()),
        (200, b"{\"providers\":{},\"models\":{}}".to_vec()),
    ] {
        let directory = temp_dir();
        let path = directory.join("models.json");
        let original = serde_json::to_vec(&fixture(131072)).unwrap();
        fs::write(&path, &original).unwrap();
        let (url, server) = mock_catalog(status, body).await;
        let catalog = ModelCatalog::load_from(&path, &url, Duration::ZERO)
            .await
            .unwrap();
        server.await.unwrap();
        assert_eq!(catalog.limits("provider/model").unwrap().context, 131072);
        assert_eq!(fs::read(&path).unwrap(), original);
        fs::remove_dir_all(directory).unwrap();
    }
}

#[tokio::test]
async fn missing_or_corrupt_cache_is_rebuilt_only_from_a_valid_response() {
    for corrupt in [false, true] {
        let directory = temp_dir();
        let path = directory.join("models.json");
        if corrupt {
            fs::write(&path, "invalid cache").unwrap();
        }
        let (url, server) = mock_catalog(200, serde_json::to_vec(&fixture(262144)).unwrap()).await;
        let catalog = ModelCatalog::load_from(&path, &url, Duration::ZERO)
            .await
            .unwrap();
        server.await.unwrap();
        assert_eq!(catalog.limits("provider/model").unwrap().context, 262144);
        fs::remove_dir_all(directory).unwrap();
    }
    let directory = temp_dir();
    let path = directory.join("models.json");
    let (url, server) = mock_catalog(500, b"failed".to_vec()).await;
    assert!(
        ModelCatalog::load_from(&path, &url, Duration::ZERO)
            .await
            .is_err()
    );
    server.await.unwrap();
    assert!(!path.exists());
    fs::remove_dir_all(directory).unwrap();
}

#[test]
fn provider_only_snapshot_exposes_usable_entries() {
    let data = json!({
        "providers": {
            "provider": {
                "models": {
                    "model": {
                        "name": "Provider Only",
                        "limit": {"context": 131072, "input": 120000, "output": 8192}
                    }
                }
            }
        }
    });
    let catalog = ModelCatalog::from_snapshot(&serde_json::to_vec(&data).unwrap()).unwrap();
    let entries = catalog.entries();
    assert_eq!(entries.len(), 1);
    assert_eq!(entries[0].id, "provider/model");
    assert_eq!(entries[0].name.as_deref(), Some("Provider Only"));
    assert_eq!(
        entries[0].limits,
        ModelLimits {
            context: 131072,
            input: Some(120000),
            output: Some(8192),
        }
    );
    assert_eq!(catalog.limits("provider/model"), Some(entries[0].limits));
}

#[test]
fn entries_are_deduplicated_sorted_and_match_direct_limits() {
    let data = json!({
        "providers": {
            "zeta": {
                "models": {
                    "b": {"name": "Provider B", "limit": {"context": 200, "output": 10}},
                    "dup": {"name": "Provider Dup", "limit": {"context": 300}},
                    "zero": {"limit": {"context": 0}}
                }
            },
            "alpha": {
                "models": {
                    "a": {"limit": {"context": 100}}
                }
            }
        },
        "models": {
            "zeta/dup": {"name": "Root Dup", "limit": {"context": 999}},
            "zeta/a": {"name": "Root A", "limit": {"context": 50}},
            "root-only": {"name": "Root Only", "limit": {"context": 400}},
            "bad/zero": {"limit": {"context": 0}},
            "shared/one": {"name": "Shared", "limit": {"context": 10}},
            "shared/two": {"name": "Shared", "limit": {"context": 20}}
        }
    });
    let catalog = ModelCatalog::from_snapshot(&serde_json::to_vec(&data).unwrap()).unwrap();
    let entries = catalog.entries();
    let ids: Vec<_> = entries.iter().map(|entry| entry.id.as_str()).collect();
    assert_eq!(
        ids,
        [
            "alpha/a",
            "root-only",
            "shared/one",
            "shared/two",
            "zeta/a",
            "zeta/b",
            "zeta/dup",
        ]
    );
    assert!(entries.windows(2).all(|pair| pair[0].id < pair[1].id));
    for entry in &entries {
        assert_eq!(catalog.limits(&entry.id), Some(entry.limits));
    }
    let duplicate = entries.iter().find(|entry| entry.id == "zeta/dup").unwrap();
    assert_eq!(duplicate.name.as_deref(), Some("Provider Dup"));
    assert_eq!(duplicate.limits.context, 300);
    assert!(catalog.limits("Shared").is_none());
}

#[test]
fn zero_context_entries_are_excluded_and_do_not_mask_a_valid_fallback() {
    let data = json!({
        "providers": {
            "p": {
                "models": {
                    "m": {"limit": {"context": 0}},
                    "n": {"limit": {"context": 0}}
                }
            }
        },
        "models": {
            "p/m": {"name": "Root Fallback", "limit": {"context": 42}},
            "valid": {"name": "Valid", "limit": {"context": 1}},
            "invalid": {"name": "Invalid", "limit": {"context": 0}}
        }
    });
    let catalog = ModelCatalog::from_snapshot(&serde_json::to_vec(&data).unwrap()).unwrap();
    let entries = catalog.entries();
    let ids: Vec<_> = entries.iter().map(|entry| entry.id.as_str()).collect();
    assert_eq!(ids, ["p/m", "valid"]);
    let fallback = entries.iter().find(|entry| entry.id == "p/m").unwrap();
    assert_eq!(fallback.name.as_deref(), Some("Root Fallback"));
    assert_eq!(fallback.limits.context, 42);
    assert_eq!(catalog.limits("p/m"), Some(fallback.limits));
    assert!(catalog.limits("invalid").is_none());
    assert_eq!(catalog.limits("valid").unwrap().context, 1);
}

#[test]
fn an_exact_qualified_root_id_wins_over_its_unqualified_alias() {
    let snapshot = br#"{"models":{"m":{"limit":{"context":100}},"p/m":{"limit":{"context":200}}}}"#;
    let catalog = ModelCatalog::from_snapshot(snapshot).unwrap();
    assert_eq!(catalog.limits("p/m").unwrap().context, 200);
    assert_eq!(
        catalog
            .entries()
            .iter()
            .find(|entry| entry.id == "p/m")
            .unwrap()
            .limits
            .context,
        200
    );
}
