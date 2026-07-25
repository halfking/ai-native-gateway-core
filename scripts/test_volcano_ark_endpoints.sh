#!/bin/bash
# test_volcano_ark_endpoints.sh - 测试火山方舟 API 和 endpoint

set -e

export ARK_API_KEY="ark-fe9a24ef-6bfb-4566-abff-bf01c5f796cd-f6031"
BASE_URL="https://ark.cn-beijing.volces.com/api/v3"

echo "=========================================="
echo "Volcano Ark Endpoint Testing"
echo "=========================================="
echo ""

# 测试函数
test_endpoint() {
    local model=$1
    local endpoint=$2
    
    echo "=== Testing: $model (endpoint: $endpoint) ==="
    
    response=$(curl -s "$BASE_URL/chat/completions" \
        -H "Authorization: Bearer $ARK_API_KEY" \
        -H "Content-Type: application/json" \
        -d "{
            \"model\": \"$endpoint\",
            \"messages\": [{
                \"role\": \"user\",
                \"content\": \"Hello, please respond with 'OK'\"
            }],
            \"max_tokens\": 50
        }")
    
    echo "$response" | jq .
    
    # 检查是否成功
    error=$(echo "$response" | jq -r '.error.message // empty')
    if [ -n "$error" ]; then
        echo "❌ ERROR: $error"
    else
        content=$(echo "$response" | jq -r '.choices[0].message.content // empty')
        if [ -n "$content" ]; then
            echo "✅ SUCCESS: $content"
        else
            echo "⚠️ No content returned"
        fi
    fi
    echo ""
}

# 测试已知的 endpoints
echo "--- Testing Known Endpoints ---"
test_endpoint "seedance-2.0" "ep-20260725234308-rhgbx"
test_endpoint "glm-5.2" "ep-20260725234534-62jcd"
test_endpoint "memora-embed-vision-251215" "ep-20260522180936-2wgrv"

echo ""
echo "--- Testing minimax-m3 with model name (expected to fail) ---"
test_endpoint "minimax-m3" "minimax-m3"

echo ""
echo "=========================================="
echo "Listing Available Models"
echo "=========================================="

# 尝试列出可用模型
models_response=$(curl -s "$BASE_URL/models" \
    -H "Authorization: Bearer $ARK_API_KEY")

echo "$models_response" | jq .

# 查找 minimax 相关模型
echo ""
echo "--- Searching for minimax models ---"
echo "$models_response" | jq '.data[] | select(.id | contains("minimax")) | {id, object, created}'

echo ""
echo "=========================================="
echo "Test Complete"
echo "=========================================="
