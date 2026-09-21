#!/usr/bin/env bash
# =====================================================================
# scripts/test-request-detail-apis.sh - 测试请求详情页面的各个 API
# =====================================================================
set -euo pipefail

GREEN=$'\033[0;32m'
YELLOW=$'\033[1;33m'
RED=$'\033[0;31m'
BLUE=$'\033[0;34m'
NC=$'\033[0m'

log()  { echo -e "${BLUE}[TEST]${NC} $*"; }
ok()   { echo -e "${GREEN}  ✓${NC} $*"; }
warn() { echo -e "${YELLOW}  ⚠${NC} $*"; }
err()  { echo -e "${RED}  ✗${NC} $*" >&2; }

usage() {
  cat <<'EOF'
用法:
  TOKEN=<admin-token> ./scripts/test-request-detail-apis.sh [BASE_URL]
  USERNAME=<admin-user> PASSWORD=<admin-password> ./scripts/test-request-detail-apis.sh [BASE_URL]

默认仅允许 localhost 目标。访问远程目标必须同时设置
LLM_GATEWAY_REQUEST_DETAIL_ALLOW_REMOTE=1。

环境变量:
  BASE_URL                                    默认 http://127.0.0.1:8781
  TOKEN                                       已有管理员 token；设置后跳过登录
  USERNAME, PASSWORD                          未设置 TOKEN 时用于登录
  LLM_GATEWAY_REQUEST_DETAIL_ALLOW_REMOTE=1  明确允许非 localhost 目标
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi
if [[ $# -gt 1 ]]; then
  usage >&2
  exit 2
fi

BASE_URL="${1:-${BASE_URL:-http://127.0.0.1:8781}}"
BASE_URL="${BASE_URL%/}"
case "$BASE_URL" in
  http://127.0.0.1:*|http://localhost:*|https://127.0.0.1:*|https://localhost:*) ;;
  *)
    [[ "${LLM_GATEWAY_REQUEST_DETAIL_ALLOW_REMOTE:-0}" == "1" ]] || {
      err "拒绝非 localhost 目标；如确有授权，设置 LLM_GATEWAY_REQUEST_DETAIL_ALLOW_REMOTE=1"
      exit 2
    }
    ;;
esac

CURL=(curl --fail --silent --show-error --connect-timeout 5 --max-time 30)
request() {
  "${CURL[@]}" "$@"
}

TOKEN="${TOKEN:-}"
if [[ -z "$TOKEN" ]]; then
  : "${USERNAME:?set USERNAME or TOKEN}"
  : "${PASSWORD:?set PASSWORD or TOKEN}"

  log "登录获取 token..."
  login_payload=$(jq -n --arg username "$USERNAME" --arg password "$PASSWORD" \
    '{username: $username, password: $password}')
  if ! LOGIN_RESPONSE=$(request -X POST "$BASE_URL/api/auth/token" \
    -H "Content-Type: application/json" \
    --data "$login_payload"); then
    err "登录请求失败"
    exit 1
  fi

  TOKEN=$(jq -r '.access_token // empty' <<<"$LOGIN_RESPONSE")
  if [[ -z "$TOKEN" ]]; then
    err "登录失败：未返回 access_token"
    exit 1
  fi
  ok "登录成功"
else
  ok "使用调用方提供的 token"
fi

AUTH_HEADER="Authorization: Bearer $TOKEN"

log "测试 1: 获取请求日志列表 (/api/logs?limit=10)"
if ! LOGS_RESPONSE=$(request "$BASE_URL/api/logs?limit=10" -H "$AUTH_HEADER"); then
  err "请求日志列表 API 调用失败"
  exit 1
fi
ITEM_COUNT=$(jq -r '.count // 0' <<<"$LOGS_RESPONSE")
ok "获取到 $ITEM_COUNT 条请求记录"

log "测试 2: 测试请求详情 API"
SUCCESS_COUNT=0
FAILED_COUNT=0
MISSING_RESPONSE_BODY_COUNT=0
TOTAL_TESTED=0
REQUEST_IDS=$(jq -r '.items[:5] | .[] | .request_id // empty' <<<"$LOGS_RESPONSE")

while IFS= read -r req_id; do
  [[ -n "$req_id" ]] || continue
  TOTAL_TESTED=$((TOTAL_TESTED + 1))

  if ! DETAIL=$(request "$BASE_URL/api/admin/request-detail/$req_id" -H "$AUTH_HEADER"); then
    err "$req_id - 请求详情 API 调用失败"
    FAILED_COUNT=$((FAILED_COUNT + 1))
    continue
  fi

  HAS_REQ=$(jq 'has("bodies") and .bodies.request_body != null' <<<"$DETAIL")
  HAS_RESP=$(jq 'has("bodies") and .bodies.response_body != null' <<<"$DETAIL")
  STATUS=$(jq -r '.meta.request_status // "unknown"' <<<"$DETAIL")
  MODEL=$(jq -r '.meta.client_model // "unknown"' <<<"$DETAIL")

  if [[ "$HAS_REQ" == "true" ]]; then
    if [[ "$HAS_RESP" == "true" ]]; then
      ok "$req_id - status:$STATUS, model:$MODEL [完整]"
      SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
    elif [[ "$STATUS" == "success" ]]; then
      err "$req_id - status:$STATUS, model:$MODEL [缺少 response_body]"
      MISSING_RESPONSE_BODY_COUNT=$((MISSING_RESPONSE_BODY_COUNT + 1))
      FAILED_COUNT=$((FAILED_COUNT + 1))
    else
      ok "$req_id - status:$STATUS, model:$MODEL [无 response_body，符合预期]"
      SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
    fi
  else
    err "$req_id - 缺少 request_body"
    FAILED_COUNT=$((FAILED_COUNT + 1))
  fi
done <<<"$REQUEST_IDS"

echo
log "测试结果汇总:"
echo "  - 总测试数: $TOTAL_TESTED"
echo "  - 完整记录: $SUCCESS_COUNT"
echo "  - 详情 API/请求体失败: $FAILED_COUNT"
echo "  - success 状态但缺少 response_body: $MISSING_RESPONSE_BODY_COUNT"

if [[ "$TOTAL_TESTED" -eq 0 ]]; then
  warn "没有可供详情验证的请求记录，测试跳过"
elif [[ "$FAILED_COUNT" -gt 0 ]]; then
  err "请求详情验证失败: $FAILED_COUNT 项"
  exit 1
fi

log "测试 3: 测试系统版本 API"
if ! VERSION_RESPONSE=$(request "$BASE_URL/api/system/version"); then
  err "系统版本 API 调用失败"
  exit 1
fi
VERSION=$(jq -r '.version // "unknown"' <<<"$VERSION_RESPONSE")
ok "系统版本: $VERSION"

log "测试 4: 测试路由概览 API"
if ! ROUTING=$(request "$BASE_URL/api/routing/overview" -H "$AUTH_HEADER"); then
  err "路由概览 API 调用失败"
  exit 1
fi
PROVIDER_COUNT=$(jq '.providers | length // 0' <<<"$ROUTING")
ok "路由提供商数量: $PROVIDER_COUNT"

log "测试 5: 测试模型列表 API (暂时跳过，需要更多参数)"
echo
if [[ "$TOTAL_TESTED" -eq 0 ]]; then
  warn "无请求记录，详情部分已跳过；其余 API 检查完成"
else
  ok "请求详情验证通过"
fi
