use std::borrow::Cow;
use std::fs;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};

use rmcp::model::{
    CallToolRequestMethod, CallToolRequestParams, CallToolResponse, CallToolResult, ContentBlock,
    Implementation, JsonObject, ListToolsResult, PaginatedRequestParams, ProtocolVersion,
    ServerCapabilities, ServerConfig, Tool, ToolAnnotations,
};
use rmcp::service::RequestContext;
use rmcp::{ErrorData as McpError, RoleServer, ServerHandler, ServiceExt};
use serde::Deserialize;
use serde::de::DeserializeOwned;
use serde_json::{Value, json};

const SERVER_NAME: &str = "saki-mcp-tool-example";
const READ_MARKER: &str = "read_marker";
const INCREMENT: &str = "increment";

#[derive(Clone, Copy)]
enum ProtocolMode {
    Modern,
    Legacy,
}

struct MarkerServer {
    // Captured once at process startup; tool calls cannot change it.
    cwd: PathBuf,
    mode: ProtocolMode,
    increment: AtomicU64,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ReadMarkerArgs {
    path: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct IncrementArgs {}

impl MarkerServer {
    fn new(cwd: PathBuf, mode: ProtocolMode) -> Self {
        Self {
            cwd,
            mode,
            increment: AtomicU64::new(0),
        }
    }

    fn protocol_version(&self) -> ProtocolVersion {
        match self.mode {
            ProtocolMode::Modern => ProtocolVersion::V_2026_07_28,
            ProtocolMode::Legacy => ProtocolVersion::V_2025_11_25,
        }
    }

    fn read_marker(&self, args: JsonObject) -> Result<CallToolResult, McpError> {
        let args = match parse_args::<ReadMarkerArgs>(args) {
            Ok(args) => args,
            Err(error) => return Ok(tool_error(format!("{READ_MARKER}: {error}"))),
        };
        let path = self.cwd.join(&args.path);
        let marker = match fs::read_to_string(&path) {
            Ok(marker) => marker,
            Err(error) => {
                return Ok(tool_error(format!(
                    "{READ_MARKER}: cannot read {}: {error}",
                    path.display()
                )));
            }
        };
        let mut result =
            CallToolResult::structured(json!({ "path": args.path, "marker": marker.clone() }));
        result.content = vec![ContentBlock::text(marker)];
        Ok(result)
    }

    fn increment(&self, args: JsonObject) -> Result<CallToolResult, McpError> {
        if let Err(error) = parse_args::<IncrementArgs>(args) {
            return Ok(tool_error(format!("{INCREMENT}: {error}")));
        }
        let count = self.increment.fetch_add(1, Ordering::SeqCst) + 1;
        let mut result = CallToolResult::structured(json!({ "count": count }));
        result.content = vec![ContentBlock::text(count.to_string())];
        Ok(result)
    }

    fn tools() -> Vec<Tool> {
        vec![read_marker_tool(), increment_tool()]
    }
}

impl ServerHandler for MarkerServer {
    fn get_info(&self) -> ServerConfig {
        ServerConfig::new(ServerCapabilities::builder().enable_tools().build())
            .with_protocol_version(self.protocol_version())
            .with_server_info(Implementation::new(
                SERVER_NAME,
                env!("CARGO_PKG_VERSION"),
            ))
            .with_instructions(
                "Read a file using a path resolved against the server's startup cwd, or increment a per-process counter.",
            )
    }

    fn supported_protocol_versions(&self) -> Cow<'static, [ProtocolVersion]> {
        Cow::Owned(vec![self.protocol_version()])
    }

    async fn list_tools(
        &self,
        _request: Option<PaginatedRequestParams>,
        _context: RequestContext<RoleServer>,
    ) -> Result<ListToolsResult, McpError> {
        Ok(ListToolsResult::with_all_items(Self::tools()))
    }

    async fn call_tool(
        &self,
        request: CallToolRequestParams,
        _context: RequestContext<RoleServer>,
    ) -> Result<CallToolResponse, McpError> {
        let arguments = request.arguments.unwrap_or_default();
        match request.name.as_ref() {
            READ_MARKER => self.read_marker(arguments).map(Into::into),
            INCREMENT => self.increment(arguments).map(Into::into),
            _ => Err(McpError::method_not_found::<CallToolRequestMethod>()),
        }
    }
}

fn parse_args<T: DeserializeOwned>(args: JsonObject) -> Result<T, String> {
    serde_json::from_value(Value::Object(args))
        .map_err(|error| format!("invalid arguments: {error}"))
}

fn tool_error(message: impl Into<String>) -> CallToolResult {
    CallToolResult::error(vec![ContentBlock::text(message)])
}

fn schema(value: Value) -> Arc<JsonObject> {
    Arc::new(rmcp::model::object(value))
}

fn read_marker_tool() -> Tool {
    Tool::new(
        READ_MARKER,
        "Read a UTF-8 text file using cwd.join(path). The text content is the exact file contents.",
        schema(json!({
            "type": "object",
            "properties": {
                "path": {
                    "type": "string",
                    "description": "Path passed to cwd.join(path) and then read."
                }
            },
            "required": ["path"],
            "additionalProperties": false
        })),
    )
    .with_annotations(
        ToolAnnotations::new()
            .read_only(true)
            .idempotent(true)
            .open_world(false),
    )
    .with_raw_output_schema(schema(json!({
        "type": "object",
        "properties": {
            "path": { "type": "string" },
            "marker": { "type": "string" }
        },
        "required": ["path", "marker"],
        "additionalProperties": false
    })))
}

fn increment_tool() -> Tool {
    Tool::new(
        INCREMENT,
        "Increment and return a counter owned by this server process.",
        schema(json!({
            "type": "object",
            "properties": {},
            "additionalProperties": false
        })),
    )
    .with_annotations(
        ToolAnnotations::new()
            .read_only(false)
            .destructive(false)
            .idempotent(false)
            .open_world(false),
    )
    .with_raw_output_schema(schema(json!({
        "type": "object",
        "properties": {
            "count": { "type": "integer", "minimum": 1 }
        },
        "required": ["count"],
        "additionalProperties": false
    })))
}

fn parse_mode() -> Result<ProtocolMode, String> {
    let mut mode = ProtocolMode::Modern;
    for argument in std::env::args().skip(1) {
        match argument.as_str() {
            "--legacy" => mode = ProtocolMode::Legacy,
            "--help" | "-h" => {
                print_help();
                std::process::exit(0);
            }
            other => {
                return Err(format!(
                    "unknown argument {other:?}; expected --legacy or --help"
                ));
            }
        }
    }
    Ok(mode)
}

fn print_help() {
    println!(
        "Usage: {SERVER_NAME} [--legacy]\n\
         \n\
         Default: MCP 2026-07-28 discovery lifecycle.\n\
         --legacy: MCP 2025-11-25 initialize lifecycle.\n\
         \n\
         The server reads files under its process working directory."
    );
}

fn run() -> Result<(), Box<dyn std::error::Error>> {
    let mode = parse_mode()?;
    let cwd = std::env::current_dir()?;
    let server = MarkerServer::new(cwd, mode);
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_io()
        .enable_time()
        .build()?;
    runtime.block_on(async move {
        let service = server.serve(rmcp::transport::stdio()).await?;
        service.waiting().await?;
        Ok::<(), Box<dyn std::error::Error>>(())
    })
}

fn main() {
    if let Err(error) = run() {
        eprintln!("{SERVER_NAME}: {error}");
        std::process::exit(1);
    }
}
