#!/usr/bin/env python3
"""Exercise mixed nginx generations without touching a live deployment.

2026-11-29 rewrite: verify_nginx_identity_samples uses three-stage convergence
— budget 40 samples, require the LAST 20 consecutive samples to carry the new
identity; old-identity samples early on only reset the tail counter (transition
window jitter from graceful-reload draining workers is benign); transport and
malformed failures stay fail-closed immediately.
"""
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
    def run_samples(self, sequence):
        """sequence: list of per-sample outcomes ('ok'|'old'|'http'|'malformed').
        Returns (rc, samples_consumed)."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fake = root / 'ssh'
            # Emit the scripted outcome per invocation; keep asserting the
            # connection-closing header and curl flags the function must use.
            fake.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
c=pathlib.Path(os.environ['SAMPLE_COUNT'])
n=int(c.read_text())+1 if c.exists() else 1
c.write_text(str(n))
seq=[l.strip() for l in pathlib.Path(os.environ['SEQUENCE_FILE']).read_text().splitlines() if l.strip()]
outcome=seq[n-1] if n <= len(seq) else seq[-1]
assert "Connection: close" in sys.argv[1]
assert "-kfSs" in sys.argv[1]
identity=json.loads(os.environ['IDENTITY'])
if outcome == 'http': sys.exit(22)
if outcome == 'old': identity['build_seq']=2955
if outcome == 'malformed': print('gateway error'); sys.exit(0)
print(json.dumps(identity))
''', encoding='utf-8')
            fake.chmod(0o755)
            script = root / 'test.sh'
            script.write_text('set -eu\n' + FUNCTIONS + '\nverify_nginx_identity_samples https://example/healthz "" release 2957 998269fb 20261109\n', encoding='utf-8')
            counter = root / 'count'
            counter.write_text('0')
            seq_file = root / 'seq'
            seq_file.write_text('\n'.join(sequence) + '\n')
            env = dict(os.environ, SSH_CMD=str(fake), SAMPLE_COUNT=str(counter),
                       IDENTITY=json.dumps(IDENTITY), SEQUENCE_FILE=str(seq_file))
            result = subprocess.run(['/bin/bash', str(script)], env=env, capture_output=True, text=True)
            return result.returncode, int(counter.read_text())

    def test_clean_convergence_twenty_connections(self):
        rc, n = self.run_samples(['ok'] * 20)
        self.assertEqual(rc, 0)
        self.assertEqual(n, 20)

    def test_transition_jitter_then_convergence_passes(self):
        # 2026-11-29 245 seq 3207 shape: reload happened, draining workers
        # served a few old-identity samples, then new workers took over.
        rc, n = self.run_samples(['old', 'old', 'old'] + ['ok'] * 20)
        self.assertEqual(rc, 0)
        self.assertEqual(n, 23)

    def test_jitter_between_convergence_attempts_resets_tail(self):
        # Old identity reappearing mid-run resets the tail counter; convergence
        # only counts trailing consecutive good samples.
        seq = ['ok'] * 19 + ['old'] + ['ok'] * 20
        rc, n = self.run_samples(seq)
        self.assertEqual(rc, 0)
        self.assertEqual(n, 40)

    def test_persistent_old_identity_fails_closed(self):
        rc, n = self.run_samples(['old'] * 40)
        self.assertEqual(rc, 1)
        self.assertEqual(n, 40)

    def test_alternating_identity_never_converges(self):
        rc, n = self.run_samples((['ok'] * 5 + ['old']) * 7)
        self.assertEqual(rc, 1)
        self.assertEqual(n, 40)

    def test_transport_failure_fail_closed(self):
        rc, n = self.run_samples(['ok'] * 5 + ['http'])
        self.assertEqual(rc, 1)
        self.assertEqual(n, 6)

    def test_malformed_body_fail_closed(self):
        rc, n = self.run_samples(['ok'] * 5 + ['malformed'])
        self.assertEqual(rc, 1)
        self.assertEqual(n, 6)

    def test_tail_knob_allows_smaller_requirement(self):
        # NGINX_IDENTITY_TAIL_OK is honored (deployment-local escape hatch).
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fake = root / 'ssh'
            fake.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
c=pathlib.Path(os.environ['SAMPLE_COUNT'])
n=int(c.read_text())+1 if c.exists() else 1
c.write_text(str(n))
print(json.dumps(json.loads(os.environ['IDENTITY'])))
''', encoding='utf-8')
            fake.chmod(0o755)
            script = root / 'test.sh'
            script.write_text('set -eu\n' + FUNCTIONS + '\nverify_nginx_identity_samples https://example/healthz "" release 2957 998269fb 20261109\n', encoding='utf-8')
            counter = root / 'count'
            counter.write_text('0')
            env = dict(os.environ, SSH_CMD=str(fake), SAMPLE_COUNT=str(counter),
                       IDENTITY=json.dumps(IDENTITY), NGINX_IDENTITY_TAIL_OK='10')
            result = subprocess.run(['/bin/bash', str(script)], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(int(counter.read_text()), 10)


if __name__ == '__main__':
    unittest.main()
