#!/usr/bin/env bash
# verify-install.sh — 部署后自检（L1 → L4），Linux / macOS / Windows(WSL) 通用。
#
# 用法:
#   bash verify-install.sh                       # 默认读 ~/llm-gateway/.env
#   LLM_GATEWAY_HOME=/opt/kx-gateway bash verify-install.sh
#   VERIFY_PORT=8781 bash verify-install.sh      # 覆盖端口
#
# 退出码: 0=全过 1=任一 L1-L4 失败
set -euo pipefail

HOME_DIR="${LLM_GATEWAY_HOME:-$HOME/llm-gateway}"
ENV_FILE="$HOME_DIR/.env"
PORT="${VERIFY_PORT:-8781}"
BASE="http://127.0.0.1:${PORT}"

if [[ -f "$ENV_FILE" ]]; then
  set -a; # shellcheck disable=SC1091
  source "$ENV_FILE"; set +a
else
  echo "[verify] WARN: $ENV_FILE 不存在，跳过 env 读取（L2 DB/Redis 需手动核对）" >&2
fi

pass() { echo "✅ L$1 $2"; }
fail() { echo "❌ L$1 $2" >&2; exit 1; }

# ---------- L1 HTTP 存活 ----------
echo "=== L1 HTTP 存活 ==="
code=$(curl -s -o /tmp/verify-l1.json -w "%{http_code}" "$BASE/healthz" || echo 000)
[[ "$code" == "200" ]] || fail 1 "/healthz 返回 $code (base=$BASE)"
grep -q '"ok"' /tmp/verify-l1.json || fail 1 "/healthz body 缺 ok 字段"
pass 1 "/healthz 200 + ok"

# ---------- L2 依赖连通 ----------
echo "=== L2 依赖连通 ==="
if command -v psql >/dev/null 2>&1 && [[ -n "${DB_HOST:-}" ]]; then
  PGPASSWORD="${DB_PASSWORD:-}" psql -h "$DB_HOST" -p "${DB_PORT:-5432}" -U "${DB_USER:-llm_user}" \
    -d "${DB_NAME:-llm_gateway}" -tAc "SELECT 1;" | grep -q 1 || fail 2 "DB 查询失败"
  pass 2 "PostgreSQL SELECT 1"
else
  echo "⚠️ L2 psql 不在 PATH 或 DB_HOST 未设 — 跳过 DB 连通（compose 模式由安装器已验）" >&2
fi

if command -v redis-cli >/dev/null 2>&1 && [[ -n "${REDIS_PORT:-}" ]]; then
  REDISCLI_AUTH="${REDIS_PASSWORD:-}" redis-cli -h "${REDIS_HOST:-127.0.0.1}" -p "$REDIS_PORT" \
    ping | grep -q PONG || fail 2 "Redis PING 失败"
  pass 2 "Redis PING PONG"
else
  echo "⚠️ L2 redis-cli 不在 PATH 或 REDIS_PORT 未设 — 跳过 Redis" >&2
fi

# ---------- L3 功能链路 ----------
echo "=== L3 功能链路 ==="
if [[ -n "${LLM_GATEWAY_API_KEY:-}" ]]; then
  m_code=$(curl -s -o /tmp/verify-l3.json -w "%{http_code}" \
    "$BASE/v1/models" -H "Authorization: Bearer $LLM_GATEWAY_API_KEY")
  [[ "$m_code" == "200" ]] || fail 3 "/v1/models 返回 $m_code"
  pass 3 "/v1/models 200"
else
  echo "⚠️ L3 LLM_GATEWAY_API_KEY 未设 — 跳过 models 请求" >&2
fi

# ---------- L4 业务真实 ----------
echo "=== L4 业务真实 ==="
INSTANCE_ID_FILE=""
for c in "$HOME_DIR/instance_id" "$HOME/.local/share/kx-gateway/instance.id"; do
  [[ -f "$c" ]] && INSTANCE_ID_FILE="$c" && break
done
if [[ -n "$INSTANCE_ID_FILE" ]]; then
  echo "instance_id: $(cat "$INSTANCE_ID_FILE")"
  echo "→ 请到 https://llm.kxpms.cn/maintain 确认该实例在线"
  pass 4 "实例注册文件存在"
else
  echo "⚠️ L4 未找到 instance_id（首次部署需激活后生成，属预期）" >&2
fi

echo "🎉 L1-L4 自检完成"
