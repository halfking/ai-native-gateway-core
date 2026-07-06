#!/usr/bin/env bash
# ST3: Sticky TTL 过期前, mock 变 broken, 下次请求应 recheck
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ ST3: Sticky TTL 过期前 mock 变 broken (应 recheck) ═══"
echo ""

echo "[1/3] mock-A=healthy, 建立 sticky (TTL 假设 60s)..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
for i in {1..5}; do
  curl -sS -X POST http://localhost:19080/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -H 'X-Session-ID: st3-session' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"init"}]}' \
    -o /dev/null 2>&1
done
echo "  Sticky 建立 (session → mock-A)"

echo ""
echo "[2/3] 10s 后, mock-A → server_error (TTL 未过期)..."
sleep 10
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 server_error 0

echo ""
echo "[3/3] 下次请求 (sticky 仍有效, 但 mock-A broken)..."
STATUS=$(curl -sS -X POST http://localhost:19080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'X-Session-ID: st3-session' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"retry"}]}' \
  -w '%{http_code}' -o /dev/null 2>&1)
echo "  响应: $STATUS"

echo ""
echo "✓ ST3 完成"
echo "  验证点: 即使 sticky TTL 未过期, 下次请求应触发 health recheck (不盲目用 sticky)"
