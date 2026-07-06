#!/usr/bin/env bash
# ST1: L1 credential 返回 500, 验证降到 L2 (L2 应是 healthy)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ ST1: L1 Fail → L2 Fallback (L2 选 healthy credential) ═══"
echo ""
echo "注意: 需 gateway 配合, mock 仅提供状态"
echo ""

echo "[1/2] Setup: mock-A (L1 candidate) = server_error, mock-B (L2 candidate) = healthy..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 server_error 0

echo ""
echo "[2/2] Verify..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" health-all

echo ""
echo "✓ ST1 完成 (mock 状态准备)"
echo "  实际验证: gateway 中 L1 选 mock-A 失败 → L2 应选 mock-B (通过最近 health check)"
