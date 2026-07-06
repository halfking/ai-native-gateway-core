#!/usr/bin/env bash
# S16: Slow + Error Mix - 2 mock slow, 1 mock 50% error, 其余 healthy
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S16: Slow + Error Mix (复杂组合故障) ═══"
echo ""

echo "[1/3] Setup: mock-A=slow, mock-B=slow, mock-C=server_error, mock-D=healthy..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 slow 0
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19081 slow 0
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19082 server_error 0

echo ""
echo "[2/3] Verify..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" health-all

echo ""
echo "[3/3] Send 10 requests 分散到 4 个 mock..."
for i in {0..9}; do
  PORT=$((19080 + (i % 4)))
  curl -sS -X POST "http://localhost:$PORT/v1/chat/completions" \
    -H 'Content-Type: application/json' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"test"}]}' \
    -w " [%{http_code}]\n" -o /dev/null 2>&1 &
done
wait

echo ""
echo "✓ S16 完成"
echo "  验证点: gateway sticky 选择时应优先选 mock-D (唯一健康快速的)"
