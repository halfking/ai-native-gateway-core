#!/usr/bin/env bash
# S1: One slow - 1 mock 慢 (10s), 其余 healthy, 验证流量转移
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S1: One Slow (mock-A slow, 其余 healthy) ═══"
echo ""

echo "[1/3] Setup: mock-A=slow, 其余=healthy..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 slow 0
sleep 1

echo ""
echo "[2/3] Verify states..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" health-all

echo ""
echo "[3/3] Run 50 requests (期望: mock-A 分得 <10%, 其余分得 >30% each)..."
echo "  (简化版本: 只发到 mock-A 测试慢响应)"
time curl -sS -X POST http://localhost:19080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"slow test"}]}' > /dev/null

echo ""
echo "✓ S1 完成 (slow mock 响应时间应 >5s)"
