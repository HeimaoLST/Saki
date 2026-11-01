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
import sys
import tempfile
import threading
import time
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
        self.last_method = None
        self.last_request_id = None
        self.stage = "init"
        self.closed = False
        self.reader = threading.Thread(target=self._read, daemon=True)
        try:
            self.reader.start()
            ready = self.next()
            if ready.get("version") != 1 or ready.get("type") != "ready":
                raise AssertionError("runtime ready frame is invalid")
        except BaseException as error:
            before = self._snapshot()
            cleanup = self._cleanup_failure()
            self.closed = True
            self._report_failure("init", error, cleanup, before)
            raise

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
        self.last_method = method
        self.last_request_id = request_id
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

    def _snapshot(self):
        return (
            self.process.poll(),
            self.process.stdin.closed,
            self.reader.is_alive(),
        )

    def _normal_close(self):
        self.stage = "shutdown_response"
        self.call("runtime.shutdown", {})
        self.stage = "stdin_close"
        self.process.stdin.close()
        self.stage = "eof"
        while True:
            message = self.messages.get(timeout=30)
            if message is None:
                break
            if isinstance(message, Exception):
                raise message
            self.received.append(message)
        self.stage = "reader_join"
        self.reader.join(timeout=5)
        if self.reader.is_alive():
            raise RuntimeError("stdio reader did not stop")
        self.stage = "stdout_close"
        self.process.stdout.close()
        self.stage = "child_wait"
        if self.process.wait(timeout=30) != 0:
            raise RuntimeError("stdio child exited nonzero")

    def _cleanup_failure(self):
        # Failure-only path: never send another protocol shutdown. Kill and
        # reap before touching the stdin wrapper, then stop the reader before
        # closing a buffered stdout object that the reader may be using.
        deadline = time.monotonic() + 10
        result = "ok"
        try:
            if self.process.poll() is None:
                self.process.kill()
            self.process.wait(timeout=max(0.0, min(5.0, deadline - time.monotonic())))
        except subprocess.TimeoutExpired:
            result = "wait_timeout"
            try:
                self.process.kill()
                self.process.wait(timeout=max(0.0, deadline - time.monotonic()))
            except Exception:
                result = "kill_timeout"
        except Exception:
            result = "wait_error"
        try:
            self.process.stdin.close()
        except Exception:
            if result == "ok":
                result = "stdin_error"
        try:
            self.reader.join(timeout=max(0.0, deadline - time.monotonic()))
        except Exception:
            if result == "ok":
                result = "reader_error"
        if self.reader.is_alive():
            result = "reader_alive"
        else:
            try:
                self.process.stdout.close()
            except Exception:
                if result == "ok":
                    result = "stdout_error"
        return result

    @staticmethod
    def _frame_counts(received):
        ready = response = event = other = 0
        for message in received:
            if not isinstance(message, dict):
                other += 1
                continue
            kind = message.get("type")
            if kind == "ready":
                ready += 1
            elif kind == "response":
                response += 1
            elif kind == "event":
                event += 1
            else:
                other += 1
        return ready, response, event, other

    def _report_failure(self, stage, error, cleanup, before):
        # Keep this bounded and payload-free. A diagnostic failure must never
        # replace the original exception.
        try:
            ready, response, event, other = self._frame_counts(self.received)
            failure_poll, failure_stdin, failure_reader = before
            print(
                "runtime cleanup failure"
                f" stage={stage}"
                f" method={str(self.last_method)[:80]!r}"
                f" request_id={self.last_request_id}"
                f" frames={len(self.received)}"
                f" frame_ready={ready}"
                f" frame_response={response}"
                f" frame_event={event}"
                f" frame_other={other}"
                f" pid={self.process.pid}"
                f" failure_poll={failure_poll}"
                f" failure_stdin_closed={failure_stdin}"
                f" failure_reader_alive={failure_reader}"
                f" cleanup_poll={self.process.poll()}"
                f" cleanup_stdin_closed={self.process.stdin.closed}"
                f" cleanup_reader_alive={self.reader.is_alive()}"
                f" cleanup={cleanup}"
                f" error_type={type(error).__name__}",
                file=sys.stderr,
            )
        except Exception:
            pass

    def close(self):
        if self.closed:
            return
        try:
            self._normal_close()
        except BaseException as error:
            before = self._snapshot()
            cleanup = self._cleanup_failure()
            self.closed = True
            self._report_failure(self.stage, error, cleanup, before)
            raise
        self.closed = True

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        if self.closed:
            return False
        if exc_type is not None:
            # A body failure must not wait for a protocol shutdown/EOF cycle.
            before = self._snapshot()
            cleanup = self._cleanup_failure()
            self.closed = True
            self._report_failure("body_exception", exc, cleanup, before)
            return False
        self.close()
        return False


