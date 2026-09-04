#!/usr/bin/env bash
# deploy-lib/post-deploy-verify.sh — 部署后 DB / 网关就绪校验（245 / 154 共用）
#
# 背景：/healthz 与 /api/system/version 不依赖 DB，无法发现
# 「postgres disabled → database not configured」故障。
# 本库在 restart 后等待 EnsureSchema 完成，并断言 DB 端点可用。
#
# 用法（在 deploy-154.sh / deploy-245.sh / deploy-seamless.sh 中 source）:
#   source "$SCRIPT_DIR/deploy-lib/post-deploy-verify.sh"
#   deploy_verify_gateway_ready "$SSH" "$SERVICE_NAME" 8781 120 /path/to/env
#
set -euo pipefail

: "${DEPLOY_VERIFY_GREEN:=$'\033[0;32m'}"
: "${DEPLOY_VERIFY_RED:=$'\033[0;31m'}"
: "${DEPLOY_VERIFY_YELLOW:=$'\033[1;33m'}"
: "${DEPLOY_VERIFY_NC:=$'\033[0m'}"

_deploy_verify_log()  { echo -e "${DEPLOY_VERIFY_GREEN}[verify]${DEPLOY_VERIFY_NC} $*"; }
_deploy_verify_warn() { echo -e "${DEPLOY_VERIFY_YELLOW}[verify]${DEPLOY_VERIFY_NC} $*"; }
_deploy_verify_err()  { echo -e "${DEPLOY_VERIFY_RED}[verify]${DEPLOY_VERIFY_NC} $*" >&2; }

_deploy_verify_ssh() {
  local ssh_cmd=$1
  shift
  # shellcheck disable=SC2086
  $ssh_cmd "$@"
}

# 远端 curl，强制 IPv4，避免 localhost→::1 误报。
_deploy_verify_curl_code() {
  local ssh_cmd=$1 url=$2
  _deploy_verify_ssh "$ssh_cmd" "curl -sS -o /dev/null -w '%{http_code}' --max-time 5 '$url'" 2>/dev/null || echo "000"
}

# 使用远端 env 登录，并访问真正触及 admin DB handler 的受保护端点。
# 匿名 401 只能证明 auth middleware 存活，不能证明数据库可用。
_deploy_verify_authenticated_db_code() {
  local ssh_cmd=$1 base=$2 env_file=$3
  _deploy_verify_ssh "$ssh_cmd" "ENV_FILE='$env_file' BASE='$base' python3 - <<'PY'
import json
import os
import urllib.error
import urllib.request

env = {}
with open(os.environ['ENV_FILE'], encoding='utf-8') as handle:
    for raw in handle:
        line = raw.strip()
        if not line or line.startswith('#') or '=' not in line:
            continue
        key, value = line.split('=', 1)
        env[key] = value

payload = json.dumps({
    'username': env.get('LLM_GATEWAY_ADMIN_USER', ''),
    'password': env.get('LLM_GATEWAY_ADMIN_PASSWORD', ''),
}).encode()

try:
    login = urllib.request.Request(
        os.environ['BASE'] + '/api/auth/token',
        data=payload,
        headers={'Content-Type': 'application/json'},
        method='POST',
    )
    with urllib.request.urlopen(login, timeout=5) as response:
        login_response = json.load(response)
        token = login_response.get('access_token') or login_response.get('api_key') or ''
    if not token:
        raise RuntimeError('login response did not contain a bearer token')
    request = urllib.request.Request(
        os.environ['BASE'] + '/api/system/background-tasks',
        headers={'Authorization': 'Bearer ' + token},
    )
    with urllib.request.urlopen(request, timeout=5) as response:
        print(response.status)
except urllib.error.HTTPError as error:
    print(error.code)
except Exception:
    print('000')
PY" 2>/dev/null | tail -n1 | tr -d '[:space:]'
}

