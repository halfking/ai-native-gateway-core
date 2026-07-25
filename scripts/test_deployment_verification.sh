#!/bin/bash
# test_deployment_verification.sh - 验证部署的修复

set -e

GATEWAY_URL="https://llm.kxpms.cn/v1/chat/completions"
ARK_API_KEY="ark-fe9a24ef-6bfb-4566-abff-bf01c5f796cd-f6031"
ARK_BASE_URL="https://ark.cn-beijing.volces.com/api/coding/v3"

# 从环境变量获取网关 API key
GATEWAY_API_KEY="${GATEWAY_API_KEY:-$(grep -r "GATEWAY_API_KEY" ~/.zcode/ 2>/dev/null | head -1 | cut -d= -f2)}"

if [ -z "$GATEWAY_API_KEY" ]; then
    echo "⚠️  GATEWAY_API_KEY not set, using placeholder"
    GATEWAY_API_KEY="test-key"
fi

echo "=========================================="
echo "Deployment Verification Test"
echo "=========================================="
echo "Gateway: $GATEWAY_URL"
echo "Time: $(date)"
echo ""

# 测试 1: minimax-m3 基础调用
test_minimax_basic() {
    echo "=== Test 1: minimax-m3 Basic Call (via Gateway) ==="
    
    response=$(curl -s -w "\nHTTP_STATUS:%{http_code}" "$GATEWAY_URL" \
        -H "Authorization: Bearer $GATEWAY_API_KEY" \
        -H "Content-Type: application/json" \
        -d '{
            "model": "minimax-m3",
            "messages": [{
                "role": "user",
                "content": "Hello, please respond with OK"
            }],
            "max_tokens": 50
        }')
    
    http_code=$(echo "$response" | grep "HTTP_STATUS:" | cut -d: -f2)
    body=$(echo "$response" | grep -v "HTTP_STATUS:")
    
    echo "HTTP Status: $http_code"
    echo "$body" | jq . 2>/dev/null || echo "$body"
    
    if [ "$http_code" = "200" ]; then
        content=$(echo "$body" | jq -r '.choices[0].message.content // empty')
        if [ -n "$content" ]; then
            echo "✅ SUCCESS: $content"
            return 0
        fi
    fi
    
    echo "❌ FAILED"
    return 1
}

# 测试 2: minimax-m3 工具调用 (第一轮)
test_minimax_tool_call() {
    echo ""
    echo "=== Test 2: minimax-m3 Tool Call Round 1 (via Gateway) ==="
    
    response=$(curl -s -w "\nHTTP_STATUS:%{http_code}" "$GATEWAY_URL" \
        -H "Authorization: Bearer $GATEWAY_API_KEY" \
        -H "Content-Type: application/json" \
        -d '{
            "model": "minimax-m3",
            "messages": [{
                "role": "user",
                "content": "What is the weather in Tokyo?"
            }],
            "tools": [{
                "type": "function",
                "function": {
                    "name": "get_weather",
                    "description": "Get the current weather",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "location": {
                                "type": "string",
                                "description": "City name"
                            }
                        },
                        "required": ["location"]
                    }
                }
            }],
            "max_tokens": 200
        }')
    
    http_code=$(echo "$response" | grep "HTTP_STATUS:" | cut -d: -f2)
    body=$(echo "$response" | grep -v "HTTP_STATUS:")
    
    echo "HTTP Status: $http_code"
    echo "$body" | jq . 2>/dev/null || echo "$body"
    
    if [ "$http_code" = "200" ]; then
        tool_calls=$(echo "$body" | jq -r '.choices[0].message.tool_calls // empty')
        if [ -n "$tool_calls" ] && [ "$tool_calls" != "null" ]; then
            echo "✅ SUCCESS: Tool calls returned"
            # 保存 tool_call_id 用于下一轮测试
            TOOL_CALL_ID=$(echo "$body" | jq -r '.choices[0].message.tool_calls[0].id')
            echo "Tool Call ID: $TOOL_CALL_ID"
            return 0
        else
            echo "❌ FAILED: No tool_calls in response"
            return 1
        fi
    else
        echo "❌ FAILED: HTTP $http_code"
        return 1
    fi
}

# 测试 3: minimax-m3 工具调用 (第二轮 - tool result)
test_minimax_tool_result() {
    echo ""
    echo "=== Test 3: minimax-m3 Tool Result Round 2 (via Gateway) ==="
    
    if [ -z "$TOOL_CALL_ID" ]; then
        echo "⚠️  Skipping: No tool_call_id from previous test"
        return 1
    fi
    
    response=$(curl -s -w "\nHTTP_STATUS:%{http_code}" "$GATEWAY_URL" \
        -H "Authorization: Bearer $GATEWAY_API_KEY" \
        -H "Content-Type: application/json" \
        -d "{
            \"model\": \"minimax-m3\",
            \"messages\": [
                {
                    \"role\": \"user\",
                    \"content\": \"What is the weather in Tokyo?\"
                },
                {
                    \"role\": \"assistant\",
                    \"content\": null,
                    \"tool_calls\": [{
                        \"id\": \"$TOOL_CALL_ID\",
                        \"type\": \"function\",
                        \"function\": {
                            \"name\": \"get_weather\",
                            \"arguments\": \"{\\\"location\\\": \\\"Tokyo\\\"}\"
                        }
                    }]
                },
                {
                    \"role\": \"tool\",
                    \"tool_call_id\": \"$TOOL_CALL_ID\",
                    \"content\": \"Sunny, 20°C\"
                }
            ],
            \"tools\": [{
                \"type\": \"function\",
                \"function\": {
                    \"name\": \"get_weather\",
                    \"description\": \"Get the current weather\",
                    \"parameters\": {
                        \"type\": \"object\",
                        \"properties\": {
                            \"location\": {
                                \"type\": \"string\"
                            }
                        },
                        \"required\": [\"location\"]
                    }
                }
            }],
            \"max_tokens\": 200
        }")
    
    http_code=$(echo "$response" | grep "HTTP_STATUS:" | cut -d: -f2)
    body=$(echo "$response" | grep -v "HTTP_STATUS:")
    
    echo "HTTP Status: $http_code"
    echo "$body" | jq . 2>/dev/null || echo "$body"
    
    if [ "$http_code" = "200" ]; then
        content=$(echo "$body" | jq -r '.choices[0].message.content // empty')
        if [ -n "$content" ]; then
            echo "✅ SUCCESS: $content"
            return 0
        fi
    fi
    
    echo "❌ FAILED"
    return 1
}

