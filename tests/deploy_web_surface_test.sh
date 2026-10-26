#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
python3 - "$ROOT" <<'PY'
import http.server
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import threading
import unittest

source = (Path(sys.argv[1]) / 'scripts/deploy-seamless.sh').read_text()

def function(name):
    match = re.search(r'^' + name + r'\(\) \{\n.*?^\}', source, re.M | re.S)
    if not match:
        raise AssertionError('missing actual deployment function: ' + name)
    return match.group()

def shell(name, body, env):
    setup = 'set -euo pipefail\nremote_ssh() { bash -c "$1"; }\nerr() { echo "$*" >&2; }\n'
    return subprocess.run(['bash', '-c', setup + function(name) + '\n' + body],
                          env=dict(os.environ, **env), text=True, capture_output=True)

class WebSurface(unittest.TestCase):
    def test_private_umask_and_public_only_permissions(self):
        for mask in (0o022, 0o077):
            with self.subTest(umask=oct(mask)), tempfile.TemporaryDirectory() as folder:
                bundle = Path(folder) / 'bundle'
                bundle.mkdir(mode=0o777 & ~mask)
                release = Path(folder) / 'releases/candidate'
                release.mkdir(parents=True, mode=0o755)
                for surface in ('web', 'web-mobile'):
                    assets = bundle / surface / 'assets'
                    assets.mkdir(parents=True)
                    os.chmod(assets.parent, 0o777 & ~mask)
                    os.chmod(assets, 0o777 & ~mask)
                    (assets / 'app.js').write_text('export default 1')
                    os.chmod(assets / 'app.js', 0o666 & ~mask)
                secret = bundle / 'private-config'
                secret.write_text('private fixture')
                secret.chmod(0o600)
                archive = Path(folder) / 'bundle.tar'
                subprocess.run(['tar', 'cf', str(archive), '-C', str(bundle), '.'], check=True)
                # Remote GNU tar runs as root and preserves archive permissions;
                # -p requests the same behavior from non-root BSD tar on macOS.
                subprocess.run(['tar', 'xpf', str(archive), '-C', str(release)], check=True)
                self.assertEqual(release.stat().st_mode & 0o777, 0o777 & ~mask)
                result = shell('normalize_release_web_permissions',
                               'normalize_release_web_permissions candidate', {'REMOTE_ROOT': folder})
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(release.stat().st_mode & 0o777, 0o755)
                for surface in ('web', 'web-mobile'):
                    self.assertEqual((release / surface).stat().st_mode & 0o777, 0o755)
                    self.assertEqual((release / surface / 'assets').stat().st_mode & 0o777, 0o755)
                    self.assertEqual((release / surface / 'assets/app.js').stat().st_mode & 0o777, 0o644)
                self.assertEqual((release / 'private-config').stat().st_mode & 0o777, 0o600)

    def test_real_http_gate_checks_html_and_javascript(self):
        state = {'mode': 'ok'}
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_): pass
            def do_GET(self):
                mode = state['mode'] if self.headers['Host'].split(':')[0] == 'llm.kxpms.cn' else 'ok'
                status, kind, body = 200, 'text/html', '<html><script src="/assets/app.js"></script></html>'
                if self.path == '/healthz': kind, body = 'application/json', '{"status":"ok"}'
                elif self.path == '/dashboard' and mode == '500': status, body = 500, 'nginx error'
                elif self.path == '/dashboard' and mode == 'json': kind, body = 'application/json', '{}'
                elif self.path == '/dashboard' and mode == 'json_mime': kind = 'application/json'
                elif self.path == '/dashboard' and mode == 'html_params': kind = 'Text/HTML; charset=utf-8'
                elif self.path == '/assets/app.js':
                    kind, body = 'text/javascript', 'export default 1'
                    if mode == 'missing_js': status = 404
                    if mode == 'html_js': kind, body = 'text/html', '<html>fallback</html>'
                    if mode == 'mixed_js_mime': kind = 'text/html; note=javascript'
                    if mode == 'html_params': kind = 'Application/JavaScript; charset=utf-8'
                self.send_response(status)
                self.send_header('Content-Type', kind)
                self.end_headers()
                self.wfile.write(body.encode())
        server = http.server.HTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            for mode in ('ok', '500', 'json', 'json_mime', 'missing_js', 'html_js', 'mixed_js_mime', 'html_params'):
                with self.subTest(mode=mode):
                    state['mode'] = mode
                    body = 'target_field() { if [ "$2" = internal_https_health_url ]; then echo "$WEB_URL/healthz"; else echo "$WEB_ARGS"; fi; }\nverify_public_web'
                    result = shell('verify_public_web', body,
                                   {'TARGET': '154', 'WEB_URL': 'http://llm.kxpms.cn:' + str(server.server_port),
                                    'WEB_ARGS': '--noproxy "*" --resolve llm.kxpms.cn:' + str(server.server_port) + ':127.0.0.1'})
                    self.assertEqual(result.returncode == 0, mode in ('ok', 'html_params'), result.stderr)
        finally:
            server.shutdown()
            thread.join()
            server.server_close()

    def test_154_pins_public_host_and_sni(self):
        with tempfile.TemporaryDirectory() as folder:
            body = r'''target_field() { if [ "$2" = internal_https_health_url ]; then echo https://127.0.0.1/healthz; fi; }
remote_ssh() { echo "$1" >> "$TRACE"; case "$1" in *dashboard*) printf '<html><script src="/assets/app.js"></script></html>\ntext/html';; *) echo text/javascript;; esac; }
verify_public_web'''
            trace = Path(folder) / 'curl-calls'
            result = shell('verify_public_web', body, {'TARGET': '154', 'TRACE': str(trace)})
            self.assertEqual(result.returncode, 0, result.stderr)
            calls = trace.read_text().splitlines()
            self.assertEqual(len(calls), 2)
            self.assertTrue(all('--resolve llm.kxpms.cn:443:127.0.0.1' in x and "'https://llm.kxpms.cn/" in x for x in calls))

    def test_fail_closed_call_sites(self):
        stage = source[source.index('# 5.5 --no-frontend'):source.index('# 6. verify')]
        self.assertIn('normalize_release_web_permissions "$version" ||', stage)
        gate = source.index('if ! verify_public_web; then')
        mark = source.index('if ! host_mark_verified', gate)
        self.assertIn('_bluegreen_abort', source[gate:mark])

unittest.main(argv=['deploy_web_surface'], verbosity=2)
PY
