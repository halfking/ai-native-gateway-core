#!/usr/bin/env python3
"""Conservative release cleanup: emit a plan, then apply that exact plan."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import time


ALLOWED = {'llm-gateway-go', 'gateway', 'web', 'configs', 'VERSION', 'version.json', 'SHA256SUMS', 'deployment.json'}
SENSITIVE = {'.git', 'data', 'logs', 'backups', 'pg_wal', 'PG_VERSION', 'postmaster.pid'}


def inventory(path):
    stamp = hashlib.sha256()
    size = 0
    newest = 0
    for base, dirs, files in os.walk(path, followlinks=False):
        for name in sorted(dirs + files):
            p = Path(base) / name
            s = p.lstat()
            if p.is_symlink() or os.path.ismount(p) or name in SENSITIVE or name.endswith(('.db', '.db-wal', '.db-shm', '.sqlite', '.dump')):
                raise ValueError('data/symlink/mount present')
            stamp.update(str((str(p.relative_to(path)), s.st_ino, s.st_size, s.st_mtime_ns)).encode())
            size += s.st_blocks * 512
            newest = max(newest, s.st_mtime)
    return stamp.hexdigest(), size, newest


def references(root):
    refs = []
    for link in [root / 'current', *list((root / 'slots').glob('*'))]:
        if link.is_symlink():
            if not link.exists():
                raise ValueError('dangling active/slot link; operator review required')
            refs.append(str(link.resolve()))
    if not (root / 'current').is_symlink():
        raise ValueError('current must be a valid symlink')
    for proc in Path('/proc').glob('[0-9]*'):
        for item in [proc / 'exe', proc / 'cwd', *list((proc / 'fd').glob('*'))]:
            try:
                refs.append(os.readlink(item))
            except FileNotFoundError:
                pass
            except PermissionError:
                raise ValueError('cannot inspect process references')
        try:
            # Only mapped file paths, never environment or command-line secrets.
            for line in (proc / 'maps').read_text().splitlines():
                parts = line.split(None, 5)
                if len(parts) == 6 and parts[5].startswith('/'):
                    refs.append(parts[5])
        except FileNotFoundError:
            pass
        except PermissionError:
            raise ValueError('cannot inspect process maps')
    config = subprocess.run(['nginx', '-T'], capture_output=True, text=True, check=True)
    # Include backup configs conservatively, too: a rollback must not break.
    for p in Path('/etc/nginx').rglob('*'):
        if p.is_file() and not p.is_symlink() and p.stat().st_size < 1024 * 1024:
            refs.append(p.read_text(errors='ignore'))
    refs.append(config.stdout)
    for p in Path('/etc/systemd/system').glob('*gateway*'):
        if p.is_file():
            refs.append(p.read_text(errors='ignore'))
    return refs


def plan(root, refs, now, keep=5, days=7):
    releases = root / 'releases'
    if releases.is_symlink() or not releases.is_dir():
        raise ValueError('releases must be a real directory')
    items = []
    for path in sorted(releases.iterdir()):
        if path.is_symlink() or not path.is_dir() or not re.fullmatch(r'[0-9]+-[a-f0-9]+', path.name):
            continue
        try:
            meta = json.loads((path / 'deployment.json').read_text())
            version = json.loads((path / 'version.json').read_text())
            if meta.get('verified') is not True or meta.get('version') != path.name:
                continue
            if path.name != str(version['build_seq']) + '-' + version['git_sha']:
                continue
            items.append((path, meta))
        except (ValueError, KeyError, OSError):
            continue
    # Metadata timestamps are ISO UTC. Malformed timestamps retain their release.
    verified = sorted(items, key=lambda item: item[1].get('verified_at', ''), reverse=True)
    retained = {p.name for p, _ in verified[:keep]}
    candidates = []
    protected = []
    for path, meta in items:
        reason = ''
        if path.name in retained:
            reason = 'recent verified rollback point'
        elif any(str(path) in ref for ref in refs):
            reason = 'process/config/slot reference'
        elif any(p.name not in ALLOWED and not p.name.startswith('._') for p in path.iterdir()):
            reason = 'unknown top-level content'
        elif not re.fullmatch(r'\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ', meta.get('verified_at', '')):
            reason = 'untrusted verification timestamp'
        if reason:
            protected.append({'release': path.name, 'reason': reason})
            continue
        try:
            fingerprint, size, newest = inventory(path)
        except (ValueError, OSError) as exc:
            protected.append({'release': path.name, 'reason': str(exc)})
            continue
        if now - max(newest, path.stat().st_mtime) < days * 86400:
            protected.append({'release': path.name, 'reason': 'recently modified'})
            continue
        candidates.append({'release': path.name, 'fingerprint': fingerprint, 'allocated_bytes': size})
    return {'root': str(root), 'keep': keep, 'days': days, 'candidates': candidates, 'protected': protected, 'allocated_bytes': sum(p['allocated_bytes'] for p in candidates)}


def execute(root, approved, refs, now):
    fresh = plan(root, refs, now, approved['keep'], approved['days'])
    allowed = {p['release']: p for p in fresh['candidates']}
    if approved['root'] != str(root) or any(allowed.get(p['release']) != p for p in approved['candidates']):
        raise ValueError('plan changed; refusing cleanup')
    for p in approved['candidates']:
        path = root / 'releases' / p['release']
        if path.is_symlink() or path.resolve().parent != (root / 'releases').resolve():
            raise ValueError('unsafe path')
        shutil.rmtree(path)
    return {'deleted': len(approved['candidates']), 'allocated_bytes': sum(p['allocated_bytes'] for p in approved['candidates'])}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--root', type=Path, default=Path('/opt/llm-gateway-go'))
    p.add_argument('--apply-plan', type=Path)
    args = p.parse_args()
    if os.geteuid() != 0:
        p.error('root required for complete process-reference inspection')
    root = args.root.resolve()
    # Do not race the canonical deployment lock or remove anyone else's lock.
    lock = Path('/var/lib/llm-gateway-go/deploy.lock')
    if args.apply_plan:
        lock.mkdir(mode=0o700)
    elif lock.exists():
        p.error('deploy lock held')
    try:
        refs = references(root)
        if args.apply_plan:
            result = execute(root, json.loads(args.apply_plan.read_text()), refs, time.time())
        else:
            result = plan(root, refs, time.time())
        print(json.dumps(result, indent=2))
    finally:
        if args.apply_plan:
            lock.rmdir()


if __name__ == '__main__':
    main()
