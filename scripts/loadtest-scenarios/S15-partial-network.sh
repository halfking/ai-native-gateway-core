#!/usr/bin/env bash
# S15: Partial Network Partition - mock-A/B 可达, mock-C timeout (模拟跨 AZ 抖动)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S15: Partial Network Partition (mock-C timeout, 模拟网络分区) ═══"
echo ""

echo "[1/3] Setup: mock-A/B=healthy, mock-C=timeout, mock-D=healthy..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19082 timeout 0

echo ""
echo "[2/3] Verify..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" health-all

echo ""
echo "[3/3] Send request to mock-C with 2s timeout (期望 timeout)..."
timeout 3s curl -sS http://localhost:19082/v1/chat/completions \
  -X POST -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"test"}]}' \
  --max-time 2 2>&1 || echo "  ✓ Timeout (网络分区模拟)"

echo ""
echo "✓ S15 完成"
echo "  验证点: gateway 应将 mock-C 标记为 unreachable, 流量走 A/B/D"
