#!/bin/bash
# tests/local/scenarios/03-high-concurrency.sh
#
# 03 - 高并发压测：
#   phase 1: baseline 50 RPS × 30s
#   phase 2: spike 200 RPS × 30s
#   phase 3: soak 50 RPS × 120s
#
# 工具优先顺序：
#   1. k6 (推荐) — 已生成 tests/k6/llm-gateway-spike.js
#   2. hey/wrk/ab — fallback（系统自带的简易压测）
#
# 用法：
#   make up                            # 起基础设施
#   make seed                          # seed 测试数据
#   KILL_SESSION_CACHE=1 make scenario-03-helper  # 带 kill-switch 的压测
#
# 输出：
#   reports/2026-07-12-loadtest/spike.json   # k6 raw metrics
#   reports/2026-07-12-loadtest/spike.log    # 人类可读摘要

set -euo pipefail

GATEWAY_URL="${GATEWAY_URL:-http://localhost:58781}"
TEST_API_KEY="${TEST_API_KEY:-sk-loc-1234567890abcdef}"
RPS="${RPS:-50}"
DURATION="${DURATION:-30s}"
MODEL="${MODEL:-minimax-m3}"
REPORT_DIR="${REPORT_DIR:-reports/$(date +%Y%m%d)-loadtest}"

mkdir -p "$REPORT_DIR"

echo "=== Scenario 03: high concurrency ==="
echo "Gateway: $GATEWAY_URL"
echo "Model:   $MODEL"
echo "RPS:     $RPS  Duration: $DURATION"
echo ""

# 选工具
TOOL=""
if command -v k6 >/dev/null 2>&1; then
  TOOL="k6"
elif command -v hey >/dev/null 2>&1; then
  TOOL="hey"
elif command -v ab >/dev/null 2>&1; then
  TOOL="ab"
elif command -v wrk >/dev/null 2>&1; then
  TOOL="wrk"
fi

echo "[1/4] Tool detection: ${TOOL:-(none — using curl loop)}"

# 0. 准备 gateway config (可临时调低/rate_limit)
echo "[2/4] Query gateway config baseline:"
curl -sS -m 3 "$GATEWAY_URL/healthz/full" 2>&1 | head -10 || true
echo ""

phase_k6() {
  local label="$1"
  local target_rps="$2"
  local dur="$3"
  local out="$REPORT_DIR/${label}.json"

  echo "─── phase: $label  RPS=$target_rps DUR=$dur ───"

  if ! command -v k6 >/dev/null 2>&1; then
    echo "  k6 not installed; install via: brew install k6"
    return 1
  fi

  MODEL="$MODEL" GATEWAY_URL="$GATEWAY_URL" API_KEY="$TEST_API_KEY" \
  k6 run \
    --out json="$out" \
    --vus $(( target_rps / 5 < 20 ? 20 : target_rps / 5 )) \
    --duration "$dur" \
    -e TARGET_RPS="$target_rps" \
    "$(dirname "$0")/../../k6/llm-gateway-spike.js" 2>&1 | tee "$REPORT_DIR/${label}.log"

  echo ""
  cat <<EOF
  📊 $label metrics: $(grep -E "http_reqs|http_req_failed|checks" "$REPORT_DIR/${label}.log" | tail -10)
EOF
}

phase_curl() {
  local label="$1"
  local target_rps="$2"
  local dur_sec="$3"
  local out="$REPORT_DIR/${label}.log"

  echo "─── phase: $label  RPS=$target_rps DUR=${dur_sec}s (curl loop) ───"
  echo "  end_ts,http_code,duration_ms" > "$out"
  local total=$((target_rps * dur_sec))
  local interval_ns=$((1000000000 / target_rps))
  local succ=0 fail=0

  for i in $(seq 1 "$total"); do
    local line
    line=$(curl -sS -o /dev/null -m 10 -w "%{http_code} %{time_total}" \
      -H "Authorization: Bearer $TEST_API_KEY" -H "Content-Type: application/json" \
      -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"$label-$i\"}],\"max_tokens\":10}" \
      "$GATEWAY_URL/v1/chat/completions" 2>&1 || echo "000 0")
    local code=$(echo "$line" | awk '{print $1}')
    local t=$(echo "$line" | awk '{print $2}')
    echo "$(date -Iseconds),$code,$t" >> "$out"
    if [ "$code" = "200" ]; then
      succ=$((succ + 1))
    else
      fail=$((fail + 1))
    fi
    # nano-second sleep — fall back to seconds on systems lacking sleep-with-ns
    if command -v gsleep >/dev/null 2>&1; then
      gsleep "0.0$((target_rps / 10000000))$(printf "%07d" $((1000000000 / target_rps % 10000000)))" 2>/dev/null || sleep 0.0$((target_rps / 100))
    else
      perl -e "select(undef,undef,undef,$interval_ns/1e9)" 2>/dev/null || true
    fi
  done

  local rate=$(echo "scale=1; 100*$succ/($succ+$fail)" | bc)
  echo "  success=$succ fail=$fail rate=$rate%"
}

case "$TOOL" in
  k6)
    phase_k6 phase1-baseline 50  30s
    phase_k6 phase2-spike    200 30s
    phase_k6 phase3-soak     50  120s
    ;;
  hey|wrk|ab|"")
    echo "  (warning) prefer k6; falling back to curl loop"
    phase_curl phase1-baseline 50  30
    phase_curl phase2-spike    200 30
    phase_curl phase3-soak     50  120
    ;;
esac

echo ""
echo "[4/4] Reports:"
ls -la "$REPORT_DIR"

# 检查错误率
echo ""
TOTAL_FAILS=$(awk -F, 'NR>1 && $2!="200"' "$REPORT_DIR"/*.log 2>/dev/null | wc -l | tr -d ' ')
TOTAL_REQS=$(awk -F, 'NR>1' "$REPORT_DIR"/*.log 2>/dev/null | wc -l | tr -d ' ')
echo ""
echo "Aggregate errors: $TOTAL_FAILS / $TOTAL_REQS"
if [ "$TOTAL_REQS" -gt 0 ]; then
  ERR_RATE=$(echo "scale=2; 100*$TOTAL_FAILS/$TOTAL_REQS" | bc)
  echo "Error rate: $ERR_RATE%"
fi