# 等待 gateway 完成启动且 DB 可用。
# 成功：登录后 background-tasks 返回 200，且无 postgres disabled 日志。
# 失败：返回 1 并打印诊断提示。
deploy_verify_gateway_ready() {
  local ssh_cmd=$1 service_name=$2 port=${3:-8781} timeout_s=${4:-90}
  local env_file=${5:-/etc/llm-gateway-go/env}
  local base="http://127.0.0.1:${port}"
  local deadline=$(( $(date +%s) + timeout_s ))
  local last_bg="000"
  local start_ts=$(date +%s)
  local poll=1

  _deploy_verify_log "等待 DB 就绪 (最长 ${timeout_s}s，预迁移后通常 <30s)..."

  while (( $(date +%s) < deadline )); do
    local pg_disabled
    pg_disabled=$(_deploy_verify_ssh "$ssh_cmd" "journalctl -u '$service_name' --since '2 minutes ago' --no-pager -o cat 2>/dev/null | grep -c 'postgres disabled' || true" | tr -d '[:space:]')
    pg_disabled=${pg_disabled:-0}
    if [[ "$pg_disabled" -gt 0 ]]; then
      _deploy_verify_err "postgres disabled（${pg_disabled} 条日志）"
      _deploy_verify_ssh "$ssh_cmd" "journalctl -u '$service_name' --since '2 minutes ago' --no-pager | grep 'postgres disabled' | tail -3" || true
      _deploy_verify_err "常见原因: 252 PG 磁盘满 / EnsureSchema 超时 / schema 漂移"
      _deploy_verify_err "诊断: journalctl -u $service_name --since '5 min ago' | grep -E 'postgres disabled|schema ensured'"
      return 1
    fi

    last_bg=$(_deploy_verify_authenticated_db_code "$ssh_cmd" "$base" "$env_file" || echo "000")
    last_bg=${last_bg:-000}
    if [[ "$last_bg" == "200" ]]; then
      local health
      health=$(_deploy_verify_curl_code "$ssh_cmd" "${base}/healthz")
      if [[ "$health" == "200" ]]; then
        local elapsed=$(( $(date +%s) - start_ts ))
        _deploy_verify_log "✓ DB 就绪 (${elapsed}s, authenticated background-tasks=${last_bg})"
        return 0
      fi
    fi

    if [[ "$last_bg" == "503" || "$last_bg" == "000" ]]; then
      _deploy_verify_log "… 仍在启动 (authenticated background-tasks=${last_bg})"
    fi
    # 前 45s 密集轮询，之后略放宽
    if (( $(date +%s) - start_ts > 45 )); then poll=2; fi
    sleep "$poll"
  done

  _deploy_verify_err "超时: authenticated background-tasks 最后=${last_bg}（期望 200）"
  _deploy_verify_err "首页会显示 database not configured，或部署凭据无法登录"
  return 1
}

# 部署前：从远端 env 读取 DSN 并做 SELECT 1。
# env_file: 154 默认 /etc/llm-gateway-go/env；245 用 /opt/llm-gateway-go/.env
deploy_preflight_pg_from_remote_env() {
  local ssh_cmd=$1 env_file=${2:-/etc/llm-gateway-go/env}
  _deploy_verify_log "预检: 远端 PG SELECT 1 (${env_file})..."
  if ! _deploy_verify_ssh "$ssh_cmd" "set -e; ENV='$env_file'; test -f \"\$ENV\"; DB=\$(grep '^LLM_GATEWAY_DATABASE_URL=' \"\$ENV\" | cut -d= -f2-); test -n \"\$DB\";
    if command -v psql >/dev/null 2>&1; then psql \"\$DB\" -c 'SELECT 1' >/dev/null
    else docker run --rm --network host postgres:17-alpine psql \"\$DB\" -c 'SELECT 1' >/dev/null
    fi"; then
    _deploy_verify_err "远端 PG 不可达 — 中止部署"
    _deploy_verify_err "检查 252 磁盘: df -h（历史事故: No space left on device）"
    return 1
  fi
  _deploy_verify_log "✓ 远端 PG 连通"
}

# 凭据解密冒烟（2026-09-04 245 事故固化）。
#
# 事故：245 与 154 共享 252 PG，密钥 env 完全一致，但 245 跑了缺
# DecryptAny 修复的旧 binary（decryptCred 把 v1:legacy: 前缀的 AES-GCM
# 密文一律送进 Fernet 路径）。结果 /providers/{id} 页面所有凭据
# 「无法解析」、用户 apikey reveal 报 invalid fernet token — 而
# healthz / background-tasks / admin 登录全绿。DB 就绪 ≠ 解密链路可用。
#
# 本检查登录 admin API，抓取有凭据的 provider 的凭据列表，统计
# key_mask_error=decrypt_failed：
#   - 扫描的所有凭据全部失败 = 系统性 keyring/解密回归 → FAIL (return 1)
#   - 部分失败 / 空库 = WARN (return 0，可能是历史脏数据，不阻断部署)
#   - 至少一条 key_masked 成功 = OK
# 可用远端 env 的 LLM_GATEWAY_DECRYPT_SMOKE_PROVIDER_ID=587[,id...] 固定
# 抽检对象（默认按 active_credential_count 降序取前 3 家）。
deploy_verify_credential_decrypt() {
  local ssh_cmd=$1 port=${2:-8781} env_file=${3:-/etc/llm-gateway-go/env}
  local base="http://127.0.0.1:${port}"
  _deploy_verify_log "凭据解密冒烟: ${base} (admin 登录 → providers → credentials)..."
  local verdict
  verdict=$(_deploy_verify_ssh "$ssh_cmd" "ENV_FILE='$env_file' BASE='$base' python3 - <<'PY'
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
PY" 2>/dev/null | tail -n1) || verdict="VERDICT=FAIL reason=ssh_or_python_error"

  case "$verdict" in
    VERDICT=OK*)
      _deploy_verify_log "✓ ${verdict#VERDICT=OK }"
      return 0
      ;;
    VERDICT=FAIL*)
      _deploy_verify_err "${verdict}"
      _deploy_verify_err "系统性解密失败 — keyring env 与 DB 不一致, 或 binary 回归了解密路径"
      _deploy_verify_err "对照: 245/154 共享 252 PG, LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY 必须一致"
      _deploy_verify_err "诊断: journalctl -u <unit> | grep -iE 'decrypt|fernet' + 手工 GET /api/providers/<id>/credentials"
      return 1
      ;;
    *)
      _deploy_verify_warn "${verdict}（不阻断, 人工确认）"
      return 0
      ;;
  esac
}
