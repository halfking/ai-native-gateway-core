#!/usr/bin/env bash
# 将远端 env 中的 LLM_GATEWAY_ADMIN_PASSWORD 同步到 users 表（JWT 登录 SSOT）。
set -euo pipefail

TARGET="${1:-154}"

case "$TARGET" in
  154)
    SSH_HOST="${LLM_GATEWAY_154_SSH:-root@47.97.111.154}"
    ENV_FILE="/etc/llm-gateway-go/env"
    SERVICE_NAME="${LLM_GATEWAY_ADMIN_SYNC_SERVICE:-${LLM_GATEWAY_154_SERVICE:-llm-gateway-go.service}}"
    HEALTH_URL="http://127.0.0.1:${LLM_GATEWAY_ADMIN_SYNC_PORT:-8781}/healthz"
    ;;
  245)
    SSH_HOST="${LLM_GATEWAY_245_SSH:-root@8.136.114.245}"
    ENV_FILE="/opt/llm-gateway-go/.env"
    SERVICE_NAME="${LLM_GATEWAY_ADMIN_SYNC_SERVICE:-${LLM_GATEWAY_245_SERVICE:-llmgo-245.service}}"
    HEALTH_URL="http://127.0.0.1:${LLM_GATEWAY_ADMIN_SYNC_PORT:-8781}/healthz"
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

# 预检：等待远端 DB 就绪再尝试登录。
# 背景：admin/auth.go 在 db == nil 时 handleLogin 返回 503 database not configured，
# 与 /healthz 200 同时存在（旧 deploy 顺序会因此误触发回滚）。
# 这里循环探测 /healthz 200 且 journalctl 不出现 "postgres disabled"，
# 失败时打印明确诊断，避免 POST 触发误导性 503。
echo "[sync-admin] 预检 $TARGET DB 就绪 (service=$SERVICE_NAME, health=$HEALTH_URL)..."
SVC="$SERVICE_NAME"
HEALTH="$HEALTH_URL"
ssh_run "
  set +e
  deadline=\$(( \$(date +%s) + 60 ))
  while (( \$(date +%s) < deadline )); do
    pg_disabled=\$(journalctl -u '$SVC' --since '2 minutes ago' --no-pager -o cat 2>/dev/null | grep -c 'postgres disabled' || true)
    health=\$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 '$HEALTH' 2>/dev/null || echo 000)
    if [[ \"\$pg_disabled\" -gt 0 ]]; then
      echo 'pg_disabled'
      journalctl -u '$SVC' --since '2 minutes ago' --no-pager 2>/dev/null | grep 'postgres disabled' | tail -3
      exit 2
    fi
    if [[ \"\$health\" == '200' ]]; then
      echo 'ok'
      exit 0
    fi
    sleep 1
  done
  echo \"deadline health=\$health\"
  exit 1
" || {
  rc=$?
  echo "[sync-admin] ✗ 预检失败 (DB 未就绪 / healthz 未返回 200 / postgres disabled)" >&2
  echo "[sync-admin] 诊断: ssh $SSH_HOST 'journalctl -u $SVC --since '5 min ago' | grep -E \"postgres disabled|schema ensured\"'" >&2
  exit $rc
}

echo "[sync-admin] 远端 pgcrypto 更新 users.password_hash ($TARGET)..."
ssh_run "ENV_FILE='$ENV_FILE' TARGET='$TARGET' SYNC_PORT='${LLM_GATEWAY_ADMIN_SYNC_PORT:-8781}' bash -s" <<'REMOTE'
set -euo pipefail
python3 <<'PY'
import json, os, subprocess, sys, urllib.error, urllib.request, uuid

def read_env(path):
    out = {}
    with open(path, encoding='utf-8') as f:
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
    'http://127.0.0.1:' + os.environ.get('SYNC_PORT', '8781') + '/api/auth/token',
    data=body,
    headers={'Content-Type': 'application/json'},
    method='POST',
)
try:
    with urllib.request.urlopen(req, timeout=15) as resp:
        print(resp.status)
        sys.exit(0)
except urllib.error.HTTPError as e:
    print(e.code, file=sys.stderr)
    if e.code == 503:
        print('admin/auth.go handleLogin returned 503 — db likely nil (postgres disabled). Run: journalctl -u <service> --since "5 min ago" | grep "postgres disabled"', file=sys.stderr)
    elif e.code == 401:
        print('login failed: invalid credentials (env password does not match users.password_hash). Check LLM_GATEWAY_ADMIN_USER/PASSWORD consistency.', file=sys.stderr)
    elif e.code == 429:
        print('login rate-limited; retry after backoff.', file=sys.stderr)
    sys.exit(1)
PY
REMOTE
echo "[sync-admin] ✓ 密码已同步且登录验证通过 (HTTP 200)"
