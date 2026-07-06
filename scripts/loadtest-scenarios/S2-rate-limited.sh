#!/usr/bin/env bash
# S2: Rate Limited - 1 mock 返回 429, 验证降级
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S2: Rate Limited (mock-B 返回 429) ═══"
echo ""

echo "[1/3] Setup: mock-B=rate_limited..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19081 rate_limited 0

echo ""
echo "[2/3] Send request to mock-B (期望 429)..."
curl -sS -X POST http://localhost:19081/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"test"}]}' | jq -c '{error: .error.type, status: "429"}'

echo ""
echo "[3/3] Metrics..."
curl -sS http://localhost:19081/admin/metrics | jq -c '{token, counters}'

echo ""
echo "✓ S2 完成"
