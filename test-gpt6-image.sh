#!/bin/bash
# 测试 gpt-6-astra 是否支持图片输入

# 使用一个小的测试图片（1x1 透明 PNG 的 base64）
TEST_IMAGE="iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

# 获取 API key（从环境变量或配置文件）
API_KEY="${LLM_GATEWAY_API_KEY:-sk-test}"
GATEWAY_URL="${LLM_GATEWAY_URL:-https://llm.itestu.cn}"

echo "测试 gpt-6-astra 图片输入支持..."
echo "Gateway: $GATEWAY_URL"
echo ""

# 发送包含图片的请求
response=$(curl -s -w "\n%{http_code}" -X POST "$GATEWAY_URL/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "gpt-6-astra",
    "messages": [
      {
        "role": "user",
        "content": [
          {
            "type": "text",
            "text": "这是什么图片？"
          },
          {
            "type": "image_url",
            "image_url": {
              "url": "data:image/png;base64,'"$TEST_IMAGE"'"
            }
          }
        ]
      }
    ],
    "max_tokens": 100
  }')

# 分离响应体和状态码
http_code=$(echo "$response" | tail -n 1)
response_body=$(echo "$response" | head -n -1)

echo "HTTP 状态码: $http_code"
echo ""

if [ "$http_code" = "200" ]; then
    echo "✅ 成功！gpt-6-astra 现在支持图片输入"
    echo ""
    echo "响应内容:"
    echo "$response_body" | jq '.'
elif echo "$response_body" | grep -q "modality_mismatch"; then
    echo "❌ 失败：仍然报告 modality_mismatch 错误"
    echo ""
    echo "错误详情:"
    echo "$response_body" | jq '.'
    exit 1
else
    echo "⚠️  其他错误 (HTTP $http_code)"
    echo ""
    echo "响应内容:"
    echo "$response_body" | jq '.'
    exit 1
fi
