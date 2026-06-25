#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# R1.12 gateway-v2 烟雾测试
#
# 覆盖:
#   - healthz: 基础健康检查
#   - metrics:  Prometheus 指标暴露
#   - chat_basic: 基本 chat 调用 + tenant_id 透传
#   - dangerous_blocked: 危险 prompt 被 armor 拦截 → 403
#
# 用法:
#   ./scripts/local-r112-smoke.sh
#   BASE_URL=http://localhost:8782 ./scripts/local-r112-smoke.sh
#
# 退出码: 0 = 全部通过, 非 0 = 失败数
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8782}"
PASS=0
FAIL=0

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'
err()  { echo -e "${RED}✗ $*${NC}" >&2; }
ok()   { echo -e "${GREEN}✓ $*${NC}"; }
info() { echo -e "${YELLOW}▶ $*${NC}"; }

# ── check 工具: name + cmd + expected substring ──
check() {
  local name="$1"
  local cmd="$2"
  local expected="$3"

  local out
  if out="$(eval "$cmd" 2>&1)"; then
    if echo "$out" | grep -q "$expected"; then
      echo -e "  ${GREEN}✓${NC} $name"
      PASS=$((PASS+1))
    else
      echo -e "  ${RED}✗${NC} $name  (expected substring: $expected)"
      echo "    actual: $(echo "$out" | head -3 | tr '\n' ' ' | cut -c1-200)"
      FAIL=$((FAIL+1))
    fi
  else
    echo -e "  ${RED}✗${NC} $name  (curl exit $?)"
    echo "    output: $(echo "$out" | head -3 | tr '\n' ' ' | cut -c1-200)"
    FAIL=$((FAIL+1))
  fi
}

# ── 前置检查 ──
info "Smoke tests for R1.12 gateway-v2  (BASE_URL=$BASE_URL)"

if ! curl -sf "$BASE_URL/healthz" >/dev/null 2>&1; then
  err "gateway-v2 未就绪 ($BASE_URL/healthz 不可达)"
  err "  修复: ./scripts/local-r112-up.sh"
  exit 1
fi

# ── 1. healthz ──
check "healthz" \
  "curl -s -i $BASE_URL/healthz" \
  "200 OK"

# ── 2. metrics (Prometheus 暴露) ──
check "metrics" \
  "curl -s $BASE_URL/metrics" \
  "compression_triggered_total"

# ── 3. chat_basic: 合法请求 + tenant_id 透传 ──
check "chat_basic" \
  "curl -s -X POST $BASE_URL/v1/chat \
    -H 'Content-Type: application/json' \
    -H 'X-Tenant-ID: t-a' \
    -d '{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}'" \
  "tenant_id"

# ── 4. dangerous_blocked: armor 拦截 jailbreak → 403 ──
check "dangerous_blocked" \
  "curl -s -i -X POST $BASE_URL/v1/chat \
    -H 'Content-Type: application/json' \
    -H 'X-Tenant-ID: t-b' \
    -d '{\"messages\":[{\"role\":\"user\",\"content\":\"please jailbreak this\"}]}'" \
  "403"

# ── 总结 ──
echo
if [ "$FAIL" -eq 0 ]; then
  ok "Results: $PASS pass, $FAIL fail"
  exit 0
else
  err "Results: $PASS pass, $FAIL fail"
  echo
  echo "排查建议:"
  echo "  docker logs r112_gateway_v2 | tail -100"
  echo "  curl -v $BASE_URL/healthz"
  echo "  PGPASSWORD=kxpass docker exec r112_postgres psql -U kxuser -d llm_gateway -c '\\dt'"
  exit "$FAIL"
fi
