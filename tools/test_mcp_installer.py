"""Exercise the distributed CLI-auth installer in disposable homes."""

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
import json, os, pathlib, sys, re
root = pathlib.Path(os.environ['HOME'])
args = sys.argv[1:]
with (root / 'calls.jsonl').open('a') as log:
    log.write(json.dumps(args) + '\n')
if args == ['--version']:
    print(os.environ.get('FAKE_VERSION', 'codex-cli 0.162.1'))
elif args == ['mcp', 'list', '--json']:
    status = 'unknown'
    print(json.dumps([dict(name='metricspire-cli', enabled=True, auth_status=status,
        transport=dict(type='streamable_http', url=os.environ['FAKE_URL']))]))
elif args == ['app-server', '--listen', 'stdio://']:
    tools = ['list_namespaces','search_metrics','explain_query','plan_query','submit_query','get_query','cancel_query']
    if os.environ.get('FAKE_MISSING_TOOL'): tools.remove('get_query')
    for line in sys.stdin:
        request = json.loads(line)
        if 'id' not in request: continue
        method = request['method']
        if method == 'initialize': result = {}
        elif method == 'config/read':
            raw = (pathlib.Path(os.environ['CODEX_HOME']) / 'config.toml').read_text()
            helper = json.loads(re.search(r'^http_headers_helper = (.*)$', raw, re.M).group(1))
            server = dict(url=os.environ['FAKE_URL'],http_headers_helper=helper)
            if os.environ.get('FAKE_HELPER_OVERRIDE'): server['http_headers_helper'] = 'different-helper'
            if os.environ.get('FAKE_AUTH_OVERRIDE'): server['bearer_token_env_var'] = 'TOKEN'
            result = dict(config=dict(mcp_servers={'metricspire-cli':server}))
        elif method == 'mcpServerStatus/list':
            result = dict(data=[dict(name='metricspire-cli', authStatus='unknown',
                toolsError='HTTP 401 secret-marker' if os.environ.get('FAKE_HANDSHAKE_FAIL') else None,
                serverInfo=dict(name='metricspire',version='fixture'),tools={x:{} for x in tools})])
        elif method == 'thread/start': result = dict(thread=dict(id='ephemeral-test'))
        elif method == 'mcpServer/tool/call':
            assert request['params']['tool'] == 'list_namespaces'
            with (root / 'verified-tools').open('a') as calls: calls.write('list_namespaces\n')
            result = dict(isError=bool(os.environ.get('FAKE_TOOL_FAIL')),
                          structuredContent=dict(namespaces=[] if os.environ.get('FAKE_EMPTY') else ['acceptance']))
        else: sys.exit(3)
        print(json.dumps(dict(id=request['id'],result=result)), flush=True)
else:
    sys.exit(2)
'''


FAKE_DATABRICKS = r'''#!/usr/bin/env python3
import json, os, sys
if os.environ.get('FAKE_DB_FAIL'):
    print('secret-marker', file=sys.stderr); sys.exit(1)
assert sys.argv[1:2] == ['auth']
assert sys.argv[3:5] == ['--profile', 'team']
if sys.argv[2] == 'describe':
    print(json.dumps({'details': {'host':'https://workspace.example.invalid','auth_type':'databricks-cli'}}))
elif sys.argv[2] == 'token':
    print(json.dumps({'access_token':'fixture-not-a-real-token'}))
