import importlib.util
import json
import os
from pathlib import Path
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('prune', ROOT / 'scripts/ops/prune-gateway-releases.py')
prune = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prune)


class PruneTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / 'releases').mkdir()
        self.now = time.time()
        for n in range(1, 9):
            p = self.root / 'releases' / (str(n) + '-abcdef12')
            p.mkdir()
            (p / 'deployment.json').write_text(json.dumps({'version': p.name, 'verified': True, 'verified_at': '2026-08-%02dT00:00:00Z' % n}))
            (p / 'version.json').write_text(json.dumps({'build_seq': n, 'git_sha': 'abcdef12'}))
            (p / 'gateway').write_text('binary')
            for f in [*p.iterdir(), p]:
                os.utime(f, (self.now - 30 * 86400, self.now - 30 * 86400))

    def plan(self, refs=()):
        return prune.plan(self.root, refs, self.now)

    def test_dry_plan_preserves_latest_five(self):
        result = self.plan()
        self.assertEqual([p['release'] for p in result['candidates']], ['1-abcdef12', '2-abcdef12', '3-abcdef12'])
        self.assertEqual(len(list((self.root / 'releases').iterdir())), 8)

    def test_reference_protection_includes_config_and_slot(self):
        refs = [str(self.root / 'releases/1-abcdef12/web'), 'alias ' + str(self.root / 'releases/2-abcdef12/web/index.html')]
        self.assertEqual([p['release'] for p in self.plan(refs)['candidates']], ['3-abcdef12'])

    def test_unknown_data_and_symlinks_preserved(self):
        (self.root / 'releases/1-abcdef12/data').mkdir()
        (self.root / 'releases/2-abcdef12/web').mkdir()
        (self.root / 'releases/2-abcdef12/web/state.db').write_text('unique state')
        (self.root / 'releases/3-abcdef12/web').symlink_to(self.root, target_is_directory=True)
        self.assertFalse(self.plan()['candidates'])

    def test_recent_file_preserved(self):
        (self.root / 'releases/1-abcdef12/gateway').write_text('recent binary')
        self.assertNotIn('1-abcdef12', [p['release'] for p in self.plan()['candidates']])

    def test_changed_plan_fails_without_deletion(self):
        approved = self.plan()
        (self.root / 'releases/2-abcdef12/gateway').write_text('changed')
        with self.assertRaises(ValueError):
            prune.execute(self.root, approved, [], self.now)
        self.assertEqual(len(list((self.root / 'releases').iterdir())), 8)

    def test_new_reference_aborts(self):
        approved = self.plan()
        with self.assertRaises(ValueError):
            prune.execute(self.root, approved, [str(self.root / 'releases/1-abcdef12')], self.now)
        self.assertEqual(len(list((self.root / 'releases').iterdir())), 8)

    def test_apply_deletes_only_approved_subset(self):
        approved = self.plan()
        approved['candidates'] = approved['candidates'][:1]
        result = prune.execute(self.root, approved, [], self.now)
        self.assertEqual(result['deleted'], 1)
        self.assertFalse((self.root / 'releases/1-abcdef12').exists())
        self.assertEqual(len(list((self.root / 'releases').iterdir())), 7)


if __name__ == '__main__':
    unittest.main()
