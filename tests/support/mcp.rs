use anyhow::{Context, Result, ensure};
use serde_json::{Value, json};
use std::{
    path::{Path, PathBuf},
    sync::Arc,
    time::Duration,
};
use tokio::{
    io::{AsyncBufReadExt, AsyncWriteExt, BufReader, ReadHalf, WriteHalf, split},
    net::{TcpListener, TcpStream},
    sync::Mutex,
    time::timeout,
};

const WIRE_MAX: usize = 8 * 1024 * 1024;
pub(crate) const WIRE_TIMEOUT: Duration = Duration::from_secs(5);
const TRACE_MAX: usize = 256;

#[derive(Clone)]
pub(crate) struct ControlSender {
    writer: Arc<Mutex<WriteHalf<TcpStream>>>,
}

impl ControlSender {
    pub(crate) async fn send(&self, value: Value) -> Result<()> {
        let mut bytes = serde_json::to_vec(&value).context("serialize control frame")?;
        ensure!(bytes.len() <= WIRE_MAX, "control frame exceeds 8 MiB");
        bytes.push(b'\n');
        let mut writer = self.writer.lock().await;
        timeout(WIRE_TIMEOUT, async {
            writer.write_all(&bytes).await?;
            writer.flush().await?;
            Ok::<(), std::io::Error>(())
        })
        .await
        .context("control write timed out")??;
        Ok(())
    }
}

pub(crate) struct ControlConnection {
    reader: BufReader<ReadHalf<TcpStream>>,
    pub(crate) sender: ControlSender,
    pub(crate) trace: Vec<Value>,
}

impl ControlConnection {
    pub(crate) fn from_stream(stream: TcpStream) -> Self {
        let (read, write) = split(stream);
        Self {
            reader: BufReader::new(read),
            sender: ControlSender {
                writer: Arc::new(Mutex::new(write)),
            },
            trace: Vec::new(),
        }
    }

    pub(crate) async fn accept(listener: &TcpListener) -> Result<Self> {
        let (stream, _) = timeout(WIRE_TIMEOUT, listener.accept())
            .await
            .context("control accept timed out")??;
        Ok(Self::from_stream(stream))
    }

    pub(crate) async fn recv(&mut self) -> Result<Value> {
        let mut bytes = Vec::new();
        let read = timeout(WIRE_TIMEOUT, self.reader.read_until(b'\n', &mut bytes))
            .await
            .context("control read timed out")??;
        ensure!(read != 0, "control EOF");
        ensure!(bytes.len() <= WIRE_MAX, "control frame exceeds 8 MiB");
        let value: Value = serde_json::from_slice(&bytes).context("invalid control JSON frame")?;
        let value = if value.get("event").and_then(Value::as_str) == Some("received") {
            value
                .get("message")
                .cloned()
                .context("received envelope has no message")?
        } else {
            value
        };
        ensure!(
            self.trace.len() < TRACE_MAX,
            "control trace capacity exceeded"
        );
        self.trace.push(value.clone());
        Ok(value)
    }

    pub(crate) async fn next_request(&mut self) -> Result<Value> {
        loop {
            let value = self.recv().await?;
            if value.get("id").is_none() {
                continue;
            }
            ensure!(
                value.get("method").and_then(Value::as_str).is_some(),
                "control frame with id but no method"
            );
            return Ok(value);
        }
    }

    pub(crate) async fn send(&self, value: Value) -> Result<()> {
        self.sender.send(json!({"send":value})).await
    }

    pub(crate) async fn respond(&self, id: &Value, result: Value) -> Result<()> {
        self.send(json!({"jsonrpc": "2.0", "id": id, "result": result}))
            .await
    }

    pub(crate) async fn expect_ready(&mut self) -> Result<()> {
        let event = self.recv().await?;
        ensure!(
            event.get("event").and_then(Value::as_str) == Some("ready"),
            "expected ready event"
        );
        let environment = event
            .get("environment")
            .and_then(Value::as_array)
            .context("ready environment missing")?;
        ensure!(
            environment
                .iter()
                .any(|name| name.as_str() == Some("MCP_TEST_TOKEN")),
            "test environment variable was not inherited"
        );
        ensure!(
            !event.to_string().contains("wire-test-token"),
            "ready event leaked an environment value"
        );
        Ok(())
    }
}

pub(crate) async fn accept_ready(listener: &TcpListener) -> Result<ControlConnection> {
    let mut control = ControlConnection::accept(listener).await?;
    control.expect_ready().await?;
    Ok(control)
}

