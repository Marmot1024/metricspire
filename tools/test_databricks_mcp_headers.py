import contextlib
import io
import json
import subprocess
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent))
import databricks_mcp_headers as helper


class HeaderHelperTests(unittest.TestCase):
    @mock.patch.object(helper.subprocess, "run")
    def test_uses_named_cli_profile_without_inheriting_static_token(self, run):
        run.return_value.stdout = '{"access_token":"example-short-lived-token"}'
        with mock.patch.dict(helper.os.environ, {"DATABRICKS_TOKEN": "unrelated-static-token"}):
            result = helper.headers("staging-user")
        self.assertEqual(result, {"Authorization": "Bearer example-short-lived-token"})
        args, options = run.call_args
        self.assertEqual(args[0], ["databricks", "auth", "token", "--profile", "staging-user"])
        self.assertNotIn("DATABRICKS_TOKEN", options["env"])
        self.assertEqual(options["stderr"], subprocess.DEVNULL)

    @mock.patch.object(helper.subprocess, "run")
    def test_rejects_missing_or_multiline_tokens(self, run):
        for raw in ('{}', '{"access_token":""}', '{"access_token":"bad\\nheader"}'):
            run.return_value.stdout = raw
            with self.assertRaises(ValueError):
                helper.headers("staging-user")

    @mock.patch.object(helper.subprocess, "run", side_effect=subprocess.CalledProcessError(1, ["databricks"]))
    def test_failure_exposes_no_token_or_header(self, _run):
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(sys, "argv", ["helper", "--profile", "staging-user"]), contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            self.assertEqual(helper.main(), 1)
        self.assertEqual(out.getvalue(), "")
        self.assertNotIn("Bearer", err.getvalue())

    @mock.patch.object(helper.subprocess, "run")
    def test_stdout_is_only_json_headers(self, run):
        run.return_value.stdout = '{"access_token":"example-short-lived-token"}'
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(sys, "argv", ["helper", "--profile", "staging-user"]), contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            self.assertEqual(helper.main(), 0)
        self.assertEqual(json.loads(out.getvalue()), {"Authorization": "Bearer example-short-lived-token"})
        self.assertEqual(err.getvalue(), "")


if __name__ == "__main__":
    unittest.main()