def completed(text):
    return {"id": "response", "status": "completed", "output": [{
        "type": "message", "id": "answer", "status": "completed",
        "role": "assistant", "content": [{"type": "output_text", "text": text}],
    }]}


def self_test(binary):
    requests = []
    marker = "saki-stdio-cross-process-60"
    disconnect_arrived = threading.Event()
    disconnect_release = threading.Event()

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            requests.append(request)
            if len(requests) == 4:
                disconnect_arrived.set()
                assert disconnect_release.wait(timeout=30)
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
            for broken in (dict(env, SAKI_CONTEXT_TOKENS="1"),
                           {key: value for key, value in env.items() if key != "API_KEY"}):
                rejected = subprocess.run([str(binary), "--stdio"], cwd=root, env=broken,
                                          input="", capture_output=True, text=True, timeout=30)
                assert rejected.returncode != 0 and rejected.stdout == "", rejected.stdout
            with Runtime(binary, cwd=root, env=env) as runtime:
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
            # New OS process; restoring and viewing do not replay the old tool/model calls.
            with Runtime(binary, cwd=root, env=env) as runtime:
                loaded = runtime.call("session.load", {"session_id": session})
                assert loaded["loaded"] and loaded["status"] == "completed"
                history = runtime.call("session.history", {"session_id": session})
                assert marker in json.dumps(history)
                assert len(requests) == 2
                started = next(r["record"] for r in history["records"] if r["record"]["type"] == "task_started")
                assert started["task_id"] == task["task_id"] and started["message_id"] == task["message_id"]
                task = runtime.call("task.send", {"session_id": session, "message": {"text": "Continue new process"}})
                assert runtime.finish(task["task_id"])["event"]["status"] == "completed"
            assert len(requests) == 3
            assert "Continue new process" in json.dumps(requests[2]["input"])
            assert marker in json.dumps(requests[2]["input"])
            # Real OS pipes expose Tokio stdin's blocking-read shutdown boundary.
            process = subprocess.Popen(
                [str(binary), "--stdio"], cwd=root, env=env,
                stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                stderr=subprocess.PIPE, text=True,
            )
            try:
                assert json.loads(process.stdout.readline())["type"] == "ready"

                def raw_call(request_id, method, params):
                    process.stdin.write(json.dumps({"version": 1, "id": request_id,
                                                   "method": method, "params": params}) + "\n")
                    process.stdin.flush()
                    while True:
                        message = json.loads(process.stdout.readline())
                        if message.get("id") == request_id:
                            assert "result" in message, message
                            return message["result"]

                raw_call("load", "session.load", {"session_id": session})
                raw_call("send", "task.send", {"session_id": session,
                                               "message": {"text": "Drain after output loss"}})
                while True:
                    message = json.loads(process.stdout.readline())
                    if message.get("event", {}).get("type") == "model_request_started":
                        break
                assert disconnect_arrived.wait(timeout=30)
                process.stdout.close()
                disconnect_release.set()
                # Keep stdin open: the child must drain and exit on its own.
                assert process.wait(timeout=10) != 0
                assert "broken pipe" in process.stderr.read().lower()
            finally:
                disconnect_release.set()
                process.stdin.close()
                if process.poll() is None:
                    process.kill()
                    process.wait(timeout=10)
                process.stdout.close()
                process.stderr.close()
            with Runtime(binary, cwd=root, env=env) as runtime:
                assert runtime.call("session.load", {"session_id": session})["status"] == "completed"
                history = runtime.call("session.history", {"session_id": session})
                assert history["status"] == "completed"
            assert len(requests) == 4
        print("PASS: real read, correlated journal, cross-process restore/continuation, "
              "invalid startup, stdout-loss drain/exit with stdin open; 4 model requests")
    finally:
        disconnect_release.set()
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
    with Runtime(binary) as runtime:
        session = runtime.call("session.load", {"session_id": args.session}) if args.session else runtime.call("session.create", {})
        print(json.dumps(session, ensure_ascii=False))
        task = runtime.call("task.send", {"session_id": session["session_id"], "message": {"text": args.prompt}})
        terminal = runtime.finish(task["task_id"])
        for message in runtime.received:
            if message.get("type") == "event":
                print(json.dumps(message, ensure_ascii=False))
        assert terminal["event"]["status"] == "completed", terminal


if __name__ == "__main__":
    main()
