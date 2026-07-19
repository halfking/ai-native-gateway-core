#!/bin/bash
# 使用正确的模型名称测试

BASE_URL="https://ark.cn-beijing.volces.com/api/v3"
API_KEY="ark-a0e01643-6050-4fc3-a5c6-eef5c4e6ca86-dbc14"

echo "=== 测试火山方舟正确的模型名称 ==="
echo ""

# 从 API 返回的可用模型中选择测试
correct_models=(
  "doubao-seed-2-0-pro-260215"
  "doubao-seed-2-0-lite-260428"
  "doubao-seed-2-0-code-preview-260215"
  "deepseek-v4-pro-260425"
  "deepseek-v4-flash-260425"
  "glm-5-2-260617"
  "kimi-k2-thinking-251104"
)

for model in "${correct_models[@]}"; do
  echo "--- 测试模型: $model ---"
  
  response=$(curl -s -w "\nHTTP_CODE:%{http_code}" \
    -X POST "$BASE_URL/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $API_KEY" \
    -d @- <<JSON
{
  "model": "$model",
  "messages": [
    {
      "role": "user",
      "content": "你好，请简单回复"
    }
  ],
  "max_tokens": 50
}
JSON
)
  
  http_code=$(echo "$response" | grep "HTTP_CODE:" | cut -d: -f2)
  body=$(echo "$response" | sed '/HTTP_CODE:/d')
  
  echo "HTTP Status: $http_code"
  if [ "$http_code" = "200" ]; then
    echo "✅ 成功! Response:"
    echo "$body" | jq -r '.choices[0].message.content // .error.message // .' 2>/dev/null | head -c 200
  else
    echo "❌ 失败! Response:"
    echo "$body" | jq '.' 2>/dev/null || echo "$body"
  fi
  echo ""
  echo "================================"
  echo ""
  
  sleep 1
done

