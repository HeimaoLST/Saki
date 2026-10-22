# Saki MCP stdio tool example

A standalone MCP server example for Saki issue #107. It is not a Saki
dependency and does not import or rebuild the Saki crate. It uses `rmcp`
`=3.6.0` with `default-features = false` and exactly `server` and
`transport-io`.

## Build and run

From the repository root:

```sh
CARGO_TARGET_DIR=/private/tmp/saki107-example-target \
  cargo build --manifest-path examples/mcp_tool/Cargo.toml
```

The binary is:

```text
/private/tmp/saki107-example-target/debug/saki-mcp-tool-example
```

Run it with stdio (normally an MCP client launches it):

```sh
/private/tmp/saki107-example-target/debug/saki-mcp-tool-example
```

The process working directory is captured once at startup. `read_marker`
passes `cwd.join(path)` to `std::fs::read_to_string`; this minimal example does
not sandbox paths. It reads the actual UTF-8 file contents; it does not
generate or substitute marker text.

Protocol modes:

- default: MCP `2026-07-28` (SDK discovery lifecycle)
- `--legacy`: MCP `2025-11-25` (SDK legacy `initialize` lifecycle)

The SDK performs the handshake. The server does not manually implement
`initialize` or discovery.

## Tools

### `read_marker`

Input schema:

```json
{
  "type": "object",
  "properties": {
    "path": { "type": "string" }
  },
  "required": ["path"],
  "additionalProperties": false
}
```

`path` is joined to the server's startup cwd and read as UTF-8. A missing
path, a directory, or non-UTF-8 content is returned as a tool error
(`isError: true`) with a visible text message.

Successful result:

- `content[0].text` is the exact file contents (newlines preserved).
- `structuredContent` is `{ "path": "<path argument>", "marker": "<file text>" }`.

### `increment`

Input schema:

```json
{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}
```

It updates one `AtomicU64` counter owned by the server process. The first call
returns `1`; every call in the same process increments it. The result has
`content[0].text` equal to the decimal count and
`structuredContent: { "count": <integer> }`.

## Scope

The tool catalog is static. The server advertises only tools: it does not
advertise `listChanged` or subscriptions. It also does not add resources,
prompts, sampling, roots, or task features.