else: sys.exit(2)
'''


class InstallerTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.connection = dict(url="https://metrics.example.invalid/api/v1/mcp",
                              workspace_host="https://workspace.example.invalid")
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
        db = binaries / "databricks"
        db.write_text(FAKE_DATABRICKS)
        db.chmod(0o700)
        self.env = dict(os.environ, HOME=str(self.home), CODEX_HOME=str(self.home / "codex"),
                        PATH=str(binaries) + os.pathsep + os.environ["PATH"],
                        TMPDIR=str(self.home), FAKE_URL=self.connection["url"])
        self.config = self.home / "codex/config.toml"
        self.skill = self.home / ".agents/skills/metricspire/SKILL.md"

    def run_file(self, *args, terminal=False):
        command = ["sh", str(self.artifact), "--profile", "team", *args]
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

    def test_install_and_repeat_verify_actual_connection(self):
        result = self.run_file()
        self.assertEqual(result.returncode, 0, (result.stdout + result.stderr).decode())
        original = self.config.read_bytes()
        stat = self.config.stat()
        self.assertTrue(self.skill.exists())
        self.assertEqual(stat.st_mode & 0o777, 0o600)
        for args in [(), ("--check",)]:
            result = self.run_file(*args)
            self.assertEqual(result.returncode, 0, (result.stdout + result.stderr).decode())
            self.assertEqual(self.config.read_bytes(), original)
            self.assertEqual(self.config.stat().st_mtime_ns, stat.st_mtime_ns)
        self.assert_no_login()
        self.assertEqual((self.home / 'verified-tools').read_text().splitlines(), ['list_namespaces'] * 3)
        helpers = list((self.home / 'codex/metricspire/bin').iterdir())
        self.assertEqual(len(helpers), 1)
        result = subprocess.run([str(helpers[0]), '--headers', '--profile', 'team',
                                 '--workspace-host', self.connection['workspace_host']],
                                 env=self.env, capture_output=True, timeout=10)
        self.assertEqual(json.loads(result.stdout), {'Authorization': 'Bearer fixture-not-a-real-token'})
        self.assertEqual(list(self.home.glob("metricspire-install.*")), [])

    def test_cli_failure_changes_nothing_and_never_logs_in(self):
        self.env['FAKE_DB_FAIL'] = '1'
        result = self.run_file()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.config.exists())
        self.assertNotIn(b'secret-marker', result.stdout + result.stderr)
        self.assert_no_login()

    def test_saved_cli_authorization_is_not_connection_success(self):
        self.assertEqual(self.run_file().returncode, 0)
        original = self.config.read_bytes()
        for flag in ('FAKE_HANDSHAKE_FAIL','FAKE_MISSING_TOOL','FAKE_TOOL_FAIL','FAKE_EMPTY','FAKE_HELPER_OVERRIDE','FAKE_AUTH_OVERRIDE'):
            with self.subTest(flag=flag):
                self.env[flag] = '1'
                result = self.run_file('--check')
                del self.env[flag]
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn(b'secret-marker', result.stdout + result.stderr)
                self.assertNotIn('实际调用验证通过'.encode(), result.stdout)
                self.assertEqual(self.config.read_bytes(), original)
        self.assert_no_login()

    def test_explicit_migration_backs_up_and_disables_old_oauth(self):
        self.config.parent.mkdir()
        original = ("# personal\n[mcp_servers.metricspire]\nurl='" + self.connection['url'] +
                    "'\n[mcp_servers.metricspire.oauth]\nclient_id='public-test-client'\n").encode()
        self.config.write_bytes(original)
        self.assertNotEqual(self.run_file().returncode, 0)
        self.assertEqual(self.config.read_bytes(), original)
        result = self.run_file('--migrate-oauth')
        self.assertEqual(result.returncode, 0, (result.stdout + result.stderr).decode())
        self.assertIn(b'enabled = false', self.config.read_bytes())
        self.assertIn(b'client_id=', self.config.read_bytes())
        backups = list(self.config.parent.glob('config.toml.metricspire-backup-*'))
        self.assertEqual(len(backups), 1)
        self.assertEqual(backups[0].read_bytes(), original)
        self.assert_no_login()

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
        self.run_file()
        result = subprocess.run([codex, "mcp", "get", "metricspire-cli", "--json"],
                                env=dict(self.env, PATH=os.environ["PATH"]), cwd=self.home,
                                capture_output=True, timeout=20)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(json.loads(result.stdout)["transport"]["url"], self.connection["url"])
        self.assert_no_login()


class BuilderTest(unittest.TestCase):
    def test_only_public_https_origins_and_mcp_paths(self):
        c = dict(url="https://metrics.example.invalid/api/v1/mcp", workspace_host="https://workspace.example.invalid")
        builder.validate_connection(c)
        for changed in [dict(c, client_secret="no"), dict(c, url="https://user:pass@metrics.example.invalid/api/v1/mcp"),
                        dict(c, url="https://metrics.example.invalid:65536/api/v1/mcp"),
                        dict(c, workspace_host="https://workspace.example.invalid/path"),
                        dict(c, workspace_host="http://workspace.example.invalid")]:
            with self.assertRaises(ValueError): builder.validate_connection(changed)


if __name__ == '__main__':
    unittest.main()
