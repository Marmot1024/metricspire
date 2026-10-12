"""Exercise the distributed file in disposable homes; no real OAuth grants."""

import json
import os
from pathlib import Path
import platform
import pty
import select
import shutil
import subprocess
import tempfile
import time
import unittest

from tools import build_mcp_installer as builder


FAKE_CODEX = r'''#!/usr/bin/env python3
import json, os, pathlib, sys
root = pathlib.Path(os.environ['HOME'])
args = sys.argv[1:]
with (root / 'calls.jsonl').open('a') as log:
    log.write(json.dumps(args) + '\n')
if args == ['--version']:
    print(os.environ.get('FAKE_VERSION', 'codex-cli 0.162.1'))
elif args == ['mcp', 'list', '--json']:
    status = 'o_auth' if (root / 'logged-in').exists() else 'not_logged_in'
    print(json.dumps([dict(name='metricspire', enabled=True, auth_status=status,
        transport=dict(type='streamable_http', url=os.environ['FAKE_URL']))]))
elif args == ['mcp', 'login', 'metricspire']:
    if os.environ.get('FAKE_DENY'):
        print('OAuth denied', file=sys.stderr); sys.exit(1)
    (root / 'logged-in').write_text('fixture; no token')
else:
    sys.exit(2)
'''


class InstallerTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.connection = dict(url="https://metrics.example.invalid/api/v1/mcp",
                              client_id="public-test-client",
                              callback_url="http://127.0.0.1/callback/test", callback_port=43127)
        (builder.ROOT / "dist").mkdir(exist_ok=True)
        cls.artifact_dir = tempfile.TemporaryDirectory(dir=builder.ROOT / "dist")
        cls.artifact = Path(cls.artifact_dir.name) / "metricspire-install.sh"
        system = {"Darwin": "darwin", "Linux": "linux"}[platform.system()]
        machine = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64"}[platform.machine()]
        builder.build(cls.connection, [system + "-" + machine], cls.artifact)

    @classmethod
    def tearDownClass(cls):
        cls.artifact_dir.cleanup()

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name)
        binaries = self.home / "bin"
        binaries.mkdir()
        fake = binaries / "codex"
        fake.write_text(FAKE_CODEX)
        fake.chmod(0o700)
        self.env = dict(os.environ, HOME=str(self.home), CODEX_HOME=str(self.home / "codex"),
                        PATH=str(binaries) + os.pathsep + os.environ["PATH"],
                        TMPDIR=str(self.home), FAKE_URL=self.connection["url"])
        self.config = self.home / "codex/config.toml"
        self.skill = self.home / ".agents/skills/metricspire/SKILL.md"

    def run_file(self, *args, terminal=False):
        command = ["sh", str(self.artifact), *args]
        if not terminal:
            return subprocess.run(command, env=self.env, capture_output=True, timeout=30)
        master, slave = pty.openpty()
        try:
            proc = subprocess.Popen(command, env=self.env, stdin=slave, stdout=slave, stderr=slave)
            os.close(slave)
            slave = None
            output = bytearray()
            deadline = time.monotonic() + 30
            while proc.poll() is None:
                if time.monotonic() >= deadline:
                    proc.kill(); proc.wait(); self.fail("PTY installer timed out")
                if select.select([master], [], [], 0.1)[0]:
                    try:
                        output.extend(os.read(master, 65536))
                    except OSError:
                        break
            proc.wait(timeout=5)
            return subprocess.CompletedProcess(command, proc.returncode, bytes(output), b"")
        finally:
            os.close(master)
            if slave is not None:
                os.close(slave)

    def calls(self):
        file = self.home / "calls.jsonl"
        return [json.loads(line) for line in file.read_text().splitlines()] if file.exists() else []

    def assert_no_login(self):
        self.assertNotIn(["mcp", "login", "metricspire"], self.calls())

    def test_first_login_and_repeat_preserve_authorization(self):
        result = self.run_file(terminal=True)
        self.assertEqual(result.returncode, 0, result.stdout.decode())
        original = self.config.read_bytes()
        stat = self.config.stat()
        self.assertTrue(self.skill.exists())
        self.assertEqual(stat.st_mode & 0o777, 0o600)
        for args in [(), ("--check",)]:
            result = self.run_file(*args, terminal=True)
            self.assertEqual(result.returncode, 0, result.stdout.decode())
            self.assertEqual(self.config.read_bytes(), original)
            self.assertEqual(self.config.stat().st_mtime_ns, stat.st_mtime_ns)
        self.assertEqual(self.calls().count(["mcp", "login", "metricspire"]), 1)
        self.assertEqual(list(self.home.glob("metricspire-install.*")), [])

    def test_no_login_then_repeat_does_not_reauthorize(self):
        self.assertNotEqual(self.run_file("--no-login").returncode, 0)
        self.assertTrue(self.config.exists())
        self.assertTrue(self.skill.exists())
        self.assertNotEqual(self.run_file(terminal=True).returncode, 0)
        self.assert_no_login()

    def test_null_stdin_never_starts_login(self):
        result = subprocess.run(["sh", str(self.artifact)], env=self.env,
                                stdin=subprocess.DEVNULL, capture_output=True, timeout=30)
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(self.config.exists())
        self.assert_no_login()

    def test_denied_login_stops_and_repeat_does_not_retry(self):
        self.env["FAKE_DENY"] = "1"
        self.assertNotEqual(self.run_file(terminal=True).returncode, 0)
        self.assertNotEqual(self.run_file(terminal=True).returncode, 0)
        self.assertEqual(self.calls().count(["mcp", "login", "metricspire"]), 1)

    def test_check_missing_and_old_codex_are_read_only(self):
        self.assertNotEqual(self.run_file("--check").returncode, 0)
        self.assertFalse(self.config.exists())
        self.assertFalse(self.skill.exists())
        self.env["FAKE_VERSION"] = "codex-cli 0.161.9"
        self.assertNotEqual(self.run_file().returncode, 0)
        self.assertFalse(self.config.exists())
        self.assert_no_login()

    def test_conflicting_config_and_skill_are_preserved(self):
        self.config.parent.mkdir()
        original = b"# personal setting\n[mcp_servers.metricspire]\nurl = 'https://other.example.invalid/mcp'\n"
        self.config.write_bytes(original)
        self.assertNotEqual(self.run_file().returncode, 0)
        self.assertEqual(self.config.read_bytes(), original)
        self.assertFalse(self.skill.exists())
        self.config.unlink()
        self.skill.parent.mkdir(parents=True)
        self.skill.write_text("my custom skill")
        self.assertNotEqual(self.run_file().returncode, 0)
        self.assertFalse(self.config.exists())
        self.assertEqual(self.skill.read_text(), "my custom skill")
        self.assert_no_login()

    def test_corrupted_file_stops_before_codex(self):
        damaged = self.home / "damaged.sh"
        content = bytearray(self.artifact.read_bytes())
        content[-10] ^= 1
        damaged.write_bytes(content)
        result = subprocess.run(["sh", str(damaged)], env=self.env, capture_output=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.calls(), [])
        self.assertFalse(self.config.exists())

    def test_actual_codex_parses_generated_config_without_login(self):
        codex = shutil.which("codex")
        if not codex:
            self.skipTest("Codex CLI not installed")
        self.run_file("--no-login")
        result = subprocess.run([codex, "mcp", "get", "metricspire", "--json"],
                                env=dict(self.env, PATH=os.environ["PATH"]), cwd=self.home,
                                capture_output=True, timeout=20)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(json.loads(result.stdout)["transport"]["url"], self.connection["url"])
        self.assert_no_login()


class BuilderTest(unittest.TestCase):
    def test_invalid_mcp_ports_rejected_before_build(self):
        c = dict(url="https://metrics.example.invalid/mcp", client_id="public",
                 callback_url="http://127.0.0.1/callback/test", callback_port=43127)
        for port in ("not-a-port", "65536"):
            with self.subTest(port=port), self.assertRaises(ValueError):
                builder.validate_connection(dict(c, url=f"https://metrics.example.invalid:{port}/mcp"))
        builder.validate_connection(dict(c, url="https://metrics.example.invalid:443/mcp"))

    def test_secrets_and_remote_callbacks_rejected(self):
        c = dict(url="https://metrics.example.invalid/mcp", client_id="public",
                 callback_url="http://127.0.0.1/callback/test", callback_port=43127)
        for modified in [dict(c, client_secret="do-not-package"),
                         dict(c, url="https://user:secret@metrics.example.invalid/mcp"),
                         dict(c, callback_url="https://remote.example.invalid/callback")]:
            with self.assertRaises(ValueError):
                builder.validate_connection(modified)


if __name__ == "__main__":
    unittest.main()
