#!/usr/bin/env bash
# S14: Cascade Failure - mock-A slow → 其他 mock 也变慢 (模拟连锁)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S14: Cascade Failure (mock-A slow → 连锁效应) ═══"
echo ""

echo "[1/4] Setup: mock-A=slow (10s)..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 slow 0

echo ""
echo "[2/4] Send 5 requests to mock-A (会很慢)..."
for i in {1..5}; do
  curl -sS -X POST http://localhost:19080/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"cascade"}]}' \
    -o /dev/null 2>&1 &
done
echo "  (后台运行, 不等待完成)"
sleep 2

echo ""
echo "[3/4] 模拟连锁: mock-B/C 也变慢 (因为 A 慢导致请求堆积)..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19081 slow 0
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19082 slow 0

echo ""
echo "[4/4] 此时只有 mock-D 是 healthy..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" health-all

wait  # 等待前面的请求完成

echo ""
echo "✓ S14 完成"
echo "  验证点: gateway 应识别级联故障, 流量集中到 mock-D"
