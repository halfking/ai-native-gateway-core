#!/usr/bin/env bash
# S20: Cross-Session Pollution - Session-A 选 mock-X (healthy), Session-B 新建时 mock-X broken
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S20: Cross-Session Pollution (L2/L3 sticky 不应跨会话污染) ═══"
echo ""

echo "[1/4] Session-A 选 mock-A (当时 healthy)..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
echo "  模拟: Session-A 发 10 请求到 mock-A"
for i in {1..10}; do
  curl -sS -X POST http://localhost:19080/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -H 'X-Session-ID: session-A' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"A"}]}' \
    -o /dev/null 2>&1
done
echo "  Session-A 成功绑定 mock-A"

echo ""
echo "[2/4] 现在 mock-A 变 broken..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 server_error 0

echo ""
echo "[3/4] Session-B 新建 (此时 mock-A 已 broken)..."
echo "  Session-B 不应因为 L2/L3 sticky 强行选 mock-A"
STATUS=$(curl -sS -X POST http://localhost:19081/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'X-Session-ID: session-B' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"B"}]}' \
  -w '%{http_code}' -o /dev/null 2>&1)
echo "  Session-B 响应: $STATUS (应选 mock-B/C/D, 避免 A)"

echo ""
echo "[4/4] Metrics..."
for port in 19080 19081; do
  curl -sS "http://localhost:$port/admin/metrics" | jq -c '{token, counters}'
done

echo ""
echo "✓ S20 完成"
echo "  验证点: Session-B 不应继承 Session-A 的 sticky (如果 sticky credential 已 broken)"
