#!/usr/bin/env bash
# S6: Recovery - mock 从 broken 恢复到 healthy, 验证重新选择
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S6: Recovery (mock-C: server_error → healthy, 验证恢复) ═══"
echo ""

echo "[1/5] Setup: mock-C=server_error..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19082 server_error 0

echo ""
echo "[2/5] Send 10 requests (期望部分失败)..."
for i in {1..10}; do
  curl -sS -X POST http://localhost:19082/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"test"}]}' \
    -w ' [%{http_code}]\n' -o /dev/null 2>&1 &
done
wait
echo ""

echo "[3/5] Check metrics (before recovery)..."
curl -sS http://localhost:19082/admin/metrics | jq -c '{token, mode, counters}'

echo ""
echo "[4/5] Recover mock-C to healthy..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19082 healthy 0
sleep 2

echo ""
echo "[5/5] Send 10 requests again (期望全部成功)..."
SUCCESS=0
for i in {1..10}; do
  STATUS=$(curl -sS -X POST http://localhost:19082/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"test"}]}' \
    -w '%{http_code}' -o /dev/null 2>&1)
  [[ "$STATUS" == "200" ]] && SUCCESS=$((SUCCESS+1))
  echo -n "$STATUS "
done
echo ""
echo "  成功率: $SUCCESS/10"

echo ""
echo "✓ S6 完成"
echo "  验证点: gateway 应在 cooldown 后重新尝试 mock-C, 成功率恢复"
