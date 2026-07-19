#!/bin/bash
# 测试 GLM-5.2 模型

BASE_URL="https://ark.cn-beijing.volces.com/api/v3"
API_KEY="ark-a0e01643-6050-4fc3-a5c6-eef5c4e6ca86-dbc14"

echo "=== 测试 GLM-5.2 的各种可能的模型名称 ==="
echo ""

# 从之前的模型列表中，我们看到 GLM-5.2 的可能名称
test_models=(
  "glm-5.2"
  "glm-5-2"
  "glm-5-2-260617"
  "GLM-5.2"
  "GLM-5-2-260617"
)

for model in "${test_models[@]}"; do
  echo "--- 测试模型名: $model ---"
  
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
      "content": "你好，请回复"测试成功""
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
    echo "❌ 失败! Error:"
    echo "$body" | jq '.error.message // .' 2>/dev/null | head -c 300
  fi
  echo ""
  echo "================================"
  echo ""
  
  sleep 1
done

echo ""
echo "=== 同时测试你提到的其他模型 ==="
echo ""

other_models=(
  "doubao-seed-2.1-pro"
  "doubao-seed-2-1-pro"
  "doubao-seed-2-1-pro-260628"
)

for model in "${other_models[@]}"; do
  echo "--- 测试模型名: $model ---"
  
  response=$(curl -s -w "\nHTTP_CODE:%{http_code}" \
    -X POST "$BASE_URL/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $API_KEY" \
    -d "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"max_tokens\":50}")
  
  http_code=$(echo "$response" | grep "HTTP_CODE:" | cut -d: -f2)
  body=$(echo "$response" | sed '/HTTP_CODE:/d')
  
  echo "HTTP Status: $http_code"
  if [ "$http_code" = "200" ]; then
    echo "✅ 成功!"
    echo "$body" | jq -r '.choices[0].message.content' 2>/dev/null | head -c 100
  else
    echo "❌ 失败!"
    echo "$body" | jq '.error.message' 2>/dev/null | head -c 200
  fi
  echo ""
  echo "================================"
  echo ""
  
  sleep 1
done

