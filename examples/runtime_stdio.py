#!/usr/bin/env python3
"""No-GUI stdio v1 client and deterministic cross-process tool smoke.

Build: cargo build --locked
Offline: python3 examples/runtime_stdio.py --self-test
Live: API_KEY=... SAKI_API=openai-chat-completions SAKI_MODEL=... \
      python3 examples/runtime_stdio.py --prompt 'Use read to inspect probe.txt'
"""
import argparse
import json
import os
from pathlib import Path
import queue
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Runtime:
    def __init__(self, binary, *, cwd=None, env=None):
        self.process = subprocess.Popen(
            [str(binary), "--stdio"], cwd=cwd, env=env,
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True,
        )
        self.messages = queue.Queue()
        self.received = []
        self.request_id = 0
        self.reader = threading.Thread(target=self._read, daemon=True)
        self.reader.start()
        ready = self.next()
        assert ready["version"] == 1 and ready["type"] == "ready", ready

    def _read(self):
        try:
            for line in self.process.stdout:
                self.messages.put(json.loads(line))
        except Exception as error:
            self.messages.put(error)
        finally:
            self.messages.put(None)

    def next(self):
        message = self.messages.get(timeout=30)
        if isinstance(message, Exception):
            raise message
        if message is None:
            raise RuntimeError("runtime disconnected before the expected result")
        self.received.append(message)
        return message

    def call(self, method, params):
        self.request_id += 1
        request_id = str(self.request_id)
        self.process.stdin.write(json.dumps({
            "version": 1, "id": request_id, "method": method, "params": params,
        }) + "\n")
        self.process.stdin.flush()
        while True:
            message = self.next()
            if message.get("type") == "response" and message.get("id") == request_id:
                if "error" in message:
                    raise RuntimeError(message["error"])
                return message["result"]

    def finish(self, task_id):
        for message in self.received:
            if (message.get("task_id") == task_id
                    and message.get("event", {}).get("type") == "task_finished"):
                return message
        while True:
            message = self.next()
            if (message.get("task_id") == task_id
                    and message.get("event", {}).get("type") == "task_finished"):
                return message

    def close(self):
        self.call("runtime.shutdown", {})
        self.process.stdin.close()
        while True:
            message = self.messages.get(timeout=30)
            if message is None:
                break
            if isinstance(message, Exception):
                raise message
            self.received.append(message)
        self.reader.join(timeout=5)
        self.process.stdout.close()
        assert self.process.wait(timeout=30) == 0


def completed(text):
    return {"id": "response", "status": "completed", "output": [{
        "type": "message", "id": "answer", "status": "completed",
        "role": "assistant", "content": [{"type": "output_text", "text": text}],
    }]}


def self_test(binary):
    requests = []
    marker = "saki-stdio-cross-process-60"

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            requests.append(request)
            if len(requests) == 1:
                response = {"id": "read", "status": "completed", "output": [{
                    "type": "function_call", "status": "completed", "id": "f", "call_id": "read-file",
                    "name": "read", "arguments": '{"path":"probe.txt"}',
                }]}
            else:
                response = completed(marker)
            body = json.dumps(response).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix="saki-stdio-") as root:
            Path(root, "probe.txt").write_text(marker)
            env = dict(os.environ, API_KEY="offline-fixture", SAKI_API="openai-responses",
                       SAKI_BASE_URL=f"http://127.0.0.1:{server.server_port}/v1",
                       SAKI_MODEL="fixture", SAKI_CONTEXT_TOKENS="65536",
                       SAKI_SESSION_DIR=str(Path(root, "sessions")), SAKI_SESSION="missing")
            runtime = Runtime(binary, cwd=root, env=env)
            assert not list(Path(root, "sessions").iterdir())
            session = runtime.call("session.create", {})["session_id"]
            task = runtime.call("task.send", {"session_id": session, "message": {"text": "Read probe.txt"}})
            assert runtime.finish(task["task_id"])["event"]["status"] == "completed"
            assert len(requests) == 2
            assert marker in json.dumps(requests[1]["input"])
            events = [m for m in runtime.received if m.get("task_id") == task["task_id"]]
            assert all(m["session_id"] == session and m["message_id"] == task["message_id"] for m in events)
            assert sum(m["event"]["type"] == "task_finished" for m in events) == 1
            assert any(m["event"]["type"] == "tool_finished" and marker in json.dumps(m) for m in events)
            runtime.close()
            # New OS process; restoring and viewing do not replay the old tool/model calls.
            runtime = Runtime(binary, cwd=root, env=env)
            loaded = runtime.call("session.load", {"session_id": session})
            assert loaded["loaded"] and loaded["status"] == "completed"
            history = runtime.call("session.history", {"session_id": session})
            assert marker in json.dumps(history)
            assert len(requests) == 2
            started = next(r["record"] for r in history["records"] if r["record"]["type"] == "task_started")
            assert started["task_id"] == task["task_id"] and started["message_id"] == task["message_id"]
            task = runtime.call("task.send", {"session_id": session, "message": {"text": "Continue new process"}})
            assert runtime.finish(task["task_id"])["event"]["status"] == "completed"
            runtime.close()
            assert len(requests) == 3
            assert "Continue new process" in json.dumps(requests[2]["input"])
            assert marker in json.dumps(requests[2]["input"])
        print("PASS: real read tool, correlated journal, cross-process restore without replay, continuation; 3 model requests")
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="target/debug/Saki", type=Path)
    parser.add_argument("--self-test", action="store_true")
    parser.add_argument("--session", help="load this existing ID instead of creating")
    parser.add_argument("--prompt", default="Use read to inspect probe.txt and report its marker.")
    args = parser.parse_args()
    binary = args.binary.resolve()
    if args.self_test:
        self_test(binary)
        return
    runtime = Runtime(binary)
    try:
        session = runtime.call("session.load", {"session_id": args.session}) if args.session else runtime.call("session.create", {})
        print(json.dumps(session, ensure_ascii=False))
        task = runtime.call("task.send", {"session_id": session["session_id"], "message": {"text": args.prompt}})
        terminal = runtime.finish(task["task_id"])
        for message in runtime.received:
            if message.get("type") == "event":
                print(json.dumps(message, ensure_ascii=False))
        assert terminal["event"]["status"] == "completed", terminal
    finally:
        runtime.close()


if __name__ == "__main__":
    main()
