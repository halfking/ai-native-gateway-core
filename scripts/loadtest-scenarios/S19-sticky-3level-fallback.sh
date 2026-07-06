#!/usr/bin/env bash
# S19: Sticky 3-Level Fallback - L1 挂 → L2, L2 挂 → L3
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S19: Sticky 3-Level Fallback (L1 → L2 → L3 降级路径) ═══"
echo ""
echo "注意: 本场景需要 gateway 配合测试 (mock 无法模拟 sticky 逻辑)"
echo "      这里仅验证 mock 状态设置正确性"
echo ""

echo "[1/3] Setup: 模拟 L1 credential (mock-A) 挂了..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 server_error 0

echo ""
echo "[2/3] 模拟 L2 credential (mock-B) 也挂了..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19081 server_error 0

echo ""
echo "[3/3] L3 fallback 应选 mock-C/D (healthy)..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" health-all

echo ""
echo "✓ S19 完成 (mock 状态准备)"
echo "  实际验证: 需在 gateway 中跑完整流程, 检查 sticky cache 降级行为"
