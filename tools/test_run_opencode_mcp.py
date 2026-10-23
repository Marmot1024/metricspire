import argparse
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from tools import run_opencode_mcp as runner


class OpenCodeRunTest(unittest.TestCase):
    def args(self, directory):
        return argparse.Namespace(directory=directory, opencode="opencode", server="http://127.0.0.1:12345",
                                  mcp_name="metricspire", ready_timeout=1, timeout=5,
                                  model="fixture/model", session=None, prompt="unchanged question", output=None)

    def test_existing_or_unwritable_receipt_never_invokes_client(self):
        with tempfile.TemporaryDirectory() as directory:
            args = self.args(directory)
            path = Path(directory) / "existing.json"
            path.write_text("previous failure")
            with patch.object(runner.subprocess, "run") as execute:
                for target in (path, Path(directory) / "missing" / "receipt.json"):
                    args.output = str(target)
                    with self.assertRaises(OSError):
                        runner.run(args)
                execute.assert_not_called()
            self.assertEqual(path.read_text(), "previous failure")

    def test_malformed_readiness_retains_failure_before_model(self):
        with tempfile.TemporaryDirectory() as directory:
            args = self.args(directory)
            args.output = str(Path(directory) / "receipt.json")
            malformed = subprocess.CompletedProcess([], 0, '{"data": [{"name":"metricspire","status":null}]}', "")
            with patch.object(runner.subprocess, "run", return_value=malformed) as execute, patch.object(runner.time, "monotonic", side_effect=[0, 0, 2]):
                with self.assertRaisesRegex(RuntimeError, "before model invocation"):
                    runner.run(args)
                self.assertEqual(execute.call_count, 1)
            receipt = json.loads(Path(args.output).read_text())
            self.assertEqual(receipt["outcome"], "readiness_failed")
            self.assertEqual(receipt["prompt"], args.prompt)

    def test_waits_for_connection_and_preserves_question_without_agent_context(self):
        with tempfile.TemporaryDirectory() as directory:
            calls = []

            def execute(command, **kwargs):
                calls.append((command, kwargs))
                if "mcp.list" in command:
                    data = [] if len(calls) == 1 else [dict(name="metricspire", status=dict(status="connected"))]
                    return subprocess.CompletedProcess(command, 0, json.dumps(dict(data=data)), "")
                event = dict(type="tool_use", sessionID="session_fixture", part=dict(tool="metricspire_list_namespaces", state=dict(status="completed")))
                return subprocess.CompletedProcess(command, 0, json.dumps(event), "")

            with patch.object(runner.subprocess, "run", side_effect=execute), patch.object(runner.time, "sleep"), contextlib.redirect_stderr(io.StringIO()):
                runner.run(self.args(directory))
            self.assertEqual(len(calls), 3)
            command, kwargs = calls[-1]
            self.assertEqual(command[-1], "unchanged question")
            self.assertNotIn("--agent", command)
            self.assertEqual(kwargs["env"]["PWD"], str(Path(directory).resolve()))

    def test_exit_zero_with_pseudo_tool_text_fails_and_retains_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            args = self.args(directory)
            args.output = str(Path(directory) / "receipt.json")
            ready = json.dumps(dict(data=[dict(name="metricspire", status=dict(status="connected"))]))
            replies = [subprocess.CompletedProcess([], 0, ready, ""),
                       subprocess.CompletedProcess([], 0, json.dumps(dict(type="text", part=dict(text="<tool>pretend</tool>"))), "secret diagnostic")]
            with patch.object(runner.subprocess, "run", side_effect=replies), contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaisesRegex(RuntimeError, "completed MCP interaction"):
                    runner.run(args)
            receipt = Path(args.output).read_text()
            self.assertNotIn("secret diagnostic", receipt)
            self.assertEqual(json.loads(receipt)["completed_mcp_calls"], 0)
            self.assertEqual(os.stat(args.output).st_mode & 0o777, 0o600)

    def test_readiness_failure_never_calls_model(self):
        with tempfile.TemporaryDirectory() as directory:
            def execute(command, **kwargs):
                self.assertIn("mcp.list", command)
                return subprocess.CompletedProcess(command, 1, "", "secret diagnostic")
            with patch.object(runner.subprocess, "run", side_effect=execute), patch.object(runner.time, "monotonic", side_effect=[0, 0, 2]):
                with self.assertRaisesRegex(RuntimeError, "before model invocation"):
                    runner.run(self.args(directory))

    def test_timeout_retains_partial_session_for_uncertain_submission(self):
        with tempfile.TemporaryDirectory() as directory:
            args = self.args(directory)
            args.output = str(Path(directory) / "timeout.json")
            ready = json.dumps(dict(data=[dict(name="metricspire", status=dict(status="connected"))]))
            partial = json.dumps(dict(type="text", sessionID="uncertain_session", part=dict(text="pending"))).encode()
            replies = [subprocess.CompletedProcess([], 0, ready, ""), subprocess.TimeoutExpired([], 5, output=partial)]
            with patch.object(runner.subprocess, "run", side_effect=replies), contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaisesRegex(RuntimeError, "Inspect the existing session/jobs"):
                    runner.run(args)
            receipt = json.loads(Path(args.output).read_text())
            self.assertEqual(receipt["session"], "uncertain_session")
            self.assertTrue(receipt["timed_out"])


if __name__ == "__main__":
    unittest.main()
