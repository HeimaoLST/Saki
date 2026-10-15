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
