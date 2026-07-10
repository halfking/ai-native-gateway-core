#!/usr/bin/env bash
# 缓存命中率诊断 - 高级场景（粘性路由验证）
# 文件: scripts/cache-baseline/run-sticky-test.sh
# 用途: 验证凭据粘性行为

set -euo pipefail

GATEWAY="${GATEWAY:-http://localhost:8781}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESULTS_DIR="${SCRIPT_DIR}/results"
mkdir -p "$RESULTS_DIR"

SYSTEM_PROMPT='You are a helpful AI assistant.'

call_with_session() {
  local session_id="$1"
  local turn="$2"
  local body=$(cat <<EOF
{
  "model": "gpt-4o",
  "messages": [
    {"role":"system","content":"$SYSTEM_PROMPT"},
    {"role":"user","content":"Tell me about turn $turn"}
  ]
}
EOF
)
  
  echo "Turn $turn (session=$session_id):"
  curl -s -i -X POST "$GATEWAY/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "X-Session-ID: $session_id" \
    -d "$body" 2>&1 | grep -iE '^(X-Gw-Prefix|X-Request-Id)' | head -3
  echo
}

echo "=== 场景 A: 同会话 5 轮连续请求 ==="
SESSION_A="sticky-test-A-$(date +%s)"
for i in 1 2 3 4 5; do
  call_with_session "$SESSION_A" "$i"
done

echo "=== 场景 B: 不同会话 3 个 ==="
for s in B1 B2 B3; do
  call_with_session "sticky-test-$s-$(date +%s)" "1"
done