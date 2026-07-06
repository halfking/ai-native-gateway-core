#!/usr/bin/env bash
# S0: Baseline - 4 mock 全 healthy, 验证均匀分布
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ORCHESTRATOR="$SCRIPT_DIR/../mock-state-orchestrator.sh"

MOCK_URLS=(http://localhost:19080 http://localhost:19081 http://localhost:19082 http://localhost:19083)

echo "═══ S0: Baseline (4 mock healthy, 流量均匀) ═══"
echo ""

# 1. Reset all mocks
echo "[1/4] Reset all mocks to healthy..."
bash "$ORCHESTRATOR" reset-all
sleep 1

# 2. Verify all healthy
echo ""
echo "[2/4] Verify all healthy..."
bash "$ORCHESTRATOR" health-all

# 3. Run load test (100 requests, 10 concurrent, round-robin across 4 mocks)
echo ""
echo "[3/4] Run 100 requests (10 concurrent, round-robin)..."
for i in {1..100}; do
  # Round-robin across 4 mocks (i % 4)
  IDX=$(( (i - 1) % 4 ))
  URL="${MOCK_URLS[$IDX]}"
  curl -sS -X POST "$URL/v1/chat/completions" \
    -H 'Content-Type: application/json' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"test"}]}' \
    -w '%{http_code}' -o /dev/null 2>&1 &
  
  if [[ $((i % 10)) == 0 ]]; then
    wait  # 每 10 个请求 wait 一次 (模拟 10 并发)
    echo "  $i requests sent..."
  fi
done
wait

echo ""
echo "[4/4] Check metrics (期望分布相对均匀, 每个 ≈25)..."
EXPECTED_PER_MOCK=25
PASS=true
for port in 19080 19081 19082 19083; do
  TOTAL=$(curl -sS "http://localhost:$port/admin/metrics" | jq '.counters.requests_total // 0')
  DIFF=$((TOTAL - EXPECTED_PER_MOCK))
  [[ $DIFF -lt 0 ]] && DIFF=$((-DIFF))
  STATUS="✓"
  if [[ $DIFF -gt 10 ]]; then
    STATUS="✗"
    PASS=false
  fi
  echo "  $STATUS localhost:$port → $TOTAL requests (expected ~$EXPECTED_PER_MOCK)"
done

echo ""
if $PASS; then
  echo "✓ S0 完成 — 流量分布均匀"
else
  echo "⚠ S0 完成 — 流量分布不均匀, 可能有路由偏向"
fi
