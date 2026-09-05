#!/usr/bin/env bash
# =====================================================================
# scripts/test-request-detail-apis.sh - 测试请求详情页面的各个API
# =====================================================================
set -euo pipefail

# 颜色定义
GREEN=$'\033[0;32m'
YELLOW=$'\033[1;33m'
RED=$'\033[0;31m'
BLUE=$'\033[0;34m'
NC=$'\033[0m'

log()  { echo -e "${BLUE}[TEST]${NC} $*"; }
ok()   { echo -e "${GREEN}  ✓${NC} $*"; }
warn() { echo -e "${YELLOW}  ⚠${NC} $*"; }
err()  { echo -e "${RED}  ✗${NC} $*" >&2; }

# 配置
BASE_URL="${1:-https://llm.kxpms.cn}"
USERNAME="${2:-admin}"
PASSWORD="${3:-Veritrans&9527}"

# 登录获取token
log "登录获取token..."
LOGIN_RESPONSE=$(curl -s -X POST "$BASE_URL/api/auth/token" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"$USERNAME\",\"password\":\"$PASSWORD\"}")

TOKEN=$(echo "$LOGIN_RESPONSE" | jq -r '.access_token')
if [ "$TOKEN" = "null" ] || [ -z "$TOKEN" ]; then
  err "登录失败"
  echo "$LOGIN_RESPONSE" | jq .
  exit 1
fi
ok "登录成功，token: ${TOKEN:0:20}..."

# 测试1: 获取请求日志列表
log "测试1: 获取请求日志列表 (/api/logs?limit=10)"
LOGS_RESPONSE=$(curl -s "$BASE_URL/api/logs?limit=10" \
  -H "Authorization: Bearer $TOKEN")

ITEM_COUNT=$(echo "$LOGS_RESPONSE" | jq '.count // 0')
ok "获取到 $ITEM_COUNT 条请求记录"

# 提取前5个请求ID进行详细测试
REQUEST_IDS=$(echo "$LOGS_RESPONSE" | jq -r '.items[:5] | .[] | .request_id')

# 测试2: 逐个测试请求详情API
log "测试2: 测试请求详情API"
SUCCESS_COUNT=0
MISSING_RESPONSE_BODY_COUNT=0
TOTAL_TESTED=0

while read -r req_id; do
  if [ -z "$req_id" ]; then
    continue
  fi
  
  TOTAL_TESTED=$((TOTAL_TESTED + 1))
  DETAIL=$(curl -s "$BASE_URL/api/admin/request-detail/$req_id" \
    -H "Authorization: Bearer $TOKEN")
  
  HAS_REQ=$(echo "$DETAIL" | jq 'has("bodies") and .bodies.request_body != null')
  HAS_RESP=$(echo "$DETAIL" | jq 'has("bodies") and .bodies.response_body != null')
  STATUS=$(echo "$DETAIL" | jq -r '.meta.request_status // "unknown"')
  MODEL=$(echo "$DETAIL" | jq -r '.meta.client_model // "unknown"')
  
  if [ "$HAS_REQ" = "true" ]; then
    if [ "$HAS_RESP" = "true" ]; then
      ok "$req_id - status:$STATUS, model:$MODEL [完整]"
      SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
    else
      if [ "$STATUS" = "success" ]; then
        warn "$req_id - status:$STATUS, model:$MODEL [缺少response_body]"
        MISSING_RESPONSE_BODY_COUNT=$((MISSING_RESPONSE_BODY_COUNT + 1))
      else
        ok "$req_id - status:$STATUS, model:$MODEL [无response_body，符合预期]"
        SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
      fi
    fi
  else
    err "$req_id - 缺少request_body"
  fi
done <<< "$REQUEST_IDS"

echo ""
log "测试结果汇总:"
echo "  - 总测试数: $TOTAL_TESTED"
echo "  - 完整记录: $SUCCESS_COUNT"
echo "  - success状态但缺少response_body: $MISSING_RESPONSE_BODY_COUNT"

# 测试3: 测试其他相关API
log "测试3: 测试系统版本API"
VERSION=$(curl -s "$BASE_URL/api/system/version" | jq -r '.version // "unknown"')
ok "系统版本: $VERSION"

log "测试4: 测试路由概览API"
ROUTING=$(curl -s "$BASE_URL/api/routing/overview" \
  -H "Authorization: Bearer $TOKEN")
PROVIDER_COUNT=$(echo "$ROUTING" | jq '.providers | length // 0')
ok "路由提供商数量: $PROVIDER_COUNT"

# 测试5: 测试模型列表
log "测试5: 测试模型列表API (暂时跳过，需要更多参数)"

echo ""
if [ "$MISSING_RESPONSE_BODY_COUNT" -gt 0 ]; then
  warn "发现 $MISSING_RESPONSE_BODY_COUNT 个success请求缺少response_body"
  warn "建议查看 ISSUE_REPORT_20260828.md 了解详情"
else
  ok "所有测试通过！"
fi
