#!/usr/bin/env bash
# S21: Long Session - 单 session 1000 请求, 中途 mock 切换 3 次状态
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "═══ S21: Long Session (1000 轮对话, mock 状态变化 3 次) ═══"
echo ""
echo "警告: 本场景需要 ~3min, 发送 1000 请求"
echo ""

bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" reset-all

SESSION_ID="long-session-$(date +%s)"
echo "Session ID: $SESSION_ID"

for batch in {1..10}; do
  echo ""
  echo "[Batch $batch/10] 发送 100 请求..."
  
  # 每 300 请求切换一次 mock-A 状态
  if [[ $batch == 3 ]]; then
    echo "  T+300: mock-A → slow"
    bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 slow 0
  elif [[ $batch == 6 ]]; then
    echo "  T+600: mock-A → server_error"
    bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 server_error 0
  elif [[ $batch == 9 ]]; then
    echo "  T+900: mock-A → healthy (recover)"
    bash "$SCRIPT_DIR/../mock-state-orchestrator.sh" set http://localhost:19080 healthy 0
  fi
  
  # 发 100 请求 (10 并发)
  for i in {1..100}; do
    curl -sS -X POST http://localhost:19080/v1/chat/completions \
      -H 'Content-Type: application/json' \
      -H "X-Session-ID: $SESSION_ID" \
      -d '{"model":"gpt-4o","messages":[{"role":"user","content":"msg'$((batch*100+i))'"}]}' \
      -o /dev/null 2>&1 &
    
    [[ $((i % 10)) == 0 ]] && wait  # 每 10 个 wait (模拟 10 并发)
  done
  wait
  
  echo "  Batch $batch 完成 (累计 $((batch*100)) 请求)"
done

echo ""
echo "✓ S21 完成"
echo "  验证点: session 不丢失, sticky 正确降级/恢复, 无内存泄漏"
echo ""
echo "Metrics:"
curl -sS http://localhost:19080/admin/metrics | jq -c '{token, counters}'
