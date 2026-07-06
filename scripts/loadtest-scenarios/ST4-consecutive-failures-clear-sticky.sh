#!/usr/bin/env bash
# ST4: 连续 5 次请求失败, sticky 是否清除?
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ ST4: 连续失败 5 次 → Sticky 清除 ═══"
echo ""

echo "[1/3] mock-A=healthy, 建立 sticky..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
curl -sS -X POST http://localhost:19080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'X-Session-ID: st4-session' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"init"}]}' | jq -c '{id}'

echo ""
echo "[2/3] mock-A → server_error, 发 5 次请求..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 server_error 0
for i in {1..5}; do
  STATUS=$(curl -sS -X POST http://localhost:19080/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -H 'X-Session-ID: st4-session' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"retry'$i'"}]}' \
    -w '%{http_code}' -o /dev/null 2>&1)
  echo "  尝试 $i: $STATUS"
done

echo ""
echo "[3/3] Metrics..."
curl -sS http://localhost:19080/admin/metrics | jq -c '{token, counters}'

echo ""
echo "✓ ST4 完成"
echo "  验证点: 连续 5 次失败后, gateway 应清除 sticky (下次走 round-robin)"
