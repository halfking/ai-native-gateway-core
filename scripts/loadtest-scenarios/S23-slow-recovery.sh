#!/usr/bin/env bash
# S23: Slow Recovery - mock-A slow 持续 30s → 突然变 healthy
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S23: Slow Recovery (slow 30s → healthy, 验证 cooldown) ═══"
echo ""

echo "[1/4] mock-A=slow (10s)..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 slow 0

echo ""
echo "[2/4] 发 3 请求 (会很慢, 后台)..."
for i in {1..3}; do
  curl -sS -X POST http://localhost:19080/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"slow"}]}' \
    -o /dev/null 2>&1 &
done
echo "  (后台运行, 等待 30s 模拟 'slow 持续 30s')"
sleep 30

echo ""
echo "[3/4] mock-A → healthy (突然恢复)..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 healthy 0

echo ""
echo "[4/4] 发 10 请求验证快速响应..."
for i in {1..10}; do
  curl -sS -X POST http://localhost:19080/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"fast"}]}' \
    -w ' [%{http_code}]' -o /dev/null 2>&1
done
echo ""

wait  # 等待前面的慢请求

echo ""
echo "✓ S23 完成"
echo "  验证点: HealthTracker 在多少秒后重新信任 A? (cooldown 机制)"
