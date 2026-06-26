#!/bin/bash
# 测试路由修复脚本
# 使用方法：./scripts/test_routing_fix.sh

set -e

GATEWAY_URL="${GATEWAY_URL:-http://localhost:8781}"
API_KEY="${API_KEY:-sk-test-key}"

echo "=========================================="
echo "LLM Gateway 路由修复测试"
echo "=========================================="
echo "网关地址: $GATEWAY_URL"
echo ""

# 测试1: 测试 minimax-m3 模型
echo "测试1: 请求 minimax-m3 模型"
echo "-------------------------------------------"
curl -s -X POST "$GATEWAY_URL/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "minimax-m3",
    "messages": [{"role": "user", "content": "你好"}],
    "max_tokens": 10,
    "stream": false
  }' | jq '.'

echo ""
echo "测试2: 测试流式请求"
echo "-------------------------------------------"
curl -s -X POST "$GATEWAY_URL/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "minimax-m3",
    "messages": [{"role": "user", "content": "你好"}],
    "max_tokens": 10,
    "stream": true
  }' 2>&1 | head -20

echo ""
echo "测试3: 检查路由解析 API"
echo "-------------------------------------------"
curl -s "$GATEWAY_URL/api/routing/resolve?model=minimax-m3" | jq '.'

echo ""
echo "=========================================="
echo "测试完成"
echo "=========================================="
