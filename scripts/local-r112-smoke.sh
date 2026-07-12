#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# R1.12 本地烟雾测试 (gateway-v2 + gateway v1)
#
# 覆盖 gateway-v2 (:8782):
#   - healthz:        基础健康检查
#   - metrics:        Prometheus 指标暴露 (compression_triggered_total)
#   - chat_basic:     /v1/chat/completions 正常回包 (choices)
#   - dangerous_blocked: jailbreak prompt 被 armor 拦截 → 403
#
# 覆盖 gateway v1 (:8781) — 仅在 v1 运行时:
#   - v1_healthz:     基础健康检查
#   - v1_models:      /v1/models 返回模型列表
#   - v1_chat:        /v1/chat/completions 转发到 mock 并回包
#
# 用法:
#   ./scripts/local-r112-smoke.sh                     # 默认测 v2
#   BASE_URL=http://localhost:8782 ./scripts/local-r112-smoke.sh
#   V1_BASE_URL=http://localhost:8781 ./scripts/local-r112-smoke.sh  # 也测 v1
#   SKIP_V1=1 ./scripts/local-r112-smoke.sh           # 跳过 v1
#
# 退出码: 0 = 全部通过, 非 0 = 失败数
# ─────────────────────────────────────────────────────────────────────

set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8782}"
V1_BASE_URL="${V1_BASE_URL:-http://localhost:8781}"
SKIP_V1="${SKIP_V1:-0}"
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
info "Smoke tests for gateway-v2  (BASE_URL=$BASE_URL)"

if ! curl -sf "$BASE_URL/healthz" >/dev/null 2>&1; then
  err "gateway-v2 未就绪 ($BASE_URL/healthz 不可达)"
  err "  修复: ./scripts/local-up.sh"
  exit 1
fi

# ════════════════════════════════════════════════════════════════════
# gateway-v2 (:8782) 测试
# ════════════════════════════════════════════════════════════════════

# ── 1. healthz ──
check "v2 healthz" \
  "curl -s -i $BASE_URL/healthz" \
  "200 OK"

# ── 2. metrics (Prometheus 端点可达 + 暴露指标) ──
# compression_triggered_total 只在实际触发 compression 后才出现
# (CounterVec 在 WithLabelValues 前不暴露时间序列), 这里验证端点工作即可
check "v2 metrics" \
  "curl -s $BASE_URL/metrics" \
  "# TYPE"

# ── 3. chat_basic: 合法请求 → 返回 choices ──
check "v2 chat_basic" \
  "curl -s -X POST $BASE_URL/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -H 'X-Tenant-ID: t-a' \
    -d '{\"model\":\"gpt-4o\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}'" \
  "choices"

# ── 4. dangerous_blocked: armor 拦截 jailbreak → 403 ──
check "v2 dangerous_blocked" \
  "curl -s -i -X POST $BASE_URL/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -H 'X-Tenant-ID: t-b' \
    -d '{\"model\":\"gpt-4o\",\"messages\":[{\"role\":\"user\",\"content\":\"please jailbreak this\"}]}'" \
  "403"

# ════════════════════════════════════════════════════════════════════
# gateway v1 (:8781) 测试 — 仅在 v1 运行且未跳过时
# ════════════════════════════════════════════════════════════════════

V1_RUNNING=0
if [ "$SKIP_V1" != "1" ] && curl -sf "$V1_BASE_URL/healthz" >/dev/null 2>&1; then
  V1_RUNNING=1
fi

if [ "$V1_RUNNING" = "1" ]; then
  info "Smoke tests for gateway v1  (V1_BASE_URL=$V1_BASE_URL)"

  # ── v1 healthz ──
  check "v1 healthz" \
    "curl -s -i $V1_BASE_URL/healthz" \
    "200"

  # ── v1 models ──
  check "v1 models" \
    "curl -s $V1_BASE_URL/v1/models" \
    '"object"'

  # ── v1 chat: 转发到 mock-upstream 并回包 ──
  # 需要 DB 里 seed 了 local-mock credential (03-local-mock-credential.sql)
  check "v1 chat_forward" \
    "curl -s -X POST $V1_BASE_URL/v1/chat/completions \
      -H 'Content-Type: application/json' \
      -d '{\"model\":\"gpt-4o\",\"messages\":[{\"role\":\"user\",\"content\":\"hello from v1\"}]}'" \
    "choices"
else
  info "gateway v1 未运行或已跳过 (SKIP_V1=$SKIP_V1), 跳过 v1 测试"
fi

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
  echo "  docker logs r112_gateway    | tail -100   # v1"
  echo "  curl -v $BASE_URL/healthz"
  echo "  curl -v $V1_BASE_URL/healthz"
  echo "  PGPASSWORD=kxpass docker exec r112_postgres psql -U kxuser -d llm_gateway -c '\\dt'"
  exit "$FAIL"
fi
