#!/usr/bin/env python3
"""Run the actual SDK resolver with a noisy recoverable-delete shim."""
import pathlib
import subprocess
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]


class SDKStdoutContract(unittest.TestCase):
    def test_resolver_stdout_contains_only_sdk_path(self):
        source = (ROOT / 'scripts/deploy-local.sh').read_text()
        start = source.index('dl_resolve_sdkroot() {')
        end = source.index('\n}\n', start) + 3
        resolver = source[start:end]
        work = ROOT / '.audit-validation' / 'sdk-stdout'
        work.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=work) as probe:
            script = resolver + r'''
uname() { printf 'Darwin\n'; }
ls() { printf 'MacOSX26.5.sdk\n'; }
clang() { return 0; }
rm() { printf 'mavis-trash: moved to trash\n'; }
mktemp() { printf '%s\n' "$TEST_PROBE"; }
unset DL_HOST_SDKROOT
value=$(dl_resolve_sdkroot)
rc=$?
printf '%s\n' "$value"
exit "$rc"
'''
            import os
            env = dict(os.environ, TEST_PROBE=probe)
            result = subprocess.run(['bash', '-c', script], env=env, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), '/Library/Developer/CommandLineTools/SDKs/MacOSX26.5.sdk')
            self.assertIn('mavis-trash:', result.stderr)


if __name__ == '__main__':
    unittest.main()
