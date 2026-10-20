"""Opt-in native Codex OAuth acceptance; isolated homes and synthetic identities.

METRICSPIRE_RUN_OIDC_CLIENT_TEST=1 python3 -m unittest tools/test_oidc_mcp.py
No existing Codex/Databricks profiles or credentials are read or changed.
"""
import base64
import hashlib
import http.server
import json
import os
from pathlib import Path
import queue
import re
import shutil
import subprocess
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.parse
import urllib.request


@unittest.skipUnless(os.environ.get('METRICSPIRE_RUN_OIDC_CLIENT_TEST') == '1' and shutil.which('codex'),
                     'requires opt-in and native local Codex CLI')
class NativeOAuthTest(unittest.TestCase):
    def test_login_refresh_rotation_revocation_and_identity_isolation(self):
        with tempfile.TemporaryDirectory(prefix='metricspire-oidc-test-') as temp:
            root = Path(temp)
            binary = root / 'metricspire-oidc'
            subprocess.run(['go', 'build', '-o', str(binary), './tools/mcp-oidc'], check=True,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            state = dict(identity='reader', codes={}, refresh={}, access={}, refreshes=0, revoked=False, serial=0)
            class Handler(http.server.BaseHTTPRequestHandler):
                def log_message(self, *args): pass
                def reply(self, code, value):
                    body = json.dumps(value).encode()
                    self.send_response(code); self.send_header('Content-Type', 'application/json')
                    self.send_header('Content-Length', str(len(body))); self.end_headers(); self.wfile.write(body)
                def do_GET(self):
                    u = urllib.parse.urlsplit(self.path)
                    if u.path.startswith('/.well-known/oauth-protected-resource'):
                        return self.reply(200, dict(resource=origin+'/mcp', authorization_servers=[origin]))
                    if u.path.startswith('/.well-known/'):
                        return self.reply(200, dict(issuer=origin, authorization_endpoint=origin+'/authorize',
                            token_endpoint=origin+'/token', response_types_supported=['code'],
                            grant_types_supported=['authorization_code', 'refresh_token'],
                            token_endpoint_auth_methods_supported=['none'], code_challenge_methods_supported=['S256'],
                            authorization_response_iss_parameter_supported=True,
                            scopes_supported=['metrics', 'offline_access']))
                    if u.path == '/authorize':
                        p = urllib.parse.parse_qs(u.query)
                        if p.get('code_challenge_method') != ['S256'] or p.get('client_id') != ['fixture-public']:
                            return self.reply(400, dict(error='invalid_request'))
                        code = 'code-'+str(len(state['codes']))
                        state['codes'][code] = (p['code_challenge'][0], state['identity'], p['redirect_uri'][0])
                        callback = p['redirect_uri'][0] + '?' + urllib.parse.urlencode(dict(code=code, state=p['state'][0], iss=origin))
                        self.send_response(302); self.send_header('Location', callback); self.end_headers(); return
                    return self.reply(405, {})
                def do_POST(self):
                    raw = self.rfile.read(int(self.headers.get('Content-Length', '0')))
                    if self.path == '/token':
                        p = urllib.parse.parse_qs(raw.decode())
                        if p.get('grant_type') == ['authorization_code']:
                            item = state['codes'].pop(p.get('code', [''])[0], None)
                            verifier = p.get('code_verifier', [''])[0]
                            challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b'=').decode()
                            if not item or item[0] != challenge or item[2] != p.get('redirect_uri', [''])[0]:
                                return self.reply(400, dict(error='invalid_grant'))
                            identity = item[1]
                        else:
                            identity = state['refresh'].pop(p.get('refresh_token', [''])[0], None)
                            if state['revoked'] or not identity:
                                return self.reply(400, dict(error='invalid_grant'))
                            state['refreshes'] += 1
                        state['serial'] += 1
                        access, refresh = 'synthetic-access-'+str(state['serial']), 'synthetic-refresh-'+str(state['serial'])
                        state['access'][access] = (identity, time.monotonic()+4)
                        state['refresh'][refresh] = identity
                        return self.reply(200, dict(access_token=access, refresh_token=refresh, expires_in=4,
                            token_type='Bearer', scope='metrics offline_access'))
                    token = self.headers.get('Authorization', '').removeprefix('Bearer ')
                    identity, expires = state['access'].get(token, ('', 0))
                    if not identity or expires < time.monotonic():
                        self.send_response(401)
                        self.send_header('WWW-Authenticate', 'Bearer resource_metadata="'+origin+'/.well-known/oauth-protected-resource/mcp"')
                        self.end_headers(); return
                    request = json.loads(raw)
                    if 'id' not in request:
                        self.send_response(202); self.end_headers(); return
                    method = request['method']
                    if method == 'initialize':
                        result = dict(protocolVersion=request['params']['protocolVersion'], capabilities=dict(tools={}),
                                      serverInfo=dict(name='metricspire', version='oidc-fixture'))
                    elif method == 'tools/list':
                        result = dict(tools=[dict(name=x, description='fixture', inputSchema=dict(type='object', properties={}))
                            for x in ['list_namespaces', 'search_metrics', 'explain_query', 'plan_query', 'submit_query', 'get_query', 'cancel_query']])
                    elif method == 'tools/call':
                        result = dict(content=[dict(type='text', text=json.dumps(dict(namespaces=['synthetic'] if identity=='reader' else [])))])
                    else: result = {}
                    self.reply(200, dict(jsonrpc='2.0', id=request['id'], result=result))
            server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
            origin = f'http://127.0.0.1:{server.server_port}'
            threading.Thread(target=server.serve_forever, daemon=True).start()
            bundle = root/'connection.json'
            bundle.write_text(json.dumps(dict(url=origin+'/mcp', issuer=origin, client_id='fixture-public', scopes=['metrics','offline_access'])))
            def environment(name):
                home = root/name; home.mkdir()
                (home/'config.toml').write_text('mcp_oauth_credentials_store = "file"\n')
                return dict(os.environ, CODEX_HOME=os.path.relpath(home, Path.cwd())), home
            def command(env, flag, success=True):
                result = subprocess.run([str(binary),'--bundle',str(bundle),'--development-loopback',flag],env=env,
                    stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,timeout=100)
                self.assertEqual(result.returncode==0,success,'OIDC command failed at '+flag)
            def login(env):
                proc = subprocess.Popen(['codex','mcp','login','metricspire-oidc','--no-browser','--scopes','metrics,offline_access'],
                    env=env,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True,bufsize=1)
                lines = queue.Queue()
                def read():
                    for line in proc.stdout: lines.put(line)
                reader = threading.Thread(target=read, daemon=True); reader.start()
                try:
                    deadline = time.monotonic()+30
                    auth = None
                    while time.monotonic()<deadline:
                        line = lines.get(timeout=max(.1,deadline-time.monotonic()))
                        for candidate in re.findall(r'https?://[^\s]+',line):
                            if urllib.parse.urlsplit(candidate).path=='/authorize': auth=candidate
                        if auth: break
                    self.assertTrue(auth,'native login did not provide an authorization request')
                    class NoRedirect(urllib.request.HTTPRedirectHandler):
                        def redirect_request(self,*args,**kwargs): return None
                    try: urllib.request.build_opener(NoRedirect).open(auth,timeout=5)
                    except urllib.error.HTTPError as error:
                        self.assertEqual(error.code,302); callback=error.headers['Location']
                    proc.stdin.write(callback+'\n'); proc.stdin.flush()
                    self.assertEqual(proc.wait(timeout=30),0,'native OAuth callback failed')
                finally:
                    if proc.poll() is None: proc.kill();proc.wait()
                    proc.stdin.close();reader.join(timeout=2);proc.stdout.close()
            try:
                env, home = environment('reader')
                incompatible = subprocess.run([str(binary),'--check','--configure-only'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                self.assertNotEqual(incompatible.returncode,0)
                # A reviewed scope remains one literal argument in the printed command.
                original_bundle=bundle.read_text()
                special=dict(json.loads(original_bundle)); special['scopes']=["metrics;$(id)'"]
                bundle.write_text(json.dumps(special))
                printed=subprocess.run([str(binary),'--bundle',str(bundle),'--development-loopback','--configure-only'],env=env,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,text=True,timeout=100)
                self.assertEqual(printed.returncode,0)
                import shlex
                scope_command=printed.stdout.split('authenticate with ',1)[1].strip()
                self.assertEqual(shlex.split(scope_command)[-1],special['scopes'][0])
                bundle.write_text(original_bundle)
                command(env,'--configure-only'); login(env); command(env,'--check')
                before = state['refreshes']; time.sleep(5)
                command(env,'--check')
                self.assertGreater(state['refreshes'],before,'expired token was not refreshed')
                before = state['refreshes']; time.sleep(5); command(env,'--check')
                self.assertGreater(state['refreshes'],before,'rotated refresh grant was not persisted')
                state['identity']='denied'
                other, _ = environment('other'); command(other,'--configure-only'); login(other)
                command(other,'--check',False); command(env,'--check')
                state['revoked']=True;time.sleep(5);command(env,'--check',False)
            finally:
                server.shutdown();server.server_close()

if __name__ == '__main__': unittest.main()
