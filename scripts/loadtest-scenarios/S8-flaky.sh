#!/usr/bin/env bash
# S8: Flaky - 1 mock 30% 错误率, 验证容错
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S8: Flaky (mock-D 30% error, 验证容错) ═══"
echo ""

echo "[1/3] Setup: mock-D=flaky (30% error)..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19083 flaky 0

echo ""
echo "[2/3] Send 30 requests (期望 ~70% 成功, ~30% 失败)..."
SUCCESS=0
for i in {1..30}; do
  STATUS=$(curl -sS -X POST http://localhost:19083/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"test"}]}' \
    -w '%{http_code}' -o /dev/null 2>&1)
  [[ "$STATUS" == "200" ]] && SUCCESS=$((SUCCESS+1))
  echo -n "$STATUS "
  [[ $((i % 10)) == 0 ]] && echo ""
done
echo ""
echo "  成功率: $SUCCESS/30 (期望 ~21)"

echo ""
echo "[3/3] Metrics..."
curl -sS http://localhost:19083/admin/metrics | jq '{token, mode, counters, error_rate: (.counters.requests_error / .counters.requests_total)}'

echo ""
echo "✓ S8 完成"
echo "  验证点: gateway 应容忍 flaky mock (不立即降级), 因为仍有 70% 可用"
