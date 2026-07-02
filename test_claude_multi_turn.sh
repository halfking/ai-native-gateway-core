#!/bin/bash
# 测试 claude-sonnet-4-6 多轮对话修复
# 使用方法: ./test_claude_multi_turn.sh <API_KEY>

set -e

API_KEY="${1:-test-key}"
BASE_URL="https://llmgo.kxpms.cn/v1"

echo "=========================================="
echo "测试 Claude Sonnet 4-6 多轮对话修复"
echo "=========================================="
echo ""

# 测试1: 简单的多轮对话
echo "测试 1: 简单的多轮对话"
echo "----------------------------------------"

RESPONSE1=$(curl -s -X POST "${BASE_URL}/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -d '{
    "model": "claude-sonnet-4-6",
    "messages": [
      {"role": "user", "content": "请记住这个数字：42"},
      {"role": "assistant", "content": "好的，我记住了数字 42。"},
      {"role": "user", "content": "我刚才让你记住的数字是多少？"}
    ],
    "max_tokens": 50
  }' 2>&1)

echo "响应:"
echo "$RESPONSE1" | jq -r '.choices[0].message.content // .error.message // .' 2>/dev/null || echo "$RESPONSE1"
echo ""

# 测试2: 包含tool_calls的多轮对话（模拟）
echo "测试 2: 多轮对话上下文保持"
echo "----------------------------------------"

RESPONSE2=$(curl -s -X POST "${BASE_URL}/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -d '{
    "model": "claude-sonnet-4-6",
    "messages": [
      {"role": "user", "content": "我们需要对vibe coding规范进行总结"},
      {"role": "assistant", "content": "好的，我可以帮你总结vibe coding规范。这包括代码规范、工作流程、测试策略等方面。"},
      {"role": "user", "content": "具体怎么做？"}
    ],
    "max_tokens": 150
  }' 2>&1)

echo "响应:"
echo "$RESPONSE2" | jq -r '.choices[0].message.content // .error.message // .' 2>/dev/null || echo "$RESPONSE2"
echo ""

# 检查是否返回了"我需要更多信息"这样的错误回复
if echo "$RESPONSE2" | grep -qi "需要更多信息\|need more information\|what would you like"; then
    echo "❌ 测试失败: 模型没有正确接收到上下文"
    echo "   模型回复表明它没有看到之前的对话历史"
    exit 1
else
    echo "✓ 测试通过: 模型正确理解了上下文"
fi

echo ""
echo "=========================================="
echo "测试完成"
echo "=========================================="
