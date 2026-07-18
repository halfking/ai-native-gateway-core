#!/bin/bash
# Layer 1 Test: OpenAI Direct Connection
# Tests basic connectivity to OpenAI provider without routing

set -euo pipefail

# Load assertion library
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../lib/assert.sh"

# Configuration
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-test-key}"
FORCE_CREDENTIAL_ID="${FORCE_CREDENTIAL_ID:-123}"

# Test environment setup
setup_test_env() {
    echo "Setting up test environment..."
    export LLM_GATEWAY_BYPASS_ROUTING=true
    export LLM_GATEWAY_FORCE_CREDENTIAL_ID="$FORCE_CREDENTIAL_ID"
    export LLM_GATEWAY_DISABLE_STICKY=true
    export LLM_GATEWAY_DISABLE_COMPRESSION=true
    export LLM_GATEWAY_DISABLE_DETECTION=true
    export LLM_GATEWAY_DISABLE_CACHE=true
    
    echo "Environment configured:"
    echo "  GATEWAY_URL: $GATEWAY_URL"
    echo "  FORCE_CREDENTIAL_ID: $FORCE_CREDENTIAL_ID"
}

# T1.1: OpenAI Direct - Chat Completion (Non-streaming)
test_openai_chat_completion() {
    test_case "T1.1: OpenAI Direct - Chat Completion (Non-streaming)"
    
    local payload='{
        "model": "gpt-4",
        "messages": [
            {"role": "user", "content": "Hello, respond with just OK"}
        ],
        "stream": false,
        "max_tokens": 10
    }'
    
    local headers="Authorization: Bearer $API_KEY"
    
    local response_time
    response_time=$(http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" "$headers")
    
    # Assertions
    assert_http_status 200
    assert_json_field_exists "$LAST_RESPONSE" ".id"
    assert_json_field_exists "$LAST_RESPONSE" ".choices"
    assert_json_field_exists "$LAST_RESPONSE" ".usage"
    assert_json_field_equals "$LAST_RESPONSE" ".object" "chat.completion"
    assert_response_time_under 3000 "$response_time"
    
    # Verify credential was used
    capture_logs 50
    assert_log_contains "credential_id=$FORCE_CREDENTIAL_ID"
    
    echo "Response time: ${response_time}ms"
}

# T1.2: OpenAI Direct - Streaming Response
test_openai_streaming() {
    test_case "T1.2: OpenAI Direct - Streaming Response"
    
    local payload='{
        "model": "gpt-4",
        "messages": [
            {"role": "user", "content": "Count from 1 to 3"}
        ],
        "stream": true
    }'
    
    local temp_output=$(mktemp)
    
    # Make streaming request
    curl -s -N \
        -X POST "$GATEWAY_URL/v1/chat/completions" \
        -H "Authorization: Bearer $API_KEY" \
        -H "Content-Type: application/json" \
        -d "$payload" \
        > "$temp_output" 2>&1
    
    local output=$(cat "$temp_output")
    rm -f "$temp_output"
    
    # Assertions
    assert_contains "$output" "data: "
    assert_contains "$output" "data: [DONE]"
    
    # Count data chunks (should be multiple)
    local chunk_count
    chunk_count=$(echo "$output" | grep -c "^data: " || echo "0")
    assert_gt 0 "$chunk_count" "chunk count"
    
    echo "Received $chunk_count chunks"
}

# T1.3: OpenAI Direct - Error Handling (Invalid Model)
test_openai_invalid_model() {
    test_case "T1.3: OpenAI Direct - Error Handling (Invalid Model)"
    
    local payload='{
        "model": "nonexistent-model-xyz",
        "messages": [
            {"role": "user", "content": "Hello"}
        ]
    }'
    
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" "Authorization: Bearer $API_KEY"
    
    # Should return 4xx error
    if [ "$LAST_HTTP_STATUS" -ge 400 ] && [ "$LAST_HTTP_STATUS" -lt 500 ]; then
        echo -e "${GREEN}✓${NC} Correctly returned 4xx error: $LAST_HTTP_STATUS"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo -e "${RED}✗${NC} Expected 4xx, got $LAST_HTTP_STATUS"
        TESTS_FAILED=$((TESTS_FAILED + 1))
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
    
    # Error response should have error field
    assert_json_field_exists "$LAST_RESPONSE" ".error"
}

