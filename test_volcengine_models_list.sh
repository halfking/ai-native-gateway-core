#!/bin/bash
# 查询火山方舟可用模型列表

BASE_URL="https://ark.cn-beijing.volces.com/api/v3"
API_KEY="ark-a0e01643-6050-4fc3-a5c6-eef5c4e6ca86-dbc14"

echo "=== 查询火山方舟可用模型列表 ==="
echo ""

# 尝试列出模型
curl -s -X GET "$BASE_URL/models" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" | jq '.' 2>/dev/null || echo "无法格式化 JSON"

echo ""
echo "=== 尝试官方文档中的标准模型名 ==="
echo ""

# 火山方舟的模型通常是 endpoint ID，不是模型名
# 尝试一些常见的格式
test_models=(
  "ep-20241230172535-vlkmk"  # 示例 endpoint
  "doubao-pro-4k"
  "doubao-lite-4k"
  "ep-*"  # endpoint 格式
)

for model in "${test_models[@]}"; do
  echo "--- 测试: $model ---"
  response=$(curl -s -w "\nHTTP_CODE:%{http_code}" \
    -X POST "$BASE_URL/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $API_KEY" \
    -d "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}")
  
  http_code=$(echo "$response" | grep "HTTP_CODE:" | cut -d: -f2)
  echo "HTTP Status: $http_code"
  echo ""
done

