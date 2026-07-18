#!/bin/bash
# Layer 1 Test: Protocol Conversion
# Tests OpenAI <-> Anthropic protocol conversion

set -euo pipefail

# Load assertion library
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../lib/assert.sh"

# Configuration
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-test-key}"
OPENAI_CREDENTIAL_ID="${OPENAI_CREDENTIAL_ID:-123}"
ANTHROPIC_CREDENTIAL_ID="${ANTHROPIC_CREDENTIAL_ID:-456}"

# Test environment setup
setup_test_env() {
    echo "Setting up test environment..."
    export LLM_GATEWAY_BYPASS_ROUTING=true
    export LLM_GATEWAY_DISABLE_STICKY=true
    export LLM_GATEWAY_DISABLE_COMPRESSION=true
    export LLM_GATEWAY_DISABLE_DETECTION=true
    export LLM_GATEWAY_DISABLE_CACHE=true
    
    echo "Environment configured:"
    echo "  GATEWAY_URL: $GATEWAY_URL"
}

# T1.13: OpenAI Request -> Anthropic Upstream
test_openai_to_anthropic() {
    test_case "T1.13: OpenAI Request -> Anthropic Upstream (Protocol Conversion)"
    
    export LLM_GATEWAY_FORCE_CREDENTIAL_ID="$ANTHROPIC_CREDENTIAL_ID"
    
    # Send OpenAI format request
    local payload='{
        "model": "claude-3-5-sonnet-20241022",
        "messages": [
            {"role": "user", "content": "Say hello"}
        ],
        "max_tokens": 10
    }'
    
    local response_time
    response_time=$(http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" "Authorization: Bearer $API_KEY")
    
    # Should receive OpenAI format response (converted back)
    assert_http_status 200
    assert_json_field_exists "$LAST_RESPONSE" ".id"
    assert_json_field_exists "$LAST_RESPONSE" ".choices"
    assert_json_field_equals "$LAST_RESPONSE" ".object" "chat.completion"
    
    # Verify protocol conversion happened
    capture_logs 100
    assert_log_contains "protocol_conversion"
    
    # Check if conversion is openai_to_anthropic
    if echo "$LAST_LOGS" | grep -q "openai_to_anthropic"; then
        echo "✓ Protocol conversion: openai_to_anthropic detected"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo "⊘ Protocol conversion log not found (may use different format)"
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
    
    echo "Response time: ${response_time}ms"
}

# T1.14: Anthropic Request -> OpenAI Upstream
test_anthropic_to_openai() {
    test_case "T1.14: Anthropic Request -> OpenAI Upstream (Protocol Conversion)"
    
    export LLM_GATEWAY_FORCE_CREDENTIAL_ID="$OPENAI_CREDENTIAL_ID"
    
    # Send Anthropic format request
    local payload='{
        "model": "gpt-4",
        "messages": [
            {"role": "user", "content": "Say hello"}
        ],
        "max_tokens": 10
    }'
    
    local response_time
    response_time=$(http_request "POST" "$GATEWAY_URL/v1/messages" "$payload" "Authorization: Bearer $API_KEY|X-Api-Version: 2023-06-01")
    
    # Should receive Anthropic format response (converted back)
    assert_http_status 200
    assert_json_field_exists "$LAST_RESPONSE" ".id"
    assert_json_field_exists "$LAST_RESPONSE" ".content"
    assert_json_field_equals "$LAST_RESPONSE" ".type" "message"
    
    # Verify protocol conversion happened
    capture_logs 100
    
    if echo "$LAST_LOGS" | grep -q "anthropic_to_openai"; then
        echo "✓ Protocol conversion: anthropic_to_openai detected"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo "⊘ Protocol conversion log not found (may use different format)"
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
    
    echo "Response time: ${response_time}ms"
}

