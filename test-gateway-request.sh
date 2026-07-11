#!/bin/bash
# 测试 LLM Gateway 请求路由
# 用途：验证请求是否能正常路由到上游模型

set -e

# 配置
GATEWAY_URL="http://llmgo.kxpms.cn"
API_KEY="sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9"
REQUEST_ID="test-$(date +%s)"

echo "========================================="
echo "LLM Gateway 请求测试"
echo "========================================="
echo "Gateway: $GATEWAY_URL"
echo "Request ID: $REQUEST_ID"
echo ""

# 测试 1: 简单的聊天请求
echo "【测试 1】发送简单的聊天请求..."
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "$GATEWAY_URL/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -H "X-Request-Id: $REQUEST_ID" \
  -d '{
    "model": "deepseek-v3",
    "messages": [{"role": "user", "content": "你好，请用一句话介绍你自己"}],
    "max_tokens": 50
  }')

HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
BODY=$(echo "$RESPONSE" | head -n-1)

echo "HTTP状态码: $HTTP_CODE"
echo ""

if [ "$HTTP_CODE" == "200" ]; then
    echo "✅ 请求成功！"
    echo ""
    echo "响应内容:"
    echo "$BODY" | jq '.' 2>/dev/null || echo "$BODY"
    
    # 提取关键信息
    echo ""
    echo "关键信息："
    echo "  - Model: $(echo "$BODY" | jq -r '.model' 2>/dev/null || echo 'N/A')"
    echo "  - Content: $(echo "$BODY" | jq -r '.choices[0].message.content' 2>/dev/null || echo 'N/A')"
    echo "  - Tokens: $(echo "$BODY" | jq -r '.usage.total_tokens' 2>/dev/null || echo 'N/A')"
else
    echo "❌ 请求失败！"
    echo ""
    echo "错误信息:"
    echo "$BODY" | jq '.' 2>/dev/null || echo "$BODY"
fi

echo ""
echo "========================================="
echo "测试完成"
echo "========================================="
echo ""
echo "后续操作："
echo "1. 查看请求日志:"
echo "   ssh -p 25022 root@47.97.111.154 \"docker logs llm-gateway-go | grep '$REQUEST_ID'\"  # 154 替代 71"
echo ""
echo "2. 查询数据库日志:"
echo "   SELECT * FROM request_logs WHERE client_request_id = '$REQUEST_ID';"
