#!/usr/bin/env bash
# S0: Baseline - 4 mock 全 healthy, 验证均匀分布
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

echo "═══ S0: Baseline (4 mock healthy, 流量均匀) ═══"
echo ""

# 1. Reset all mocks
echo "[1/4] Reset all mocks to healthy..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
sleep 1

# 2. Verify all healthy
echo ""
echo "[2/4] Verify all healthy..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" health-all

# 3. Run load test (100 requests, 10 concurrent)
echo ""
echo "[3/4] Run 100 requests (10 concurrent)..."
# 这里我们先用简单的 curl loop, Phase 1 会用 autocannon
for i in {1..100}; do
  curl -sS -X POST http://localhost:19080/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"test"}]}' \
    > /dev/null &
  
  if [[ $((i % 10)) == 0 ]]; then
    wait  # 每 10 个请求 wait 一次 (模拟 10 并发)
    echo "  $i requests sent..."
  fi
done
wait

echo ""
echo "[4/4] Check metrics (期望分布相对均匀)..."
for port in 19080 19081 19082 19083; do
  echo -n "  localhost:$port → "
  curl -sS "http://localhost:$port/admin/metrics" | jq -c '{token, counters: {total: .counters.requests_total, success: .counters.requests_success}}'
done

echo ""
echo "✓ S0 完成"
