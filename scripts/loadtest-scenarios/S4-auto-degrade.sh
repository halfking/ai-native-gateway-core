#!/usr/bin/env bash
# S4: Auto Degrade - 1 mock 50% 错误率, 验证 health tracker 自动降级
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S4: Auto Degrade (mock-A 50% error, 验证自动降级) ═══"
echo ""

echo "[1/4] Setup: mock-A=server_error (50% chance), 其余=healthy..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 server_error 0
sleep 1

echo ""
echo "[2/4] Verify states..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" health-all

echo ""
echo "[3/4] Send 20 requests to mock-A (期望 ~10 成功, ~10 失败)..."
for i in {1..20}; do
  STATUS=$(curl -sS -X POST http://localhost:19080/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"test"}]}' \
    -w '%{http_code}' -o /dev/null 2>&1)
  echo -n "$STATUS "
  [[ $((i % 10)) == 0 ]] && echo ""
done
echo ""

echo ""
echo "[4/4] Check metrics (期望 error_rate ≈ 50%)..."
curl -sS http://localhost:19080/admin/metrics | jq '{token, counters, error_rate: (.counters.requests_error / .counters.requests_total)}'

echo ""
echo "✓ S4 完成"
echo "  验证点: gateway 应在连续多次 500 后标记此 credential 为 degraded"
