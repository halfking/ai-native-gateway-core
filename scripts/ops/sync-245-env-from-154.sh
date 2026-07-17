#!/usr/bin/env bash
# 从 154 同步 245 缺失的关键 env（admin 登录等）。
set -euo pipefail

SSH_PORT="${LLM_GATEWAY_SSH_PORT:-${SSH_PORT:-25022}}"
SSH_KEY_154="${SSH_KEY_154:-${SSH_KEY_FILE:-}}"
SSH_KEY_245="${SSH_KEY_245:-${SSH_KEY_FILE:-}}"
[[ -n "$SSH_KEY_154" && -f "$SSH_KEY_154" ]] || { echo "ERROR: missing SSH_KEY_154" >&2; exit 1; }
[[ -n "$SSH_KEY_245" && -f "$SSH_KEY_245" ]] || { echo "ERROR: missing SSH_KEY_245" >&2; exit 1; }
SSH_OPTS_154=(-i "$SSH_KEY_154" -p "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=accept-new)
SSH_OPTS_245=(-i "$SSH_KEY_245" -p "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=accept-new)
SCP_OPTS_245=(-i "$SSH_KEY_245" -P "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=accept-new)
SSH_154="${LLM_GATEWAY_154_SSH:-root@47.97.111.154}"
SSH_245="${LLM_GATEWAY_245_SSH:-root@8.136.114.245}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TMP="/tmp/llm-gateway-154-admin-keys.$$"
trap 'rm -f "$TMP"' EXIT

echo "[sync-245-env] 从 154 导出 admin 键..."
ssh "${SSH_OPTS_154[@]}" "$SSH_154" \
  "grep -E '^LLM_GATEWAY_(ADMIN_USER|ADMIN_PASSWORD|PUBLIC_URL)=' /etc/llm-gateway-go/env" >"$TMP"
[[ -s "$TMP" ]] || { echo "ERROR: 154 未找到 ADMIN 键" >&2; exit 1; }

echo "[sync-245-env] 合并到 245 .env..."
scp "${SCP_OPTS_245[@]}" \
  "$TMP" "$SSH_245:/tmp/llm-154-admin-keys.env"
ssh "${SSH_OPTS_245[@]}" "$SSH_245" "python3 - <<'PY'
import shutil, time
from pathlib import Path

src = Path('/tmp/llm-154-admin-keys.env')
env_path = Path('/opt/llm-gateway-go/.env')
bak = Path(f'/opt/llm-gateway-go/.env.bak.sync154.{time.strftime(\"%Y%m%d-%H%M%S\")}')
shutil.copy2(env_path, bak)

incoming = {}
for line in src.read_text().splitlines():
    line = line.strip()
    if line and '=' in line:
        k, v = line.split('=', 1)
        incoming[k] = v
if 'LLM_GATEWAY_ADMIN_PASSWORD' in incoming:
    incoming['LLM_GATEWAY_SEED_ADMIN_PASSWORD'] = incoming['LLM_GATEWAY_ADMIN_PASSWORD']

lines = env_path.read_text().splitlines()
out, added = [], []
for ln in lines:
    if ln.startswith('LLM_GATEWAY_') and '=' in ln:
        key = ln.split('=', 1)[0]
        if key in incoming:
            out.append(f'{key}={incoming[key]}')
            added.append(key)
            del incoming[key]
        else:
            out.append(ln)
    else:
        out.append(ln)
if out and out[-1].strip():
    out.append('')
if incoming:
    out.append('# synced from 154')
    for k in sorted(incoming):
        out.append(f'{k}={incoming[k]}')
        added.append(k)
env_path.write_text('\n'.join(out) + '\n')
try:
    src.unlink()
except FileNotFoundError:
    pass
print('backup:', bak)
print('keys:', ','.join(sorted(set(added))))
PY"

echo "[sync-245-env] 同步 admin 密码到 users 表..."
bash "$SCRIPT_DIR/sync-admin-password-from-env.sh" 245

echo "[sync-245-env] 验证登录..."
ssh "${SSH_OPTS_245[@]}" "$SSH_245" "python3 - <<'PY'
import json, urllib.request
from pathlib import Path
env = {}
for ln in Path('/opt/llm-gateway-go/.env').read_text().splitlines():
    if '=' in ln and not ln.strip().startswith('#'):
        k, v = ln.split('=', 1)
        env[k] = v
for req in ('LLM_GATEWAY_ADMIN_USER', 'LLM_GATEWAY_ADMIN_PASSWORD'):
    if req not in env or not env[req]:
        raise SystemExit(f'missing {req}')
body = json.dumps({'username': env['LLM_GATEWAY_ADMIN_USER'], 'password': env['LLM_GATEWAY_ADMIN_PASSWORD']}).encode()
req = urllib.request.Request('http://127.0.0.1:8781/api/auth/token', data=body, headers={'Content-Type': 'application/json'}, method='POST')
with urllib.request.urlopen(req, timeout=15) as r:
    print('login HTTP', r.status)
PY"

echo "[sync-245-env] ✓ 245 环境已更正"
