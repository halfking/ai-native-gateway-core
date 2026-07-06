#!/usr/bin/env bash
# ST2: L1/L2/L3 全失效, 验证 round-robin fallback (不选已知 degraded)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ ST2: L1/L2/L3 Fail → Round-Robin (不选 degraded) ═══"
echo ""

echo "[1/2] Setup: mock-A/B=server_error (模拟 L1/L2/L3 候选都 fail), mock-C/D=healthy..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 server_error 0
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19081 server_error 0

echo ""
echo "[2/2] Verify..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" health-all

echo ""
echo "✓ ST2 完成 (mock 状态准备)"
echo "  实际验证: gateway round-robin fallback 不应选 mock-A/B (已知 degraded)"
