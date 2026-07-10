#!/usr/bin/env bash
# 缓存命中率诊断 - 多场景测试
# 文件: scripts/cache-baseline/run-diagnostic.sh
# 用途: 在本地 r112 环境生成多场景诊断流量，采集缓存命中率与响应头数据

set -euo pipefail

GATEWAY="${GATEWAY:-http://localhost:8781}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESULTS_DIR="${SCRIPT_DIR}/results"
mkdir -p "$RESULTS_DIR"

# 长 system prompt（用于 C2 prefix 稳定化检测）
SYSTEM_PROMPT='You are an expert AI assistant with deep knowledge of computer science, distributed systems, and software engineering. Always provide detailed, accurate, and helpful responses. When discussing code, include examples. When discussing algorithms, explain complexity. When discussing systems, mention trade-offs.'

USER_TAIL_SHORT='What is the capital of France?'

run_scenario() {
  local name="$1"
  local body="$2"
  local extra_headers="${3:-}"
  
  echo "=== Scenario: $name ==="
  local out_file="$RESULTS_DIR/${name}.json"
  local headers_file="$RESULTS_DIR/${name}.headers"
  
  # 捕获响应 + headers
  curl -s -i -X POST "$GATEWAY/v1/chat/completions" \
    -H "Content-Type: application/json" \
    $extra_headers \
    -d "$body" > "$RESULTS_DIR/${name}.raw" 2>&1
  
  # 提取 X-Gw-Prefix-Stabilized
  grep -i '^X-Gw-Prefix' "$RESULTS_DIR/${name}.raw" | head -1 > "$headers_file" || true
  
  # 提取 usage 字段
  grep -oE '"usage":\{[^}]*\}' "$RESULTS_DIR/${name}.raw" > "$RESULTS_DIR/${name}.usage" || true
  
  cat "$headers_file" 2>/dev/null || echo "(no prefix header)"
  cat "$RESULTS_DIR/${name}.usage" 2>/dev/null || echo "(no usage)"
  echo
}

# ============================================================
# 场景 1: 标准顺序（system 在前，user 在后）— 应该不需要重排
# ============================================================
run_scenario "01-standard-order" "$(cat <<EOF
{
  "model": "gpt-4o",
  "messages": [
    {"role":"system","content":"$SYSTEM_PROMPT"},
    {"role":"user","content":"$USER_TAIL_SHORT"}
  ]
}
EOF
)"

# ============================================================
# 场景 2: 倒序顺序（user 在前，system 在后）— C2 应该触发重排
# ============================================================
run_scenario "02-reversed-order" "$(cat <<EOF
{
  "model": "gpt-4o",
  "messages": [
    {"role":"user","content":"$USER_TAIL_SHORT"},
    {"role":"system","content":"$SYSTEM_PROMPT"}
  ]
}
EOF
)"

# ============================================================
# 场景 3: 多轮对话（5 轮）— 验证前缀稳定性对缓存的影响
# ============================================================
run_scenario "03-multi-turn-5" "$(cat <<EOF
{
  "model": "gpt-4o",
  "messages": [
    {"role":"system","content":"$SYSTEM_PROMPT"},
    {"role":"user","content":"What is Python?"},
    {"role":"assistant","content":"Python is a high-level programming language."},
    {"role":"user","content":"What about JavaScript?"},
    {"role":"assistant","content":"JavaScript is a scripting language for web."},
    {"role":"user","content":"Which is faster?"},
    {"role":"assistant","content":"Depends on the use case."},
    {"role":"user","content":"$USER_TAIL_SHORT"}
  ]
}
EOF
)"

# ============================================================
# 场景 4: 工具调用（添加 tools 定义）— 验证 ToolClass 位置
# ============================================================
run_scenario "04-with-tools" "$(cat <<EOF
{
  "model": "gpt-4o",
  "messages": [
    {"role":"user","content":"$USER_TAIL_SHORT"},
    {"role":"system","content":"$SYSTEM_PROMPT"}
  ],
  "tools": [
    {"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}
  ]
}
EOF
)"

# ============================================================
# 场景 5: 长上下文（10K tokens）— 验证长 prompt 下的稳定性
# ============================================================
LONG_CONTEXT=$(python3 -c "
system = 'You are an expert AI assistant. ' * 200
user = 'What is the capital of France?'
import json
print(json.dumps({
  'model': 'gpt-4o',
  'messages': [
    {'role':'system','content':system},
    {'role':'user','content':user}
  ]
}))
")
run_scenario "05-long-context" "$LONG_CONTEXT"

# ============================================================
# 场景 6: 同会话重复请求（验证 cache 命中）— 同一 session_id
# ============================================================
SESSION_ID="diag-session-$(date +%s)"
run_scenario "06-session-repeat-1" "$(cat <<EOF
{
  "model": "gpt-4o",
  "messages": [
    {"role":"system","content":"$SYSTEM_PROMPT"},
    {"role":"user","content":"Tell me about caching."}
  ]
}
EOF
)" "-H X-Session-ID:$SESSION_ID"

# 同一会话第二次（理应命中粘性凭据）
run_scenario "06-session-repeat-2" "$(cat <<EOF
{
  "model": "gpt-4o",
  "messages": [
    {"role":"system","content":"$SYSTEM_PROMPT"},
    {"role":"user","content":"Tell me about caching."},
    {"role":"assistant","content":"Caching is a technique..."},
    {"role":"user","content":"$USER_TAIL_SHORT"}
  ]
}
EOF
)" "-H X-Session-ID:$SESSION_ID"

# ============================================================
# 汇总
# ============================================================
echo "=========================================="
echo "诊断完成。结果已保存到: $RESULTS_DIR"
echo "=========================================="
ls -la "$RESULTS_DIR"