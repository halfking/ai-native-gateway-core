#!/usr/bin/env bash
# 将远端 env 中的 LLM_GATEWAY_ADMIN_PASSWORD 同步到 users 表（JWT 登录 SSOT）。
# users 表存在 admin 时，/api/auth/token 不会回退 env 密码。
#
# 用法:
#   bash scripts/ops/sync-admin-password-from-env.sh 154
#   bash scripts/ops/sync-admin-password-from-env.sh 245
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
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

SSH_PORT="${LLM_GATEWAY_SSH_PORT:-25022}"
SSH_KEY_FILE="${SSH_KEY_FILE:-}"
for k in ~/.ssh/id_ed25519 ~/.ssh/56_id_rsa ~/.ssh/71_id_rsa; do
  [[ -f "$k" ]] && SSH_KEY_FILE="$k" && break
done

ssh_cmd() {
  if [[ -n "$SSH_KEY_FILE" && -f "$SSH_KEY_FILE" ]]; then
    ssh -i "$SSH_KEY_FILE" -p "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=accept-new "$SSH_HOST" "$@"
  else
    sshpass -e ssh -p "$SSH_PORT" -o StrictHostKeyChecking=accept-new "$SSH_HOST" "$@"
  fi
}

echo "[sync-admin] 读取 $TARGET env 并生成 bcrypt hash..."
ADMIN_USER=$(ssh_cmd "grep '^LLM_GATEWAY_ADMIN_USER=' '$ENV_FILE' | cut -d= -f2-" | tr -d '\r')
ADMIN_PW=$(ssh_cmd "grep '^LLM_GATEWAY_ADMIN_PASSWORD=' '$ENV_FILE' | cut -d= -f2-" | tr -d '\r')
DB_URL=$(ssh_cmd "grep '^LLM_GATEWAY_DATABASE_URL=' '$ENV_FILE' | cut -d= -f2-" | tr -d '\r')

[[ -n "$ADMIN_USER" && -n "$ADMIN_PW" && -n "$DB_URL" ]] || {
  echo "ERROR: env 缺少 ADMIN_USER / ADMIN_PASSWORD / DATABASE_URL" >&2
  exit 1
}

HASH=$(printf '%s' "$ADMIN_PW" | (cd "$PROJECT_ROOT" && go run "$SCRIPT_DIR/bcrypt-hash.go"))
HASH_ESC=${HASH//\'/\'\'}

echo "[sync-admin] 更新 users.password_hash (username=$ADMIN_USER)..."
ssh_cmd "psql '$DB_URL' -v ON_ERROR_STOP=1 -c \"UPDATE users SET password_hash = '$HASH_ESC', must_change_password = false, updated_at = now() WHERE username = '$ADMIN_USER';\""

echo "[sync-admin] 验证登录..."
if ssh_cmd "python3 - <<'PY'
import json, os, subprocess, urllib.request
env = {}
with open('$ENV_FILE') as f:
    for line in f:
        line = line.strip()
        if not line or line.startswith('#') or '=' not in line:
            continue
        k, v = line.split('=', 1)
        env[k] = v
user = env.get('LLM_GATEWAY_ADMIN_USER', 'admin')
pw = env.get('LLM_GATEWAY_ADMIN_PASSWORD', '')
body = json.dumps({'username': user, 'password': pw}).encode()
req = urllib.request.Request('http://127.0.0.1:8781/api/auth/token', data=body, headers={'Content-Type': 'application/json'}, method='POST')
try:
    with urllib.request.urlopen(req, timeout=10) as r:
        print(r.status)
except urllib.error.HTTPError as e:
    print(e.code)
PY" | grep -q '^200$'; then
  echo "[sync-admin] ✓ 登录成功 (HTTP 200)"
else
  echo "[sync-admin] ✗ 登录仍失败" >&2
  exit 1
fi
