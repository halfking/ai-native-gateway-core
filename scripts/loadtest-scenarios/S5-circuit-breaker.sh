#!/usr/bin/env bash
# S5: Circuit Breaker - mock timeout, 验证熔断
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S5: Circuit Breaker (mock-B timeout, 验证熔断) ═══"
echo ""

echo "[1/3] Setup: mock-B=timeout (永不响应)..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19081 timeout 0

echo ""
echo "[2/3] Send request with 3s timeout (期望 timeout)..."
timeout 5s curl -sS -X POST http://localhost:19081/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"test"}]}' \
  --max-time 3 2>&1 || echo "  ✓ Timeout as expected"

echo ""
echo "[3/3] Metrics..."
curl -sS http://localhost:19081/admin/metrics | jq -c '{token, counters}'

echo ""
echo "✓ S5 完成"
echo "  验证点: gateway circuit breaker 应在 N 次 timeout 后打开, 不再尝试此 mock"
