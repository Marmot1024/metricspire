"""Opt-in real Codex transport test; loopback MCP and synthetic headers only.

METRICSPIRE_RUN_CODEX_HELPER_TEST=1 python3 -m unittest tools/test_codex_helper_refresh.py
"""
import http.server
import json
import os
from pathlib import Path
import queue
import shutil
import subprocess
import tempfile
import threading
import time
import unittest


@unittest.skipUnless(os.environ.get('METRICSPIRE_RUN_CODEX_HELPER_TEST') == '1' and shutil.which('codex'),
                     'requires opt-in and real local Codex CLI')
class NativeRefreshTest(unittest.TestCase):
    def exercise(self, code):
        with tempfile.TemporaryDirectory(prefix='metricspire-refresh-') as temp:
            root = Path(temp)
            count = root / 'helper-count'
            generation = root / 'generation'
            generation.write_text('1')
            helper = root / 'helper.py'
            helper.write_text('import pathlib,json\np=pathlib.Path(' + repr(str(count)) + ')\n'
                              'n=int(p.read_text())+1 if p.exists() else 1\np.write_text(str(n))\n'
                              'g=pathlib.Path(' + repr(str(generation)) + ').read_text()\n'
                              'print(json.dumps({"Authorization":"Bearer fixture-"+g}))\n')
            state = dict(rotated=False, rejected=0)

            class Handler(http.server.BaseHTTPRequestHandler):
                def log_message(self, *args):
                    pass

                def do_GET(self):
                    self.send_response(405); self.end_headers()

                def do_POST(self):
                    raw = self.rfile.read(int(self.headers.get('Content-Length', '0')))
                    expected = 'Bearer fixture-2' if state['rotated'] else 'Bearer fixture-1'
                    if self.headers.get('Authorization') != expected:
                        state['rejected'] += 1
                        self.send_response(code); self.end_headers(); return
                    request = json.loads(raw)
                    method = request['method']
                    if 'id' not in request:
                        self.send_response(202); self.end_headers(); return
                    if method == 'initialize':
                        result = dict(protocolVersion=request['params']['protocolVersion'],
                                      capabilities=dict(tools={}), serverInfo=dict(name='metricspire', version='fixture'))
                    elif method == 'tools/list':
                        result = dict(tools=[dict(name='list_namespaces', description='fixture',
                                                 inputSchema=dict(type='object', properties={}))])
                    elif method == 'tools/call':
                        result = dict(content=[dict(type='text', text=json.dumps(dict(namespaces=['acceptance'])))])
                    else:
                        result = {}
                    body = json.dumps(dict(jsonrpc='2.0', id=request['id'], result=result)).encode()
                    self.send_response(200); self.send_header('Content-Type', 'application/json')
                    self.send_header('Content-Length', str(len(body))); self.end_headers(); self.wfile.write(body)

            server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
            threading.Thread(target=server.serve_forever, daemon=True).start()
            command = 'python3 ' + str(helper)
            (root / 'config.toml').write_text('[mcp_servers.metricspire-cli]\nurl = ' +
                json.dumps(f'http://127.0.0.1:{server.server_port}/mcp') + '\nhttp_headers_helper = ' + json.dumps(command) + '\n')
            env = dict(os.environ, CODEX_HOME=str(root))
            proc = subprocess.Popen(['codex', 'app-server', '--listen', 'stdio://'], cwd=root, env=env,
                                    stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                    text=True, bufsize=1)
            replies = queue.Queue()
            def reader():
                for line in proc.stdout:
                    try: replies.put(json.loads(line))
                    except ValueError: pass
            thread = threading.Thread(target=reader, daemon=True); thread.start()
            seq = 0
            def rpc(method, params):
                nonlocal seq
                seq += 1
                proc.stdin.write(json.dumps(dict(id=seq, method=method, params=params)) + '\n'); proc.stdin.flush()
                deadline = time.monotonic() + 30
                while time.monotonic() < deadline:
                    reply = replies.get(timeout=max(.1, deadline-time.monotonic()))
                    if reply.get('id') == seq:
                        self.assertNotIn('error', reply, 'native RPC failed at ' + method)
                        return reply['result']
                self.fail('native RPC timeout')
            try:
                rpc('initialize', dict(clientInfo=dict(name='metricspire-refresh-test', version='1'), capabilities=dict(experimentalApi=True)))
                status = rpc('mcpServerStatus/list', dict(serverName='metricspire-cli', detail='toolsAndAuthOnly', limit=10))
                self.assertIn('list_namespaces', status['data'][0]['tools'])
                tid = rpc('thread/start', dict(cwd=str(root), ephemeral=True, sandbox='read-only'))['thread']['id']
                rpc('mcpServer/tool/call', dict(threadId=tid, server='metricspire-cli', tool='list_namespaces', arguments={}))
                before_count = int(count.read_text())
                generation.write_text('2')
                state['rotated'] = True
                result = rpc('mcpServer/tool/call', dict(threadId=tid, server='metricspire-cli', tool='list_namespaces', arguments={}))
                self.assertFalse(result.get('isError'))
                self.assertEqual(json.loads(result['content'][0]['text']), dict(namespaces=['acceptance']))
                self.assertEqual(state['rejected'], 1)
                self.assertEqual(int(count.read_text()), before_count + 1)
            finally:
                proc.terminate()
                try: proc.wait(timeout=5)
                except subprocess.TimeoutExpired: proc.kill(); proc.wait()
                proc.stdin.close(); thread.join(timeout=2); proc.stdout.close()
                server.shutdown(); server.server_close()

    def test_same_connection_refreshes_after_401(self):
        self.exercise(401)

    def test_same_connection_refreshes_after_403(self):
        self.exercise(403)


if __name__ == '__main__':
    unittest.main()