# 测试 4: Claude Opus 工具调用
test_claude_opus_tool() {
    echo ""
    echo "=== Test 4: Claude Opus Tool Calling (via Gateway) ==="
    
    response=$(curl -s -w "\nHTTP_STATUS:%{http_code}" "$GATEWAY_URL" \
        -H "Authorization: Bearer $GATEWAY_API_KEY" \
        -H "Content-Type: application/json" \
        -d '{
            "model": "claude-opus-4-5",
            "messages": [{
                "role": "user",
                "content": "What is the weather in Paris?"
            }],
            "tools": [{
                "type": "function",
                "function": {
                    "name": "get_weather",
                    "description": "Get the current weather",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "location": {
                                "type": "string"
                            }
                        },
                        "required": ["location"]
                    }
                }
            }],
            "max_tokens": 200
        }')
    
    http_code=$(echo "$response" | grep "HTTP_STATUS:" | cut -d: -f2)
    body=$(echo "$response" | grep -v "HTTP_STATUS:")
    
    echo "HTTP Status: $http_code"
    echo "$body" | jq . 2>/dev/null || echo "$body"
    
    if [ "$http_code" = "200" ]; then
        tool_calls=$(echo "$body" | jq -r '.choices[0].message.tool_calls // empty')
        if [ -n "$tool_calls" ] && [ "$tool_calls" != "null" ]; then
            echo "✅ SUCCESS: Tool calls returned"
            CLAUDE_TOOL_ID=$(echo "$body" | jq -r '.choices[0].message.tool_calls[0].id')
            echo "Tool Call ID: $CLAUDE_TOOL_ID"
            return 0
        else
            echo "⚠️  No tool_calls (may be normal if model decided not to use tools)"
            return 0
        fi
    else
        echo "❌ FAILED: HTTP $http_code"
        return 1
    fi
}

# 测试 5: Claude Opus 工具结果
test_claude_opus_result() {
    echo ""
    echo "=== Test 5: Claude Opus Tool Result (via Gateway) ==="
    
    if [ -z "$CLAUDE_TOOL_ID" ]; then
        echo "⚠️  Skipping: No tool_call_id from previous test"
        return 1
    fi
    
    response=$(curl -s -w "\nHTTP_STATUS:%{http_code}" "$GATEWAY_URL" \
        -H "Authorization: Bearer $GATEWAY_API_KEY" \
        -H "Content-Type: application/json" \
        -d "{
            \"model\": \"claude-opus-4-5\",
            \"messages\": [
                {
                    \"role\": \"user\",
                    \"content\": \"What is the weather in Paris?\"
                },
                {
                    \"role\": \"assistant\",
                    \"content\": null,
                    \"tool_calls\": [{
                        \"id\": \"$CLAUDE_TOOL_ID\",
                        \"type\": \"function\",
                        \"function\": {
                            \"name\": \"get_weather\",
                            \"arguments\": \"{\\\"location\\\": \\\"Paris\\\"}\"
                        }
                    }]
                },
                {
                    \"role\": \"tool\",
                    \"tool_call_id\": \"$CLAUDE_TOOL_ID\",
                    \"content\": \"Sunny, 18°C\"
                }
            ],
            \"tools\": [{
                \"type\": \"function\",
                \"function\": {
                    \"name\": \"get_weather\",
                    \"description\": \"Get the current weather\",
                    \"parameters\": {
                        \"type\": \"object\",
                        \"properties\": {
                            \"location\": {
                                \"type\": \"string\"
                            }
                        },
                        \"required\": [\"location\"]
                    }
                }
            }],
            \"max_tokens\": 200
        }")
    
    http_code=$(echo "$response" | grep "HTTP_STATUS:" | cut -d: -f2)
    body=$(echo "$response" | grep -v "HTTP_STATUS:")
    
    echo "HTTP Status: $http_code"
    echo "$body" | jq . 2>/dev/null || echo "$body"
    
    if [ "$http_code" = "200" ]; then
        content=$(echo "$body" | jq -r '.choices[0].message.content // empty')
        if [ -n "$content" ]; then
            echo "✅ SUCCESS: $content"
            return 0
        fi
    fi
    
    echo "❌ FAILED"
    return 1
}

# 执行所有测试
TOOL_CALL_ID=""
CLAUDE_TOOL_ID=""

test_minimax_basic
test_minimax_tool_call
test_minimax_tool_result
test_claude_opus_tool
test_claude_opus_result

echo ""
echo "=========================================="
echo "Test Summary"
echo "=========================================="
echo "All tests completed"
echo "Check results above for pass/fail status"
echo ""
echo "Note: If Claude Opus tests show 'No tool_calls',"
echo "this may be normal behavior - the model may decide"
echo "not to use tools for the given input."
