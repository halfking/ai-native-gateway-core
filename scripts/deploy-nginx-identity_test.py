#!/usr/bin/env python3
"""Exercise mixed nginx generations without touching a live deployment."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SOURCE = (ROOT / 'scripts/deploy-seamless.sh').read_text(encoding='utf-8')
FUNCTIONS = SOURCE[SOURCE.index('version_identity_matches() {'):SOURCE.index('# detect_active_side —')]
IDENTITY = dict(version='release', build_seq=2957, git_sha='998269fb', build_date='20261109')


class NginxIdentityTest(unittest.TestCase):
    def check_samples(self, failure, expected_rc, expected_count):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fake = root / 'ssh'
            fake.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
p=pathlib.Path(os.environ['SAMPLE_COUNT'])
n=int(p.read_text())+1 if p.exists() else 1
p.write_text(str(n))
assert "Connection: close" in sys.argv[1]
assert "-kfSs" in sys.argv[1]
identity=json.loads(os.environ['IDENTITY'])
if n == 2:
    failure=os.environ['FAILURE']
    if failure == 'http': sys.exit(22)
    if failure == 'old': identity['build_seq']=2955
    if failure == 'malformed': print('gateway error'); sys.exit(0)
print(json.dumps(identity))
''', encoding='utf-8')
            fake.chmod(0o755)
            script = root / 'test.sh'
            script.write_text('set -eu\n' + FUNCTIONS + '\nverify_nginx_identity_samples https://example/healthz "" release 2957 998269fb 20261109\n', encoding='utf-8')
            counter = root / 'count'
            env = dict(os.environ, SSH_CMD=str(fake), SAMPLE_COUNT=str(counter),
                       IDENTITY=json.dumps(IDENTITY), FAILURE=failure)
            result = subprocess.run(['/bin/bash', str(script)], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, expected_rc, result.stdout + result.stderr)
            self.assertEqual(int(counter.read_text()), expected_count)

    def test_twenty_consistent_connections(self):
        self.check_samples('none', 0, 20)

    def test_mixed_generations_and_transport_fail_closed(self):
        for failure in ['http', 'old', 'malformed']:
            with self.subTest(failure=failure):
                self.check_samples(failure, 1, 2)


if __name__ == '__main__':
    unittest.main()
