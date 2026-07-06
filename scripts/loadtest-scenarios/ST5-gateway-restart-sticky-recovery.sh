#!/usr/bin/env bash
# ST5: Gateway 重启, sticky cache 丢失, session 应能恢复
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ ST5: Gateway 重启 → Sticky 从 DB 恢复 (或优雅降级) ═══"
echo ""
echo "注意: 本场景需要手动重启 gateway, mock 仅提供状态"
echo ""

echo "[1/3] mock-A=healthy, 建立 sticky..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
for i in {1..10}; do
  curl -sS -X POST http://localhost:19080/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -H 'X-Session-ID: st5-session' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"pre-restart"}]}' \
    -o /dev/null 2>&1
done
echo "  Sticky 建立完成"

echo ""
echo "[2/3] ** 手动操作: 重启 gateway (模拟 sticky cache 丢失) **"
echo "  (在另一个终端执行 gateway 重启, 然后回车继续)"
read -p "  按回车继续..." 

echo ""
echo "[3/3] Gateway 重启后, 发请求 (session 应能恢复 或 优雅降级)..."
for i in {1..5}; do
  curl -sS -X POST http://localhost:19080/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -H 'X-Session-ID: st5-session' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"post-restart"}]}' \
    -w ' [%{http_code}]' -o /dev/null 2>&1
done
echo ""

echo ""
echo "✓ ST5 完成"
echo "  验证点: Gateway 重启后, session 能从 DB 恢复 sticky (如有 dbPool), 或优雅降级"
