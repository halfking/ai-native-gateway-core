#!/usr/bin/env bash
# S3: Quota Exceeded - 模拟 tc6 silent failover
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S3: Quota Exceeded (mock-C 返回 insufficient_quota) ═══"
echo ""

echo "[1/2] Setup: mock-C=quota_exceeded..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19082 quota_exceeded 0

echo ""
echo "[2/2] Send request (期望 429 + insufficient_quota)..."
curl -sS -X POST http://localhost:19082/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"test"}]}' | jq -c '{error: .error.type}'

echo ""
echo "✓ S3 完成"
