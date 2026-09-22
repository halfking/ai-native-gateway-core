import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts/ops" / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


repair = load("repair-gateway-static")
verify = load("verify-gateway-delivery")


class StaticRepairTest(unittest.TestCase):
    def fixture(self):
        return '''server { listen 80; server_name llm.kxpms.cn; }
server {
    listen 443 ssl;
    server_name llm.kxpms.cn;
    location = /menu-config.json {
        alias /opt/llm-gateway-go/releases/1325-6d1ea6f5/web/menu-config.json;
    }
    location /assets/ { expires 7d; add_header Cache-Control "public, immutable"; }
''' + repair.SPA + '\n}\n'

    def test_alias_cache_and_idempotence(self):
        after = repair.repair(self.fixture())
        self.assertIn('alias ' + repair.ROOT + '/menu-config.json;', after)
        self.assertIn('Cache-Control "no-cache" always', after)
        self.assertIn('X-Frame-Options "DENY" always', after)
        self.assertIn('X-Content-Type-Options "nosniff" always', after)
        self.assertIn('Referrer-Policy "strict-origin-when-cross-origin" always', after)
        self.assertIn('location /assets/ { expires 7d;', after)
        self.assertNotIn('/versionz', after)
        self.assertEqual(after, repair.repair(after))

    def test_unknown_topology_and_ambiguous_blocks_rejected(self):
        for text in (self.fixture().replace('llm.kxpms.cn', 'other.example'),
                     self.fixture() + repair.SPA,
                     self.fixture().replace('try_files $uri /index.html;', 'proxy_pass http://other;')):
            with self.assertRaises(ValueError):
                repair.repair(text)

    def test_dry_run_does_not_write(self):
        with tempfile.TemporaryDirectory() as td:
            config = Path(td) / 'vhost.conf'
            config.write_text(self.fixture())
            out = subprocess.run(['python3', str(ROOT / 'scripts/ops/repair-gateway-static.py'), str(config)], capture_output=True, text=True)
            self.assertEqual(out.returncode, 0, out.stderr)
            self.assertIn('DRY_RUN', out.stdout)
            self.assertEqual(config.read_text(), self.fixture())
            self.assertEqual(len(list(Path(td).iterdir())), 1)

    def test_validation_failure_rolls_back(self):
        with tempfile.TemporaryDirectory() as td:
            config = Path(td) / 'vhost.conf'
            before = self.fixture()
            config.write_text(before)
            failure = subprocess.CalledProcessError(1, ['nginx', '-t'])
            with patch.object(repair.subprocess, 'run', side_effect=[None, failure, None, None]) as run:
                with self.assertRaises(subprocess.CalledProcessError):
                    repair.apply(config, before, repair.repair(before))
                self.assertEqual(run.call_count, 4)
            self.assertEqual(config.read_text(), before)
            self.assertEqual(len(list(Path(td).glob('*.pre-static-repair-*'))), 1)

    def test_reload_failure_rolls_back(self):
        with tempfile.TemporaryDirectory() as td:
            config = Path(td) / 'vhost.conf'
            before = self.fixture()
            config.write_text(before)
            failure = subprocess.CalledProcessError(1, ['nginx', '-s', 'reload'])
            with patch.object(repair.subprocess, 'run', side_effect=[None, None, failure, None, None]):
                with self.assertRaises(subprocess.CalledProcessError):
                    repair.apply(config, before, repair.repair(before))
            self.assertEqual(config.read_text(), before)

    def test_changed_input_refused(self):
        with tempfile.TemporaryDirectory() as td:
            p = Path(td) / 'conf'
            p.write_text('parallel edit')
            with self.assertRaises(ValueError):
                repair.apply(p, self.fixture(), repair.repair(self.fixture()))


class DeliveryTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dist = Path(self.tmp.name)
        (self.dist / 'assets').mkdir()
        html = '<script src="/assets/index-a.js"></script><link rel="stylesheet" href="/assets/index-a.css">'
        js = '按处理队列 QueuePerspectivePanel RequestJourneyQueues NodeStatusMatrix'
        menu = json.dumps({'source': 'gateway-appNav'})
        self.payloads = {}
        for path, content, mime in [('index.html', html, 'text/html'), ('assets/index-a.js', js, 'application/javascript'), ('assets/index-a.css', '.x{}', 'text/css'), ('menu-config.json', menu, 'application/json')]:
            (self.dist / path).write_text(content)
            self.payloads['/' + path] = (content.encode(), {'content-type': mime})
        self.payloads['/?tab=stream'] = (html.encode(), {'Cache-Control': 'no-cache'})
        identity = json.dumps({'git_sha': 'abc12345', 'build_seq': 9, 'ready': True}).encode()
        for path in ('/version', '/healthz', '/version.json'):
            self.payloads[path] = (identity, {})
        self.payloads['/readyz'] = (b'{"status":"ready","database":{"connected":true},"redis":{"connected":true}}', {})

    def result(self):
        return verify.verify('https://example.test', 'abc12345', 9, self.dist, lambda url: self.payloads[url.removeprefix('https://example.test')])

    def test_pass_does_not_claim_rendered_or_cache_cause(self):
        result = self.result()
        self.assertTrue(result['pass'])
        self.assertEqual(result['rendered_ui'], 'NOT_VERIFIED')
        self.assertEqual(result['cache_root_cause'], 'NOT_PROVEN')

    def test_old_identity_fails(self):
        self.payloads['/version'] = (b'{"git_sha":"older","build_seq":9}', {})
        self.assertFalse(self.result()['pass'])

    def test_stale_asset_same_filename_fails(self):
        self.payloads['/assets/index-a.js'] = (b'old JS', {'content-type': 'application/javascript'})
        self.assertFalse(self.result()['pass'])

    def test_missing_cache_policy_fails(self):
        body, _ = self.payloads['/?tab=stream']
        self.payloads['/?tab=stream'] = (body, {})
        self.assertFalse(self.result()['pass'])

    def test_html_instead_of_menu_rejected(self):
        self.payloads['/menu-config.json'] = (b'<html>fallback</html>', {'content-type': 'text/html'})
        with self.assertRaises(ValueError):
            self.result()

    def test_asset_mime_fails(self):
        body, _ = self.payloads['/assets/index-a.js']
        self.payloads['/assets/index-a.js'] = (body, {'content-type': 'text/html'})
        self.assertFalse(self.result()['pass'])

    def test_database_not_ready_fails(self):
        self.payloads['/readyz'] = (b'{"status":"not_ready","database":{},"redis":{}}', {})
        self.assertFalse(self.result()['pass'])


if __name__ == '__main__':
    unittest.main()
