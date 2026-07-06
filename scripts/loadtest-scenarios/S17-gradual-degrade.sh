#!/usr/bin/env bash
# S17: Gradual Degrade - mock-A 渐进式降级 (healthy → slow → error → recover)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S17: Gradual Degrade (时序故障: healthy → slow → error → recover) ═══"
echo ""

echo "[T+0s] mock-A=healthy..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
curl -sS -X POST http://localhost:19080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"T0"}]}' | jq -c '{id, time: "T0"}'
  
echo ""
echo "[T+5s] mock-A → slow (10s)..."
sleep 5
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 slow 0
echo "  (发一个请求, 会很慢, 后台运行)"
curl -sS -X POST http://localhost:19080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"T5"}]}' \
  -o /dev/null 2>&1 &

echo ""
echo "[T+10s] mock-A → server_error..."
sleep 5
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 server_error 0
STATUS=$(curl -sS -X POST http://localhost:19080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"T10"}]}' \
  -w '%{http_code}' -o /dev/null 2>&1)
echo "  T10 response: $STATUS (期望 500)"

echo ""
echo "[T+15s] mock-A → healthy (recover)..."
sleep 5
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 healthy 0
curl -sS -X POST http://localhost:19080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"T15"}]}' | jq -c '{id, time: "T15_recovered"}'

wait

echo ""
echo "✓ S17 完成"
echo "  验证点: sticky session 在 T+5s 仍选 A (慢但可用), T+10s 切到 B, T+15s 可选回 A"