# T1.4: OpenAI Direct - Tool Calls
test_openai_tool_calls() {
    test_case "T1.4: OpenAI Direct - Tool Calls"
    
    local payload='{
        "model": "gpt-4",
        "messages": [
            {"role": "user", "content": "What is the weather in San Francisco?"}
        ],
        "tools": [
            {
                "type": "function",
                "function": {
                    "name": "get_weather",
                    "description": "Get current weather",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "location": {"type": "string"}
                        }
                    }
                }
            }
        ]
    }'
    
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" "Authorization: Bearer $API_KEY"
    
    # Check if response is valid (tool call may or may not be triggered)
    assert_http_status 200
    assert_json_field_exists "$LAST_RESPONSE" ".choices"
    
    # If tool call was made, verify structure
    local finish_reason
    finish_reason=$(echo "$LAST_RESPONSE" | jq -r '.choices[0].finish_reason')
    
    if [ "$finish_reason" = "tool_calls" ]; then
        echo "Tool call triggered"
        assert_json_field_exists "$LAST_RESPONSE" ".choices[0].message.tool_calls"
    else
        echo "Tool call not triggered (finish_reason: $finish_reason)"
    fi
}

# T1.5: OpenAI Direct - Large Context
test_openai_large_context() {
    test_case "T1.5: OpenAI Direct - Large Context"
    
    # Generate a large context message (~4K tokens)
    local large_content
    large_content=$(python3 -c "print('This is a test sentence. ' * 500)")
    
    local payload
    payload=$(jq -n \
        --arg content "$large_content" \
        '{
            "model": "gpt-4",
            "messages": [
                {"role": "user", "content": $content}
            ],
            "max_tokens": 50
        }')
    
    local response_time
    response_time=$(http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" "Authorization: Bearer $API_KEY")
    
    # Should handle large context without error
    assert_http_status 200
    assert_json_field_exists "$LAST_RESPONSE" ".usage.prompt_tokens"
    
    local prompt_tokens
    prompt_tokens=$(echo "$LAST_RESPONSE" | jq -r '.usage.prompt_tokens')
    assert_gt 1000 "$prompt_tokens" "prompt tokens"
    
    echo "Response time: ${response_time}ms"
    echo "Prompt tokens: $prompt_tokens"
}

# T1.6: OpenAI Direct - Concurrent Requests
test_openai_concurrent() {
    test_case "T1.6: OpenAI Direct - Concurrent Requests"
    
    local concurrent=10
    local temp_dir=$(mktemp -d)
    
    echo "Sending $concurrent concurrent requests..."
    
    for i in $(seq 1 $concurrent); do
        {
            local payload='{
                "model": "gpt-4",
                "messages": [{"role": "user", "content": "Say hello"}],
                "max_tokens": 5
            }'
            
            curl -s -w "\n%{http_code}" \
                -X POST "$GATEWAY_URL/v1/chat/completions" \
                -H "Authorization: Bearer $API_KEY" \
                -H "Content-Type: application/json" \
                -d "$payload" \
                > "$temp_dir/response_$i.txt"
        } &
    done
    
    # Wait for all requests
    wait
    
    # Count successful responses
    local success_count=0
    for i in $(seq 1 $concurrent); do
        local status
        status=$(tail -1 "$temp_dir/response_$i.txt")
        if [ "$status" = "200" ]; then
            success_count=$((success_count + 1))
        fi
    done
    
    rm -rf "$temp_dir"
    
    # Should have high success rate
    local success_rate=$((success_count * 100 / concurrent))
    echo "Success rate: $success_rate% ($success_count/$concurrent)"
    
    if [ $success_rate -ge 95 ]; then
        echo -e "${GREEN}✓${NC} Concurrent test passed: $success_rate%"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo -e "${RED}✗${NC} Concurrent test failed: $success_rate% < 95%"
        TESTS_FAILED=$((TESTS_FAILED + 1))
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
}

# Main execution
main() {
    init_test_run
    
    test_section "Layer 1: OpenAI Direct Connection Tests"
    
    # Check if service is ready
    if ! wait_for_service "$GATEWAY_URL/healthz" 30; then
        echo -e "${RED}Gateway not ready, skipping tests${NC}"
        exit 1
    fi
    
    setup_test_env
    
    # Run tests
    test_openai_chat_completion
    test_openai_streaming
    test_openai_invalid_model
    test_openai_tool_calls
    test_openai_large_context
    test_openai_concurrent
    
    finalize_test_run
}

# Run main if executed directly
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    main "$@"
fi
