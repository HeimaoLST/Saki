#!/usr/bin/env python3
"""Deterministic stdio MCP wire proxy used by the Rust MCP tests.

The proxy does not speak MCP itself. It forwards stdin JSONL to a control
channel, and writes control-provided JSON objects to stdout as JSONL.
"""

import argparse
import asyncio
import json
import os
import sys
from typing import Optional

MAX_FRAME = 8 * 1024 * 1024
STREAM_LIMIT = MAX_FRAME + 2


def fatal(message: str) -> None:
    try:
        sys.stderr.write(f"mcp_server.py: {message}\n")
        sys.stderr.flush()
    finally:
        os._exit(1)


def encode_line(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode() + b"\n"


async def send_control(
    writer: asyncio.StreamWriter, control_lock: asyncio.Lock, value: object
) -> None:
    async with control_lock:
        writer.write(encode_line(value))
        await writer.drain()


async def read_frame(
    reader: asyncio.StreamReader, label: str
) -> Optional[object]:
    try:
        line = await reader.readline()
    except (ValueError, asyncio.LimitOverrunError) as error:
        fatal(f"{label} frame exceeds {MAX_FRAME} bytes: {error}")
    if not line:
        return None

    content = line[:-1] if line.endswith(b"\n") else line
    if len(content) > MAX_FRAME:
        fatal(f"{label} frame exceeds {MAX_FRAME} bytes")
    try:
        return json.loads(content)
    except (json.JSONDecodeError, UnicodeDecodeError) as error:
        fatal(f"invalid {label} JSON: {error}")


async def stdin_pump(
    reader: asyncio.StreamReader,
    control: asyncio.StreamWriter,
    control_lock: asyncio.Lock,
    hold_after_stdin_eof: asyncio.Event,
    control_closed: asyncio.Event,
) -> None:
    while True:
        message = await read_frame(reader, "stdin")
        if message is None:
            await send_control(control, control_lock, {"event": "stdin_eof"})
            if hold_after_stdin_eof.is_set():
                await control_closed.wait()
            return
        await send_control(
            control, control_lock, {"event": "received", "message": message}
        )


def write_stdout(message: object) -> None:
    sys.stdout.buffer.write(encode_line(message))
    sys.stdout.buffer.flush()


async def control_pump(
    reader: asyncio.StreamReader,
    control: asyncio.StreamWriter,
    control_lock: asyncio.Lock,
    hold_after_stdin_eof: asyncio.Event,
    control_closed: asyncio.Event,
) -> None:
    while True:
        message = await read_frame(reader, "control")
        if message is None:
            control_closed.set()
            return
        if not isinstance(message, dict):
            fatal("control message must be a JSON object")
        if set(message) == {"exit"}:
            code = message["exit"]
            if not isinstance(code, int) or isinstance(code, bool):
                fatal("control exit must be an integer")
            os._exit(code)
        if set(message) == {"send"}:
            payload = message["send"]
            if not isinstance(payload, dict):
                fatal("control send must be a JSON object")
            write_stdout(payload)
            continue
        if set(message) == {"hold_after_stdin_eof"}:
            hold = message["hold_after_stdin_eof"]
            if not isinstance(hold, bool):
                fatal("hold_after_stdin_eof must be a boolean")
            if hold:
                hold_after_stdin_eof.set()
            else:
                hold_after_stdin_eof.clear()
            await send_control(
                control,
                control_lock,
                {"event": "hold_after_stdin_eof", "enabled": hold},
            )
            continue
        fatal("control message must contain exactly one of send, exit, or hold_after_stdin_eof")


async def open_stdin() -> tuple[asyncio.StreamReader, asyncio.BaseTransport]:
    loop = asyncio.get_running_loop()
    reader = asyncio.StreamReader(limit=STREAM_LIMIT)
    protocol = asyncio.StreamReaderProtocol(reader)
    transport, _ = await loop.connect_read_pipe(lambda: protocol, sys.stdin.buffer)
    return reader, transport


async def run(control_port: int) -> int:
    try:
        control_reader, control_writer = await asyncio.open_connection(
            "127.0.0.1", control_port, limit=STREAM_LIMIT
        )
    except OSError as error:
        sys.stderr.write(f"mcp_server.py: control connection failed: {error}\n")
        return 1

    stdin_reader, stdin_transport = await open_stdin()
    hold_after_stdin_eof = asyncio.Event()
    control_closed = asyncio.Event()
    control_lock = asyncio.Lock()
    await send_control(
        control_writer,
        control_lock,
        {
            "event": "ready",
            "pid": os.getpid(),
            "cwd": os.getcwd(),
            "environment": sorted(os.environ),
        },
    )

    stdin_task = asyncio.create_task(
        stdin_pump(
            stdin_reader,
            control_writer,
            control_lock,
            hold_after_stdin_eof,
            control_closed,
        )
    )
    control_task = asyncio.create_task(
        control_pump(
            control_reader,
            control_writer,
            control_lock,
            hold_after_stdin_eof,
            control_closed,
        )
    )
    done, pending = await asyncio.wait(
        {stdin_task, control_task}, return_when=asyncio.FIRST_COMPLETED
    )
    for task in pending:
        task.cancel()
    await asyncio.gather(*pending, return_exceptions=True)

    stdin_transport.close()
    control_writer.close()
    try:
        await control_writer.wait_closed()
    except OSError:
        pass

    for task in done:
        error = task.exception()
        if error is not None:
            raise error
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--control-port", type=int, required=True)
    args = parser.parse_args()
    try:
        return asyncio.run(run(args.control_port))
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