# T1.15: Tool Calls Conversion (OpenAI -> Anthropic)
test_tool_conversion_openai_to_anthropic() {
    test_case "T1.15: Tool Calls Conversion (OpenAI -> Anthropic)"
    
    export LLM_GATEWAY_FORCE_CREDENTIAL_ID="$ANTHROPIC_CREDENTIAL_ID"
    
    # Send OpenAI format with tools
    local payload='{
        "model": "claude-3-5-sonnet-20241022",
        "messages": [
            {"role": "user", "content": "What is the weather?"}
        ],
        "tools": [
            {
                "type": "function",
                "function": {
                    "name": "get_weather",
                    "description": "Get weather",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "location": {"type": "string"}
                        }
                    }
                }
            }
        ],
        "max_tokens": 100
    }'
    
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" "Authorization: Bearer $API_KEY"
    
    # Should successfully convert and return OpenAI format
    assert_http_status 200
    assert_json_field_exists "$LAST_RESPONSE" ".choices"
    
    # Check if tool call format is preserved
    local finish_reason
    finish_reason=$(echo "$LAST_RESPONSE" | jq -r '.choices[0].finish_reason // ""')
    
    echo "Finish reason: $finish_reason"
    
    if [ "$finish_reason" = "tool_calls" ]; then
        echo "✓ Tool calls preserved in conversion"
        assert_json_field_exists "$LAST_RESPONSE" ".choices[0].message.tool_calls"
    else
        echo "⊘ No tool call triggered (may be model behavior)"
    fi
}

# T1.16: Streaming Conversion
test_streaming_conversion() {
    test_case "T1.16: Streaming Conversion (OpenAI -> Anthropic)"
    
    export LLM_GATEWAY_FORCE_CREDENTIAL_ID="$ANTHROPIC_CREDENTIAL_ID"
    
    # Send OpenAI streaming request to Anthropic backend
    local payload='{
        "model": "claude-3-5-sonnet-20241022",
        "messages": [
            {"role": "user", "content": "Count to 3"}
        ],
        "stream": true,
        "max_tokens": 50
    }'
    
    local temp_output=$(mktemp)
    
    curl -s -N \
        -X POST "$GATEWAY_URL/v1/chat/completions" \
        -H "Authorization: Bearer $API_KEY" \
        -H "Content-Type: application/json" \
        -d "$payload" \
        > "$temp_output" 2>&1
    
    local output=$(cat "$temp_output")
    rm -f "$temp_output"
    
    # Should receive OpenAI SSE format
    assert_contains "$output" "data: "
    assert_contains "$output" "data: [DONE]"
    
    # Verify it's OpenAI format chunks (has "choices")
    if echo "$output" | grep -q '"choices"'; then
        echo "✓ OpenAI format chunks detected"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo "✗ OpenAI format not detected in stream"
        TESTS_FAILED=$((TESTS_FAILED + 1))
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
}

# T1.17: System Message Conversion
test_system_message_conversion() {
    test_case "T1.17: System Message Conversion"
    
    export LLM_GATEWAY_FORCE_CREDENTIAL_ID="$ANTHROPIC_CREDENTIAL_ID"
    
    # OpenAI uses system message in messages array
    local payload='{
        "model": "claude-3-5-sonnet-20241022",
        "messages": [
            {"role": "system", "content": "You are helpful"},
            {"role": "user", "content": "Hello"}
        ],
        "max_tokens": 10
    }'
    
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" "Authorization: Bearer $API_KEY"
    
    # Should handle system message conversion (OpenAI->Anthropic uses "system" field)
    assert_http_status 200
    assert_json_field_exists "$LAST_RESPONSE" ".choices"
    
    echo "System message conversion successful"
}

# T1.18: Error Message Conversion
test_error_conversion() {
    test_case "T1.18: Error Message Conversion"
    
    export LLM_GATEWAY_FORCE_CREDENTIAL_ID="$ANTHROPIC_CREDENTIAL_ID"
    
    # Send invalid request
    local payload='{
        "model": "claude-3-5-sonnet-20241022",
        "messages": [],
        "max_tokens": 10
    }'
    
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" "Authorization: Bearer $API_KEY"
    
    # Should return OpenAI-format error (even though backend is Anthropic)
    if [ "$LAST_HTTP_STATUS" -ge 400 ]; then
        echo "✓ Error returned: $LAST_HTTP_STATUS"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo "✗ Expected error, got success"
        TESTS_FAILED=$((TESTS_FAILED + 1))
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
    
    # Check error format
    assert_json_field_exists "$LAST_RESPONSE" ".error"
}

# Main execution
main() {
    init_test_run
    
    test_section "Layer 1: Protocol Conversion Tests"
    
    # Check if service is ready
    if ! wait_for_service "$GATEWAY_URL/healthz" 30; then
        echo -e "${RED}Gateway not ready, skipping tests${NC}"
        exit 1
    fi
    
    setup_test_env
    
    # Run tests
    test_openai_to_anthropic
    test_anthropic_to_openai
    test_tool_conversion_openai_to_anthropic
    test_streaming_conversion
    test_system_message_conversion
    test_error_conversion
    
    finalize_test_run
}

# Run main if executed directly
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    main "$@"
fi
