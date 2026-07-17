#!/usr/bin/env bash
# 将远端 env 中的 LLM_GATEWAY_ADMIN_PASSWORD 同步到 users 表（JWT 登录 SSOT）。
set -euo pipefail

TARGET="${1:-154}"

case "$TARGET" in
  154)
    SSH_HOST="${LLM_GATEWAY_154_SSH:-root@47.97.111.154}"
    ENV_FILE="/etc/llm-gateway-go/env"
    ;;
  245)
    SSH_HOST="${LLM_GATEWAY_245_SSH:-root@8.136.114.245}"
    ENV_FILE="/opt/llm-gateway-go/.env"
    ;;
  *)
    echo "用法: $0 <154|245>" >&2
    exit 1
    ;;
esac

SSH_PORT="${LLM_GATEWAY_SSH_PORT:-${SSH_PORT:-25022}}"
case "$TARGET" in
  245) SSH_KEY_FILE="${SSH_KEY_245:-${SSH_KEY_FILE:-}}" ;;
  154) SSH_KEY_FILE="${SSH_KEY_154:-${SSH_KEY_FILE:-}}" ;;
esac
[[ -n "$SSH_KEY_FILE" && -f "$SSH_KEY_FILE" ]] || { echo "ERROR: missing SSH key for $TARGET" >&2; exit 1; }

ssh_run() {
  if [[ -n "$SSH_KEY_FILE" && -f "$SSH_KEY_FILE" ]]; then
    ssh -i "$SSH_KEY_FILE" -p "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=accept-new "$SSH_HOST" "$@"
  else
    sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=accept-new "$SSH_HOST" "$@"
  fi
}

echo "[sync-admin] 远端 pgcrypto 更新 users.password_hash ($TARGET)..."
ssh_run "ENV_FILE='$ENV_FILE' bash -s" <<'REMOTE'
set -euo pipefail
python3 <<'PY'
import json, os, subprocess, sys, urllib.error, urllib.request, uuid

def read_env(path):
    out = {}
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith('#') or '=' not in line:
                continue
            k, v = line.split('=', 1)
            out[k] = v
    return out

env = read_env(os.environ['ENV_FILE'])
user = env.get('LLM_GATEWAY_ADMIN_USER', 'admin')
pw = env.get('LLM_GATEWAY_ADMIN_PASSWORD', '')
db = env.get('LLM_GATEWAY_DATABASE_URL', '')
if not user or not pw or not db:
    print('ERROR: missing ADMIN_USER/PASSWORD/DATABASE_URL in env', file=sys.stderr)
    sys.exit(1)

tag = 'pw_' + uuid.uuid4().hex
sql = (
    f"UPDATE users SET password_hash = crypt(${tag}${pw}${tag}$, gen_salt('bf', 10)), "
    f"must_change_password = false, updated_at = now() WHERE username = '{user.replace(chr(39), chr(39)*2)}';"
)
r = subprocess.run(['psql', db, '-v', 'ON_ERROR_STOP=1', '-c', sql], stdout=subprocess.PIPE, stderr=subprocess.PIPE, universal_newlines=True)
if r.returncode != 0:
    print(r.stderr or r.stdout, file=sys.stderr)
    sys.exit(r.returncode)

body = json.dumps({'username': user, 'password': pw}).encode()
req = urllib.request.Request(
    'http://127.0.0.1:8781/api/auth/token',
    data=body,
    headers={'Content-Type': 'application/json'},
    method='POST',
)
try:
    with urllib.request.urlopen(req, timeout=15) as resp:
        print(resp.status)
except urllib.error.HTTPError as e:
    print(e.code, file=sys.stderr)
    sys.exit(1)
PY
REMOTE

echo "[sync-admin] ✓ 密码已同步且登录验证通过 (HTTP 200)"
