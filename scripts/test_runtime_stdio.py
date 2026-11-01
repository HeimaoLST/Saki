#!/usr/bin/env python3
"""Deterministic lifecycle tests for examples/runtime_stdio.py.

The child is a real Python process with a tiny stdio protocol implementation, not
the Rust runtime. These tests exercise only client-side process/pipe cleanup.
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import queue
import subprocess
import sys
import tempfile
import textwrap
import threading
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parent.parent
EXAMPLE = ROOT / "examples" / "runtime_stdio.py"


def load_runtime_module():
    spec = importlib.util.spec_from_file_location("saki_runtime_stdio_test", EXAMPLE)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class FailAfterQueue:
    """A queue that makes a controlled EOF wait fail without a 30s wait."""

    def __init__(self, inner: queue.Queue, allowed: int):
        self.inner = inner
        self.allowed = allowed
        self.lock = threading.Lock()

    def put(self, item):
        self.inner.put(item)

    def get(self, timeout=None):
        with self.lock:
            if self.allowed <= 0:
                raise queue.Empty
            self.allowed -= 1
        return self.inner.get(timeout=timeout)


class RuntimeLifecycleTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.module = load_runtime_module()

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="saki-runtime-stdio-test-")
        self.root = Path(self.temp.name)
        self.child = self.root / "scripted_child.py"
        self.child.write_text(textwrap.dedent(
            """\
            #!/usr/bin/env python3
            import json
            import os
            import signal
            import sys

            mode = os.environ.get("RUNTIME_STDIO_TEST_MODE", "normal")

            def send(value):
                sys.stdout.write(json.dumps(value) + "\\n")
                sys.stdout.flush()

            if mode == "bad-ready":
                send({"version": 1, "type": "not-ready"})
                signal.pause()

            send({"version": 1, "type": "ready", "api": "openai-responses"})
            shutdown_seen = False
            for line in sys.stdin:
                request = json.loads(line)
                if request.get("method") == "runtime.shutdown" and not shutdown_seen:
                    send({"version": 1, "type": "response",
                          "id": request.get("id"), "result": {"state": "draining"}})
                    shutdown_seen = True
            send({"version": 1, "type": "event", "task_id": "test-task",
                  "event": {"type": "task_finished"}})
            if mode == "held":
                signal.pause()
            if mode == "nonzero":
                sys.exit(1)
            """
        ))
        self.child.chmod(0o755)
        self.wrapper = self.root / "wrapper.py"
        self.wrapper.write_text(textwrap.dedent(
            """\
            import importlib.util
            import json
            import os
            from pathlib import Path
            import queue
            import sys
            import threading

            spec = importlib.util.spec_from_file_location(
                "saki_runtime_stdio_wrapper", os.environ["RUNTIME_EXAMPLE"])
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)

            class FailAfterQueue:
                def __init__(self, inner, allowed):
                    self.inner = inner
                    self.allowed = allowed
                    self.lock = threading.Lock()
                def put(self, item):
                    self.inner.put(item)
                def get(self, timeout=None):
                    with self.lock:
                        if self.allowed <= 0:
                            raise queue.Empty
                        self.allowed -= 1
                    return self.inner.get(timeout=timeout)

            action = os.environ["RUNTIME_WRAPPER_ACTION"]
            child = Path(os.environ["RUNTIME_CHILD"])
            env = dict(os.environ)
            try:
                runtime = module.Runtime(child, cwd=Path(os.environ["RUNTIME_ROOT"]), env=env)
                if action == "close":
                    runtime.messages = FailAfterQueue(runtime.messages, 1)
                    runtime.close()
                elif action != "init":
                    raise RuntimeError("unknown wrapper action")
            except BaseException as error:
                print("%s_failure type=%s" % (action, type(error).__name__),
                      file=sys.stderr)
                sys.exit(1)
            print("unexpected cleanup success", file=sys.stderr)
            sys.exit(2)
            """
        ))

    def tearDown(self):
        self.temp.cleanup()

    def runtime(self, mode: str):
        env = dict(os.environ, RUNTIME_STDIO_TEST_MODE=mode)
        return self.module.Runtime(self.child, cwd=self.root, env=env)

    def cleanup_runtime(self, runtime):
        """Test-owned last-resort cleanup; never required for a passing close."""
        process = runtime.process
        try:
            if process.stdin is not None and not process.stdin.closed:
                process.stdin.close()
        except Exception:
            pass
        try:
            if process.poll() is None:
                process.kill()
            process.wait(timeout=5)
        except Exception:
            pass
        try:
            process.stdout.close()
        except Exception:
            pass
        try:
            if runtime.reader.is_alive():
                runtime.reader.join(timeout=5)
        except Exception:
            pass

    def run_wrapper(self, action: str, mode: str) -> tuple[int, bytes, bytes]:
        env = dict(
            os.environ,
            RUNTIME_EXAMPLE=str(EXAMPLE),
            RUNTIME_CHILD=str(self.child),
            RUNTIME_ROOT=str(self.root),
            RUNTIME_STDIO_TEST_MODE=mode,
            RUNTIME_WRAPPER_ACTION=action,
        )
        process = subprocess.Popen(
            [sys.executable, str(self.wrapper)],
            env=env,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
        timed_out = False
        try:
            try:
                stdout, stderr = process.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                timed_out = True
                stdout, stderr = b"", b""
        finally:
            if timed_out or process.poll() is None:
                try:
                    os.killpg(process.pid, 9)
                except ProcessLookupError:
                    pass
            if timed_out:
                try:
                    process.communicate(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
        if timed_out:
            self.fail("cleanup left the wrapper's inherited stderr pipe open")
        return process.returncode, stdout, stderr

    def test_normal_close_keeps_response_eof_and_exit_zero(self):
        runtime = self.runtime("normal")
        try:
            runtime.close()
            self.assertEqual(runtime.process.wait(timeout=1), 0)
            self.assertFalse(runtime.reader.is_alive())
            self.assertTrue(runtime.process.stdin.closed)
            self.assertTrue(runtime.process.stdout.closed)
            self.assertTrue(any(
                message.get("event", {}).get("type") == "task_finished"
                for message in runtime.received
            ))
        finally:
            self.cleanup_runtime(runtime)

    def test_close_eof_failure_reaps_child_reader_and_keeps_original_error(self):
        runtime = self.runtime("held")
        try:
            runtime.messages = FailAfterQueue(runtime.messages, 1)
            captured = io.StringIO()
            with contextlib.redirect_stderr(captured):
                with self.assertRaises(queue.Empty):
                    runtime.close()
            self.assertIsNotNone(runtime.process.poll())
            self.assertFalse(runtime.reader.is_alive())
            self.assertTrue(runtime.process.stdin.closed)
            self.assertTrue(runtime.process.stdout.closed)
            diagnostics = captured.getvalue()
            self.assertIn("stage=eof", diagnostics)
            self.assertIn("method='runtime.shutdown'", diagnostics)
            self.assertIn("request_id=1", diagnostics)
            self.assertIn("frames=", diagnostics)
            self.assertIn("frame_ready=1", diagnostics)
            self.assertIn("frame_response=1", diagnostics)
            self.assertIn("failure_poll=None", diagnostics)
            self.assertIn("failure_reader_alive=True", diagnostics)
            self.assertIn("cleanup_poll=", diagnostics)
            self.assertIn("cleanup_reader_alive=False", diagnostics)
            self.assertIn("cleanup=", diagnostics)
        finally:
            self.cleanup_runtime(runtime)

    def test_close_shutdown_response_failure_has_distinct_stage_and_cleanup(self):
        runtime = self.runtime("held")
        try:
            runtime.messages = FailAfterQueue(runtime.messages, 0)
            captured = io.StringIO()
            with contextlib.redirect_stderr(captured):
                with self.assertRaises(queue.Empty):
                    runtime.close()
            self.assertIsNotNone(runtime.process.poll())
            self.assertFalse(runtime.reader.is_alive())
            self.assertIn("stage=shutdown_response", captured.getvalue())
        finally:
            self.cleanup_runtime(runtime)

    def test_body_assertion_preserves_first_error_and_skips_protocol_shutdown(self):
        runtime = self.runtime("held")
        captured = io.StringIO()
        original = AssertionError("body failure")
        try:
            with contextlib.redirect_stderr(captured), mock.patch.object(
                runtime.process.stdout, "close", side_effect=OSError("cleanup failure")
            ):
                with self.assertRaises(AssertionError) as failure:
                    with runtime:
                        raise original
            self.assertIs(failure.exception, original)
            self.assertIsNotNone(runtime.process.poll())
            self.assertFalse(runtime.reader.is_alive())
            self.assertFalse(any(
                message.get("type") == "response" for message in runtime.received
            ))
            self.assertIn("stage=body_exception", captured.getvalue())
            self.assertIn("cleanup=stdout_error", captured.getvalue())
            self.assertIn("failure_reader_alive=True", captured.getvalue())
        finally:
            self.cleanup_runtime(runtime)

    def test_real_child_nonzero_after_eof_fails_at_child_wait(self):
        runtime = self.runtime("nonzero")
        captured = io.StringIO()
        try:
            with contextlib.redirect_stderr(captured):
                with self.assertRaises(RuntimeError):
                    runtime.close()
            diagnostics = captured.getvalue()
            self.assertIn("stage=child_wait", diagnostics)
            self.assertEqual(runtime.process.poll(), 1)
            self.assertFalse(runtime.reader.is_alive())
        finally:
            self.cleanup_runtime(runtime)

    def test_ready_parse_failure_reaps_child_and_releases_inherited_stderr(self):
        returncode, _stdout, stderr = self.run_wrapper("init", "bad-ready")
        self.assertEqual(returncode, 1)
        self.assertIn(b"init_failure type=AssertionError", stderr)
        self.assertNotIn(b"not-ready", stderr)

    def test_close_failure_releases_inherited_stderr_without_manual_release(self):
        returncode, _stdout, stderr = self.run_wrapper("close", "held")
        self.assertEqual(returncode, 1)
        self.assertIn(b"close_failure", stderr)


if __name__ == "__main__":
    unittest.main()
