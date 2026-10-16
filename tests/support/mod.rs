use std::{
    path::PathBuf,
    sync::atomic::{AtomicUsize, Ordering},
};

use serde_json::{Value, json};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::{TcpListener, TcpStream},
    task::JoinHandle,
};

use crate::client::ModelClient;

pub fn completed(text: &str) -> Value {
    json!({"id":"response", "status":"completed", "output":[{
        "type":"message", "id":"message", "status":"completed", "role":"assistant",
        "content":[{"type":"output_text", "text":text}]
    }], "usage":{"input_tokens":6000,"output_tokens":40}})
}

pub fn overflow() -> Value {
    json!({"error":{"code":"context_length_exceeded", "message":"input exceeds context window"}})
}

pub async fn scripted_http(responses: Vec<(u16, Value)>) -> (ModelClient, JoinHandle<Vec<Value>>) {
    scripted_bodies(
        responses
            .into_iter()
            .map(|(status, value)| {
                (
                    status,
                    "application/json",
                    serde_json::to_vec(&value).unwrap(),
                )
            })
            .collect(),
    )
    .await
}

pub async fn scripted_stream(responses: Vec<String>) -> (ModelClient, JoinHandle<Vec<Value>>) {
    scripted_bodies(
        responses
            .into_iter()
            .map(|body| (200, "text/event-stream", body.into_bytes()))
            .collect(),
    )
    .await
}

pub async fn scripted_bodies(
    responses: Vec<(u16, &'static str, Vec<u8>)>,
) -> (ModelClient, JoinHandle<Vec<Value>>) {
    let (base_url, task) = scripted_server(responses).await;
    (ModelClient::for_test(base_url), task)
}

/// A scripted wire mock serving one response per element and returning the
/// request bodies it received. Shared by the Responses and Chat test helpers.
pub async fn scripted_server(
    responses: Vec<(u16, &'static str, Vec<u8>)>,
) -> (String, JoinHandle<Vec<Value>>) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let task = tokio::spawn(async move {
        let mut requests = Vec::new();
        for (status, content_type, body) in responses {
            let (mut socket, _) = listener.accept().await.unwrap();
            requests.push(read_request(&mut socket).await);
            socket.write_all(format!("HTTP/1.1 {status} Test\r\nContent-Type: {content_type}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n", body.len()).as_bytes()).await.unwrap();
            socket.write_all(&body).await.unwrap();
        }
        requests
    });
    (format!("http://{address}/v1"), task)
}

pub async fn read_request(socket: &mut TcpStream) -> Value {
    let mut data = Vec::new();
    let header_end = loop {
        let mut buffer = [0; 4096];
        let n = socket.read(&mut buffer).await.unwrap();
        assert!(n > 0);
        data.extend_from_slice(&buffer[..n]);
        if let Some(index) = data.windows(4).position(|window| window == b"\r\n\r\n") {
            break index + 4;
        }
    };
    let length: usize = std::str::from_utf8(&data[..header_end])
        .unwrap()
        .lines()
        .find_map(|line| {
            let (name, value) = line.split_once(':')?;
            name.eq_ignore_ascii_case("content-length")
                .then(|| value.trim().parse().unwrap())
        })
        .unwrap();
    while data.len() < header_end + length {
        let mut buffer = [0; 4096];
        let n = socket.read(&mut buffer).await.unwrap();
        assert!(n > 0);
        data.extend_from_slice(&buffer[..n]);
    }
    serde_json::from_slice(&data[header_end..header_end + length]).unwrap()
}

pub fn sse(event: Value) -> String {
    format!("data: {event}\n\n")
}

pub async fn requests(task: JoinHandle<Vec<Value>>) -> Vec<Value> {
    tokio::time::timeout(std::time::Duration::from_secs(5), task)
        .await
        .unwrap()
        .unwrap()
}

pub struct Journal(pub PathBuf);

impl Journal {
    pub fn new() -> Self {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        Self(std::env::temp_dir().join(format!(
            "saki-overflow-{}-{}.jsonl",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        )))
    }

    pub fn records(&self) -> Vec<Value> {
        std::fs::read_to_string(&self.0)
            .unwrap()
            .lines()
            .map(|line| serde_json::from_str(line).unwrap())
            .collect()
    }
}

impl Drop for Journal {
    fn drop(&mut self) {
        let _ = std::fs::remove_file(&self.0);
    }
}
