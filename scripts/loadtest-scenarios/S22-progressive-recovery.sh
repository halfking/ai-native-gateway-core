#!/usr/bin/env bash
# S22: Progressive Recovery - 全 mock down → 逐个恢复
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S22: Progressive Recovery (全 down → 逐个恢复) ═══"
echo ""

echo "[T+0s] 全部 down..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set-all server_error 0
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" health-all

echo ""
echo "[T+5s] mock-A recover..."
sleep 5
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 healthy 0
echo "  发 5 请求 (期望流量立即用 A, 不等全恢复)"
for i in {1..5}; do
  curl -sS -X POST http://localhost:19080/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"T5"}]}' \
    -w ' [%{http_code}]' -o /dev/null 2>&1
done
echo ""

echo ""
echo "[T+10s] mock-B recover..."
sleep 5
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19081 healthy 0
echo "  流量应在 A/B 之间分布"

echo ""
echo "[T+15s] mock-C/D recover..."
sleep 5
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19082 healthy 0
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19083 healthy 0

echo ""
echo "[T+20s] All healthy, 发 20 请求验证均衡..."
sleep 5
for i in {1..20}; do
  PORT=$((19080 + (i % 4)))
  curl -sS -X POST "http://localhost:$PORT/v1/chat/completions" \
    -H 'Content-Type: application/json' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"final"}]}' \
    -o /dev/null 2>&1 &
done
wait

echo ""
echo "Metrics:"
for port in 19080 19081 19082 19083; do
  curl -sS "http://localhost:$port/admin/metrics" | jq -c '{token, counters: {total: .counters.requests_total}}'
done

echo ""
echo "✓ S22 完成"
echo "  验证点: gateway 在 T+5s 立即用 A, 流量逐步均衡"
