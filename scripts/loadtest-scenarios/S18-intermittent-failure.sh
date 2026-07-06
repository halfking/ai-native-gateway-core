#!/usr/bin/env bash
# S18: Intermittent Failure - mock-B 间歇性故障 (每 10s 抖动一次)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S18: Intermittent Failure (mock-B 间歇性抖动 × 3 轮) ═══"
echo ""

bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all

for round in 1 2 3; do
  echo "[Round $round] mock-B → server_error (10s)..."
  bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19081 server_error 10  # TTL=10s
  
  echo "  发 5 个请求 (期望部分失败)..."
  for i in {1..5}; do
    curl -sS -X POST http://localhost:19081/v1/chat/completions \
      -H 'Content-Type: application/json' \
      -d '{"model":"gpt-4o","messages":[{"role":"user","content":"R'$round'"}]}' \
      -w ' [%{http_code}]' -o /dev/null 2>&1
  done
  echo ""
  
  echo "  等待 12s (TTL 过期, 自动恢复 healthy)..."
  sleep 12
  
  echo "  验证已恢复:"
  curl -sS http://localhost:19081/healthz | jq -c '{token, mode}'
  echo ""
done

echo ""
echo "✓ S18 完成"
echo "  验证点: CircuitBreaker 在第 2 轮应 trip, 第 3 轮不再尝试 mock-B (或延长 cooldown)"
