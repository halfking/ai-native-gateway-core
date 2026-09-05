#!/usr/bin/env bash
# 远端凭据解密冒烟(154/245 例行巡检)
#
# 用途：定期检查 154/245 生产环境的凭据解密状态
# 背景：2026-09-05 provider 587 凭据解密事故修复后，固化为例行巡检
# 用法：./scripts/remote-credential-smoke.sh
# 频率：建议每周运行一次，或每次部署后运行
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SMOKE_PY="$(mktemp).py"

# 清理临时文件
trap 'rm -f "$SMOKE_PY"' EXIT

cat > "$SMOKE_PY" <<'PY'
import json
import os
import urllib.request

env = {}
try:
    with open(os.environ['ENV_FILE'], encoding='utf-8') as handle:
        for raw in handle:
            line = raw.strip()
            if not line or line.startswith('#') or '=' not in line:
                continue
            key, value = line.split('=', 1)
            env[key] = value
except OSError:
    print('VERDICT=FAIL reason=env_file_unreadable')
    raise SystemExit

def get(url, token=None):
    headers = {}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    request = urllib.request.Request(url, headers=headers)
    with urllib.request.urlopen(request, timeout=10) as response:
        return response.status, json.load(response)

payload = json.dumps({
    'username': env.get('LLM_GATEWAY_ADMIN_USER', ''),
    'password': env.get('LLM_GATEWAY_ADMIN_PASSWORD', ''),
}).encode()
try:
    login_request = urllib.request.Request(
        os.environ['BASE'] + '/api/auth/token',
        data=payload,
        headers={'Content-Type': 'application/json'},
        method='POST',
    )
    with urllib.request.urlopen(login_request, timeout=10) as response:
        token = json.load(response).get('access_token', '')
    if not token:
        raise RuntimeError('no access_token')
except Exception as error:
    print('VERDICT=FAIL reason=admin_login_failed:%s' % error)
    raise SystemExit

pinned = [p for p in env.get('LLM_GATEWAY_DECRYPT_SMOKE_PROVIDER_ID', '').split(',') if p.strip()]
try:
    _, providers = get(os.environ['BASE'] + '/api/providers', token)
except Exception as error:
    print('VERDICT=FAIL reason=providers_fetch_failed:%s' % error)
    raise SystemExit
rows = providers if isinstance(providers, list) else providers.get('providers', providers.get('data', []))

if pinned:
    order = []
    for pid in pinned:
        for row in rows:
            if str(row.get('id')) == pid.strip():
                order.append(row)
                break
    candidates = order
else:
    with_creds = [r for r in rows if int(r.get('active_credential_count') or 0) > 0]
    with_creds.sort(key=lambda r: int(r.get('active_credential_count') or 0), reverse=True)
    candidates = with_creds[:3]

total = 0
failed = 0
scanned = []
for row in candidates:
    pid = row.get('id')
    try:
        _, creds = get(os.environ['BASE'] + '/api/providers/%s/credentials' % pid, token)
    except Exception as error:
        print('VERDICT=FAIL reason=credentials_fetch_failed:provider=%s:%s' % (pid, error))
        raise SystemExit
    creds = creds if isinstance(creds, list) else creds.get('credentials', creds.get('data', []))
    if not creds:
        continue
    scanned.append(pid)
    total += len(creds)
    failed += sum(1 for c in creds if c.get('key_mask_error'))

if not scanned or total == 0:
    print('VERDICT=WARN reason=no_credentials_to_scan')
    raise SystemExit

summary = 'providers=%s creds=%d failed=%d' % (','.join(str(p) for p in scanned), total, failed)
if failed == 0:
    print('VERDICT=OK %s' % summary)
elif failed >= total:
    print('VERDICT=FAIL %s reason=all_credentials_undecryptable' % summary)
else:
    print('VERDICT=WARN %s reason=partial_failures_likely_legacy_rows' % summary)
PY

echo "==================================================================="
echo "远端凭据解密冒烟测试 - 例行巡检"
echo "==================================================================="
echo ""

# 154 环境
echo "=== 154 (47.97.111.154:8782) ==="
if ssh -o BatchMode=yes -o ConnectTimeout=10 -p 25022 root@47.97.111.154 \
  "ENV_FILE=/etc/llm-gateway-go/env BASE=http://127.0.0.1:8782 python3 -" < "$SMOKE_PY" 2>&1; then
  echo "✓ 154 解密冒烟通过"
else
  echo "✗ 154 解密冒烟失败 (exit code: $?)"
fi
echo ""

# 245 环境
echo "=== 245 (8.136.114.245:8781) ==="
if ssh -o BatchMode=yes -o ConnectTimeout=10 -p 25022 root@8.136.114.245 \
  "ENV_FILE=/opt/llm-gateway-go/.env BASE=http://127.0.0.1:8781 python3 -" < "$SMOKE_PY" 2>&1; then
  echo "✓ 245 解密冒烟通过"
else
  echo "✗ 245 解密冒烟失败 (exit code: $?)"
fi
echo ""

echo "==================================================================="
echo "巡检完成"
echo "==================================================================="
