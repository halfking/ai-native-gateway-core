#!/bin/bash
# 直接测试火山普通版 API

BASE_URL="https://ark.cn-beijing.volces.com/api/v3"
API_KEY="ark-a0e01643-6050-4fc3-a5c6-eef5c4e6ca86-dbc14"

echo "=== 测试火山普通版直连 ==="
echo "Base URL: $BASE_URL"
echo ""

# 测试各个模型
models=(
  "doubao-seed-code"
  "doubao-seed-2.0-code"
  "doubao-seed-2.0-pro"
  "doubao-seed-2.0-lite"
  "minimax-m2.7"
  "glm-5.1"
  "kimi-k2.6"
  "deepseek-v4-pro"
  "deepseek-v4-flash"
  "minimax-m3"
)

for model in "${models[@]}"; do
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
      "content": "你好"
    }
  ]
}
JSON
)
  
  http_code=$(echo "$response" | grep "HTTP_CODE:" | cut -d: -f2)
  body=$(echo "$response" | sed '/HTTP_CODE:/d')
  
  echo "HTTP Status: $http_code"
  echo "Response: $body" | head -c 500
  echo ""
  echo "================================"
  echo ""
  
  sleep 1
done
