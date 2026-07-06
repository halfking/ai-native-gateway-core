#!/usr/bin/env bash
# ST6: 并发 100 请求同一 session, credential 中途挂, 验证切换
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ ST6: 并发 100 请求 + Credential 中途挂 ═══"
echo ""

echo "[1/4] mock-A=healthy, 先发 10 请求建立 sticky..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all
for i in {1..10}; do
  curl -sS -X POST http://localhost:19080/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -H 'X-Session-ID: st6-concurrent' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"init"}]}' \
    -o /dev/null 2>&1
done
echo "  Sticky 建立 (session → mock-A)"

echo ""
echo "[2/4] 并发发 100 请求 (20 并发)..."
for i in {1..100}; do
  curl -sS -X POST http://localhost:19080/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -H 'X-Session-ID: st6-concurrent' \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"batch'$i'"}]}' \
    -o /dev/null 2>&1 &
  
  # 在第 50 个请求时, mock-A 挂掉
  if [[ $i == 50 ]]; then
    echo "  [中途] mock-A → server_error"
    bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 server_error 0 &
  fi
  
  [[ $((i % 20)) == 0 ]] && wait  # 每 20 个 wait
done
wait

echo ""
echo "[3/4] Check metrics..."
curl -sS http://localhost:19080/admin/metrics | jq -c '{token, counters, error_rate: (.counters.requests_error / .counters.requests_total)}'

echo ""
echo "[4/4] Reset for next test..."
bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all

echo ""
echo "✓ ST6 完成"
echo "  验证点: 应只有少数请求失败 (故障检测窗口), 其余切到备用 mock"
