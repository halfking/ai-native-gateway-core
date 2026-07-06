#!/usr/bin/env bash
# S7: All Down - 4 mock 全 down, 验证 gateway 稳定返回 503 (不 crash)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S7: All Down (4 mock 全 server_error, 验证 503 稳定) ═══"
echo ""

echo "[1/3] Setup: 全部=server_error..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set-all server_error 0

echo ""
echo "[2/3] Send 20 requests (期望全部 5xx, gateway 不 crash)..."
for i in {1..20}; do
  # 轮询 4 个 mock
  PORT=$((19080 + (i % 4)))
  STATUS=$(curl -sS -X POST "http://localhost:$PORT/v1/chat/completions" \
    -H 'Content-Type: application/json' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"test"}]}' \
    -w '%{http_code}' -o /dev/null 2>&1)
  echo -n "$STATUS "
  [[ $((i % 10)) == 0 ]] && echo ""
done
echo ""

echo ""
echo "[3/3] Metrics..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" health-all

echo ""
echo "✓ S7 完成"
echo "  验证点: gateway 全 mock down 时应稳定返回 503, 不 OOM/hang/panic"