pub(crate) fn python_command() -> PathBuf {
    for candidate in [
        "/usr/bin/python3",
        "/usr/local/bin/python3",
        "/opt/homebrew/bin/python3",
    ] {
        let path = PathBuf::from(candidate);
        if path.is_file() {
            return path;
        }
    }
    panic!("python3 is required for MCP wire tests");
}

pub(crate) fn proxy_script() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/tools/mcp_server.py")
}

pub(crate) fn tool(name: &str) -> Value {
    json!({
        "name": name,
        "description": format!("tool {name}"),
        "inputSchema": {
            "type": "object",
            "properties": {},
            "additionalProperties": false
        }
    })
}

pub(crate) fn discover_result(list_changed: bool) -> Value {
    json!({
        "resultType": "complete",
        "supportedVersions": ["2026-07-28"],
        "capabilities": {
            "tools": if list_changed {
                json!({"listChanged": true})
            } else {
                json!({})
            }
        },
        "ttlMs": 0,
        "cacheScope": "private"
    })
}

pub(crate) fn initialize_result() -> Value {
    json!({
        "protocolVersion": "2025-11-25",
        "capabilities": {"tools": {}},
        "serverInfo": {"name": "python-wire", "version": "1"}
    })
}

pub(crate) fn tools_page(tools: Vec<Value>, next: Option<&str>, modern: bool) -> Value {
    let mut result = json!({"tools": tools});
    if modern {
        result["resultType"] = json!("complete");
        result["ttlMs"] = json!(0);
        result["cacheScope"] = json!("private");
    }
    if let Some(next) = next {
        result["nextCursor"] = json!(next);
    }
    result
}

pub(crate) fn listen_ack(id: &Value) -> Value {
    json!({
        "jsonrpc": "2.0",
        "method": "notifications/subscriptions/acknowledged",
        "params": {
            "_meta": {"io.modelcontextprotocol/subscriptionId": id},
            "notifications": {"toolsListChanged": true}
        }
    })
}

pub(crate) fn list_changed(id: &Value) -> Value {
    json!({
        "jsonrpc": "2.0",
        "method": "notifications/tools/list_changed",
        "params": {
            "_meta": {"io.modelcontextprotocol/subscriptionId": id}
        }
    })
}

pub(crate) async fn serve_tools_pages(
    control: &mut ControlConnection,
    pages: &[Vec<Value>],
    modern: bool,
) -> Result<()> {
    let mut cursor: Option<String> = None;
    for (index, page) in pages.iter().enumerate() {
        let request = control.next_request().await?;
        ensure!(
            request["method"].as_str() == Some("tools/list"),
            "expected tools/list, got {}",
            request["method"]
        );
        let actual = request["params"]
            .get("cursor")
            .and_then(Value::as_str)
            .map(str::to_owned);
        ensure!(actual == cursor, "unexpected tools/list cursor");
        let next = (index + 1 < pages.len()).then(|| format!("page-{}", index + 2));
        control
            .respond(
                &request["id"],
                tools_page(page.clone(), next.as_deref(), modern),
            )
            .await?;
        cursor = next;
    }
    Ok(())
}

pub(crate) async fn serve_modern_startup(
    control: &mut ControlConnection,
    list_changed: bool,
    pages: &[Vec<Value>],
) -> Result<Option<Value>> {
    let request = control.next_request().await?;
    ensure!(
        request["method"].as_str() == Some("server/discover"),
        "expected server/discover, got {}",
        request["method"]
    );
    control
        .respond(&request["id"], discover_result(list_changed))
        .await?;

    let mut listen = None;
    if list_changed {
        let request = control.next_request().await?;
        ensure!(
            request["method"].as_str() == Some("subscriptions/listen"),
            "expected subscriptions/listen, got {}",
            request["method"]
        );
        ensure!(
            request["params"]["notifications"] == json!({"toolsListChanged": true}),
            "subscription filter was not tools-only"
        );
        let id = request["id"].clone();
        control.send(listen_ack(&id)).await?;
        listen = Some(id);
    }
    serve_tools_pages(control, pages, true).await?;
    Ok(listen)
}

pub(crate) async fn serve_legacy_startup(
    control: &mut ControlConnection,
    pages: &[Vec<Value>],
) -> Result<()> {
    let request = control.next_request().await?;
    ensure!(
        request["method"].as_str() == Some("initialize"),
        "expected initialize, got {}",
        request["method"]
    );
    control.respond(&request["id"], initialize_result()).await?;
    serve_tools_pages(control, pages, false).await
}
